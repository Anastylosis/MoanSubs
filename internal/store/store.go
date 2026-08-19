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
	// tokenKey is MOANSUBS_TOKEN_KEY (WP-C8), installed via SetTokenKey.
	// nil means no key was configured: every Create*/Rotate* account method
	// leaves accounts.token_enc NULL in that case, and DecryptToken always
	// fails closed.
	tokenKey []byte
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

// SetTokenKey installs the AES-256-GCM key (32 bytes) used to write and
// read accounts.token_enc (WP-C8): serve.go parses MOANSUBS_TOKEN_KEY (64
// hex chars) once at startup and calls this before serving any traffic.
// Never called at all — the CLI's openStore, and tests that don't care —
// leaves token_enc NULL on every account this Store creates or rotates,
// and DecryptToken always reports "can't recover it".
func (s *Store) SetTokenKey(key []byte) {
	s.tokenKey = key
}
