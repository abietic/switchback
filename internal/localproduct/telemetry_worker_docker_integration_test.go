//go:build integration && observability_native

package localproduct

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/moby/moby/client"
)

const workerFixtureImage = "docker.io/grafana/alloy@sha256:f111cce835516c5f99166342be7038496b52ced16667be5a11e19258a3e4cd30"

// Native seam: real PostgreSQL -> production collector -> Moby HTTP stream ->
// production sender -> opt-in owned Alloy/Loki. No Agent or provider is started.
func TestTelemetryWorkerDockerNativeCollectionAndReplacement(t *testing.T) {
	if os.Getenv("SWITCHBACK_TEST_WORKER_TELEMETRY_DOCKER") != "1" {
		t.Skip("explicit Worker Docker/Alloy/Loki fixture opt-in required")
	}
	push, loki := os.Getenv("SWITCHBACK_TEST_WORKER_ALLOY_URL"), os.Getenv("SWITCHBACK_TEST_WORKER_LOKI_URL")
	if push == "" || loki == "" {
		t.Fatal("owned native Alloy and Loki endpoints required")
	}
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id := fmt.Sprintf("worker-obsv-%d", time.Now().UnixNano())
	seedWorkerTelemetry(t, pool, id, "tenant", "user", "pending")
	authorities, err := discoverWorkerOutput(ctx, pool, "tenant", "user")
	if err != nil || len(authorities) != 1 {
		t.Fatal("initial authority unavailable")
	}
	a := authorities[0]
	startFixture := func(a workerOutputAuthority) string {
		t.Helper()
		args := []string{"run", "-d", "--name", a.scope.RuntimeInstanceID, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m", "--pids-limit", "32", "--label", "com.switchback.acceptance=worker-obsv-0929", "--entrypoint", "/bin/sh"}
		for k, v := range fixtureWorkerLabels(a) {
			args = append(args, "--label", k+"="+v)
		}
		args = append(args, workerFixtureImage, "-c", "while true; do printf 'credential-SENTINEL-native-stdout\\n'; printf 'credential-SENTINEL-native-stderr\\n' >&2; sleep 0.2; done")
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("create owned output fixture failed: %v", err)
		}
		containerID := strings.TrimSpace(string(out))
		if len(containerID) != 64 {
			t.Fatal("expected exact container id")
		}
		t.Logf("owned output container: %s", containerID)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := exec.CommandContext(cleanup, "docker", "rm", "-f", containerID).Run(); err != nil {
				t.Error("owned output fixture cleanup failed")
			}
		})
		return containerID
	}
	oldID := startFixture(a)
	if _, err = pool.Exec(ctx, `UPDATE runtime_instances SET container_id=$1 WHERE runtime_instance_id=$2`, oldID, a.scope.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	engine, err := dockeradapter.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	tracked := &trackedWorkerEngine{workerOutputEngine: workerOutputEngine{engine}, opened: make(chan string, 4), closed: make(chan string, 4)}
	metrics := telemetry.NewMetrics()
	sender, err := telemetry.NewSender(telemetry.SenderOptions{PushURL: push, ScanJSON: func(_ context.Context, _ string, v any) error {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "credential-SENTINEL") {
			t.Error("sentinel reached serialized sender input")
		}
		return nil
	}}, metrics)
	if err != nil {
		t.Fatal(err)
	}
	collector := workerOutputCollector{discover: func(ctx context.Context) ([]workerOutputAuthority, error) {
		return discoverWorkerOutput(ctx, pool, "tenant", "user")
	}, docker: tracked, emit: func(e telemetry.Event) { sender.Enqueue(e) }, metrics: metrics, interval: 100 * time.Millisecond}
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sender.Run(runCtx) }()
	go func() { defer wg.Done(); collector.run(runCtx) }()
	defer func() { stop(); wg.Wait() }()
	await := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("unexpected container %s", got)
			}
		case <-ctx.Done():
			t.Fatal("native stream deadline")
		}
	}
	await(tracked.opened, oldID)
	queryGeneration := func(generation uint64) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			query := url.Values{"query": {`{service="switchback-backend"} |= "` + id + `"`}, "limit": {"1000"}, "start": {fmt.Sprint(time.Now().Add(-time.Minute).UnixNano())}, "end": {fmt.Sprint(time.Now().UnixNano())}}
			req, _ := http.NewRequestWithContext(ctx, "GET", loki+"/loki/api/v1/query_range?"+query.Encode(), nil)
			resp, err := (&http.Client{Timeout: time.Second}).Do(req)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				if strings.Contains(string(body), "credential-SENTINEL") {
					t.Fatal("secret reached native Loki")
				}
				var result struct {
					Data struct {
						Result []struct {
							Stream map[string]string `json:"stream"`
							Values [][]string        `json:"values"`
						} `json:"result"`
					} `json:"data"`
				}
				if json.Unmarshal(body, &result) == nil {
					seen := map[telemetry.OutputSource]bool{}
					for _, stream := range result.Data.Result {
						for _, value := range stream.Values {
							if len(value) != 2 {
								continue
							}
							var e telemetry.Event
							if json.Unmarshal([]byte(value[1]), &e) == nil && e.Scope.RuntimeGeneration == generation && e.Scope.WorkerSessionID == id && e.Bytes != nil && *e.Bytes > 0 {
								if e.Scope.WorkerRunID != "" || e.Scope.AgentTurnID != "" {
									t.Fatal("raw process output attributed to Turn")
								}
								seen[e.OutputSource] = true
							}
						}
					}
					if seen[telemetry.OutputStdout] && seen[telemetry.OutputStderr] {
						return
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("native Loki missing safe stdout/stderr metadata")
			}
			select {
			case <-ctx.Done():
				t.Fatal("native deadline")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	queryGeneration(1)
	b := a
	b.scope.RuntimeInstanceID = id + "-replacement"
	b.scope.RuntimeGeneration = 2
	newID := startFixture(b)
	_, err = pool.Exec(ctx, `UPDATE runtime_instances SET status='terminated',ended_at=now() WHERE runtime_instance_id=$1;
`, a.scope.RuntimeInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO runtime_instances(runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,network_enforcement_status,created_at)
SELECT $1,worker_session_id,worker_run_id,2,runtime_type,$2,'active',image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,network_enforcement_status,now() FROM runtime_instances WHERE runtime_instance_id=$3`, b.scope.RuntimeInstanceID, newID, a.scope.RuntimeInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE worker_sessions SET active_runtime_instance_id=$1,runtime_generation=2 WHERE worker_session_id=$2`, b.scope.RuntimeInstanceID, id)
	if err != nil {
		t.Fatal(err)
	}
	await(tracked.closed, oldID)
	await(tracked.opened, newID)
	queryGeneration(2)
	// Query result streams may expose Loki's detected_level structured metadata;
	// the series endpoint, rather than query output, proves indexed cardinality.
	seriesURL := loki + "/loki/api/v1/series?" + url.Values{"match[]": {`{service="switchback-backend",component="worker"}`}}.Encode()
	request, _ := http.NewRequestWithContext(ctx, "GET", seriesURL, nil)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatal("native series unavailable")
	}
	defer response.Body.Close()
	var series struct {
		Data []map[string]string `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&series) != nil || len(series.Data) == 0 {
		t.Fatal("native indexed series missing")
	}
	for _, labels := range series.Data {
		if len(labels) != 4 || labels["service"] != telemetry.Service || labels["environment"] != telemetry.Environment || labels["component"] != "worker" || labels["level"] != "info" {
			t.Fatal("native indexed labels changed")
		}
	}
	stop()
	wg.Wait()
	t.Log("native PostgreSQL -> production collector/sender -> Alloy -> Loki generations 1 and 2 verified; sentinel absent; real old HTTP stream closed")
}

type trackedWorkerEngine struct {
	workerOutputEngine
	opened, closed chan string
}

func (d *trackedWorkerEngine) ContainerLogs(ctx context.Context, id string, o client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	r, err := d.Client.ContainerLogs(ctx, id, o)
	if err != nil {
		return nil, err
	}
	d.opened <- id
	return &trackedWorkerStream{ReadCloser: r, id: id, closed: d.closed}, nil
}

type trackedWorkerStream struct {
	io.ReadCloser
	once   sync.Once
	id     string
	closed chan string
}

func (r *trackedWorkerStream) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() { r.closed <- r.id })
	return err
}
