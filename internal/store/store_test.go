package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"internship-tracker/internal/db"
	"internship-tracker/internal/listing"
)

// These tests need a real Postgres. They use $TEST_DATABASE_URL, or the local
// internship_tracker_test database. Each test runs in a transaction that is
// rolled back at the end, so tests never see each other's data.

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://localhost:5432/internship_tracker_test?sslmode=disable"
	}
	ctx := context.Background()

	pool, err := db.Connect(ctx, url)
	if err != nil {
		fmt.Println("store tests need Postgres:", err)
		os.Exit(1)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// newTestStore returns a Store whose writes are thrown away after the test.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(ctx) })
	return &Store{db: tx}
}

// requireUniqueViolation fails the test unless err is Postgres error 23505.
func requireUniqueViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("want unique_violation (23505), got %v", err)
	}
}

// exec runs a statement, inside a savepoint so a failure doesn't abort the
// test's transaction, and returns its error.
func exec(t *testing.T, s *Store, sql string, args ...any) error {
	t.Helper()
	return s.InTx(context.Background(), func(tx *Store) error {
		_, err := tx.db.Exec(context.Background(), sql, args...)
		return err
	})
}

func scalar[T any](t *testing.T, s *Store, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := s.db.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// --- Rule 5: every uniqueness rule is enforced by the database ---

func TestDB_CompanyNormalizedNameIsUnique(t *testing.T) {
	s := newTestStore(t)
	q := `INSERT INTO companies (name, normalized_name) VALUES ($1, 'stripe')`
	if err := exec(t, s, q, "Stripe"); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, "Stripe, Inc."))
}

func TestDB_SourceNameIsUnique(t *testing.T) {
	s := newTestStore(t)
	// "simplify" is already inserted by the migration.
	requireUniqueViolation(t, exec(t, s,
		`INSERT INTO sources (name, format, url) VALUES ('simplify', 'json', 'https://x')`))
}

func TestDB_ListingDedupeKeyIsUnique(t *testing.T) {
	s := newTestStore(t)
	companyID := scalar[int64](t, s,
		`INSERT INTO companies (name, normalized_name) VALUES ('Stripe', 'stripe') RETURNING id`)
	q := `INSERT INTO listings (company_id, title, job_type, season, degree_level, dedupe_key)
	      VALUES ($1, 'SWE Intern', 'internship', 'Summer 2027', 'bachelors_ok', 'same-key')`
	if err := exec(t, s, q, companyID); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, companyID))
}

func TestDB_ListingSourceTripleIsUnique(t *testing.T) {
	s := newTestStore(t)
	companyID := scalar[int64](t, s,
		`INSERT INTO companies (name, normalized_name) VALUES ('Stripe', 'stripe') RETURNING id`)
	listingID := scalar[int64](t, s,
		`INSERT INTO listings (company_id, title, job_type, season, degree_level, dedupe_key)
		 VALUES ($1, 'SWE Intern', 'internship', 'Summer 2027', 'bachelors_ok', 'k') RETURNING id`, companyID)
	sourceID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)

	q := `INSERT INTO listing_sources (listing_id, source_id, source_listing_id, apply_url, active)
	      VALUES ($1, $2, 'abc', 'https://x', true)`
	if err := exec(t, s, q, listingID, sourceID); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, listingID, sourceID))
}

// --- SaveListing behaviour ---

func stripeListing(active bool, companyName string) listing.Listing {
	return listing.Group([]listing.Posting{{
		SourceListingID: "a1", Company: companyName, Title: "Software Engineer Intern",
		JobType: listing.Internship, Season: "Summer 2027", Locations: []string{"SF"},
		ApplyURL: "https://stripe.com/a1", DegreeLevel: listing.BachelorsOK, Active: active,
	}})[0]
}

func save(t *testing.T, s *Store, l listing.Listing) Outcome {
	t.Helper()
	sourceID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)
	got, err := s.SaveListing(context.Background(), sourceID, l)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSaveListing_TwiceIsUnchanged(t *testing.T) {
	s := newTestStore(t)
	if got := save(t, s, stripeListing(true, "Stripe")); got != OutcomeNew {
		t.Fatalf("first save = %s, want new", got)
	}
	// Same job, company spelled differently: still one company, one listing.
	if got := save(t, s, stripeListing(true, "Stripe, Inc.")); got != OutcomeUnchanged {
		t.Fatalf("second save = %s, want unchanged", got)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM companies`); n != 1 {
		t.Errorf("companies = %d, want 1", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 1 {
		t.Errorf("listings = %d, want 1", n)
	}
}

func TestSaveListing_NewPostingIsUpdated(t *testing.T) {
	s := newTestStore(t)
	save(t, s, stripeListing(true, "Stripe"))

	l := stripeListing(true, "Stripe")
	l.Postings = append(l.Postings, listing.Posting{
		SourceListingID: "a2", ApplyURL: "https://stripe.com/a2", Locations: []string{"NYC"}, Active: true,
	})
	if got := save(t, s, l); got != OutcomeUpdated {
		t.Errorf("save with extra posting = %s, want updated", got)
	}
}

func TestSaveListing_InactiveClosesThenReopens(t *testing.T) {
	s := newTestStore(t)
	save(t, s, stripeListing(true, "Stripe"))

	if got := save(t, s, stripeListing(false, "Stripe")); got != OutcomeClosed {
		t.Fatalf("save inactive = %s, want closed", got)
	}
	if !scalar[bool](t, s, `SELECT closed_at IS NOT NULL FROM listings`) {
		t.Error("closed listing has no closed_at")
	}
	if got := save(t, s, stripeListing(false, "Stripe")); got != OutcomeUnchanged {
		t.Errorf("save inactive again = %s, want unchanged", got)
	}
	if got := save(t, s, stripeListing(true, "Stripe")); got != OutcomeUpdated {
		t.Errorf("reopen = %s, want updated", got)
	}
	if status := scalar[string](t, s, `SELECT status FROM listings`); status != "open" {
		t.Errorf("status after reopen = %s, want open", status)
	}
}

func TestSaveListing_InactiveNeverStoredIsIgnored(t *testing.T) {
	s := newTestStore(t)
	if got := save(t, s, stripeListing(false, "Stripe")); got != OutcomeIgnored {
		t.Fatalf("save = %s, want ignored", got)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 0 {
		t.Errorf("listings = %d, want 0", n)
	}
}

func TestOpenListings_SkipsClosed(t *testing.T) {
	s := newTestStore(t)
	save(t, s, stripeListing(true, "Stripe"))

	open, err := s.OpenListings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Company != "Stripe" || open[0].Season != "Summer 2027" {
		t.Fatalf("OpenListings = %+v, want the one Stripe listing", open)
	}

	save(t, s, stripeListing(false, "Stripe")) // closes it
	if open, _ := s.OpenListings(context.Background()); len(open) != 0 {
		t.Errorf("OpenListings after closing = %+v, want none", open)
	}
}

func TestSaveListing_StoresOneFormPerCity(t *testing.T) {
	s := newTestStore(t)
	save(t, s, stripeListing(true, "Stripe")) // posted with Locations: "SF"
	got := scalar[[]string](t, s, `SELECT locations FROM listing_sources`)
	if len(got) != 1 || got[0] != "San Francisco, CA" {
		t.Errorf("locations = %q, want [San Francisco, CA]", got)
	}
}
