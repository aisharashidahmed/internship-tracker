package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"internship-tracker/internal/listing"
)

// A fake server that behaves like GitHub: 304 if you send the current ETag.
func TestFetchSendsETagAndHandles304(t *testing.T) {
	const etag = `"v1"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	ctx := context.Background()

	first, err := fetch(ctx, srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.NotModified || string(first.Body) != "[]" || first.ETag != etag {
		t.Fatalf("first fetch = %+v, want body [] and etag %s", first, etag)
	}

	second, err := fetch(ctx, srv.Client(), srv.URL, first.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified {
		t.Errorf("second fetch with matching ETag should be NotModified, got %+v", second)
	}
}

func TestFetchRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	if _, err := fetch(context.Background(), srv.Client(), srv.URL, ""); err == nil {
		t.Error("expected an error for HTTP 429")
	}
}

func TestKeepYearKeepsUnknownSeason(t *testing.T) {
	got := keepYear([]listing.Posting{
		{SourceListingID: "a", Season: "Summer 2027"},
		{SourceListingID: "b", Season: "Summer 2026"},
		{SourceListingID: "c", Season: ""}, // SpeedyApply title without a season
	}, "2027")
	if len(got) != 2 || got[0].SourceListingID != "a" || got[1].SourceListingID != "c" {
		t.Errorf("keepYear kept %+v, want a and c", got)
	}
}
