//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestIntegrationEventV2MigrationRunnerBlocksUndrained021AndAppliesAfterDrain(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	latestMigrationVersion, expectedMigrationCount := productionMigrationInventory(t)
	recordAppliedMigrationsThrough(t, pool, 21)
	assertReceiptMigrationIndexes(t, pool, true, false)
	historicalIDs := []string{
		"event-v1-client-published",
		"event-v1-client-unpublished",
	}
	beforeRows := make(map[string]string, len(historicalIDs))
	beforeDigests := make(map[string]string, len(historicalIDs))
	for _, eventID := range historicalIDs {
		streamID := "task/" + eventID
		insertOwnerStream(t, pool, eventID, streamID)
		if _, err := insertMigrationEvent(
			ctx, pool, eventID, streamID, "event.v1", "client-"+eventID,
		); err != nil {
			t.Fatalf("seed historical 000021 event.v1 client row: %v", err)
		}
		beforeRows[eventID], beforeDigests[eventID] = migrationEventEvidence(t, pool, eventID)
	}
	markMigrationEventDrained(t, pool, historicalIDs[0], beforeDigests[historicalIDs[0]])
	beforeRows[historicalIDs[0]], beforeDigests[historicalIDs[0]] =
		migrationEventEvidence(t, pool, historicalIDs[0])
	beforeSourceDigest := sourceReceiptDigest(t, pool, historicalIDs[0])
	assertSourceReceiptRecorded(t, pool, historicalIDs[1], false)
	beforeSchema := fullMigrationSchemaFingerprint(t, pool)
	beforeLedger := migrationLedgerEvidence(t, pool)
	before021AppliedAt := migrationAppliedAt(t, pool, "000021")

	output, err := runProductionMigrationRunner(t, pool)
	if err == nil ||
		!strings.Contains(output, "drain with v1-compatible binary before upgrade") {
		t.Fatalf("undrained 000021 migration error=%v output=%s", err, output)
	}
	afterRejectedSchema := fullMigrationSchemaFingerprint(t, pool)
	if !reflect.DeepEqual(afterRejectedSchema, beforeSchema) {
		t.Fatalf("rejected migration changed schema\nbefore=%v\nafter=%v",
			beforeSchema, afterRejectedSchema)
	}
	if got := migrationLedgerEvidence(t, pool); got != beforeLedger {
		t.Fatalf("rejected migration changed production ledger\nbefore=%s\nafter=%s",
			beforeLedger, got)
	}
	for _, eventID := range historicalIDs {
		afterRow, afterDigest := migrationEventEvidence(t, pool, eventID)
		if afterRow != beforeRows[eventID] || afterDigest != beforeDigests[eventID] {
			t.Fatalf(
				"rejected migration changed historical event %s\nrow %s -> %s\ndigest %s -> %s",
				eventID, beforeRows[eventID], afterRow, beforeDigests[eventID], afterDigest,
			)
		}
	}
	if got := sourceReceiptDigest(t, pool, historicalIDs[0]); got != beforeSourceDigest {
		t.Fatalf("rejected migration changed source receipt digest=%q want=%q",
			got, beforeSourceDigest)
	}
	assertSourceReceiptRecorded(t, pool, historicalIDs[1], false)
	assertMigrationRecorded(t, pool, "000021", true)
	assertMigrationRecorded(t, pool, "000022", false)

	markMigrationEventDrained(t, pool, historicalIDs[1], beforeDigests[historicalIDs[1]])
	assertNoUnpublishedGrandfather(t, pool)
	drainedRows := make(map[string]string, len(historicalIDs))
	drainedDigests := make(map[string]string, len(historicalIDs))
	drainedSourceDigests := make(map[string]string, len(historicalIDs))
	for _, eventID := range historicalIDs {
		drainedRows[eventID], drainedDigests[eventID] = migrationEventEvidence(t, pool, eventID)
		drainedSourceDigests[eventID] = sourceReceiptDigest(t, pool, eventID)
	}

	output, err = runProductionMigrationRunner(t, pool)
	if err != nil {
		t.Fatalf("apply migration runner after v1-compatible drain: %v\n%s", err, output)
	}
	if !strings.Contains(output, "switchback_schema_version="+latestMigrationVersion) {
		t.Fatalf("migration runner output=%s want latest version=%s", output, latestMigrationVersion)
	}
	assertReceiptMigrationIndexes(t, pool, false, true)
	assertMigrationRecorded(t, pool, "000021", true)
	assertMigrationRecorded(t, pool, "000022", true)
	if got := migrationAppliedAt(t, pool, "000021"); !got.Equal(before021AppliedAt) {
		t.Fatalf("migration runner rewrote 000021 applied_at=%s want=%s", got, before021AppliedAt)
	}
	var migrationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM switchback_schema_migrations`).Scan(
		&migrationCount,
	); err != nil {
		t.Fatal(err)
	}
	if migrationCount != expectedMigrationCount {
		t.Fatalf("production migration ledger rows=%d want=%d", migrationCount, expectedMigrationCount)
	}
	for _, eventID := range historicalIDs {
		afterRow, afterDigest := migrationEventEvidence(t, pool, eventID)
		if afterRow != drainedRows[eventID] || afterDigest != drainedDigests[eventID] {
			t.Fatalf(
				"successful migration changed drained event %s\nrow %s -> %s\ndigest %s -> %s",
				eventID, drainedRows[eventID], afterRow, drainedDigests[eventID], afterDigest,
			)
		}
		if got := sourceReceiptDigest(t, pool, eventID); got != drainedSourceDigests[eventID] {
			t.Fatalf("successful migration changed %s source receipt digest=%q want=%q",
				eventID, got, drainedSourceDigests[eventID])
		}
	}
	var validated bool
	if err := pool.QueryRow(ctx, `
SELECT constraint_record.convalidated
FROM pg_constraint constraint_record
JOIN pg_class relation ON relation.oid=constraint_record.conrelid
JOIN pg_namespace namespace_record ON namespace_record.oid=relation.relnamespace
WHERE namespace_record.nspname=current_schema()
  AND relation.relname='outbox_events'
  AND constraint_record.conname='outbox_events_client_command_schema_check'`,
	).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if validated {
		t.Fatal("grandfather compatibility constraint unexpectedly validated historical rows")
	}

	insertOwnerStream(t, pool, "event-v2", "task/event-v2")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v2", "task/event-v2", "event.v2", "client-v2",
	); err != nil {
		t.Fatalf("insert event.v2 with explicit client identity: %v", err)
	}
	insertOwnerStream(t, pool, "event-v1-client", "task/event-v1-client")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v1-client", "task/event-v1-client", "event.v1", "client-v1-forbidden",
	); err == nil {
		t.Fatal("migration 000022 accepted event.v1 with client_command_id")
	}
	insertOwnerStream(t, pool, "event-v1-update", "task/event-v1-update")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v1-update", "task/event-v1-update", "event.v1", nil,
	); err != nil {
		t.Fatalf("insert event.v1 without client identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE outbox_events SET client_command_id='client-v1-update-forbidden' WHERE event_id=$1`,
		"event-v1-update"); err == nil {
		t.Fatal("migration 000022 accepted an update that gives event.v1 a client_command_id")
	}

	beforeNoopSchema := fullMigrationSchemaFingerprint(t, pool)
	beforeNoopLedger := migrationLedgerEvidence(t, pool)
	output, err = runProductionMigrationRunner(t, pool)
	if err != nil {
		t.Fatalf("repeat production migration runner: %v\n%s", err, output)
	}
	if got := fullMigrationSchemaFingerprint(t, pool); !reflect.DeepEqual(got, beforeNoopSchema) {
		t.Fatalf("repeat migration runner changed schema\nbefore=%v\nafter=%v",
			beforeNoopSchema, got)
	}
	if got := migrationLedgerEvidence(t, pool); got != beforeNoopLedger {
		t.Fatalf("repeat migration runner changed ledger\nbefore=%s\nafter=%s",
			beforeNoopLedger, got)
	}
	assertNoUnpublishedGrandfather(t, pool)
}

func TestIntegrationEventV2MigrationRunnerWaitsForConcurrentLegacyWriter(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	recordAppliedMigrationsThrough(t, pool, 21)
	insertOwnerStream(t, pool, "event-v1-concurrent-before-up", "task/event-v1-concurrent-before-up")
	beforeSchema := fullMigrationSchemaFingerprint(t, pool)
	beforeLedger := migrationLedgerEvidence(t, pool)

	writerTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerTransaction.Rollback(ctx) //nolint:errcheck
	if _, err := insertMigrationEventWithExecutor(
		ctx, writerTransaction, "event-v1-concurrent-before-up",
		"task/event-v1-concurrent-before-up", "event.v1", "client-concurrent-before-up",
	); err != nil {
		t.Fatal(err)
	}
	beforeRow, beforeDigest := migrationEventEvidenceFrom(
		t, writerTransaction, "event-v1-concurrent-before-up",
	)

	runnerDone := startProductionMigrationRunner(t, pool)
	if err := waitForMigrationTableLock(t, pool, "ShareRowExclusiveLock", false); err != nil {
		writerTransaction.Rollback(ctx) //nolint:errcheck
		result := awaitMigrationRunner(t, runnerDone)
		t.Fatalf("production runner did not wait at its first lock: %v; runner=%v\n%s",
			err, result.err, result.output)
	}
	select {
	case result := <-runnerDone:
		t.Fatalf("production runner completed before legacy writer commit: %v\n%s",
			result.err, result.output)
	default:
	}
	if err := writerTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := awaitMigrationRunner(t, runnerDone)
	if result.err == nil ||
		!strings.Contains(result.output, "drain with v1-compatible binary before upgrade") {
		t.Fatalf("production runner concurrent writer error=%v output=%s",
			result.err, result.output)
	}

	if got := fullMigrationSchemaFingerprint(t, pool); !reflect.DeepEqual(got, beforeSchema) {
		t.Fatalf("concurrent rejected migration changed schema\nbefore=%v\nafter=%v",
			beforeSchema, got)
	}
	if got := migrationLedgerEvidence(t, pool); got != beforeLedger {
		t.Fatalf("concurrent rejected migration changed ledger\nbefore=%s\nafter=%s",
			beforeLedger, got)
	}
	afterRow, afterDigest := migrationEventEvidence(t, pool, "event-v1-concurrent-before-up")
	if afterRow != beforeRow || afterDigest != beforeDigest {
		t.Fatalf("concurrent rejected migration changed legacy event\nrow %s -> %s\ndigest %s -> %s",
			beforeRow, afterRow, beforeDigest, afterDigest)
	}
	assertSourceReceiptRecorded(t, pool, "event-v1-concurrent-before-up", false)
	assertMigrationRecorded(t, pool, "000022", false)
}

func TestIntegrationEventV2MigrationLockMakesWaitingLegacyInsertUseNewConstraint(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	insertOwnerStream(t, pool, "event-v1-concurrent-after-lock", "task/event-v1-concurrent-after-lock")
	up, _ := migration22SQL(t)
	lockStatement, afterLock := migrationSQLAfterFirstLock(t, up)

	migrationTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationTransaction.Rollback(ctx) //nolint:errcheck
	if _, err := migrationTransaction.Exec(ctx, lockStatement); err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan error, 1)
	go func() {
		_, err := insertMigrationEvent(
			ctx, pool, "event-v1-concurrent-after-lock", "task/event-v1-concurrent-after-lock",
			"event.v1", "client-concurrent-after-lock",
		)
		writerDone <- err
	}()
	if err := waitForMigrationTableLock(t, pool, "RowExclusiveLock", false); err != nil {
		migrationTransaction.Rollback(ctx) //nolint:errcheck
		writerErr := awaitConcurrentWriter(t, writerDone)
		t.Fatalf("legacy writer did not wait behind migration lock: %v; writer=%v", err, writerErr)
	}
	select {
	case writerErr := <-writerDone:
		t.Fatalf("legacy writer completed while migration lock was held: %v", writerErr)
	default:
	}
	if _, err := migrationTransaction.Exec(ctx, afterLock); err != nil {
		t.Fatal(err)
	}
	if err := migrationTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	writerErr := awaitConcurrentWriter(t, writerDone)
	if writerErr == nil || !strings.Contains(writerErr.Error(), "outbox_events_client_command_schema_check") {
		t.Fatalf("waiting legacy writer error=%v", writerErr)
	}
	var illegalRows int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM outbox_events WHERE event_id='event-v1-concurrent-after-lock'`,
	).Scan(&illegalRows); err != nil {
		t.Fatal(err)
	}
	if illegalRows != 0 {
		t.Fatalf("waiting legacy writer persisted %d illegal rows", illegalRows)
	}
	assertReceiptMigrationIndexes(t, pool, false, true)
	assertNoUnpublishedGrandfather(t, pool)
}

func TestIntegrationEventV2MigrationDownWaitsForConcurrentV2Writer(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	up, down := migration22SQL(t)
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	insertOwnerStream(t, pool, "event-v2-concurrent-before-down", "task/event-v2-concurrent-before-down")
	beforeSchema := receiptMigrationSchemaFingerprint(t, pool)

	writerTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerTransaction.Rollback(ctx) //nolint:errcheck
	if _, err := insertMigrationEventWithExecutor(
		ctx, writerTransaction, "event-v2-concurrent-before-down",
		"task/event-v2-concurrent-before-down", "event.v2", "client-concurrent-before-down",
	); err != nil {
		t.Fatal(err)
	}
	downDone := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, down)
		downDone <- err
	}()
	if err := waitForMigrationTableLock(t, pool, "ShareRowExclusiveLock", false); err != nil {
		writerTransaction.Rollback(ctx) //nolint:errcheck
		downErr := awaitConcurrentWriter(t, downDone)
		t.Fatalf("down migration did not wait at its first lock: %v; down=%v", err, downErr)
	}
	select {
	case downErr := <-downDone:
		t.Fatalf("down migration completed before v2 writer commit: %v", downErr)
	default:
	}
	if err := writerTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	downErr := awaitConcurrentWriter(t, downDone)
	if downErr == nil ||
		!strings.Contains(downErr.Error(), "cannot downgrade IntegrationEvent v2 migration") {
		t.Fatalf("concurrent down error=%v", downErr)
	}
	if got := receiptMigrationSchemaFingerprint(t, pool); !reflect.DeepEqual(got, beforeSchema) {
		t.Fatalf("concurrent rejected down changed schema\nbefore=%v\nafter=%v",
			beforeSchema, got)
	}
	var v2Rows int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM outbox_events WHERE event_id='event-v2-concurrent-before-down'`,
	).Scan(&v2Rows); err != nil {
		t.Fatal(err)
	}
	if v2Rows != 1 {
		t.Fatalf("concurrent v2 writer rows=%d want=1", v2Rows)
	}
}

func TestProductionMigrationRunnerRejectsEmptyOverrideDirectory(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	recordAppliedMigrationsThrough(t, pool, 21)
	beforeSchema := fullMigrationSchemaFingerprint(t, pool)
	beforeLedger := migrationLedgerEvidence(t, pool)

	output, err := runProductionMigrationRunnerWithDirectory(t, pool, t.TempDir())
	if err == nil || !strings.Contains(output, "no PostgreSQL migrations found") {
		t.Fatalf("empty migration directory error=%v output=%s", err, output)
	}
	if got := fullMigrationSchemaFingerprint(t, pool); !reflect.DeepEqual(got, beforeSchema) {
		t.Fatalf("empty migration directory changed schema\nbefore=%v\nafter=%v", beforeSchema, got)
	}
	if got := migrationLedgerEvidence(t, pool); got != beforeLedger {
		t.Fatalf("empty migration directory changed ledger\nbefore=%s\nafter=%s",
			beforeLedger, got)
	}
}

func TestIntegrationEventV2MigrationDownFailsBeforeAnyChangeWhenV2RowsExist(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	up, down := migration22SQL(t)
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	insertOwnerStream(t, pool, "event-v2-down-block", "task/event-v2-down-block")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v2-down-block", "task/event-v2-down-block", "event.v2", "client-v2-down-block",
	); err != nil {
		t.Fatal(err)
	}
	before := receiptMigrationSchemaFingerprint(t, pool)

	if _, err := pool.Exec(ctx, down); err == nil ||
		!strings.Contains(err.Error(), "cannot downgrade IntegrationEvent v2 migration") {
		t.Fatalf("unsafe downgrade error=%v", err)
	}
	after := receiptMigrationSchemaFingerprint(t, pool)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed downgrade changed schema\nbefore=%v\nafter=%v", before, after)
	}
	assertReceiptMigrationIndexes(t, pool, false, true)
}

func TestIntegrationEventV2MigrationDownRestoresExact021SchemaWithoutV2Rows(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 21)
	ctx := context.Background()
	recordAppliedMigrationsThrough(t, pool, 21)
	baseline := receiptMigrationSchemaFingerprint(t, pool)
	insertOwnerStream(t, pool, "event-v1-grandfather-down", "task/event-v1-grandfather-down")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v1-grandfather-down", "task/event-v1-grandfather-down",
		"event.v1", "client-v1-grandfather-down",
	); err != nil {
		t.Fatal(err)
	}
	beforeRow, beforeDigest := migrationEventEvidence(t, pool, "event-v1-grandfather-down")
	markMigrationEventDrained(t, pool, "event-v1-grandfather-down", beforeDigest)
	beforeRow, beforeDigest = migrationEventEvidence(t, pool, "event-v1-grandfather-down")
	up, down := migration22SQL(t)
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatalf("downgrade migration 000022 without event.v2 rows: %v", err)
	}

	restored := receiptMigrationSchemaFingerprint(t, pool)
	if !reflect.DeepEqual(restored, baseline) {
		t.Fatalf("000022 down did not restore exact 000021 schema\nwant=%v\ngot=%v", baseline, restored)
	}
	afterRow, afterDigest := migrationEventEvidence(t, pool, "event-v1-grandfather-down")
	if afterRow != beforeRow || afterDigest != beforeDigest {
		t.Fatalf("grandfather event changed across up/down\nrow %s -> %s\ndigest %s -> %s",
			beforeRow, afterRow, beforeDigest, afterDigest)
	}
	assertReceiptMigrationIndexes(t, pool, true, false)
	insertOwnerStream(t, pool, "event-v2-after-down", "task/event-v2-after-down")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v2-after-down", "task/event-v2-after-down", "event.v2", "client-v2-after-down",
	); err == nil {
		t.Fatal("restored 000021 schema accepted event.v2")
	}
	insertOwnerStream(t, pool, "event-v1-client-after-down", "task/event-v1-client-after-down")
	if _, err := insertMigrationEvent(
		ctx, pool, "event-v1-client-after-down", "task/event-v1-client-after-down",
		"event.v1", "client-v1-allowed-by-021",
	); err != nil {
		t.Fatalf("restored 000021 schema rejected its historical v1 client shape: %v", err)
	}
}

func migration22SQL(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "migrations",
		"000022_integration_event_v2_and_receipt_retention.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("migration 000022 lacks down marker")
	}
	return up, down
}

func recordAppliedMigrationsThrough(t *testing.T, pool *pgxpool.Pool, lastSequence int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
CREATE TABLE switchback_schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
INSERT INTO switchback_schema_migrations(version,applied_at)
SELECT lpad(sequence::text,6,'0'), $2::timestamptz + sequence * interval '1 second'
FROM generate_series(1,$1) AS sequence`,
		lastSequence, time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func productionMigrationInventory(t *testing.T) (string, int) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(
		"..", "..", "..", "migrations", "[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql",
	))
	if err != nil || len(files) == 0 {
		t.Fatalf("list production migrations: files=%d error=%v", len(files), err)
	}
	latestVersion, _, _ := strings.Cut(filepath.Base(files[len(files)-1]), "_")
	return latestVersion, len(files)
}

func runProductionMigrationRunner(t *testing.T, pool *pgxpool.Pool) (string, error) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return runProductionMigrationRunnerWithDirectory(t, pool, filepath.Join(root, "migrations"))
}

type migrationRunnerResult struct {
	output string
	err    error
}

func startProductionMigrationRunner(
	t *testing.T,
	pool *pgxpool.Pool,
) <-chan migrationRunnerResult {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := productionMigrationRunnerCommand(t, pool, filepath.Join(root, "migrations"))
	done := make(chan migrationRunnerResult, 1)
	go func() {
		output, err := command.CombinedOutput()
		done <- migrationRunnerResult{output: string(output), err: err}
	}()
	return done
}

func runProductionMigrationRunnerWithDirectory(
	t *testing.T,
	pool *pgxpool.Pool,
	migrationsDirectory string,
) (string, error) {
	t.Helper()
	command := productionMigrationRunnerCommand(t, pool, migrationsDirectory)
	output, err := command.CombinedOutput()
	return string(output), err
}

func productionMigrationRunnerCommand(
	t *testing.T,
	pool *pgxpool.Pool,
	migrationsDirectory string,
) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql is required to exercise deploy/local/migrate-postgres.sh")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := pool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	connection := pool.Config().ConnConfig
	overrides := map[string]string{
		"PGDATABASE":                connection.Database,
		"PGHOST":                    connection.Host,
		"PGOPTIONS":                 "-csearch_path=" + schema,
		"PGPASSWORD":                connection.Password,
		"PGPORT":                    strconv.Itoa(int(connection.Port)),
		"PGSSLMODE":                 "disable",
		"PGUSER":                    connection.User,
		"SWITCHBACK_MIGRATIONS_DIR": migrationsDirectory,
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	command := exec.Command("sh", filepath.Join(root, "deploy", "local", "migrate-postgres.sh"))
	command.Dir = root
	command.Env = environment
	return command
}

func awaitMigrationRunner(
	t *testing.T,
	done <-chan migrationRunnerResult,
) migrationRunnerResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for production migration runner")
		return migrationRunnerResult{}
	}
}

func awaitConcurrentWriter(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for concurrent migration participant")
		return nil
	}
}

func waitForMigrationTableLock(
	t *testing.T,
	pool *pgxpool.Pool,
	mode string,
	granted bool,
) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var found bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_locks
    WHERE locktype='relation'
      AND relation=to_regclass(current_schema()||'.outbox_events')
      AND mode=$1
      AND granted=$2
)`, mode, granted).Scan(&found); err != nil {
			return err
		}
		if found {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s granted=%t", mode, granted)
		case <-ticker.C:
		}
	}
}

func migrationSQLAfterFirstLock(t *testing.T, migrationSQL string) (string, string) {
	t.Helper()
	body := strings.TrimSpace(strings.TrimPrefix(migrationSQL, "-- migrate:up"))
	const lock = "LOCK TABLE outbox_events IN SHARE ROW EXCLUSIVE MODE;"
	if !strings.HasPrefix(body, lock) {
		t.Fatal("migration SQL does not start with the required Outbox lock")
	}
	return lock, strings.TrimSpace(strings.TrimPrefix(body, lock))
}

func migrationLedgerEvidence(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var evidence string
	if err := pool.QueryRow(context.Background(), `
SELECT COALESCE(
    string_agg(
        version || ':' || to_char(applied_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        ',' ORDER BY version
    ),
    ''
)
FROM switchback_schema_migrations`,
	).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func migrationAppliedAt(t *testing.T, pool *pgxpool.Pool, version string) time.Time {
	t.Helper()
	var appliedAt time.Time
	if err := pool.QueryRow(context.Background(), `
SELECT applied_at FROM switchback_schema_migrations WHERE version=$1`,
		version).Scan(&appliedAt); err != nil {
		t.Fatal(err)
	}
	return appliedAt.UTC()
}

func assertMigrationRecorded(
	t *testing.T,
	pool *pgxpool.Pool,
	version string,
	want bool,
) {
	t.Helper()
	var recorded bool
	if err := pool.QueryRow(context.Background(), `
SELECT EXISTS (SELECT 1 FROM switchback_schema_migrations WHERE version=$1)`,
		version).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != want {
		t.Fatalf("migration %s recorded=%t want=%t", version, recorded, want)
	}
}

func markMigrationEventDrained(
	t *testing.T,
	pool *pgxpool.Pool,
	eventID, sourceDigest string,
) {
	t.Helper()
	ctx := context.Background()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(ctx) //nolint:errcheck
	if _, err := transaction.Exec(ctx, `
INSERT INTO projection_event_receipts
  (source_event_id,source_stream_id,source_sequence,event_digest,source_event_digest,
   projection_result,applied_at)
SELECT event_id,stream_id,sequence,$2,$2,'[]',$3
FROM outbox_events
WHERE event_id=$1`,
		eventID, sourceDigest, time.Date(2026, 7, 30, 8, 2, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	commandTag, err := transaction.Exec(ctx, `
UPDATE outbox_events SET published_at=$2 WHERE event_id=$1 AND published_at IS NULL`,
		eventID, time.Date(2026, 7, 30, 8, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if commandTag.RowsAffected() != 1 {
		t.Fatalf("drain migration event %s affected %d rows", eventID, commandTag.RowsAffected())
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func sourceReceiptDigest(t *testing.T, pool *pgxpool.Pool, eventID string) string {
	t.Helper()
	var digest string
	if err := pool.QueryRow(context.Background(), `
SELECT source_event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, eventID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertSourceReceiptRecorded(
	t *testing.T,
	pool *pgxpool.Pool,
	eventID string,
	want bool,
) {
	t.Helper()
	var recorded bool
	if err := pool.QueryRow(context.Background(), `
SELECT EXISTS (SELECT 1 FROM projection_event_receipts WHERE source_event_id=$1)`,
		eventID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != want {
		t.Fatalf("source receipt %s recorded=%t want=%t", eventID, recorded, want)
	}
}

func assertNoUnpublishedGrandfather(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*)
FROM outbox_events
WHERE schema_version='event.v1'
  AND client_command_id IS NOT NULL
  AND published_at IS NULL`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("current dispatcher has %d unpublished grandfather event.v1 heads", count)
	}
}

func insertOwnerStream(t *testing.T, pool *pgxpool.Pool, aggregateID, streamID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
INSERT INTO owner_stream_positions
  (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ($1,'migration-test','Task',$2,2,1,$3)`,
		streamID, aggregateID, time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func insertMigrationEvent(
	ctx context.Context,
	pool *pgxpool.Pool,
	eventID, streamID, schemaVersion string,
	clientCommandID any,
) (int64, error) {
	return insertMigrationEventWithExecutor(
		ctx, pool, eventID, streamID, schemaVersion, clientCommandID,
	)
}

type migrationEventExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertMigrationEventWithExecutor(
	ctx context.Context,
	executor migrationEventExecutor,
	eventID, streamID, schemaVersion string,
	clientCommandID any,
) (int64, error) {
	commandTag, err := executor.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,
   aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
   client_command_id,visibility,payload,occurred_at)
VALUES ($1,$2,'MigrationEvent.v1','migration-test',$3,1,'tenant-migration',
        'Task',$1,'trace-migration',$1,$1,$1,$4,'internal','{}',$5)`,
		eventID, schemaVersion, streamID, clientCommandID,
		time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC))
	return commandTag.RowsAffected(), err
}

func migrationEventEvidence(
	t *testing.T,
	pool *pgxpool.Pool,
	eventID string,
) (string, string) {
	t.Helper()
	return migrationEventEvidenceFrom(t, pool, eventID)
}

type migrationEventQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func migrationEventEvidenceFrom(
	t *testing.T,
	querier migrationEventQuerier,
	eventID string,
) (string, string) {
	t.Helper()
	ctx := context.Background()
	var row string
	if err := querier.QueryRow(ctx, `
SELECT row_to_json(event_record)::text
FROM outbox_events event_record
WHERE event_id=$1`, eventID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	var event contractsv1.IntegrationEvent
	var clientCommandID sql.NullString
	if err := querier.QueryRow(ctx, `
SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,
       aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
       client_command_id,visibility,occurred_at,payload
FROM outbox_events WHERE event_id=$1`, eventID).Scan(
		&event.SchemaVersion, &event.EventID, &event.EventType, &event.ProducerContext,
		&event.StreamID, &event.Sequence, &event.TenantID, &event.AggregateType,
		&event.AggregateID, &event.TraceID, &event.CorrelationID, &event.CausationID,
		&event.IdempotencyKey, &clientCommandID, &event.Visibility, &event.OccurredAt,
		&event.Payload,
	); err != nil {
		t.Fatal(err)
	}
	event.ClientCommandID = clientCommandID.String
	event.OccurredAt = event.OccurredAt.UTC()
	digest, err := platformpostgres.IntegrationEventDigest(event)
	if err != nil {
		t.Fatal(err)
	}
	return row, digest
}

func assertReceiptMigrationIndexes(
	t *testing.T,
	pool *pgxpool.Pool,
	wantSnapshot, wantCleanup bool,
) {
	t.Helper()
	var snapshot, cleanup bool
	if err := pool.QueryRow(context.Background(), `
SELECT
  to_regclass(current_schema()||'.projection_command_receipts_snapshot_lookup') IS NOT NULL,
  to_regclass(current_schema()||'.projection_command_receipts_cleanup') IS NOT NULL`,
	).Scan(&snapshot, &cleanup); err != nil {
		t.Fatal(err)
	}
	if snapshot != wantSnapshot || cleanup != wantCleanup {
		t.Fatalf("receipt indexes snapshot=%t cleanup=%t want=%t/%t",
			snapshot, cleanup, wantSnapshot, wantCleanup)
	}
}

func fullMigrationSchemaFingerprint(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ctx := context.Background()
	result := []string{}
	queries := []string{
		`SELECT format('column:%s:%s:%s:%s:%s',table_name,column_name,data_type,is_nullable,
		        COALESCE(column_default,''))
		   FROM information_schema.columns
		  WHERE table_schema=current_schema()`,
		`SELECT format('constraint:%s:%s:%s',relation.relname,constraint_record.conname,
		        pg_get_constraintdef(constraint_record.oid))
		   FROM pg_constraint constraint_record
		   JOIN pg_class relation ON relation.oid=constraint_record.conrelid
		   JOIN pg_namespace namespace_record ON namespace_record.oid=relation.relnamespace
		  WHERE namespace_record.nspname=current_schema()`,
		`SELECT format('index:%s:%s:%s',tablename,indexname,indexdef)
		   FROM pg_indexes
		  WHERE schemaname=current_schema()`,
	}
	for _, query := range queries {
		rows, err := pool.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result = append(result, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	sort.Strings(result)
	if len(result) == 0 {
		t.Fatal("empty full migration schema fingerprint")
	}
	return result
}

func receiptMigrationSchemaFingerprint(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ctx := context.Background()
	result := []string{}
	queries := []string{
		`SELECT format('column:%s:%s:%s:%s:%s',table_name,column_name,data_type,is_nullable,
		        COALESCE(column_default,''))
		   FROM information_schema.columns
		  WHERE table_schema=current_schema()
		    AND table_name IN ('outbox_events','projection_command_receipts')`,
		`SELECT format('constraint:%s:%s:%s',relation.relname,constraint_record.conname,
		        pg_get_constraintdef(constraint_record.oid))
		   FROM pg_constraint constraint_record
		   JOIN pg_class relation ON relation.oid=constraint_record.conrelid
		   JOIN pg_namespace namespace_record ON namespace_record.oid=relation.relnamespace
		  WHERE namespace_record.nspname=current_schema()
		    AND relation.relname IN ('outbox_events','projection_command_receipts')`,
		`SELECT format('index:%s:%s:%s',tablename,indexname,indexdef)
		   FROM pg_indexes
		  WHERE schemaname=current_schema()
		    AND tablename IN ('outbox_events','projection_command_receipts')`,
	}
	for _, query := range queries {
		rows, err := pool.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result = append(result, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	sort.Strings(result)
	if len(result) == 0 {
		t.Fatal("empty migration schema fingerprint")
	}
	return result
}
