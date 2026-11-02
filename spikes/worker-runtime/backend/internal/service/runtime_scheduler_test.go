package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
)

func TestTrustedBootstrapCarriesAgentProfileAndRedactsTokens(t *testing.T) {
	root := t.TempDir()
	mapper := runtime.PathMapper{
		ContainerDataRoot: filepath.Join(root, "backend"),
		HostDataRoot:      filepath.Join(root, "host"),
	}
	for _, path := range []string{mapper.ContainerDataRoot, mapper.HostDataRoot} {
		if err := os.MkdirAll(filepath.Join(path, "sessions", "session-1"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options := TrustedSupplementalResolverOptions{
		Mapper: mapper,
		ModelSecretSource: runtime.PathPair{
			ContainerPath: filepath.Join(mapper.ContainerDataRoot, "env", "upstream-model.key"),
			HostPath:      filepath.Join(mapper.HostDataRoot, "env", "upstream-model.key"),
		},
		ModelKey: []byte("upstream-secret"),
		ModelSecretOwnerUID: func(*os.File) (uint32, error) {
			return 0, nil
		},
		UpstreamModel:   "openai/codex",
		UpstreamAPIBase: "https://models.example.invalid/v1",
		AgentProfile:    agentprofile.IntegrationFake,
		NewToken: func() (string, error) {
			return "internal-token", nil
		},
	}
	resolver, err := NewTrustedSupplementalPathResolver(options)
	if err != nil {
		t.Fatalf("NewTrustedSupplementalPathResolver() error = %v", err)
	}
	paths, err := resolver(
		context.Background(),
		control.Identity{WorkerSessionID: "session-1"},
		MaterializedSession{},
	)
	if err != nil {
		t.Fatalf("resolver() error = %v", err)
	}
	content, err := os.ReadFile(paths.Bootstrap.ContainerPath)
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap map[string]string
	if err := json.Unmarshal(content, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap["agent_profile"] != "integration-fake" {
		t.Fatalf("bootstrap agent_profile = %q", bootstrap["agent_profile"])
	}
	for _, formatted := range []string{
		fmt.Sprintf("%v", options),
		fmt.Sprintf("%+v", options),
		fmt.Sprintf("%#v", options),
	} {
		for _, token := range []string{"upstream-secret", "internal-token"} {
			if strings.Contains(formatted, token) {
				t.Fatalf("formatted options exposed %q: %s", token, formatted)
			}
		}
	}
}

func TestTrustedSupplementalResolverCopiesModelKeyIntoRootOwnedSessionFile(t *testing.T) {
	root := t.TempDir()
	mapper := runtime.PathMapper{ContainerDataRoot: filepath.Join(root, "backend"), HostDataRoot: filepath.Join(root, "host")}
	for _, path := range []string{mapper.ContainerDataRoot, mapper.HostDataRoot} {
		if err := os.MkdirAll(filepath.Join(path, "sessions", "session-1"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sourceSecret := runtime.PathPair{
		ContainerPath: filepath.Join(mapper.ContainerDataRoot, "env", "upstream-model.key"),
		HostPath:      filepath.Join(mapper.HostDataRoot, "env", "upstream-model.key"),
	}
	resolver, err := NewTrustedSupplementalPathResolver(TrustedSupplementalResolverOptions{
		Mapper:            mapper,
		ModelSecretSource: sourceSecret,
		ModelKey:          []byte("upstream-key\n"),
		ModelSecretOwnerUID: func(*os.File) (uint32, error) {
			return 0, nil
		},
		UpstreamModel:   "openai/codex",
		UpstreamAPIBase: "https://models.example.invalid/v1",
		AgentProfile:    agentprofile.Codex,
		NewToken: func() (string, error) {
			return "trusted-token", nil
		},
	})
	if err != nil {
		t.Fatalf("NewTrustedSupplementalPathResolver() error = %v", err)
	}
	paths, err := resolver(context.Background(), control.Identity{WorkerSessionID: "session-1"}, MaterializedSession{})
	if err != nil {
		t.Fatalf("resolver() error = %v", err)
	}
	wantModelSecret, err := mapper.SessionPath("session-1", "model-secret")
	if err != nil {
		t.Fatal(err)
	}
	if paths.ModelSecret != wantModelSecret {
		t.Fatalf("ModelSecret = %#v, want session copy %#v", paths.ModelSecret, wantModelSecret)
	}
	if paths.ModelSecret == sourceSecret {
		t.Fatal("ModelSecret reused the host-user-owned source file")
	}
	modelInfo, err := os.Lstat(paths.ModelSecret.ContainerPath)
	if err != nil {
		t.Fatalf("lstat model secret: %v", err)
	}
	if !modelInfo.Mode().IsRegular() || modelInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("model secret mode = %v, want a regular non-symlink", modelInfo.Mode())
	}
	if modelInfo.Mode().Perm() != 0o400 && modelInfo.Mode().Perm() != 0o600 {
		t.Fatalf("model secret mode = %o, want 0400 or 0600", modelInfo.Mode().Perm())
	}
	modelContent, err := os.ReadFile(paths.ModelSecret.ContainerPath)
	if err != nil {
		t.Fatalf("read model secret: %v", err)
	}
	if string(modelContent) != "upstream-key\n" {
		t.Fatalf("model secret content = %q, want source key copy", modelContent)
	}
	info, err := os.Stat(paths.Bootstrap.ContainerPath)
	if err != nil {
		t.Fatalf("stat bootstrap: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap mode = %o, want 600", info.Mode().Perm())
	}
	var bootstrap map[string]string
	content, err := os.ReadFile(paths.Bootstrap.ContainerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &bootstrap); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrap["upstream_model"] != "openai/codex" || bootstrap["upstream_api_base"] != "https://models.example.invalid/v1" {
		t.Fatalf("bootstrap route = %#v", bootstrap)
	}
	if string(content) == "upstream-key" || strings.Contains(string(content), "upstream-key") {
		t.Fatalf("bootstrap exposed the upstream key: %s", content)
	}
}

func TestTrustedSupplementalResolverFailsClosedWhenSessionSecretIsNotRootOwned(t *testing.T) {
	root := t.TempDir()
	mapper := runtime.PathMapper{
		ContainerDataRoot: filepath.Join(root, "backend"),
		HostDataRoot:      filepath.Join(root, "host"),
	}
	for _, path := range []string{mapper.ContainerDataRoot, mapper.HostDataRoot} {
		if err := os.MkdirAll(filepath.Join(path, "sessions", "session-1"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := NewTrustedSupplementalPathResolver(TrustedSupplementalResolverOptions{
		Mapper: mapper,
		ModelSecretSource: runtime.PathPair{
			ContainerPath: filepath.Join(mapper.ContainerDataRoot, "env", "upstream-model.key"),
			HostPath:      filepath.Join(mapper.HostDataRoot, "env", "upstream-model.key"),
		},
		ModelKey: []byte("must-not-leak\n"),
		ModelSecretOwnerUID: func(*os.File) (uint32, error) {
			return 501, nil
		},
		UpstreamModel:   "openai/codex",
		UpstreamAPIBase: "https://models.example.invalid/v1",
		AgentProfile:    agentprofile.Codex,
		NewToken: func() (string, error) {
			return "trusted-token", nil
		},
	})
	if err != nil {
		t.Fatalf("NewTrustedSupplementalPathResolver() error = %v", err)
	}

	_, err = resolver(context.Background(), control.Identity{WorkerSessionID: "session-1"}, MaterializedSession{})
	if err == nil {
		t.Fatal("resolver() error = nil, want non-root owner rejection")
	}
	if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("resolver() error exposed key bytes: %v", err)
	}
	modelSecret, pathErr := mapper.SessionPath("session-1", "model-secret")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Lstat(modelSecret.ContainerPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed model secret stat error = %v, want removed", statErr)
	}
}

func TestRuntimeSchedulerRollsBackEveryPostProvisionFailureExactlyOnce(t *testing.T) {
	tests := []struct {
		name          string
		provisionErr  error
		waitReadyErr  error
		sendErr       error
		wantLastSteps []string
	}{
		{
			name:          "provision",
			provisionErr:  errors.New("provision failed"),
			wantLastSteps: []string{"revoke", "cleanup"},
		},
		{
			name:          "wait ready",
			waitReadyErr:  errors.New("ready failed"),
			wantLastSteps: []string{"revoke", "cleanup"},
		},
		{
			name:          "initialize enqueue",
			sendErr:       errors.New("enqueue failed"),
			wantLastSteps: []string{"revoke", "cleanup"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &schedulerCallRecorder{}
			controlPlane := newSchedulerControl(recorder)
			controlPlane.waitReadyErr = test.waitReadyErr
			controlPlane.sendErr = test.sendErr
			provisioner := newSchedulerProvisioner(recorder)
			provisioner.provisionErr = test.provisionErr
			scheduler := mustRuntimeScheduler(t, controlPlane, provisioner)
			handoff := schedulerTestHandoff()

			err := scheduler.Initial(context.Background(), handoff)
			expected := test.provisionErr
			if expected == nil {
				expected = test.waitReadyErr
			}
			if expected == nil {
				expected = test.sendErr
			}
			if !errors.Is(err, expected) {
				t.Fatalf("Initial() error = %v, want injected failure", err)
			}
			if err := scheduler.Cleanup(context.Background(), handoff); err != nil && !errors.Is(err, expected) {
				t.Fatalf("first Cleanup() error = %v", err)
			}
			if err := scheduler.Cleanup(context.Background(), handoff); err != nil && !errors.Is(err, expected) {
				t.Fatalf("second Cleanup() error = %v", err)
			}
			if got := provisioner.cleanupCalls(); got != 1 {
				t.Fatalf("provisioner cleanup calls = %d, want exactly 1", got)
			}
			if got := controlPlane.revokeCalls(); got != 1 {
				t.Fatalf("control revoke calls = %d, want exactly 1", got)
			}
			assertLastSchedulerSteps(t, recorder.snapshot(), test.wantLastSteps)
		})
	}
}

func TestRuntimeSchedulerInitializeCarriesTraceAndWorkerVisiblePaths(t *testing.T) {
	recorder := &schedulerCallRecorder{}
	controlPlane := newSchedulerControl(recorder)
	scheduler, err := NewRuntimeScheduler(
		controlPlane,
		newSchedulerProvisioner(recorder),
		agentprofile.IntegrationFake,
	)
	if err != nil {
		t.Fatalf("NewRuntimeScheduler() error = %v", err)
	}
	handoff := schedulerTestHandoff()

	if err := scheduler.Initial(context.Background(), handoff); err != nil {
		t.Fatalf("Initial() error = %v", err)
	}
	command := controlPlane.lastSentCommand()
	initialize := command.GetInitializeSession()
	if initialize == nil {
		t.Fatalf("initial command = %+v, want InitializeSession", command)
	}
	if initialize.GetTraceId() != handoff.traceID {
		t.Fatalf("InitializeSession trace_id = %q, want %q", initialize.GetTraceId(), handoff.traceID)
	}
	if initialize.GetWorkingCopy() != runtime.WorkerWorkingCopyPath ||
		initialize.GetAgentState() != runtime.WorkerAgentStatePath ||
		initialize.GetTraceSpool() != runtime.WorkerTraceSpoolPath {
		t.Fatalf("InitializeSession paths = %q/%q/%q, want Worker-visible mount paths",
			initialize.GetWorkingCopy(), initialize.GetAgentState(), initialize.GetTraceSpool())
	}
	if initialize.GetAgentProfile() != "integration-fake" {
		t.Fatalf("InitializeSession agent_profile = %q", initialize.GetAgentProfile())
	}
}

func TestRuntimeSchedulerRejectsUnknownAgentProfile(t *testing.T) {
	if scheduler, err := NewRuntimeScheduler(nil, nil, agentprofile.Profile("unknown")); err == nil {
		t.Fatalf("NewRuntimeScheduler() = %T, want invalid profile rejection", scheduler)
	}
}

func mustRuntimeScheduler(
	t *testing.T,
	controlPlane ControlPlane,
	provisioner RuntimeProvisioner,
) Scheduler {
	t.Helper()
	scheduler, err := NewRuntimeScheduler(controlPlane, provisioner, agentprofile.Codex)
	if err != nil {
		t.Fatalf("NewRuntimeScheduler() error = %v", err)
	}
	return scheduler
}

func TestRuntimeSchedulerCleanupPreemptsInitialAndSerializesTurn(t *testing.T) {
	recorder := &schedulerCallRecorder{}
	controlPlane := newSchedulerControl(recorder)
	controlPlane.blockWaitReady = true
	provisioner := newSchedulerProvisioner(recorder)
	provisioner.blockCleanup()
	t.Cleanup(provisioner.releaseCleanup)
	scheduler := mustRuntimeScheduler(t, controlPlane, provisioner)
	handoff := schedulerTestHandoff()

	initialDone := make(chan error, 1)
	go func() { initialDone <- scheduler.Initial(context.Background(), handoff) }()
	controlPlane.waitUntilWaiting(t)

	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- scheduler.Cleanup(context.Background(), handoff) }()
	provisioner.waitCleanupStarted(t)
	if got := controlPlane.revokeCalls(); got != 1 {
		t.Fatalf("cleanup reached provisioner before fencing: revoke calls = %d, want 1", got)
	}

	turnDone := make(chan error, 1)
	go func() {
		next := handoff
		next.turnID = "turn-2"
		turnDone <- scheduler.Turn(context.Background(), next)
	}()
	select {
	case err := <-turnDone:
		t.Fatalf("Turn() crossed an in-flight Initial/Cleanup boundary: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if got := controlPlane.startTurnSendCalls(); got != 0 {
		t.Fatalf("StartAgentTurn sends during cleanup = %d, want 0", got)
	}

	provisioner.releaseCleanup()
	if err := <-initialDone; !errors.Is(err, control.ErrUnauthenticated) {
		t.Fatalf("Initial() after cleanup fence error = %v, want ErrUnauthenticated", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if err := <-turnDone; err == nil {
		t.Fatal("Turn() after cleanup error = nil")
	}
	if got := provisioner.cleanupCalls(); got != 1 {
		t.Fatalf("provisioner cleanup calls = %d, want exactly 1", got)
	}
	if got := controlPlane.startTurnSendCalls(); got != 0 {
		t.Fatalf("StartAgentTurn sends after cleanup = %d, want 0", got)
	}
}

func TestRuntimeSchedulerCleanupCannotFenceBeforeCredentialRegistrationCommits(t *testing.T) {
	controlPlane := newBlockingRegistrationControl()
	t.Cleanup(controlPlane.releaseRegister)
	provisioner := newSchedulerProvisioner(&schedulerCallRecorder{})
	scheduler := mustRuntimeScheduler(t, controlPlane, provisioner)
	handoff := schedulerTestHandoff()

	initialDone := make(chan error, 1)
	go func() { initialDone <- scheduler.Initial(context.Background(), handoff) }()
	controlPlane.waitRegisterStarted(t)

	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- scheduler.Cleanup(context.Background(), handoff) }()
	select {
	case <-controlPlane.revokeStarted:
		t.Fatal("Cleanup fenced before credential registration committed")
	case <-time.After(25 * time.Millisecond):
	}

	controlPlane.releaseRegister()
	if err := <-initialDone; !errors.Is(err, control.ErrUnauthenticated) {
		t.Fatalf("Initial() after concurrent cleanup error = %v, want ErrUnauthenticated", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if controlPlane.isCredentialActive() {
		t.Fatal("credential remained active after concurrent Initial/Cleanup")
	}
}

func TestSecretBearingProvisioningValuesDoNotStringifySecretsOrPaths(t *testing.T) {
	handoff := schedulerTestHandoff()
	request := RuntimeProvisionRequest{
		Identity:     handoffIdentity(handoff),
		WorkerToken:  handoff.workerToken,
		Materialized: handoff.materialized,
	}
	credential := control.Credential{Identity: request.Identity, Token: handoff.workerToken}
	token := workerToken{
		value:             handoff.workerToken,
		workerSessionID:   handoff.workerSessionID,
		workerRunID:       handoff.workerRunID,
		runtimeInstanceID: handoff.runtimeInstanceID,
		runtimeGeneration: handoff.runtimeGeneration,
	}
	authorization := EvidenceAuthorization{
		workerSessionID:   handoff.workerSessionID,
		workerRunID:       handoff.workerRunID,
		runtimeInstanceID: handoff.runtimeInstanceID,
		runtimeGeneration: handoff.runtimeGeneration,
		turnID:            handoff.turnID,
		workerToken:       handoff.workerToken,
	}
	values := []struct {
		name  string
		value any
	}{
		{name: "scheduler handoff value", value: handoff},
		{name: "scheduler handoff pointer", value: &handoff},
		{name: "provision request value", value: request},
		{name: "provision request pointer", value: &request},
		{name: "credential value", value: credential},
		{name: "credential pointer", value: &credential},
		{name: "worker token value", value: token},
		{name: "worker token pointer", value: &token},
		{name: "evidence authorization value", value: authorization},
		{name: "evidence authorization pointer", value: &authorization},
	}
	forbidden := []string{handoff.workerToken}
	for _, pair := range []runtime.PathPair{
		handoff.materialized.WorkingCopy,
		handoff.materialized.AgentState,
		handoff.materialized.TraceSpool,
		handoff.materialized.RuntimeLogs,
	} {
		forbidden = append(forbidden, pair.HostPath, pair.ContainerPath)
	}

	for _, value := range values {
		formatted := []struct {
			name string
			text string
		}{
			{name: "%v", text: fmt.Sprintf("%v", value.value)},
			{name: "%+v", text: fmt.Sprintf("%+v", value.value)},
			{name: "%#v", text: fmt.Sprintf("%#v", value.value)},
		}
		for _, output := range formatted {
			for index, secret := range forbidden {
				if strings.Contains(output.text, secret) {
					t.Fatalf("%s with %s exposed protected value #%d", value.name, output.name, index+1)
				}
			}
		}
	}
}

func TestRuntimeProvisionerAdapterBuildsTheTrustedRuntimeRequest(t *testing.T) {
	handoff := schedulerTestHandoff()
	input := RuntimeProvisionRequest{
		Identity:     handoffIdentity(handoff),
		WorkerToken:  handoff.workerToken,
		Materialized: handoff.materialized,
	}
	bootstrap := runtime.PathPair{ContainerPath: "/backend/bootstrap.json", HostPath: "/host/bootstrap.json"}
	modelSecret := runtime.PathPair{ContainerPath: "/backend/model-secret", HostPath: "/host/model-secret"}
	inner := &capturingRuntimeProvisioner{}
	resolverCalls := 0
	adapter := NewRuntimeProvisionerAdapter(
		inner,
		func(_ context.Context, identity control.Identity, materialized MaterializedSession) (RuntimeSupplementalPaths, error) {
			resolverCalls++
			if identity != input.Identity || materialized != input.Materialized {
				t.Fatalf("resolver input = %#v %#v, want %#v %#v", identity, materialized, input.Identity, input.Materialized)
			}
			return RuntimeSupplementalPaths{Bootstrap: bootstrap, ModelSecret: modelSecret}, nil
		},
	)

	if err := adapter.Provision(context.Background(), input); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
	want := runtime.ProvisionRequest{
		Identity: runtime.RuntimeIdentity{
			WorkerSessionID:   input.Identity.WorkerSessionID,
			WorkerRunID:       input.Identity.WorkerRunID,
			RuntimeInstanceID: input.Identity.RuntimeInstanceID,
			RuntimeGeneration: input.Identity.RuntimeGeneration,
		},
		WorkerToken: input.WorkerToken,
		WorkingCopy: input.Materialized.WorkingCopy,
		AgentState:  input.Materialized.AgentState,
		TraceSpool:  input.Materialized.TraceSpool,
		Bootstrap:   bootstrap,
		ModelSecret: modelSecret,
	}
	if inner.started != want {
		t.Fatalf("runtime Start request = %#v, want %#v", inner.started, want)
	}
	if err := adapter.Cleanup(context.Background(), input.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if inner.cleaned != want.Identity {
		t.Fatalf("runtime Cleanup identity = %#v, want %#v", inner.cleaned, want.Identity)
	}
}

func TestRuntimeProvisionerAdapterSanitizesResolverErrorsAndPreservesCause(t *testing.T) {
	handoff := schedulerTestHandoff()
	input := RuntimeProvisionRequest{
		Identity:     handoffIdentity(handoff),
		WorkerToken:  handoff.workerToken,
		Materialized: handoff.materialized,
	}
	protected := []string{
		input.WorkerToken,
		input.Materialized.WorkingCopy.HostPath,
		"/host/bootstrap-secret.json",
		"/host/model-secret",
	}
	cause := &injectedResolverError{text: strings.Join(protected, " | ")}
	adapter := NewRuntimeProvisionerAdapter(
		&capturingRuntimeProvisioner{},
		func(context.Context, control.Identity, MaterializedSession) (RuntimeSupplementalPaths, error) {
			return RuntimeSupplementalPaths{}, cause
		},
	)

	err := adapter.Provision(context.Background(), input)
	if !errors.Is(err, cause) {
		t.Fatalf("Provision() error = %v, want errors.Is(injected cause)", err)
	}
	var typed *injectedResolverError
	if !errors.As(err, &typed) || typed != cause {
		t.Fatalf("Provision() error = %v, want errors.As(*injectedResolverError)", err)
	}
	var wrapped *sanitizedResolverError
	if !errors.As(err, &wrapped) {
		t.Fatalf("Provision() error = %v, want errors.As(*sanitizedResolverError)", err)
	}
	want := "resolve supplemental runtime paths failed"
	formats := []struct {
		format string
		want   string
	}{
		{format: "%v", want: want},
		{format: "%+v", want: want},
		{format: "%#v", want: want},
		{format: "%q", want: fmt.Sprintf("%q", want)},
	}
	values := []struct {
		name  string
		value any
	}{
		{name: "pointer", value: wrapped},
		{name: "value", value: *wrapped},
	}
	for _, value := range values {
		for _, format := range formats {
			got := fmt.Sprintf(format.format, value.value)
			if got != format.want {
				t.Errorf("%s formatted with %s = %q, want %q", value.name, format.format, got, format.want)
			}
			for _, secret := range protected {
				if strings.Contains(got, secret) {
					t.Errorf("%s formatted with %s exposed protected value %q", value.name, format.format, secret)
				}
			}
		}
	}
}

type injectedResolverError struct {
	text string
}

func (e *injectedResolverError) Error() string {
	return e.text
}

type capturingRuntimeProvisioner struct {
	started runtime.ProvisionRequest
	cleaned runtime.RuntimeIdentity
}

func (p *capturingRuntimeProvisioner) Start(_ context.Context, request runtime.ProvisionRequest) (runtime.Instance, error) {
	p.started = request
	return runtime.Instance{ContainerID: "container-1"}, nil
}

func (p *capturingRuntimeProvisioner) Cleanup(_ context.Context, identity runtime.RuntimeIdentity) error {
	p.cleaned = identity
	return nil
}

func schedulerTestHandoff() schedulerHandoff {
	return schedulerHandoff{
		workerSessionID:   "session-1",
		workerRunID:       "run-1",
		runtimeInstanceID: "runtime-1",
		runtimeGeneration: 1,
		turnID:            "turn-1",
		traceID:           "trace-1",
		workerToken:       "private-worker-token",
		materialized:      distinctiveMaterializedSession("session-1"),
	}
}

type schedulerCallRecorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *schedulerCallRecorder) add(step string) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	r.mu.Unlock()
}

func (r *schedulerCallRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

func assertLastSchedulerSteps(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("scheduler steps count = %d, want at least %d", len(got), len(want))
	}
	offset := len(got) - len(want)
	for index := range want {
		if got[offset+index] != want[index] {
			t.Fatalf("scheduler step %d from end = %q, want %q", len(want)-index, got[offset+index], want[index])
		}
	}
}

type schedulerControl struct {
	recorder       *schedulerCallRecorder
	waitReadyErr   error
	sendErr        error
	blockWaitReady bool
	waitStarted    chan struct{}
	revoked        chan struct{}
	waitOnce       sync.Once
	revokeOnce     sync.Once
	mu             sync.Mutex
	revokes        int
	startTurnSends int
	lastCommand    *workercontrolv1.BackendEnvelope
}

type blockingRegistrationControl struct {
	registerStarted chan struct{}
	registerRelease chan struct{}
	revokeStarted   chan struct{}
	registerOnce    sync.Once
	releaseOnce     sync.Once
	revokeOnce      sync.Once
	mu              sync.Mutex
	credentialLive  bool
}

func newBlockingRegistrationControl() *blockingRegistrationControl {
	return &blockingRegistrationControl{
		registerStarted: make(chan struct{}),
		registerRelease: make(chan struct{}),
		revokeStarted:   make(chan struct{}),
	}
}

func (c *blockingRegistrationControl) RegisterCredential(control.Credential) error {
	c.registerOnce.Do(func() { close(c.registerStarted) })
	<-c.registerRelease
	c.mu.Lock()
	c.credentialLive = true
	c.mu.Unlock()
	return nil
}

func (c *blockingRegistrationControl) Revoke(control.Identity) {
	c.mu.Lock()
	c.credentialLive = false
	c.mu.Unlock()
	c.revokeOnce.Do(func() { close(c.revokeStarted) })
}

func (c *blockingRegistrationControl) WaitReady(context.Context, control.Identity) error {
	return nil
}

func (c *blockingRegistrationControl) Send(context.Context, control.Identity, *workercontrolv1.BackendEnvelope) error {
	return nil
}

func (c *blockingRegistrationControl) waitRegisterStarted(t *testing.T) {
	t.Helper()
	select {
	case <-c.registerStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for credential registration")
	}
}

func (c *blockingRegistrationControl) releaseRegister() {
	c.releaseOnce.Do(func() { close(c.registerRelease) })
}

func (c *blockingRegistrationControl) isCredentialActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentialLive
}

func newSchedulerControl(recorder *schedulerCallRecorder) *schedulerControl {
	return &schedulerControl{
		recorder:    recorder,
		waitStarted: make(chan struct{}),
		revoked:     make(chan struct{}),
	}
}

func (c *schedulerControl) RegisterCredential(control.Credential) error {
	c.recorder.add("register")
	return nil
}

func (c *schedulerControl) Revoke(control.Identity) {
	c.recorder.add("revoke")
	c.mu.Lock()
	c.revokes++
	c.mu.Unlock()
	c.revokeOnce.Do(func() { close(c.revoked) })
}

func (c *schedulerControl) WaitReady(ctx context.Context, _ control.Identity) error {
	c.recorder.add("wait_ready")
	c.waitOnce.Do(func() { close(c.waitStarted) })
	if c.waitReadyErr != nil {
		return c.waitReadyErr
	}
	if !c.blockWaitReady {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.revoked:
		return control.ErrUnauthenticated
	}
}

func (c *schedulerControl) Send(_ context.Context, _ control.Identity, command *workercontrolv1.BackendEnvelope) error {
	c.mu.Lock()
	c.lastCommand = command
	c.mu.Unlock()
	switch command.GetPayload().(type) {
	case *workercontrolv1.BackendEnvelope_StartAgentTurn:
		c.recorder.add("send_turn")
		c.mu.Lock()
		c.startTurnSends++
		c.mu.Unlock()
	default:
		c.recorder.add("send")
	}
	return c.sendErr
}

func (c *schedulerControl) lastSentCommand() *workercontrolv1.BackendEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCommand
}

func (c *schedulerControl) waitUntilWaiting(t *testing.T) {
	t.Helper()
	select {
	case <-c.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Initial WaitReady")
	}
}

func (c *schedulerControl) revokeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revokes
}

func (c *schedulerControl) startTurnSendCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startTurnSends
}

type schedulerProvisioner struct {
	recorder       *schedulerCallRecorder
	provisionErr   error
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	startOnce      sync.Once
	releaseOnce    sync.Once
	mu             sync.Mutex
	cleanups       int
}

func newSchedulerProvisioner(recorder *schedulerCallRecorder) *schedulerProvisioner {
	return &schedulerProvisioner{
		recorder:       recorder,
		cleanupStarted: make(chan struct{}),
	}
}

func (p *schedulerProvisioner) Provision(context.Context, RuntimeProvisionRequest) error {
	p.recorder.add("provision")
	return p.provisionErr
}

func (p *schedulerProvisioner) Cleanup(ctx context.Context, _ control.Identity) error {
	p.recorder.add("cleanup")
	p.mu.Lock()
	p.cleanups++
	p.mu.Unlock()
	p.startOnce.Do(func() { close(p.cleanupStarted) })
	if p.cleanupRelease != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.cleanupRelease:
		}
	}
	return nil
}

func (p *schedulerProvisioner) blockCleanup() {
	p.cleanupRelease = make(chan struct{})
}

func (p *schedulerProvisioner) waitCleanupStarted(t *testing.T) {
	t.Helper()
	select {
	case <-p.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provisioner cleanup")
	}
}

func (p *schedulerProvisioner) releaseCleanup() {
	p.releaseOnce.Do(func() { close(p.cleanupRelease) })
}

func (p *schedulerProvisioner) cleanupCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanups
}
