package linkcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

type fakeStore struct {
	due     []store.DueLink
	mu      sync.Mutex
	results map[int64]store.LinkResult
}

func (f *fakeStore) DueLinks(context.Context, int) ([]store.DueLink, error) { return f.due, nil }

func (f *fakeStore) RecordLinkCheck(_ context.Context, id int64, r store.LinkResult, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[id] = r
	return r == store.LinkDead, nil
}

func TestChecker_PoliteAndCorrect(t *testing.T) {
	var mu sync.Mutex
	var hits []time.Time
	var agents []string
	robotsFetches := 0

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		agents = append(agents, r.UserAgent())
		mu.Unlock()
		switch r.URL.Path {
		case "/robots.txt":
			robotsFetches++
			w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/jobs/11111":
			w.WriteHeader(http.StatusOK)
		case "/jobs/22222":
			http.NotFound(w, r)
		case "/jobs/33333": // same-site redirect to a live page
			http.Redirect(w, r, "/careers/33333", http.StatusFound)
		case "/careers/33333":
			w.WriteHeader(http.StatusOK)
		case "/jobs/44444":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("fetched %s, which robots.txt disallows or nobody asked for", r.URL.Path)
		}
	}))
	defer site.Close()

	fs := &fakeStore{results: map[int64]store.LinkResult{}, due: []store.DueLink{
		{PostingID: 1, URL: site.URL + "/jobs/11111"},
		{PostingID: 2, URL: site.URL + "/jobs/22222"},
		{PostingID: 3, URL: site.URL + "/jobs/33333"},
		{PostingID: 4, URL: site.URL + "/jobs/44444"},
		{PostingID: 5, URL: site.URL + "/private/55555"},
	}}
	const gap = 40 * time.Millisecond
	c := &Checker{Store: fs, Client: NewClient(), UserAgent: "test-agent", PerHost: gap, Sites: 4}

	sum, err := c.Run(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}

	want := map[int64]store.LinkResult{1: store.LinkAlive, 2: store.LinkDead, 3: store.LinkAlive, 4: store.LinkUnknown, 5: store.LinkUnknown}
	for id, r := range want {
		if fs.results[id] != r {
			t.Errorf("posting %d: %q, want %q", id, fs.results[id], r)
		}
	}
	if sum.Checked != 5 || sum.Blocked != 1 || sum.Dead != 1 || sum.ClosedListings != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if robotsFetches != 1 {
		t.Errorf("robots.txt fetched %d times, want once per site", robotsFetches)
	}
	for i := 1; i < len(hits); i++ {
		if d := hits[i].Sub(hits[i-1]); d < gap-5*time.Millisecond {
			t.Errorf("requests %d and %d were %s apart, want at least %s", i-1, i, d, gap)
		}
	}
	for _, a := range agents {
		if a != "test-agent" {
			t.Errorf("request sent user agent %q", a)
		}
	}
}

func TestChecker_UnreadableRobotsBlocksForNow(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("fetched %s although robots.txt couldn't be read", r.URL.Path)
	}))
	defer site.Close()
	fs := &fakeStore{results: map[int64]store.LinkResult{}, due: []store.DueLink{{PostingID: 1, URL: site.URL + "/jobs/11111"}}}
	c := &Checker{Store: fs, Client: NewClient(), UserAgent: "test-agent", PerHost: time.Millisecond, Sites: 1}
	if _, err := c.Run(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if fs.results[1] != store.LinkUnknown {
		t.Errorf("result %q, want unknown (not dead) while robots.txt is unreadable", fs.results[1])
	}
}
