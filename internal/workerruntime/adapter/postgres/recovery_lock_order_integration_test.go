//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRecoveryReplacementWaitsForSessionBeforeRetiringRuntime(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{name: "replacement_and_replay"},
		{name: "stale_session_revision", mutation: "session_revision=session_revision+1"},
		{name: "changed_failed_binding", mutation: "active_runtime_instance_id=NULL"},
		{name: "changed_generation", mutation: "runtime_generation=runtime_generation+1"},
		{name: "changed_status", mutation: "status='waiting_user'"},
		{name: "outbox_failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			base := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
			started, active := startActiveRecoveryFixture(t, pool, base)
			advancePool := lockOrderPool(t, pool, "recovery-replacement")
			sessionPool := lockOrderPool(t, pool, "recovery-session-owner")
			service := workerapplication.RuntimeRecoveryService{
				Store: workerpostgres.NewRuntimeRecoveryStore(advancePool), IDs: &concurrentAllocationIDs{},
				Now: func() time.Time { return base.Add(6 * time.Minute) },
			}
			incident, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
				TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
				WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
				FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 1,
				Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
				EvidenceAt: base.Add(6 * time.Minute), ExpectedSessionRevision: active.SessionRevision,
				ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
				ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "recovery-lock/open",
			})
			if err != nil {
				t.Fatal(err)
			}
			fencing, err := service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
				TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID,
				TargetPhase: domain.RecoveryFencing, ExpectedIncidentRevision: incident.IncidentRevision,
				IdempotencyKey: "recovery-lock/fencing",
			})
			if err != nil {
				t.Fatal(err)
			}
			command := workerapplication.AdvanceRuntimeRecoveryCommand{
				TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID,
				TargetPhase: domain.RecoveryProvisioning, ExpectedIncidentRevision: fencing.IncidentRevision,
				FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 2, IdempotencyKey: "recovery-lock/replacement",
			}
			blocker, err := sessionPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			var id string
			if err := blocker.QueryRow(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, started.WorkerSessionID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			type result struct {
				state workerapplication.RuntimeRecoveryState
				err   error
			}
			done := make(chan result, 1)
			received := false
			go func() { state, err := service.Advance(ctx, command); done <- result{state, err} }()
			// Always cancel and drain the caller before pool/schema cleanup, including a failed probe.
			defer func() {
				cancel()
				if received {
					return
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("replacement caller did not stop")
				}
			}()
			waitDispatchLockEdge(t, ctx, pool, "recovery-replacement", "recovery-session-owner")
			probe, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			probeErr := probe.QueryRow(ctx, `SELECT runtime_instance_id FROM runtime_instances WHERE runtime_instance_id=$1 FOR UPDATE NOWAIT`, started.RuntimeInstanceID).Scan(&id)
			// Release the probe before the Session lock: the probe must never manufacture a cycle.
			if err := probe.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if probeErr != nil {
				var pgErr *pgconn.PgError
				if errors.As(probeErr, &pgErr) {
					t.Logf("failed Runtime NOWAIT SQLSTATE=%s", pgErr.Code)
				}
				t.Fatalf("replacement held failed Runtime while waiting for Session: %v", probeErr)
			}
			t.Log("failed Runtime NOWAIT acquired while replacement waits for exact Session")
			if tc.mutation != "" {
				if _, err := blocker.Exec(ctx, `UPDATE worker_sessions SET `+tc.mutation+` WHERE worker_session_id=$1`, started.WorkerSessionID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "outbox_failure" {
				rejectRuntimeRecoveryOutbox(t, pool)
			}
			before := recoveryReplacementSnapshot(t, ctx, blocker)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got := <-done
			received = true
			if tc.name == "outbox_failure" {
				var pgErr *pgconn.PgError
				if !errors.As(got.err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "injected Runtime recovery Outbox failure" {
					t.Fatalf("replacement Outbox error=%v", got.err)
				}
			}
			if tc.mutation != "" || tc.name == "outbox_failure" {
				if tc.mutation != "" && !errors.Is(got.err, platformpostgres.ErrRevisionConflict) {
					t.Fatalf("changed Session error=%v want revision conflict", got.err)
				}
				after := recoveryReplacementSnapshot(t, ctx, pool)
				if before != after {
					t.Fatalf("rejected replacement partially wrote state\nbefore=%s\nafter=%s", before, after)
				}
				t.Logf("rejected replacement (%v); Session/Runtime/fence/intent/incident/Run/Turn/Task/outbox/idempotency snapshot unchanged", got.err)
				return
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			state := got.state
			if state.Phase != domain.RecoveryProvisioning || state.AttemptCount != 1 || state.ReplacementGeneration != 2 || state.ReplacementChannelEpoch != 3 || state.ReplacementRuntimeInstanceID == "" || state.SessionRevision != fencing.SessionRevision+1 || state.WorkerRunID != started.WorkerRunID || state.AgentTurnID != started.AgentTurnID || !state.AgentTurnDeadlineAt.Equal(base.Add(4*time.Minute+24*time.Hour)) {
				t.Fatalf("replacement=%+v", state)
			}
			var committed bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM worker_sessions s
JOIN runtime_instances failed ON failed.runtime_instance_id=$2
JOIN runtime_instances replacement ON replacement.runtime_instance_id=$3
JOIN runtime_mount_fences fence ON fence.incident_id=$4
JOIN runtime_replacement_intents intent ON intent.incident_id=$4
WHERE s.worker_session_id=$1 AND s.status='recovering'
  AND s.active_runtime_instance_id=$3 AND s.runtime_generation=2
  AND failed.status='lost' AND replacement.status='starting'
  AND replacement.worker_session_id=$1 AND replacement.worker_run_id=$5
  AND fence.status='confirmed' AND fence.fencing_token=2
  AND intent.status='reserved' AND intent.replacement_runtime_instance_id=$3
)`, started.WorkerSessionID, started.RuntimeInstanceID, state.ReplacementRuntimeInstanceID,
				incident.IncidentID, started.WorkerRunID).Scan(&committed); err != nil {
				t.Fatal(err)
			}
			if !committed {
				t.Fatal("replacement/fence/intent/Session bindings were not committed together")
			}
			assertAllocationCount(t, pool, "runtime_instances", 2)
			assertAllocationCount(t, pool, "runtime_replacement_intents", 1)
			assertAllocationCount(t, pool, "runtime_recovery_incidents", 1)
			var events int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE correlation_id=$1`, incident.IncidentID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != 3 {
				t.Fatalf("recovery phase events=%d want=3", events)
			}
			after := recoveryReplacementSnapshot(t, ctx, pool)
			replayed, err := service.Advance(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			if replayed.ReplacementRuntimeInstanceID != state.ReplacementRuntimeInstanceID || replayed.IncidentRevision != state.IncidentRevision || replayed.EventID != state.EventID {
				t.Fatalf("replay=%+v original=%+v", replayed, state)
			}
			if replay := recoveryReplacementSnapshot(t, ctx, pool); replay != after {
				t.Fatalf("replay wrote durable state\nbefore=%s\nafter=%s", after, replay)
			}
			t.Log("one replacement/intent, generation 2, original Run/Turn/deadline; exact replay made zero writes")
		})
	}
}

// Catalog/row inspection is the explicitly agreed PostgreSQL lock/atomicity seam.
func recoveryReplacementSnapshot(t *testing.T, ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) string {
	t.Helper()
	var snapshot string
	if err := query.QueryRow(ctx, `SELECT jsonb_build_object(
 'sessions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY worker_session_id) FROM worker_sessions r),
 'runtimes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY runtime_instance_id) FROM runtime_instances r),
 'fences',(SELECT jsonb_agg(to_jsonb(r) ORDER BY incident_id) FROM runtime_mount_fences r),
 'intents',(SELECT jsonb_agg(to_jsonb(r) ORDER BY incident_id) FROM runtime_replacement_intents r),
 'incidents',(SELECT jsonb_agg(to_jsonb(r) ORDER BY incident_id) FROM runtime_recovery_incidents r),
 'runs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY worker_run_id) FROM worker_runs r),
 'turns',(SELECT jsonb_agg(to_jsonb(r) ORDER BY agent_turn_id) FROM agent_turns r),
 'tasks',(SELECT jsonb_agg(to_jsonb(r) ORDER BY task_id) FROM tasks r),
 'outbox',(SELECT jsonb_agg(to_jsonb(r) ORDER BY event_id) FROM outbox_events r),
 'idempotency',(SELECT jsonb_agg(to_jsonb(r) ORDER BY command_kind,idempotency_key) FROM runtime_recovery_idempotency_results r)
 )::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
