package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

var ErrRuntimeCleanupUnconfirmed = errors.New("Runtime cleanup remains unconfirmed")

type RuntimeCleanupCommandHandler struct {
	Service application.RuntimeCleanupService
}

func (handler RuntimeCleanupCommandHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.CommandType != "runtime_cleanup_reconcile" ||
		command.TargetType != "RuntimeInstance" {
		return fmt.Errorf("unsupported Runtime cleanup ScheduledCommand")
	}
	var lease application.RuntimeCleanupLease
	if err := json.Unmarshal(command.Payload, &lease); err != nil {
		return fmt.Errorf("decode Runtime cleanup ScheduledCommand: %w", err)
	}
	if lease.RuntimeInstanceID != command.TargetID ||
		lease.ExpectedRuntimeRevision != command.ExpectedRevision ||
		lease.ScheduledCommandID != command.ID {
		return fmt.Errorf("Runtime cleanup ScheduledCommand identity conflict")
	}
	result, err := handler.Service.Reconcile(ctx, lease)
	if err != nil {
		return err
	}
	if !result.Confirmed {
		return ErrRuntimeCleanupUnconfirmed
	}
	return nil
}

var _ platformpostgres.ScheduledCommandHandler = RuntimeCleanupCommandHandler{}
