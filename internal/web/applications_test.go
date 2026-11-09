package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

// fakeTracker keeps applications in memory. The real rules (uniqueness,
// history, ownership in SQL) are tested against Postgres in package store;
// these tests check the pages and buttons.
type fakeTracker struct {
	mu     sync.Mutex
	apps   map[int64]*store.Application
	owner  map[int64]int64 // app id -> user id
	events map[int64][]store.Event
	nextID int64
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{apps: map[int64]*store.Application{}, owner: map[int64]int64{}, events: map[int64][]store.Event{}}
}

func (f *fakeTracker) StartApplication(_ context.Context, uid, lid int64, stage store.Stage, on time.Time) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, a := range f.apps {
		if a.ListingID == lid && f.owner[id] == uid {
			return id, false, nil
		}
	}
	f.nextID++
	f.apps[f.nextID] = &store.Application{ID: f.nextID, ListingID: lid, Title: fmt.Sprintf("Listing %d", lid), Company: "Acme", Stage: stage}
	f.owner[f.nextID] = uid
	f.events[f.nextID] = []store.Event{{To: stage, At: time.Now()}}
	return f.nextID, true, nil
}

func (f *fakeTracker) get(uid, id int64) (*store.Application, error) {
	a, ok := f.apps[id]
	if !ok || f.owner[id] != uid {
		return nil, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeTracker) MoveStage(_ context.Context, uid, id int64, to store.Stage, _ *int, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.get(uid, id)
	if err != nil {
		return err
	}
	from := a.Stage
	a.Stage = to
	f.events[id] = append(f.events[id], store.Event{From: &from, To: to, At: time.Now()})
	return nil
}

func (f *fakeTracker) UpdateDetails(_ context.Context, uid, id int64, d store.Details) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.get(uid, id)
	if err != nil {
		return err
	}
	a.AppliedOn, a.ResumeVersionID, a.ExpectedRounds, a.Notes = d.AppliedOn, d.ResumeVersionID, d.ExpectedRounds, d.Notes
	return nil
}

func (f *fakeTracker) DeleteApplication(_ context.Context, uid, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(uid, id); err != nil {
		return err
	}
	delete(f.apps, id)
	return nil
}

func (f *fakeTracker) Applications(_ context.Context, uid int64) ([]store.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Application
	for id, a := range f.apps {
		if f.owner[id] == uid {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeTracker) Application(_ context.Context, uid, id int64) (store.Application, []store.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.get(uid, id)
	if err != nil {
		return store.Application{}, nil, err
	}
	return *a, f.events[id], nil
}

func (f *fakeTracker) ResumeVersions(context.Context, int64) ([]store.ResumeVersion, error) {
	return []store.ResumeVersion{{ID: 1, Label: "Backend v2"}}, nil
}

func (f *fakeTracker) AddResumeVersion(context.Context, int64, string) (int64, error) { return 2, nil }

// signedIn returns a browser logged in as a new user.
func signedIn(t *testing.T, email string, accounts *fakeAccounts, tracker *fakeTracker) *browser {
	t.Helper()
	s, err := New(fakeFeed{listings: listings(2)}, accounts, tracker, newFakeStars(), newFakeImports(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowserFor(t, s)
	accounts.invites["inv-"+email] = 1
	b.post("/signup", url.Values{"invite": {"inv-" + email}, "email": {email}, "password": {"correct horse battery"}})
	return b
}

func TestTracker_FromCardToHistory(t *testing.T) {
	accounts, tracker := newFakeAccounts(), newFakeTracker()
	b := signedIn(t, "a@college.edu", accounts, tracker)

	if _, page := b.get("/"); !strings.Contains(page, ">Save</button>") || !strings.Contains(page, ">I applied</button>") {
		t.Fatal("logged-in feed cards should have Save and I applied buttons")
	}

	// Save returns to where you were; I applied opens the application.
	status, _ := b.post("/applications", url.Values{"listing_id": {"1"}, "stage": {"saved"}, "back": {"/?type=internship&role=ml"}})
	if status != http.StatusSeeOther {
		t.Fatalf("save: status %d", status)
	}
	res, err := b.client.PostForm(b.srv.URL+"/applications", url.Values{"listing_id": {"2"}, "stage": {"applied"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); loc != "/applications/2" {
		t.Errorf("I applied went to %q, want the application page", loc)
	}

	_, page := b.get("/applications")
	if !strings.Contains(page, "Applied <span class=\"count\">1</span>") || !strings.Contains(page, "Saved <span class=\"count\">1</span>") {
		t.Error("My applications should group by stage")
	}

	b.post("/applications/2/stage", url.Values{"stage": {"interview"}})
	_, page = b.get("/applications/2")
	if !strings.Contains(page, "Applied → Interview") || !strings.Contains(page, "Next interview round") {
		t.Error("the history should show the move, and the Interview button should offer the next round")
	}
}

func TestTracker_OtherUsersGet404(t *testing.T) {
	accounts, tracker := newFakeAccounts(), newFakeTracker()
	owner := signedIn(t, "owner@college.edu", accounts, tracker)
	owner.post("/applications", url.Values{"listing_id": {"1"}, "stage": {"saved"}})

	stranger := signedIn(t, "stranger@college.edu", accounts, tracker)
	if status, _ := stranger.get("/applications/1"); status != http.StatusNotFound {
		t.Errorf("stranger viewing: status %d, want 404", status)
	}
	if status, _ := stranger.post("/applications/1/stage", url.Values{"stage": {"offer"}}); status != http.StatusNotFound {
		t.Errorf("stranger moving: status %d, want 404", status)
	}
}

func TestTracker_RejectsBadDetails(t *testing.T) {
	accounts, tracker := newFakeAccounts(), newFakeTracker()
	b := signedIn(t, "a@college.edu", accounts, tracker)
	b.post("/applications", url.Values{"listing_id": {"1"}, "stage": {"applied"}})

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if status, page := b.post("/applications/1", url.Values{"applied_on": {tomorrow}}); status != http.StatusUnprocessableEntity || !strings.Contains(page, "not in the future") {
		t.Errorf("future date: status %d", status)
	}
	if status, _ := b.post("/applications/1", url.Values{"expected_rounds": {"0"}}); status != http.StatusUnprocessableEntity {
		t.Errorf("0 rounds: status %d", status)
	}
	if status, _ := b.post("/applications/1/stage", url.Values{"stage": {"ghosted"}}); status != http.StatusBadRequest {
		t.Errorf("unknown stage: status %d", status)
	}
}

func TestTracker_LoggedOutIsSentToLogin(t *testing.T) {
	b := newBrowser(t, newFakeAccounts())
	res, err := b.client.Get(b.srv.URL + "/applications")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Errorf("status %d to %q, want a redirect to /login", res.StatusCode, res.Header.Get("Location"))
	}
	if _, page := b.get("/"); strings.Contains(page, "I applied") {
		t.Error("logged-out cards should have no tracker buttons")
	}
}

func TestTimezone_FromBrowserAndShownInIt(t *testing.T) {
	accounts, tracker := newFakeAccounts(), newFakeTracker()
	s, err := New(fakeFeed{listings: listings(1)}, accounts, tracker, newFakeStars(), newFakeImports(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowserFor(t, s)
	accounts.invites["inv"] = 2
	b.post("/signup", url.Values{"invite": {"inv"}, "email": {"tokyo@college.edu"}, "password": {"correct horse battery"},
		"tz": {"Asia/Tokyo"}})
	if u, _, _ := accounts.UserByEmail(context.Background(), "tokyo@college.edu"); u.Timezone != "Asia/Tokyo" {
		t.Fatalf("saved timezone %q, want the browser's Asia/Tokyo", u.Timezone)
	}

	// A history entry at 15:00 UTC is midnight the next day in Tokyo.
	b.post("/applications", url.Values{"listing_id": {"1"}, "stage": {"saved"}})
	tracker.events[1][0].At = time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	if _, page := b.get("/applications/1"); !strings.Contains(page, "Nov 9, 12:00 AM") {
		t.Error("history time not shown in the user's timezone")
	}

	// A junk timezone falls back to the default, not to the server's zone.
	other := newBrowserFor(t, s)
	other.post("/signup", url.Values{"invite": {"inv"}, "email": {"x@college.edu"}, "password": {"correct horse battery"},
		"tz": {"Local"}})
	if u, _, _ := accounts.UserByEmail(context.Background(), "x@college.edu"); u.Timezone != "America/New_York" {
		t.Errorf("timezone %q, want the default America/New_York", u.Timezone)
	}
}

func TestTracker_ShowsWhyAListingClosed(t *testing.T) {
	accounts, tracker := newFakeAccounts(), newFakeTracker()
	b := signedIn(t, "a@college.edu", accounts, tracker)
	b.post("/applications", url.Values{"listing_id": {"1"}, "stage": {"applied"}})
	b.post("/applications", url.Values{"listing_id": {"2"}, "stage": {"applied"}})
	tracker.apps[1].ListingStatus, tracker.apps[1].ClosedReason = "closed", "link"
	tracker.apps[2].ListingStatus, tracker.apps[2].ClosedReason = "closed", "source"

	_, page := b.get("/applications")
	if !strings.Contains(page, "Closed · link no longer works") || !strings.Contains(page, "Closed · source marked closed") {
		t.Error("My applications should say why each listing closed")
	}
	if _, page := b.get("/applications/1"); !strings.Contains(page, "Closed · link no longer works") {
		t.Error("the application page should say why its listing closed")
	}
}
