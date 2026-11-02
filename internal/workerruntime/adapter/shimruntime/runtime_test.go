package shimruntime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/modelobs"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTerminalDrainDoesNotWaitForFutureBoundModelCallback(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	stream := &recordingControlStream{}
	observations := make(chan modelobs.Observation, 1)
	binder := modelobs.NewRequestBinder()
	turn := modelobs.Correlation{TraceID: "trace-one", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1"}
	if err := binder.Activate(turn); err != nil {
		t.Fatal(err)
	}
	if err := binder.Bind("late-request"); err != nil {
		t.Fatal(err)
	}
	// The production terminal drain must return with an open, empty callback
	// channel. Completion is synchronized directly, with no timing delay.
	done := make(chan error, 1)
	go func() { done <- drainModelObservations(stream, bootstrap, "agent-session-1", observations) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal drain waited for future callback")
	}
	if len(stream.events) != 0 {
		t.Fatal("drain fabricated a model fact")
	}
	if err := binder.Deactivate(turn); err != nil {
		t.Fatal(err)
	}
	next := turn
	next.AgentTurnID = "turn-2"
	if err := binder.Activate(next); err != nil {
		t.Fatal(err)
	}
	c, err := binder.Take("late-request")
	if err != nil {
		t.Fatal(err)
	}
	observations <- modelobs.Observation{Correlation: c, Model: "fixture-model", Route: "fixture", StartedAt: time.Now(), EndedAt: time.Now(), Input: json.RawMessage(`{"input":"old"}`), Output: json.RawMessage(`{"output":"old"}`)}
	if err := drainModelObservations(stream, bootstrap, "agent-session-1", observations); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].Envelope.AgentTurnID != "turn-1" || stream.events[0].Envelope.WorkerRunID != "run-1" {
		t.Fatalf("late callback rebound: %+v", stream.events)
	}
}

func TestSendModelObservationUsesTrustedRuntimeAndTurnIdentity(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	stream := &recordingControlStream{}
	observation := modelobs.Observation{
		Model: "deepseek-chat", Route: "switchback-codex",
		StartedAt: time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 7, 24, 1, 0, 1, 0, time.UTC),
		Usage:     modelobs.Usage{InputTokens: 3, OutputTokens: 5},
		Input:     json.RawMessage(`{"messages":[{"role":"user","content":"complete-model-input"}]}`),
		Output:    json.RawMessage(`{"choices":[{"message":{"content":"complete-model-output"}}]}`),
		Correlation: modelobs.Correlation{
			RequestID: "call-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
			AgentTurnID: "turn-1", TraceID: "langfuse-turn-1",
		},
	}
	if err := sendModelObservation(stream, bootstrap, "agent-session-1", observation); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].Envelope.MessageType != "model.observation" ||
		stream.events[0].Envelope.RuntimeInstanceID != "runtime-1" ||
		stream.events[0].Envelope.AgentTurnID != "turn-1" {
		t.Fatalf("model observation event=%+v", stream.events)
	}
	var payload application.ModelObservation
	if err := json.Unmarshal(stream.events[0].Envelope.Payload, &payload); err != nil ||
		payload.ObservationID != "call-1" || payload.WorkerSessionID != "session-1" || payload.Status != "ok" ||
		string(payload.Input) != string(observation.Input) || string(payload.Output) != string(observation.Output) {
		t.Fatalf("model observation payload=%+v error=%v", payload, err)
	}
}

func TestModelProxyCommandContainsOneSelectedRouteAndNoAmbientEnvironment(t *testing.T) {
	t.Setenv("AMBIENT_MUST_NOT_REACH_CHILD", "ambient-secret")
	profile, _ := config.ProductionProfileRegistry().Agent(config.KimiCodexAnthropicV1)
	bootstrap := testBootstrap(profile)
	command, err := modelProxyCommand(bootstrap, runtimesecret.ProviderCredential("provider-key"), io.Discard)
	if err != nil {
		t.Fatalf("build model proxy command: %v", err)
	}
	joined := strings.Join(command.Env, "\n")
	for _, expected := range []string{
		"SWITCHBACK_UPSTREAM_MODEL=" + profile.LiteLLMModel,
		"SWITCHBACK_UPSTREAM_API_BASE=" + profile.APIBase,
		"SWITCHBACK_UPSTREAM_API_KEY=provider-key",
		"SWITCHBACK_PROXY_TOKEN=proxy-token",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("LiteLLM environment does not contain %q", expected)
		}
	}
	if strings.Contains(joined, "AMBIENT_MUST_NOT_REACH_CHILD") || strings.Contains(joined, "deepseek") {
		t.Fatalf("LiteLLM environment contains ambient or fallback route: %s", joined)
	}
	if command.SysProcAttr == nil || command.SysProcAttr.Credential == nil ||
		command.SysProcAttr.Credential.Uid != runtimesecret.LiteLLMUID {
		t.Fatalf("LiteLLM child credential = %+v", command.SysProcAttr)
	}
}

func TestDecodeStartCommandRequiresExactAgentSessionAndStrictBoundedPayload(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	command := testStartCommand(bootstrap, "agent-session-1", json.RawMessage(`{"prompt":"write the file"}`))
	prompt, err := decodeStartCommand(command, bootstrap, "agent-session-1")
	if err != nil || prompt != "write the file" {
		t.Fatalf("decode exact start command = %q, %v", prompt, err)
	}
	for _, mutate := range []func(*application.WorkerControlCommand){
		func(command *application.WorkerControlCommand) { command.Envelope.AgentSessionID = "other-session" },
		func(command *application.WorkerControlCommand) {
			command.Payload = json.RawMessage(`{"prompt":"ok","fallback":"kimi"}`)
		},
		func(command *application.WorkerControlCommand) { command.Payload = json.RawMessage(`{"prompt":""}`) },
	} {
		invalid := command
		mutate(&invalid)
		if _, err := decodeStartCommand(invalid, bootstrap, "agent-session-1"); err == nil {
			t.Fatal("invalid AgentTurn start command was accepted")
		}
	}
}

func TestBackendEndpointsRejectSchemesCredentialsAndMissingPorts(t *testing.T) {
	for _, invalid := range [][2]string{
		{"http://backend:9090", "http://backend:8080"},
		{"backend", "http://backend:8080"},
		{"backend:9090", "http://user:pass@backend:8080"},
		{"backend:9090", "ftp://backend:8080"},
	} {
		if err := validateBackendEndpoints(invalid[0], invalid[1]); err == nil {
			t.Fatalf("invalid Backend endpoints were accepted: %q %q", invalid[0], invalid[1])
		}
	}
	if err := validateBackendEndpoints("backend:9090", "http://backend:8080"); err != nil {
		t.Fatalf("valid Backend endpoints rejected: %v", err)
	}
}

func TestPrepareAgentDirectoriesCreatesProtectedUID10002Roots(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "agent-state", "codex")}
	var prepared []string
	if err := prepareAgentDirectories(paths, func(path string, uid, gid int) error {
		if uid != runtimesecret.AgentUID || gid != runtimesecret.AgentUID {
			t.Fatalf("Agent directory owner = %d:%d", uid, gid)
		}
		prepared = append(prepared, path)
		return os.MkdirAll(path, 0o700)
	}); err != nil {
		t.Fatalf("prepare Agent directories: %v", err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("Agent directory %q = %+v, %v", path, info, err)
		}
	}
	if len(prepared) != len(paths) {
		t.Fatalf("prepared directories = %v", prepared)
	}
}

func TestPrepareAgentDirectoriesRejectsExistingSymlinkBeforePreparation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	path := filepath.Join(root, "codex")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	called := false
	err := prepareAgentDirectories([]string{path}, func(string, int, int) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("symlink preparation err=%v called=%t", err, called)
	}
}

func testBootstrap(profile config.AgentProfile) runtimesecret.Bootstrap {
	return runtimesecret.Bootstrap{
		SchemaVersion: "switchback.runtime-bootstrap.v1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
		WorkerSessionID: "session-1", WorkerRunID: "run-1", ChannelEpoch: 7,
		WorkerControlToken: "control-token", AgentProfileID: profile.ID, AgentProfileRevision: profile.Revision,
		AgentProfileDigest: profile.Digest, LiteLLMModel: profile.LiteLLMModel, UpstreamAPIBase: profile.APIBase,
		UpstreamProtocol: profile.UpstreamProtocol, ProxyToken: "proxy-token", ModelSinkToken: "sink-token",
		LiteLLMUID: runtimesecret.LiteLLMUID, AgentUID: runtimesecret.AgentUID, ModelSecretPath: "/run/switchback-secrets/model",
	}
}

func testStartCommand(bootstrap runtimesecret.Bootstrap, agentSessionID string, payload json.RawMessage) application.WorkerControlCommand {
	return application.WorkerControlCommand{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: "command-1", Kind: "command",
		WorkerSessionID: contractsv1.WorkerSessionID(bootstrap.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(bootstrap.WorkerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(bootstrap.RuntimeInstanceID), RuntimeGeneration: uint32(bootstrap.RuntimeGeneration),
		AgentSessionID: contractsv1.AgentSessionID(agentSessionID), AgentTurnID: "turn-1", ChannelEpoch: bootstrap.ChannelEpoch,
		Sequence: 1, IdempotencyKey: "command-1", MessageType: "agent_turn.start", Payload: payload, SentAt: time.Now().UTC(),
	}, Payload: payload}
}

type recordingControlStream struct {
	events []application.WorkerControlEvent
}

func (stream *recordingControlStream) SendEvent(event application.WorkerControlEvent) error {
	stream.events = append(stream.events, event)
	return nil
}

func (*recordingControlStream) ReceiveCommand() (application.WorkerControlCommand, error) {
	return application.WorkerControlCommand{}, context.Canceled
}

func (*recordingControlStream) CloseSend() error { return nil }
