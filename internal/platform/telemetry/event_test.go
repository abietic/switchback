package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRuntimeOutputMetadataIsFiniteAndPayloadFree(t *testing.T) {
	e := Event{TS: time.Now(), Level: LevelInfo, Service: Service, Component: ComponentWorker, Event: EventRuntimeOutputSuppressed, SchemaVersion: SchemaVersion,
		Scope: Scope{WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 2}, OutputSource: OutputStdout}
	for _, source := range []OutputSource{OutputStdout, OutputStderr, OutputCombined} {
		e.OutputSource = source
		if !e.valid() {
			t.Fatalf("valid source rejected: %s", source)
		}
	}
	e.OutputSource = OutputSource("credential=sentinel")
	if e.valid() {
		t.Fatal("arbitrary output source accepted")
	}
	e.OutputSource = OutputStdout
	e.Scope.RuntimeInstanceID = "bad identity\n"
	if e.valid() {
		t.Fatal("invalid runtime ID accepted")
	}
	e.Scope.RuntimeInstanceID = "runtime-1"
	e.Scope.RuntimeGeneration = 0
	if e.valid() {
		t.Fatal("runtime identity without generation accepted")
	}
	e.Scope.RuntimeGeneration = 2
	e.Scope.WorkerRunID = "run-current"
	if e.valid() {
		t.Fatal("arbitrary container output falsely attributed to Run")
	}
	e.Scope.WorkerRunID = ""
	b, err := json.Marshal(e)
	if err != nil || strings.Contains(string(b), "message") || strings.Contains(string(b), "payload") {
		t.Fatalf("unsafe envelope: %s", b)
	}
}
