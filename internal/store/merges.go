package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// MergeReport says what ReattachMovedPostings did.
type MergeReport struct {
	Moved     int // postings that moved to a different listing
	Merged    int // emptied listings merged into the listing their postings moved to
	KeptApart int // emptied listings kept, because merging would lose something
}

// ReattachMovedPostings cleans up after a rule change. When the matching
// rules change, a source's posting can be saved under a different listing
// than before (say "New Grad" titles now make new-grad listings). The old
// row is then left behind on the old listing, which would stay open forever.
//
// For one source, after its saves, this removes each old row that the same
// posting (same source id, same season) now has on another listing. A
// listing left with no postings at all is merged into the listing that took
// them, unless mergeBlocked says otherwise; then both are kept and the pair
// is recorded for the possible-duplicates report.
//
// It must run in the same transaction as the saves: "saved in this run"
// means last_seen_at = now(), and now() is fixed for a whole transaction.
func (s *Store) ReattachMovedPostings(ctx context.Context, sourceID int64) (MergeReport, error) {
	var rep MergeReport
	rows, err := s.db.Query(ctx, `
		DELETE FROM listing_sources old
		USING listing_sources new, listings lo, listings ln
		WHERE old.source_id = $1 AND new.source_id = $1
		  AND old.source_listing_id = new.source_listing_id
		  AND old.listing_id <> new.listing_id
		  AND lo.id = old.listing_id AND ln.id = new.listing_id
		  AND lo.season = ln.season          -- one ad for Summer and Fall is two listings, not a move
		  AND new.last_seen_at = now()        -- saved in this run...
		  AND old.last_seen_at < now()        -- ...and the old row wasn't
		RETURNING old.listing_id, new.listing_id`,
		sourceID)
	if err != nil {
		return rep, fmt.Errorf("reattach moved postings: %w", err)
	}
	type move struct{ from, to int64 }
	moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (move, error) {
		var m move
		return m, row.Scan(&m.from, &m.to)
	})
	if err != nil {
		return rep, fmt.Errorf("reattach moved postings: %w", err)
	}
	rep.Moved = len(moves)

	done := map[int64]bool{}
	for _, m := range moves {
		if done[m.from] {
			continue
		}
		done[m.from] = true

		var left int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM listing_sources WHERE listing_id = $1`, m.from).Scan(&left); err != nil {
			return rep, err
		}
		if left > 0 {
			// Still has other postings: just recompute what changed.
			if _, _, err := s.refreshStatus(ctx, m.from); err != nil {
				return rep, err
			}
			if err := s.refreshRoles(ctx, m.from); err != nil {
				return rep, err
			}
			continue
		}

		blocked, reason, err := s.mergeBlocked(ctx, m.from, m.to)
		if err != nil {
			return rep, err
		}
		if blocked {
			if err := s.keepApart(ctx, m.from, m.to, reason); err != nil {
				return rep, err
			}
			rep.KeptApart++
			continue
		}
		if err := s.mergeInto(ctx, m.from, m.to); err != nil {
			return rep, err
		}
		rep.Merged++
	}
	return rep, nil
}

// mergeBlocked says whether merging listing from into to would lose
// something a person did. Decided rule: if the same user has an
// application on each listing, they are not merged. Two separate
// applications are stronger evidence than the matching rules that these
// are different jobs (and merging would break "one application per job").
func (s *Store) mergeBlocked(ctx context.Context, from, to int64) (blocked bool, reason string, err error) {
	var users int
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FROM applications a JOIN applications b ON b.user_id = a.user_id
		WHERE a.listing_id = $1 AND b.listing_id = $2`, from, to).Scan(&users)
	if err != nil {
		return false, "", fmt.Errorf("check merge of %d into %d: %w", from, to, err)
	}
	if users > 0 {
		return true, fmt.Sprintf("%d user(s) applied to each separately", users), nil
	}
	return false, "", nil
}

// mergeInto moves everything that points at listing from over to listing
// to, then deletes from. Its postings have already moved. Applications
// move too; mergeBlocked has already made sure no user has one on both.
func (s *Store) mergeInto(ctx context.Context, from, to int64) error {
	if _, err := s.db.Exec(ctx, `UPDATE applications SET listing_id = $2 WHERE listing_id = $1`, from, to); err != nil {
		return fmt.Errorf("merge listing %d into %d: move applications: %w", from, to, err)
	}
	// Keep the earlier first-seen time and posted date: the job was known then.
	_, err := s.db.Exec(ctx, `
		UPDATE listings t SET
			first_seen_at = LEAST(t.first_seen_at, f.first_seen_at),
			posted_at     = LEAST(t.posted_at, f.posted_at)
		FROM listings f
		WHERE t.id = $2 AND f.id = $1`, from, to)
	if err != nil {
		return fmt.Errorf("merge listing %d into %d: %w", from, to, err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM listings WHERE id = $1`, from); err != nil {
		return fmt.Errorf("merge listing %d into %d: %w", from, to, err)
	}
	return nil
}

// keepApart records a pair the report should show as possible duplicates.
func (s *Store) keepApart(ctx context.Context, a, b int64, reason string) error {
	if a > b {
		a, b = b, a
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO listings_kept_apart (listing_a, listing_b, reason) VALUES ($1, $2, $3)
		ON CONFLICT (listing_a, listing_b) DO UPDATE SET reason = EXCLUDED.reason`, a, b, reason)
	if err != nil {
		return fmt.Errorf("keep listings %d and %d apart: %w", a, b, err)
	}
	// The emptied listing has no postings, so nothing says it's open.
	_, _, err = s.refreshStatus(ctx, a)
	if err == nil {
		_, _, err = s.refreshStatus(ctx, b)
	}
	return err
}

// KeptApartPairs lists pairs kept apart, for the possible-duplicates report.
func (s *Store) KeptApartPairs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT format('#%s %L and #%s %L (%s)', a.id, a.title, b.id, b.title, k.reason)
		FROM listings_kept_apart k
		JOIN listings a ON a.id = k.listing_a JOIN listings b ON b.id = k.listing_b
		ORDER BY k.created_at`)
	if err != nil {
		return nil, fmt.Errorf("kept-apart pairs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
