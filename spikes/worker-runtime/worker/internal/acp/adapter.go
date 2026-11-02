// Package acp owns the typed ACP connection to the WorkerSession's codex-acp child.
package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"golang.org/x/sys/unix"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/internal/redact"
	"switchback.local/spikes/worker-runtime/worker/internal/process"
	"switchback.local/spikes/worker-runtime/worker/internal/trace"
)

const (
	proxyBaseURL = "http://127.0.0.1:4000/v1"
	providerName = "Switchback LiteLLM"

	acpShutdownGrace    = 250 * time.Millisecond
	acpKillGrace        = time.Second
	acpProcessGroupPoll = 10 * time.Millisecond
)

var fixedAgentEnvironment = []string{
	"APP_SERVER_LOGS=/trace-spool/app-server",
	`CODEX_CONFIG={"model":"switchback-codex","approval_policy":"never","sandbox_mode":"danger-full-access","web_search":"disabled"}`,
	"CODEX_HOME=/agent-state/codex",
	"CODEX_PATH=/opt/switchback/node_modules/.bin/codex",
	"INITIAL_AGENT_MODE=agent-full-access",
	"NO_BROWSER=1",
}

// Adapter is the narrow Worker-side ACP lifecycle boundary.
type Adapter interface {
	Start(context.Context, StartConfig) (CapabilitySnapshot, error)
	NewSession(context.Context, string) (string, error)
	Prompt(context.Context, string, string) (PromptResult, error)
	Close(context.Context) error
}

// StartConfig contains trusted server-owned child configuration.
type StartConfig struct {
	AgentProfile agentprofile.Profile
	ProxyToken   string
	Trace        io.Writer
}

// CapabilitySnapshot is retained after a successful authenticated initialize.
type CapabilitySnapshot struct {
	ProtocolVersion      sdk.ProtocolVersion
	GatewayAuthenticated bool
	FileSystemAdvertised bool
}

// PromptResult is the typed terminal result of one ACP prompt.
type PromptResult struct {
	StopReason sdk.StopReason
}

// PromptMetadata is trusted Worker-owned correlation carried only in ACP _meta.
type PromptMetadata struct {
	RequestID       string
	TraceID         string
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
}

type promptMetadataContextKey struct{}

// WithPromptMetadata binds model-observation correlation to one trusted Turn.
func WithPromptMetadata(ctx context.Context, metadata PromptMetadata) context.Context {
	return context.WithValue(ctx, promptMetadataContextKey{}, metadata)
}

func promptMetadataFromContext(ctx context.Context) (PromptMetadata, bool) {
	metadata, ok := ctx.Value(promptMetadataContextKey{}).(PromptMetadata)
	return metadata, ok
}

type adapterLifecycleState uint8

const (
	adapterLifecycleNew adapterLifecycleState = iota
	adapterLifecycleStarting
	adapterLifecycleRunning
	adapterLifecycleStopping
	adapterLifecycleStopped
)

type adapterStartupHooks struct {
	beforeCommandStart func()
	afterCommandStart  func()
}

type adapterSessionHooks struct {
	beforeCreationCommit func()
	afterCreationCommit  func()
}

type adapterCloseHooks struct {
	afterLifecycleFence func()
}

type sessionCreationState uint8

const (
	sessionNotStarted sessionCreationState = iota
	sessionCreating
	sessionCreated
	sessionCreationUnknown
)

type promptState uint8

const (
	promptIdle promptState = iota
	promptActive
	promptTerminalityUnknown
)

// ProcessAdapter owns one codex-acp child and one typed connection.
type ProcessAdapter struct {
	mu        sync.Mutex
	sessionMu sync.Mutex

	state           adapterLifecycleState
	command         *exec.Cmd
	processGroupID  int
	stdin           io.WriteCloser
	conn            *sdk.ClientSideConnection
	client          sdk.Client
	recordedInput   *recordingWriter
	startupCancel   context.CancelFunc
	startupDone     chan struct{}
	startupComplete bool
	processDone     chan struct{}
	stdoutDrainDone chan struct{}
	stdoutDrainErr  error
	sessionID       string
	sessionState    sessionCreationState
	promptState     promptState
	promptSystemErr bool
	startupHooks    adapterStartupHooks
	sessionHooks    adapterSessionHooks
	closeHooks      adapterCloseHooks

	getProcessGroupID func(int) (int, error)
	waitCommand       func(*exec.Cmd) error
	wrapStdin         func(io.WriteCloser) io.WriteCloser
	resolveAgentSpec  func(agentprofile.Profile) (process.ChildSpec, error)
	newCommand        func(process.ChildSpec) *exec.Cmd

	closeOnce sync.Once
	closeErr  error
}

var _ Adapter = (*ProcessAdapter)(nil)

// NewProcessAdapter returns an unstarted ACP adapter.
func NewProcessAdapter() *ProcessAdapter {
	return &ProcessAdapter{
		getProcessGroupID: unix.Getpgid,
		waitCommand: func(command *exec.Cmd) error {
			return command.Wait()
		},
		resolveAgentSpec: process.AgentChildSpecForProfile,
		newCommand:       commandForAgentSpec,
	}
}

// Start launches codex-acp, initializes the typed connection, and authenticates
// the fixed gateway method before any session can be created.
func (a *ProcessAdapter) Start(
	ctx context.Context,
	config StartConfig,
) (CapabilitySnapshot, error) {
	startupCtx, err := a.beginStart(ctx)
	if err != nil {
		return CapabilitySnapshot{}, err
	}

	if err := validateProxyToken(config.ProxyToken); err != nil {
		return a.failStart(err)
	}
	recorder, err := trace.NewRedactingJSONLRecorder(
		config.Trace,
		redact.New(redact.Secrets{LiteLLMProxyToken: config.ProxyToken}),
	)
	if err != nil {
		return a.failStart(err)
	}
	spec, err := a.resolveAgentSpec(config.AgentProfile)
	if err != nil {
		return a.failStart(fmt.Errorf("resolve ACP child profile: %w", err))
	}
	spec, err = completeAgentSpec(config.AgentProfile, spec)
	if err != nil {
		return a.failStart(err)
	}

	command := a.newCommand(spec)
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if os.Geteuid() == 0 &&
		(spec.UID != uint32(os.Geteuid()) || spec.GID != uint32(os.Getegid())) {
		command.SysProcAttr.Credential = &syscall.Credential{
			Uid:    spec.UID,
			Gid:    spec.GID,
			Groups: []uint32{},
		}
	}
	if a.startupHooks.beforeCommandStart != nil {
		a.startupHooks.beforeCommandStart()
	}
	if a.isStopping() {
		return a.failStart(fmt.Errorf("ACP adapter startup was stopped"))
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return a.failStart(fmt.Errorf("open codex-acp stdin"))
	}
	if a.wrapStdin != nil {
		stdin = a.wrapStdin(stdin)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return a.failStart(fmt.Errorf("open codex-acp stdout"))
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return a.failStart(fmt.Errorf("start codex-acp child"))
	}
	processGroupID, processGroupErr := captureACPProcessGroup(
		command.Process.Pid,
		a.getProcessGroupID,
	)

	client := a.client
	if client == nil {
		client = &protocolClient{
			workspaceRoot:   workspaceRoot,
			onSessionUpdate: a.observeSessionUpdate,
		}
	}
	recordedInput := newRecordingWriter(
		stdin,
		recorder,
		directionClientToAgent,
	)
	sdkOutputReader, sdkOutputWriter := io.Pipe()
	connection := sdk.NewClientSideConnection(client, recordedInput, sdkOutputReader)
	stdoutDrain := make(chan error, 1)
	stdoutDrainDone := make(chan struct{})
	processDone := make(chan struct{})
	go func() {
		drainErr := pumpRecordedOutput(
			stdout,
			sdkOutputWriter,
			NewFrameWriter(recorder, directionAgentToClient),
		)
		close(stdoutDrainDone)
		stdoutDrain <- drainErr
	}()
	go func() {
		<-connection.Done()
		_ = sdkOutputReader.CloseWithError(io.ErrClosedPipe)
		_ = stdin.Close()
		recordedInput.close()
	}()
	go func() {
		drainErr := <-stdoutDrain
		recordedInput.close()
		waitErr := a.waitCommand(command)
		a.mu.Lock()
		a.stdoutDrainErr = drainErr
		a.mu.Unlock()
		_ = waitErr
		close(processDone)
	}()

	if a.startupHooks.afterCommandStart != nil {
		a.startupHooks.afterCommandStart()
	}

	a.mu.Lock()
	a.command = command
	a.processGroupID = processGroupID
	a.stdin = stdin
	a.conn = connection
	a.recordedInput = recordedInput
	a.processDone = processDone
	a.stdoutDrainDone = stdoutDrainDone
	a.mu.Unlock()
	if processGroupErr != nil {
		return a.failStart(fmt.Errorf("codex-acp child process group is unverified"))
	}
	if a.isStopping() {
		return a.failStart(fmt.Errorf("ACP adapter startup was stopped"))
	}

	initializeResponse, err := connection.Initialize(startupCtx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientCapabilities: sdk.ClientCapabilities{
			Fs: sdk.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Auth: sdk.AuthCapabilities{
				Meta: map[string]any{"gateway": true},
			},
		},
	})
	if err != nil {
		return a.failStart(fmt.Errorf("initialize ACP connection"))
	}
	if initializeResponse.ProtocolVersion != sdk.ProtocolVersionNumber {
		return a.failStart(fmt.Errorf(
			"unsupported ACP protocol version %d",
			initializeResponse.ProtocolVersion,
		))
	}
	if !hasAuthMethod(initializeResponse.AuthMethods, "gateway") {
		return a.failStart(fmt.Errorf("codex-acp did not advertise gateway authentication"))
	}
	if _, err := connection.Authenticate(startupCtx, sdk.AuthenticateRequest{
		MethodId: "gateway",
		Meta: map[string]any{
			"gateway": map[string]any{
				"baseUrl":      proxyBaseURL,
				"providerName": providerName,
				"headers": map[string]string{
					"Authorization": "Bearer " + config.ProxyToken,
				},
			},
		},
	}); err != nil {
		return a.failStart(fmt.Errorf("authenticate ACP gateway"))
	}
	if err := a.publishRunning(); err != nil {
		return a.failStart(err)
	}
	a.finishStartup()
	return CapabilitySnapshot{
		ProtocolVersion:      initializeResponse.ProtocolVersion,
		GatewayAuthenticated: true,
		FileSystemAdvertised: true,
	}, nil
}

func (a *ProcessAdapter) beginStart(ctx context.Context) (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != adapterLifecycleNew {
		return nil, fmt.Errorf("ACP adapter cannot be started")
	}
	a.state = adapterLifecycleStarting
	a.startupDone = make(chan struct{})
	startupCtx, cancel := context.WithCancel(ctx)
	a.startupCancel = cancel
	return startupCtx, nil
}

func (a *ProcessAdapter) publishRunning() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != adapterLifecycleStarting {
		return fmt.Errorf("ACP adapter startup was stopped")
	}
	a.state = adapterLifecycleRunning
	return nil
}

func (a *ProcessAdapter) finishStartup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startupComplete {
		return
	}
	a.startupComplete = true
	if a.startupCancel != nil {
		a.startupCancel()
	}
	close(a.startupDone)
}

func (a *ProcessAdapter) isStopping() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == adapterLifecycleStopping ||
		a.state == adapterLifecycleStopped
}

// NewSession creates the adapter's only ACP session.
func (a *ProcessAdapter) NewSession(ctx context.Context, cwd string) (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	if cwd != workspaceRoot {
		return "", fmt.Errorf("ACP session cwd must be %s", workspaceRoot)
	}
	a.mu.Lock()
	connection := a.conn
	if a.state != adapterLifecycleRunning || connection == nil {
		a.mu.Unlock()
		return "", fmt.Errorf("ACP adapter is not running")
	}
	a.mu.Unlock()
	if a.sessionHooks.beforeCreationCommit != nil {
		a.sessionHooks.beforeCreationCommit()
	}

	a.mu.Lock()
	if a.state != adapterLifecycleRunning ||
		a.conn == nil ||
		a.conn != connection {
		a.mu.Unlock()
		return "", fmt.Errorf("ACP adapter is not running")
	}
	switch a.sessionState {
	case sessionCreating:
		a.mu.Unlock()
		return "", fmt.Errorf("ACP session creation is already in progress")
	case sessionCreated:
		a.mu.Unlock()
		return "", fmt.Errorf("ACP session already exists")
	case sessionCreationUnknown:
		a.mu.Unlock()
		return "", fmt.Errorf("ACP session creation outcome is unknown; replace the adapter")
	}
	a.sessionState = sessionCreating
	a.mu.Unlock()
	if a.sessionHooks.afterCreationCommit != nil {
		a.sessionHooks.afterCreationCommit()
	}
	response, err := connection.NewSession(ctx, sdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []sdk.McpServer{},
	})
	if err != nil {
		a.markSessionCreationUnknown()
		return "", fmt.Errorf("create ACP session: creation outcome is unknown")
	}
	if response.SessionId == "" {
		a.markSessionCreationUnknown()
		return "", fmt.Errorf("create ACP session: creation outcome is unknown")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionState != sessionCreating ||
		a.state != adapterLifecycleRunning {
		a.sessionState = sessionCreationUnknown
		return "", fmt.Errorf("create ACP session: creation outcome is unknown")
	}
	a.sessionID = string(response.SessionId)
	a.sessionState = sessionCreated
	return string(response.SessionId), nil
}

func (a *ProcessAdapter) markSessionCreationUnknown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionState == sessionCreating {
		a.sessionState = sessionCreationUnknown
	}
}

// Prompt drives one AgentTurn through the existing ACP session.
func (a *ProcessAdapter) Prompt(
	ctx context.Context,
	sessionID string,
	message string,
) (PromptResult, error) {
	a.mu.Lock()
	connection := a.conn
	activeSessionID := a.sessionID
	running := a.state == adapterLifecycleRunning && connection != nil
	if !running {
		a.mu.Unlock()
		return PromptResult{}, fmt.Errorf("ACP adapter is not running")
	}
	if sessionID == "" || sessionID != activeSessionID {
		a.mu.Unlock()
		return PromptResult{}, fmt.Errorf("ACP prompt session does not match the active session")
	}
	if message == "" {
		a.mu.Unlock()
		return PromptResult{}, fmt.Errorf("ACP prompt message is required")
	}
	switch a.promptState {
	case promptActive:
		a.mu.Unlock()
		return PromptResult{}, fmt.Errorf("an ACP prompt is already active")
	case promptTerminalityUnknown:
		a.mu.Unlock()
		return PromptResult{}, fmt.Errorf("ACP prompt terminality is unknown; replace the adapter")
	}
	a.promptState = promptActive
	a.promptSystemErr = false
	a.mu.Unlock()

	request := sdk.PromptRequest{
		SessionId: sdk.SessionId(sessionID),
		Prompt:    []sdk.ContentBlock{sdk.TextBlock(message)},
	}
	if metadata, ok := promptMetadataFromContext(ctx); ok {
		request.Meta = map[string]any{
			"switchback": map[string]any{
				"request_id":        metadata.RequestID,
				"trace_id":          metadata.TraceID,
				"worker_session_id": metadata.WorkerSessionID,
				"worker_run_id":     metadata.WorkerRunID,
				"agent_turn_id":     metadata.AgentTurnID,
			},
		}
	}
	response, err := connection.Prompt(ctx, request)
	if err != nil {
		a.markPromptTerminalityUnknown()
		return PromptResult{}, fmt.Errorf("run ACP prompt: terminality is unknown")
	}
	if !isTerminalStopReason(response.StopReason) {
		a.markPromptTerminalityUnknown()
		return PromptResult{}, fmt.Errorf("run ACP prompt: terminality is unknown")
	}
	a.mu.Lock()
	systemError := a.promptSystemErr
	if a.promptState == promptActive {
		a.promptState = promptIdle
	}
	a.promptSystemErr = false
	a.mu.Unlock()
	if systemError {
		return PromptResult{}, fmt.Errorf("run ACP prompt: codex reported a system error")
	}
	return PromptResult{StopReason: response.StopReason}, nil
}

func (a *ProcessAdapter) markPromptTerminalityUnknown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.promptState == promptActive {
		a.promptState = promptTerminalityUnknown
		a.promptSystemErr = false
	}
}

func (a *ProcessAdapter) observeSessionUpdate(notification sdk.SessionNotification) {
	if !isCodexSystemError(notification) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.promptState == promptActive &&
		string(notification.SessionId) == a.sessionID {
		a.promptSystemErr = true
	}
}

func isCodexSystemError(notification sdk.SessionNotification) bool {
	info := notification.Update.SessionInfoUpdate
	if info == nil {
		return false
	}
	codex, ok := info.Meta["codex"].(map[string]any)
	if !ok {
		return false
	}
	threadStatus, ok := codex["threadStatus"].(map[string]any)
	if !ok {
		return false
	}
	status, ok := threadStatus["type"].(string)
	return ok && status == "systemError"
}

func isTerminalStopReason(reason sdk.StopReason) bool {
	switch reason {
	case sdk.StopReasonEndTurn,
		sdk.StopReasonMaxTokens,
		sdk.StopReasonMaxTurnRequests,
		sdk.StopReasonRefusal,
		sdk.StopReasonCancelled:
		return true
	default:
		return false
	}
}

// Close terminates and reaps the owned codex-acp child.
func (a *ProcessAdapter) Close(ctx context.Context) error {
	a.mu.Lock()
	lifecycleFenced := false
	switch a.state {
	case adapterLifecycleNew, adapterLifecycleStarting, adapterLifecycleRunning:
		a.state = adapterLifecycleStopping
		lifecycleFenced = true
	}
	if a.startupCancel != nil {
		a.startupCancel()
	}
	startupDone := a.startupDone
	a.mu.Unlock()
	if lifecycleFenced && a.closeHooks.afterLifecycleFence != nil {
		a.closeHooks.afterLifecycleFence()
	}

	a.closeOnce.Do(func() {
		if startupDone != nil {
			<-startupDone
		}
		a.closeErr = a.performClose(ctx)
		a.mu.Lock()
		a.state = adapterLifecycleStopped
		a.mu.Unlock()
	})
	return a.closeErr
}

func (a *ProcessAdapter) performClose(ctx context.Context) error {
	a.mu.Lock()
	stdin := a.stdin
	command := a.command
	processGroupID := a.processGroupID
	processDone := a.processDone
	a.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if command == nil || command.Process == nil {
		return nil
	}
	leaderPID := command.Process.Pid
	if processGroupID <= 0 || processGroupID != leaderPID {
		_ = unix.Kill(-leaderPID, syscall.SIGTERM)
		_ = command.Process.Signal(syscall.SIGTERM)
		if waitForACPProcessExit(
			ctx,
			processDone,
			leaderPID,
			leaderPID,
			acpShutdownGrace,
		) {
			return a.processCompletionError()
		}
		_ = unix.Kill(-leaderPID, syscall.SIGKILL)
		_ = command.Process.Kill()
		if waitForACPProcessExit(
			context.Background(),
			processDone,
			leaderPID,
			leaderPID,
			acpKillGrace,
		) {
			return a.processCompletionError()
		}
		return fmt.Errorf("close ACP child: termination could not be confirmed")
	}

	_ = signalACPProcessGroup(processGroupID, syscall.SIGTERM)
	if waitForACPProcessExit(
		ctx,
		processDone,
		leaderPID,
		processGroupID,
		acpShutdownGrace,
	) {
		return a.processCompletionError()
	}
	_ = signalACPProcessGroup(processGroupID, syscall.SIGKILL)
	if waitForACPProcessExit(
		context.Background(),
		processDone,
		leaderPID,
		processGroupID,
		acpKillGrace,
	) {
		return a.processCompletionError()
	}
	return fmt.Errorf("close ACP child: termination could not be confirmed")
}

func (a *ProcessAdapter) processCompletionError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stdoutDrainErr
}

func (a *ProcessAdapter) failStart(cause error) (CapabilitySnapshot, error) {
	a.finishStartup()
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = a.Close(closeContext)
	return CapabilitySnapshot{}, cause
}

func completeAgentSpec(
	profile agentprofile.Profile,
	spec process.ChildSpec,
) (process.ChildSpec, error) {
	wantBaseline, err := process.AgentChildSpecForProfile(profile)
	if err != nil {
		return process.ChildSpec{}, fmt.Errorf("ACP child profile is invalid")
	}
	if spec.Path != wantBaseline.Path ||
		len(spec.Args) != 0 ||
		spec.UID != wantBaseline.UID ||
		spec.GID != wantBaseline.GID ||
		!equalEnvironment(spec.Env, wantBaseline.Env) {
		return process.ChildSpec{}, fmt.Errorf("ACP child profile mapping is invalid")
	}
	spec.Args = []string{wantBaseline.Path}
	spec.Env = append(append([]string(nil), spec.Env...), fixedAgentEnvironment...)
	return spec, nil
}

func commandForAgentSpec(spec process.ChildSpec) *exec.Cmd {
	command := exec.Command(spec.Path)
	command.Args = append([]string(nil), spec.Args...)
	command.Env = append([]string(nil), spec.Env...)
	return command
}

func equalEnvironment(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotValues := make(map[string]string, len(got))
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return false
		}
		if _, exists := gotValues[key]; exists {
			return false
		}
		gotValues[key] = value
	}
	for _, entry := range want {
		key, value, _ := strings.Cut(entry, "=")
		if gotValues[key] != value {
			return false
		}
	}
	return true
}

func hasAuthMethod(methods []sdk.AuthMethod, want string) bool {
	for _, method := range methods {
		switch {
		case method.Agent != nil && method.Agent.Id == want:
			return true
		case method.EnvVar != nil && method.EnvVar.Id == want:
			return true
		case method.Terminal != nil && method.Terminal.Id == want:
			return true
		}
	}
	return false
}

func validateProxyToken(token string) error {
	if token == "" ||
		strings.TrimSpace(token) != token ||
		strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("ACP proxy token is invalid")
	}
	return nil
}

func captureACPProcessGroup(
	leaderPID int,
	getProcessGroupID func(int) (int, error),
) (int, error) {
	if leaderPID <= 0 || getProcessGroupID == nil {
		return 0, fmt.Errorf("codex-acp child process group is unverified")
	}
	processGroupID, err := getProcessGroupID(leaderPID)
	if err != nil || processGroupID <= 0 || processGroupID != leaderPID {
		return 0, fmt.Errorf("codex-acp child process group is unverified")
	}
	return processGroupID, nil
}

func signalACPProcessGroup(processGroupID int, signal syscall.Signal) error {
	if processGroupID <= 0 {
		return fmt.Errorf("codex-acp child process group is unverified")
	}
	err := unix.Kill(-processGroupID, signal)
	if err == nil || err == unix.ESRCH {
		return nil
	}
	return fmt.Errorf("signal codex-acp child process group")
}

func waitForACPProcessExit(
	ctx context.Context,
	processDone <-chan struct{},
	leaderPID int,
	processGroupID int,
	timeout time.Duration,
) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(acpProcessGroupPoll)
	defer ticker.Stop()
	for {
		if channelClosed(processDone) &&
			!acpProcessAlive(leaderPID, processGroupID) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

func channelClosed(channel <-chan struct{}) bool {
	if channel == nil {
		return false
	}
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func acpProcessAlive(leaderPID, processGroupID int) bool {
	target := leaderPID
	if processGroupID > 0 {
		target = -processGroupID
	}
	if target == 0 {
		return true
	}
	err := unix.Kill(target, 0)
	return err == nil || err != unix.ESRCH
}
