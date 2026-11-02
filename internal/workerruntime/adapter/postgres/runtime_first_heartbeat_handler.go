package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type RuntimeFirstHeartbeatCommandHandler struct {
	Watchdog        application.RuntimeFirstHeartbeatService
	Recovery        application.RuntimeRecoveryService
	DisableRecovery bool
}

func (handler RuntimeFirstHeartbeatCommandHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.CommandType !=
		CommandRuntimeFirstHeartbeatEvaluate {
		return fmt.Errorf(
			"unsupported Runtime first-heartbeat command %q",
			command.CommandType,
		)
	}
	if command.TargetType != runtimeFirstHeartbeatTargetType {
		return fmt.Errorf(
			"Runtime first-heartbeat target must be %s",
			runtimeFirstHeartbeatTargetType,
		)
	}
	var payload runtimeFirstHeartbeatCommandPayload
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf(
			"decode Runtime first-heartbeat command: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf(
			"Runtime first-heartbeat command has trailing content",
		)
	}
	evaluation, err := handler.Watchdog.Evaluate(
		ctx,
		application.EvaluateRuntimeFirstHeartbeatCommand{
			TenantID:          command.TenantID,
			RuntimeInstanceID: command.TargetID,
			RuntimeGeneration: payload.RuntimeGeneration,
			ChannelEpoch:      payload.ChannelEpoch,
			AnchorKind: application.RuntimeFirstHeartbeatAnchorKind(
				payload.AnchorKind,
			),
			AnchorRevision: payload.AnchorRevision,
			Stage: application.RuntimeFirstHeartbeatStage(
				payload.Stage,
			),
			ExpectedWatchdogRevision: command.ExpectedRevision,
		},
	)
	if err != nil {
		return err
	}
	if evaluation.OpenRecovery == nil ||
		handler.DisableRecovery {
		return nil
	}
	_, err = handler.Recovery.Open(
		ctx,
		*evaluation.OpenRecovery,
	)
	if errors.Is(err, platformpostgres.ErrRevisionConflict) ||
		errors.Is(
			err,
			application.ErrTurnOutcomeAuthorityClaimed,
		) {
		// A real heartbeat, durable Turn outcome owner, or already-opened
		// incident won the exact graph revision/authority race.
		return nil
	}
	return err
}

var _ platformpostgres.ScheduledCommandHandler = RuntimeFirstHeartbeatCommandHandler{}
