//go:build observability_native

package architecture_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type monitoringFixture struct {
	ctx                                                   context.Context
	root, network, backend, prometheus, grafana, registry string
}

func (f *monitoringFixture) docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newMonitoringFixture(t *testing.T, ctx context.Context) *monitoringFixture {
	t.Helper()
	root, err := filepath.Abs("../../deploy/local")
	if err != nil {
		t.Fatal(err)
	}
	f := &monitoringFixture{ctx: ctx, root: root, network: fmt.Sprintf("sb-monitoring-%d", time.Now().UnixNano())}
	source, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct{ Image string }
	}
	if err = yaml.Unmarshal(source, &compose); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prometheus", "grafana"} {
		if !strings.Contains(compose.Services[name].Image, "@sha256:") {
			t.Fatal("unpinned image")
		}
		f.docker(t, "image", "inspect", compose.Services[name].Image)
	}
	architecture := f.docker(t, "image", "inspect", "--format", "{{.Architecture}}", compose.Services["prometheus"].Image)
	dir := t.TempDir()
	binary := filepath.Join(dir, "monitoring.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-tags=observability_fixture", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+architecture, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v: %s", err, out)
	}
	f.docker(t, "network", "create", f.network)
	var containers []string
	t.Cleanup(func() {
		remove := func(args ...string) ([]byte, error) {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return exec.CommandContext(cleanup, "docker", args...).CombinedOutput()
		}
		for i := len(containers) - 1; i >= 0; i-- {
			out, err := remove("rm", "-f", containers[i])
			if err != nil {
				t.Errorf("cleanup owned container %s: %v %s", containers[i], err, out)
			} else {
				t.Logf("removed owned container %s", containers[i])
			}
		}
		out, err := remove("network", "rm", f.network)
		if err != nil {
			t.Errorf("cleanup owned network: %v %s", err, out)
		} else {
			t.Logf("removed owned network %s; no volumes deleted", f.network)
		}
	})
	run := func(alias, image string, options []string, command ...string) (string, string) {
		name := f.network + "-" + alias
		args := []string{"run", "-d", "--pull=never", "--name", name, "--network", f.network, "--network-alias", alias, "--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
		args = append(args, options...)
		args = append(args, image)
		args = append(args, command...)
		// Register exact unique name before create, including partial creation failures.
		containers = append(containers, name)
		id := f.docker(t, args...)
		t.Logf("owned fixture %s id=%s", name, id)
		return name, id
	}
	f.backend, _ = run("switchback-backend", compose.Services["prometheus"].Image, []string{"-p", "127.0.0.1::9091", "--tmpfs", "/prometheus:uid=65534,gid=65534", "-v", binary + ":/fixture.test:ro", "-e", "SWITCHBACK_MONITORING_REGISTRY_HELPER=1", "--entrypoint", "/fixture.test"}, "-test.run=^TestMonitoringRegistryFixture$", "-test.timeout=20m")
	prom, _ := run("prometheus", compose.Services["prometheus"].Image, []string{"-p", "127.0.0.1::9090", "-v", filepath.Join(root, "observability/prometheus.yaml") + ":/etc/prometheus/prometheus.yaml:ro", "--tmpfs", "/prometheus:uid=65534,gid=65534"}, "--config.file=/etc/prometheus/prometheus.yaml", "--storage.tsdb.path=/prometheus", "--storage.tsdb.retention.time=7d")
	grafana, _ := run("grafana", compose.Services["grafana"].Image, []string{"-p", "127.0.0.1::3000", "--tmpfs", "/var/lib/grafana:uid=472,gid=472", "-e", "GF_SECURITY_ADMIN_USER=monitoring", "-e", "GF_SECURITY_ADMIN_PASSWORD=synthetic-monitoring-only", "-e", "GF_ANALYTICS_REPORTING_ENABLED=false", "-e", "GF_ANALYTICS_CHECK_FOR_UPDATES=false", "-e", "GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES=false", "-v", filepath.Join(root, "observability/grafana/provisioning") + ":/etc/grafana/provisioning:ro", "-v", filepath.Join(root, "observability/grafana/dashboards") + ":/etc/grafana/dashboards:ro"})
	endpoint := func(name, port string) string { return "http://" + f.docker(t, "port", name, port) }
	f.registry = endpoint(f.backend, "9091/tcp")
	f.prometheus = endpoint(prom, "9090/tcp")
	f.grafana = endpoint(grafana, "3000/tcp")
	monitoringPoll(t, ctx, "Grafana ready", func() error { _, e := monitoringHTTP(ctx, f.grafana+"/api/health", nil); return e })
	return f
}

func (f *monitoringFixture) verifyProvisionedRules(t *testing.T, ctx context.Context) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(f.root, "observability/grafana/provisioning/alerting/local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Groups []struct {
			Interval string
			Rules    []monitoringRuleDefinition
		}
	}
	if err = yaml.Unmarshal(source, &expected); err != nil {
		t.Fatal(err)
	}
	b, err := monitoringHTTP(ctx, f.grafana+"/api/v1/provisioning/alert-rules", nil)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []monitoringRuleDefinition
	if err = json.Unmarshal(b, &loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 5 {
		t.Fatalf("want five loaded rules, got %d", len(loaded))
	}
	for _, g := range expected.Groups {
		if g.Interval != "1m" {
			t.Fatal("unexpected production interval")
		}
		for _, want := range g.Rules {
			found := false
			for _, got := range loaded {
				if got.UID != want.UID {
					continue
				}
				found = true
				wantJSON, e := json.Marshal(want)
				if e != nil {
					t.Fatal(e)
				}
				gotJSON, e := json.Marshal(got)
				if e != nil {
					t.Fatal(e)
				}
				if e = monitoringRuleMatches(wantJSON, gotJSON); e != nil {
					t.Fatal(e)
				}
			}
			if !found {
				t.Fatalf("missing rule %s", want.UID)
			}
		}
	}
	t.Log("five delivered rule definitions loaded with production hold durations; production alerting file mounted unchanged")
}
