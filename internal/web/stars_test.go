package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

// fakeStars keeps stars and notifications in memory. The real rules (one
// ping per role, only after starring, job type) are tested in package store.
type fakeStars struct {
	mu      sync.Mutex
	starred map[[2]int64]bool // (user, company)
	jobType map[int64]string
	notes   map[int64][]store.Notification
}

func newFakeStars() *fakeStars {
	return &fakeStars{starred: map[[2]int64]bool{}, jobType: map[int64]string{}, notes: map[int64][]store.Notification{}}
}

func (f *fakeStars) Star(_ context.Context, u, c int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starred[[2]int64{u, c}] = true
	return nil
}
func (f *fakeStars) Unstar(_ context.Context, u, c int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.starred, [2]int64{u, c})
	return nil
}
func (f *fakeStars) StarredCompanies(_ context.Context, u int64) ([]store.StarredCompany, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.StarredCompany
	for k := range f.starred {
		if k[0] == u {
			out = append(out, store.StarredCompany{ID: k[1], Name: "Google", OpenRoles: 3})
		}
	}
	return out, nil
}
func (f *fakeStars) NotifyJobType(_ context.Context, u int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.jobType[u]; ok {
		return t, nil
	}
	return store.NotifyInternship, nil
}
func (f *fakeStars) SetNotifyJobType(_ context.Context, u int64, t string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobType[u] = t
	return nil
}
func (f *fakeStars) Notifications(_ context.Context, u int64, limit int) ([]store.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notes[u], nil
}
func (f *fakeStars) UnreadCount(_ context.Context, u int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.notes[u] {
		if !x.Read {
			n++
		}
	}
	return n, nil
}
func (f *fakeStars) OpenNotification(_ context.Context, u, id int64) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.notes[u] {
		if x.ID == id {
			f.notes[u][i].Read = true
			return x.Company, x.Title, nil
		}
	}
	return "", "", store.ErrNotFound
}
func (f *fakeStars) MarkAllNotificationsRead(_ context.Context, u int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.notes[u] {
		f.notes[u][i].Read = true
	}
	return nil
}

func TestStarsAndBell(t *testing.T) {
	accounts, tracker, stars := newFakeAccounts(), newFakeTracker(), newFakeStars()
	s, err := New(fakeFeed{listings: listings(1)}, accounts, tracker, stars, newFakeImports(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowserFor(t, s)
	b.post("/signup", url.Values{"email": {"a@college.edu"}, "password": {"correct horse battery"}})

	// A card offers ☆; starring returns to where you were.
	if _, page := b.get("/"); !strings.Contains(page, `aria-label="Star Nokia"`) || !strings.Contains(page, "☆ Starred by you") {
		t.Fatal("logged-in cards need a star button, and the Tier group a Starred filter")
	}
	status, _ := b.post("/stars", url.Values{"company_id": {"9"}, "star": {"1"}, "back": {"/?type=internship"}})
	if status != http.StatusSeeOther || !stars.starred[[2]int64{1, 9}] {
		t.Fatalf("star: status %d, starred=%v", status, stars.starred)
	}

	// The bell: empty, then two unread.
	if _, page := b.get("/"); !strings.Contains(page, "No notifications yet") {
		t.Error("an empty bell should say how to get notifications")
	}
	stars.notes[1] = []store.Notification{
		{ID: 1, Company: "Google", Title: "SWE Intern", Season: "Summer 2027", CreatedAt: time.Now()},
		{ID: 2, Company: "Google", Title: "Data Intern", CreatedAt: time.Now()},
	}
	if _, page := b.get("/"); !strings.Contains(page, `<span class="bell-count">2</span>`) || !strings.Contains(page, "New role at Google") {
		t.Error("the bell should show 2 unread and list them")
	}

	// Opening one marks it read and shows that company's roles.
	res, err := b.client.Get(b.srv.URL + "/notifications/1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); loc != "/?q=Google" || !stars.notes[1][0].Read {
		t.Errorf("open: went to %q, read=%v", loc, stars.notes[1][0].Read)
	}
	b.post("/notifications/read", url.Values{"back": {"/"}})
	if _, page := b.get("/"); strings.Contains(page, `class="bell-count"`) {
		t.Error("after Mark all read, no count")
	}

	// Settings.
	b.post("/stars/settings", url.Values{"notify_job_type": {"both"}})
	if _, page := b.get("/stars"); !strings.Contains(page, `value="both">Both`) || stars.jobType[1] != "both" {
		t.Error("the setting should be saved and shown")
	}
	if status, _ := b.post("/stars/settings", url.Values{"notify_job_type": {"everything"}}); status != http.StatusBadRequest {
		t.Errorf("a made-up setting: status %d, want 400", status)
	}
}

func TestStarredFilterNeedsLogin(t *testing.T) {
	var q store.FeedQuery
	get(t, fakeRecorder{&q}, "/?starred=1")
	if q.StarredOnly {
		t.Error("logged out, ?starred=1 must do nothing")
	}
	if _, page := get(t, fakeFeed{listings: listings(1)}, "/"); strings.Contains(page, "Starred by you") || strings.Contains(page, `class="star`) {
		t.Error("logged-out visitors shouldn't see star controls")
	}
}
