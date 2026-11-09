package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"internship-tracker/internal/feed"
	"internship-tracker/internal/store"
)

// Tracker is what the application pages need. *store.Store satisfies it.
// Every method takes the user id: a user only ever sees their own.
type Tracker interface {
	StartApplication(ctx context.Context, userID, listingID int64, stage store.Stage, appliedOn time.Time) (int64, bool, error)
	MoveStage(ctx context.Context, userID, appID int64, to store.Stage, round *int, today time.Time) error
	UpdateDetails(ctx context.Context, userID, appID int64, d store.Details) error
	DeleteApplication(ctx context.Context, userID, appID int64) error
	Applications(ctx context.Context, userID int64) ([]store.Application, error)
	Application(ctx context.Context, userID, appID int64) (store.Application, []store.Event, error)
	ResumeVersions(ctx context.Context, userID int64) ([]store.ResumeVersion, error)
	AddResumeVersion(ctx context.Context, userID int64, label string) (int64, error)
}

// requireUser sends logged-out visitors to the login page.
func requireUser(w http.ResponseWriter, r *http.Request) *store.User {
	u := currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
	return u
}

// pathID reads {id} from the URL; ok is false if it isn't a number.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// localBack returns a "go back to" path from a form, only if it's on this site.
func localBack(r *http.Request, fallback string) string {
	back := r.PostFormValue("back")
	if strings.HasPrefix(back, "/") && !strings.HasPrefix(back, "//") && !strings.Contains(back, `\`) {
		return back
	}
	return fallback
}

// startApplication handles the Save and Applied buttons on a feed card.
// Save returns to the feed; Applied opens the application to pick a resume.
func (s *Server) startApplication(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	listingID, err := strconv.ParseInt(r.PostFormValue("listing_id"), 10, 64)
	stage := store.Stage(r.PostFormValue("stage"))
	if err != nil || (stage != store.StageSaved && stage != store.StageApplied) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	// "Applied today" means today where the user is.
	id, _, err := s.tracker.StartApplication(r.Context(), u.ID, listingID, stage, time.Now().In(s.location(r)))
	if err != nil {
		serverError(w, err)
		return
	}
	if stage == store.StageApplied {
		http.Redirect(w, r, fmt.Sprintf("/applications/%d", id), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, localBack(r, "/"), http.StatusSeeOther)
}

// StageGroup is one section of the My applications page.
type StageGroup struct {
	Label string
	Apps  []AppRow
}

// AppRow is one application in a list.
type AppRow struct {
	store.Application
	StageLabel string
}

// ApplicationsPage is the data for My applications.
type ApplicationsPage struct {
	User   *store.User
	Loc    *time.Location
	Total  int
	Groups []StageGroup
}

func (s *Server) applicationsPage(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	apps, err := s.tracker.Applications(r.Context(), u.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	page := ApplicationsPage{User: u, Loc: s.location(r), Total: len(apps)}
	for _, stage := range store.Stages {
		g := StageGroup{Label: store.StageLabels[stage]}
		for _, a := range apps {
			if a.Stage == stage {
				g.Apps = append(g.Apps, AppRow{a, feed.StageLabel(a.Stage, a.InterviewRound)})
			}
		}
		if len(g.Apps) > 0 {
			page.Groups = append(page.Groups, g)
		}
	}
	s.render(w, http.StatusOK, "applications.html", page)
}

// ApplicationPage is the data for one application.
type ApplicationPage struct {
	User       *store.User
	Loc        *time.Location
	App        store.Application
	StageLabel string
	Events     []EventRow
	Resumes    []store.ResumeVersion
	Stages     []StageOption
	Error      string
}

// EventRow is one line of the history.
type EventRow struct {
	At    time.Time
	Label string
}

// StageOption is one stage button.
type StageOption struct {
	Value, Label string
	Current      bool
}

func (s *Server) applicationPage(w http.ResponseWriter, r *http.Request) {
	s.showApplication(w, r, http.StatusOK, "")
}

func (s *Server) showApplication(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	app, events, err := s.tracker.Application(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r) // someone else's looks exactly like one that doesn't exist
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	resumes, err := s.tracker.ResumeVersions(r.Context(), u.ID)
	if err != nil {
		serverError(w, err)
		return
	}

	page := ApplicationPage{User: u, Loc: s.location(r), App: app, StageLabel: feed.StageLabel(app.Stage, app.InterviewRound),
		Resumes: resumes, Error: errMsg}
	for _, e := range events {
		label := feed.StageLabel(e.To, e.InterviewRound)
		if e.From != nil {
			label = store.StageLabels[*e.From] + " → " + label
		}
		page.Events = append(page.Events, EventRow{At: e.At, Label: label})
	}
	for _, st := range []store.Stage{store.StageSaved, store.StageApplied, store.StageOA, store.StageInterview,
		store.StageOffer, store.StageRejected, store.StageWithdrawn} {
		page.Stages = append(page.Stages, StageOption{string(st), store.StageLabels[st], st == app.Stage})
	}
	s.render(w, status, "application.html", page)
}

// moveStage handles the stage buttons ("Interview" means the next round).
func (s *Server) moveStage(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	stage := r.PostFormValue("stage")
	if !ok || !store.ValidStage(stage) {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	err := s.tracker.MoveStage(r.Context(), u.ID, id, store.Stage(stage), nil, time.Now().In(s.location(r)))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d", id), http.StatusSeeOther)
}

// updateApplication saves the details form. A new resume label typed in
// the form is created (or reused, if you already have that label).
func (s *Server) updateApplication(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := store.Details{Notes: strings.TrimSpace(r.PostFormValue("notes"))}

	if v := r.PostFormValue("applied_on"); v != "" {
		// A date typed in a form is a day on the user's calendar, so read it
		// in their timezone: in UTC, "tomorrow" may already have begun tonight.
		t, err := time.ParseInLocation("2006-01-02", v, s.location(r))
		if err != nil || t.After(time.Now()) {
			s.showApplication(w, r, http.StatusUnprocessableEntity, "The applied date must be a real date, not in the future.")
			return
		}
		d.AppliedOn = &t
	}
	if v := r.PostFormValue("expected_rounds"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 20 {
			s.showApplication(w, r, http.StatusUnprocessableEntity, "Expected rounds must be a number from 1 to 20.")
			return
		}
		d.ExpectedRounds = &n
	}
	if label := strings.TrimSpace(r.PostFormValue("new_resume")); label != "" {
		rid, err := s.tracker.AddResumeVersion(r.Context(), u.ID, label)
		if err != nil {
			serverError(w, err)
			return
		}
		d.ResumeVersionID = &rid
	} else if v := r.PostFormValue("resume_version_id"); v != "" {
		rid, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "Bad request.", http.StatusBadRequest)
			return
		}
		d.ResumeVersionID = &rid // the database refuses another user's resume
	}

	err := s.tracker.UpdateDetails(r.Context(), u.ID, id, d)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d", id), http.StatusSeeOther)
}

func (s *Server) deleteApplication(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.tracker.DeleteApplication(r.Context(), u.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/applications", http.StatusSeeOther)
}
