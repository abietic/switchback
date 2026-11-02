package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The journal owns the transaction and supplies locked facts. Participants
// cannot choose another transaction or perform external work.
type ObservationCommitParticipant interface {
	CommitModelObservation(context.Context, platformpostgres.Tx, application.RuntimeControlMessage, application.RuntimeControlMessage, application.ApplyRuntimeControlEventRecord) error
	CommitTurnBasis(context.Context, platformpostgres.Tx, application.TurnSettlement, application.FreezeTurnSettlementCheckpointRecord) error
}

func (journal *RuntimeControlJournal) applyModelObservation(ctx context.Context, tx platformpostgres.Tx, state application.RuntimeControlChannelState, message application.RuntimeControlMessage, record *application.ApplyRuntimeControlEventRecord, relaxed bool) error {
	e, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil {
		return application.ErrRuntimeControlMessageConflict
	}
	if e.MessageType != "model.observation" {
		if record.Command.ObservationCapture != nil {
			return application.ErrRuntimeControlMessageConflict
		}
		return nil
	}
	if relaxed {
		return application.ErrRuntimeControlStaleOwner
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_fragment_activations a WHERE a.version='langfuse.projection.v3') AND NOT EXISTS(SELECT 1 FROM observation_fragment_run_exclusions x WHERE x.version='langfuse.projection.v3' AND x.worker_run_id=$1)`, string(e.WorkerRunID)).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	if err := verifyObservationJournalMessage(message); err != nil {
		return err
	}
	if string(e.WorkerSessionID) != state.WorkerSessionID || string(e.RuntimeInstanceID) != state.RuntimeInstanceID || uint64(e.RuntimeGeneration) != state.RuntimeGeneration || e.ChannelEpoch != state.ChannelEpoch {
		return application.ErrRuntimeControlIdentityConflict
	}
	if message.EffectStatus == "applied" {
		if message.ApplyIdempotencyKey != record.Command.IdempotencyKey {
			return application.ErrRuntimeControlMessageConflict
		}
		// The server's original ignored decision is immutable on replay.
		var outcome struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(message.Outcome, &outcome) != nil {
			return application.ErrRuntimeControlMessageConflict
		}
		if outcome.Status == "model_observation_ignored" || outcome.Status == "model_observation_rejected" {
			if message.ApplyOutcomeDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(message.Outcome)) {
				return application.ErrRuntimeControlMessageConflict
			}
			record.Command.Outcome = append(json.RawMessage(nil), message.Outcome...)
			record.OutcomeDigest = message.ApplyOutcomeDigest
			return nil
		}
	}
	if journal.observation == nil || record.Command.ObservationCapture == nil {
		return errors.New("model_observation_receiver_unavailable")
	}
	var outcome struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(record.Command.Outcome, &outcome) != nil || outcome.Status != "model_observation_recorded" || record.EffectDisposition != "" {
		return application.ErrRuntimeControlMessageConflict
	}
	start, err := readRuntimeControlStartCommandForTurn(ctx, tx, state.RuntimeInstanceID, string(e.AgentTurnID))
	if err != nil && !errors.Is(err, application.ErrRuntimeControlMessageConflict) {
		return err
	}
	if err == nil {
		if integrityErr := verifyObservationJournalMessage(start); integrityErr != nil {
			return integrityErr
		}
	}
	if err != nil || application.ValidateModelObservationBinding(message, start) != nil {
		if message.EffectStatus == "applied" {
			return application.ErrRuntimeControlMessageConflict
		}
		record.Command.Outcome = json.RawMessage(`{"status":"model_observation_ignored"}`)
		record.OutcomeDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(record.Command.Outcome))
		return nil
	}
	// Duplicate participants only verify previously committed evidence.
	return journal.observation.CommitModelObservation(ctx, tx, message, start, *record)
}

func verifyObservationJournalMessage(message application.RuntimeControlMessage) error {
	e, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil {
		return application.ErrRuntimeControlMessageConflict
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(e)
	if err != nil || digest != message.Fingerprint || e.Sequence != message.Sequence || e.MessageID != message.MessageID || e.IdempotencyKey != message.IdempotencyKey {
		return application.ErrRuntimeControlMessageConflict
	}
	return nil
}
func NewRuntimeControlJournalWithObservation(pool *pgxpool.Pool, p ObservationCommitParticipant) (*RuntimeControlJournal, error) {
	if p == nil || reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil() {
		return nil, errors.New("observation participant is required")
	}
	return &RuntimeControlJournal{pool: pool, observation: p}, nil
}
func NewTurnSettlementRepositoryWithObservation(pool *pgxpool.Pool, p ObservationCommitParticipant) (*TurnSettlementRepository, error) {
	if p == nil || reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil() {
		return nil, errors.New("observation participant is required")
	}
	return &TurnSettlementRepository{pool: pool, observation: p}, nil
}
