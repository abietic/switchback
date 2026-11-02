package localproduct

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const maxSettlementStartupBarrierTransitions = 1024

type settlementStartupBarrier struct {
	Catalog workerapp.PendingTurnSettlementStartupStore

	TenantID string
	OwnerID  string
	Now      func() time.Time

	ReconcileSource   func(context.Context, string) (bool, error)
	ReconcileTerminal func(context.Context, string) (bool, error)
}

type startupSettlementQuarantine struct {
	byWorkerSession map[string][]startupSettlementQuarantineEntry
}

type startupSettlementQuarantineEntry struct {
	settlement        workerapp.TurnSettlement
	disposition       workerapp.TurnSettlementStartupDisposition
	runtimeID         string
	runtimeGeneration uint64
	channelEpoch      uint64
}

func (runner *runtimeRunner) prepareSettlementStartupBarrier(
	ctx context.Context,
) (startupSettlementQuarantine, error) {
	if runner == nil || runner.settlements.Store == nil {
		return startupSettlementQuarantine{},
			fmt.Errorf("settlement startup catalog is unavailable")
	}
	catalog, ok := runner.settlements.Store.(workerapp.PendingTurnSettlementStartupStore)
	if !ok {
		return startupSettlementQuarantine{},
			fmt.Errorf("Turn settlement store has no startup catalog")
	}
	return (settlementStartupBarrier{
		Catalog:  catalog,
		TenantID: runner.configuration.Profile.TenantID,
		OwnerID:  runner.settlerID,
		Now:      runner.now,
		ReconcileSource: func(
			reconcileCtx context.Context,
			settlementID string,
		) (bool, error) {
			return runner.reconcileSettlementStartupSource(
				reconcileCtx,
				settlementID,
			)
		},
		ReconcileTerminal: func(
			reconcileCtx context.Context,
			settlementID string,
		) (bool, error) {
			return runner.reconcileDurableTerminalSettlementByID(
				reconcileCtx,
				settlementID,
			)
		},
	}).Prepare(ctx)
}

func (runner *runtimeRunner) reconcileSettlementStartupSource(
	ctx context.Context,
	settlementID string,
) (bool, error) {
	current, err := runner.settlements.Load(ctx, settlementID)
	if err != nil {
		return true, err
	}
	if current.Phase == workerapp.TurnSettlementCompleted {
		return false, nil
	}
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		current.Source,
	)
	if err != nil {
		return true, err
	}
	if state.Phase ==
		workerapp.BackendDeadlineSourceCleanupConfirmed {
		return runner.reconcileBackendDeadlineTurnSettlementByID(
			ctx,
			settlementID,
		)
	}
	return runner.reconcileBackendDeadlineSource(ctx, settlementID)
}

func (runner *runtimeRunner) reconcileDurableTerminalSettlementByID(
	ctx context.Context,
	settlementID string,
) (bool, error) {
	current, err := runner.settlements.Load(ctx, settlementID)
	if err != nil {
		return true, err
	}
	if current.Phase == workerapp.TurnSettlementCompleted {
		return false, nil
	}
	now := runner.now().UTC()
	if current.LeaseOwnerID != "" &&
		current.LeaseOwnerID != runner.settlerID &&
		current.LeaseExpiresAt.After(now) {
		return true, workerapp.ErrTurnSettlementLeaseHeld
	}
	live, err := runner.backendDeadlineSettlementLive(ctx, current)
	if err != nil {
		return true, err
	}
	needsDelivery, err :=
		runtimeSettlementRequiresTerminalDelivery(current)
	if err != nil {
		return true, err
	}
	if needsDelivery {
		event, err := (workerapp.DurableTerminalEventLoaderService{
			Store: workerpostgres.NewDurableTerminalEventLoader(
				runner.pool,
				runner.now,
			),
		}).Load(ctx, current)
		if err != nil {
			return true, err
		}
		events := make(chan workerapp.WorkerControlEvent, 1)
		events <- event
		live.events = events
	}
	_, err = runner.reconcilePendingTurnSettlement(
		ctx,
		current,
		live,
	)
	if errors.Is(err, errRuntimeRecoverySettled) {
		return true, nil
	}
	return true, err
}

// Prepare advances only immediately runnable durable work. Anything waiting
// on another lease or on the frozen force boundary remains quarantined, so
// startup can become available without transferring or inventing authority.
func (barrier settlementStartupBarrier) Prepare(
	ctx context.Context,
) (startupSettlementQuarantine, error) {
	if barrier.Catalog == nil || barrier.TenantID == "" ||
		barrier.OwnerID == "" || barrier.Now == nil ||
		barrier.ReconcileSource == nil ||
		barrier.ReconcileTerminal == nil {
		return startupSettlementQuarantine{},
			fmt.Errorf("settlement startup barrier dependencies are required")
	}

	var preReconcileFingerprint string
	for transition := 0; transition < maxSettlementStartupBarrierTransitions; transition++ {
		now := barrier.Now().UTC()
		if now.IsZero() {
			return startupSettlementQuarantine{},
				fmt.Errorf("settlement startup barrier time is required")
		}
		settlements, err := barrier.Catalog.
			ListPendingTurnSettlementsForStartup(
				ctx,
				workerapp.ListPendingTurnSettlementsForStartupRecord{
					TenantID: barrier.TenantID,
				},
			)
		if err != nil {
			return startupSettlementQuarantine{}, err
		}
		quarantine, fingerprint, err :=
			buildStartupSettlementQuarantine(settlements, now)
		if err != nil {
			return startupSettlementQuarantine{}, err
		}
		if len(settlements) == 0 ||
			fingerprint == preReconcileFingerprint {
			return quarantine, nil
		}

		var (
			reconcile func(context.Context, string) (bool, error)
			targetID  string
		)
		for _, settlement := range settlements {
			entries := quarantine.byWorkerSession[settlement.WorkerSessionID]
			var entry *startupSettlementQuarantineEntry
			for index := range entries {
				if entries[index].settlement.SettlementID ==
					settlement.SettlementID {
					entry = &entries[index]
					break
				}
			}
			if entry == nil {
				return startupSettlementQuarantine{},
					workerapp.ErrTurnSettlementConflict
			}
			if entry.disposition.Lease.Active &&
				entry.disposition.Lease.OwnerID != barrier.OwnerID {
				continue
			}
			switch entry.disposition.Action {
			case workerapp.TurnSettlementStartupReconcileSource:
				reconcile = barrier.ReconcileSource
			case workerapp.TurnSettlementStartupReconcileTerminal:
				reconcile = barrier.ReconcileTerminal
			case workerapp.TurnSettlementStartupWaitForceAfter:
				continue
			default:
				return startupSettlementQuarantine{},
					workerapp.ErrTurnSettlementConflict
			}
			targetID = settlement.SettlementID
			break
		}
		if reconcile == nil {
			return quarantine, nil
		}
		found, err := reconcile(ctx, targetID)
		if err != nil {
			if errors.Is(err, workerapp.ErrTurnSettlementLeaseHeld) ||
				errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) ||
				errors.Is(
					err,
					workerapp.ErrTurnSettlementRevisionConflict,
				) {
				return quarantine, nil
			}
			return startupSettlementQuarantine{}, err
		}
		if !found {
			return quarantine, nil
		}
		preReconcileFingerprint = fingerprint
	}
	return startupSettlementQuarantine{},
		fmt.Errorf(
			"settlement startup barrier exceeded %d durable transitions",
			maxSettlementStartupBarrierTransitions,
		)
}

func buildStartupSettlementQuarantine(
	settlements []workerapp.TurnSettlement,
	now time.Time,
) (startupSettlementQuarantine, string, error) {
	quarantine := startupSettlementQuarantine{
		byWorkerSession: make(
			map[string][]startupSettlementQuarantineEntry,
		),
	}
	var fingerprint strings.Builder
	for _, settlement := range settlements {
		if settlement.WorkerSessionID == "" {
			return startupSettlementQuarantine{}, "",
				workerapp.ErrTurnSettlementConflict
		}
		disposition, err :=
			workerapp.ClassifyTurnSettlementForStartup(settlement, now)
		if err != nil {
			return startupSettlementQuarantine{}, "", err
		}
		entry := startupSettlementQuarantineEntry{
			settlement:  settlement,
			disposition: disposition,
		}
		switch settlement.Source.Kind {
		case workerapp.TurnSettlementSourceRuntimeEvent:
			if settlement.RuntimeInstanceID == "" ||
				settlement.RuntimeGeneration == 0 ||
				settlement.ChannelEpoch == 0 {
				return startupSettlementQuarantine{}, "",
					workerapp.ErrTurnSettlementConflict
			}
			entry.runtimeID = settlement.RuntimeInstanceID
			entry.runtimeGeneration = settlement.RuntimeGeneration
			entry.channelEpoch = settlement.ChannelEpoch
		case workerapp.TurnSettlementSourceBackendDeadline:
			state, err := workerapp.DecodeBackendDeadlineSourceState(
				settlement.Source,
			)
			if err != nil {
				return startupSettlementQuarantine{}, "", err
			}
			entry.runtimeID = state.RuntimeInstanceID
			entry.runtimeGeneration = state.RuntimeGeneration
			entry.channelEpoch = state.ChannelEpoch
		default:
			return startupSettlementQuarantine{}, "",
				workerapp.ErrTurnSettlementSourceConflict
		}
		quarantine.byWorkerSession[settlement.WorkerSessionID] = append(
			quarantine.byWorkerSession[settlement.WorkerSessionID],
			entry,
		)
		fingerprint.WriteString(settlement.SettlementID)
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(strconv.FormatUint(settlement.Revision, 10))
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(string(settlement.Phase))
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(settlement.Source.StateDigest)
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(settlement.LeaseOwnerID)
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(strconv.FormatUint(settlement.LeaseFence, 10))
		fingerprint.WriteByte('\x00')
		fingerprint.WriteString(
			settlement.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
		)
		fingerprint.WriteByte('\xff')
	}
	return quarantine, fingerprint.String(), nil
}

// PrepareRuntimeCandidate returns true only for sessions with no unfinished
// settlement, or for the one exact Runtime authority still inside its durable
// interrupt grace. Multiple unfinished outcomes fail closed.
func (quarantine startupSettlementQuarantine) PrepareRuntimeCandidate(
	candidate startupRuntimeCandidate,
) (startupRuntimeCandidate, bool) {
	entries := quarantine.byWorkerSession[candidate.Restore.WorkerSessionID]
	if len(entries) == 0 {
		return candidate, true
	}
	if len(entries) != 1 {
		return startupRuntimeCandidate{}, false
	}
	entry := entries[0]
	if !entry.disposition.AllowExactReconnect ||
		entry.disposition.Action !=
			workerapp.TurnSettlementStartupWaitForceAfter ||
		entry.disposition.NextAttemptAt.IsZero() ||
		entry.settlement.WorkerRunID != candidate.Restore.WorkerRunID ||
		entry.settlement.AgentSessionID != candidate.Restore.AgentSessionID ||
		entry.settlement.AgentTurnID != candidate.TurnID ||
		entry.runtimeID != candidate.Restore.RuntimeInstanceID ||
		entry.runtimeGeneration != candidate.Restore.RuntimeGeneration ||
		entry.channelEpoch != candidate.Restore.ControlChannelEpoch {
		return startupRuntimeCandidate{}, false
	}
	candidate.SettlementReconnectUntil =
		entry.disposition.NextAttemptAt.UTC()
	return candidate, true
}
