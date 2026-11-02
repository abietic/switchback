package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

func TestBuildCreatesCompleteRedactedTurnEvidence(t *testing.T) {
	t.Parallel()

	workerTimestamp := time.Date(2026, time.July, 17, 12, 1, 2, 3, time.UTC)
	serverTimestamp := time.Date(2026, time.July, 17, 12, 1, 1, 2, time.UTC)
	raw := rawACPJSONL(t,
		rawFrame("agent_to_client", map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "agent-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{
						"type": "text",
						"text": "Final answer with worker-secret. ",
					},
				},
			},
		}),
		rawFrame("agent_to_client", map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "agent-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{
						"type": "text",
						"text": "All tests passed.",
					},
				},
			},
		}),
		rawFrame("agent_to_client", map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "agent-1",
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "tool-1",
					"title":         "Run tests",
					"rawInput": map[string]any{
						"command": "go test ./...",
					},
				},
			},
		}),
		rawFrame("agent_to_client", map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "agent-1",
				"update": map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "tool-1",
					"status":        "completed",
				},
			},
		}),
	)

	bundle, err := Build(Input{
		Identity: sharedevidence.TurnIdentity{
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 2,
			AgentSessionID:    "agent-1",
			AgentTurnID:       "turn-1",
			TraceID:           "trace-1",
		},
		ServerTimestamp: serverTimestamp,
		WorkerTimestamp: workerTimestamp,
		BeforeTree:      strings.Repeat("a", 40),
		AfterTree:       strings.Repeat("b", 40),
		RawACPJSONL:     raw,
		Patch:           []byte("diff --git a/file.txt b/file.txt\n"),
		Secrets: redact.Secrets{
			WorkerToken: "worker-secret",
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	wantKinds := []string{
		sharedevidence.KindReport,
		sharedevidence.KindRawACP,
		sharedevidence.KindTrace,
		sharedevidence.KindPatch,
	}
	if len(bundle.Objects) != len(wantKinds) {
		t.Fatalf("object count = %d, want %d", len(bundle.Objects), len(wantKinds))
	}
	for index, object := range bundle.Objects {
		if object.Kind != wantKinds[index] {
			t.Fatalf("object %d kind = %q, want %q", index, object.Kind, wantKinds[index])
		}
		digest := sha256.Sum256(object.Payload)
		if object.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("object %q SHA256 = %q, want digest of payload", object.Kind, object.SHA256)
		}
		if object.Bytes != int64(len(object.Payload)) {
			t.Fatalf("object %q bytes = %d, want %d", object.Kind, object.Bytes, len(object.Payload))
		}
		if bytes.Contains(object.Payload, []byte("worker-secret")) {
			t.Fatalf("object %q leaked Worker token", object.Kind)
		}
	}

	report := string(objectPayload(t, bundle, sharedevidence.KindReport))
	if !strings.Contains(report, "Final answer with [REDACTED:worker_token]. All tests passed.") {
		t.Fatalf("report does not contain accumulated final message:\n%s", report)
	}
	if !strings.Contains(report, "## Verification Appendix") ||
		!strings.Contains(report, "`go test ./...`") ||
		!strings.Contains(report, "completed") {
		t.Fatalf("report does not contain observed verification command and status:\n%s", report)
	}

	normalized := decodeJSONLLines(t, objectPayload(t, bundle, sharedevidence.KindTrace))
	if len(normalized) != 4 {
		t.Fatalf("normalized record count = %d, want 4", len(normalized))
	}
	for index, record := range normalized {
		if record["schema_version"] != sharedevidence.NormalizedTraceSchemaVersion {
			t.Fatalf("record %d schema = %v", index, record["schema_version"])
		}
		if record["sequence"] != float64(index+1) {
			t.Fatalf("record %d sequence = %v, want %d", index, record["sequence"], index+1)
		}
		if record["server_timestamp"] != serverTimestamp.Format(time.RFC3339Nano) {
			t.Fatalf("record %d server timestamp = %v", index, record["server_timestamp"])
		}
		if record["worker_timestamp"] != workerTimestamp.Format(time.RFC3339Nano) {
			t.Fatalf("record %d worker timestamp = %v", index, record["worker_timestamp"])
		}
		assertNormalizedIdentities(t, index, record["identity"])
		payload, ok := record["payload"].(map[string]any)
		if !ok {
			t.Fatalf("record %d payload type = %T, want JSON object", index, record["payload"])
		}
		if _, ok := payload["frame"].(map[string]any); !ok {
			t.Fatalf("record %d frame type = %T, want decoded JSON object", index, payload["frame"])
		}
	}
}

func TestBuildRequiresExplicitNoFilesChangedForEmptyPatch(t *testing.T) {
	t.Parallel()

	input := minimalInput(t)
	input.Patch = nil
	if _, err := Build(input); err == nil || !strings.Contains(err.Error(), "no files changed") {
		t.Fatalf("Build() error = %v, want explicit no-files-changed failure", err)
	}

	input.NoFilesChanged = true
	bundle, err := Build(input)
	if err != nil {
		t.Fatalf("Build() explicit no-files-changed error = %v", err)
	}
	report := string(objectPayload(t, bundle, sharedevidence.KindReport))
	if !strings.Contains(report, "No files changed.") {
		t.Fatalf("empty-patch report lacks explicit statement:\n%s", report)
	}
	if patch := objectPayload(t, bundle, sharedevidence.KindPatch); len(patch) != 0 {
		t.Fatalf("empty patch payload = %q", patch)
	}
}

func minimalInput(t *testing.T) Input {
	t.Helper()
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	return Input{
		Identity: sharedevidence.TurnIdentity{
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1,
			AgentSessionID:    "agent-1",
			AgentTurnID:       "turn-1",
			TraceID:           "trace-1",
		},
		ServerTimestamp: now,
		WorkerTimestamp: now,
		BeforeTree:      strings.Repeat("a", 40),
		AfterTree:       strings.Repeat("a", 40),
		RawACPJSONL: rawACPJSONL(t, rawFrame("agent_to_client", map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{
						"type": "text",
						"text": "No-op turn.",
					},
				},
			},
		})),
	}
}

func rawFrame(direction string, frame map[string]any) map[string]any {
	encoded, err := json.Marshal(frame)
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"type":      "acp.raw_frame",
		"direction": direction,
		"frame":     string(encoded) + "\n",
	}
}

func rawACPJSONL(t *testing.T, records ...map[string]any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}

func objectPayload(t *testing.T, bundle Bundle, kind string) []byte {
	t.Helper()
	for _, object := range bundle.Objects {
		if object.Kind == kind {
			return object.Payload
		}
	}
	t.Fatalf("object kind %q not found", kind)
	return nil
}

func decodeJSONLLines(t *testing.T, content []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(content))
	var records []map[string]any
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func assertNormalizedIdentities(t *testing.T, recordIndex int, value any) {
	t.Helper()
	identity, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("record %d identity type = %T", recordIndex, value)
	}
	want := map[string]any{
		"worker_session_id":   "session-1",
		"worker_run_id":       "run-1",
		"runtime_instance_id": "runtime-1",
		"runtime_generation":  float64(2),
		"agent_session_id":    "agent-1",
		"agent_turn_id":       "turn-1",
		"trace_id":            "trace-1",
	}
	for key, expected := range want {
		if identity[key] != expected {
			t.Fatalf("record %d identity[%q] = %v, want %v", recordIndex, key, identity[key], expected)
		}
	}
}
