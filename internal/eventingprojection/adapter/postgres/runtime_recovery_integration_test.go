//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestRuntimeRecoveryProjectionReplaysOneIncidentAndSuppressesExecutionActions(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 26, 14, 0, 0, 0, time.UTC)
	seedDeliveredBoundTask(t, ctx, pool, now)
	for index, statement := range []string{
		`UPDATE tasks SET status='running',revision=4 WHERE task_id='task-1'`,
		`UPDATE worker_runs SET status='running',revision=4,terminal_at=NULL WHERE worker_run_id='run-1'`,
		`UPDATE agent_turns SET status='delivered',revision=3,terminal_at=NULL WHERE agent_turn_id='turn-1'`,
		`UPDATE worker_sessions SET status='recovering',session_revision=5 WHERE worker_session_id='session-1'`,
		`UPDATE runtime_instances SET status='unhealthy',revision=3 WHERE runtime_instance_id='runtime-1'`,
		`INSERT INTO runtime_recovery_incidents
  (incident_id,tenant_id,worker_session_id,worker_run_id,agent_turn_id,
   failed_runtime_instance_id,failed_generation,failed_channel_epoch,trigger,phase,
   previous_session_status,attempt_count,fence_status,continuity_status,evidence_ref,evidence_at,
   revision,opened_at,updated_at)
VALUES
  ('incident-1','tenant-local','session-1','run-1','turn-1',
   'runtime-1',1,1,'container_exit','open',
   'running',0,'pending','pending','runtime-inspection/runtime-1',$1,1,$1,$1)`,
	} {
		args := []any(nil)
		if index == 5 {
			args = append(args, now)
		}
		if _, err := pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed recovery projection statement %d: %v", index+1, err)
		}
	}
	store := NewStore(pool)
	materializer := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		UserID: "user-local", Now: func() time.Time { return now.Add(time.Second) },
	}
	event := recoveryProjectionEvent(1, "RuntimeRecoveryStarted.v1", now)
	if err := materializer.Publish(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Publish(ctx, event); err != nil {
		t.Fatalf("duplicate recovery event: %v", err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now: func() time.Time { return now.Add(2 * time.Second) },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewWorkerDetail,
		ViewID:   "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Status          string `json:"status"`
		RuntimeRecovery struct {
			Status           string `json:"status"`
			Phase            string `json:"phase"`
			FailedGeneration uint64 `json:"failed_generation"`
			AttemptCount     uint32 `json:"attempt_count"`
			ContinuityStatus string `json:"continuity_status"`
		} `json:"runtime_recovery"`
	}
	if err := json.Unmarshal(snapshot.Data, &detail); err != nil {
		t.Fatal(err)
	}
	var contract map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &contract); err != nil {
		t.Fatal(err)
	}
	if _, ok := contract["timeline"]; ok {
		t.Fatalf("WorkerDetail still projects migration-only flat timeline: %s", snapshot.Data)
	}
	if _, ok := contract["timeline_groups"]; !ok {
		t.Fatalf("WorkerDetail is missing authoritative grouped timeline: %s", snapshot.Data)
	}
	if detail.Status != "recovering" ||
		detail.RuntimeRecovery.Status != "recovering_runtime" ||
		detail.RuntimeRecovery.Phase != "open" ||
		detail.RuntimeRecovery.FailedGeneration != 1 ||
		detail.RuntimeRecovery.AttemptCount != 0 ||
		detail.RuntimeRecovery.ContinuityStatus != "pending" {
		t.Fatalf("detail=%+v data=%s", detail, snapshot.Data)
	}
	for _, action := range snapshot.AvailableActions {
		if strings.HasPrefix(action.Action, "worker.") {
			t.Fatalf("recovering Worker exposed execution action %+v", action)
		}
	}
	var receipts, deltas int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_event_receipts WHERE source_event_id=$1),
  (SELECT count(*) FROM projection_deltas
   WHERE view_type='worker_detail' AND view_id='session-1')`,
		event.EventID,
	).Scan(&receipts, &deltas); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || deltas != 1 {
		t.Fatalf("receipts=%d worker_deltas=%d", receipts, deltas)
	}
}

func recoveryProjectionEvent(
	sequence uint64,
	eventType string,
	at time.Time,
) contractsv1.IntegrationEvent {
	return contractsv1.IntegrationEvent{
		SchemaVersion:   contractsv1.IntegrationEventSchemaVersion,
		EventID:         "recovery-projection-event-1",
		EventType:       eventType,
		ProducerContext: "worker-runtime", StreamID: "worker-session/session-1",
		Sequence: sequence, TenantID: "tenant-local", AggregateType: "WorkerSession",
		AggregateID: "session-1", TraceID: "worker-session/session-1",
		CorrelationID: "incident-1", CausationID: "runtime-1",
		IdempotencyKey: "recovery-projection/incident-1/open",
		Visibility:     "internal", OccurredAt: at,
		Payload: json.RawMessage(`{"incident_id":"incident-1","phase":"open"}`),
	}
}
