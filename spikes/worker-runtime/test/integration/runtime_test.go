package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
)

const (
	defaultBackendURL      = "http://127.0.0.1:18080"
	managedWorkerLabel     = "com.switchback.managed=worker-runtime"
	managedPopulatorLabel  = "com.switchback.managed=runtime-secret-populator"
	managedVolumeLabel     = "com.switchback.managed=runtime-secret-volume"
	sessionLabel           = "com.switchback.worker-session-id="
	runLabel               = "com.switchback.worker-run-id"
	runtimeInstanceLabel   = "com.switchback.runtime-instance-id"
	runtimeGenerationLabel = "com.switchback.runtime-generation"
)

type createdRun struct {
	WorkerSessionID string `json:"worker_session_id"`
	WorkerRunID     string `json:"worker_run_id"`
	TurnID          string `json:"turn_id"`
}

type createdTurn struct {
	WorkerRunID string `json:"worker_run_id"`
	TurnID      string `json:"turn_id"`
}

type runSnapshot struct {
	WorkerSessionID       string `json:"worker_session_id"`
	WorkerRunID           string `json:"worker_run_id"`
	RuntimeInstanceID     string `json:"runtime_instance_id"`
	RuntimeGeneration     uint32 `json:"runtime_generation"`
	AgentSessionID        string `json:"agent_session_id"`
	State                 string `json:"state"`
	ExecutionOutcome      string `json:"execution_outcome"`
	SpikeAcceptance       string `json:"spike_acceptance"`
	ModelObservationCount int    `json:"model_observation_count"`
	Turns                 []struct {
		ID string `json:"id"`
	} `json:"turns"`
	Diagnostics []runDiagnostic `json:"diagnostics"`
}

type runDiagnostic struct {
	Kind   string `json:"kind"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type processStatus struct {
	PID    int    `json:"pid"`
	UID    int    `json:"uid"`
	Name   string `json:"name"`
	Cmd    string `json:"cmd"`
	CapPrm string `json:"cap_prm"`
	CapEff string `json:"cap_eff"`
}

type rawACPRecord struct {
	Type       string `json:"type"`
	Direction  string `json:"direction,omitempty"`
	Frame      string `json:"frame,omitempty"`
	UpdateType string `json:"update_type,omitempty"`
}

type normalizedTraceRecord struct {
	SchemaVersion string                      `json:"schema_version"`
	Sequence      uint64                      `json:"sequence"`
	Identity      sharedevidence.TurnIdentity `json:"identity"`
	Kind          string                      `json:"kind"`
}

type rpcFrame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type inspectedContainer struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Privileged   bool                   `json:"Privileged"`
		PortBindings map[string]interface{} `json:"PortBindings"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func TestRuntimeFullPath(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_INTEGRATION") != "1" {
		t.Skip("set SWITCHBACK_SPIKE_INTEGRATION=1 to run the Docker integration proof")
	}
	root := spikeRoot(t)
	dataRoot := os.Getenv("SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT")
	if dataRoot == "" {
		dataRoot = filepath.Join(root, ".spike-data")
	}
	dataRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "fixtures", "go-repository")
	fixtureBefore := treeDigest(t, fixture)
	backendURL := os.Getenv("SWITCHBACK_SPIKE_INTEGRATION_BACKEND_URL")
	if backendURL == "" {
		backendURL = defaultBackendURL
	}
	client := &http.Client{Timeout: 5 * time.Second}

	waitHTTPReady(t, client, backendURL)
	created := postJSON[createdRun](t, client, backendURL+"/spike/runs", map[string]any{
		"goal":                "repair the deterministic counter fixture",
		"acceptance_criteria": []string{"two turns edit one persistent WorkingCopy"},
	})
	t.Cleanup(func() {
		cleanupSession(t, client, backendURL, created.WorkerSessionID)
	})

	first := waitRunState(t, client, backendURL, created.WorkerRunID, "idle")
	if first.AgentSessionID == "" || len(first.Turns) != 1 || first.Turns[0].ID != created.TurnID {
		t.Fatalf("first turn snapshot = %+v", first)
	}
	if first.ModelObservationCount != 1 {
		t.Fatalf("first turn model observations = %d, want 1", first.ModelObservationCount)
	}
	workingCopy := filepath.Join(dataRoot, "sessions", created.WorkerSessionID, "working-copy")
	counterSource := readFile(t, filepath.Join(workingCopy, "counter", "counter.go"))
	if !strings.Contains(counterSource, "return current + delta") {
		t.Fatalf("first turn did not repair counter.go:\n%s", counterSource)
	}

	second := postJSON[createdTurn](
		t,
		client,
		backendURL+"/spike/sessions/"+created.WorkerSessionID+"/turns",
		map[string]string{"message": "add the zero-delta boundary test"},
	)
	if second.WorkerRunID != created.WorkerRunID || second.TurnID == created.TurnID {
		t.Fatalf("second turn identity = %+v, first = %+v", second, created)
	}
	completed := waitRunState(t, client, backendURL, created.WorkerRunID, "completed")
	if completed.AgentSessionID != first.AgentSessionID ||
		completed.RuntimeGeneration != 1 ||
		len(completed.Turns) != 2 ||
		completed.ModelObservationCount != 2 {
		t.Fatalf("completed snapshot = %+v", completed)
	}
	completed = waitProjectionFailure(t, client, backendURL, created.WorkerRunID)
	counterTest := readFile(t, filepath.Join(workingCopy, "counter", "counter_test.go"))
	if !strings.Contains(counterTest, "TestAddZeroDelta") {
		t.Fatalf("second turn did not add the boundary test:\n%s", counterTest)
	}

	containerID := findOneContainer(
		t,
		"label="+managedWorkerLabel,
		"label="+sessionLabel+created.WorkerSessionID,
	)
	assertWorkerIsolation(t, containerID, dataRoot)
	assertLiveProcessCapabilities(t, containerID)
	assertFakeACPProtocol(t, dockerOutput(t, "logs", containerID), first.AgentSessionID)
	assertFakeUpstream(t)
	assertEvidence(t, dataRoot, created, second)
	if fixtureAfter := treeDigest(t, fixture); fixtureAfter != fixtureBefore {
		t.Fatalf("fixture source digest changed: before %s after %s", fixtureBefore, fixtureAfter)
	}

	cleanupSession(t, client, backendURL, created.WorkerSessionID)
	waitContainerRemoved(t, containerID)
}

func waitProjectionFailure(
	t *testing.T,
	client *http.Client,
	backendURL, runID string,
) runSnapshot {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var latest runSnapshot
	for time.Now().Before(deadline) {
		response, err := client.Get(backendURL + "/spike/runs/" + runID)
		if err == nil {
			content, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				if decodeErr := json.Unmarshal(content, &latest); decodeErr == nil &&
					latest.ExecutionOutcome == "completed" &&
					latest.SpikeAcceptance == "failed" &&
					hasLangfuseProjectionFailure(latest.Diagnostics) {
					return latest
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf(
		"run %s projection did not settle: outcome=%q acceptance=%q diagnostics=%+v",
		runID,
		latest.ExecutionOutcome,
		latest.SpikeAcceptance,
		latest.Diagnostics,
	)
	return runSnapshot{}
}

func hasLangfuseProjectionFailure(diagnostics []runDiagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Kind == "langfuse_projection" &&
			diagnostic.Code == "acceptance_failed" &&
			diagnostic.Detail != "" &&
			len([]rune(diagnostic.Kind)) <= 64 &&
			len([]rune(diagnostic.Code)) <= 128 &&
			len([]rune(diagnostic.Detail)) <= 512 {
			return true
		}
	}
	return false
}

func cleanupSession(
	t *testing.T,
	client *http.Client,
	backendURL, sessionID string,
) {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		backendURL+"/spike/sessions/"+sessionID+"/cleanup",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		t.Errorf("construct cleanup request: %v", err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Errorf("cleanup session %s: %v", sessionID, err)
		return
	}
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Errorf("cleanup session %s status = %d", sessionID, response.StatusCode)
		return
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		containerCommand := exec.Command(
			"docker",
			"ps",
			"-aq",
			"--filter",
			"label="+sessionLabel+sessionID,
		)
		containers, containerErr := containerCommand.CombinedOutput()
		volumeCommand := exec.Command(
			"docker",
			"volume",
			"ls",
			"-q",
			"--filter",
			"label="+sessionLabel+sessionID,
		)
		volumes, volumeErr := volumeCommand.CombinedOutput()
		if containerErr == nil &&
			volumeErr == nil &&
			len(bytes.TrimSpace(containers)) == 0 &&
			len(bytes.TrimSpace(volumes)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("dynamic Worker, populator, or secret volume for session %s was not removed", sessionID)
}

func waitHTTPReady(t *testing.T, client *http.Client, backendURL string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, backendURL+"/spike/runs/not-ready", nil)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Backend did not become reachable at %s", backendURL)
}

func postJSON[T any](t *testing.T, client *http.Client, url string, body any) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("POST %s status = %d: %s", url, response.StatusCode, content)
	}
	var decoded T
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("decode POST %s response: %v: %s", url, err, content)
	}
	return decoded
}

func waitRunState(
	t *testing.T,
	client *http.Client,
	backendURL, runID, want string,
) runSnapshot {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var latest runSnapshot
	for time.Now().Before(deadline) {
		response, err := client.Get(backendURL + "/spike/runs/" + runID)
		if err == nil {
			content, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				if decodeErr := json.Unmarshal(content, &latest); decodeErr == nil && latest.State == want {
					return latest
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	diagnostic := managedWorkerDiagnostic(runID)
	t.Fatalf("run %s state = %q, want %q\n%s", runID, latest.State, want, diagnostic)
	return runSnapshot{}
}

func managedWorkerDiagnostic(runID string) string {
	command := exec.Command(
		"docker",
		"ps",
		"-aq",
		"--filter",
		"label="+managedWorkerLabel,
		"--filter",
		"label=com.switchback.worker-run-id="+runID,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("list dynamic Worker: %v: %s", err, output)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return "dynamic Worker container was not present"
	}
	inspect := exec.Command(
		"docker",
		"inspect",
		"--format",
		"status={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}}",
		containerID,
	)
	inspectOutput, inspectErr := inspect.CombinedOutput()
	logs := exec.Command("docker", "logs", containerID)
	logOutput, logErr := logs.CombinedOutput()
	return fmt.Sprintf(
		"dynamic Worker %s inspect=%q inspect_error=%v logs_error=%v\n%s",
		containerID,
		strings.TrimSpace(string(inspectOutput)),
		inspectErr,
		logErr,
		logOutput,
	)
}

func assertWorkerIsolation(t *testing.T, containerID, dataRoot string) {
	t.Helper()
	var inspected []inspectedContainer
	if err := json.Unmarshal(
		[]byte(dockerOutput(t, "inspect", containerID)),
		&inspected,
	); err != nil || len(inspected) != 1 {
		t.Fatalf("decode docker inspect: %v", err)
	}
	container := inspected[0]
	if container.HostConfig.Privileged {
		t.Fatal("dynamic Worker is privileged")
	}
	if len(container.HostConfig.PortBindings) != 0 {
		t.Fatalf("dynamic Worker has port bindings: %+v", container.HostConfig.PortBindings)
	}
	if container.Config.Labels["com.switchback.managed"] != "worker-runtime" {
		t.Fatalf("dynamic Worker labels = %+v", container.Config.Labels)
	}
	cleanRoot := filepath.Clean(dataRoot) + string(filepath.Separator)
	if len(container.Mounts) != 4 {
		t.Fatalf("dynamic Worker mounts = %d, want 4", len(container.Mounts))
	}
	wantBindTargets := map[string]bool{
		"/workspace":   false,
		"/agent-state": false,
		"/trace-spool": false,
	}
	volumeName := "switchback-runtime-secrets-" +
		container.Config.Labels[runtimeInstanceLabel] + "-g" +
		container.Config.Labels[runtimeGenerationLabel]
	volumeSeen := false
	for _, mounted := range container.Mounts {
		if mounted.Destination == "/var/run/docker.sock" ||
			strings.Contains(mounted.Source, "docker.sock") {
			t.Fatalf("dynamic Worker received Docker Socket: %+v", mounted)
		}
		switch mounted.Type {
		case "bind":
			if _, ok := wantBindTargets[mounted.Destination]; !ok {
				t.Fatalf("dynamic Worker received an unexpected host bind: %+v", mounted)
			}
			if !strings.HasPrefix(filepath.Clean(mounted.Source), cleanRoot) {
				t.Fatalf("dynamic Worker mount is not server-derived: %+v", mounted)
			}
			wantBindTargets[mounted.Destination] = true
		case "volume":
			if mounted.Name != volumeName ||
				mounted.Destination != "/run/switchback-secrets" ||
				mounted.RW {
				t.Fatalf("dynamic Worker runtime secret volume = %+v", mounted)
			}
			volumeSeen = true
		default:
			t.Fatalf("dynamic Worker received an unexpected mount type: %+v", mounted)
		}
	}
	for target, seen := range wantBindTargets {
		if !seen {
			t.Fatalf("dynamic Worker is missing host bind %s", target)
		}
	}
	if !volumeSeen {
		t.Fatal("dynamic Worker is missing the runtime secret volume")
	}
	assertRuntimeSecretVolume(t, volumeName, container.Config.Labels)
	assertRuntimeSecretStagingRemoved(t, dataRoot, container.Config.Labels)
}

func assertRuntimeSecretVolume(
	t *testing.T,
	volumeName string,
	workerLabels map[string]string,
) {
	t.Helper()
	var volumes []struct {
		Name   string            `json:"Name"`
		Driver string            `json:"Driver"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(
		[]byte(dockerOutput(t, "volume", "inspect", volumeName)),
		&volumes,
	); err != nil || len(volumes) != 1 {
		t.Fatalf("decode runtime secret volume inspect: %v", err)
	}
	wantLabels := map[string]string{
		"com.switchback.managed":             "runtime-secret-volume",
		"com.switchback.worker-session-id":   workerLabels["com.switchback.worker-session-id"],
		"com.switchback.worker-run-id":       workerLabels[runLabel],
		"com.switchback.runtime-instance-id": workerLabels[runtimeInstanceLabel],
		"com.switchback.runtime-generation":  workerLabels[runtimeGenerationLabel],
	}
	if volumes[0].Name != volumeName ||
		volumes[0].Driver != "local" ||
		!equalStringMap(volumes[0].Labels, wantLabels) {
		t.Fatalf("runtime secret volume identity = %+v, want exact labels %+v", volumes[0], wantLabels)
	}
}

func assertRuntimeSecretStagingRemoved(
	t *testing.T,
	dataRoot string,
	workerLabels map[string]string,
) {
	t.Helper()
	sessionID := workerLabels["com.switchback.worker-session-id"]
	populators := strings.Fields(dockerOutput(
		t,
		"ps",
		"-aq",
		"--filter",
		"label="+managedPopulatorLabel,
		"--filter",
		"label="+sessionLabel+sessionID,
	))
	if len(populators) != 0 {
		t.Fatalf("runtime secret populators remain after staging: %v", populators)
	}
	sessionRoot := filepath.Join(dataRoot, "sessions", sessionID)
	for _, source := range []string{
		filepath.Join(sessionRoot, "bootstrap.json"),
		filepath.Join(sessionRoot, "model-secret"),
	} {
		if _, err := os.Lstat(source); !os.IsNotExist(err) {
			t.Fatalf("transient runtime secret source remains after staging: %s", source)
		}
	}
	globalSource := filepath.Join(dataRoot, "env", "upstream-model.key")
	if info, err := os.Lstat(globalSource); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("global Backend model source was removed or replaced: %v", err)
	}
}

func equalStringMap(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func assertLiveProcessCapabilities(t *testing.T, containerID string) {
	t.Helper()
	const script = `
import json
from pathlib import Path

result = []
for status_path in Path("/proc").glob("[0-9]*/status"):
    fields = {}
    for line in status_path.read_text().splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            fields[key] = value.strip()
    try:
        cmd = (status_path.parent / "cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace").strip()
    except OSError:
        continue
    result.append({
        "pid": int(status_path.parent.name),
        "uid": int(fields["Uid"].split()[0]),
        "name": fields["Name"],
        "cmd": cmd,
        "cap_prm": fields["CapPrm"],
        "cap_eff": fields["CapEff"],
    })
print(json.dumps(result))
`
	var statuses []processStatus
	if err := json.Unmarshal(
		[]byte(dockerOutput(t, "exec", containerID, "python", "-c", script)),
		&statuses,
	); err != nil {
		t.Fatalf("decode live process capabilities: %v", err)
	}
	var rootShim *processStatus
	children := map[int][]processStatus{10001: {}, 10002: {}}
	for index := range statuses {
		status := statuses[index]
		if status.UID == 0 &&
			status.Name == "worker-shim" &&
			strings.HasPrefix(status.Cmd, "/usr/local/bin/worker-shim") {
			if rootShim != nil {
				t.Fatalf("multiple live root Shims: %+v and %+v", rootShim, status)
			}
			rootShim = &statuses[index]
		}
		if _, wanted := children[status.UID]; wanted {
			children[status.UID] = append(children[status.UID], status)
		}
	}
	if rootShim == nil || rootShim.UID != 0 {
		t.Fatalf("root Shim status = %+v, processes = %+v", rootShim, statuses)
	}
	assertCapabilityMask(t, "root Shim CapPrm", rootShim.CapPrm, 0xe0)
	assertCapabilityMask(t, "root Shim CapEff", rootShim.CapEff, 0xe0)
	childMarkers := map[int]string{
		10001: "litellm",
		10002: "/opt/switchback/bin/fake-acp",
	}
	for _, uid := range []int{10001, 10002} {
		if len(children[uid]) == 0 {
			t.Fatalf("live child UID %d not found: %+v", uid, statuses)
		}
		var matched bool
		for _, child := range children[uid] {
			assertCapabilityMask(t, fmt.Sprintf("UID %d PID %d CapPrm", uid, child.PID), child.CapPrm, 0)
			assertCapabilityMask(t, fmt.Sprintf("UID %d PID %d CapEff", uid, child.PID), child.CapEff, 0)
			matched = matched || strings.Contains(child.Cmd, childMarkers[uid])
		}
		if !matched {
			t.Fatalf("UID %d has no live %q child: %+v", uid, childMarkers[uid], children[uid])
		}
	}
}

func assertCapabilityMask(t *testing.T, label, encoded string, want uint64) {
	t.Helper()
	got, err := strconv.ParseUint(encoded, 16, 64)
	if err != nil || got != want {
		t.Fatalf("%s = %q (%d), want %#x: %v", label, encoded, got, want, err)
	}
}

func assertFakeACPProtocol(t *testing.T, logs, sessionID string) {
	t.Helper()
	type fakeACPEvent struct {
		Event     string `json:"event"`
		SessionID string `json:"session_id"`
		Turn      int    `json:"turn"`
	}

	const prefix = "SWITCHBACK_FAKE_ACP "
	var actual []fakeACPEvent
	scanner := bufio.NewScanner(strings.NewReader(logs))
	for scanner.Scan() {
		line := scanner.Text()
		index := strings.Index(line, prefix)
		if index < 0 {
			continue
		}
		var event fakeACPEvent
		if err := json.Unmarshal([]byte(line[index+len(prefix):]), &event); err != nil {
			t.Fatalf("decode fake ACP log event: %v: %s", err, line)
		}
		actual = append(actual, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fake ACP logs: %v", err)
	}
	expected := []fakeACPEvent{
		{Event: "initialize"},
		{Event: "authenticate"},
		{Event: "new_session", SessionID: sessionID},
		{Event: "prompt", SessionID: sessionID, Turn: 1},
		{Event: "prompt", SessionID: sessionID, Turn: 2},
	}
	if len(actual) != len(expected) {
		t.Fatalf("fake ACP events = %+v, want %+v:\n%s", actual, expected, logs)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("fake ACP event %d = %+v, want %+v:\n%s", index, actual[index], expected[index], logs)
		}
	}
}

func assertFakeUpstream(t *testing.T) {
	t.Helper()
	containerID := findOneContainer(
		t,
		"label=com.docker.compose.project=switchback-worker-runtime-spike",
		"label=com.docker.compose.service=fake-openai",
	)
	logs := dockerOutput(t, "logs", containerID)
	if count := strings.Count(logs, `"path":"/v1/responses"`); count != 2 {
		t.Fatalf("fake upstream calls = %d, want 2:\n%s", count, logs)
	}
	if count := strings.Count(logs, `"authorization_present":true`); count != 2 {
		t.Fatalf("authorized fake upstream calls = %d, want 2:\n%s", count, logs)
	}
	for _, secret := range []string{"integration-upstream-key", "Bearer "} {
		if strings.Contains(logs, secret) {
			t.Fatalf("fake upstream logs exposed credential marker %q", secret)
		}
	}
	var inspected []inspectedContainer
	if err := json.Unmarshal([]byte(dockerOutput(t, "inspect", containerID)), &inspected); err != nil ||
		len(inspected) != 1 {
		t.Fatalf("decode fake upstream inspect: %v", err)
	}
	container := inspected[0]
	if container.HostConfig.Privileged ||
		len(container.HostConfig.PortBindings) != 0 ||
		len(container.Mounts) != 0 {
		t.Fatalf("fake upstream isolation = %+v", container)
	}
	if len(container.NetworkSettings.Networks) != 1 {
		t.Fatalf("fake upstream networks = %+v, want only spike-control", container.NetworkSettings.Networks)
	}
	if _, ok := container.NetworkSettings.Networks["switchback-spike-control"]; !ok {
		t.Fatalf("fake upstream networks = %+v, want switchback-spike-control", container.NetworkSettings.Networks)
	}
}

func assertEvidence(t *testing.T, dataRoot string, first createdRun, second createdTurn) {
	t.Helper()
	runRoot := filepath.Join(dataRoot, "objects", first.WorkerSessionID, first.WorkerRunID)
	turnIDs := []string{first.TurnID, second.TurnID}
	for _, turnID := range turnIDs {
		turnRoot := filepath.Join(runRoot, "turns", turnID)
		entries, err := os.ReadDir(turnRoot)
		if err != nil {
			t.Fatalf("read evidence for turn %s: %v", turnID, err)
		}
		if len(entries) != 4 {
			t.Fatalf("turn %s evidence objects = %d, want 4", turnID, len(entries))
		}
		for _, name := range []string{"raw-acp.jsonl", "trace.jsonl", "report.md", "changes.patch"} {
			info, statErr := os.Stat(filepath.Join(turnRoot, name))
			if statErr != nil || info.Size() == 0 {
				t.Fatalf("turn %s evidence %s missing or empty: %v", turnID, name, statErr)
			}
		}
		assertRawACPProvenance(t, filepath.Join(turnRoot, "raw-acp.jsonl"), first, turnID)
		assertNormalizedTraceProvenance(t, filepath.Join(turnRoot, "trace.jsonl"), first, turnID)
	}
	var manifest sharedevidence.TurnManifest
	decodeJSONFile(t, filepath.Join(runRoot, "manifest.json"), &manifest)
	if manifest.SchemaVersion != sharedevidence.TurnManifestSchemaVersion ||
		manifest.WorkerSessionID != first.WorkerSessionID ||
		manifest.WorkerRunID != first.WorkerRunID ||
		manifest.RuntimeGeneration != 1 ||
		manifest.AgentSessionID != "fake-session-1" ||
		manifest.AgentTurnID != second.TurnID ||
		manifest.RuntimeInstanceID == "" ||
		manifest.TraceID == "" {
		t.Fatalf("latest manifest identity = %+v", manifest)
	}
	assertManifestObjects(t, manifest, runRoot, second.TurnID)
	assertDeterministicPatch(t, filepath.Join(runRoot, "turns", first.TurnID, "changes.patch"), 1)
	assertDeterministicPatch(t, filepath.Join(runRoot, "turns", second.TurnID, "changes.patch"), 2)
}

func assertManifestObjects(
	t *testing.T,
	manifest sharedevidence.TurnManifest,
	runRoot, turnID string,
) {
	t.Helper()
	want := []struct {
		kind string
		name string
	}{
		{sharedevidence.KindReport, "report.md"},
		{sharedevidence.KindRawACP, "raw-acp.jsonl"},
		{sharedevidence.KindTrace, "trace.jsonl"},
		{sharedevidence.KindPatch, "changes.patch"},
	}
	if len(manifest.Objects) != len(want) {
		t.Fatalf("manifest objects = %d, want %d", len(manifest.Objects), len(want))
	}
	for index, expected := range want {
		object := manifest.Objects[index]
		expectedPath := filepath.Join(
			"/var/lib/switchback-spike/objects",
			manifest.WorkerSessionID,
			manifest.WorkerRunID,
			"turns",
			turnID,
			expected.name,
		)
		if object.Kind != expected.kind || filepath.Clean(object.Path) != expectedPath {
			t.Fatalf("manifest object %d = %+v, want kind=%q path=%q", index, object, expected.kind, expectedPath)
		}
		content, err := os.ReadFile(filepath.Join(runRoot, "turns", turnID, expected.name))
		if err != nil {
			t.Fatalf("read manifest object %q: %v", object.Path, err)
		}
		digest := sha256.Sum256(content)
		if object.SHA256 != hex.EncodeToString(digest[:]) || object.Bytes != int64(len(content)) {
			t.Fatalf("manifest object %d digest/size = %+v", index, object)
		}
	}
}

func assertRawACPProvenance(t *testing.T, path string, run createdRun, turnID string) {
	t.Helper()
	var records []rawACPRecord
	decodeJSONLFile(t, path, func(decoder *json.Decoder) error {
		var record rawACPRecord
		if err := decoder.Decode(&record); err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	var prompts int
	for _, record := range records {
		if record.Type != "acp.raw_frame" || record.Direction != "client_to_agent" {
			continue
		}
		var frame rpcFrame
		if err := json.Unmarshal([]byte(record.Frame), &frame); err != nil {
			t.Fatalf("decode raw ACP frame for turn %s: %v", turnID, err)
		}
		if frame.Method != "session/prompt" {
			continue
		}
		prompts++
		var params struct {
			SessionID string `json:"sessionId"`
			Meta      struct {
				Switchback map[string]string `json:"switchback"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			t.Fatalf("decode prompt params for turn %s: %v", turnID, err)
		}
		if params.SessionID != "fake-session-1" ||
			params.Meta.Switchback["worker_session_id"] != run.WorkerSessionID ||
			params.Meta.Switchback["worker_run_id"] != run.WorkerRunID ||
			params.Meta.Switchback["agent_turn_id"] != turnID {
			t.Fatalf("raw ACP prompt provenance for turn %s = %+v", turnID, params)
		}
	}
	if prompts != 1 {
		t.Fatalf("raw ACP prompt records for turn %s = %d, want 1", turnID, prompts)
	}
}

func assertNormalizedTraceProvenance(t *testing.T, path string, run createdRun, turnID string) {
	t.Helper()
	var records []normalizedTraceRecord
	decodeJSONLFile(t, path, func(decoder *json.Decoder) error {
		var record normalizedTraceRecord
		if err := decoder.Decode(&record); err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if len(records) == 0 {
		t.Fatalf("normalized trace %s contains no records", path)
	}
	for index, record := range records {
		identity := record.Identity
		if record.SchemaVersion != sharedevidence.NormalizedTraceSchemaVersion ||
			record.Sequence != uint64(index+1) ||
			record.Kind == "" ||
			identity.WorkerSessionID != run.WorkerSessionID ||
			identity.WorkerRunID != run.WorkerRunID ||
			identity.AgentTurnID != turnID ||
			identity.AgentSessionID != "fake-session-1" ||
			identity.RuntimeInstanceID == "" ||
			identity.RuntimeGeneration != 1 ||
			identity.TraceID == "" {
			t.Fatalf("normalized record %d for turn %s = %+v", index, turnID, record)
		}
	}
}

func assertDeterministicPatch(t *testing.T, path string, turn int) {
	t.Helper()
	content := readFile(t, path)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, "index ") {
			filtered = append(filtered, line)
		}
	}
	actual := strings.Join(filtered, "\n")
	var want string
	switch turn {
	case 1:
		want = strings.Join([]string{
			"diff --git a/counter/counter.go b/counter/counter.go",
			"--- a/counter/counter.go",
			"+++ b/counter/counter.go",
			"@@ -1,5 +1,5 @@",
			" package counter",
			" ",
			" func Add(current, delta int) int {",
			"-\treturn current - delta // Intentional Spike fixture defect.",
			"+\treturn current + delta",
			" }",
		}, "\n")
	case 2:
		want = strings.Join([]string{
			"diff --git a/counter/counter_test.go b/counter/counter_test.go",
			"--- a/counter/counter_test.go",
			"+++ b/counter/counter_test.go",
			"@@ -7,3 +7,10 @@ func TestAdd(t *testing.T) {",
			" \t\tt.Fatalf(\"Add(3, 2) = %d, want 5\", got)",
			" \t}",
			" }",
			"+",
			"+",
			"+func TestAddZeroDelta(t *testing.T) {",
			"+\tif got := Add(7, 0); got != 7 {",
			"+\t\tt.Fatalf(\"Add(7, 0) = %d, want 7\", got)",
			"+\t}",
			"+}",
		}, "\n")
	default:
		t.Fatalf("unsupported deterministic turn %d", turn)
	}
	if actual != want {
		t.Fatalf("turn %d patch:\n%s\nwant:\n%s", turn, actual, want)
	}
}

func decodeJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("decode %s: trailing JSON: %v", path, err)
	}
}

func decodeJSONLFile(t *testing.T, path string, decode func(*json.Decoder) error) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	for {
		err := decode(decoder)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

func findOneContainer(t *testing.T, filters ...string) string {
	t.Helper()
	args := []string{"ps", "-aq"}
	for _, filter := range filters {
		args = append(args, "--filter", filter)
	}
	lines := strings.Fields(dockerOutput(t, args...))
	if len(lines) != 1 {
		t.Fatalf("matching Docker containers = %v, want exactly one", lines)
	}
	return lines[0]
}

func waitContainerRemoved(t *testing.T, containerID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		command := exec.Command("docker", "inspect", containerID)
		if err := command.Run(); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("dynamic Worker %s was not removed", containerID)
}

func dockerOutput(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, output)
	}
	return string(output)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(digest, "%s\x00", path)
		_, _ = digest.Write(content)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func spikeRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join(workingDirectory, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestNoManagedWorkersRemain(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_INTEGRATION") != "1" {
		t.Skip("integration cleanup assertion")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		containerCommand := exec.CommandContext(
			ctx,
			"docker",
			"ps",
			"-aq",
			"--filter",
			"label="+managedWorkerLabel,
		)
		workers, err := containerCommand.CombinedOutput()
		if err != nil {
			t.Fatalf("list managed Worker containers: %v: %s", err, workers)
		}
		populatorCommand := exec.CommandContext(
			ctx,
			"docker",
			"ps",
			"-aq",
			"--filter",
			"label="+managedPopulatorLabel,
		)
		populators, err := populatorCommand.CombinedOutput()
		if err != nil {
			t.Fatalf("list managed populator containers: %v: %s", err, populators)
		}
		volumeCommand := exec.CommandContext(
			ctx,
			"docker",
			"volume",
			"ls",
			"-q",
			"--filter",
			"label="+managedVolumeLabel,
		)
		volumes, err := volumeCommand.CombinedOutput()
		if err != nil {
			t.Fatalf("list managed runtime secret volumes: %v: %s", err, volumes)
		}
		if len(bytes.TrimSpace(workers)) != 0 ||
			len(bytes.TrimSpace(populators)) != 0 ||
			len(bytes.TrimSpace(volumes)) != 0 {
			t.Errorf(
				"managed runtime resources remain after integration: workers=%q populators=%q volumes=%q",
				workers,
				populators,
				volumes,
			)
		}
	})
}
