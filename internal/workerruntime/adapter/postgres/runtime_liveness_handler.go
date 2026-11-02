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

const CommandRuntimeLivenessEvaluate = "runtime_liveness_evaluate"

type RuntimeLivenessCommandHandler struct {
	Liveness        application.RuntimeLivenessService
	Recovery        application.RuntimeRecoveryService
	DisableRecovery bool
}

func (handler RuntimeLivenessCommandHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.CommandType != CommandRuntimeLivenessEvaluate {
		return fmt.Errorf("unsupported Runtime liveness command %q", command.CommandType)
	}
	if command.TargetType != "RuntimeHeartbeat" {
		return fmt.Errorf("Runtime liveness target must be RuntimeHeartbeat")
	}
	var payload struct {
		RuntimeGeneration uint64                           `json:"runtime_generation"`
		ChannelEpoch      uint64                           `json:"channel_epoch"`
		HeartbeatSequence uint64                           `json:"heartbeat_sequence"`
		Stage             application.RuntimeLivenessStage `json:"stage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode Runtime liveness command: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("Runtime liveness command has trailing content")
	}
	evaluation, err := handler.Liveness.Evaluate(ctx, application.EvaluateRuntimeLivenessCommand{
		TenantID: command.TenantID, RuntimeInstanceID: command.TargetID,
		RuntimeGeneration: payload.RuntimeGeneration, ChannelEpoch: payload.ChannelEpoch,
		HeartbeatSequence: payload.HeartbeatSequence, Stage: payload.Stage,
		ExpectedObservationRevision: command.ExpectedRevision,
	})
	if err != nil {
		return err
	}
	if evaluation.OpenRecovery == nil {
		return nil
	}
	if handler.DisableRecovery {
		return nil
	}
	_, err = handler.Recovery.Open(ctx, *evaluation.OpenRecovery)
	if errors.Is(err, platformpostgres.ErrRevisionConflict) ||
		errors.Is(
			err,
			application.ErrTurnOutcomeAuthorityClaimed,
		) {
		// A newer heartbeat, durable Turn outcome owner, or independently
		// opened incident won the race. Exact authority/revisions make this a
		// safe no-op.
		return nil
	}
	return err
}

var _ platformpostgres.ScheduledCommandHandler = RuntimeLivenessCommandHandler{}
