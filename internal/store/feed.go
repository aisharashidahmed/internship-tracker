package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FeedListing is one listing as the feed needs it, with all its postings.
type FeedListing struct {
	ID          int64
	Company     string
	Title       string
	Season      string // "" if unknown
	DegreeLevel string
	Status      string     // StatusOpen or StatusStale
	PostedAt    *time.Time // nil if no source says
	FirstSeenAt time.Time
	Postings    []FeedPosting // in the order we first stored them
}

// FeedPosting is one source's posting of a listing.
type FeedPosting struct {
	Source       string // "simplify", "speedyapply"
	ApplyURL     string
	JobLink      *string // nil for generic links
	Locations    []string
	Active       bool
	MissingSince *time.Time // set if the source stopped listing it
}

// Feed returns one page of visible listings: open before stale, then
// freshest first (see listings.feed_at), unknown dates last.
func (s *Store) Feed(ctx context.Context, limit, offset int) ([]FeedListing, error) {
	// The ORDER BY matches listings_feed_idx column for column, so Postgres
	// can read the page straight from the index.
	rows, err := s.db.Query(ctx, `
		SELECT l.id, c.name, l.title, l.season, l.degree_level, l.status, l.posted_at, l.first_seen_at
		FROM listings l JOIN companies c ON c.id = l.company_id
		WHERE l.status IN ('open', 'stale')
		ORDER BY (l.status = 'stale'), l.feed_at DESC NULLS LAST, l.posted_at DESC NULLS LAST, l.id DESC
		LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	listings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FeedListing, error) {
		var l FeedListing
		err := row.Scan(&l.ID, &l.Company, &l.Title, &l.Season, &l.DegreeLevel, &l.Status, &l.PostedAt, &l.FirstSeenAt)
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	if len(listings) == 0 {
		return listings, nil
	}

	// One query for every posting on the page, instead of one per listing.
	ids := make([]int64, len(listings))
	byID := make(map[int64]*FeedListing, len(listings))
	for i := range listings {
		ids[i] = listings[i].ID
		byID[listings[i].ID] = &listings[i]
	}
	rows, err = s.db.Query(ctx, `
		SELECT ls.listing_id, src.name, ls.apply_url, ls.job_link, ls.locations, ls.active, ls.missing_since
		FROM listing_sources ls JOIN sources src ON src.id = ls.source_id
		WHERE ls.listing_id = ANY($1)
		ORDER BY ls.id`,
		ids)
	if err != nil {
		return nil, fmt.Errorf("feed postings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var listingID int64
		var p FeedPosting
		if err := rows.Scan(&listingID, &p.Source, &p.ApplyURL, &p.JobLink, &p.Locations, &p.Active, &p.MissingSince); err != nil {
			return nil, fmt.Errorf("feed postings: %w", err)
		}
		byID[listingID].Postings = append(byID[listingID].Postings, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feed postings: %w", err)
	}
	return listings, nil
}
