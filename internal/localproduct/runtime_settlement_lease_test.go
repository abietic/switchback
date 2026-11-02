package localproduct

import (
	"context"
	"errors"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeTurnSettlementCoordinatorRenewsLeaseWhileEffectIsRunning(
	t *testing.T,
) {
	base := &recordingRuntimeSettlementController{
		current: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			AgentTurnID:  "turn-1",
			Phase:        workerapp.TurnSettlementAccepted,
			PhaseState: mustRuntimeSettlementCheckpoint(
				t,
				completeRuntimeSettlementCheckpointThrough(
					workerapp.TurnSettlementAccepted,
				),
			),
			Revision:     7,
			LeaseOwnerID: "settler-1",
			LeaseFence:   3,
		},
	}
	controller := &heartbeatRuntimeSettlementController{
		recordingRuntimeSettlementController: base,
		secondRenewed:                        make(chan struct{}),
	}
	effects := recordingRuntimeSettlementEffects{
		calls: &base.calls,
	}.steps()
	effects.finalizeEvidence = func(
		ctx context.Context,
		checkpoint runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error) {
		select {
		case <-controller.secondRenewed:
			return runtimeTurnSettlementCheckpoint{},
				errInjectedLongSettlementEffect
		case <-ctx.Done():
			return runtimeTurnSettlementCheckpoint{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
			return runtimeTurnSettlementCheckpoint{},
				errors.New("lease heartbeat was not observed")
		}
	}
	coordinator := runtimeTurnSettlementCoordinator{
		controller: controller,
		effects:    effects,
	}

	if _, err := coordinator.reconcileClaimed(
		context.Background(),
		base.current,
	); !errors.Is(err, errInjectedLongSettlementEffect) {
		t.Fatalf("reconcile error=%v", err)
	}
	if controller.renewCalls < 2 {
		t.Fatalf("renew calls=%d want at least 2", controller.renewCalls)
	}
	if got := countRuntimeSettlementCall(
		base.calls,
		"advance:evidence_prepared",
	); got != 0 {
		t.Fatalf("long effect unexpectedly persisted, calls=%v", base.calls)
	}
}

func TestRuntimeTurnSettlementCoordinatorDoesNotPersistAfterLeaseHeartbeatFails(
	t *testing.T,
) {
	base := &recordingRuntimeSettlementController{
		current: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			AgentTurnID:  "turn-1",
			Phase:        workerapp.TurnSettlementAccepted,
			PhaseState: mustRuntimeSettlementCheckpoint(
				t,
				completeRuntimeSettlementCheckpointThrough(
					workerapp.TurnSettlementAccepted,
				),
			),
			Revision:     7,
			LeaseOwnerID: "settler-1",
			LeaseFence:   3,
		},
	}
	controller := &heartbeatRuntimeSettlementController{
		recordingRuntimeSettlementController: base,
		renewErrAt:                           2,
		renewErr:                             workerapp.ErrTurnSettlementLeaseStale,
	}
	effects := recordingRuntimeSettlementEffects{
		calls: &base.calls,
	}.steps()
	effects.finalizeEvidence = func(
		ctx context.Context,
		_ runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error) {
		<-ctx.Done()
		return runtimeTurnSettlementCheckpoint{}, ctx.Err()
	}
	coordinator := runtimeTurnSettlementCoordinator{
		controller: controller,
		effects:    effects,
	}

	if _, err := coordinator.reconcileClaimed(
		context.Background(),
		base.current,
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
		t.Fatalf("reconcile error=%v", err)
	}
	if got := countRuntimeSettlementCall(
		base.calls,
		"advance:evidence_prepared",
	); got != 0 {
		t.Fatalf("stale owner persisted effect, calls=%v", base.calls)
	}
}

func TestRuntimeTurnSettlementCoordinatorDoesNotPersistWhenPostEffectRenewalFails(
	t *testing.T,
) {
	base := &recordingRuntimeSettlementController{
		current: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			AgentTurnID:  "turn-1",
			Phase:        workerapp.TurnSettlementAccepted,
			PhaseState: mustRuntimeSettlementCheckpoint(
				t,
				completeRuntimeSettlementCheckpointThrough(
					workerapp.TurnSettlementAccepted,
				),
			),
			Revision:     7,
			LeaseOwnerID: "settler-1",
			LeaseFence:   3,
		},
	}
	controller := &heartbeatRuntimeSettlementController{
		recordingRuntimeSettlementController: base,
		interval:                             time.Second,
		renewErrAt:                           2,
		renewErr:                             workerapp.ErrTurnSettlementLeaseStale,
	}
	effects := recordingRuntimeSettlementEffects{
		calls: &base.calls,
	}.steps()
	coordinator := runtimeTurnSettlementCoordinator{
		controller: controller,
		effects:    effects,
	}

	if _, err := coordinator.reconcileClaimed(
		context.Background(),
		base.current,
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
		t.Fatalf("reconcile error=%v", err)
	}
	if controller.renewCalls != 2 {
		t.Fatalf("renew calls=%d want=2", controller.renewCalls)
	}
	if got := countRuntimeSettlementCall(
		base.calls,
		"advance:evidence_prepared",
	); got != 0 {
		t.Fatalf("unowned effect persisted, calls=%v", base.calls)
	}
}

var errInjectedLongSettlementEffect = errors.New(
	"injected long Turn settlement effect",
)

func (controller *recordingRuntimeSettlementController) renew(
	_ context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	current.LeaseExpiresAt = time.Now().Add(
		workerapp.TurnSettlementLeaseDuration,
	)
	controller.current = current
	return current, nil
}

type heartbeatRuntimeSettlementController struct {
	*recordingRuntimeSettlementController
	renewCalls    int
	secondRenewed chan struct{}
	interval      time.Duration
	renewErrAt    int
	renewErr      error
}

func (controller *heartbeatRuntimeSettlementController) renew(
	_ context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	controller.renewCalls++
	if controller.renewCalls == controller.renewErrAt &&
		controller.renewErr != nil {
		return workerapp.TurnSettlement{}, controller.renewErr
	}
	current.LeaseExpiresAt = time.Now().Add(
		workerapp.TurnSettlementLeaseDuration,
	)
	controller.current = current
	if controller.renewCalls == 2 && controller.secondRenewed != nil {
		close(controller.secondRenewed)
	}
	return current, nil
}

func (controller *heartbeatRuntimeSettlementController) settlementLeaseRenewInterval() time.Duration {
	if controller.interval > 0 {
		return controller.interval
	}
	return time.Millisecond
}
