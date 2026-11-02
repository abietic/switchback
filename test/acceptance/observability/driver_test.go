package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestClaimRejectsDefaultsAndExistingOutput(t *testing.T) {
	for _, project := range []string{"", "switchback-m1", "switchback", "../bad", "switchback-obsv-ABC"} {
		if err := claim(project, filepath.Join(t.TempDir(), "evidence")); err == nil {
			t.Fatalf("accepted unsafe project %q", project)
		}
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "evidence")
	if err := claim("switchback-obsv-a1234567", root); err != nil {
		t.Fatal(err)
	}
	if err := claim("switchback-obsv-a1234567", root); err == nil {
		t.Fatal("reclaimed existing evidence")
	}
	info, err := os.Stat(filepath.Join(root, "claim.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("claim must be private")
	}
}

func TestCorrelationClaimRequiresFreshMonitoringValues(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "correlation")
	project := "switchback-obsv-test1234"
	if err := claim(project, root); err != nil {
		t.Fatal(err)
	}
	values, err := correlationEnvironment(project, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(values["SWITCHBACK_GRAFANA_PASSWORD"]) != 64 {
		t.Fatal("synthetic Grafana password missing")
	}
	path := filepath.Join(root, "acceptance.env")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"SWITCHBACK_GRAFANA_PORT", "SWITCHBACK_PROMETHEUS_PORT", "SWITCHBACK_GRAFANA_PASSWORD", "COMPOSE_PROJECT_NAME"} {
		changed := strings.Replace(string(original), key+"="+values[key], key+"=", 1)
		if err = os.WriteFile(path, []byte(changed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = correlationEnvironment(project, root); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
	changed := strings.Replace(string(original), "SWITCHBACK_GRAFANA_PORT="+values["SWITCHBACK_GRAFANA_PORT"], "SWITCHBACK_GRAFANA_PORT="+values["SWITCHBACK_PROMETHEUS_PORT"], 1)
	if err = os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = correlationEnvironment(project, root); err == nil {
		t.Fatal("accepted aliased origins")
	}
}

func TestFullReportRejectsMissingPairsCoverageAndSlowSamples(t *testing.T) {
	valid := evidenceReport(t)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	short := valid
	short.Pairs = short.Pairs[:19]
	if short.Validate() == nil {
		t.Fatal("accepted fewer than20 pairs")
	}
	valid.Pairs[0].Outage.SettlementsMS[0] = 5001
	if valid.Validate() == nil {
		t.Fatal("accepted slow sample")
	}
	valid.Pairs[0].Outage.SettlementsMS[0] = 12
	delete(valid.Coverage, "connected_unanswered_query")
	if valid.Validate() == nil {
		t.Fatal("accepted missing scenario")
	}
}

func TestFullReportRejectsLabelsWithoutPhysicalEvidence(t *testing.T) {
	r := Report{Mode: "full", ControlledACP: true, SourceCommit: strings.Repeat("a", 40), WorkerImage: "sha256:" + strings.Repeat("a", 64), BackendImage: "sha256:" + strings.Repeat("b", 64), Coverage: map[string]string{}}
	for _, name := range requiredScenarios {
		r.Coverage[name] = "passed"
	}
	for i := 0; i < 20; i++ {
		r.Pairs = append(r.Pairs, Pair{Healthy: Sample{ID: fmt.Sprint("h", i), SettlementsMS: []float64{10, 20}, NextDispatchMS: 30, SourceVerified: true, IdentityStable: true}, Outage: Sample{ID: fmt.Sprint("o", i), SettlementsMS: []float64{12, 22}, NextDispatchMS: 32, SourceVerified: true, IdentityStable: true}})
	}
	if err := r.Validate(); err == nil {
		t.Fatal("accepted 20 pairs and passed recipe labels without any physical evidence")
	}
}

func TestPairP95StillRejectsMoreThanOneSecondIncrease(t *testing.T) {
	r := evidenceReport(t)
	for i := range r.Pairs {
		s := &r.Pairs[i].Outage
		s.SettlementsMS[0] = 1011
		s.Measurement.Settlements[0].Milliseconds = 1011
		s.Measurement.Settlements[0].To = s.Measurement.From.Add(1011 * time.Millisecond)
		s.Measurement.To = s.Measurement.Settlements[0].To
		for j := range r.Physical.Intervals.Subscriptions {
			stream := &r.Physical.Intervals.Subscriptions[j]
			if stream.ID != s.ID {
				continue
			}
			for k := range stream.Probes {
				if stream.Probes[k].From.After(s.Measurement.From) {
					stream.Probes[k].From = s.Measurement.To.Add(time.Millisecond)
					stream.Probes[k].To = stream.Probes[k].From
				}
			}
		}
	}
	r.Physical.Intervals.Intervals, _ = r.Physical.Intervals.derive(r.Physical.Project)
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "p95") {
		t.Fatalf("got %v, want unchanged p95 bound", err)
	}
}

func TestDerivedImageContextOnlyContainsFixtureInputs(t *testing.T) {
	data, err := os.ReadFile("Dockerfile.worker.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	allowed := map[string]bool{"spikes/": true, "spikes/worker-runtime/": true, "spikes/worker-runtime/go.mod": true, "spikes/worker-runtime/go.sum": true, "spikes/worker-runtime/test/": true, "spikes/worker-runtime/test/fakes/": true, "spikes/worker-runtime/test/fakes/cmd/": true, "spikes/worker-runtime/test/fakes/cmd/fake-acp/": true, "spikes/worker-runtime/test/fakes/cmd/fake-acp/main.go": true, "spikes/worker-runtime/test/fakes/cmd/fake-openai/": true, "spikes/worker-runtime/test/fakes/cmd/fake-openai/main.go": true, "test/": true, "test/acceptance/": true, "test/acceptance/observability/": true, "test/acceptance/observability/litellm.yaml": true}
	denyAll := false
	for _, line := range lines {
		if line == "**" {
			denyAll = true
		}
		if strings.HasPrefix(line, "!") {
			key := strings.TrimPrefix(line, "!")
			if !allowed[key] {
				t.Fatalf("unexpected build input %s", line)
			}
			delete(allowed, key)
		}
	}
	if !denyAll || len(allowed) != 0 {
		t.Fatal("missing deny-all or fixture inputs")
	}
}

func TestPostgresOverlayPreservesCoreNetworksAndLoopbackAccess(t *testing.T) {
	data, err := os.ReadFile("compose.override.yaml")
	if err != nil {
		t.Fatal(err)
	}
	expected := "  postgres:\n    networks: [switchback-data, langfuse-data, local-access]\n    ports:\n      - 127.0.0.1:${OBSERVABILITY_POSTGRES_PORT:?owned loopback PostgreSQL port required}:5432"
	if !strings.Contains(string(data), expected) {
		t.Fatal("test PostgreSQL must retain core networks and loopback-only host access")
	}
}

func TestCaptureInspectTemplateExcludesSecretBearingFields(t *testing.T) {
	for _, field := range []string{".Config.Env", ".Config.Cmd", ".Config.Entrypoint", ".Mounts", ".State.Health.Log", "json .Config.Labels", "json .}}"} {
		if strings.Contains(inspectTemplate, field) {
			t.Fatalf("capture includes secret-bearing field %s", field)
		}
	}
	if !strings.Contains(inspectTemplate, "com.docker.compose.project") || !strings.Contains(inspectTemplate, ".State.StartedAt") {
		t.Fatal("capture misses ownership/start proof")
	}
}

func TestCaptureTemplateSupportsContainerWithoutHealthcheck(t *testing.T) {
	input := map[string]any{"Id": "id", "Name": "migrate", "Created": "2026-09-10T00:00:00Z", "Image": "digest", "Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": "switchback-obsv-a1234567", "com.docker.compose.service": "switchback-migrate"}}, "State": map[string]any{"Status": "exited", "Running": false, "StartedAt": "2026-09-10T00:00:00Z", "FinishedAt": "2026-09-10T00:00:01Z", "ExitCode": 0}}
	tmpl, err := template.New("inspect").Option("missingkey=error").Funcs(template.FuncMap{"json": func(v any) string { b, _ := json.Marshal(v); return string(b) }}).Parse(inspectTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = tmpl.Execute(&output, input); err != nil {
		t.Fatal(err)
	}
	var captured CapturedContainer
	if err = json.Unmarshal(output.Bytes(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Health != nil {
		t.Fatal("container without healthcheck must emit null")
	}
}
