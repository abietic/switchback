package architecture_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

func TestObservationFragmentsMigration024IsAdditiveAndRetainsEvidence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000024_observation_fragments.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("missing down marker")
	}
	for _, table := range []string{"observation_candidates", "observation_publications", "observation_fragment_jobs", "observation_fragment_activations", "observation_fragment_run_exclusions", "observation_fragment_requests", "observation_fragment_conflicts"} {
		if !strings.Contains(up, "CREATE TABLE "+table+" (") {
			t.Errorf("missing table %s", table)
		}
	}
	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "UPDATE observability_projection", "UPDATE trace_manifests", "UPDATE worker_turn_settlements"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("non-additive migration: %s", forbidden)
		}
	}
	if !strings.Contains(down, "RAISE EXCEPTION 'observation fragment evidence is retained; application rollback must disable projection'") || strings.Contains(down, "DROP ") {
		t.Fatal("destructive downgrade must be refused")
	}
}

func TestProjectionCommandReceiptMigration021RemainsImmutable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "migrations", "000021_projection_command_receipts.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	const want = "5a152e096295c0e60ad7b069e8a52e71aec487b2aa65818d322f2c5aed05fefc"
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
		t.Fatalf("historical migration 000021 digest=%s want=%s", got, want)
	}
}

func TestIntegrationEventV2Migration022LocksThenPreflightsBeforeDDL(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "migrations", "000022_integration_event_v2_and_receipt_retention.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("migration 000022 lacks down marker")
	}
	up = strings.TrimSpace(strings.TrimPrefix(up, "-- migrate:up"))
	const lock = "LOCK TABLE outbox_events IN SHARE ROW EXCLUSIVE MODE;"
	if !strings.HasPrefix(up, lock) {
		t.Fatal("migration 000022 up does not lock Outbox before its preflight")
	}
	upAfterLock := strings.TrimSpace(strings.TrimPrefix(up, lock))
	if !strings.HasPrefix(upAfterLock, "DO $$") {
		t.Fatal("migration 000022 up does not preflight immediately after its lock")
	}
	for _, required := range []string{
		"schema_version = 'event.v1'",
		"client_command_id IS NOT NULL",
		"published_at IS NULL",
		"drain with v1-compatible binary before upgrade",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("migration 000022 up preflight lacks %q", required)
		}
	}
	down = strings.TrimSpace(down)
	if !strings.HasPrefix(down, lock) {
		t.Fatal("migration 000022 down does not lock Outbox before its preflight")
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(down, lock)), "DO $$") {
		t.Fatal("migration 000022 down does not preflight immediately after its lock")
	}
}

func TestInitialPersistenceMigrationOwnsRequiredTables(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "000001_task_session_persistence.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read initial persistence migration: %v", err)
	}
	sql := string(data)
	tables := []string{
		"task_readiness_assessments",
		"task_creation_intents",
		"worker_session_target_intents",
		"tasks",
		"task_transitions",
		"worker_session_continuity_quotas",
		"session_allocation_queue_positions",
		"session_allocation_requests",
		"worker_sessions",
		"session_task_queue_positions",
		"session_task_queue_entries",
		"session_dispatch_permits",
		"worker_runs",
		"agent_turns",
		"runtime_instances",
		"scheduled_commands",
		"idempotency_results",
		"owner_stream_positions",
		"outbox_events",
		"consumer_receipts",
	}
	for _, table := range tables {
		if !strings.Contains(sql, "CREATE TABLE "+table+" ") {
			t.Errorf("migration does not create %s", table)
		}
		if !strings.Contains(sql, "DROP TABLE "+table+";") {
			t.Errorf("migration does not drop %s", table)
		}
	}
}

func TestInitialPersistenceMigrationEnforcesCoreCardinality(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000001_task_session_persistence.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	required := []string{
		"CHECK (status IN ('queued', 'running', 'waiting_user', 'delivered', 'completed', 'failed', 'cancelled'))",
		"UNIQUE (task_id, result_revision)",
		"UNIQUE (tenant_id, user_id, session_allocation_sequence)",
		"UNIQUE (worker_session_id, session_queue_sequence)",
		"worker_runs_one_active_per_session",
		"runtime_instances_one_active_per_session",
		"UNIQUE (stream_id, sequence)",
		"CHECK (reserved_count >= 0 AND reserved_count <= quota_limit)",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration lacks invariant %q", fragment)
		}
	}
	if strings.Contains(strings.ToLower(sql), "timestamp without time zone") || strings.Contains(strings.ToLower(sql), " serial") {
		t.Fatal("migration uses ambiguous time or implicit sequence types")
	}
}

func TestRuntimeControlResumeMigrationOwnsDurableAuthorityAndJournal(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000014_runtime_control_resume.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, table := range []string{"runtime_control_channels", "runtime_control_messages"} {
		if !strings.Contains(sql, "CREATE TABLE "+table+" ") {
			t.Errorf("migration does not create %s", table)
		}
		if !strings.Contains(sql, "DROP TABLE "+table+";") {
			t.Errorf("migration does not drop %s", table)
		}
	}
	for _, fragment := range []string{
		"credential_digest ~ '^sha256:[0-9a-f]{64}$'",
		"UNIQUE (worker_session_id, runtime_generation, channel_epoch)",
		"PRIMARY KEY (runtime_instance_id, direction, sequence)",
		"UNIQUE (runtime_instance_id, direction, message_id)",
		"UNIQUE (runtime_instance_id, direction, idempotency_key)",
		"CHECK (command_acknowledged_sequence <= command_committed_sequence)",
		"CHECK (command_applied_sequence <= command_acknowledged_sequence",
		"resume_claim_revision",
		"resume_completed_state",
		"CHECK (event_applied_sequence <= event_acknowledged_sequence",
		"event_replay_checkpoint_sequence",
		"event_replay_checkpoint_digest",
		"event_replay_target_sequence",
		"event_replay_target_digest",
		"DEFAULT '" + contractsv1.RuntimeControlCursorGenesisDigest + "'",
		"runtime_control_messages_pending_events",
		"direction = 'command'",
		"direction = 'event'",
		"effect_status = 'received'",
		"effect_status = 'applied'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("Runtime control migration lacks invariant %q", fragment)
		}
	}
}

func TestRuntimeControlFenceSnapshotMigrationOwnsImmutableFenceProof(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000017_runtime_control_fence_snapshot.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, fragment := range []string{
		"ADD COLUMN fenced_at timestamptz",
		"ADD COLUMN fence_revision bigint",
		"ADD COLUMN fence_owner_fence bigint",
		"SET fenced_at = updated_at",
		"fence_revision = revision",
		"fence_owner_fence = owner_fence",
		"WHERE status = 'fenced'",
		"runtime_control_channels_fence_snapshot_check",
		"fenced_at <= updated_at",
		"fence_revision <= revision",
		"fence_owner_fence = owner_fence",
		"fence_revision IS DISTINCT FROM revision",
		"fenced_at IS DISTINCT FROM updated_at",
		"fence_owner_fence IS DISTINCT FROM owner_fence",
		"cannot downgrade Runtime control fence snapshot after post-fence journal progress",
		"DROP CONSTRAINT runtime_control_channels_fence_snapshot_check",
		"DROP COLUMN fence_owner_fence",
		"DROP COLUMN fence_revision",
		"DROP COLUMN fenced_at",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf(
				"Runtime control fence-snapshot migration lacks invariant %q",
				fragment,
			)
		}
	}
}

func TestRuntimeFirstHeartbeatWatchdogMigrationOwnsImmutableAnchorAndCrashProof(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000018_runtime_first_heartbeat_watchdog.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	upSQL := strings.SplitN(sql, "\n-- migrate:down", 2)[0]
	downSQL := strings.SplitN(sql, "\n-- migrate:down", 2)
	if len(downSQL) != 2 {
		t.Fatal("First-heartbeat watchdog migration lacks migrate:down section")
	}
	for _, fragment := range []string{
		"CREATE TABLE runtime_first_heartbeat_watchdogs ",
		"runtime_instance_id text PRIMARY KEY REFERENCES runtime_control_channels (runtime_instance_id)",
		"UNIQUE (worker_session_id, runtime_generation, channel_epoch)",
		"anchor_kind text NOT NULL",
		"anchor_revision bigint NOT NULL",
		"anchor_kind IN ('resume_complete_v1', 'legacy_runtime_started_v1')",
		"first_authenticated_resume_at timestamptz NOT NULL",
		"suspect_due_at timestamptz NOT NULL",
		"unhealthy_due_at timestamptz NOT NULL",
		"status IN ('awaiting', 'suspect', 'satisfied', 'unhealthy', 'cancelled')",
		"first_heartbeat_sequence bigint",
		"first_heartbeat_sequence > 0",
		"first_heartbeat_control_sequence bigint",
		"first_heartbeat_control_sequence > 0",
		"first_heartbeat_received_at timestamptz",
		"recovery_intent jsonb",
		"unhealthy_runtime_revision bigint",
		"terminal_reason text",
		"first_heartbeat_received_at IS NOT NULL",
		"unhealthy_at IS NOT NULL",
		"unhealthy_runtime_revision IS NOT NULL",
		"recovery_intent IS NOT NULL",
		"jsonb_typeof(recovery_intent) = 'object'",
		"terminal_reason = 'replacement_first_heartbeat_timeout'",
		"'turn_outcome_owned'",
		"suspect_due_at = first_authenticated_resume_at + INTERVAL '45 seconds'",
		"unhealthy_due_at = first_authenticated_resume_at + INTERVAL '90 seconds'",
		"INSERT INTO runtime_first_heartbeat_watchdogs",
		"'legacy_runtime_started_v1'",
		"LEFT JOIN runtime_heartbeat_observations heartbeat",
		"INSERT INTO scheduled_commands",
		"'runtime_first_heartbeat_evaluate'",
		"CREATE TRIGGER runtime_first_heartbeat_watchdogs_immutable_proof",
		"runtime_first_heartbeat_watchdogs_guard_immutable_proof",
		"First-heartbeat watchdog identity and deadline anchor are immutable",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("First-heartbeat watchdog migration lacks invariant %q", fragment)
		}
	}
	for _, fragment := range []string{
		"DELETE FROM scheduled_commands",
		"command_type = 'runtime_first_heartbeat_evaluate'",
		"DROP TABLE runtime_first_heartbeat_watchdogs;",
	} {
		if !strings.Contains(downSQL[1], fragment) {
			t.Errorf("First-heartbeat watchdog down migration lacks cleanup %q", fragment)
		}
	}
	if strings.Index(downSQL[1], "DELETE FROM scheduled_commands") >
		strings.Index(downSQL[1], "DROP TABLE runtime_first_heartbeat_watchdogs;") {
		t.Fatal("First-heartbeat ScheduledCommands must be deleted before dropping the watchdog table")
	}
	if strings.Contains(upSQL, "ALTER TABLE runtime_recovery_incidents") {
		t.Fatal("First-heartbeat watchdog migration must preserve historical recovery trigger compatibility")
	}
	recoveryData, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000013_single_runtime_recovery.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recoveryData), "'worker_control_closed'") {
		t.Fatal("historical worker_control_closed recovery trigger is no longer readable")
	}
}

func TestWorkerTurnSettlementMigrationOwnsDurablePhaseLeaseAndUpgradeBackfill(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000015_worker_turn_settlement.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, fragment := range []string{
		"CREATE TABLE worker_turn_settlements ",
		"DROP TABLE worker_turn_settlements;",
		"UNIQUE (runtime_instance_id, terminal_event_sequence)",
		"UNIQUE (runtime_instance_id, terminal_message_id)",
		"REFERENCES runtime_control_messages (runtime_instance_id, direction, sequence)",
		"'accepted'",
		"'task_settled'",
		"'terminal_event_applied'",
		"'continuation_settled'",
		"lease_fence bigint NOT NULL DEFAULT 0",
		"worker_turn_settlements_expired_leases",
		"worker_turn_settlements_session_pending",
		"ON worker_turn_settlements (accepted_at, settlement_id)",
		"INSERT INTO worker_turn_settlements",
		"message.effect_status = 'received'",
		`'{"legacy_backfill":true,"schema_version":1}'::jsonb`,
		"sha256:cf47ef167e70344cd3e6e48d4426c1e34055aa32da428e623c15b9ff82fa8079",
		"multiple terminal events exist for one AgentTurn",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("Turn settlement migration lacks invariant %q", fragment)
		}
	}
}

func TestWorkerTurnSettlementSourceUnionMigrationOwnsRuntimeAndDeadlineSources(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000016_turn_settlement_source_union.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	upSQL := strings.SplitN(sql, "\n-- migrate:down", 2)[0]
	baseData, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"migrations",
		"000015_worker_turn_settlement.sql",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"ADD COLUMN source_kind text",
		"ADD COLUMN source_reason text",
		"ADD COLUMN source_occurred_at timestamptz",
		"ADD COLUMN source_ref text",
		"ADD COLUMN source_digest text",
		"ADD COLUMN source_state jsonb",
		"ADD COLUMN source_state_digest text",
		"source_kind IN ('runtime_event', 'backend_deadline')",
		"source_kind = 'runtime_event'",
		"source_kind = 'backend_deadline'",
		"preempted_by_turn_settlement_id text",
		"REFERENCES worker_turn_settlements (settlement_id)",
		"runtime_recovery_incidents_deadline_settlement_check",
		"outcome_reason = 'preempted_by_backend_deadline'",
		"source_reason = 'runtime_terminal'",
		"source_occurred_at = accepted_at",
		"source_ref = terminal_message_id",
		"source_digest = terminal_envelope_digest",
		`'{"phase":"terminal_observed","schema_version":1}'::jsonb`,
		"sha256:eade79e0a1ca190d82f879fee630b0a45261063eefac1d74cd68c417e861348f",
		"source_digest ~ '^sha256:[0-9a-f]{64}$'",
		"source_state_digest ~ '^sha256:[0-9a-f]{64}$'",
		"jsonb_typeof(source_state) = 'object'",
		"ALTER COLUMN runtime_instance_id DROP NOT NULL",
		"ALTER COLUMN runtime_generation DROP NOT NULL",
		"ALTER COLUMN channel_epoch DROP NOT NULL",
		"ALTER COLUMN terminal_event_direction DROP DEFAULT",
		"ALTER COLUMN terminal_event_sequence DROP NOT NULL",
		"ALTER COLUMN terminal_message_id DROP NOT NULL",
		"DROP COLUMN source_state_digest",
		"DROP COLUMN source_kind",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("Turn settlement source-union migration lacks invariant %q", fragment)
		}
	}
	for _, preserved := range []string{
		"REFERENCES runtime_control_messages (runtime_instance_id, direction, sequence)",
		"UNIQUE (runtime_instance_id, terminal_event_sequence)",
		"UNIQUE (runtime_instance_id, terminal_message_id)",
	} {
		if !strings.Contains(string(baseData), preserved) {
			t.Errorf("Base Turn settlement migration lacks preserved invariant %q", preserved)
		}
	}
	// The source union deliberately replaces the base lifecycle CHECK so a
	// forced Backend source can retain an ignored late witness without taking
	// the old Accepted -> Completed shortcut. No FK or UNIQUE constraint may
	// be removed.
	upSQLWithoutLifecycleCheck := strings.Replace(
		upSQL,
		"DROP CONSTRAINT worker_turn_settlements_check1",
		"",
		1,
	)
	if strings.Contains(upSQLWithoutLifecycleCheck, "DROP CONSTRAINT") {
		t.Fatal("Turn settlement source-union migration drops an existing FK or unique constraint")
	}
}
