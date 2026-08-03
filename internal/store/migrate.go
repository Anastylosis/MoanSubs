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

// Migrate applies every embedded migration whose filename isn't already
// recorded in schema_migrations, in filename order (hence the 0001_, 0002_
// prefix convention), each inside its own transaction. Safe to call on
// every process startup: already-applied migrations are no-ops, so this
// also serves as the idempotency check PLAN.md's Verification section asks
// for ("migrations apply cleanly and are idempotent on re-run").
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
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
