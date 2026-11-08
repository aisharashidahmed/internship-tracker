package ingest

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// maxBody guards against a runaway download (Simplify's file is ~13 MB).
const maxBody = 100 << 20 // 100 MB

// fetchResult is what one conditional GET returned.
type fetchResult struct {
	NotModified bool // the server said "same as your ETag", so there's no body
	Body        []byte
	ETag        string // send this next time
}

// fetch downloads url. If etag is set, it sends If-None-Match, and the server
// answers 304 Not Modified (no body) when the file hasn't changed.
func fetch(ctx context.Context, client *http.Client, url, etag string) (fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fetchResult{}, err
	}
	req.Header.Set("User-Agent", "internship-tracker (personal project)")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fetchResult{}, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return fetchResult{NotModified: true, ETag: etag}, nil
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return fetchResult{}, fmt.Errorf("read %s: %w", url, err)
		}
		return fetchResult{Body: body, ETag: resp.Header.Get("ETag")}, nil
	default:
		return fetchResult{}, fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}
}
