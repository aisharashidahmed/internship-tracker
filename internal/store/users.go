package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"internship-tracker/internal/auth"
)

// User is an account. PasswordHash is only filled in by UserByEmail,
// which login needs; nothing else should carry it around.
type User struct {
	ID           int64
	Email        string
	Timezone     string // IANA name, e.g. "America/New_York"
	PasswordHash string
}

var (
	// ErrInviteInvalid: the link is unknown, expired, or used up. One error
	// for all three, so a guessed link learns nothing.
	ErrInviteInvalid = errors.New("this invite link is invalid, expired, or already used")
	// ErrEmailTaken: an account with this email (in any letter case) exists.
	ErrEmailTaken = errors.New("email already has an account")
)

// CreateInvite makes a sign-up link token that works maxUses times until
// it expires. Only its hash is stored; the token is shown once.
func (s *Store) CreateInvite(ctx context.Context, note string, maxUses int, validFor time.Duration) (string, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO invites (token_hash, note, max_uses, expires_at) VALUES ($1, $2, $3, now() + $4::interval)`,
		hash, note, maxUses, validFor.String())
	if err != nil {
		return "", fmt.Errorf("create invite: %w", err)
	}
	return token, nil
}

// InviteUsable reports whether an invite token can still be used, so the
// sign-up page can say so before asking for a password.
func (s *Store) InviteUsable(ctx context.Context, token string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM invites WHERE token_hash = $1 AND expires_at > now() AND uses < max_uses)`,
		auth.HashToken(token)).Scan(&ok)
	return ok, err
}

// SignUp creates an account using an invite, all in one transaction: if
// the email is taken, the invite isn't used up.
// SignUp creates an account. Sign-up is open (decided 2026-11-08); an
// invite token is optional. If one is given it must be valid, and it is
// used up: invite links still work for sharing with a group.
func (s *Store) SignUp(ctx context.Context, inviteToken, email, passwordHash, timezone string) (User, error) {
	var u User
	err := s.InTx(ctx, func(tx *Store) error {
		var inviteID *int64
		if inviteToken != "" {
			// FOR UPDATE locks the invite row, so two people using the last
			// seat of a group link at the same moment can't both get in.
			var id int64
			err := tx.db.QueryRow(ctx, `
				SELECT id FROM invites
				WHERE token_hash = $1 AND expires_at > now() AND uses < max_uses
				FOR UPDATE`,
				auth.HashToken(inviteToken)).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInviteInvalid
			}
			if err != nil {
				return fmt.Errorf("sign up: %w", err)
			}
			inviteID = &id
		}

		err := tx.db.QueryRow(ctx,
			`INSERT INTO users (email, password_hash, invite_id, timezone) VALUES ($1, $2, $3, $4) RETURNING id, email, timezone`,
			email, passwordHash, inviteID, timezone).Scan(&u.ID, &u.Email, &u.Timezone)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on lower(email)
			return ErrEmailTaken
		}
		if err != nil {
			return fmt.Errorf("sign up: %w", err)
		}

		if inviteID == nil {
			return nil
		}
		_, err = tx.db.Exec(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = $1`, *inviteID)
		return err
	})
	return u, err
}

// UserByEmail finds an account for login, ignoring letter case.
// found is false if there is none.
func (s *Store) UserByEmail(ctx context.Context, email string) (u User, found bool, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT id, email, timezone, password_hash FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&u.ID, &u.Email, &u.Timezone, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	return u, true, nil
}

// CreateSession logs a user in and returns the token for their cookie.
func (s *Store) CreateSession(ctx context.Context, userID int64) (token string, expires time.Time, err error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	err = s.db.QueryRow(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + $3::interval) RETURNING expires_at`,
		userID, hash, auth.SessionLength.String()).Scan(&expires)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expires, nil
}

// SessionUser returns the user a cookie token belongs to, or found=false
// if it is unknown or expired. If the session wasn't used in the last hour
// it is renewed for another SessionLength (renewed=true, with the new
// expiry), so active users stay logged in. Renewing at most hourly keeps
// page loads from writing to the database every time.
func (s *Store) SessionUser(ctx context.Context, token string) (u User, found, renewed bool, expires time.Time, err error) {
	hash := auth.HashToken(token)
	var lastUsed time.Time
	err = s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.timezone, s.last_used_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hash,
	).Scan(&u.ID, &u.Email, &u.Timezone, &lastUsed, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, false, time.Time{}, nil
	}
	if err != nil {
		return User{}, false, false, time.Time{}, fmt.Errorf("find session: %w", err)
	}

	if time.Since(lastUsed) > time.Hour {
		err = s.db.QueryRow(ctx,
			`UPDATE sessions SET last_used_at = now(), expires_at = now() + $2::interval WHERE token_hash = $1 RETURNING expires_at`,
			hash, auth.SessionLength.String()).Scan(&expires)
		if err != nil {
			return User{}, false, false, time.Time{}, fmt.Errorf("renew session: %w", err)
		}
		renewed = true
	}
	return u, true, renewed, expires, nil
}

// SetTimezone records the timezone a user's browser reported at login,
// so times follow them if they travel or move.
func (s *Store) SetTimezone(ctx context.Context, userID int64, timezone string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET timezone = $2 WHERE id = $1 AND timezone <> $2`, userID, timezone)
	return err
}

// DeleteSession logs a cookie token out. Unknown tokens are ignored.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, auth.HashToken(token))
	return err
}

// DeleteExpiredSessions removes sessions nobody can use any more.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}

// Rules for "new since you last looked" (decided 2026-11-08).
const (
	// AwayGap: a visit after this long away starts a new session, and
	// everything up to the end of the last one counts as seen. Shorter gaps
	// (refreshing, paging) keep the New badges.
	AwayGap = 30 * time.Minute
	// FirstVisitWindow: on a first visit, the last day counts as new.
	FirstVisitWindow = 24 * time.Hour
)

// ViewFeed records that a user opened the feed and returns the "new since"
// boundary to use. One UPDATE does the whole decision, so two tabs opened
// at once can't move the boundary twice:
//   - first visit ever: new since FirstVisitWindow ago;
//   - back after AwayGap or more: new since the end of the last visit;
//   - otherwise (refresh, next page): the boundary doesn't move.
func (s *Store) ViewFeed(ctx context.Context, userID int64) (time.Time, error) {
	var since time.Time
	err := s.db.QueryRow(ctx, `
		UPDATE users SET
			last_seen_feed_at = CASE
				WHEN last_feed_view_at IS NULL THEN now() - $2::interval
				WHEN last_feed_view_at < now() - $3::interval
					-- GREATEST: "Mark all as seen" may already be later than the last view
					THEN GREATEST(last_feed_view_at, last_seen_feed_at)
				ELSE last_seen_feed_at
			END,
			last_feed_view_at = now()
		WHERE id = $1
		RETURNING last_seen_feed_at`,
		userID, FirstVisitWindow.String(), AwayGap.String()).Scan(&since)
	if err != nil {
		return time.Time{}, fmt.Errorf("record feed view: %w", err)
	}
	return since, nil
}

// FeedNewSince returns the current boundary without counting a visit
// (for the JSON API). ok is false if the user has never opened the feed.
func (s *Store) FeedNewSince(ctx context.Context, userID int64) (since time.Time, ok bool, err error) {
	var t *time.Time
	err = s.db.QueryRow(ctx, `SELECT last_seen_feed_at FROM users WHERE id = $1`, userID).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, false, err
	}
	return *t, true, nil
}

// MarkAllSeen moves the boundary to now: "Mark all as seen".
func (s *Store) MarkAllSeen(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET last_seen_feed_at = now(), last_feed_view_at = now() WHERE id = $1`, userID)
	return err
}
