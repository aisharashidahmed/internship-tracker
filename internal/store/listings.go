package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"internship-tracker/internal/listing"
)

// Outcome says what happened to a listing during a run, so ingestion can
// report counts.
type Outcome string

const (
	OutcomeNew       Outcome = "new"       // first time we've seen this job
	OutcomeUpdated   Outcome = "updated"   // a posting was added or its link, cities or status changed
	OutcomeUnchanged Outcome = "unchanged" // seen again, nothing different
	OutcomeClosed    Outcome = "closed"    // a source now says it is closed
	OutcomeStale     Outcome = "stale"     // was open; its open postings vanished from their source
	OutcomeIgnored   Outcome = "ignored"   // inactive and never stored, so not worth adding
)

// Listing statuses, as stored in listings.status.
const (
	StatusOpen   = "open"
	StatusStale  = "stale"  // no source says closed, but nothing open is still listed: "may be closed"
	StatusClosed = "closed" // a source explicitly said closed
)

// SaveListing records one listing from one source. Running it twice with the
// same input changes nothing the second time except last_seen_at.
func (s *Store) SaveListing(ctx context.Context, sourceID int64, l listing.Listing) (Outcome, error) {
	listingID, oldStatus, err := s.findOrCreateListing(ctx, l)
	if err != nil {
		return "", err
	}
	if listingID == 0 {
		return OutcomeIgnored, nil
	}

	postingsChanged := false
	for _, p := range l.Postings {
		changed, err := s.upsertPosting(ctx, listingID, sourceID, p)
		if err != nil {
			return "", err
		}
		postingsChanged = postingsChanged || changed
	}

	_, newStatus, err := s.refreshStatus(ctx, listingID)
	if err != nil {
		return "", err
	}

	switch {
	case oldStatus == "":
		return OutcomeNew, nil
	case oldStatus != StatusClosed && newStatus == StatusClosed:
		return OutcomeClosed, nil
	case postingsChanged || oldStatus != newStatus:
		return OutcomeUpdated, nil
	default:
		return OutcomeUnchanged, nil
	}
}

// findOrCreateListing returns the listing's id and its status before this
// run ("" if it is brand new). It looks the job up in this order:
//
//  1. by job link: another posting (from any source) has the same apply link
//  2. by dedupe key: same company + title + job type + season
//  3. not found: create it, but only if a posting is active (we only start
//     tracking open jobs); otherwise return id 0.
func (s *Store) findOrCreateListing(ctx context.Context, l listing.Listing) (id int64, oldStatus string, err error) {
	id, oldStatus, err = s.findByJobLink(ctx, l)
	if err != nil || id != 0 {
		return id, oldStatus, err
	}

	if !l.AnyActive() {
		// Only touch a listing we already have (so it can be marked closed).
		err = s.db.QueryRow(ctx,
			`UPDATE listings SET last_seen_at = now() WHERE dedupe_key = $1 RETURNING id, status`,
			l.Key,
		).Scan(&id, &oldStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", nil
		}
		if err != nil {
			return 0, "", fmt.Errorf("touch listing %q: %w", l.Key, err)
		}
		return id, oldStatus, nil
	}

	companyID, err := s.upsertCompany(ctx, l.Company)
	if err != nil {
		return 0, "", err
	}

	// The CTE "old" reads the row as it was before this statement, which
	// tells us whether the INSERT created it or the ON CONFLICT updated it.
	var old *string
	err = s.db.QueryRow(ctx, `
		WITH old AS (SELECT status FROM listings WHERE dedupe_key = $1)
		INSERT INTO listings (dedupe_key, company_id, title, job_type, season, degree_level)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (dedupe_key) DO UPDATE SET last_seen_at = now()
		RETURNING id, (SELECT status FROM old)`,
		l.Key, companyID, l.Title, l.JobType, l.Season, l.DegreeLevel,
	).Scan(&id, &old)
	if err != nil {
		return 0, "", fmt.Errorf("upsert listing %q: %w", l.Key, err)
	}
	if old != nil {
		oldStatus = *old
	}
	return id, oldStatus, nil
}

// findByJobLink finds the stored listing that already has a posting with
// one of l's job links. It returns id 0 when there is no match, and also
// when the links point at more than one listing (e.g. the same ad stored
// for Summer and Fall, and l doesn't say which season): when unsure, keep
// the posting separate.
func (s *Store) findByJobLink(ctx context.Context, l listing.Listing) (id int64, status string, err error) {
	var links []string
	for _, p := range l.Postings {
		if link, ok := listing.JobLink(p.ApplyURL); ok {
			links = append(links, link)
		}
	}
	if len(links) == 0 {
		return 0, "", nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT li.id, li.status, li.degree_level
		FROM listing_sources ls JOIN listings li ON li.id = ls.listing_id
		WHERE ls.job_link = ANY($1)
		  AND li.job_type = $2
		  AND ($3 = '' OR li.season = $3) -- an unknown season can match any season`,
		links, l.JobType, l.Season)
	if err != nil {
		return 0, "", fmt.Errorf("find listing by job link: %w", err)
	}
	type match struct {
		id             int64
		status, degree string
	}
	matches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (match, error) {
		var m match
		return m, row.Scan(&m.id, &m.status, &m.degree)
	})
	if err != nil {
		return 0, "", fmt.Errorf("find listing by job link: %w", err)
	}
	if len(matches) != 1 {
		return 0, "", nil
	}

	m := matches[0]
	degree := listing.MergeDegree(listing.DegreeLevel(m.degree), l.DegreeLevel)
	_, err = s.db.Exec(ctx,
		`UPDATE listings SET last_seen_at = now(), degree_level = $2 WHERE id = $1`, m.id, degree)
	if err != nil {
		return 0, "", fmt.Errorf("touch listing %d: %w", m.id, err)
	}
	return m.id, m.status, nil
}

// upsertCompany returns the id of the company, creating it if needed.
func (s *Store) upsertCompany(ctx context.Context, name string) (int64, error) {
	var id int64
	// The DO UPDATE changes nothing; it is there so RETURNING gives us the
	// existing row's id (DO NOTHING would return no row).
	err := s.db.QueryRow(ctx, `
		INSERT INTO companies (name, normalized_name) VALUES ($1, $2)
		ON CONFLICT (normalized_name) DO UPDATE SET normalized_name = EXCLUDED.normalized_name
		RETURNING id`,
		name, listing.NormalizeCompany(name),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert company %q: %w", name, err)
	}
	return id, nil
}

// upsertPosting saves one posting and reports whether anything about it is
// new or different (link, cities, active flag, or it was missing and is back).
func (s *Store) upsertPosting(ctx context.Context, listingID, sourceID int64, p listing.Posting) (changed bool, err error) {
	locations := p.Locations
	if locations == nil {
		locations = []string{} // the column is NOT NULL; a nil slice would be sent as NULL
	}
	var jobLink *string // NULL for generic links
	if link, ok := listing.JobLink(p.ApplyURL); ok {
		jobLink = &link
	}

	err = s.db.QueryRow(ctx, `
		WITH old AS (
			SELECT apply_url, locations, active, missing_since FROM listing_sources
			WHERE listing_id = $1 AND source_id = $2 AND source_listing_id = $3
		)
		INSERT INTO listing_sources (listing_id, source_id, source_listing_id, apply_url, locations, active, job_link)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (listing_id, source_id, source_listing_id) DO UPDATE SET
			apply_url     = EXCLUDED.apply_url,
			locations     = EXCLUDED.locations,
			active        = EXCLUDED.active,
			job_link      = EXCLUDED.job_link,
			missing_since = NULL, -- we just saw it
			last_seen_at  = now()
		RETURNING NOT EXISTS (
			SELECT 1 FROM old
			WHERE (old.apply_url, old.locations, old.active) = ($4, $5::text[], $6)
			  AND old.missing_since IS NULL
		)`,
		listingID, sourceID, p.SourceListingID, p.ApplyURL, locations, p.Active, jobLink,
	).Scan(&changed)
	if err != nil {
		return false, fmt.Errorf("upsert posting %q: %w", p.SourceListingID, err)
	}
	return changed, nil
}

// MarkMissing records that a source's latest file no longer lists some
// postings: every posting of sourceID whose id is not in seen gets
// missing_since ("not seen since"), unless it already has one. It then
// recomputes those listings' status and returns how many went open -> stale.
//
// Call it only after a successful, complete parse, in the same transaction
// as the saves; a partial file would make real jobs look stale.
func (s *Store) MarkMissing(ctx context.Context, sourceID int64, seen []string) (becameStale int, err error) {
	if seen == nil {
		// A nil slice is sent as NULL, and "NOT (x = ANY(NULL))" is never
		// true in SQL, so nothing would be marked. An empty array works.
		seen = []string{}
	}
	rows, err := s.db.Query(ctx, `
		UPDATE listing_sources SET missing_since = now()
		WHERE source_id = $1
		  AND missing_since IS NULL
		  AND NOT (source_listing_id = ANY($2))
		RETURNING listing_id`,
		sourceID, seen)
	if err != nil {
		return 0, fmt.Errorf("mark missing postings: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, fmt.Errorf("mark missing postings: %w", err)
	}

	done := map[int64]bool{} // a listing can have several missing postings
	for _, id := range ids {
		if done[id] {
			continue
		}
		done[id] = true
		oldStatus, newStatus, err := s.refreshStatus(ctx, id)
		if err != nil {
			return 0, err
		}
		if oldStatus == StatusOpen && newStatus == StatusStale {
			becameStale++
		}
	}
	return becameStale, nil
}

// refreshStatus recomputes a listing's status from all its postings, from
// every source, and returns the status before and after:
//
//	open   - some posting is active and still listed by its source
//	stale  - some posting is active, but none of those is still listed
//	closed - every posting is explicitly inactive
func (s *Store) refreshStatus(ctx context.Context, listingID int64) (oldStatus, newStatus string, err error) {
	err = s.db.QueryRow(ctx, `
		WITH p AS (
			SELECT CASE
				WHEN bool_or(active AND missing_since IS NULL) THEN 'open'
				WHEN bool_or(active)                           THEN 'stale'
				ELSE                                                'closed'
			END AS status
			FROM listing_sources WHERE listing_id = $1
		),
		old AS (SELECT status FROM listings WHERE id = $1)
		UPDATE listings l SET
			status    = p.status,
			closed_at = CASE WHEN p.status = 'closed' THEN COALESCE(l.closed_at, now()) END
		FROM p
		WHERE l.id = $1
		RETURNING (SELECT status FROM old), l.status`,
		listingID,
	).Scan(&oldStatus, &newStatus)
	if err != nil {
		return "", "", fmt.Errorf("refresh status of listing %d: %w", listingID, err)
	}
	return oldStatus, newStatus, nil
}

// VisibleListings returns every listing the feed shows (open or stale),
// for the possible-duplicates report.
func (s *Store) VisibleListings(ctx context.Context) ([]listing.Candidate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT l.id, c.name, l.title, l.season
		FROM listings l JOIN companies c ON c.id = l.company_id
		WHERE l.status IN ('open', 'stale')
		ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("visible listings: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (listing.Candidate, error) {
		var c listing.Candidate
		err := row.Scan(&c.ID, &c.Company, &c.Title, &c.Season)
		return c, err
	})
}
