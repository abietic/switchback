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

	options               Options
	state                 adapterState
	command               *exec.Cmd
	stdin                 io.WriteCloser
	conn                  *sdk.ClientSideConnection
	recorder              *traceRecorder
	done                  chan error
	lifecycle             context.Context
	cancelLifecycle       context.CancelFunc
	explicitClose         chan struct{}
	closeOnce             sync.Once
	outputReader          *io.PipeReader
	outputWriter          *io.PipeWriter
	outputSource          io.ReadCloser
	notificationCredits   chan struct{}
	admittedNotifications chan *notificationCredit

	workerSessionID        string
	runtimeID              string
	generation             uint64
	capabilities           application.AgentCapabilities
	authenticationDeclared bool
	agentSessionID         string
	activeTurnID           string
	lastTerminalTurnID     string
	activeDelivery         *turnDelivery
	systemError            bool
}

type turnDelivery struct {
	ctx       context.Context
	updates   chan application.AgentUpdate
	done      chan struct{}
	closing   bool           // guarded by Adapter.mu
	callbacks sync.WaitGroup // Add under Adapter.mu before closing is set
}

type notificationCredit struct {
	delivery *turnDelivery
	release  func()
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
	delivery := &turnDelivery{ctx: ctx, updates: updates, done: make(chan struct{})}
	a.activeDelivery = delivery
	a.systemError = false
	connection := a.conn
	a.mu.Unlock()

	go a.runPrompt(ctx, connection, request, delivery)
	return updates, nil
}

func (a *Adapter) runPrompt(ctx context.Context, connection *sdk.ClientSideConnection, request application.AgentPromptRequest, delivery *turnDelivery) {
	updates := delivery.updates
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
	// Normal SDK responses already wait for the notification watermark. Error,
	// cancellation and Close paths need an explicit send/close barrier too.
	delivery.closing = true
	close(delivery.done)
	systemError := a.systemError
	a.mu.Unlock()
	delivery.callbacks.Wait()
	// Only a normal SDK return proves both the response and notification
	// watermark. Cancel intent, EOF, RPC error and context abort are not proof.
	// A context cancelled after a proven normal response does not revoke proof.
	if err != nil || systemError || !terminalStopReason(response.StopReason) {
		code := "acp_prompt_failed"
		if err != nil {
			select {
			case <-connection.Done():
				code = "acp_eof"
			default:
			}
		}
		a.failClosed()
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
		a.activeDelivery = nil
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
	a.mu.Unlock()
	if err := connection.Cancel(ctx, sdk.CancelNotification{SessionId: sdk.SessionId(agentSessionID)}); err != nil {
		return fmt.Errorf("cancel ACP prompt")
	}
	return nil
}

func (a *Adapter) observeSessionUpdate(ctx context.Context, notification sdk.SessionNotification) error {
	// The raw tee admits exactly one credit for each typed notification. Binding
	// its Turn there prevents old queued callbacks from entering a later Prompt.
	var credit *notificationCredit
	select {
	case credit = <-a.admittedNotifications:
	case <-ctx.Done():
		a.failClosed()
		return ctx.Err()
	case <-a.lifecycle.Done():
		return a.lifecycle.Err()
	}
	defer credit.release()
	payload, err := json.Marshal(notification.Update)
	if err != nil {
		return err
	}
	kind := sessionUpdateKind(payload)
	a.mu.Lock()
	delivery := credit.delivery
	if string(notification.SessionId) != a.agentSessionID || delivery == nil || delivery != a.activeDelivery || delivery.closing || a.state != stateSessionOpen {
		a.mu.Unlock()
		return nil
	}
	delivery.callbacks.Add(1)
	if a.options.IsSystemError != nil && a.options.IsSystemError(notification) {
		a.systemError = true
	}
	a.mu.Unlock()
	defer delivery.callbacks.Done()
	select {
	case delivery.updates <- application.AgentUpdate{Kind: kind, Payload: payload}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-delivery.ctx.Done():
		return delivery.ctx.Err()
	case <-delivery.done:
		return nil
	case <-a.lifecycle.Done():
		return a.lifecycle.Err()
	}
}

func (a *Adapter) sendUpdate(ctx context.Context, updates chan application.AgentUpdate, update application.AgentUpdate) {
	select {
	case updates <- update:
	case <-ctx.Done():
	case <-a.explicitClose:
	}
}

func (a *Adapter) failClosed() {
	a.mu.Lock()
	if a.state != stateClosed {
		a.state = stateUnusable
	}
	a.mu.Unlock()
	a.cancelLifecycle()
	a.closeOutput()
}

func (a *Adapter) closeOutput() {
	a.mu.Lock()
	reader, writer, source := a.outputReader, a.outputWriter, a.outputSource
	// Admission and queue teardown share Adapter.mu. A callback may already own
	// a ticket; its defer releases that credit. Remaining queued tickets are
	// released here, including a target-write failure's ticket (release is Once).
	var queued []*notificationCredit
	for {
		select {
		case credit := <-a.admittedNotifications:
			queued = append(queued, credit)
		default:
			goto drained
		}
	}
drained:
	a.mu.Unlock()
	for _, credit := range queued {
		credit.release()
	}
	if reader != nil {
		_ = reader.Close()
	}
	if writer != nil {
		_ = writer.Close()
	}
	if source != nil {
		_ = source.Close()
	}
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
