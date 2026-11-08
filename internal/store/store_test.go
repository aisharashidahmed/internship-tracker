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

func TestVisibleListings_SkipsClosed(t *testing.T) {
	s := newTestStore(t)
	save(t, s, stripeListing(true, "Stripe"))

	open, err := s.VisibleListings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Company != "Stripe" || open[0].Season != "Summer 2027" {
		t.Fatalf("VisibleListings = %+v, want the one Stripe listing", open)
	}

	save(t, s, stripeListing(false, "Stripe")) // closes it
	if open, _ := s.VisibleListings(context.Background()); len(open) != 0 {
		t.Errorf("VisibleListings after closing = %+v, want none", open)
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

// --- Slice 3: matching by job link, and stale listings ---

// saveFrom saves postings from the named source, grouped as ingestion would.
func saveFrom(t *testing.T, s *Store, sourceName string, postings ...listing.Posting) []Outcome {
	t.Helper()
	sourceID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = $1`, sourceName)
	var outcomes []Outcome
	for _, l := range listing.Group(postings) {
		o, err := s.SaveListing(context.Background(), sourceID, l)
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, o)
	}
	return outcomes
}

func markMissing(t *testing.T, s *Store, sourceName string, seen ...string) int {
	t.Helper()
	sourceID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = $1`, sourceName)
	n, err := s.MarkMissing(context.Background(), sourceID, seen)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func status(t *testing.T, s *Store) string {
	t.Helper()
	return scalar[string](t, s, `SELECT string_agg(status, ',' ORDER BY id) FROM listings`)
}

// The Simplify posting as stored first; SpeedyApply postings join it.
var simplifyFigma = listing.Posting{
	SourceListingID: "s1", Company: "Figma", Title: "Software Engineer Intern",
	JobType: listing.Internship, Season: "Summer 2027", Locations: []string{"San Francisco, CA", "NYC"},
	ApplyURL:    "https://job-boards.greenhouse.io/figma/jobs/6241542004?utm_source=Simplify",
	DegreeLevel: listing.AdvancedRequired, Active: true,
}

func speedyPosting(title, url string) listing.Posting {
	id, _ := listing.JobLink(url)
	return listing.Posting{
		SourceListingID: id, Company: "Figma", Title: title, JobType: listing.Internship,
		Season: "", Locations: []string{"San Francisco, CA"}, ApplyURL: url,
		DegreeLevel: listing.DegreeUnknown, Active: true,
	}
}

func TestSave_SameJobLinkAcrossSourcesIsOneListing(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)

	// Different title wording and no season, but the same Greenhouse job.
	got := saveFrom(t, s, "speedyapply", speedyPosting("PhD Intern - Software Engineer - 2027",
		"https://boards.greenhouse.io/figma/jobs/6241542004?gh_jid=6241542004"))

	if got[0] != OutcomeUpdated {
		t.Errorf("outcome = %s, want updated (joined the Simplify listing)", got[0])
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 1 {
		t.Errorf("listings = %d, want 1", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listing_sources`); n != 2 {
		t.Errorf("postings = %d, want 2 (each source keeps its own link)", n)
	}
	// SpeedyApply's unknown degree must not override Simplify's known one.
	if d := scalar[string](t, s, `SELECT degree_level FROM listings`); d != string(listing.AdvancedRequired) {
		t.Errorf("degree = %s, want advanced_required", d)
	}
}

func TestSave_GenericLinkNeverMatches(t *testing.T) {
	s := newTestStore(t)
	generic := simplifyFigma
	generic.ApplyURL = "https://www.figma.com/careers/"
	saveFrom(t, s, "simplify", generic)

	// Same generic link, different job: must not join.
	saveFrom(t, s, "speedyapply", speedyPosting("Product Designer Intern", "https://www.figma.com/careers/"))

	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 2 {
		t.Errorf("listings = %d, want 2 (generic links are never used for matching)", n)
	}
}

func TestSave_AmbiguousJobLinkStaysSeparate(t *testing.T) {
	s := newTestStore(t)
	fall := simplifyFigma
	fall.Season = "Fall 2027"
	saveFrom(t, s, "simplify", simplifyFigma, fall) // one ad, two seasons: two listings

	// SpeedyApply doesn't say the season, so we can't tell which one it is.
	saveFrom(t, s, "speedyapply", speedyPosting("Software Engineer Intern, Growth",
		"https://job-boards.greenhouse.io/figma/jobs/6241542004"))

	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 3 {
		t.Errorf("listings = %d, want 3 (unsure which season: keep separate)", n)
	}
}

func TestMarkMissing_VanishedPostingMakesListingStale(t *testing.T) {
	s := newTestStore(t)
	p := speedyPosting("Software Engineer Intern", "https://jobs.ashbyhq.com/figma/3fba1c39-c5cb-47d7-9ad2-1cec4d7e9d0c")
	saveFrom(t, s, "speedyapply", p)

	// The next SpeedyApply file doesn't list it.
	if n := markMissing(t, s, "speedyapply", "something-else"); n != 1 {
		t.Errorf("became stale = %d, want 1", n)
	}
	if st := status(t, s); st != StatusStale {
		t.Fatalf("status = %s, want stale", st)
	}
	if !scalar[bool](t, s, `SELECT missing_since IS NOT NULL FROM listing_sources`) {
		t.Error("posting has no missing_since")
	}
	if !scalar[bool](t, s, `SELECT closed_at IS NULL FROM listings`) {
		t.Error("a stale listing must not have closed_at")
	}

	// It comes back: open again, and no longer missing.
	if got := saveFrom(t, s, "speedyapply", p); got[0] != OutcomeUpdated {
		t.Errorf("reappeared outcome = %s, want updated", got[0])
	}
	if st := status(t, s); st != StatusOpen {
		t.Errorf("status after reappearing = %s, want open", st)
	}
}

func TestMarkMissing_OtherSourceStillOpenKeepsListingOpen(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	saveFrom(t, s, "speedyapply", speedyPosting("Software Engineer Intern",
		"https://job-boards.greenhouse.io/figma/jobs/6241542004"))

	if n := markMissing(t, s, "speedyapply"); n != 0 {
		t.Errorf("became stale = %d, want 0: Simplify still lists it as active", n)
	}
	if st := status(t, s); st != StatusOpen {
		t.Errorf("status = %s, want open", st)
	}
}

func TestMarkMissing_OnlyAnExplicitFlagCloses(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	markMissing(t, s, "simplify") // vanished: stale, not closed
	if st := status(t, s); st != StatusStale {
		t.Fatalf("status = %s, want stale", st)
	}

	closed := simplifyFigma
	closed.Active = false
	if got := saveFrom(t, s, "simplify", closed); got[0] != OutcomeClosed {
		t.Errorf("outcome = %s, want closed", got[0])
	}
	if st := status(t, s); st != StatusClosed {
		t.Errorf("status = %s, want closed", st)
	}
}

func TestDB_StatusAndClosedAtAgree(t *testing.T) {
	s := newTestStore(t)
	companyID := scalar[int64](t, s,
		`INSERT INTO companies (name, normalized_name) VALUES ('Stripe', 'stripe') RETURNING id`)
	err := exec(t, s, `INSERT INTO listings (company_id, title, job_type, season, degree_level, dedupe_key, status, closed_at)
	      VALUES ($1, 'SWE Intern', 'internship', '', 'unknown', 'k', 'stale', now())`, companyID)
	if err == nil {
		t.Error("a stale listing with closed_at was accepted; the CHECK should reject it")
	}
}
