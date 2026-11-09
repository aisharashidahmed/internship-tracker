package linkcheck

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"internship-tracker/internal/store"
)

// Agent identifies us to the sites we check, with a link explaining who
// we are, so an operator who sees us in their logs knows what this is.
const Agent = "internship-tracker-linkcheck"

// Store is what the checker needs; *store.Store satisfies it.
type Store interface {
	DueLinks(ctx context.Context, limit int) ([]store.DueLink, error)
	RecordLinkCheck(ctx context.Context, postingID int64, result store.LinkResult, detail string) (bool, error)
}

// Checker checks apply links politely:
//   - one request at a time per website, at least PerHost apart;
//   - robots.txt is read (and cached for a day) and obeyed;
//   - redirects are followed only on the same website, at most 3;
//   - only the first few kilobytes of each page are read.
type Checker struct {
	Store     Store
	Client    *http.Client // must not follow redirects itself; see NewClient
	UserAgent string
	PerHost   time.Duration
	Sites     int // how many different websites to check at once

	mu     sync.Mutex
	robots map[string]robotsEntry
}

type robotsEntry struct {
	rules   *Robots
	expires time.Time // a day for a file we read; an hour if we couldn't read it
}

// NewClient returns an HTTP client that reports redirects instead of
// following them, so Classify can see where a job link points.
func NewClient() *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Summary counts one run's results.
type Summary struct {
	Checked, Alive, Dead, Unknown, Blocked, ClosedListings int
	Took                                                   time.Duration
}

func (s Summary) String() string {
	return fmt.Sprintf("link checks: %d checked: %d alive, %d dead, %d couldn't tell (%d blocked by robots.txt); %d listing(s) closed; in %s",
		s.Checked, s.Alive, s.Dead, s.Unknown, s.Blocked, s.ClosedListings, s.Took.Round(time.Second))
}

// Run checks up to limit due links and records each result.
func (c *Checker) Run(ctx context.Context, limit int) (Summary, error) {
	start := time.Now()
	due, err := c.Store.DueLinks(ctx, limit)
	if err != nil {
		return Summary{}, err
	}

	// Group by website: each website gets one worker, which paces itself.
	byHost := map[string][]store.DueLink{}
	for _, d := range due {
		u, err := url.Parse(d.URL)
		if err != nil || u.Host == "" {
			continue
		}
		byHost[strings.ToLower(u.Host)] = append(byHost[strings.ToLower(u.Host)], d)
	}

	var (
		mu  sync.Mutex
		sum Summary
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(1, c.Sites))
	)
	for host, links := range byHost {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s := c.checkSite(ctx, host, links)
			mu.Lock()
			sum.Checked += s.Checked
			sum.Alive += s.Alive
			sum.Dead += s.Dead
			sum.Unknown += s.Unknown
			sum.Blocked += s.Blocked
			sum.ClosedListings += s.ClosedListings
			mu.Unlock()
		}()
	}
	wg.Wait()
	sum.Took = time.Since(start)
	return sum, ctx.Err()
}

// checkSite checks one website's links, one at a time, PerHost apart.
func (c *Checker) checkSite(ctx context.Context, host string, links []store.DueLink) Summary {
	var sum Summary
	pace := newPacer(c.PerHost)
	for _, link := range links {
		if ctx.Err() != nil {
			return sum
		}
		verdict, blocked := c.checkLink(ctx, link.URL, pace)
		if ctx.Err() != nil {
			return sum // shutting down: don't record a half-done check
		}
		closed, err := c.Store.RecordLinkCheck(ctx, link.PostingID, verdict.Result, verdict.Detail)
		if err != nil {
			log.Printf("link check: save result for posting %d: %v", link.PostingID, err)
			continue // it stays due, so the next run tries again
		}
		sum.Checked++
		switch verdict.Result {
		case store.LinkAlive:
			sum.Alive++
		case store.LinkDead:
			sum.Dead++
		default:
			sum.Unknown++
		}
		if blocked {
			sum.Blocked++
		}
		if closed {
			sum.ClosedListings++
		}
	}
	return sum
}

// checkLink fetches one link (following up to 3 same-site redirects) and
// classifies it. blocked is true if robots.txt said no.
func (c *Checker) checkLink(ctx context.Context, raw string, pace *pacer) (Verdict, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return Verdict{Result: store.LinkUnknown, Detail: "unreadable URL"}, false
	}
	for hop := 0; hop <= 3; hop++ {
		if !c.robotsFor(ctx, u, pace).Allowed(u.RequestURI()) {
			return Verdict{Result: store.LinkUnknown, Detail: "robots.txt disallows"}, true
		}
		status, location, err := c.fetch(ctx, u, pace)
		if err != nil {
			return Verdict{Result: store.LinkUnknown, Detail: "no response: " + shortError(err)}, false
		}
		v := Classify(u, status, location)
		if v.Next == nil {
			return v, false
		}
		if !sameSite(v.Next, u) {
			return Verdict{Result: store.LinkUnknown, Detail: "redirected to another site"}, false
		}
		u = v.Next
	}
	return Verdict{Result: store.LinkUnknown, Detail: "too many redirects"}, false
}

// sameSite reports whether two URLs belong to the same registered domain,
// so boards.greenhouse.io -> job-boards.greenhouse.io is followed, but a
// redirect to some other company's domain is not. The public suffix list
// knows that "greenhouse.io" is one site while "co.uk" is not.
func sameSite(a, b *url.URL) bool {
	if strings.EqualFold(a.Host, b.Host) {
		return true // includes IP addresses, which have no registered domain
	}
	da, errA := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(a.Hostname()))
	db, errB := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(b.Hostname()))
	return errA == nil && errB == nil && da == db
}

// fetch makes one paced GET and reads at most 64 KB of the body (enough
// for the connection to be reused; we only need the status line).
func (c *Checker) fetch(ctx context.Context, u *url.URL, pace *pacer) (status int, location string, err error) {
	if err := pace.wait(ctx); err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	res, err := c.Client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, res.Header.Get("Location"), nil
}

// robotsFor returns the site's robots.txt rules, fetching them at most
// once a day per site.
func (c *Checker) robotsFor(ctx context.Context, u *url.URL, pace *pacer) *Robots {
	key := strings.ToLower(u.Scheme + "://" + u.Host)
	c.mu.Lock()
	if c.robots == nil {
		c.robots = map[string]robotsEntry{}
	}
	e, ok := c.robots[key]
	c.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.rules
	}

	rules, keep := DisallowAll, time.Hour // until we can read it, assume no; try again soon
	if err := pace.wait(ctx); err == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, key+"/robots.txt", nil)
		req.Header.Set("User-Agent", c.UserAgent)
		if res, err := c.Client.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 512<<10))
			res.Body.Close()
			switch {
			case res.StatusCode >= 200 && res.StatusCode < 300:
				rules, keep = ParseRobots(string(body), Agent), 24*time.Hour
			case res.StatusCode >= 300 && res.StatusCode < 500:
				rules, keep = AllowAll, 24*time.Hour // no robots.txt (or it moved): no rules
			}
		}
	}
	c.mu.Lock()
	c.robots[key] = robotsEntry{rules: rules, expires: time.Now().Add(keep)}
	c.mu.Unlock()
	return rules
}

// pacer spaces out requests to one website.
type pacer struct {
	gap  time.Duration
	last time.Time
}

func newPacer(gap time.Duration) *pacer { return &pacer{gap: gap} }

func (p *pacer) wait(ctx context.Context) error {
	if d := time.Until(p.last.Add(p.gap)); d > 0 && !p.last.IsZero() {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.last = time.Now()
	return nil
}

func shortError(err error) string {
	s := err.Error()
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
