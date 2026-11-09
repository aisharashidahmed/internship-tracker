package ingest

import (
	"context"
	"fmt"
	"log"
	"time"

	"internship-tracker/internal/source"
	"internship-tracker/internal/source/simplify"
	"internship-tracker/internal/store"
)

// Result is one source's outcome in a run of every source.
type Result struct {
	Report Report
	Err    error // nil on success
}

// RunAll ingests every source in order. A failing source doesn't stop the
// others: its error goes in its Result, and the next source still runs.
// Afterwards it re-tags company industries (cheap, and it means a rule
// change applies on the next run even if no source file changed).
func (in *Ingester) RunAll(ctx context.Context, adapters []source.Adapter, opts Options) []Result {
	results := make([]Result, 0, len(adapters))
	for _, a := range adapters {
		report, err := in.Run(ctx, a, opts)
		results = append(results, Result{Report: report, Err: err})
	}
	results = append(results, in.syncSimplifyTopCompanies(ctx, opts))
	if err := in.Store.SyncIndustries(ctx); err != nil {
		results = append(results, Result{Report: Report{Source: "industry tags"}, Err: err})
	}
	// Now that this hour's roles are saved: notify users about new roles at
	// companies they starred (each role pings each user at most once).
	start := time.Now()
	n, err := in.Store.CreateNotifications(ctx)
	results = append(results, Result{Err: err, Report: Report{Source: "notifications",
		Note: fmt.Sprintf("%d new", n), Took: time.Since(start)}})
	return results
}

// Every calls run once right away, then every interval, until ctx is
// cancelled. Runs never overlap: if one takes longer than the interval,
// the missed ticks are dropped rather than queued (time.Ticker does this),
// because running twice in a row would fetch the same files twice.
func Every(ctx context.Context, interval time.Duration, run func(context.Context)) {
	run(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run(ctx)
		}
	}
}

// LogResults writes one line per source, so even a quiet hour shows that
// the scheduler is alive:
//
//	simplify: unchanged (HTTP 304) in 81ms
//	speedyapply: 827 postings, 3 new, 2 updated, 0 unchanged, 1 stale, 0 closed in 240ms
//	speedyapply: FAILED after 2s: unexpected status 404 Not Found
func LogResults(results []Result) {
	for _, r := range results {
		log.Print(Summary(r))
	}
}

// Summary is the one-line description of a source's result.
func Summary(r Result) string {
	rep := r.Report
	took := rep.Took.Round(time.Millisecond)
	switch {
	case r.Err != nil:
		return fmt.Sprintf("%s: FAILED: %v", rep.Source, r.Err)
	case rep.Note != "":
		return fmt.Sprintf("%s: %s in %s", rep.Source, rep.Note, took)
	case rep.NotModified:
		return fmt.Sprintf("%s: unchanged (HTTP 304) in %s", rep.Source, took)
	default:
		c := rep.Counts
		line := fmt.Sprintf("%s: %d postings, %d new, %d updated, %d unchanged, %d stale, %d closed in %s",
			rep.Source, rep.Postings, c[store.OutcomeNew], c[store.OutcomeUpdated], c[store.OutcomeUnchanged],
			c[store.OutcomeStale], c[store.OutcomeClosed], took)
		if rep.RulesChanged {
			line += " (rules changed: re-saved)"
		}
		if m := rep.Merges; m.Moved > 0 {
			line += fmt.Sprintf("; %d postings moved, %d listings merged, %d kept apart", m.Moved, m.Merged, m.KeptApart)
		}
		return line
	}
}

// syncSimplifyTopCompanies reads Simplify's README (its JSON has no FAANG+
// field) and updates the "simplify-readme" top-company signal. It has its
// own ETag, so an unchanged README costs one small request.
func (in *Ingester) syncSimplifyTopCompanies(ctx context.Context, opts Options) Result {
	const name = "simplify-readme"
	start := time.Now()
	rep := Report{Source: "simplify README (top companies)"}
	done := func(err error) Result { rep.Took = time.Since(start); return Result{Report: rep, Err: err} }

	f, err := in.Store.SignalFileByName(ctx, name)
	if err != nil {
		return done(err)
	}
	etag := f.ETag
	if opts.Force {
		etag = ""
	}
	res, err := fetch(ctx, in.Client, f.URL, etag)
	if err != nil {
		return done(err)
	}
	if res.NotModified {
		rep.Note = "unchanged (HTTP 304)"
		return done(in.Store.MarkSignalFetched(ctx, name, res.ETag))
	}
	names, err := simplify.TopCompanies(res.Body)
	if err != nil {
		return done(err) // the old signal stays as it was
	}
	n, err := in.Store.SyncTopCompanies(ctx, name, names)
	if err != nil {
		return done(err)
	}
	rep.Note = fmt.Sprintf("%d top companies marked, %d of them with listings here", len(names), n)
	return done(in.Store.MarkSignalFetched(ctx, name, res.ETag))
}
