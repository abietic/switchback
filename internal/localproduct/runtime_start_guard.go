package localproduct

import (
	"context"
	"errors"
	"fmt"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

var errRuntimeStartCapacityUnavailable = errors.New("Runtime start capacity is unavailable")

// Infrastructure diagnostics must not hold HTTP or reconciliation forever.
// This is unrelated to Task/AgentTurn execution time or user budgets.
const runtimeStartDiagnosticTimeout = time.Second

type runtimeStartPolicy interface {
	checkRuntimeStart(context.Context) error
}

type writableLayerChecker interface {
	CheckWritableLayer(context.Context) error
}

// Storage compatibility is diagnostic only. Neither a compatible Engine nor
// Docker accepting size=10G proves storage availability or a durable reservation.
// Production has no successful branch until both authorities are implemented.
type unavailableRuntimeStartPolicy struct {
	checker writableLayerChecker
}

func (policy unavailableRuntimeStartPolicy) checkRuntimeStart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if policy.checker == nil {
		return fmt.Errorf("%w: writable-layer checker is unavailable", errRuntimeStartCapacityUnavailable)
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, runtimeStartDiagnosticTimeout)
	defer cancel()
	if err := policy.checker.CheckWritableLayer(diagnosticCtx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: writable-layer diagnostic: %v", errRuntimeStartCapacityUnavailable, err)
	}
	return fmt.Errorf("%w: authoritative storage pool and aggregate reservation are unavailable", errRuntimeStartCapacityUnavailable)
}

func checkRuntimeStart(ctx context.Context, policy runtimeStartPolicy) error {
	if policy == nil {
		policy = unavailableRuntimeStartPolicy{}
	}
	return policy.checkRuntimeStart(ctx)
}

// runtimeStagingWait recognizes only a single, known diagnostic cause. Reject
// multi-cause wrappers even if one leaf is a wait: another leaf may be a hard
// failure or cancellation. Classification never substitutes the original error.
func runtimeStagingWait(err error) bool {
	cause := singleRuntimeCause(err)
	if cause == errRuntimeStartCapacityUnavailable {
		return true
	}
	reason, ok := cause.(workerapp.CapacityWaitReason)
	if !ok {
		return false
	}
	switch reason {
	case workerapp.CapacityUninitialized, workerapp.CapacityEvidenceInvalid,
		workerapp.CapacityEvidenceStale, workerapp.CapacityIdentityDrift,
		workerapp.CapacityInventoryUnknown, workerapp.CapacityExhausted,
		workerapp.CapacityPathNotIntegrated, workerapp.CapacityContainerProofPending,
		workerapp.InitialRuntimeStartReplayPending:
		return true
	default:
		return false
	}
}
func singleRuntimeCause(err error) error {
	for depth := 0; err != nil && depth < 64; depth++ {
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return nil
		}
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
	return nil
}

// Only these two initial ownership diagnoses prohibit startup cleanup. Other
// staging waits do not grant a generic exemption from failure reconciliation.
func initialRuntimePending(err error) bool {
	return runtimeStagingWait(err) && (errors.Is(err, workerapp.InitialRuntimeStartReplayPending) || errors.Is(err, workerapp.CapacityContainerProofPending))
}
