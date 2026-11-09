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
}

// SourceByName loads a source, e.g. "simplify".
func (s *Store) SourceByName(ctx context.Context, name string) (Source, error) {
	src := Source{Name: name}
	err := s.db.QueryRow(ctx,
		`SELECT id, url, COALESCE(etag, ''), last_fetched_at IS NULL FROM sources WHERE name = $1`, name,
	).Scan(&src.ID, &src.URL, &src.ETag, &src.FirstLoad)
	if err != nil {
		return Source{}, fmt.Errorf("load source %q: %w", name, err)
	}
	return src, nil
}

// MarkFetched records a successful fetch and the ETag to send next time.
func (s *Store) MarkFetched(ctx context.Context, sourceID int64, etag string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sources SET etag = NULLIF($2, ''), last_fetched_at = now() WHERE id = $1`,
		sourceID, etag)
	return err
}
