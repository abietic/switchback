package application_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

func TestACPDisplayStreamConvergesTransientAgentTextToDurableCheckpoint(t *testing.T) {
	stream := displayStream(t)
	base := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	started, err := stream.Start(base)
	if err != nil || started.CheckpointVersion != 1 || len(started.Parts) != 0 {
		t.Fatalf("start checkpoint=%+v error=%v", started, err)
	}
	first := applyDisplay(t, stream, 1, "agent_message_chunk",
		`{"messageId":"message-1","content":{"type":"text","text":"**Done"}}`, base.Add(time.Second))
	second := applyDisplay(t, stream, 2, "agent_message_chunk",
		`{"messageId":"message-1","content":{"type":"text","text":"**\n"}}`, base.Add(2*time.Second))
	hidden := applyDisplay(t, stream, 3, "agent_thought_chunk",
		`{"content":{"type":"text","text":"private chain of thought"}}`, base.Add(3*time.Second))
	if first.Transient == nil || second.Transient == nil || first.Checkpoint != nil ||
		second.Checkpoint != nil || hidden != (projectionapp.DisplayEmission{}) {
		t.Fatalf("text emissions first=%+v second=%+v hidden=%+v", first, second, hidden)
	}
	terminal := applyDisplay(t, stream, 4, "terminal",
		`{"stop_reason":"end_turn"}`, base.Add(4*time.Second))
	if terminal.Checkpoint == nil || terminal.Checkpoint.CheckpointVersion != 2 ||
		terminal.Checkpoint.Status != "completed" || len(terminal.Checkpoint.Parts) != 1 {
		t.Fatalf("terminal checkpoint = %+v", terminal.Checkpoint)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(terminal.Checkpoint.Parts[0].Payload, &payload); err != nil ||
		payload.Content != "**Done**\n" ||
		terminal.Checkpoint.Parts[0].LifecycleStatus != "completed" {
		t.Fatalf("text part=%+v payload=%+v error=%v", terminal.Checkpoint.Parts[0], payload, err)
	}
	encoded, _ := json.Marshal(terminal.Checkpoint)
	if strings.Contains(string(encoded), "private chain of thought") {
		t.Fatal("hidden reasoning entered the durable DisplayCheckpoint")
	}
}

func TestACPDisplayStreamCheckpointsSafeToolLifecycleAndTruncatesOutput(t *testing.T) {
	stream := displayStream(t)
	base := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	if _, err := stream.Start(base); err != nil {
		t.Fatal(err)
	}
	start := applyDisplay(t, stream, 1, "tool_call", `{
  "toolCallId":"call-1",
  "status":"in_progress",
  "kind":"execute",
  "title":"curl -H 'Authorization: Bearer secret-token-value' https://example.com",
  "locations":[{"path":"/workspace/really/long/file.go","line":12}],
  "rawInput":{"command":"echo sk-secret123456789","cwd":"/workspace","unsafe":"never"}
}`, base.Add(time.Second))
	longOutput := strings.Repeat("界", 120) + " sk-secret123456789"
	finish := applyDisplay(t, stream, 2, "tool_call_update", fmt.Sprintf(`{
  "toolCallId":"call-1",
  "status":"completed",
  "rawOutput":{"formatted_output":%q,"exit_code":0}
}`, longOutput), base.Add(3*time.Second))
	if start.Checkpoint == nil || finish.Checkpoint == nil ||
		start.Checkpoint.CheckpointVersion != 2 || finish.Checkpoint.CheckpointVersion != 3 {
		t.Fatalf("Tool checkpoints start=%+v finish=%+v", start, finish)
	}
	encoded, _ := json.Marshal(finish.Checkpoint.Parts[0])
	content := string(encoded)
	for _, forbidden := range []string{
		"secret-token-value", "sk-secret123456789", `"command"`, `"unsafe"`,
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("safe Tool part exposed %q: %s", forbidden, content)
		}
	}
	for _, required := range []string{
		`"title":"Run command"`, `"cwd":"/workspace"`, `"duration_ms":2000`,
		`"path":"/workspace/really/long/file.go"`, `[truncated]`, `"output_truncated":true`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("safe Tool part omitted %q: %s", required, content)
		}
	}
}

func TestACPDisplayStreamDeduplicatesUpdatesAndAppendsLateArtifacts(t *testing.T) {
	stream := displayStream(t)
	base := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	if _, err := stream.Start(base); err != nil {
		t.Fatal(err)
	}
	update := projectionapp.NormalizedDisplayUpdate{
		Sequence: 1, Kind: "terminal", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`),
		ObservedAt: base.Add(time.Second),
	}
	first, err := stream.Apply(update)
	if err != nil || first.Checkpoint == nil {
		t.Fatalf("terminal emission=%+v error=%v", first, err)
	}
	replayed, err := stream.Apply(update)
	if err != nil || replayed != (projectionapp.DisplayEmission{}) {
		t.Fatalf("duplicate emission=%+v error=%v", replayed, err)
	}
	conflict := update
	conflict.Payload = json.RawMessage(`{"stop_reason":"cancelled"}`)
	if _, err := stream.Apply(conflict); !errors.Is(err, projectionapp.ErrDisplayCheckpointConflict) {
		t.Fatalf("conflicting duplicate error=%v", err)
	}
	late, err := stream.AppendArtifacts([]projectionapp.DisplayArtifact{{
		ArtifactID: "artifact-report-1", Type: "report", Label: "Stage report",
		Href: "/api/v1/views/artifact_detail/artifact-report-1",
	}}, base.Add(2*time.Second))
	if err != nil || late.CheckpointVersion != first.Checkpoint.CheckpointVersion+1 ||
		late.Status != "completed" || len(late.Parts) != 1 ||
		late.Parts[0].PartType != "artifact" {
		t.Fatalf("late Artifact checkpoint=%+v error=%v", late, err)
	}
}

func TestACPDisplayStreamSeparatesKnownModelDiagnosticFromAgentContent(t *testing.T) {
	stream := displayStream(t)
	base := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	if _, err := stream.Start(base); err != nil {
		t.Fatal(err)
	}
	emission := applyDisplay(t, stream, 1, "agent_message_chunk", `{
  "messageId":"message-1",
  "content":{"type":"text","text":"Warning: Model metadata for `+"`switchback-codex`"+` not found. Defaulting to fallback metadata; this can degrade performance and cause issues. ## Result\nDone."}
}`, base.Add(time.Second))
	if emission.Transient == nil || emission.Checkpoint == nil ||
		len(emission.Checkpoint.Parts) != 2 {
		t.Fatalf("diagnostic emission=%+v", emission)
	}
	encoded, _ := json.Marshal(emission.Checkpoint.Parts)
	content := string(encoded)
	if strings.Contains(content, "Model metadata for") ||
		!strings.Contains(content, `"code":"model_metadata_fallback"`) ||
		!strings.Contains(content, `## Result\nDone.`) {
		t.Fatalf("diagnostic parts=%s", content)
	}
}

func displayStream(t *testing.T) *projectionapp.ACPDisplayStream {
	t.Helper()
	stream, err := projectionapp.NewACPDisplayStream(projectionapp.DisplayStreamIdentity{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		AgentSessionID: "agent-session-1", RuntimeGeneration: 1,
	}, projectionapp.ACPDisplayPolicy{MaxTextBytes: 4096, MaxToolOutputBytes: 256, MaxLocations: 4})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func applyDisplay(
	t *testing.T,
	stream *projectionapp.ACPDisplayStream,
	sequence uint64,
	kind, payload string,
	at time.Time,
) projectionapp.DisplayEmission {
	t.Helper()
	emission, err := stream.Apply(projectionapp.NormalizedDisplayUpdate{
		Sequence: sequence, Kind: kind, Payload: json.RawMessage(payload), ObservedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return emission
}
