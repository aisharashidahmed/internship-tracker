// Package store is the only place that talks SQL. Other packages call its
// methods with Go types (listing.Listing, etc.) and never see a query.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbtx is what both a connection pool and a transaction can do, so the same
// Store methods work inside or outside a transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store runs queries against Postgres.
type Store struct {
	db dbtx
}

// New returns a Store backed by a connection pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{db: pool}
}

// InTx runs fn inside one transaction: if fn returns an error, everything it
// wrote is rolled back; otherwise it is all committed together.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		return fn(&Store{db: tx})
	})
}
