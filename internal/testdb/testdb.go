// Package testdb gives each test package its own Postgres database so
// package test binaries can run in parallel without sharing tables.
package testdb

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	once   sync.Once
	dsn    string
	setErr error
)

// DSN returns a connection string for this package's private database,
// recreated empty on first use per process. It skips the test when
// DATABASE_URL is unset. Needs only CREATEDB, not superuser.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL not set; skipping database tests")
	}
	once.Do(func() { dsn, setErr = create(base) })
	if setErr != nil {
		t.Fatalf("testdb: %v", setErr)
	}
	return dsn
}

var nonIdent = regexp.MustCompile(`[^a-z0-9]+`)

func create(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("DATABASE_URL must be a postgres:// URL")
	}
	orig := strings.TrimPrefix(u.Path, "/")

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	name := orig + "_" + nonIdent.ReplaceAllString(strings.ToLower(pkgPath(wd)), "_")
	if len(name) > 50 {
		sum := sha1.Sum([]byte(name))
		name = name[:40] + "_" + hex.EncodeToString(sum[:])[:8]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()

	q := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+q+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+q); err != nil {
		return "", err
	}

	u.Path = "/" + name
	return u.String(), nil
}

// pkgPath is the package directory relative to the module root.
func pkgPath(wd string) string {
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			rel, _ := filepath.Rel(dir, wd)
			return rel
		}
		if filepath.Dir(dir) == dir {
			return filepath.Base(wd)
		}
	}
}
