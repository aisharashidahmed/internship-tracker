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
	"net/http"
	"os"
	"time"

	"internship-tracker/internal/db"
	"internship-tracker/internal/ingest"
	"internship-tracker/internal/listing"
	"internship-tracker/internal/source"
	"internship-tracker/internal/source/simplify"
	"internship-tracker/internal/source/speedyapply"
	"internship-tracker/internal/store"
)

// sources run in this order. Simplify goes first: it has explicit seasons,
// degrees and closed flags, so its listings are the better base for
// SpeedyApply postings to join.
var sources = []source.Adapter{
	simplify.Adapter{},
	speedyapply.Adapter{},
}

func main() {
	force := flag.Bool("force", false, "download and re-save even if a source hasn't changed")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	in := &ingest.Ingester{
		Store:  store.New(pool),
		Client: &http.Client{Timeout: 60 * time.Second},
		Year:   "2027",
	}

	failed := 0
	for _, adapter := range sources {
		report, err := in.Run(ctx, adapter, ingest.Options{Force: *force})
		if err != nil {
			// Each source runs in its own transaction, so a failure here
			// leaves that source's data exactly as it was before the run.
			fmt.Fprintf(os.Stderr, "\nFAILED %s: %v\n  its data was left unchanged; other sources continue\n\n",
				adapter.Name(), err)
			failed++
			continue
		}
		printReport(report)
	}

	if err := printPossibleDuplicates(ctx, in.Store); err != nil {
		log.Fatal(err)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d source(s) failed\n", failed)
		os.Exit(1)
	}
}

func printReport(r ingest.Report) {
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
	return nil
}

func seasonOrUnknown(s string) string {
	if s == "" {
		return "season unknown"
	}
	return s
}
