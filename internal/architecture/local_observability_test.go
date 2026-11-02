package architecture_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLocalObservabilityContracts(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, e := os.ReadFile(filepath.Join("..", "..", "deploy", "local", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	compose := read("compose.yaml")
	for _, name := range []string{"prometheus", "loki", "alloy", "grafana"} {
		marker := "\n  " + name + ":\n"
		_, rest, ok := strings.Cut(compose, marker)
		if !ok {
			t.Fatalf("missing telemetry service %s", name)
		}
		service := regexp.MustCompile("\\n  [a-z]").Split(rest, 2)[0]
		for _, required := range []string{"@sha256:", "cap_drop: [ALL]", "no-new-privileges:true", "observability"} {
			if !strings.Contains(service, required) {
				t.Errorf("%s missing %s", name, required)
			}
		}
		for _, forbidden := range []string{"docker.sock", "PASSWORD: ${LANGFUSE", "SWITCHBACK_POSTGRES_PASSWORD", "provider/"} {
			if strings.Contains(service, forbidden) {
				t.Errorf("%s leaks core access: %s", name, forbidden)
			}
		}
		if (name == "alloy" || name == "loki") && strings.Contains(service, "ports:") {
			t.Errorf("%s must not publish ports", name)
		}
		if name == "alloy" && !strings.Contains(service, `user: "473:473"`) {
			t.Error("Alloy must own its fresh volume without DAC_OVERRIDE")
		}
	}
	_, backend, _ := strings.Cut(compose, "\n  switchback-backend:\n")
	startup := strings.Split(backend, "    environment:")[0]
	for _, s := range []string{"prometheus:", "loki:", "alloy:", "grafana:"} {
		if strings.Contains(startup, s) {
			t.Errorf("backend depends on %s", s)
		}
	}
	for file, required := range map[string][]string{
		"observability/prometheus.yaml":                             {"scrape_interval: 15s", "switchback-backend:9091", "alloy:12346"},
		"observability/loki.yaml":                                   {"schema: v13", "period: 24h", "retention_period: 168h", "retention_enabled: true", "delete_request_store: filesystem", "discover_service_name: []"},
		"observability/alloy.alloy":                                 {"use_incoming_timestamp = true", "max_backoff_retries = 2", "capacity = \"1MiB\"", "block_on_overflow = false"},
		"observability/grafana/provisioning/datasources/local.yaml": {"uid: switchback-prometheus", "uid: switchback-loki", "editable: false"},
	} {
		content := read(file)
		for _, s := range required {
			if !strings.Contains(content, s) {
				t.Errorf("%s missing %s", file, s)
			}
		}
	}
	for _, name := range []string{"platform-health", "worker-runtime", "harness-model", "projection-health"} {
		var dashboard struct {
			UID    string
			Panels []json.RawMessage
		}
		if err := json.Unmarshal([]byte(read("observability/grafana/dashboards/"+name+".json")), &dashboard); err != nil {
			t.Fatal(err)
		}
		if dashboard.UID == "" || len(dashboard.Panels) < 2 {
			t.Errorf("incomplete dashboard %s", name)
		}
	}
	rules := read("observability/grafana/provisioning/alerting/local.yaml")
	for _, s := range []string{"backend-down", "log-drops", "sample-stale", "projection-backlog", "runtime-attention", "noDataState:", "execErrState:", "for:"} {
		if !strings.Contains(rules, s) {
			t.Errorf("missing alert contract %s", s)
		}
	}
}

func TestLocalObservabilityBootstrapPreservesPassword(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy", "local")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "bootstrap-env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "bootstrap-env.sh")
	if err := os.WriteFile(script, source, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if output, err := exec.Command("sh", script).CombinedOutput(); err != nil {
			t.Fatalf("bootstrap: %v %s", err, output)
		}
		content, err := os.ReadFile(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(content), "SWITCHBACK_GRAFANA_PASSWORD=") != 1 {
			t.Fatal("password missing or duplicated")
		}
		if i == 0 {
			// Simulate an existing operator-managed password before re-running bootstrap.
			updated := regexp.MustCompile("(?m)^SWITCHBACK_GRAFANA_PASSWORD=.*$").ReplaceAllString(string(content), "SWITCHBACK_GRAFANA_PASSWORD=preserve-fixture")
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(updated), 0600); err != nil {
				t.Fatal(err)
			}
		} else if !strings.Contains(string(content), "SWITCHBACK_GRAFANA_PASSWORD=preserve-fixture\n") {
			t.Fatal("existing password changed")
		}
	}
}
