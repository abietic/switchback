package codexacp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	sdk "github.com/coder/acp-go-sdk"
)

const (
	testProxyToken = "test-litellm-proxy-token"
	testSessionID  = "agent-session-new"
)

func TestFacadeRejectsNilTraceWithCompatibleMessage(t *testing.T) {
	a, err := New(nil)
	if a != nil || err == nil || err.Error() != "CodexAdapter process, workspace and Trace are required" {
		t.Fatalf("New(nil) = %v, %v", a, err)
	}
}
func TestFacadeFixedCommandUsesTrustedPolicy(t *testing.T) {
	cmd := fixedCommand()
	expectedEnv := []string{"APP_SERVER_LOGS=/run/switchback-runtime/app-server-logs", `CODEX_CONFIG={"model":"switchback-codex","approval_policy":"never","sandbox_mode":"danger-full-access","web_search":"disabled"}`, "CODEX_HOME=/var/lib/switchback/agent-state/codex", "CODEX_PATH=/opt/switchback/node_modules/.bin/codex", "CODEX_SQLITE_HOME=/run/switchback-runtime/codex-sqlite", "HOME=/var/lib/switchback/agent-state", "INITIAL_AGENT_MODE=agent-full-access", "NO_BROWSER=1", "PATH=/usr/local/bin:/usr/bin:/bin"}
	if cmd.Path != "/opt/switchback/node_modules/.bin/codex-acp" || !reflect.DeepEqual(cmd.Args, []string{"/opt/switchback/node_modules/.bin/codex-acp"}) || !reflect.DeepEqual(cmd.Env, expectedEnv) {
		t.Fatalf("fixed command: %+v", cmd)
	}
	if os.Geteuid() == 0 {
		if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
			t.Fatal("root command omitted fixed credential")
		}
		credential := cmd.SysProcAttr.Credential
		if credential.Uid != 10002 || credential.Gid != 10002 || len(credential.Groups) != 0 {
			t.Fatalf("credential: %+v", credential)
		}
	} else if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatal("non-root command supplied unexpected credential")
	}
}
func TestFacadeClassifiesCodexSystemError(t *testing.T) {
	var trace safeTraceBuffer
	a := newTestAdapter(t, &trace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initializeTestAdapter(t, ctx, a)
	id, err := a.OpenSession(ctx, application.AgentSessionRequest{WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := a.Prompt(ctx, application.AgentPromptRequest{AgentSessionID: id, AgentTurnID: "turn-system-error", Content: "system-error"})
	if err != nil {
		t.Fatal(err)
	}
	var last application.AgentUpdate
	for u := range ch {
		last = u
	}
	if last.Kind != "error" || string(last.Payload) != `{"code":"acp_prompt_failed"}` {
		t.Fatalf("last update: %+v", last)
	}
}

func TestFixedAgentEnvironmentUsesPersistedSessionMounts(t *testing.T) {
	joined := strings.Join(fixedAgentEnvironment, "\n")
	for _, expected := range []string{
		"APP_SERVER_LOGS=/run/switchback-runtime/app-server-logs",
		"CODEX_HOME=/var/lib/switchback/agent-state/codex",
		"CODEX_SQLITE_HOME=/run/switchback-runtime/codex-sqlite",
		"HOME=/var/lib/switchback/agent-state",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("fixed Agent environment does not contain %q", expected)
		}
	}
	for _, stale := range []string{
		"APP_SERVER_LOGS=/trace-spool",
		"APP_SERVER_LOGS=/var/lib/switchback/trace-spool",
		"CODEX_HOME=/agent-state",
		"HOME=/agent-state",
	} {
		if strings.Contains(joined, stale) {
			t.Fatalf("fixed Agent environment still contains non-persisted path %q", stale)
		}
	}
}

func TestAdapterDrivesTypedLifecycleAndRedactsPersistedFrames(t *testing.T) {
	var trace safeTraceBuffer
	adapter := newTestAdapter(t, &trace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	capabilities, err := adapter.Initialize(ctx, application.AgentInitializeRequest{
		WorkerSessionID: "worker-session-1",
		RuntimeID:       "runtime-1",
		Generation:      1,
		ProtocolVersion: "1",
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if capabilities.ProtocolVersion != "1" || !capabilities.ResumeSession || !capabilities.LoadSession ||
		!capabilities.Prompt || !capabilities.Cancel {
		t.Fatalf("capability snapshot = %+v", capabilities)
	}
	if !json.Valid(capabilities.Raw) || !bytes.Contains(capabilities.Raw, []byte(`"loadSession":true`)) {
		t.Fatalf("raw capability snapshot = %s", capabilities.Raw)
	}
	if _, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1",
		WorkingCopyPath: "/workspace",
	}); err == nil {
		t.Fatal("session opened before authentication")
	}
	if err := adapter.Authenticate(ctx, testProxyToken); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1",
		WorkingCopyPath: "/workspace",
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if sessionID != testSessionID {
		t.Fatalf("session ID = %q, want %q", sessionID, testSessionID)
	}
	updates, err := adapter.Prompt(ctx, application.AgentPromptRequest{
		AgentSessionID: sessionID,
		AgentTurnID:    "agent-turn-1",
		Content:        "emit unknown update",
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var kinds []string
	for update := range updates {
		kinds = append(kinds, update.Kind)
	}
	if strings.Join(kinds, ",") != "agent_message_chunk,terminal" {
		t.Fatalf("update kinds = %v", kinds)
	}
	traceBytes := trace.Bytes()
	if bytes.Contains(traceBytes, []byte(testProxyToken)) {
		t.Fatal("persisted ACP trace exposed the LiteLLM proxy token")
	}
	if !bytes.Contains(traceBytes, []byte("[REDACTED:litellm_proxy_token]")) {
		t.Fatal("persisted ACP trace omitted the proxy-token redaction marker")
	}
	if !bytes.Contains(traceBytes, []byte(`"type":"acp.unknown_update"`)) {
		t.Fatal("unknown optional update was not retained without killing the prompt")
	}
	for _, method := range []string{"initialize", "authenticate", "session/new", "session/prompt"} {
		if !bytes.Contains(traceBytes, []byte(method)) {
			t.Fatalf("persisted ACP trace omitted %q", method)
		}
	}
}

func TestAdapterReusesSessionAfterTerminal(t *testing.T) {
	for _, firstContent := range []string{"finish naturally", "wait for cancel"} {
		t.Run(firstContent, func(t *testing.T) {
			var trace safeTraceBuffer
			adapter := newTestAdapter(t, &trace)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			initializeTestAdapter(t, ctx, adapter)
			sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
				WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := adapter.Prompt(ctx, application.AgentPromptRequest{
				AgentSessionID: sessionID, AgentTurnID: "turn-first", Content: firstContent,
			})
			if err != nil {
				t.Fatal(err)
			}
			if update := <-first; update.Kind != "agent_message_chunk" {
				t.Fatalf("first update = %+v", update)
			}
			want := `{"stop_reason":"end_turn"}`
			if firstContent == "wait for cancel" {
				if err := adapter.Cancel(ctx, sessionID, "turn-first"); err != nil {
					t.Fatal(err)
				}
				want = `{"stop_reason":"cancelled"}`
			}
			terminal := <-first
			if terminal.Kind != "terminal" || string(terminal.Payload) != want {
				t.Fatalf("first terminal = %+v, want %s", terminal, want)
			}
			if _, open := <-first; open {
				t.Fatal("first channel remained open")
			}
			if err := adapter.Cancel(ctx, sessionID, "turn-first"); err != nil {
				t.Fatal(err)
			}
			second, err := adapter.Prompt(ctx, application.AgentPromptRequest{
				AgentSessionID: sessionID, AgentTurnID: "turn-second", Content: "finish naturally",
			})
			if err != nil {
				t.Fatalf("reuse Session: %v", err)
			}
			var kinds []string
			for update := range second {
				kinds = append(kinds, update.Kind)
			}
			if strings.Join(kinds, ",") != "agent_message_chunk,terminal" {
				t.Fatalf("second kinds = %v", kinds)
			}
		})
	}
}

func TestAdapterCancelsExactActiveTurn(t *testing.T) {
	var trace safeTraceBuffer
	adapter := newTestAdapter(t, &trace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initializeTestAdapter(t, ctx, adapter)
	sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	updates, err := adapter.Prompt(ctx, application.AgentPromptRequest{
		AgentSessionID: sessionID, AgentTurnID: "agent-turn-cancel", Content: "wait for cancel",
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if update := <-updates; update.Kind != "agent_message_chunk" {
		t.Fatalf("first update = %+v", update)
	}
	if err := adapter.Cancel(ctx, sessionID, "agent-turn-cancel"); err != nil {
		t.Fatalf("cancel active turn: %v", err)
	}
	terminal := <-updates
	if terminal.Kind != "terminal" || !bytes.Contains(terminal.Payload, []byte("cancelled")) {
		t.Fatalf("cancel terminal update = %+v", terminal)
	}
	if _, open := <-updates; open {
		t.Fatal("updates channel remained open after cancellation terminal")
	}
	if !bytes.Contains(trace.Bytes(), []byte("session/cancel")) {
		t.Fatal("typed session/cancel was not recorded")
	}
}

func TestAdapterTreatsCancelAfterCompletedTerminalAsIdempotent(t *testing.T) {
	var trace safeTraceBuffer
	adapter := newTestAdapter(t, &trace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initializeTestAdapter(t, ctx, adapter)
	sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := adapter.Prompt(ctx, application.AgentPromptRequest{
		AgentSessionID: sessionID,
		AgentTurnID:    "agent-turn-natural-terminal",
		Content:        "finish naturally",
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for update := range updates {
		kinds = append(kinds, update.Kind)
	}
	if strings.Join(kinds, ",") != "agent_message_chunk,terminal" {
		t.Fatalf("completed updates=%v", kinds)
	}
	if err := adapter.Cancel(ctx, sessionID, "agent-turn-natural-terminal"); err != nil {
		t.Fatalf("cancel after completed terminal: %v", err)
	}
	if bytes.Contains(trace.Bytes(), []byte(`"method":"session/cancel"`)) {
		t.Fatal("already-terminal Cancel sent a redundant ACP cancel")
	}
}

func TestAdapterPrefersResumeForExactExistingSession(t *testing.T) {
	var trace safeTraceBuffer
	adapter := newTestAdapter(t, &trace)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initializeTestAdapter(t, ctx, adapter)

	sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1",
		AgentSessionID:  "agent-session-existing",
		WorkingCopyPath: "/workspace",
		LoadExisting:    true,
	})
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if sessionID != "agent-session-existing" {
		t.Fatalf("loaded session ID = %q", sessionID)
	}
	if !bytes.Contains(trace.Bytes(), []byte("session/resume")) ||
		bytes.Contains(trace.Bytes(), []byte(`"method":"session/load"`)) {
		t.Fatal("typed session/resume was not preferred")
	}
}

func TestAdapterFallsBackToLoadWhenResumeIsNotDeclared(t *testing.T) {
	var trace safeTraceBuffer
	adapter := newTestAdapterWithResume(t, &trace, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initializeTestAdapter(t, ctx, adapter)

	sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: "worker-session-1",
		AgentSessionID:  "agent-session-existing",
		WorkingCopyPath: "/workspace",
		LoadExisting:    true,
	})
	if err != nil {
		t.Fatalf("load session fallback: %v", err)
	}
	if sessionID != "agent-session-existing" ||
		!bytes.Contains(trace.Bytes(), []byte("session/load")) {
		t.Fatalf("fallback session=%q trace=%s", sessionID, trace.Bytes())
	}
}

func TestAdapterFailsClosedOnProtocolAndIdentityMismatch(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Adapter) error
	}{
		{
			name: "protocol",
			run: func(ctx context.Context, adapter *Adapter) error {
				_, err := adapter.Initialize(ctx, application.AgentInitializeRequest{
					WorkerSessionID: "worker-session-1", RuntimeID: "runtime-1",
					Generation: 1, ProtocolVersion: "2",
				})
				return err
			},
		},
		{
			name: "prompt session",
			run: func(ctx context.Context, adapter *Adapter) error {
				initializeTestAdapter(t, ctx, adapter)
				_, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
					WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
				})
				if err != nil {
					return err
				}
				_, err = adapter.Prompt(ctx, application.AgentPromptRequest{
					AgentSessionID: "another-session", AgentTurnID: "turn-1", Content: "wrong target",
				})
				return err
			},
		},
		{
			name: "cancel turn",
			run: func(ctx context.Context, adapter *Adapter) error {
				initializeTestAdapter(t, ctx, adapter)
				sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
					WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
				})
				if err != nil {
					return err
				}
				return adapter.Cancel(ctx, sessionID, "never-started")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var trace safeTraceBuffer
			adapter := newTestAdapter(t, &trace)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := test.run(ctx, adapter); err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
		})
	}
}

type safeTraceBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *safeTraceBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *safeTraceBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(content []byte) (int, error) { return f(content) }

func newTestAdapter(t *testing.T, trace io.Writer) *Adapter {
	return newTestAdapterWithResume(t, trace, true)
}

func newTestAdapterWithResume(t *testing.T, trace io.Writer, resume bool) *Adapter {
	t.Helper()
	adapter, err := newAdapter(processOptions{
		command: func() *exec.Cmd {
			command := exec.Command(os.Args[0], "-test.run=^TestACPHelperProcess$")
			resumeValue := "0"
			if resume {
				resumeValue = "1"
			}
			command.Env = append(
				os.Environ(),
				"SWITCHBACK_ACP_HELPER=1",
				"SWITCHBACK_ACP_HELPER_RESUME="+resumeValue,
			)
			return command
		},
		workspaceRoot: "/workspace",
		trace:         trace,
	})
	if err != nil {
		t.Fatalf("new test adapter: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})
	return adapter
}

func initializeTestAdapter(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	if _, err := adapter.Initialize(ctx, application.AgentInitializeRequest{
		WorkerSessionID: "worker-session-1", RuntimeID: "runtime-1",
		Generation: 1, ProtocolVersion: "1",
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := adapter.Authenticate(ctx, testProxyToken); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
}

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("SWITCHBACK_ACP_HELPER") != "1" {
		return
	}
	agent := &helperAgent{cancelled: make(chan struct{})}
	connection := sdk.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.connection = connection
	<-connection.Done()
}

type helperAgent struct {
	connection *sdk.AgentSideConnection
	cancelled  chan struct{}
	cancelOnce sync.Once
}

var (
	_ sdk.Agent       = (*helperAgent)(nil)
	_ sdk.AgentLoader = (*helperAgent)(nil)
)

func (*helperAgent) Initialize(_ context.Context, request sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	if request.ProtocolVersion != sdk.ProtocolVersionNumber {
		return sdk.InitializeResponse{}, fmt.Errorf("unsupported protocol")
	}
	capabilities := sdk.AgentCapabilities{LoadSession: true}
	if os.Getenv("SWITCHBACK_ACP_HELPER_RESUME") == "1" {
		capabilities.SessionCapabilities.Resume = &sdk.SessionResumeCapabilities{}
	}
	return sdk.InitializeResponse{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		AuthMethods: []sdk.AuthMethod{{Agent: &sdk.AuthMethodAgent{
			Id: "gateway", Name: "Switchback LiteLLM",
		}}},
		AgentCapabilities: capabilities,
	}, nil
}

func (*helperAgent) Authenticate(_ context.Context, request sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	gateway, ok := request.Meta["gateway"].(map[string]any)
	if request.MethodId != "gateway" || !ok {
		return sdk.AuthenticateResponse{}, fmt.Errorf("gateway authentication required")
	}
	headers, ok := gateway["headers"].(map[string]any)
	if !ok || headers["Authorization"] != "Bearer "+testProxyToken {
		return sdk.AuthenticateResponse{}, fmt.Errorf("invalid gateway token")
	}
	return sdk.AuthenticateResponse{}, nil
}

func (*helperAgent) NewSession(_ context.Context, request sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	if request.Cwd != "/workspace" {
		return sdk.NewSessionResponse{}, fmt.Errorf("invalid cwd")
	}
	return sdk.NewSessionResponse{SessionId: sdk.SessionId(testSessionID)}, nil
}

func (*helperAgent) LoadSession(_ context.Context, request sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	if request.SessionId != "agent-session-existing" || request.Cwd != "/workspace" {
		return sdk.LoadSessionResponse{}, fmt.Errorf("invalid existing session")
	}
	return sdk.LoadSessionResponse{}, nil
}

func (agent *helperAgent) Prompt(ctx context.Context, request sdk.PromptRequest) (sdk.PromptResponse, error) {
	if request.SessionId == "" {
		return sdk.PromptResponse{}, fmt.Errorf("missing session")
	}
	if len(request.Prompt) > 0 && request.Prompt[0].Text != nil && request.Prompt[0].Text.Text == "system-error" {
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"session_info_update","_meta":{"codex":{"threadStatus":{"type":"systemError"}}}}}}`+"\n", string(request.SessionId))
	}
	if len(request.Prompt) > 0 && request.Prompt[0].Text != nil &&
		request.Prompt[0].Text.Text == "emit unknown update" {
		_, _ = fmt.Fprintln(os.Stdout,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"agent-session-new","update":{"sessionUpdate":"future_update"}}}`,
		)
	}
	if err := agent.connection.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: request.SessionId,
		Update:    sdk.UpdateAgentMessageText("working"),
	}); err != nil {
		return sdk.PromptResponse{}, err
	}
	if len(request.Prompt) > 0 && request.Prompt[0].Text != nil &&
		request.Prompt[0].Text.Text == "wait for cancel" {
		select {
		case <-agent.cancelled:
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		case <-ctx.Done():
			// AgentSideConnection cancels this session context before invoking
			// Cancel. The peer must confirm cancellation with a normal response,
			// not an RPC error that the client could mistake for stop proof.
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		}
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

func (agent *helperAgent) Cancel(context.Context, sdk.CancelNotification) error {
	agent.cancelOnce.Do(func() { close(agent.cancelled) })
	return nil
}
func (*helperAgent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}
func (*helperAgent) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, nil
}
func (*helperAgent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}
func (*helperAgent) ResumeSession(_ context.Context, request sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	if request.SessionId != "agent-session-existing" || request.Cwd != "/workspace" {
		return sdk.ResumeSessionResponse{}, fmt.Errorf("invalid resumed session")
	}
	return sdk.ResumeSessionResponse{}, nil
}
func (*helperAgent) SetSessionConfigOption(context.Context, sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	return sdk.SetSessionConfigOptionResponse{}, nil
}
func (*helperAgent) SetSessionMode(context.Context, sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	return sdk.SetSessionModeResponse{}, nil
}
