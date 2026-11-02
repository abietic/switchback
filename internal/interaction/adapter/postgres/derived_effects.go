package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

func (store Store) RecordAssistantDerivedEffects(
	ctx context.Context,
	record interactionapp.DerivedEffectRecord,
) error {
	if store.Pool == nil || record.AssistantRunID == "" || record.RecordedAt.IsZero() {
		return fmt.Errorf("Assistant derived-effect record is invalid")
	}
	var evidenceObjectKey, evidenceErrorCode, observabilityErrorCode, traceID any
	if record.EvidenceObjectKey != "" {
		evidenceObjectKey = record.EvidenceObjectKey
	}
	if record.EvidenceErrorCode != "" {
		evidenceErrorCode = record.EvidenceErrorCode
	}
	if record.ObservabilityErrorCode != "" {
		observabilityErrorCode = record.ObservabilityErrorCode
	}
	if record.LangfuseTraceID != "" {
		traceID = record.LangfuseTraceID
	}
	tag, err := store.Pool.Exec(ctx, `
UPDATE assistant_runs
SET evidence_status=$1,evidence_error_code=$2,evidence_object_key=$3,
    observability_status=$4,observability_error_code=$5,langfuse_trace_id=$6
WHERE assistant_run_id=$7 AND status IN ('completed','failed','cancelled')`,
		record.EvidenceStatus, evidenceErrorCode, evidenceObjectKey,
		record.ObservabilityStatus, observabilityErrorCode, traceID, record.AssistantRunID,
	)
	if err != nil {
		return fmt.Errorf("record Assistant derived-effect status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ErrConversationUnavailable
	}
	return nil
}

func (store Store) LoadAssistantDerivedEffectStatus(
	ctx context.Context,
	runID string,
) (interactionapp.DerivedEffectRecord, error) {
	var record interactionapp.DerivedEffectRecord
	err := store.Pool.QueryRow(ctx, `
SELECT assistant_run_id,evidence_status,COALESCE(evidence_error_code,''),
       COALESCE(evidence_object_key,''),observability_status,
       COALESCE(observability_error_code,''),COALESCE(langfuse_trace_id,''),updated_at
FROM assistant_runs WHERE assistant_run_id=$1`, runID).Scan(
		&record.AssistantRunID, &record.EvidenceStatus, &record.EvidenceErrorCode,
		&record.EvidenceObjectKey, &record.ObservabilityStatus,
		&record.ObservabilityErrorCode, &record.LangfuseTraceID, &record.RecordedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.DerivedEffectRecord{}, interactionapp.ErrConversationUnavailable
	}
	if err != nil {
		return interactionapp.DerivedEffectRecord{}, fmt.Errorf("load Assistant derived-effect status: %w", err)
	}
	return record, nil
}

var _ interactionapp.DerivedEffectStore = Store{}
