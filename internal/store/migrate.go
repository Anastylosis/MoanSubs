package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationsFS embeds every .sql migration file; no external migration
// binary or framework, per PLAN.md's "Keep dependencies minimal".
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockKey is the fixed pg_advisory_lock key Migrate serializes
// on — the bytes of "moansubs" read as a big-endian bigint. Any process
// restart or second replica calling Migrate against the same DB at the
// same time otherwise races check-then-apply per file (each file+record is
// its own tx, so it's not corruption, but the loser hits a duplicate-key
// error on schema_migrations and crash-loops).
const migrationLockKey int64 = 0x6d6f616e73756273

// Migrate applies every embedded migration whose filename isn't already
// recorded in schema_migrations, in filename order (hence the 0001_, 0002_
// prefix convention), each inside its own transaction. Safe to call on
// every process startup: already-applied migrations are no-ops, so this
// also serves as the idempotency check PLAN.md's Verification section asks
// for ("migrations apply cleanly and are idempotent on re-run").
//
// The whole run holds a session-level pg_advisory_lock on one dedicated
// connection (migrationLockKey): a concurrent Migrate against the same DB
// blocks until the first finishes rather than racing it.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	pconn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquiring connection for migration lock: %w", err)
	}
	// Hijacked (and explicitly Closed, never Released) rather than a plain
	// pool checkout: Open (WP-P9) applies statement_timeout to every
	// pooled connection, and that cap also bounds pg_advisory_lock's
	// blocking wait below — a second replica legitimately waiting out a
	// slow first migration would otherwise have its lock wait itself
	// cancelled with 57014 instead of blocking. Since the whole point here
	// is an unbounded wait, statement_timeout is disabled on this one
	// connection; Closing it rather than Release-ing it afterwards is what
	// keeps that disablement from leaking into whatever the pool hands
	// this physical connection to next.
	conn := pconn.Hijack()
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, `SET statement_timeout = 0`); err != nil {
		return fmt.Errorf("store: disabling statement_timeout for migration lock: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("store: acquiring migration lock: %w", err)
	}
	defer func() {
		// Best-effort: an unlock failure here just leaves the lock held
		// until this connection closes right after, which still releases
		// it (session-level advisory locks die with their session).
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("store: creating schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		applied, err := migrationApplied(ctx, pool, name)
		if err != nil {
			return fmt.Errorf("store: checking migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, pool, name); err != nil {
			return err
		}
	}
	return nil
}

// migrationNames lists the embedded .sql files in filename order.
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: reading embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func migrationApplied(ctx context.Context, pool *pgxpool.Pool, name string) (bool, error) {
	var applied bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
	).Scan(&applied)
	return applied, err
}

// applyMigration runs one migration file's SQL and records it, both inside
// a single transaction so a failure partway through never leaves the
// migration half-applied and half-recorded.
func applyMigration(ctx context.Context, pool *pgxpool.Pool, name string) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("store: reading migration %s: %w", name, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: beginning tx for migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	// Migration DDL is exempt from the pool's statement_timeout (WP-P9):
	// that cap protects against slow application queries, not a schema
	// change that may legitimately need longer on a large existing table
	// (e.g. building an index). SET LOCAL confines this to the migration's
	// own transaction.
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return fmt.Errorf("store: disabling statement_timeout for migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("store: applying migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("store: recording migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: committing migration %s: %w", name, err)
	}
	return nil
}
