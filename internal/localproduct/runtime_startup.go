package localproduct

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

type startupReconnectPolicy struct {
	SuspectAfter   time.Duration
	UnhealthyAfter time.Duration
}

func defaultStartupReconnectPolicy() startupReconnectPolicy {
	return startupReconnectPolicy{
		SuspectAfter:   workerapp.RuntimeSuspectAfter,
		UnhealthyAfter: workerapp.RuntimeUnhealthyAfter,
	}
}

type startupRuntimeCandidate struct {
	Restore         liveRuntimeRestoreRequest
	ContainerID     string
	RuntimeStatus   string
	RuntimeRevision uint64
	LivenessAnchor  time.Time
	// SettlementReconnectUntil is non-zero only when a durable Backend
	// deadline interrupt owns this exact Runtime generation/channel and still
	// permits its bounded reconnect window. Ordinary liveness recovery must
	// not mutate that source-owned outcome branch.
	SettlementReconnectUntil time.Time
	SessionStatus            string
	SessionRevision          uint64
	RunStatus                string
	RunRevision              uint64
	TurnStatus               string
	TurnRevision             uint64
	TurnID                   string
	ActiveTurn               *pendingTurn
}

type startupRestoreDependencies struct {
	prepare    func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error)
	inspect    func(context.Context, string) (workerapp.RuntimeInspection, error)
	waitEvents func(
		context.Context,
		controlgrpc.RuntimeIdentity,
	) (<-chan workerapp.WorkerControlEvent, error)
	markSuspect    func(context.Context, startupRuntimeCandidate) error
	fence          func(context.Context, string, uint64, string) error
	fenceIfNotLive func(context.Context, string, uint64, string, time.Time) (bool, error)
	openFailure    func(context.Context, workerpostgres.DetectedRuntimeFailure) (bool, error)
	now            func() time.Time
}

type startupRuntimeRestoreResult struct {
	Prepared    preparedRuntime
	Events      <-chan workerapp.WorkerControlEvent
	Reconnected bool
	Deferred    bool
}

type restoredStartupTurn struct {
	live *liveRuntime
	turn pendingTurn
}

func (runner *runtimeRunner) restoreStartupRuntimes(
	ctx context.Context,
	quarantine startupSettlementQuarantine,
) ([]restoredStartupTurn, error) {
	candidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		return nil, err
	}
	restorable := candidates[:0]
	for _, candidate := range candidates {
		prepared, allowed :=
			quarantine.PrepareRuntimeCandidate(candidate)
		if allowed {
			restorable = append(restorable, prepared)
		}
	}
	candidates = restorable
	if len(candidates) == 0 {
		return nil, nil
	}
	type outcome struct {
		candidate startupRuntimeCandidate
		result    startupRuntimeRestoreResult
		live      *liveRuntime
		err       error
	}
	restoreCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make(chan outcome, len(candidates))
	var group sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		group.Add(1)
		go func() {
			defer group.Done()
			candidate, result, live, restoreErr :=
				runner.restoreStartupRuntimeCandidate(
					restoreCtx,
					candidate,
				)
			// A valid initial owner without a live handoff remains held, but
			// must not abort Backend startup or cancel unrelated restoration.
			// Only this exact single cause is deferred; corrupt/joined errors
			// remain hard. The direct candidate boundary still refuses effects.
			if singleRuntimeCause(restoreErr) == workerapp.InitialRuntimeStartReplayPending {
				result = startupRuntimeRestoreResult{Deferred: true}
				restoreErr = nil
			}
			if restoreErr != nil {
				cancel()
			}
			outcomes <- outcome{
				candidate: candidate,
				result:    result,
				live:      live,
				err:       restoreErr,
			}
		}()
	}
	group.Wait()
	close(outcomes)

	var joined error
	var restoredTurns []restoredStartupTurn
	for outcome := range outcomes {
		if outcome.err != nil {
			if errors.Is(outcome.err, context.Canceled) && joined != nil {
				continue
			}
			joined = errors.Join(joined, outcome.err)
			continue
		}
		if !outcome.result.Reconnected {
			continue
		}
		if outcome.live == nil {
			joined = errors.Join(
				joined,
				fmt.Errorf("restored startup Runtime has no live owner"),
			)
			continue
		}
		if outcome.candidate.ActiveTurn != nil {
			restoredTurns = append(restoredTurns, restoredStartupTurn{
				live: outcome.live,
				turn: *outcome.candidate.ActiveTurn,
			})
		}
	}
	if joined != nil {
		return nil, joined
	}
	return restoredTurns, nil
}

func (runner *runtimeRunner) restoreStartupRuntimeCandidate(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) (
	startupRuntimeCandidate,
	startupRuntimeRestoreResult,
	*liveRuntime,
	error,
) {
	// A live owner already drains its own immutable work. Startup must never
	// reopen its environment or take a second owner during initial handoff.
	runner.mu.Lock()
	owned := runner.runtimes[candidate.Restore.RuntimeInstanceID] != nil
	runner.mu.Unlock()
	if owned {
		return candidate, startupRuntimeRestoreResult{Deferred: true}, nil, nil
	}
	if err := runner.interruptedInitialRuntime(ctx, candidate.Restore.RuntimeInstanceID); err != nil {
		return candidate, startupRuntimeRestoreResult{}, nil, err
	}
	result, err := restoreStartupRuntime(
		ctx,
		candidate,
		startupRestoreDependencies{
			prepare:        runner.restorePolicyVerifiedLiveRuntime,
			inspect:        runner.provisioner.Inspect,
			waitEvents:     runner.hub.WaitEvents,
			markSuspect:    runner.markStartupRuntimeSuspect,
			fence:          runner.hub.Fence,
			fenceIfNotLive: runner.hub.FenceIfNotLive,
			openFailure: func(
				failureCtx context.Context,
				signal workerpostgres.DetectedRuntimeFailure,
			) (bool, error) {
				return runner.handleStartupRuntimeFailure(
					failureCtx,
					candidate,
					signal,
				)
			},
			now: runner.now,
		},
		defaultStartupReconnectPolicy(),
	)
	if err != nil || !result.Reconnected {
		return candidate, result, nil, err
	}
	return completeStartupRuntimeReconnect(
		ctx,
		candidate,
		result,
		startupReconnectCompletionDependencies{
			build:       buildStartupLiveRuntime,
			register:    runner.registerStartupLiveRuntime,
			settle:      runner.reconcileStartupRuntimeTurnSettlements,
			refresh:     runner.refreshStartupRuntimeCandidate,
			awaitActive: runner.awaitRestoredRuntimeActive,
			readActiveTurn: runner.
				readRestoredActiveTurn,
			unregister: runner.unregisterStartupLiveRuntime,
		},
	)
}

func (runner *runtimeRunner) restoreUnownedRuntimeByID(
	ctx context.Context,
	runtimeInstanceID string,
) (*restoredStartupTurn, error) {
	if runtimeInstanceID == "" {
		return nil, fmt.Errorf("unowned Runtime identity is required")
	}
	runner.mu.Lock()
	alreadyOwned := runner.runtimes[runtimeInstanceID] != nil
	runner.mu.Unlock()
	if alreadyOwned {
		return nil, nil
	}
	candidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.Restore.RuntimeInstanceID != runtimeInstanceID {
			continue
		}
		pendingSettlement, err := runner.runtimeHasPendingTurnSettlement(
			ctx,
			candidate.Restore.WorkerSessionID,
		)
		if err != nil {
			return nil, err
		}
		if pendingSettlement {
			return nil, nil
		}
		candidate, result, live, err :=
			runner.restoreStartupRuntimeCandidate(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if !result.Reconnected {
			return nil, nil
		}
		if live == nil {
			return nil, fmt.Errorf(
				"restored unowned Runtime has no live owner",
			)
		}
		if candidate.ActiveTurn == nil {
			return nil, nil
		}
		return &restoredStartupTurn{
			live: live,
			turn: *candidate.ActiveTurn,
		}, nil
	}
	return nil, nil
}

func (runner *runtimeRunner) restoreOneUnownedRuntime(
	ctx context.Context,
) (bool, error) {
	restore := runner.restoreUnownedRuntimeByID
	if runner.unownedRuntimeRestorer != nil {
		restore = runner.unownedRuntimeRestorer
	}
	return runner.restoreOneUnownedRuntimeWith(
		ctx,
		unownedRuntimeAdoptionDependencies{
			loadCandidates:       runner.loadStartupRuntimeCandidates,
			hasPendingSettlement: runner.runtimeHasPendingTurnSettlement,
			restore:              restore,
			supervise:            runner.superviseRestoredTurn,
		},
	)
}

type unownedRuntimeAdoptionDependencies struct {
	loadCandidates func(
		context.Context,
	) ([]startupRuntimeCandidate, error)
	hasPendingSettlement func(context.Context, string) (bool, error)
	restore              func(
		context.Context,
		string,
	) (*restoredStartupTurn, error)
	supervise func(context.Context, restoredStartupTurn)
}

func (runner *runtimeRunner) restoreOneUnownedRuntimeWith(
	ctx context.Context,
	dependencies unownedRuntimeAdoptionDependencies,
) (bool, error) {
	if dependencies.loadCandidates == nil ||
		dependencies.hasPendingSettlement == nil ||
		dependencies.restore == nil ||
		dependencies.supervise == nil {
		return false, fmt.Errorf(
			"unowned Runtime adoption dependencies are required",
		)
	}
	candidates, err := dependencies.loadCandidates(ctx)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		runtimeInstanceID := candidate.Restore.RuntimeInstanceID
		if runner.runtimeOwnedOrAdopting(runtimeInstanceID) {
			continue
		}
		pendingSettlement, err := dependencies.hasPendingSettlement(
			ctx,
			candidate.Restore.WorkerSessionID,
		)
		if err != nil {
			return true, err
		}
		if pendingSettlement {
			continue
		}
		if !runner.beginRuntimeAdoption(runtimeInstanceID) {
			continue
		}
		go func() {
			restored, restoreErr := dependencies.restore(
				ctx,
				runtimeInstanceID,
			)
			runner.finishRuntimeAdoption(runtimeInstanceID)
			if restoreErr != nil {
				if !errors.Is(restoreErr, context.Canceled) &&
					ctx.Err() == nil {
					slog.Error(
						"adopt unowned Runtime",
						"runtime_instance_id",
						runtimeInstanceID,
						"error",
						restoreErr,
					)
				}
				return
			}
			if restored != nil {
				dependencies.supervise(ctx, *restored)
			}
		}()
		return true, nil
	}
	return false, nil
}

func (runner *runtimeRunner) runtimeOwnedOrAdopting(
	runtimeInstanceID string,
) bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runtimes[runtimeInstanceID] != nil || runner.pendingInitialActivations[runtimeInstanceID] != nil {
		return true
	}
	_, adopting := runner.adoptingRuntimeIDs[runtimeInstanceID]
	return adopting
}

func (runner *runtimeRunner) runtimeAdoptionInFlight(
	runtimeInstanceID string,
) bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	_, adopting := runner.adoptingRuntimeIDs[runtimeInstanceID]
	return adopting
}

func (runner *runtimeRunner) beginRuntimeAdoption(
	runtimeInstanceID string,
) bool {
	if runtimeInstanceID == "" {
		return false
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runtimes[runtimeInstanceID] != nil || runner.pendingInitialActivations[runtimeInstanceID] != nil {
		return false
	}
	if _, adopting := runner.adoptingRuntimeIDs[runtimeInstanceID]; adopting {
		return false
	}
	if runner.adoptingRuntimeIDs == nil {
		runner.adoptingRuntimeIDs = make(map[string]struct{})
	}
	runner.adoptingRuntimeIDs[runtimeInstanceID] = struct{}{}
	return true
}

func (runner *runtimeRunner) finishRuntimeAdoption(
	runtimeInstanceID string,
) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	delete(runner.adoptingRuntimeIDs, runtimeInstanceID)
}

type startupReconnectCompletionDependencies struct {
	build func(
		startupRuntimeCandidate,
		startupRuntimeRestoreResult,
	) (*liveRuntime, error)
	register func(string, *liveRuntime) error
	settle   func(
		context.Context,
		string,
		*liveRuntime,
	) (map[string]struct{}, error)
	refresh func(
		context.Context,
		startupRuntimeCandidate,
	) (startupRuntimeCandidate, error)
	awaitActive func(
		context.Context,
		startupRuntimeCandidate,
	) (bool, error)
	readActiveTurn func(
		context.Context,
		startupRuntimeCandidate,
	) (*pendingTurn, error)
	unregister func(string, *liveRuntime)
}

func completeStartupRuntimeReconnect(
	ctx context.Context,
	candidate startupRuntimeCandidate,
	result startupRuntimeRestoreResult,
	dependencies startupReconnectCompletionDependencies,
) (
	startupRuntimeCandidate,
	startupRuntimeRestoreResult,
	*liveRuntime,
	error,
) {
	if dependencies.build == nil ||
		dependencies.register == nil ||
		dependencies.settle == nil ||
		dependencies.refresh == nil ||
		dependencies.awaitActive == nil ||
		dependencies.readActiveTurn == nil ||
		dependencies.unregister == nil {
		return candidate,
			result,
			nil,
			fmt.Errorf(
				"startup reconnect completion dependencies are incomplete",
			)
	}
	live, err := dependencies.build(candidate, result)
	if err != nil {
		return candidate, result, nil, err
	}
	runtimeInstanceID := candidate.Restore.RuntimeInstanceID
	if err := dependencies.register(runtimeInstanceID, live); err != nil {
		return candidate, result, nil, err
	}
	keepRegistered := false
	defer func() {
		if !keepRegistered {
			dependencies.unregister(runtimeInstanceID, live)
		}
	}()
	settledTurns, err := dependencies.settle(
		ctx,
		runtimeInstanceID,
		live,
	)
	if err != nil {
		if errors.Is(err, errRuntimeRecoverySettled) {
			result.Reconnected = false
			return candidate, result, nil, nil
		}
		return candidate, result, nil, err
	}
	if len(settledTurns) > 0 {
		previousTurnID := candidate.TurnID
		candidate, err = dependencies.refresh(ctx, candidate)
		if err != nil {
			return candidate, result, nil, err
		}
		if _, settled := settledTurns[previousTurnID]; settled &&
			candidate.ActiveTurn != nil &&
			candidate.ActiveTurn.AgentTurnID == previousTurnID {
			return candidate,
				result,
				nil,
				fmt.Errorf(
					"settled startup AgentTurn remained active",
				)
		}
	}
	active, err := dependencies.awaitActive(ctx, candidate)
	if err != nil {
		return candidate, result, nil, err
	}
	if !active {
		result.Reconnected = false
		return candidate, result, nil, nil
	}
	if candidate.ActiveTurn != nil {
		candidate.ActiveTurn, err =
			dependencies.readActiveTurn(ctx, candidate)
		if err != nil {
			return candidate, result, nil, err
		}
	}
	keepRegistered = true
	return candidate, result, live, nil
}

func buildStartupLiveRuntime(
	candidate startupRuntimeCandidate,
	result startupRuntimeRestoreResult,
) (*liveRuntime, error) {
	tracePath := filepath.Join(
		result.Prepared.Storage.TraceSpool.BackendPath,
		"acp.raw.jsonl",
	)
	var (
		offset int64
		err    error
	)
	if candidate.ActiveTurn == nil {
		offset, err = fileSize(tracePath)
	} else {
		offset, err = restoredTurnTraceOffset(
			tracePath,
			*candidate.ActiveTurn,
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"restore startup Runtime %s trace boundary: %w",
			candidate.Restore.RuntimeInstanceID,
			err,
		)
	}
	return &liveRuntime{
		identity:       result.Prepared.Credential.Identity,
		agentSessionID: candidate.Restore.AgentSessionID,
		storage:        result.Prepared.Storage,
		sourceDigest:   result.Prepared.SourceDigest,
		events:         result.Events,
		traceOffset:    offset,
		containerID:    candidate.ContainerID,
	}, nil
}

func (runner *runtimeRunner) registerStartupLiveRuntime(
	runtimeInstanceID string,
	live *liveRuntime,
) error {
	if live == nil {
		return fmt.Errorf("startup live Runtime is required")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runtimes[runtimeInstanceID] != nil || runner.pendingInitialActivations[runtimeInstanceID] != nil {
		return fmt.Errorf("startup Runtime identity is already live")
	}
	runner.runtimes[runtimeInstanceID] = live
	return nil
}

func (runner *runtimeRunner) unregisterStartupLiveRuntime(
	runtimeInstanceID string,
	live *liveRuntime,
) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.runtimes[runtimeInstanceID] == live {
		delete(runner.runtimes, runtimeInstanceID)
	}
}

func (runner *runtimeRunner) refreshStartupRuntimeCandidate(
	ctx context.Context,
	previous startupRuntimeCandidate,
) (startupRuntimeCandidate, error) {
	candidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		return startupRuntimeCandidate{}, err
	}
	for _, candidate := range candidates {
		if candidate.Restore.RuntimeInstanceID ==
			previous.Restore.RuntimeInstanceID {
			return candidate, nil
		}
	}
	return startupRuntimeCandidate{}, fmt.Errorf(
		"settled startup Runtime %s is no longer restorable",
		previous.Restore.RuntimeInstanceID,
	)
}

func (runner *runtimeRunner) loadStartupRuntimeCandidates(
	ctx context.Context,
) ([]startupRuntimeCandidate, error) {
	rows, err := runner.pool.Query(ctx, `
SELECT session.tenant_id,session.user_id,session.workspace_id,session.status,
       session.session_revision,session.worker_session_id,
       runtime_run.worker_run_id,runtime_run.status,runtime_run.revision,
       COALESCE(runtime_run.task_id,''),COALESCE(task.revision,0),
       turn.agent_turn_id,turn.status,turn.revision,turn.agent_session_id,
       COALESCE(turn.started_at,turn.created_at),
       COALESCE(turn.deadline_at,turn.created_at + interval '24 hours'),turn.content,
       runtime.runtime_instance_id,runtime.status,runtime.revision,runtime.generation,
       runtime.control_channel_epoch,runtime.container_id,
       runtime.network_policy_id,runtime.network_policy_revision,
       runtime.network_policy_digest,runtime.network_enforcement_status,
       runtime_run.agent_profile_id,runtime_run.agent_profile_revision,
       runtime_run.agent_profile_digest,checkpoint.digest,checkpoint.worker_run_id,
       COALESCE(
         heartbeat.last_received_at,
         watchdog.first_authenticated_resume_at,
         runtime.started_at,
         runtime.created_at
       )
FROM worker_sessions session
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=session.active_runtime_instance_id
JOIN worker_runs runtime_run ON runtime_run.worker_run_id=runtime.worker_run_id
JOIN LATERAL (
  SELECT candidate.agent_turn_id,candidate.status,candidate.revision,
         candidate.agent_session_id,candidate.started_at,candidate.deadline_at,
         candidate.created_at,candidate.content
  FROM agent_turns candidate
  WHERE candidate.worker_run_id=runtime_run.worker_run_id
    AND candidate.worker_session_id=session.worker_session_id
  ORDER BY candidate.created_at DESC,candidate.agent_turn_id DESC
  LIMIT 1
) turn ON true
LEFT JOIN tasks task ON task.task_id=runtime_run.task_id
JOIN LATERAL (
  WITH RECURSIVE lineage AS (
    SELECT seed.worker_run_id,seed.previous_worker_run_id,seed.start_snapshot,0 AS depth
    FROM worker_runs seed WHERE seed.worker_run_id=runtime_run.worker_run_id
    UNION ALL
    SELECT previous.worker_run_id,previous.previous_worker_run_id,
           previous.start_snapshot,lineage.depth+1
    FROM lineage
    JOIN worker_runs previous ON previous.worker_run_id=lineage.previous_worker_run_id
    WHERE lineage.depth < 64
  )
  SELECT lineage.start_snapshot->>'working_copy_checkpoint_digest' AS digest,
         lineage.worker_run_id
  FROM lineage
  WHERE lineage.start_snapshot->>'working_copy_checkpoint_digest'
    ~ '^sha256:[0-9a-f]{64}$'
  ORDER BY lineage.depth
  LIMIT 1
) checkpoint ON true
LEFT JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
 AND heartbeat.runtime_generation=runtime.generation
 AND heartbeat.channel_epoch=runtime.control_channel_epoch
LEFT JOIN runtime_first_heartbeat_watchdogs watchdog
  ON watchdog.runtime_instance_id=runtime.runtime_instance_id
 AND watchdog.worker_session_id=runtime.worker_session_id
 AND watchdog.runtime_generation=runtime.generation
 AND watchdog.channel_epoch=runtime.control_channel_epoch
WHERE session.tenant_id=$1 AND session.user_id=$2
  AND session.status IN ('interactive','running','waiting_user','idle')
  AND runtime.status IN ('active','suspect')
  AND runtime.container_id IS NOT NULL
ORDER BY session.updated_at,session.worker_session_id`,
		runner.configuration.Profile.TenantID,
		runner.configuration.Profile.ActorUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("query startup Runtime restore candidates: %w", err)
	}
	defer rows.Close()
	var candidates []startupRuntimeCandidate
	for rows.Next() {
		var candidate startupRuntimeCandidate
		var profileRevision string
		var tenantID, userID, workspaceID, taskID string
		var taskRevision uint64
		var turnStartedAt, turnDeadlineAt time.Time
		var turnContent json.RawMessage
		var networkID, networkDigest, networkStatus string
		var networkRevision uint64
		err := rows.Scan(
			&tenantID,
			&userID,
			&workspaceID,
			&candidate.SessionStatus,
			&candidate.SessionRevision,
			&candidate.Restore.WorkerSessionID,
			&candidate.Restore.WorkerRunID,
			&candidate.RunStatus,
			&candidate.RunRevision,
			&taskID,
			&taskRevision,
			&candidate.TurnID,
			&candidate.TurnStatus,
			&candidate.TurnRevision,
			&candidate.Restore.AgentSessionID,
			&turnStartedAt,
			&turnDeadlineAt,
			&turnContent,
			&candidate.Restore.RuntimeInstanceID,
			&candidate.RuntimeStatus,
			&candidate.RuntimeRevision,
			&candidate.Restore.RuntimeGeneration,
			&candidate.Restore.ControlChannelEpoch,
			&candidate.ContainerID,
			&networkID,
			&networkRevision,
			&networkDigest,
			&networkStatus,
			&candidate.Restore.AgentProfileID,
			&profileRevision,
			&candidate.Restore.AgentProfileDigest,
			&candidate.Restore.WorkingCopyCheckpointDigest,
			&candidate.Restore.BootstrapWorkerRunID,
			&candidate.LivenessAnchor,
		)
		if err != nil {
			return nil, err
		}
		profileRevisionValue, parseErr := strconv.ParseUint(profileRevision, 10, 64)
		if parseErr != nil || profileRevisionValue == 0 {
			return nil, fmt.Errorf(
				"startup Runtime %s has an invalid AgentProfile revision",
				candidate.Restore.RuntimeInstanceID,
			)
		}
		candidate.Restore.AgentProfileRevision = profileRevisionValue
		if candidate.TurnStatus == "consumed" &&
			candidate.RunStatus == "running" &&
			candidate.SessionStatus == "running" {
			candidate.ActiveTurn = &pendingTurn{
				TenantID: tenantID, UserID: userID, WorkspaceID: workspaceID,
				TaskID: taskID, TaskRevision: taskRevision,
				WorkerSessionID: candidate.Restore.WorkerSessionID,
				SessionRevision: candidate.SessionRevision,
				WorkerRunID:     candidate.Restore.WorkerRunID,
				RunRevision:     candidate.RunRevision,
				AgentTurnID:     candidate.TurnID, TurnRevision: candidate.TurnRevision,
				AgentSessionID:    candidate.Restore.AgentSessionID,
				RuntimeInstanceID: candidate.Restore.RuntimeInstanceID,
				RuntimeRevision:   candidate.RuntimeRevision,
				RuntimeGeneration: candidate.Restore.RuntimeGeneration,
				NetworkPolicyID:   networkID, NetworkPolicyRevision: networkRevision,
				NetworkPolicyDigest: networkDigest, NetworkEnforcementStatus: networkStatus,
				StartedAt: turnStartedAt, DeadlineAt: turnDeadlineAt,
				Content: turnContent,
			}
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

type restoredRuntimeState struct {
	Status                string
	Revision              uint64
	Generation            uint64
	ChannelEpoch          uint64
	ContainerID           string
	SessionStatus         string
	RuntimeWorkerRunID    string
	HeartbeatReceivedAt   time.Time
	ReconnectStillAllowed bool
}

func (runner *runtimeRunner) awaitRestoredRuntimeActive(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) (bool, error) {
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	unhealthyAt := candidate.LivenessAnchor.UTC().Add(
		workerapp.RuntimeUnhealthyAfter,
	)
	now := time.Now().UTC()
	if runner.now != nil {
		now = runner.now().UTC()
	}
	restoreDeadline := unhealthyAt
	settlementOwned := !candidate.SettlementReconnectUntil.IsZero()
	if settlementOwned {
		restoreDeadline = candidate.SettlementReconnectUntil.UTC()
	}
	remaining := restoreDeadline.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	timeout := time.NewTimer(remaining)
	defer timeout.Stop()
	for {
		state, err := runner.readRestoredRuntimeState(ctx, candidate)
		if err != nil {
			return false, err
		}
		if settlementOwned && !state.ReconnectStillAllowed {
			return false, nil
		}
		switch state.Status {
		case "active":
			if state.HeartbeatReceivedAt.After(
				candidate.LivenessAnchor.UTC(),
			) {
				return true, nil
			}
		case "suspect":
			if state.SessionStatus == "recovering" {
				return false, nil
			}
		case "unhealthy":
			return false, nil
		default:
			if settlementOwned {
				return false, nil
			}
			return false, fmt.Errorf(
				"startup Runtime %s changed to %s before restoration",
				candidate.Restore.RuntimeInstanceID,
				state.Status,
			)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		case <-timeout.C:
			if settlementOwned {
				return false, nil
			}
			now := time.Now().UTC()
			if runner.now != nil {
				now = runner.now().UTC()
			}
			opened, err := runner.handleStartupRuntimeFailure(
				ctx,
				candidate,
				startupFailure(
					candidate,
					workerdomain.RecoveryTriggerHeartbeatUnhealthy,
					now,
				),
			)
			if errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if opened {
				if err := runner.hub.Fence(
					ctx,
					candidate.Restore.RuntimeInstanceID,
					candidate.Restore.RuntimeGeneration,
					"startup_reconnected_without_heartbeat",
				); err != nil {
					return false, fmt.Errorf(
						"fence startup Runtime without a fresh heartbeat: %w",
						err,
					)
				}
				return false, nil
			}
			// A heartbeat or an independently opened incident won the
			// revision-CAS race. Re-read exact authority before deciding.
			state, err := runner.readRestoredRuntimeState(ctx, candidate)
			if err != nil {
				return false, err
			}
			if state.Status == "active" &&
				state.HeartbeatReceivedAt.After(
					candidate.LivenessAnchor.UTC(),
				) {
				return true, nil
			}
			return false, nil
		}
	}
}

func (runner *runtimeRunner) readRestoredRuntimeState(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) (restoredRuntimeState, error) {
	var state restoredRuntimeState
	var heartbeatReceivedAt *time.Time
	err := runner.pool.QueryRow(ctx, `
SELECT runtime.status,runtime.revision,runtime.generation,
       runtime.control_channel_epoch,COALESCE(runtime.container_id,''),
       session.status,runtime.worker_run_id,heartbeat.last_received_at,
       EXISTS (
         SELECT 1
         FROM worker_turn_settlements settlement
         WHERE settlement.worker_session_id=session.worker_session_id
           AND settlement.worker_run_id=runtime.worker_run_id
           AND settlement.phase <> 'completed'
           AND settlement.source_kind='backend_deadline'
           AND settlement.source_state->>'runtime_instance_id'=
               runtime.runtime_instance_id
           AND (settlement.source_state->>'runtime_generation')::bigint=
               runtime.generation
           AND (settlement.source_state->>'channel_epoch')::bigint=
               runtime.control_channel_epoch
           AND settlement.source_state->>'phase'='interrupt_enqueued'
           AND (settlement.source_state->>'force_after')::timestamptz > $3
       )
FROM runtime_instances runtime
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
LEFT JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
 AND heartbeat.runtime_generation=runtime.generation
 AND heartbeat.channel_epoch=runtime.control_channel_epoch
WHERE runtime.runtime_instance_id=$1
  AND session.worker_session_id=$2`,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.WorkerSessionID,
		func() time.Time {
			if runner.now != nil {
				return runner.now().UTC()
			}
			return time.Now().UTC()
		}(),
	).Scan(
		&state.Status,
		&state.Revision,
		&state.Generation,
		&state.ChannelEpoch,
		&state.ContainerID,
		&state.SessionStatus,
		&state.RuntimeWorkerRunID,
		&heartbeatReceivedAt,
		&state.ReconnectStillAllowed,
	)
	if err != nil {
		return restoredRuntimeState{}, fmt.Errorf(
			"read restored startup Runtime state: %w",
			err,
		)
	}
	if state.Generation != candidate.Restore.RuntimeGeneration ||
		state.ChannelEpoch != candidate.Restore.ControlChannelEpoch ||
		state.ContainerID != candidate.ContainerID ||
		state.RuntimeWorkerRunID != candidate.Restore.WorkerRunID {
		if !candidate.SettlementReconnectUntil.IsZero() {
			state.ReconnectStillAllowed = false
			return state, nil
		}
		return restoredRuntimeState{}, fmt.Errorf(
			"restored startup Runtime authority changed",
		)
	}
	switch state.SessionStatus {
	case "interactive", "running", "recovering", "waiting_user", "idle":
	default:
		return restoredRuntimeState{}, fmt.Errorf(
			"restored startup Runtime session changed to %s",
			state.SessionStatus,
		)
	}
	if heartbeatReceivedAt != nil {
		state.HeartbeatReceivedAt = heartbeatReceivedAt.UTC()
	}
	return state, nil
}

func (runner *runtimeRunner) readRestoredActiveTurn(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) (*pendingTurn, error) {
	var turn pendingTurn
	var sessionStatus, runStatus, turnStatus, runtimeStatus, containerID string
	var channelEpoch uint64
	err := runner.pool.QueryRow(ctx, `
SELECT session.tenant_id,session.user_id,session.workspace_id,session.status,
       session.session_revision,runtime_run.status,runtime_run.revision,
       COALESCE(runtime_run.task_id,''),COALESCE(task.revision,0),
       turn.revision,turn.status,turn.agent_session_id,
       COALESCE(turn.started_at,turn.created_at),
       COALESCE(turn.deadline_at,turn.created_at + interval '24 hours'),turn.content,
       runtime.revision,runtime.status,runtime.generation,
       runtime.control_channel_epoch,COALESCE(runtime.container_id,''),
       runtime.network_policy_id,runtime.network_policy_revision,
       runtime.network_policy_digest,runtime.network_enforcement_status
FROM runtime_instances runtime
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
JOIN worker_runs runtime_run
  ON runtime_run.worker_run_id=runtime.worker_run_id
JOIN agent_turns turn
  ON turn.agent_turn_id=$4
 AND turn.worker_run_id=runtime_run.worker_run_id
 AND turn.worker_session_id=session.worker_session_id
LEFT JOIN tasks task ON task.task_id=runtime_run.task_id
WHERE runtime.runtime_instance_id=$1
  AND session.worker_session_id=$2
  AND runtime_run.worker_run_id=$3`,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.WorkerSessionID,
		candidate.Restore.WorkerRunID,
		candidate.TurnID,
	).Scan(
		&turn.TenantID,
		&turn.UserID,
		&turn.WorkspaceID,
		&sessionStatus,
		&turn.SessionRevision,
		&runStatus,
		&turn.RunRevision,
		&turn.TaskID,
		&turn.TaskRevision,
		&turn.TurnRevision,
		&turnStatus,
		&turn.AgentSessionID,
		&turn.StartedAt,
		&turn.DeadlineAt,
		&turn.Content,
		&turn.RuntimeRevision,
		&runtimeStatus,
		&turn.RuntimeGeneration,
		&channelEpoch,
		&containerID,
		&turn.NetworkPolicyID,
		&turn.NetworkPolicyRevision,
		&turn.NetworkPolicyDigest,
		&turn.NetworkEnforcementStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("read restored active AgentTurn: %w", err)
	}
	if sessionStatus != "running" || runStatus != "running" ||
		turnStatus != "consumed" || runtimeStatus != "active" ||
		turn.AgentSessionID != candidate.Restore.AgentSessionID ||
		turn.RuntimeGeneration != candidate.Restore.RuntimeGeneration ||
		channelEpoch != candidate.Restore.ControlChannelEpoch ||
		containerID != candidate.ContainerID {
		return nil, fmt.Errorf(
			"restored active AgentTurn authority changed",
		)
	}
	turn.WorkerSessionID = candidate.Restore.WorkerSessionID
	turn.WorkerRunID = candidate.Restore.WorkerRunID
	turn.AgentTurnID = candidate.TurnID
	turn.RuntimeInstanceID = candidate.Restore.RuntimeInstanceID
	return &turn, nil
}

func (runner *runtimeRunner) handleStartupRuntimeFailure(
	ctx context.Context,
	candidate startupRuntimeCandidate,
	signal workerpostgres.DetectedRuntimeFailure,
) (bool, error) {
	pendingSettlement, err := runner.runtimeHasPendingTurnSettlement(
		ctx,
		candidate.Restore.WorkerSessionID,
	)
	if err != nil {
		return false, err
	}
	if pendingSettlement {
		return false, workerapp.ErrTurnOutcomeAuthorityClaimed
	}
	if runner.configuration.RuntimeRecoveryEnabled {
		_, opened, err := runner.failures.OpenDetectedRuntimeFailure(
			ctx,
			signal,
		)
		return opened, err
	}
	return true, runner.rejectStartupRuntime(ctx, candidate)
}

func (runner *runtimeRunner) markStartupRuntimeSuspect(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) error {
	result, err := runner.pool.Exec(ctx, `
UPDATE runtime_instances
SET status='suspect',revision=revision+1
WHERE runtime_instance_id=$1
  AND worker_session_id=$2
  AND generation=$3
  AND control_channel_epoch=$4
  AND revision=$5
  AND status='active'`,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.WorkerSessionID,
		candidate.Restore.RuntimeGeneration,
		candidate.Restore.ControlChannelEpoch,
		candidate.RuntimeRevision,
	)
	if err != nil {
		return fmt.Errorf("mark startup Runtime suspect: %w", err)
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var status string
	var generation, epoch uint64
	err = runner.pool.QueryRow(ctx, `
SELECT status,generation,control_channel_epoch
FROM runtime_instances
WHERE runtime_instance_id=$1 AND worker_session_id=$2`,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.WorkerSessionID,
	).Scan(&status, &generation, &epoch)
	if err != nil {
		return fmt.Errorf("resolve startup Runtime suspect conflict: %w", err)
	}
	if status == "suspect" &&
		generation == candidate.Restore.RuntimeGeneration &&
		epoch == candidate.Restore.ControlChannelEpoch {
		return nil
	}
	return fmt.Errorf("startup Runtime suspect authority is stale")
}

func (runner *runtimeRunner) rejectStartupRuntime(
	ctx context.Context,
	candidate startupRuntimeCandidate,
) error {
	var sessionID, runID, turnID, runtimeID, containerID, status string
	var generation, sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	err := runner.pool.QueryRow(ctx, `
SELECT session.worker_session_id,runtime_run.worker_run_id,turn.agent_turn_id,
       runtime.runtime_instance_id,COALESCE(runtime.container_id,''),runtime.generation,
       runtime.status,session.session_revision,runtime_run.revision,turn.revision,
       runtime.revision
FROM runtime_instances runtime
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
JOIN worker_runs runtime_run ON runtime_run.worker_run_id=runtime.worker_run_id
JOIN LATERAL (
  SELECT candidate.agent_turn_id,candidate.revision
  FROM agent_turns candidate
  WHERE candidate.worker_run_id=runtime_run.worker_run_id
  ORDER BY candidate.created_at DESC,candidate.agent_turn_id DESC
  LIMIT 1
) turn ON true
WHERE runtime.runtime_instance_id=$1
  AND session.worker_session_id=$2
  AND runtime.generation=$3
  AND runtime.control_channel_epoch=$4
  AND runtime.status IN ('active','suspect')`,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.WorkerSessionID,
		candidate.Restore.RuntimeGeneration,
		candidate.Restore.ControlChannelEpoch,
	).Scan(
		&sessionID,
		&runID,
		&turnID,
		&runtimeID,
		&containerID,
		&generation,
		&status,
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	)
	if err != nil {
		return fmt.Errorf("resolve startup Runtime rejection: %w", err)
	}
	_, err = runner.reconciler.Execute(ctx, workerapp.ReconcileUnownedRuntimeCommand{
		TenantID: runner.configuration.Profile.TenantID, WorkerSessionID: sessionID,
		WorkerRunID: runID, AgentTurnID: turnID, RuntimeInstanceID: runtimeID,
		ExpectedContainerID: containerID, RuntimeGeneration: generation, RuntimeStatus: status,
		ExpectedSessionRevision: sessionRevision, ExpectedRunRevision: runRevision,
		ExpectedTurnRevision: turnRevision, ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey: "backend-restart/" + runtimeID + "/" +
			strconv.FormatUint(generation, 10),
	})
	return err
}

func restoreStartupRuntime(
	ctx context.Context,
	candidate startupRuntimeCandidate,
	dependencies startupRestoreDependencies,
	policy startupReconnectPolicy,
) (startupRuntimeRestoreResult, error) {
	if dependencies.prepare == nil || dependencies.inspect == nil ||
		dependencies.waitEvents == nil || dependencies.fence == nil ||
		dependencies.fenceIfNotLive == nil || dependencies.openFailure == nil ||
		dependencies.now == nil || policy.SuspectAfter <= 0 ||
		policy.UnhealthyAfter <= policy.SuspectAfter {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"startup Runtime restore dependencies are incomplete",
		)
	}
	prepared, err := dependencies.prepare(ctx, candidate.Restore)
	if err != nil {
		if errors.Is(err, runtimesecret.ErrLiveRuntimeCredentialUnavailable) {
			return recoverStartupRuntimeWithoutCredential(
				ctx,
				candidate,
				dependencies,
			)
		}
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"restore Runtime environment %s: %w",
			candidate.Restore.RuntimeInstanceID,
			err,
		)
	}
	inspection, err := dependencies.inspect(ctx, candidate.Restore.RuntimeInstanceID)
	if err != nil {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"inspect startup Runtime %s: %w",
			candidate.Restore.RuntimeInstanceID,
			err,
		)
	}
	observedAt := inspection.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = dependencies.now().UTC()
	}
	if !inspection.Exists || !inspection.Running {
		if !candidate.SettlementReconnectUntil.IsZero() {
			return startupRuntimeRestoreResult{
				Prepared: prepared,
				Deferred: true,
			}, nil
		}
		trigger := workerdomain.RecoveryTriggerContainerMissing
		if inspection.Exists {
			trigger = workerdomain.RecoveryTriggerContainerExit
		}
		opened, err := dependencies.openFailure(ctx, startupFailure(
			candidate,
			trigger,
			observedAt,
		))
		if errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
			return startupRuntimeRestoreResult{
				Prepared: prepared,
				Deferred: true,
			}, nil
		}
		if err != nil {
			return startupRuntimeRestoreResult{}, err
		}
		if opened {
			if err := dependencies.fence(
				ctx,
				candidate.Restore.RuntimeInstanceID,
				candidate.Restore.RuntimeGeneration,
				"startup_"+string(trigger),
			); err != nil {
				return startupRuntimeRestoreResult{}, fmt.Errorf(
					"fence failed startup Runtime: %w",
					err,
				)
			}
		}
		return startupRuntimeRestoreResult{Prepared: prepared}, nil
	}
	if inspection.Generation != candidate.Restore.RuntimeGeneration ||
		inspection.ContainerID == "" ||
		candidate.ContainerID == "" ||
		inspection.ContainerID != candidate.ContainerID {
		if !candidate.SettlementReconnectUntil.IsZero() {
			return startupRuntimeRestoreResult{
				Prepared: prepared,
				Deferred: true,
			}, nil
		}
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"startup Runtime container identity is stale",
		)
	}
	identity := prepared.Credential.Identity
	if identity.WorkerSessionID != candidate.Restore.WorkerSessionID ||
		identity.RuntimeInstanceID != candidate.Restore.RuntimeInstanceID ||
		uint64(identity.RuntimeGeneration) != candidate.Restore.RuntimeGeneration ||
		identity.ChannelEpoch != candidate.Restore.ControlChannelEpoch {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"restored Runtime credential identity is stale",
		)
	}
	if !candidate.SettlementReconnectUntil.IsZero() {
		// The durable deadline source owns this reconnect window. Startup has
		// already revalidated the exact environment, container and credential
		// identity; waiting until force_after here would hold the global
		// restore barrier behind one Session. The deadline worker and
		// post-ready unowned-Runtime adoption continue the persisted branch.
		return startupRuntimeRestoreResult{
			Prepared: prepared,
			Deferred: true,
		}, nil
	}
	if candidate.LivenessAnchor.IsZero() {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"startup Runtime has no liveness anchor",
		)
	}

	unhealthyAt := candidate.LivenessAnchor.UTC().Add(policy.UnhealthyAfter)
	waitCtx, cancel := context.WithDeadline(ctx, unhealthyAt)
	events, waitErr := dependencies.waitEvents(waitCtx, identity)
	cancel()
	if waitErr == nil {
		return startupRuntimeRestoreResult{
			Prepared: prepared, Events: events, Reconnected: true,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return startupRuntimeRestoreResult{}, err
	}
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"wait for startup Runtime reconnect: %w",
			waitErr,
		)
	}

	// Resolve local publication at the boundary before attempting the durable
	// recovery CAS. Suspect is intentionally not written here: only the
	// terminal recovery transition has an AgentTurn outcome-authority guard.
	recheckCtx, recheckCancel := context.WithCancel(ctx)
	recheckCancel()
	events, recheckErr := dependencies.waitEvents(recheckCtx, identity)
	if recheckErr == nil {
		return startupRuntimeRestoreResult{
			Prepared: prepared, Events: events, Reconnected: true,
		}, nil
	}
	if !errors.Is(recheckErr, context.Canceled) &&
		!errors.Is(recheckErr, context.DeadlineExceeded) {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"recheck startup Runtime reconnect: %w",
			recheckErr,
		)
	}

	now := dependencies.now().UTC()
	opened, err := dependencies.openFailure(ctx, startupFailure(
		candidate,
		workerdomain.RecoveryTriggerHeartbeatUnhealthy,
		now,
	))
	if errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
		return startupRuntimeRestoreResult{
			Prepared: prepared,
			Deferred: true,
		}, nil
	}
	if err != nil {
		return startupRuntimeRestoreResult{}, err
	}
	if !opened {
		// A fresh heartbeat or another exact recovery transition won the
		// revision CAS. Give a winning live ResumeComplete a bounded interval
		// to publish; never fence on the losing startup snapshot.
		settleCtx, settleCancel := context.WithTimeout(
			ctx,
			startupResumeSettleDelay(policy),
		)
		events, settleErr := dependencies.waitEvents(settleCtx, identity)
		settleCancel()
		if settleErr == nil {
			return startupRuntimeRestoreResult{
				Prepared: prepared, Events: events, Reconnected: true,
			}, nil
		}
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"startup Runtime authority changed but live publication did not settle: %w",
			settleErr,
		)
	}
	if err := dependencies.fence(
		ctx,
		candidate.Restore.RuntimeInstanceID,
		candidate.Restore.RuntimeGeneration,
		"startup_reconnect_timeout",
	); err != nil {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"fence recovered startup Runtime: %w",
			err,
		)
	}
	return startupRuntimeRestoreResult{Prepared: prepared}, nil
}

func recoverStartupRuntimeWithoutCredential(
	ctx context.Context,
	candidate startupRuntimeCandidate,
	dependencies startupRestoreDependencies,
) (startupRuntimeRestoreResult, error) {
	inspection, err := dependencies.inspect(ctx, candidate.Restore.RuntimeInstanceID)
	if err != nil {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"inspect startup Runtime %s without credential: %w",
			candidate.Restore.RuntimeInstanceID,
			err,
		)
	}
	observedAt := inspection.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = dependencies.now().UTC()
	}
	if inspection.Exists && inspection.Running &&
		(inspection.Generation != candidate.Restore.RuntimeGeneration ||
			inspection.ContainerID == "" ||
			candidate.ContainerID == "" ||
			inspection.ContainerID != candidate.ContainerID) {
		return startupRuntimeRestoreResult{}, fmt.Errorf(
			"startup Runtime container identity is stale",
		)
	}
	if !candidate.SettlementReconnectUntil.IsZero() {
		return startupRuntimeRestoreResult{Deferred: true}, nil
	}
	trigger := workerdomain.RecoveryTriggerHeartbeatUnhealthy
	if !inspection.Exists {
		trigger = workerdomain.RecoveryTriggerContainerMissing
	} else if !inspection.Running {
		trigger = workerdomain.RecoveryTriggerContainerExit
	}
	_, err = dependencies.openFailure(ctx, startupFailure(
		candidate,
		trigger,
		observedAt,
	))
	if errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
		return startupRuntimeRestoreResult{Deferred: true}, nil
	}
	if err != nil {
		return startupRuntimeRestoreResult{}, err
	}
	// No local credential was restored, so this Hub cannot fence a stream that
	// never authenticated after restart. The durable recovery incident owns
	// the exact journal fence and container cleanup before replacement.
	return startupRuntimeRestoreResult{}, nil
}

func startupResumeSettleDelay(policy startupReconnectPolicy) time.Duration {
	delay := policy.SuspectAfter / 10
	if delay < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	if delay > time.Second {
		return time.Second
	}
	return delay
}

const maxStartupRawTraceRecordBytes = 32 << 20

func restoredTurnTraceOffset(path string, turn pendingTurn) (int64, error) {
	if path == "" || turn.AgentSessionID == "" || turn.AgentTurnID == "" ||
		turn.WorkerSessionID == "" || turn.RuntimeInstanceID == "" ||
		turn.RuntimeGeneration == 0 {
		return 0, fmt.Errorf("active AgentTurn trace identity is required")
	}
	trace, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer trace.Close()

	reader := bufio.NewReaderSize(trace, 64<<10)
	var offset int64
	var promptOffset int64 = -1
	var promptMatches int
	for {
		recordOffset := offset
		line, readErr := readBoundedStartupTraceRecord(reader)
		offset += int64(len(line))
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, io.EOF) {
			return 0, fmt.Errorf(
				"raw ACP JSONL has an incomplete record at offset %d",
				recordOffset,
			)
		}
		if readErr != nil {
			return 0, fmt.Errorf("read raw ACP JSONL: %w", readErr)
		}
		var record struct {
			Type      string `json:"type"`
			Direction string `json:"direction"`
			Frame     string `json:"frame"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
			return 0, fmt.Errorf(
				"decode raw ACP JSONL record at offset %d: %w",
				recordOffset,
				err,
			)
		}
		if record.Type != "acp.raw_frame" {
			continue
		}
		if record.Direction != "client_to_agent" &&
			record.Direction != "agent_to_client" {
			return 0, fmt.Errorf(
				"raw ACP JSONL record at offset %d has invalid direction",
				recordOffset,
			)
		}
		var frame struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Meta      struct {
					Switchback struct {
						AgentTurnID       string `json:"agent_turn_id"`
						RuntimeID         string `json:"runtime_id"`
						RuntimeGeneration uint64 `json:"runtime_generation"`
						WorkerSessionID   string `json:"worker_session_id"`
					} `json:"switchback"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(
			[]byte(strings.TrimSpace(record.Frame)),
			&frame,
		); err != nil {
			return 0, fmt.Errorf(
				"decode ACP frame at offset %d: %w",
				recordOffset,
				err,
			)
		}
		if record.Direction != "client_to_agent" ||
			frame.Method != "session/prompt" ||
			frame.Params.SessionID != turn.AgentSessionID ||
			frame.Params.Meta.Switchback.AgentTurnID != turn.AgentTurnID {
			continue
		}
		if frame.Params.Meta.Switchback.RuntimeID != turn.RuntimeInstanceID ||
			frame.Params.Meta.Switchback.RuntimeGeneration != turn.RuntimeGeneration ||
			frame.Params.Meta.Switchback.WorkerSessionID != turn.WorkerSessionID {
			return 0, fmt.Errorf(
				"active AgentTurn prompt trace authority mismatch at offset %d",
				recordOffset,
			)
		}
		promptMatches++
		if promptMatches > 1 {
			return 0, fmt.Errorf(
				"active AgentTurn has multiple matching session/prompt trace boundaries",
			)
		}
		promptOffset = recordOffset
	}
	if promptMatches != 1 {
		return 0, fmt.Errorf(
			"active AgentTurn has no matching session/prompt trace boundary",
		)
	}
	return promptOffset, nil
}

func readBoundedStartupTraceRecord(reader *bufio.Reader) ([]byte, error) {
	var record []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(record)+len(fragment) > maxStartupRawTraceRecordBytes {
			return nil, fmt.Errorf(
				"raw ACP JSONL record exceeds %d bytes",
				maxStartupRawTraceRecordBytes,
			)
		}
		record = append(record, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return record, err
	}
}

func startupFailure(
	candidate startupRuntimeCandidate,
	trigger workerdomain.RuntimeRecoveryTrigger,
	evidenceAt time.Time,
) workerpostgres.DetectedRuntimeFailure {
	return workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID:   candidate.Restore.WorkerSessionID,
		RuntimeInstanceID: candidate.Restore.RuntimeInstanceID,
		RuntimeGeneration: candidate.Restore.RuntimeGeneration,
		ChannelEpoch:      candidate.Restore.ControlChannelEpoch,
		Trigger:           trigger,
		EvidenceRef: "startup-inspection/" + candidate.Restore.RuntimeInstanceID +
			"/g" + strconv.FormatUint(candidate.Restore.RuntimeGeneration, 10) +
			"/" + string(trigger),
		EvidenceAt: evidenceAt,
	}
}
