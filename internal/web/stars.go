package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"internship-tracker/internal/store"
)

// Stars is what starring and notifications need. *store.Store satisfies it.
type Stars interface {
	Star(ctx context.Context, userID, companyID int64) error
	Unstar(ctx context.Context, userID, companyID int64) error
	StarredCompanies(ctx context.Context, userID int64) ([]store.StarredCompany, error)
	NotifyJobType(ctx context.Context, userID int64) (string, error)
	SetNotifyJobType(ctx context.Context, userID int64, jobType string) error
	Notifications(ctx context.Context, userID int64, limit int) ([]store.Notification, error)
	UnreadCount(ctx context.Context, userID int64) (int, error)
	OpenNotification(ctx context.Context, userID, id int64) (company, title string, err error)
	MarkAllNotificationsRead(ctx context.Context, userID int64) error
}

// Nav is what the header shows a logged-in user: the bell.
type Nav struct {
	Unread int
	Items  []store.Notification
}

// bellSize is how many notifications the bell's dropdown lists.
const bellSize = 8

// nav loads the bell for the header, or nil when logged out. A failure
// only hides the bell; it never breaks the page.
func (s *Server) nav(r *http.Request) *Nav {
	u := currentUser(r)
	if u == nil {
		return nil
	}
	unread, err := s.stars.UnreadCount(r.Context(), u.ID)
	if err != nil {
		log.Printf("bell: %v", err)
		return nil
	}
	items, err := s.stars.Notifications(r.Context(), u.ID, bellSize)
	if err != nil {
		log.Printf("bell: %v", err)
		return nil
	}
	return &Nav{Unread: unread, Items: items}
}

// toggleStar handles the ☆ / ★ button on a card.
func (s *Server) toggleStar(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	companyID, err := strconv.ParseInt(r.PostFormValue("company_id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("star") == "1" {
		err = s.stars.Star(r.Context(), u.ID, companyID)
	} else {
		err = s.stars.Unstar(r.Context(), u.ID, companyID)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, localBack(r, "/"), http.StatusSeeOther)
}

// StarsPage is the data for "Starred companies".
type StarsPage struct {
	User      *store.User
	Nav       *Nav
	Companies []store.StarredCompany
	JobType   string
	Options   []Option
}

func (s *Server) starsPage(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	companies, err := s.stars.StarredCompanies(r.Context(), u.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	jt, err := s.stars.NotifyJobType(r.Context(), u.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	var opts []Option
	for _, o := range []Option{{Value: store.NotifyInternship, Label: "Internships"}, {Value: store.NotifyNewGrad, Label: "New grad"}, {Value: store.NotifyBoth, Label: "Both"}} {
		o.On = o.Value == jt
		opts = append(opts, o)
	}
	s.render(w, http.StatusOK, "stars.html", StarsPage{User: u, Nav: s.nav(r), Companies: companies, JobType: jt, Options: opts})
}

func (s *Server) saveNotifySettings(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	jt := r.PostFormValue("notify_job_type")
	if jt != store.NotifyInternship && jt != store.NotifyNewGrad && jt != store.NotifyBoth {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if err := s.stars.SetNotifyJobType(r.Context(), u.ID, jt); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/stars", http.StatusSeeOther)
}

// openNotification marks a notification read and shows the company's roles,
// newest first, so the new one is at the top.
func (s *Server) openNotification(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	company, _, err := s.stars.OpenNotification(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/?q=%s", url.QueryEscape(company)), http.StatusSeeOther)
}

func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	if err := s.stars.MarkAllNotificationsRead(r.Context(), u.ID); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, localBack(r, "/"), http.StatusSeeOther)
}
