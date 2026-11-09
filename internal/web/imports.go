package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/safefetch"
	"internship-tracker/internal/store"
)

// Imports is what "Add jobs I applied to" and sharing need.
type Imports interface {
	EnqueueImports(ctx context.Context, userID int64, urls []string) (int, error)
	Imports(ctx context.Context, userID int64) ([]store.Import, error)
	DismissImport(ctx context.Context, userID, id int64) error
	SaveImport(ctx context.Context, userID, importID int64, e store.ImportEdit, appliedOn time.Time) (int64, error)
	ImportStats(ctx context.Context, since time.Time) (store.ImportStats, error)
	Share(ctx context.Context, userID, listingID int64) (outcome, reason string, err error)
	LastShare(ctx context.Context, userID, listingID int64) (outcome, reason string, err error)
}

// stepLabels say where a result came from, so the user knows how much to trust it.
var stepLabels = map[string]string{
	"match":  "Found in our listings",
	"api":    "From the job board",
	"jsonld": "Read from the job page",
	"form":   "We couldn't read this page: please fill it in",
}

var seasons = func() []string {
	var out []string
	for _, y := range []string{"2026", "2027", "2028"} {
		for _, s := range []string{"Spring", "Summer", "Fall", "Winter"} {
			out = append(out, s+" "+y)
		}
	}
	return out
}()

// ImportsPage is the data for "Add jobs I applied to".
type ImportsPage struct {
	User    *store.User
	Nav     *Nav
	Loc     *time.Location
	Items   []ImportRow
	Pending bool
	Error   string
	Today   string
	Seasons []string
	Limit   int
}

// ImportRow is one link on the page.
type ImportRow struct {
	store.Import
	StepLabel string
}

func (s *Server) importsPage(w http.ResponseWriter, r *http.Request) {
	s.showImports(w, r, http.StatusOK, "")
}

func (s *Server) showImports(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	items, err := s.imports.Imports(r.Context(), u.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	page := ImportsPage{User: u, Nav: s.nav(r), Loc: s.location(r), Error: errMsg, Seasons: seasons, Limit: store.DailyImportLimit,
		Today: time.Now().In(s.location(r)).Format("2006-01-02")}
	for _, it := range items {
		page.Items = append(page.Items, ImportRow{it, stepLabels[it.Step]})
		page.Pending = page.Pending || it.Status == "queued" || it.Status == "running"
	}
	s.render(w, status, "imports.html", page)
}

// addImports queues pasted links (one per line). Bad links are refused
// right away; good ones are looked up in the background.
func (s *Server) addImports(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var links []string
	seen := map[string]bool{}
	for _, line := range strings.Fields(r.PostFormValue("links")) {
		if seen[line] {
			continue
		}
		seen[line] = true
		if _, err := safefetch.Check(line); err != nil {
			s.showImports(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("%q: %v", truncate(line, 80), err))
			return
		}
		links = append(links, line)
	}
	if len(links) == 0 {
		s.showImports(w, r, http.StatusUnprocessableEntity, "Paste at least one job link.")
		return
	}
	if _, err := s.imports.EnqueueImports(r.Context(), u.ID, links); errors.Is(err, store.ErrImportLimit) {
		s.showImports(w, r, http.StatusTooManyRequests, err.Error())
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/applications/add", http.StatusSeeOther)
}

// saveImport saves a confirmed (possibly edited) result as an application.
func (s *Server) saveImport(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	e := store.ImportEdit{
		Company: clean(r.PostFormValue("company")), Title: clean(r.PostFormValue("title")),
		Location: clean(r.PostFormValue("location")), Season: r.PostFormValue("season"),
		JobType: listing.JobType(r.PostFormValue("job_type")),
	}
	if e.Company == "" || e.Title == "" {
		s.showImports(w, r, http.StatusUnprocessableEntity, "Company and title are needed to save.")
		return
	}
	if e.Season != "" && !contains(seasons, e.Season) {
		e.Season = ""
	}
	if e.JobType != listing.Internship && e.JobType != listing.NewGrad {
		e.JobType = listing.Internship
	}
	applied, err := time.ParseInLocation("2006-01-02", r.PostFormValue("applied_on"), s.location(r))
	if err != nil || applied.After(time.Now()) {
		applied = time.Now().In(s.location(r))
	}
	appID, err := s.imports.SaveImport(r.Context(), u.ID, id, e, applied)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d", appID), http.StatusSeeOther)
}

func (s *Server) dismissImport(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	if id, ok := pathID(r); ok {
		if err := s.imports.DismissImport(r.Context(), u.ID, id); err != nil {
			serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/applications/add", http.StatusSeeOther)
}

// shareApplication asks to make the application's private listing public.
func (s *Server) shareApplication(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	app, _, err := s.tracker.Application(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if _, _, err := s.imports.Share(r.Context(), u.ID, app.ListingID); err != nil && !errors.Is(err, store.ErrNotFound) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d", id), http.StatusSeeOther)
}

// importStats reports, in aggregate only (no links, no users), which step
// of the chain produced each result over the last N days (default 7).
func (s *Server) importStats(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 || days > 90 {
		days = 7
	}
	st, err := s.imports.ImportStats(r.Context(), time.Now().AddDate(0, 0, -days))
	if err != nil {
		serverError(w, err)
		return
	}
	done := st.Match + st.API + st.JSONLD + st.Form
	pct := func(n int) float64 {
		if done == 0 {
			return 0
		}
		return float64(int(1000*float64(n)/float64(done))) / 10
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"days": days, "since": st.Since.UTC().Format(time.RFC3339), "finished": done,
		"by_step": map[string]int{"match": st.Match, "api": st.API, "jsonld": st.JSONLD, "form": st.Form},
		"percent": map[string]float64{"match": pct(st.Match), "api": pct(st.API), "jsonld": pct(st.JSONLD), "form": pct(st.Form)},
		"failed":  st.Failed, "pending": st.Pending,
	})
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
