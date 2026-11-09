package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Star adds a company to a user's starred companies (starring twice is
// one star, keeping the first time).
func (s *Store) Star(ctx context.Context, userID, companyID int64) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO starred_companies (user_id, company_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, companyID)
	if err != nil {
		return fmt.Errorf("star: %w", err)
	}
	return nil
}

// Unstar removes a star. Notifications already sent stay: they're events
// that happened.
func (s *Store) Unstar(ctx context.Context, userID, companyID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM starred_companies WHERE user_id = $1 AND company_id = $2`, userID, companyID)
	return err
}

// StarredCompany is one of a user's stars.
type StarredCompany struct {
	ID        int64
	Name      string
	StarredAt time.Time
	OpenRoles int
}

// StarredCompanies lists a user's stars, alphabetically, with how many
// roles each has open.
func (s *Store) StarredCompanies(ctx context.Context, userID int64) ([]StarredCompany, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.name, sc.starred_at,
		       (SELECT count(*) FROM listings l WHERE l.company_id = c.id AND l.status = 'open')
		FROM starred_companies sc JOIN companies c ON c.id = sc.company_id
		WHERE sc.user_id = $1
		ORDER BY lower(c.name)`, userID)
	if err != nil {
		return nil, fmt.Errorf("starred companies: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StarredCompany, error) {
		var c StarredCompany
		return c, row.Scan(&c.ID, &c.Name, &c.StarredAt, &c.OpenRoles)
	})
}

// Notify settings: which job type a user hears about.
const (
	NotifyInternship = "internship"
	NotifyNewGrad    = "new_grad"
	NotifyBoth       = "both"
)

// NotifyJobType returns a user's setting.
func (s *Store) NotifyJobType(ctx context.Context, userID int64) (string, error) {
	var t string
	err := s.db.QueryRow(ctx, `SELECT notify_job_type FROM users WHERE id = $1`, userID).Scan(&t)
	return t, err
}

// SetNotifyJobType saves a user's setting (the database refuses other values).
func (s *Store) SetNotifyJobType(ctx context.Context, userID int64, jobType string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET notify_job_type = $2 WHERE id = $1`, userID, jobType)
	return err
}

// CreateNotifications records "a new role at a company you starred" for
// every user and listing that qualify, and returns how many are new:
//   - the listing is open and at a company the user starred,
//   - it was first seen after the user starred (stars aren't backdated:
//     roles you could already see aren't news),
//   - its job type is one the user wants (not role tags: a star is broad).
//
// The UNIQUE (user, listing, kind) constraint makes it safe to run after
// every ingest: whatever was already sent is skipped, so each role pings
// each user exactly once, even if it appears in two sources.
func (s *Store) CreateNotifications(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO notifications (user_id, listing_id, kind)
		SELECT sc.user_id, l.id, 'starred_company'
		FROM starred_companies sc
		JOIN users u ON u.id = sc.user_id
		JOIN listings l ON l.company_id = sc.company_id
		WHERE l.status = 'open'
		  AND l.first_seen_at > sc.starred_at
		  AND (u.notify_job_type = 'both' OR l.job_type = u.notify_job_type)
		ON CONFLICT (user_id, listing_id, kind) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("create notifications: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Notification is one event in a user's bell.
type Notification struct {
	ID        int64
	Company   string
	Title     string
	Season    string
	CreatedAt time.Time
	Read      bool
}

// Notifications returns a user's newest notifications.
func (s *Store) Notifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	rows, err := s.db.Query(ctx, `
		SELECT n.id, c.name, l.title, l.season, n.created_at, n.read_at IS NOT NULL
		FROM notifications n JOIN listings l ON l.id = n.listing_id JOIN companies c ON c.id = l.company_id
		WHERE n.user_id = $1
		ORDER BY n.created_at DESC, n.id DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Notification, error) {
		var n Notification
		return n, row.Scan(&n.ID, &n.Company, &n.Title, &n.Season, &n.CreatedAt, &n.Read)
	})
}

// UnreadCount is the number on the bell.
func (s *Store) UnreadCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// OpenNotification marks one of a user's notifications read and returns
// the role it's about, so the page can show it.
func (s *Store) OpenNotification(ctx context.Context, userID, id int64) (company, title string, err error) {
	err = s.db.QueryRow(ctx, `
		UPDATE notifications n SET read_at = COALESCE(n.read_at, now())
		FROM listings l JOIN companies c ON c.id = l.company_id
		WHERE n.id = $2 AND n.user_id = $1 AND l.id = n.listing_id
		RETURNING c.name, l.title`, userID, id).Scan(&company, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return company, title, err
}

// MarkAllNotificationsRead empties the bell.
func (s *Store) MarkAllNotificationsRead(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
	return err
}
