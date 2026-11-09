// Package ingest runs one source end to end:
//
//	fetch (skip if unchanged) -> adapter.Parse -> keep target year -> group -> save
//	-> mark postings the source no longer lists as missing
//
// It knows the order of the steps but none of the details: parsing lives in
// the adapters, matching rules in package listing, SQL in package store.
package ingest

import (
	"context"
	"errors"
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

// TargetYear is the year whose listings we keep.
const TargetYear = "2027"

// New returns an Ingester with the settings both cmd/ingest and the
// server use.
func New(st *store.Store) *Ingester {
	return &Ingester{
		Store:  st,
		Client: &http.Client{Timeout: 60 * time.Second}, // never hang on a slow download
		Year:   TargetYear,
	}
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

// ErrBusy means another ingest of the same source was writing at the same
// moment, so this run skipped its writes; the other run saves the same data.
var ErrBusy = errors.New("another ingest of this source is running; skipped")

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
	// An empty file almost certainly means the format changed and the parser
	// found nothing it recognizes. Saving it would mark every job stale.
	if len(postings) == 0 {
		return report, fmt.Errorf("%s: parsed 0 postings; refusing to save (format changed?)", adapter.Name())
	}
	postings = keepYear(postings, in.Year)
	report.Postings = len(postings)

	err = in.Store.InTx(ctx, func(tx *store.Store) error {
		if ok, err := tx.TryLockSource(ctx, src.ID); err != nil {
			return err
		} else if !ok {
			return ErrBusy
		}
		for _, l := range listing.Group(postings) {
			outcome, err := tx.SaveListing(ctx, src.ID, l, src.FirstLoad)
			if err != nil {
				return err
			}
			report.Counts[outcome]++
		}

		// Postings this file no longer lists become "not seen since now".
		var seen []string
		for _, p := range postings {
			seen = append(seen, p.SourceListingID)
		}
		stale, err := tx.MarkMissing(ctx, src.ID, seen)
		if err != nil {
			return err
		}
		report.Counts[store.OutcomeStale] = stale

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
// Postings with an unknown season are kept: every source we read is a list
// for one year, so "unknown" can only mean "unknown season of that year".
func keepYear(postings []listing.Posting, year string) []listing.Posting {
	var kept []listing.Posting
	for _, p := range postings {
		season := strings.TrimSpace(p.Season)
		if season == "" || strings.HasSuffix(season, year) {
			kept = append(kept, p)
		}
	}
	return kept
}
