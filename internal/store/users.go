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
func (s *Store) SignUp(ctx context.Context, inviteToken, email, passwordHash string) (User, error) {
	var u User
	err := s.InTx(ctx, func(tx *Store) error {
		// FOR UPDATE locks the invite row, so two people using the last
		// seat of a group link at the same moment can't both get in.
		var inviteID int64
		err := tx.db.QueryRow(ctx, `
			SELECT id FROM invites
			WHERE token_hash = $1 AND expires_at > now() AND uses < max_uses
			FOR UPDATE`,
			auth.HashToken(inviteToken)).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInviteInvalid
		}
		if err != nil {
			return fmt.Errorf("sign up: %w", err)
		}

		err = tx.db.QueryRow(ctx,
			`INSERT INTO users (email, password_hash, invite_id) VALUES ($1, $2, $3) RETURNING id, email`,
			email, passwordHash, inviteID).Scan(&u.ID, &u.Email)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on lower(email)
			return ErrEmailTaken
		}
		if err != nil {
			return fmt.Errorf("sign up: %w", err)
		}

		_, err = tx.db.Exec(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = $1`, inviteID)
		return err
	})
	return u, err
}

// UserByEmail finds an account for login, ignoring letter case.
// found is false if there is none.
func (s *Store) UserByEmail(ctx context.Context, email string) (u User, found bool, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT id, email, password_hash FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash)
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
		SELECT u.id, u.email, s.last_used_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hash,
	).Scan(&u.ID, &u.Email, &lastUsed, &expires)
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
