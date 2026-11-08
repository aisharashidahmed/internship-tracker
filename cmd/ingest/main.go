// Command ingest fetches listings from a source once and prints what changed.
//
//	go run ./cmd/ingest            # normal run; skips if the source is unchanged
//	go run ./cmd/ingest -force     # ignore the saved ETag and re-save everything
//
// Slice 3 will run this every hour from the server instead.
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
	"internship-tracker/internal/source/simplify"
	"internship-tracker/internal/store"
)

func main() {
	force := flag.Bool("force", false, "download and re-save even if the source hasn't changed")
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

	report, err := in.Run(ctx, simplify.Adapter{}, ingest.Options{Force: *force})
	if err != nil {
		log.Fatal(err)
	}
	printReport(report)

	if err := printPossibleDuplicates(ctx, in.Store); err != nil {
		log.Fatal(err)
	}
}

// Titles sharing at least this share of words are worth a human look.
// Chosen by reading the report on real data, not derived; tune freely.
const duplicateSimilarity = 0.75

// How many pairs to print; the rest are only counted.
const duplicatesShown = 15

// printPossibleDuplicates lists open listings at the same company and season
// with very similar titles that matching kept apart.
func printPossibleDuplicates(ctx context.Context, s *store.Store) error {
	open, err := s.OpenListings(ctx)
	if err != nil {
		return err
	}
	pairs := listing.PossibleDuplicates(open, duplicateSimilarity)

	fmt.Printf("\npossible duplicates (same company and season, similar titles, not merged): %d\n", len(pairs))
	for i, p := range pairs {
		if i == duplicatesShown {
			fmt.Printf("  ... and %d more\n", len(pairs)-duplicatesShown)
			break
		}
		fmt.Printf("  %.2f  %s, %s\n        #%d %q\n        #%d %q\n",
			p.Similarity, p.A.Company, p.A.Season, p.A.ID, p.A.Title, p.B.ID, p.B.Title)
	}
	return nil
}

func printReport(r ingest.Report) {
	if r.NotModified {
		fmt.Printf("%s: unchanged since last fetch (HTTP 304), skipped in %s\n",
			r.Source, r.Took.Round(time.Millisecond))
		fmt.Println("  run with -force to re-save anyway")
		return
	}
	fmt.Printf("%s: %d postings for the target year, in %s\n",
		r.Source, r.Postings, r.Took.Round(time.Millisecond))
	for _, o := range []store.Outcome{store.OutcomeNew, store.OutcomeUpdated, store.OutcomeUnchanged, store.OutcomeClosed, store.OutcomeIgnored} {
		fmt.Fprintf(os.Stdout, "  %-10s %6d\n", o, r.Counts[o])
	}
	fmt.Println("  (ignored = inactive on the source and never stored)")
}
