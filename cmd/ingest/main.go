// Command ingest fetches every source once and prints what changed.
//
//	go run ./cmd/ingest            # normal run; skips sources that are unchanged
//	go run ./cmd/ingest -force     # ignore saved ETags and re-save everything
//
// If one source fails (download error, format change), the others still
// run, and the command exits with status 1 so the failure isn't missed.
// A later slice will run this every hour from the server instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"internship-tracker/internal/db"
	"internship-tracker/internal/ingest"
	"internship-tracker/internal/listing"
	"internship-tracker/internal/source"
	"internship-tracker/internal/store"
)

func main() {
	force := flag.Bool("force", false, "download and re-save even if a source hasn't changed")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	in := ingest.New(store.New(pool))

	failed := 0
	for _, r := range in.RunAll(ctx, source.All(), ingest.Options{Force: *force}) {
		if r.Err != nil {
			// Each source runs in its own transaction, so a failure here
			// leaves that source's data exactly as it was before the run.
			fmt.Fprintf(os.Stderr, "\nFAILED %s: %v\n  its data was left unchanged; other sources continue\n\n",
				r.Report.Source, r.Err)
			failed++
			continue
		}
		printReport(r.Report)
	}

	if err := printPossibleDuplicates(ctx, in.Store); err != nil {
		log.Fatal(err)
	}
	if err := printIndustryReview(ctx, in.Store); err != nil {
		log.Fatal(err)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d source(s) failed\n", failed)
		os.Exit(1)
	}
}

func printReport(r ingest.Report) {
	if r.Note != "" { // a step that doesn't save listings (top companies, notifications)
		fmt.Printf("%s: %s in %s\n", r.Source, r.Note, r.Took.Round(time.Millisecond))
		return
	}
	if r.NotModified {
		fmt.Printf("%s: unchanged since last fetch (HTTP 304), skipped in %s\n",
			r.Source, r.Took.Round(time.Millisecond))
		return
	}
	fmt.Printf("%s: %d postings for the target year, in %s\n",
		r.Source, r.Postings, r.Took.Round(time.Millisecond))
	for _, o := range []store.Outcome{store.OutcomeNew, store.OutcomeUpdated, store.OutcomeUnchanged,
		store.OutcomeStale, store.OutcomeClosed, store.OutcomeIgnored} {
		fmt.Printf("  %-10s %6d\n", o, r.Counts[o])
	}
	if r.RulesChanged {
		fmt.Println("  (matching rules changed since the last run: re-saved everything)")
	}
	if m := r.Merges; m.Moved > 0 {
		fmt.Printf("  after a rule change: %d postings moved, %d listings merged, %d kept apart\n", m.Moved, m.Merged, m.KeptApart)
	}
}

// Titles sharing at least this share of words are worth a human look.
// Chosen by reading the report on real data, not derived; tune freely.
const duplicateSimilarity = 0.75

// How many pairs to print; the rest are only counted.
const duplicatesShown = 15

// printPossibleDuplicates lists visible listings at the same company with
// compatible seasons and very similar titles that matching kept apart.
func printPossibleDuplicates(ctx context.Context, s *store.Store) error {
	visible, err := s.VisibleListings(ctx)
	if err != nil {
		return err
	}
	pairs := listing.PossibleDuplicates(visible, duplicateSimilarity)

	fmt.Printf("\npossible duplicates (same company, similar titles, not merged): %d\n", len(pairs))
	for i, p := range pairs {
		if i == duplicatesShown {
			fmt.Printf("  ... and %d more\n", len(pairs)-duplicatesShown)
			break
		}
		fmt.Printf("  %.2f  %s\n        #%d %q (%s)\n        #%d %q (%s)\n",
			p.Similarity, p.A.Company, p.A.ID, p.A.Title, seasonOrUnknown(p.A.Season),
			p.B.ID, p.B.Title, seasonOrUnknown(p.B.Season))
	}
	fmt.Println("  (ignored = inactive on the source and never stored; stale = vanished from its source, may be closed)")

	// Pairs a rule change would have merged, kept apart on purpose.
	kept, err := s.KeptApartPairs(ctx)
	if err != nil {
		return err
	}
	if len(kept) > 0 {
		fmt.Printf("\nkept apart after a rule change (possible duplicates): %d\n", len(kept))
		for _, k := range kept {
			fmt.Println("  " + k)
		}
	}
	return nil
}

// printIndustryReview lists companies a keyword tagged ("Health",
// "Medical", ...) that nobody has reviewed. Move each to the "approved" or
// "never" list in internal/listing/tags.go; it won't be listed again.
func printIndustryReview(ctx context.Context, s *store.Store) error {
	tagged, err := s.IndustriesToReview(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("\nindustry review: %d new keyword match(es) (move each to approved or never in internal/listing/tags.go)\n", len(tagged))
	for _, t := range tagged {
		fmt.Println("  " + t)
	}
	return nil
}

func seasonOrUnknown(s string) string {
	if s == "" {
		return "season unknown"
	}
	return s
}
