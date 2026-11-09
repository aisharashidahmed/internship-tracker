package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"internship-tracker/internal/jobimport"
	"internship-tracker/internal/listing"
)

// DailyImportLimit: links a user may paste per day (decided 2026-11-09).
// Each one makes the server fetch other sites on their behalf.
const DailyImportLimit = 25

// ErrImportLimit: the user has pasted their daily allowance.
var ErrImportLimit = fmt.Errorf("you can add up to %d links a day; try again tomorrow", DailyImportLimit)

// --- Step 1 and the cache (jobimport.Store) ----------------------------------

// ListingByJobKey finds the single public listing with this job link.
// Several (one per season) is ambiguous, so it reports not found.
func (s *Store) ListingByJobKey(ctx context.Context, jobKey string) (jobimport.Known, bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT l.id, c.name, l.title, l.season, l.job_type,
		       COALESCE((SELECT x.locations[1] FROM listing_sources x WHERE x.listing_id = l.id ORDER BY x.id LIMIT 1), '')
		FROM listing_sources ls JOIN listings l ON l.id = ls.listing_id JOIN companies c ON c.id = l.company_id
		WHERE ls.job_link = $1 AND l.owner_user_id IS NULL`, jobKey)
	if err != nil {
		return jobimport.Known{}, false, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (jobimport.Known, error) {
		var k jobimport.Known
		var jt string
		err := row.Scan(&k.ListingID, &k.Company, &k.Title, &k.Season, &jt, &k.Location)
		k.JobType = listing.JobType(jt)
		return k, err
	})
	if err != nil || len(found) != 1 {
		return jobimport.Known{}, false, err
	}
	return found[0], true, nil
}

func (s *Store) CachedExtraction(ctx context.Context, jobKey string) (jobimport.Result, bool, error) {
	var r jobimport.Result
	var step, jt string
	err := s.db.QueryRow(ctx, `SELECT step, company, title, location, season, job_type FROM extraction_cache WHERE job_key = $1`, jobKey).
		Scan(&step, &r.Company, &r.Title, &r.Location, &r.Season, &jt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	r.Step, r.JobType = jobimport.Step(step), listing.JobType(jt)
	return r, err == nil, err
}

func (s *Store) SaveExtraction(ctx context.Context, jobKey string, r jobimport.Result) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO extraction_cache (job_key, step, company, title, location, season, job_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (job_key) DO NOTHING`,
		jobKey, r.Step, r.Company, r.Title, r.Location, r.Season, r.JobType)
	return err
}

// --- The queue ---------------------------------------------------------------

// Import is one pasted link and what we learned about it.
type Import struct {
	ID        int64
	URL       string
	Status    string // queued, running, ready, saved, failed
	Step      string // match, api, jsonld, form (once ready)
	Error     string
	Company   string
	Title     string
	Location  string
	Season    string
	JobType   string
	ListingID *int64
	CreatedAt time.Time
}

// EnqueueImports queues links for a user, up to the daily limit.
func (s *Store) EnqueueImports(ctx context.Context, userID int64, urls []string) (int, error) {
	var n int
	err := s.InTx(ctx, func(tx *Store) error {
		var today int
		if err := tx.db.QueryRow(ctx, `SELECT count(*) FROM job_imports WHERE user_id = $1 AND created_at > now() - interval '1 day'`,
			userID).Scan(&today); err != nil {
			return err
		}
		if today+len(urls) > DailyImportLimit {
			return ErrImportLimit
		}
		for _, u := range urls {
			if _, err := tx.db.Exec(ctx, `INSERT INTO job_imports (user_id, url) VALUES ($1, $2)`, userID, u); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ImportJob is a claimed queue row.
type ImportJob struct {
	ID, UserID int64
	URL        string
	Attempts   int
}

// ClaimImport takes the next due link. FOR UPDATE SKIP LOCKED means two
// workers never take the same row, and neither waits for the other.
func (s *Store) ClaimImport(ctx context.Context) (ImportJob, bool, error) {
	var j ImportJob
	err := s.db.QueryRow(ctx, `
		UPDATE job_imports SET status = 'running', attempts = attempts + 1
		WHERE id = (SELECT id FROM job_imports WHERE status = 'queued' AND next_attempt_at <= now()
		            ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, user_id, url, attempts`).Scan(&j.ID, &j.UserID, &j.URL, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// FinishImport stores a result for the user to confirm.
func (s *Store) FinishImport(ctx context.Context, id int64, r jobimport.Result) error {
	var listingID *int64
	if r.ListingID != 0 {
		listingID = &r.ListingID
	}
	if r.JobType == "" {
		r.JobType = listing.Internship // the lists we serve are internship lists
	}
	_, err := s.db.Exec(ctx, `
		UPDATE job_imports SET status = 'ready', step = $2, company = $3, title = $4, location = $5, season = $6,
		       job_type = $7, listing_id = $8, last_error = '', finished_at = now()
		WHERE id = $1`, id, r.Step, r.Company, r.Title, r.Location, r.Season, r.JobType, listingID)
	return err
}

// RetryImport puts a link back in the queue after delay.
func (s *Store) RetryImport(ctx context.Context, id int64, why string, delay time.Duration) error {
	_, err := s.db.Exec(ctx, `UPDATE job_imports SET status = 'queued', last_error = $2, next_attempt_at = now() + $3::interval WHERE id = $1`,
		id, why, delay.String())
	return err
}

// FailImport marks a link that can't be used at all (e.g. not a web link).
func (s *Store) FailImport(ctx context.Context, id int64, why string) error {
	_, err := s.db.Exec(ctx, `UPDATE job_imports SET status = 'failed', last_error = $2, finished_at = now() WHERE id = $1`, id, why)
	return err
}

const importColumns = `id, url, status, COALESCE(step, ''), last_error, company, title, location, season, job_type, listing_id, created_at`

func scanImport(row pgx.CollectableRow) (Import, error) {
	var i Import
	err := row.Scan(&i.ID, &i.URL, &i.Status, &i.Step, &i.Error, &i.Company, &i.Title, &i.Location, &i.Season, &i.JobType, &i.ListingID, &i.CreatedAt)
	return i, err
}

// Imports lists a user's links that aren't saved yet, newest first.
func (s *Store) Imports(ctx context.Context, userID int64) ([]Import, error) {
	rows, err := s.db.Query(ctx, `SELECT `+importColumns+` FROM job_imports
		WHERE user_id = $1 AND status <> 'saved' AND created_at > now() - interval '30 days'
		ORDER BY created_at DESC, id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanImport)
}

// DismissImport removes an unsaved link.
func (s *Store) DismissImport(ctx context.Context, userID, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM job_imports WHERE id = $1 AND user_id = $2 AND status <> 'saved'`, id, userID)
	return err
}

// ImportEdit is what the user confirmed (possibly edited).
type ImportEdit struct {
	Company, Title, Location, Season string
	JobType                          listing.JobType
}

// SaveImport turns a confirmed import into an application. It attaches to
// the matching public listing if there is one (checked again now: ingest
// may have added the job since), and otherwise creates a private listing
// only this user can see. Returns the application's id.
func (s *Store) SaveImport(ctx context.Context, userID, importID int64, e ImportEdit, appliedOn time.Time) (int64, error) {
	var appID int64
	err := s.InTx(ctx, func(tx *Store) error {
		var url, status string
		var matched *int64
		err := tx.db.QueryRow(ctx, `SELECT url, status, listing_id FROM job_imports WHERE id = $1 AND user_id = $2 FOR UPDATE`,
			importID, userID).Scan(&url, &status, &matched)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "ready" {
			return fmt.Errorf("this link isn't ready to save (%s)", status)
		}

		key, _ := listing.JobLink(url)
		listingID := int64(0)
		if matched != nil {
			listingID = *matched
		} else if key != "" {
			if k, ok, err := tx.ListingByJobKey(ctx, key); err != nil {
				return err
			} else if ok {
				listingID = k.ListingID
			}
		}
		if listingID == 0 {
			if listingID, err = tx.createPrivateListing(ctx, userID, importID, url, key, e); err != nil {
				return err
			}
		}

		if appID, _, err = tx.StartApplication(ctx, userID, listingID, StageApplied, appliedOn); err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, `UPDATE job_imports SET status = 'saved', listing_id = $2 WHERE id = $1`, importID, listingID)
		return err
	})
	return appID, err
}

// createPrivateListing makes a listing owned by userID, with one posting
// from the "user-added" source.
func (s *Store) createPrivateListing(ctx context.Context, userID, importID int64, url, jobKey string, e ImportEdit) (int64, error) {
	companyID, err := s.upsertCompany(ctx, e.Company)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `
		INSERT INTO listings (dedupe_key, company_id, title, job_type, season, degree_level, owner_user_id)
		VALUES ($1, $2, $3, $4, $5, 'unknown', $6) RETURNING id`,
		fmt.Sprintf("private:%d:%d", userID, importID), companyID, e.Title, e.JobType, e.Season, userID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create private listing: %w", err)
	}
	var link *string
	if jobKey != "" {
		link = &jobKey
	}
	locations := []string{}
	if e.Location != "" {
		locations = listing.NormalizeLocations([]string{e.Location})
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO listing_sources (listing_id, source_id, source_listing_id, apply_url, locations, active, job_link, roles)
		VALUES ($1, (SELECT id FROM sources WHERE name = 'user-added'), $2, $3, $4, true, $5, $6)`,
		id, fmt.Sprintf("import:%d", importID), url, locations, link, listing.RoleStrings(listing.Roles(e.Title, "")))
	if err != nil {
		return 0, fmt.Errorf("create private posting: %w", err)
	}
	if err := s.refreshRoles(ctx, id); err != nil {
		return 0, err
	}
	_, _, err = s.refreshStatus(ctx, id)
	return id, err
}

// ImportStats counts finished imports by the step that produced them, for
// deciding later whether a smarter fallback is worth it.
type ImportStats struct {
	Since                            time.Time
	Match, API, JSONLD, Form, Failed int
	Pending                          int
}

func (s *Store) ImportStats(ctx context.Context, since time.Time) (ImportStats, error) {
	st := ImportStats{Since: since}
	rows, err := s.db.Query(ctx, `
		SELECT COALESCE(step, status), count(*) FROM job_imports
		WHERE created_at >= $1 GROUP BY 1`, since)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		switch k {
		case "match":
			st.Match = n
		case "api":
			st.API = n
		case "jsonld":
			st.JSONLD = n
		case "form":
			st.Form = n
		case "failed":
			st.Failed = n
		default: // queued or running
			st.Pending += n
		}
	}
	return st, rows.Err()
}

// --- Sharing ------------------------------------------------------------------

// ShareCandidateFor loads one of the user's own private listings.
func (s *Store) ShareCandidateFor(ctx context.Context, userID, listingID int64) (jobimport.ShareCandidate, error) {
	c := jobimport.ShareCandidate{ListingID: listingID}
	var jt string
	var key *string
	var locs []string
	err := s.db.QueryRow(ctx, `
		SELECT c.name, l.title, l.season, l.job_type, ls.apply_url, ls.job_link, ls.locations,
		       u.email_verified_at IS NOT NULL,
		       (SELECT count(*) FROM share_requests r WHERE r.user_id = $1 AND r.created_at > now() - interval '1 day')
		FROM listings l JOIN companies c ON c.id = l.company_id
		JOIN listing_sources ls ON ls.listing_id = l.id
		JOIN users u ON u.id = l.owner_user_id
		WHERE l.id = $2 AND l.owner_user_id = $1
		LIMIT 1`, userID, listingID).
		Scan(&c.Company, &c.Title, &c.Season, &jt, &c.ApplyURL, &key, &locs, &c.EmailVerified, &c.SharesToday)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.JobType = listing.JobType(jt)
	if key != nil {
		c.JobKey = *key
	}
	p := listing.Posting{Company: c.Company, Title: c.Title, JobType: c.JobType, Season: c.Season, Locations: locs}
	c.PublicDedupeKey, c.RoleKey = listing.DedupeKey(p), listing.RoleKey(p)
	return c, nil
}

// PublicDuplicate finds a public listing that is the same job: the same job
// link, or the same dedupe key.
func (s *Store) PublicDuplicate(ctx context.Context, c jobimport.ShareCandidate) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		SELECT l.id FROM listings l
		WHERE l.owner_user_id IS NULL AND (
			l.dedupe_key = $1 OR
			($2 <> '' AND EXISTS (SELECT 1 FROM listing_sources ls WHERE ls.listing_id = l.id AND ls.job_link = $2)))
		ORDER BY l.id LIMIT 1`, c.PublicDedupeKey, c.JobKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// MergePrivateInto moves the user's application from their private listing
// to the public one and deletes the private listing. If they already had an
// application on the public listing, that one is kept.
func (s *Store) MergePrivateInto(ctx context.Context, userID, privateID, publicID int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx, `
			UPDATE applications SET listing_id = $3
			WHERE listing_id = $2 AND user_id = $1
			  AND NOT EXISTS (SELECT 1 FROM applications a WHERE a.user_id = $1 AND a.listing_id = $3)`,
			userID, privateID, publicID); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, `DELETE FROM applications WHERE listing_id = $1 AND user_id = $2`, privateID, userID); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, `DELETE FROM listings WHERE id = $1 AND owner_user_id = $2`, privateID, userID)
		return err
	})
}

// MakePublic publishes a private listing under its normal dedupe key.
func (s *Store) MakePublic(ctx context.Context, c jobimport.ShareCandidate) error {
	err := s.InTx(ctx, func(tx *Store) error {
		_, err := tx.db.Exec(ctx, `
			UPDATE listings SET owner_user_id = NULL, dedupe_key = $2, role_key = $3, first_seen_at = now(), last_seen_at = now()
			WHERE id = $1`, c.ListingID, c.PublicDedupeKey, c.RoleKey)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return jobimport.ErrDuplicate
	}
	return err
}

// RecordShare remembers a share request and its outcome (it also counts
// toward the daily limit).
func (s *Store) RecordShare(ctx context.Context, userID, listingID int64, outcome, reason string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO share_requests (user_id, listing_id, outcome, reason) VALUES ($1, $2, $3, $4)`,
		userID, listingID, outcome, reason)
	return err
}

// LastShare is the latest share outcome for a listing ("" if never shared).
func (s *Store) LastShare(ctx context.Context, userID, listingID int64) (outcome, reason string, err error) {
	err = s.db.QueryRow(ctx, `SELECT outcome, reason FROM share_requests WHERE user_id = $1 AND listing_id = $2
		ORDER BY created_at DESC, id DESC LIMIT 1`, userID, listingID).Scan(&outcome, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return outcome, reason, err
}
