// Package safefetch fetches web pages on a user's behalf without letting
// them point the server at itself or its private network (SSRF).
//
// Rules, enforced on every connection (including after redirects):
//   - only http and https, only ports 80 and 443;
//   - the host's DNS answers are checked, and we connect to the exact
//     checked IP, so a name can't resolve to a public address for the
//     check and a private one for the connection (DNS rebinding);
//   - loopback, private, link-local (incl. 169.254.169.254, the cloud
//     metadata service), carrier-grade NAT, multicast and other special
//     ranges are refused;
//   - redirects only within the same registered domain, at most 3;
//   - 10 s per request, at most 2 MB read.
package safefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	MaxBytes     = 2 << 20
	Timeout      = 10 * time.Second
	MaxRedirects = 3
)

var (
	ErrScheme   = errors.New("only http and https links are allowed")
	ErrPort     = errors.New("only standard web ports (80, 443) are allowed")
	ErrBlocked  = errors.New("that address is not a public website")
	ErrRedirect = errors.New("the link redirected to a different website")
	ErrTooBig   = errors.New("the page is too large")
)

// blocked holds address ranges a fetch may never reach.
var blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96", "2001:db8::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// Public reports whether ip is an ordinary public internet address.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap() // ::ffff:127.0.0.1 is 127.0.0.1
	if !ip.IsValid() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Client fetches pages safely. The zero value is ready to use.
type Client struct {
	UserAgent string
	// forTests lets tests talk to httptest servers (127.0.0.1, any port).
	// It can only be set from inside this package's tests.
	forTests bool
	// lookupFn replaces DNS in tests; nil means the system resolver.
	lookupFn func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Result is a fetched page.
type Result struct {
	Status   int
	FinalURL *url.URL
	Body     []byte
}

// Get fetches rawURL under the rules above.
func (c *Client) Get(ctx context.Context, rawURL string) (Result, error) {
	u, err := c.check(rawURL)
	if err != nil {
		return Result{}, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil, // never route through an environment proxy
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := c.lookup(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !Public(ip) && !(c.forTests && ip.IsLoopback()) {
					return nil, ErrBlocked
				}
			}
			// Connect to the IP we checked, not the name (no second lookup).
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: Timeout,
	}
	client := &http.Client{
		Timeout:   Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if _, err := c.check(req.URL.String()); err != nil {
				return err
			}
			if !SameSite(req.URL, via[0].URL) {
				return ErrRedirect
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	// Ask like a browser. Workday answers a request that mentions
	// application/json with a 155-byte JSON stub instead of the job page
	// (measured Nov 2026); the job-board APIs return JSON either way.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	res, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && errors.Is(ue.Err, ErrBlocked) {
			return Result{}, ErrBlocked
		}
		return Result{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return Result{}, err
	}
	if len(body) > MaxBytes {
		return Result{}, ErrTooBig
	}
	return Result{Status: res.StatusCode, FinalURL: res.Request.URL, Body: body}, nil
}

func (c *Client) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if c.lookupFn != nil {
		return c.lookupFn(ctx, host)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	return ips, nil
}

func (c *Client) check(rawURL string) (*url.URL, error) {
	if c.forTests {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, ErrScheme
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !Public(ip) && !ip.IsLoopback() {
			return nil, ErrBlocked
		}
		return u, nil
	}
	return Check(rawURL)
}

// Check validates a URL's shape before any network access: scheme, port,
// and that a literal IP host is public. Names are checked when dialing.
func Check(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("not a valid link")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrScheme
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return nil, ErrPort
	}
	if u.User != nil {
		return nil, fmt.Errorf("links with a username or password aren't allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return nil, ErrBlocked
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !Public(ip) {
			return nil, ErrBlocked
		}
		return u, nil
	}
	// Shorthand and odd number forms of IPs (127.1, 2130706433, 0x7f.1,
	// 0177.0.0.1) aren't valid names, but some system resolvers turn them
	// into addresses. A real domain name ends in letters, so refuse any
	// host whose last label is numeric or hex-looking.
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return nil, ErrBlocked
	}
	return u, nil
}

// SameSite: same registered domain (boards.greenhouse.io and
// job-boards.greenhouse.io are one site; two different companies aren't).
func SameSite(a, b *url.URL) bool {
	if strings.EqualFold(a.Host, b.Host) {
		return true
	}
	da, errA := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(a.Hostname()))
	db, errB := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(b.Hostname()))
	return errA == nil && errB == nil && da == db
}
