package localproduct

import (
	"context"
	"fmt"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const runtimeSettlementLeaseRenewInterval = workerapp.TurnSettlementLeaseDuration / 3

type runtimeSettlementLeaseController interface {
	renew(
		context.Context,
		workerapp.TurnSettlement,
	) (workerapp.TurnSettlement, error)
}

type runtimeSettlementLeaseIntervaler interface {
	settlementLeaseRenewInterval() time.Duration
}

type runtimeSettlementLeaseResult struct {
	current workerapp.TurnSettlement
	err     error
}

// runWithLease protects one external settlement effect. It verifies authority
// before starting, keeps the lease alive while the effect is blocked, and
// verifies authority again before the caller persists the effect's proof.
func (coordinator runtimeTurnSettlementCoordinator) runWithLease(
	ctx context.Context,
	current workerapp.TurnSettlement,
	effect func(context.Context, workerapp.TurnSettlement) error,
) (workerapp.TurnSettlement, error) {
	controller, ok := coordinator.controller.(runtimeSettlementLeaseController)
	if !ok {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Runtime Turn settlement lease renewal is required",
		)
	}
	current, err := controller.renew(ctx, current)
	if err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"renew Runtime Turn settlement lease before effect: %w",
			err,
		)
	}
	interval := runtimeSettlementLeaseRenewInterval
	if configured, ok := coordinator.controller.(runtimeSettlementLeaseIntervaler); ok {
		interval = configured.settlementLeaseRenewInterval()
	}
	if interval <= 0 || interval >= workerapp.TurnSettlementLeaseDuration {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Runtime Turn settlement lease renewal interval is invalid",
		)
	}

	effectCtx, cancelEffect := context.WithCancel(ctx)
	defer cancelEffect()
	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	stop := make(chan struct{})
	done := make(chan runtimeSettlementLeaseResult, 1)
	go maintainRuntimeSettlementLease(
		leaseCtx,
		controller,
		current,
		interval,
		stop,
		cancelEffect,
		done,
	)

	effectErr := effect(effectCtx, current)
	close(stop)
	leaseResult := <-done
	if leaseResult.err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"maintain Runtime Turn settlement lease during effect: %w",
			leaseResult.err,
		)
	}
	if effectErr != nil {
		return workerapp.TurnSettlement{}, effectErr
	}
	current, err = controller.renew(ctx, leaseResult.current)
	if err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"renew Runtime Turn settlement lease after effect: %w",
			err,
		)
	}
	return current, nil
}

func maintainRuntimeSettlementLease(
	ctx context.Context,
	controller runtimeSettlementLeaseController,
	current workerapp.TurnSettlement,
	interval time.Duration,
	stop <-chan struct{},
	cancelEffect context.CancelFunc,
	done chan<- runtimeSettlementLeaseResult,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			done <- runtimeSettlementLeaseResult{current: current}
			return
		case <-ctx.Done():
			cancelEffect()
			done <- runtimeSettlementLeaseResult{
				current: current,
				err:     ctx.Err(),
			}
			return
		case <-ticker.C:
			renewed, err := controller.renew(ctx, current)
			if err != nil {
				cancelEffect()
				done <- runtimeSettlementLeaseResult{
					current: current,
					err:     err,
				}
				return
			}
			current = renewed
		}
	}
}

func (persistence runtimeSettlementPersistence) renew(
	ctx context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	store, ok := persistence.service.Store.(workerapp.TurnSettlementLeaseStore)
	if !ok {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Turn settlement store does not support lease renewal",
		)
	}
	return (workerapp.TurnSettlementLeaseService{
		Store: store,
		Now:   persistence.service.Now,
	}).Renew(
		ctx,
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
		},
	)
}
