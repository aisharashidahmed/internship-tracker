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
}

// Server holds what every request needs.
type Server struct {
	feed FeedReader
	tmpl *template.Template
}

// New parses the templates once, at startup, so a broken template fails
// immediately instead of on the first request.
func New(f FeedReader) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"ago":           func(t time.Time) string { return ago(t, time.Now()) },
		"roleLabel":     listing.RoleLabel,
		"industryLabel": func(s string) string { return listing.IndustryLabels[listing.Industry(s)] },
	}).ParseFS(templates, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{feed: f, tmpl: tmpl}, nil
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.feedPage)
	mux.HandleFunc("GET /api/listings", s.feedJSON)
	return mux
}

// Filters is what the visitor chose in the filter bar.
type Filters struct {
	JobType    listing.JobType    `json:"job_type"`
	Roles      []listing.Role     `json:"roles"`
	MatchAll   bool               `json:"match_all_roles"`
	Industries []listing.Industry `json:"industries"`
}

// jobTypes are the two tabs; the first is the default.
var jobTypes = []listing.JobType{listing.Internship, listing.NewGrad}

var jobTypeLabels = map[listing.JobType]string{listing.Internship: "Internships", listing.NewGrad: "New grad"}

// parseFilters reads the URL. Unknown values are ignored rather than
// rejected: an old bookmark should still show something sensible.
func parseFilters(q url.Values) Filters {
	f := Filters{JobType: jobTypes[0], MatchAll: q.Get("match") == "all"}
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
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return "?" + v.Encode()
}

// Page is one page of the feed; it is also the JSON response body.
type Page struct {
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

// Filtered reports whether any role or industry filter is on.
func (p Page) Filtered() bool { return len(p.Filters.Roles) > 0 || len(p.Filters.Industries) > 0 }

// ClearURL keeps the tab but drops role and industry filters.
func (p Page) ClearURL() string { return Filters{JobType: p.Filters.JobType}.query(1) }
func (p Page) NextURL() string  { return p.Filters.query(p.NextPage) }
func (p Page) PrevURL() string  { return p.Filters.query(p.Page - 1) }

func (s *Server) loadPage(r *http.Request) (Page, error) {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	f := parseFilters(r.URL.Query())

	// Ask for one extra row: if it comes back, there is a next page.
	listings, err := s.feed.Feed(r.Context(), store.FeedQuery{
		JobType: f.JobType, Roles: f.Roles, MatchAllRoles: f.MatchAll, Industries: f.Industries,
		Limit: PageSize + 1, Offset: (n - 1) * PageSize,
	})
	if err != nil {
		return Page{}, err
	}
	p := Page{Filters: f, Page: n, Cards: []feed.Card{}}
	if len(listings) > PageSize {
		listings = listings[:PageSize]
		p.NextPage = n + 1
	}
	for _, l := range listings {
		p.Cards = append(p.Cards, feed.NewCard(l))
	}
	return p, nil
}

func (s *Server) feedJSON(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadPage(r)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadPage(r)
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
