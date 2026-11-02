package localproduct

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRestoreLiveRuntimeAfterTransientInputsAreRemoved(t *testing.T) {
	dataRoot := t.TempDir()
	engineRoot := filepath.Join(string(filepath.Separator), "srv", "switchback-data")
	hub := controlgrpc.NewHub()
	environment := newRuntimeEnvironment(Config{
		DataRoot: dataRoot, EngineDataRoot: engineRoot,
	}, hub)
	environment.storage = localstorage.Materializer{
		BackendDataRoot: dataRoot, EngineDataRoot: engineRoot,
		Ownership: localstorage.OwnershipFunc(func(string, int, int) error {
			return nil
		}),
	}
	environment.secrets = runtimesecret.Materializer{
		Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error {
			return nil
		}),
		OwnerUID: func(string) (uint32, error) { return 0, nil },
		NewToken: startupSequentialTokens("proxy-token", "sink-token"),
	}
	storage, err := environment.storage.Materialize("session-1")
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := platformconfig.ProductionProfileRegistry().Agent(
		platformconfig.DeepSeekCodexOpenAIV1,
	)
	if !ok {
		t.Fatal("production AgentProfile is missing")
	}
	_, err = environment.secrets.Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-1", Generation: 1, ChannelEpoch: 7,
		WorkerControlToken: "surviving-worker-control-token",
		SessionStorage:     storage,
		AgentProfile: workerapp.ProfileRef{
			ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
		},
		ProviderCredential: runtimesecret.ProviderCredential([]byte("provider-key")),
	})
	if err != nil {
		t.Fatal(err)
	}
	transientSecretRoot := filepath.Join(
		storage.BackendRoot,
		"runtimes",
		"runtime-1-g1",
		"secret-input",
	)
	if err := os.RemoveAll(transientSecretRoot); err != nil {
		t.Fatal(err)
	}

	prepared, err := environment.RestoreLiveRuntime(context.Background(), liveRuntimeRestoreRequest{
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, ControlChannelEpoch: 7,
		AgentSessionID:       "agent-session-established-after-start",
		AgentProfileID:       profile.ID,
		AgentProfileRevision: profile.Revision,
		AgentProfileDigest:   profile.Digest,
		WorkingCopyCheckpointDigest: "sha256:" +
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(transientSecretRoot); !os.IsNotExist(err) {
		t.Fatalf("startup restore recreated transient Runtime inputs: %v", err)
	}
	if prepared.Credential.Token != "surviving-worker-control-token" ||
		prepared.Credential.Identity.WorkerSessionID != "session-1" ||
		prepared.Credential.Identity.RuntimeInstanceID != "runtime-1" ||
		prepared.Credential.Identity.RuntimeGeneration != 1 ||
		prepared.Credential.Identity.ChannelEpoch != 7 ||
		prepared.Storage != storage {
		t.Fatalf("restored Runtime=%+v", prepared)
	}
}

func TestRestoreStartupRuntimeLegacyMissingCredentialOpensRecovery(t *testing.T) {
	var incidents []workerpostgres.DetectedRuntimeFailure
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3, ControlChannelEpoch: 9,
		},
		ContainerID:    "container-1",
		RuntimeStatus:  "active",
		LivenessAnchor: time.Now().UTC(),
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{}, runtimesecret.ErrLiveRuntimeCredentialUnavailable
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{
					Exists: true, Running: true, ContainerID: "container-1", Generation: 3,
					ObservedAt: time.Now().UTC(),
				}, nil
			},
			waitEvents: func(
				context.Context,
				controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				t.Fatal("legacy Runtime without a restorable credential must not reconnect")
				return nil, nil
			},
			markSuspect: func(context.Context, startupRuntimeCandidate) error {
				t.Fatal("legacy Runtime recovery must not wait for the liveness window")
				return nil
			},
			fence: func(context.Context, string, uint64, string) error {
				return controlgrpc.ErrNotConnected
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("legacy Runtime recovery must use the durable recovery fence")
				return false, nil
			},
			openFailure: func(
				_ context.Context,
				signal workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				incidents = append(incidents, signal)
				return true, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{SuspectAfter: time.Second, UnhealthyAfter: 2 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || len(incidents) != 1 {
		t.Fatalf("result=%+v incidents=%+v", result, incidents)
	}
	if incidents[0].Trigger != workerdomain.RecoveryTriggerHeartbeatUnhealthy {
		t.Fatalf("incident=%+v", incidents[0])
	}
}

func startupSequentialTokens(tokens ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		token := tokens[index]
		index++
		return token, nil
	}
}

func TestRestoreStartupRuntimeKeepsRunningContainerAndOpensNoIncident(t *testing.T) {
	events := make(chan workerapp.WorkerControlEvent)
	var calls []string
	var mu sync.Mutex
	record := func(value string) {
		mu.Lock()
		calls = append(calls, value)
		mu.Unlock()
	}
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3, ControlChannelEpoch: 9,
		},
		ContainerID:    "container-1",
		RuntimeStatus:  "active",
		LivenessAnchor: time.Now().UTC(),
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				record("prepare")
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				record("inspect")
				return workerapp.RuntimeInspection{
					Exists: true, Running: true, ContainerID: "container-1", Generation: 3,
				}, nil
			},
			waitEvents: func(
				_ context.Context,
				identity controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				record("wait")
				if identity.WorkerSessionID != "session-1" ||
					identity.RuntimeInstanceID != "runtime-1" ||
					identity.RuntimeGeneration != 3 || identity.ChannelEpoch != 9 {
					t.Fatalf("wait identity=%+v", identity)
				}
				return events, nil
			},
			markSuspect: func(context.Context, startupRuntimeCandidate) error {
				record("suspect")
				return nil
			},
			fence: func(context.Context, string, uint64, string) error {
				record("fence")
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				record("conditional-fence")
				return true, nil
			},
			openFailure: func(
				context.Context,
				workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				record("incident")
				return true, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{SuspectAfter: time.Second, UnhealthyAfter: 2 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconnected || result.Events != events {
		t.Fatalf("restore result=%+v", result)
	}
	if got := calls; len(got) != 3 ||
		got[0] != "prepare" || got[1] != "inspect" || got[2] != "wait" {
		t.Fatalf("startup calls=%v", got)
	}
}

func TestRestoreStartupRuntimeMissingContainerImmediatelyOpensOneIncident(t *testing.T) {
	var incidents []workerpostgres.DetectedRuntimeFailure
	var fences int
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3, ControlChannelEpoch: 9,
		},
		ContainerID:    "container-1",
		RuntimeStatus:  "active",
		LivenessAnchor: time.Now().UTC(),
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{ObservedAt: time.Now().UTC()}, nil
			},
			waitEvents: func(
				context.Context,
				controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				t.Fatal("missing container waited for WorkerControl")
				return nil, nil
			},
			fence: func(context.Context, string, uint64, string) error {
				fences++
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("missing container used conditional fence")
				return false, nil
			},
			openFailure: func(
				_ context.Context,
				signal workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				incidents = append(incidents, signal)
				return true, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{SuspectAfter: time.Second, UnhealthyAfter: 2 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || fences != 1 || len(incidents) != 1 ||
		incidents[0].Trigger != workerdomain.RecoveryTriggerContainerMissing {
		t.Fatalf("result=%+v incidents=%+v", result, incidents)
	}
}

func TestRestoreStartupRuntimeSettlementOwnedMissingContainerDefersWithoutOrdinaryRecovery(
	t *testing.T,
) {
	reconnectUntil := time.Now().UTC().Add(time.Minute)
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3,
			ControlChannelEpoch: 9,
		},
		ContainerID:              "container-1",
		RuntimeStatus:            "active",
		LivenessAnchor:           time.Now().UTC(),
		SettlementReconnectUntil: reconnectUntil,
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{ObservedAt: time.Now().UTC()}, nil
			},
			waitEvents: func(
				context.Context,
				controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				t.Fatal("missing settlement-owned container waited for WorkerControl")
				return nil, nil
			},
			markSuspect: func(context.Context, startupRuntimeCandidate) error {
				t.Fatal("settlement-owned Runtime was marked suspect")
				return nil
			},
			fence: func(context.Context, string, uint64, string) error {
				t.Fatal("settlement-owned Runtime used ordinary fence")
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("settlement-owned Runtime used conditional ordinary fence")
				return false, nil
			},
			openFailure: func(
				context.Context,
				workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				t.Fatal("settlement-owned Runtime opened ordinary recovery")
				return false, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{
			SuspectAfter: time.Second, UnhealthyAfter: 2 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || !result.Deferred {
		t.Fatalf("restore result=%+v", result)
	}
}

func TestRestoreStartupRuntimeMissingContainerLetsSettlementAuthorityWinBeforeFence(
	t *testing.T,
) {
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3,
			ControlChannelEpoch: 9,
		},
		ContainerID:    "container-1",
		RuntimeStatus:  "active",
		LivenessAnchor: time.Now().UTC(),
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{ObservedAt: time.Now().UTC()}, nil
			},
			waitEvents: func(
				context.Context,
				controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				t.Fatal("missing container waited for WorkerControl")
				return nil, nil
			},
			fence: func(context.Context, string, uint64, string) error {
				t.Fatal("ordinary fence ran after settlement won authority")
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("conditional fence ran after settlement won authority")
				return false, nil
			},
			openFailure: func(
				context.Context,
				workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				return false, workerapp.ErrTurnOutcomeAuthorityClaimed
			},
			now: time.Now,
		},
		startupReconnectPolicy{
			SuspectAfter: time.Second, UnhealthyAfter: 2 * time.Second,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || !result.Deferred {
		t.Fatalf("restore result=%+v", result)
	}
}

func TestRestoreStartupRuntimeTimeoutClaimsRecoveryBeforeFencing(t *testing.T) {
	var suspectCalls int
	var fences int
	var incidents []workerpostgres.DetectedRuntimeFailure
	anchor := time.Now().UTC()
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3, ControlChannelEpoch: 9,
		},
		ContainerID: "container-1", RuntimeStatus: "active", LivenessAnchor: anchor,
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{
					Exists: true, Running: true, ContainerID: "container-1", Generation: 3,
				}, nil
			},
			waitEvents: func(
				ctx context.Context,
				_ controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			markSuspect: func(context.Context, startupRuntimeCandidate) error {
				suspectCalls++
				return nil
			},
			fence: func(context.Context, string, uint64, string) error {
				fences++
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("startup timeout fenced before recovery authority")
				return false, nil
			},
			openFailure: func(
				_ context.Context,
				signal workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				incidents = append(incidents, signal)
				return true, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{SuspectAfter: 10 * time.Millisecond, UnhealthyAfter: 25 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || suspectCalls != 0 || fences != 1 || len(incidents) != 1 ||
		incidents[0].Trigger != workerdomain.RecoveryTriggerHeartbeatUnhealthy {
		t.Fatalf(
			"result=%+v suspect=%d incidents=%+v",
			result,
			suspectCalls,
			incidents,
		)
	}
}

func TestRestoreStartupRuntimeSettlementOwnedReconnectDefersImmediatelyWithoutOrdinaryRecovery(
	t *testing.T,
) {
	anchor := time.Now().UTC().Add(-time.Minute)
	reconnectUntil := time.Now().UTC().Add(time.Minute)
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3,
			ControlChannelEpoch: 9,
		},
		ContainerID:              "container-1",
		RuntimeStatus:            "active",
		LivenessAnchor:           anchor,
		SettlementReconnectUntil: reconnectUntil,
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		50*time.Millisecond,
	)
	defer cancel()
	var waitCalls int
	result, err := restoreStartupRuntime(
		ctx,
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{
					Exists: true, Running: true, ContainerID: "container-1",
					Generation: 3,
				}, nil
			},
			waitEvents: func(
				ctx context.Context,
				_ controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				waitCalls++
				<-ctx.Done()
				return nil, ctx.Err()
			},
			markSuspect: func(context.Context, startupRuntimeCandidate) error {
				t.Fatal("settlement-owned Runtime was marked suspect")
				return nil
			},
			fence: func(context.Context, string, uint64, string) error {
				t.Fatal("settlement-owned Runtime used ordinary fence")
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				t.Fatal("settlement-owned Runtime used conditional ordinary fence")
				return false, nil
			},
			openFailure: func(
				context.Context,
				workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				t.Fatal("settlement-owned Runtime opened ordinary recovery")
				return false, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{
			SuspectAfter: 5 * time.Millisecond, UnhealthyAfter: 10 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnected || !result.Deferred ||
		waitCalls != 0 ||
		!time.Now().UTC().Before(reconnectUntil) {
		t.Fatalf(
			"restore result=%+v wait_calls=%d now=%s deadline=%s",
			result,
			waitCalls,
			time.Now().UTC(),
			reconnectUntil,
		)
	}
}

func TestRestoreStartupRuntimeResumeCompleteWinnerDoesNotOpenFailure(t *testing.T) {
	events := make(chan workerapp.WorkerControlEvent)
	var waitCalls, incidents, unconditionalFences int
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3,
			ControlChannelEpoch: 9,
		},
		ContainerID: "container-1", RuntimeStatus: "suspect",
		LivenessAnchor: time.Now().UTC().Add(-time.Second),
	}
	result, err := restoreStartupRuntime(
		context.Background(),
		candidate,
		startupRestoreDependencies{
			prepare: func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
				return preparedRuntime{Credential: controlgrpc.Credential{
					Identity: controlgrpc.RuntimeIdentity{
						WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
						RuntimeGeneration: 3, ChannelEpoch: 9,
					},
					Token: "worker-control-token",
				}}, nil
			},
			inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
				return workerapp.RuntimeInspection{
					Exists: true, Running: true, ContainerID: "container-1", Generation: 3,
				}, nil
			},
			waitEvents: func(
				ctx context.Context,
				_ controlgrpc.RuntimeIdentity,
			) (<-chan workerapp.WorkerControlEvent, error) {
				waitCalls++
				if waitCalls < 3 {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return events, nil
			},
			fence: func(context.Context, string, uint64, string) error {
				unconditionalFences++
				return nil
			},
			fenceIfNotLive: func(
				context.Context,
				string,
				uint64,
				string,
				time.Time,
			) (bool, error) {
				return false, nil
			},
			openFailure: func(
				context.Context,
				workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				incidents++
				return false, nil
			},
			now: time.Now,
		},
		startupReconnectPolicy{
			SuspectAfter: 10 * time.Millisecond, UnhealthyAfter: 20 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconnected || result.Events != events ||
		waitCalls != 3 || incidents != 1 || unconditionalFences != 0 {
		t.Fatalf(
			"result=%+v waits=%d incidents=%d unconditional_fences=%d",
			result,
			waitCalls,
			incidents,
			unconditionalFences,
		)
	}
}

func TestRestoredTurnTraceOffsetIncludesPreRestartAndPostRestartFrames(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "acp.raw.jsonl")
	previousPrompt := rawACPTraceLine(
		t,
		"client_to_agent",
		"session/prompt",
		"agent-session-1",
		"turn-previous",
	)
	currentPrompt := rawACPTraceLine(
		t,
		"client_to_agent",
		"session/prompt",
		"agent-session-1",
		"turn-1",
	)
	preRestartUpdate := rawACPTraceLine(
		t,
		"agent_to_client",
		"session/update",
		"agent-session-1",
		"",
	)
	beforeRestart := append(
		append(append([]byte(nil), previousPrompt...), currentPrompt...),
		preRestartUpdate...,
	)
	if err := os.WriteFile(tracePath, beforeRestart, 0o600); err != nil {
		t.Fatal(err)
	}

	offset, err := restoredTurnTraceOffset(tracePath, pendingTurn{
		WorkerSessionID: "session-1", AgentSessionID: "agent-session-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(len(previousPrompt)) {
		t.Fatalf("trace offset=%d want=%d", offset, len(previousPrompt))
	}

	postRestartTerminal := rawACPTraceLine(
		t,
		"agent_to_client",
		"session/update",
		"agent-session-1",
		"",
	)
	trace, err := os.OpenFile(tracePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trace.Write(postRestartTerminal); err != nil {
		trace.Close()
		t.Fatal(err)
	}
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}

	segment, _, err := readFileFrom(tracePath, offset)
	if err != nil {
		t.Fatal(err)
	}
	expected := append(
		append(append([]byte(nil), currentPrompt...), preRestartUpdate...),
		postRestartTerminal...,
	)
	if !bytes.Equal(segment, expected) {
		t.Fatalf("restored trace segment=%q want=%q", segment, expected)
	}

	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recordedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: store,
		IDs:   randomIDs{},
		Now:   func() time.Time { return recordedAt },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1",
			TraceLifecyclePolicyRef:    "trace-default-v1",
		},
	}).Execute(context.Background(), artifactapp.FinalizeTurnEvidenceCommand{
		Identity: artifactdomain.TurnIdentity{
			TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-1",
			WorkerSessionID: "session-1", WorkerRunID: "run-1",
			AgentSessionID: "agent-session-1", AgentTurnID: "turn-1",
			RuntimeInstanceID: "runtime-1", RuntimeGeneration: 3,
			TraceStreamID: "trace-turn-1", LangfuseTraceID: "langfuse-turn-1",
			RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
				ID:                contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1,
				EnforcementStatus: "legacy_unenforced",
			},
		},
		ServerRecordedAt: recordedAt, WorkerRecordedAt: recordedAt,
		RawACPJSONL: segment, NoFilesChanged: true,
		SourceCheckpointRef: contractsv1.CheckpointRef{
			CheckpointID: "checkpoint-run-1",
			Digest: "sha256:" +
				"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, traceSegment := range finalized.Manifest.Segments {
		if traceSegment.RecordCount != 3 ||
			traceSegment.FirstSequence != 1 ||
			traceSegment.LastSequence != 3 {
			t.Fatalf(
				"final manifest omitted pre/post-restart evidence: %+v",
				finalized.Manifest,
			)
		}
	}
}

func TestRestoredTurnTraceOffsetFailsClosedWithoutMatchingPrompt(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "acp.raw.jsonl")
	if err := os.WriteFile(
		tracePath,
		rawACPTraceLine(
			t,
			"agent_to_client",
			"session/update",
			"agent-session-1",
			"",
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := restoredTurnTraceOffset(tracePath, pendingTurn{
		WorkerSessionID: "session-1", AgentSessionID: "agent-session-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 3,
	}); err == nil {
		t.Fatal("active Turn without a matching prompt was restored")
	}
}

func TestRestoredTurnTraceOffsetFailsClosedForAmbiguousOrInvalidAuthority(t *testing.T) {
	exactPrompt := rawACPTraceLine(
		t,
		"client_to_agent",
		"session/prompt",
		"agent-session-1",
		"turn-1",
	)
	exactTurn := pendingTurn{
		WorkerSessionID: "session-1", AgentSessionID: "agent-session-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 3,
	}
	for name, content := range map[string][]byte{
		"duplicate exact prompt": append(
			append([]byte(nil), exactPrompt...),
			exactPrompt...,
		),
		"malformed record": []byte("not-json\n"),
		"partial tail": append(
			append([]byte(nil), exactPrompt...),
			[]byte(`{"type":"acp.raw_frame"`)...,
		),
	} {
		t.Run(name, func(t *testing.T) {
			tracePath := filepath.Join(t.TempDir(), "acp.raw.jsonl")
			if err := os.WriteFile(tracePath, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := restoredTurnTraceOffset(
				tracePath,
				exactTurn,
			); err == nil {
				t.Fatalf("%s trace was accepted", name)
			}
		})
	}

	tracePath := filepath.Join(t.TempDir(), "acp.raw.jsonl")
	if err := os.WriteFile(tracePath, exactPrompt, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongGeneration := exactTurn
	wrongGeneration.RuntimeGeneration++
	if _, err := restoredTurnTraceOffset(
		tracePath,
		wrongGeneration,
	); err == nil {
		t.Fatal("generation-mismatched prompt was accepted")
	}
}

func TestReadBoundedStartupTraceRecordRejectsOversizeRecord(t *testing.T) {
	reader := bufio.NewReaderSize(
		bytes.NewReader(bytes.Repeat(
			[]byte{'x'},
			maxStartupRawTraceRecordBytes+1,
		)),
		64<<10,
	)
	if _, err := readBoundedStartupTraceRecord(reader); err == nil {
		t.Fatal("oversize startup trace record was accepted")
	}
}

func rawACPTraceLine(
	t *testing.T,
	direction string,
	method string,
	agentSessionID string,
	agentTurnID string,
) []byte {
	t.Helper()
	params := map[string]any{
		"sessionId": agentSessionID,
		"_meta": map[string]any{
			"switchback": map[string]any{
				"agent_turn_id":      agentTurnID,
				"runtime_id":         "runtime-1",
				"runtime_generation": 3,
				"worker_session_id":  "session-1",
			},
		},
	}
	if method == "session/update" {
		params["update"] = map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": "continued",
			},
		}
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{
		"type":      "acp.raw_frame",
		"direction": direction,
		"frame":     string(frame),
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(record, '\n')
}

func TestRunOnceIsFrozenUntilStartupRestoreCompletes(t *testing.T) {
	runner := &runtimeRunner{}
	err := runner.runOnce(context.Background())
	if !errors.Is(err, errRuntimeStartupRestoreInProgress) {
		t.Fatalf("runOnce before startup restore error=%v", err)
	}
}

func TestBackendShutdownLeavesRestoredActiveTurnDurable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &runtimeRunner{now: time.Now}
	live := &liveRuntime{
		events: make(chan workerapp.WorkerControlEvent),
	}
	err := runner.awaitAndSettleTurn(ctx, live, pendingTurn{
		AgentTurnID: "turn-1",
		DeadlineAt:  time.Now().Add(time.Hour),
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Backend shutdown error=%v, want context cancellation", err)
	}
}

func TestStartupPendingTerminalIsSettledBeforeFreshHeartbeatBarrier(
	t *testing.T,
) {
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			RuntimeInstanceID: "runtime-1",
		},
		TurnID:     "turn-1",
		ActiveTurn: &pendingTurn{AgentTurnID: "turn-1"},
	}
	result := startupRuntimeRestoreResult{Reconnected: true}
	live := &liveRuntime{}
	var calls []string
	updated, completed, gotLive, err := completeStartupRuntimeReconnect(
		context.Background(),
		candidate,
		result,
		startupReconnectCompletionDependencies{
			build: func(
				startupRuntimeCandidate,
				startupRuntimeRestoreResult,
			) (*liveRuntime, error) {
				calls = append(calls, "build")
				return live, nil
			},
			register: func(string, *liveRuntime) error {
				calls = append(calls, "register")
				return nil
			},
			settle: func(
				context.Context,
				string,
				*liveRuntime,
			) (map[string]struct{}, error) {
				calls = append(calls, "settle_terminal")
				return map[string]struct{}{"turn-1": {}}, nil
			},
			refresh: func(
				_ context.Context,
				candidate startupRuntimeCandidate,
			) (startupRuntimeCandidate, error) {
				calls = append(calls, "refresh_runtime_owner")
				candidate.ActiveTurn = nil
				return candidate, nil
			},
			awaitActive: func(
				_ context.Context,
				candidate startupRuntimeCandidate,
			) (bool, error) {
				calls = append(calls, "fresh_heartbeat")
				if candidate.ActiveTurn != nil {
					t.Fatal(
						"settled Turn reached heartbeat barrier as active",
					)
				}
				return true, nil
			},
			readActiveTurn: func(
				context.Context,
				startupRuntimeCandidate,
			) (*pendingTurn, error) {
				t.Fatal("settled Turn started a restored supervisor")
				return nil, nil
			},
			unregister: func(string, *liveRuntime) {
				t.Fatal("healthy restored Runtime was unregistered")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"build",
		"register",
		"settle_terminal",
		"refresh_runtime_owner",
		"fresh_heartbeat",
	}
	if !reflect.DeepEqual(calls, wantCalls) ||
		updated.ActiveTurn != nil ||
		!completed.Reconnected ||
		gotLive != live {
		t.Fatalf(
			"calls=%v candidate=%+v result=%+v live=%p",
			calls,
			updated,
			completed,
			gotLive,
		)
	}
}

func TestStartupRecoverySignalStopsReconnectBeforeFreshHeartbeat(
	t *testing.T,
) {
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			RuntimeInstanceID: "runtime-1",
		},
		TurnID:     "turn-1",
		ActiveTurn: &pendingTurn{AgentTurnID: "turn-1"},
	}
	result := startupRuntimeRestoreResult{Reconnected: true}
	live := &liveRuntime{}
	var calls []string
	_, completed, gotLive, err := completeStartupRuntimeReconnect(
		context.Background(),
		candidate,
		result,
		startupReconnectCompletionDependencies{
			build: func(
				startupRuntimeCandidate,
				startupRuntimeRestoreResult,
			) (*liveRuntime, error) {
				calls = append(calls, "build")
				return live, nil
			},
			register: func(string, *liveRuntime) error {
				calls = append(calls, "register")
				return nil
			},
			settle: func(
				context.Context,
				string,
				*liveRuntime,
			) (map[string]struct{}, error) {
				calls = append(calls, "apply_ignore_then_open_recovery")
				return nil, errRuntimeRecoverySettled
			},
			refresh: func(
				context.Context,
				startupRuntimeCandidate,
			) (startupRuntimeCandidate, error) {
				t.Fatal("recovery signal refreshed a reconnect candidate")
				return startupRuntimeCandidate{}, nil
			},
			awaitActive: func(
				context.Context,
				startupRuntimeCandidate,
			) (bool, error) {
				t.Fatal("recovery signal reached fresh-heartbeat barrier")
				return false, nil
			},
			readActiveTurn: func(
				context.Context,
				startupRuntimeCandidate,
			) (*pendingTurn, error) {
				t.Fatal("recovery signal started a restored supervisor")
				return nil, nil
			},
			unregister: func(runtimeID string, got *liveRuntime) {
				if runtimeID != "runtime-1" || got != live {
					t.Fatalf(
						"unregister runtime=%s live=%p",
						runtimeID,
						got,
					)
				}
				calls = append(calls, "unregister")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"build",
		"register",
		"apply_ignore_then_open_recovery",
		"unregister",
	}
	if !reflect.DeepEqual(calls, wantCalls) ||
		completed.Reconnected ||
		gotLive != nil {
		t.Fatalf(
			"calls=%v result=%+v live=%p",
			calls,
			completed,
			gotLive,
		)
	}
}
