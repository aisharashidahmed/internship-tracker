package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"internship-tracker/internal/listing"
)

// FeedListing is one listing as the feed needs it, with all its postings.
type FeedListing struct {
	ID int64
	// GroupKey is shared by the same role's listings in different seasons
	// (listings.role_key); the feed shows one card per group.
	GroupKey    string
	Company     string
	Title       string
	Season      string // "" if unknown
	DegreeLevel string
	Status      string     // StatusOpen or StatusStale
	PostedAt    *time.Time // nil if no source says
	FirstSeenAt time.Time
	FeedAt      *time.Time // the sort time (listings.feed_at); nil if unknown
	NewAt       *time.Time // when the app could first show it: "New for you" compares this
	// The viewer's application, if they have one (nil otherwise).
	ApplicationID  *int64
	Stage          *string
	InterviewRound *int
	TopCompany     bool // a source flags the company as a top company
	CompanyID      int64
	Starred        bool          // the viewer starred this company
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
	// Search matches company or title, ignoring case ("" = no search).
	Search string
	// TopOnly keeps listings at top companies (company_top_signals).
	TopOnly bool
	// StarredOnly keeps listings at companies the viewer starred (needs UserID).
	StarredOnly bool
	// Limit and Offset count groups (cards), not listings.
	Limit  int
	Offset int
}

// searchPattern turns typed text into an ILIKE pattern that matches it
// anywhere, with %, _ and \ taken literally (so "100%" isn't a wildcard).
func searchPattern(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
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
	return []any{q.JobType, roles, q.MatchAllRoles, industries, newSince, userID, q.ShowApplied, searchPattern(q.Search), q.TopOnly, q.StarredOnly && q.UserID != 0}
}

// feedFilter is the WHERE clause shared by Feed and CountFeed, so the
// "N new" count always agrees with the cards shown. Parameters $1-$10 come
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
	  -- "new for you": the app first showed it after your boundary (listings.new_at)
	  AND ($5::timestamptz IS NULL OR l.new_at > $5)
	  -- search: company or title contains the text
	  AND ($8 = '' OR c.name ILIKE $8 OR l.title ILIKE $8)
	  -- top companies only
	  AND (NOT $9 OR EXISTS (SELECT 1 FROM company_top_signals t WHERE t.company_id = l.company_id))
	  -- the viewer's starred companies only
	  AND (NOT $10 OR EXISTS (SELECT 1 FROM starred_companies sc WHERE sc.company_id = l.company_id AND sc.user_id = $6))`

// groupKey is a listing's card: its role_key, or itself if it has none yet.
// COLLATE "C" compares raw bytes: grouping and count(DISTINCT ...) only need
// equality, and the default language-aware comparison made sorting ~4,000
// keys take about 30 ms (measured with EXPLAIN ANALYZE) against 7 ms to read
// the rows themselves.
const groupKey = `(COALESCE(l.role_key, 'id:' || l.id) COLLATE "C")`

// FeedPosting is one source's posting of a listing.
type FeedPosting struct {
	Source       string // "simplify", "speedyapply"
	ApplyURL     string
	JobLink      *string // nil for generic links
	Locations    []string
	Active       bool
	MissingSince *time.Time // set if the source stopped listing it
	LinkDead     bool       // the link checker found the link no longer works
}

// Feed returns one page of cards matching q, as listings: every matching
// listing of each card on the page, card after card, so a card's seasons
// arrive together. Cards are grouped in the database *before* paging, so a
// card is never split across pages. Card order: open before stale, then
// freshest first (by the freshest season), unknown dates last. Within a
// card, seasons come in the same order, so the first is the default.
func (s *Store) Feed(ctx context.Context, q FeedQuery) ([]FeedListing, error) {
	rows, err := s.db.Query(ctx, `
		WITH m AS (  -- every listing that matches the filters
			SELECT l.id, `+groupKey+` AS gk, l.status, l.feed_at, l.posted_at
			`+feedFilter+`
		),
		page AS (    -- one page of cards
			SELECT gk, row_number() OVER (ORDER BY bool_and(status = 'stale'), max(feed_at) DESC NULLS LAST,
			                                      max(posted_at) DESC NULLS LAST, max(id) DESC) AS pos
			FROM m GROUP BY gk
			ORDER BY pos
			LIMIT $11 OFFSET $12
		)
		SELECT l.id, page.gk, c.name, l.title, l.season, l.degree_level, l.status, l.posted_at, l.first_seen_at, l.feed_at, l.new_at,
		       ua.id, ua.stage, ua.interview_round,
		       EXISTS (SELECT 1 FROM company_top_signals t WHERE t.company_id = l.company_id),
		       l.company_id,
		       EXISTS (SELECT 1 FROM starred_companies sc WHERE sc.company_id = l.company_id AND sc.user_id = $6),
		       ARRAY(SELECT role FROM listing_roles WHERE listing_id = l.id),
		       ARRAY(SELECT industry FROM company_industries WHERE company_id = l.company_id ORDER BY industry)
		FROM page
		JOIN m ON m.gk = page.gk
		JOIN listings l ON l.id = m.id
		JOIN companies c ON c.id = l.company_id
		LEFT JOIN applications ua ON ua.listing_id = l.id AND ua.user_id = $6
		ORDER BY page.pos, (l.status = 'stale'), l.feed_at DESC NULLS LAST, l.posted_at DESC NULLS LAST, l.id DESC`,
		append(q.args(), q.Limit, q.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	listings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FeedListing, error) {
		var l FeedListing
		err := row.Scan(&l.ID, &l.GroupKey, &l.Company, &l.Title, &l.Season, &l.DegreeLevel, &l.Status, &l.PostedAt,
			&l.FirstSeenAt, &l.FeedAt, &l.NewAt, &l.ApplicationID, &l.Stage, &l.InterviewRound, &l.TopCompany, &l.CompanyID, &l.Starred, &l.Roles, &l.Industries)
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
		SELECT ls.listing_id, src.name, ls.apply_url, ls.job_link, ls.locations, ls.active, ls.missing_since,
		       ls.link_dead_at IS NOT NULL
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
		if err := rows.Scan(&listingID, &p.Source, &p.ApplyURL, &p.JobLink, &p.Locations, &p.Active, &p.MissingSince, &p.LinkDead); err != nil {
			return nil, fmt.Errorf("feed postings: %w", err)
		}
		byID[listingID].Postings = append(byID[listingID].Postings, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feed postings: %w", err)
	}
	return listings, nil
}

// CountFeed counts the cards (roles, all seasons together) matching q's
// filters, ignoring paging: "312 roles match", or with OnlyNew, "12 new".
func (s *Store) CountFeed(ctx context.Context, q FeedQuery) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(DISTINCT `+groupKey+`) `+feedFilter, q.args()...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count feed: %w", err)
	}
	return n, nil
}
