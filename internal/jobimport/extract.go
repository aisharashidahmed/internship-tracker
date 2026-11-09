// Package jobimport turns a pasted job link into job details, using the
// cheapest and most certain source first:
//
//  1. match:  the link's job ID already belongs to one of our listings
//  2. api:    the job board's public API (Greenhouse, Lever, Ashby)
//  3. jsonld: the page's schema.org JobPosting data (Workday and many others)
//  4. form:   nothing worked; pre-fill what the link itself tells us
//
// Everything fetched is untrusted: only specific fields are read, then
// cleaned and length-limited; nothing in a page can add fields or change
// which step runs.
package jobimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/safefetch"
)

// Step is which link in the chain produced a result.
type Step string

const (
	StepMatch  Step = "match"
	StepAPI    Step = "api"
	StepJSONLD Step = "jsonld"
	StepForm   Step = "form"
)

// Result is what we learned about a job. The user always confirms or edits
// it before anything is saved.
type Result struct {
	Step      Step
	JobKey    string // the canonical job identity (listing.JobLink), "" for generic links
	ListingID int64  // set for StepMatch: the existing listing to attach to
	Company   string
	Title     string
	Location  string
	Season    string
	JobType   listing.JobType
}

// Known is a listing we already have, found by job link.
type Known struct {
	ListingID                        int64
	Company, Title, Location, Season string
	JobType                          listing.JobType
}

// Store is what the chain needs from the database.
type Store interface {
	// ListingByJobKey finds the one public listing with this job link; ok
	// is false if there's none, or several (e.g. one per season).
	ListingByJobKey(ctx context.Context, jobKey string) (Known, bool, error)
	CachedExtraction(ctx context.Context, jobKey string) (Result, bool, error)
	SaveExtraction(ctx context.Context, jobKey string, r Result) error
}

// Fetcher gets pages; *safefetch.Client is the real one.
type Fetcher interface {
	Get(ctx context.Context, rawURL string) (safefetch.Result, error)
}

// Extractor runs the chain.
type Extractor struct {
	Store Store
	Fetch Fetcher
	// API base URLs; empty means the real public APIs. Tests point them
	// at fake servers.
	GreenhouseAPI, LeverAPI, AshbyAPI string
}

// ErrTemporary wraps failures worth retrying (timeouts, 5xx): the queue
// tries again later. Anything else ends at the form step.
var ErrTemporary = errors.New("temporary failure")

// Extract runs the chain for one link. It returns a usable Result even when
// every step fails (StepForm, pre-filled from the link). err is non-nil
// only for an invalid link, or (wrapping ErrTemporary) when a retry might
// do better; the Result is still the best so far.
func (e *Extractor) Extract(ctx context.Context, rawURL string) (Result, error) {
	u, err := safefetch.Check(rawURL)
	if err != nil {
		return Result{}, err
	}
	key, _ := listing.JobLink(u.String())
	form := Result{Step: StepForm, JobKey: key, Company: companyFromURL(u), JobType: listing.Internship}

	// 1. Already one of our listings.
	if key != "" {
		k, ok, err := e.Store.ListingByJobKey(ctx, key)
		if err != nil {
			return form, fmt.Errorf("%w: %v", ErrTemporary, err)
		}
		if ok {
			return Result{Step: StepMatch, JobKey: key, ListingID: k.ListingID, Company: k.Company, Title: k.Title,
				Location: k.Location, Season: k.Season, JobType: k.JobType}, nil
		}
		if r, ok, err := e.Store.CachedExtraction(ctx, key); err == nil && ok {
			r.JobKey = key
			return r, nil
		}
	}

	var temporary error
	save := func(r Result) (Result, error) {
		r.JobKey = key
		if key != "" {
			_ = e.Store.SaveExtraction(ctx, key, r) // a cache miss next time is fine
		}
		return r, nil
	}

	// 2. The job board's API.
	if r, found, err := e.fromAPI(ctx, u); err != nil {
		temporary = err
	} else if found {
		return save(r)
	}

	// 3. JobPosting data in the page.
	if r, found, err := e.fromJSONLD(ctx, u); err != nil {
		temporary = err
	} else if found {
		return save(r)
	}

	// 4. The form.
	if temporary != nil {
		return form, temporary
	}
	return form, nil
}

// --- Step 2: job board APIs --------------------------------------------------

var (
	greenhousePath = regexp.MustCompile(`^/([^/]+)/jobs/(\d+)`)
	uuidPath       = regexp.MustCompile(`^/([^/]+)/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
)

func (e *Extractor) fromAPI(ctx context.Context, u *url.URL) (Result, bool, error) {
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "greenhouse.io"):
		m := greenhousePath.FindStringSubmatch(u.Path)
		if m == nil {
			return Result{}, false, nil
		}
		var job struct {
			Title       string `json:"title"`
			CompanyName string `json:"company_name"`
			Location    struct {
				Name string `json:"name"`
			} `json:"location"`
		}
		found, err := e.getJSON(ctx, or(e.GreenhouseAPI, "https://boards-api.greenhouse.io")+"/v1/boards/"+m[1]+"/jobs/"+m[2], &job)
		if !found || err != nil {
			return Result{}, false, err
		}
		company := job.CompanyName
		if company == "" {
			company = prettySlug(m[1])
		}
		return finish(StepAPI, company, job.Title, job.Location.Name, "")

	case host == "jobs.lever.co":
		m := uuidPath.FindStringSubmatch(u.Path)
		if m == nil {
			return Result{}, false, nil
		}
		var job struct {
			Text       string `json:"text"`
			Categories struct {
				Location   string `json:"location"`
				Commitment string `json:"commitment"`
			} `json:"categories"`
		}
		found, err := e.getJSON(ctx, or(e.LeverAPI, "https://api.lever.co")+"/v0/postings/"+m[1]+"/"+m[2], &job)
		if !found || err != nil {
			return Result{}, false, err
		}
		// Lever's API has no company name: the link's site name stands in
		// (the user can correct it before saving).
		return finish(StepAPI, prettySlug(m[1]), job.Text, job.Categories.Location, job.Categories.Commitment)

	case host == "jobs.ashbyhq.com":
		m := uuidPath.FindStringSubmatch(u.Path)
		if m == nil {
			return Result{}, false, nil
		}
		var board struct {
			Jobs []struct {
				ID             string `json:"id"`
				Title          string `json:"title"`
				Location       string `json:"location"`
				EmploymentType string `json:"employmentType"`
			} `json:"jobs"`
		}
		found, err := e.getJSON(ctx, or(e.AshbyAPI, "https://api.ashbyhq.com")+"/posting-api/job-board/"+m[1], &board)
		if !found || err != nil {
			return Result{}, false, err
		}
		for _, j := range board.Jobs {
			if j.ID == m[2] {
				return finish(StepAPI, prettySlug(m[1]), j.Title, j.Location, j.EmploymentType)
			}
		}
	}
	return Result{}, false, nil
}

// getJSON fetches and decodes an API response. found is false for 404s and
// unusable answers; err (ErrTemporary) for 5xx and network failures.
func (e *Extractor) getJSON(ctx context.Context, apiURL string, into any) (found bool, err error) {
	res, err := e.Fetch.Get(ctx, apiURL)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrTemporary, err)
	}
	switch {
	case res.Status >= 500 || res.Status == 429:
		return false, fmt.Errorf("%w: HTTP %d", ErrTemporary, res.Status)
	case res.Status != 200:
		return false, nil
	}
	if err := json.Unmarshal(res.Body, into); err != nil {
		return false, nil // not the JSON we expected: move on, don't retry
	}
	return true, nil
}

// --- Step 3: JobPosting JSON-LD ----------------------------------------------

var ldScript = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)

// jobPosting holds only the schema.org fields we read. Anything else in
// the page, including text addressed to an AI or a reader, is ignored.
type jobPosting struct {
	Type               any             `json:"@type"`
	Title              string          `json:"title"`
	HiringOrganization json.RawMessage `json:"hiringOrganization"`
	JobLocation        json.RawMessage `json:"jobLocation"`
	EmploymentType     json.RawMessage `json:"employmentType"`
}

func (e *Extractor) fromJSONLD(ctx context.Context, u *url.URL) (Result, bool, error) {
	res, err := e.Fetch.Get(ctx, u.String())
	if err != nil {
		if errors.Is(err, safefetch.ErrBlocked) || errors.Is(err, safefetch.ErrRedirect) || errors.Is(err, safefetch.ErrTooBig) {
			return Result{}, false, nil // permanent: go to the form
		}
		return Result{}, false, fmt.Errorf("%w: %v", ErrTemporary, err)
	}
	if res.Status >= 500 || res.Status == 429 {
		return Result{}, false, fmt.Errorf("%w: HTTP %d", ErrTemporary, res.Status)
	}
	if res.Status != 200 {
		return Result{}, false, nil
	}
	for _, block := range ldScript.FindAllSubmatch(res.Body, 20) {
		for _, p := range postings(block[1]) {
			// Some sites (seen on HP's Workday) leave hiringOrganization.name
			// empty; the title and location are still good, so the company
			// comes from the link instead (the user can correct it).
			company := orgName(p.HiringOrganization)
			if strings.TrimSpace(company) == "" {
				company = companyFromURL(u)
			}
			if r, ok, _ := finish(StepJSONLD, company, p.Title, place(p.JobLocation), strings.Join(strs(p.EmploymentType), " ")); ok {
				return r, true, nil
			}
		}
	}
	return Result{}, false, nil
}

// postings finds JobPosting objects in one JSON-LD block: a single object,
// a list, or an "@graph".
func postings(raw []byte) []jobPosting {
	var anyv any
	if json.Unmarshal(raw, &anyv) != nil {
		return nil
	}
	var out []jobPosting
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, y := range x {
				walk(y)
			}
		case map[string]any:
			if g, ok := x["@graph"]; ok {
				walk(g)
			}
			if t := fmt.Sprint(x["@type"]); strings.Contains(t, "JobPosting") {
				b, _ := json.Marshal(x)
				var p jobPosting
				if json.Unmarshal(b, &p) == nil {
					out = append(out, p)
				}
			}
		}
	}
	walk(anyv)
	return out
}

func orgName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &o)
	return o.Name
}

// place reads jobLocation (an object or a list) into "City, Region".
func place(raw json.RawMessage) string {
	type loc struct {
		Address struct {
			Locality string `json:"addressLocality"`
			Region   string `json:"addressRegion"`
			Country  any    `json:"addressCountry"`
		} `json:"address"`
	}
	var many []loc
	if json.Unmarshal(raw, &many) != nil {
		var one loc
		if json.Unmarshal(raw, &one) != nil {
			return ""
		}
		many = []loc{one}
	}
	if len(many) == 0 {
		return ""
	}
	a := many[0].Address
	var parts []string
	for _, p := range []string{a.Locality, a.Region} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func strs(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	json.Unmarshal(raw, &many)
	return many
}

// --- Cleaning and validation (the "schema" every result passes through) -----

const maxField = 200

var (
	tags       = regexp.MustCompile(`<[^>]*>`)
	spaces     = regexp.MustCompile(`\s+`)
	internWord = regexp.MustCompile(`(?i)\bintern(ship)?\b|co-?op`)
)

// Clean turns untrusted text into a plain, single-line, bounded string:
// HTML tags and entities removed, control characters dropped.
func Clean(s string) string {
	s = html.UnescapeString(tags.ReplaceAllString(s, " "))
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '�' {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > maxField {
		s = string(r[:maxField])
	}
	return s
}

// finish cleans fields and decides job type and season. ok is false when
// company or title is missing: then this step "didn't find" the job.
func finish(step Step, company, title, location, employment string) (Result, bool, error) {
	r := Result{Step: step, Company: Clean(company), Title: Clean(title), Location: Clean(location)}
	if r.Company == "" || r.Title == "" {
		return Result{}, false, nil
	}
	r.Season = listing.SeasonFromTitle(r.Title)
	r.JobType = listing.JobTypeFromTitle(r.Title, listing.Internship)
	if r.JobType == listing.Internship && employment != "" && !internWord.MatchString(employment) &&
		strings.Contains(strings.ToLower(employment), "full") {
		// a full-time role that doesn't say "intern": treat as new grad
		r.JobType = listing.NewGrad
	}
	return r, true, nil
}

// --- Step 4: what the link alone tells us --------------------------------------

// companyFromURL guesses a company from a link, for the form:
// jobs.lever.co/matchgroup/... -> "Matchgroup", nvidia.wd5.myworkdayjobs.com -> "Nvidia",
// careers.stripe.com/... -> "Stripe".
func companyFromURL(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case host == "jobs.lever.co" || host == "jobs.ashbyhq.com" || strings.HasSuffix(host, "greenhouse.io"):
		if len(path) > 0 {
			return prettySlug(path[0])
		}
	case strings.HasSuffix(host, ".myworkdayjobs.com"):
		return prettySlug(strings.Split(host, ".")[0])
	}
	labels := strings.Split(strings.TrimPrefix(host, "www."), ".")
	if len(labels) >= 2 {
		return prettySlug(labels[len(labels)-2])
	}
	return ""
}

// prettySlug: "allen-control-systems" -> "Allen Control Systems".
func prettySlug(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return Clean(strings.Join(words, " "))
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
