package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"internship-tracker/internal/listing"
)

// SyncIndustries recomputes every company's industry tags from the rules
// in listing.Industries. It runs after each ingest, so a rule change (say,
// a new "never" entry) takes effect on the next run.
//
// It upserts the tags it wants and deletes the rest, rather than deleting
// everything and re-inserting: two runs at once can then never insert the
// same row twice.
func (s *Store) SyncIndustries(ctx context.Context) error {
	return s.InTx(ctx, func(tx *Store) error {
		rows, err := tx.db.Query(ctx, `SELECT id, name FROM companies`)
		if err != nil {
			return fmt.Errorf("sync industries: %w", err)
		}
		type company struct {
			id   int64
			name string
		}
		companies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (company, error) {
			var c company
			return c, row.Scan(&c.id, &c.name)
		})
		if err != nil {
			return fmt.Errorf("sync industries: %w", err)
		}

		// Three parallel arrays: Postgres unnests them back into rows.
		ids, industries, needsReview := []int64{}, []string{}, []bool{}
		for _, c := range companies {
			for _, m := range listing.Industries(c.name) {
				ids = append(ids, c.id)
				industries = append(industries, string(m.Industry))
				needsReview = append(needsReview, m.NeedsReview)
			}
		}

		_, err = tx.db.Exec(ctx, `
			WITH wanted AS (
				SELECT * FROM unnest($1::bigint[], $2::text[], $3::bool[]) AS w(company_id, industry, needs_review)
			),
			removed AS (
				DELETE FROM company_industries ci
				WHERE NOT EXISTS (SELECT 1 FROM wanted w
				                  WHERE w.company_id = ci.company_id AND w.industry = ci.industry)
			)
			INSERT INTO company_industries (company_id, industry, needs_review)
			SELECT company_id, industry, needs_review FROM wanted
			ON CONFLICT (company_id, industry) DO UPDATE SET needs_review = EXCLUDED.needs_review`,
			ids, industries, needsReview)
		if err != nil {
			return fmt.Errorf("sync industries: %w", err)
		}
		return nil
	})
}

// IndustriesToReview lists keyword-only tags nobody has approved yet,
// e.g. "Medical Properties Trust: healthcare".
func (s *Store) IndustriesToReview(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.name || ': ' || ci.industry
		FROM company_industries ci JOIN companies c ON c.id = ci.company_id
		WHERE ci.needs_review
		ORDER BY ci.industry, c.name`)
	if err != nil {
		return nil, fmt.Errorf("industries to review: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
