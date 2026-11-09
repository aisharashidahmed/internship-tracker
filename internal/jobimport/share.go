package jobimport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"internship-tracker/internal/listing"
)

// DailyShareLimit: shares per user per day (decided 2026-11-09).
const DailyShareLimit = 5

// ErrDuplicate: making it public would collide with an existing listing.
var ErrDuplicate = errors.New("duplicate listing")

// ShareCandidate is a private listing as the share review sees it.
type ShareCandidate struct {
	ListingID       int64
	Company, Title  string
	Season          string
	JobType         listing.JobType
	ApplyURL        string
	JobKey          string
	EmailVerified   bool
	SharesToday     int
	PublicDedupeKey string // its dedupe key if it were public
	RoleKey         string
}

// Share outcomes.
const (
	Public  = "public"  // passed every gate: now in everyone's feed
	Merged  = "merged"  // it was already public: your application moved there
	Private = "private" // a gate clearly failed: stays private, with the reason
	Review  = "review"  // a gate couldn't tell: waits for a human
)

// ShareStore is what sharing needs from the database.
type ShareStore interface {
	ShareCandidateFor(ctx context.Context, userID, listingID int64) (ShareCandidate, error)
	PublicDuplicate(ctx context.Context, c ShareCandidate) (int64, bool, error)
	MergePrivateInto(ctx context.Context, userID, privateID, publicID int64) error
	MakePublic(ctx context.Context, c ShareCandidate) error
	RecordShare(ctx context.Context, userID, listingID int64, outcome, reason string) error
}

// Sharer reviews a request to make a private listing public.
type Sharer struct {
	Store ShareStore
	Fetch Fetcher
}

// knownBoards: job boards whose links carry a job ID we recognize
// (listing.JobLink names them in the identity).
var knownBoards = []string{"greenhouse:", "lever:", "ashby:", "workday:", "amazon:"}

var seasonFormat = regexp.MustCompile(`^(Summer|Fall|Winter|Spring) 20\d\d$`)

// Share runs the gates in order, cheapest first, and records the outcome:
//
//	limit  at most DailyShareLimit shares per user per day
//	1      a job-specific link on a known job board, with a job ID
//	4      the fields are valid
//	3      not already public (if it is: merge into it)
//	5      the sharer's email is verified
//	2      the link works right now (dead: private; can't tell: review)
func (s *Sharer) Share(ctx context.Context, userID, listingID int64) (outcome, reason string, err error) {
	c, err := s.Store.ShareCandidateFor(ctx, userID, listingID)
	if err != nil {
		return "", "", err
	}
	decide := func(o, why string) (string, string, error) {
		return o, why, s.Store.RecordShare(ctx, userID, listingID, o, why)
	}

	if c.SharesToday >= DailyShareLimit {
		return Private, fmt.Sprintf("You can share %d listings a day. Try again tomorrow.", DailyShareLimit), nil // not recorded: doesn't count
	}

	// Gate 1.
	known := false
	for _, b := range knownBoards {
		known = known || strings.HasPrefix(c.JobKey, b)
	}
	if !known {
		return decide(Private, "Only links to a specific job on Greenhouse, Lever, Ashby, Workday or Amazon can be shared.")
	}

	// Gate 4.
	if Clean(c.Company) == "" || Clean(c.Title) == "" || (c.Season != "" && !seasonFormat.MatchString(c.Season)) {
		return decide(Private, "The company, title or season isn't filled in correctly.")
	}

	// Gate 3: already public? Merge instead of adding a second copy.
	if publicID, dup, err := s.Store.PublicDuplicate(ctx, c); err != nil {
		return "", "", err
	} else if dup {
		if err := s.Store.MergePrivateInto(ctx, userID, listingID, publicID); err != nil {
			return "", "", err
		}
		return Merged, "This job was already public; your application now points to it.", s.Store.RecordShare(ctx, userID, publicID, Merged, "")
	}

	// Gate 5.
	if !c.EmailVerified {
		return decide(Private, "Verify your email to share.")
	}

	// Gate 2: does the link work right now?
	switch s.linkWorks(ctx, c.ApplyURL) {
	case "dead":
		return decide(Private, "The link no longer works, so the job may be closed.")
	case "unknown":
		return decide(Review, "Waiting for review: we couldn't confirm the link works.")
	}

	if err := s.Store.MakePublic(ctx, c); errors.Is(err, ErrDuplicate) {
		return decide(Review, "Waiting for review: a very similar listing exists.")
	} else if err != nil {
		return "", "", err
	}
	return decide(Public, "")
}

var greenhouseJob = regexp.MustCompile(`/jobs/(\d+)`)

// linkWorks: "alive", "dead" or "unknown", using the same clear signals as
// the link checker (404/410; Greenhouse sending you away from the job).
func (s *Sharer) linkWorks(ctx context.Context, link string) string {
	res, err := s.Fetch.Get(ctx, link)
	if err != nil {
		return "unknown"
	}
	switch {
	case res.Status == 404 || res.Status == 410:
		return "dead"
	case res.Status != 200:
		return "unknown"
	}
	if u, err := url.Parse(link); err == nil && strings.HasSuffix(u.Hostname(), "greenhouse.io") {
		if m := greenhouseJob.FindStringSubmatch(u.Path); m != nil && res.FinalURL != nil && !strings.Contains(res.FinalURL.String(), m[1]) {
			return "dead"
		}
	}
	if u, err := url.Parse(link); err == nil && strings.HasSuffix(u.Hostname(), "myworkdayjobs.com") {
		return "unknown" // Workday answers 200 for everything (verified Nov 2026)
	}
	return "alive"
}
