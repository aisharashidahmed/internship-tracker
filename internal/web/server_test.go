package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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

func (f fakeFeed) Feed(_ context.Context, q store.FeedQuery) ([]store.FeedListing, error) {
	if f.err != nil {
		return nil, f.err
	}
	if q.JobType != listing.Internship {
		return nil, nil
	}
	limit, offset := q.Limit, q.Offset
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
	s, err := New(f, newFakeAccounts(), Options{})
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
	for _, want := range []string{"AI R&amp;D Engineer Co-op", "Sunnyvale", "posted 3 days ago", "May be closed", "first seen today"} {
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
	if p.Filters.JobType != listing.Internship || len(p.Cards) != 2 {
		t.Errorf("default tab: %s with %d cards, want internship with 2", p.Filters.JobType, len(p.Cards))
	}

	_, body = get(t, f, "/api/listings?type=new_grad")
	p = Page{}
	json.Unmarshal([]byte(body), &p)
	if p.Filters.JobType != listing.NewGrad || len(p.Cards) != 0 {
		t.Errorf("new grad tab: %s with %d cards, want new_grad with 0", p.Filters.JobType, len(p.Cards))
	}

	_, html := get(t, f, "/?type=new_grad&page=1")
	if !strings.Contains(html, `value="new_grad" checked`) {
		t.Error("New grad is not selected on its own page")
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

// fakeRecorder remembers the query the server asked for.
type fakeRecorder struct{ got *store.FeedQuery }

func (f fakeRecorder) Feed(_ context.Context, q store.FeedQuery) ([]store.FeedListing, error) {
	*f.got = q
	return nil, nil
}

func TestFilters_FromURL(t *testing.T) {
	var q store.FeedQuery
	_, html := get(t, fakeRecorder{&q}, "/?type=new_grad&role=backend&role=ml&role=nonsense&role=ml&match=all&industry=healthcare&industry=crypto&page=2")

	if q.JobType != listing.NewGrad || !q.MatchAllRoles || q.Offset != PageSize {
		t.Errorf("query = %+v", q)
	}
	if !slices.Equal(q.Roles, []listing.Role{listing.Backend, listing.ML}) {
		t.Errorf("roles = %v, want [backend ml] (unknown and repeated values dropped)", q.Roles)
	}
	if !slices.Equal(q.Industries, []listing.Industry{listing.Healthcare}) {
		t.Errorf("industries = %v, want [healthcare]", q.Industries)
	}
	for _, want := range []string{`name="role" value="backend" checked`, `name="match" value="all" checked`,
		`name="industry" value="healthcare" checked`, "No listings match these filters", `href="?type=new_grad"`} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %s", want)
		}
	}
}

func TestFilters_PageLinksKeepFilters(t *testing.T) {
	f := Filters{JobType: listing.Internship, Roles: []listing.Role{listing.ML}, Industries: []listing.Industry{listing.Healthcare}}
	if got := f.query(3); got != "?industry=healthcare&page=3&role=ml&type=internship" {
		t.Errorf("query(3) = %s", got)
	}
}
