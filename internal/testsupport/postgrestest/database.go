// Package postgrestest provides isolated PostgreSQL schemas for integration tests.
package postgrestest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaSequence atomic.Uint64

func Open(t *testing.T) *pgxpool.Pool {
	return open(t, 0)
}

// OpenThroughMigration creates an isolated schema with historical migrations
// applied only through the requested inclusive sequence.
func OpenThroughMigration(t *testing.T, lastSequence int) *pgxpool.Pool {
	t.Helper()
	if lastSequence < 1 {
		t.Fatalf("last migration sequence must be positive")
	}
	return open(t, lastSequence)
}

func OpenSiblingWithTracer(
	t *testing.T,
	source *pgxpool.Pool,
	tracer pgx.QueryTracer,
) *pgxpool.Pool {
	t.Helper()
	if source == nil || tracer == nil {
		t.Fatal("PostgreSQL source pool and tracer are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config := source.Config()
	config.ConnConfig.Tracer = tracer
	sibling, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open traced PostgreSQL sibling pool: %v", err)
	}
	if err := sibling.Ping(ctx); err != nil {
		sibling.Close()
		t.Fatalf("ping traced PostgreSQL sibling pool: %v", err)
	}
	t.Cleanup(sibling.Close)
	return sibling
}

func open(t *testing.T, lastSequence int) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("SWITCHBACK_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SWITCHBACK_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	schema := fmt.Sprintf("switchback_test_%d_%d", time.Now().UnixNano(), schemaSequence.Add(1))
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("parse PostgreSQL test config: %v", err)
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "SET search_path TO "+identifier)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("open isolated PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("ping isolated PostgreSQL pool: %v", err)
	}
	if _, err := pool.Exec(ctx, migrationUp(t, lastSequence)); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("apply migration: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	return pool
}

func Restart(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	config := pool.Config().Copy()
	pool.Close()
	restarted, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("restart PostgreSQL pool: %v", err)
	}
	if err := restarted.Ping(ctx); err != nil {
		restarted.Close()
		t.Fatalf("ping restarted PostgreSQL pool: %v", err)
	}
	t.Cleanup(restarted.Close)
	return restarted
}

func migrationUp(t *testing.T, lastSequence int) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate PostgreSQL test helper")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	files, err := filepath.Glob(filepath.Join(root, "migrations", "[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("list PostgreSQL migrations: files=%d error=%v", len(files), err)
	}
	var migration strings.Builder
	for _, file := range files {
		name := filepath.Base(file)
		sequence, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatalf("parse migration sequence %s: %v", name, err)
		}
		if lastSequence != 0 && sequence > lastSequence {
			break
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		up, _, ok := strings.Cut(string(data), "-- migrate:down")
		if !ok {
			t.Fatalf("migration %s lacks down marker", name)
		}
		migration.WriteString(up)
		migration.WriteByte('\n')
	}
	return migration.String()
}
