package langfuse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

func compilerFragment(kind artifactdomain.FragmentKind) artifactdomain.ObservationFragment {
	start := time.Date(2026, 9, 13, 8, 0, 0, 123456789, time.FixedZone("offset", 8*60*60))
	end := start.Add(time.Second)
	f := artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: kind, Scope: artifactdomain.ObservationScope{TenantID: "1", UserID: "true", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run"}, StartedAt: start, InputJSON: "{ \"n\":9007199254740993 }"}
	if kind == artifactdomain.FragmentRunBasis {
		f.InitialTurnID = "turn"
		return f
	}
	f.Turn = &artifactdomain.TurnIdentity{TenantID: "1", UserID: "true", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: "turn", RuntimeInstanceID: "runtime", RuntimeGeneration: 1}
	f.EndedAt, f.OutputJSON = &end, `{ "n": 9007199254740994 }`
	if kind == artifactdomain.FragmentModel {
		f.ObservationID, f.Model, f.Route, f.Status, f.InputTokens, f.OutputTokens = "obs", "model", "route", "ok", 2, 3
	}
	return f
}

func TestFragmentCompilerDeterministicSingleEvent(t *testing.T) {
	ids := map[artifactdomain.FragmentKind]string{}
	golden := map[artifactdomain.FragmentKind][2]string{
		artifactdomain.FragmentRunBasis:  {"368b8724f6cf58ebd906adfa9a1e082ca93406e9ca399783132e0a1abb836218", "c4c0b747ba8f2668deeaf60ef35aa8fb44cc6a4de113b6de7a8e16241a3d3147"},
		artifactdomain.FragmentTurnBasis: {"23ea5c4cc47788971d5dabb425d9351e6b2530922cc45b2d67dbd7b5949bc1d8", "7722106c1129f5c199e1f46ffc1f0e4a53d7bd527583281fa6fa21a2c3320d7d"},
		artifactdomain.FragmentModel:     {"382f3f0a7f40427e150143d5d7bf10cd293c42b23e270f07effa58211f40b253", "fc6d4bc8f5fd6205ac6a48a7abdf66535ba19059054f85c1cc8dc33037ee837d"},
	}
	for _, kind := range []artifactdomain.FragmentKind{artifactdomain.FragmentRunBasis, artifactdomain.FragmentTurnBasis, artifactdomain.FragmentModel} {
		f := compilerFragment(kind)
		frame, err := artifactdomain.EncodeObservationFragment(f)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := (FragmentCompiler{}).CompileObservationFragment(context.Background(), frame)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(frame)) != golden[kind][0] || fmt.Sprintf("%x", sha256.Sum256(wire)) != golden[kind][1] {
			t.Fatalf("%s frame/wire golden changed", kind)
		}
		repeat, err := (FragmentCompiler{}).CompileObservationFragment(context.Background(), frame)
		if err != nil || !bytes.Equal(wire, repeat) || sha256.Sum256(wire) != sha256.Sum256(repeat) {
			t.Fatal("wire is nondeterministic")
		}
		var batch IngestionBatch
		if err := json.Unmarshal(wire, &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Batch) != 1 {
			t.Fatal("fragment must emit exactly one event")
		}
		event := batch.Batch[0]
		body := event.Body
		ids[kind] = body["id"].(string)
		if event.Timestamp.Format(time.RFC3339Nano) != "2026-09-13T00:00:00.123Z" {
			t.Fatal("wire time", event.Timestamp)
		}
		metadata := body["metadata"].(map[string]any)
		if metadata["tenant_id"] != `"1"` || metadata["user_id"] != `"true"` || metadata["source_started_at"] != `"2026-09-13T00:00:00.123456789Z"` {
			t.Fatal("metadata type/time lost", metadata)
		}
		input := body["input"].(map[string]any)
		if input["json"] != f.InputJSON || input["encoding"] != "json-text.v1" {
			t.Fatal("I/O text lost")
		}
		if kind == artifactdomain.FragmentRunBasis {
			if _, ok := body["output"]; ok {
				t.Fatal("root has output")
			}
			if len(metadata) != 11 {
				t.Fatal("root metadata whitelist", metadata)
			}
		} else {
			wantKeys := 15
			if kind == artifactdomain.FragmentModel {
				wantKeys = 18
			}
			if len(metadata) != wantKeys {
				t.Fatal("metadata whitelist", metadata)
			}
			if body["traceId"] != ids[artifactdomain.FragmentRunBasis] {
				t.Fatal("wrong trace parent")
			}
			if body["output"].(map[string]any)["json"] != f.OutputJSON {
				t.Fatal("output text lost")
			}
		}
		if kind == artifactdomain.FragmentModel {
			if body["parentObservationId"] != ids[artifactdomain.FragmentTurnBasis] {
				t.Fatal("wrong turn parent")
			}
			usage := body["usageDetails"].(map[string]any)
			if usage["input"] != float64(2) || usage["output"] != float64(3) || usage["total"] != float64(5) {
				t.Fatal("usage", usage)
			}
			if _, ok := body["usage"]; ok {
				t.Fatal("usage is duplicated")
			}
		}
	}
}

func TestFragmentCompilerRejectsInvalidAndCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _ := artifactdomain.EncodeObservationFragment(compilerFragment(artifactdomain.FragmentModel))
	for _, tc := range []struct {
		ctx   context.Context
		frame []byte
	}{{context.Background(), []byte(`{}`)}, {ctx, f}} {
		wire, err := (FragmentCompiler{}).CompileObservationFragment(tc.ctx, tc.frame)
		if err == nil || len(wire) != 0 {
			t.Fatal("content returned on failure")
		}
	}
}
