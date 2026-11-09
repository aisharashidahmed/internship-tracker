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
	// A later run: new to us right now, and posted within 14 days of being
	// found, so it's the freshest. (Older discoveries: see the next test.)
	save(simplifyID, false, "5", "Found later", daysAgo(10))
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

// --- Slice 7: accounts ---

func TestDB_EmailIsUniqueIgnoringCase(t *testing.T) {
	s := newTestStore(t)
	q := `INSERT INTO users (email, password_hash) VALUES ($1, 'x')`
	if err := exec(t, s, q, "aisha@college.edu"); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, "Aisha@College.EDU"))
}

func TestDB_SessionTokenIsUnique(t *testing.T) {
	s := newTestStore(t)
	uid := scalar[int64](t, s, `INSERT INTO users (email, password_hash) VALUES ('a@b.co', 'x') RETURNING id`)
	q := `INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, 'same', now() + interval '1 day')`
	if err := exec(t, s, q, uid); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, uid))
}

func TestDB_InviteCannotBeOverused(t *testing.T) {
	s := newTestStore(t)
	err := exec(t, s, `INSERT INTO invites (token_hash, max_uses, uses, expires_at) VALUES ('t', 1, 2, now())`)
	if err == nil {
		t.Error("an invite with uses > max_uses was accepted; the CHECK should reject it")
	}
}

func TestSignUp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	token, err := s.CreateInvite(ctx, "two seats", 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.SignUp(ctx, token, "aisha@college.edu", "hash", "America/New_York"); err != nil {
		t.Fatalf("first sign-up: %v", err)
	}
	// Same email, different case: refused, and the seat is not used up.
	if _, err := s.SignUp(ctx, token, "AISHA@college.edu", "hash", "America/New_York"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email err = %v, want ErrEmailTaken", err)
	}
	if _, err := s.SignUp(ctx, token, "friend@college.edu", "hash", "America/New_York"); err != nil {
		t.Errorf("second seat: %v", err)
	}
	// Both seats used.
	if _, err := s.SignUp(ctx, token, "third@college.edu", "hash", "America/New_York"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("third sign-up err = %v, want ErrInviteInvalid", err)
	}
	if _, err := s.SignUp(ctx, "made-up-token", "x@college.edu", "hash", "America/New_York"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("unknown invite err = %v, want ErrInviteInvalid", err)
	}

	u, found, err := s.UserByEmail(ctx, "Aisha@College.edu")
	if err != nil || !found || u.Email != "aisha@college.edu" || u.PasswordHash != "hash" {
		t.Errorf("UserByEmail = %+v, %v, %v", u, found, err)
	}
}

func TestSignUp_ExpiredInvite(t *testing.T) {
	s := newTestStore(t)
	token, _ := s.CreateInvite(context.Background(), "", 1, -time.Minute) // already expired
	if _, err := s.SignUp(context.Background(), token, "a@b.co", "h", "America/New_York"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("err = %v, want ErrInviteInvalid", err)
	}
}

func TestSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid := scalar[int64](t, s, `INSERT INTO users (email, password_hash) VALUES ('a@b.co', 'x') RETURNING id`)

	token, _, err := s.CreateSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM sessions WHERE token_hash::text LIKE '%' || $1 || '%'`, token); n != 0 {
		t.Error("the raw token is stored; only its hash should be")
	}

	u, found, renewed, _, err := s.SessionUser(ctx, token)
	if err != nil || !found || u.ID != uid || renewed {
		t.Fatalf("SessionUser = %+v found=%v renewed=%v err=%v", u, found, renewed, err)
	}

	// Not used for two hours: the next request renews it.
	exec(t, s, `UPDATE sessions SET last_used_at = now() - interval '2 hours', expires_at = now() + interval '1 day'`)
	_, _, renewed, expires, _ := s.SessionUser(ctx, token)
	if !renewed || time.Until(expires) < 29*24*time.Hour {
		t.Errorf("renewed=%v expires in %s; want renewed to ~30 days", renewed, time.Until(expires))
	}

	// Expired: unknown.
	exec(t, s, `UPDATE sessions SET expires_at = now() - interval '1 second'`)
	if _, found, _, _, _ := s.SessionUser(ctx, token); found {
		t.Error("an expired session still logs in")
	}
	if n, _ := s.DeleteExpiredSessions(ctx); n != 1 {
		t.Errorf("deleted %d expired sessions, want 1", n)
	}

	// Logout.
	token, _, _ = s.CreateSession(ctx, uid)
	s.DeleteSession(ctx, token)
	if _, found, _, _, _ := s.SessionUser(ctx, token); found {
		t.Error("session still works after logout")
	}
}

// --- Slice 8: cleaning up after rule changes ---

// previousRun makes every stored posting look saved by an earlier run.
func previousRun(t *testing.T, s *Store) {
	t.Helper()
	if err := exec(t, s, `UPDATE listing_sources SET last_seen_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
}

func reattach(t *testing.T, s *Store, source string) MergeReport {
	t.Helper()
	id := scalar[int64](t, s, `SELECT id FROM sources WHERE name = $1`, source)
	rep, err := s.ReattachMovedPostings(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestReattach_RuleChangeMergesTheEmptiedListing(t *testing.T) {
	s := newTestStore(t)
	old := listing.Posting{SourceListingID: "r1", Company: "Shield AI", Title: "Engineer 2 New Grad",
		JobType: listing.Internship, Season: "Summer 2027", ApplyURL: "https://x.com/jobs/r1", Active: true}
	saveFrom(t, s, "simplify", old)
	oldID := scalar[int64](t, s, `SELECT id FROM listings`)
	previousRun(t, s)

	// The new rule says it's a new-grad role: a different key, so a new listing.
	now := old
	now.JobType = listing.NewGrad
	saveFrom(t, s, "simplify", now)

	rep := reattach(t, s, "simplify")
	if rep.Moved != 1 || rep.Merged != 1 {
		t.Errorf("report = %+v, want 1 moved and 1 merged", rep)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings WHERE id = $1`, oldID); n != 0 {
		t.Error("the old, emptied listing is still there")
	}
	if jt := scalar[string](t, s, `SELECT job_type FROM listings`); jt != "new_grad" {
		t.Errorf("remaining listing is %s, want new_grad", jt)
	}
}

func TestReattach_ListingWithOtherPostingsStays(t *testing.T) {
	s := newTestStore(t)
	a := listing.Posting{SourceListingID: "a", Company: "Acme", Title: "SWE Intern", JobType: listing.Internship,
		Season: "Summer 2027", ApplyURL: "https://acme.com/jobs/a1234", Active: true}
	b := a
	b.SourceListingID, b.ApplyURL = "b", "https://acme.com/jobs/b1234"
	saveFrom(t, s, "simplify", a, b) // one listing, two postings
	previousRun(t, s)

	moved := a
	moved.Title = "SWE Intern, Payments" // a rule change gives "a" a different key
	saveFrom(t, s, "simplify", moved)

	if rep := reattach(t, s, "simplify"); rep.Moved != 1 || rep.Merged != 0 {
		t.Errorf("report = %+v, want 1 moved, 0 merged (the old listing still has b)", rep)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 2 {
		t.Errorf("listings = %d, want 2", n)
	}
}

func TestReattach_SameAdInTwoSeasonsIsNotAMove(t *testing.T) {
	s := newTestStore(t)
	summer := listing.Posting{SourceListingID: "x", Company: "Acme", Title: "SWE Intern", JobType: listing.Internship,
		Season: "Summer 2027", ApplyURL: "https://acme.com/careers", Active: true}
	saveFrom(t, s, "simplify", summer)
	previousRun(t, s)
	fall := summer
	fall.Season = "Fall 2027"
	saveFrom(t, s, "simplify", fall) // same record id, another season

	if rep := reattach(t, s, "simplify"); rep.Moved != 0 {
		t.Errorf("report = %+v; a second season is a second listing, not a move", rep)
	}
}

func TestDB_KeptApartPairIsUniqueAndOrdered(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	fall := simplifyFigma
	fall.Season, fall.SourceListingID = "Fall 2027", "s2"
	saveFrom(t, s, "simplify", fall)
	ids := scalar[[]int64](t, s, `SELECT array_agg(id ORDER BY id) FROM listings`)

	q := `INSERT INTO listings_kept_apart (listing_a, listing_b, reason) VALUES ($1, $2, 'test')`
	if err := exec(t, s, q, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, ids[0], ids[1]))
	if err := exec(t, s, q, ids[1], ids[0]); err == nil {
		t.Error("pair stored larger id first; the CHECK should require a < b, so each pair has one row")
	}
}

// --- Slice 8: new since you last looked ---

func TestViewFeed_Boundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid := scalar[int64](t, s, `INSERT INTO users (email, password_hash) VALUES ('a@b.co', 'x') RETURNING id`)
	dbNow := scalar[time.Time](t, s, `SELECT now()`) // fixed for the whole test transaction
	view := func() time.Time {
		t.Helper()
		since, err := s.ViewFeed(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		return since
	}

	if got := view(); !got.Equal(dbNow.Add(-FirstVisitWindow)) {
		t.Errorf("first visit: new since %v, want 24 hours ago", got)
	}
	if got := view(); !got.Equal(dbNow.Add(-FirstVisitWindow)) {
		t.Errorf("refresh: new since %v; the boundary must not move mid-visit", got)
	}

	// The last page view was 2 hours ago: a new visit starts, and
	// everything up to then counts as seen.
	exec(t, s, `UPDATE users SET last_feed_view_at = now() - interval '2 hours'`)
	if got := view(); !got.Equal(dbNow.Add(-2 * time.Hour)) {
		t.Errorf("back after 2h: new since %v, want 2 hours ago", got)
	}

	// 20 minutes away is the same visit.
	exec(t, s, `UPDATE users SET last_feed_view_at = now() - interval '20 minutes'`)
	if got := view(); !got.Equal(dbNow.Add(-2 * time.Hour)) {
		t.Errorf("back after 20m: new since %v; under 30 minutes is the same visit", got)
	}

	if err := s.MarkAllSeen(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if got := view(); !got.Equal(dbNow) {
		t.Errorf("after Mark all as seen: new since %v, want now", got)
	}
	if got, ok, _ := s.FeedNewSince(ctx, uid); !ok || !got.Equal(dbNow) {
		t.Errorf("FeedNewSince = %v, %v", got, ok)
	}
}

func TestFeed_OnlyNewAndCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	add := func(title string, daysAgo int) {
		t.Helper()
		l := listing.Group([]listing.Posting{{SourceListingID: title, Company: "Acme", Title: title,
			JobType: listing.Internship, Season: "Summer 2027", ApplyURL: "https://acme.com/c", Active: true,
			PostedAt: time.Now().AddDate(0, 0, -daysAgo)}})[0]
		simplifyID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)
		if _, err := s.SaveListing(ctx, simplifyID, l, true); err != nil { // first load: feed_at = posted date
			t.Fatal(err)
		}
	}
	add("Old Intern", 5)
	add("Yesterday Intern", 1)
	add("Today Intern", 0)

	since := time.Now().Add(-36 * time.Hour)
	q := FeedQuery{JobType: listing.Internship, Limit: 10, NewSince: &since, OnlyNew: true}
	feed, _ := s.Feed(ctx, q)
	if len(feed) != 2 || feed[0].Title != "Today Intern" {
		t.Errorf("only new = %d listings starting %q; want Today, Yesterday", len(feed), feed[0].Title)
	}
	if n, _ := s.CountFeed(ctx, q); n != 2 {
		t.Errorf("CountFeed(only new) = %d, want 2", n)
	}
	q.OnlyNew = false
	if n, _ := s.CountFeed(ctx, q); n != 3 {
		t.Errorf("CountFeed(all) = %d, want 3", n)
	}
	q.NewSince, q.OnlyNew = nil, true // logged out: "only new" means nothing
	if n, _ := s.CountFeed(ctx, q); n != 3 {
		t.Errorf("CountFeed(logged out) = %d, want 3", n)
	}
}

// --- Slice 9: applications ---

func newUser(t *testing.T, s *Store, email string) int64 {
	t.Helper()
	return scalar[int64](t, s, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email)
}

func aListing(t *testing.T, s *Store) int64 {
	t.Helper()
	saveFrom(t, s, "simplify", simplifyFigma)
	return scalar[int64](t, s, `SELECT id FROM listings ORDER BY id DESC LIMIT 1`)
}

func TestDB_OneApplicationPerUserAndListing(t *testing.T) {
	s := newTestStore(t)
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	q := `INSERT INTO applications (user_id, listing_id, stage) VALUES ($1, $2, 'saved')`
	if err := exec(t, s, q, uid, lid); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, uid, lid))
}

func TestDB_ResumeLabelIsUniquePerUserIgnoringCase(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, s, "a@b.co")
	q := `INSERT INTO resume_versions (user_id, label) VALUES ($1, $2)`
	if err := exec(t, s, q, uid, "Backend v2"); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, uid, "backend V2"))
	if err := exec(t, s, q, newUser(t, s, "other@b.co"), "Backend v2"); err != nil {
		t.Errorf("another user can use the same label: %v", err)
	}
}

func TestDB_ApplicationRules(t *testing.T) {
	s := newTestStore(t)
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	other := newUser(t, s, "other@b.co")
	theirResume := scalar[int64](t, s, `INSERT INTO resume_versions (user_id, label) VALUES ($1, 'theirs') RETURNING id`, other)

	for _, bad := range []struct{ why, sql string }{
		{"interviewing without a round", `INSERT INTO applications (user_id, listing_id, stage, applied_on) VALUES ($1, $2, 'interview', CURRENT_DATE)`},
		{"applied without a date", `INSERT INTO applications (user_id, listing_id, stage) VALUES ($1, $2, 'applied')`},
		{"an unknown stage", `INSERT INTO applications (user_id, listing_id, stage) VALUES ($1, $2, 'ghosted')`},
	} {
		if err := exec(t, s, bad.sql, uid, lid); err == nil {
			t.Errorf("accepted %s", bad.why)
		}
	}
	err := exec(t, s, `INSERT INTO applications (user_id, listing_id, stage, resume_version_id) VALUES ($1, $2, 'saved', $3)`,
		uid, lid, theirResume)
	if err == nil {
		t.Error("accepted another user's resume version")
	}
}

func TestApplication_DoubleClickAndHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	today := time.Now()

	id, created, err := s.StartApplication(ctx, uid, lid, StageApplied, today)
	if err != nil || !created {
		t.Fatalf("first click: id=%d created=%v err=%v", id, created, err)
	}
	again, created, err := s.StartApplication(ctx, uid, lid, StageApplied, today)
	if err != nil || created || again != id {
		t.Errorf("second click: id=%d created=%v err=%v; want the same application, not a new one", again, created, err)
	}

	two := 2
	s.MoveStage(ctx, uid, id, StageInterview, nil, time.Now())  // round 1
	s.MoveStage(ctx, uid, id, StageInterview, nil, time.Now())  // next round: 2
	s.MoveStage(ctx, uid, id, StageInterview, &two, time.Now()) // same round again: not a move
	s.MoveStage(ctx, uid, id, StageRejected, nil, time.Now())

	app, events, err := s.Application(ctx, uid, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		step := string(e.To)
		if e.InterviewRound != nil {
			step += fmt.Sprint(*e.InterviewRound)
		}
		got = append(got, step)
	}
	if strings.Join(got, ",") != "applied,interview1,interview2,rejected" {
		t.Errorf("history = %v", got)
	}
	if app.Stage != StageRejected || app.InterviewRound != nil {
		t.Errorf("now %s round %v; want rejected with no round", app.Stage, app.InterviewRound)
	}
}

func TestApplication_OtherUsersCantTouchIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, stranger, lid := newUser(t, s, "a@b.co"), newUser(t, s, "x@b.co"), aListing(t, s)
	id, _, _ := s.StartApplication(ctx, owner, lid, StageSaved, time.Now())

	if _, _, err := s.Application(ctx, stranger, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("read: err = %v, want ErrNotFound", err)
	}
	if err := s.MoveStage(ctx, stranger, id, StageOffer, nil, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("move: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteApplication(ctx, stranger, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: err = %v, want ErrNotFound", err)
	}
}

func TestApplication_SavedThenApplied(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	id, _, _ := s.StartApplication(ctx, uid, lid, StageSaved, time.Now())
	if err := s.MoveStage(ctx, uid, id, StageApplied, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, _, _ := s.Application(ctx, uid, id)
	if app.AppliedOn == nil {
		t.Error("moving past saved should set the applied date (to today)")
	}

	lastWeek := time.Now().AddDate(0, 0, -7).Truncate(24 * time.Hour)
	resume, _ := s.AddResumeVersion(ctx, uid, "  Backend   v2 ")
	sameResume, _ := s.AddResumeVersion(ctx, uid, "backend v2")
	if resume != sameResume {
		t.Error("the same label (different case/spacing) made two resume versions")
	}
	if err := s.UpdateDetails(ctx, uid, id, Details{AppliedOn: &lastWeek, ResumeVersionID: &resume, Notes: "used the Greenhouse link"}); err != nil {
		t.Fatal(err)
	}
	app, _, _ = s.Application(ctx, uid, id)
	if app.ResumeLabel != "Backend v2" || app.Notes != "used the Greenhouse link" || app.AppliedOn.Format("2006-01-02") != lastWeek.Format("2006-01-02") {
		t.Errorf("details = %+v", app)
	}
}

// The merge rule promised in slice 8, now that applications exist.
func TestReattach_SameUserAppliedToBothKeepsThemApart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid := newUser(t, s, "a@b.co")

	// Listing A has posting "r1"; listing B (same job type and season) has "r2".
	a := listing.Posting{SourceListingID: "r1", Company: "Shield AI", Title: "Engineer 2 New Grad",
		JobType: listing.NewGrad, Season: "Summer 2027", ApplyURL: "https://x.com/careers", Active: true}
	b := a
	b.SourceListingID, b.Title = "r2", "Engineer II - New Grad"
	saveFrom(t, s, "simplify", a)
	saveFrom(t, s, "simplify", b)
	ids := scalar[[]int64](t, s, `SELECT array_agg(id ORDER BY id) FROM listings`)
	s.StartApplication(ctx, uid, ids[0], StageApplied, time.Now())
	s.StartApplication(ctx, uid, ids[1], StageApplied, time.Now())
	previousRun(t, s)

	// A rule change now gives r1 the same key as B: r1 moves, A is emptied.
	moved := a
	moved.Title = b.Title
	saveFrom(t, s, "simplify", moved)

	rep := reattach(t, s, "simplify")
	if rep.Merged != 0 || rep.KeptApart != 1 {
		t.Fatalf("report = %+v, want kept apart, not merged", rep)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM applications`); n != 2 {
		t.Errorf("applications = %d, want both kept", n)
	}
	if pairs, _ := s.KeptApartPairs(ctx); len(pairs) != 1 {
		t.Errorf("possible-duplicates report pairs = %v, want this one", pairs)
	}
}

func TestReattach_MergeMovesApplications(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid := newUser(t, s, "a@b.co")
	old := listing.Posting{SourceListingID: "r1", Company: "Shield AI", Title: "Engineer 2 New Grad",
		JobType: listing.Internship, Season: "Summer 2027", ApplyURL: "https://x.com/careers", Active: true}
	saveFrom(t, s, "simplify", old)
	oldID := scalar[int64](t, s, `SELECT id FROM listings`)
	appID, _, _ := s.StartApplication(ctx, uid, oldID, StageApplied, time.Now())
	previousRun(t, s)

	now := old
	now.JobType = listing.NewGrad
	saveFrom(t, s, "simplify", now)
	if rep := reattach(t, s, "simplify"); rep.Merged != 1 {
		t.Fatalf("report = %+v, want merged", rep)
	}
	app, _, err := s.Application(ctx, uid, appID)
	if err != nil || app.ListingID == oldID {
		t.Errorf("application still points at the deleted listing (%v)", err)
	}
}

func TestFeed_HidesWhatYouAppliedTo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid, other := newUser(t, s, "a@b.co"), newUser(t, s, "b@b.co")
	applied, saved := aListing(t, s), func() int64 {
		other := simplifyFigma // a different role, so it's its own card
		other.Title, other.SourceListingID, other.ApplyURL = "Product Design Intern", "s2", "https://figma.com/careers"
		saveFrom(t, s, "simplify", other)
		return scalar[int64](t, s, `SELECT max(id) FROM listings`)
	}()
	s.StartApplication(ctx, uid, applied, StageApplied, time.Now())
	s.StartApplication(ctx, uid, saved, StageSaved, time.Now())

	count := func(q FeedQuery) int {
		q.JobType, q.Limit = listing.Internship, 10
		n, err := s.CountFeed(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(FeedQuery{UserID: uid}); n != 1 {
		t.Errorf("my feed shows %d, want 1 (applied hidden, saved shown)", n)
	}
	if n := count(FeedQuery{UserID: uid, ShowApplied: true}); n != 2 {
		t.Errorf("with Show applied: %d, want 2", n)
	}
	if n := count(FeedQuery{UserID: other}); n != 2 {
		t.Errorf("someone else's feed shows %d, want 2 (my applications are mine)", n)
	}
	if n := count(FeedQuery{}); n != 2 {
		t.Errorf("logged out: %d, want 2", n)
	}

	feed, _ := s.Feed(ctx, FeedQuery{JobType: listing.Internship, Limit: 10, UserID: uid, ShowApplied: true})
	stages := map[string]bool{}
	for _, l := range feed {
		if l.Stage != nil {
			stages[*l.Stage] = true
		}
	}
	if !stages["applied"] || !stages["saved"] {
		t.Errorf("feed cards should carry my stage, got %v", stages)
	}
}

func TestApplication_AppliedOnASavedCard(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	id, _, _ := s.StartApplication(ctx, uid, lid, StageSaved, time.Now())

	again, created, err := s.StartApplication(ctx, uid, lid, StageApplied, time.Now())
	if err != nil || created || again != id {
		t.Fatalf("id=%d created=%v err=%v; want the saved one", again, created, err)
	}
	app, events, _ := s.Application(ctx, uid, id)
	if app.Stage != StageApplied || len(events) != 2 {
		t.Errorf("stage %s with %d events; want applied, saved->applied recorded", app.Stage, len(events))
	}

	// A late double-click must not move an interview back to applied.
	s.MoveStage(ctx, uid, id, StageInterview, nil, time.Now())
	s.StartApplication(ctx, uid, lid, StageApplied, time.Now())
	if app, _, _ := s.Application(ctx, uid, id); app.Stage != StageInterview {
		t.Errorf("stage %s; a repeated click moved it backwards", app.Stage)
	}
}

func TestTimezone_StoredAndUpdated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	token, _ := s.CreateInvite(ctx, "", 1, time.Hour)
	u, err := s.SignUp(ctx, token, "a@b.co", "h", "America/Los_Angeles")
	if err != nil || u.Timezone != "America/Los_Angeles" {
		t.Fatalf("SignUp = %+v, %v", u, err)
	}
	if err := s.SetTimezone(ctx, u.ID, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	session, _, _ := s.CreateSession(ctx, u.ID)
	if got, _, _, _, _ := s.SessionUser(ctx, session); got.Timezone != "Asia/Tokyo" {
		t.Errorf("session user timezone = %q, want Asia/Tokyo", got.Timezone)
	}
}

func TestDB_ConnectionsUseUTC(t *testing.T) {
	s := newTestStore(t)
	if tz := scalar[string](t, s, `SELECT current_setting('TimeZone')`); tz != "UTC" {
		t.Errorf("connection timezone = %q, want UTC whatever the server's setting", tz)
	}
}

func TestMoveStage_AppliedDateIsTheUsersToday(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	uid, lid := newUser(t, s, "a@b.co"), aListing(t, s)
	id, _, _ := s.StartApplication(ctx, uid, lid, StageSaved, time.Now())

	// 11pm on Nov 8 in New York is already Nov 9 in UTC (and in Nairobi).
	ny, _ := time.LoadLocation("America/New_York")
	lateEvening := time.Date(2026, 11, 8, 23, 0, 0, 0, ny)
	if err := s.MoveStage(ctx, uid, id, StageApplied, nil, lateEvening); err != nil {
		t.Fatal(err)
	}
	app, _, _ := s.Application(ctx, uid, id)
	if got := app.AppliedOn.Format("2006-01-02"); got != "2026-11-08" {
		t.Errorf("applied on %s, want 2026-11-08 (the user's date)", got)
	}
}

// --- Slice 10: link checks ---

func postingID(t *testing.T, s *Store, source string) int64 {
	t.Helper()
	return scalar[int64](t, s, `SELECT ls.id FROM listing_sources ls JOIN sources src ON src.id = ls.source_id
		WHERE src.name = $1 ORDER BY ls.id LIMIT 1`, source)
}

func check(t *testing.T, s *Store, id int64, r LinkResult) bool {
	t.Helper()
	closed, err := s.RecordLinkCheck(context.Background(), id, r, "test")
	if err != nil {
		t.Fatal(err)
	}
	return closed
}

func TestLinkCheck_ThreeDeadInARowCloses(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	id := postingID(t, s, "simplify")

	check(t, s, id, LinkDead)
	check(t, s, id, LinkUnknown) // a timeout in between neither counts nor resets
	if closed := check(t, s, id, LinkDead); closed {
		t.Fatal("closed after 2 dead results; it takes 3")
	}
	if closed := check(t, s, id, LinkDead); !closed {
		t.Fatal("not closed after 3 dead results")
	}
	if got := scalar[string](t, s, `SELECT status || '/' || closed_reason FROM listings`); got != "closed/link" {
		t.Errorf("listing = %s, want closed/link", got)
	}

	// The job comes back (re-posted at the same link): open again.
	check(t, s, id, LinkAlive)
	if got := scalar[string](t, s, `SELECT status || '/' || COALESCE(closed_reason, '-') FROM listings`); got != "open/-" {
		t.Errorf("after an alive check: %s, want open/-", got)
	}
}

func TestLinkCheck_AliveResetsTheCount(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	id := postingID(t, s, "simplify")
	check(t, s, id, LinkDead)
	check(t, s, id, LinkDead)
	check(t, s, id, LinkAlive)
	check(t, s, id, LinkDead)
	if got := scalar[string](t, s, `SELECT status FROM listings`); got != StatusOpen {
		t.Errorf("status %s: dead, dead, alive, dead is not 3 in a row", got)
	}
}

func TestLinkCheck_AnotherWorkingLinkKeepsItOpen(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	other := speedyPosting("Software Engineer Intern", "https://jobs.ashbyhq.com/figma/3fba1c39-c5cb-47d7-9ad2-1cec4d7e9d0c")
	other.Season = "Summer 2027" // same key as the Simplify posting, so the same listing
	saveFrom(t, s, "speedyapply", other)
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 1 {
		t.Fatalf("listings = %d; the test needs both postings on one listing", n)
	}
	id := postingID(t, s, "simplify")
	for range DeadAfter {
		check(t, s, id, LinkDead)
	}
	if got := scalar[string](t, s, `SELECT status FROM listings`); got != StatusOpen {
		t.Errorf("status %s; SpeedyApply's link still works, so it stays open", got)
	}
}

func TestLinkCheck_SourceClosedReason(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	closed := simplifyFigma
	closed.Active = false
	saveFrom(t, s, "simplify", closed)
	if got := scalar[string](t, s, `SELECT closed_reason FROM listings`); got != "source" {
		t.Errorf("closed_reason = %s, want source", got)
	}
}

func TestLinkCheck_NewApplyLinkStartsClean(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	id := postingID(t, s, "simplify")
	for range DeadAfter {
		check(t, s, id, LinkDead)
	}
	moved := simplifyFigma
	moved.ApplyURL = "https://job-boards.greenhouse.io/figma/jobs/6241542004?gh_src=new" // same job, new URL
	saveFrom(t, s, "simplify", moved)
	if got := scalar[string](t, s, `SELECT ls.link_failures || '/' || (ls.link_dead_at IS NULL) || '/' || l.status
		FROM listing_sources ls JOIN listings l ON l.id = ls.listing_id`); got != "0/true/open" {
		t.Errorf("after the source changed the link: %s, want 0/true/open", got)
	}
}

func TestDueLinks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	saveFrom(t, s, "simplify", simplifyFigma) // job-specific link: due
	generic := simplifyFigma
	generic.SourceListingID, generic.ApplyURL, generic.Season = "g", "https://figma.com/careers", "Fall 2027"
	saveFrom(t, s, "simplify", generic) // generic link: never checked

	due, err := s.DueLinks(ctx, 10)
	if err != nil || len(due) != 1 || !strings.Contains(due[0].URL, "6241542004") {
		t.Fatalf("due = %+v, %v; want only the job-specific link", due, err)
	}
	check(t, s, due[0].PostingID, LinkAlive)
	if due, _ := s.DueLinks(ctx, 10); len(due) != 0 {
		t.Errorf("checked just now, still due: %+v", due)
	}
}

func TestDB_ClosedReasonMatchesStatus(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	if err := exec(t, s, `UPDATE listings SET closed_reason = 'link'`); err == nil {
		t.Error("an open listing with a closed reason was accepted")
	}
}

// --- Slice 11: seasons grouped into one card, search ---

func TestFeed_GroupsSeasonsBeforePaging(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	add := func(title, season string, daysAgo int) {
		t.Helper()
		p := listing.Posting{SourceListingID: title + season, Company: "Tesla", Title: title, JobType: listing.Internship,
			Season: season, ApplyURL: "https://tesla.com/careers", Active: true, PostedAt: time.Now().AddDate(0, 0, -daysAgo)}
		l := listing.Group([]listing.Posting{p})[0]
		simplifyID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)
		if _, err := s.SaveListing(ctx, simplifyID, l, true); err != nil {
			t.Fatal(err)
		}
	}
	add("Software Engineer Intern", "Summer 2027", 1)
	add("Software Engineer Intern", "Spring 2027", 3)
	add("Software Engineer Intern", "Winter 2027", 9)
	add("Data Intern", "Summer 2027", 2)

	q := FeedQuery{JobType: listing.Internship, Limit: 1}
	page1, _ := s.Feed(ctx, q)
	if len(page1) != 3 || page1[0].Season != "Summer 2027" || page1[0].GroupKey != page1[2].GroupKey {
		t.Fatalf("page 1 should be the whole Tesla SWE card, newest season first; got %d listings", len(page1))
	}
	q.Offset = 1
	page2, _ := s.Feed(ctx, q)
	if len(page2) != 1 || page2[0].Title != "Data Intern" {
		t.Errorf("page 2 should be the Data Intern card, got %d listings", len(page2))
	}
	if n, _ := s.CountFeed(ctx, FeedQuery{JobType: listing.Internship}); n != 2 {
		t.Errorf("count = %d, want 2 cards (not 4 listings)", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM listings`); n != 4 {
		t.Errorf("listings = %d; grouping must not merge them", n)
	}
}

func TestFeed_Search(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	saveFrom(t, s, "simplify", simplifyFigma) // Figma, Software Engineer Intern
	odd := simplifyFigma
	odd.Company, odd.Title, odd.SourceListingID, odd.ApplyURL = "100% Labs", "Growth_Intern", "o", "https://x.com/careers"
	saveFrom(t, s, "simplify", odd)

	count := func(search string) int {
		t.Helper()
		n, err := s.CountFeed(ctx, FeedQuery{JobType: listing.Internship, Search: search})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	tests := map[string]int{"figma": 1, "SOFTWARE eng": 1, "100%": 1, "100": 1, "%": 1, "growth_": 1, "_": 1, "": 2, "nothing like this": 0}
	for q, want := range tests {
		if got := count(q); got != want {
			t.Errorf("search %q matched %d, want %d", q, got, want)
		}
	}
}

func TestSignUp_WithoutInvite(t *testing.T) {
	s := newTestStore(t)
	u, err := s.SignUp(context.Background(), "", "open@b.co", "h", "America/New_York")
	if err != nil || u.ID == 0 {
		t.Fatalf("open sign-up: %+v %v", u, err)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM users WHERE invite_id IS NULL`); n != 1 {
		t.Errorf("users without an invite = %d, want 1", n)
	}
}

// --- Old discoveries don't jump to the top ---

func TestFeed_OldDiscoverySortsByPostedDate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	simplifyID := scalar[int64](t, s, `SELECT id FROM sources WHERE name = 'simplify'`)
	save := func(title string, postedDaysAgo int, firstLoad bool) {
		t.Helper()
		var posted time.Time
		if postedDaysAgo >= 0 {
			posted = time.Now().AddDate(0, 0, -postedDaysAgo)
		}
		l := listing.Group([]listing.Posting{{SourceListingID: title, Company: "Acme", Title: title,
			JobType: listing.Internship, Season: "Summer 2027", ApplyURL: "https://acme.com/careers", Active: true, PostedAt: posted}})[0]
		if _, err := s.SaveListing(ctx, simplifyID, l, firstLoad); err != nil {
			t.Fatal(err)
		}
	}
	// From the first ingest: sorted by posted date.
	save("Posted Yesterday", 1, true)
	// Discovered today by a later run (not a first load):
	save("Posted 69 Days Ago", 69, false) // older than 14 days when found: sorts by its posted date
	save("Posted 10 Days Ago", 10, false) // found within 14 days: still sorts as newly found (top)
	save("No Posted Date", -1, false)     // no date: first-seen, as before (top)

	feed, err := s.Feed(ctx, FeedQuery{JobType: listing.Internship, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, l := range feed {
		pos[l.Title] = i
	}
	if pos["Posted 69 Days Ago"] < pos["Posted Yesterday"] {
		t.Errorf("a 69-day-old role found today sorts above one posted yesterday: order %v", pos)
	}
	if pos["Posted 10 Days Ago"] > pos["Posted Yesterday"] || pos["No Posted Date"] > pos["Posted Yesterday"] {
		t.Errorf("recent or undated discoveries should still sort as newly found: order %v", pos)
	}

	// Still "New for you": it's new to the user even though it's old.
	since := time.Now().Add(-time.Hour)
	newOnes, _ := s.Feed(ctx, FeedQuery{JobType: listing.Internship, Limit: 10, OnlyNew: true, NewSince: &since})
	found := false
	for _, l := range newOnes {
		found = found || l.Title == "Posted 69 Days Ago"
	}
	if !found {
		t.Error("the 69-day-old discovery should still be New for you")
	}
}

// --- Top companies ---

func TestDB_TopSignalIsUniquePerCompany(t *testing.T) {
	s := newTestStore(t)
	saveFrom(t, s, "simplify", simplifyFigma)
	id := scalar[int64](t, s, `SELECT id FROM companies`)
	q := `INSERT INTO company_top_signals (company_id, signal) VALUES ($1, 'simplify-readme')`
	if err := exec(t, s, q, id); err != nil {
		t.Fatal(err)
	}
	requireUniqueViolation(t, exec(t, s, q, id))
}

func TestSyncTopCompanies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	saveFrom(t, s, "simplify", simplifyFigma) // company "Figma"
	other := simplifyFigma
	other.Company, other.SourceListingID, other.ApplyURL = "Acme", "a", "https://acme.com/careers"
	saveFrom(t, s, "simplify", other)

	if n, err := s.SyncTopCompanies(ctx, "simplify-readme", []string{"🔥 Figma", "Unknown Co"}); err != nil || n != 1 {
		t.Fatalf("simplify sync = %d, %v; want Figma only (Unknown Co has no listings)", n, err)
	}
	s.SyncTopCompanies(ctx, "speedyapply-faang", []string{"Figma, Inc."})

	// Simplify drops its marker: Figma stays top through SpeedyApply.
	s.SyncTopCompanies(ctx, "simplify-readme", nil)
	if n := scalar[int](t, s, `SELECT count(*) FROM company_top_signals t JOIN companies c ON c.id = t.company_id WHERE c.normalized_name = 'figma'`); n != 1 {
		t.Errorf("Figma has %d signals, want 1 (SpeedyApply's)", n)
	}

	// The filter and the card flag.
	top, _ := s.Feed(ctx, FeedQuery{JobType: listing.Internship, Limit: 10, TopOnly: true})
	if len(top) != 1 || top[0].Company != "Figma" || !top[0].TopCompany {
		t.Errorf("top-only feed = %+v, want just Figma, flagged", top)
	}
	if n, _ := s.CountFeed(ctx, FeedQuery{JobType: listing.Internship}); n != 2 {
		t.Errorf("without the filter: %d, want 2", n)
	}
}
