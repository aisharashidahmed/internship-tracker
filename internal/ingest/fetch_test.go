package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
