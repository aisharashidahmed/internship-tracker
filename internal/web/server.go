// Package web serves the feed: an HTML page for people (GET /) and the
// same data as JSON (GET /api/listings). Both are built by one function,
// loadPage, so they can never show different things.
//
// Filters live in the URL (?type=internship&role=backend&role=ml&match=all
// &industry=healthcare&page=2), so any filtered view can be bookmarked.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"internship-tracker/internal/auth"
	"internship-tracker/internal/feed"
	"internship-tracker/internal/listing"
	"internship-tracker/internal/store"
)

// PageSize is how many cards one feed page shows.
const PageSize = 50

//go:embed templates/*.html
var templates embed.FS

// FeedReader is what the server needs from the database. *store.Store
// satisfies it; tests pass a fake, so they need no database.
type FeedReader interface {
	Feed(ctx context.Context, q store.FeedQuery) ([]store.FeedListing, error)
	CountFeed(ctx context.Context, q store.FeedQuery) (int, error)
}

// Server holds what every request needs.
type Server struct {
	feed          FeedReader
	accounts      Accounts
	tracker       Tracker
	tmpl          *template.Template
	secureCookies bool // set when served over HTTPS behind a proxy
	emailLimiter  *auth.Limiter
	ipLimiter     *auth.Limiter
}

// Options configure a Server.
type Options struct {
	// SecureCookies marks the session cookie HTTPS-only. Turn it on in
	// production; plain http://localhost needs it off.
	SecureCookies bool
}

// New parses the templates once, at startup, so a broken template fails
// immediately instead of on the first request.
func New(f FeedReader, accounts Accounts, tracker Tracker, opts Options) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"ago":           func(t time.Time) string { return ago(t, time.Now()) },
		"since":         func(t time.Time) string { return t.Local().Format("Mon 3:04 PM") },
		"deref":         func(p any) any { return derefValue(p) },
		"roleLabel":     listing.RoleLabel,
		"industryLabel": func(s string) string { return listing.IndustryLabels[listing.Industry(s)] },
	}).ParseFS(templates, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		feed: f, accounts: accounts, tracker: tracker, tmpl: tmpl, secureCookies: opts.SecureCookies,
		emailLimiter: auth.NewLimiter(5, loginWindow),
		ipLimiter:    auth.NewLimiter(20, loginWindow),
	}, nil
}

// Handler returns the routes, wrapped in two layers:
//   - withUser recognizes the logged-in user from their cookie;
//   - CrossOriginProtection rejects form posts sent from other websites
//     (cross-site request forgery), using the browser's Sec-Fetch-Site
//     and Origin headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.feedPage)
	mux.HandleFunc("GET /api/listings", s.feedJSON)
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signup)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("POST /seen", s.markAllSeen)
	mux.HandleFunc("POST /applications", s.startApplication)
	mux.HandleFunc("GET /applications", s.applicationsPage)
	mux.HandleFunc("GET /applications/{id}", s.applicationPage)
	mux.HandleFunc("POST /applications/{id}", s.updateApplication)
	mux.HandleFunc("POST /applications/{id}/stage", s.moveStage)
	mux.HandleFunc("POST /applications/{id}/delete", s.deleteApplication)
	return http.NewCrossOriginProtection().Handler(s.withUser(mux))
}

// Filters is what the visitor chose in the filter bar.
type Filters struct {
	JobType     listing.JobType    `json:"job_type"`
	Roles       []listing.Role     `json:"roles"`
	MatchAll    bool               `json:"match_all_roles"`
	Industries  []listing.Industry `json:"industries"`
	OnlyNew     bool               `json:"only_new"`     // only listings new since you last looked (logged in)
	ShowApplied bool               `json:"show_applied"` // include listings you've applied to (logged in)
}

// jobTypes are the two tabs; the first is the default.
var jobTypes = []listing.JobType{listing.Internship, listing.NewGrad}

var jobTypeLabels = map[listing.JobType]string{listing.Internship: "Internships", listing.NewGrad: "New grad"}

// parseFilters reads the URL. Unknown values are ignored rather than
// rejected: an old bookmark should still show something sensible.
func parseFilters(q url.Values) Filters {
	f := Filters{JobType: jobTypes[0], MatchAll: q.Get("match") == "all", OnlyNew: q.Get("new") == "1",
		ShowApplied: q.Get("applied") == "1"}
	if t := listing.JobType(q.Get("type")); slices.Contains(jobTypes, t) {
		f.JobType = t
	}
	for _, r := range q["role"] {
		if role := listing.Role(r); slices.Contains(listing.AllRoles, role) && !slices.Contains(f.Roles, role) {
			f.Roles = append(f.Roles, role)
		}
	}
	for _, i := range q["industry"] {
		if ind := listing.Industry(i); listing.IndustryLabels[ind] != "" && !slices.Contains(f.Industries, ind) {
			f.Industries = append(f.Industries, ind)
		}
	}
	return f
}

// query turns filters back into URL parameters, for links that keep them.
func (f Filters) query(page int) string {
	v := url.Values{"type": {string(f.JobType)}}
	for _, r := range f.Roles {
		v.Add("role", string(r))
	}
	if f.MatchAll {
		v.Set("match", "all")
	}
	for _, i := range f.Industries {
		v.Add("industry", string(i))
	}
	if f.OnlyNew {
		v.Set("new", "1")
	}
	if f.ShowApplied {
		v.Set("applied", "1")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return "?" + v.Encode()
}

// Page is one page of the feed; it is also the JSON response body.
type Page struct {
	User     *store.User `json:"-"`                   // for the header only
	NewSince *time.Time  `json:"new_since,omitempty"` // logged in only
	NewCount int         `json:"new_count"`           // listings new since then, under these filters
	Filters  Filters     `json:"filters"`
	Page     int         `json:"page"`
	NextPage int         `json:"next_page,omitempty"` // 0 (omitted) on the last page
	Cards    []feed.Card `json:"listings"`
}

// Option is one choice in the filter bar.
type Option struct {
	Value, Label string
	On           bool
}

// The template uses these methods to draw the filter bar and page links.

func (p Page) JobTypeOptions() []Option {
	var out []Option
	for _, t := range jobTypes {
		out = append(out, Option{string(t), jobTypeLabels[t], t == p.Filters.JobType})
	}
	return out
}

func (p Page) RoleOptions() []Option {
	var out []Option
	for _, r := range listing.AllRoles {
		out = append(out, Option{string(r), listing.RoleLabels[r], slices.Contains(p.Filters.Roles, r)})
	}
	return out
}

func (p Page) IndustryOptions() []Option {
	var out []Option
	for ind, label := range listing.IndustryLabels {
		out = append(out, Option{string(ind), label, slices.Contains(p.Filters.Industries, ind)})
	}
	slices.SortFunc(out, func(a, b Option) int { return strings.Compare(a.Label, b.Label) })
	return out
}

// Filtered reports whether any filter beyond the tab is on.
func (p Page) Filtered() bool {
	return len(p.Filters.Roles) > 0 || len(p.Filters.Industries) > 0 || p.Filters.OnlyNew || p.Filters.ShowApplied
}

// HereURL is this page's own address, so "Mark all as seen" can come back.
func (p Page) HereURL() string { return "/" + p.Filters.query(p.Page) }

// ClearURL keeps the tab but drops role and industry filters.
func (p Page) ClearURL() string { return Filters{JobType: p.Filters.JobType}.query(1) }
func (p Page) NextURL() string  { return p.Filters.query(p.NextPage) }
func (p Page) PrevURL() string  { return p.Filters.query(p.Page - 1) }

// loadPage builds one feed page. countsAsVisit is true for the HTML page:
// opening it is "looking" (see store.ViewFeed). The JSON API only reads
// the boundary, so a script polling it can't clear your New badges.
func (s *Server) loadPage(r *http.Request, countsAsVisit bool) (Page, error) {
	ctx := r.Context()
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	p := Page{User: currentUser(r), Filters: parseFilters(r.URL.Query()), Page: n, Cards: []feed.Card{}}

	if p.User != nil {
		var since time.Time
		ok := true
		if countsAsVisit {
			since, err = s.accounts.ViewFeed(ctx, p.User.ID)
		} else {
			since, ok, err = s.accounts.FeedNewSince(ctx, p.User.ID)
		}
		if err != nil {
			return Page{}, err
		}
		if ok {
			p.NewSince = &since
		}
	}
	if p.NewSince == nil {
		p.Filters.OnlyNew = false // nothing is "new" without a boundary
	}
	var userID int64
	if p.User != nil {
		userID = p.User.ID
	} else {
		p.Filters.ShowApplied = false // nothing is "applied" when logged out
	}

	f := p.Filters
	q := store.FeedQuery{
		JobType: f.JobType, Roles: f.Roles, MatchAllRoles: f.MatchAll, Industries: f.Industries,
		OnlyNew: f.OnlyNew, NewSince: p.NewSince, UserID: userID, ShowApplied: f.ShowApplied,
		Limit: PageSize + 1, Offset: (n - 1) * PageSize, // one extra row: if it comes back, there's a next page
	}
	listings, err := s.feed.Feed(ctx, q)
	if err != nil {
		return Page{}, err
	}
	if p.NewSince != nil {
		q.OnlyNew = true
		if p.NewCount, err = s.feed.CountFeed(ctx, q); err != nil {
			return Page{}, err
		}
	}

	if len(listings) > PageSize {
		listings = listings[:PageSize]
		p.NextPage = n + 1
	}
	for _, l := range listings {
		p.Cards = append(p.Cards, feed.NewCard(l, p.NewSince))
	}
	return p, nil
}

// markAllSeen handles the "Mark all as seen" button, then returns to the
// same feed view. Only paths on this site are accepted as the way back.
func (s *Server) markAllSeen(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := s.accounts.MarkAllSeen(r.Context(), u.ID); err != nil {
		serverError(w, err)
		return
	}
	back := r.PostFormValue("back")
	if !strings.HasPrefix(back, "/?") {
		back = "/" // never redirect off-site ("//evil.com" doesn't start with "/?")
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) feedJSON(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadPage(r, false)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadPage(r, true)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "feed.html", p); err != nil {
		log.Printf("render feed: %v", err) // headers are already sent; just log it
	}
}

// serverError logs the real error and shows the visitor a generic one, so
// database details never leak into a page.
func serverError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

// ago formats how long ago t was, to the day: "today", "1 day ago", "5 days ago".
func ago(t, now time.Time) string {
	days := int(now.Sub(t).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}

// derefValue lets templates show *int and *int64 fields.
func derefValue(p any) any {
	switch v := p.(type) {
	case *int:
		if v != nil {
			return *v
		}
	case *int64:
		if v != nil {
			return *v
		}
	}
	return nil
}
