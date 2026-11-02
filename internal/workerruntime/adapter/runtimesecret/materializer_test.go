package runtimesecret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestMaterializerCreatesRootOnlyPerRuntimeInputsWithoutKeyInBootstrap(t *testing.T) {
	backendSessionRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendSessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ownership := &recordingOwnership{}
	materializer := runtimesecret.Materializer{
		Ownership: ownership,
		OwnerUID:  func(string) (uint32, error) { return 0, nil },
		NewToken:  sequentialTokens("proxy-token", "model-sink-token"),
	}
	providerKey := runtimesecret.ProviderCredential([]byte("provider-secret-value"))

	resources, err := materializer.Materialize(runtimesecret.Request{
		WorkerSessionID:    "session-1",
		WorkerRunID:        "run-1",
		RuntimeInstanceID:  "runtime-1",
		Generation:         1,
		ChannelEpoch:       7,
		WorkerControlToken: "worker-control-token",
		SessionStorage: application.SessionStorage{
			BackendRoot: backendSessionRoot,
			EngineRoot:  "/srv/switchback/sessions/session-1",
		},
		AgentProfile: application.ProfileRef{
			ID:       "deepseek-codex-openai-v1",
			Revision: 1,
			Digest:   "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb",
		},
		ProviderCredential: providerKey,
	})
	if err != nil {
		t.Fatalf("materialize Runtime secrets: %v", err)
	}

	wantBackendRoot := filepath.Join(backendSessionRoot, "runtimes", "runtime-1-g1", "secret-input")
	wantEngineRoot := "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input"
	if resources.BackendRoot != wantBackendRoot || resources.EngineRoot != wantEngineRoot {
		t.Fatalf("secret roots = %+v", resources)
	}
	for _, path := range []string{resources.Bootstrap.BackendPath, resources.ModelCredential.BackendPath} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat secret input %q: %v", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o400 {
			t.Fatalf("secret input %q mode = %v", path, info.Mode())
		}
		if !ownership.has(path, 0, 0) {
			t.Fatalf("secret input %q was not handed to root", path)
		}
	}
	modelBytes, err := os.ReadFile(resources.ModelCredential.BackendPath)
	if err != nil || !bytes.Equal(modelBytes, providerKey) {
		t.Fatalf("model credential bytes = %q, err = %v", modelBytes, err)
	}
	bootstrapBytes, err := os.ReadFile(resources.Bootstrap.BackendPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bootstrapBytes, providerKey) {
		t.Fatal("bootstrap contains provider credential")
	}
	var bootstrap runtimesecret.Bootstrap
	if err := json.Unmarshal(bootstrapBytes, &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap.AgentProfileID != "deepseek-codex-openai-v1" || bootstrap.LiteLLMModel != "deepseek/deepseek-v4-pro" ||
		bootstrap.UpstreamAPIBase != "https://api.deepseek.com" || bootstrap.LiteLLMUID != 10001 || bootstrap.AgentUID != 10002 ||
		bootstrap.ProxyToken != "proxy-token" || bootstrap.ModelSinkToken != "model-sink-token" ||
		bootstrap.WorkerSessionID != "session-1" || bootstrap.WorkerRunID != "run-1" ||
		bootstrap.ChannelEpoch != 7 || bootstrap.WorkerControlToken != "worker-control-token" {
		t.Fatalf("unexpected bootstrap: %+v", bootstrap)
	}
	if formatted := fmt.Sprintf("%v %#v", bootstrap, bootstrap); strings.Contains(formatted, "worker-control-token") ||
		strings.Contains(formatted, "proxy-token") || strings.Contains(formatted, "model-sink-token") {
		t.Fatalf("Bootstrap formatting leaked a generated token: %s", formatted)
	}
	if formatted := fmt.Sprintf("%v %#v", providerKey, providerKey); strings.Contains(formatted, "provider-secret-value") {
		t.Fatalf("ProviderCredential formatting leaked secret: %s", formatted)
	}
	liveCredentialPath := filepath.Join(
		filepath.Dir(resources.BackendRoot),
		"control-plane",
		"live-credential.json",
	)
	liveCredentialInfo, err := os.Lstat(liveCredentialPath)
	if err != nil || !liveCredentialInfo.Mode().IsRegular() ||
		liveCredentialInfo.Mode().Perm() != 0o400 ||
		!ownership.has(liveCredentialPath, 0, 0) {
		t.Fatalf("live credential metadata=%v error=%v", liveCredentialInfo, err)
	}
	liveCredentialBytes, err := os.ReadFile(liveCredentialPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		providerKey,
		[]byte("proxy-token"),
		[]byte("model-sink-token"),
	} {
		if bytes.Contains(liveCredentialBytes, forbidden) {
			t.Fatal("Backend live credential retained a non-control secret")
		}
	}
}

func TestChildProcessSpecsEnforceSameUIDCredentialBoundary(t *testing.T) {
	bootstrap := runtimesecret.Bootstrap{
		LiteLLMUID:         10001,
		AgentUID:           10002,
		LiteLLMModel:       "anthropic/kimi-for-coding",
		UpstreamAPIBase:    "https://api.kimi.com/coding",
		ProxyToken:         "local-proxy-token",
		ModelSinkToken:     "sink-token",
		WorkerSessionID:    "session-1",
		WorkerRunID:        "run-1",
		ChannelEpoch:       1,
		WorkerControlToken: "worker-control-token",
	}
	providerKey := runtimesecret.ProviderCredential([]byte("upstream-key"))
	specs, err := runtimesecret.ChildProcessSpecs(bootstrap, providerKey)
	if err != nil {
		t.Fatalf("build child specs: %v", err)
	}
	if specs.LiteLLM.UID != 10001 || specs.LiteLLM.GID != 10001 || specs.Codex.UID != 10002 || specs.Tool.UID != 10002 {
		t.Fatalf("child identities = %+v", specs)
	}
	if !containsEnvValue(specs.LiteLLM.Env, "SWITCHBACK_UPSTREAM_API_KEY", "upstream-key") {
		t.Fatal("LiteLLM child did not receive the upstream key")
	}
	for name, spec := range map[string]runtimesecret.ChildProcessSpec{"codex": specs.Codex, "tool": specs.Tool} {
		if envContains(spec.Env, "upstream-key") || envContains(spec.Env, "worker-control-token") ||
			containsEnvName(spec.Env, "SWITCHBACK_UPSTREAM_API_KEY") {
			t.Fatalf("%s child received the upstream credential: %v", name, spec.Env)
		}
		if !containsEnvValue(spec.Env, "OPENAI_API_KEY", "local-proxy-token") || !containsEnvValue(spec.Env, "OPENAI_BASE_URL", "http://127.0.0.1:4000/v1") {
			t.Fatalf("%s child local proxy environment = %v", name, spec.Env)
		}
	}
	if !reflect.DeepEqual(specs.Codex, specs.Tool) {
		t.Fatal("Codex and Tool must share the same UID and non-provider environment boundary")
	}
	for _, uid := range []int{10001, 10002} {
		if runtimesecret.POSIXCanRead(uid, 0, 0o400) {
			t.Fatalf("UID %d can read a root-owned 0400 secret", uid)
		}
	}
}

func TestOpenExistingReturnsExactReplacementSecretsWithoutRotation(t *testing.T) {
	backendSessionRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendSessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := application.ProfileRef{
		ID: "deepseek-codex-openai-v1", Revision: 1,
		Digest: "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb",
	}
	storage := application.SessionStorage{
		BackendRoot: backendSessionRoot,
		EngineRoot:  "/srv/switchback/sessions/session-1",
	}
	materializer := runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:  func(string) (uint32, error) { return 0, nil },
		NewToken:  sequentialTokens("proxy-token", "sink-token"),
	}
	created, err := materializer.Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-2", Generation: 2, ChannelEpoch: 3,
		WorkerControlToken: "worker-control-replacement",
		AgentSessionID:     "agent-session-1", LoadExistingSession: true,
		SessionStorage: storage, AgentProfile: profile,
		ProviderCredential: runtimesecret.ProviderCredential([]byte("provider-key")),
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, bootstrap, err := materializer.OpenExisting(runtimesecret.ExistingRequest{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-2", Generation: 2, ChannelEpoch: 3,
		AgentSessionID: "agent-session-1", SessionStorage: storage, AgentProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened, created) ||
		bootstrap.WorkerControlToken != "worker-control-replacement" ||
		!bootstrap.LoadExistingSession || bootstrap.AgentSessionID != "agent-session-1" {
		t.Fatalf("resources=%+v bootstrap=%+v", reopened, bootstrap)
	}
}

func TestOpenLiveExistingRestoresOriginalRuntimeTokenWithoutRotation(t *testing.T) {
	backendSessionRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendSessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := application.ProfileRef{
		ID: "deepseek-codex-openai-v1", Revision: 1,
		Digest: "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb",
	}
	storage := application.SessionStorage{
		BackendRoot: backendSessionRoot,
		EngineRoot:  "/srv/switchback/sessions/session-1",
	}
	materializer := runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:  func(string) (uint32, error) { return 0, nil },
		NewToken:  sequentialTokens("proxy-token", "sink-token"),
	}
	created, err := materializer.Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-1", Generation: 1, ChannelEpoch: 7,
		WorkerControlToken: "original-worker-control-token",
		SessionStorage:     storage, AgentProfile: profile,
		ProviderCredential: runtimesecret.ProviderCredential([]byte("provider-key")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(created.BackendRoot); err != nil {
		t.Fatal(err)
	}

	reopened, bootstrap, err := materializer.OpenLiveExisting(runtimesecret.LiveExistingRequest{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-1", Generation: 1, ChannelEpoch: 7,
		AgentSessionID: "agent-session-established-after-bootstrap",
		SessionStorage: storage, AgentProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened, created) ||
		bootstrap.WorkerControlToken != "original-worker-control-token" ||
		bootstrap.AgentSessionID != "" || bootstrap.LoadExistingSession {
		t.Fatalf("resources=%+v bootstrap=%+v", reopened, bootstrap)
	}

	_, _, err = materializer.OpenLiveExisting(runtimesecret.LiveExistingRequest{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-1", Generation: 1, ChannelEpoch: 8,
		AgentSessionID: "agent-session-established-after-bootstrap",
		SessionStorage: storage, AgentProfile: profile,
	})
	if err == nil {
		t.Fatal("stale live Runtime epoch was accepted")
	}
}

func TestMaterializerRejectsNonRootOwnershipAndRemovesPartialInputs(t *testing.T) {
	backendSessionRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendSessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	materializer := runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:  func(string) (uint32, error) { return 501, nil },
		NewToken:  sequentialTokens("proxy", "sink"),
	}
	_, err := materializer.Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1", Generation: 1,
		ChannelEpoch: 1, WorkerControlToken: "worker-token",
		SessionStorage:     application.SessionStorage{BackendRoot: backendSessionRoot, EngineRoot: "/srv/switchback/sessions/session-1"},
		AgentProfile:       application.ProfileRef{ID: "kimi-codex-anthropic-v1", Revision: 1, Digest: "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2"},
		ProviderCredential: runtimesecret.ProviderCredential([]byte("must-not-leak")),
	})
	if err == nil {
		t.Fatal("non-root-owned Runtime secret was accepted")
	}
	if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("error leaked provider credential: %v", err)
	}
	runtimeRoot := filepath.Join(backendSessionRoot, "runtimes", "runtime-1-g1")
	if _, statErr := os.Lstat(runtimeRoot); !os.IsNotExist(statErr) {
		t.Fatalf("partial Runtime secret tree remains: %v", statErr)
	}
}

func TestMaterializerRejectsSymlinkRuntimeResourcesRoot(t *testing.T) {
	backendSessionRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendSessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(backendSessionRoot, "runtimes")); err != nil {
		t.Fatal(err)
	}
	_, err := (runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }),
		OwnerUID:  func(string) (uint32, error) { return 0, nil },
		NewToken:  sequentialTokens("proxy", "sink"),
	}).Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1", Generation: 1,
		ChannelEpoch: 1, WorkerControlToken: "worker-token",
		SessionStorage:     application.SessionStorage{BackendRoot: backendSessionRoot, EngineRoot: "/srv/switchback/sessions/session-1"},
		AgentProfile:       application.ProfileRef{ID: "kimi-codex-anthropic-v1", Revision: 1, Digest: "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2"},
		ProviderCredential: runtimesecret.ProviderCredential([]byte("provider-key")),
	})
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink Runtime root error = %v", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%v err=%v", entries, readErr)
	}
}

func TestObservationRegistryWriteFailureDoesNotFailMaterializeAndLogsFixedWarning(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	secretError := "sensitive /private/backend/path token=" + observationProviderToken
	storage, _, resources := materializeObservationRegistry(t, func(path string, uid, gid int) error {
		if filepath.Base(path) == "observation-tokens.json" {
			return fmt.Errorf("%s", secretError)
		}
		return nil
	})
	if _, err := os.Stat(resources.Bootstrap.BackendPath); err != nil {
		t.Fatalf("core materialize did not succeed: %v", err)
	}
	_, err := observationScannerMaterializer(func(string) (uint32, error) { return 0, nil }).ObservationScanner(observationRequest(storage))
	assertSanitizedObservationError(t, err)
	diagnostic := logs.String()
	if !strings.Contains(diagnostic, "Runtime observation registry unavailable") ||
		!strings.Contains(diagnostic, "code=observation_registry_unavailable") {
		t.Fatalf("fixed warning missing: %q", diagnostic)
	}
	for _, forbidden := range []string{secretError, observationProviderToken, "/private/backend/path"} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("warning leaked sensitive detail: %q", diagnostic)
		}
	}
}

type ownershipCall struct {
	path string
	uid  int
	gid  int
}

type recordingOwnership struct{ calls []ownershipCall }

func (r *recordingOwnership) Chown(path string, uid, gid int) error {
	r.calls = append(r.calls, ownershipCall{path: path, uid: uid, gid: gid})
	return nil
}

func (r *recordingOwnership) has(path string, uid, gid int) bool {
	for _, call := range r.calls {
		if call.path == path && call.uid == uid && call.gid == gid {
			return true
		}
	}
	return false
}

func sequentialTokens(tokens ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index >= len(tokens) {
			return "", fmt.Errorf("no token")
		}
		token := tokens[index]
		index++
		return token, nil
	}
}

func containsEnvName(environment []string, name string) bool {
	for _, entry := range environment {
		if strings.HasPrefix(entry, name+"=") {
			return true
		}
	}
	return false
}

func containsEnvValue(environment []string, name, value string) bool {
	for _, entry := range environment {
		if entry == name+"="+value {
			return true
		}
	}
	return false
}

func envContains(environment []string, value string) bool {
	for _, entry := range environment {
		if strings.Contains(entry, value) {
			return true
		}
	}
	return false
}
