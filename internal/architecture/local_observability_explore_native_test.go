//go:build observability_native

package architecture_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
)

// Explicit fixture opt-in: the named task-owned client must have wget and share
// the isolated Alloy/Loki network. This test never starts Backend or a provider.
// SWITCHBACK_OBSERVABILITY_LOG_CLIENT=switchback-obsv-stack-0929-prometheus-1 go test -tags=observability_native ./internal/architecture -run TestLocalObservabilityExploreNative -count=1 -v
func TestLocalObservabilityExploreNative(t *testing.T) {
	client := os.Getenv("SWITCHBACK_OBSERVABILITY_LOG_CLIENT")
	if client == "" {
		t.Skip("set SWITCHBACK_OBSERVABILITY_LOG_CLIENT for isolated Alloy/Loki fixture")
	}
	wget := func(args ...string) []byte {
		t.Helper()
		output, err := exec.Command("docker", append([]string{"exec", client, "wget"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture HTTP request: %v: %s", err, output)
		}
		return output
	}
	timestamp := time.Now().UTC().Add(-time.Minute)
	run := "run-envelope-" + strconv.FormatInt(timestamp.UnixNano(), 10)
	var values [][2]string
	for i, id := range []string{run, run + "-other"} {
		event := telemetry.Event{TS: timestamp.Add(time.Duration(i) * time.Nanosecond), Level: telemetry.LevelInfo, Service: telemetry.Service, Component: telemetry.ComponentWorker, Event: telemetry.EventRunTerminal, SchemaVersion: telemetry.SchemaVersion, Code: telemetry.CodeOK, Scope: telemetry.Scope{WorkerSessionID: "session-fixture", WorkerRunID: id, AgentTurnID: "turn-fixture"}}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, [2]string{strconv.FormatInt(event.TS.UnixNano(), 10), string(encoded)})
	}
	payload, err := json.Marshal(map[string]any{"streams": []map[string]any{{"stream": map[string]string{"service": telemetry.Service, "environment": telemetry.Environment, "component": string(telemetry.ComponentWorker), "level": string(telemetry.LevelInfo)}, "values": values}}})
	if err != nil {
		t.Fatal(err)
	}
	response := wget("-S", "-O", "-", "--header=Content-Type: application/json", "--post-data="+string(payload), "http://alloy:12345/loki/api/v1/push")
	if !strings.Contains(string(response), "204 No Content") {
		t.Fatalf("receiver did not accept: %s", response)
	}
	t.Log("Alloy accepted two real Event envelope fixtures with HTTP 204")
	for _, expr := range exploreRunQueries(t, run) {
		endpoint := "http://loki:3100/loki/api/v1/query_range?" + url.Values{"query": {expr}, "start": {strconv.FormatInt(timestamp.Add(-time.Second).UnixNano(), 10)}, "end": {strconv.FormatInt(time.Now().UnixNano(), 10)}}.Encode()
		var result struct {
			Status string
			Data   struct {
				Result []struct{ Values [][2]string }
			}
		}
		for attempt := 0; attempt < 10; attempt++ {
			if err := json.Unmarshal(wget("-q", "-O", "-", endpoint), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Data.Result) > 0 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if result.Status != "success" || len(result.Data.Result) != 1 || len(result.Data.Result[0].Values) != 1 || result.Data.Result[0].Values[0] != values[0] {
			t.Fatalf("exact Run query must return matching timestamp/envelope and exclude other Run: %+v", result)
		}
	}
	var labels struct {
		Status string
		Data   []string
	}
	if err := json.Unmarshal(wget("-q", "-O", "-", "http://loki:3100/loki/api/v1/labels"), &labels); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(labels.Data) != "[component environment level service]" {
		t.Fatalf("unexpected indexed labels: %v", labels.Data)
	}
	t.Log("all four delivered Explore queries matched the typed Event and excluded the other Run; four indexed labels only")
}
