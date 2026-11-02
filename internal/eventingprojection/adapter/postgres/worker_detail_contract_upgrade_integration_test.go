//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestBootstrapUpgradesPreexistingWorkerDetailContractOnce(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 29, 10, 30, 0, 0, time.UTC)
	store := NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	legacyData := json.RawMessage(`{
		"worker_session_id":"session-legacy-worker-detail",
		"timeline":[
			{"id":"turn-legacy-input","kind":"user","title":"Prompt","body":"keep grouped history","at":"2026-07-29T10:00:00Z"}
		],
		"timeline_groups":[
			{
				"worker_run_id":"run-legacy",
				"agent_turn_id":"turn-legacy",
				"mode":"prompt",
				"status":"completed",
				"started_at":"2026-07-29T10:00:00Z",
				"terminal_at":"2026-07-29T10:01:00Z",
				"input":{"text":"keep grouped history"},
				"updates":[{"kind":"agent_message","body":{"text":"grouped response"}}]
			}
		],
		"timeline_cursor":"worker-timeline-v1:legacy",
		"timeline_has_more":true,
		"unrelated_contract":{"preserved":true}
	}`)
	legacyEvent := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       "legacy-worker-detail-snapshot", EventType: "LegacyWorkerDetailStored.v1",
		ProducerContext: "eventing-projection", StreamID: "legacy-worker-detail-snapshot",
		Sequence: 1, TenantID: "tenant-local", AggregateType: "WorkerSession",
		AggregateID: "session-legacy-worker-detail", TraceID: "legacy-worker-detail-snapshot",
		CorrelationID: "legacy-worker-detail-snapshot", CausationID: "legacy-worker-detail-snapshot",
		IdempotencyKey: "legacy-worker-detail-snapshot", Visibility: "internal",
		OccurredAt: now.Add(-time.Hour), Payload: json.RawMessage(`{"legacy":true}`),
	}
	legacyActions := []projectionapp.ActionRef{{
		Action: "worker.send_prompt", TargetID: "session-legacy-worker-detail", ExpectedRevision: 7,
	}}
	legacyRefs := []projectionapp.RelatedRef{{
		Type: "task", ID: "task-legacy-worker-detail", Href: "/api/v1/tasks/task-legacy-worker-detail",
	}}
	legacyProjection := projectionapp.ProjectionEvent{
		SourceEventID: legacyEvent.EventID, SourceStreamID: legacyEvent.StreamID,
		SourceSequence: legacyEvent.Sequence, ProjectedAt: legacyEvent.OccurredAt,
		Updates: []projectionapp.ViewUpdate{
			materializer.update(
				legacyEvent, projectionapp.ViewWorkerDetail,
				legacyEvent.AggregateID, legacyData, legacyActions, legacyRefs,
			),
		},
	}
	if _, err := materializer.Projector.Execute(ctx, legacyProjection); err != nil {
		t.Fatal(err)
	}

	viewer := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}
	before, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: legacyEvent.AggregateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeContract := decodeWorkerDetailContract(t, before.Data)
	if _, ok := beforeContract["timeline"]; !ok {
		t.Fatalf("preexisting WorkerDetail fixture lacks legacy timeline: %s", before.Data)
	}

	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}
	after, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: legacyEvent.AggregateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterContract := decodeWorkerDetailContract(t, after.Data)
	if _, ok := afterContract["timeline"]; ok {
		t.Fatalf("upgraded WorkerDetail still stores flat timeline: %s", after.Data)
	}
	for _, key := range []string{
		"timeline_groups", "timeline_cursor", "timeline_has_more", "unrelated_contract",
	} {
		if !reflect.DeepEqual(decodeJSONValue(t, beforeContract[key]), decodeJSONValue(t, afterContract[key])) {
			t.Fatalf("WorkerDetail contract key %q changed: before=%s after=%s", key, beforeContract[key], afterContract[key])
		}
	}
	if !reflect.DeepEqual(before.AvailableActions, after.AvailableActions) ||
		!reflect.DeepEqual(before.RelatedRefs, after.RelatedRefs) {
		t.Fatalf("WorkerDetail authority changed: before=%+v/%+v after=%+v/%+v",
			before.AvailableActions, before.RelatedRefs, after.AvailableActions, after.RelatedRefs)
	}
	if after.ViewEpoch != before.ViewEpoch ||
		after.ViewCursor != before.ViewCursor+1 ||
		after.ProjectionVersion != before.ProjectionVersion+1 {
		t.Fatalf(
			"upgraded WorkerDetail identity epoch/cursor/version=%d/%d/%d, want %d/%d/%d",
			after.ViewEpoch, after.ViewCursor, after.ProjectionVersion,
			before.ViewEpoch, before.ViewCursor+1, before.ProjectionVersion+1,
		)
	}

	snapshot, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: legacyEvent.AggregateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeWorkerDetailContract(t, snapshot.Data)["timeline"]; ok {
		t.Fatalf("Frontier read still publishes flat timeline: %s", snapshot.Data)
	}
	replay, err := views.Resume(ctx, projectionapp.ResumeQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail,
		ViewID: legacyEvent.AggregateID, AfterEpoch: before.ViewEpoch,
		AfterCursor: before.ViewCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Control != nil || len(replay.Deltas) != 1 ||
		replay.Deltas[0].ViewCursor != after.ViewCursor {
		t.Fatalf("WorkerDetail upgrade replay=%+v", replay)
	}
	var deltaPayload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(replay.Deltas[0].Payload, &deltaPayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeWorkerDetailContract(t, deltaPayload.Data)["timeline"]; ok {
		t.Fatalf("WorkerDetail upgrade delta republishes flat timeline: %s", replay.Deltas[0].Payload)
	}

	clock = now.Add(time.Hour)
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatalf("repeat bootstrap: %v", err)
	}
	restarted, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: legacyEvent.AggregateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ViewEpoch != after.ViewEpoch ||
		restarted.ViewCursor != after.ViewCursor ||
		restarted.ProjectionVersion != after.ProjectionVersion {
		t.Fatalf("restart rewrote upgraded WorkerDetail: before=%+v after=%+v", after, restarted)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM projection_event_receipts
WHERE source_event_id LIKE 'local-worker-detail-contract-upgrade-v2/%'`,
	).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("WorkerDetail contract upgrade receipts=%d, want 1", receipts)
	}
}

func decodeWorkerDetailContract(t *testing.T, data json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var contract map[string]json.RawMessage
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func decodeJSONValue(t *testing.T, data json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
