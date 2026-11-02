package telemetry

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestRuntimeSourceStatesHaveFiniteLabels(t *testing.T) {
	for _, status := range []Status{"starting", "active", "suspect", "unhealthy", "draining", "terminated", "lost", "unknown"} {
		if err := NewMetrics().Replace(Snapshot{SampledAt: time.Now(), Counts: []CountMeasurement{{Family: FamilyRuntimeState, Status: status, Harness: HarnessUnknown, Value: 1}}}); err != nil {
			t.Fatalf("state %s: %v", status, err)
		}
	}
	if err := NewMetrics().Replace(Snapshot{SampledAt: time.Now(), Counts: []CountMeasurement{{Family: FamilyRuntimeState, Status: "arbitrary-runtime-id", Harness: HarnessUnknown, Value: 1}}}); err == nil {
		t.Fatal("unbounded runtime status accepted")
	}
}

func metricValue(t *testing.T, m *Metrics, name string, wantLabels map[string]string) float64 {
	t.Helper()
	parser := expfmt.NewTextParser(model.NameValidationScheme)
	families, err := parser.TextToMetricFamilies(strings.NewReader(scrape(t, m)))
	if err != nil {
		t.Fatal(err)
	}
	family := families[name]
	if family == nil {
		t.Fatalf("missing metric family %s", name)
	}
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, pair := range metric.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}
		if labels["service"] != Service || labels["environment"] != Environment {
			t.Fatalf("missing global labels on %s: %v", name, labels)
		}
		match := true
		for key, value := range wantLabels {
			if labels[key] != value {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if metric.Gauge != nil {
			return metric.GetGauge().GetValue()
		}
		if metric.Counter != nil {
			return metric.GetCounter().GetValue()
		}
		t.Fatalf("metric %s has unsupported type", name)
	}
	t.Fatalf("missing series %s %v", name, wantLabels)
	return 0
}

func TestScrapeBeforeSampling(t *testing.T) {
	m := NewMetrics()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK || metricValue(t, m, "switchback_telemetry_sample_available", nil) != 0 {
		t.Fatal(w.Body.String())
	}
}

func TestEveryMetricCarriesGlobalLabels(t *testing.T) {
	m := NewMetrics()
	parser := expfmt.NewTextParser(model.NameValidationScheme)
	families, err := parser.TextToMetricFamilies(strings.NewReader(scrape(t, m)))
	if err != nil {
		t.Fatal(err)
	}
	for name, family := range families {
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			if labels["service"] != Service || labels["environment"] != Environment {
				t.Fatalf("%s lacks global labels: %v", name, labels)
			}
		}
	}
}

func TestReplaceOmitsMissingAndDoesNotRecount(t *testing.T) {
	m := NewMetrics()
	s := Snapshot{SampledAt: time.Unix(100, 0), Counts: []CountMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Value: 3}}}
	if err := m.Replace(s); err != nil {
		t.Fatal(err)
	}
	if err := m.Replace(s); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	if !strings.Contains(body, `switchback_worker_run_terminal{environment="local",harness="codex",service="switchback-backend",status="completed"} 3`) {
		t.Fatal(body)
	}
	if strings.Contains(body, "switchback_agent_turn_terminal") {
		t.Fatal(body)
	}
	if strings.Contains(body, "switchback_model_ttft") || strings.Contains(body, "switchback_model_cost") {
		t.Fatal(body)
	}
	if !strings.Contains(body, `switchback_model_measurement_available{environment="local",measurement="ttft",service="switchback-backend"} 0`) {
		t.Fatal(body)
	}
}

func TestFailedSampleKeepsSnapshot(t *testing.T) {
	m := NewMetrics()
	if err := m.Replace(Snapshot{SampledAt: time.Unix(100, 0), Counts: []CountMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Value: 3}}}); err != nil {
		t.Fatal(err)
	}
	m.SampleFailed()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metricValue(t, m, "switchback_telemetry_sample_failures_total", nil) != 1 || metricValue(t, m, "switchback_worker_run_terminal", map[string]string{"status": "completed", "harness": "codex"}) != 3 {
		t.Fatal(w.Body.String())
	}
	if metricValue(t, m, "switchback_telemetry_sample_current", nil) != 0 {
		t.Fatal(w.Body.String())
	}
}

func TestRejectsInvalidAndDuplicateSnapshot(t *testing.T) {
	m := NewMetrics()
	base := Snapshot{SampledAt: time.Now(), Counts: []CountMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Value: 1}}}
	bad := base
	bad.Counts = []CountMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Value: math.NaN()}}
	if m.Replace(bad) == nil {
		t.Fatal("accepted NaN")
	}
	bad = base
	bad.Counts = append(append([]CountMeasurement{}, base.Counts...), base.Counts[0])
	if m.Replace(bad) == nil {
		t.Fatal("accepted duplicate")
	}
	bad = base
	bad.Counts = []CountMeasurement{{Family: Family("user-defined"), Status: StatusCompleted, Harness: HarnessCodex, Value: 1}}
	if m.Replace(bad) == nil {
		t.Fatal("accepted dynamic family")
	}
	if strings.Contains(scrape(t, m), "switchback_worker_run_terminal{") {
		t.Fatal("invalid sample published")
	}
}

func TestConcurrentScrapeAndReplace(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if err := m.Replace(Snapshot{SampledAt: time.Now(), Counts: []CountMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Value: float64(j)}}}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				w := httptest.NewRecorder()
				m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if w.Code != http.StatusOK {
					t.Errorf("scrape: %d", w.Code)
				}
			}
		}()
	}
	wg.Wait()
}

func TestDurationHistogramValidationAndMissingOmission(t *testing.T) {
	m := NewMetrics()
	valid := Snapshot{SampledAt: time.Now(), Durations: []DurationMeasurement{{Family: FamilyWorkerRunTerminal, Status: StatusCompleted, Harness: HarnessCodex, Count: 2, SumSeconds: 2.1, Buckets: [11]uint64{0, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2}}}}
	if err := m.Replace(valid); err != nil {
		t.Fatal(err)
	}
	body := scrape(t, m)
	if !strings.Contains(body, `switchback_worker_run_duration_seconds_count{environment="local",harness="codex",service="switchback-backend",status="completed"} 2`) {
		t.Fatal(body)
	}
	bad := valid
	bad.Durations = append([]DurationMeasurement{}, valid.Durations...)
	bad.Durations[0].Buckets[2] = 0
	if m.Replace(bad) == nil {
		t.Fatal("accepted noncumulative histogram")
	}
	if err := m.Replace(Snapshot{SampledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(scrape(t, m), "switchback_worker_run_duration_seconds_count{") {
		t.Fatal("missing duration retained")
	}
}
