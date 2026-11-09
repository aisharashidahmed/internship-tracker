package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LinkResult is what one check of an apply link concluded.
type LinkResult string

const (
	LinkAlive   LinkResult = "alive"   // the job page is there
	LinkDead    LinkResult = "dead"    // a clear signal it's gone (404, a board's "job removed" redirect)
	LinkUnknown LinkResult = "unknown" // couldn't tell: never counts toward closing
)

// DeadAfter is how many clear "dead" results in a row make a link dead,
// so one flaky check never closes a job.
const DeadAfter = 3

// CheckEvery is how often each link is checked.
const CheckEvery = 24 * time.Hour

// DueLink is an apply link that is due for a check.
type DueLink struct {
	PostingID int64
	URL       string
}

// DueLinks returns up to limit links to check, least recently checked
// first: job-specific links (generic careers pages say nothing about one
// job) of postings their source still calls active, on visible listings.
func (s *Store) DueLinks(ctx context.Context, limit int) ([]DueLink, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ls.id, ls.apply_url
		FROM listing_sources ls JOIN listings l ON l.id = ls.listing_id
		WHERE ls.job_link IS NOT NULL
		  AND ls.active
		  AND l.status IN ('open', 'stale')
		  AND l.owner_user_id IS NULL
		  AND (ls.link_checked_at IS NULL OR ls.link_checked_at < now() - $1::interval)
		ORDER BY ls.link_checked_at NULLS FIRST, ls.id
		LIMIT $2`,
		CheckEvery.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("due links: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DueLink, error) {
		var d DueLink
		return d, row.Scan(&d.PostingID, &d.URL)
	})
}

// RecordLinkCheck saves one check and updates the listing's status. It
// returns closed=true if this check is what closed the listing.
//
//	alive   - the failure count resets, and a dead link comes back to life
//	dead    - one more in a row; at DeadAfter the link is dead
//	unknown - the count is left alone (neither a failure nor a success)
func (s *Store) RecordLinkCheck(ctx context.Context, postingID int64, result LinkResult, detail string) (closed bool, err error) {
	err = s.InTx(ctx, func(tx *Store) error {
		var listingID int64
		err := tx.db.QueryRow(ctx, `
			UPDATE listing_sources SET
				link_checked_at = now(), link_result = $2, link_detail = $3,
				link_failures = CASE $2 WHEN 'alive' THEN 0 WHEN 'dead' THEN link_failures + 1 ELSE link_failures END,
				link_dead_at  = CASE $2
					WHEN 'alive' THEN NULL
					WHEN 'dead'  THEN CASE WHEN link_failures + 1 >= $4 THEN COALESCE(link_dead_at, now()) END
					ELSE link_dead_at END
			WHERE id = $1
			RETURNING listing_id`,
			postingID, result, detail, DeadAfter).Scan(&listingID)
		if err != nil {
			return fmt.Errorf("record link check: %w", err)
		}
		oldStatus, newStatus, err := tx.refreshStatus(ctx, listingID)
		closed = oldStatus != StatusClosed && newStatus == StatusClosed
		return err
	})
	return closed, err
}
