package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"internship-tracker/internal/listing"
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
	FeedAt      *time.Time // what the feed sorts by and "new" compares; nil if unknown
	// The viewer's application, if they have one (nil otherwise).
	ApplicationID  *int64
	Stage          *string
	InterviewRound *int
	Roles          []string      // role tags, e.g. ["backend", "ml"]
	Industries     []string      // the company's industry tags
	Postings       []FeedPosting // in the order we first stored them
}

// FeedQuery says which listings to show. Filters in different groups
// combine with AND: job type AND roles AND industries.
type FeedQuery struct {
	JobType listing.JobType
	// Roles: with MatchAllRoles, a listing needs every role ("Backend + ML");
	// otherwise any one of them ("Backend / ML"). Empty means no role filter.
	Roles         []listing.Role
	MatchAllRoles bool
	// Industries: any one of them. Empty means no industry filter.
	Industries []listing.Industry
	// OnlyNew keeps listings that appeared after NewSince ("Only new").
	// NewSince is nil for logged-out visitors; OnlyNew then does nothing.
	OnlyNew  bool
	NewSince *time.Time
	// UserID is the viewer (0 when logged out). Listings they've applied
	// to are hidden unless ShowApplied; saved ones always show.
	UserID      int64
	ShowApplied bool
	Limit       int
	Offset      int
}

// args returns the filter values in the order feedFilter numbers them.
func (q FeedQuery) args() []any {
	roles := make([]string, len(q.Roles))
	for i, r := range q.Roles {
		roles[i] = string(r)
	}
	industries := make([]string, len(q.Industries))
	for i, ind := range q.Industries {
		industries[i] = string(ind)
	}
	var newSince *time.Time
	if q.OnlyNew {
		newSince = q.NewSince
	}
	var userID *int64
	if q.UserID != 0 {
		userID = &q.UserID
	}
	return []any{q.JobType, roles, q.MatchAllRoles, industries, newSince, userID, q.ShowApplied}
}

// feedFilter is the WHERE clause shared by Feed and CountFeed, so the
// "N new" count always agrees with the cards shown. Parameters $1-$7 come
// from FeedQuery.args.
const feedFilter = `
	FROM listings l JOIN companies c ON c.id = l.company_id
	-- the viewer's own application, if any ($6 is NULL when logged out)
	LEFT JOIN applications ua ON ua.listing_id = l.id AND ua.user_id = $6
	WHERE l.status IN ('open', 'stale') AND l.job_type = $1
	  -- the feed is "what to apply to next": applied ones only on request
	  AND ($7 OR ua.id IS NULL OR ua.stage = 'saved')
	  -- roles: none chosen, or "all" and it has every one, or "any" and it has one
	  AND (cardinality($2::text[]) = 0
	       OR (SELECT count(*) FROM listing_roles lr WHERE lr.listing_id = l.id AND lr.role = ANY($2))
	          >= CASE WHEN $3 THEN cardinality($2::text[]) ELSE 1 END)
	  AND (cardinality($4::text[]) = 0
	       OR EXISTS (SELECT 1 FROM company_industries ci
	                  WHERE ci.company_id = l.company_id AND ci.industry = ANY($4)))
	  -- "new" means: the feed's own time (see listings.feed_at) is after the boundary
	  AND ($5::timestamptz IS NULL OR l.feed_at > $5)`

// FeedPosting is one source's posting of a listing.
type FeedPosting struct {
	Source       string // "simplify", "speedyapply"
	ApplyURL     string
	JobLink      *string // nil for generic links
	Locations    []string
	Active       bool
	MissingSince *time.Time // set if the source stopped listing it
}

// Feed returns one page of visible listings matching q: open before stale,
// then freshest first (see listings.feed_at), unknown dates last.
func (s *Store) Feed(ctx context.Context, q FeedQuery) ([]FeedListing, error) {
	// The ORDER BY matches listings_feed_idx, so Postgres can walk the index
	// in feed order and stop once it has a page of matching rows.
	rows, err := s.db.Query(ctx, `
		SELECT l.id, c.name, l.title, l.season, l.degree_level, l.status, l.posted_at, l.first_seen_at, l.feed_at,
		       ua.id, ua.stage, ua.interview_round,
		       ARRAY(SELECT role FROM listing_roles WHERE listing_id = l.id),
		       ARRAY(SELECT industry FROM company_industries WHERE company_id = l.company_id ORDER BY industry)
		`+feedFilter+`
		ORDER BY (l.status = 'stale'), l.feed_at DESC NULLS LAST, l.posted_at DESC NULLS LAST, l.id DESC
		LIMIT $8 OFFSET $9`,
		append(q.args(), q.Limit, q.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	listings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FeedListing, error) {
		var l FeedListing
		err := row.Scan(&l.ID, &l.Company, &l.Title, &l.Season, &l.DegreeLevel, &l.Status, &l.PostedAt, &l.FirstSeenAt,
			&l.FeedAt, &l.ApplicationID, &l.Stage, &l.InterviewRound, &l.Roles, &l.Industries)
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

// CountFeed counts the listings matching q's filters, ignoring paging:
// with OnlyNew set it is the "12 new" number.
func (s *Store) CountFeed(ctx context.Context, q FeedQuery) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) `+feedFilter, q.args()...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count feed: %w", err)
	}
	return n, nil
}
