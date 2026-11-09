package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Stage is where an application stands.
type Stage string

const (
	StageSaved     Stage = "saved"
	StageApplied   Stage = "applied"
	StageOA        Stage = "online_assessment"
	StageInterview Stage = "interview"
	StageOffer     Stage = "offer"
	StageRejected  Stage = "rejected"
	StageWithdrawn Stage = "withdrawn"
)

// Stages in the order the My applications page lists them: furthest along first.
var Stages = []Stage{StageOffer, StageInterview, StageOA, StageApplied, StageSaved, StageRejected, StageWithdrawn}

// StageLabels are the names people see.
var StageLabels = map[Stage]string{
	StageSaved: "Saved", StageApplied: "Applied", StageOA: "Online assessment",
	StageInterview: "Interview", StageOffer: "Offer", StageRejected: "Rejected", StageWithdrawn: "Withdrawn",
}

// ValidStage reports whether s is a known stage.
func ValidStage(s string) bool { _, ok := StageLabels[Stage(s)]; return ok }

// ErrNotFound: no such application for this user. Another user's
// application is "not found" too, so ids can't be probed.
var ErrNotFound = errors.New("not found")

// Application is one user's application, with what the pages show of its listing.
type Application struct {
	ID              int64
	ListingID       int64
	Company         string
	Title           string
	Season          string
	ListingStatus   string // open, stale, closed
	ClosedReason    string // when closed: "source" or "link"
	Private         bool   // a listing only this user can see (added by link)
	Stage           Stage
	InterviewRound  *int
	ExpectedRounds  *int
	AppliedOn       *time.Time
	ResumeVersionID *int64
	ResumeLabel     string
	Notes           string
	UpdatedAt       time.Time
}

// Event is one stage change.
type Event struct {
	From           *Stage
	To             Stage
	InterviewRound *int
	At             time.Time
}

// ResumeVersion is one of a user's resume labels.
type ResumeVersion struct {
	ID    int64
	Label string
}

// StartApplication saves or marks applied a listing for a user. If the
// user already has an application for it (say, a double-click), that one
// is returned with created=false: the UNIQUE (user_id, listing_id) rule
// decides, not a check-then-insert in Go. The one change it makes to an
// existing application: "applied" on a saved one moves it to applied
// (never backwards, so a double-click can't undo an interview).
func (s *Store) StartApplication(ctx context.Context, userID, listingID int64, stage Stage, appliedOn time.Time) (id int64, created bool, err error) {
	if stage != StageSaved && stage != StageApplied {
		return 0, false, fmt.Errorf("start application: stage must be saved or applied, not %q", stage)
	}
	var on *time.Time
	if stage == StageApplied {
		on = &appliedOn
	}
	err = s.InTx(ctx, func(tx *Store) error {
		err := tx.db.QueryRow(ctx, `
			INSERT INTO applications (user_id, listing_id, stage, applied_on, resume_version_id)
			VALUES ($1, $2, $3, $4,
			        -- default to the resume this user used most recently
			        (SELECT resume_version_id FROM applications
			         WHERE user_id = $1 AND resume_version_id IS NOT NULL
			         ORDER BY updated_at DESC LIMIT 1))
			ON CONFLICT (user_id, listing_id) DO NOTHING
			RETURNING id`,
			userID, listingID, stage, on).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) { // it already existed
			var existing Stage
			err := tx.db.QueryRow(ctx,
				`SELECT id, stage FROM applications WHERE user_id = $1 AND listing_id = $2`, userID, listingID).Scan(&id, &existing)
			if err != nil || existing != StageSaved || stage != StageApplied {
				return err
			}
			if _, err := tx.db.Exec(ctx,
				`UPDATE applications SET stage = 'applied', applied_on = $2, updated_at = now() WHERE id = $1`, id, on); err != nil {
				return err
			}
			_, err = tx.db.Exec(ctx,
				`INSERT INTO application_events (application_id, from_stage, to_stage) VALUES ($1, 'saved', 'applied')`, id)
			return err
		}
		if err != nil {
			return err
		}
		created = true
		_, err = tx.db.Exec(ctx,
			`INSERT INTO application_events (application_id, to_stage) VALUES ($1, $2)`, id, stage)
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("start application: %w", err)
	}
	return id, created, nil
}

// MoveStage changes an application's stage and records it. Any move is
// allowed; fixing a mistake is just another move. Moving to interview
// without a round number means the next round (1 if not interviewing yet).
// Moving past "saved" without an applied date sets it to today, the
// user's today (pass the current time in their timezone).
func (s *Store) MoveStage(ctx context.Context, userID, appID int64, to Stage, round *int, today time.Time) error {
	if !ValidStage(string(to)) {
		return fmt.Errorf("move stage: unknown stage %q", to)
	}
	return s.InTx(ctx, func(tx *Store) error {
		var from Stage
		var oldRound *int
		err := tx.db.QueryRow(ctx,
			`SELECT stage, interview_round FROM applications WHERE id = $1 AND user_id = $2 FOR UPDATE`,
			appID, userID).Scan(&from, &oldRound)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("move stage: %w", err)
		}

		var newRound *int
		if to == StageInterview {
			n := 1
			switch {
			case round != nil && *round >= 1:
				n = *round
			case oldRound != nil:
				n = *oldRound + 1
			}
			newRound = &n
		}
		if from == to && (to != StageInterview || *newRound == *oldRound) {
			return nil // nothing changed; don't record a non-move
		}

		_, err = tx.db.Exec(ctx, `
			UPDATE applications SET stage = $3, interview_round = $4, updated_at = now(),
			       applied_on = CASE WHEN $3 <> 'saved' THEN COALESCE(applied_on, $5::date) ELSE applied_on END
			WHERE id = $1 AND user_id = $2`,
			// The user's date, not the database's: it may be tomorrow in UTC.
			appID, userID, to, newRound, today.Format("2006-01-02"))
		if err != nil {
			return fmt.Errorf("move stage: %w", err)
		}
		_, err = tx.db.Exec(ctx,
			`INSERT INTO application_events (application_id, from_stage, to_stage, interview_round) VALUES ($1, $2, $3, $4)`,
			appID, from, to, newRound)
		return err
	})
}

// Details are the editable fields of an application.
type Details struct {
	AppliedOn       *time.Time
	ResumeVersionID *int64
	ExpectedRounds  *int
	Notes           string
}

// UpdateDetails saves an application's editable fields. A resume that
// isn't this user's is refused by the database (a foreign key on
// resume_version_id + user_id).
func (s *Store) UpdateDetails(ctx context.Context, userID, appID int64, d Details) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE applications SET
			applied_on        = CASE WHEN stage = 'saved' THEN $3 ELSE COALESCE($3, applied_on) END,
			resume_version_id = $4, expected_rounds = $5, notes = $6, updated_at = now()
		WHERE id = $1 AND user_id = $2`,
		appID, userID, d.AppliedOn, d.ResumeVersionID, d.ExpectedRounds, d.Notes)
	if err != nil {
		return fmt.Errorf("update application: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteApplication removes an application and its history.
func (s *Store) DeleteApplication(ctx context.Context, userID, appID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM applications WHERE id = $1 AND user_id = $2`, appID, userID)
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const applicationColumns = `
	a.id, a.listing_id, c.name, l.title, l.season, l.status, COALESCE(l.closed_reason, ''), l.owner_user_id IS NOT NULL, a.stage, a.interview_round, a.expected_rounds,
	a.applied_on, a.resume_version_id, COALESCE(r.label, ''), a.notes, a.updated_at
	FROM applications a
	JOIN listings l ON l.id = a.listing_id
	JOIN companies c ON c.id = l.company_id
	LEFT JOIN resume_versions r ON r.id = a.resume_version_id`

func scanApplication(row pgx.CollectableRow) (Application, error) {
	var a Application
	err := row.Scan(&a.ID, &a.ListingID, &a.Company, &a.Title, &a.Season, &a.ListingStatus, &a.ClosedReason, &a.Private, &a.Stage,
		&a.InterviewRound, &a.ExpectedRounds, &a.AppliedOn, &a.ResumeVersionID, &a.ResumeLabel, &a.Notes, &a.UpdatedAt)
	return a, err
}

// Applications lists a user's applications, most recently changed first.
func (s *Store) Applications(ctx context.Context, userID int64) ([]Application, error) {
	rows, err := s.db.Query(ctx, `SELECT `+applicationColumns+`
		WHERE a.user_id = $1 ORDER BY a.updated_at DESC, a.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("applications: %w", err)
	}
	return pgx.CollectRows(rows, scanApplication)
}

// Application returns one of a user's applications and its history, oldest first.
func (s *Store) Application(ctx context.Context, userID, appID int64) (Application, []Event, error) {
	rows, err := s.db.Query(ctx, `SELECT `+applicationColumns+` WHERE a.id = $1 AND a.user_id = $2`, appID, userID)
	if err != nil {
		return Application{}, nil, fmt.Errorf("application: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanApplication)
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, nil, ErrNotFound
	}
	if err != nil {
		return Application{}, nil, fmt.Errorf("application: %w", err)
	}

	rows, err = s.db.Query(ctx, `
		SELECT from_stage, to_stage, interview_round, at FROM application_events
		WHERE application_id = $1 ORDER BY at, id`, appID)
	if err != nil {
		return Application{}, nil, fmt.Errorf("application history: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		return e, row.Scan(&e.From, &e.To, &e.InterviewRound, &e.At)
	})
	return a, events, err
}

// ResumeVersions lists a user's resume labels, newest first.
func (s *Store) ResumeVersions(ctx context.Context, userID int64) ([]ResumeVersion, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, label FROM resume_versions WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("resume versions: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ResumeVersion, error) {
		var r ResumeVersion
		return r, row.Scan(&r.ID, &r.Label)
	})
}

// AddResumeVersion returns the id of the user's resume with this label,
// creating it if needed ("backend v2" and "Backend V2" are the same label).
func (s *Store) AddResumeVersion(ctx context.Context, userID int64, label string) (int64, error) {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return 0, errors.New("resume label is empty")
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO resume_versions (user_id, label) VALUES ($1, $2)
			ON CONFLICT (user_id, lower(label)) DO NOTHING RETURNING id
		)
		SELECT id FROM ins
		UNION ALL
		SELECT id FROM resume_versions WHERE user_id = $1 AND lower(label) = lower($2)
		LIMIT 1`, userID, label).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add resume version: %w", err)
	}
	return id, nil
}

// StageOrder is a stage's position in Stages, for sorting.
func StageOrder(s Stage) int { return slices.Index(Stages, s) }
