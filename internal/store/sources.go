package store

import (
	"context"
	"fmt"
)

// Source is a row of the sources table.
type Source struct {
	ID   int64
	Name string
	URL  string
	ETag string // empty if we have never fetched it
	// FirstLoad is true until the source's first successful ingest. Listings
	// created during it are sorted in the feed by their posted date.
	FirstLoad bool
	// RulesVersion is the listing.RulesVersion its listings were last saved
	// with; "" if never.
	RulesVersion string
}

// SourceByName loads a source, e.g. "simplify".
func (s *Store) SourceByName(ctx context.Context, name string) (Source, error) {
	src := Source{Name: name}
	err := s.db.QueryRow(ctx,
		`SELECT id, url, COALESCE(etag, ''), last_fetched_at IS NULL, COALESCE(rules_version, '')
		 FROM sources WHERE name = $1`, name,
	).Scan(&src.ID, &src.URL, &src.ETag, &src.FirstLoad, &src.RulesVersion)
	if err != nil {
		return Source{}, fmt.Errorf("load source %q: %w", name, err)
	}
	return src, nil
}

// MarkFetched records a successful fetch, the ETag to send next time, and
// the rules version the data was saved with ("" leaves it unchanged, for a
// 304 where nothing was saved).
func (s *Store) MarkFetched(ctx context.Context, sourceID int64, etag, rulesVersion string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE sources SET etag = NULLIF($2, ''), last_fetched_at = now(),
		       rules_version = COALESCE(NULLIF($3, ''), rules_version)
		WHERE id = $1`,
		sourceID, etag, rulesVersion)
	return err
}

// TryLockSource takes a lock that only one ingest of this source can hold,
// so the hourly run and a manual `go run ./cmd/ingest` never write the same
// source at once. It returns false at once (no waiting) if another run holds
// it. The lock is released when the transaction ends; call it inside InTx.
func (s *Store) TryLockSource(ctx context.Context, sourceID int64) (bool, error) {
	var ok bool
	// Advisory locks are named by a number; the two-number form
	// (namespace, id) keeps "ingest" locks apart from any others we add later.
	err := s.db.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext('ingest-source'), $1::int)`, sourceID,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("lock source %d: %w", sourceID, err)
	}
	return ok, nil
}
