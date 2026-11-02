package postgres

import (
	"context"
	"encoding/json"
	"reflect"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func (store *MetadataStore) PublishSettlementEvidence(ctx context.Context, settlement workerapp.TurnSettlement, record artifactapp.PublishTurnEvidenceRecord) (artifactapp.PublishedTurnEvidence, error) {
	var result artifactapp.PublishedTurnEvidence
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		proof, err := workerpostgres.LockSettlementEffect(ctx, tx, settlement, workerapp.TurnSettlementDomainCommitted)
		if err != nil {
			return err
		}
		var evidence artifactapp.FinalizedTurnEvidence
		if err := json.Unmarshal(proof.Evidence.Finalized, &evidence); err != nil {
			return err
		}
		command := record.Command
		if !reflect.DeepEqual(evidence, command.Evidence) || command.ExpectedSessionRevision != proof.Domain.SessionRevision || command.ExpectedRunRevision != proof.Domain.RunRevision || command.ExpectedTurnRevision != proof.Domain.TurnRevision || command.ExpectedRuntimeRevision != proof.Domain.RuntimeRevision || command.IdempotencyKey != "publish-turn/"+settlement.AgentTurnID {
			return workerapp.ErrTurnSettlementCheckpointConflict
		}
		if !proof.ForcedStop {
			revision, err := workerpostgres.LockSettlementEffectRuntime(ctx, tx, settlement, proof, proof.Domain)
			if err != nil {
				return err
			}
			record.Command.ExpectedRuntimeRevision = revision
		}
		result, err = store.publishTurnEvidenceTx(ctx, tx, record)
		if err != nil {
			return err
		}
		return workerpostgres.ValidateSettlementEffectLease(ctx, tx, settlement)
	})
	return result, err
}
