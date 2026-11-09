package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/store"
)

// fakeFeed returns n listings, like a database holding that many.
type fakeFeed struct {
	listings []store.FeedListing // all internships
	err      error
}

func (f fakeFeed) Feed(_ context.Context, jobType listing.JobType, limit, offset int) ([]store.FeedListing, error) {
	if f.err != nil {
		return nil, f.err
	}
	if jobType != listing.Internship {
		return nil, nil
	}
	if offset >= len(f.listings) {
		return nil, nil
	}
	return f.listings[offset:min(offset+limit, len(f.listings))], nil
}

func listings(n int) []store.FeedListing {
	posted := time.Now().Add(-72 * time.Hour)
	var out []store.FeedListing
	for i := range n {
		link := fmt.Sprintf("workday:nokia:R%d", i)
		out = append(out, store.FeedListing{
			ID: int64(i + 1), Company: "Nokia", Title: "AI R&D Engineer Co-op", Status: store.StatusOpen,
			PostedAt: &posted, FirstSeenAt: time.Now(),
			Postings: []store.FeedPosting{{Source: "simplify", ApplyURL: "https://nokia.example/" + link,
				JobLink: &link, Locations: []string{"Sunnyvale, CA"}, Active: true}},
		})
	}
	return out
}

func get(t *testing.T, f FeedReader, path string) (*http.Response, string) {
	t.Helper()
	s, err := New(f)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

func TestFeedJSON_Pages(t *testing.T) {
	f := fakeFeed{listings: listings(PageSize + 3)}

	_, body := get(t, f, "/api/listings")
	var p Page
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Cards) != PageSize || p.NextPage != 2 {
		t.Errorf("page 1: %d cards, next %d; want %d and 2", len(p.Cards), p.NextPage, PageSize)
	}

	_, body = get(t, f, "/api/listings?page=2")
	p = Page{}
	json.Unmarshal([]byte(body), &p)
	if len(p.Cards) != 3 || p.NextPage != 0 {
		t.Errorf("page 2: %d cards, next %d; want 3 and none", len(p.Cards), p.NextPage)
	}
}

func TestFeedPage_ShowsCards(t *testing.T) {
	f := fakeFeed{listings: listings(1)}
	stale := f.listings[0]
	stale.ID, stale.Status, stale.PostedAt = 2, store.StatusStale, nil
	f.listings = append(f.listings, stale)

	res, body := get(t, f, "/")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	for _, want := range []string{"AI R&amp;D Engineer Co-op", "Sunnyvale", "posted 3 days ago", "may be closed", "first seen today"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestFeed_Toggle(t *testing.T) {
	f := fakeFeed{listings: listings(2)}

	_, body := get(t, f, "/api/listings") // no ?type: Internships
	var p Page
	json.Unmarshal([]byte(body), &p)
	if p.JobType != listing.Internship || len(p.Cards) != 2 {
		t.Errorf("default tab: %s with %d cards, want internship with 2", p.JobType, len(p.Cards))
	}

	_, body = get(t, f, "/api/listings?type=new_grad")
	p = Page{}
	json.Unmarshal([]byte(body), &p)
	if p.JobType != listing.NewGrad || len(p.Cards) != 0 {
		t.Errorf("new grad tab: %s with %d cards, want new_grad with 0", p.JobType, len(p.Cards))
	}

	_, html := get(t, f, "/?type=new_grad&page=1")
	if !strings.Contains(html, `href="?type=new_grad" class="on"`) {
		t.Error("New grad tab is not highlighted on its own page")
	}
}

func TestFeed_ErrorDoesNotLeakDetails(t *testing.T) {
	res, body := get(t, fakeFeed{err: errors.New("pq: password authentication failed")}, "/api/listings")
	if res.StatusCode != 500 || strings.Contains(body, "password") {
		t.Errorf("status %d body %q; want 500 with a generic message", res.StatusCode, body)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{0: "today", 30 * time.Hour: "1 day ago", 5 * 24 * time.Hour: "5 days ago"} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(-%v) = %q, want %q", d, got, want)
		}
	}
}
