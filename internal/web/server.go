// Package web serves the feed: an HTML page for people (GET /) and the
// same data as JSON (GET /api/listings). Both are built by one function,
// loadPage, so they can never show different things.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
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
	Feed(ctx context.Context, jobType listing.JobType, limit, offset int) ([]store.FeedListing, error)
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
		"ago":      func(t time.Time) string { return ago(t, time.Now()) },
		"subtract": func(a, b int) int { return a - b },
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

// Tabs are the job types the feed can show, first one is the default.
var Tabs = []Tab{
	{Type: listing.Internship, Label: "Internships"},
	{Type: listing.NewGrad, Label: "New grad"},
}

// Tab is one choice in the Internships / New grad toggle.
type Tab struct {
	Type  listing.JobType
	Label string
}

// Page is one page of the feed; it is also the JSON response body.
type Page struct {
	JobType  listing.JobType `json:"job_type"`
	Tabs     []Tab           `json:"-"` // for the HTML toggle only
	Page     int             `json:"page"`
	NextPage int             `json:"next_page,omitempty"` // 0 (omitted) on the last page
	Cards    []feed.Card     `json:"listings"`
}

func (s *Server) loadPage(r *http.Request) (Page, error) {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	jobType := Tabs[0].Type // anything unrecognized shows the default tab
	for _, t := range Tabs {
		if string(t.Type) == r.URL.Query().Get("type") {
			jobType = t.Type
		}
	}
	// Ask for one extra row: if it comes back, there is a next page.
	listings, err := s.feed.Feed(r.Context(), jobType, PageSize+1, (n-1)*PageSize)
	if err != nil {
		return Page{}, err
	}
	p := Page{JobType: jobType, Tabs: Tabs, Page: n, Cards: []feed.Card{}}
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
