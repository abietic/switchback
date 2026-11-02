//go:build observability_native

package architecture_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLocalObservabilityMonitoringNative(t *testing.T) {
	if os.Getenv("SWITCHBACK_OBSERVABILITY_MONITORING_NATIVE_TEST") != "1" {
		t.Skip("set SWITCHBACK_OBSERVABILITY_MONITORING_NATIVE_TEST=1 for isolated native services")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	f := newMonitoringFixture(t, ctx)
	sample := func(expr string, want float64, after time.Time) error {
		b, e := monitoringHTTP(ctx, f.prometheus+"/api/v1/query?"+url.Values{"query": {expr}}.Encode(), nil)
		if e != nil {
			return e
		}
		return monitoringSample(b, "switchback-backend", "switchback-backend:9091", want, after)
	}
	start := time.Now()
	monitoringPoll(t, ctx, "healthy scrape", func() error { return sample(`up{job="switchback-backend"}`, 1, start) })
	for _, metric := range []string{"switchback_telemetry_sample_available", "switchback_telemetry_sample_current"} {
		monitoringPoll(t, ctx, metric, func() error { return sample(metric, 1, start) })
	}
	// Wait for two real scrapes so increase() panels have data.
	monitoringPoll(t, ctx, "two scrape samples", func() error {
		b, e := monitoringHTTP(ctx, f.prometheus+"/api/v1/query?"+url.Values{"query": {`count_over_time(up{job="switchback-backend"}[1m]) >= 2`}}.Encode(), nil)
		if e != nil {
			return e
		}
		var r struct{ Data struct{ Result []any } }
		if e = json.Unmarshal(b, &r); e != nil {
			return e
		}
		if len(r.Data.Result) != 1 {
			return fmt.Errorf("need two scrapes")
		}
		return nil
	})
	for _, name := range []string{"platform-health", "worker-runtime", "harness-model", "projection-health"} {
		source, e := os.ReadFile(filepath.Join(f.root, "observability/grafana/dashboards", name+".json"))
		if e != nil {
			t.Fatal(e)
		}
		var expected monitoringDashboard
		if e = json.Unmarshal(source, &expected); e != nil {
			t.Fatal(e)
		}
		var loaded struct{ Dashboard monitoringDashboard }
		monitoringPoll(t, ctx, "dashboard "+name, func() error {
			b, e := monitoringHTTP(ctx, f.grafana+"/api/dashboards/uid/"+expected.UID, nil)
			if e != nil {
				return e
			}
			return json.Unmarshal(b, &loaded)
		})
		actual, e := json.Marshal(loaded.Dashboard)
		if e != nil {
			t.Fatal(e)
		}
		if e = monitoringDashboardMatches(source, actual); e != nil {
			t.Fatal(e)
		}
		for _, p := range loaded.Dashboard.Panels {
			if p.Type == "text" {
				continue
			}
			if len(p.Targets) == 0 {
				t.Fatal("panel without queries")
			}
			var refs []string
			for _, q := range p.Targets {
				q["datasource"] = p.Datasource
				q["intervalMs"] = 15000
				q["maxDataPoints"] = 1000
				refs = append(refs, q["refId"].(string))
			}
			unavailable := name == "platform-health" && (p.ID == 5 || p.ID == 6)
			panelCtx, panelCancel := context.WithTimeout(ctx, 90*time.Second)
			monitoringPoll(t, panelCtx, name+"/"+p.Title, func() error {
				payload := map[string]any{"from": strconv.FormatInt(start.UnixMilli(), 10), "to": strconv.FormatInt(time.Now().UnixMilli(), 10), "queries": p.Targets}
				b, e := monitoringHTTP(panelCtx, f.grafana+"/api/ds/query", payload)
				if e != nil {
					return e
				}
				return monitoringFrames(b, refs, unavailable)
			})
			panelCancel()
			t.Logf("dashboard=%s panel=%d title=%q unavailable=%v", name, p.ID, p.Title, unavailable)
		}
	}
	// Verify loaded rule UIDs and exact provisioned definitions, including timing.
	f.verifyProvisionedRules(t, ctx)
	ruleState := func(uid, want string, after time.Time) (time.Time, error) {
		rules, e := monitoringRules(ctx, f.grafana)
		if e != nil {
			return time.Time{}, e
		}
		for _, r := range rules {
			if r.UID == uid {
				if e := monitoringAlertState(r, want, after); e != nil {
					return time.Time{}, e
				}
				return r.LastEvaluation, nil
			}
		}
		return time.Time{}, fmt.Errorf("rule %s missing", uid)
	}
	monitoringPoll(t, ctx, "Backend Normal", func() error { _, e := ruleState("sb-backend-down", "Normal", start); return e })
	monitoringPoll(t, ctx, "domain legitimate zero Normal", func() error { _, e := ruleState("sb-runtime-attention", "Normal", start); return e })
	fault := time.Now()
	f.docker(t, "stop", "--time", "1", f.backend)
	monitoringPoll(t, ctx, "unreachable scrape", func() error { return sample(`up{job="switchback-backend"}`, 0, fault) })
	var fired time.Time
	monitoringPoll(t, ctx, "Backend firing with production 2m hold", func() error { var e error; fired, e = ruleState("sb-backend-down", "Alerting", fault); return e })
	t.Logf("Backend fault=%s firing evaluation=%s", fault.UTC(), fired.UTC())
	f.docker(t, "start", f.backend)
	f.registry = "http://" + f.docker(t, "port", f.backend, "9091/tcp")
	restored := time.Now()
	monitoringPoll(t, ctx, "restored scrape", func() error { return sample(`up{job="switchback-backend"}`, 1, restored) })
	monitoringPoll(t, ctx, "post-firing recovery", func() error {
		evaluated, e := ruleState("sb-backend-down", "Normal", restored)
		if e != nil {
			return e
		}
		if e = monitoringRecovery("Normal", evaluated, fired); e != nil {
			return e
		}
		t.Logf("Backend recovery evaluated=%s after firing=%s and restored=%s", evaluated.UTC(), fired.UTC(), restored.UTC())
		return nil
	})
	missing := time.Now()
	if _, e := monitoringHTTP(ctx, f.registry+"/sample-failed", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	monitoringPoll(t, ctx, "failed sample current=0", func() error { return sample("switchback_telemetry_sample_current", 0, missing) })
	monitoringPoll(t, ctx, "domain missing data distinct from zero", func() error {
		evaluated, e := ruleState("sb-runtime-attention", "NoData", missing)
		if e == nil {
			t.Logf("domain rule=sb-runtime-attention source=switchback-prometheus/A NoData evaluated=%s after failed sample=%s", evaluated.UTC(), missing.UTC())
		}
		return e
	})
	t.Log("Backend healthy -> unreachable -> Alerting -> reachable -> Normal; runtime domain Normal zero -> NoData; API query evidence only, no browser rendering claim")
}
