package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"golang.org/x/sys/unix"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/worker/internal/process"
)

const childMarker = "switchback-acp-test-child"
const childNoGatewayMarker = "switchback-acp-test-child-no-gateway"
const childHangMarker = "switchback-acp-test-child-hang"
const childDropSessionMarker = "switchback-acp-test-child-drop-session"
const childPromptGateMarker = "switchback-acp-test-child-prompt-gate"
const childPromptHangMarker = "switchback-acp-test-child-prompt-hang"
const childFinalFrameMarker = "switchback-acp-test-child-final-frame"
const childFastExitMarker = "switchback-acp-test-child-fast-exit"
const childDescendantGroupMarker = "switchback-acp-test-child-descendant-group"
const childDescendantLeafMarker = "switchback-acp-test-child-descendant-leaf"
const childAuthSecretErrorMarker = "switchback-acp-test-child-auth-secret-error"
const childSDKReaderStopsMarker = "switchback-acp-test-child-sdk-reader-stops"
const childBlockedOutboundDoneMarker = "switchback-acp-test-child-blocked-outbound-done"
const childSystemErrorTerminalityMarker = "switchback-acp-test-child-system-error-terminality"

func TestProcessAdapterDrivesTwoPromptsThroughOneSession(t *testing.T) {
	t.Parallel()

	var transcript bytes.Buffer
	spec := process.AgentChildSpec()
	spec.Path = os.Args[0]
	spec.Args = []string{
		os.Args[0],
		"-test.run=^TestACPChildProcess$",
		"--",
		childMarker,
	}
	spec.UID = uint32(os.Geteuid())
	spec.GID = uint32(os.Getegid())

	adapter := NewProcessAdapter()
	installTestCommand(adapter, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})

	snapshot, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        &transcript,
	})
	if err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	if snapshot.ProtocolVersion != sdk.ProtocolVersionNumber {
		t.Fatalf("protocol version = %d, want %d", snapshot.ProtocolVersion, sdk.ProtocolVersionNumber)
	}
	if !snapshot.GatewayAuthenticated || !snapshot.FileSystemAdvertised {
		t.Fatalf("capability snapshot = %+v", snapshot)
	}

	sessionID, err := adapter.NewSession(ctx, "/workspace")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if sessionID != "session-one" {
		t.Fatalf("session ID = %q, want session-one", sessionID)
	}
	for _, message := range []string{"first turn", "second turn"} {
		result, promptErr := adapter.Prompt(ctx, sessionID, message)
		if promptErr != nil {
			t.Fatalf("prompt %q: %v", message, promptErr)
		}
		if result.StopReason != sdk.StopReasonEndTurn {
			t.Fatalf("prompt %q stop reason = %q", message, result.StopReason)
		}
	}
	if _, err := adapter.NewSession(ctx, "/workspace"); err == nil {
		t.Fatal("second NewSession unexpectedly succeeded")
	}

	records := decodeTraceRecords(t, transcript.Bytes())
	if countRecords(records, "acp.raw_frame", "client_to_agent") != 9 {
		t.Fatalf("client_to_agent raw records = %d, want 9", countRecords(records, "acp.raw_frame", "client_to_agent"))
	}
	if countRecords(records, "acp.raw_frame", "agent_to_client") != 10 {
		t.Fatalf("agent_to_client raw records = %d, want 10", countRecords(records, "acp.raw_frame", "agent_to_client"))
	}
	if countRecords(records, "acp.unknown_update", "agent_to_client") != 1 {
		t.Fatalf("unknown update records = %d, want 1", countRecords(records, "acp.unknown_update", "agent_to_client"))
	}
	if bytes.Contains(transcript.Bytes(), []byte("short-proxy-token")) {
		t.Fatal("authoritative ACP trace exposed the LiteLLM proxy token")
	}
	if !bytes.Contains(transcript.Bytes(), []byte("[REDACTED:litellm_proxy_token]")) {
		t.Fatal("authoritative ACP trace omitted the proxy-token redaction marker")
	}
}

func TestProcessAdapterFailsClosedOnMatchingCodexSystemError(t *testing.T) {
	t.Parallel()

	var transcript bytes.Buffer
	adapter := startAdapterWithChild(t, &transcript, []string{
		childSystemErrorTerminalityMarker,
	})
	sessionID := createTestSession(t, adapter)

	_, err := adapter.Prompt(context.Background(), sessionID, "system error turn")
	if err == nil || !strings.Contains(err.Error(), "codex reported a system error") {
		t.Fatalf("matching systemError Prompt() error = %v, want sanitized terminal failure", err)
	}
	if strings.Contains(err.Error(), "upstream-secret") {
		t.Fatalf("matching systemError Prompt() exposed upstream detail: %v", err)
	}

	for _, message := range []string{
		"ordinary active status",
		"ordinary idle status",
		"other session system error",
		"next prompt after system error",
	} {
		result, promptErr := adapter.Prompt(context.Background(), sessionID, message)
		if promptErr != nil {
			t.Fatalf("Prompt(%q) error = %v", message, promptErr)
		}
		if result.StopReason != sdk.StopReasonEndTurn {
			t.Fatalf("Prompt(%q) stop reason = %q, want %q", message, result.StopReason, sdk.StopReasonEndTurn)
		}
	}

	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRPCMethod(records, directionAgentToClient, "session/update"); got != 4 {
		t.Fatalf("session/update raw records = %d, want 4", got)
	}
}

func TestAgentSpecRejectsPathAndArgOverridesAtLaunchBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*process.ChildSpec)
	}{
		{
			name: "arbitrary path",
			mutate: func(spec *process.ChildSpec) {
				spec.Path = "/tmp/client-selected-agent"
			},
		},
		{
			name: "replaced argv zero",
			mutate: func(spec *process.ChildSpec) {
				spec.Args = []string{"/tmp/replaced-argv-zero"}
			},
		},
		{
			name: "appended argument",
			mutate: func(spec *process.ChildSpec) {
				spec.Args = []string{spec.Path, "--client-selected-option"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := process.AgentChildSpec()
			test.mutate(&spec)
			if completed, err := completeAgentSpec(agentprofile.Codex, spec); err == nil {
				t.Fatalf("completeAgentSpec() = %+v, want fixed launch rejection", completed)
			}
		})
	}
}

func TestProfileMapsToExactExecutableAndArgvAtLaunchBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		profile agentprofile.Profile
		path    string
	}{
		{profile: agentprofile.Codex, path: "/opt/switchback/node_modules/.bin/codex-acp"},
		{profile: agentprofile.IntegrationFake, path: "/opt/switchback/bin/fake-acp"},
	}
	for _, test := range tests {
		t.Run(test.profile.String(), func(t *testing.T) {
			spec, err := process.AgentChildSpecForProfile(test.profile)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := completeAgentSpec(test.profile, spec)
			if err != nil {
				t.Fatalf("completeAgentSpec(%q) error = %v", test.profile, err)
			}
			if completed.Path != test.path ||
				len(completed.Args) != 1 ||
				completed.Args[0] != test.path {
				t.Fatalf(
					"completeAgentSpec(%q) path/argv = %q/%q, want %q/[%q]",
					test.profile,
					completed.Path,
					completed.Args,
					test.path,
					test.path,
				)
			}
		})
	}
}

func TestACPChildProcess(t *testing.T) {
	if len(os.Args) == 0 {
		t.Skip("only runs as the adapter child process")
	}
	var err error
	switch os.Args[len(os.Args)-1] {
	case childMarker:
		err = runACPChild()
	case childNoGatewayMarker:
		err = runACPChildWithoutGateway()
	case childHangMarker:
		err = runHangingACPChild(os.Args[len(os.Args)-2])
	case childDropSessionMarker:
		err = runACPChildDroppingSessionResponse(os.Args[len(os.Args)-2])
	case childPromptGateMarker:
		err = runACPChildWithPromptGate(
			os.Args[len(os.Args)-3],
			os.Args[len(os.Args)-2],
		)
	case childPromptHangMarker:
		err = runACPChildHangingPrompt(os.Args[len(os.Args)-2])
	case childFinalFrameMarker:
		err = runACPChildWithBufferedFinalFrame()
	case childFastExitMarker:
		err = validateChildEnvironment()
	case childDescendantGroupMarker:
		err = runACPChildWithDescendant(
			os.Args[len(os.Args)-3],
			os.Args[len(os.Args)-2],
		)
	case childDescendantLeafMarker:
		err = runDescendantLeaf(os.Args[len(os.Args)-2])
	case childAuthSecretErrorMarker:
		err = runACPChildWithSecretAuthError()
	case childSDKReaderStopsMarker:
		err = runACPChildAfterSDKReaderStops()
	case childBlockedOutboundDoneMarker:
		err = runACPChildWithBlockedOutboundWrite(os.Args[len(os.Args)-2])
	case childSystemErrorTerminalityMarker:
		err = runACPChildWithSystemErrorTerminality()
	default:
		t.Skip("only runs as the adapter child process")
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestProcessAdapterDoesNotExposeProviderOrSecretErrorText(t *testing.T) {
	adapter := NewProcessAdapter()
	installTestCommand(adapter, childSpec(t, childAuthSecretErrorMarker))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("Start() error = nil, want authentication failure")
	}
	for _, secret := range []string{
		"short-proxy-token",
		"raw-provider-secret",
	} {
		if strings.Contains(err.Error(), secret) ||
			strings.Contains(fmt.Sprintf("%+v", err), secret) ||
			strings.Contains(fmt.Sprintf("%#v", err), secret) {
			t.Fatalf("Start() error exposed secret %q: %v", secret, err)
		}
	}
}

func TestProcessAdapterDrainsBufferedFinalFrameBeforeWait(t *testing.T) {
	var transcript bytes.Buffer
	adapter := NewProcessAdapter()
	waitCalled := false
	adapter.waitCommand = func(command *exec.Cmd) error {
		adapter.mu.Lock()
		stdoutDrainDone := adapter.stdoutDrainDone
		adapter.mu.Unlock()
		if !channelClosed(stdoutDrainDone) {
			t.Error("Wait called before independent stdout drain proved EOF")
		}
		waitCalled = true
		return command.Wait()
	}
	startAdapterWithSpec(t, adapter, &transcript, childSpec(
		t,
		childFinalFrameMarker,
	))

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := adapter.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !waitCalled {
		t.Fatal("Close() returned without Wait/reap")
	}
	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRecords(records, "acp.unknown_update", directionAgentToClient); got != 1 {
		t.Fatalf("buffered final unknown updates = %d, want one before Close returns", got)
	}
}

func TestProcessAdapterDrainsStdoutAfterSDKReaderStops(t *testing.T) {
	const tailPayload = "tail_after_sdk_done"

	transcript := newSynchronizedTraceBuffer(tailPayload)
	sessionUpdatesEntered := make(chan struct{})
	releaseSessionUpdates := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpdates := func() {
		releaseOnce.Do(func() {
			close(releaseSessionUpdates)
		})
	}
	t.Cleanup(releaseUpdates)

	adapter := NewProcessAdapter()
	adapter.client = &blockingSessionUpdateClient{
		protocolClient: &protocolClient{workspaceRoot: workspaceRoot},
		entered:        sessionUpdatesEntered,
		release:        releaseSessionUpdates,
	}
	waitEntered := make(chan struct{})
	var waitCalled atomic.Bool
	adapter.waitCommand = func(command *exec.Cmd) error {
		waitCalled.Store(true)
		close(waitEntered)
		return command.Wait()
	}
	startAdapterWithSpec(
		t,
		adapter,
		transcript,
		childSpec(t, childSDKReaderStopsMarker),
	)

	select {
	case <-sessionUpdatesEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("SDK did not begin processing the blocked session update")
	}
	adapter.mu.Lock()
	connection := adapter.conn
	adapter.mu.Unlock()
	if connection == nil {
		t.Fatal("SDK connection was not published")
	}
	select {
	case <-connection.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("SDK reader did not stop after its notification queue overflowed")
	}
	select {
	case <-waitEntered:
		t.Fatal("Wait was called when SDK Done closed, before stdout EOF")
	case <-transcript.tailSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("stdout pump did not retain the child tail after SDK reader shutdown")
	}
	if waitCalled.Load() {
		t.Fatal("Wait was called while the child still held stdout open")
	}

	releaseUpdates()
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := adapter.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !waitCalled.Load() {
		t.Fatal("Close() returned before Wait reaped the child")
	}
	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRawFramesContaining(
		records,
		directionAgentToClient,
		tailPayload,
	); got != 1 {
		t.Fatalf("tail raw frames = %d, want one after SDK reader shutdown", got)
	}
}

func TestProcessAdapterDoneUnblocksBlockedOutboundWriteBeforeReap(t *testing.T) {
	releasePath := filepath.Join(t.TempDir(), "release-overflow")
	var transcript bytes.Buffer
	sessionUpdatesEntered := make(chan struct{})
	releaseSessionUpdates := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpdates := func() {
		releaseOnce.Do(func() {
			close(releaseSessionUpdates)
		})
	}
	t.Cleanup(releaseUpdates)

	adapter := NewProcessAdapter()
	var blockedInput *closeUnblocksWriter
	adapter.wrapStdin = func(stdin io.WriteCloser) io.WriteCloser {
		blockedInput = newCloseUnblocksWriter(stdin)
		return blockedInput
	}
	adapter.client = &blockingSessionUpdateClient{
		protocolClient: &protocolClient{workspaceRoot: workspaceRoot},
		entered:        sessionUpdatesEntered,
		release:        releaseSessionUpdates,
	}
	waitEntered := make(chan struct{})
	var waitBeforeDrain atomic.Bool
	adapter.waitCommand = func(command *exec.Cmd) error {
		adapter.mu.Lock()
		stdoutDrainDone := adapter.stdoutDrainDone
		adapter.mu.Unlock()
		if !channelClosed(stdoutDrainDone) {
			waitBeforeDrain.Store(true)
		}
		close(waitEntered)
		return command.Wait()
	}
	startAdapterWithSpec(
		t,
		adapter,
		&transcript,
		childSpec(t, releasePath, childBlockedOutboundDoneMarker),
	)
	sessionID := createTestSession(t, adapter)

	adapter.mu.Lock()
	connection := adapter.conn
	recordedInput := adapter.recordedInput
	processDone := adapter.processDone
	adapter.mu.Unlock()
	if connection == nil || recordedInput == nil || processDone == nil {
		t.Fatal("adapter did not publish connection lifecycle resources")
	}
	if blockedInput == nil {
		t.Fatal("adapter did not install the deterministic blocked stdin")
	}
	blockedInput.block.Store(true)

	promptResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := adapter.Prompt(ctx, sessionID, strings.Repeat("x", 3<<20))
		promptResult <- err
	}()

	select {
	case <-blockedInput.writeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("outbound SDK write did not enter the blocked stdin")
	}
	if recordedInput.mu.TryLock() {
		recordedInput.mu.Unlock()
		t.Fatal("blocked outbound SDK write did not retain the recording writer lock")
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release child overflow: %v", err)
	}

	select {
	case <-sessionUpdatesEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("SDK did not begin processing the blocked session update")
	}
	select {
	case <-connection.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("SDK reader did not stop after notification overflow")
	}
	releaseUpdates()

	select {
	case err := <-promptResult:
		if err == nil {
			t.Fatal("blocked Prompt() unexpectedly succeeded after connection shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection Done did not unblock the outbound SDK write")
	}
	select {
	case <-waitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("stdout drain and Wait did not complete after connection Done")
	}
	select {
	case <-processDone:
	case <-time.After(3 * time.Second):
		t.Fatal("child was not reaped after stdout reached EOF")
	}
	if waitBeforeDrain.Load() {
		t.Fatal("Wait entered before the stdout pump reached EOF")
	}
}

func TestProcessAdapterCapturesProcessGroupBeforeWaitOnFastExit(t *testing.T) {
	adapter := NewProcessAdapter()
	installTestCommand(adapter, childSpec(t, childFastExitMarker))
	captured := false
	waitCalled := false
	adapter.getProcessGroupID = func(pid int) (int, error) {
		captured = true
		return unix.Getpgid(pid)
	}
	adapter.waitCommand = func(command *exec.Cmd) error {
		if !captured {
			t.Error("Wait started before process group capture completed")
		}
		waitCalled = true
		return command.Wait()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        &bytes.Buffer{},
	}); err == nil {
		t.Fatal("Start() succeeded after the child exited before initialize")
	}
	if !captured {
		t.Fatal("fast-exit child process group was never captured")
	}
	if !waitCalled {
		t.Fatal("fast-exit child was not reaped")
	}
}

func TestProcessAdapterKillsVerifiedProcessGroupDescendants(t *testing.T) {
	root := t.TempDir()
	parentPIDPath := filepath.Join(root, "parent.pid")
	descendantPIDPath := filepath.Join(root, "descendant.pid")
	adapter := NewProcessAdapter()
	startAdapterWithSpec(t, adapter, &bytes.Buffer{}, childSpec(
		t,
		parentPIDPath,
		descendantPIDPath,
		childDescendantGroupMarker,
	))
	parentPID, parentExists := readChildPID(t, parentPIDPath)
	descendantPID, descendantExists := readChildPID(t, descendantPIDPath)
	if !parentExists || !descendantExists {
		t.Fatalf("process group PIDs exist = parent:%t descendant:%t", parentExists, descendantExists)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	if err := adapter.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("Close() elapsed = %s, want internal TERM/KILL bounds before caller deadline", elapsed)
	}
	waitForProcessGone(t, parentPID)
	waitForProcessGone(t, descendantPID)
}

func TestProcessAdapterSerializesPromptsAndAllowsNextAfterTerminal(t *testing.T) {
	var transcript bytes.Buffer
	root := t.TempDir()
	receivedPath := filepath.Join(root, "prompt.received")
	releasePath := filepath.Join(root, "prompt.release")
	adapter := startAdapterWithChild(t, &transcript, []string{
		receivedPath,
		releasePath,
		childPromptGateMarker,
	})
	sessionID := createTestSession(t, adapter)

	firstResult := make(chan error, 1)
	go func() {
		_, err := adapter.Prompt(context.Background(), sessionID, "first turn")
		firstResult <- err
	}()
	waitForPath(t, receivedPath)

	secondCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := adapter.Prompt(secondCtx, sessionID, "overlapping turn"); err == nil ||
		!strings.Contains(err.Error(), "already active") {
		t.Fatalf("concurrent Prompt() error = %v, want local active-turn rejection", err)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release first prompt: %v", err)
	}
	if err := <-firstResult; err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
	if _, err := adapter.Prompt(context.Background(), sessionID, "next turn"); err != nil {
		t.Fatalf("Prompt() after proven terminal result error = %v", err)
	}

	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRPCMethod(records, directionClientToAgent, "session/prompt"); got != 2 {
		t.Fatalf("session/prompt raw frames = %d, want first and post-terminal turns only", got)
	}
}

func TestProcessAdapterBlocksPromptsAfterCancellationMakesTerminalityUnknown(t *testing.T) {
	var transcript bytes.Buffer
	receivedPath := filepath.Join(t.TempDir(), "prompt.received")
	adapter := startAdapterWithChild(t, &transcript, []string{
		receivedPath,
		childPromptHangMarker,
	})
	sessionID := createTestSession(t, adapter)

	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	promptResult := make(chan error, 1)
	go func() {
		_, err := adapter.Prompt(promptCtx, sessionID, "cancelled turn")
		promptResult <- err
	}()
	waitForPath(t, receivedPath)
	cancelPrompt()
	if err := <-promptResult; err == nil {
		t.Fatal("cancelled Prompt() error = nil")
	}

	nextCtx, cancelNext := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelNext()
	if _, err := adapter.Prompt(nextCtx, sessionID, "unsafe next turn"); err == nil ||
		!strings.Contains(err.Error(), "terminality is unknown") {
		t.Fatalf("post-cancel Prompt() error = %v, want terminality_unknown rejection", err)
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClose()
	if err := adapter.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRPCMethod(records, directionClientToAgent, "session/prompt"); got != 1 {
		t.Fatalf("session/prompt raw frames = %d, want no prompt after cancellation", got)
	}
}

func TestProcessAdapterDoesNotRetryAmbiguousSessionCreation(t *testing.T) {
	var transcript bytes.Buffer
	receivedPath := filepath.Join(t.TempDir(), "session-new.received")
	spec := process.AgentChildSpec()
	spec.Path = os.Args[0]
	spec.Args = []string{
		os.Args[0],
		"-test.run=^TestACPChildProcess$",
		"--",
		receivedPath,
		childDropSessionMarker,
	}
	spec.UID = uint32(os.Geteuid())
	spec.GID = uint32(os.Getegid())

	adapter := NewProcessAdapter()
	installTestCommand(adapter, spec)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        &transcript,
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := adapter.NewSession(ctx, workspaceRoot); err == nil {
		t.Fatal("NewSession() error = nil after child dropped the response")
	}
	if _, err := os.Stat(receivedPath); err != nil {
		t.Fatalf("child did not prove session/new receipt: %v", err)
	}
	if _, err := adapter.NewSession(ctx, workspaceRoot); err == nil ||
		!strings.Contains(err.Error(), "creation outcome is unknown") {
		t.Fatalf("second NewSession() error = %v, want local creation_unknown rejection", err)
	}

	records := decodeTraceRecords(t, transcript.Bytes())
	if got := countRPCMethod(records, directionClientToAgent, "session/new"); got != 1 {
		t.Fatalf("session/new raw frames = %d, want exactly one after ambiguous result", got)
	}
}

func TestProcessAdapterFencesSessionCreationAgainstClose(t *testing.T) {
	t.Run("validates cwd before lifecycle", func(t *testing.T) {
		adapter := NewProcessAdapter()
		if _, err := adapter.NewSession(context.Background(), "/outside"); err == nil ||
			!strings.Contains(err.Error(), "cwd must be") {
			t.Fatalf("NewSession() error = %v, want cwd rejection before lifecycle", err)
		}
	})

	t.Run("close wins before creation commit", func(t *testing.T) {
		var transcript bytes.Buffer
		adapter := NewProcessAdapter()
		reachedCommit := make(chan struct{})
		releaseCommit := make(chan struct{})
		adapter.sessionHooks.beforeCreationCommit = func() {
			close(reachedCommit)
			<-releaseCommit
		}
		startAdapterWithSpec(t, adapter, &transcript, childSpec(t, childMarker))

		sessionResult := make(chan error, 1)
		go func() {
			_, err := adapter.NewSession(context.Background(), workspaceRoot)
			sessionResult <- err
		}()
		<-reachedCommit

		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		close(releaseCommit)
		if err := <-sessionResult; err == nil ||
			!strings.Contains(err.Error(), "not running") {
			t.Fatalf("NewSession() error = %v, want shutdown fence rejection", err)
		}

		records := decodeTraceRecords(t, transcript.Bytes())
		if got := countRPCMethod(records, directionClientToAgent, "session/new"); got != 0 {
			t.Fatalf("session/new raw frames = %d, want none after Close won", got)
		}
	})

	t.Run("session creation wins before close", func(t *testing.T) {
		adapter := NewProcessAdapter()
		creationCommitted := make(chan struct{})
		releaseSend := make(chan struct{})
		closeFenced := make(chan struct{})
		adapter.sessionHooks.afterCreationCommit = func() {
			close(creationCommitted)
			<-releaseSend
		}
		adapter.closeHooks.afterLifecycleFence = func() {
			close(closeFenced)
		}
		startAdapterWithSpec(t, adapter, &bytes.Buffer{}, childSpec(t, childMarker))

		sessionResult := make(chan error, 1)
		go func() {
			_, err := adapter.NewSession(context.Background(), workspaceRoot)
			sessionResult <- err
		}()
		<-creationCommitted

		closeResult := make(chan error, 1)
		go func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			closeResult <- adapter.Close(closeCtx)
		}()
		<-closeFenced
		close(releaseSend)

		<-sessionResult
		if err := <-closeResult; err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		adapter.mu.Lock()
		state := adapter.sessionState
		adapter.mu.Unlock()
		if state == sessionNotStarted {
			t.Fatal("NewSession() lost its committed linearization before Close")
		}
	})
}

func childSpec(t *testing.T, childArgs ...string) process.ChildSpec {
	t.Helper()
	spec := process.AgentChildSpec()
	spec.Path = os.Args[0]
	spec.Args = append([]string{
		os.Args[0],
		"-test.run=^TestACPChildProcess$",
		"--",
	}, childArgs...)
	spec.UID = uint32(os.Geteuid())
	spec.GID = uint32(os.Getegid())
	return spec
}

func startAdapterWithSpec(
	t *testing.T,
	adapter *ProcessAdapter,
	transcript io.Writer,
	spec process.ChildSpec,
) {
	t.Helper()
	installTestCommand(adapter, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        transcript,
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
}

func installTestCommand(adapter *ProcessAdapter, testSpec process.ChildSpec) {
	adapter.newCommand = func(trustedSpec process.ChildSpec) *exec.Cmd {
		command := exec.Command(testSpec.Path)
		command.Args = append([]string(nil), testSpec.Args...)
		command.Env = append([]string(nil), trustedSpec.Env...)
		return command
	}
}

func startAdapterWithChild(
	t *testing.T,
	transcript *bytes.Buffer,
	childArgs []string,
) *ProcessAdapter {
	t.Helper()
	adapter := NewProcessAdapter()
	startAdapterWithSpec(t, adapter, transcript, childSpec(t, childArgs...))
	return adapter
}

func createTestSession(t *testing.T, adapter *ProcessAdapter) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sessionID, err := adapter.NewSession(ctx, workspaceRoot)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return sessionID
}

func TestProcessAdapterCloseWaitsForEveryStartupPhase(t *testing.T) {
	tests := []struct {
		name          string
		installHook   func(*ProcessAdapter, func())
		childExpected bool
	}{
		{
			name: "before command start",
			installHook: func(adapter *ProcessAdapter, hook func()) {
				adapter.startupHooks.beforeCommandStart = hook
			},
		},
		{
			name: "after child start before publication",
			installHook: func(adapter *ProcessAdapter, hook func()) {
				adapter.startupHooks.afterCommandStart = hook
			},
			childExpected: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			spec := process.AgentChildSpec()
			spec.Path = os.Args[0]
			spec.Args = []string{
				os.Args[0],
				"-test.run=^TestACPChildProcess$",
				"--",
				pidPath,
				childHangMarker,
			}
			spec.UID = uint32(os.Geteuid())
			spec.GID = uint32(os.Getegid())

			reached := make(chan struct{})
			release := make(chan struct{})
			adapter := NewProcessAdapter()
			installTestCommand(adapter, spec)
			test.installHook(adapter, func() {
				close(reached)
				<-release
			})
			startResult := make(chan error, 1)
			go func() {
				_, err := adapter.Start(context.Background(), StartConfig{
					AgentProfile: agentprofile.Codex,
					ProxyToken:   "short-proxy-token",
					Trace:        &bytes.Buffer{},
				})
				startResult <- err
			}()
			<-reached
			childPID := 0
			if test.childExpected {
				var exists bool
				childPID, exists = readChildPID(t, pidPath)
				if !exists {
					t.Fatal("child did not prove it started before shutdown")
				}
			}

			closeResult := make(chan error, 1)
			go func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				closeResult <- adapter.Close(closeCtx)
			}()
			select {
			case err := <-closeResult:
				t.Fatalf("Close() returned before startup phase completed: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(release)

			if err := <-startResult; err == nil {
				t.Fatal("Start() succeeded after Close began")
			}
			if err := <-closeResult; err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if _, err := adapter.Start(context.Background(), StartConfig{}); err == nil {
				t.Fatal("Start() succeeded after Close completed")
			}

			if !test.childExpected {
				if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
					t.Fatalf("child started before the pre-command shutdown: %v", err)
				}
				return
			}
			waitForProcessGone(t, childPID)
		})
	}
}

func TestProcessAdapterRejectsMissingGatewayMethod(t *testing.T) {
	t.Parallel()

	var transcript bytes.Buffer
	spec := process.AgentChildSpec()
	spec.Path = os.Args[0]
	spec.Args = []string{
		os.Args[0],
		"-test.run=^TestACPChildProcess$",
		"--",
		childNoGatewayMarker,
	}
	spec.UID = uint32(os.Geteuid())
	spec.GID = uint32(os.Getegid())

	adapter := NewProcessAdapter()
	installTestCommand(adapter, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := adapter.Start(ctx, StartConfig{
		AgentProfile: agentprofile.Codex,
		ProxyToken:   "short-proxy-token",
		Trace:        &transcript,
	})
	if err == nil || !strings.Contains(err.Error(), "did not advertise gateway") {
		t.Fatalf("start error = %v, want missing gateway rejection", err)
	}
}

func TestGatewayAuthMethodSupportsEverySDKUnionVariant(t *testing.T) {
	t.Parallel()

	methods := []sdk.AuthMethod{
		{Agent: &sdk.AuthMethodAgent{Id: "gateway", Name: "agent"}},
		{EnvVar: &sdk.AuthMethodEnvVarInline{Id: "gateway", Name: "env"}},
		{Terminal: &sdk.AuthMethodTerminalInline{Id: "gateway", Name: "terminal"}},
	}
	for index, method := range methods {
		if !hasAuthMethod([]sdk.AuthMethod{method}, "gateway") {
			t.Fatalf("union variant %d did not expose gateway ID", index)
		}
	}
	if hasAuthMethod([]sdk.AuthMethod{{Agent: &sdk.AuthMethodAgent{Id: "other"}}}, "gateway") {
		t.Fatal("non-gateway method matched")
	}
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func runACPChild() error {
	if err := validateChildEnvironment(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	wantMethods := []string{
		"initialize",
		"authenticate",
		"session/new",
		"session/prompt",
		"session/prompt",
	}
	for index, wantMethod := range wantMethods {
		if !scanner.Scan() {
			return fmt.Errorf("read %s request: %w", wantMethod, scanner.Err())
		}
		var frame rpcFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return fmt.Errorf("decode %s request: %w", wantMethod, err)
		}
		if frame.Method != wantMethod {
			return fmt.Errorf("request %d method = %q, want %q", index, frame.Method, wantMethod)
		}
		if err := validateChildRequest(index, frame.Params); err != nil {
			return err
		}

		switch wantMethod {
		case "initialize":
			if err := writeRPCResult(writer, frame.ID, map[string]any{
				"protocolVersion":   sdk.ProtocolVersionNumber,
				"agentCapabilities": map[string]any{},
				"authMethods": []map[string]any{{
					"id":   "gateway",
					"name": "Switchback Gateway",
				}},
			}); err != nil {
				return err
			}
		case "authenticate":
			if err := writeRPCResult(writer, frame.ID, map[string]any{}); err != nil {
				return err
			}
		case "session/new":
			if err := writeRPCResult(writer, frame.ID, map[string]any{"sessionId": "session-one"}); err != nil {
				return err
			}
		case "session/prompt":
			if index == 3 {
				if err := writeRPCNotification(writer, "session/update", map[string]any{
					"sessionId": "session-one",
					"update": map[string]any{
						"sessionUpdate": "future_optional_update",
						"payload":       "retain this",
					},
				}); err != nil {
					return err
				}
			}
			if index == 4 {
				callbacks := []struct {
					id              int
					method          string
					params          any
					cancelledResult bool
				}{
					{
						id:     901,
						method: "session/request_permission",
						params: map[string]any{
							"sessionId": "session-one",
							"options":   []any{},
							"toolCall":  map[string]any{"toolCallId": "permission-call"},
						},
						cancelledResult: true,
					},
					{
						id:     902,
						method: "terminal/create",
						params: map[string]any{
							"sessionId": "session-one",
							"command":   "echo",
						},
					},
					{
						id:     903,
						method: "fs/read_text_file",
						params: map[string]any{
							"sessionId": "session-one",
							"path":      "relative.txt",
						},
					},
					{
						id:     904,
						method: "fs/write_text_file",
						params: map[string]any{
							"sessionId": "session-one",
							"path":      "/workspace-copy/file.txt",
							"content":   "blocked",
						},
					},
				}
				for _, callback := range callbacks {
					if err := writeRPCRequest(writer, callback.id, callback.method, callback.params); err != nil {
						return err
					}
					if err := validateCallbackResponse(scanner, callback.id, callback.cancelledResult); err != nil {
						return err
					}
				}
			}
			if err := writeRPCResult(writer, frame.ID, map[string]any{"stopReason": "end_turn"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func runACPChildWithPromptGate(receivedPath, releasePath string) error {
	scanner, writer, err := startACPChildSession()
	if err != nil {
		return err
	}
	promptID, err := receivePrompt(scanner, writer, receivedPath, true)
	if err != nil {
		return err
	}
	if err := waitForPathInChild(releasePath); err != nil {
		return err
	}
	if err := writeRPCResult(writer, promptID, map[string]any{
		"stopReason": "end_turn",
	}); err != nil {
		return err
	}
	_, err = receivePrompt(scanner, writer, "", false)
	return err
}

func runACPChildWithSystemErrorTerminality() error {
	scanner, writer, err := startACPChildSession()
	if err != nil {
		return err
	}
	updates := []struct {
		sessionID string
		status    string
	}{
		{sessionID: "session-one", status: "systemError"},
		{sessionID: "session-one", status: "active"},
		{sessionID: "session-one", status: "idle"},
		{sessionID: "other-session", status: "systemError"},
		{},
	}
	for _, update := range updates {
		promptID, receiveErr := receivePrompt(scanner, writer, "", true)
		if receiveErr != nil {
			return receiveErr
		}
		if update.status != "" {
			if err := writeRPCNotification(writer, "session/update", map[string]any{
				"sessionId": update.sessionID,
				"update": map[string]any{
					"sessionUpdate": "session_info_update",
					"_meta": map[string]any{
						"codex": map[string]any{
							"threadStatus": map[string]any{
								"type":    update.status,
								"message": "upstream-secret",
							},
						},
					},
				},
			}); err != nil {
				return err
			}
		}
		if err := writeRPCResult(writer, promptID, map[string]any{
			"stopReason": "end_turn",
		}); err != nil {
			return err
		}
	}
	return nil
}

func runACPChildWithSecretAuthError() error {
	scanner, writer, err := startACPChildAuthenticatedUntil("initialize")
	if err != nil {
		return err
	}
	if !scanner.Scan() {
		return fmt.Errorf("read authenticate request: %w", scanner.Err())
	}
	var frame rpcFrame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		return fmt.Errorf("decode authenticate request: %w", err)
	}
	if frame.Method != "authenticate" {
		return fmt.Errorf("method = %q, want authenticate", frame.Method)
	}
	return writeRPCError(
		writer,
		frame.ID,
		"short-proxy-token raw-provider-secret",
	)
}

func runACPChildAfterSDKReaderStops() error {
	scanner, writer, err := startACPChildAuthenticated()
	if err != nil {
		return err
	}
	for index := 0; index < 1100; index++ {
		if err := writeRPCNotification(writer, "session/update", map[string]any{
			"sessionId": "session-one",
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": fmt.Sprintf("queued-%04d", index),
				},
			},
		}); err != nil {
			return err
		}
	}
	if scanner.Scan() {
		return fmt.Errorf("unexpected request after notification overflow")
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("wait for stdin fence: %w", err)
	}
	if err := writeRPCNotification(writer, "session/update", map[string]any{
		"sessionId": "session-one",
		"update": map[string]any{
			"sessionUpdate": "future_tail_update",
			"payload":       "tail_after_sdk_done",
		},
	}); err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return nil
}

func runACPChildWithBlockedOutboundWrite(releasePath string) error {
	_, writer, err := startACPChildSession()
	if err != nil {
		return err
	}
	if err := waitForPathInChild(releasePath); err != nil {
		return err
	}
	for index := 0; index < 1100; index++ {
		if err := writeRPCNotification(writer, "session/update", map[string]any{
			"sessionId": "session-one",
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": fmt.Sprintf("blocked-write-%04d", index),
				},
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func runACPChildWithBufferedFinalFrame() error {
	scanner, writer, err := startACPChildAuthenticated()
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	stdinClosed := make(chan struct{})
	go func() {
		scanner.Scan()
		close(stdinClosed)
	}()
	select {
	case <-signals:
	case <-stdinClosed:
	}
	return writeRPCNotification(writer, "session/update", map[string]any{
		"sessionId": "session-one",
		"update": map[string]any{
			"sessionUpdate": "final_buffered_update",
			"payload":       strings.Repeat("x", 512<<10),
		},
	})
}

func runACPChildWithDescendant(parentPIDPath, descendantPIDPath string) error {
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(parentPIDPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write parent PID: %w", err)
	}
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestACPChildProcess$",
		"--",
		descendantPIDPath,
		childDescendantLeafMarker,
	)
	command.Env = os.Environ()
	if err := command.Start(); err != nil {
		return fmt.Errorf("start descendant: %w", err)
	}
	if _, _, err := startACPChildAuthenticated(); err != nil {
		return err
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runDescendantLeaf(pidPath string) error {
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write descendant PID: %w", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runACPChildHangingPrompt(receivedPath string) error {
	scanner, writer, err := startACPChildSession()
	if err != nil {
		return err
	}
	if _, err := receivePrompt(scanner, writer, receivedPath, true); err != nil {
		return err
	}
	for scanner.Scan() {
	}
	return scanner.Err()
}

func startACPChildSession() (*bufio.Scanner, *bufio.Writer, error) {
	scanner, writer, err := startACPChildAuthenticated()
	if err != nil {
		return nil, nil, err
	}
	if !scanner.Scan() {
		return nil, nil, fmt.Errorf("read session/new request: %w", scanner.Err())
	}
	var frame rpcFrame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		return nil, nil, fmt.Errorf("decode session/new request: %w", err)
	}
	if frame.Method != "session/new" {
		return nil, nil, fmt.Errorf("method = %q, want session/new", frame.Method)
	}
	if err := writeRPCResult(writer, frame.ID, map[string]any{
		"sessionId": "session-one",
	}); err != nil {
		return nil, nil, err
	}
	return scanner, writer, nil
}

func startACPChildAuthenticated() (*bufio.Scanner, *bufio.Writer, error) {
	return startACPChildAuthenticatedUntil("authenticate")
}

func startACPChildAuthenticatedUntil(
	lastMethod string,
) (*bufio.Scanner, *bufio.Writer, error) {
	if err := validateChildEnvironment(); err != nil {
		return nil, nil, err
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	for _, wantMethod := range []string{"initialize", "authenticate"} {
		if !scanner.Scan() {
			return nil, nil, fmt.Errorf("read %s request: %w", wantMethod, scanner.Err())
		}
		var frame rpcFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return nil, nil, fmt.Errorf("decode %s request: %w", wantMethod, err)
		}
		if frame.Method != wantMethod {
			return nil, nil, fmt.Errorf("method = %q, want %q", frame.Method, wantMethod)
		}
		var result any = map[string]any{}
		switch wantMethod {
		case "initialize":
			result = map[string]any{
				"protocolVersion":   sdk.ProtocolVersionNumber,
				"agentCapabilities": map[string]any{},
				"authMethods": []map[string]any{{
					"id":   "gateway",
					"name": "Switchback Gateway",
				}},
			}
		}
		if err := writeRPCResult(writer, frame.ID, result); err != nil {
			return nil, nil, err
		}
		if wantMethod == lastMethod {
			return scanner, writer, nil
		}
	}
	return nil, nil, fmt.Errorf("unsupported handshake boundary %q", lastMethod)
}

func receivePrompt(
	scanner *bufio.Scanner,
	writer *bufio.Writer,
	receivedPath string,
	deferResponse bool,
) (json.RawMessage, error) {
	if !scanner.Scan() {
		return nil, fmt.Errorf("read prompt request: %w", scanner.Err())
	}
	var frame rpcFrame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		return nil, fmt.Errorf("decode prompt request: %w", err)
	}
	if frame.Method != "session/prompt" {
		return nil, fmt.Errorf("method = %q, want session/prompt", frame.Method)
	}
	if receivedPath != "" {
		if err := os.WriteFile(receivedPath, frame.ID, 0o600); err != nil {
			return nil, fmt.Errorf("write prompt receipt: %w", err)
		}
	}
	if deferResponse {
		return append(json.RawMessage(nil), frame.ID...), nil
	}
	return nil, writeRPCResult(writer, frame.ID, map[string]any{
		"stopReason": "end_turn",
	})
}

func waitForPathInChild(path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for release path")
}

func runACPChildDroppingSessionResponse(receivedPath string) error {
	if err := validateChildEnvironment(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	for index, wantMethod := range []string{"initialize", "authenticate", "session/new"} {
		if !scanner.Scan() {
			return fmt.Errorf("read %s request: %w", wantMethod, scanner.Err())
		}
		var frame rpcFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return fmt.Errorf("decode %s request: %w", wantMethod, err)
		}
		if frame.Method != wantMethod {
			return fmt.Errorf("request %d method = %q, want %q", index, frame.Method, wantMethod)
		}
		switch wantMethod {
		case "initialize":
			if err := writeRPCResult(writer, frame.ID, map[string]any{
				"protocolVersion":   sdk.ProtocolVersionNumber,
				"agentCapabilities": map[string]any{},
				"authMethods": []map[string]any{{
					"id":   "gateway",
					"name": "Switchback Gateway",
				}},
			}); err != nil {
				return err
			}
		case "authenticate":
			if err := writeRPCResult(writer, frame.ID, map[string]any{}); err != nil {
				return err
			}
		case "session/new":
			return os.WriteFile(receivedPath, []byte("received"), 0o600)
		}
	}
	return nil
}

func runHangingACPChild(pidPath string) error {
	if err := validateChildEnvironment(); err != nil {
		return err
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write child PID: %w", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func readChildPID(t *testing.T, path string) (int, bool) {
	t.Helper()
	var content []byte
	var err error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		content, err = os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(content))
			if parseErr != nil {
				t.Fatalf("parse child PID %q: %v", content, parseErr)
			}
			return pid, true
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, false
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d still exists after Close returned", pid)
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func validateChildEnvironment() error {
	wantEnv := []string{
		`APP_SERVER_LOGS=/trace-spool/app-server`,
		`CODEX_CONFIG={"model":"switchback-codex","approval_policy":"never","sandbox_mode":"danger-full-access","web_search":"disabled"}`,
		`CODEX_HOME=/agent-state/codex`,
		`CODEX_PATH=/opt/switchback/node_modules/.bin/codex`,
		`SWITCHBACK_SPIKE_MODEL_BASE_URL=http://127.0.0.1:4000`,
		`HOME=/agent-state`,
		`INITIAL_AGENT_MODE=agent-full-access`,
		`NO_BROWSER=1`,
		`PATH=/usr/local/bin:/usr/bin:/bin`,
	}
	gotEnv := os.Environ()
	slices.Sort(gotEnv)
	slices.Sort(wantEnv)
	if !slices.Equal(gotEnv, wantEnv) {
		return fmt.Errorf("child environment = %q, want %q", gotEnv, wantEnv)
	}
	return nil
}

func runACPChildWithoutGateway() error {
	if err := validateChildEnvironment(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()
	if !scanner.Scan() {
		return fmt.Errorf("read initialize request: %w", scanner.Err())
	}
	var frame rpcFrame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		return fmt.Errorf("decode initialize request: %w", err)
	}
	if frame.Method != "initialize" {
		return fmt.Errorf("first method = %q, want initialize", frame.Method)
	}
	return writeRPCResult(writer, frame.ID, map[string]any{
		"protocolVersion":   sdk.ProtocolVersionNumber,
		"agentCapabilities": map[string]any{},
		"authMethods":       []any{},
	})
}

func validateChildRequest(index int, raw json.RawMessage) error {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("decode request %d params: %w", index, err)
	}
	switch index {
	case 0:
		capabilities, _ := params["clientCapabilities"].(map[string]any)
		fs, _ := capabilities["fs"].(map[string]any)
		auth, _ := capabilities["auth"].(map[string]any)
		meta, _ := auth["_meta"].(map[string]any)
		if fs["readTextFile"] != true || fs["writeTextFile"] != true || meta["gateway"] != true {
			return fmt.Errorf("initialize capabilities = %#v", capabilities)
		}
	case 1:
		meta, _ := params["_meta"].(map[string]any)
		gateway, _ := meta["gateway"].(map[string]any)
		headers, _ := gateway["headers"].(map[string]any)
		if params["methodId"] != "gateway" ||
			gateway["baseUrl"] != "http://127.0.0.1:4000/v1" ||
			gateway["providerName"] != "Switchback LiteLLM" ||
			len(headers) != 1 ||
			headers["Authorization"] != "Bearer short-proxy-token" {
			return fmt.Errorf("authenticate params = %#v", params)
		}
	case 2:
		servers, ok := params["mcpServers"].([]any)
		if params["cwd"] != "/workspace" || !ok || len(servers) != 0 {
			return fmt.Errorf("new session params = %#v", params)
		}
	case 3, 4:
		prompt, _ := params["prompt"].([]any)
		block, _ := prompt[0].(map[string]any)
		wantText := []string{"first turn", "second turn"}[index-3]
		if params["sessionId"] != "session-one" ||
			block["type"] != "text" ||
			block["text"] != wantText {
			return fmt.Errorf("prompt params = %#v", params)
		}
	}
	return nil
}

func writeRPCResult(writer *bufio.Writer, id json.RawMessage, result any) error {
	return writeRPCFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(writer *bufio.Writer, id json.RawMessage, message string) error {
	return writeRPCFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32603,
			"message": message,
		},
	})
}

func writeRPCNotification(writer *bufio.Writer, method string, params any) error {
	return writeRPCFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func writeRPCRequest(writer *bufio.Writer, id int, method string, params any) error {
	return writeRPCFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func validateCallbackResponse(
	scanner *bufio.Scanner,
	wantID int,
	cancelledResult bool,
) error {
	if !scanner.Scan() {
		return fmt.Errorf("read callback %d response: %w", wantID, scanner.Err())
	}
	var response rpcFrame
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return fmt.Errorf("decode callback %d response: %w", wantID, err)
	}
	var gotID int
	if err := json.Unmarshal(response.ID, &gotID); err != nil || gotID != wantID {
		return fmt.Errorf("callback response ID = %s, want %d", response.ID, wantID)
	}
	if cancelledResult {
		var result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return fmt.Errorf("decode permission response: %w", err)
		}
		if result.Outcome.Outcome != "cancelled" || len(response.Error) != 0 {
			return fmt.Errorf("permission response = %s, error = %s", response.Result, response.Error)
		}
		return nil
	}
	if len(response.Error) == 0 || string(response.Error) == "null" {
		return fmt.Errorf("callback %d unexpectedly succeeded: %s", wantID, response.Result)
	}
	return nil
}

func writeRPCFrame(writer *bufio.Writer, frame any) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}

func decodeTraceRecords(t *testing.T, content []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64<<10), maxRawFrameBytes+(64<<10))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode trace record %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace records: %v", err)
	}
	return records
}

func countRecords(records []map[string]any, recordType, direction string) int {
	count := 0
	for _, record := range records {
		if record["type"] == recordType && record["direction"] == direction {
			count++
		}
	}
	return count
}

func countRPCMethod(records []map[string]any, direction, method string) int {
	count := 0
	for _, record := range records {
		if record["type"] != "acp.raw_frame" || record["direction"] != direction {
			continue
		}
		frame, _ := record["frame"].(string)
		var envelope rpcFrame
		if json.Unmarshal([]byte(frame), &envelope) == nil && envelope.Method == method {
			count++
		}
	}
	return count
}

func countRawFramesContaining(
	records []map[string]any,
	direction string,
	content string,
) int {
	count := 0
	for _, record := range records {
		frame, _ := record["frame"].(string)
		if record["type"] == "acp.raw_frame" &&
			record["direction"] == direction &&
			strings.Contains(frame, content) {
			count++
		}
	}
	return count
}

type blockingSessionUpdateClient struct {
	*protocolClient
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *blockingSessionUpdateClient) SessionUpdate(
	ctx context.Context,
	_ sdk.SessionNotification,
) error {
	c.once.Do(func() {
		close(c.entered)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return nil
	}
}

type closeUnblocksWriter struct {
	target       io.WriteCloser
	block        atomic.Bool
	writeEntered chan struct{}
	unblocked    chan struct{}
	enterOnce    sync.Once
	closeOnce    sync.Once
	closeErr     error
}

func newCloseUnblocksWriter(target io.WriteCloser) *closeUnblocksWriter {
	return &closeUnblocksWriter{
		target:       target,
		writeEntered: make(chan struct{}),
		unblocked:    make(chan struct{}),
	}
}

func (w *closeUnblocksWriter) Write(content []byte) (int, error) {
	if !w.block.Load() {
		return w.target.Write(content)
	}
	w.enterOnce.Do(func() {
		close(w.writeEntered)
	})
	<-w.unblocked
	return 0, io.ErrClosedPipe
}

func (w *closeUnblocksWriter) Close() error {
	w.closeOnce.Do(func() {
		close(w.unblocked)
		w.closeErr = w.target.Close()
	})
	return w.closeErr
}

type synchronizedTraceBuffer struct {
	mu          sync.Mutex
	buffer      bytes.Buffer
	tailPayload string
	tailSeen    chan struct{}
	tailOnce    sync.Once
}

func newSynchronizedTraceBuffer(tailPayload string) *synchronizedTraceBuffer {
	return &synchronizedTraceBuffer{
		tailPayload: tailPayload,
		tailSeen:    make(chan struct{}),
	}
}

func (b *synchronizedTraceBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written, err := b.buffer.Write(content)
	if bytes.Contains(content, []byte(b.tailPayload)) {
		b.tailOnce.Do(func() {
			close(b.tailSeen)
		})
	}
	return written, err
}

func (b *synchronizedTraceBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}
