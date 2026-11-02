package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/internal/redact"
	"switchback.local/spikes/worker-runtime/worker/internal/acp"
	workercontrol "switchback.local/spikes/worker-runtime/worker/internal/control"
)

func TestProductionSecretPathsUseTheReadOnlyRuntimeVolumeDirectory(t *testing.T) {
	if got, want := bootstrapPath, "/run/switchback-secrets/bootstrap.json"; got != want {
		t.Fatalf("bootstrapPath = %q, want %q", got, want)
	}
	if got, want := modelKeyPath, "/run/switchback-secrets/model"; got != want {
		t.Fatalf("modelKeyPath = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"/run/switchback/bootstrap.json", "/run/secrets/switchback/model"} {
		if bootstrapPath == forbidden || modelKeyPath == forbidden {
			t.Fatalf("production secret path retained legacy host-bind target %q", forbidden)
		}
	}
}

func TestLoadRuntimeConfigRequiresAndValidatesServerOwnedEnvironment(t *testing.T) {
	valid := map[string]string{
		"SWITCHBACK_WORKER_SESSION_ID":     "session-1",
		"SWITCHBACK_WORKER_RUN_ID":         "run-1",
		"SWITCHBACK_RUNTIME_INSTANCE_ID":   "runtime-1",
		"SWITCHBACK_RUNTIME_GENERATION":    "1",
		"SWITCHBACK_WORKER_TOKEN":          "worker-token",
		"SWITCHBACK_BACKEND_GRPC_TARGET":   "switchback-backend:8081",
		"SWITCHBACK_BACKEND_HTTP_BASE_URL": "http://switchback-backend:8080",
	}
	lookup := func(key string) (string, bool) {
		value, ok := valid[key]
		return value, ok
	}
	config, err := loadRuntimeConfig(lookup)
	if err != nil {
		t.Fatalf("loadRuntimeConfig() error = %v", err)
	}
	if config.Identity.WorkerSessionID != "session-1" ||
		config.Identity.WorkerRunID != "run-1" ||
		config.Identity.RuntimeInstanceID != "runtime-1" ||
		config.Identity.RuntimeGeneration != 1 ||
		config.WorkerToken != "worker-token" ||
		config.BackendGRPCTarget != "switchback-backend:8081" ||
		config.BackendHTTPBaseURL != "http://switchback-backend:8080" {
		t.Fatalf("runtime config = %+v", config)
	}
	for _, formatted := range []string{
		fmt.Sprintf("%v", config),
		fmt.Sprintf("%+v", config),
		fmt.Sprintf("%#v", config),
	} {
		if strings.Contains(formatted, "worker-token") {
			t.Fatalf("runtime config formatting exposed Worker token: %s", formatted)
		}
	}

	for key := range valid {
		t.Run("missing "+key, func(t *testing.T) {
			missing := func(candidate string) (string, bool) {
				if candidate == key {
					return "", false
				}
				value, ok := valid[candidate]
				return value, ok
			}
			if _, err := loadRuntimeConfig(missing); err == nil ||
				!strings.Contains(err.Error(), key) {
				t.Fatalf("loadRuntimeConfig() error = %v, want missing %s", err, key)
			}
		})
	}

	invalid := map[string]string{
		"SWITCHBACK_WORKER_SESSION_ID":     "../session",
		"SWITCHBACK_RUNTIME_GENERATION":    "2",
		"SWITCHBACK_WORKER_TOKEN":          "token with spaces",
		"SWITCHBACK_BACKEND_GRPC_TARGET":   "http://switchback-backend:8081",
		"SWITCHBACK_BACKEND_HTTP_BASE_URL": "switchback-backend:8080",
	}
	for key, value := range invalid {
		t.Run("invalid "+key, func(t *testing.T) {
			replaced := func(candidate string) (string, bool) {
				if candidate == key {
					return value, true
				}
				current, ok := valid[candidate]
				return current, ok
			}
			if _, err := loadRuntimeConfig(replaced); err == nil ||
				!strings.Contains(err.Error(), key) {
				t.Fatalf("loadRuntimeConfig() error = %v, want invalid %s", err, key)
			}
		})
	}
}

func TestProductionRunFailsBeforeStartupWhenServerConfigIsMissing(t *testing.T) {
	for _, key := range []string{
		envWorkerSessionID,
		envWorkerRunID,
		envRuntimeInstance,
		envRuntimeGeneration,
		envWorkerToken,
		envBackendGRPC,
		envBackendHTTP,
	} {
		t.Setenv(key, "")
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), envWorkerSessionID) {
		t.Fatalf("run() error = %v, want startup config rejection", err)
	}
}

func TestProductionRunDependenciesBoundEvidenceHTTP(t *testing.T) {
	dependencies := productionRunDependencies()
	if dependencies.HTTPClient == nil || dependencies.HTTPClient.Timeout <= 0 {
		t.Fatalf(
			"production evidence HTTP client = %+v, want a positive timeout",
			dependencies.HTTPClient,
		)
	}
}

func TestRunWithConfigInvokesProductionCommandLoop(t *testing.T) {
	workingCopy := mainTestGitWorkingCopy(t)
	agentState := t.TempDir()
	traceSpool := t.TempDir()
	traceFile, err := os.OpenFile(filepath.Join(traceSpool, "acp.jsonl"), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = traceFile.Close() })
	agent := &mainFakeAgent{workingCopy: workingCopy, trace: traceFile}
	var runtimeStarted atomic.Int32
	var runtimeStopped atomic.Int32

	uploadServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		kind := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"kind":   kind,
			"path":   request.URL.Path,
			"sha256": strings.Repeat("a", 64),
			"bytes":  request.ContentLength,
		})
	}))
	defer uploadServer.Close()

	identity := workercontrol.RuntimeIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	workercontrolv1.RegisterWorkerControlServer(grpcServer, mainScriptServer{
		script: func(stream workercontrolv1.WorkerControl_ConnectServer) error {
			ready, err := stream.Recv()
			if err != nil || ready.GetRuntimeReady() == nil {
				return fmt.Errorf("RuntimeReady = %+v, error = %v", ready, err)
			}
			if err := stream.Send(mainBackendCommand(identity, 1, 1, "", &workercontrolv1.BackendEnvelope_InitializeSession{
				InitializeSession: &workercontrolv1.InitializeSession{
					WorkingCopy: workingCopy, AgentState: agentState, TraceSpool: traceSpool,
					AgentProfile: "codex", TraceId: "trace-1",
				},
			})); err != nil {
				return err
			}
			sessionReady, err := stream.Recv()
			if err != nil || sessionReady.GetAgentSessionReady().GetAgentSessionId() != "agent-session-1" {
				return fmt.Errorf("AgentSessionReady = %+v, error = %v", sessionReady, err)
			}
			if err := stream.Send(mainBackendCommand(identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
				StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "production prompt"},
			})); err != nil {
				return err
			}
			started, err := stream.Recv()
			if err != nil || started.GetTurnStarted() == nil {
				return fmt.Errorf("TurnStarted = %+v, error = %v", started, err)
			}
			completed, err := stream.Recv()
			if err != nil || completed.GetTurnCompleted() == nil ||
				len(completed.GetTurnCompleted().GetEvidence()) != 4 {
				return fmt.Errorf("TurnCompleted = %+v, error = %v", completed, err)
			}
			return stream.Send(mainBackendCommand(identity, 3, 4, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
				ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "test complete"},
			}))
		},
	})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	config := runtimeConfig{
		Identity:           identity,
		WorkerToken:        "worker-token",
		BackendGRPCTarget:  "switchback-backend:8081",
		BackendHTTPBaseURL: uploadServer.URL,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = runWithConfig(ctx, config, runDependencies{
		StartRuntime: func(context.Context, runtimeConfig) (*startedRuntime, error) {
			runtimeStarted.Add(1)
			return &startedRuntime{
				Agent:        agent,
				AgentProfile: agentprofile.Codex,
				Capabilities: acp.CapabilitySnapshot{ProtocolVersion: sdk.ProtocolVersionNumber, GatewayAuthenticated: true, FileSystemAdvertised: true},
				WorkingCopy:  workingCopy,
				AgentState:   agentState,
				TraceSpool:   traceSpool,
				Trace:        traceFile,
				Secrets:      redact.Secrets{WorkerToken: "worker-token"},
				Shutdown: func(context.Context) error {
					runtimeStopped.Add(1)
					return nil
				},
			}, nil
		},
		DialControl: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient(
				"passthrough:///bufnet",
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
					return listener.Dial()
				}),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
		},
		HTTPClient: uploadServer.Client(),
	})
	if err != nil {
		t.Fatalf("runWithConfig() error = %v", err)
	}
	if runtimeStarted.Load() != 1 || runtimeStopped.Load() != 1 {
		t.Fatalf("runtime lifecycle = started:%d stopped:%d, want 1/1", runtimeStarted.Load(), runtimeStopped.Load())
	}
	if agent.newSessionCalls.Load() != 1 || agent.promptCalls.Load() != 1 ||
		agent.lastPrompt != "production prompt" {
		t.Fatalf("ACP calls = sessions:%d prompts:%d last=%q",
			agent.newSessionCalls.Load(), agent.promptCalls.Load(), agent.lastPrompt)
	}
}

type mainFakeAgent struct {
	workingCopy     string
	trace           *os.File
	newSessionCalls atomic.Int32
	promptCalls     atomic.Int32
	lastPrompt      string
}

func (a *mainFakeAgent) NewSession(_ context.Context, workingCopy string) (string, error) {
	a.newSessionCalls.Add(1)
	if workingCopy != a.workingCopy {
		return "", fmt.Errorf("working copy = %q", workingCopy)
	}
	return "agent-session-1", nil
}

func (a *mainFakeAgent) Prompt(_ context.Context, sessionID, prompt string) (acp.PromptResult, error) {
	a.promptCalls.Add(1)
	a.lastPrompt = prompt
	if sessionID != "agent-session-1" {
		return acp.PromptResult{}, fmt.Errorf("session ID = %q", sessionID)
	}
	if err := os.WriteFile(filepath.Join(a.workingCopy, "result.txt"), []byte("done\n"), 0o600); err != nil {
		return acp.PromptResult{}, err
	}
	if err := json.NewEncoder(a.trace).Encode(map[string]string{
		"type":      "acp.raw_frame",
		"direction": "agent_to_client",
		"frame":     `{"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}}}`,
	}); err != nil {
		return acp.PromptResult{}, err
	}
	return acp.PromptResult{StopReason: sdk.StopReasonEndTurn}, nil
}

type mainScriptServer struct {
	workercontrolv1.UnimplementedWorkerControlServer
	script func(workercontrolv1.WorkerControl_ConnectServer) error
}

func (s mainScriptServer) Connect(stream workercontrolv1.WorkerControl_ConnectServer) error {
	return s.script(stream)
}

func mainBackendCommand(
	identity workercontrol.RuntimeIdentity,
	sequence, ack uint64,
	turnID string,
	payload any,
) *workercontrolv1.BackendEnvelope {
	command := &workercontrolv1.BackendEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      fmt.Sprintf("backend-%d", sequence),
			StreamSequence: sequence,
			AckSequence:    ack,
			Identity: &workercontrolv1.Identity{
				WorkerSessionId: identity.WorkerSessionID, WorkerRunId: identity.WorkerRunID,
				RuntimeInstanceId: identity.RuntimeInstanceID, RuntimeGeneration: identity.RuntimeGeneration,
				AgentTurnId: turnID,
			},
			SentAt: timestamppb.New(time.Unix(int64(sequence), 0).UTC()),
		},
	}
	switch typed := payload.(type) {
	case *workercontrolv1.BackendEnvelope_InitializeSession:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_StartAgentTurn:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_ShutdownRuntime:
		command.Payload = typed
	default:
		panic(fmt.Sprintf("unsupported payload %T", payload))
	}
	return command
}

func mainTestGitWorkingCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mainRunGit(t, root, "init", "-q")
	mainRunGit(t, root, "config", "user.name", "Switchback Test")
	mainRunGit(t, root, "config", "user.email", "switchback@example.test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainRunGit(t, root, "add", "base.txt")
	mainRunGit(t, root, "commit", "-qm", "base")
	return root
}

func mainRunGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
