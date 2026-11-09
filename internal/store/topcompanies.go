package store

import (
	"context"
	"fmt"

	"internship-tracker/internal/listing"
)

// SyncTopCompanies makes signal's set of top companies exactly the given
// names: companies it no longer flags lose this signal (but keep any
// other). Names are matched to stored companies by normalized name;
// a name we have no listings for yet is skipped. Returns how many
// companies the signal now flags.
func (s *Store) SyncTopCompanies(ctx context.Context, signal string, names []string) (int, error) {
	normalized := make([]string, 0, len(names))
	for _, n := range names {
		normalized = append(normalized, listing.NormalizeCompany(n))
	}
	var n int
	err := s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx, `
			DELETE FROM company_top_signals t USING companies c
			WHERE t.company_id = c.id AND t.signal = $1 AND NOT (c.normalized_name = ANY($2))`,
			signal, normalized); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, `
			INSERT INTO company_top_signals (company_id, signal)
			SELECT id, $1 FROM companies WHERE normalized_name = ANY($2)
			ON CONFLICT DO NOTHING`,
			signal, normalized); err != nil {
			return err
		}
		return tx.db.QueryRow(ctx, `SELECT count(*) FROM company_top_signals WHERE signal = $1`, signal).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("sync top companies (%s): %w", signal, err)
	}
	return n, nil
}

// SignalFile is an extra file read only for signals, with its saved ETag.
type SignalFile struct {
	Name, URL, ETag string
}

// SignalFileByName loads a signal file, e.g. "simplify-readme".
func (s *Store) SignalFileByName(ctx context.Context, name string) (SignalFile, error) {
	f := SignalFile{Name: name}
	err := s.db.QueryRow(ctx, `SELECT url, COALESCE(etag, '') FROM signal_files WHERE name = $1`, name).Scan(&f.URL, &f.ETag)
	if err != nil {
		return SignalFile{}, fmt.Errorf("load signal file %q: %w", name, err)
	}
	return f, nil
}

// MarkSignalFetched saves the ETag to send next time.
func (s *Store) MarkSignalFetched(ctx context.Context, name, etag string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE signal_files SET etag = NULLIF($2, ''), last_fetched_at = now() WHERE name = $1`, name, etag)
	return err
}
