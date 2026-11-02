package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

const (
	runtimeReadyTimeout             = 3 * time.Minute
	runtimePollInterval             = 250 * time.Millisecond
	maxStartupDeadlineDrainBatches  = 1024
	initialAgentSessionBindAttempts = 3
)

var errRuntimeRecoverySettled = errors.New("Runtime recovery settled the active AgentTurn")
var errRuntimeStartupRestoreInProgress = errors.New("Runtime startup restore is in progress")
var errBackendDeadlineSourceHandoff = errors.New(
	"Backend deadline source owns the active AgentTurn",
)

type runtimeRunner struct {
	configuration Config
	pool          *pgxpool.Pool
	ids           randomIDs
	now           func() time.Time
	hub           *controlgrpc.Hub
	environment   *runtimeEnvironment
	engine        io.Closer
	provisioner   *dockeradapter.Provisioner
	workerPolicy  *workerPolicyAdmission
	startPolicy   runtimeStartPolicy
	initialPolicy runtimeStartPolicy
	objects       *artifactlocal.ObjectStore
	display       projectionpostgres.Materializer
	transient     *projectionapp.TransientBroker

	allocator                    workerapp.WorkerSessionAllocator
	dispatch                     taskapp.SessionDispatchService
	starts                       workerapp.RuntimeCoordinator
	binding                      workerapp.AgentSessionBindingService
	residency                    workerapp.RuntimeResidencyService
	queued                       workerapp.QueuedTurnService
	settlements                  workerapp.TurnSettlementService
	domainSettle                 workerapp.TurnSettlementDomainService
	settlerID                    string
	finalizer                    artifactapp.TurnEvidenceFinalizer
	publisher                    artifactapp.ArtifactMetadataPublisher
	delivery                     taskapp.TaskDeliveryService
	pauses                       taskapp.TaskPauseService
	taskActions                  taskapp.TaskActionService
	reconciler                   workerapp.UnownedRuntimeReconciler
	cleanup                      workerapp.RuntimeCleanupService
	recoveryStore                *workerpostgres.RuntimeRecoveryStore
	failures                     *workerpostgres.RuntimeFailureObserver
	recovery                     workerapp.RuntimeRecoveryCoordinator
	deadlineSourceEffects        workerapp.BackendDeadlineSourceEffects
	applyDeadlineTerminalWitness func(
		context.Context,
		workerapp.TurnSettlement,
	) (workerapp.TurnSettlement, error)
	durableTerminalReconciler func(
		context.Context,
		string,
	) (bool, error)
	unownedRuntimeRestorer func(
		context.Context,
		string,
	) (*restoredStartupTurn, error)
	scheduled platformpostgres.ScheduledCommandPoller

	startupReady                   atomic.Bool
	mu                             sync.Mutex
	runtimes                       map[string]*liveRuntime
	initializingSessionGenerations map[string]uint64
	pendingInitialActivations      map[string]*pendingInitialActivation
	adoptingRuntimeIDs             map[string]struct{}
	turnSupervisors                activeTurnSupervisors
}

type activeTurnSupervisors struct {
	mu       sync.Mutex
	active   map[string]struct{}
	stopping bool
	group    sync.WaitGroup
}

func (supervisors *activeTurnSupervisors) start(
	agentTurnID string,
	supervise func(),
) (bool, error) {
	if !canonical(agentTurnID) || supervise == nil {
		return false, fmt.Errorf("active Turn supervisor requires an exact AgentTurn")
	}
	supervisors.mu.Lock()
	defer supervisors.mu.Unlock()
	if supervisors.stopping {
		return false, nil
	}
	if _, exists := supervisors.active[agentTurnID]; exists {
		return false, nil
	}
	if supervisors.active == nil {
		supervisors.active = make(map[string]struct{})
	}
	supervisors.active[agentTurnID] = struct{}{}
	supervisors.group.Add(1)
	go func() {
		defer func() {
			supervisors.mu.Lock()
			delete(supervisors.active, agentTurnID)
			supervisors.mu.Unlock()
			supervisors.group.Done()
		}()
		supervise()
	}()
	return true, nil
}

func (supervisors *activeTurnSupervisors) stopAndWait() {
	supervisors.mu.Lock()
	supervisors.stopping = true
	supervisors.mu.Unlock()
	supervisors.group.Wait()
}

type liveRuntime struct {
	initialRunID, initialTurnID string
	mu                          sync.Mutex
	identity                    controlgrpc.RuntimeIdentity
	agentSessionID              string
	storage                     workerapp.SessionStorage
	sourceDigest                string
	events                      <-chan workerapp.WorkerControlEvent
	traceOffset                 int64
	containerID                 string
}

type runtimeReadyPayload struct {
	AgentSessionID       string                      `json:"agent_session_id"`
	AgentProfileID       string                      `json:"agent_profile_id"`
	AgentProfileRevision uint64                      `json:"agent_profile_revision"`
	AgentProfileDigest   string                      `json:"agent_profile_digest"`
	Capabilities         workerapp.AgentCapabilities `json:"capabilities"`
}

type pendingTurn struct {
	TenantID                 string
	UserID                   string
	WorkspaceID              string
	TaskID                   string
	TaskRevision             uint64
	WorkerSessionID          string
	SessionRevision          uint64
	WorkerRunID              string
	RunRevision              uint64
	AgentTurnID              string
	TurnRevision             uint64
	AgentSessionID           string
	RuntimeInstanceID        string
	RuntimeRevision          uint64
	RuntimeGeneration        uint64
	NetworkPolicyID          string
	NetworkPolicyRevision    uint64
	NetworkPolicyDigest      string
	NetworkEnforcementStatus string
	StartedAt                time.Time
	DeadlineAt               time.Time
	Content                  json.RawMessage
}

type activeDisplayStream struct {
	stream            *projectionapp.ACPDisplayStream
	checkpointHealthy bool
	transientHealthy  bool
}

type turnTerminalResult struct {
	reason      string
	timedOut    bool
	forcedStop  bool
	sourceKind  workerapp.TurnOutcomeSource
	occurredAt  time.Time
	sourceEvent *workerapp.WorkerControlEvent
}

func classifyAgentErrorTerminal(
	code string,
	deadlineTriggered bool,
) (turnTerminalResult, bool, error) {
	if !canonical(code) {
		return turnTerminalResult{}, false, fmt.Errorf(
			"Agent error update code is invalid",
		)
	}
	if deadlineTriggered {
		return turnTerminalResult{
			reason:   "agent_turn_deadline",
			timedOut: true,
		}, false, nil
	}
	if code == "acp_eof" {
		return turnTerminalResult{}, true, nil
	}
	return turnTerminalResult{reason: "agent_error"}, false, nil
}

func residentTargetsForTerminal(
	terminal turnTerminalResult,
) (
	workerdomain.WorkerSessionStatus,
	workerdomain.WorkerRunStatus,
	workerdomain.AgentTurnStatus,
) {
	sessionTarget := workerdomain.SessionInteractive
	runTarget := workerdomain.RunCompleted
	turnTarget := workerdomain.TurnCompleted
	switch {
	case terminal.timedOut:
		sessionTarget = workerdomain.SessionWaitingUser
		runTarget = workerdomain.RunTimedOut
		turnTarget = workerdomain.TurnTimedOut
	case terminal.reason == "agent_error":
		sessionTarget = workerdomain.SessionWaitingUser
		runTarget = workerdomain.RunFailed
		turnTarget = workerdomain.TurnFailed
	case terminalReasonCancelled(terminal.reason):
		runTarget = workerdomain.RunCancelled
		turnTarget = workerdomain.TurnCancelled
	}
	return sessionTarget, runTarget, turnTarget
}

func newRuntimeControlJournalService(
	pool *pgxpool.Pool,
	now func() time.Time,
) workerapp.RuntimeControlJournalService {
	return workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   now,
	}
}

func newRuntimeRunner(
	configuration Config,
	pool *pgxpool.Pool,
	objects *artifactlocal.ObjectStore,
	display projectionpostgres.Materializer,
	transient *projectionapp.TransientBroker,
	initialPolicy runtimeStartPolicy,
) (*runtimeRunner, error) {
	engine, err := dockeradapter.NewClient()
	if err != nil {
		return nil, fmt.Errorf("connect Docker Engine: %w", err)
	}
	verifier := dockeradapter.HostPathVerifier(dockeradapter.LocalHostPathVerifier{})
	if configuration.Profile.Containerized && configuration.DataRoot != configuration.EngineDataRoot {
		verifier = dockeradapter.MappedHostPathVerifier{
			EngineRoot: configuration.EngineDataRoot, BackendRoot: configuration.DataRoot,
		}
	}
	provisioner, err := dockeradapter.NewProvisioner(engine, dockeradapter.Options{
		Image: configuration.WorkerImage, Network: configuration.WorkerNetwork,
		EngineDataRoot:     configuration.EngineDataRoot,
		BackendGRPCTarget:  configuration.BackendGRPCTarget,
		BackendHTTPBaseURL: configuration.BackendHTTPBaseURL,
		HostPathVerifier:   verifier,
	})
	if err != nil {
		engine.Close()
		return nil, err
	}
	now := time.Now
	ids := randomIDs{}
	settlerID, err := ids.NewID("turn-settlement-owner")
	if err != nil {
		engine.Close()
		return nil, fmt.Errorf("generate Turn settlement owner: %w", err)
	}
	observationParticipant := &artifactpostgres.ObservationCaptureStore{}
	journalStore, err := workerpostgres.NewRuntimeControlJournalWithObservation(pool, observationParticipant)
	if err != nil {
		engine.Close()
		return nil, err
	}
	workerPolicy := &workerPolicyAdmission{
		lookup: func(ctx context.Context, runtimeID string) (workerPolicyOwner, error) {
			return loadWorkerPolicyOwner(ctx, pool, runtimeID)
		},
		inspect: provisioner.VerifyWorkerPolicy,
		handoff: provisioner.LookupWorkerPolicyHandoff,
	}
	journal := workerapp.RuntimeControlJournalService{Store: workerPolicy.guardJournal(journalStore), Now: now}
	hub := controlgrpc.NewHubWithJournal(journal)
	recoveryStore := workerpostgres.NewRuntimeRecoveryStore(pool)
	recoveryService := workerapp.RuntimeRecoveryService{
		Store: recoveryStore, IDs: ids, Now: now,
	}
	failureObserver := workerpostgres.NewGatedRuntimeFailureObserver(
		pool,
		recoveryService,
		configuration.RuntimeRecoveryEnabled,
	)
	if err := hub.SetEventObserver(localRuntimeControlObserver{
		heartbeat: workerapp.RuntimeHeartbeatObserver{
			Service: workerapp.RuntimeHeartbeatService{
				Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
				IDs:   ids,
			},
		},
	}); err != nil {
		engine.Close()
		return nil, fmt.Errorf("configure Runtime control observer: %w", err)
	}
	environment := newRuntimeEnvironment(configuration, hub)
	runner := &runtimeRunner{
		configuration: configuration, pool: pool, ids: ids, now: now, hub: hub,
		environment: environment, engine: engine, provisioner: provisioner, objects: objects,
		workerPolicy:  workerPolicy,
		startPolicy:   unavailableRuntimeStartPolicy{checker: provisioner},
		initialPolicy: initialPolicy,
		display:       display, transient: transient,
		runtimes: make(map[string]*liveRuntime), settlerID: settlerID,
	}
	if err := hub.SetModelObservationAcceptor(workerapp.ModelObservationAcceptor{Preparer: runner}); err != nil {
		engine.Close()
		return nil, err
	}
	runner.allocator = workerapp.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids, Now: now,
	}
	runner.dispatch = taskapp.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids, Now: now, PermitTTL: 5 * time.Minute,
	}
	runner.starts = workerapp.RuntimeCoordinator{
		CreatedLayerChecker: initialCreatedLayerChecker(initialPolicy),
		Authorizer:          workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool),
		Starts:              workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: now},
		Environment:         environment, Provisioner: provisioner,
	}
	runner.binding = workerapp.AgentSessionBindingService{
		Store: workerpostgres.NewAgentSessionBindingStore(pool), IDs: ids, Now: now,
	}
	runner.residency = workerapp.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: now,
	}
	runner.queued = workerapp.QueuedTurnService{
		Store: workerpostgres.NewQueuedTurnStore(pool), IDs: ids, Now: now,
	}
	settlementRepository, err := workerpostgres.NewTurnSettlementRepositoryWithObservation(pool, observationParticipant)
	if err != nil {
		engine.Close()
		return nil, err
	}
	runner.settlements = workerapp.TurnSettlementService{
		Store: settlementRepository, Now: now,
	}
	runner.domainSettle = workerapp.TurnSettlementDomainService{
		Store: settlementRepository, Now: now,
	}
	runner.finalizer = artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: ids, Now: now,
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1",
			TraceLifecyclePolicyRef:    "trace-default-v1",
		},
	}
	runner.publisher = artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool), IDs: ids, Now: now,
	}
	runner.delivery = taskapp.TaskDeliveryService{
		Store: taskpostgres.NewDeliveryStore(pool), IDs: ids, Now: now,
	}
	runner.pauses = taskapp.TaskPauseService{Store: taskpostgres.NewStore(pool), IDs: ids, Now: now}
	runner.taskActions = taskapp.TaskActionService{Store: taskpostgres.NewStore(pool), IDs: ids, Now: now}
	runner.reconciler = workerapp.UnownedRuntimeReconciler{
		Store: workerpostgres.NewRuntimeLossStore(pool), Cleaner: provisioner,
		Control: hub, IDs: ids, Now: now,
	}
	runner.cleanup = workerapp.RuntimeCleanupService{
		Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: provisioner,
		Control: runtimeObservationCleanupControl{WorkerControl: hub, runner: runner}, IDs: ids, Now: now, RetryDelay: time.Minute,
	}
	runner.recoveryStore = recoveryStore
	runner.failures = failureObserver
	runner.recovery = workerapp.RuntimeRecoveryCoordinator{
		Recovery: recoveryService,
		DeadlineSources: workerapp.TurnSettlementSourceService{
			Store: settlementRepository,
			Now:   now,
		},
		Effects:     localRuntimeRecoveryEffects{runner: runner},
		Provisioner: provisioner,
	}
	runner.deadlineSourceEffects = localBackendDeadlineSourceEffects{
		runner: runner,
	}
	runner.scheduled = platformpostgres.ScheduledCommandPoller{
		Pool: pool,
		Handler: localScheduledCommandHandler{
			lifecycle: workerpostgres.LifecycleCommandHandler{Pool: pool, Now: now},
			cleanup:   workerpostgres.RuntimeCleanupCommandHandler{Service: runner.cleanup},
			liveness: workerpostgres.RuntimeLivenessCommandHandler{
				Liveness: workerapp.RuntimeLivenessService{
					Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids, Now: now,
				},
				Recovery: workerapp.RuntimeRecoveryService{
					Store: recoveryStore, IDs: ids, Now: now,
				},
				DisableRecovery: !configuration.RuntimeRecoveryEnabled,
			},
			firstHeartbeat: workerpostgres.RuntimeFirstHeartbeatCommandHandler{
				Watchdog: workerapp.RuntimeFirstHeartbeatService{
					Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
					IDs:   ids,
					Now:   now,
				},
				Recovery: workerapp.RuntimeRecoveryService{
					Store: recoveryStore,
					IDs:   ids,
					Now:   now,
				},
				DisableRecovery: !configuration.RuntimeRecoveryEnabled,
			},
			deadline: backendDeadlineClaimHandler{
				Pool: pool,
				Source: workerapp.TurnSettlementSourceService{
					Store: settlementRepository,
					Now:   now,
				},
			},
			deadlineReconcile: runner.
				handleBackendDeadlineReconcileCommand,
		},
		LeaseOwner: "localproduct-scheduled", LeaseDuration: 30 * time.Second, BatchSize: 32, Now: now,
	}
	return runner, nil
}

func newRuntimeKnownTokenScanner(configuration Config) (*secretcheck.Scanner, error) {
	tokens := make([]secretcheck.KnownToken, 0, 5)
	seen := make(map[string]struct{})
	for _, token := range []secretcheck.KnownToken{
		{Label: "langfuse-public-key", Value: configuration.LangfusePublicKey},
		{Label: "langfuse-secret-key", Value: configuration.LangfuseSecretKey},
		{Label: "assistant-proxy-key", Value: configuration.AssistantLiteLLMAPIKey},
	} {
		if token.Value == "" {
			continue
		}
		if _, duplicate := seen[token.Value]; duplicate {
			continue
		}
		seen[token.Value] = struct{}{}
		tokens = append(tokens, token)
	}
	for _, candidate := range []struct {
		label string
		path  string
	}{
		{label: "kimi-provider-key", path: configuration.KimiCredentialFile},
		{label: "deepseek-provider-key", path: configuration.DeepSeekCredentialFile},
	} {
		credential, err := readProviderCredential(candidate.path)
		if err != nil {
			continue
		}
		value := string(credential)
		zero(credential)
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		tokens = append(tokens, secretcheck.KnownToken{Label: candidate.label, Value: value})
	}
	return secretcheck.New(tokens)
}

func (runner *runtimeRunner) Close() {
	if runner != nil && runner.engine != nil {
		_ = runner.engine.Close()
	}
}

func (runner *runtimeRunner) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", runner.configuration.GRPCAddress)
	if err != nil {
		return fmt.Errorf("listen for WorkerControl: %w", err)
	}
	defer listener.Close()
	server := grpc.NewServer()
	controlgrpc.RegisterWorkerControlRPCServer(server, controlgrpc.NewServer(runner.hub))
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	defer server.GracefulStop()

	startupDeadlines := runner.startupDeadlineScheduledCommandPoller()
	if err := drainStartupDeadlineCommands(ctx, startupDeadlines); err != nil {
		return fmt.Errorf("drain startup AgentTurn deadlines: %w", err)
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer func() {
		cancelWorkers()
		runner.turnSupervisors.stopAndWait()
	}()
	deadlineSourceDone := make(chan error, 1)
	go func() {
		deadlineSourceDone <- runner.runBackendDeadlineSourceWorker(workerCtx)
	}()
	startupDeadlineCtx, cancelStartupDeadlines :=
		context.WithCancel(workerCtx)
	startupDeadlineDone := make(chan error, 1)
	startupDeadlineExited := make(chan error, 1)
	go func() {
		workerErr := runStartupDeadlineCommands(
			startupDeadlineCtx,
			startupDeadlines,
		)
		startupDeadlineDone <- workerErr
		startupDeadlineExited <- workerErr
	}()
	startupDeadlineStopped := false
	var startupDeadlineErr error
	stopStartupDeadlines := func() error {
		if !startupDeadlineStopped {
			cancelStartupDeadlines()
			startupDeadlineErr = <-startupDeadlineDone
			startupDeadlineStopped = true
		}
		return startupDeadlineErr
	}
	defer stopStartupDeadlines()

	restoredTurns, restoreErr := awaitStartupRuntimeRestore(
		ctx,
		startupDeadlineExited,
		func(restoreCtx context.Context) (
			[]restoredStartupTurn,
			error,
		) {
			quarantine, err :=
				runner.prepareSettlementStartupBarrier(restoreCtx)
			if err != nil {
				return nil, fmt.Errorf(
					"prepare settlement startup barrier: %w",
					err,
				)
			}
			return runner.restoreStartupRuntimes(
				restoreCtx,
				quarantine,
			)
		},
	)
	if restoreErr != nil {
		return errors.Join(restoreErr, stopStartupDeadlines())
	}
	select {
	case startupDeadlineErr = <-startupDeadlineDone:
		startupDeadlineStopped = true
		if startupDeadlineErr != nil {
			return startupDeadlineErr
		}
	default:
	}
	scheduledDone := make(chan error, 1)
	go func() { scheduledDone <- runner.runScheduledCommands(workerCtx) }()
	if err := stopStartupDeadlines(); err != nil {
		return fmt.Errorf(
			"handoff startup AgentTurn deadline worker: %w",
			err,
		)
	}
	if err := drainStartupDeadlineCommands(
		ctx,
		runner.startupDeadlineScheduledCommandPoller(),
	); err != nil {
		return fmt.Errorf(
			"drain AgentTurn deadlines after worker handoff: %w",
			err,
		)
	}
	if !runner.configuration.RuntimeRecoveryEnabled {
		if err := runner.rejectUnownedRuntimes(ctx); err != nil {
			if !runtimeStagingWait(err) {
				return err
			}
			slog.Warn("startup retains pending initial Runtime ownership", "error", err)
		}
	}
	runner.startupReady.Store(true)
	for _, restored := range restoredTurns {
		runner.superviseRestoredTurn(workerCtx, restored)
	}
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-serveErrors:
			return fmt.Errorf("WorkerControl server stopped: %w", err)
		case err := <-scheduledDone:
			return err
		case err := <-deadlineSourceDone:
			if err != nil {
				return fmt.Errorf(
					"Backend deadline source worker stopped: %w",
					err,
				)
			}
			return nil
		case <-ticker.C:
			if err := runner.runOnce(workerCtx); err != nil &&
				!errors.Is(err, context.Canceled) {
				slog.Error("run local Worker delivery step", "error", err)
			}
		}
	}
}

type startupRuntimeRestoreFunc func(
	context.Context,
) ([]restoredStartupTurn, error)

type startupRuntimeRestoreOutcome struct {
	restored []restoredStartupTurn
	err      error
}

func awaitStartupRuntimeRestore(
	ctx context.Context,
	startupDeadlineExited <-chan error,
	restore startupRuntimeRestoreFunc,
) ([]restoredStartupTurn, error) {
	if startupDeadlineExited == nil || restore == nil {
		return nil, fmt.Errorf(
			"startup Runtime restore dependencies are required",
		)
	}
	restoreCtx, cancelRestore := context.WithCancel(ctx)
	defer cancelRestore()
	outcomes := make(chan startupRuntimeRestoreOutcome, 1)
	go func() {
		restored, err := restore(restoreCtx)
		outcomes <- startupRuntimeRestoreOutcome{
			restored: restored,
			err:      err,
		}
	}()
	for {
		select {
		case outcome := <-outcomes:
			select {
			case workerErr := <-startupDeadlineExited:
				return nil, errors.Join(
					startupDeadlineWorkerExitError(workerErr),
					outcome.err,
				)
			default:
			}
			return outcome.restored, outcome.err
		case workerErr := <-startupDeadlineExited:
			cancelRestore()
			outcome := <-outcomes
			return nil, errors.Join(
				startupDeadlineWorkerExitError(workerErr),
				outcome.err,
			)
		case <-ctx.Done():
			cancelRestore()
			outcome := <-outcomes
			return nil, errors.Join(ctx.Err(), outcome.err)
		}
	}
}

func startupDeadlineWorkerExitError(err error) error {
	if err == nil {
		return fmt.Errorf(
			"startup AgentTurn deadline worker stopped unexpectedly",
		)
	}
	return fmt.Errorf(
		"startup AgentTurn deadline worker stopped: %w",
		err,
	)
}

func (runner *runtimeRunner) runOnce(ctx context.Context) error {
	return runner.runOnceWith(
		ctx,
		runtimeRunOnceDependencies{
			reconcileSettlement: runner.reconcileOnePendingTurnSettlement,
			reconcileInitial:    runner.reconcileInitialActivation,
			startAdoption:       runner.restoreOneUnownedRuntime,
			detectFailures:      runner.detectRuntimeFailures,
			reconcileRecovery:   runner.reconcileRuntimeRecovery,
			cleanupTerminal:     runner.cleanupTerminalRuntime,
			startQueuedTask:     runner.startNextQueuedTask,
			executeTurn:         runner.executeNextDeliveredTurn,
		},
	)
}

type runtimeRunOnceDependencies struct {
	reconcileInitial    func(context.Context) error
	reconcileSettlement func(
		context.Context,
	) (string, bool, error)
	startAdoption     func(context.Context) (bool, error)
	detectFailures    func(context.Context) error
	reconcileRecovery func(
		context.Context,
	) (workerapp.RuntimeRecoveryState, bool, error)
	cleanupTerminal func(context.Context) error
	startQueuedTask func(context.Context) error
	executeTurn     func(context.Context) error
}

func (runner *runtimeRunner) runOnceWith(
	ctx context.Context,
	dependencies runtimeRunOnceDependencies,
) error {
	if !runner.startupReady.Load() {
		return errRuntimeStartupRestoreInProgress
	}
	if dependencies.reconcileSettlement == nil ||
		dependencies.startAdoption == nil ||
		dependencies.detectFailures == nil ||
		dependencies.reconcileRecovery == nil ||
		dependencies.cleanupTerminal == nil ||
		dependencies.startQueuedTask == nil ||
		dependencies.executeTurn == nil {
		return fmt.Errorf("Runtime runOnce dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var capacityWait error
	if _, found, err := dependencies.reconcileSettlement(ctx); err != nil {
		if singleRuntimeCause(err) == errRuntimeSettlementOwnerBusy {
			return nil
		}
		if !runtimeStagingWait(err) {
			return err
		}
		capacityWait = err
	} else if found {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep draining already-owned work after a classified wait at any stage.
	rememberWait := func(err error) error {
		if err != nil && !runtimeStagingWait(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if capacityWait == nil {
			capacityWait = err
		}
		return nil
	}
	if dependencies.reconcileInitial != nil {
		if err := rememberWait(dependencies.reconcileInitial(ctx)); err != nil {
			return err
		}
	}
	_, err := dependencies.startAdoption(ctx)
	if err = rememberWait(err); err != nil {
		return err
	}
	if err = rememberWait(dependencies.detectFailures(ctx)); err != nil {
		return err
	}
	_, _, err = dependencies.reconcileRecovery(ctx)
	if err = rememberWait(err); err != nil {
		return err
	}
	if err = rememberWait(dependencies.cleanupTerminal(ctx)); err != nil {
		return err
	}
	// An accepted continuation is still pending. Drain existing work and
	// cleanup, but do not turn the unavailable next start into completion.
	if capacityWait != nil {
		return capacityWait
	}
	if err := dependencies.startQueuedTask(ctx); err != nil {
		return err
	}
	return dependencies.executeTurn(ctx)
}

func (runner *runtimeRunner) detectRuntimeFailures(ctx context.Context) error {
	rows, err := runner.pool.Query(ctx, `
SELECT session.worker_session_id,runtime.runtime_instance_id,
       runtime.generation,runtime.control_channel_epoch,COALESCE(runtime.container_id,'')
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id
WHERE session.tenant_id=$1 AND session.user_id=$2
  AND session.status IN ('interactive','running','waiting_user','idle')
  AND runtime.status IN ('starting','active','suspect')
ORDER BY session.updated_at,session.worker_session_id`,
		runner.configuration.Profile.TenantID,
		runner.configuration.Profile.ActorUserID,
	)
	if err != nil {
		return err
	}
	type candidate struct {
		sessionID, runtimeID, containerID string
		generation, epoch                 uint64
	}
	var candidates []candidate
	for rows.Next() {
		var candidate candidate
		if err := rows.Scan(
			&candidate.sessionID, &candidate.runtimeID,
			&candidate.generation, &candidate.epoch, &candidate.containerID,
		); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var pending error
	for _, candidate := range candidates {
		runner.mu.Lock()
		initializing := runner.initializingSessionGenerations[candidate.sessionID] == candidate.generation
		runner.mu.Unlock()
		if initializing {
			continue
		}
		if runner.runtimeAdoptionInFlight(candidate.runtimeID) {
			continue
		}
		if err := runner.interruptedInitialRuntime(ctx, candidate.runtimeID); err != nil {
			if !runtimeStagingWait(err) {
				return err
			}
			if pending == nil {
				pending = err
			}
			continue
		}
		inspection, err := runner.provisioner.Inspect(ctx, candidate.runtimeID)
		if err != nil {
			return err
		}
		if inspection.Exists && inspection.Running {
			continue
		}
		if inspection.Exists && (inspection.ContainerID != candidate.containerID || inspection.Generation != candidate.generation) {
			return fmt.Errorf("Runtime hard inspection identity conflict")
		}
		trigger := workerdomain.RecoveryTriggerContainerMissing
		if inspection.Exists {
			trigger = workerdomain.RecoveryTriggerContainerExit
		}
		_, _, err = runner.failures.OpenDetectedRuntimeFailure(
			ctx,
			workerpostgres.DetectedRuntimeFailure{
				WorkerSessionID: candidate.sessionID, RuntimeInstanceID: candidate.runtimeID,
				RuntimeGeneration: candidate.generation, ChannelEpoch: candidate.epoch,
				Trigger: trigger, EvidenceRef: "runtime-inspection/" + candidate.runtimeID + "/g" +
					strconv.FormatUint(candidate.generation, 10),
				EvidenceAt: inspection.ObservedAt, Inspection: &inspection,
			},
		)
		if err != nil {
			return err
		}
	}
	return pending
}

func (runner *runtimeRunner) reconcileRuntimeRecovery(
	ctx context.Context,
) (workerapp.RuntimeRecoveryState, bool, error) {
	state, found, err := runner.recoveryStore.FindActiveRuntimeRecovery(
		ctx,
		runner.configuration.Profile.TenantID,
	)
	if err != nil || !found {
		return state, found, err
	}
	state, err = runner.recovery.Reconcile(ctx, state)
	return state, true, err
}

func (runner *runtimeRunner) runScheduledCommands(ctx context.Context) error {
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		if _, err := runner.scheduled.RunOnce(ctx); err != nil {
			if !errors.Is(err, context.Canceled) ||
				errors.Is(
					err,
					platformpostgres.ErrScheduledCommandFinalization,
				) {
				slog.Error(
					"run local ScheduledCommand",
					"error",
					err,
				)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (runner *runtimeRunner) startupDeadlineScheduledCommandPoller() platformpostgres.ScheduledCommandPoller {
	poller := runner.scheduled
	poller.LeaseOwner = runner.scheduled.LeaseOwner + "-startup-deadline"
	poller.TenantID = runner.configuration.Profile.TenantID
	poller.CommandTypes = []string{
		workerpostgres.CommandAgentTurnDeadline,
		workerpostgres.CommandRuntimeFirstHeartbeatEvaluate,
	}
	return poller
}

func drainStartupDeadlineCommands(
	ctx context.Context,
	poller platformpostgres.ScheduledCommandPoller,
) error {
	appliedBatches := 0
	for {
		applied, err := poller.RunOnce(ctx)
		if err != nil {
			return err
		}
		outstanding, err := poller.HasDueOutstanding(ctx)
		if err != nil {
			return err
		}
		if !outstanding {
			return nil
		}
		if applied > 0 {
			appliedBatches++
			if appliedBatches >= maxStartupDeadlineDrainBatches {
				return fmt.Errorf(
					"startup AgentTurn deadline drain exceeded %d applied batches",
					maxStartupDeadlineDrainBatches,
				)
			}
			continue
		}
		timer := time.NewTimer(runtimePollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runStartupDeadlineCommands(
	ctx context.Context,
	poller platformpostgres.ScheduledCommandPoller,
) error {
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		if _, err := poller.RunOnce(ctx); err != nil {
			if errors.Is(
				err,
				platformpostgres.ErrScheduledCommandFinalization,
			) {
				return err
			}
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type localScheduledCommandHandler struct {
	lifecycle         workerpostgres.LifecycleCommandHandler
	cleanup           workerpostgres.RuntimeCleanupCommandHandler
	liveness          workerpostgres.RuntimeLivenessCommandHandler
	firstHeartbeat    workerpostgres.RuntimeFirstHeartbeatCommandHandler
	deadline          backendDeadlineClaimHandler
	deadlineReconcile func(
		context.Context,
		platformpostgres.ScheduledCommand,
	) error
}

func (handler localScheduledCommandHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.CommandType == "runtime_cleanup_reconcile" {
		return handler.cleanup.HandleScheduledCommand(ctx, command)
	}
	if command.CommandType == workerpostgres.CommandRuntimeLivenessEvaluate {
		return handler.liveness.HandleScheduledCommand(ctx, command)
	}
	if command.CommandType ==
		workerpostgres.CommandRuntimeFirstHeartbeatEvaluate {
		return handler.firstHeartbeat.HandleScheduledCommand(ctx, command)
	}
	if command.CommandType == workerpostgres.CommandAgentTurnDeadline {
		return handler.deadline.HandleScheduledCommand(ctx, command)
	}
	if command.CommandType ==
		workerpostgres.CommandBackendDeadlineSourceReconcile {
		if handler.deadlineReconcile == nil {
			return fmt.Errorf(
				"Backend deadline reconciliation handler is required",
			)
		}
		return handler.deadlineReconcile(ctx, command)
	}
	return handler.lifecycle.HandleScheduledCommand(ctx, command)
}

func (runner *runtimeRunner) cleanupTerminalRuntime(ctx context.Context) error {
	// Exhausted incidents and committed startup/unowned loss rediscover cleanup
	// after a crash before its first effect. Draining runtimes are already owned
	// by ScheduledCommand; excluding them preserves the existing retry backoff.
	var sessionID, runtimeID, containerID string
	var sessionRevision, runtimeRevision, generation uint64
	err := runner.pool.QueryRow(ctx, `SELECT session.worker_session_id,runtime.runtime_instance_id,
       COALESCE(runtime.container_id,''),session.session_revision,runtime.revision,runtime.generation
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id
WHERE session.tenant_id=$1 AND session.user_id=$2
  AND (session.status IN ('closed','expired') OR
    (session.status='lost' AND runtime.status='lost'
      AND runtime.loss_diagnostic->>'idempotency_key' IN (
        'startup-failed/' || runtime.runtime_instance_id,
        'backend-restart/' || runtime.runtime_instance_id || '/' || runtime.generation::text)
      AND NOT EXISTS (
        SELECT 1 FROM runtime_recovery_incidents incident
        WHERE incident.worker_session_id=session.worker_session_id
          AND incident.phase NOT IN ('recovered','exhausted'))
      AND NOT EXISTS (
        SELECT 1 FROM worker_turn_settlements settlement
        WHERE settlement.worker_session_id=session.worker_session_id
          AND settlement.phase <> 'completed')) OR
    (session.status='waiting_user' AND EXISTS (
      SELECT 1 FROM runtime_recovery_incidents incident
      WHERE incident.worker_session_id=session.worker_session_id
        AND incident.tenant_id=session.tenant_id AND incident.phase='exhausted'
        AND ((incident.replacement_runtime_instance_id=runtime.runtime_instance_id
              AND incident.replacement_generation=runtime.generation
              AND incident.replacement_channel_epoch=runtime.control_channel_epoch)
          OR (incident.replacement_runtime_instance_id IS NULL
              AND incident.failed_runtime_instance_id=runtime.runtime_instance_id
              AND incident.failed_generation=runtime.generation
              AND incident.failed_channel_epoch=runtime.control_channel_epoch)))))
  AND runtime.status IN ('starting','active','lost')
ORDER BY session.updated_at,session.worker_session_id LIMIT 1`, runner.configuration.Profile.TenantID,
		runner.configuration.Profile.ActorUserID).Scan(
		&sessionID, &runtimeID, &containerID, &sessionRevision, &runtimeRevision, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := runner.cleanup.Cleanup(ctx, workerapp.BeginRuntimeCleanupCommand{
		TenantID: runner.configuration.Profile.TenantID, WorkerSessionID: sessionID,
		RuntimeInstanceID: runtimeID, ExpectedContainerID: containerID, RuntimeGeneration: generation,
		ExpectedSessionRevision: sessionRevision, ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey: "terminal-cleanup/" + runtimeID + "/" + strconv.FormatUint(generation, 10),
	})
	if err != nil {
		return err
	}
	if result.Confirmed {
		runner.mu.Lock()
		delete(runner.runtimes, runtimeID)
		runner.mu.Unlock()
	}
	return nil
}

func (runner *runtimeRunner) startNextQueuedTask(ctx context.Context) error {
	var taskID, allocationRequestID, sessionID string
	var issuedPermitIDs []string
	var taskRevision, sessionRevision, queueSequence, queueRevision uint64
	err := runner.pool.QueryRow(ctx, `
SELECT task.task_id,task.revision,allocation.allocation_request_id,
       COALESCE(task.worker_session_id,''),COALESCE(session.session_revision,0),
       COALESCE(task.session_queue_sequence,0),COALESCE(queue.queue_revision,0),
       (SELECT COALESCE(array_agg(p.permit_id ORDER BY p.permit_id COLLATE "C"),ARRAY[]::text[])
        FROM session_dispatch_permits p WHERE p.target_kind='task' AND p.target_id=task.task_id
          AND p.worker_session_id=task.worker_session_id
          AND p.session_queue_sequence=task.session_queue_sequence AND p.queue_revision=queue.queue_revision)
FROM tasks task
JOIN session_allocation_requests allocation
  ON allocation.request_kind='task' AND allocation.owner_id=task.task_id
LEFT JOIN worker_sessions session ON session.worker_session_id=task.worker_session_id
LEFT JOIN session_task_queue_entries queue ON queue.task_id=task.task_id AND queue.worker_session_id=task.worker_session_id AND queue.session_queue_sequence=task.session_queue_sequence AND queue.status IN ('pending','permit_issued')
WHERE task.tenant_id=$1 AND task.initiator_user_id=$2 AND task.status='queued'
ORDER BY task.created_at,task.task_id LIMIT 1`, runner.configuration.Profile.TenantID,
		runner.configuration.Profile.ActorUserID).Scan(
		&taskID, &taskRevision, &allocationRequestID, &sessionID, &sessionRevision, &queueSequence, &queueRevision, &issuedPermitIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read next queued Task: %w", err)
	}
	recordWait := func(cause error) error {
		reason := ""
		leaf := singleRuntimeCause(cause)
		if leaf == errRuntimeStartCapacityUnavailable {
			reason = "capacity_path_not_integrated"
		} else {
			switch leaf {
			case workerapp.CapacityUninitialized:
				reason = "capacity_uninitialized"
			case workerapp.CapacityEvidenceInvalid:
				reason = "capacity_evidence_invalid"
			case workerapp.CapacityEvidenceStale:
				reason = "capacity_evidence_stale"
			case workerapp.CapacityIdentityDrift:
				reason = "capacity_identity_drift"
			case workerapp.CapacityInventoryUnknown:
				reason = "capacity_inventory_unknown"
			case workerapp.CapacityExhausted:
				reason = "capacity_exhausted"
			case workerapp.CapacityPathNotIntegrated:
				reason = "capacity_path_not_integrated"
			}
		}
		if !taskapp.ValidRuntimeDispatchWaitReason(reason) {
			return cause
		}
		_, err := (taskapp.RuntimeDispatchWaitService{Store: taskpostgres.NewStore(runner.pool), IDs: randomIDs{}}).Record(ctx, taskapp.RuntimeDispatchWaitObservation{TenantID: runner.configuration.Profile.TenantID, UserID: runner.configuration.Profile.ActorUserID, TaskID: taskID, ExpectedPermitIDs: issuedPermitIDs, Wait: taskapp.RuntimeDispatchWait{Reason: reason, TaskRevision: taskRevision, WorkerSessionID: sessionID, QueueSequence: queueSequence, QueueRevision: queueRevision}})
		if err != nil {
			return errors.Join(cause, fmt.Errorf("record queued runtime dispatch wait: %w", err))
		}
		return cause
	}
	if err := runner.checkInitialCapacity(ctx); err != nil {
		return recordWait(err)
	}
	if sessionID == "" {
		allocated, err := runner.allocator.Execute(ctx, workerapp.AllocateWorkerSessionCommand{
			TenantID:            runner.configuration.Profile.TenantID,
			UserID:              runner.configuration.Profile.ActorUserID,
			AllocationRequestID: allocationRequestID,
		})
		if err != nil {
			return fmt.Errorf("allocate queued Task WorkerSession: %w", err)
		}
		sessionID = allocated.WorkerSessionID
		sessionRevision = 1
		enqueued, err := runner.dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{
			TenantID: runner.configuration.Profile.TenantID,
			UserID:   runner.configuration.Profile.ActorUserID, TaskID: taskID,
			WorkerSessionID: sessionID, ExpectedTaskRevision: taskRevision,
			ExpectedSessionRevision: sessionRevision,
			IdempotencyKey:          "local-enqueue/" + taskID,
		})
		if err != nil {
			return fmt.Errorf("enqueue Task on allocated WorkerSession: %w", err)
		}
		taskRevision = enqueued.TaskRevision
		queueSequence, queueRevision = enqueued.SessionQueueSequence, enqueued.QueueRevision
	}
	var existingRun string
	err = runner.pool.QueryRow(ctx, `SELECT COALESCE(current_worker_run_id,'') FROM tasks WHERE task_id=$1`, taskID).Scan(&existingRun)
	if err != nil || existingRun != "" {
		return err
	}
	permit, err := runner.dispatch.IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{
		TenantID: runner.configuration.Profile.TenantID,
		UserID:   runner.configuration.Profile.ActorUserID, WorkerSessionID: sessionID,
		RuntimeGeneration: 1, IdempotencyKey: "local-dispatch/" + taskID,
	})
	if err != nil {
		return recordWait(err)
	}
	var specJSON json.RawMessage
	if err := runner.pool.QueryRow(ctx, `SELECT task_spec_snapshot FROM tasks WHERE task_id=$1`, taskID).Scan(&specJSON); err != nil {
		return err
	}
	spec, err := contractsv1.DecodeTaskSpec(specJSON)
	if err != nil {
		return fmt.Errorf("decode persisted TaskSpec: %w", err)
	}
	profile, ok := config.ProductionProfileRegistry().Agent(spec.AgentConfig.ProfileID)
	if !ok {
		return fmt.Errorf("persisted AgentProfile is unavailable")
	}
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveStored(config.RuntimeNetworkEvidence{
		ID:       spec.RuntimeConfig.NetworkPolicy,
		Revision: spec.RuntimeConfig.NetworkPolicyRevision,
		Digest:   spec.RuntimeConfig.NetworkPolicyDigest,
	})
	if err != nil {
		return fmt.Errorf("persisted Runtime network profile is unavailable: %w", err)
	}
	sourceDigest, err := workspaceSourceDigest(runner.configuration.WorkspaceSourceRoot)
	if err != nil {
		return err
	}
	snapshot, _ := json.Marshal(map[string]any{
		"workspace_revision_id":          spec.WorkspaceResolutionRef.WorkspaceRevisionID,
		"working_copy_checkpoint_digest": "sha256:" + sourceDigest,
		"task_spec_digest":               spec.CreationIntentRef.TaskSpecDigest,
	})
	prompt, _ := json.Marshal(map[string]string{"text": taskPrompt(spec)})
	// Ownership commits before environment preparation and Provision. Another
	// Session's terminal waiter may inspect all Runtimes during that window.
	// Protect only this locally owned initializer, including its live handoff.
	runner.mu.Lock()
	if _, owned := runner.initializingSessionGenerations[sessionID]; owned {
		runner.mu.Unlock()
		return fmt.Errorf("WorkerSession %s already has an initializing Runtime", sessionID)
	}
	if runner.initializingSessionGenerations == nil {
		runner.initializingSessionGenerations = make(map[string]uint64)
	}
	runner.initializingSessionGenerations[sessionID] = 1
	runner.mu.Unlock()
	defer func() {
		runner.mu.Lock()
		delete(runner.initializingSessionGenerations, sessionID)
		runner.mu.Unlock()
	}()
	started, err := runner.starts.Start(ctx, workerapp.StartRuntimeCommand{Run: workerapp.StartWorkerRunCommand{
		TenantID: runner.configuration.Profile.TenantID,
		UserID:   runner.configuration.Profile.ActorUserID, WorkerSessionID: sessionID,
		TaskID: taskID, PermitID: permit.PermitID,
		ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: 1,
		TriggerType: "task_initial", StartSnapshot: snapshot,
		AgentProfileID: profile.ID, AgentProfileRevision: strconv.FormatUint(profile.Revision, 10),
		AgentProfileDigest: profile.Digest, AgentTurnContent: prompt,
		TurnIdempotencyKey: "local-turn/" + taskID,
		RuntimeImageRef:    runner.configuration.WorkerImage,
		ResourceProfileID:  config.LocalStandardV1,
		NetworkPolicyID:    network.ID, NetworkPolicyRevision: network.Revision,
		NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: network.EnforcementStatus,
		ControlChannelEpoch: 1,
	}})
	if err != nil {
		return runner.handleInitialRuntimeStartFailure(ctx, started, err)
	}
	// Provision already returned a physical Runtime. NULL database CID does
	// not prove absence; hard ready/binding/cancellation failures retain the
	// durable interrupted owner and prepared storage for explicit reconciliation.
	prepared, ok := runner.environment.get(started.Ownership.RuntimeInstanceID)
	if !ok || prepared.SourceDigest != sourceDigest {
		return fmt.Errorf("prepared WorkingCopy differs from the start snapshot")
	}
	events, err := runner.hub.Events(ctx, started.Ownership.RuntimeInstanceID, 1)
	if err != nil {
		return err
	}
	readyEvent, err := waitWorkerEvent(ctx, events, runtimeReadyTimeout, func(event workerapp.WorkerControlEvent) bool {
		return event.Envelope.MessageType == "runtime.ready"
	})
	if err != nil {
		return err
	}
	var ready runtimeReadyPayload
	if err := json.Unmarshal(readyEvent.Payload, &ready); err != nil ||
		ready.AgentSessionID == "" || ready.AgentProfileID != profile.ID ||
		ready.AgentProfileRevision != profile.Revision || ready.AgentProfileDigest != profile.Digest {
		return fmt.Errorf("runtime.ready identity or profile is invalid")
	}
	bound, err := runner.bindInitialAgentSession(ctx, started.Ownership, ready.AgentSessionID)
	if err != nil {
		return fmt.Errorf("bind initial AgentSession after runtime.ready: %w", err)
	}
	return runner.beginInitialActivation(ctx, started, prepared, bound, ready, readyEvent, events)
}

func (runner *runtimeRunner) bindInitialAgentSession(
	ctx context.Context,
	ownership workerapp.StartedWorkerRun,
	actualAgentSessionID string,
) (workerapp.BoundAgentSession, error) {
	var lastConflict error
	for attempt := 0; attempt < initialAgentSessionBindAttempts; attempt++ {
		var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
		err := runner.pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id AND turn.agent_turn_id=$3
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1 AND session.tenant_id=$5
  AND session.status='running' AND session.active_worker_run_id=run.worker_run_id
  AND session.active_runtime_instance_id=runtime.runtime_instance_id
  AND run.status='starting' AND turn.status='delivered' AND runtime.status='starting'
  AND runtime.generation=$6`,
			ownership.WorkerSessionID,
			ownership.WorkerRunID,
			ownership.AgentTurnID,
			ownership.RuntimeInstanceID,
			runner.configuration.Profile.TenantID,
			ownership.RuntimeGeneration,
		).Scan(&sessionRevision, &runRevision, &turnRevision, &runtimeRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return workerapp.BoundAgentSession{}, platformpostgres.ErrRevisionConflict
		}
		if err != nil {
			return workerapp.BoundAgentSession{}, fmt.Errorf(
				"load post-ready startup revisions: %w",
				err,
			)
		}
		bound, err := runner.binding.Execute(ctx, workerapp.BindAgentSessionCommand{
			TenantID:        runner.configuration.Profile.TenantID,
			WorkerSessionID: ownership.WorkerSessionID,
			WorkerRunID:     ownership.WorkerRunID, AgentTurnID: ownership.AgentTurnID,
			RuntimeInstanceID:       ownership.RuntimeInstanceID,
			ReservedAgentSessionID:  ownership.AgentSessionID,
			ActualAgentSessionID:    actualAgentSessionID,
			RuntimeGeneration:       ownership.RuntimeGeneration,
			ExpectedSessionRevision: sessionRevision,
			ExpectedRunRevision:     runRevision,
			ExpectedTurnRevision:    turnRevision,
			ExpectedRuntimeRevision: runtimeRevision,
			IdempotencyKey:          "agent-session-bound/" + ownership.RuntimeInstanceID,
		})
		if err == nil {
			return bound, nil
		}
		if !errors.Is(err, platformpostgres.ErrRevisionConflict) {
			return workerapp.BoundAgentSession{}, err
		}
		lastConflict = err
	}
	return workerapp.BoundAgentSession{}, fmt.Errorf(
		"bind initial AgentSession after %d revision refresh attempts: %w",
		initialAgentSessionBindAttempts,
		lastConflict,
	)
}

func (runner *runtimeRunner) executeNextDeliveredTurn(ctx context.Context) error {
	turn, err := runner.readPendingTurn(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	runner.mu.Lock()
	live := runner.runtimes[turn.RuntimeInstanceID]
	runner.mu.Unlock()
	if err := runner.checkDeliveredCapacity(ctx, live, turn); err != nil {
		return err
	}
	if live == nil {
		return fmt.Errorf("delivered AgentTurn has no owned live Runtime")
	}
	_, err = runner.turnSupervisors.start(turn.AgentTurnID, func() {
		if superviseErr := runner.superviseDeliveredTurn(
			ctx,
			live,
			turn,
		); superviseErr != nil && !errors.Is(superviseErr, context.Canceled) {
			slog.Error(
				"supervise delivered AgentTurn",
				"agent_turn_id",
				turn.AgentTurnID,
				"error",
				superviseErr,
			)
		}
	})
	return err
}

func (runner *runtimeRunner) superviseDeliveredTurn(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
) error {
	if err := runner.checkDeliveredCapacity(ctx, live, turn); err != nil {
		return err
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if turn.AgentSessionID != live.agentSessionID {
		return fmt.Errorf("delivered AgentTurn AgentSession differs from live Runtime")
	}
	prompt := turnText(turn.Content)
	if prompt == "" {
		return fmt.Errorf("delivered AgentTurn content is empty")
	}
	display := runner.startDisplayStream(ctx, turn)
	payload, _ := json.Marshal(map[string]string{"prompt": prompt})
	messageID := "start-" + turn.AgentTurnID
	err := runner.hub.Send(ctx, workerapp.WorkerControlCommand{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: messageID, Kind: "command",
		WorkerSessionID:   contractsv1.WorkerSessionID(turn.WorkerSessionID),
		WorkerRunID:       contractsv1.WorkerRunID(turn.WorkerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(turn.RuntimeInstanceID),
		RuntimeGeneration: uint32(turn.RuntimeGeneration),
		AgentSessionID:    contractsv1.AgentSessionID(turn.AgentSessionID),
		AgentTurnID:       contractsv1.AgentTurnID(turn.AgentTurnID), ChannelEpoch: live.identity.ChannelEpoch,
		IdempotencyKey: messageID, MessageType: "agent_turn.start", Payload: payload,
	}})
	if err != nil {
		return fmt.Errorf("dispatch AgentTurn to live Runtime: %w", err)
	}
	return runner.awaitAndSettleTurn(ctx, live, turn, display)
}

func (runner *runtimeRunner) superviseRestoredTurn(
	ctx context.Context,
	restored restoredStartupTurn,
) {
	_, err := runner.turnSupervisors.start(
		restored.turn.AgentTurnID,
		func() {
			if superviseErr := runner.awaitRestoredTurn(
				ctx,
				restored,
			); superviseErr != nil &&
				!errors.Is(superviseErr, context.Canceled) {
				slog.Error(
					"supervise restored AgentTurn",
					"agent_turn_id",
					restored.turn.AgentTurnID,
					"error",
					superviseErr,
				)
			}
		},
	)
	if err != nil {
		slog.Error(
			"start restored AgentTurn supervisor",
			"agent_turn_id",
			restored.turn.AgentTurnID,
			"error",
			err,
		)
	}
}

func (runner *runtimeRunner) awaitRestoredTurn(
	ctx context.Context,
	restored restoredStartupTurn,
) error {
	restored.live.mu.Lock()
	defer restored.live.mu.Unlock()
	if restored.turn.AgentSessionID != restored.live.agentSessionID {
		return fmt.Errorf(
			"restored AgentTurn AgentSession differs from live Runtime",
		)
	}
	display := runner.startDisplayStream(ctx, restored.turn)
	return runner.awaitAndSettleTurn(
		ctx,
		restored.live,
		restored.turn,
		display,
	)
}

func (runner *runtimeRunner) awaitAndSettleTurn(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	display *activeDisplayStream,
) error {
	terminal, terminalErr := runner.waitTurnTerminal(ctx, live, turn, display)
	terminalDeliveryHandled := false
	if terminal.sourceEvent != nil && terminal.sourceEvent.Delivery != nil {
		// The terminal inbox lease is deliberately held across evidence,
		// domain and Task settlement. Any return before durable Apply releases
		// it for ordered redelivery.
		defer func() {
			if !terminalDeliveryHandled {
				terminal.sourceEvent.Delivery.ReleaseForRetry()
			}
		}()
	}
	applyTerminal := func() error {
		if terminal.sourceEvent != nil {
			// Apply owns any retry after it is attempted.
			terminalDeliveryHandled = true
		}
		err := applyTerminalWorkerControlEvent(ctx, terminal.sourceEvent)
		return err
	}
	if terminalErr != nil {
		if errors.Is(terminalErr, context.Canceled) {
			// Backend shutdown is not an Agent or Runtime failure. Durable Turn
			// state remains active for the next Backend to resume.
			return terminalErr
		}
		if errors.Is(terminalErr, errRuntimeRecoverySettled) {
			return nil
		}
		if errors.Is(terminalErr, errBackendDeadlineSourceHandoff) {
			return nil
		}
		// Database, transport, validation and orchestration failures are not
		// evidence that the AgentTurn itself failed. Keep the durable Turn
		// active and release any terminal inbox delivery for retry.
		return terminalErr
	}
	if terminal.sourceEvent != nil &&
		terminal.sourceEvent.Envelope.MessageType == "agent_turn.terminal" {
		settlementID, err := workerapp.TurnSettlementID(turn.AgentTurnID)
		if err != nil {
			return err
		}
		settlement, err := runner.settlements.Load(ctx, settlementID)
		if err != nil {
			return err
		}
		classified, recoveryRequired, err :=
			terminalResultFromRuntimeSettlementEvent(
				terminal.sourceEvent,
				settlement,
				turn,
			)
		if err != nil {
			return err
		}
		if recoveryRequired {
			return fmt.Errorf(
				"Runtime recovery terminal reached normal Turn settlement",
			)
		}
		terminal = classified
		checkpoint, applyAttempted, err :=
			runner.reconcileRuntimeEventTurnSettlement(
				ctx,
				live,
				turn,
				terminal,
			)
		terminalDeliveryHandled = applyAttempted
		if err != nil {
			return err
		}
		if checkpoint.EvidenceInput == nil ||
			checkpoint.Evidence == nil ||
			checkpoint.DomainPlan == nil {
			return fmt.Errorf(
				"completed Runtime Turn settlement lacks durable outcome",
			)
		}
		runner.appendDisplayArtifacts(
			ctx,
			display,
			checkpoint.Evidence.Finalized.ArtifactRefs,
		)
		live.traceOffset = checkpoint.EvidenceInput.TraceEndOffset
		return nil
	}
	finalized, nextTraceOffset, err := runner.finalizeTurnEvidence(ctx, live, turn)
	if err != nil {
		return err
	}
	sessionTarget, runTarget, turnTarget :=
		residentTargetsForTerminal(terminal)
	completed, err := runner.residency.CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
		TenantID: turn.TenantID, WorkerSessionID: turn.WorkerSessionID,
		WorkerRunID: turn.WorkerRunID, AgentTurnID: turn.AgentTurnID,
		RuntimeInstanceID: turn.RuntimeInstanceID, RuntimeGeneration: turn.RuntimeGeneration,
		ExpectedSessionRevision: turn.SessionRevision, ExpectedRunRevision: turn.RunRevision,
		ExpectedTurnRevision: turn.TurnRevision, ExpectedRuntimeRevision: turn.RuntimeRevision,
		SessionTarget: sessionTarget, RunTarget: runTarget,
		TurnTarget: turnTarget, TerminalReason: terminal.reason,
	})
	if err != nil {
		return err
	}
	if _, err := runner.publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: completed.SessionRevision,
		ExpectedRunRevision: completed.RunRevision, ExpectedTurnRevision: completed.TurnRevision,
		ExpectedRuntimeRevision: completed.RuntimeRevision,
		IdempotencyKey:          "publish-turn/" + turn.AgentTurnID,
	}); err != nil {
		return err
	}
	runner.appendDisplayArtifacts(ctx, display, finalized.ArtifactRefs)
	live.traceOffset = nextTraceOffset
	if cancelled, revision, err := runner.cancelledTaskRevision(ctx, turn.TaskID); err != nil {
		return err
	} else if cancelled {
		_, err := runner.taskActions.SettleCancelled(ctx, taskapp.SettleCancelledTaskCommand{
			Actor:  taskapp.Actor{TenantID: turn.TenantID, UserID: turn.UserID},
			TaskID: turn.TaskID, WorkerSessionID: turn.WorkerSessionID,
			WorkerRunID: turn.WorkerRunID, ExpectedTaskRevision: revision,
			ExpectedSessionRevision: completed.SessionRevision,
			IdempotencyKey:          "settle-cancel/" + turn.AgentTurnID,
		})
		if err != nil {
			return err
		}
		return applyTerminal()
	}
	continued, err := runner.continueQueuedTurn(ctx, turn, completed)
	if err != nil && !runtimeStagingWait(err) {
		return err
	}
	// Capacity denial concerns only the accepted successor. The previous
	// Turn's report, artifacts and terminal acknowledgement still settle.
	if continued {
		return applyTerminal()
	}
	if turnTarget == workerdomain.TurnTimedOut {
		if err := runner.pauseTaskAfterTurn(ctx, turn, "agent_turn_deadline"); err != nil {
			return err
		}
		return applyTerminal()
	}
	if turnTarget == workerdomain.TurnCancelled {
		if err := runner.pauseTaskAfterTurn(ctx, turn, "user_interrupt"); err != nil {
			return err
		}
		return applyTerminal()
	}
	if turn.TaskID != "" {
		if _, err := runner.delivery.RecordStageResult(ctx, taskapp.RecordStageResultCommand{
			Actor:  taskapp.Actor{TenantID: turn.TenantID, UserID: turn.UserID},
			TaskID: turn.TaskID, WorkerSessionID: turn.WorkerSessionID,
			WorkerRunID: turn.WorkerRunID, ExpectedTaskRevision: turn.TaskRevision,
			ArtifactRefs: finalized.ArtifactRefs, DeterministicEvidence: false,
			IdempotencyKey: "deliver-turn/" + turn.AgentTurnID,
		}); err != nil {
			return err
		}
	}
	return applyTerminal()
}

func (runner *runtimeRunner) startDisplayStream(
	ctx context.Context,
	turn pendingTurn,
) *activeDisplayStream {
	if !runner.configuration.WorkerDisplayEnabled {
		return nil
	}
	stream, err := projectionapp.NewACPDisplayStream(projectionapp.DisplayStreamIdentity{
		TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID,
		WorkerSessionID: turn.WorkerSessionID, WorkerRunID: turn.WorkerRunID,
		AgentTurnID: turn.AgentTurnID, AgentSessionID: turn.AgentSessionID,
		RuntimeGeneration: turn.RuntimeGeneration,
	}, projectionapp.ACPDisplayPolicy{})
	if err != nil {
		slog.Error("initialize ACP Display stream", "agent_turn_id", turn.AgentTurnID, "error", err)
		return nil
	}
	active := &activeDisplayStream{stream: stream, checkpointHealthy: true, transientHealthy: true}
	checkpoint, err := stream.Start(runner.now().UTC())
	if err != nil {
		slog.Error("start ACP Display stream", "agent_turn_id", turn.AgentTurnID, "error", err)
		active.checkpointHealthy = false
		active.transientHealthy = false
		return active
	}
	if !runner.publishDisplayCheckpoint(ctx, active, checkpoint) {
		return active
	}
	if runner.transient == nil {
		active.transientHealthy = false
		return active
	}
	if err := runner.transient.Register(stream.DisplayStreamID(), turn.WorkerSessionID); err != nil {
		slog.Error("register transient Display stream", "agent_turn_id", turn.AgentTurnID, "error", err)
		active.transientHealthy = false
		return active
	}
	if err := runner.transient.Supersede(checkpoint); err != nil {
		slog.Error("fence transient Display stream", "agent_turn_id", turn.AgentTurnID, "error", err)
		active.transientHealthy = false
	}
	return active
}

func (runner *runtimeRunner) applyDisplayUpdate(
	ctx context.Context,
	active *activeDisplayStream,
	sequence uint64,
	kind string,
	payload json.RawMessage,
) {
	if active == nil || !active.checkpointHealthy {
		return
	}
	emission, err := active.stream.Apply(projectionapp.NormalizedDisplayUpdate{
		Sequence: sequence, Kind: kind, Payload: payload, ObservedAt: runner.now().UTC(),
	})
	if err != nil {
		slog.Error("adapt ACP Display update", "display_stream_id", active.stream.DisplayStreamID(), "error", err)
		active.checkpointHealthy = false
		active.transientHealthy = false
		return
	}
	runner.publishDisplayEmission(ctx, active, emission)
}

func (runner *runtimeRunner) terminateDisplayStream(
	ctx context.Context,
	active *activeDisplayStream,
	reason string,
) {
	if active == nil || !active.checkpointHealthy {
		return
	}
	emission, err := active.stream.Terminate(reason, runner.now().UTC())
	if err != nil {
		slog.Error("terminate ACP Display stream", "display_stream_id", active.stream.DisplayStreamID(), "error", err)
		active.checkpointHealthy = false
		active.transientHealthy = false
		return
	}
	runner.publishDisplayEmission(ctx, active, emission)
}

func (runner *runtimeRunner) appendDisplayArtifacts(
	ctx context.Context,
	active *activeDisplayStream,
	refs []contractsv1.ArtifactRef,
) {
	if active == nil || !active.checkpointHealthy || len(refs) == 0 {
		return
	}
	artifacts := make([]projectionapp.DisplayArtifact, 0, len(refs))
	for _, ref := range refs {
		label := "Artifact"
		switch ref.Type {
		case artifactdomain.KindReport:
			label = "Stage report"
		case artifactdomain.KindPatch:
			label = "Patch review"
		}
		artifacts = append(artifacts, projectionapp.DisplayArtifact{
			ArtifactID: ref.ArtifactID, Type: ref.Type, Label: label,
			Href: "/api/v1/views/artifact_detail/" + ref.ArtifactID,
		})
	}
	checkpoint, err := active.stream.AppendArtifacts(artifacts, runner.now().UTC())
	if err != nil {
		slog.Error("append Display Artifacts", "display_stream_id", active.stream.DisplayStreamID(), "error", err)
		active.checkpointHealthy = false
		active.transientHealthy = false
		return
	}
	runner.publishDisplayCheckpoint(ctx, active, checkpoint)
}

func (runner *runtimeRunner) publishDisplayEmission(
	ctx context.Context,
	active *activeDisplayStream,
	emission projectionapp.DisplayEmission,
) {
	if emission.Transient != nil && active.transientHealthy && runner.transient != nil {
		if err := runner.transient.Publish(*emission.Transient); err != nil {
			slog.Error("publish transient Display delta", "display_stream_id", active.stream.DisplayStreamID(), "error", err)
			active.transientHealthy = false
		}
	}
	if emission.Checkpoint != nil {
		runner.publishDisplayCheckpoint(ctx, active, *emission.Checkpoint)
	}
}

func (runner *runtimeRunner) publishDisplayCheckpoint(
	ctx context.Context,
	active *activeDisplayStream,
	checkpoint projectionapp.DisplayCheckpoint,
) bool {
	if err := runner.display.PublishDisplayCheckpoint(ctx, checkpoint); err != nil {
		slog.Error("publish durable Display checkpoint",
			"display_stream_id", checkpoint.DisplayStreamID,
			"checkpoint_version", checkpoint.CheckpointVersion,
			"error", err,
		)
		active.checkpointHealthy = false
		active.transientHealthy = false
		return false
	}
	if active.transientHealthy && runner.transient != nil {
		if err := runner.transient.Supersede(checkpoint); err != nil {
			slog.Error("supersede transient Display checkpoint",
				"display_stream_id", checkpoint.DisplayStreamID, "error", err)
			active.transientHealthy = false
		}
	}
	return true
}

// handleInitialRuntimeStartFailure preserves an interrupted physical owner;
// no database NULL-CID fact can turn an uncertain Engine effect into absence.
func (runner *runtimeRunner) handleInitialRuntimeStartFailure(ctx context.Context, started workerapp.StartedRuntime, cause error) error {
	if initialRuntimePending(cause) || started.Runtime.ContainerID != "" || started.Runtime.PhysicalEffectUnknown {
		return cause
	}
	if started.Ownership.RuntimeInstanceID != "" {
		return runner.reconcileFailedRuntimeStart(ctx, started.Ownership, cause)
	}
	return fmt.Errorf("start queued Task Runtime: %w", cause)
}

func (runner *runtimeRunner) reconcileFailedRuntimeStart(
	ctx context.Context,
	ownership workerapp.StartedWorkerRun,
	cause error,
) error {
	if initialRuntimePending(cause) {
		return cause
	}
	var containerID, runtimeStatus string
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	err := runner.pool.QueryRow(ctx, `SELECT COALESCE(runtime.container_id,''),runtime.status,
       session.session_revision,run.revision,turn.revision,runtime.revision
FROM runtime_instances runtime
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
JOIN worker_runs run ON run.worker_run_id=runtime.worker_run_id
JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id
WHERE runtime.runtime_instance_id=$1 AND session.worker_session_id=$2
  AND run.worker_run_id=$3 AND turn.agent_turn_id=$4`, ownership.RuntimeInstanceID,
		ownership.WorkerSessionID, ownership.WorkerRunID, ownership.AgentTurnID).Scan(
		&containerID, &runtimeStatus, &sessionRevision, &runRevision, &turnRevision, &runtimeRevision)
	if err != nil {
		return errors.Join(fmt.Errorf("Runtime startup failed after ownership commit: %w", cause), err)
	}
	_, reconcileErr := runner.reconciler.Execute(ctx, workerapp.ReconcileUnownedRuntimeCommand{
		TenantID: runner.configuration.Profile.TenantID, WorkerSessionID: ownership.WorkerSessionID,
		WorkerRunID: ownership.WorkerRunID, AgentTurnID: ownership.AgentTurnID,
		RuntimeInstanceID: ownership.RuntimeInstanceID, ExpectedContainerID: containerID,
		RuntimeGeneration: ownership.RuntimeGeneration, RuntimeStatus: runtimeStatus,
		ExpectedSessionRevision: sessionRevision, ExpectedRunRevision: runRevision,
		ExpectedTurnRevision: turnRevision, ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey: "startup-failed/" + ownership.RuntimeInstanceID,
	})
	discardErr := runner.environment.discard(controlgrpc.RuntimeIdentity{
		WorkerSessionID: ownership.WorkerSessionID, RuntimeInstanceID: ownership.RuntimeInstanceID,
		RuntimeGeneration: uint32(ownership.RuntimeGeneration), ChannelEpoch: ownership.ControlChannelEpoch,
	})
	return errors.Join(fmt.Errorf("Runtime startup failed after ownership commit: %w", cause), reconcileErr, discardErr)
}

func (runner *runtimeRunner) cancelledTaskRevision(ctx context.Context, taskID string) (bool, uint64, error) {
	if taskID == "" {
		return false, 0, nil
	}
	var status string
	var revision uint64
	if err := runner.pool.QueryRow(ctx, `SELECT status,revision FROM tasks WHERE task_id=$1`, taskID).Scan(&status, &revision); err != nil {
		return false, 0, err
	}
	return status == "cancelled", revision, nil
}

func (runner *runtimeRunner) continueQueuedTurn(
	ctx context.Context,
	turn pendingTurn,
	completed workerapp.ResidentRuntimeState,
) (bool, error) {
	var commandID string
	err := runner.pool.QueryRow(ctx, `
SELECT command_id FROM worker_user_commands
WHERE worker_session_id=$1 AND command_type='queue' AND status='accepted'
ORDER BY queue_sequence LIMIT 1`, turn.WorkerSessionID).Scan(&commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := checkRuntimeStart(ctx, runner.startPolicy); err != nil {
		return false, err
	}
	_, err = runner.queued.Execute(ctx, workerapp.ContinueQueuedTurnCommand{
		TenantID: turn.TenantID, UserID: turn.UserID, WorkerSessionID: turn.WorkerSessionID,
		PreviousWorkerRunID: turn.WorkerRunID, PreviousAgentTurnID: turn.AgentTurnID,
		RuntimeInstanceID: completed.RuntimeInstanceID, RuntimeGeneration: completed.RuntimeGeneration,
		QueueCommandID:          commandID,
		ExpectedSessionRevision: completed.SessionRevision,
		ExpectedRunRevision:     completed.RunRevision, ExpectedTurnRevision: completed.TurnRevision,
		ExpectedRuntimeRevision: completed.RuntimeRevision,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (runner *runtimeRunner) readPendingTurn(ctx context.Context) (pendingTurn, error) {
	var turn pendingTurn
	err := runner.pool.QueryRow(ctx, `
SELECT session.tenant_id,session.user_id,session.workspace_id,COALESCE(run.task_id,''),
       COALESCE(task.revision,0),session.worker_session_id,session.session_revision,
	       run.worker_run_id,run.revision,turn.agent_turn_id,turn.revision,turn.agent_session_id,
	       runtime.runtime_instance_id,runtime.revision,runtime.generation,
	       runtime.network_policy_id,runtime.network_policy_revision,
	       runtime.network_policy_digest,runtime.network_enforcement_status,
	       turn.started_at,turn.deadline_at,turn.content
FROM agent_turns turn
JOIN worker_runs run ON run.worker_run_id=turn.worker_run_id
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id
LEFT JOIN tasks task ON task.task_id=run.task_id
WHERE turn.status='delivered' AND run.status='running' AND session.status='running'
  AND runtime.status='active' AND session.tenant_id=$1 AND session.user_id=$2
ORDER BY turn.created_at,turn.agent_turn_id LIMIT 1`, runner.configuration.Profile.TenantID,
		runner.configuration.Profile.ActorUserID).Scan(
		&turn.TenantID, &turn.UserID, &turn.WorkspaceID, &turn.TaskID, &turn.TaskRevision,
		&turn.WorkerSessionID, &turn.SessionRevision, &turn.WorkerRunID, &turn.RunRevision,
		&turn.AgentTurnID, &turn.TurnRevision, &turn.AgentSessionID, &turn.RuntimeInstanceID,
		&turn.RuntimeRevision, &turn.RuntimeGeneration, &turn.NetworkPolicyID,
		&turn.NetworkPolicyRevision, &turn.NetworkPolicyDigest, &turn.NetworkEnforcementStatus,
		&turn.StartedAt, &turn.DeadlineAt, &turn.Content)
	return turn, err
}

func (runner *runtimeRunner) completeFailedTurn(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	display *activeDisplayStream,
	cause error,
) (bool, error) {
	finalized, nextTraceOffset, evidenceErr := runner.finalizeTurnEvidence(ctx, live, turn)
	completed, stateErr := runner.residency.CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
		TenantID: turn.TenantID, WorkerSessionID: turn.WorkerSessionID,
		WorkerRunID: turn.WorkerRunID, AgentTurnID: turn.AgentTurnID,
		RuntimeInstanceID: turn.RuntimeInstanceID, RuntimeGeneration: turn.RuntimeGeneration,
		ExpectedSessionRevision: turn.SessionRevision, ExpectedRunRevision: turn.RunRevision,
		ExpectedTurnRevision: turn.TurnRevision, ExpectedRuntimeRevision: turn.RuntimeRevision,
		SessionTarget: workerdomain.SessionWaitingUser, RunTarget: workerdomain.RunFailed,
		TurnTarget: workerdomain.TurnFailed, TerminalReason: "agent_error",
	})
	if stateErr != nil {
		return false, errors.Join(fmt.Errorf("AgentTurn failed: %w", cause), evidenceErr, stateErr)
	}
	var publishErr error
	if evidenceErr == nil {
		_, publishErr = runner.publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
			Evidence: finalized, ExpectedSessionRevision: completed.SessionRevision,
			ExpectedRunRevision: completed.RunRevision, ExpectedTurnRevision: completed.TurnRevision,
			ExpectedRuntimeRevision: completed.RuntimeRevision,
			IdempotencyKey:          "publish-turn/" + turn.AgentTurnID,
		})
		if publishErr == nil {
			runner.appendDisplayArtifacts(ctx, display, finalized.ArtifactRefs)
			live.traceOffset = nextTraceOffset
		}
	}
	var pauseErr error
	if turn.TaskID != "" {
		_, pauseErr = runner.pauses.Execute(ctx, taskapp.PauseTaskCommand{
			Actor:  taskapp.Actor{TenantID: turn.TenantID, UserID: turn.UserID},
			TaskID: turn.TaskID, WorkerSessionID: turn.WorkerSessionID,
			WorkerRunID: turn.WorkerRunID, ExpectedTaskRevision: turn.TaskRevision,
			Reason: "agent_error", IdempotencyKey: "pause-turn/" + turn.AgentTurnID,
		})
	}
	settled := evidenceErr == nil && publishErr == nil && pauseErr == nil
	return settled, errors.Join(
		fmt.Errorf("AgentTurn failed: %w", cause),
		evidenceErr,
		publishErr,
		pauseErr,
	)
}

func (runner *runtimeRunner) finalizeTurnEvidence(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
) (artifactapp.FinalizedTurnEvidence, int64, error) {
	rawTrace, nextOffset, err := readFileFrom(filepath.Join(live.storage.TraceSpool.BackendPath, "acp.raw.jsonl"), live.traceOffset)
	if err != nil {
		return artifactapp.FinalizedTurnEvidence{}, live.traceOffset, fmt.Errorf("read terminal ACP trace segment: %w", err)
	}
	if len(rawTrace) == 0 {
		return artifactapp.FinalizedTurnEvidence{}, live.traceOffset, fmt.Errorf("terminal ACP trace segment is empty")
	}
	patch, err := buildWorkspacePatch(ctx, runner.configuration.WorkspaceSourceRoot, live.storage.WorkingCopy.BackendPath)
	if err != nil {
		return artifactapp.FinalizedTurnEvidence{}, live.traceOffset, err
	}
	now := runner.now().UTC()
	observationSource, err := runner.captureRuntimeObservationSource(ctx, live, turn, now, now, 0, rawTrace, patch)
	if err != nil {
		return artifactapp.FinalizedTurnEvidence{}, live.traceOffset, err
	}
	finalized, err := runner.finalizer.Execute(ctx, artifactapp.FinalizeTurnEvidenceCommand{
		Identity: artifactdomain.TurnIdentity{
			TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID,
			TaskID:            contractsv1.TaskID(turn.TaskID),
			WorkerSessionID:   contractsv1.WorkerSessionID(turn.WorkerSessionID),
			WorkerRunID:       contractsv1.WorkerRunID(turn.WorkerRunID),
			AgentSessionID:    contractsv1.AgentSessionID(turn.AgentSessionID),
			AgentTurnID:       contractsv1.AgentTurnID(turn.AgentTurnID),
			RuntimeInstanceID: contractsv1.RuntimeInstanceID(turn.RuntimeInstanceID),
			RuntimeGeneration: uint32(turn.RuntimeGeneration),
			RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
				ID: turn.NetworkPolicyID, Revision: turn.NetworkPolicyRevision,
				Digest: turn.NetworkPolicyDigest, EnforcementStatus: turn.NetworkEnforcementStatus,
			},
			TraceStreamID:   "trace-" + turn.AgentTurnID,
			LangfuseTraceID: "langfuse-" + turn.AgentTurnID,
		},
		ServerRecordedAt: now, WorkerRecordedAt: now, RawACPJSONL: rawTrace,
		ObservationSourceJSONL: observationSource,
		Patch:                  patch, NoFilesChanged: len(patch) == 0,
		SourceCheckpointRef: contractsv1.CheckpointRef{
			CheckpointID: "working-copy-start-" + turn.WorkerRunID,
			Digest:       "sha256:" + live.sourceDigest,
		},
	})
	if err != nil {
		return artifactapp.FinalizedTurnEvidence{}, live.traceOffset, err
	}
	return finalized, nextOffset, nil
}

func (runner *runtimeRunner) pauseTaskAfterTurn(ctx context.Context, turn pendingTurn, reason string) error {
	if turn.TaskID == "" {
		return nil
	}
	_, err := runner.pauses.Execute(ctx, taskapp.PauseTaskCommand{
		Actor:  taskapp.Actor{TenantID: turn.TenantID, UserID: turn.UserID},
		TaskID: turn.TaskID, WorkerSessionID: turn.WorkerSessionID,
		WorkerRunID: turn.WorkerRunID, ExpectedTaskRevision: turn.TaskRevision,
		Reason: reason, IdempotencyKey: "pause-turn/" + turn.AgentTurnID,
	})
	return err
}

func (runner *runtimeRunner) rejectUnownedRuntimes(ctx context.Context) error {
	rows, err := runner.pool.Query(ctx, `
SELECT session.worker_session_id,run.worker_run_id,turn.agent_turn_id,
       runtime.runtime_instance_id,COALESCE(runtime.container_id,''),runtime.generation,runtime.status,
       session.session_revision,run.revision,turn.revision,runtime.revision,
       EXISTS (
         SELECT 1 FROM worker_turn_settlements settlement
         WHERE settlement.worker_session_id=session.worker_session_id
           AND settlement.phase <> 'completed'
       )
FROM runtime_instances runtime
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
JOIN worker_runs run ON run.worker_run_id=runtime.worker_run_id
JOIN LATERAL (
  SELECT agent_turn_id,revision FROM agent_turns
  WHERE worker_run_id=run.worker_run_id ORDER BY created_at DESC,agent_turn_id DESC LIMIT 1
) turn ON true
WHERE session.tenant_id=$1 AND session.user_id=$2
  AND runtime.status IN ('starting','active','suspect','unhealthy','draining')`,
		runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var pending error
	for rows.Next() {
		var sessionID, runID, turnID, runtimeID, containerID, status string
		var generation, sessionRevision, runRevision, turnRevision, runtimeRevision uint64
		var pendingSettlement bool
		if err := rows.Scan(
			&sessionID, &runID, &turnID, &runtimeID, &containerID, &generation, &status,
			&sessionRevision, &runRevision, &turnRevision, &runtimeRevision,
			&pendingSettlement,
		); err != nil {
			return err
		}
		runner.mu.Lock()
		owned := runner.runtimes[runtimeID] != nil
		runner.mu.Unlock()
		if owned {
			continue
		}
		if err := runner.interruptedInitialRuntime(ctx, runtimeID); err != nil {
			if !runtimeStagingWait(err) {
				return err
			}
			if pending == nil {
				pending = err
			}
			continue
		}
		if pendingSettlement {
			continue
		}
		if _, err := runner.reconciler.Execute(ctx, workerapp.ReconcileUnownedRuntimeCommand{
			TenantID: runner.configuration.Profile.TenantID, WorkerSessionID: sessionID,
			WorkerRunID: runID, AgentTurnID: turnID, RuntimeInstanceID: runtimeID,
			ExpectedContainerID: containerID, RuntimeGeneration: generation, RuntimeStatus: status,
			ExpectedSessionRevision: sessionRevision, ExpectedRunRevision: runRevision,
			ExpectedTurnRevision: turnRevision, ExpectedRuntimeRevision: runtimeRevision,
			IdempotencyKey: "backend-restart/" + runtimeID + "/" +
				strconv.FormatUint(generation, 10),
		}); err != nil {
			return fmt.Errorf("reconcile unowned Runtime %s: %w", runtimeID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return pending
}

func taskPrompt(spec contractsv1.TaskSpec) string {
	var builder strings.Builder
	builder.WriteString(spec.Goal)
	builder.WriteString("\n\nAcceptance criteria:\n")
	for _, criterion := range spec.AcceptanceCriteria {
		builder.WriteString("- ")
		builder.WriteString(criterion)
		builder.WriteByte('\n')
	}
	if len(spec.Constraints) > 0 {
		builder.WriteString("\nConstraints:\n")
		for _, constraint := range spec.Constraints {
			builder.WriteString("- ")
			builder.WriteString(constraint)
			builder.WriteByte('\n')
		}
	}
	return strings.TrimSpace(builder.String())
}

func turnText(content json.RawMessage) string {
	var value struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value.Text)
}

func waitWorkerEvent(
	ctx context.Context,
	events <-chan workerapp.WorkerControlEvent,
	timeout time.Duration,
	match func(workerapp.WorkerControlEvent) bool,
) (workerapp.WorkerControlEvent, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return workerapp.WorkerControlEvent{}, ctx.Err()
		case <-timer.C:
			return workerapp.WorkerControlEvent{}, fmt.Errorf("wait for Worker Runtime event timed out")
		case event, open := <-events:
			if !open {
				return workerapp.WorkerControlEvent{}, fmt.Errorf("Worker Runtime event stream closed")
			}
			if match(event) {
				return event, nil
			}
			return workerapp.WorkerControlEvent{}, fmt.Errorf(
				"unexpected Worker Runtime event %q while waiting for startup",
				event.Envelope.MessageType,
			)
		}
	}
}

func processWorkerControlEvent(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	status string,
	process func() error,
) error {
	return processWorkerControlEventOutcome(ctx, event, status, "", process)
}

func processWorkerControlEffectEvent(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	status string,
	disposition string,
	process func() error,
) error {
	if disposition != "commit" && disposition != "ignore" {
		return fmt.Errorf("WorkerControl effect disposition is invalid")
	}
	return processWorkerControlEventOutcome(ctx, event, status, disposition, process)
}

func processWorkerControlEventOutcome(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	status string,
	effectDisposition string,
	process func() error,
) error {
	if process == nil {
		return fmt.Errorf("WorkerControl business event processor is required")
	}
	if err := process(); err != nil {
		if event.Delivery != nil {
			event.Delivery.ReleaseForRetry()
		}
		return err
	}
	if event.Delivery == nil {
		return fmt.Errorf("WorkerControl business event has no durable delivery")
	}
	outcome, err := json.Marshal(struct {
		Status            string `json:"status"`
		EffectDisposition string `json:"effect_disposition,omitempty"`
	}{Status: status, EffectDisposition: effectDisposition})
	if err != nil {
		return fmt.Errorf("encode WorkerControl delivery outcome: %w", err)
	}
	if err := event.Delivery.Apply(ctx, outcome); err != nil {
		return fmt.Errorf("apply WorkerControl business event: %w", err)
	}
	return nil
}

func applyTerminalWorkerControlEvent(
	ctx context.Context,
	event *workerapp.WorkerControlEvent,
) error {
	if event == nil {
		return nil
	}
	if event.Envelope.MessageType == "agent_turn.terminal" {
		return processWorkerControlEffectEvent(
			ctx,
			*event,
			"agent_turn_terminal_committed",
			"commit",
			func() error { return nil },
		)
	}
	return processWorkerControlEvent(
		ctx,
		*event,
		"agent_turn_terminal_committed",
		func() error { return nil },
	)
}

func ignoreWorkerControlEvent(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	status string,
) error {
	switch event.Envelope.MessageType {
	case "agent_turn.effect", "agent_turn.terminal":
		return processWorkerControlEffectEvent(
			ctx,
			event,
			status,
			"ignore",
			func() error { return nil },
		)
	default:
		return processWorkerControlEvent(
			ctx,
			event,
			status,
			func() error { return nil },
		)
	}
}

func (runner *runtimeRunner) waitTurnTerminal(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	display *activeDisplayStream,
) (turnTerminalResult, error) {
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return turnTerminalResult{}, ctx.Err()
		case <-poll.C:
			handoff, err :=
				runner.backendDeadlineSourceWaiterHandoff(ctx, turn)
			if err != nil {
				return turnTerminalResult{}, err
			}
			if handoff {
				runner.terminateDisplayStream(
					ctx,
					display,
					"agent_turn_deadline",
				)
				return turnTerminalResult{},
					errBackendDeadlineSourceHandoff
			}
			if settled, err := runner.terminalRuntimeFailureHandoff(ctx, live, turn); err != nil {
				return turnTerminalResult{}, err
			} else if settled {
				runner.terminateDisplayStream(ctx, display, "exhausted")
				return turnTerminalResult{}, errRuntimeRecoverySettled
			}
			if err := runner.detectRuntimeFailures(ctx); err != nil {
				return turnTerminalResult{}, err
			}
			recovery, found, recoveryErr := runner.reconcileRuntimeRecovery(ctx)
			if recoveryErr != nil {
				return turnTerminalResult{}, errors.Join(errRuntimeRecoverySettled, recoveryErr)
			}
			if found && recovery.AgentTurnID == turn.AgentTurnID && recovery.PhaseTerminal() {
				runner.terminateDisplayStream(ctx, display, string(recovery.Phase))
				return turnTerminalResult{}, errRuntimeRecoverySettled
			}
			if err := runner.dispatchPendingInterrupt(ctx, live, turn); err != nil {
				if errors.Is(err, errRuntimeRecoverySettled) {
					runner.terminateDisplayStream(ctx, display, "exhausted")
				}
				return turnTerminalResult{}, err
			}
		case event, open := <-live.events:
			if !open {
				if settled, err := runner.terminalRuntimeFailureHandoff(ctx, live, turn); err != nil {
					return turnTerminalResult{}, err
				} else if settled {
					runner.terminateDisplayStream(ctx, display, "exhausted")
					return turnTerminalResult{}, errRuntimeRecoverySettled
				}
				handoff, err :=
					runner.backendDeadlineSourceWaiterHandoff(ctx, turn)
				if err != nil {
					return turnTerminalResult{}, err
				}
				if handoff {
					runner.terminateDisplayStream(
						ctx,
						display,
						"agent_turn_deadline",
					)
					return turnTerminalResult{},
						errBackendDeadlineSourceHandoff
				}
				return turnTerminalResult{}, fmt.Errorf("Worker Runtime event stream closed")
			}
			eventFailure := func(err error) (turnTerminalResult, error) {
				return turnTerminalResult{sourceEvent: &event}, err
			}
			current, err := runner.currentRuntimeEventAuthority(ctx, event)
			if err != nil {
				return eventFailure(err)
			}
			if !current {
				if err := ignoreWorkerControlEvent(
					ctx,
					event,
					"ignored_stale_runtime",
				); err != nil {
					return eventFailure(err)
				}
				continue
			}
			if event.Envelope.AgentTurnID != contractsv1.AgentTurnID(turn.AgentTurnID) {
				if err := ignoreWorkerControlEvent(
					ctx,
					event,
					"ignored_unrelated_turn",
				); err != nil {
					return eventFailure(err)
				}
				continue
			}
			switch event.Envelope.MessageType {
			case "agent_turn.effect":
				if err := processWorkerControlEffectEvent(
					ctx,
					event,
					"agent_turn_effect_committed",
					"commit",
					func() error { return nil },
				); err != nil {
					return eventFailure(err)
				}
				continue
			case "agent.update", "agent_turn.terminal":
				// Process below. Terminal delivery is deliberately returned to
				// the caller and applied only after Turn evidence, domain state,
				// and Task settlement have converged.
			default:
				return eventFailure(fmt.Errorf(
					"unsupported Worker Runtime business event %q",
					event.Envelope.MessageType,
				))
			}
			var payload struct {
				UpdateKind string          `json:"update_kind"`
				Update     json.RawMessage `json:"update"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil {
				return eventFailure(fmt.Errorf("Agent update is invalid"))
			}
			runner.applyDisplayUpdate(
				ctx, display, event.Envelope.Sequence, payload.UpdateKind, payload.Update,
			)
			if payload.UpdateKind == "error" {
				var failure struct {
					Code string `json:"code"`
				}
				if json.Unmarshal(payload.Update, &failure) != nil {
					return eventFailure(fmt.Errorf(
						"Agent error update is invalid",
					))
				}
				recoveryEvidenceAt := runner.now().UTC()
				classified, recoveryRequired, classificationErr :=
					classifyAgentErrorTerminal(
						failure.Code,
						false,
					)
				if classificationErr == nil &&
					event.Envelope.MessageType ==
						"agent_turn.terminal" {
					settlementID, idErr := workerapp.TurnSettlementID(
						turn.AgentTurnID,
					)
					if idErr != nil {
						return eventFailure(idErr)
					}
					settlement, loadErr := runner.settlements.Load(
						ctx,
						settlementID,
					)
					if loadErr != nil {
						return eventFailure(loadErr)
					}
					classified, recoveryRequired, classificationErr =
						terminalResultFromRuntimeSettlementEvent(
							&event,
							settlement,
							turn,
						)
					recoveryEvidenceAt = settlement.AcceptedAt
				}
				if classificationErr != nil {
					return eventFailure(classificationErr)
				}
				if recoveryRequired {
					opened, err := runner.applyACPEOFRecoverySignal(
						ctx,
						live,
						turn,
						event,
						recoveryEvidenceAt,
					)
					if err != nil {
						// The concrete delivery owns retry after Apply was
						// attempted; do not release it through eventFailure.
						return turnTerminalResult{}, err
					}
					if !opened {
						return turnTerminalResult{},
							fmt.Errorf("Agent ACP connection closed")
					}
					runner.terminateDisplayStream(ctx, display, "recovering_runtime")
					return turnTerminalResult{},
						errRuntimeRecoverySettled
				}
				classified.sourceEvent = &event
				return classified, nil
			}
			if payload.UpdateKind != "terminal" {
				if event.Envelope.MessageType == "agent_turn.terminal" {
					return eventFailure(fmt.Errorf(
						"Agent terminal event has nonterminal update kind %q",
						payload.UpdateKind,
					))
				}
				if err := processWorkerControlEvent(
					ctx,
					event,
					"agent_update_committed",
					func() error { return nil },
				); err != nil {
					return eventFailure(err)
				}
				continue
			}
			var terminal struct {
				StopReason string `json:"stop_reason"`
			}
			if json.Unmarshal(payload.Update, &terminal) != nil || terminal.StopReason == "" {
				return eventFailure(fmt.Errorf("Agent terminal update is invalid"))
			}
			if err := runner.finishInterruptCommands(ctx, turn, terminal.StopReason); err != nil {
				return eventFailure(err)
			}
			return turnTerminalResult{
				reason: terminal.StopReason, sourceEvent: &event,
			}, nil
		}
	}
}

func (runner *runtimeRunner) currentRuntimeEventAuthority(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
) (bool, error) {
	var current bool
	err := runner.pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM worker_sessions session
  JOIN runtime_instances runtime
    ON runtime.runtime_instance_id=session.active_runtime_instance_id
  WHERE session.worker_session_id=$1
    AND session.status IN ('interactive','running','waiting_user','idle')
    AND runtime.runtime_instance_id=$2
    AND runtime.generation=$3
    AND runtime.control_channel_epoch=$4
    AND runtime.status IN ('starting','active','suspect')
)`,
		event.Envelope.WorkerSessionID,
		event.Envelope.RuntimeInstanceID,
		event.Envelope.RuntimeGeneration,
		event.Envelope.ChannelEpoch,
	).Scan(&current)
	return current, err
}

func (runner *runtimeRunner) backendDeadlineSourceWaiterHandoff(
	ctx context.Context,
	turn pendingTurn,
) (bool, error) {
	settlementID, err := workerapp.TurnSettlementID(turn.AgentTurnID)
	if err != nil {
		return false, err
	}
	current, err := runner.settlements.Load(ctx, settlementID)
	if errors.Is(err, workerapp.ErrTurnSettlementNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline {
		return false, nil
	}
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		current.Source,
	)
	if err != nil {
		return false, err
	}
	return backendDeadlineSourcePhaseHandsOffWaiter(state.Phase), nil
}

func backendDeadlineSourcePhaseHandsOffWaiter(
	phase workerapp.BackendDeadlineSourcePhase,
) bool {
	switch phase {
	case workerapp.BackendDeadlineSourceForceStopIntent,
		workerapp.BackendDeadlineSourceWriteFenced,
		workerapp.BackendDeadlineSourceCleanupConfirmed:
		return true
	default:
		return false
	}
}

func (runner *runtimeRunner) dispatchPendingInterrupt(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
) error {
	if settled, err := runner.terminalRuntimeFailureHandoff(ctx, live, turn); err != nil {
		return err
	} else if settled {
		return errRuntimeRecoverySettled
	}
	var commandID string
	err := runner.pool.QueryRow(ctx, `
SELECT command_id FROM worker_user_commands
WHERE worker_session_id=$1 AND worker_run_id=$2 AND agent_turn_id=$3
  AND command_type='interrupt' AND status='accepted'
ORDER BY created_at,command_id LIMIT 1`, turn.WorkerSessionID, turn.WorkerRunID, turn.AgentTurnID).Scan(&commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := runner.hub.Send(ctx, workerapp.WorkerControlCommand{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: commandID, Kind: "command",
		WorkerSessionID:   contractsv1.WorkerSessionID(turn.WorkerSessionID),
		WorkerRunID:       contractsv1.WorkerRunID(turn.WorkerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(turn.RuntimeInstanceID),
		RuntimeGeneration: uint32(turn.RuntimeGeneration),
		AgentSessionID:    contractsv1.AgentSessionID(turn.AgentSessionID),
		AgentTurnID:       contractsv1.AgentTurnID(turn.AgentTurnID), ChannelEpoch: live.identity.ChannelEpoch,
		IdempotencyKey: commandID, MessageType: "agent_turn.interrupt", Payload: json.RawMessage(`{}`),
	}}); err != nil {
		return err
	}
	_, err = runner.pool.Exec(ctx, `UPDATE worker_user_commands
SET status='dispatched',outcome=outcome || '{"runtime_dispatch":"accepted"}'::jsonb,updated_at=$2
WHERE command_id=$1 AND status='accepted'`, commandID, runner.now().UTC())
	return err
}

func (runner *runtimeRunner) terminalRuntimeFailureHandoff(ctx context.Context, live *liveRuntime, turn pendingTurn) (bool, error) {
	if runner.recoveryStore == nil {
		return false, nil
	}
	_, found, err := runner.recoveryStore.FindTerminalRuntimeFailureForTurn(ctx, turn.TenantID, turn.WorkerSessionID, turn.WorkerRunID, turn.AgentTurnID, turn.RuntimeInstanceID, turn.RuntimeGeneration, live.identity.ChannelEpoch)
	return found, err
}

func (runner *runtimeRunner) finishInterruptCommands(ctx context.Context, turn pendingTurn, reason string) error {
	_, err := runner.pool.Exec(ctx, `UPDATE worker_user_commands
SET status=CASE WHEN status='dispatched' THEN 'applied' ELSE 'rejected' END,
    outcome=outcome || jsonb_build_object('terminal_reason',$4::text),updated_at=$5
WHERE worker_session_id=$1 AND worker_run_id=$2 AND agent_turn_id=$3
  AND command_type='interrupt' AND status IN ('accepted','dispatched')`, turn.WorkerSessionID,
		turn.WorkerRunID, turn.AgentTurnID, reason, runner.now().UTC())
	return err
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func readFileFrom(path string, offset int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	content, err := io.ReadAll(file)
	return content, offset + int64(len(content)), err
}

func buildWorkspacePatch(ctx context.Context, sourceRoot, workingRoot string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", "diff", "--no-index", "--binary",
		"--src-prefix=a/", "--dst-prefix=b/", "--", sourceRoot, workingRoot)
	var output bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return nil, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return output.Bytes(), nil
	}
	return nil, fmt.Errorf("compare WorkingCopy with source checkpoint")
}
