package localproduct

import (
	"context"
	"fmt"
	"strconv"
	"time"

	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

// applyRuntimeRecoverySignalBeforeOpen closes the durable terminal inbox claim
// before RuntimeRecovery may acquire authority for the same AgentTurn. The
// ordering is part of the interface: Open is never called after a failed
// Apply, so restart cannot observe two live owners.
func applyRuntimeRecoverySignalBeforeOpen(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	apply func(context.Context, workerapp.WorkerControlEvent) error,
	open func(context.Context) (bool, error),
) (bool, error) {
	if apply == nil || open == nil {
		return false, fmt.Errorf(
			"Runtime recovery signal settlement dependencies are required",
		)
	}
	if err := apply(ctx, event); err != nil {
		return false, err
	}
	return open(ctx)
}

func (runner *runtimeRunner) applyACPEOFRecoverySignal(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	event workerapp.WorkerControlEvent,
	evidenceAt time.Time,
) (bool, error) {
	if runner == nil || runner.failures == nil || live == nil {
		return false, fmt.Errorf("Runtime recovery signal owner is unavailable")
	}
	return applyRuntimeRecoverySignalBeforeOpen(
		ctx,
		event,
		func(
			ctx context.Context,
			event workerapp.WorkerControlEvent,
		) error {
			return ignoreWorkerControlEvent(
				ctx,
				event,
				"ignored_runtime_recovery_signal",
			)
		},
		func(ctx context.Context) (bool, error) {
			_, opened, err := runner.failures.OpenDetectedRuntimeFailure(
				ctx,
				workerpostgres.DetectedRuntimeFailure{
					WorkerSessionID:   turn.WorkerSessionID,
					RuntimeInstanceID: turn.RuntimeInstanceID,
					RuntimeGeneration: turn.RuntimeGeneration,
					ChannelEpoch:      uint64(live.identity.ChannelEpoch),
					Trigger:           workerdomain.RecoveryTriggerACPEOF,
					EvidenceRef: "acp-eof/" +
						turn.RuntimeInstanceID +
						"/g" +
						strconv.FormatUint(turn.RuntimeGeneration, 10),
					EvidenceAt: evidenceAt,
				},
			)
			return opened, err
		},
	)
}
