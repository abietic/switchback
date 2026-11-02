package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

func TestBootstrapRejectsUnknownAgentProfile(t *testing.T) {
	content := []byte(`{
		"upstream_model":"openai/codex",
		"upstream_api_base":"https://model.example.test/v1",
		"proxy_token":"proxy-secret",
		"model_sink_token":"sink-secret",
		"agent_profile":"unknown"
	}`)
	if got, err := parseBootstrap(content); err == nil {
		t.Fatalf("parseBootstrap() = %+v, want unknown agent profile rejection", got)
	}
}

func TestBootstrapFormattingRetainsProfileAndRedactsTokens(t *testing.T) {
	bootstrap := Bootstrap{
		UpstreamModel:   "openai/codex",
		UpstreamAPIBase: "https://model.example.test/v1",
		ProxyToken:      "proxy-secret",
		ModelSinkToken:  "sink-secret",
		AgentProfile:    agentprofile.IntegrationFake,
	}
	for _, formatted := range []string{
		bootstrap.String(),
		bootstrap.GoString(),
		fmt.Sprintf("%v", bootstrap),
		fmt.Sprintf("%#v", bootstrap),
	} {
		if !strings.Contains(formatted, agentprofile.IntegrationFake.String()) {
			t.Fatalf("formatted bootstrap omitted profile: %q", formatted)
		}
		for _, token := range []string{bootstrap.ProxyToken, bootstrap.ModelSinkToken} {
			if strings.Contains(formatted, token) {
				t.Fatalf("formatted bootstrap exposed token %q: %q", token, formatted)
			}
		}
	}
}

func TestAgentProfileMapsOnlyFixedExecutables(t *testing.T) {
	tests := []struct {
		profile agentprofile.Profile
		path    string
	}{
		{profile: agentprofile.Codex, path: "/opt/switchback/node_modules/.bin/codex-acp"},
		{profile: agentprofile.IntegrationFake, path: "/opt/switchback/bin/fake-acp"},
	}
	for _, test := range tests {
		spec, err := AgentChildSpecForProfile(test.profile)
		if err != nil {
			t.Fatalf("AgentChildSpecForProfile(%q) error = %v", test.profile, err)
		}
		if spec.Path != test.path {
			t.Fatalf("AgentChildSpecForProfile(%q).Path = %q, want %q", test.profile, spec.Path, test.path)
		}
		for _, entry := range spec.Env {
			if strings.Contains(entry, "AGENT") || strings.Contains(entry, "EXECUTABLE") {
				t.Fatalf("agent environment exposed executable selection: %q", entry)
			}
		}
	}
	if got, err := AgentChildSpecForProfile(agentprofile.Profile("/tmp/client-agent")); err == nil {
		t.Fatalf("AgentChildSpecForProfile(custom path) = %+v, want rejection", got)
	}
}

func TestChildSpecsSeparateLiteLLMAndAgentSecrets(t *testing.T) {
	for name, value := range map[string]string{
		"SWITCHBACK_SPIKE_UPSTREAM_API_KEY": "ambient-upstream-key",
		"SWITCHBACK_WORKER_TOKEN":           "ambient-worker-token",
		"LANGFUSE_PUBLIC_KEY":          "ambient-langfuse-public",
		"LANGFUSE_SECRET_KEY":          "ambient-langfuse-secret",
		"SWITCHBACK_SPIKE_MODEL_SINK_TOKEN": "ambient-sink-token",
		"UNRELATED":                    "ambient-unrelated",
	} {
		t.Setenv(name, value)
	}

	bootstrap := Bootstrap{
		UpstreamModel:   "openai/gpt-5.2-codex",
		UpstreamAPIBase: "https://model.example.test/v1",
		ProxyToken:      "proxy-secret",
		ModelSinkToken:  "sink-secret",
	}
	liteLLM := LiteLLMChildSpec(bootstrap, "upstream-secret")
	agent := AgentChildSpec()

	if liteLLM.UID != 10001 || liteLLM.GID != 10001 {
		t.Fatalf("LiteLLM credentials = %d:%d, want 10001:10001", liteLLM.UID, liteLLM.GID)
	}
	if liteLLM.Path != "/usr/local/bin/litellm" {
		t.Fatalf("LiteLLM path = %q, want fixed image path", liteLLM.Path)
	}
	wantLiteLLMEnv := []string{
		"SWITCHBACK_SPIKE_MODEL_SINK_TOKEN=sink-secret",
		"SWITCHBACK_SPIKE_PROXY_TOKEN=proxy-secret",
		"SWITCHBACK_SPIKE_UPSTREAM_API_BASE=https://model.example.test/v1",
		"SWITCHBACK_SPIKE_UPSTREAM_API_KEY=upstream-secret",
		"SWITCHBACK_SPIKE_UPSTREAM_MODEL=openai/gpt-5.2-codex",
		"HOME=/var/lib/switchback-litellm",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"PYTHONPATH=/opt/switchback/worker/litellm",
	}
	if !reflect.DeepEqual(liteLLM.Env, wantLiteLLMEnv) {
		t.Fatalf("LiteLLM environment = %#v, want %#v", liteLLM.Env, wantLiteLLMEnv)
	}
	if got, want := liteLLM.Args, []string{
		"litellm",
		"--config", "/opt/switchback/worker/litellm/config.yaml",
		"--host", "127.0.0.1",
		"--port", "4000",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LiteLLM argv = %#v, want %#v", got, want)
	}

	if agent.UID != 10002 || agent.GID != 10002 {
		t.Fatalf("Agent credentials = %d:%d, want 10002:10002", agent.UID, agent.GID)
	}
	wantAgentEnv := []string{
		"SWITCHBACK_SPIKE_MODEL_BASE_URL=http://127.0.0.1:4000",
		"HOME=/agent-state",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if !reflect.DeepEqual(agent.Env, wantAgentEnv) {
		t.Fatalf("Agent environment = %#v, want %#v", agent.Env, wantAgentEnv)
	}

	protected := []string{
		"upstream-secret",
		"proxy-secret",
		"sink-secret",
		"ambient-upstream-key",
		"ambient-worker-token",
		"ambient-langfuse-public",
		"ambient-langfuse-secret",
		"ambient-sink-token",
		"ambient-unrelated",
	}
	for _, value := range protected {
		if strings.Contains(strings.Join(agent.Env, "\n"), value) {
			t.Fatalf("Agent environment leaked %q", value)
		}
	}
	for _, formatted := range []string{
		liteLLM.String(),
		liteLLM.GoString(),
		fmt.Sprintf("%v", liteLLM),
		fmt.Sprintf("%+v", liteLLM),
		fmt.Sprintf("%#v", liteLLM),
	} {
		for _, secret := range []string{"upstream-secret", "proxy-secret", "sink-secret"} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatted child spec leaked %q: %q", secret, formatted)
			}
		}
	}
}

func TestLoadSecretsAcceptsOwnerOnlyRegularFilesWithoutLeakingValues(t *testing.T) {
	paths := writeSupervisorSecrets(t)

	secrets, err := loadSecretsForIdentity(paths, os.Geteuid(), uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("loadSecretsForIdentity() error = %v", err)
	}
	if secrets.UpstreamKey != "upstream-secret" {
		t.Fatalf("upstream key = %q, want loaded key", secrets.UpstreamKey)
	}
	if secrets.Bootstrap.UpstreamModel != "openai/gpt-5.2-codex" ||
		secrets.Bootstrap.UpstreamAPIBase != "https://model.example.test/v1" ||
		secrets.Bootstrap.ProxyToken != "proxy-secret" ||
		secrets.Bootstrap.ModelSinkToken != "sink-secret" ||
		secrets.Bootstrap.AgentProfile != agentprofile.Codex {
		t.Fatalf("loaded bootstrap = %#v", secrets.Bootstrap)
	}

	for _, formatted := range []string{
		secrets.String(),
		secrets.GoString(),
		fmt.Sprintf("%v", secrets),
		fmt.Sprintf("%+v", secrets),
		fmt.Sprintf("%#v", secrets),
	} {
		for _, secret := range []string{"upstream-secret", "proxy-secret", "sink-secret"} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatted secrets leaked %q: %q", secret, formatted)
			}
		}
	}
}

func TestLoadSecretsProductionRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("production EUID rejection requires a non-root test host")
	}
	_, err := LoadSecrets(writeSupervisorSecrets(t))
	if err == nil || !strings.Contains(err.Error(), "EUID 0") {
		t.Fatalf("LoadSecrets() error = %v, want production EUID 0 rejection", err)
	}
}

func TestLoadSecretsRejectsUnsafePathAndFinalFileProperties(t *testing.T) {
	root := realTempDir(t)
	paths := writeSecretsAt(t, root)
	expectedEUID := os.Geteuid()
	expectedOwner := uint32(expectedEUID)

	t.Run("final symlink", func(t *testing.T) {
		link := filepath.Join(root, "bootstrap-link")
		if err := os.Symlink(paths.Bootstrap, link); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(
			SecretPaths{Bootstrap: link, ModelKey: paths.ModelKey},
			expectedEUID,
			expectedOwner,
		)
		if err == nil {
			t.Fatal("loadSecretsForIdentity() accepted a final symlink")
		}
	})

	t.Run("ancestor symlink", func(t *testing.T) {
		realDir := filepath.Join(root, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		nested := writeSecretsAt(t, realDir)
		linkDir := filepath.Join(root, "linked")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(
			SecretPaths{
				Bootstrap: filepath.Join(linkDir, filepath.Base(nested.Bootstrap)),
				ModelKey:  nested.ModelKey,
			},
			expectedEUID,
			expectedOwner,
		)
		if err == nil {
			t.Fatal("loadSecretsForIdentity() accepted an ancestor symlink")
		}
	})

	t.Run("group readable", func(t *testing.T) {
		if err := os.Chmod(paths.ModelKey, 0o440); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(paths, expectedEUID, expectedOwner)
		if err == nil || !strings.Contains(err.Error(), "owner-only") {
			t.Fatalf("loadSecretsForIdentity() error = %v, want safe mode rejection", err)
		}
		for _, secret := range []string{"upstream-secret", "proxy-secret", "sink-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("loadSecretsForIdentity() error leaked %q: %v", secret, err)
			}
		}
	})

	t.Run("wrong owner", func(t *testing.T) {
		_, err := loadSecretsForIdentity(paths, expectedEUID, expectedOwner+1)
		if err == nil || !strings.Contains(err.Error(), "expected owner") {
			t.Fatalf("loadSecretsForIdentity() error = %v, want owner rejection", err)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		directory := filepath.Join(root, "model-key-directory")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(
			SecretPaths{Bootstrap: paths.Bootstrap, ModelKey: directory},
			expectedEUID,
			expectedOwner,
		)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("loadSecretsForIdentity() error = %v, want type rejection", err)
		}
	})
}

func TestProtectedFileReadPinsOpenedDescriptorAgainstPathReplacement(t *testing.T) {
	root := realTempDir(t)
	path := filepath.Join(root, "model-key")
	if err := os.WriteFile(path, []byte("original-secret"), 0o400); err != nil {
		t.Fatal(err)
	}

	fd, err := openProtectedFile(path)
	if err != nil {
		t.Fatalf("openProtectedFile() error = %v", err)
	}
	defer unix.Close(fd)

	oldPath := filepath.Join(root, "model-key-old")
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement-secret"), 0o400); err != nil {
		t.Fatal(err)
	}

	content, err := readProtectedFD(
		"model key",
		fd,
		uint32(os.Geteuid()),
		maxModelKeyBytes,
	)
	if err != nil {
		t.Fatalf("readProtectedFD() error = %v", err)
	}
	if got, want := string(content), "original-secret"; got != want {
		t.Fatalf("readProtectedFD() = %q, want pinned %q", got, want)
	}
}

func TestLoadSecretsRejectsOversizeAndStrictBootstrapJSONViolations(t *testing.T) {
	expectedEUID := os.Geteuid()
	expectedOwner := uint32(expectedEUID)

	t.Run("oversize bootstrap", func(t *testing.T) {
		paths := writeSupervisorSecrets(t)
		if err := os.WriteFile(
			paths.Bootstrap,
			bytes.Repeat([]byte("x"), maxBootstrapBytes+1),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(paths, expectedEUID, expectedOwner)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("loadSecretsForIdentity() error = %v, want bootstrap size rejection", err)
		}
	})

	t.Run("oversize model key", func(t *testing.T) {
		paths := writeSupervisorSecrets(t)
		if err := os.Chmod(paths.ModelKey, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			paths.ModelKey,
			bytes.Repeat([]byte("x"), maxModelKeyBytes+1),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(paths.ModelKey, 0o400); err != nil {
			t.Fatal(err)
		}
		_, err := loadSecretsForIdentity(paths, expectedEUID, expectedOwner)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("loadSecretsForIdentity() error = %v, want model key size rejection", err)
		}
	})

	valid := `{
		"upstream_model":"openai/gpt-5.2-codex",
		"upstream_api_base":"https://model.example.test/v1",
		"proxy_token":"proxy-secret",
		"model_sink_token":"sink-secret",
		"agent_profile":"codex"
	}`
	for name, content := range map[string]string{
		"unknown field": strings.TrimSuffix(valid, "}") + `,"extra":"rejected"}`,
		"trailing JSON": valid + `{}`,
		"missing model": `{
			"upstream_api_base":"https://model.example.test/v1",
			"proxy_token":"proxy-secret",
			"model_sink_token":"sink-secret"
		}`,
		"missing API base": `{
			"upstream_model":"openai/gpt-5.2-codex",
			"proxy_token":"proxy-secret",
			"model_sink_token":"sink-secret"
		}`,
		"missing proxy token": `{
			"upstream_model":"openai/gpt-5.2-codex",
			"upstream_api_base":"https://model.example.test/v1",
			"model_sink_token":"sink-secret"
		}`,
		"missing sink token": `{
			"upstream_model":"openai/gpt-5.2-codex",
			"upstream_api_base":"https://model.example.test/v1",
			"proxy_token":"proxy-secret",
			"agent_profile":"codex"
		}`,
		"missing agent profile": `{
			"upstream_model":"openai/gpt-5.2-codex",
			"upstream_api_base":"https://model.example.test/v1",
			"proxy_token":"proxy-secret",
			"model_sink_token":"sink-secret"
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			paths := writeSupervisorSecrets(t)
			if err := os.WriteFile(paths.Bootstrap, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadSecretsForIdentity(paths, expectedEUID, expectedOwner); err == nil {
				t.Fatalf("loadSecretsForIdentity() accepted %s", name)
			}
		})
	}
}

func TestSupervisorExposesACPOnlyAfterSinkProcessAndReadiness(t *testing.T) {
	paths := writeSupervisorSecrets(t)
	var order []string
	sink := &fakeObservationSink{onStart: func() { order = append(order, "sink") }}
	child := newFakeManagedProcess()
	factory := &fakeProcessFactory{
		start: func(spec ChildSpec, stdout, stderr io.Writer) (ManagedProcess, error) {
			order = append(order, "litellm")
			if !sink.started {
				t.Fatal("LiteLLM started before observation sink")
			}
			_, _ = io.WriteString(stdout, "proxy-secret\n")
			_, _ = io.WriteString(stderr, "upstream-secret\n")
			return child, nil
		},
	}
	probe := &fakeReadinessProbe{
		check: func(context.Context) error {
			order = append(order, "readiness")
			if len(order) == 3 {
				return fmt.Errorf("not ready")
			}
			return nil
		},
	}
	clock := fakeClock{after: func(duration time.Duration) <-chan time.Time {
		if duration == readinessPollInterval {
			return firedTimer()
		}
		return make(chan time.Time)
	}}
	var diagnostics []Diagnostic
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: paths,
		NewSink: func(token string) (ObservationSink, error) {
			if token != "sink-secret" {
				t.Fatalf("sink token = %q, want protected bootstrap token", token)
			}
			return sink, nil
		},
		Processes: factory,
		Readiness: probe,
		Clock:     clock,
		Diagnostics: DiagnosticSinkFunc(func(diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}),
	})

	ready, err := supervisor.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !ready.ACPReady {
		t.Fatal("Start() did not expose ACP-ready after successful readiness")
	}
	if got, want := order, []string{"sink", "litellm", "readiness", "readiness"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("startup order = %#v, want %#v", got, want)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("successful startup diagnostics = %#v, want none", diagnostics)
	}
	if factory.spec.UID != 10001 || factory.spec.GID != 10001 {
		t.Fatalf("started child credentials = %d:%d, want 10001:10001", factory.spec.UID, factory.spec.GID)
	}

	child.closeOnSignal = syscall.SIGTERM
	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want %#v", got, want)
	}
	if !sink.stopped {
		t.Fatal("Shutdown() did not stop observation sink")
	}
}

func TestSupervisorConsumesOneDefensivePreloadedSecretsSnapshot(t *testing.T) {
	paths := writeSupervisorSecrets(t)
	snapshot := RuntimeSecrets{
		Bootstrap: Bootstrap{
			UpstreamModel:   "openai/gpt-5.2-codex",
			UpstreamAPIBase: "https://original.example.test/v1",
			ProxyToken:      "original-proxy-secret",
			ModelSinkToken:  "original-sink-secret",
		},
		UpstreamKey: "original-upstream-secret",
	}
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGTERM
	var sinkToken string
	factory := &fakeProcessFactory{
		start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
			return child, nil
		},
	}
	options := SupervisorOptions{
		SecretPaths:      paths,
		PreloadedSecrets: &snapshot,
		NewSink: func(token string) (ObservationSink, error) {
			sinkToken = token
			return &fakeObservationSink{}, nil
		},
		Processes: factory,
		Readiness: &fakeReadinessProbe{
			check: func(context.Context) error { return nil },
		},
	}
	for _, formatted := range []string{
		fmt.Sprintf("%+v", options),
		fmt.Sprintf("%#v", options),
	} {
		for _, secret := range []string{
			snapshot.Bootstrap.ProxyToken,
			snapshot.Bootstrap.ModelSinkToken,
			snapshot.UpstreamKey,
		} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("formatted SupervisorOptions leaked a preloaded secret: %s", formatted)
			}
		}
	}

	supervisor, err := NewSupervisor(options)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}
	loadCalls := 0
	supervisor.loadSecrets = func(SecretPaths) (RuntimeSecrets, error) {
		loadCalls++
		return RuntimeSecrets{
			Bootstrap: Bootstrap{
				UpstreamModel:   "openai/rotated",
				UpstreamAPIBase: "https://rotated.example.test/v1",
				ProxyToken:      "rotated-proxy-secret",
				ModelSinkToken:  "rotated-sink-secret",
			},
			UpstreamKey: "rotated-upstream-secret",
		}, nil
	}
	snapshot.Bootstrap.ProxyToken = "caller-mutated-proxy-secret"
	snapshot.Bootstrap.ModelSinkToken = "caller-mutated-sink-secret"
	snapshot.UpstreamKey = "caller-mutated-upstream-secret"

	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if loadCalls != 0 {
		t.Fatalf("secret loader calls = %d, want none for a preloaded snapshot", loadCalls)
	}
	if sinkToken != "original-sink-secret" {
		t.Fatalf("sink token = %q, want the immutable preloaded snapshot", sinkToken)
	}
	for _, want := range []string{
		"SWITCHBACK_SPIKE_PROXY_TOKEN=original-proxy-secret",
		"SWITCHBACK_SPIKE_UPSTREAM_API_KEY=original-upstream-secret",
	} {
		if !slices.Contains(factory.spec.Env, want) {
			t.Fatalf("LiteLLM environment = %q, want %q from preloaded snapshot", factory.spec.Env, want)
		}
	}
	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestSupervisorReadinessTimeoutPreventsACPAndReturnsRedactedBoundedDiagnostic(t *testing.T) {
	paths := writeSupervisorSecrets(t)
	sink := &fakeObservationSink{}
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGTERM
	factory := &fakeProcessFactory{
		start: func(_ ChildSpec, stdout, stderr io.Writer) (ManagedProcess, error) {
			_, _ = io.WriteString(stdout, strings.Repeat("x", diagnosticOutputLimit)+" proxy-secret sink-secret\n")
			_, _ = io.WriteString(stderr, "upstream-secret ambient-worker-token ambient-langfuse-secret\n")
			return child, nil
		},
	}
	probeStarted := make(chan struct{})
	probe := &fakeReadinessProbe{
		check: func(ctx context.Context) error {
			close(probeStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	timeout := make(chan time.Time, 1)
	clock := fakeClock{after: func(duration time.Duration) <-chan time.Time {
		switch duration {
		case readinessTimeout:
			return timeout
		default:
			return make(chan time.Time)
		}
	}}
	var diagnostics []Diagnostic
	t.Setenv("SWITCHBACK_WORKER_TOKEN", "ambient-worker-token")
	t.Setenv("LANGFUSE_SECRET_KEY", "ambient-langfuse-secret")
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: paths,
		NewSink: func(string) (ObservationSink, error) {
			return sink, nil
		},
		Processes: factory,
		Readiness: probe,
		Clock:     clock,
		Diagnostics: DiagnosticSinkFunc(func(diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}),
	})

	result := make(chan error, 1)
	go func() {
		ready, err := supervisor.Start(context.Background())
		if ready.ACPReady {
			result <- fmt.Errorf("ACP was exposed on readiness timeout")
			return
		}
		result <- err
	}()
	<-probeStarted
	timeout <- time.Now()
	if err := <-result; err == nil {
		t.Fatal("Start() error = nil, want structured readiness failure")
	}

	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failure shutdown signals = %#v, want %#v", got, want)
	}
	if !sink.stopped {
		t.Fatal("readiness failure left observation sink running")
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diagnostics)
	}
	diagnostic := diagnostics[0]
	if diagnostic.Code != "litellm_readiness_timeout" || diagnostic.Phase != "readiness" {
		t.Fatalf("diagnostic identity = %#v", diagnostic)
	}
	if len(diagnostic.Output) > diagnosticOutputLimit {
		t.Fatalf("diagnostic output bytes = %d, limit %d", len(diagnostic.Output), diagnosticOutputLimit)
	}
	for _, secret := range []string{
		"upstream-secret",
		"proxy-secret",
		"sink-secret",
		"ambient-worker-token",
		"ambient-langfuse-secret",
	} {
		if strings.Contains(diagnostic.Output, secret) || strings.Contains(fmt.Sprint(diagnostic), secret) {
			t.Fatalf("diagnostic leaked %q: %#v", secret, diagnostic)
		}
	}
	for _, marker := range []string{
		"[REDACTED:upstream_model_key]",
		"[REDACTED:litellm_proxy_token]",
		"[REDACTED:model_sink_token]",
		"[REDACTED:worker_token]",
		"[REDACTED:langfuse_secret_key]",
	} {
		if !strings.Contains(diagnostic.Output, marker) {
			t.Fatalf("diagnostic output %q does not contain %q", diagnostic.Output, marker)
		}
	}
}

func TestSupervisorShutdownEscalatesFromProcessGroupTERMToKILLAfterFiveSeconds(t *testing.T) {
	paths := writeSupervisorSecrets(t)
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGKILL
	clock := fakeClock{after: func(duration time.Duration) <-chan time.Time {
		switch duration {
		case readinessTimeout:
			return make(chan time.Time)
		case shutdownGracePeriod:
			return firedTimer()
		default:
			return make(chan time.Time)
		}
	}}
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: paths,
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
		Clock:     clock,
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want %#v", got, want)
	}
}

func TestSupervisorShutdownCancellationStillKillsTheProcessGroup(t *testing.T) {
	paths := writeSupervisorSecrets(t)
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGKILL
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: paths,
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
		Clock: fakeClock{after: func(time.Duration) <-chan time.Time {
			return make(chan time.Time)
		}},
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v, want bounded confirmed cleanup", err)
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want %#v", got, want)
	}
}

func TestSupervisorLeaderExitStillCleansSurvivingProcessGroup(t *testing.T) {
	child := newFakeManagedProcess()
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
		Clock: fakeClock{after: func(time.Duration) <-chan time.Time {
			return make(chan time.Time)
		}},
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	child.finishLeader(nil)
	child.closeOnSignal = syscall.SIGTERM

	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want descendant cleanup %#v", got, want)
	}
}

func TestWaitForProcessTerminationDisablesLeaderDoneWhilePollingDescendants(t *testing.T) {
	child := newFakeManagedProcess()
	child.finishLeader(nil)
	deadline := make(chan time.Time, 1)
	poll := make(chan time.Time)
	firstPoll := make(chan struct{})
	secondPoll := make(chan struct{})
	var pollCalls int
	supervisor := &Supervisor{
		options: SupervisorOptions{
			Clock: fakeClock{after: func(duration time.Duration) <-chan time.Time {
				switch duration {
				case shutdownGracePeriod:
					return deadline
				case processGroupPoll:
					pollCalls++
					if pollCalls == 1 {
						close(firstPoll)
					}
					if pollCalls == 2 {
						close(secondPoll)
					}
					return poll
				default:
					t.Fatalf("unexpected clock duration %s", duration)
					return nil
				}
			}},
		},
	}
	result := make(chan bool, 1)
	go func() {
		terminated, _ := supervisor.waitForProcessTermination(
			context.Background(),
			child,
			shutdownGracePeriod,
		)
		result <- terminated
	}()

	<-firstPoll
	busySpun := false
	select {
	case <-secondPoll:
		busySpun = true
	case <-time.After(100 * time.Millisecond):
	}
	deadline <- time.Now()
	if terminated := <-result; terminated {
		t.Fatal("waitForProcessTermination() reported live descendants terminated")
	}
	if busySpun {
		t.Fatalf("leader Done caused %d poll clock calls before any poll/deadline event", pollCalls)
	}
	if pollCalls != 1 {
		t.Fatalf("process-group poll clock calls = %d, want exactly one blocked poll", pollCalls)
	}
}

func TestSupervisorShutdownReturnsStructuredErrorWhenGroupTerminationCannotBeProven(t *testing.T) {
	child := newFakeManagedProcess()
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
		Clock: fakeClock{after: func(duration time.Duration) <-chan time.Time {
			if duration == shutdownGracePeriod {
				return firedTimer()
			}
			return make(chan time.Time)
		}},
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	first := supervisor.Shutdown(context.Background())
	second := supervisor.Shutdown(context.Background())
	var cleanup *CleanupError
	if !errors.As(first, &cleanup) || !cleanup.HasCode("process_group_not_terminated") {
		t.Fatalf("Shutdown() error = %v, want structured process_group_not_terminated", first)
	}
	if first != second {
		t.Fatalf("repeated Shutdown() errors differ: first=%p second=%p", first, second)
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want exactly %#v", got, want)
	}
}

func TestSupervisorConcurrentShutdownSharesOneTerminalResultWithoutDoubleSignal(t *testing.T) {
	child := newFakeManagedProcess()
	termObserved := make(chan struct{})
	child.onSignal = func(signal syscall.Signal) {
		if signal == syscall.SIGTERM {
			close(termObserved)
		}
	}
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
		Clock: fakeClock{after: func(time.Duration) <-chan time.Time {
			return make(chan time.Time)
		}},
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	results := make(chan error, 2)
	go func() { results <- supervisor.Shutdown(context.Background()) }()
	<-termObserved
	go func() { results <- supervisor.Shutdown(context.Background()) }()
	child.setGroupAlive(false)
	child.finishLeader(nil)

	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Shutdown() error = %v", err)
		}
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want one shared cleanup %#v", got, want)
	}
}

func TestSupervisorShutdownBeforeSinkAssignmentCancelsAndWaitsForStartup(t *testing.T) {
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	var sinkCreations int
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			sinkCreations++
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				t.Fatal("process started after shutdown began")
				return nil, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error {
			t.Fatal("readiness checked after shutdown began")
			return nil
		}},
	})
	supervisor.loadSecrets = func(SecretPaths) (RuntimeSecrets, error) {
		close(loadStarted)
		<-releaseLoad
		return RuntimeSecrets{
			Bootstrap: Bootstrap{
				UpstreamModel:   "openai/gpt-5.2-codex",
				UpstreamAPIBase: "https://model.example.test/v1",
				ProxyToken:      "proxy-secret",
				ModelSinkToken:  "sink-secret",
			},
			UpstreamKey: "upstream-secret",
		}, nil
	}

	startResult := make(chan error, 1)
	go func() {
		ready, err := supervisor.Start(context.Background())
		if ready.ACPReady {
			startResult <- fmt.Errorf("startup published ACP-ready after shutdown")
			return
		}
		startResult <- err
	}()
	<-loadStarted

	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- supervisor.Shutdown(context.Background())
	}()
	waitForSupervisorState(t, supervisor, lifecycleStopping)
	select {
	case err := <-shutdownResult:
		t.Fatalf("Shutdown() returned before in-flight startup stopped: %v", err)
	default:
	}

	close(releaseLoad)
	var startup *StartupError
	if err := <-startResult; !errors.As(err, &startup) ||
		startup.Diagnostic().Code != "startup_stopped" {
		t.Fatalf("Start() error = %v, want structured startup_stopped", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if sinkCreations != 0 {
		t.Fatalf("sink creations = %d, want none after stopping", sinkCreations)
	}
}

func TestSupervisorShutdownAfterSinkAssignmentPreventsSinkStartAndOwnsCleanup(t *testing.T) {
	sink := &fakeObservationSink{}
	sinkAssigned := make(chan struct{})
	releaseStartup := make(chan struct{})
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return sink, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				t.Fatal("process started after shutdown began")
				return nil, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error {
			t.Fatal("readiness checked after shutdown began")
			return nil
		}},
	})
	supervisor.beforeSinkStart = func() {
		close(sinkAssigned)
		<-releaseStartup
	}

	startResult := make(chan error, 1)
	go func() {
		_, err := supervisor.Start(context.Background())
		startResult <- err
	}()
	<-sinkAssigned

	shutdownResults := make(chan error, 2)
	go func() { shutdownResults <- supervisor.Shutdown(context.Background()) }()
	waitForSupervisorState(t, supervisor, lifecycleStopping)
	go func() { shutdownResults <- supervisor.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownResults:
		t.Fatalf("Shutdown() returned before assigned sink was collected: %v", err)
	default:
	}
	if sink.startCount() != 0 {
		t.Fatal("sink Start() ran after stopping began")
	}

	close(releaseStartup)
	var startup *StartupError
	if err := <-startResult; !errors.As(err, &startup) ||
		startup.Diagnostic().Code != "startup_stopped" {
		t.Fatalf("Start() error = %v, want structured startup_stopped", err)
	}
	for range 2 {
		if err := <-shutdownResults; err != nil {
			t.Fatalf("concurrent Shutdown() error = %v", err)
		}
	}
	if got := sink.shutdownCount(); got != 1 {
		t.Fatalf("sink Shutdown() calls = %d, want one cleanup owner", got)
	}
}

func TestSupervisorShutdownAfterChildStartPreventsReadyAndOwnsCleanup(t *testing.T) {
	sink := &fakeObservationSink{}
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGTERM
	probeStarted := make(chan struct{})
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return sink, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(ctx context.Context) error {
			close(probeStarted)
			<-ctx.Done()
			return ctx.Err()
		}},
	})

	startResult := make(chan struct {
		ready ReadyState
		err   error
	}, 1)
	go func() {
		ready, err := supervisor.Start(context.Background())
		startResult <- struct {
			ready ReadyState
			err   error
		}{ready: ready, err: err}
	}()
	<-probeStarted

	shutdownResults := make(chan error, 2)
	go func() { shutdownResults <- supervisor.Shutdown(context.Background()) }()
	waitForSupervisorState(t, supervisor, lifecycleStopping)
	go func() { shutdownResults <- supervisor.Shutdown(context.Background()) }()

	result := <-startResult
	if result.ready.ACPReady {
		t.Fatal("Start() published ACP-ready after stopping began")
	}
	var startup *StartupError
	if !errors.As(result.err, &startup) {
		t.Fatalf("Start() error = %v, want structured startup error", result.err)
	}
	for range 2 {
		if err := <-shutdownResults; err != nil {
			t.Fatalf("concurrent Shutdown() error = %v", err)
		}
	}
	if got, want := child.receivedSignals(), []syscall.Signal{syscall.SIGTERM}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown signals = %#v, want one cleanup %#v", got, want)
	}
	if got := sink.shutdownCount(); got != 1 {
		t.Fatalf("sink Shutdown() calls = %d, want one cleanup owner", got)
	}
	if err := supervisor.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown() error = %v", err)
	}
	if got := sink.shutdownCount(); got != 1 {
		t.Fatalf("repeated Shutdown() changed cleanup count to %d", got)
	}
}

func TestSupervisorRefusesToSignalZeroOrUnverifiedProcessGroup(t *testing.T) {
	child := newFakeManagedProcess()
	child.pgid = 0
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error { return nil }},
	})
	_, err := supervisor.Start(context.Background())
	var startup *StartupError
	if !errors.As(err, &startup) {
		t.Fatalf("Start() error = %v, want structured startup failure", err)
	}
	if got := child.receivedSignals(); len(got) != 0 {
		t.Fatalf("unverified group received signals %#v", got)
	}
	if got := startup.Diagnostic().Cleanup; len(got) != 1 ||
		got[0].Code != "process_group_unverified" {
		t.Fatalf("startup cleanup diagnostic = %#v", got)
	}
}

func TestSupervisorProbeSuccessRevalidatesSimultaneousChildExitBeforeACPReady(t *testing.T) {
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGTERM
	var diagnostics []Diagnostic
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error {
			child.finishLeader(nil)
			return nil
		}},
		Diagnostics: DiagnosticSinkFunc(func(diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}),
	})

	ready, err := supervisor.Start(context.Background())
	if err == nil || ready.ACPReady {
		t.Fatalf("Start() = %#v, %v; want simultaneous exit to block ACP-ready", ready, err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != "litellm_readiness_failed" {
		t.Fatalf("diagnostics = %#v, want deterministic readiness failure", diagnostics)
	}
}

func TestSupervisorRetainsReadinessCleanupFailureDiagnostic(t *testing.T) {
	child := newFakeManagedProcess()
	var diagnostics []Diagnostic
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return &fakeObservationSink{}, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error {
			return fmt.Errorf("not ready")
		}},
		Clock: fakeClock{after: func(duration time.Duration) <-chan time.Time {
			switch duration {
			case readinessTimeout, shutdownGracePeriod:
				return firedTimer()
			default:
				return make(chan time.Time)
			}
		}},
		Diagnostics: DiagnosticSinkFunc(func(diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}),
	})

	if _, err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want readiness cleanup failure")
	}
	if len(diagnostics) != 1 || len(diagnostics[0].Cleanup) != 1 ||
		diagnostics[0].Cleanup[0].Code != "process_group_not_terminated" {
		t.Fatalf("readiness diagnostic cleanup = %#v", diagnostics)
	}
}

func TestCaptureVerifiedProcessGroupRejectsZeroMismatchAndLookupFailure(t *testing.T) {
	tests := []struct {
		name   string
		lookup func(int) (int, error)
	}{
		{name: "zero", lookup: func(int) (int, error) { return 0, nil }},
		{name: "mismatch", lookup: func(int) (int, error) { return 42, nil }},
		{name: "lookup failure", lookup: func(int) (int, error) { return 0, syscall.ESRCH }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if pgid, err := captureVerifiedProcessGroup(41, test.lookup); err == nil || pgid != 0 {
				t.Fatalf("captureVerifiedProcessGroup() = %d, %v; want fail closed", pgid, err)
			}
		})
	}
	if pgid, err := captureVerifiedProcessGroup(
		41,
		func(int) (int, error) { return 41, nil },
	); err != nil || pgid != 41 {
		t.Fatalf("captureVerifiedProcessGroup() = %d, %v; want verified PGID 41", pgid, err)
	}
}

func TestStartManagedCommandCapturesProcessGroupBeforeStartingWait(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 0")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	waitStarted := make(chan struct{})
	result := make(chan struct {
		process ManagedProcess
		err     error
	}, 1)

	go func() {
		process, err := startManagedCommandWithWait(
			command,
			func(pid int) (int, error) {
				close(lookupStarted)
				<-releaseLookup
				return pid, nil
			},
			func() error {
				close(waitStarted)
				return command.Wait()
			},
		)
		result <- struct {
			process ManagedProcess
			err     error
		}{process: process, err: err}
	}()

	<-lookupStarted
	select {
	case <-waitStarted:
		t.Fatal("command Wait started before exact PGID capture completed")
	default:
	}
	close(releaseLookup)
	started := <-result
	if started.err != nil {
		t.Fatalf("startManagedCommandWithWait() error = %v", started.err)
	}
	select {
	case <-started.process.Done():
	case <-time.After(time.Second):
		t.Fatal("fast-exit leader was not reaped after PGID capture")
	}
	if got := started.process.ProcessGroupID(); got != command.Process.Pid {
		t.Fatalf("captured PGID = %d, want exact leader PID %d", got, command.Process.Pid)
	}
}

func TestStartManagedCommandVerificationFailureCleansContainedDescendantsAndDrainsWait(t *testing.T) {
	root := realTempDir(t)
	childPIDPath := filepath.Join(root, "child-pid")
	command := exec.Command("/bin/sh", "-c", `
		sh -c 'trap "" HUP TERM; while :; do sleep 1; done' &
		printf '%s' "$!" > "$SWITCHBACK_TEST_CHILD_PID"
		while :; do sleep 1; done
	`)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"SWITCHBACK_TEST_CHILD_PID=" + childPIDPath,
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var leaderPID int
	var descendantPID int
	process, err := startManagedCommand(command, func(pid int) (int, error) {
		leaderPID = pid
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			content, readErr := os.ReadFile(childPIDPath)
			if readErr == nil {
				descendantPID, readErr = strconv.Atoi(string(content))
				if readErr != nil {
					return 0, readErr
				}
				pgid, lookupErr := unix.Getpgid(descendantPID)
				if lookupErr == nil && pgid == pid {
					return 0, syscall.EPERM
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		return 0, syscall.ETIMEDOUT
	})
	t.Cleanup(func() {
		if leaderPID > 0 {
			_ = unix.Kill(-leaderPID, syscall.SIGKILL)
		}
		if descendantPID > 0 {
			_ = unix.Kill(descendantPID, syscall.SIGKILL)
		}
	})
	if err == nil {
		t.Fatal("startManagedCommand() error = nil, want PGID verification failure")
	}
	if process != nil {
		t.Fatalf("startManagedCommand() process = %#v, want no PGID-zero handle", process)
	}
	if command.ProcessState == nil {
		t.Fatal("verification failure returned before command Wait/drain completed")
	}
	if descendantPID <= 0 {
		t.Fatal("verification seam did not observe the descendant")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(unix.Kill(descendantPID, 0), unix.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("verification failure left descendant PID %d alive", descendantPID)
}

func TestOSManagedProcessCleansSurvivingGroupAfterLeaderExit(t *testing.T) {
	root := realTempDir(t)
	childPIDPath := filepath.Join(root, "child-pid")
	releasePath := filepath.Join(root, "release")
	command := exec.Command("/bin/sh", "-c", `
		sh -c 'trap "" HUP; trap "exit 0" TERM; while :; do sleep 1; done' &
		printf '%s' "$!" > "$SWITCHBACK_TEST_CHILD_PID"
		while [ ! -e "$SWITCHBACK_TEST_RELEASE" ]; do sleep 0.01; done
		exit 0
	`)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"SWITCHBACK_TEST_CHILD_PID=" + childPIDPath,
		"SWITCHBACK_TEST_RELEASE=" + releasePath,
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	child, err := startManagedCommand(command, unix.Getpgid)
	if err != nil {
		t.Fatalf("startManagedCommand() error = %v", err)
	}
	t.Cleanup(func() {
		_ = child.SignalGroup(syscall.SIGKILL)
	})

	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		content, readErr := os.ReadFile(childPIDPath)
		if readErr == nil {
			childPID, err = strconv.Atoi(string(content))
			if err != nil {
				t.Fatalf("parse descendant PID: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("descendant PID was not published")
	}
	if pgid, err := unix.Getpgid(childPID); err != nil || pgid != child.ProcessGroupID() {
		t.Fatalf(
			"descendant PGID = %d, %v; want verified group %d",
			pgid,
			err,
			child.ProcessGroupID(),
		)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaderGone := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(unix.Kill(child.ProcessGroupID(), 0), unix.ESRCH) {
			leaderGone = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !leaderGone {
		t.Fatal("leader PID still exists after release")
	}
	if alive, err := child.GroupAlive(); err != nil || !alive {
		t.Fatalf("surviving descendant group alive = %t, %v; want true", alive, err)
	}

	supervisor := &Supervisor{
		options: SupervisorOptions{Clock: realClock{}},
	}
	if failure := supervisor.stopProcess(context.Background(), child); failure != nil {
		t.Fatalf("stopProcess() failure = %#v", failure)
	}
	if alive, err := child.GroupAlive(); err != nil || alive {
		t.Fatalf("process group alive after cleanup = %t, %v; want proven gone", alive, err)
	}
	select {
	case <-child.Done():
	default:
		t.Fatal("leader wait/output drain result was not complete after group cleanup")
	}
}

func TestSupervisorProbeSuccessRevalidatesSinkHealthBeforeACPReady(t *testing.T) {
	child := newFakeManagedProcess()
	child.closeOnSignal = syscall.SIGTERM
	sink := &fakeObservationSink{}
	supervisor := newTestSupervisor(t, SupervisorOptions{
		SecretPaths: writeSupervisorSecrets(t),
		NewSink: func(string) (ObservationSink, error) {
			return sink, nil
		},
		Processes: &fakeProcessFactory{
			start: func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error) {
				return child, nil
			},
		},
		Readiness: &fakeReadinessProbe{check: func(context.Context) error {
			sink.mu.Lock()
			sink.healthErr = fmt.Errorf("serve stopped")
			sink.mu.Unlock()
			return nil
		}},
	})

	ready, err := supervisor.Start(context.Background())
	if err == nil || ready.ACPReady {
		t.Fatalf("Start() = %#v, %v; want unhealthy sink to block ACP-ready", ready, err)
	}
}

func TestOSProcessFactoryBuildsSeparateCredentialAndProcessGroupWithoutInheritingEnvironment(t *testing.T) {
	t.Setenv("SWITCHBACK_WORKER_TOKEN", "must-not-be-inherited")
	spec := LiteLLMChildSpec(Bootstrap{
		UpstreamModel:   "openai/gpt-5.2-codex",
		UpstreamAPIBase: "https://model.example.test/v1",
		ProxyToken:      "proxy-secret",
		ModelSinkToken:  "sink-secret",
	}, "upstream-secret")
	command, err := buildCommand(spec, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	if !reflect.DeepEqual(command.Args, spec.Args) {
		t.Fatalf("command argv = %#v, want %#v", command.Args, spec.Args)
	}
	if !reflect.DeepEqual(command.Env, spec.Env) {
		t.Fatalf("command environment = %#v, want exact filtered %#v", command.Env, spec.Env)
	}
	if strings.Contains(strings.Join(command.Env, "\n"), "must-not-be-inherited") {
		t.Fatal("command environment inherited Worker-control token")
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("command SysProcAttr = %#v, want own process group", command.SysProcAttr)
	}
	credential := command.SysProcAttr.Credential
	if credential == nil || credential.Uid != 10001 || credential.Gid != 10001 {
		t.Fatalf("command credential = %#v, want 10001:10001", credential)
	}
	if credential.Groups == nil || len(credential.Groups) != 0 {
		t.Fatalf("command supplementary groups = %#v, want explicit empty list", credential.Groups)
	}
}

func TestDiagnosticWriterRedactsTokensSplitAcrossWrites(t *testing.T) {
	known := redact.Secrets{
		UpstreamModelKey: "upstream-secret",
		ModelSinkToken:   "sink-secret",
	}
	buffer := newDiagnosticBuffer(known, []string{known.UpstreamModelKey, known.ModelSinkToken})
	for _, fragment := range []string{
		"before upstream-",
		"secret middle sink-",
		"secret after",
	} {
		if _, err := io.WriteString(buffer, fragment); err != nil {
			t.Fatal(err)
		}
	}
	got := buffer.Finalize()
	for _, secret := range []string{known.UpstreamModelKey, known.ModelSinkToken} {
		if strings.Contains(got, secret) {
			t.Fatalf("split-token output leaked %q: %q", secret, got)
		}
	}
	for _, marker := range []string{
		"[REDACTED:upstream_model_key]",
		"[REDACTED:model_sink_token]",
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("redacted output %q does not contain %q", got, marker)
		}
	}
}

func TestDiagnosticWriterFinalizeMasksResidualPossibleSecretPrefix(t *testing.T) {
	known := redact.Secrets{UpstreamModelKey: "upstream-secret"}
	buffer := newDiagnosticBuffer(known, []string{known.UpstreamModelKey})
	if _, err := io.WriteString(buffer, "safe output upstream-"); err != nil {
		t.Fatal(err)
	}

	snapshot := buffer.Snapshot()
	if strings.Contains(snapshot, "upstream-") {
		t.Fatalf("Snapshot() leaked pending possible secret prefix: %q", snapshot)
	}
	final := buffer.Finalize()
	if strings.Contains(final, "upstream-") {
		t.Fatalf("Finalize() leaked residual possible secret prefix: %q", final)
	}
	if !strings.Contains(final, "[REDACTED:possible_secret_prefix]") {
		t.Fatalf("Finalize() = %q, want conservative residual marker", final)
	}
	if again := buffer.Finalize(); again != final {
		t.Fatalf("repeated Finalize() = %q, want stable %q", again, final)
	}
}

func TestDiagnosticWriterSnapshotDoesNotMutateCrossWriteState(t *testing.T) {
	known := redact.Secrets{UpstreamModelKey: "secret-token"}
	buffer := newDiagnosticBuffer(known, []string{known.UpstreamModelKey})
	if _, err := io.WriteString(buffer, "benign se"); err != nil {
		t.Fatal(err)
	}
	snapshot := buffer.Snapshot()
	if strings.Contains(snapshot, " se") {
		t.Fatalf("Snapshot() exposed pending prefix: %q", snapshot)
	}
	if _, err := io.WriteString(buffer, "parate output"); err != nil {
		t.Fatal(err)
	}
	if got, want := buffer.Finalize(), "benign separate output"; got != want {
		t.Fatalf("Finalize() after Snapshot() = %q, want non-mutating %q", got, want)
	}
}

func TestDiagnosticWriterHandlesOverlappingSecretPrefixesAcrossWrites(t *testing.T) {
	known := redact.Secrets{
		UpstreamModelKey: "secret",
		ModelSinkToken:   "secret-extended",
	}
	buffer := newDiagnosticBuffer(
		known,
		[]string{known.UpstreamModelKey, known.ModelSinkToken},
	)
	if _, err := io.WriteString(buffer, "secret-"); err != nil {
		t.Fatal(err)
	}
	if snapshot := buffer.Snapshot(); strings.Contains(snapshot, "secret-") {
		t.Fatalf("Snapshot() leaked overlapping secret prefix: %q", snapshot)
	}
	if _, err := io.WriteString(buffer, "extended then secret"); err != nil {
		t.Fatal(err)
	}
	got := buffer.Finalize()
	if strings.Contains(got, "secret") {
		t.Fatalf("Finalize() leaked overlapping token: %q", got)
	}
	for _, marker := range []string{
		"[REDACTED:model_sink_token]",
		"[REDACTED:upstream_model_key]",
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("Finalize() = %q, want %q", got, marker)
		}
	}
}

func TestDiagnosticWriterPreservesBenignOutput(t *testing.T) {
	known := redact.Secrets{UpstreamModelKey: "secret-token"}
	buffer := newDiagnosticBuffer(known, []string{known.UpstreamModelKey})
	const benign = "ordinary startup output\nhealthy route\n"
	if _, err := io.WriteString(buffer, benign); err != nil {
		t.Fatal(err)
	}
	if got := buffer.Finalize(); got != benign {
		t.Fatalf("Finalize() = %q, want benign output unchanged", got)
	}
}

func writeSupervisorSecrets(t *testing.T) SecretPaths {
	t.Helper()
	return writeSecretsAt(t, realTempDir(t))
}

func writeSecretsAt(t *testing.T, root string) SecretPaths {
	t.Helper()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRoot
	bootstrapPath := filepath.Join(root, "bootstrap.json")
	modelKeyPath := filepath.Join(root, "model-key")
	if err := os.WriteFile(bootstrapPath, []byte(`{
		"upstream_model":"openai/gpt-5.2-codex",
		"upstream_api_base":"https://model.example.test/v1",
		"proxy_token":"proxy-secret",
		"model_sink_token":"sink-secret",
		"agent_profile":"codex"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelKeyPath, []byte("upstream-secret\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return SecretPaths{Bootstrap: bootstrapPath, ModelKey: modelKeyPath}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestSupervisor(t *testing.T, options SupervisorOptions) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(options)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}
	supervisor.loadSecrets = func(paths SecretPaths) (RuntimeSecrets, error) {
		return loadSecretsForIdentity(paths, os.Geteuid(), uint32(os.Geteuid()))
	}
	return supervisor
}

type fakeObservationSink struct {
	mu        sync.Mutex
	started   bool
	stopped   bool
	healthErr error
	onStart   func()
	starts    int
	shutdowns int
}

func (s *fakeObservationSink) Start() error {
	s.mu.Lock()
	s.started = true
	s.starts++
	s.mu.Unlock()
	if s.onStart != nil {
		s.onStart()
	}
	return nil
}

func (s *fakeObservationSink) Shutdown(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.shutdowns++
	return nil
}

func (s *fakeObservationSink) Healthy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthErr
}

func (s *fakeObservationSink) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func (s *fakeObservationSink) shutdownCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdowns
}

type fakeProcessFactory struct {
	spec  ChildSpec
	start func(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error)
}

func (f *fakeProcessFactory) Start(spec ChildSpec, stdout, stderr io.Writer) (ManagedProcess, error) {
	f.spec = spec
	return f.start(spec, stdout, stderr)
}

type fakeManagedProcess struct {
	mu            sync.Mutex
	done          chan struct{}
	waitErr       error
	pgid          int
	groupAlive    bool
	groupCheckErr error
	signals       []syscall.Signal
	closeOnSignal syscall.Signal
	onSignal      func(syscall.Signal)
	finishOnce    sync.Once
}

func newFakeManagedProcess() *fakeManagedProcess {
	return &fakeManagedProcess{
		done:       make(chan struct{}),
		pgid:       4242,
		groupAlive: true,
	}
}

func (p *fakeManagedProcess) Done() <-chan struct{} {
	return p.done
}

func (p *fakeManagedProcess) LifecycleResult() ProcessLifecycleResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProcessLifecycleResult{LeaderErr: p.waitErr}
}

func (p *fakeManagedProcess) ProcessGroupID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pgid
}

func (p *fakeManagedProcess) GroupAlive() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.groupAlive, p.groupCheckErr
}

func (p *fakeManagedProcess) SignalGroup(signal syscall.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, signal)
	onSignal := p.onSignal
	closeGroup := signal == p.closeOnSignal
	if closeGroup {
		p.groupAlive = false
	}
	p.mu.Unlock()
	if onSignal != nil {
		onSignal(signal)
	}
	if closeGroup {
		p.finishLeader(nil)
	}
	return nil
}

func (p *fakeManagedProcess) finishLeader(err error) {
	p.finishOnce.Do(func() {
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.done)
	})
}

func (p *fakeManagedProcess) setGroupAlive(alive bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.groupAlive = alive
}

func (p *fakeManagedProcess) receivedSignals() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]syscall.Signal(nil), p.signals...)
}

type fakeReadinessProbe struct {
	check func(context.Context) error
}

func (p *fakeReadinessProbe) Check(ctx context.Context) error {
	return p.check(ctx)
}

type fakeClock struct {
	after func(time.Duration) <-chan time.Time
}

func (c fakeClock) After(duration time.Duration) <-chan time.Time {
	return c.after(duration)
}

func firedTimer() <-chan time.Time {
	timer := make(chan time.Time, 1)
	timer <- time.Now()
	return timer
}

func waitForSupervisorState(t *testing.T, supervisor *Supervisor, want lifecycleState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		supervisor.mu.Lock()
		got := supervisor.state
		supervisor.mu.Unlock()
		if got == want || want == lifecycleStopping && got == lifecycleStopped {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("supervisor did not reach lifecycle state %v", want)
}
