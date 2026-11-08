// Package ingest runs one source end to end:
//
//	fetch (skip if unchanged) -> adapter.Parse -> keep target year -> group -> save
//
// It knows the order of the steps but none of the details: parsing lives in
// the adapters, matching rules in package listing, SQL in package store.
package ingest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"internship-tracker/internal/listing"
	"internship-tracker/internal/source"
	"internship-tracker/internal/store"
)

// Ingester holds what every run needs.
type Ingester struct {
	Store  *store.Store
	Client *http.Client
	Year   string // only seasons ending in this year are kept, e.g. "2027"
}

// Options change a single run.
type Options struct {
	// Force downloads and re-saves even if the source says nothing changed.
	Force bool
}

// Report summarizes one run.
type Report struct {
	Source      string
	NotModified bool // skipped: the file hasn't changed since the last fetch
	Postings    int  // postings for the target year after parsing
	Counts      map[store.Outcome]int
	Took        time.Duration
}

// Run ingests one source. All writes happen in one transaction, so a failed
// run leaves the database (and the saved ETag) exactly as it was.
func (in *Ingester) Run(ctx context.Context, adapter source.Adapter, opts Options) (Report, error) {
	start := time.Now()
	report := Report{Source: adapter.Name(), Counts: map[store.Outcome]int{}}

	src, err := in.Store.SourceByName(ctx, adapter.Name())
	if err != nil {
		return report, err
	}

	etag := src.ETag
	if opts.Force {
		etag = ""
	}
	res, err := fetch(ctx, in.Client, src.URL, etag)
	if err != nil {
		return report, err
	}
	if res.NotModified {
		report.NotModified = true
		report.Took = time.Since(start)
		return report, in.Store.MarkFetched(ctx, src.ID, res.ETag)
	}

	postings, err := adapter.Parse(res.Body)
	if err != nil {
		return report, err
	}
	postings = keepYear(postings, in.Year)
	report.Postings = len(postings)

	err = in.Store.InTx(ctx, func(tx *store.Store) error {
		for _, l := range listing.Group(postings) {
			outcome, err := tx.SaveListing(ctx, src.ID, l)
			if err != nil {
				return err
			}
			report.Counts[outcome]++
		}
		// Save the ETag in the same transaction, so we only skip next time
		// if this run's data was actually stored.
		return tx.MarkFetched(ctx, src.ID, res.ETag)
	})
	if err != nil {
		return report, fmt.Errorf("ingest %s: %w", adapter.Name(), err)
	}

	report.Took = time.Since(start)
	return report, nil
}

// keepYear drops postings for other years, e.g. leftover Summer 2026 roles.
func keepYear(postings []listing.Posting, year string) []listing.Posting {
	var kept []listing.Posting
	for _, p := range postings {
		if strings.HasSuffix(strings.TrimSpace(p.Season), year) {
			kept = append(kept, p)
		}
	}
	return kept
}
