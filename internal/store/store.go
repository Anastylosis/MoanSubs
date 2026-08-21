// Package store is the Postgres persistence layer for moansubs, via
// github.com/jackc/pgx/v5 (pgxpool). No ORM, no external migration
// framework — migrations are embedded .sql files applied in filename
// order (see migrate.go).
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

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

// DefaultStatementTimeout is the statement_timeout Open applies to every
// pooled connection when the DSN doesn't already set one and the caller
// passes no Options (WP-P9): an anonymous fuzzy phash lookup is a full
// bit_count table scan and CreatorNames is a DISTINCT+unnest over every
// release, so without a cap a burst of slow requests pins every connection
// with nothing able to kill them.
const DefaultStatementTimeout = 30 * time.Second

// maxConnLifetime and healthCheckPeriod are fixed, not operator-tunable —
// unlike the statement timeout there's no known reason a deployment would
// need to change either (WP-P9).
const (
	maxConnLifetime   = time.Hour
	healthCheckPeriod = 30 * time.Second
)

// Options configures Open beyond the bare DSN.
type Options struct {
	// StatementTimeout overrides DefaultStatementTimeout, in the sense
	// that once an Options value is passed to Open at all, this field's
	// literal value governs: 0 means no limit, a positive duration is the
	// timeout to apply. Passing no Options to Open is what selects
	// DefaultStatementTimeout — there is deliberately no way to write
	// "unset, please default" once you're constructing an Options value,
	// since callers that don't care simply omit the argument. The DSN's
	// own statement_timeout parameter, if present, always wins over this
	// (see MANUAL.md).
	StatementTimeout time.Duration
}

// Open connects to Postgres at dsn and applies any pending migrations.
// Callers own the returned Store and must Close it. opts is variadic
// rather than a plain parameter so every existing call site keeps
// compiling unchanged; at most the first value is used.
func Open(ctx context.Context, dsn string, opts ...Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parsing dsn: %w", err)
	}

	if _, dsnSetsTimeout := cfg.ConnConfig.RuntimeParams["statement_timeout"]; !dsnSetsTimeout {
		timeout := DefaultStatementTimeout
		setTimeout := true
		if len(opts) > 0 {
			timeout = opts[0].StatementTimeout
			setTimeout = timeout > 0
		}
		if setTimeout {
			cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
		}
	}
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.HealthCheckPeriod = healthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
