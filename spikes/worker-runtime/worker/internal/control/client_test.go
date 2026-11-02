package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/redact"
	"switchback.local/spikes/worker-runtime/worker/internal/acp"
	"switchback.local/spikes/worker-runtime/worker/internal/modelobs"
	"switchback.local/spikes/worker-runtime/worker/internal/upload"
)

func TestWorkerControlRejectsInitializeSessionAgentProfileMismatch(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}
	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		initialize := validInitialize(
			workingCopy,
			agentState,
			filepath.Dir(traceFile.Name()),
			"trace-1",
		)
		initialize.InitializeSession.AgentProfile = "integration-fake"
		sendBackendCommand(t, stream, identity, 1, 1, "", initialize)
		return nil
	})
	defer cleanup()

	client := newConfiguredClient(
		t,
		rpc,
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		nil,
	)
	client.options.AgentProfile = agentprofile.Codex
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("Run() error = %v, want profile mismatch rejection", err)
	}
	agent.mu.Lock()
	newSessions := agent.newSessionCalls
	agent.mu.Unlock()
	if newSessions != 0 {
		t.Fatalf("NewSession calls = %d, want 0 before profile mismatch rejection", newSessions)
	}
}

func TestClientRunsTwoSequentialTurnsOnOneAgentSession(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	observations := make(chan modelobs.Observation, 1)
	requestBinder := modelobs.NewRequestBinder()
	releaseFirstPrompt := make(chan struct{})
	agent := &fakeAgent{
		trace:              traceFile,
		workingCopy:        workingCopy,
		observations:       observations,
		requestBinder:      requestBinder,
		releaseFirstPrompt: releaseFirstPrompt,
	}
	uploadServer, uploads := newRecordingUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		incoming, ok := metadata.FromIncomingContext(stream.Context())
		if !ok || strings.Join(incoming.Get("authorization"), ",") != "Bearer worker-token" {
			return fmt.Errorf("authorization metadata = %v", incoming.Get("authorization"))
		}
		ready := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, ready, 1, 0, "", "runtime_ready")

		sendBackendCommand(t, stream, identity, 1, 1, "", &workercontrolv1.BackendEnvelope_InitializeSession{
			InitializeSession: &workercontrolv1.InitializeSession{
				WorkingCopy:  workingCopy,
				AgentState:   agentState,
				TraceSpool:   filepath.Dir(traceFile.Name()),
				AgentProfile: "codex",
				TraceId:      "trace-1",
			},
		})
		sessionReady := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, sessionReady, 2, 1, "", "agent_session_ready")
		if sessionReady.GetAgentSessionReady().GetAgentSessionId() != "agent-session-1" {
			return fmt.Errorf("agent session ID = %q", sessionReady.GetAgentSessionReady().GetAgentSessionId())
		}

		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "first prompt"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		modelEvent := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, modelEvent, 4, 2, "turn-1", "model_observation")
		if modelEvent.GetModelObservation().GetModelObservationId() != "litellm-call-1" {
			return fmt.Errorf("model observation ID = %q", modelEvent.GetModelObservation().GetModelObservationId())
		}
		var correlation modelobs.Correlation
		if err := json.Unmarshal(modelEvent.GetModelObservation().GetMetadataJson(), &correlation); err != nil {
			return fmt.Errorf("decode model observation correlation: %w", err)
		}
		if correlation != (modelobs.Correlation{
			RequestID: "litellm-call-1", TraceID: "trace-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		}) {
			return fmt.Errorf("model observation correlation = %#v", correlation)
		}
		close(releaseFirstPrompt)
		firstTerminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, firstTerminal, 5, 2, "turn-1", "turn_completed")
		if len(firstTerminal.GetTurnCompleted().GetEvidence()) != 4 {
			return fmt.Errorf("first terminal evidence = %d", len(firstTerminal.GetTurnCompleted().GetEvidence()))
		}

		sendBackendCommand(t, stream, identity, 3, 5, "turn-2", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "second prompt"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 6, 3, "turn-2", "turn_started")
		secondTerminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, secondTerminal, 7, 3, "turn-2", "turn_completed")
		if len(secondTerminal.GetTurnCompleted().GetEvidence()) != 4 {
			return fmt.Errorf("second terminal evidence = %d", len(secondTerminal.GetTurnCompleted().GetEvidence()))
		}

		sendBackendCommand(t, stream, identity, 4, 7, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "test complete"},
		})
		return nil
	})
	defer cleanup()

	client, err := NewClient(Options{
		RPC:           rpc,
		Identity:      identity,
		WorkerToken:   "worker-token",
		ShimVersion:   "test",
		Agent:         agent,
		AgentProfile:  agentprofile.Codex,
		Capabilities:  acp.CapabilitySnapshot{ProtocolVersion: sdk.ProtocolVersionNumber, GatewayAuthenticated: true, FileSystemAdvertised: true},
		WorkingCopy:   workingCopy,
		AgentState:    agentState,
		TraceSpool:    filepath.Dir(traceFile.Name()),
		Trace:         traceFile,
		Uploader:      uploader,
		Secrets:       redact.Secrets{WorkerToken: "worker-token"},
		Observations:  observations,
		RequestBinder: requestBinder,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.newSessionCalls != 1 {
		t.Fatalf("NewSession calls = %d, want 1", agent.newSessionCalls)
	}
	if strings.Join(agent.prompts, ",") != "first prompt,second prompt" {
		t.Fatalf("prompts = %v", agent.prompts)
	}
	for index, sessionID := range agent.promptSessionIDs {
		if sessionID != "agent-session-1" {
			t.Fatalf("Prompt %d session ID = %q", index+1, sessionID)
		}
	}
	records := uploads.snapshot()
	kinds := make([]string, 0, len(records))
	for _, record := range records {
		kinds = append(kinds, record.kind)
	}
	if got := strings.Join(kinds, ","); got != "report,raw-acp,trace,patch,report,raw-acp,trace,patch" {
		t.Fatalf("uploaded kinds = %s", got)
	}
	firstPatch := findUploadRecord(t, records, "turn-1", sharedevidence.KindPatch)
	secondPatch := findUploadRecord(t, records, "turn-2", sharedevidence.KindPatch)
	if secondPatch.beforeTree != firstPatch.afterTree {
		t.Fatalf(
			"turn two before tree = %q, want turn one after tree %q",
			secondPatch.beforeTree,
			firstPatch.afterTree,
		)
	}
	secondPatchBody := string(secondPatch.body)
	if !strings.Contains(secondPatchBody, "turn-2.txt") ||
		!strings.Contains(secondPatchBody, "second prompt") ||
		strings.Contains(secondPatchBody, "turn-1.txt") ||
		strings.Contains(secondPatchBody, "first prompt") {
		t.Fatalf("turn two patch is not incremental:\n%s", secondPatchBody)
	}
}

func TestClientShutdownCancelsActivePromptAndEmitsOneFailedTerminal(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &cancellingAgent{
		trace:       traceFile,
		workingCopy: workingCopy,
		started:     make(chan struct{}),
		done:        make(chan struct{}),
	}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", &workercontrolv1.BackendEnvelope_InitializeSession{
			InitializeSession: &workercontrolv1.InitializeSession{
				WorkingCopy:  workingCopy,
				AgentState:   agentState,
				TraceSpool:   filepath.Dir(traceFile.Name()),
				AgentProfile: "codex",
				TraceId:      "trace-1",
			},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "wait for shutdown"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		<-agent.started
		sendBackendCommand(t, stream, identity, 3, 3, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "runtime cleanup"},
		})
		terminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, terminal, 4, 3, "turn-1", "turn_failed")
		if terminal.GetTurnFailed().GetCode() != failureCodeCancelled ||
			len(terminal.GetTurnFailed().GetPartialEvidence()) != 4 {
			return fmt.Errorf("shutdown terminal = %+v", terminal.GetTurnFailed())
		}
		return nil
	})
	defer cleanup()

	client, err := NewClient(Options{
		RPC:          rpc,
		Identity:     identity,
		WorkerToken:  "worker-token",
		ShimVersion:  "test",
		Agent:        agent,
		AgentProfile: agentprofile.Codex,
		Capabilities: acp.CapabilitySnapshot{ProtocolVersion: sdk.ProtocolVersionNumber, GatewayAuthenticated: true, FileSystemAdvertised: true},
		WorkingCopy:  workingCopy,
		AgentState:   agentState,
		TraceSpool:   filepath.Dir(traceFile.Name()),
		Trace:        traceFile,
		Uploader:     uploader,
		Secrets:      redact.Secrets{WorkerToken: "worker-token"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-agent.done:
	case <-ctx.Done():
		t.Fatal("cancelled ACP Prompt did not return")
	}
}

func TestClientPartialUploadFailureNeverCompletesTurn(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, uploadedKinds := newUploadServer(t, sharedevidence.KindRawACP)
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", &workercontrolv1.BackendEnvelope_InitializeSession{
			InitializeSession: &workercontrolv1.InitializeSession{
				WorkingCopy: workingCopy, AgentState: agentState,
				TraceSpool: filepath.Dir(traceFile.Name()), AgentProfile: "codex", TraceId: "trace-1",
			},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "upload evidence"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		terminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, terminal, 4, 2, "turn-1", "turn_failed")
		if terminal.GetTurnFailed().GetCode() != upload.FailureCodeEvidenceUpload ||
			len(terminal.GetTurnFailed().GetPartialEvidence()) != 1 {
			return fmt.Errorf("partial upload terminal = %+v", terminal.GetTurnFailed())
		}
		sendBackendCommand(t, stream, identity, 3, 4, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "failed run cleanup"},
		})
		return nil
	})
	defer cleanup()

	client := newConfiguredClient(t, rpc, identity, agent, workingCopy, agentState, traceFile, uploader, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.Join(*uploadedKinds, ","); got != "report,raw-acp" {
		t.Fatalf("uploaded kinds = %s, want report,raw-acp", got)
	}
}

func TestClientCancelCommandCancelsPromptAndFailsWithCompleteEvidence(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &cancellingAgent{
		trace:       traceFile,
		workingCopy: workingCopy,
		started:     make(chan struct{}),
		done:        make(chan struct{}),
	}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", &workercontrolv1.BackendEnvelope_InitializeSession{
			InitializeSession: &workercontrolv1.InitializeSession{
				WorkingCopy: workingCopy, AgentState: agentState,
				TraceSpool: filepath.Dir(traceFile.Name()), AgentProfile: "codex", TraceId: "trace-1",
			},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "wait for cancel"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		<-agent.started
		sendBackendCommand(t, stream, identity, 3, 3, "turn-1", &workercontrolv1.BackendEnvelope_CancelAgentTurn{
			CancelAgentTurn: &workercontrolv1.CancelAgentTurn{Reason: "user interrupt"},
		})
		terminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, terminal, 4, 3, "turn-1", "turn_failed")
		if terminal.GetTurnFailed().GetCode() != failureCodeCancelled ||
			len(terminal.GetTurnFailed().GetPartialEvidence()) != 4 {
			return fmt.Errorf("cancel terminal = %+v", terminal.GetTurnFailed())
		}
		sendBackendCommand(t, stream, identity, 4, 4, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "cancel complete"},
		})
		return nil
	})
	defer cleanup()

	client := newConfiguredClient(t, rpc, identity, agent, workingCopy, agentState, traceFile, uploader, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-agent.done:
	case <-ctx.Done():
		t.Fatal("cancelled ACP Prompt did not return")
	}
}

func TestClientRejectsInvalidBackendSequenceAckAndIdentity(t *testing.T) {
	tests := []struct {
		name      string
		script    func(*testing.T, workercontrolv1.WorkerControl_ConnectServer, RuntimeIdentity, string, string, string)
		wantError string
	}{
		{
			name: "out of order first sequence",
			script: func(t *testing.T, stream workercontrolv1.WorkerControl_ConnectServer, identity RuntimeIdentity, workingCopy, agentState, traceSpool string) {
				sendBackendCommand(t, stream, identity, 2, 1, "", validInitialize(workingCopy, agentState, traceSpool, "trace-1"))
			},
			wantError: "stream sequence = 2, want 1",
		},
		{
			name: "ack exceeds sent sequence",
			script: func(t *testing.T, stream workercontrolv1.WorkerControl_ConnectServer, identity RuntimeIdentity, workingCopy, agentState, traceSpool string) {
				sendBackendCommand(t, stream, identity, 1, 2, "", validInitialize(workingCopy, agentState, traceSpool, "trace-1"))
			},
			wantError: "ACK sequence 2 exceeds sent sequence 1",
		},
		{
			name: "runtime identity mismatch",
			script: func(t *testing.T, stream workercontrolv1.WorkerControl_ConnectServer, identity RuntimeIdentity, workingCopy, agentState, traceSpool string) {
				identity.RuntimeInstanceID = "other-runtime"
				sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(workingCopy, agentState, traceSpool, "trace-1"))
			},
			wantError: "runtime identity does not match",
		},
		{
			name: "ack moves backwards",
			script: func(t *testing.T, stream workercontrolv1.WorkerControl_ConnectServer, identity RuntimeIdentity, workingCopy, agentState, traceSpool string) {
				sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(workingCopy, agentState, traceSpool, "trace-1"))
				assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
				sendBackendCommand(t, stream, identity, 2, 0, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
					ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "invalid ACK"},
				})
			},
			wantError: "ACK sequence moved backwards",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingCopy := newGitWorkingCopy(t)
			traceFile := newTraceFile(t)
			agentState := t.TempDir()
			agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
			uploadServer, _ := newUploadServer(t, "")
			defer uploadServer.Close()
			uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
			if err != nil {
				t.Fatalf("upload.NewClient() error = %v", err)
			}
			identity := testRuntimeIdentity()
			traceSpool := filepath.Dir(traceFile.Name())
			rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
				assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
				test.script(t, stream, identity, workingCopy, agentState, traceSpool)
				return nil
			})
			defer cleanup()

			client := newConfiguredClient(t, rpc, identity, agent, workingCopy, agentState, traceFile, uploader, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = client.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Run() error = %v, want %q", err, test.wantError)
			}
			agent.mu.Lock()
			newSessions := agent.newSessionCalls
			agent.mu.Unlock()
			if test.name != "ack moves backwards" && newSessions != 0 {
				t.Fatalf("NewSession calls = %d, want 0 before rejected command", newSessions)
			}
		})
	}
}

func TestClientRejectsConcurrentStartWithoutSecondPrompt(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &cancellingAgent{
		trace:       traceFile,
		workingCopy: workingCopy,
		started:     make(chan struct{}),
		done:        make(chan struct{}),
	}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}
	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(workingCopy, agentState, filepath.Dir(traceFile.Name()), "trace-1"))
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "first"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		<-agent.started
		sendBackendCommand(t, stream, identity, 3, 3, "turn-2", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "must not run"},
		})
		return nil
	})
	defer cleanup()

	client := newConfiguredClient(t, rpc, identity, agent, workingCopy, agentState, traceFile, uploader, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("Run() error = %v, want active-turn rejection", err)
	}
	select {
	case <-agent.done:
	case <-ctx.Done():
		t.Fatal("active Prompt was not cancelled after protocol rejection")
	}
	if calls := agent.promptCalls.Load(); calls != 1 {
		t.Fatalf("Prompt calls = %d, want 1", calls)
	}
}

func TestExecuteTurnFailsClosedWhenTerminalSendIsAmbiguous(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}
	identity := testRuntimeIdentity()
	client := &Client{
		options: Options{
			Identity:    identity,
			Agent:       agent,
			WorkingCopy: workingCopy,
			Trace:       traceFile,
			Uploader:    uploader,
			Now:         time.Now,
		},
		runContext: context.Background(),
		sender: &streamSender{
			stream:   failingTerminalStream{ClientStream: nil},
			identity: identity,
			now:      time.Now,
		},
	}
	capture, err := client.beginTurn(context.Background())
	if err != nil {
		t.Fatalf("beginTurn() error = %v", err)
	}
	turnContext, cancel := context.WithCancel(context.Background())
	active := &activeTurn{
		identity: sharedevidence.TurnIdentity{
			WorkerSessionID:   identity.WorkerSessionID,
			WorkerRunID:       identity.WorkerRunID,
			RuntimeInstanceID: identity.RuntimeInstanceID,
			RuntimeGeneration: identity.RuntimeGeneration,
			AgentSessionID:    "agent-session-1",
			AgentTurnID:       "turn-1",
			TraceID:           "trace-1",
		},
		serverTimestamp: time.Now().UTC(),
		prompt:          "ambiguous terminal",
		capture:         capture,
		cancel:          cancel,
	}
	client.active = active
	client.turnEvents = make(chan turnEvent, 1)
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		event := <-client.turnEvents
		event.response <- client.handleTurnEvent(event)
	}()

	result := client.executeTurn(turnContext, active)
	<-ownerDone
	if result.fatal == nil || !strings.Contains(result.fatal.Error(), "terminality is unknown") {
		t.Fatalf("executeTurn() fatal = %v, want ambiguous terminal send failure", result.fatal)
	}
}

type failingTerminalStream struct {
	grpc.ClientStream
}

func (failingTerminalStream) Send(*workercontrolv1.WorkerEnvelope) error {
	return errors.New("injected terminal send failure")
}

func (failingTerminalStream) Recv() (*workercontrolv1.BackendEnvelope, error) {
	return nil, io.EOF
}

type fakeAgent struct {
	mu                 sync.Mutex
	trace              *os.File
	workingCopy        string
	observations       chan<- modelobs.Observation
	requestBinder      *modelobs.RequestBinder
	releaseFirstPrompt <-chan struct{}
	newSessionCalls    int
	prompts            []string
	promptSessionIDs   []string
}

type cancellingAgent struct {
	trace       *os.File
	workingCopy string
	started     chan struct{}
	done        chan struct{}
	promptCalls atomic.Int32
}

func (*cancellingAgent) NewSession(context.Context, string) (string, error) {
	return "agent-session-1", nil
}

func (a *cancellingAgent) Prompt(ctx context.Context, _, prompt string) (acp.PromptResult, error) {
	a.promptCalls.Add(1)
	defer close(a.done)
	if err := os.WriteFile(filepath.Join(a.workingCopy, "cancelled.txt"), []byte(prompt+"\n"), 0o600); err != nil {
		return acp.PromptResult{}, err
	}
	if err := json.NewEncoder(a.trace).Encode(map[string]string{
		"type":      "acp.raw_frame",
		"direction": "agent_to_client",
		"frame":     `{"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"cancelled"}}}}`,
	}); err != nil {
		return acp.PromptResult{}, err
	}
	close(a.started)
	<-ctx.Done()
	return acp.PromptResult{}, ctx.Err()
}

func (a *fakeAgent) NewSession(_ context.Context, workingCopy string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.newSessionCalls++
	if workingCopy != a.workingCopy {
		return "", fmt.Errorf("working copy = %q", workingCopy)
	}
	return "agent-session-1", nil
}

func (a *fakeAgent) Prompt(ctx context.Context, sessionID, prompt string) (acp.PromptResult, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	a.promptSessionIDs = append(a.promptSessionIDs, sessionID)
	turnNumber := len(a.prompts)
	a.mu.Unlock()

	if err := os.WriteFile(
		filepath.Join(a.workingCopy, fmt.Sprintf("turn-%d.txt", turnNumber)),
		[]byte(prompt+"\n"),
		0o600,
	); err != nil {
		return acp.PromptResult{}, err
	}
	frame := fmt.Sprintf(
		`{"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`,
		"finished "+prompt,
	)
	if err := json.NewEncoder(a.trace).Encode(map[string]string{
		"type":      "acp.raw_frame",
		"direction": "agent_to_client",
		"frame":     frame,
	}); err != nil {
		return acp.PromptResult{}, err
	}
	if turnNumber == 1 && a.observations != nil {
		correlation := modelobs.Correlation{
			RequestID:       "request-1",
			TraceID:         "trace-1",
			WorkerSessionID: "session-1",
			WorkerRunID:     "run-1",
			AgentTurnID:     "turn-1",
		}
		if a.requestBinder != nil {
			if err := a.requestBinder.Bind("litellm-call-1"); err != nil {
				return acp.PromptResult{}, err
			}
			var err error
			correlation, err = a.requestBinder.Take("litellm-call-1")
			if err != nil {
				return acp.PromptResult{}, err
			}
		}
		a.observations <- modelobs.Observation{
			Model:       "codex",
			Route:       "litellm",
			StartedAt:   time.Unix(10, 0).UTC(),
			EndedAt:     time.Unix(11, 0).UTC(),
			LatencyMS:   1000,
			Usage:       modelobs.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			CostUSD:     0.01,
			Correlation: correlation,
		}
		if a.releaseFirstPrompt != nil {
			select {
			case <-ctx.Done():
				return acp.PromptResult{}, ctx.Err()
			case <-a.releaseFirstPrompt:
			}
		}
	}
	return acp.PromptResult{StopReason: sdk.StopReasonEndTurn}, nil
}

type scriptedControlServer struct {
	workercontrolv1.UnimplementedWorkerControlServer
	script func(workercontrolv1.WorkerControl_ConnectServer) error
}

func (s scriptedControlServer) Connect(stream workercontrolv1.WorkerControl_ConnectServer) error {
	return s.script(stream)
}

func startControlServer(
	t *testing.T,
	script func(workercontrolv1.WorkerControl_ConnectServer) error,
) (workercontrolv1.WorkerControlClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	workercontrolv1.RegisterWorkerControlServer(server, scriptedControlServer{script: script})
	go func() {
		_ = server.Serve(listener)
	}()
	connection, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	return workercontrolv1.NewWorkerControlClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}

func receiveWorkerEvent(
	t *testing.T,
	stream workercontrolv1.WorkerControl_ConnectServer,
) *workercontrolv1.WorkerEnvelope {
	t.Helper()
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() worker event error = %v", err)
	}
	return event
}

func sendBackendCommand(
	t *testing.T,
	stream workercontrolv1.WorkerControl_ConnectServer,
	identity RuntimeIdentity,
	sequence, ack uint64,
	turnID string,
	payload any,
) {
	t.Helper()
	command := &workercontrolv1.BackendEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      fmt.Sprintf("backend-%d", sequence),
			StreamSequence: sequence,
			AckSequence:    ack,
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   identity.WorkerSessionID,
				WorkerRunId:       identity.WorkerRunID,
				RuntimeInstanceId: identity.RuntimeInstanceID,
				RuntimeGeneration: identity.RuntimeGeneration,
				AgentTurnId:       turnID,
			},
			SentAt: timestamppb.New(time.Unix(int64(sequence), 0).UTC()),
		},
	}
	switch typed := payload.(type) {
	case *workercontrolv1.BackendEnvelope_InitializeSession:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_StartAgentTurn:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_CancelAgentTurn:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_ShutdownRuntime:
		command.Payload = typed
	default:
		t.Fatalf("unsupported Backend payload %T", payload)
	}
	if err := stream.Send(command); err != nil {
		t.Fatalf("Send() Backend command error = %v", err)
	}
}

func assertWorkerEvent(
	t *testing.T,
	event *workercontrolv1.WorkerEnvelope,
	sequence, ack uint64,
	turnID, kind string,
) {
	t.Helper()
	meta := event.GetMeta()
	if meta.GetStreamSequence() != sequence || meta.GetAckSequence() != ack ||
		meta.GetIdentity().GetAgentTurnId() != turnID {
		t.Fatalf("event meta = %+v, want sequence=%d ack=%d turn=%q", meta, sequence, ack, turnID)
	}
	var got string
	switch event.GetPayload().(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeReady:
		got = "runtime_ready"
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		got = "agent_session_ready"
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		got = "turn_started"
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		got = "model_observation"
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		got = "turn_completed"
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		got = "turn_failed"
	}
	if got != kind {
		t.Fatalf("event payload kind = %q, want %q", got, kind)
	}
}

func testRuntimeIdentity() RuntimeIdentity {
	return RuntimeIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
}

func validInitialize(workingCopy, agentState, traceSpool, traceID string) *workercontrolv1.BackendEnvelope_InitializeSession {
	return &workercontrolv1.BackendEnvelope_InitializeSession{
		InitializeSession: &workercontrolv1.InitializeSession{
			WorkingCopy:  workingCopy,
			AgentState:   agentState,
			TraceSpool:   traceSpool,
			AgentProfile: "codex",
			TraceId:      traceID,
		},
	}
}

func newConfiguredClient(
	t *testing.T,
	rpc workercontrolv1.WorkerControlClient,
	identity RuntimeIdentity,
	agent Agent,
	workingCopy, agentState string,
	traceFile *os.File,
	uploader *upload.Client,
	observations <-chan modelobs.Observation,
) *Client {
	t.Helper()
	client, err := NewClient(Options{
		RPC:          rpc,
		Identity:     identity,
		WorkerToken:  "worker-token",
		ShimVersion:  "test",
		Agent:        agent,
		AgentProfile: agentprofile.Codex,
		Capabilities: acp.CapabilitySnapshot{ProtocolVersion: sdk.ProtocolVersionNumber, GatewayAuthenticated: true, FileSystemAdvertised: true},
		WorkingCopy:  workingCopy,
		AgentState:   agentState,
		TraceSpool:   filepath.Dir(traceFile.Name()),
		Trace:        traceFile,
		Uploader:     uploader,
		Secrets:      redact.Secrets{WorkerToken: "worker-token"},
		Observations: observations,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func newGitWorkingCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Switchback Test")
	runGit(t, root, "config", "user.email", "switchback@example.test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "-qm", "base")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func newTraceFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "acp.jsonl"), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func newUploadServer(t *testing.T, failKind string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	kinds := make([]string, 0, 8)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		kind := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		mu.Lock()
		kinds = append(kinds, kind)
		mu.Unlock()
		if kind == failKind {
			http.Error(writer, "injected upload failure", http.StatusServiceUnavailable)
			return
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upload: %v", err)
		}
		digest := sha256.Sum256(payload)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(sharedevidence.ObjectRef{
			Kind:   kind,
			Path:   request.URL.Path,
			SHA256: hex.EncodeToString(digest[:]),
			Bytes:  int64(len(payload)),
		})
	}))
	return server, &kinds
}

type recordedUpload struct {
	turnID     string
	kind       string
	beforeTree string
	afterTree  string
	body       []byte
}

type uploadRecorder struct {
	mu      sync.Mutex
	records []recordedUpload
}

func (r *uploadRecorder) snapshot() []recordedUpload {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make([]recordedUpload, len(r.records))
	for index, record := range r.records {
		records[index] = record
		records[index].body = append([]byte(nil), record.body...)
	}
	return records
}

func newRecordingUploadServer(
	t *testing.T,
	failKind string,
) (*httptest.Server, *uploadRecorder) {
	t.Helper()
	recorder := &uploadRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		var turnID string
		for index, segment := range segments {
			if segment == "turns" && index+1 < len(segments) {
				turnID = segments[index+1]
				break
			}
		}
		kind := segments[len(segments)-1]
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upload: %v", err)
		}
		recorder.mu.Lock()
		recorder.records = append(recorder.records, recordedUpload{
			turnID:     turnID,
			kind:       kind,
			beforeTree: request.Header.Get(upload.HeaderBeforeTree),
			afterTree:  request.Header.Get(upload.HeaderAfterTree),
			body:       append([]byte(nil), payload...),
		})
		recorder.mu.Unlock()
		if kind == failKind {
			http.Error(writer, "injected upload failure", http.StatusServiceUnavailable)
			return
		}
		digest := sha256.Sum256(payload)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(sharedevidence.ObjectRef{
			Kind:   kind,
			Path:   request.URL.Path,
			SHA256: hex.EncodeToString(digest[:]),
			Bytes:  int64(len(payload)),
		})
	}))
	return server, recorder
}

func findUploadRecord(
	t *testing.T,
	records []recordedUpload,
	turnID, kind string,
) recordedUpload {
	t.Helper()
	for _, record := range records {
		if record.turnID == turnID && record.kind == kind {
			return record
		}
	}
	t.Fatalf("missing upload record for turn %q kind %q", turnID, kind)
	return recordedUpload{}
}
