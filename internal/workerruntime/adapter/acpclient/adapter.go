package acpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/abietic/switchback/internal/workerruntime/application"
	sdk "github.com/coder/acp-go-sdk"
)

type adapterState uint8

const (
	stateNew adapterState = iota
	stateStarting
	stateInitialized
	stateAuthenticated
	stateSessionOpening
	stateSessionOpen
	stateUnusable
	stateClosed
)

// Adapter owns one ACP child process, one connection and one AgentSession.
type Adapter struct {
	mu sync.Mutex

	options  Options
	state    adapterState
	command  *exec.Cmd
	stdin    io.WriteCloser
	conn     *sdk.ClientSideConnection
	recorder *traceRecorder
	done     chan error

	workerSessionID        string
	runtimeID              string
	generation             uint64
	capabilities           application.AgentCapabilities
	authenticationDeclared bool
	agentSessionID         string
	activeTurnID           string
	lastTerminalTurnID     string
	activeUpdates          chan application.AgentUpdate
	cancelRequested        bool
	systemError            bool
}

var _ application.AgentAdapter = (*Adapter)(nil)

// Initialize starts the child and negotiates a typed, exact-version ACP connection.
func (a *Adapter) Initialize(ctx context.Context, request application.AgentInitializeRequest) (application.AgentCapabilities, error) {
	if request.WorkerSessionID == "" || request.RuntimeID == "" || request.Generation == 0 ||
		request.ProtocolVersion != strconv.Itoa(sdk.ProtocolVersionNumber) {
		return application.AgentCapabilities{}, fmt.Errorf("ACP initialize identity or protocol is invalid")
	}
	a.mu.Lock()
	if a.state != stateNew {
		a.mu.Unlock()
		return application.AgentCapabilities{}, fmt.Errorf("ACP adapter cannot be initialized")
	}
	a.state = stateStarting
	a.mu.Unlock()
	if err := a.startProcess(); err != nil {
		a.failClosed()
		return application.AgentCapabilities{}, err
	}

	a.mu.Lock()
	connection := a.conn
	a.mu.Unlock()
	response, err := connection.Initialize(ctx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientInfo:      &sdk.Implementation{Name: "switchback-worker-shim", Version: "m1"},
		ClientCapabilities: sdk.ClientCapabilities{
			Fs:   sdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Auth: a.options.Authentication.Capabilities,
		},
	})
	if err != nil {
		a.failClosed()
		return application.AgentCapabilities{}, fmt.Errorf("initialize ACP connection")
	}
	if response.ProtocolVersion != sdk.ProtocolVersionNumber || !hasAuthMethod(response.AuthMethods, a.options.Authentication.MethodID) {
		a.failClosed()
		return application.AgentCapabilities{}, fmt.Errorf("ACP protocol or %s authentication capability is unsupported", a.options.Authentication.MethodID)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		a.failClosed()
		return application.AgentCapabilities{}, fmt.Errorf("record ACP capability snapshot")
	}
	snapshot := application.AgentCapabilities{
		ProtocolVersion: strconv.Itoa(int(response.ProtocolVersion)),
		ResumeSession:   response.AgentCapabilities.SessionCapabilities.Resume != nil,
		LoadSession:     response.AgentCapabilities.LoadSession,
		Prompt:          true,
		Cancel:          true,
		Raw:             raw,
	}
	a.mu.Lock()
	if a.state != stateStarting {
		a.mu.Unlock()
		a.failClosed()
		return application.AgentCapabilities{}, fmt.Errorf("ACP adapter startup was fenced")
	}
	a.workerSessionID = request.WorkerSessionID
	a.runtimeID = request.RuntimeID
	a.generation = request.Generation
	a.capabilities = snapshot
	a.authenticationDeclared = true
	a.state = stateInitialized
	a.mu.Unlock()
	return snapshot, nil
}

// Authenticate sends the proxy token through the trusted authentication policy.
func (a *Adapter) Authenticate(ctx context.Context, proxyToken string) error {
	if err := validateProxyToken(proxyToken); err != nil {
		return err
	}
	request, err := a.options.Authentication.Request(proxyToken)
	if err != nil || request.MethodId != a.options.Authentication.MethodID {
		return fmt.Errorf("ACP authentication request is invalid")
	}
	a.mu.Lock()
	if a.state != stateInitialized || !a.authenticationDeclared || a.conn == nil {
		a.mu.Unlock()
		return fmt.Errorf("ACP adapter is not ready for authentication")
	}
	connection := a.conn
	a.recorder.setProxyToken(proxyToken)
	a.mu.Unlock()
	_, err = connection.Authenticate(ctx, request)
	if err != nil {
		a.failClosed()
		return fmt.Errorf("authenticate ACP %s", a.options.Authentication.MethodID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateInitialized {
		return fmt.Errorf("ACP authentication result was fenced")
	}
	a.state = stateAuthenticated
	return nil
}

// OpenSession creates or loads exactly one AgentSession for this adapter.
func (a *Adapter) OpenSession(ctx context.Context, request application.AgentSessionRequest) (string, error) {
	if request.WorkerSessionID == "" || request.WorkingCopyPath != a.options.WorkspaceRoot {
		return "", fmt.Errorf("ACP session identity or WorkingCopy is invalid")
	}
	if request.LoadExisting && request.AgentSessionID == "" {
		return "", fmt.Errorf("existing ACP session id is required")
	}
	if !request.LoadExisting && request.AgentSessionID != "" {
		return "", fmt.Errorf("new ACP session cannot select an existing id")
	}
	a.mu.Lock()
	if a.state != stateAuthenticated || a.conn == nil || request.WorkerSessionID != a.workerSessionID {
		a.mu.Unlock()
		return "", fmt.Errorf("ACP adapter is not authenticated")
	}
	if request.LoadExisting && !a.capabilities.ResumeSession && !a.capabilities.LoadSession {
		a.mu.Unlock()
		return "", fmt.Errorf("ACP session resume/load is unsupported")
	}
	resumeExisting := request.LoadExisting && a.capabilities.ResumeSession
	a.state = stateSessionOpening
	connection := a.conn
	a.mu.Unlock()

	var sessionID string
	var err error
	if request.LoadExisting {
		if resumeExisting {
			_, err = connection.ResumeSession(ctx, sdk.ResumeSessionRequest{
				SessionId: sdk.SessionId(request.AgentSessionID),
				Cwd:       request.WorkingCopyPath, McpServers: []sdk.McpServer{},
			})
		} else {
			_, err = connection.LoadSession(ctx, sdk.LoadSessionRequest{
				SessionId: sdk.SessionId(request.AgentSessionID),
				Cwd:       request.WorkingCopyPath, McpServers: []sdk.McpServer{},
			})
		}
		sessionID = request.AgentSessionID
	} else {
		var response sdk.NewSessionResponse
		response, err = connection.NewSession(ctx, sdk.NewSessionRequest{
			Cwd: request.WorkingCopyPath, McpServers: []sdk.McpServer{},
		})
		sessionID = string(response.SessionId)
	}
	if err != nil || sessionID == "" {
		a.failClosed()
		return "", fmt.Errorf("open ACP session: outcome is unknown")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateSessionOpening {
		return "", fmt.Errorf("ACP session result was fenced")
	}
	a.agentSessionID = sessionID
	a.lastTerminalTurnID = ""
	a.state = stateSessionOpen
	return sessionID, nil
}

// Prompt starts exactly one typed prompt and emits normalized SDK updates plus one terminal result.
func (a *Adapter) Prompt(ctx context.Context, request application.AgentPromptRequest) (<-chan application.AgentUpdate, error) {
	if request.AgentSessionID == "" || request.AgentTurnID == "" || request.Content == "" {
		return nil, fmt.Errorf("ACP prompt identity and content are required")
	}
	a.mu.Lock()
	if a.state != stateSessionOpen || request.AgentSessionID != a.agentSessionID ||
		a.activeTurnID != "" || a.conn == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("ACP prompt target is stale or busy")
	}
	updates := make(chan application.AgentUpdate, 128)
	a.lastTerminalTurnID = ""
	a.activeTurnID = request.AgentTurnID
	a.activeUpdates = updates
	a.cancelRequested = false
	a.systemError = false
	connection := a.conn
	a.mu.Unlock()

	go a.runPrompt(ctx, connection, request, updates)
	return updates, nil
}

func (a *Adapter) runPrompt(ctx context.Context, connection *sdk.ClientSideConnection, request application.AgentPromptRequest, updates chan application.AgentUpdate) {
	response, err := connection.Prompt(ctx, sdk.PromptRequest{
		SessionId: sdk.SessionId(request.AgentSessionID),
		Prompt:    []sdk.ContentBlock{sdk.TextBlock(request.Content)},
		Meta: map[string]any{"switchback": map[string]any{
			"agent_turn_id":      request.AgentTurnID,
			"runtime_id":         a.runtimeID,
			"runtime_generation": a.generation,
			"worker_session_id":  a.workerSessionID,
		}},
	})
	a.mu.Lock()
	systemError := a.systemError
	cancelRequested := a.cancelRequested
	a.mu.Unlock()
	if err != nil && cancelRequested {
		response.StopReason = sdk.StopReasonCancelled
		err = nil
	}
	if err != nil || systemError || !terminalStopReason(response.StopReason) {
		code := "acp_prompt_failed"
		if err != nil {
			select {
			case <-connection.Done():
				code = "acp_eof"
			default:
			}
		}
		payload, _ := json.Marshal(map[string]string{"code": code})
		a.sendUpdate(ctx, updates, application.AgentUpdate{
			Kind: "error", Payload: payload,
		})
		a.finishPrompt(request.AgentTurnID)
		close(updates)
		return
	}
	payload, _ := json.Marshal(map[string]string{"stop_reason": string(response.StopReason)})
	a.sendUpdate(ctx, updates, application.AgentUpdate{Kind: "terminal", Payload: payload})
	a.finishPrompt(request.AgentTurnID)
	close(updates)
}

func (a *Adapter) finishPrompt(agentTurnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeTurnID == agentTurnID {
		a.lastTerminalTurnID = agentTurnID
		a.activeTurnID = ""
		a.activeUpdates = nil
		a.cancelRequested = false
		a.systemError = false
	}
}

// Cancel targets the exact currently active AgentTurn and preserves the Session.
func (a *Adapter) Cancel(ctx context.Context, agentSessionID, agentTurnID string) error {
	a.mu.Lock()
	if a.state != stateSessionOpen || a.conn == nil || agentSessionID != a.agentSessionID ||
		agentTurnID == "" {
		a.mu.Unlock()
		return fmt.Errorf("ACP cancel target is stale or inactive")
	}
	if a.activeTurnID == "" && agentTurnID == a.lastTerminalTurnID {
		a.mu.Unlock()
		return nil
	}
	if agentTurnID != a.activeTurnID {
		a.mu.Unlock()
		return fmt.Errorf("ACP cancel target is stale or inactive")
	}
	connection := a.conn
	a.cancelRequested = true
	a.mu.Unlock()
	if err := connection.Cancel(ctx, sdk.CancelNotification{SessionId: sdk.SessionId(agentSessionID)}); err != nil {
		a.mu.Lock()
		if a.activeTurnID == agentTurnID {
			a.cancelRequested = false
		}
		a.mu.Unlock()
		return fmt.Errorf("cancel ACP prompt")
	}
	return nil
}

func (a *Adapter) observeSessionUpdate(notification sdk.SessionNotification) {
	payload, err := json.Marshal(notification.Update)
	if err != nil {
		return
	}
	kind := sessionUpdateKind(payload)
	a.mu.Lock()
	defer a.mu.Unlock()
	if string(notification.SessionId) != a.agentSessionID || a.activeTurnID == "" || a.activeUpdates == nil {
		return
	}
	updates := a.activeUpdates
	if a.options.IsSystemError != nil && a.options.IsSystemError(notification) {
		a.systemError = true
	}
	select {
	case updates <- application.AgentUpdate{Kind: kind, Payload: payload}:
	default:
		// Bounded backpressure is a protocol failure, not permission to drop updates.
		a.systemError = true
	}
}

func (a *Adapter) sendUpdate(ctx context.Context, updates chan application.AgentUpdate, update application.AgentUpdate) {
	select {
	case updates <- update:
	case <-ctx.Done():
	}
}

func (a *Adapter) failClosed() {
	a.mu.Lock()
	if a.state != stateClosed {
		a.state = stateUnusable
	}
	a.mu.Unlock()
}

func hasAuthMethod(methods []sdk.AuthMethod, wanted string) bool {
	for _, method := range methods {
		if method.Agent != nil && method.Agent.Id == wanted {
			return true
		}
		if method.EnvVar != nil && method.EnvVar.Id == wanted {
			return true
		}
		if method.Terminal != nil && method.Terminal.Id == wanted {
			return true
		}
	}
	return false
}

func validateProxyToken(token string) error {
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("ACP proxy token is invalid")
	}
	return nil
}

func terminalStopReason(reason sdk.StopReason) bool {
	switch reason {
	case sdk.StopReasonEndTurn, sdk.StopReasonMaxTokens, sdk.StopReasonMaxTurnRequests,
		sdk.StopReasonRefusal, sdk.StopReasonCancelled:
		return true
	default:
		return false
	}
}

func sessionUpdateKind(payload []byte) string {
	var envelope struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.SessionUpdate == "" {
		return "unknown"
	}
	return envelope.SessionUpdate
}
