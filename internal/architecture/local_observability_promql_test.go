//go:build observability_native

package architecture_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Opt in because this executes a cached, pinned native promtool image. It never
// starts the product, contacts a provider, or mounts credentials/data volumes.
// SWITCHBACK_OBSERVABILITY_NATIVE_TEST=1 go test -tags=observability_native ./internal/architecture -run TestLocalObservabilityPromQL -count=1 -v
func TestLocalObservabilityPromQL(t *testing.T) {
	if os.Getenv("SWITCHBACK_OBSERVABILITY_NATIVE_TEST") != "1" {
		t.Skip("set SWITCHBACK_OBSERVABILITY_NATIVE_TEST=1 for cached native promtool")
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "observability", "grafana", "provisioning", "alerting", "local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var provision struct {
		Groups []struct {
			Rules []struct {
				UID  string
				Data []struct{ Model struct{ Expr string } }
			}
		}
	}
	if err := yaml.Unmarshal(source, &provision); err != nil {
		t.Fatal(err)
	}
	var recording []map[string]any
	for _, group := range provision.Groups {
		for _, rule := range group.Rules {
			if rule.UID == "sb-log-drops" || rule.UID == "sb-projection-backlog" || rule.UID == "sb-runtime-attention" {
				recording = append(recording, map[string]any{"record": strings.ReplaceAll(rule.UID, "-", "_"), "expr": rule.Data[0].Model.Expr})
			}
		}
	}
	if len(recording) != 3 {
		t.Fatal("expected three production alert expressions")
	}
	type series struct {
		Series string `yaml:"series"`
		Values string `yaml:"values"`
	}
	fresh := []series{{"switchback_telemetry_sample_available", "1+0x5"}, {"switchback_telemetry_sample_current", "1+0x5"}, {"switchback_telemetry_sample_last_success_timestamp_seconds", "0+60x5"}}
	collector := []series{{`up{job="switchback-backend"}`, "1+0x5"}, {`up{job="alloy"}`, "1+0x5"}, {"switchback_telemetry_dropped_total", "0+0x5"}, {"loki_write_dropped_entries_total", "0+0x5"}}
	copySeries := func(s []series) []series { return append([]series(nil), s...) }
	replace := func(s []series, name, value string) []series {
		s = copySeries(s)
		for i := range s {
			if s[i].Series == name {
				s[i].Values = value
			}
		}
		return s
	}
	drop := func(s []series, name string) []series {
		var out []series
		for _, v := range s {
			if v.Series != name {
				out = append(out, v)
			}
		}
		return out
	}
	positive := append(copySeries(fresh), series{"switchback_fragment_oldest_pending_age_seconds", "600+0x5"}, series{`switchback_session_state{status="lost"}`, "1+0x5"})
	type scenario struct {
		name   string
		series []series
		rule   string
		want   *float64
	}
	cases := []scenario{
		{"healthy_zero_drops", collector, "sb_log_drops", number(0)},
		{"actual_drop", replace(collector, "loki_write_dropped_entries_total", "0+1x5"), "sb_log_drops", number(1)},
		{"alloy_down_retained_counters", replace(collector, `up{job="alloy"}`, "0+0x5"), "sb_log_drops", nil},
		{"alloy_target_missing", drop(collector, `up{job="alloy"}`), "sb_log_drops", nil},
		{"alloy_family_missing", drop(collector, "loki_write_dropped_entries_total"), "sb_log_drops", nil},
		{"alloy_family_stale_with_prior_samples", replace(collector, "loki_write_dropped_entries_total", "0 0 0 0 0 stale"), "sb_log_drops", nil},
		{"both_families_missing", collector[:2], "sb_log_drops", nil},
		{"all_collection_absent", nil, "sb_log_drops", nil},
		{"backend_family_missing", drop(collector, "switchback_telemetry_dropped_total"), "sb_log_drops", nil},
	}
	for _, rule := range []string{"sb_projection_backlog", "sb_runtime_attention"} {
		cases = append(cases,
			scenario{"fresh_empty_" + rule, fresh, rule, number(0)},
			scenario{"fresh_positive_" + rule, positive, rule, number(1)},
			scenario{"failed_empty_" + rule, replace(fresh, "switchback_telemetry_sample_current", "0+0x5"), rule, nil},
			scenario{"failed_positive_" + rule, replace(positive, "switchback_telemetry_sample_current", "0+0x5"), rule, nil},
			scenario{"aged_positive_" + rule, replace(positive, "switchback_telemetry_sample_last_success_timestamp_seconds", "0+0x5"), rule, nil},
			scenario{"missing_timestamp_" + rule, drop(fresh, "switchback_telemetry_sample_last_success_timestamp_seconds"), rule, nil},
			scenario{"never_available_" + rule, replace(fresh, "switchback_telemetry_sample_available", "0+0x5"), rule, nil},
		)
	}
	var tests []map[string]any
	for _, tc := range cases {
		expected := []map[string]any{}
		if tc.want != nil {
			expected = append(expected, map[string]any{"labels": "{}", "value": *tc.want})
		}
		tests = append(tests, map[string]any{"name": tc.name, "interval": "1m", "input_series": tc.series, "promql_expr_test": []map[string]any{{"expr": tc.rule + " > bool 0", "eval_time": "5m", "exp_samples": expected}}})
	}
	dir := t.TempDir()
	for name, value := range map[string]any{
		"rules.yaml": map[string]any{"groups": []map[string]any{{"name": "production-expressions", "rules": recording}}},
		"tests.yaml": map[string]any{"rule_files": []string{"rules.yaml"}, "evaluation_interval": "1m", "tests": tests},
	} {
		data, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	compose, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(compose), "\n  prometheus:\n")
	if !ok {
		t.Fatal("prometheus service missing")
	}
	_, image, _ := strings.Cut(strings.Split(block, "\n")[0], "image:")
	image = strings.TrimSpace(image)
	if !strings.Contains(image, "prom/prometheus@sha256:") {
		t.Fatal("expected pinned Prometheus image")
	}
	command := exec.Command("docker", "run", "--pull=never", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", dir+":/fixture:ro", "--entrypoint", "/bin/promtool", image, "test", "rules", "/fixture/tests.yaml")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native PromQL semantics: %v\n%s", err, output)
	}
	t.Logf("%d production-expression scenarios passed: %s", len(cases), output)
}
