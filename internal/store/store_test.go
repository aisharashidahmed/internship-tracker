package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

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
	got, err := s.SaveListing(context.Background(), sourceID, l, false)
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
		o, err := s.SaveListing(context.Background(), sourceID, l, false)
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

// --- Slice 4: feed order ---

func TestFeed_Order(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	simplifyID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)
	speedyID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'speedyapply'`)
	daysAgo := func(n int) time.Time { return time.Now().AddDate(0, 0, -n) }

	save := func(sourceID int64, firstLoad bool, id, title string, posted time.Time) {
		t.Helper()
		l := listing.Group([]listing.Posting{{
			SourceListingID: id, Company: "Acme", Title: title, JobType: listing.Internship,
			Season: "Summer 2027", ApplyURL: "https://acme.com/jobs/" + id + "0000", Active: true, PostedAt: posted,
		}})[0]
		if _, err := s.SaveListing(ctx, sourceID, l, firstLoad); err != nil {
			t.Fatal(err)
		}
	}

	// First ingest: everything gets the same first_seen_at, so posted date decides.
	save(simplifyID, true, "1", "Older", daysAgo(5))
	save(simplifyID, true, "2", "Newer", daysAgo(1))
	save(simplifyID, true, "3", "No date", time.Time{})
	save(speedyID, true, "4", "Will go stale", daysAgo(0))
	// A later run: new to us right now, so it's the freshest, whatever its posted date.
	save(simplifyID, false, "5", "Found later", daysAgo(30))
	markMissing(t, s, "speedyapply") // "Will go stale" vanished from SpeedyApply

	feed, err := s.Feed(ctx, FeedQuery{JobType: listing.Internship, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range feed {
		got = append(got, l.Title)
	}
	want := []string{"Found later", "Newer", "Older", "No date", "Will go stale"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("feed order:\n got  %q\n want %q", got, want)
	}
	if len(feed[0].Postings) != 1 || feed[0].Postings[0].Source != "simplify" {
		t.Errorf("first listing's postings = %+v, want one from simplify", feed[0].Postings)
	}
}

func TestSave_EarlierPostedDateWins(t *testing.T) {
	s := newTestStore(t)
	early := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

	p := simplifyFigma
	p.PostedAt = late
	saveFrom(t, s, "simplify", p)
	q := speedyPosting("Software Engineer Intern", "https://job-boards.greenhouse.io/figma/jobs/6241542004")
	q.PostedAt = early
	saveFrom(t, s, "speedyapply", q)

	if got := scalar[time.Time](t, s, `SELECT posted_at FROM listings`); !got.Equal(early) {
		t.Errorf("posted_at = %v, want the earlier %v", got, early)
	}
}

// --- Slice 5: only one ingest per source at a time ---

func TestTryLockSource_SecondRunIsTurnedAway(t *testing.T) {
	ctx := context.Background()
	// Two separate transactions, like the hourly run and a manual run.
	tx1, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	tx2, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	first, second := &Store{db: tx1}, &Store{db: tx2}

	if ok, err := first.TryLockSource(ctx, 1); err != nil || !ok {
		t.Fatalf("first lock = %v, %v; want true", ok, err)
	}
	if ok, _ := second.TryLockSource(ctx, 1); ok {
		t.Error("second run got the lock while the first holds it")
	}
	if ok, _ := second.TryLockSource(ctx, 2); !ok {
		t.Error("a different source should not be blocked")
	}

	tx1.Rollback(ctx) // first run ends: its lock is released
	if ok, _ := second.TryLockSource(ctx, 1); !ok {
		t.Error("lock not released when the first transaction ended")
	}
}

// --- Slice 6: tags ---

func TestDB_ListingRoleIsUnique(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	id := scalar[int64](t, s, `SELECT id FROM listings`)
	// SaveListing already tagged it general_swe; a second identical row must fail.
	requireUniqueViolation(t, exec(t, s, `INSERT INTO listing_roles (listing_id, role) VALUES ($1, 'general_swe')`, id))
}

func TestDB_CompanyIndustryIsUnique(t *testing.T) {
	s := newTestStore(t)
	id := scalar[int64](t, s, `INSERT INTO companies (name, normalized_name) VALUES ('CVS Health', 'cvs health') RETURNING id`)
	q := `INSERT INTO company_industries (company_id, industry, needs_review) VALUES ($1, 'healthcare', true)`
	if err := exec(t, s, q, id); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, id))
}

func roles(t *testing.T, s *Store) string {
	t.Helper()
	return scalar[string](t, s, `SELECT COALESCE(string_agg(role, ',' ORDER BY role), '') FROM listing_roles`)
}

func TestSave_SpecificRoleFromAnySourceBeatsGeneralSWE(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma) // "Software Engineer Intern": no signal
	if got := roles(t, s); got != "general_swe" {
		t.Fatalf("roles = %q, want general_swe", got)
	}

	// SpeedyApply lists the same job (same link) as a backend role.
	saveFrom(t, s, "speedyapply", speedyPosting("Backend Software Engineer Intern",
		"https://job-boards.greenhouse.io/figma/jobs/6241542004"))
	if got := roles(t, s); got != "backend" {
		t.Errorf("roles = %q, want backend (general_swe dropped)", got)
	}
}

func TestFeed_Filters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	add := func(company, title, section string) {
		t.Helper()
		p := listing.Posting{SourceListingID: title, Company: company, Title: title, Section: section,
			JobType: listing.Internship, Season: "Summer 2027", ApplyURL: "https://x.com/jobs/" + title, Active: true}
		saveFrom(t, s, "simplify", p)
	}
	add("Stripe", "Backend Engineer Intern", "Software")
	add("Stripe", "Machine Learning Intern", "AI/ML/Data")
	add("Stripe", "Backend ML Infrastructure Intern", "Software")
	add("CVS Health", "Machine Learning Intern - Pharmacy", "AI/ML/Data")
	if err := s.SyncIndustries(ctx); err != nil {
		t.Fatal(err)
	}

	titles := func(q FeedQuery) string {
		t.Helper()
		q.JobType, q.Limit = listing.Internship, 50
		feed, err := s.Feed(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range feed {
			got = append(got, l.Title)
		}
		slices.Sort(got)
		return strings.Join(got, " | ")
	}

	backendOrML := titles(FeedQuery{Roles: []listing.Role{listing.Backend, listing.ML}})
	if backendOrML != "Backend Engineer Intern | Backend ML Infrastructure Intern | Machine Learning Intern | Machine Learning Intern - Pharmacy" {
		t.Errorf("Backend / ML = %s", backendOrML)
	}
	backendAndML := titles(FeedQuery{Roles: []listing.Role{listing.Backend, listing.ML}, MatchAllRoles: true})
	if backendAndML != "Backend ML Infrastructure Intern" {
		t.Errorf("Backend + ML = %s", backendAndML)
	}
	mlInHealthcare := titles(FeedQuery{Roles: []listing.Role{listing.ML}, Industries: []listing.Industry{listing.Healthcare}})
	if mlInHealthcare != "Machine Learning Intern - Pharmacy" {
		t.Errorf("ML + Healthcare = %s", mlInHealthcare)
	}
	if all := titles(FeedQuery{}); strings.Count(all, "|") != 3 {
		t.Errorf("no filters should show all 4, got %s", all)
	}
}

func TestSyncIndustries_RemovesTagsTheRulesNoLongerGive(t *testing.T) {
	s := newTestStore(t)
	id := scalar[int64](t, s, `INSERT INTO companies (name, normalized_name) VALUES ('Medical Properties Trust', 'medical properties trust') RETURNING id`)
	// As if an older rule had tagged it, before it was on the never-list.
	if err := exec(t, s, `INSERT INTO company_industries VALUES ($1, 'healthcare', true)`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncIndustries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM company_industries WHERE company_id = $1`, id); n != 0 {
		t.Errorf("Medical Properties Trust still tagged healthcare")
	}
}
