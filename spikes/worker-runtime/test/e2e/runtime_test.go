package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
)

const (
	turnOnePrompt              = "Inspect this Go repository, identify why the existing tests fail, fix the production code, and run go test ./.... Do not add or change tests in this turn. Finish with a concise report of the change and verification."
	turnOneAcceptanceCriterion = "The production defect is fixed, go test ./... passes, and Turn 1 does not add or change tests."
	turnTwoPrompt              = "Continue from the current session and working tree. Add a focused boundary test proving that adding a zero delta leaves the value unchanged. Adjust production code only if the new test exposes a defect, run go test ./..., and report the result. Do not restate or redo the first-turn fix."
	invalidUpstreamCredential  = "switchback-task14-invalid-upstream-credential-v1"
	checksumProxyNetwork       = "switchback-spike-control"
	checksumProxyAlias         = "switchback-task14-checksum-proxy"
	checksumProxyUpstream      = "http://spike-backend:8080"
)

const checksumFaultProxyProgram = `#!/usr/bin/env python3
import hashlib
import http.server
import json
import os
import re
import threading
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

UPSTREAM = "http://spike-backend:8080"
STATE_PATH = Path("/var/run/switchback/fault-proxy-state.json")
EVIDENCE_PATH = re.compile(r"^/internal/v1/sessions/([^/]+)/runs/([^/]+)/turns/([^/]+)/evidence/([^/]+)$")
state_lock = threading.Lock()
fault_claimed = False


def write_state(state):
    temporary = STATE_PATH.with_suffix(".tmp")
    temporary.write_text(json.dumps(state, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(temporary, 0o600)
    os.replace(temporary, STATE_PATH)


def exact_evidence_path(raw_path):
    parsed = urlsplit(raw_path)
    if parsed.query or parsed.fragment:
        return None
    return EVIDENCE_PATH.fullmatch(parsed.path)


def checksum_valid(value):
    if len(value) != 64:
        return False
    try:
        return len(bytes.fromhex(value)) == 32
    except ValueError:
        return False


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format, *_args):
        return

    def reject_non_put(self):
        if exact_evidence_path(self.path):
            self.send_error(405, "only evidence PUT is accepted")
        else:
            self.send_error(404, "not found")

    do_GET = reject_non_put
    do_POST = reject_non_put
    do_DELETE = reject_non_put
    do_PATCH = reject_non_put
    do_HEAD = reject_non_put
    do_OPTIONS = reject_non_put

    def do_PUT(self):
        global fault_claimed
        matched = exact_evidence_path(self.path)
        if matched is None:
            self.send_error(404, "not found")
            return
        authorization_present = bool(self.headers.get("Authorization", "").strip())
        if not authorization_present:
            self.send_error(401, "worker authorization is required")
            return
        try:
            content_length = int(self.headers.get("Content-Length", ""))
        except ValueError:
            self.send_error(400, "content length is required")
            return
        if content_length < 0 or content_length > (32 << 20):
            self.send_error(413, "evidence payload exceeds proxy limit")
            return
        original_body = self.rfile.read(content_length)
        original_checksum = self.headers.get("X-Content-SHA256", "")
        original_checksum_valid = checksum_valid(original_checksum)
        with state_lock:
            inject_fault = not fault_claimed
            if inject_fault:
                fault_claimed = True
        mutated_body = original_body
        if inject_fault:
            if mutated_body:
                mutated_body = bytes([mutated_body[0] ^ 0xff]) + mutated_body[1:]
            else:
                mutated_body = b"\x01"
        headers = {
            name: value
            for name, value in self.headers.items()
            if name.lower() not in {"host", "content-length", "connection"}
        }
        upstream_request = urllib.request.Request(
            UPSTREAM + urlsplit(self.path).path,
            data=mutated_body,
            headers=headers,
            method="PUT",
        )
        try:
            with urllib.request.urlopen(upstream_request, timeout=30) as response:
                backend_status = response.status
                backend_headers = response.headers
                backend_body = response.read(1 << 20)
        except urllib.error.HTTPError as response:
            backend_status = response.code
            backend_headers = response.headers
            backend_body = response.read(1 << 20)
        except (OSError, urllib.error.URLError):
            self.send_error(502, "Backend request failed")
            return
        backend_error_code = ""
        if backend_status == 400:
            try:
                if json.loads(backend_body.decode("utf-8")) == {"error": "evidence checksum mismatch"}:
                    backend_error_code = "evidence_checksum_mismatch"
            except (UnicodeDecodeError, json.JSONDecodeError):
                pass
        if inject_fault:
            mutated_digest = hashlib.sha256(mutated_body).hexdigest()
            digest_mismatch = (
                original_checksum_valid
                and mutated_digest.lower() != original_checksum.lower()
            )
            proven = (
                authorization_present
                and original_checksum_valid
                and digest_mismatch
                and backend_status == 400
                and backend_error_code == "evidence_checksum_mismatch"
            )
            write_state({
                "ready": True,
                "fault_triggered": proven,
                "fault_count": 1 if proven else 0,
                "checksum_request_matched": True,
                "evidence_path_sha256": hashlib.sha256(urlsplit(self.path).path.encode()).hexdigest(),
                "evidence_kind": matched.group(4),
                "authorization_present": authorization_present,
                "original_checksum_valid": original_checksum_valid,
                "original_checksum_sha256": original_checksum,
                "mutated_body_sha256": mutated_digest,
                "digest_mismatch": digest_mismatch,
                "backend_status_code": backend_status,
                "backend_error_code": backend_error_code,
            })
        self.send_response(backend_status)
        content_type = backend_headers.get("Content-Type")
        if content_type:
            self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(backend_body)))
        self.end_headers()
        self.wfile.write(backend_body)


write_state({
    "ready": True,
    "fault_triggered": False,
    "fault_count": 0,
})
http.server.ThreadingHTTPServer(("", 8080), Handler).serve_forever()
`

type runtimeIdentity struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
	AgentSessionID    string
	ContainerID       string
	WorkingCopy       string
	AgentTurnID       string
	TraceID           string
}

type langfuseExpectation struct {
	TraceID               string
	TurnIDs               []string
	ModelObservationCount int
}

type langfuseTrace struct {
	ID           string                `json:"id"`
	Observations []langfuseObservation `json:"observations"`
}

type langfuseObservation struct {
	ID                  string `json:"id"`
	TraceID             string `json:"traceId"`
	Type                string `json:"type"`
	Name                string `json:"name"`
	ParentObservationID string `json:"parentObservationId"`
}

type expectedGeneration struct {
	ID           string
	ParentTurnID string
}

type knownToken struct {
	Label string
	Value string
}

type createdRun struct {
	WorkerSessionID string `json:"worker_session_id"`
	WorkerRunID     string `json:"worker_run_id"`
	TurnID          string `json:"turn_id"`
}

type createRunRequest struct {
	Goal               string   `json:"goal"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

type createdTurn struct {
	WorkerRunID string `json:"worker_run_id"`
	TurnID      string `json:"turn_id"`
}

type runSnapshot struct {
	WorkerSessionID       string   `json:"worker_session_id"`
	WorkerRunID           string   `json:"worker_run_id"`
	RuntimeInstanceID     string   `json:"runtime_instance_id"`
	RuntimeGeneration     uint32   `json:"runtime_generation"`
	AgentSessionID        string   `json:"agent_session_id"`
	TraceID               string   `json:"trace_id"`
	Goal                  string   `json:"goal"`
	AcceptanceCriteria    []string `json:"acceptance_criteria"`
	State                 string   `json:"state"`
	ExecutionOutcome      string   `json:"execution_outcome"`
	SpikeAcceptance       string   `json:"spike_acceptance"`
	ModelObservationCount int      `json:"model_observation_count"`
	Turns                 []struct {
		ID string `json:"id"`
	} `json:"turns"`
	Diagnostics []struct {
		Kind   string `json:"kind"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"diagnostics"`
}

type rawACPRecord struct {
	Type       string `json:"type"`
	Direction  string `json:"direction"`
	Frame      string `json:"frame"`
	UpdateType string `json:"update_type"`
}

type normalizedTraceRecord struct {
	SchemaVersion   string                      `json:"schema_version"`
	Sequence        uint64                      `json:"sequence"`
	ServerTimestamp string                      `json:"server_timestamp"`
	WorkerTimestamp string                      `json:"worker_timestamp"`
	Identity        sharedevidence.TurnIdentity `json:"identity"`
	Kind            string                      `json:"kind"`
	Payload         json.RawMessage             `json:"payload"`
}

type rpcFrame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type inspectedContainer struct {
	ID     string `json:"Id"`
	Config struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode     string                 `json:"NetworkMode"`
		Privileged      bool                   `json:"Privileged"`
		PublishAllPorts bool                   `json:"PublishAllPorts"`
		ReadonlyRootfs  bool                   `json:"ReadonlyRootfs"`
		PortBindings    map[string]interface{} `json:"PortBindings"`
		CapDrop         []string               `json:"CapDrop"`
		CapAdd          []string               `json:"CapAdd"`
		NanoCPUs        int64                  `json:"NanoCpus"`
		Memory          int64                  `json:"Memory"`
		PidsLimit       int64                  `json:"PidsLimit"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

type processStatus struct {
	PID    int    `json:"pid"`
	UID    int    `json:"uid"`
	Name   string `json:"name"`
	Cmd    string `json:"cmd"`
	CapPrm string `json:"cap_prm"`
	CapEff string `json:"cap_eff"`
}

type secretBoundaryReport struct {
	BootstrapUID      int                     `json:"bootstrap_uid"`
	BootstrapGID      int                     `json:"bootstrap_gid"`
	BootstrapMode     int                     `json:"bootstrap_mode"`
	SecretUID         int                     `json:"secret_uid"`
	SecretGID         int                     `json:"secret_gid"`
	SecretMode        int                     `json:"secret_mode"`
	ProcessScanErrors int                     `json:"process_scan_errors"`
	Processes         []secretBoundaryProcess `json:"processes"`
}

type secretBoundaryProcess struct {
	UID                 int      `json:"uid"`
	Role                string   `json:"role"`
	ForbiddenNameLabels []string `json:"forbidden_name_labels"`
	KnownValueLabel     bool     `json:"known_value_label"`
	TrustedUpstreamKey  bool     `json:"trusted_upstream_key"`
}

type acceptanceSummary struct {
	SchemaVersion       string   `json:"schema_version"`
	FailureVariant      string   `json:"failure_variant,omitempty"`
	WorkerSessionID     string   `json:"worker_session_id"`
	WorkerRunID         string   `json:"worker_run_id"`
	RuntimeInstanceID   string   `json:"runtime_instance_id"`
	RuntimeGeneration   uint32   `json:"runtime_generation"`
	AgentSessionID      string   `json:"agent_session_id"`
	ContainerID         string   `json:"container_id"`
	AgentTurnIDs        []string `json:"agent_turn_ids"`
	TraceID             string   `json:"trace_id"`
	ExecutionOutcome    string   `json:"execution_outcome"`
	SpikeAcceptance     string   `json:"spike_acceptance"`
	FixtureDigestBefore string   `json:"fixture_digest_before"`
	FixtureDigestAfter  string   `json:"fixture_digest_after"`
	EvidenceVerified    bool     `json:"evidence_verified"`
	TokenAbsence        bool     `json:"token_absence"`
	LangfuseVerified    bool     `json:"langfuse_verified"`
	CleanupConfirmed    bool     `json:"cleanup_confirmed"`
}

type runtimeResourceIdentity struct {
	WorkerSessionID   string `json:"worker_session_id"`
	WorkerRunID       string `json:"worker_run_id"`
	RuntimeInstanceID string `json:"runtime_instance_id"`
	RuntimeGeneration uint32 `json:"runtime_generation"`
	ContainerID       string `json:"container_id"`
}

type runtimeResourceSet struct {
	Workers    []string
	Populators []string
	Volumes    []string
}

func (r runtimeResourceSet) empty() bool {
	return len(r.Workers) == 0 && len(r.Populators) == 0 && len(r.Volumes) == 0
}

type faultProxySnapshot struct {
	Ready                  bool   `json:"ready"`
	ProxySocket            string `json:"proxy_socket,omitempty"`
	TargetContainerID      string `json:"target_container_id,omitempty"`
	FaultTriggered         bool   `json:"fault_triggered"`
	FaultCount             int    `json:"fault_count"`
	ChecksumRequestMatched bool   `json:"checksum_request_matched,omitempty"`
	EvidencePathSHA256     string `json:"evidence_path_sha256,omitempty"`
	EvidenceKind           string `json:"evidence_kind,omitempty"`
	AuthorizationPresent   bool   `json:"authorization_present,omitempty"`
	OriginalChecksumValid  bool   `json:"original_checksum_valid,omitempty"`
	OriginalChecksumSHA256 string `json:"original_checksum_sha256,omitempty"`
	MutatedBodySHA256      string `json:"mutated_body_sha256,omitempty"`
	DigestMismatch         bool   `json:"digest_mismatch,omitempty"`
	BackendStatusCode      int    `json:"backend_status_code,omitempty"`
	BackendErrorCode       string `json:"backend_error_code,omitempty"`
}

type faultProxyState struct {
	mu              sync.Mutex
	snapshotValue   faultProxySnapshot
	checksumClaimed bool
	onChange        func(faultProxySnapshot)
}

func (s *faultProxyState) snapshot() faultProxySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotValue
}

func (s *faultProxyState) update(mutate func(*faultProxySnapshot)) {
	s.mu.Lock()
	mutate(&s.snapshotValue)
	snapshot := s.snapshotValue
	onChange := s.onChange
	s.mu.Unlock()
	if onChange != nil {
		onChange(snapshot)
	}
}

func (s *faultProxyState) claimChecksumAttempt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checksumClaimed {
		return false
	}
	s.checksumClaimed = true
	return true
}

func (s *faultProxyState) recordChecksumResult(result faultProxySnapshot) {
	s.update(func(snapshot *faultProxySnapshot) {
		snapshot.ChecksumRequestMatched = result.ChecksumRequestMatched
		snapshot.EvidencePathSHA256 = result.EvidencePathSHA256
		snapshot.EvidenceKind = result.EvidenceKind
		snapshot.AuthorizationPresent = result.AuthorizationPresent
		snapshot.OriginalChecksumValid = result.OriginalChecksumValid
		snapshot.OriginalChecksumSHA256 = result.OriginalChecksumSHA256
		snapshot.MutatedBodySHA256 = result.MutatedBodySHA256
		snapshot.DigestMismatch = result.DigestMismatch
		snapshot.BackendStatusCode = result.BackendStatusCode
		snapshot.BackendErrorCode = result.BackendErrorCode
		proven := result.ChecksumRequestMatched &&
			result.AuthorizationPresent &&
			result.OriginalChecksumValid &&
			result.DigestMismatch &&
			result.BackendStatusCode == http.StatusBadRequest &&
			result.BackendErrorCode == "evidence_checksum_mismatch"
		snapshot.FaultTriggered = proven
		if proven {
			snapshot.FaultCount = 1
		} else {
			snapshot.FaultCount = 0
		}
	})
}

func (s *faultProxyState) setTargetContainer(containerID string) {
	s.update(func(snapshot *faultProxySnapshot) {
		if snapshot.TargetContainerID == "" {
			snapshot.TargetContainerID = containerID
		}
	})
}

func (s *faultProxyState) recordDockerCleanupFault() {
	s.update(func(snapshot *faultProxySnapshot) {
		snapshot.FaultTriggered = true
		snapshot.FaultCount++
	})
}

func TestWriteChecksumFaultProxyProgram(t *testing.T) {
	path := os.Getenv("SWITCHBACK_SPIKE_E2E_CHECKSUM_PROXY_PROGRAM")
	if path == "" {
		t.Skip("checksum proxy program is generated only by the E2E harness")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("checksum proxy program path must be absolute")
	}
	if err := writeChecksumFaultProxyProgram(path); err != nil {
		t.Fatal(err)
	}
}

func writeChecksumFaultProxyProgram(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create checksum proxy program directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(checksumFaultProxyProgram), 0o600); err != nil {
		return fmt.Errorf("write checksum proxy program: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish checksum proxy program: %w", err)
	}
	return nil
}

func TestE2EFaultProxyHelper(t *testing.T) {
	mode := os.Getenv("SWITCHBACK_SPIKE_E2E_HELPER_MODE")
	if mode == "" {
		t.Skip("fault proxy helper is started only by scripts/run-e2e")
	}
	stateFile := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE")
	stopFile := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_FAULT_STOP_FILE")
	var persistMu sync.Mutex
	persistErrors := make(chan error, 1)
	state := &faultProxyState{}
	state.onChange = func(snapshot faultProxySnapshot) {
		persistMu.Lock()
		err := writeStrictJSON(stateFile, snapshot, 0o600)
		persistMu.Unlock()
		if err != nil {
			select {
			case persistErrors <- err:
			default:
			}
		}
	}

	var (
		listener net.Listener
		handler  http.Handler
	)
	switch mode {
	case "checksum-mismatch":
		t.Fatal("checksum fault proxy must run as an isolated control-network container")
	case "docker-cleanup":
		realSocket := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_REAL_DOCKER_SOCKET")
		proxySocket := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_PROXY_DOCKER_SOCKET")
		if err := os.Remove(proxySocket); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove stale Docker proxy socket: %v", err)
		}
		var err error
		listener, err = net.Listen("unix", proxySocket)
		if err != nil {
			t.Fatalf("listen for Docker Engine fault proxy: %v", err)
		}
		defer os.Remove(proxySocket)
		if err := os.Chmod(proxySocket, 0o600); err != nil {
			_ = listener.Close()
			t.Fatalf("protect Docker Engine fault proxy socket: %v", err)
		}
		handler = newDockerEngineFaultProxy(realSocket, state)
		state.update(func(snapshot *faultProxySnapshot) {
			snapshot.Ready = true
			snapshot.ProxySocket = proxySocket
		})
	default:
		t.Fatalf("unsupported fault proxy helper mode %q", mode)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()
	if err := waitForFaultProxyStop(stopFile, serverErrors, persistErrors, 50*time.Minute); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		t.Fatalf("shutdown fault proxy helper: %v", err)
	}
}

func waitForFaultProxyStop(
	stopFile string,
	serverErrors <-chan error,
	persistErrors <-chan error,
	timeout time.Duration,
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-serverErrors:
			if err == http.ErrServerClosed {
				return nil
			}
			return fmt.Errorf("fault proxy server stopped unexpectedly")
		case <-persistErrors:
			return fmt.Errorf("persist fault proxy state")
		case <-deadline.C:
			return fmt.Errorf("fault proxy helper timed out")
		case <-ticker.C:
			if _, err := os.Stat(stopFile); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect fault proxy stop file")
			}
		}
	}
}

func TestRealCodexTwoTurnE2E(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_E2E") != "1" {
		t.Skip("set SWITCHBACK_SPIKE_E2E=1 through scripts/run-e2e")
	}
	root := spikeRoot(t)
	dataRoot := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_DATA_ROOT")
	stateFile := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_STATE_FILE")
	summaryFile := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_SUMMARY_FILE")
	tokenFile := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_TOKEN_FILE")
	backendURL := requiredURL(t, "SWITCHBACK_SPIKE_E2E_BACKEND_URL")
	langfuseURL := requiredURL(t, "SWITCHBACK_SPIKE_E2E_LANGFUSE_BASE_URL")
	langfusePublicKey := requiredEnv(t, "SWITCHBACK_SPIKE_E2E_LANGFUSE_PUBLIC_KEY")
	langfuseSecretKey := requiredEnv(t, "SWITCHBACK_SPIKE_E2E_LANGFUSE_SECRET_KEY")
	variant := os.Getenv("SWITCHBACK_SPIKE_E2E_VARIANT")
	switch variant {
	case "", "success", "invalid-upstream-credential", "langfuse-unavailable", "checksum-mismatch", "cleanup-failure":
	default:
		t.Fatalf("unsupported SWITCHBACK_SPIKE_E2E_VARIANT %q", variant)
	}
	fixtureRoot := filepath.Join(root, "fixtures", "go-repository")
	fixtureBefore := treeDigestPath(t, fixtureRoot)
	if configured := requiredEnv(t, "SWITCHBACK_SPIKE_E2E_FIXTURE_DIGEST"); configured != fixtureBefore {
		t.Fatalf("fixture digest before E2E does not match the harness record")
	}
	tokens := readKnownTokens(t, tokenFile)
	client := &http.Client{Timeout: 10 * time.Second}
	if variant == "invalid-upstream-credential" {
		verifyInvalidCredentialInjection(t, dataRoot)
	}

	created := createTurnOneRun(t, client, backendURL+"/spike/runs")
	if created.WorkerSessionID == "" || created.WorkerRunID == "" || created.TurnID == "" {
		t.Fatalf("Backend returned incomplete run identity")
	}
	cleaned := false
	cleanupIdentity := runtimeResourceIdentity{
		WorkerSessionID: created.WorkerSessionID,
		WorkerRunID:     created.WorkerRunID,
	}
	t.Cleanup(func() {
		if !cleaned {
			if err := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second); err != nil {
				t.Errorf(
					"cleanup Worker Session worker_session_id=%s worker_run_id=%s: %v",
					created.WorkerSessionID,
					created.WorkerRunID,
					err,
				)
			}
		}
	})
	writeE2EState(t, stateFile, cleanupIdentity)
	if variant == "invalid-upstream-credential" {
		failed := waitForRun(t, client, backendURL, created.WorkerRunID, func(snapshot runSnapshot) bool {
			return snapshot.State == "failed" && snapshot.ExecutionOutcome == "failed"
		})
		if err := verifyInvalidCredentialOutcome(failed); err != nil {
			t.Fatal(err)
		}
		cleanupIdentity = runtimeResourceIdentityFromSnapshot(failed, "")
		if err := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second); err != nil {
			t.Fatal(err)
		}
		cleaned = true
		fixtureAfter := treeDigestPath(t, fixtureRoot)
		if fixtureAfter != fixtureBefore {
			t.Fatalf("fixture digest changed during invalid-credential variant")
		}
		summary := acceptanceSummary{
			SchemaVersion:       "switchback.spike.acceptance.v1",
			FailureVariant:      variant,
			WorkerSessionID:     failed.WorkerSessionID,
			WorkerRunID:         failed.WorkerRunID,
			RuntimeInstanceID:   failed.RuntimeInstanceID,
			RuntimeGeneration:   failed.RuntimeGeneration,
			AgentSessionID:      failed.AgentSessionID,
			AgentTurnIDs:        []string{created.TurnID},
			TraceID:             failed.TraceID,
			ExecutionOutcome:    failed.ExecutionOutcome,
			SpikeAcceptance:     failed.SpikeAcceptance,
			FixtureDigestBefore: fixtureBefore,
			FixtureDigestAfter:  fixtureAfter,
			CleanupConfirmed:    true,
		}
		if err := writeStrictJSON(summaryFile, summary, 0o600); err != nil {
			t.Fatalf("write invalid-credential summary: %v", err)
		}
		return
	}
	if variant == "checksum-mismatch" {
		failed := waitForRun(t, client, backendURL, created.WorkerRunID, func(snapshot runSnapshot) bool {
			return snapshot.State == "failed" && snapshot.ExecutionOutcome == "failed"
		})
		if err := verifyChecksumMismatchOutcome(failed); err != nil {
			t.Fatal(err)
		}
		fault := readFaultProxyState(t)
		if !checksumFaultProven(fault) {
			t.Fatalf("checksum fault proxy did not prove exact Backend checksum rejection")
		}
		cleanupIdentity = runtimeResourceIdentityFromSnapshot(failed, "")
		if err := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second); err != nil {
			t.Fatal(err)
		}
		cleaned = true
		fixtureAfter := treeDigestPath(t, fixtureRoot)
		if fixtureAfter != fixtureBefore {
			t.Fatalf("fixture digest changed during checksum-mismatch variant")
		}
		summary := acceptanceSummary{
			SchemaVersion:       "switchback.spike.acceptance.v1",
			FailureVariant:      variant,
			WorkerSessionID:     failed.WorkerSessionID,
			WorkerRunID:         failed.WorkerRunID,
			RuntimeInstanceID:   failed.RuntimeInstanceID,
			RuntimeGeneration:   failed.RuntimeGeneration,
			AgentSessionID:      failed.AgentSessionID,
			AgentTurnIDs:        []string{created.TurnID},
			TraceID:             failed.TraceID,
			ExecutionOutcome:    failed.ExecutionOutcome,
			SpikeAcceptance:     failed.SpikeAcceptance,
			FixtureDigestBefore: fixtureBefore,
			FixtureDigestAfter:  fixtureAfter,
			CleanupConfirmed:    true,
		}
		if err := writeStrictJSON(summaryFile, summary, 0o600); err != nil {
			t.Fatalf("write checksum-mismatch summary: %v", err)
		}
		return
	}

	first := waitForRun(t, client, backendURL, created.WorkerRunID, func(snapshot runSnapshot) bool {
		return snapshot.State == "idle"
	})
	turnOneRequest := turnOneCreateRunRequest()
	if first.Goal != turnOnePrompt ||
		!reflect.DeepEqual(first.AcceptanceCriteria, turnOneRequest.AcceptanceCriteria) ||
		first.ExecutionOutcome != "pending" ||
		first.SpikeAcceptance != "pending" ||
		len(first.Turns) != 1 ||
		first.Turns[0].ID != created.TurnID ||
		first.ModelObservationCount < 1 {
		t.Fatalf("first Turn snapshot does not satisfy the exact contract: %+v", first)
	}
	containerID := findExactWorkerContainer(t, created.WorkerSessionID)
	cleanupIdentity = runtimeResourceIdentityFromSnapshot(first, containerID)
	writeE2EState(t, stateFile, cleanupIdentity)
	workingCopy := filepath.Join(dataRoot, "sessions", created.WorkerSessionID, "working-copy")
	firstIdentity := identityFromSnapshot(first, created.TurnID, containerID, workingCopy)
	if err := verifyWorkerIsolation(containerID, dataRoot, first, tokens); err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveProcessCapabilities(containerID, tokens); err != nil {
		t.Fatal(err)
	}
	runWorkingCopyTests(t, workingCopy)
	runRoot := filepath.Join(dataRoot, "objects", created.WorkerSessionID, created.WorkerRunID)
	if err := verifyTurnEvidence(runRoot, firstIdentity, turnOnePrompt, true); err != nil {
		t.Fatal(err)
	}
	if variant == "langfuse-unavailable" {
		stopComposeService(t, dataRoot, "langfuse-web")
	}

	secondCreated := postJSON[createdTurn](
		t,
		client,
		backendURL+"/spike/sessions/"+created.WorkerSessionID+"/turns",
		map[string]string{"message": turnTwoPrompt},
	)
	if secondCreated.WorkerRunID != created.WorkerRunID ||
		secondCreated.TurnID == "" ||
		secondCreated.TurnID == created.TurnID {
		t.Fatalf("second Turn identity is not incremental: %+v", secondCreated)
	}
	completed := waitForRun(t, client, backendURL, created.WorkerRunID, func(snapshot runSnapshot) bool {
		if variant == "langfuse-unavailable" {
			return snapshot.State == "completed" &&
				snapshot.ExecutionOutcome == "completed" &&
				snapshot.SpikeAcceptance == "failed"
		}
		return snapshot.State == "completed" &&
			snapshot.ExecutionOutcome == "completed" &&
			snapshot.SpikeAcceptance == "passed"
	})
	if variant == "langfuse-unavailable" {
		if err := verifyLangfuseUnavailableOutcome(completed); err != nil {
			t.Fatal(err)
		}
	}
	if len(completed.Turns) != 2 ||
		completed.Turns[0].ID != created.TurnID ||
		completed.Turns[1].ID != secondCreated.TurnID ||
		completed.ModelObservationCount < 2 {
		t.Fatalf("completed run does not contain the exact two Turns: %+v", completed)
	}
	secondContainerID := findExactWorkerContainer(t, created.WorkerSessionID)
	secondIdentity := identityFromSnapshot(completed, secondCreated.TurnID, secondContainerID, workingCopy)
	if err := verifyRuntimeContinuity(firstIdentity, secondIdentity); err != nil {
		t.Fatal(err)
	}
	runWorkingCopyTests(t, workingCopy)
	if err := verifyTurnEvidence(runRoot, secondIdentity, turnTwoPrompt, false); err != nil {
		t.Fatal(err)
	}
	if err := verifyZeroDeltaMutationProof(workingCopy); err != nil {
		t.Fatal(err)
	}
	if err := verifyFullACPTrace(
		filepath.Join(dataRoot, "sessions", created.WorkerSessionID, "trace-spool", "acp.jsonl"),
		completed.AgentSessionID,
		[]string{created.TurnID, secondCreated.TurnID},
	); err != nil {
		t.Fatal(err)
	}

	var traceResponse []byte
	langfuseVerified := false
	if variant != "langfuse-unavailable" {
		trace, response := waitForLangfuseTrace(
			t,
			client,
			langfuseURL,
			langfusePublicKey,
			langfuseSecretKey,
			completed.TraceID,
		)
		traceResponse = response
		generations, err := verifyLangfuseTrace(trace, langfuseExpectation{
			TraceID:               completed.TraceID,
			TurnIDs:               []string{created.TurnID, secondCreated.TurnID},
			ModelObservationCount: completed.ModelObservationCount,
		})
		if err != nil {
			t.Fatal(err)
		}
		runLangfuseVerifier(
			t,
			root,
			langfuseURL,
			langfusePublicKey,
			langfuseSecretKey,
			completed,
			[]string{created.TurnID, secondCreated.TurnID},
			generations,
		)
		langfuseVerified = true
	}

	sources := collectPersistedSources(t, runRoot, filepath.Join(dataRoot, "sessions", created.WorkerSessionID))
	sources["dynamic Worker logs"] = []byte(dockerCommand(t, "logs", containerID))
	sources["dynamic Worker inspect"] = []byte(dockerCommand(t, "inspect", containerID))
	sources["Compose logs"] = []byte(composeLogs(t, dataRoot))
	if len(traceResponse) > 0 {
		sources["Langfuse response"] = traceResponse
	}
	if err := verifyTokenAbsence(sources, tokens); err != nil {
		t.Fatal(err)
	}

	cleanupIdentity = runtimeResourceIdentityFromSnapshot(completed, containerID)
	cleanupErr := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second)
	if variant == "cleanup-failure" {
		if cleanupErr == nil {
			t.Fatal("cleanup-failure variant did not reject target Worker removal")
		}
		fault := readFaultProxyState(t)
		if !fault.FaultTriggered ||
			fault.TargetContainerID == "" ||
			fault.TargetContainerID != containerID {
			t.Fatal("cleanup-failure variant did not target the exact dynamic Worker")
		}
		cleaned = true
		fixtureAfter := treeDigestPath(t, fixtureRoot)
		if fixtureAfter != fixtureBefore {
			t.Fatalf("fixture digest changed during cleanup-failure variant")
		}
		summary := acceptanceSummary{
			SchemaVersion:       "switchback.spike.acceptance.v1",
			FailureVariant:      variant,
			WorkerSessionID:     completed.WorkerSessionID,
			WorkerRunID:         completed.WorkerRunID,
			RuntimeInstanceID:   completed.RuntimeInstanceID,
			RuntimeGeneration:   completed.RuntimeGeneration,
			AgentSessionID:      completed.AgentSessionID,
			ContainerID:         containerID,
			AgentTurnIDs:        []string{created.TurnID, secondCreated.TurnID},
			TraceID:             completed.TraceID,
			ExecutionOutcome:    completed.ExecutionOutcome,
			SpikeAcceptance:     "failed",
			FixtureDigestBefore: fixtureBefore,
			FixtureDigestAfter:  fixtureAfter,
			EvidenceVerified:    true,
			TokenAbsence:        true,
			LangfuseVerified:    langfuseVerified,
			CleanupConfirmed:    false,
		}
		if err := writeStrictJSON(summaryFile, summary, 0o600); err != nil {
			t.Fatalf("write cleanup-failure summary: %v", err)
		}
		return
	}
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	cleaned = true
	fixtureAfter := treeDigestPath(t, fixtureRoot)
	if fixtureAfter != fixtureBefore {
		t.Fatalf("fixture digest changed: before %s after %s", fixtureBefore, fixtureAfter)
	}
	summary := acceptanceSummary{
		SchemaVersion:       "switchback.spike.acceptance.v1",
		FailureVariant:      variant,
		WorkerSessionID:     completed.WorkerSessionID,
		WorkerRunID:         completed.WorkerRunID,
		RuntimeInstanceID:   completed.RuntimeInstanceID,
		RuntimeGeneration:   completed.RuntimeGeneration,
		AgentSessionID:      completed.AgentSessionID,
		ContainerID:         containerID,
		AgentTurnIDs:        []string{created.TurnID, secondCreated.TurnID},
		TraceID:             completed.TraceID,
		ExecutionOutcome:    completed.ExecutionOutcome,
		SpikeAcceptance:     completed.SpikeAcceptance,
		FixtureDigestBefore: fixtureBefore,
		FixtureDigestAfter:  fixtureAfter,
		EvidenceVerified:    true,
		TokenAbsence:        true,
		LangfuseVerified:    langfuseVerified,
		CleanupConfirmed:    true,
	}
	if err := writeStrictJSON(summaryFile, summary, 0o600); err != nil {
		t.Fatalf("write acceptance summary: %v", err)
	}
}

func TestRunE2EStopsAfterPreflightFailure(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "data")
	statePath := filepath.Join(dataRoot, "acceptance", "e2e-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		statePath,
		[]byte("{\"worker_session_id\":\"stale-session\"}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *preflight* ]]; then
	exit 23
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 0
`)
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 99
`)

	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
	})
	if err == nil {
		t.Fatalf("run-e2e succeeded after preflight failure:\n%s", output)
	}
	log := readFile(t, logPath)
	if got, want := strings.Count(log, "make "), 1; got != want {
		t.Fatalf("make calls = %d, want %d:\n%s", got, want, log)
	}
	if !strings.Contains(log, "preflight") {
		t.Fatalf("preflight was not invoked first:\n%s", log)
	}
	for _, forbidden := range []string{" build", " compose up", " container create", " container start"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("preflight failure reached forbidden action %q:\n%s", forbidden, log)
		}
	}
	if strings.Contains(log, "curl ") {
		t.Fatalf("preflight failure attempted cleanup from stale state:\n%s", log)
	}
}

func TestRunE2EFaultVariantsReachPreflight(t *testing.T) {
	for _, variant := range []string{"checksum-mismatch", "cleanup-failure"} {
		t.Run(variant, func(t *testing.T) {
			root := spikeRoot(t)
			tempRoot := t.TempDir()
			binDir := filepath.Join(tempRoot, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(tempRoot, "commands.log")
			writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 23
`)
			for _, name := range []string{"docker", "curl", "go"} {
				writeExecutable(t, binDir, name, `#!/usr/bin/env bash
printf 'unexpected %s\n' "$0" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 99
`)
			}
			output, err := runHarness(t, root, binDir, logPath, []string{
				"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + filepath.Join(tempRoot, "data"),
				"SWITCHBACK_SPIKE_E2E_VARIANT=" + variant,
			})
			if err == nil {
				t.Fatalf("%s unexpectedly passed a failed preflight", variant)
			}
			log, readErr := os.ReadFile(logPath)
			if readErr != nil || string(log) != "make preflight\n" {
				t.Fatalf("%s did not reach only preflight; log=%q err=%v output=%s", variant, log, readErr, output)
			}
		})
	}
}

func TestRealE2ERegistersCleanupBeforeStatePersistence(t *testing.T) {
	cleanupRequested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/spike/runs":
			writeTestJSON(writer, http.StatusAccepted, createdRun{
				WorkerSessionID: "session-state-failure",
				WorkerRunID:     "run-state-failure",
				TurnID:          "turn-state-failure",
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == "/spike/sessions/session-state-failure/cleanup":
			select {
			case cleanupRequested <- struct{}{}:
			default:
			}
			writeTestJSON(writer, http.StatusAccepted, map[string]string{"status": "cleanup_scheduled"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := spikeRoot(t)
	dataRoot := t.TempDir()
	tokenFile := filepath.Join(dataRoot, "known-tokens.tsv")
	if err := os.WriteFile(tokenFile, []byte("token-1\ttest-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedParent := filepath.Join(dataRoot, "state-parent-is-a-file")
	if err := os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRealCodexTwoTurnE2E$", "-test.count=1")
	command.Dir = filepath.Join(root, "test", "e2e")
	command.Env = append(environmentWithout(
		"SWITCHBACK_SPIKE_E2E",
		"SWITCHBACK_SPIKE_E2E_DATA_ROOT",
		"SWITCHBACK_SPIKE_E2E_STATE_FILE",
		"SWITCHBACK_SPIKE_E2E_SUMMARY_FILE",
		"SWITCHBACK_SPIKE_E2E_TOKEN_FILE",
		"SWITCHBACK_SPIKE_E2E_BACKEND_URL",
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_BASE_URL",
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_PUBLIC_KEY",
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_SECRET_KEY",
		"SWITCHBACK_SPIKE_E2E_FIXTURE_DIGEST",
		"SWITCHBACK_SPIKE_E2E_VARIANT",
	),
		"SWITCHBACK_SPIKE_E2E=1",
		"SWITCHBACK_SPIKE_E2E_DATA_ROOT="+dataRoot,
		"SWITCHBACK_SPIKE_E2E_STATE_FILE="+filepath.Join(blockedParent, "state.json"),
		"SWITCHBACK_SPIKE_E2E_SUMMARY_FILE="+filepath.Join(dataRoot, "summary.json"),
		"SWITCHBACK_SPIKE_E2E_TOKEN_FILE="+tokenFile,
		"SWITCHBACK_SPIKE_E2E_BACKEND_URL="+server.URL,
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_BASE_URL="+server.URL,
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_PUBLIC_KEY=pk-test",
		"SWITCHBACK_SPIKE_E2E_LANGFUSE_SECRET_KEY=sk-test",
		"SWITCHBACK_SPIKE_E2E_FIXTURE_DIGEST="+treeDigestPath(t, filepath.Join(root, "fixtures", "go-repository")),
		"SWITCHBACK_SPIKE_E2E_VARIANT=success",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("E2E child unexpectedly persisted state through a non-directory parent:\n%s", output)
	}
	select {
	case <-cleanupRequested:
	case <-time.After(2 * time.Second):
		t.Fatalf("Session cleanup was not registered before state persistence failed:\n%s", output)
	}
}

func TestRunE2EFailureTrapCleansSessionAndReportsLeakedWorker(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "data")
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *build* ]]; then
	mkdir -p "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env"
	cat >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" <<'EOF'
SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080
SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000
SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030
LANGFUSE_PUBLIC_KEY=lf_pk_test
LANGFUSE_SECRET_KEY=lf_sk_test
POSTGRES_PASSWORD=postgres-test
EOF
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=worker-runtime"* ]]; then
	printf 'leaked-container-id\n'
	exit 0
fi
exit 0
`)
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
printf '{"status":"ok"}\n'
exit 0
`)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-leak","worker_run_id":"run-leak","runtime_instance_id":"runtime-leak","runtime_generation":1,"container_id":"leaked-container-id"}
JSON
exit 17
`)

	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
	})
	if err == nil {
		t.Fatalf("run-e2e succeeded after E2E runner failure:\n%s", output)
	}
	log := readFile(t, logPath)
	for _, required := range []string{
		"make preflight",
		"make build",
		"docker compose",
		"go test ./test/e2e",
		"/spike/sessions/session-leak/cleanup",
		"down --remove-orphans",
	} {
		if !strings.Contains(log, required) {
			t.Errorf("command log is missing %q:\n%s", required, log)
		}
	}
	if !strings.Contains(output, "leaked-container-id") {
		t.Fatalf("cleanup failure did not report leaked identity:\n%s", output)
	}
}

func TestRunE2ECleanupFailsClosedWhenWorkerEnumerationFails(t *testing.T) {
	output, err := runSuccessfulFakeLifecycleHarness(t, `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=worker-runtime"* ]]; then
	exit 44
fi
exit 0
`)
	if err == nil {
		t.Fatalf("run-e2e accepted an unprovable dynamic Worker cleanup:\n%s", output)
	}
	if !strings.Contains(output, "runtime resource enumeration failed") {
		t.Fatalf("run-e2e did not classify the failed runtime-resource enumeration:\n%s", output)
	}
}

func TestRunE2ECleanupFailsClosedWhenComposeDownFails(t *testing.T) {
	output, err := runSuccessfulFakeLifecycleHarness(t, `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "compose" && "$*" == *" down --remove-orphans"* ]]; then
	exit 45
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=worker-runtime"* ]]; then
	exit 0
fi
if [[ "$1" == "ps" && "$*" == *"com.docker.compose.project=switchback-worker-runtime-spike"* ]]; then
	printf 'static-container-id\n'
	exit 0
fi
exit 0
`)
	if err == nil {
		t.Fatalf("run-e2e accepted failed Compose shutdown:\n%s", output)
	}
	for _, want := range []string{"Compose down failed", "static-container-id"} {
		if !strings.Contains(output, want) {
			t.Fatalf("run-e2e output is missing %q:\n%s", want, output)
		}
	}
}

func TestRunE2ECleanupFailsClosedWhenSessionCleanupPOSTFails(t *testing.T) {
	output, err := runSuccessfulFakeLifecycleHarnessWithCurl(
		t,
		successfulFakeDockerScript(),
		`#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *"/spike/sessions/session-cleanup-proof/cleanup"* ]]; then
	exit 46
fi
printf '{"status":"ok"}\n'
exit 0
`,
	)
	if err == nil {
		t.Fatalf("run-e2e accepted failed explicit Session cleanup:\n%s", output)
	}
	if !strings.Contains(output, "explicit Session cleanup POST failed") ||
		!strings.Contains(output, "worker_session_id=session-cleanup-proof") {
		t.Fatalf("run-e2e did not report the failed Session cleanup and identity:\n%s", output)
	}
}

func TestRunE2ECleanupFailsClosedOnPostDownDynamicResidue(t *testing.T) {
	output, err := runSuccessfulFakeLifecycleHarness(t, `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=worker-runtime"* ]]; then
	count_file="$SWITCHBACK_SPIKE_HOST_DATA_ROOT/dynamic-enumeration-count"
	count=0
	[[ -f "$count_file" ]] && count="$(cat "$count_file")"
	count=$((count + 1))
	printf '%s' "$count" >"$count_file"
	if ((count >= 2)); then
		printf 'post-down-worker-id\n'
	fi
	exit 0
fi
exit 0
`)
	if err == nil {
		t.Fatalf("run-e2e accepted a post-down dynamic Worker residue:\n%s", output)
	}
	if !strings.Contains(output, "post-down remaining runtime resources: worker:post-down-worker-id") {
		t.Fatalf("run-e2e did not report the post-down Worker identity:\n%s", output)
	}
}

func TestRunE2ECleanupFailsClosedOnSecretResourceResidue(t *testing.T) {
	tests := []struct {
		name       string
		dockerCase string
		want       string
	}{
		{
			name: "populator residue",
			dockerCase: `if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=runtime-secret-populator"* ]]; then
	printf 'populator-residue-id\n'
	exit 0
fi`,
			want: "populator-residue-id",
		},
		{
			name: "volume residue",
			dockerCase: `if [[ "$1" == "volume" && "$2" == "ls" && "$*" == *"com.switchback.managed=runtime-secret-volume"* ]]; then
	printf 'secret-volume-residue\n'
	exit 0
fi`,
			want: "secret-volume-residue",
		},
		{
			name: "volume enumeration failure",
			dockerCase: `if [[ "$1" == "volume" && "$2" == "ls" && "$*" == *"com.switchback.managed=runtime-secret-volume"* ]]; then
	exit 47
fi`,
			want: "runtime resource enumeration failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
` + test.dockerCase + `
exit 0
`
			output, err := runSuccessfulFakeLifecycleHarness(t, script)
			if err == nil {
				t.Fatalf("run-e2e accepted unproven secret-resource cleanup:\n%s", output)
			}
			if !strings.Contains(output, test.want) {
				t.Fatalf("run-e2e output is missing %q:\n%s", test.want, output)
			}
		})
	}
}

func TestCleanupRuntimeUsesWorkerPopulatorAndVolumeEnumeration(t *testing.T) {
	tests := []struct {
		name       string
		dockerCase string
		want       string
	}{
		{
			name: "populator residue",
			dockerCase: `if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=runtime-secret-populator"* ]]; then
	printf 'populator-residue-id\n'
fi`,
			want: "populator-residue-id",
		},
		{
			name: "volume residue",
			dockerCase: `if [[ "$1" == "volume" && "$2" == "ls" ]]; then
	printf 'secret-volume-residue\n'
fi`,
			want: "secret-volume-residue",
		},
		{
			name: "volume enumeration failure",
			dockerCase: `if [[ "$1" == "volume" && "$2" == "ls" ]]; then
	exit 48
fi`,
			want: "enumerate runtime resources",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binDir := t.TempDir()
			writeExecutable(t, binDir, "docker", "#!/usr/bin/env bash\n"+test.dockerCase+"\nexit 0\n")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost ||
					request.URL.Path != "/spike/sessions/session-cleanup/cleanup" {
					http.NotFound(writer, request)
					return
				}
				writeTestJSON(writer, http.StatusAccepted, map[string]string{"status": "accepted"})
			}))
			defer server.Close()

			err := cleanupRuntime(
				&http.Client{Timeout: time.Second},
				server.URL,
				runtimeResourceIdentity{
					WorkerSessionID:   "session-cleanup",
					WorkerRunID:       "run-cleanup",
					RuntimeInstanceID: "runtime-cleanup",
					RuntimeGeneration: 1,
				},
				0,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("cleanupRuntime() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRemoveExactRuntimeResourcesRejectsMismatchedLabelsBeforeRemoval(t *testing.T) {
	binDir := t.TempDir()
	removeMarker := filepath.Join(t.TempDir(), "remove-called")
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
if [[ "$1" == "ps" && "$*" == *"com.switchback.managed=worker-runtime"* ]]; then
	printf 'worker-with-replaced-labels\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"worker-with-replaced-labels"* ]]; then
	printf '%s\n' '{"com.switchback.managed":"worker-runtime","com.switchback.worker-session-id":"session-cleanup","com.switchback.worker-run-id":"replacement-run","com.switchback.runtime-instance-id":"runtime-cleanup","com.switchback.runtime-generation":"1"}'
	exit 0
fi
if [[ "$1" == "rm" ]]; then
	: >"$SWITCHBACK_REMOVE_MARKER"
	exit 0
fi
exit 0
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SWITCHBACK_REMOVE_MARKER", removeMarker)

	err := removeExactRuntimeResources(runtimeResourceIdentity{
		WorkerSessionID:   "session-cleanup",
		WorkerRunID:       "run-cleanup",
		RuntimeInstanceID: "runtime-cleanup",
		RuntimeGeneration: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "labels do not match exact runtime identity") {
		t.Fatalf("removeExactRuntimeResources() error = %v, want label mismatch", err)
	}
	if _, statErr := os.Stat(removeMarker); !os.IsNotExist(statErr) {
		t.Fatalf("fallback removed a mismatched resource: stat error = %v", statErr)
	}
}

func TestRunE2EInvalidCredentialInjectsKeyIntoIsolatedBuild(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "caller data")
	callerEnvPath := filepath.Join(dataRoot, "env", "compose.env")
	callerKeyPath := filepath.Join(dataRoot, "env", "upstream-model.key")
	if err := os.MkdirAll(filepath.Dir(callerEnvPath), 0o700); err != nil {
		t.Fatal(err)
	}
	callerEnv := strings.Join([]string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=/caller-owned-root",
		"SWITCHBACK_SPIKE_UPSTREAM_MODEL=openai/caller-model",
		"SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://caller-model.example.invalid/v1",
		"SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080",
		"SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000",
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030",
		"LANGFUSE_PUBLIC_KEY=lf_pk_task14_caller",
		"LANGFUSE_SECRET_KEY=lf_sk_task14_caller",
		"NEXTAUTH_SECRET=nextauth-task14-caller",
		"SALT=salt-task14-caller",
		"ENCRYPTION_KEY=encryption-task14-caller",
		"POSTGRES_PASSWORD=postgres-task14-caller",
		"REDIS_AUTH=redis-task14-caller",
		"CLICKHOUSE_PASSWORD=clickhouse-task14-caller",
		"MINIO_ROOT_PASSWORD=minio-task14-caller",
		"",
	}, "\n")
	if err := os.WriteFile(callerEnvPath, []byte(callerEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(callerKeyPath, []byte("caller-model-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	callerEnvBefore := readFile(t, callerEnvPath)
	callerKeyBefore := readFile(t, callerKeyPath)
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *preflight* ]]; then
	"$PWD/scripts/generate-env"
	printf 'preflight data_root=%s generated_env=true\n' "$SWITCHBACK_SPIKE_HOST_DATA_ROOT" >>"$SWITCHBACK_E2E_TEST_LOG"
fi
if [[ "$*" == *build* ]]; then
	printf 'build data_root=%s\n' "$SWITCHBACK_SPIKE_HOST_DATA_ROOT" >>"$SWITCHBACK_E2E_TEST_LOG"
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
if [[ "$1" == "context" && "$2" == "inspect" ]]; then
	printf '%s\n' '[{"Endpoints":{"docker":{"Host":"unix:///tmp/task14-docker.sock"}}}]'
	exit 0
fi
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
exit 0
`)
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf '{"status":"ok"}\n'
exit 0
`)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-invalid-key","worker_run_id":"run-invalid-key","runtime_instance_id":"runtime-invalid-key","runtime_generation":1,"container_id":"container-invalid-key"}
JSON
cat >"$SWITCHBACK_SPIKE_E2E_SUMMARY_FILE" <<'JSON'
{"schema_version":"switchback.spike.acceptance.v1","failure_variant":"invalid-upstream-credential","execution_outcome":"failed","spike_acceptance":"pending","fixture_digest_before":"same","fixture_digest_after":"same","cleanup_confirmed":true}
JSON
exit 0
`)

	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
		"SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential",
	})
	if err != nil {
		t.Fatalf("invalid-credential harness failed before its expected outcome check: %v\n%s", err, output)
	}
	log := readFile(t, logPath)
	isolationPrefix := "data_root=" + dataRoot + "/variants/invalid-upstream-credential-"
	if !strings.Contains(log, "preflight "+isolationPrefix+"") ||
		strings.Index(log, "preflight "+isolationPrefix) > strings.Index(log, "build "+isolationPrefix) {
		t.Fatalf("invalid-credential variant did not generate its isolated environment before build")
	}
	if strings.Contains(log, "build data_root="+dataRoot+" ") {
		t.Fatal("invalid-credential build reused the caller data root")
	}
	if callerEnvAfter := readFile(t, callerEnvPath); callerEnvAfter != callerEnvBefore {
		t.Fatal("invalid-credential variant modified the caller compose env")
	}
	if callerKeyAfter := readFile(t, callerKeyPath); callerKeyAfter != callerKeyBefore {
		t.Fatal("invalid-credential variant modified the caller model key")
	}
	for _, path := range []string{callerEnvPath, callerKeyPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("caller secret file %q mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	preflightLineStart := strings.Index(log, "preflight "+isolationPrefix)
	preflightLineEnd := strings.Index(log[preflightLineStart:], " generated_env=true")
	if preflightLineEnd < 0 {
		t.Fatal("invalid-credential preflight did not report its isolated root")
	}
	variantRoot := strings.TrimPrefix(
		log[preflightLineStart:preflightLineStart+preflightLineEnd],
		"preflight data_root=",
	)
	variantKeyPath := filepath.Join(variantRoot, "env", "upstream-model.key")
	if variantKey := readFile(t, variantKeyPath); variantKey != invalidUpstreamCredential+"\n" {
		t.Fatalf("isolated model key = %q, want replacement key", variantKey)
	}
	if info, err := os.Stat(variantKeyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("isolated model key mode = %v, want 600", err)
	}
	variantEnvPath := filepath.Join(variantRoot, "env", "compose.env")
	canonicalVariantRoot := canonicalPath(t, variantRoot)
	for key, value := range map[string]string{
		"LANGFUSE_PUBLIC_KEY":           "lf_pk_task14_caller",
		"LANGFUSE_SECRET_KEY":           "lf_sk_task14_caller",
		"NEXTAUTH_SECRET":               "nextauth-task14-caller",
		"SALT":                          "salt-task14-caller",
		"ENCRYPTION_KEY":                "encryption-task14-caller",
		"POSTGRES_PASSWORD":             "postgres-task14-caller",
		"REDIS_AUTH":                    "redis-task14-caller",
		"CLICKHOUSE_PASSWORD":           "clickhouse-task14-caller",
		"MINIO_ROOT_PASSWORD":           "minio-task14-caller",
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT":    canonicalVariantRoot,
		"SWITCHBACK_SPIKE_UPSTREAM_MODEL":    "openai/e2e-test",
		"SWITCHBACK_SPIKE_UPSTREAM_API_BASE": "https://models.example.invalid/v1",
	} {
		requireEnvValue(t, variantEnvPath, key, value)
	}
	if info, err := os.Stat(variantEnvPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("isolated compose env mode = %v, want 600", err)
	}
}

func TestRunE2EInvalidCredentialRejectsStaleVariantDestinations(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(tempRoot, "caller data")
	callerEnvPath, callerKeyPath := writeInvalidCredentialCallerSecrets(t, dataRoot)
	callerEnvBefore := snapshotFile(t, callerEnvPath)
	callerKeyBefore := snapshotFile(t, callerKeyPath)
	externalTarget := filepath.Join(tempRoot, "external target")
	if err := os.WriteFile(externalTarget, []byte("external-target-before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalBefore := snapshotFile(t, externalTarget)
	realHarness := filepath.Join(root, "scripts", "run-e2e")
	installRejectingResourceCommandStubs(t, binDir)
	writeExecutable(t, binDir, "stale-variant-wrapper", `#!/usr/bin/env bash
set -euo pipefail
variant_root="$SWITCHBACK_SPIKE_HOST_DATA_ROOT/variants/invalid-upstream-credential-$$"
case "$SWITCHBACK_E2E_ATTACK_KIND" in
stale-root)
	mkdir -p "$variant_root"
	;;
destination-symlink)
	mkdir -p "$variant_root/env"
	ln -s "$SWITCHBACK_E2E_ATTACK_TARGET" "$variant_root/env/compose.env"
	;;
*)
	exit 64
	;;
esac
exec "$SWITCHBACK_E2E_REAL_RUN_E2E"
`)

	for _, attack := range []struct {
		name   string
		kind   string
		target string
	}{
		{name: "stale regular root", kind: "stale-root", target: externalTarget},
		{name: "destination symlink to caller key", kind: "destination-symlink", target: callerKeyPath},
		{name: "destination symlink to external target", kind: "destination-symlink", target: externalTarget},
	} {
		t.Run(attack.name, func(t *testing.T) {
			logPath := filepath.Join(tempRoot, attack.kind+".log")
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := runHarnessExecutable(t, filepath.Join(binDir, "stale-variant-wrapper"), root, binDir, logPath, []string{
				"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
				"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
				"SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential",
				"SWITCHBACK_E2E_ATTACK_KIND=" + attack.kind,
				"SWITCHBACK_E2E_ATTACK_TARGET=" + attack.target,
				"SWITCHBACK_E2E_REAL_RUN_E2E=" + realHarness,
			})
			if err == nil {
				t.Fatal("invalid-credential harness accepted a stale variant destination")
			}
			if strings.Contains(output, "task14-caller") {
				t.Fatal("invalid-credential rejection exposed a test secret")
			}
			assertNoResourceCommandCalls(t, logPath)
			assertFileUnchanged(t, callerEnvPath, callerEnvBefore)
			assertFileUnchanged(t, callerKeyPath, callerKeyBefore)
			assertFileUnchanged(t, externalTarget, externalBefore)
		})
	}
}

func TestRunE2EInvalidCredentialCallerSeedModeIsPortable(t *testing.T) {
	root := spikeRoot(t)
	for _, test := range []struct {
		name                string
		platform            string
		unameFails          bool
		statMode            string
		statFails           bool
		wantStatArguments   []string
		wantResourceCommand string
	}{
		{
			name:                "Darwin accepts owner-only source",
			platform:            "Darwin",
			statMode:            "600",
			wantStatArguments:   []string{"-f", "%Lp"},
			wantResourceCommand: "unexpected make preflight",
		},
		{
			name:                "Linux accepts owner-only source",
			platform:            "Linux",
			statMode:            "600",
			wantStatArguments:   []string{"-c", "%a"},
			wantResourceCommand: "unexpected make preflight",
		},
		{
			name:              "Darwin rejects non-owner-only source",
			platform:          "Darwin",
			statMode:          "644",
			wantStatArguments: []string{"-f", "%Lp"},
		},
		{
			name:              "Linux rejects non-owner-only source",
			platform:          "Linux",
			statMode:          "644",
			wantStatArguments: []string{"-c", "%a"},
		},
		{name: "unknown platform rejects source", platform: "Plan9"},
		{name: "uname error rejects source", unameFails: true},
		{
			name:              "stat error rejects source",
			platform:          "Linux",
			statFails:         true,
			wantStatArguments: []string{"-c", "%a"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			binDir := filepath.Join(tempRoot, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			dataRoot := filepath.Join(tempRoot, "caller data")
			callerEnvPath, callerKeyPath := writeInvalidCredentialCallerSecrets(t, dataRoot)
			callerEnvBefore := snapshotFile(t, callerEnvPath)
			callerKeyBefore := snapshotFile(t, callerKeyPath)
			logPath := filepath.Join(tempRoot, "commands.log")
			statLogPath := filepath.Join(tempRoot, "stat.log")
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			installRejectingResourceCommandStubs(t, binDir)
			installPlatformModeStubs(t, binDir, test.platform, test.unameFails, test.statMode, test.statFails)

			output, err := runHarness(t, root, binDir, logPath, []string{
				"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
				"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
				"SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential",
				"SWITCHBACK_E2E_STAT_LOG=" + statLogPath,
			})
			if err == nil {
				t.Fatal("invalid-credential harness unexpectedly completed")
			}
			if test.wantResourceCommand == "" {
				if !strings.Contains(output, "caller compose env must be an owner-only regular file") {
					t.Fatalf("invalid-credential harness did not reject the caller seed: %s", output)
				}
				assertNoResourceCommandCalls(t, logPath)
			} else if log := readFile(t, logPath); !strings.Contains(log, test.wantResourceCommand) {
				t.Fatalf("valid caller seed did not reach the expected next command:\n%s", log)
			}
			assertPlatformStatArguments(t, statLogPath, test.wantStatArguments, callerEnvPath)
			assertFileUnchanged(t, callerEnvPath, callerEnvBefore)
			assertFileUnchanged(t, callerKeyPath, callerKeyBefore)
		})
	}
}

func TestGenerateEnvModelKeyModeIsPortable(t *testing.T) {
	root := spikeRoot(t)
	for _, test := range []struct {
		name              string
		platform          string
		unameFails        bool
		statMode          string
		statFails         bool
		wantStatArguments []string
		acceptsMode       bool
	}{
		{name: "Darwin accepts owner-only key", platform: "Darwin", statMode: "600", wantStatArguments: []string{"-f", "%Lp"}, acceptsMode: true},
		{name: "Linux accepts owner-only key", platform: "Linux", statMode: "600", wantStatArguments: []string{"-c", "%a"}, acceptsMode: true},
		{name: "Darwin rejects non-owner-only key", platform: "Darwin", statMode: "640", wantStatArguments: []string{"-f", "%Lp"}},
		{name: "Linux rejects non-owner-only key", platform: "Linux", statMode: "640", wantStatArguments: []string{"-c", "%a"}},
		{name: "unknown platform rejects key", platform: "Plan9"},
		{name: "uname error rejects key", unameFails: true},
		{name: "stat error rejects key", platform: "Darwin", statFails: true, wantStatArguments: []string{"-f", "%Lp"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			binDir := filepath.Join(tempRoot, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			dataRoot := filepath.Join(tempRoot, "data")
			modelKeyPath := filepath.Join(dataRoot, "env", "upstream-model.key")
			if err := os.MkdirAll(filepath.Dir(modelKeyPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(modelKeyPath, []byte("e2e-test-upstream-key\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			modelKeyBefore := snapshotFile(t, modelKeyPath)
			logPath := filepath.Join(tempRoot, "commands.log")
			statLogPath := filepath.Join(tempRoot, "stat.log")
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			installGenerateEnvCommandStubs(t, binDir)
			installPlatformModeStubs(t, binDir, test.platform, test.unameFails, test.statMode, test.statFails)

			output, err := runHarnessExecutable(t, filepath.Join(root, "scripts", "generate-env"), root, binDir, logPath, []string{
				"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
				"SWITCHBACK_E2E_STAT_LOG=" + statLogPath,
			})
			if err == nil {
				t.Fatal("generate-env unexpectedly completed")
			}
			if test.acceptsMode {
				if log := readFile(t, logPath); !strings.Contains(log, "unexpected openssl rand -hex 32") {
					t.Fatalf("valid model-key mode did not reach secret generation:\n%s\n%s", output, log)
				}
			} else {
				if !strings.Contains(output, "model key file must be owner-only") {
					t.Fatalf("generate-env did not reject the model-key mode: %s", output)
				}
				assertNoResourceCommandCalls(t, logPath)
			}
			assertPlatformStatArguments(t, statLogPath, test.wantStatArguments, canonicalPath(t, modelKeyPath))
			assertFileUnchanged(t, modelKeyPath, modelKeyBefore)
		})
	}
}

func TestRunE2EInvalidCredentialRejectsNonRegularCallerSeed(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(tempRoot, "caller data")
	callerEnvPath, callerKeyPath := writeInvalidCredentialCallerSecrets(t, dataRoot)
	callerKeyBefore := snapshotFile(t, callerKeyPath)
	externalTarget := filepath.Join(tempRoot, "caller-env-target")
	if err := os.WriteFile(externalTarget, []byte("caller-env-target-before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalBefore := snapshotFile(t, externalTarget)
	if err := os.Remove(callerEnvPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalTarget, callerEnvPath); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, binDir, "make", "#!/usr/bin/env bash\nexit 93\n")

	output, err := runHarness(t, root, binDir, filepath.Join(tempRoot, "commands.log"), []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
		"SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential",
	})
	if err == nil {
		t.Fatal("invalid-credential harness accepted a symlink caller seed")
	}
	if !strings.Contains(output, "caller compose env must be an owner-only regular file") {
		t.Fatal("invalid-credential harness did not reject the caller seed before preflight")
	}
	assertFileUnchanged(t, callerKeyPath, callerKeyBefore)
	assertFileUnchanged(t, externalTarget, externalBefore)
}

func TestRunE2EInvalidCredentialWithoutCallerEnvKeepsFreshBootstrap(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "caller data without env")
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *preflight* ]]; then
	if [[ -e "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" ]]; then
		exit 1
	fi
	printf 'preflight fresh_bootstrap=true\n' >>"$SWITCHBACK_E2E_TEST_LOG"
fi
if [[ "$*" == *build* ]]; then
	[[ "$SWITCHBACK_SPIKE_UPSTREAM_API_KEY" == "switchback-task14-invalid-upstream-credential-v1" ]]
	mkdir -p "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env"
	cat >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" <<'EOF'
SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080
SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000
SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030
LANGFUSE_PUBLIC_KEY=lf_pk_fresh
LANGFUSE_SECRET_KEY=lf_sk_fresh
POSTGRES_PASSWORD=postgres-fresh
EOF
	printf '%s\n' "$SWITCHBACK_SPIKE_UPSTREAM_API_KEY" >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/upstream-model.key"
	chmod 600 "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/upstream-model.key"
	printf 'build fresh_bootstrap=true\n' >>"$SWITCHBACK_E2E_TEST_LOG"
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", successfulFakeDockerScript())
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf '{"status":"ok"}\n'
exit 0
`)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-fresh","worker_run_id":"run-fresh","runtime_instance_id":"runtime-fresh","runtime_generation":1,"container_id":"container-fresh"}
JSON
cat >"$SWITCHBACK_SPIKE_E2E_SUMMARY_FILE" <<'JSON'
{"schema_version":"switchback.spike.acceptance.v1","failure_variant":"invalid-upstream-credential","execution_outcome":"failed","spike_acceptance":"pending","fixture_digest_before":"same","fixture_digest_after":"same","cleanup_confirmed":true}
JSON
exit 0
`)

	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
		"SWITCHBACK_SPIKE_E2E_VARIANT=invalid-upstream-credential",
	})
	if err != nil {
		t.Fatalf("invalid-credential fresh-bootstrap harness failed: %v\n%s", err, output)
	}
	log := readFile(t, logPath)
	if !strings.Contains(log, "preflight fresh_bootstrap=true") ||
		!strings.Contains(log, "build fresh_bootstrap=true") {
		t.Fatalf("invalid-credential variant did not preserve fresh bootstrap without a caller env:\n%s", log)
	}
}

func TestRunE2EChecksumVariantCleansHelperAndComposeOverride(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "data")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *build* ]]; then
	mkdir -p "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env"
	cat >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" <<'EOF'
SWITCHBACK_SPIKE_DOCKER_SOCKET=/tmp/unused-docker.sock
SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080
SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000
SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030
LANGFUSE_PUBLIC_KEY=lf_pk_test
LANGFUSE_SECRET_KEY=lf_sk_test
POSTGRES_PASSWORD=postgres-test
EOF
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "run" && "$*" == *"checksum-fault-proxy.py"* ]]; then
	mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE")"
	cat >"$SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE" <<'JSON'
{"ready":true,"fault_triggered":true,"fault_count":1,"checksum_request_matched":true,"evidence_path_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidence_kind":"trace","authorization_present":true,"original_checksum_valid":true,"original_checksum_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mutated_body_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","digest_mismatch":true,"backend_status_code":400,"backend_error_code":"evidence_checksum_mismatch"}
JSON
	printf 'checksum-proxy-container\n'
	exit 0
fi
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *".State.Running"* ]]; then
	printf 'true\n'
	exit 0
fi
exit 0
`)
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf '{"status":"ok"}\n'
exit 0
`)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
if [[ "$*" == *"TestWriteChecksumFaultProxyProgram"* ]]; then
	exec "$SWITCHBACK_E2E_REAL_GO" "$@"
fi
jq -e '.fault_triggered == true and .backend_status_code == 400 and .backend_error_code == "evidence_checksum_mismatch"' "$SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE" >/dev/null
printf 'checksum-state-proven\n' >>"$SWITCHBACK_E2E_TEST_LOG"
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-checksum","worker_run_id":"run-checksum","runtime_instance_id":"runtime-checksum","runtime_generation":1,"container_id":""}
JSON
cat >"$SWITCHBACK_SPIKE_E2E_SUMMARY_FILE" <<'JSON'
{"schema_version":"switchback.spike.acceptance.v1","failure_variant":"checksum-mismatch","execution_outcome":"failed","spike_acceptance":"pending","fixture_digest_before":"same","fixture_digest_after":"same","cleanup_confirmed":true}
JSON
exit 0
`)
	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
		"SWITCHBACK_SPIKE_E2E_VARIANT=checksum-mismatch",
		"SWITCHBACK_E2E_REAL_GO=" + realGo,
	})
	if err != nil {
		t.Fatalf("checksum harness failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(dataRoot, "acceptance", "checksum-fault-proxy"),
		filepath.Join(dataRoot, "acceptance", "fault-proxy-stop"),
		filepath.Join(dataRoot, "acceptance", "fault-proxy.log"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("checksum harness left temporary helper artifact %q", path)
		}
	}
	log := readFile(t, logPath)
	if !strings.Contains(log, "docker run --detach") ||
		!strings.Contains(log, "--network switchback-spike-control") ||
		!strings.Contains(log, "checksum-state-proven") ||
		strings.Contains(log, "host.docker.internal") ||
		strings.Contains(log, " -p ") ||
		strings.Contains(log, " --publish ") {
		t.Fatalf("checksum harness did not use the isolated private-network transport:\n%s", log)
	}
}

func TestRunE2EChecksumProxyWaitsForComposeOwnedControlNetwork(t *testing.T) {
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "data")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *build* ]]; then
	mkdir -p "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env"
	cat >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" <<'EOF'
SWITCHBACK_SPIKE_DOCKER_SOCKET=/tmp/unused-docker.sock
SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080
SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000
SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030
LANGFUSE_PUBLIC_KEY=lf_pk_test
LANGFUSE_SECRET_KEY=lf_sk_test
POSTGRES_PASSWORD=postgres-test
EOF
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "network" && "$2" == "create" ]]; then
	echo "run-e2e must not create an independently owned control network" >&2
	exit 99
fi
if [[ "$1" == "compose" && "$*" == *" create spike-backend"* ]]; then
	: >"${SWITCHBACK_E2E_TEST_LOG}.control-network-ready"
	exit 0
fi
if [[ "$1" == "run" && "$*" == *"checksum-fault-proxy.py"* ]]; then
	if [[ ! -f "${SWITCHBACK_E2E_TEST_LOG}.control-network-ready" ]]; then
		echo "network switchback-spike-control not found" >&2
		exit 125
	fi
	mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE")"
	cat >"$SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE" <<'JSON'
{"ready":true,"fault_triggered":true,"fault_count":1,"checksum_request_matched":true,"evidence_path_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidence_kind":"trace","authorization_present":true,"original_checksum_valid":true,"original_checksum_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mutated_body_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","digest_mismatch":true,"backend_status_code":400,"backend_error_code":"evidence_checksum_mismatch"}
JSON
	printf 'checksum-proxy-container\n'
	exit 0
fi
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *".State.Running"* ]]; then
	printf 'true\n'
	exit 0
fi
exit 0
`)
	writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
printf '{"status":"ok"}\n'
exit 0
`)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
if [[ "$*" == *"TestWriteChecksumFaultProxyProgram"* ]]; then
	exec "$SWITCHBACK_E2E_REAL_GO" "$@"
fi
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-checksum","worker_run_id":"run-checksum","runtime_instance_id":"runtime-checksum","runtime_generation":1,"container_id":""}
JSON
cat >"$SWITCHBACK_SPIKE_E2E_SUMMARY_FILE" <<'JSON'
{"schema_version":"switchback.spike.acceptance.v1","failure_variant":"checksum-mismatch","execution_outcome":"failed","spike_acceptance":"pending","fixture_digest_before":"same","fixture_digest_after":"same","cleanup_confirmed":true}
JSON
exit 0
`)

	output, err := runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
		"SWITCHBACK_SPIKE_E2E_VARIANT=checksum-mismatch",
		"SWITCHBACK_E2E_REAL_GO=" + realGo,
	})
	if err != nil {
		t.Fatalf("checksum harness started proxy before the Compose-owned control network: %v\n%s", err, output)
	}
	log := readFile(t, logPath)
	createIndex := strings.Index(log, " create spike-backend\n")
	if createIndex < 0 {
		t.Fatalf("checksum harness did not ask Compose to create the control-network service:\n%s", log)
	}
	proxyIndex := strings.Index(log, "docker run --detach")
	if proxyIndex < 0 || createIndex > proxyIndex {
		t.Fatalf("checksum proxy started before Compose made the control network ready:\n%s", log)
	}
	if strings.Contains(log[:proxyIndex], " up -d langfuse-web langfuse-worker spike-backend") {
		t.Fatalf("checksum harness started Backend or Langfuse before the proxy was ready:\n%s", log)
	}
	if strings.Contains(log, "docker network create") {
		t.Fatalf("checksum harness created an independently owned control network:\n%s", log)
	}
}

func runSuccessfulFakeLifecycleHarness(t *testing.T, dockerScript string) (string, error) {
	t.Helper()
	return runSuccessfulFakeLifecycleHarnessWithCurl(t, dockerScript, `#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
printf '{"status":"ok"}\n'
exit 0
`)
}

func runSuccessfulFakeLifecycleHarnessWithCurl(
	t *testing.T,
	dockerScript, curlScript string,
) (string, error) {
	t.Helper()
	root := spikeRoot(t)
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "commands.log")
	dataRoot := filepath.Join(tempRoot, "data")
	writeExecutable(t, binDir, "make", `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$*" == *build* ]]; then
	mkdir -p "$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env"
	cat >"$SWITCHBACK_SPIKE_HOST_DATA_ROOT/env/compose.env" <<'EOF'
SWITCHBACK_SPIKE_BACKEND_HTTP_PORT=18080
SWITCHBACK_SPIKE_LANGFUSE_WEB_PORT=13000
SWITCHBACK_SPIKE_LANGFUSE_WORKER_PORT=13030
LANGFUSE_PUBLIC_KEY=lf_pk_test
LANGFUSE_SECRET_KEY=lf_sk_test
POSTGRES_PASSWORD=postgres-test
EOF
fi
exit 0
`)
	writeExecutable(t, binDir, "docker", dockerScript)
	writeExecutable(t, binDir, "curl", curlScript)
	writeExecutable(t, binDir, "go", `#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
mkdir -p "$(dirname "$SWITCHBACK_SPIKE_E2E_STATE_FILE")"
cat >"$SWITCHBACK_SPIKE_E2E_STATE_FILE" <<'JSON'
{"worker_session_id":"session-cleanup-proof","worker_run_id":"run-cleanup-proof","runtime_instance_id":"runtime-cleanup-proof","runtime_generation":1,"container_id":"container-cleanup-proof"}
JSON
cat >"$SWITCHBACK_SPIKE_E2E_SUMMARY_FILE" <<'JSON'
{"schema_version":"switchback.spike.acceptance.v1","execution_outcome":"completed","spike_acceptance":"passed","evidence_verified":true,"token_absence":true,"langfuse_verified":true,"fixture_digest_before":"same","fixture_digest_after":"same","cleanup_confirmed":true}
JSON
exit 0
`)
	return runHarness(t, root, binDir, logPath, []string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS=0",
	})
}

func successfulFakeDockerScript() string {
	return `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
if [[ "$1" == "compose" && "$*" == *" ps -q "* ]]; then
	printf 'compose-service-id\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Health.Status"* ]]; then
	printf 'healthy\n'
	exit 0
fi
if [[ "$1" == "inspect" && "$*" == *"State.Status"* ]]; then
	printf 'running\n'
	exit 0
fi
exit 0
`
}

func writeTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func runHarness(
	t *testing.T,
	root, binDir, logPath string,
	extraEnvironment []string,
) (string, error) {
	t.Helper()
	return runHarnessExecutable(t, filepath.Join(root, "scripts", "run-e2e"), root, binDir, logPath, extraEnvironment)
}

func installRejectingResourceCommandStubs(t *testing.T, binDir string) {
	t.Helper()
	for _, name := range []string{"make", "docker", "go", "curl"} {
		writeExecutable(t, binDir, name, "#!/usr/bin/env bash\nprintf 'unexpected "+name+" %s\\n' \"$*\" >>\"$SWITCHBACK_E2E_TEST_LOG\"\nexit 97\n")
	}
}

func installGenerateEnvCommandStubs(t *testing.T, binDir string) {
	t.Helper()
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
if [[ "$1" == "context" && "$2" == "inspect" ]]; then
	printf '%s\n' '[{"Endpoints":{"docker":{"Host":"unix:///tmp/task14-docker.sock"}}}]'
	exit 0
fi
printf 'unexpected docker %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 97
`)
	writeExecutable(t, binDir, "jq", `#!/usr/bin/env bash
if [[ "$1" == "-er" ]]; then
	printf '%s\n' 'unix:///tmp/task14-docker.sock'
	exit 0
fi
printf 'unexpected jq %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 97
`)
	writeExecutable(t, binDir, "openssl", `#!/usr/bin/env bash
printf 'unexpected openssl %s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
exit 97
`)
}

func installPlatformModeStubs(
	t *testing.T,
	binDir, platform string,
	unameFails bool,
	mode string,
	statFails bool,
) {
	t.Helper()
	if unameFails {
		writeExecutable(t, binDir, "uname", "#!/usr/bin/env bash\nexit 79\n")
	} else {
		writeExecutable(t, binDir, "uname", "#!/usr/bin/env bash\nprintf '%s\\n' "+strconv.Quote(platform)+"\n")
	}
	statResult := "printf '%s\\n' " + strconv.Quote(mode)
	if statFails {
		statResult = "exit 78"
	}
	writeExecutable(t, binDir, "stat", "#!/usr/bin/env bash\nprintf 'stat' >>\"$SWITCHBACK_E2E_STAT_LOG\"\nprintf ' <%s>' \"$@\" >>\"$SWITCHBACK_E2E_STAT_LOG\"\nprintf '\\n' >>\"$SWITCHBACK_E2E_STAT_LOG\"\n"+statResult+"\n")
}

func assertNoResourceCommandCalls(t *testing.T, logPath string) {
	t.Helper()
	if log := readFile(t, logPath); strings.TrimSpace(log) != "" {
		t.Fatalf("rejection invoked a resource, network, or build command:\n%s", log)
	}
}

func assertPlatformStatArguments(t *testing.T, statLogPath string, wantArguments []string, path string) {
	t.Helper()
	content, err := os.ReadFile(statLogPath)
	if len(wantArguments) == 0 {
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(content)) != "" {
			t.Fatalf("platform rejection unexpectedly invoked stat:\n%s", content)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := "stat"
	for _, argument := range append(wantArguments, path) {
		want += " <" + argument + ">"
	}
	want += "\n"
	if string(content) != want {
		t.Fatalf("stat argv = %q, want %q", content, want)
	}
}

func runHarnessExecutable(
	t *testing.T,
	executable, root, binDir, logPath string,
	extraEnvironment []string,
) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable)
	command.Dir = root
	command.Env = append(environmentWithout(
		"PATH",
		"SWITCHBACK_E2E_TEST_LOG",
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT",
		"SWITCHBACK_SPIKE_E2E_CLEANUP_TIMEOUT_SECONDS",
		"SWITCHBACK_SPIKE_E2E_VARIANT",
	),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"SWITCHBACK_E2E_TEST_LOG="+logPath,
		"SWITCHBACK_SPIKE_UPSTREAM_MODEL=openai/e2e-test",
		"SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://models.example.invalid/v1",
		"SWITCHBACK_SPIKE_UPSTREAM_API_KEY=e2e-test-upstream-key",
	)
	command.Env = append(command.Env, extraEnvironment...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("run-e2e did not terminate: %v\n%s", ctx.Err(), output.String())
	}
	return output.String(), err
}

func environmentWithout(names ...string) []string {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if _, remove := blocked[name]; !remove {
			result = append(result, item)
		}
	}
	return result
}

func writeExecutable(t *testing.T, directory, name, content string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type fileSnapshot struct {
	content []byte
	mode    os.FileMode
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileSnapshot{content: content, mode: info.Mode().Perm()}
}

func assertFileUnchanged(t *testing.T, path string, before fileSnapshot) {
	t.Helper()
	after := snapshotFile(t, path)
	if !bytes.Equal(after.content, before.content) || after.mode != before.mode {
		t.Fatalf("protected file %q changed", path)
	}
}

func writeInvalidCredentialCallerSecrets(t *testing.T, dataRoot string) (string, string) {
	t.Helper()
	callerEnvPath := filepath.Join(dataRoot, "env", "compose.env")
	callerKeyPath := filepath.Join(dataRoot, "env", "upstream-model.key")
	if err := os.MkdirAll(filepath.Dir(callerEnvPath), 0o700); err != nil {
		t.Fatal(err)
	}
	callerEnv := strings.Join([]string{
		"SWITCHBACK_SPIKE_HOST_DATA_ROOT=/caller-owned-root",
		"SWITCHBACK_SPIKE_UPSTREAM_MODEL=openai/caller-model",
		"SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://caller-model.example.invalid/v1",
		"LANGFUSE_SECRET_KEY=task14-caller-observability-secret",
		"POSTGRES_PASSWORD=task14-caller-database-secret",
		"",
	}, "\n")
	if err := os.WriteFile(callerEnvPath, []byte(callerEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(callerKeyPath, []byte("task14-caller-model-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return callerEnvPath, callerKeyPath
}

func requireEnvValue(t *testing.T, path, key, want string) {
	t.Helper()
	for _, line := range strings.Split(readFile(t, path), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found && name == key {
			if value != want {
				t.Fatalf("isolated env field %q has an unexpected value", key)
			}
			return
		}
	}
	t.Fatalf("isolated env field %q is missing", key)
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func waitForFaultProxyState(
	t *testing.T,
	path string,
	ready func(faultProxySnapshot) bool,
) faultProxySnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state faultProxySnapshot
		if err := decodeStrictJSONFile(path, &state); err == nil && ready(state) {
			return state
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("fault proxy state %q did not become ready", path)
	return faultProxySnapshot{}
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
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err != nil {
		t.Fatalf("resolve spike root %q: %v", root, err)
	}
	return root
}

func TestPromptContractConstants(t *testing.T) {
	for index, prompt := range []string{turnOnePrompt, turnTwoPrompt} {
		if strings.TrimSpace(prompt) != prompt || strings.ContainsAny(prompt, "\r\n") {
			t.Fatalf("Turn %d prompt is not one exact line: %q", index+1, prompt)
		}
	}
	if turnOnePrompt == turnTwoPrompt {
		t.Fatal("the two exact prompts must be distinct")
	}
	if testing.Verbose() {
		t.Log(fmt.Sprintf("exact prompt contract loaded: %d/%d bytes", len(turnOnePrompt), len(turnTwoPrompt)))
	}
}

func TestTurnOneCreateRunRequestCarriesExecutableAcceptanceContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body createRunRequest
		if request.Method != http.MethodPost ||
			request.URL.Path != "/spike/runs" ||
			json.NewDecoder(request.Body).Decode(&body) != nil ||
			body.Goal != turnOnePrompt ||
			len(body.AcceptanceCriteria) != 1 ||
			body.AcceptanceCriteria[0] != turnOneAcceptanceCriterion ||
			strings.TrimSpace(body.AcceptanceCriteria[0]) == "" {
			http.Error(writer, "invalid create-run contract", http.StatusBadRequest)
			return
		}
		writeTestJSON(writer, http.StatusAccepted, createdRun{
			WorkerSessionID: "session-1",
			WorkerRunID:     "run-1",
			TurnID:          "turn-1",
		})
	}))
	t.Cleanup(server.Close)

	created := createTurnOneRun(t, server.Client(), server.URL+"/spike/runs")
	if created.WorkerSessionID != "session-1" ||
		created.WorkerRunID != "run-1" ||
		created.TurnID != "turn-1" {
		t.Fatalf("create Turn 1 returned unexpected identity: %+v", created)
	}
}

func TestTurnOnePromptFrameCarriesSameAcceptanceContract(t *testing.T) {
	request := turnOneCreateRunRequest()
	expectedPrompt := fmt.Sprintf(
		"Goal: %s\nAcceptance criteria:\n- %s",
		request.Goal,
		request.AcceptanceCriteria[0],
	)
	identity := runtimeIdentity{
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentSessionID:  "agent-1",
		AgentTurnID:     "turn-1",
	}
	promptFrame := func(prompt string) rpcFrame {
		t.Helper()
		params, err := json.Marshal(map[string]any{
			"sessionId": identity.AgentSessionID,
			"prompt": []map[string]string{{
				"type": "text",
				"text": prompt,
			}},
			"_meta": map[string]any{
				"switchback": map[string]string{
					"worker_session_id": identity.WorkerSessionID,
					"worker_run_id":     identity.WorkerRunID,
					"agent_turn_id":     identity.AgentTurnID,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return rpcFrame{Method: "session/prompt", Params: params}
	}
	if err := verifyPromptFrame(
		promptFrame(expectedPrompt),
		identity,
		request.Goal,
		true,
	); err != nil {
		t.Fatal(err)
	}

	withoutCriterion := strings.TrimSuffix(expectedPrompt, "\n- "+request.AcceptanceCriteria[0])
	if err := verifyPromptFrame(
		promptFrame(withoutCriterion),
		identity,
		request.Goal,
		true,
	); err == nil {
		t.Fatal("Turn 1 ACP prompt accepted an empty acceptance-criteria section")
	}
}

func TestRuntimeContinuityRejectsContainerReplacement(t *testing.T) {
	first := runtimeIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-1",
		ContainerID:       "container-1",
		WorkingCopy:       "/data/sessions/session-1/working-copy",
		AgentTurnID:       "turn-1",
		TraceID:           "trace-1",
	}
	second := first
	second.AgentTurnID = "turn-2"
	second.ContainerID = "container-2"
	if err := verifyRuntimeContinuity(first, second); err == nil {
		t.Fatal("runtime continuity accepted a replacement container")
	}
}

func TestManifestVerificationRejectsChecksumMismatch(t *testing.T) {
	runRoot, manifest := writeManifestFixture(t)
	manifest.Objects[2].SHA256 = strings.Repeat("0", 64)
	if err := writeStrictJSON(filepath.Join(runRoot, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManifest(runRoot, manifestIdentity(manifest)); err == nil ||
		!strings.Contains(err.Error(), "checksum") {
		t.Fatalf("verifyManifest() error = %v, want checksum rejection", err)
	}
}

func TestManifestVerificationRejectsWrongAuthoritativeTrace(t *testing.T) {
	runRoot, manifest := writeManifestFixture(t)
	expected := manifestIdentity(manifest)
	expected.TraceID = "authoritative-trace"
	if _, err := verifyManifest(runRoot, expected); err == nil ||
		!strings.Contains(err.Error(), "identity") {
		t.Fatalf("verifyManifest() error = %v, want wrong Trace identity rejection", err)
	}
}

func TestNormalizedTraceVerificationRejectsWrongAuthoritativeTrace(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expected := runtimeIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-1",
		AgentTurnID:       "turn-1",
		TraceID:           "authoritative-trace",
	}
	record := normalizedTraceRecord{
		SchemaVersion:   sharedevidence.NormalizedTraceSchemaVersion,
		Sequence:        1,
		ServerTimestamp: now,
		WorkerTimestamp: now,
		Identity: sharedevidence.TurnIdentity{
			WorkerSessionID:   expected.WorkerSessionID,
			WorkerRunID:       expected.WorkerRunID,
			RuntimeInstanceID: expected.RuntimeInstanceID,
			RuntimeGeneration: expected.RuntimeGeneration,
			AgentSessionID:    expected.AgentSessionID,
			AgentTurnID:       expected.AgentTurnID,
			TraceID:           "stale-trace",
		},
		Kind:    "acp.raw_frame",
		Payload: json.RawMessage(`{"type":"acp.raw_frame"}`),
	}
	if err := verifyNormalizedTraceRecord(0, record, "acp.raw_frame", expected); err == nil {
		t.Fatal("normalized JSONL accepted a stale Trace identity")
	}
}

func TestChecksumFaultProxyCorruptsOnlyFirstEvidenceUpload(t *testing.T) {
	type observedRequest struct {
		body     string
		checksum string
	}
	var observed []observedRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		observed = append(observed, observedRequest{
			body:     string(body),
			checksum: request.Header.Get("X-Content-SHA256"),
		})
		if contentDigest(string(body)) != request.Header.Get("X-Content-SHA256") {
			writeTestJSON(writer, http.StatusBadRequest, map[string]string{
				"error": "evidence checksum mismatch",
			})
			return
		}
		writeTestJSON(writer, http.StatusCreated, sharedevidence.ObjectRef{
			Kind:   filepath.Base(request.URL.Path),
			Path:   request.URL.Path,
			SHA256: request.Header.Get("X-Content-SHA256"),
			Bytes:  int64(len(body)),
		})
	}))
	defer upstream.Close()

	state := &faultProxyState{}
	proxy := httptest.NewServer(newChecksumFaultProxy(upstream.URL, state))
	defer proxy.Close()
	for _, body := range []string{"first-evidence", "second-evidence"} {
		digest := sha256.Sum256([]byte(body))
		request, err := http.NewRequest(
			http.MethodPut,
			proxy.URL+"/internal/v1/sessions/session/runs/run/turns/turn/evidence/trace",
			strings.NewReader(body),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer worker-token")
		request.Header.Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if len(observed) != 2 ||
		observed[0].body == "first-evidence" ||
		observed[0].checksum != contentDigest("first-evidence") ||
		observed[1].body != "second-evidence" ||
		observed[1].checksum != contentDigest("second-evidence") {
		t.Fatalf("checksum proxy did not corrupt exactly one upload: %+v", observed)
	}
	snapshot := state.snapshot()
	if !checksumFaultProven(snapshot) {
		t.Fatalf("checksum fault state = %+v, want exact Backend checksum rejection proof", snapshot)
	}
}

func TestChecksumFaultProxyDoesNotProveGenericBackendFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "temporary backend failure", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	state := &faultProxyState{}
	proxy := httptest.NewServer(newChecksumFaultProxy(upstream.URL, state))
	defer proxy.Close()
	request, err := http.NewRequest(
		http.MethodPut,
		proxy.URL+"/internal/v1/sessions/session/runs/run/turns/turn/evidence/trace",
		strings.NewReader("evidence"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("X-Content-SHA256", contentDigest("evidence"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	snapshot := state.snapshot()
	if snapshot.FaultTriggered || snapshot.FaultCount != 0 {
		t.Fatalf("generic Backend failure falsely proved checksum rejection: %+v", snapshot)
	}
}

func TestChecksumFaultProxyRejectsNonEvidenceRoutes(t *testing.T) {
	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamRequests++
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	state := &faultProxyState{}
	proxy := httptest.NewServer(newChecksumFaultProxy(upstream.URL, state))
	defer proxy.Close()
	for _, requestCase := range []struct {
		method string
		path   string
		status int
	}{
		{
			method: http.MethodGet,
			path:   "/internal/v1/sessions/session/runs/run/turns/turn/evidence/trace",
			status: http.StatusMethodNotAllowed,
		},
		{
			method: http.MethodPut,
			path:   "/spike/runs",
			status: http.StatusNotFound,
		},
	} {
		request, err := http.NewRequest(
			requestCase.method,
			proxy.URL+requestCase.path,
			strings.NewReader("evidence"),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer worker-token")
		request.Header.Set("X-Content-SHA256", contentDigest("evidence"))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != requestCase.status {
			t.Fatalf("%s %s returned HTTP %d, want %d", requestCase.method, requestCase.path, response.StatusCode, requestCase.status)
		}
	}
	if upstreamRequests != 0 {
		t.Fatalf("checksum proxy forwarded %d non-evidence requests", upstreamRequests)
	}
}

func TestDockerEngineFaultProxyRejectsOnlyTargetWorkerDelete(t *testing.T) {
	tempRoot, err := os.MkdirTemp("/tmp", "switchback-task14-docker-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	upstreamSocket := filepath.Join(tempRoot, "docker-real.sock")
	upstreamListener, err := net.Listen("unix", upstreamSocket)
	if err != nil {
		t.Fatal(err)
	}
	var upstreamDeletes []string
	upstreamServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/containers/create"):
			writeTestJSON(writer, http.StatusCreated, map[string]any{
				"Id":       "worker-target-id",
				"Warnings": []string{},
			})
		case request.Method == http.MethodDelete && strings.Contains(request.URL.Path, "/containers/"):
			upstreamDeletes = append(upstreamDeletes, filepath.Base(request.URL.Path))
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	})}
	go func() { _ = upstreamServer.Serve(upstreamListener) }()
	t.Cleanup(func() {
		_ = upstreamServer.Close()
		_ = upstreamListener.Close()
	})

	state := &faultProxyState{}
	proxySocket := filepath.Join(tempRoot, "docker-proxy.sock")
	proxyListener, err := net.Listen("unix", proxySocket)
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := &http.Server{Handler: newDockerEngineFaultProxy(upstreamSocket, state)}
	go func() { _ = proxyServer.Serve(proxyListener) }()
	t.Cleanup(func() {
		_ = proxyServer.Close()
		_ = proxyListener.Close()
	})
	client := unixHTTPClient(proxySocket)
	createBody := `{"Labels":{"com.switchback.managed":"worker-runtime","com.switchback.worker-session-id":"session-1"}}`
	response := doProxyRequest(
		t,
		client,
		http.MethodPost,
		"http://docker/v1.55/containers/create",
		strings.NewReader(createBody),
	)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("proxied create returned HTTP %d", response.StatusCode)
	}
	response = doProxyRequest(
		t,
		client,
		http.MethodDelete,
		"http://docker/v1.55/containers/worker-target-id?force=1",
		nil,
	)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("target cleanup returned HTTP %d, want injected 500", response.StatusCode)
	}
	response = doProxyRequest(
		t,
		client,
		http.MethodDelete,
		"http://docker/v1.55/containers/unrelated-id?force=1",
		nil,
	)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent ||
		len(upstreamDeletes) != 1 ||
		upstreamDeletes[0] != "unrelated-id" {
		t.Fatalf("non-target Engine delete was not forwarded: status=%d deletes=%v", response.StatusCode, upstreamDeletes)
	}
	snapshot := state.snapshot()
	if snapshot.TargetContainerID != "worker-target-id" ||
		!snapshot.FaultTriggered ||
		snapshot.FaultCount != 1 {
		t.Fatalf("Docker fault state = %+v, want exact target cleanup fault", snapshot)
	}
}

func TestFaultProxyHelperProcessCleansUnixSocket(t *testing.T) {
	tempRoot, err := os.MkdirTemp("/tmp", "switchback-task14-helper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	upstreamSocket := filepath.Join(tempRoot, "real.sock")
	upstreamListener, err := net.Listen("unix", upstreamSocket)
	if err != nil {
		t.Fatal(err)
	}
	upstreamServer := &http.Server{Handler: http.NotFoundHandler()}
	go func() { _ = upstreamServer.Serve(upstreamListener) }()
	t.Cleanup(func() {
		_ = upstreamServer.Close()
		_ = upstreamListener.Close()
	})

	proxySocket := filepath.Join(tempRoot, "proxy.sock")
	stateFile := filepath.Join(tempRoot, "state.json")
	stopFile := filepath.Join(tempRoot, "stop")
	command := exec.Command(os.Args[0], "-test.run=^TestE2EFaultProxyHelper$", "-test.count=1")
	command.Env = append(environmentWithout(
		"SWITCHBACK_SPIKE_E2E_HELPER_MODE",
		"SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE",
		"SWITCHBACK_SPIKE_E2E_FAULT_STOP_FILE",
		"SWITCHBACK_SPIKE_E2E_REAL_DOCKER_SOCKET",
		"SWITCHBACK_SPIKE_E2E_PROXY_DOCKER_SOCKET",
	),
		"SWITCHBACK_SPIKE_E2E_HELPER_MODE=docker-cleanup",
		"SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE="+stateFile,
		"SWITCHBACK_SPIKE_E2E_FAULT_STOP_FILE="+stopFile,
		"SWITCHBACK_SPIKE_E2E_REAL_DOCKER_SOCKET="+upstreamSocket,
		"SWITCHBACK_SPIKE_E2E_PROXY_DOCKER_SOCKET="+proxySocket,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	state := waitForFaultProxyState(t, stateFile, func(state faultProxySnapshot) bool {
		return state.Ready
	})
	if state.ProxySocket != proxySocket {
		t.Fatalf("helper proxy socket = %q, want exact configured path", state.ProxySocket)
	}
	if err := os.WriteFile(stopFile, []byte("stop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("fault helper did not exit cleanly: %v\n%s", err, output.String())
	}
	waited = true
	if _, err := os.Stat(proxySocket); !os.IsNotExist(err) {
		t.Fatalf("fault helper left Unix socket %q", proxySocket)
	}
}

type checksumProxyRuntime struct {
	ContainerID string
	Container   string
	Root        string
	ProgramPath string
	StatePath   string
}

func startChecksumProxyContainer(t *testing.T, dataRoot string) checksumProxyRuntime {
	t.Helper()
	acceptanceRoot := filepath.Join(dataRoot, "acceptance")
	if err := os.MkdirAll(acceptanceRoot, 0o700); err != nil {
		t.Fatalf("create checksum proxy acceptance directory: %v", err)
	}
	runtimeRoot, err := os.MkdirTemp(acceptanceRoot, ".checksum-proxy-")
	if err != nil {
		t.Fatalf("create checksum proxy runtime directory: %v", err)
	}
	started := false
	runtime := checksumProxyRuntime{
		Container:   fmt.Sprintf("switchback-task14-checksum-proxy-%d", time.Now().UnixNano()),
		Root:        runtimeRoot,
		ProgramPath: filepath.Join(runtimeRoot, "checksum-fault-proxy.py"),
		StatePath:   filepath.Join(runtimeRoot, "state", "fault-proxy-state.json"),
	}
	defer func() {
		if !started {
			_, _ = dockerCombinedOutput("rm", "-f", runtime.Container)
			_ = os.RemoveAll(runtime.Root)
		}
	}()
	if err := os.MkdirAll(filepath.Dir(runtime.StatePath), 0o700); err != nil {
		t.Fatalf("create checksum proxy state directory: %v", err)
	}
	if err := writeChecksumFaultProxyProgram(runtime.ProgramPath); err != nil {
		t.Fatal(err)
	}
	output, err := dockerCombinedOutput(
		"run",
		"--detach",
		"--name", runtime.Container,
		"--label", "switchback.spike.managed=true",
		"--label", "switchback.spike.task14=checksum-proxy",
		"--network", checksumProxyNetwork,
		"--network-alias", checksumProxyAlias,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--env", "PYTHONDONTWRITEBYTECODE=1",
		"--mount", "type=bind,src="+runtime.ProgramPath+",dst=/opt/switchback/checksum-fault-proxy.py,readonly",
		"--mount", "type=bind,src="+filepath.Dir(runtime.StatePath)+",dst=/var/run/switchback",
		"--entrypoint", "python",
		"switchback-spike-worker:local",
		"/opt/switchback/checksum-fault-proxy.py",
	)
	if err != nil {
		t.Fatalf("start private-network checksum proxy: %v", err)
	}
	runtime.ContainerID = strings.TrimSpace(string(output))
	if runtime.ContainerID == "" {
		t.Fatal("private-network checksum proxy returned an empty container identity")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var state faultProxySnapshot
		if err := decodeStrictJSONFile(runtime.StatePath, &state); err == nil && state.Ready {
			started = true
			return runtime
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := dockerCombinedOutput("logs", runtime.ContainerID)
	t.Fatalf("private-network checksum proxy did not become ready: %s", logs)
	return checksumProxyRuntime{}
}

func stopChecksumProxyContainer(runtime checksumProxyRuntime) error {
	var failures []string
	if runtime.ContainerID != "" {
		if _, err := dockerCombinedOutput("rm", "-f", runtime.ContainerID); err != nil {
			failures = append(failures, "remove checksum proxy container")
		}
	}
	remaining, err := dockerCombinedOutput(
		"ps",
		"-aq",
		"--filter", "label=switchback.spike.task14=checksum-proxy",
	)
	if err != nil {
		failures = append(failures, "enumerate checksum proxy containers")
	} else if len(bytes.TrimSpace(remaining)) != 0 {
		failures = append(failures, "checksum proxy container remains")
	}
	if err := os.RemoveAll(runtime.Root); err != nil {
		failures = append(failures, "remove checksum proxy runtime directory")
	}
	if _, err := os.Stat(runtime.Root); !os.IsNotExist(err) {
		failures = append(failures, "checksum proxy runtime directory remains")
	}
	if len(failures) != 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func TestChecksumFaultProxyPrivateNetworkTransport(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_CHECKSUM_FAULT_INTEGRATION") != "1" {
		t.Skip("checksum proxy transport proof runs through make spike-integration")
	}
	dataRoot := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT")
	runtime := startChecksumProxyContainer(t, dataRoot)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_ = stopChecksumProxyContainer(runtime)
		}
	})

	raw, err := dockerCombinedOutput("inspect", runtime.ContainerID)
	if err != nil {
		t.Fatal("inspect private-network checksum proxy")
	}
	var inspected []struct {
		HostConfig struct {
			PortBindings map[string]interface{} `json:"PortBindings"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Ports    map[string]interface{} `json:"Ports"`
			Networks map[string]interface{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(raw, &inspected); err != nil || len(inspected) != 1 {
		t.Fatal("decode private-network checksum proxy inspect")
	}
	if len(inspected[0].HostConfig.PortBindings) != 0 ||
		len(inspected[0].NetworkSettings.Ports) != 0 {
		t.Fatal("checksum proxy published a host port")
	}
	if _, ok := inspected[0].NetworkSettings.Networks[checksumProxyNetwork]; !ok ||
		len(inspected[0].NetworkSettings.Networks) != 1 {
		t.Fatal("checksum proxy is not isolated to the exact control network")
	}
	t.Log("checksum proxy transport: host_bindings=0 network=switchback-spike-control")

	const clientScript = `
import urllib.error
import urllib.request

checks = [
    ("http://switchback-task14-checksum-proxy:8080/spike/runs", 404),
    ("http://switchback-task14-checksum-proxy:8080/internal/v1/sessions/s/runs/r/turns/t/evidence/trace", 405),
]
for endpoint, expected in checks:
    try:
        urllib.request.urlopen(endpoint, timeout=5)
        raise SystemExit("proxy accepted a forbidden route")
    except urllib.error.HTTPError as response:
        if response.code != expected:
            raise
`
	if output, err := dockerCombinedOutput(
		"run",
		"--rm",
		"--network", checksumProxyNetwork,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--entrypoint", "python",
		"switchback-spike-worker:local",
		"-c", clientScript,
	); err != nil {
		t.Fatalf("control-network checksum proxy route probe failed: %v: %s", err, output)
	}
	t.Log("checksum proxy transport: wrong_path=404 wrong_method=405")
	if err := stopChecksumProxyContainer(runtime); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	for _, path := range []string{runtime.ProgramPath, runtime.StatePath, runtime.Root} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("checksum proxy transport test left temporary path %q", path)
		}
	}
}

func TestDockerCleanupFaultProxyAgainstRealDaemon(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_DOCKER_FAULT_INTEGRATION") != "1" {
		t.Skip("real Docker cleanup fault proof runs through make spike-integration")
	}
	dataRoot := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT")
	realSocket, err := readComposeEnvValue(
		filepath.Join(dataRoot, "env", "compose.env"),
		"SWITCHBACK_SPIKE_DOCKER_SOCKET",
	)
	if err != nil || !filepath.IsAbs(realSocket) {
		t.Fatal("real Docker socket is unavailable for cleanup fault integration")
	}
	tempRoot, err := os.MkdirTemp("/tmp", "switchback-task14-real-docker-fault-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	proxySocket := filepath.Join(tempRoot, "proxy.sock")
	listener, err := net.Listen("unix", proxySocket)
	if err != nil {
		t.Fatal(err)
	}
	state := &faultProxyState{}
	server := &http.Server{
		Handler:           newDockerEngineFaultProxy(realSocket, state),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(proxySocket)
	})

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	identity := runtimeResourceIdentity{
		WorkerSessionID:   "task14-cleanup-fault-" + suffix,
		WorkerRunID:       "task14-cleanup-run-" + suffix,
		RuntimeInstanceID: "task14-cleanup-runtime-" + suffix,
		RuntimeGeneration: 1,
	}
	volumeName := "switchback-task14-cleanup-volume-" + suffix
	populatorName := "switchback-task14-cleanup-populator-" + suffix
	containerName := "switchback-task14-cleanup-worker-" + suffix
	volumeArguments := []string{"volume", "create"}
	volumeArguments = append(volumeArguments, exactRuntimeDockerLabelArguments(identity, "runtime-secret-volume")...)
	volumeArguments = append(volumeArguments, volumeName)
	if output, err := dockerAtSocket(realSocket, volumeArguments...); err != nil ||
		strings.TrimSpace(string(output)) != volumeName {
		t.Fatalf("create real cleanup-fault volume: %v", err)
	}
	t.Cleanup(func() {
		if err := removeExactRuntimeResources(identity); err != nil {
			t.Errorf("cleanup real fault-test resources: %v", err)
		}
	})
	createArguments := []string{"create", "--name", containerName}
	createArguments = append(createArguments, exactRuntimeDockerLabelArguments(identity, "worker-runtime")...)
	createArguments = append(
		createArguments,
		"--mount",
		"type=volume,src="+volumeName+",dst=/run/switchback-secrets,readonly",
		"--entrypoint",
		"/bin/true",
		"switchback-spike-worker:local",
	)
	output, err := dockerAtSocket(
		proxySocket,
		createArguments...,
	)
	if err != nil {
		t.Fatalf("create real cleanup-fault Worker through proxy: %v", err)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		t.Fatal("real cleanup-fault Worker identity is empty")
	}
	identity.ContainerID = containerID
	populatorArguments := []string{"create", "--name", populatorName}
	populatorArguments = append(
		populatorArguments,
		exactRuntimeDockerLabelArguments(identity, "runtime-secret-populator")...,
	)
	populatorArguments = append(
		populatorArguments,
		"--mount",
		"type=volume,src="+volumeName+",dst=/run/switchback-secrets",
		"--entrypoint",
		"/bin/true",
		"switchback-spike-worker:local",
	)
	populatorOutput, err := dockerAtSocket(realSocket, populatorArguments...)
	if err != nil || strings.TrimSpace(string(populatorOutput)) == "" {
		t.Fatalf("create real cleanup-fault populator: %v", err)
	}
	if _, err := dockerAtSocket(proxySocket, "rm", "-f", containerID); err == nil {
		t.Fatal("Docker cleanup fault proxy allowed the exact target DELETE")
	}
	snapshot := state.snapshot()
	if snapshot.TargetContainerID != containerID ||
		!snapshot.FaultTriggered ||
		snapshot.FaultCount != 1 {
		t.Fatalf("real Docker cleanup fault state = %+v, want exact target rejection", snapshot)
	}
	if err := removeExactRuntimeResources(identity); err != nil {
		t.Fatalf("real daemon exact fallback cleanup failed: %v", err)
	}
	containerID = ""
	resources, err := enumerateRuntimeResources(identity)
	if err != nil || !resources.empty() {
		t.Fatalf("real daemon fallback left cleanup-fault resources: %+v err=%v", resources, err)
	}
}

func exactRuntimeDockerLabelArguments(
	identity runtimeResourceIdentity,
	managed string,
) []string {
	return []string{
		"--label", "com.switchback.managed=" + managed,
		"--label", "com.switchback.worker-session-id=" + identity.WorkerSessionID,
		"--label", "com.switchback.worker-run-id=" + identity.WorkerRunID,
		"--label", "com.switchback.runtime-instance-id=" + identity.RuntimeInstanceID,
		"--label", "com.switchback.runtime-generation=" + strconv.FormatUint(uint64(identity.RuntimeGeneration), 10),
	}
}

func TestChecksumMismatchFaultAgainstFakeProfile(t *testing.T) {
	if os.Getenv("SWITCHBACK_SPIKE_CHECKSUM_FAULT_INTEGRATION") != "1" {
		t.Skip("checksum fault proof runs through make spike-integration")
	}
	root := spikeRoot(t)
	dataRoot := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT")
	backendURL := requiredURL(t, "SWITCHBACK_SPIKE_INTEGRATION_BACKEND_URL")

	proxyRuntime := startChecksumProxyContainer(t, dataRoot)
	proxyCleaned := false
	t.Cleanup(func() {
		if !proxyCleaned {
			if cleanupErr := stopChecksumProxyContainer(proxyRuntime); cleanupErr != nil {
				t.Errorf("cleanup checksum proxy runtime: %v", cleanupErr)
			}
		}
	})
	overridePath := filepath.Join(proxyRuntime.Root, "checksum-fault.compose.yaml")
	override := fmt.Sprintf(
		"services:\n  spike-backend:\n    environment:\n"+
			"      SWITCHBACK_SPIKE_BACKEND_HTTP_BASE_URL: http://%s:8080\n",
		checksumProxyAlias,
	)
	if err := os.WriteFile(overridePath, []byte(override), 0o600); err != nil {
		t.Fatalf("write checksum Compose override: %v", err)
	}

	composeArgs := []string{
		"compose",
		"--env-file", filepath.Join(dataRoot, "env", "compose.env"),
		"-f", filepath.Join(root, "compose.yaml"),
		"-f", filepath.Join(root, "compose.integration.yaml"),
		"-f", overridePath,
		"up", "-d", "--force-recreate", "--no-deps", "spike-backend",
	}
	command := exec.Command("docker", composeArgs...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recreate Backend with checksum proxy override: %v: %s", err, output)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	waitForBackendReachable(t, client, backendURL)
	created := postJSON[createdRun](t, client, backendURL+"/spike/runs", map[string]any{
		"goal":                "repair the deterministic counter fixture",
		"acceptance_criteria": []string{"authoritative evidence must pass checksum verification"},
	})
	t.Logf(
		"checksum run created: worker_session_id=%s worker_run_id=%s",
		created.WorkerSessionID,
		created.WorkerRunID,
	)
	cleaned := false
	cleanupIdentity := runtimeResourceIdentity{
		WorkerSessionID: created.WorkerSessionID,
		WorkerRunID:     created.WorkerRunID,
	}
	t.Cleanup(func() {
		if cleaned {
			return
		}
		if cleanupErr := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second); cleanupErr != nil {
			t.Errorf("cleanup checksum fault Session: %v", cleanupErr)
		}
		forceRemoveExactRuntimeResources(t, cleanupIdentity)
	})

	failed := waitForRun(t, client, backendURL, created.WorkerRunID, func(snapshot runSnapshot) bool {
		return snapshot.State == "failed" && snapshot.ExecutionOutcome == "failed"
	})
	if err := verifyChecksumMismatchOutcome(failed); err != nil {
		t.Fatal(err)
	}
	cleanupIdentity = runtimeResourceIdentityFromSnapshot(failed, "")
	fault := waitForFaultProxyState(t, proxyRuntime.StatePath, checksumFaultProven)
	if !checksumFaultProven(fault) {
		t.Fatalf("checksum proxy state = %+v, want exact Backend checksum rejection proof", fault)
	}
	t.Logf(
		"checksum proof: backend_status=%d backend_error_code=%s authorization_present=%t original_checksum_valid=%t digest_mismatch=%t",
		fault.BackendStatusCode,
		fault.BackendErrorCode,
		fault.AuthorizationPresent,
		fault.OriginalChecksumValid,
		fault.DigestMismatch,
	)
	containerID := findExactWorkerContainer(t, created.WorkerSessionID)
	upstreamKey := os.Getenv("SWITCHBACK_SPIKE_UPSTREAM_API_KEY")
	if upstreamKey == "" {
		t.Fatal("integration upstream key label is unavailable")
	}
	tokens := []knownToken{{Label: "integration-upstream-key", Value: upstreamKey}}
	if err := verifyWorkerIsolation(containerID, dataRoot, failed, tokens); err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveProcessCapabilities(containerID, tokens); err != nil {
		t.Fatal(err)
	}
	t.Log("same-UID environment proof: litellm_uid=10001 agent_uid=10002 child_caps=0 bootstrap_and_model_denied=10001,10002")
	cleanupIdentity.ContainerID = containerID
	if err := cleanupRuntime(client, backendURL, cleanupIdentity, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	assertNoSessionRuntimeResources(t, created.WorkerSessionID)
	if err := stopChecksumProxyContainer(proxyRuntime); err != nil {
		t.Fatal(err)
	}
	proxyCleaned = true
}

func waitForBackendReachable(t *testing.T, client *http.Client, backendURL string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(backendURL + "/spike/runs/not-ready")
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Backend did not become reachable at %s", backendURL)
}

func assertNoSessionRuntimeResources(t *testing.T, sessionID string) {
	t.Helper()
	resources, err := enumerateRuntimeResources(runtimeResourceIdentity{
		WorkerSessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("enumerate runtime resources for session %s: %v", sessionID, err)
	}
	if !resources.empty() {
		t.Fatalf("runtime resource residue remains for session %s: %+v", sessionID, resources)
	}
}

func forceRemoveExactRuntimeResources(t *testing.T, identity runtimeResourceIdentity) {
	t.Helper()
	if err := removeExactRuntimeResources(identity); err != nil {
		t.Errorf("exact runtime fallback cleanup: %v", err)
	}
}

func removeExactRuntimeResources(identity runtimeResourceIdentity) error {
	if identity.WorkerSessionID == "" ||
		identity.WorkerRunID == "" ||
		identity.RuntimeInstanceID == "" ||
		identity.RuntimeGeneration == 0 {
		return fmt.Errorf("exact runtime identity is unavailable")
	}
	resources, err := enumerateRuntimeResources(identity)
	if err != nil {
		return fmt.Errorf("enumerate exact runtime resources: %w", err)
	}
	for _, resource := range []struct {
		kind    string
		managed string
		values  []string
	}{
		{kind: "worker", managed: "worker-runtime", values: resources.Workers},
		{kind: "populator", managed: "runtime-secret-populator", values: resources.Populators},
		{kind: "volume", managed: "runtime-secret-volume", values: resources.Volumes},
	} {
		for _, resourceID := range resource.values {
			if err := verifyExactRuntimeResourceLabels(resource.kind, resourceID, resource.managed, identity); err != nil {
				return fmt.Errorf("reject %s %s: %w", resource.kind, resourceID, err)
			}
			var removeErr error
			if resource.kind == "volume" {
				_, removeErr = dockerCombinedOutput("volume", "rm", resourceID)
			} else {
				_, removeErr = dockerCombinedOutput("rm", "-f", resourceID)
			}
			if removeErr != nil {
				return fmt.Errorf("remove exact %s %s", resource.kind, resourceID)
			}
		}
	}
	resources, err = enumerateRuntimeResources(identity)
	if err != nil {
		return fmt.Errorf("verify exact runtime cleanup: %w", err)
	}
	if !resources.empty() {
		return fmt.Errorf("exact runtime cleanup left residue: %+v", resources)
	}
	return nil
}

func readComposeEnvValue(path, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found && key == name && value != "" {
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("compose environment value is missing")
}

func dockerAtSocket(socketPath string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commandArguments := append([]string{"--host", "unix://" + socketPath}, arguments...)
	return exec.CommandContext(ctx, "docker", commandArguments...).CombinedOutput()
}

func contentDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func doProxyRequest(
	t *testing.T,
	client *http.Client,
	method, endpoint string,
	body io.Reader,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newChecksumFaultProxy(upstream string, state *faultProxyState) http.Handler {
	upstreamURL, parseErr := url.Parse(upstream)
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		evidenceKind, exactEvidencePath := exactEvidenceUploadKind(request.URL)
		if !exactEvidencePath {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodPut {
			writer.Header().Set("Allow", http.MethodPut)
			http.Error(writer, "fault proxy accepts only evidence PUT", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(request.Header.Get("Authorization")) == "" {
			http.Error(writer, "worker authorization is required", http.StatusUnauthorized)
			return
		}
		if parseErr != nil || upstreamURL.Scheme != "http" || upstreamURL.Host == "" {
			http.Error(writer, "fault proxy upstream is invalid", http.StatusBadGateway)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "fault proxy could not read request", http.StatusBadGateway)
			return
		}
		checksumHeader := request.Header.Get("X-Content-SHA256")
		originalChecksum, checksumErr := hex.DecodeString(checksumHeader)
		checksumValid := checksumErr == nil && len(originalChecksum) == sha256.Size
		injectFault := state.claimChecksumAttempt()
		if injectFault {
			if len(body) == 0 {
				body = []byte{1}
			} else {
				body[0] ^= 0xff
			}
		}
		endpoint := *upstreamURL
		endpoint.Path = request.URL.Path
		endpoint.RawPath = request.URL.RawPath
		endpoint.RawQuery = request.URL.RawQuery
		forwarded, err := http.NewRequestWithContext(
			request.Context(),
			request.Method,
			endpoint.String(),
			bytes.NewReader(body),
		)
		if err != nil {
			http.Error(writer, "fault proxy could not create request", http.StatusBadGateway)
			return
		}
		forwarded.Header = request.Header.Clone()
		forwarded.ContentLength = int64(len(body))
		response, err := client.Do(forwarded)
		if err != nil {
			http.Error(writer, "fault proxy upstream request failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			http.Error(writer, "fault proxy could not read upstream response", http.StatusBadGateway)
			return
		}
		if injectFault {
			pathDigest := sha256.Sum256([]byte(request.URL.EscapedPath()))
			mutatedDigest := sha256.Sum256(body)
			backendErrorCode := ""
			var backendError struct {
				Error string `json:"error"`
			}
			if response.StatusCode == http.StatusBadRequest &&
				json.Unmarshal(responseBody, &backendError) == nil &&
				backendError.Error == "evidence checksum mismatch" {
				backendErrorCode = "evidence_checksum_mismatch"
			}
			state.recordChecksumResult(faultProxySnapshot{
				ChecksumRequestMatched: true,
				EvidencePathSHA256:     hex.EncodeToString(pathDigest[:]),
				EvidenceKind:           evidenceKind,
				AuthorizationPresent:   true,
				OriginalChecksumValid:  checksumValid,
				OriginalChecksumSHA256: checksumHeader,
				MutatedBodySHA256:      hex.EncodeToString(mutatedDigest[:]),
				DigestMismatch:         checksumValid && subtleDigestMismatch(originalChecksum, mutatedDigest[:]),
				BackendStatusCode:      response.StatusCode,
				BackendErrorCode:       backendErrorCode,
			})
		}
		for name, values := range response.Header {
			for _, value := range values {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.StatusCode)
		_, _ = writer.Write(responseBody)
	})
}

func exactEvidenceUploadKind(requestURL *url.URL) (string, bool) {
	if requestURL == nil || requestURL.RawQuery != "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(requestURL.EscapedPath(), "/"), "/")
	if len(parts) != 10 ||
		parts[0] != "internal" ||
		parts[1] != "v1" ||
		parts[2] != "sessions" ||
		parts[3] == "" ||
		parts[4] != "runs" ||
		parts[5] == "" ||
		parts[6] != "turns" ||
		parts[7] == "" ||
		parts[8] != "evidence" ||
		parts[9] == "" {
		return "", false
	}
	return parts[9], true
}

func subtleDigestMismatch(left, right []byte) bool {
	if len(left) != len(right) {
		return true
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference != 0
}

type workerCreateContextKey struct{}

func newDockerEngineFaultProxy(realSocket string, state *faultProxyState) http.Handler {
	target, _ := url.Parse("http://docker")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", realSocket)
		},
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, "Docker Engine proxy request failed", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		isWorkerCreate, _ := response.Request.Context().Value(workerCreateContextKey{}).(bool)
		if !isWorkerCreate || response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return fmt.Errorf("read Docker create response")
		}
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		var created struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
			return fmt.Errorf("decode Docker create response")
		}
		state.setTargetContainer(created.ID)
		return nil
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			targetID := dockerContainerIDFromPath(request.URL.Path)
			snapshot := state.snapshot()
			if targetID != "" && targetID == snapshot.TargetContainerID {
				state.recordDockerCleanupFault()
				writeTestJSON(writer, http.StatusInternalServerError, map[string]string{
					"message": "injected target Worker cleanup failure",
				})
				return
			}
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/containers/create") {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, "Docker Engine proxy could not read create request", http.StatusBadGateway)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			request.ContentLength = int64(len(body))
			var create struct {
				Labels map[string]string `json:"Labels"`
			}
			if json.Unmarshal(body, &create) == nil &&
				create.Labels["com.switchback.managed"] == "worker-runtime" {
				request = request.WithContext(
					context.WithValue(request.Context(), workerCreateContextKey{}, true),
				)
			}
		}
		proxy.ServeHTTP(writer, request)
	})
}

func dockerContainerIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == "containers" {
			return parts[index+1]
		}
	}
	return ""
}

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func TestLangfuseVerificationRejectsExtraAndWrongParentGeneration(t *testing.T) {
	expected := langfuseExpectation{
		TraceID:               "trace-1",
		TurnIDs:               []string{"turn-1", "turn-2"},
		ModelObservationCount: 2,
	}
	valid := langfuseTrace{
		ID: "trace-1",
		Observations: []langfuseObservation{
			{ID: "turn-1", TraceID: "trace-1", Type: "SPAN", Name: "agent.turn"},
			{ID: "turn-2", TraceID: "trace-1", Type: "SPAN", Name: "agent.turn"},
			{ID: "generation-1", TraceID: "trace-1", Type: "GENERATION", ParentObservationID: "turn-1"},
			{ID: "generation-2", TraceID: "trace-1", Type: "GENERATION", ParentObservationID: "turn-2"},
		},
	}
	if _, err := verifyLangfuseTrace(valid, expected); err != nil {
		t.Fatalf("valid Langfuse trace rejected: %v", err)
	}
	extra := valid
	extra.Observations = append(append([]langfuseObservation(nil), valid.Observations...),
		langfuseObservation{ID: "generation-extra", TraceID: "trace-1", Type: "GENERATION", ParentObservationID: "turn-2"})
	if _, err := verifyLangfuseTrace(extra, expected); err == nil {
		t.Fatal("Langfuse verification accepted an extra generation")
	}
	wrongParent := valid
	wrongParent.Observations = append([]langfuseObservation(nil), valid.Observations...)
	wrongParent.Observations[2].ParentObservationID = "turn-unknown"
	if _, err := verifyLangfuseTrace(wrongParent, expected); err == nil {
		t.Fatal("Langfuse verification accepted a wrong-parent generation")
	}
}

func TestTurnPatchVerificationRejectsRepeatedProductionFix(t *testing.T) {
	turnTwoPatch := strings.Join([]string{
		"diff --git a/counter/counter.go b/counter/counter.go",
		"--- a/counter/counter.go",
		"+++ b/counter/counter.go",
		"@@ -1 +1 @@",
		"-return current - delta",
		"+return current + delta",
		"diff --git a/counter/counter_test.go b/counter/counter_test.go",
		"--- a/counter/counter_test.go",
		"+++ b/counter/counter_test.go",
		"+func TestAddZeroDelta(t *testing.T) { if got := Add(7, 0); got != 7 { t.Fatal(got) } }",
	}, "\n")
	if err := verifyTurnPatch(2, turnTwoPatch); err == nil {
		t.Fatal("Turn 2 patch accepted a repeated Turn 1 production fix")
	}
}

func TestZeroDeltaMutationProofRejectsUnreachableAssertion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "counter"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.test/zero-delta\n\ngo 1.26.0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "counter", "counter.go"),
		[]byte("package counter\n\nfunc Add(current, delta int) int { return current + delta }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(root, "counter", "counter_test.go")
	unreachable := `package counter

import "testing"

func TestAddZeroDelta(t *testing.T) {
	if false {
		if got := Add(7, 0); got != 7 {
			t.Fatal(got)
		}
	}
}
`
	if err := os.WriteFile(testPath, []byte(unreachable), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyZeroDeltaMutationProof(root); err == nil {
		t.Fatal("mutation proof accepted an unreachable zero-delta assertion")
	}
	valid := strings.Replace(unreachable, "\tif false {\n", "", 1)
	valid = strings.Replace(valid, "\n\t}\n}", "\n}", 1)
	if err := os.WriteFile(testPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyZeroDeltaMutationProof(root); err != nil {
		t.Fatalf("real zero-delta mutation proof rejected: %v", err)
	}
}

func TestSecretScanReportsLabelWithoutEchoingValue(t *testing.T) {
	const secret = "must-never-appear-in-an-error"
	err := verifyTokenAbsence(
		map[string][]byte{"evidence": []byte("prefix " + secret + " suffix")},
		[]knownToken{{Label: "token-7", Value: secret}},
	)
	if err == nil {
		t.Fatal("secret scan accepted a known token")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "token-7") {
		t.Fatalf("secret scan error is not safely labeled: %q", err)
	}
}

func TestSecretBoundaryReportRejectsReadableModeAndAgentTokenLabels(t *testing.T) {
	valid := secretBoundaryReport{
		BootstrapUID:  0,
		BootstrapGID:  0,
		BootstrapMode: 0o400,
		SecretUID:     0,
		SecretGID:     0,
		SecretMode:    0o400,
	}
	valid.Processes = append(valid.Processes,
		secretBoundaryProcess{
			UID:                10001,
			Role:               "litellm",
			TrustedUpstreamKey: true,
		},
		secretBoundaryProcess{UID: 10002, Role: "agent"},
	)
	if err := validateSecretBoundaryReport(valid); err != nil {
		t.Fatalf("valid secret boundary report rejected: %v", err)
	}
	readable := valid
	readable.SecretMode = 0o444
	if err := validateSecretBoundaryReport(readable); err == nil {
		t.Fatal("world-readable model secret mode was accepted")
	}
	readableBootstrap := valid
	readableBootstrap.BootstrapMode = 0o444
	if err := validateSecretBoundaryReport(readableBootstrap); err == nil {
		t.Fatal("world-readable bootstrap mode was accepted")
	}
	unscannable := valid
	unscannable.ProcessScanErrors = 1
	if err := validateSecretBoundaryReport(unscannable); err == nil {
		t.Fatal("unreadable child process environment was accepted")
	}
	exposed := valid
	exposed.Processes = append([]secretBoundaryProcess(nil), valid.Processes...)
	exposed.Processes[1].KnownValueLabel = true
	if err := validateSecretBoundaryReport(exposed); err == nil {
		t.Fatal("Codex/Tool child known-token exposure was accepted")
	}
}

func TestLiveSecretBoundaryUsesSameUIDInspectors(t *testing.T) {
	tempRoot := t.TempDir()
	binDir := filepath.Join(tempRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tempRoot, "docker.log")
	writeExecutable(t, binDir, "docker", `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$SWITCHBACK_E2E_TEST_LOG"
case "$*" in
	"exec worker python -c "*)
		printf '{"bootstrap_uid":0,"bootstrap_gid":0,"bootstrap_mode":256,"secret_uid":0,"secret_gid":0,"secret_mode":256}\n'
		;;
	"exec -i --user 10001:10001 worker python -c "*)
		cat >/dev/null
		printf '{"process_scan_errors":0,"processes":[{"uid":10001,"role":"litellm","forbidden_name_labels":[],"known_value_label":false,"trusted_upstream_key":true}]}\n'
		;;
	"exec -i --user 10002:10002 worker python -c "*)
		cat >/dev/null
		printf '{"process_scan_errors":0,"processes":[{"uid":10002,"role":"agent","forbidden_name_labels":[],"known_value_label":false,"trusted_upstream_key":false}]}\n'
		;;
	*"cat /run/switchback-secrets/"*)
		printf 'cat: secret file: Permission denied\n' >&2
		exit 1
		;;
	*)
		exit 77
		;;
esac
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SWITCHBACK_E2E_TEST_LOG", logPath)
	t.Setenv("SWITCHBACK_SPIKE_UPSTREAM_API_KEY", "upstream-test-key")
	if err := verifyLiveSecretBoundary(
		"worker",
		[]knownToken{{Label: "token-1", Value: "upstream-test-key"}},
	); err != nil {
		t.Fatalf("same-UID live secret inspection failed: %v", err)
	}
	log := readFile(t, logPath)
	for _, expected := range []string{
		"exec -i --user 10001:10001 worker python -c",
		"exec -i --user 10002:10002 worker python -c",
		"exec --user 10001:10001 worker sh -c cat /run/switchback-secrets/bootstrap.json >/dev/null",
		"exec --user 10001:10001 worker sh -c cat /run/switchback-secrets/model >/dev/null",
		"exec --user 10002:10002 worker sh -c cat /run/switchback-secrets/bootstrap.json >/dev/null",
		"exec --user 10002:10002 worker sh -c cat /run/switchback-secrets/model >/dev/null",
	} {
		if !strings.Contains(log, expected) {
			t.Fatalf("live secret inspection missed %q:\n%s", expected, log)
		}
	}
	if strings.Contains(log, "exec -i worker python -c") {
		t.Fatalf("live secret inspection used a root cross-UID environment scan:\n%s", log)
	}
}

func TestVerifyLangfuseScriptRejectsPartialRuntimeMetadata(t *testing.T) {
	root := spikeRoot(t)
	command := exec.Command(
		filepath.Join(root, "scripts", "verify-langfuse"),
		"trace-1",
		"turn-1",
		"turn-2",
		"generation-1",
		"turn-1",
		"generation-2",
		"turn-2",
	)
	command.Env = append(environmentWithout(
		"LANGFUSE_BASE_URL",
		"LANGFUSE_PUBLIC_KEY",
		"LANGFUSE_SECRET_KEY",
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_SESSION_ID",
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_RUN_ID",
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_INSTANCE_ID",
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_GENERATION",
		"SWITCHBACK_SPIKE_LANGFUSE_AGENT_SESSION_ID",
	),
		"LANGFUSE_BASE_URL=http://127.0.0.1:1",
		"LANGFUSE_PUBLIC_KEY=pk_test",
		"LANGFUSE_SECRET_KEY=sk_test",
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_SESSION_ID=session-1",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("verify-langfuse accepted partial runtime metadata: %s", output)
	}
	if !strings.Contains(string(output), "runtime metadata must be supplied together") {
		t.Fatalf("verify-langfuse output = %q, want fail-closed metadata validation", output)
	}
}

func TestFailureMatrixInvalidCredentialCannotFalseComplete(t *testing.T) {
	failed := runSnapshot{
		State:            "failed",
		ExecutionOutcome: "failed",
		SpikeAcceptance:  "pending",
	}
	failed.Diagnostics = append(failed.Diagnostics, struct {
		Kind   string `json:"kind"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}{Kind: "turn_failed", Code: "agent_prompt_failed", Detail: "model route rejected the request"})
	if err := verifyInvalidCredentialOutcome(failed); err != nil {
		t.Fatalf("valid invalid-credential outcome rejected: %v", err)
	}
	failed.ExecutionOutcome = "completed"
	if err := verifyInvalidCredentialOutcome(failed); err == nil {
		t.Fatal("invalid-credential matrix accepted false completion")
	}
}

func TestFailureMatrixLangfuseUnavailablePreservesCompletedExecution(t *testing.T) {
	snapshot := runSnapshot{
		State:            "completed",
		ExecutionOutcome: "completed",
		SpikeAcceptance:  "failed",
	}
	snapshot.Diagnostics = append(snapshot.Diagnostics, struct {
		Kind   string `json:"kind"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}{Kind: "langfuse_projection", Code: "acceptance_failed", Detail: "query unavailable"})
	if err := verifyLangfuseUnavailableOutcome(snapshot); err != nil {
		t.Fatalf("valid Langfuse-unavailable outcome rejected: %v", err)
	}
	snapshot.ExecutionOutcome = "failed"
	if err := verifyLangfuseUnavailableOutcome(snapshot); err == nil {
		t.Fatal("Langfuse failure was allowed to rewrite completed execution")
	}
}

func TestFailureMatrixSecondRunRequiresIndependentIdentities(t *testing.T) {
	first := acceptanceSummary{
		SchemaVersion:       "switchback.spike.acceptance.v1",
		WorkerSessionID:     "session-1",
		WorkerRunID:         "run-1",
		RuntimeInstanceID:   "runtime-1",
		AgentSessionID:      "agent-1",
		ContainerID:         "container-1",
		AgentTurnIDs:        []string{"turn-1", "turn-2"},
		TraceID:             "trace-1",
		ExecutionOutcome:    "completed",
		SpikeAcceptance:     "passed",
		FixtureDigestBefore: "fixture-digest",
		FixtureDigestAfter:  "fixture-digest",
		EvidenceVerified:    true,
		TokenAbsence:        true,
		LangfuseVerified:    true,
		CleanupConfirmed:    true,
	}
	second := first
	second.WorkerSessionID = "session-2"
	second.WorkerRunID = "run-2"
	second.RuntimeInstanceID = "runtime-2"
	second.AgentSessionID = "agent-2"
	second.ContainerID = "container-2"
	second.AgentTurnIDs = []string{"turn-3", "turn-4"}
	second.TraceID = "trace-2"
	if err := verifyIndependentSuccessfulRuns(first, second); err != nil {
		t.Fatalf("independent second run rejected: %v", err)
	}
	second.ContainerID = first.ContainerID
	if err := verifyIndependentSuccessfulRuns(first, second); err == nil {
		t.Fatal("second run accepted a reused container identity")
	}
}

func TestCompareRealE2ESummaries(t *testing.T) {
	firstPath := os.Getenv("SWITCHBACK_SPIKE_E2E_FIRST_SUMMARY")
	secondPath := os.Getenv("SWITCHBACK_SPIKE_E2E_SECOND_SUMMARY")
	if firstPath == "" && secondPath == "" {
		t.Skip("real repeated-run summaries were not requested")
	}
	if !filepath.IsAbs(firstPath) || !filepath.IsAbs(secondPath) {
		t.Fatal("repeated-run summary paths must be absolute")
	}
	var first acceptanceSummary
	var second acceptanceSummary
	if err := decodeStrictJSONFile(firstPath, &first); err != nil {
		t.Fatal(err)
	}
	if err := decodeStrictJSONFile(secondPath, &second); err != nil {
		t.Fatal(err)
	}
	if err := verifyIndependentSuccessfulRuns(first, second); err != nil {
		t.Fatal(err)
	}
}

func verifyInvalidCredentialOutcome(snapshot runSnapshot) error {
	if snapshot.State != "failed" ||
		snapshot.ExecutionOutcome != "failed" ||
		snapshot.SpikeAcceptance != "pending" {
		return fmt.Errorf("invalid credential did not produce failed execution without acceptance")
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Kind == "turn_failed" &&
			diagnostic.Code == "agent_prompt_failed" &&
			diagnostic.Detail != "" {
			return nil
		}
	}
	return fmt.Errorf("invalid credential did not retain the bounded Agent prompt failure")
}

func verifyChecksumMismatchOutcome(snapshot runSnapshot) error {
	if snapshot.State != "failed" ||
		snapshot.ExecutionOutcome != "failed" ||
		snapshot.SpikeAcceptance == "passed" {
		return fmt.Errorf("checksum mismatch did not prevent authoritative completion")
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Kind == "turn_failed" &&
			diagnostic.Code == "evidence_upload_failed" &&
			diagnostic.Detail != "" {
			return nil
		}
	}
	return fmt.Errorf("checksum mismatch did not produce the evidence upload failure")
}

func checksumFaultProven(snapshot faultProxySnapshot) bool {
	originalChecksum, originalErr := hex.DecodeString(snapshot.OriginalChecksumSHA256)
	mutatedDigest, mutatedErr := hex.DecodeString(snapshot.MutatedBodySHA256)
	pathDigest, pathErr := hex.DecodeString(snapshot.EvidencePathSHA256)
	return snapshot.FaultTriggered &&
		snapshot.FaultCount == 1 &&
		snapshot.ChecksumRequestMatched &&
		snapshot.AuthorizationPresent &&
		snapshot.OriginalChecksumValid &&
		originalErr == nil &&
		len(originalChecksum) == sha256.Size &&
		mutatedErr == nil &&
		len(mutatedDigest) == sha256.Size &&
		snapshot.DigestMismatch &&
		!bytes.Equal(originalChecksum, mutatedDigest) &&
		pathErr == nil &&
		len(pathDigest) == sha256.Size &&
		snapshot.EvidenceKind != "" &&
		snapshot.BackendStatusCode == http.StatusBadRequest &&
		snapshot.BackendErrorCode == "evidence_checksum_mismatch"
}

func readFaultProxyState(t *testing.T) faultProxySnapshot {
	t.Helper()
	path := requiredAbsoluteEnv(t, "SWITCHBACK_SPIKE_E2E_FAULT_STATE_FILE")
	var state faultProxySnapshot
	if err := decodeStrictJSONFile(path, &state); err != nil {
		t.Fatalf("read fault proxy state: %v", err)
	}
	return state
}

func verifyInvalidCredentialInjection(t *testing.T, dataRoot string) {
	t.Helper()
	if os.Getenv("SWITCHBACK_SPIKE_INVALID_CREDENTIAL_INJECTED") != "1" ||
		!strings.Contains(filepath.ToSlash(dataRoot), "/variants/invalid-upstream-credential-") {
		t.Fatal("invalid upstream credential variant did not use its isolated harness data root")
	}
	path := filepath.Join(dataRoot, "env", "upstream-model.key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal("invalid upstream credential variant did not materialize its model key")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("invalid upstream credential model key does not have the required owner-only mode")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("invalid upstream credential model key could not be read for harness verification")
	}
	if strings.TrimSuffix(string(content), "\n") != invalidUpstreamCredential ||
		os.Getenv("SWITCHBACK_SPIKE_UPSTREAM_API_KEY") != invalidUpstreamCredential {
		t.Fatal("invalid upstream credential was not injected into the real build input")
	}
}

func verifyLangfuseUnavailableOutcome(snapshot runSnapshot) error {
	if snapshot.State != "completed" ||
		snapshot.ExecutionOutcome != "completed" ||
		snapshot.SpikeAcceptance != "failed" {
		return fmt.Errorf("Langfuse failure did not preserve completed execution and fail acceptance")
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Kind == "langfuse_projection" &&
			diagnostic.Code == "acceptance_failed" &&
			diagnostic.Detail != "" {
			return nil
		}
	}
	return fmt.Errorf("Langfuse failure diagnostic is absent")
}

func verifyIndependentSuccessfulRuns(first, second acceptanceSummary) error {
	for index, summary := range []acceptanceSummary{first, second} {
		if summary.SchemaVersion != "switchback.spike.acceptance.v1" ||
			summary.ExecutionOutcome != "completed" ||
			summary.SpikeAcceptance != "passed" ||
			summary.FixtureDigestBefore == "" ||
			summary.FixtureDigestBefore != summary.FixtureDigestAfter ||
			!summary.EvidenceVerified ||
			!summary.TokenAbsence ||
			!summary.LangfuseVerified ||
			!summary.CleanupConfirmed ||
			len(summary.AgentTurnIDs) != 2 ||
			summary.AgentTurnIDs[0] == summary.AgentTurnIDs[1] {
			return fmt.Errorf("run %d does not have the same successful functional result", index+1)
		}
	}
	firstIDs := []string{
		first.WorkerSessionID,
		first.WorkerRunID,
		first.RuntimeInstanceID,
		first.AgentSessionID,
		first.ContainerID,
		first.TraceID,
		first.AgentTurnIDs[0],
		first.AgentTurnIDs[1],
	}
	secondIDs := []string{
		second.WorkerSessionID,
		second.WorkerRunID,
		second.RuntimeInstanceID,
		second.AgentSessionID,
		second.ContainerID,
		second.TraceID,
		second.AgentTurnIDs[0],
		second.AgentTurnIDs[1],
	}
	for index, firstID := range firstIDs {
		if firstID == "" || secondIDs[index] == "" || firstID == secondIDs[index] {
			return fmt.Errorf("run identity %d was empty or reused", index)
		}
	}
	if first.FixtureDigestBefore != second.FixtureDigestBefore {
		return fmt.Errorf("the two runs did not start from the same fixture")
	}
	return nil
}

func verifyRuntimeContinuity(first, second runtimeIdentity) error {
	if first.WorkerSessionID == "" ||
		first.WorkerRunID == "" ||
		first.RuntimeInstanceID == "" ||
		first.AgentSessionID == "" ||
		first.ContainerID == "" ||
		first.WorkingCopy == "" ||
		first.AgentTurnID == "" ||
		first.TraceID == "" {
		return fmt.Errorf("first Turn runtime identity is incomplete")
	}
	if first.AgentTurnID == second.AgentTurnID || second.AgentTurnID == "" {
		return fmt.Errorf("second Turn must use a new AgentTurn identity")
	}
	comparisons := []struct {
		name   string
		first  any
		second any
	}{
		{"worker_session_id", first.WorkerSessionID, second.WorkerSessionID},
		{"worker_run_id", first.WorkerRunID, second.WorkerRunID},
		{"runtime_instance_id", first.RuntimeInstanceID, second.RuntimeInstanceID},
		{"runtime_generation", first.RuntimeGeneration, second.RuntimeGeneration},
		{"agent_session_id", first.AgentSessionID, second.AgentSessionID},
		{"container_id", first.ContainerID, second.ContainerID},
		{"working_copy", first.WorkingCopy, second.WorkingCopy},
		{"trace_id", first.TraceID, second.TraceID},
	}
	for _, comparison := range comparisons {
		if comparison.first != comparison.second {
			return fmt.Errorf("%s changed between Turns", comparison.name)
		}
	}
	if first.RuntimeGeneration != 1 {
		return fmt.Errorf("runtime generation = %d, want 1", first.RuntimeGeneration)
	}
	return nil
}

func writeManifestFixture(t *testing.T) (string, sharedevidence.TurnManifest) {
	t.Helper()
	runRoot := t.TempDir()
	turnID := "turn-2"
	turnRoot := filepath.Join(runRoot, "turns", turnID)
	if err := os.MkdirAll(turnRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	kinds := []struct {
		kind string
		name string
	}{
		{sharedevidence.KindReport, "report.md"},
		{sharedevidence.KindRawACP, "raw-acp.jsonl"},
		{sharedevidence.KindTrace, "trace.jsonl"},
		{sharedevidence.KindPatch, "changes.patch"},
	}
	objects := make([]sharedevidence.ObjectRef, 0, len(kinds))
	for _, kind := range kinds {
		content := []byte("fixture-" + kind.kind)
		path := filepath.Join(turnRoot, kind.name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		objects = append(objects, sharedevidence.ObjectRef{
			Kind: kind.kind,
			Path: filepath.Join(
				"/var/lib/switchback-spike/objects",
				"session-1",
				"run-1",
				"turns",
				turnID,
				kind.name,
			),
			SHA256: hex.EncodeToString(digest[:]),
			Bytes:  int64(len(content)),
		})
	}
	manifest := sharedevidence.TurnManifest{
		SchemaVersion:     sharedevidence.TurnManifestSchemaVersion,
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-1",
		AgentTurnID:       turnID,
		TraceID:           "trace-1",
		BeforeTree:        strings.Repeat("1", 40),
		AfterTree:         strings.Repeat("2", 40),
		Objects:           objects,
	}
	if err := writeStrictJSON(filepath.Join(runRoot, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return runRoot, manifest
}

func manifestIdentity(manifest sharedevidence.TurnManifest) runtimeIdentity {
	return runtimeIdentity{
		WorkerSessionID:   manifest.WorkerSessionID,
		WorkerRunID:       manifest.WorkerRunID,
		RuntimeInstanceID: manifest.RuntimeInstanceID,
		RuntimeGeneration: manifest.RuntimeGeneration,
		AgentSessionID:    manifest.AgentSessionID,
		AgentTurnID:       manifest.AgentTurnID,
		TraceID:           manifest.TraceID,
	}
}

func verifyManifest(
	runRoot string,
	expected runtimeIdentity,
) (sharedevidence.TurnManifest, error) {
	var manifest sharedevidence.TurnManifest
	if err := decodeStrictJSONFile(filepath.Join(runRoot, "manifest.json"), &manifest); err != nil {
		return manifest, err
	}
	if manifest.SchemaVersion != sharedevidence.TurnManifestSchemaVersion ||
		manifest.WorkerSessionID != expected.WorkerSessionID ||
		manifest.WorkerRunID != expected.WorkerRunID ||
		manifest.RuntimeInstanceID != expected.RuntimeInstanceID ||
		manifest.RuntimeGeneration != expected.RuntimeGeneration ||
		manifest.AgentSessionID != expected.AgentSessionID ||
		manifest.AgentTurnID != expected.AgentTurnID ||
		manifest.TraceID != expected.TraceID ||
		expected.TraceID == "" {
		return manifest, fmt.Errorf("manifest identity does not match the exact completed Turn")
	}
	if !validTreeID(manifest.BeforeTree) || !validTreeID(manifest.AfterTree) {
		return manifest, fmt.Errorf("manifest Git tree identity is invalid")
	}
	wanted := []struct {
		kind string
		name string
	}{
		{sharedevidence.KindReport, "report.md"},
		{sharedevidence.KindRawACP, "raw-acp.jsonl"},
		{sharedevidence.KindTrace, "trace.jsonl"},
		{sharedevidence.KindPatch, "changes.patch"},
	}
	if len(manifest.Objects) != len(wanted) {
		return manifest, fmt.Errorf("manifest object cardinality = %d, want %d", len(manifest.Objects), len(wanted))
	}
	turnRoot := filepath.Join(runRoot, "turns", manifest.AgentTurnID)
	entries, err := os.ReadDir(turnRoot)
	if err != nil {
		return manifest, fmt.Errorf("read Turn evidence: %w", err)
	}
	if len(entries) != len(wanted) {
		return manifest, fmt.Errorf("Turn evidence cardinality = %d, want %d", len(entries), len(wanted))
	}
	for index, want := range wanted {
		object := manifest.Objects[index]
		expectedPath := filepath.Join(
			"/var/lib/switchback-spike/objects",
			manifest.WorkerSessionID,
			manifest.WorkerRunID,
			"turns",
			manifest.AgentTurnID,
			want.name,
		)
		if object.Kind != want.kind || filepath.Clean(object.Path) != expectedPath {
			return manifest, fmt.Errorf("manifest object %d kind/path mismatch", index)
		}
		content, err := os.ReadFile(filepath.Join(turnRoot, want.name))
		if err != nil {
			return manifest, fmt.Errorf("read manifest object %d: %w", index, err)
		}
		digest := sha256.Sum256(content)
		if object.SHA256 != hex.EncodeToString(digest[:]) {
			return manifest, fmt.Errorf("manifest object %d checksum mismatch", index)
		}
		if object.Bytes != int64(len(content)) {
			return manifest, fmt.Errorf("manifest object %d byte size mismatch", index)
		}
	}
	return manifest, nil
}

func verifyLangfuseTrace(
	trace langfuseTrace,
	expected langfuseExpectation,
) ([]expectedGeneration, error) {
	if trace.ID != expected.TraceID || len(expected.TurnIDs) != 2 ||
		expected.TurnIDs[0] == expected.TurnIDs[1] {
		return nil, fmt.Errorf("Langfuse Trace identity or expected Turns are invalid")
	}
	expectedTurns := map[string]bool{
		expected.TurnIDs[0]: false,
		expected.TurnIDs[1]: false,
	}
	var spans int
	var generations []expectedGeneration
	generationIDs := make(map[string]struct{})
	generationsByTurn := make(map[string]int)
	for _, observation := range trace.Observations {
		switch {
		case observation.Type == "SPAN" && observation.Name == "agent.turn":
			spans++
			if observation.TraceID != expected.TraceID {
				return nil, fmt.Errorf("Langfuse AgentTurn span has wrong Trace identity")
			}
			if _, ok := expectedTurns[observation.ID]; !ok || expectedTurns[observation.ID] {
				return nil, fmt.Errorf("Langfuse AgentTurn span is extra or duplicated")
			}
			expectedTurns[observation.ID] = true
		case observation.Type == "GENERATION":
			if observation.ID == "" || observation.TraceID != expected.TraceID {
				return nil, fmt.Errorf("Langfuse generation identity is invalid")
			}
			if _, duplicate := generationIDs[observation.ID]; duplicate {
				return nil, fmt.Errorf("Langfuse generation identity is duplicated")
			}
			if _, ok := expectedTurns[observation.ParentObservationID]; !ok {
				return nil, fmt.Errorf("Langfuse generation parent is not an expected AgentTurn")
			}
			generationIDs[observation.ID] = struct{}{}
			generationsByTurn[observation.ParentObservationID]++
			generations = append(generations, expectedGeneration{
				ID:           observation.ID,
				ParentTurnID: observation.ParentObservationID,
			})
		}
	}
	if spans != 2 || !expectedTurns[expected.TurnIDs[0]] || !expectedTurns[expected.TurnIDs[1]] {
		return nil, fmt.Errorf("Langfuse requires exactly the two expected AgentTurn spans")
	}
	if len(generations) != expected.ModelObservationCount {
		return nil, fmt.Errorf(
			"Langfuse generation cardinality = %d, want %d",
			len(generations),
			expected.ModelObservationCount,
		)
	}
	for _, turnID := range expected.TurnIDs {
		if generationsByTurn[turnID] == 0 {
			return nil, fmt.Errorf("Langfuse AgentTurn %s has no generation", turnID)
		}
	}
	return generations, nil
}

func verifyTurnPatch(turn int, patch string) error {
	var files []string
	for _, line := range strings.Split(patch, "\n") {
		const prefix = "diff --git a/"
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 4 {
			return fmt.Errorf("Turn %d patch has malformed diff header", turn)
		}
		files = append(files, strings.TrimPrefix(parts[2], "a/"))
	}
	switch turn {
	case 1:
		if len(files) != 1 || files[0] != "counter/counter.go" {
			return fmt.Errorf("Turn 1 must touch only counter/counter.go, got %v", files)
		}
		if !strings.Contains(patch, "-\treturn current - delta") ||
			!strings.Contains(patch, "+\treturn current + delta") ||
			strings.Contains(patch, "_test.go") {
			return fmt.Errorf("Turn 1 patch is not the production-only counter fix")
		}
	case 2:
		if len(files) != 1 || files[0] != "counter/counter_test.go" {
			return fmt.Errorf("Turn 2 must touch only the focused boundary test, got %v", files)
		}
		if strings.Contains(patch, "diff --git a/counter/counter.go ") {
			return fmt.Errorf("Turn 2 repeated the Turn 1 production fix")
		}
	default:
		return fmt.Errorf("unsupported Turn %d", turn)
	}
	return nil
}

func verifyTokenAbsence(sources map[string][]byte, tokens []knownToken) error {
	for _, token := range tokens {
		if token.Label == "" || token.Value == "" {
			return fmt.Errorf("known token list contains an invalid entry")
		}
		for source, content := range sources {
			if bytes.Contains(content, []byte(token.Value)) {
				return fmt.Errorf("known token %s was found in %s", token.Label, source)
			}
		}
	}
	return nil
}

func validTreeID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func decodeStrictJSONFile(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode %s: trailing JSON", path)
	}
	return nil
}

func writeStrictJSON(path string, value any, mode os.FileMode) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
		t.Fatalf("%s is required and must be one bounded value", name)
	}
	return value
}

func requiredAbsoluteEnv(t *testing.T, name string) string {
	t.Helper()
	value := requiredEnv(t, name)
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		t.Fatalf("%s must be a clean absolute path", name)
	}
	return value
}

func requiredURL(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSuffix(requiredEnv(t, name), "/")
	if !strings.HasPrefix(value, "http://127.0.0.1:") || strings.ContainsAny(value, "?#") {
		t.Fatalf("%s must be one loopback HTTP origin", name)
	}
	return value
}

func postJSON[T any](t *testing.T, client *http.Client, endpoint string, body any) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("POST returned HTTP %d", response.StatusCode)
	}
	var decoded T
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	return decoded
}

func createTurnOneRun(t *testing.T, client *http.Client, endpoint string) createdRun {
	t.Helper()
	return postJSON[createdRun](t, client, endpoint, turnOneCreateRunRequest())
}

func turnOneCreateRunRequest() createRunRequest {
	return createRunRequest{
		Goal:               turnOnePrompt,
		AcceptanceCriteria: []string{turnOneAcceptanceCriterion},
	}
}

func turnOneACPPrompt() string {
	request := turnOneCreateRunRequest()
	return fmt.Sprintf(
		"Goal: %s\nAcceptance criteria:\n- %s",
		request.Goal,
		request.AcceptanceCriteria[0],
	)
}

func waitForRun(
	t *testing.T,
	client *http.Client,
	backendURL, runID string,
	ready func(runSnapshot) bool,
) runSnapshot {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	var latest runSnapshot
	for time.Now().Before(deadline) {
		response, err := client.Get(backendURL + "/spike/runs/" + runID)
		if err == nil {
			content, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK &&
				json.Unmarshal(content, &latest) == nil {
				if ready(latest) {
					return latest
				}
				if latest.State == "failed" || latest.State == "diagnostic_hold" ||
					latest.ExecutionOutcome == "failed" || latest.SpikeAcceptance == "failed" {
					t.Fatalf(
						"run reached failure before expected state: state=%s outcome=%s acceptance=%s diagnostics=%+v",
						latest.State,
						latest.ExecutionOutcome,
						latest.SpikeAcceptance,
						latest.Diagnostics,
					)
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf(
		"run timed out: state=%s outcome=%s acceptance=%s diagnostics=%+v",
		latest.State,
		latest.ExecutionOutcome,
		latest.SpikeAcceptance,
		latest.Diagnostics,
	)
	return runSnapshot{}
}

func writeE2EState(t *testing.T, path string, identity runtimeResourceIdentity) {
	t.Helper()
	if err := writeStrictJSON(path, identity, 0o600); err != nil {
		t.Fatalf("write E2E cleanup state: %v", err)
	}
}

func runtimeResourceIdentityFromSnapshot(
	snapshot runSnapshot,
	containerID string,
) runtimeResourceIdentity {
	return runtimeResourceIdentity{
		WorkerSessionID:   snapshot.WorkerSessionID,
		WorkerRunID:       snapshot.WorkerRunID,
		RuntimeInstanceID: snapshot.RuntimeInstanceID,
		RuntimeGeneration: snapshot.RuntimeGeneration,
		ContainerID:       containerID,
	}
}

func identityFromSnapshot(
	snapshot runSnapshot,
	turnID, containerID, workingCopy string,
) runtimeIdentity {
	return runtimeIdentity{
		WorkerSessionID:   snapshot.WorkerSessionID,
		WorkerRunID:       snapshot.WorkerRunID,
		RuntimeInstanceID: snapshot.RuntimeInstanceID,
		RuntimeGeneration: snapshot.RuntimeGeneration,
		AgentSessionID:    snapshot.AgentSessionID,
		ContainerID:       containerID,
		WorkingCopy:       workingCopy,
		AgentTurnID:       turnID,
		TraceID:           snapshot.TraceID,
	}
}

func findExactWorkerContainer(t *testing.T, sessionID string) string {
	t.Helper()
	output := dockerCommand(
		t,
		"ps",
		"-aq",
		"--no-trunc",
		"--filter",
		"label=com.switchback.managed=worker-runtime",
		"--filter",
		"label=com.switchback.worker-session-id="+sessionID,
	)
	identities := strings.Fields(output)
	if len(identities) != 1 {
		t.Fatalf("managed Worker cardinality = %d, want 1", len(identities))
	}
	return identities[0]
}

func verifyWorkerIsolation(
	containerID, dataRoot string,
	snapshot runSnapshot,
	tokens []knownToken,
) error {
	raw, err := dockerCombinedOutput("inspect", containerID)
	if err != nil {
		return fmt.Errorf("inspect dynamic Worker failed")
	}
	var inspected []inspectedContainer
	if err := json.Unmarshal(raw, &inspected); err != nil || len(inspected) != 1 {
		return fmt.Errorf("decode dynamic Worker inspect")
	}
	container := inspected[0]
	for name, values := range map[string][2]string{
		"container_id": {containerID, container.ID},
		"managed": {
			"worker-runtime",
			container.Config.Labels["com.switchback.managed"],
		},
		"worker_session_id": {
			snapshot.WorkerSessionID,
			container.Config.Labels["com.switchback.worker-session-id"],
		},
		"worker_run_id": {
			snapshot.WorkerRunID,
			container.Config.Labels["com.switchback.worker-run-id"],
		},
		"runtime_instance_id": {
			snapshot.RuntimeInstanceID,
			container.Config.Labels["com.switchback.runtime-instance-id"],
		},
		"runtime_generation": {
			strconv.FormatUint(uint64(snapshot.RuntimeGeneration), 10),
			container.Config.Labels["com.switchback.runtime-generation"],
		},
	} {
		if values[0] != values[1] {
			return fmt.Errorf(
				"dynamic Worker %s = %q, want %q",
				name,
				values[1],
				values[0],
			)
		}
	}
	host := container.HostConfig
	if host.Privileged || host.PublishAllPorts || len(host.PortBindings) != 0 ||
		host.NetworkMode != "switchback-spike-control" {
		return fmt.Errorf("dynamic Worker has privileged, host-network, or published-port access")
	}
	capDrop := append([]string(nil), host.CapDrop...)
	capAdd := append([]string(nil), host.CapAdd...)
	for index := range capAdd {
		capAdd[index] = strings.TrimPrefix(capAdd[index], "CAP_")
	}
	sort.Strings(capDrop)
	sort.Strings(capAdd)
	if strings.Join(capDrop, ",") != "ALL" ||
		strings.Join(capAdd, ",") != "KILL,SETGID,SETUID" ||
		host.NanoCPUs != 2_000_000_000 ||
		host.Memory != 4<<30 ||
		host.PidsLimit != 512 {
		return fmt.Errorf(
			"dynamic Worker profile cap_drop=%q cap_add=%q nano_cpus=%d memory=%d pids=%d",
			strings.Join(capDrop, ","),
			strings.Join(capAdd, ","),
			host.NanoCPUs,
			host.Memory,
			host.PidsLimit,
		)
	}
	destinations := map[string]struct {
		mountType string
		readWrite bool
	}{
		"/workspace":         {mountType: "bind", readWrite: true},
		"/agent-state":       {mountType: "bind", readWrite: true},
		"/trace-spool":       {mountType: "bind", readWrite: true},
		"/run/switchback-secrets": {mountType: "volume", readWrite: false},
	}
	if len(container.Mounts) != len(destinations) {
		return fmt.Errorf("dynamic Worker mount cardinality = %d, want %d", len(container.Mounts), len(destinations))
	}
	cleanRoot := filepath.Clean(dataRoot) + string(filepath.Separator)
	secretVolumeName := fmt.Sprintf(
		"switchback-runtime-secrets-%s-g%d",
		snapshot.RuntimeInstanceID,
		snapshot.RuntimeGeneration,
	)
	for _, mounted := range container.Mounts {
		want, known := destinations[mounted.Destination]
		sourceTrusted := false
		switch want.mountType {
		case "bind":
			sourceTrusted = strings.HasPrefix(filepath.Clean(mounted.Source), cleanRoot)
		case "volume":
			sourceTrusted = mounted.Name == secretVolumeName
		}
		if !known || mounted.Type != want.mountType || mounted.RW != want.readWrite || !sourceTrusted ||
			strings.Contains(mounted.Source, "docker.sock") ||
			mounted.Destination == "/var/run/docker.sock" {
			return fmt.Errorf("dynamic Worker has an untrusted or incorrectly writable mount")
		}
	}
	if err := verifyRuntimeSecretVolumeAndStagingCleanup(secretVolumeName, dataRoot, snapshot); err != nil {
		return err
	}
	for _, variable := range container.Config.Env {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "LANGFUSE_") ||
			strings.HasPrefix(name, "SWITCHBACK_SPIKE_UPSTREAM_") ||
			strings.Contains(name, "DOCKER") {
			return fmt.Errorf("dynamic Worker received forbidden top-level environment %s", name)
		}
	}
	if err := verifyTokenAbsence(map[string][]byte{"dynamic Worker inspect": raw}, tokens); err != nil {
		return err
	}
	return nil
}

func verifyRuntimeSecretVolumeAndStagingCleanup(
	volumeName, dataRoot string,
	snapshot runSnapshot,
) error {
	raw, err := dockerCombinedOutput("volume", "inspect", volumeName)
	if err != nil {
		return fmt.Errorf("inspect runtime secret volume failed")
	}
	var volumes []struct {
		Name   string            `json:"Name"`
		Driver string            `json:"Driver"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(raw, &volumes); err != nil || len(volumes) != 1 {
		return fmt.Errorf("decode runtime secret volume inspect")
	}
	wantLabels := map[string]string{
		"com.switchback.managed":             "runtime-secret-volume",
		"com.switchback.worker-session-id":   snapshot.WorkerSessionID,
		"com.switchback.worker-run-id":       snapshot.WorkerRunID,
		"com.switchback.runtime-instance-id": snapshot.RuntimeInstanceID,
		"com.switchback.runtime-generation":  strconv.FormatUint(uint64(snapshot.RuntimeGeneration), 10),
	}
	if volumes[0].Name != volumeName ||
		volumes[0].Driver != "local" ||
		!reflect.DeepEqual(volumes[0].Labels, wantLabels) {
		return fmt.Errorf("runtime secret volume identity does not match exact Runtime")
	}
	populators, err := dockerCombinedOutput(
		"ps",
		"-aq",
		"--filter",
		"label=com.switchback.managed=runtime-secret-populator",
		"--filter",
		"label=com.switchback.worker-session-id="+snapshot.WorkerSessionID,
	)
	if err != nil || len(bytes.TrimSpace(populators)) != 0 {
		return fmt.Errorf("runtime secret populator remains after staging")
	}
	sessionRoot := filepath.Join(dataRoot, "sessions", snapshot.WorkerSessionID)
	for _, source := range []string{
		filepath.Join(sessionRoot, "bootstrap.json"),
		filepath.Join(sessionRoot, "model-secret"),
	} {
		if _, err := os.Lstat(source); !os.IsNotExist(err) {
			return fmt.Errorf("transient runtime secret source remains after staging")
		}
	}
	globalSource := filepath.Join(dataRoot, "env", "upstream-model.key")
	if info, err := os.Lstat(globalSource); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("global Backend model source was removed or replaced")
	}
	return nil
}

func verifyLiveProcessCapabilities(containerID string, tokens []knownToken) error {
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
    result.append({"pid": int(status_path.parent.name), "uid": int(fields["Uid"].split()[0]), "name": fields["Name"], "cmd": cmd, "cap_prm": fields["CapPrm"], "cap_eff": fields["CapEff"]})
print(json.dumps(result))
`
	output, err := dockerCombinedOutput("exec", containerID, "python", "-c", script)
	if err != nil {
		return fmt.Errorf("inspect live Worker process capabilities failed")
	}
	var statuses []processStatus
	if err := json.Unmarshal(output, &statuses); err != nil {
		return fmt.Errorf("decode live Worker process capabilities")
	}
	rootShims := 0
	childSeen := map[int]bool{10001: false, 10002: false}
	for _, status := range statuses {
		switch {
		case status.UID == 0 && status.Name == "worker-shim":
			rootShims++
			if err := requireCapability(status.CapPrm, 0xe0); err != nil {
				return fmt.Errorf("root Shim permitted capabilities are not exact")
			}
			if err := requireCapability(status.CapEff, 0xe0); err != nil {
				return fmt.Errorf("root Shim effective capabilities are not exact")
			}
		case status.UID == 10001 || status.UID == 10002:
			childSeen[status.UID] = true
			if err := requireCapability(status.CapPrm, 0); err != nil {
				return fmt.Errorf("Worker child UID %d retained permitted capabilities", status.UID)
			}
			if err := requireCapability(status.CapEff, 0); err != nil {
				return fmt.Errorf("Worker child UID %d retained effective capabilities", status.UID)
			}
		}
	}
	if rootShims != 1 || !childSeen[10001] || !childSeen[10002] {
		return fmt.Errorf("live Shim, LiteLLM, and Codex ACP process identities were not all present")
	}
	if err := verifyLiveSecretBoundary(containerID, tokens); err != nil {
		return err
	}
	return nil
}

func verifyLiveSecretBoundary(containerID string, tokens []knownToken) error {
	report, err := inspectLiveSecretBoundary(containerID, tokens)
	if err != nil {
		return err
	}
	if err := validateSecretBoundaryReport(report); err != nil {
		return err
	}
	for _, uid := range []int{10001, 10002} {
		for _, secretPath := range []string{
			"/run/switchback-secrets/bootstrap.json",
			"/run/switchback-secrets/model",
		} {
			if err := verifySecretFileUnreadableByUID(containerID, uid, secretPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectLiveSecretBoundary(
	containerID string,
	tokens []knownToken,
) (secretBoundaryReport, error) {
	hashes := make([]string, 0, len(tokens))
	seenHashes := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		digest := sha256.Sum256([]byte(token.Value))
		encoded := hex.EncodeToString(digest[:])
		if _, seen := seenHashes[encoded]; seen {
			continue
		}
		seenHashes[encoded] = struct{}{}
		hashes = append(hashes, encoded)
	}
	upstreamKey := os.Getenv("SWITCHBACK_SPIKE_UPSTREAM_API_KEY")
	if upstreamKey == "" {
		return secretBoundaryReport{}, fmt.Errorf("upstream credential label is unavailable")
	}
	upstreamDigest := sha256.Sum256([]byte(upstreamKey))
	hashInput, err := json.Marshal(map[string]any{
		"known_hashes":          hashes,
		"trusted_upstream_hash": hex.EncodeToString(upstreamDigest[:]),
	})
	if err != nil {
		return secretBoundaryReport{}, fmt.Errorf("encode known-token hash labels")
	}
	report, err := inspectSecretFileMetadata(containerID)
	if err != nil {
		return secretBoundaryReport{}, err
	}
	for _, child := range []struct {
		uid  int
		role string
	}{
		{uid: 10001, role: "litellm"},
		{uid: 10002, role: "agent"},
	} {
		childReport, childErr := inspectChildEnvironmentByUID(
			containerID,
			child.uid,
			child.role,
			hashInput,
		)
		if childErr != nil {
			return secretBoundaryReport{}, childErr
		}
		report.ProcessScanErrors += childReport.ProcessScanErrors
		report.Processes = append(report.Processes, childReport.Processes...)
	}
	return report, nil
}

func inspectSecretFileMetadata(containerID string) (secretBoundaryReport, error) {
	const script = `
import json
import os
import stat

bootstrap = os.stat("/run/switchback-secrets/bootstrap.json", follow_symlinks=False)
secret = os.stat("/run/switchback-secrets/model", follow_symlinks=False)
print(json.dumps({
    "bootstrap_uid": bootstrap.st_uid,
    "bootstrap_gid": bootstrap.st_gid,
    "bootstrap_mode": stat.S_IMODE(bootstrap.st_mode),
    "secret_uid": secret.st_uid,
    "secret_gid": secret.st_gid,
    "secret_mode": stat.S_IMODE(secret.st_mode),
}))
`
	output, err := dockerCombinedOutput("exec", containerID, "python", "-c", script)
	if err != nil {
		return secretBoundaryReport{}, fmt.Errorf("inspect live runtime secret metadata failed")
	}
	var report secretBoundaryReport
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return secretBoundaryReport{}, fmt.Errorf("decode live runtime secret metadata")
	}
	return report, nil
}

func inspectChildEnvironmentByUID(
	containerID string,
	uid int,
	role string,
	hashInput []byte,
) (secretBoundaryReport, error) {
	const script = `
import hashlib
import json
import os
import sys
from pathlib import Path

hash_config = json.load(sys.stdin)
known_hashes = set(hash_config["known_hashes"])
trusted_upstream_hash = hash_config["trusted_upstream_hash"]
expected_uid = int(sys.argv[1])
expected_role = sys.argv[2]
self_pid = os.getpid()
processes = []
process_scan_errors = 0
for status_path in Path("/proc").glob("[0-9]*/status"):
    pid = int(status_path.parent.name)
    if pid == self_pid:
        continue
    fields = {}
    try:
        for line in status_path.read_text().splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                fields[key] = value.strip()
        uid = int(fields["Uid"].split()[0])
    except (OSError, KeyError, ValueError):
        continue
    if uid != expected_uid:
        continue
    try:
        cmd = (status_path.parent / "cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace").strip().lower()
    except OSError:
        continue
    if expected_role == "litellm":
        is_expected_child = "/usr/local/bin/litellm" in cmd or cmd.startswith("litellm ")
    else:
        is_expected_child = "codex-acp" in cmd or "fake-acp" in cmd or "/codex" in cmd or " codex " in cmd
    if not is_expected_child:
        continue
    try:
        environ = (status_path.parent / "environ").read_bytes().split(b"\0")
    except OSError:
        process_scan_errors += 1
        continue
    forbidden = set()
    known_value = False
    trusted_upstream_key = False
    for entry in environ:
        if b"=" not in entry:
            continue
        name_bytes, value = entry.split(b"=", 1)
        name = name_bytes.decode(errors="replace")
        if name.startswith("LANGFUSE_"):
            forbidden.add("langfuse-name")
        if uid == 10002 and name.startswith("SWITCHBACK_SPIKE_UPSTREAM_"):
            forbidden.add("upstream-name")
        if uid == 10001 and name.startswith("SWITCHBACK_SPIKE_UPSTREAM_") and name not in {
            "SWITCHBACK_SPIKE_UPSTREAM_MODEL",
            "SWITCHBACK_SPIKE_UPSTREAM_API_BASE",
            "SWITCHBACK_SPIKE_UPSTREAM_API_KEY",
        }:
            forbidden.add("upstream-name")
        value_hash = hashlib.sha256(value).hexdigest()
        if uid == 10001 and name == "SWITCHBACK_SPIKE_UPSTREAM_API_KEY":
            if value_hash == trusted_upstream_hash:
                trusted_upstream_key = True
            else:
                forbidden.add("upstream-key-mismatch")
        elif value_hash in known_hashes:
            known_value = True
    processes.append({
        "uid": uid,
        "role": expected_role,
        "forbidden_name_labels": sorted(forbidden),
        "known_value_label": known_value,
        "trusted_upstream_key": trusted_upstream_key,
    })
print(json.dumps({
    "process_scan_errors": process_scan_errors,
    "processes": processes,
}))
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	user := strconv.Itoa(uid) + ":" + strconv.Itoa(uid)
	command := exec.CommandContext(
		ctx,
		"docker",
		"exec",
		"-i",
		"--user",
		user,
		containerID,
		"python",
		"-c",
		script,
		strconv.Itoa(uid),
		role,
	)
	command.Stdin = bytes.NewReader(hashInput)
	output, err := command.Output()
	if err != nil {
		return secretBoundaryReport{}, fmt.Errorf("inspect live %s child environment labels failed", role)
	}
	var report secretBoundaryReport
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return secretBoundaryReport{}, fmt.Errorf("decode live %s child environment labels", role)
	}
	return report, nil
}

func validateSecretBoundaryReport(report secretBoundaryReport) error {
	if report.BootstrapUID != 0 ||
		report.BootstrapGID != 0 ||
		report.BootstrapMode != 0o400 {
		return fmt.Errorf("live bootstrap secret owner or mode is not root:root 0400")
	}
	if report.SecretUID != 0 || report.SecretGID != 0 || report.SecretMode != 0o400 {
		return fmt.Errorf("live model secret owner or mode is not root:root 0400")
	}
	if report.ProcessScanErrors != 0 {
		return fmt.Errorf("one or more Worker child environments could not be inspected")
	}
	childSeen := map[int]bool{10001: false, 10002: false}
	trustedUpstreamSeen := false
	for _, process := range report.Processes {
		if _, known := childSeen[process.UID]; !known {
			return fmt.Errorf("secret boundary report contains an unexpected child UID label")
		}
		expectedRole := "agent"
		if process.UID == 10001 {
			expectedRole = "litellm"
		}
		if process.Role != expectedRole {
			return fmt.Errorf("secret boundary report contains an unexpected child role label")
		}
		childSeen[process.UID] = true
		if len(process.ForbiddenNameLabels) != 0 || process.KnownValueLabel {
			return fmt.Errorf("Worker child UID %d environment contains a forbidden secret label", process.UID)
		}
		if process.UID == 10001 && process.TrustedUpstreamKey {
			trustedUpstreamSeen = true
		}
		if process.UID == 10002 && process.TrustedUpstreamKey {
			return fmt.Errorf("Codex/Tool child environment contains the upstream credential label")
		}
	}
	if !childSeen[10001] || !childSeen[10002] || !trustedUpstreamSeen {
		return fmt.Errorf("live LiteLLM and Codex/Tool environment labels are incomplete")
	}
	return nil
}

func verifySecretFileUnreadableByUID(containerID string, uid int, secretPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user := strconv.Itoa(uid) + ":" + strconv.Itoa(uid)
	command := exec.CommandContext(
		ctx,
		"docker",
		"exec",
		"--user",
		user,
		containerID,
		"sh",
		"-c",
		"cat "+secretPath+" >/dev/null",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		return fmt.Errorf("Worker child UID %d could read a root-only runtime secret", uid)
	}
	if !bytes.Contains(bytes.ToLower(output), []byte("permission denied")) {
		return fmt.Errorf("Worker child UID %d runtime-secret read did not prove permission denial", uid)
	}
	return nil
}

func requireCapability(encoded string, want uint64) error {
	got, err := strconv.ParseUint(encoded, 16, 64)
	if err != nil || got != want {
		return fmt.Errorf("capability mask mismatch")
	}
	return nil
}

func runWorkingCopyTests(t *testing.T, workingCopy string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "./...")
	command.Dir = workingCopy
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("WorkingCopy go test ./... failed: %v\n%s", err, output)
	}
}

func verifyTurnEvidence(
	runRoot string,
	identity runtimeIdentity,
	expectedPrompt string,
	firstTurn bool,
) error {
	if _, err := verifyManifest(runRoot, identity); err != nil {
		return err
	}
	turnRoot := filepath.Join(runRoot, "turns", identity.AgentTurnID)
	report, err := os.ReadFile(filepath.Join(turnRoot, "report.md"))
	if err != nil {
		return fmt.Errorf("read Turn report: %w", err)
	}
	if !bytes.Contains(report, []byte("go test ./...")) {
		return fmt.Errorf("Turn report does not contain observed go test ./... verification")
	}
	patch, err := os.ReadFile(filepath.Join(turnRoot, "changes.patch"))
	if err != nil {
		return fmt.Errorf("read Turn patch: %w", err)
	}
	turn := 2
	if firstTurn {
		turn = 1
	}
	if err := verifyTurnPatch(turn, string(patch)); err != nil {
		return err
	}
	rawPath := filepath.Join(turnRoot, "raw-acp.jsonl")
	tracePath := filepath.Join(turnRoot, "trace.jsonl")
	rawRecords, err := decodeJSONL[rawACPRecord](rawPath)
	if err != nil {
		return err
	}
	normalized, err := decodeJSONL[normalizedTraceRecord](tracePath)
	if err != nil {
		return err
	}
	if len(rawRecords) == 0 || len(normalized) != len(rawRecords) {
		return fmt.Errorf("raw and normalized JSONL cardinality does not match")
	}
	prompts := 0
	for index, record := range rawRecords {
		if record.Type == "" {
			return fmt.Errorf("raw ACP record %d has no type", index+1)
		}
		if record.Type == "acp.raw_frame" && record.Direction == "client_to_agent" {
			var frame rpcFrame
			if err := json.Unmarshal([]byte(record.Frame), &frame); err != nil {
				return fmt.Errorf("decode raw ACP frame %d", index+1)
			}
			if frame.Method == "session/prompt" {
				prompts++
				if err := verifyPromptFrame(frame, identity, expectedPrompt, firstTurn); err != nil {
					return err
				}
			}
		}
		normalizedRecord := normalized[index]
		if err := verifyNormalizedTraceRecord(index, normalizedRecord, record.Type, identity); err != nil {
			return err
		}
	}
	if prompts != 1 {
		return fmt.Errorf("Turn raw ACP prompt cardinality = %d, want 1", prompts)
	}
	return nil
}

func verifyNormalizedTraceRecord(
	index int,
	record normalizedTraceRecord,
	rawKind string,
	identity runtimeIdentity,
) error {
	serverTimestamp, serverTimeErr := time.Parse(time.RFC3339Nano, record.ServerTimestamp)
	workerTimestamp, workerTimeErr := time.Parse(time.RFC3339Nano, record.WorkerTimestamp)
	if record.SchemaVersion != sharedevidence.NormalizedTraceSchemaVersion ||
		record.Sequence != uint64(index+1) ||
		serverTimeErr != nil ||
		workerTimeErr != nil ||
		serverTimestamp.IsZero() ||
		workerTimestamp.IsZero() ||
		record.Kind != rawKind ||
		record.Identity.WorkerSessionID != identity.WorkerSessionID ||
		record.Identity.WorkerRunID != identity.WorkerRunID ||
		record.Identity.RuntimeInstanceID != identity.RuntimeInstanceID ||
		record.Identity.RuntimeGeneration != identity.RuntimeGeneration ||
		record.Identity.AgentSessionID != identity.AgentSessionID ||
		record.Identity.AgentTurnID != identity.AgentTurnID ||
		record.Identity.TraceID != identity.TraceID ||
		identity.TraceID == "" ||
		len(record.Payload) == 0 {
		return fmt.Errorf("normalized JSONL record %d has incomplete typed provenance", index+1)
	}
	return nil
}

func verifyPromptFrame(
	frame rpcFrame,
	identity runtimeIdentity,
	expectedPrompt string,
	firstTurn bool,
) error {
	var params struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
		Meta struct {
			Switchback map[string]string `json:"switchback"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(frame.Params, &params); err != nil {
		return fmt.Errorf("decode ACP session/prompt params")
	}
	actualPrompt := expectedPrompt
	if firstTurn {
		if expectedPrompt != turnOnePrompt {
			return fmt.Errorf("Turn 1 evidence does not contain the exact Task 14 goal")
		}
		actualPrompt = turnOneACPPrompt()
	}
	if params.SessionID != identity.AgentSessionID ||
		len(params.Prompt) != 1 ||
		params.Prompt[0].Type != "text" ||
		params.Prompt[0].Text != actualPrompt ||
		params.Meta.Switchback["worker_session_id"] != identity.WorkerSessionID ||
		params.Meta.Switchback["worker_run_id"] != identity.WorkerRunID ||
		params.Meta.Switchback["agent_turn_id"] != identity.AgentTurnID {
		return fmt.Errorf("ACP session/prompt does not match the exact trusted Turn contract")
	}
	return nil
}

func verifyFullACPTrace(path string, agentSessionID string, turnIDs []string) error {
	records, err := decodeJSONL[rawACPRecord](path)
	if err != nil {
		return err
	}
	counts := make(map[string]int)
	var promptTurns []string
	for _, record := range records {
		if record.Type != "acp.raw_frame" || record.Direction != "client_to_agent" {
			continue
		}
		var frame rpcFrame
		if json.Unmarshal([]byte(record.Frame), &frame) != nil {
			return fmt.Errorf("full ACP trace contains an invalid frame")
		}
		counts[frame.Method]++
		if frame.Method == "authenticate" {
			var params struct {
				Meta struct {
					Gateway struct {
						Headers map[string]string `json:"headers"`
					} `json:"gateway"`
				} `json:"_meta"`
			}
			if json.Unmarshal(frame.Params, &params) != nil ||
				params.Meta.Gateway.Headers["Authorization"] !=
					"Bearer [REDACTED:litellm_proxy_token]" {
				return fmt.Errorf("full ACP trace contains unredacted gateway authorization")
			}
		}
		if frame.Method == "session/prompt" {
			var params struct {
				SessionID string `json:"sessionId"`
				Meta      struct {
					Switchback map[string]string `json:"switchback"`
				} `json:"_meta"`
			}
			if json.Unmarshal(frame.Params, &params) != nil || params.SessionID != agentSessionID {
				return fmt.Errorf("full ACP trace changed Agent session identity")
			}
			promptTurns = append(promptTurns, params.Meta.Switchback["agent_turn_id"])
		}
	}
	for _, method := range []string{"initialize", "authenticate", "session/new"} {
		if counts[method] != 1 {
			return fmt.Errorf("full ACP trace %s cardinality = %d, want 1", method, counts[method])
		}
	}
	if counts["session/prompt"] != 2 ||
		len(promptTurns) != 2 ||
		promptTurns[0] != turnIDs[0] ||
		promptTurns[1] != turnIDs[1] {
		return fmt.Errorf("full ACP trace does not prove the ordered two-prompt flow")
	}
	return nil
}

func TestVerifyFullACPTraceRejectsUnredactedGatewayAuthorization(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "acp.jsonl")
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"_meta":{"gateway":{"headers":{"Authorization":"Bearer proxy-secret"}}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"agent-session-1","_meta":{"switchback":{"agent_turn_id":"turn-1"}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{"sessionId":"agent-session-1","_meta":{"switchback":{"agent_turn_id":"turn-2"}}}}`,
	}
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	for _, frame := range frames {
		if err := encoder.Encode(rawACPRecord{
			Type:      "acp.raw_frame",
			Direction: "client_to_agent",
			Frame:     frame,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyFullACPTrace(path, "agent-session-1", []string{"turn-1", "turn-2"})
	if err == nil || !strings.Contains(err.Error(), "unredacted gateway authorization") {
		t.Fatalf("verifyFullACPTrace() error = %v, want unredacted authorization rejection", err)
	}
}

func decodeJSONL[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open JSONL evidence: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var records []T
	for scanner.Scan() {
		var record T
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("decode JSONL record %d: %w", len(records)+1, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan JSONL evidence: %w", err)
	}
	return records, nil
}

func verifyZeroDeltaMutationProof(workingCopy string) error {
	if err := runMutationGoTest(workingCopy, "./..."); err != nil {
		return fmt.Errorf("zero-delta mutation baseline is not green")
	}
	productionPath := filepath.Join(workingCopy, "counter", "counter.go")
	original, err := os.ReadFile(productionPath)
	if err != nil {
		return fmt.Errorf("read zero-delta production source: %w", err)
	}
	info, err := os.Stat(productionPath)
	if err != nil {
		return fmt.Errorf("stat zero-delta production source: %w", err)
	}
	mutated, err := buildZeroDeltaMutation(productionPath, original)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(productionPath, mutated, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write zero-delta mutation: %w", err)
	}
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		if err := writeFileAtomic(productionPath, original, info.Mode().Perm()); err != nil {
			return err
		}
		restored = true
		return nil
	}
	defer func() { _ = restore() }()

	mutationErr := runMutationGoTest(workingCopy, "./counter")
	if err := restore(); err != nil {
		return fmt.Errorf("restore zero-delta production source: %w", err)
	}
	if err := runMutationGoTest(workingCopy, "./..."); err != nil {
		return fmt.Errorf("full WorkingCopy tests failed after zero-delta mutation restore")
	}
	if mutationErr == nil {
		return fmt.Errorf("focused test survived a controlled Add(x, 0) regression")
	}
	return nil
}

func buildZeroDeltaMutation(path string, source []byte) ([]byte, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse zero-delta production source: %w", err)
	}
	const renamed = "task14OriginalAdd"
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != "Add" {
			continue
		}
		parameters, ok := twoIntegerParameterNames(function.Type.Params)
		if !ok || !singleIntegerResult(function.Type.Results) {
			return nil, fmt.Errorf("Add signature is incompatible with the controlled mutation")
		}
		start := fileSet.Position(function.Name.Pos()).Offset
		end := fileSet.Position(function.Name.End()).Offset
		if start < 0 || end > len(source) || start >= end {
			return nil, fmt.Errorf("Add source position is invalid")
		}
		mutated := make([]byte, 0, len(source)+256)
		mutated = append(mutated, source[:start]...)
		mutated = append(mutated, renamed...)
		mutated = append(mutated, source[end:]...)
		mutated = append(mutated, []byte(fmt.Sprintf(`

func Add(%s, %s int) int {
	if %s == 0 {
		return %s + 1
	}
	return %s(%s, %s)
}
`, parameters[0], parameters[1], parameters[1], parameters[0], renamed, parameters[0], parameters[1]))...)
		return mutated, nil
	}
	return nil, fmt.Errorf("zero-delta production Add function was not found")
}

func twoIntegerParameterNames(fields *ast.FieldList) ([2]string, bool) {
	var names []string
	if fields == nil {
		return [2]string{}, false
	}
	for _, field := range fields.List {
		identifier, ok := field.Type.(*ast.Ident)
		if !ok || identifier.Name != "int" {
			return [2]string{}, false
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	if len(names) != 2 || names[0] == "" || names[1] == "" {
		return [2]string{}, false
	}
	return [2]string{names[0], names[1]}, true
}

func singleIntegerResult(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) != 1 || len(fields.List[0].Names) != 0 {
		return false
	}
	identifier, ok := fields.List[0].Type.(*ast.Ident)
	return ok && identifier.Name == "int"
}

func runMutationGoTest(workingCopy, packagePattern string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", packagePattern)
	command.Dir = workingCopy
	_, err := command.CombinedOutput()
	return err
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	temporary := path + ".task14-mutation.tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func TestWaitForLangfuseTraceReturnsCompleteResponseForTokenScan(t *testing.T) {
	t.Parallel()

	query := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query <- request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"trace-1",
			"metadata":{"secret_marker":"top-level-token"},
			"observations":[{"id":"turn-1","traceId":"trace-1","name":"agent.turn","type":"SPAN"}]
		}`))
	}))
	defer server.Close()

	trace, raw := waitForLangfuseTrace(
		t,
		server.Client(),
		server.URL,
		"public-key",
		"secret-key",
		"trace-1",
	)

	if got := <-query; got != "" {
		t.Fatalf("Langfuse Trace query = %q, want complete unfiltered response", got)
	}
	if trace.ID != "trace-1" || len(trace.Observations) != 1 {
		t.Fatalf("Langfuse Trace = %+v, want complete trace", trace)
	}
	if !bytes.Contains(raw, []byte("top-level-token")) {
		t.Fatal("raw Langfuse response omitted top-level metadata from token scan")
	}
}

func waitForLangfuseTrace(
	t *testing.T,
	client *http.Client,
	baseURL, publicKey, secretKey, traceID string,
) (langfuseTrace, []byte) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var latest []byte
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(
			http.MethodGet,
			baseURL+"/api/public/traces/"+traceID,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth(publicKey, secretKey)
		response, err := client.Do(request)
		if err == nil {
			content, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
			_ = response.Body.Close()
			if readErr == nil {
				latest = content
				if response.StatusCode == http.StatusOK {
					var trace langfuseTrace
					if json.Unmarshal(content, &trace) == nil && len(trace.Observations) > 0 {
						return trace, content
					}
				} else if response.StatusCode != http.StatusNotFound {
					t.Fatalf("Langfuse Trace query returned HTTP %d", response.StatusCode)
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Langfuse Trace query timed out; last response bytes=%d", len(latest))
	return langfuseTrace{}, nil
}

func runLangfuseVerifier(
	t *testing.T,
	root, baseURL, publicKey, secretKey string,
	snapshot runSnapshot,
	turnIDs []string,
	generations []expectedGeneration,
) {
	t.Helper()
	arguments := []string{snapshot.TraceID, turnIDs[0], turnIDs[1]}
	for _, generation := range generations {
		arguments = append(arguments, generation.ID, generation.ParentTurnID)
	}
	command := exec.Command(filepath.Join(root, "scripts", "verify-langfuse"), arguments...)
	command.Dir = root
	command.Env = append(
		environmentWithout(
			"LANGFUSE_BASE_URL",
			"LANGFUSE_PUBLIC_KEY",
			"LANGFUSE_SECRET_KEY",
			"SWITCHBACK_SPIKE_LANGFUSE_WORKER_SESSION_ID",
			"SWITCHBACK_SPIKE_LANGFUSE_WORKER_RUN_ID",
			"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_INSTANCE_ID",
			"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_GENERATION",
			"SWITCHBACK_SPIKE_LANGFUSE_AGENT_SESSION_ID",
		),
		"LANGFUSE_BASE_URL="+baseURL,
		"LANGFUSE_PUBLIC_KEY="+publicKey,
		"LANGFUSE_SECRET_KEY="+secretKey,
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_SESSION_ID="+snapshot.WorkerSessionID,
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_RUN_ID="+snapshot.WorkerRunID,
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_INSTANCE_ID="+snapshot.RuntimeInstanceID,
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_GENERATION="+strconv.FormatUint(uint64(snapshot.RuntimeGeneration), 10),
		"SWITCHBACK_SPIKE_LANGFUSE_AGENT_SESSION_ID="+snapshot.AgentSessionID,
	)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "Langfuse projection verified" {
		t.Fatalf("exact Langfuse verifier failed")
	}
}

func collectPersistedSources(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	sources := make(map[string][]byte)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sources[path] = content
			return nil
		})
		if err != nil {
			t.Fatalf("collect persisted token-scan sources: %v", err)
		}
	}
	return sources
}

func readKnownTokens(t *testing.T, path string) []knownToken {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open known token list: %v", err)
	}
	defer file.Close()
	var tokens []knownToken
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		label, value, ok := strings.Cut(scanner.Text(), "\t")
		if !ok || label == "" || value == "" {
			t.Fatal("known token list contains an invalid record")
		}
		tokens = append(tokens, knownToken{Label: label, Value: value})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 {
		t.Fatal("known token list is empty")
	}
	return tokens
}

func cleanupRuntime(
	client *http.Client,
	backendURL string,
	identity runtimeResourceIdentity,
	timeout time.Duration,
) error {
	if identity.WorkerSessionID == "" {
		return fmt.Errorf("runtime cleanup requires a Worker Session identity")
	}
	request, err := http.NewRequest(
		http.MethodPost,
		backendURL+"/spike/sessions/"+identity.WorkerSessionID+"/cleanup",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request explicit Session cleanup: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("explicit Session cleanup returned HTTP %d", response.StatusCode)
	}
	deadline := time.Now().Add(timeout)
	for {
		resources, listErr := enumerateRuntimeResources(identity)
		if listErr != nil {
			return fmt.Errorf("enumerate runtime resources after cleanup: %w", listErr)
		}
		if resources.empty() {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"runtime cleanup timed out for session %s with residue %+v",
				identity.WorkerSessionID,
				resources,
			)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func enumerateRuntimeResources(identity runtimeResourceIdentity) (runtimeResourceSet, error) {
	if identity.WorkerSessionID == "" {
		return runtimeResourceSet{}, fmt.Errorf("Worker Session identity is required")
	}
	type resourceQuery struct {
		managed string
		volume  bool
		target  *[]string
	}
	var resources runtimeResourceSet
	queries := []resourceQuery{
		{managed: "worker-runtime", target: &resources.Workers},
		{managed: "runtime-secret-populator", target: &resources.Populators},
		{managed: "runtime-secret-volume", volume: true, target: &resources.Volumes},
	}
	for _, query := range queries {
		arguments := []string{
			"ps",
			"-aq",
			"--filter",
			"label=com.switchback.managed=" + query.managed,
			"--filter",
			"label=com.switchback.worker-session-id=" + identity.WorkerSessionID,
		}
		if query.volume {
			arguments = []string{
				"volume",
				"ls",
				"-q",
				"--filter",
				"label=com.switchback.managed=" + query.managed,
				"--filter",
				"label=com.switchback.worker-session-id=" + identity.WorkerSessionID,
			}
		}
		output, err := dockerCombinedOutput(arguments...)
		if err != nil {
			return runtimeResourceSet{}, fmt.Errorf("enumerate %s resources", query.managed)
		}
		*query.target = strings.Fields(string(output))
	}
	return resources, nil
}

func verifyExactRuntimeResourceLabels(
	kind, resourceID, managed string,
	identity runtimeResourceIdentity,
) error {
	if resourceID == "" {
		return fmt.Errorf("resource identity is empty")
	}
	arguments := []string{"inspect", "--format", "{{json .Config.Labels}}", resourceID}
	if kind == "volume" {
		arguments = []string{"volume", "inspect", "--format", "{{json .Labels}}", resourceID}
	}
	output, err := dockerCombinedOutput(arguments...)
	if err != nil {
		return fmt.Errorf("inspect resource labels")
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(output), &labels); err != nil {
		return fmt.Errorf("decode resource labels")
	}
	expected := map[string]string{
		"com.switchback.managed":             managed,
		"com.switchback.worker-session-id":   identity.WorkerSessionID,
		"com.switchback.worker-run-id":       identity.WorkerRunID,
		"com.switchback.runtime-instance-id": identity.RuntimeInstanceID,
		"com.switchback.runtime-generation":  strconv.FormatUint(uint64(identity.RuntimeGeneration), 10),
	}
	if !reflect.DeepEqual(labels, expected) {
		return fmt.Errorf("resource labels do not match exact runtime identity")
	}
	return nil
}

func treeDigestPath(t *testing.T, root string) string {
	t.Helper()
	spikeDirectory := filepath.Dir(filepath.Dir(root))
	command := exec.Command(filepath.Join(spikeDirectory, "scripts", "tree-digest"), root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compute fixture tree digest: %v", err)
	}
	digest := strings.TrimSpace(string(output))
	if len(digest) != 64 || !validTreeID(digest) {
		t.Fatalf("fixture tree digest is invalid")
	}
	return digest
}

func composeLogs(t *testing.T, dataRoot string) string {
	t.Helper()
	root := spikeRoot(t)
	command := exec.Command(
		"docker",
		"compose",
		"--env-file",
		filepath.Join(dataRoot, "env", "compose.env"),
		"-f",
		filepath.Join(root, "compose.yaml"),
		"logs",
		"--no-color",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read Compose logs failed")
	}
	return string(output)
}

func stopComposeService(t *testing.T, dataRoot, service string) {
	t.Helper()
	root := spikeRoot(t)
	command := exec.Command(
		"docker",
		"compose",
		"--env-file",
		filepath.Join(dataRoot, "env", "compose.env"),
		"-f",
		filepath.Join(root, "compose.yaml"),
		"stop",
		"--timeout",
		"10",
		service,
	)
	if err := command.Run(); err != nil {
		t.Fatalf("stop %s for failure matrix: %v", service, err)
	}
	containerID := strings.TrimSpace(dockerCommand(
		t,
		"ps",
		"-aq",
		"--filter",
		"label=com.docker.compose.project=switchback-worker-runtime-spike",
		"--filter",
		"label=com.docker.compose.service="+service,
	))
	if containerID == "" {
		t.Fatalf("stopped %s container identity is unavailable", service)
	}
	state := strings.TrimSpace(dockerCommand(t, "inspect", "--format", "{{.State.Status}}", containerID))
	if state == "running" {
		t.Fatalf("%s remained running after failure-matrix stop", service)
	}
}

func dockerCommand(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := dockerCombinedOutput(arguments...)
	if err != nil {
		t.Fatalf("docker command failed")
	}
	return string(output)
}

func dockerCombinedOutput(arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", arguments...).CombinedOutput()
}
