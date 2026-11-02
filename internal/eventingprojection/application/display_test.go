package application_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

func TestTransientBrokerIsBoundedAndFencedByCheckpoint(t *testing.T) {
	t.Parallel()
	broker, err := projectionapp.NewTransientBroker(projectionapp.TransientPolicy{MaxItems: 2, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 3; sequence++ {
		if err := broker.Publish(transientDelta(1, sequence)); err != nil {
			t.Fatalf("publish sequence %d: %v", sequence, err)
		}
	}
	if _, err := broker.ReadAfter("display-1", 1, 0); !errors.Is(err, projectionapp.ErrTransientGap) {
		t.Fatalf("evicted read error = %v", err)
	}
	retained, err := broker.ReadAfter("display-1", 1, 1)
	if err != nil || len(retained) != 2 || retained[0].TransientSequence != 2 || retained[1].TransientSequence != 3 {
		t.Fatalf("retained=%#v error=%v", retained, err)
	}
	encoded, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "view_cursor") || strings.Contains(string(encoded), "projection_version") {
		t.Fatalf("transient lane contains durable cursor fields: %s", encoded)
	}
	checkpoint := displayCheckpoint(2)
	checkpoint.LastObservedTransientSequence = 3
	if err := broker.Supersede(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(transientDelta(1, 4)); !errors.Is(err, projectionapp.ErrTransientBaseMismatch) {
		t.Fatalf("stale base error = %v", err)
	}
	if err := broker.Publish(transientDelta(2, 4)); err != nil {
		t.Fatalf("publish next checkpoint delta: %v", err)
	}
}

func displayCheckpoint(version uint64) projectionapp.DisplayCheckpoint {
	now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	return projectionapp.DisplayCheckpoint{
		SchemaVersion: projectionapp.DisplayCheckpointSchemaVersion, DisplayStreamID: "display-1",
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		CheckpointVersion: version, Status: "streaming", Parts: []projectionapp.DisplayPart{{
			SchemaVersion: projectionapp.DisplayPartSchemaVersion, PartID: "part-1", PartType: "text",
			PartRevision: version, Ordinal: 0, LifecycleStatus: "streaming", Critical: false,
			Visibility: json.RawMessage(`{"audience":"user"}`), Provenance: json.RawMessage(`{"source":"acp"}`),
			AvailableActions: []projectionapp.ActionRef{}, Payload: json.RawMessage(`{"format":"markdown","content":"Working"}`),
			CreatedAt: now, UpdatedAt: now,
		}},
		TraceCursorRef:      json.RawMessage(`{"cursor":"3"}`),
		DurabilityPolicyRef: json.RawMessage(`{"policy_id":"display-m1-v1","revision":1}`),
		CheckpointedAt:      now,
	}
}

func transientDelta(base, sequence uint64) projectionapp.TransientDisplayDelta {
	return projectionapp.TransientDisplayDelta{
		SchemaVersion: projectionapp.TransientDeltaSchemaVersion, DisplayStreamID: "display-1", PartID: "part-1",
		BaseCheckpointVersion: base, TransientSequence: sequence, DeltaType: "text_delta",
		Payload: json.RawMessage(`{"text":"next"}`), EmittedAt: time.Now().UTC(),
	}
}
