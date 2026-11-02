//go:build observability_fixture

package architecture_test

import (
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
)

// Executed only inside the owned container by the cross-compiled test binary.
func TestMonitoringRegistryFixture(t *testing.T) {
	if os.Getenv("SWITCHBACK_MONITORING_REGISTRY_HELPER") != "1" {
		t.Skip("container fixture helper")
	}
	metrics := telemetry.NewMetrics()
	var failed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/sample-failed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		failed.Store(true)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		s := telemetry.Snapshot{SampledAt: time.Now(), Counts: []telemetry.CountMeasurement{
			{Family: telemetry.FamilyWorkerRunTerminal, Status: telemetry.StatusCompleted, Harness: telemetry.HarnessCodex, Value: 2},
			{Family: telemetry.FamilyAgentTurnTerminal, Status: telemetry.StatusCompleted, Harness: telemetry.HarnessCodex, Value: 2},
			{Family: telemetry.FamilySessionState, Status: telemetry.StatusIdle, Harness: telemetry.HarnessUnknown, Value: 1},
			{Family: telemetry.FamilyRuntimeState, Status: telemetry.StatusHealthy, Harness: telemetry.HarnessUnknown, Value: 1},
			{Family: telemetry.FamilyQueueState, Status: telemetry.StatusQueued, Harness: telemetry.HarnessUnknown, Value: 0},
			{Family: telemetry.FamilyCandidateProjection, Status: telemetry.StatusProjected, Harness: telemetry.HarnessUnknown, Value: 2},
			{Family: telemetry.FamilyFragmentProjection, Status: telemetry.StatusProjected, Harness: telemetry.HarnessUnknown, Value: 2},
		}, OldestPending: []telemetry.AgeMeasurement{{Family: telemetry.FamilyCandidateProjection, Seconds: 0}, {Family: telemetry.FamilyFragmentProjection, Seconds: 0}}}
		for _, family := range []telemetry.Family{telemetry.FamilyWorkerRunTerminal, telemetry.FamilyAgentTurnTerminal} {
			s.Durations = append(s.Durations, telemetry.DurationMeasurement{Family: family, Status: telemetry.StatusCompleted, Harness: telemetry.HarnessCodex, Count: 2, SumSeconds: 2, Buckets: [11]uint64{0, 0, 2, 2, 2, 2, 2, 2, 2, 2, 2}})
		}
		if failed.Load() {
			metrics.SampleFailed()
		} else if err := metrics.Replace(s); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		metrics.Handler().ServeHTTP(w, r)
	})
	server := http.Server{Addr: ":9091", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Fatal(server.ListenAndServe())
}
