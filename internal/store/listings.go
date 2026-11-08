package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"internship-tracker/internal/listing"
)

// Outcome says what SaveListing did, so ingestion can report counts.
type Outcome string

const (
	OutcomeNew       Outcome = "new"       // first time we've seen this job
	OutcomeUpdated   Outcome = "updated"   // a posting was added or its link, cities or status changed
	OutcomeUnchanged Outcome = "unchanged" // seen again, nothing different
	OutcomeClosed    Outcome = "closed"    // was open; every posting is now inactive
	OutcomeIgnored   Outcome = "ignored"   // inactive and never stored, so not worth adding
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

	newStatus, err := s.refreshStatus(ctx, listingID)
	if err != nil {
		return "", err
	}

	switch {
	case oldStatus == "":
		return OutcomeNew, nil
	case oldStatus == "open" && newStatus == "closed":
		return OutcomeClosed, nil
	case postingsChanged || oldStatus != newStatus:
		return OutcomeUpdated, nil
	default:
		return OutcomeUnchanged, nil
	}
}

// findOrCreateListing returns the listing's id and its status before this
// run ("" if it is brand new). It returns id 0 if the listing doesn't exist
// and has no active postings: we only start tracking jobs that are open.
func (s *Store) findOrCreateListing(ctx context.Context, l listing.Listing) (id int64, oldStatus string, err error) {
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
// new or different (link, cities, or active flag).
func (s *Store) upsertPosting(ctx context.Context, listingID, sourceID int64, p listing.Posting) (changed bool, err error) {
	locations := p.Locations
	if locations == nil {
		locations = []string{} // the column is NOT NULL; a nil slice would be sent as NULL
	}

	err = s.db.QueryRow(ctx, `
		WITH old AS (
			SELECT apply_url, locations, active FROM listing_sources
			WHERE listing_id = $1 AND source_id = $2 AND source_listing_id = $3
		)
		INSERT INTO listing_sources (listing_id, source_id, source_listing_id, apply_url, locations, active)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (listing_id, source_id, source_listing_id) DO UPDATE SET
			apply_url    = EXCLUDED.apply_url,
			locations    = EXCLUDED.locations,
			active       = EXCLUDED.active,
			last_seen_at = now()
		RETURNING NOT EXISTS (
			SELECT 1 FROM old
			WHERE (old.apply_url, old.locations, old.active) = ($4, $5::text[], $6)
		)`,
		listingID, sourceID, p.SourceListingID, p.ApplyURL, locations, p.Active,
	).Scan(&changed)
	if err != nil {
		return false, fmt.Errorf("upsert posting %q: %w", p.SourceListingID, err)
	}
	return changed, nil
}

// refreshStatus sets a listing open if any of its postings (from any source)
// is active, and closed otherwise. It returns the new status.
func (s *Store) refreshStatus(ctx context.Context, listingID int64) (string, error) {
	var status string
	err := s.db.QueryRow(ctx, `
		UPDATE listings l SET
			status    = CASE WHEN p.any_active THEN 'open' ELSE 'closed' END,
			closed_at = CASE WHEN p.any_active THEN NULL ELSE COALESCE(l.closed_at, now()) END
		FROM (SELECT bool_or(active) AS any_active FROM listing_sources WHERE listing_id = $1) p
		WHERE l.id = $1
		RETURNING l.status`,
		listingID,
	).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("refresh status of listing %d: %w", listingID, err)
	}
	return status, nil
}

// OpenListings returns every open listing, for the possible-duplicates report.
func (s *Store) OpenListings(ctx context.Context) ([]listing.Candidate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT l.id, c.name, l.title, l.season
		FROM listings l JOIN companies c ON c.id = l.company_id
		WHERE l.status = 'open'
		ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("open listings: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (listing.Candidate, error) {
		var c listing.Candidate
		err := row.Scan(&c.ID, &c.Company, &c.Title, &c.Season)
		return c, err
	})
}
