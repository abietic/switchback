package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLangfuseVerifierPollsUntilExactProjectionIsVisible(t *testing.T) {
	t.Parallel()

	var (
		requestMu sync.Mutex
		requests  int
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestMu.Lock()
		requests++
		attempt := requests
		requestMu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch attempt {
		case 1:
			http.Error(response, "not found", http.StatusNotFound)
		case 2:
			_, _ = response.Write([]byte(`{"id":"trace-1"}`))
		case 3:
			_, _ = response.Write([]byte(`{
				"id":"trace-1",
				"observations":[
					{"id":"turn-1","traceId":"trace-1","parentObservationId":null,"name":"agent.turn","type":"SPAN"},
					{"id":"generation-1","traceId":"trace-1","parentObservationId":"turn-1","name":"model.generation","type":"GENERATION"}
				]
			}`))
		default:
			_, _ = response.Write([]byte(`{
				"id":"trace-1",
				"metadata":{
					"worker_session_id":"session-1",
					"worker_run_id":"run-1",
					"runtime_instance_id":"runtime-1",
					"runtime_generation":1,
					"agent_session_id":"agent-session-1",
					"agent_turn_ids":["turn-1","turn-2"]
				},
				"observations":[
					{"id":"turn-1","traceId":"trace-1","parentObservationId":null,"name":"agent.turn","type":"SPAN","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-1"}},
					{"id":"generation-1","traceId":"trace-1","parentObservationId":"turn-1","name":"model.generation","type":"GENERATION","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-1"}},
					{"id":"turn-2","traceId":"trace-1","parentObservationId":null,"name":"agent.turn","type":"SPAN","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-2"}},
					{"id":"generation-2","traceId":"trace-1","parentObservationId":"turn-2","name":"model.generation","type":"GENERATION","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-2"}}
				]
			}`))
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:   server.URL,
		PublicKey: "pk-verifier",
		SecretKey: "sk-verifier",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	clock := &advancingClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	verifier, err := NewVerifier(client, VerifierOptions{Clock: clock})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	expected := ExpectedProjection{
		Metadata: RuntimeMetadata{
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1,
			AgentSessionID:    "agent-session-1",
			AgentTurnIDs:      []string{"turn-1", "turn-2"},
		},
		TurnIDs: []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: "turn-1"},
			{ID: "generation-2", ParentTurnID: "turn-2"},
		},
	}
	if err := verifier.Verify(context.Background(), "trace-1", expected); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	requestMu.Lock()
	gotRequests := requests
	requestMu.Unlock()
	if gotRequests != 4 {
		t.Fatalf("trace queries = %d, want 4", gotRequests)
	}
	if len(clock.waits) != 3 {
		t.Fatalf("poll waits = %v, want three waits", clock.waits)
	}
	for index, wait := range clock.waits {
		if wait != time.Second {
			t.Errorf("poll wait %d = %s, want 1s", index, wait)
		}
	}
}

func TestLangfuseVerifierRequiresExpectedGenerationForEveryTurn(t *testing.T) {
	t.Parallel()

	reader := &staticTraceReader{trace: Trace{Observations: []Observation{
		{ID: "turn-1", Name: "agent.turn", Type: "SPAN"},
		{ID: "turn-2", Name: "agent.turn", Type: "SPAN"},
	}}}
	clock := &advancingClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	verifier, err := NewVerifier(reader, VerifierOptions{
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	err = verifier.Verify(context.Background(), "trace-1", ExpectedProjection{
		Metadata: runtimeMetadataFixture("turn-1", "turn-2"),
		TurnIDs:  []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: "turn-1"},
		},
	})
	if err == nil {
		t.Fatal("Verify() error = nil, want missing turn generation rejection")
	}
	if !strings.Contains(err.Error(), "at least one expected generation per AgentTurn") {
		t.Fatalf("Verify() error = %q, want expected-generation validation", err)
	}
	if reader.calls != 0 {
		t.Fatalf("trace queries = %d, want validation before query", reader.calls)
	}
}

func TestLangfuseAcceptanceTimesOutAt90SecondsWithBoundedLastSummary(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-timeout-must-not-leak"
		secretKey = "sk-timeout-must-not-leak"
	)
	var (
		requestMu sync.Mutex
		requests  int
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestMu.Lock()
		requests++
		requestMu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(response, `{
			"id":"trace-1",
			"observations":[{
				"id":"unrelated",
				"traceId":"trace-1",
				"parentObservationId":null,
				"name":"unrelated",
				"type":"EVENT",
				"metadata":{"Authorization":"Basic raw","public":"%s","secret":"%s"}
			}]
		}`, publicKey, secretKey)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:   server.URL,
		PublicKey: publicKey,
		SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	start := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: start}
	verifier, err := NewVerifier(client, VerifierOptions{Clock: clock})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	err = verifier.Verify(context.Background(), "trace-1", ExpectedProjection{
		Metadata: runtimeMetadataFixture("turn-1", "turn-2"),
		TurnIDs:  []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: "turn-1"},
			{ID: "generation-2", ParentTurnID: "turn-2"},
		},
	})
	var timeoutError *VerificationTimeoutError
	if !errors.As(err, &timeoutError) {
		t.Fatalf("Verify() error = %v, want VerificationTimeoutError", err)
	}
	if clock.now.Sub(start) != 90*time.Second {
		t.Fatalf("verification elapsed = %s, want 90s", clock.now.Sub(start))
	}
	for index, wait := range clock.waits {
		if wait != time.Second {
			t.Errorf("poll wait %d = %s, want 1s", index, wait)
		}
	}
	requestMu.Lock()
	gotRequests := requests
	requestMu.Unlock()
	if gotRequests != 90 {
		t.Errorf("trace queries = %d, want 90 bounded attempts", gotRequests)
	}
	if len(timeoutError.LastSummary) == 0 || len(timeoutError.LastSummary) > 256 {
		t.Errorf("last summary length = %d, want 1..256", len(timeoutError.LastSummary))
	}
	for _, forbidden := range []string{publicKey, secretKey, "Authorization", "Basic raw"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("timeout error contains forbidden value %q: %q", forbidden, err)
		}
	}
}

func TestLangfuseVerifierRejectsMismatchedTraceIdentity(t *testing.T) {
	t.Parallel()

	parentOne := "turn-1"
	parentTwo := "turn-2"
	reader := &staticTraceReader{trace: Trace{
		ID:       "other-trace",
		Metadata: runtimeMetadataFixture(parentOne, parentTwo),
		Observations: []Observation{
			{ID: "turn-1", TraceID: "other-trace", Name: "agent.turn", Type: "SPAN"},
			{ID: "turn-2", TraceID: "other-trace", Name: "agent.turn", Type: "SPAN"},
			{ID: "generation-1", TraceID: "other-trace", ParentObservationID: &parentOne, Type: "GENERATION"},
			{ID: "generation-2", TraceID: "other-trace", ParentObservationID: &parentTwo, Type: "GENERATION"},
		},
	}}
	clock := &advancingClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	verifier, err := NewVerifier(reader, VerifierOptions{
		Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = verifier.Verify(context.Background(), "trace-1", ExpectedProjection{
		Metadata: runtimeMetadataFixture("turn-1", "turn-2"),
		TurnIDs:  []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: "turn-1"},
			{ID: "generation-2", ParentTurnID: "turn-2"},
		},
	})
	var timeoutError *VerificationTimeoutError
	if !errors.As(err, &timeoutError) {
		t.Fatalf("Verify() error = %v, want mismatched identity to remain unaccepted", err)
	}
	if !strings.Contains(timeoutError.LastSummary, "trace_identity_match=false") {
		t.Fatalf("last summary = %q, want identity mismatch", timeoutError.LastSummary)
	}
}

func TestProjectionMatchesRejectsTraceRuntimeMetadataMismatch(t *testing.T) {
	t.Parallel()

	parentOne := "turn-1"
	parentTwo := "turn-2"
	metadata := RuntimeMetadata{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		AgentTurnIDs:      []string{parentOne, parentTwo},
	}
	expected := ExpectedProjection{
		Metadata: metadata,
		TurnIDs:  []string{parentOne, parentTwo},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: parentOne},
			{ID: "generation-2", ParentTurnID: parentTwo},
		},
	}
	trace := Trace{
		ID:       "trace-1",
		Metadata: metadata,
		Observations: []Observation{
			{ID: parentOne, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN", Metadata: observationMetadataFixture(metadata, parentOne)},
			{ID: parentTwo, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN", Metadata: observationMetadataFixture(metadata, parentTwo)},
			{ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION", Metadata: observationMetadataFixture(metadata, parentOne)},
			{ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentTwo, Type: "GENERATION", Metadata: observationMetadataFixture(metadata, parentTwo)},
		},
	}
	if matched, summary := projectionMatches(trace, "trace-1", expected); !matched {
		t.Fatalf("projectionMatches() rejected exact metadata: %s", summary)
	}
	trace.Metadata.WorkerRunID = "other-run"
	if matched, summary := projectionMatches(trace, "trace-1", expected); matched {
		t.Fatalf("projectionMatches() accepted mismatched metadata: %s", summary)
	} else if !strings.Contains(summary, "runtime_metadata_match=false") {
		t.Fatalf("projection summary = %q, want metadata mismatch", summary)
	}
}

func TestProjectionMatchesRejectsObservationRuntimeMetadataMismatch(t *testing.T) {
	t.Parallel()

	parentOne := "turn-1"
	parentTwo := "turn-2"
	metadata := runtimeMetadataFixture(parentOne, parentTwo)
	expected := ExpectedProjection{
		Metadata: metadata,
		TurnIDs:  []string{parentOne, parentTwo},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: parentOne},
			{ID: "generation-2", ParentTurnID: parentTwo},
		},
	}
	trace := Trace{
		ID:       "trace-1",
		Metadata: metadata,
		Observations: []Observation{
			{ID: parentOne, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN", Metadata: observationMetadataFixture(metadata, parentOne)},
			{ID: parentTwo, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN", Metadata: observationMetadataFixture(metadata, parentTwo)},
			{ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION", Metadata: observationMetadataFixture(metadata, parentOne)},
			{ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentTwo, Type: "GENERATION", Metadata: observationMetadataFixture(metadata, parentTwo)},
		},
	}
	if matched, summary := projectionMatches(trace, "trace-1", expected); !matched {
		t.Fatalf("projectionMatches() rejected exact observation metadata: %s", summary)
	}
	trace.Observations[2].Metadata["worker_run_id"] = "other-run"
	if matched, summary := projectionMatches(trace, "trace-1", expected); matched {
		t.Fatalf("projectionMatches() accepted mismatched observation metadata: %s", summary)
	}
}

func TestProjectionMatchesRejectsAnyGenerationIdentityMismatch(t *testing.T) {
	t.Parallel()

	parentOne := "turn-1"
	parentTwo := "turn-2"
	expected := ExpectedProjection{
		Metadata: runtimeMetadataFixture(parentOne, parentTwo),
		TurnIDs:  []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: parentOne},
			{ID: "generation-2", ParentTurnID: parentTwo},
		},
	}
	base := []Observation{
		{ID: parentOne, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN"},
		{ID: parentTwo, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN"},
		{ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION"},
		{ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentTwo, Type: "GENERATION"},
	}
	tests := []struct {
		name         string
		observations []Observation
	}{
		{
			name: "extra",
			observations: append(append([]Observation(nil), base...), Observation{
				ID: "generation-extra", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{
			name: "duplicate",
			observations: append(append([]Observation(nil), base...), Observation{
				ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{name: "missing", observations: base[:len(base)-1]},
		{
			name: "wrong parent",
			observations: append(append([]Observation(nil), base[:len(base)-1]...), Observation{
				ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{
			name: "wrong trace",
			observations: append(append([]Observation(nil), base[:len(base)-1]...), Observation{
				ID: "generation-2", TraceID: "other-trace", ParentObservationID: &parentTwo, Type: "GENERATION",
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, summary := projectionMatches(Trace{
				ID:           "trace-1",
				Metadata:     runtimeMetadataFixture(parentOne, parentTwo),
				Observations: test.observations,
			}, "trace-1", expected)
			if matched {
				t.Fatalf("projectionMatches() matched %s generation set; summary=%s", test.name, summary)
			}
		})
	}
}

func TestLangfuseVerifierExposesNoCallerTimingOverrides(t *testing.T) {
	t.Parallel()

	optionsType := reflect.TypeFor[VerifierOptions]()
	for _, field := range []string{"PollInterval", "Deadline"} {
		if _, found := optionsType.FieldByName(field); found {
			t.Fatalf("VerifierOptions exposes caller-controlled %s", field)
		}
	}
	verifier, err := NewVerifier(&staticTraceReader{}, VerifierOptions{
		Clock: &advancingClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	if verifier.pollInterval != time.Second || verifier.deadline != 90*time.Second {
		t.Fatalf("verifier timing = %s/%s, want 1s/90s", verifier.pollInterval, verifier.deadline)
	}
}

func TestVerifyLangfuseScriptRejectsAnyGenerationIdentityMismatch(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by verify-langfuse")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required by verify-langfuse")
	}

	binDir := t.TempDir()
	sleepPath := filepath.Join(binDir, "sleep")
	if err := os.WriteFile(sleepPath, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	script := filepath.Clean(filepath.Join(filepath.Dir(testFile), "../../../scripts/verify-langfuse"))
	parentOne := "turn-1"
	parentTwo := "turn-2"
	base := []Observation{
		{ID: parentOne, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN"},
		{ID: parentTwo, TraceID: "trace-1", Name: "agent.turn", Type: "SPAN"},
		{ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION"},
		{ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentTwo, Type: "GENERATION"},
	}
	tests := []struct {
		name         string
		observations []Observation
	}{
		{
			name: "extra",
			observations: append(append([]Observation(nil), base...), Observation{
				ID: "generation-extra", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{
			name: "duplicate",
			observations: append(append([]Observation(nil), base...), Observation{
				ID: "generation-1", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{name: "missing", observations: base[:len(base)-1]},
		{
			name: "wrong parent",
			observations: append(append([]Observation(nil), base[:len(base)-1]...), Observation{
				ID: "generation-2", TraceID: "trace-1", ParentObservationID: &parentOne, Type: "GENERATION",
			}),
		},
		{
			name: "wrong trace",
			observations: append(append([]Observation(nil), base[:len(base)-1]...), Observation{
				ID: "generation-2", TraceID: "other-trace", ParentObservationID: &parentTwo, Type: "GENERATION",
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(response).Encode(Trace{ID: "trace-1", Observations: test.observations}); err != nil {
					t.Errorf("encode trace response: %v", err)
				}
			}))
			defer server.Close()

			command := exec.Command(script,
				"trace-1", "turn-1", "turn-2",
				"generation-1", "turn-1",
				"generation-2", "turn-2",
			)
			command.Env = append(environmentWithout("PATH"),
				"LANGFUSE_BASE_URL="+server.URL,
				"LANGFUSE_PUBLIC_KEY=pk_test",
				"LANGFUSE_SECRET_KEY=sk_test",
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("verify-langfuse accepted %s generation set: %s", test.name, output)
			}
		})
	}
}

func TestVerifyLangfuseScriptDoesNotSleepPastDeadline(t *testing.T) {
	binDir := t.TempDir()
	probePath := filepath.Join(t.TempDir(), "sleep-probe")
	bootstrapProbePath := filepath.Join(t.TempDir(), "bash-env-probe")
	clockCounterPath := filepath.Join(t.TempDir(), "clock-counter")
	if err := os.WriteFile(clockCounterPath, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), "#!/bin/sh\nprintf '404'\n")
	writeExecutable(t, filepath.Join(binDir, "perl"), "#!/bin/sh\ncount=$(cat \"$LANGFUSE_CLOCK_COUNTER\")\ncase \"$count\" in\n0) printf '0.000000000\\n'; printf '1\\n' >\"$LANGFUSE_CLOCK_COUNTER\" ;;\n1) printf '0.000000000\\n'; printf '2\\n' >\"$LANGFUSE_CLOCK_COUNTER\" ;;\n*) printf '90.000000000\\n'; printf '3\\n' >\"$LANGFUSE_CLOCK_COUNTER\" ;;\nesac\n")
	writeExecutable(t, filepath.Join(binDir, "sleep"), "#!/bin/sh\nprintf 'fallback duration=%s\\n' \"$1\" >\"$LANGFUSE_SLEEP_PROBE\"\nexit 17\n")
	bootstrapPath := filepath.Join(t.TempDir(), "bash-env")
	bootstrap := "printf 'ready\\n' >\"$LANGFUSE_BASH_ENV_PROBE\"\n" +
		"sleep() { printf 'SECONDS=%s duration=%s\\n' \"$SECONDS\" \"$1\" >\"$LANGFUSE_SLEEP_PROBE\"; return 0; }\n" +
		"trap 'case \"$BASH_COMMAND\" in sleep\\ *) SECONDS=$deadline ;; esac' DEBUG\n"
	if err := os.WriteFile(bootstrapPath, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	script := filepath.Clean(filepath.Join(filepath.Dir(testFile), "../../../scripts/verify-langfuse"))
	command := exec.Command(script,
		"trace-1", "turn-1", "turn-2",
		"generation-1", "turn-1",
		"generation-2", "turn-2",
	)
	command.Env = append(environmentWithout(
		"PATH", "BASH_ENV", "LANGFUSE_BASE_URL", "LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
	),
		"BASH_ENV="+bootstrapPath,
		"LANGFUSE_BASE_URL=http://127.0.0.1:1",
		"LANGFUSE_PUBLIC_KEY=pk_test",
		"LANGFUSE_SECRET_KEY=sk_test",
		"LANGFUSE_SLEEP_PROBE="+probePath,
		"LANGFUSE_BASH_ENV_PROBE="+bootstrapProbePath,
		"LANGFUSE_CLOCK_COUNTER="+clockCounterPath,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	output, err := command.CombinedOutput()
	if bootstrap, readErr := os.ReadFile(bootstrapProbePath); readErr != nil {
		t.Fatalf("BASH_ENV was not loaded: %v", readErr)
	} else if strings.TrimSpace(string(bootstrap)) != "ready" {
		t.Fatalf("BASH_ENV probe = %q, want ready", bootstrap)
	}
	if sleepDuration, readErr := os.ReadFile(probePath); readErr == nil {
		t.Fatalf("verify-langfuse slept %q after its deadline", strings.TrimSpace(string(sleepDuration)))
	} else if !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if err == nil {
		t.Fatalf("verify-langfuse succeeded after the deadline: %s", output)
	}
	if !strings.Contains(string(output), "Langfuse verification timed out after 90s") {
		t.Fatalf("verify-langfuse output = %q, want deadline timeout", output)
	}
	clockCalls, readErr := os.ReadFile(clockCounterPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(clockCalls)) != "3" {
		t.Fatalf("monotonic clock calls = %q, want deadline check after the query", clockCalls)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func environmentWithout(keys ...string) []string {
	prefixes := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		prefixes[key+"="] = struct{}{}
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		include := true
		for prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				include = false
				break
			}
		}
		if include {
			environment = append(environment, entry)
		}
	}
	return environment
}

func TestLangfuseAcceptanceDeadlineFencesOneBlockedQuery(t *testing.T) {
	t.Parallel()

	verifier, err := NewVerifier(blockingTraceReader{}, VerifierOptions{Clock: immediateDeadlineClock{}})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = verifier.Verify(parent, "trace-1", ExpectedProjection{
		Metadata: runtimeMetadataFixture("turn-1", "turn-2"),
		TurnIDs:  []string{"turn-1", "turn-2"},
		Generations: []ExpectedGeneration{
			{ID: "generation-1", ParentTurnID: "turn-1"},
			{ID: "generation-2", ParentTurnID: "turn-2"},
		},
	})
	elapsed := time.Since(started)
	var timeoutError *VerificationTimeoutError
	if !errors.As(err, &timeoutError) {
		t.Fatalf("Verify() error = %v, want bounded VerificationTimeoutError", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("blocked query elapsed = %s, want verifier deadline before parent deadline", elapsed)
	}
	if timeoutError.LastSummary != "no response observed" {
		t.Fatalf("last summary = %q", timeoutError.LastSummary)
	}
}

type advancingClock struct {
	now   time.Time
	waits []time.Duration
}

func runtimeMetadataFixture(turnIDs ...string) RuntimeMetadata {
	return RuntimeMetadata{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		AgentTurnIDs:      append([]string(nil), turnIDs...),
	}
}

func observationMetadataFixture(metadata RuntimeMetadata, turnID string) map[string]any {
	return map[string]any{
		"worker_session_id":   metadata.WorkerSessionID,
		"worker_run_id":       metadata.WorkerRunID,
		"runtime_instance_id": metadata.RuntimeInstanceID,
		"runtime_generation":  metadata.RuntimeGeneration,
		"agent_session_id":    metadata.AgentSessionID,
		"agent_turn_ids":      append([]string(nil), metadata.AgentTurnIDs...),
		"agent_turn_id":       turnID,
	}
}

func (c *advancingClock) Now() time.Time {
	return c.now
}

func (c *advancingClock) Wait(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		c.waits = append(c.waits, duration)
		c.now = c.now.Add(duration)
		return nil
	}
}

func (c *advancingClock) WithTimeout(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

func (c *advancingClock) String() string {
	return fmt.Sprintf("advancingClock{now:%s waits:%v}", c.now, c.waits)
}

type staticTraceReader struct {
	trace Trace
	err   error
	calls int
}

func (r *staticTraceReader) GetTrace(context.Context, string) (Trace, error) {
	r.calls++
	return r.trace, r.err
}

type blockingTraceReader struct{}

func (blockingTraceReader) GetTrace(ctx context.Context, _ string) (Trace, error) {
	<-ctx.Done()
	return Trace{}, ctx.Err()
}

type immediateDeadlineClock struct{}

func (immediateDeadlineClock) Now() time.Time {
	return time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
}

func (immediateDeadlineClock) Wait(context.Context, time.Duration) error {
	return nil
}

func (immediateDeadlineClock) WithTimeout(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 0)
}
