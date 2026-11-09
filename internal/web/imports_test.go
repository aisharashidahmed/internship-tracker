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

type fakeImports struct {
	mu      sync.Mutex
	items   map[int64][]store.Import
	queued  []string
	saved   []store.ImportEdit
	shared  []int64
	outcome [2]string
}

func newFakeImports() *fakeImports { return &fakeImports{items: map[int64][]store.Import{}} }

func (f *fakeImports) EnqueueImports(_ context.Context, u int64, urls []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queued)+len(urls) > store.DailyImportLimit {
		return 0, store.ErrImportLimit
	}
	f.queued = append(f.queued, urls...)
	return len(urls), nil
}
func (f *fakeImports) Imports(_ context.Context, u int64) ([]store.Import, error) {
	return f.items[u], nil
}
func (f *fakeImports) DismissImport(context.Context, int64, int64) error { return nil }
func (f *fakeImports) SaveImport(_ context.Context, u, id int64, e store.ImportEdit, _ time.Time) (int64, error) {
	f.saved = append(f.saved, e)
	return 77, nil
}
func (f *fakeImports) ImportStats(context.Context, time.Time) (store.ImportStats, error) {
	return store.ImportStats{Match: 1, API: 2, JSONLD: 1, Form: 0, Pending: 3}, nil
}
func (f *fakeImports) Share(_ context.Context, u, l int64) (string, string, error) {
	f.shared = append(f.shared, l)
	return "private", "Verify your email to share.", nil
}
func (f *fakeImports) LastShare(context.Context, int64, int64) (string, string, error) {
	return f.outcome[0], f.outcome[1], nil
}

func TestImportsPage(t *testing.T) {
	accounts, imports := newFakeAccounts(), newFakeImports()
	s, err := New(fakeFeed{}, accounts, newFakeTracker(), newFakeStars(), imports, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowserFor(t, s)
	b.post("/signup", url.Values{"email": {"a@college.edu"}, "password": {"correct horse battery"}})

	// Unsafe links are refused right away, before anything is queued.
	for _, bad := range []string{"http://169.254.169.254/latest/meta-data/", "http://localhost:8080/admin", "file:///etc/passwd"} {
		status, page := b.post("/applications/add", url.Values{"links": {"https://jobs.lever.co/a/x\n" + bad}})
		if status != http.StatusUnprocessableEntity || !strings.Contains(page, `role="alert"`) {
			t.Errorf("%s: status %d, want 422 with an error shown", bad, status)
		}
	}
	if len(imports.queued) != 0 {
		t.Fatalf("queued %v; a batch with a bad link must queue nothing", imports.queued)
	}

	// Good links go to the queue (duplicates in one paste count once).
	status, _ := b.post("/applications/add", url.Values{"links": {"https://jobs.lever.co/a/1\n https://jobs.lever.co/a/1 \nhttps://boards.greenhouse.io/b/jobs/2"}})
	if status != http.StatusSeeOther || len(imports.queued) != 2 {
		t.Errorf("queued %v (status %d), want 2 links", imports.queued, status)
	}

	// A pending link auto-refreshes; a ready one shows a form saying where it came from.
	imports.items[1] = []store.Import{
		{ID: 1, URL: "https://jobs.lever.co/a/1", Status: "running"},
		{ID: 2, URL: "https://boards.greenhouse.io/b/jobs/2", Status: "ready", Step: "api", Company: "B", Title: "SWE Intern", JobType: "internship"},
	}
	_, page := b.get("/applications/add")
	for _, want := range []string{`http-equiv="refresh"`, "Looking this up", "From the job board", `value="SWE Intern"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}

	// Saving needs a company and title; a good save goes to the application.
	if status, _ := b.post("/imports/2", url.Values{"company": {" "}, "title": {"x"}}); status != http.StatusUnprocessableEntity {
		t.Errorf("empty company: status %d", status)
	}
	res, err := b.client.PostForm(b.srv.URL+"/imports/2", url.Values{"company": {"B"}, "title": {"SWE Intern"}, "season": {"Summer 2027"}, "job_type": {"internship"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/applications/77" || len(imports.saved) != 1 || imports.saved[0].Season != "Summer 2027" {
		t.Errorf("save went to %q, saved %+v", res.Header.Get("Location"), imports.saved)
	}
}

func TestImportStatsEndpoint(t *testing.T) {
	_, body := get(t, fakeFeed{}, "/api/import-stats?days=7")
	for _, want := range []string{`"finished":4`, `"api":2`, `"api":50`, `"pending":3`} {
		if !strings.Contains(body, want) {
			t.Errorf("stats missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "http") {
		t.Error("stats must be aggregate: no links")
	}
}
