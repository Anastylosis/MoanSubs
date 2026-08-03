// Package store is the Postgres persistence layer for moansubs, via
// github.com/jackc/pgx/v5 (pgxpool). No ORM, no external migration
// framework — migrations are embedded .sql files applied in filename
// order (see migrate.go).
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by Get* methods when no row matches.
var ErrNotFound = errors.New("store: not found")

// Store wraps a pgx connection pool. Safe for concurrent use — pgxpool
// handles pooling internally.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres at dsn and applies any pending migrations.
// Callers own the returned Store and must Close it.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connecting: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close closes the underlying connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool exposes the raw pgxpool.Pool for tests and the rare ad-hoc query;
// production code should prefer the typed methods on Store.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
