//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestWorkerTimelinePagesDurableCheckpointsAndMarksMissingHistory(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	base := time.Date(2026, time.July, 25, 15, 0, 0, 0, time.UTC)
	seedDisplayTurn(t, ctx, pool, base)
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
 (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
  idempotency_key,created_at,started_at,deadline_at,terminal_at)
VALUES
 ('turn-2','session-1','run-1','agent-session-1','prompt','{"text":"second prompt"}',
  'completed',1,'turn-2',$1,$1,$1::timestamptz + interval '24 hours',$1::timestamptz + interval '20 seconds'),
 ('turn-3','session-1','run-1','agent-session-1','prompt','{"text":"legacy prompt"}',
  'failed',1,'turn-3',$2,$2,$2::timestamptz + interval '24 hours',$2::timestamptz + interval '20 seconds')`,
		base.Add(time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	store := projectionpostgres.NewStore(pool)
	saveTerminalCheckpoint(t, ctx, store, "display-1", "turn-1", base.Add(30*time.Second))
	saveTerminalCheckpoint(t, ctx, store, "display-2", "turn-2", base.Add(90*time.Second))

	first, err := store.ReadWorkerTimeline(ctx, projectionapp.WorkerTimelineQuery{
		Viewer:          projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		WorkerSessionID: "session-1", Limit: 1,
	})
	if err != nil || len(first.Turns) != 1 || first.Turns[0].AgentTurnID != "turn-2" ||
		!first.HasMore || first.NextCursor == "" || len(first.Turns[0].Parts) != 1 {
		t.Fatalf("first timeline page=%+v error=%v", first, err)
	}
	second, err := store.ReadWorkerTimeline(ctx, projectionapp.WorkerTimelineQuery{
		Viewer:          projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		WorkerSessionID: "session-1", BeforeCursor: first.NextCursor, Limit: 1,
	})
	if err != nil || len(second.Turns) != 1 || second.Turns[0].AgentTurnID != "turn-1" ||
		second.HasMore {
		t.Fatalf("second timeline page=%+v error=%v", second, err)
	}

	reconciled, err := store.ReconcileTerminalDisplayHistory(ctx, "tenant-local", "user-local")
	if err != nil || reconciled != 1 {
		t.Fatalf("reconciled=%d error=%v", reconciled, err)
	}
	var backfill string
	var version uint64
	var complete bool
	if err := pool.QueryRow(ctx, `
SELECT backfill_status,latest_checkpoint_version,checkpoint_complete
FROM worker_timeline_turns WHERE agent_turn_id='turn-3'`).Scan(
		&backfill, &version, &complete,
	); err != nil {
		t.Fatal(err)
	}
	if backfill != "non_backfillable" || version != 0 || complete {
		t.Fatalf("legacy timeline state backfill=%q version=%d complete=%t", backfill, version, complete)
	}
}

func saveTerminalCheckpoint(
	t *testing.T,
	ctx context.Context,
	store *projectionpostgres.Store,
	displayStreamID, agentTurnID string,
	at time.Time,
) {
	t.Helper()
	checkpoint := checkpointFixture(at)
	checkpoint.DisplayStreamID = displayStreamID
	checkpoint.AgentTurnID = agentTurnID
	checkpoint.Status = "completed"
	checkpoint.Parts[0].PartID = "part-" + agentTurnID
	checkpoint.Parts[0].LifecycleStatus = "completed"
	checkpoint.Parts[0].Payload = json.RawMessage(`{"format":"markdown","content":"Durable Agent output"}`)
	digest, err := projectionapp.DisplayCheckpointDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDisplayCheckpoint(
		ctx, checkpoint, digest, "checkpoint-"+displayStreamID,
	); err != nil {
		t.Fatal(err)
	}
}
