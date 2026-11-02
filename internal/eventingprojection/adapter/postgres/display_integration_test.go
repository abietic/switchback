//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestDisplayCheckpointPersistsAndAdvancesDurableWorkerView(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	projector := projectionapp.Projector{Store: store}
	base := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	seedDisplayTurn(t, ctx, pool, base)
	initial := projectionapp.ProjectionEvent{
		SourceEventID: "source-1", SourceStreamID: "worker-stream-1", SourceSequence: 1, ProjectedAt: base,
		Updates: []projectionapp.ViewUpdate{viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-1")},
	}
	if _, err := projector.Execute(ctx, initial); err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointFixture(base.Add(time.Minute))
	update := viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-checkpoint-1")
	update.EventType = "view.stream_checkpointed.v1"
	update.Data = json.RawMessage(`{"status":"running","display_checkpoint_version":1}`)
	update.DeltaPayload = json.RawMessage(`{"display_stream_id":"display-1","checkpoint_version":1}`)
	event := projectionapp.ProjectionEvent{
		SourceEventID: "source-2", SourceStreamID: "worker-stream-1", SourceSequence: 2,
		ProjectedAt: checkpoint.CheckpointedAt, Updates: []projectionapp.ViewUpdate{update},
	}
	service := projectionapp.DisplayCheckpointService{Checkpoints: store, Projector: projector}
	result, err := service.Save(ctx, checkpoint, event)
	if err != nil || len(result) != 1 || result[0].ViewCursor != 2 || result[0].ProjectionVersion != 2 {
		t.Fatalf("checkpoint projection result=%#v error=%v", result, err)
	}
	assertProjectionCount(t, pool, "display_checkpoints", 1)
	var eventType string
	if err := pool.QueryRow(ctx, `
SELECT event_type FROM projection_deltas WHERE view_type='worker_detail' AND view_cursor=2`).Scan(&eventType); err != nil {
		t.Fatal(err)
	}
	if eventType != "view.stream_checkpointed.v1" {
		t.Fatalf("delta event type = %q", eventType)
	}
	if _, err := service.Save(ctx, checkpoint, event); err != nil {
		t.Fatalf("idempotent checkpoint retry: %v", err)
	}
	assertProjectionCount(t, pool, "display_checkpoints", 1)
	assertProjectionCount(t, pool, "projection_deltas", 2)
}

func seedDisplayTurn(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
) {
	t.Helper()
	statements := []string{
		`INSERT INTO worker_sessions
 (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,
  session_revision,runtime_generation,quota_reservation_revision,idle_expires_at,absolute_expires_at,
  created_at,updated_at)
VALUES ('session-1','tenant-local','user-local','workspace-demo','workspace-revision-1',
 'working-copy-1','interactive',1,1,1,$1::timestamptz + interval '48 hours',
 $1::timestamptz + interval '14 days',$1,$1)`,
		`INSERT INTO worker_runs
 (worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,
  agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at,terminal_at)
VALUES ('run-1','session-1','direct_agent_turn','{}','profile-1','1',
 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
 'completed',1,$1,$1,$1::timestamptz + interval '30 seconds')`,
		`INSERT INTO agent_turns
 (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
  idempotency_key,created_at,started_at,deadline_at,terminal_at)
VALUES ('turn-1','session-1','run-1','agent-session-1','prompt','{"text":"test display"}',
 'completed',1,'turn-1',$1,$1,$1::timestamptz + interval '24 hours',
 $1::timestamptz + interval '30 seconds')`,
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed Display statement %d: %v", index+1, err)
		}
	}
}

func checkpointFixture(now time.Time) projectionapp.DisplayCheckpoint {
	return projectionapp.DisplayCheckpoint{
		SchemaVersion: projectionapp.DisplayCheckpointSchemaVersion, DisplayStreamID: "display-1",
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		CheckpointVersion: 1, Status: "streaming", Parts: []projectionapp.DisplayPart{{
			SchemaVersion: projectionapp.DisplayPartSchemaVersion, PartID: "part-1", PartType: "text",
			PartRevision: 1, Ordinal: 0, LifecycleStatus: "streaming", Visibility: json.RawMessage(`{"audience":"user"}`),
			Provenance: json.RawMessage(`{"source":"acp"}`), AvailableActions: []projectionapp.ActionRef{},
			Payload: json.RawMessage(`{"format":"markdown","content":"Working"}`), CreatedAt: now, UpdatedAt: now,
		}},
		LastObservedTransientSequence: 4, TraceCursorRef: json.RawMessage(`{"cursor":"4"}`),
		DurabilityPolicyRef: json.RawMessage(`{"policy_id":"display-m1-v1","revision":1}`), CheckpointedAt: now,
	}
}
