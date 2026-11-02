package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

func (store Store) SaveReadiness(ctx context.Context, record interactionapp.ReadinessRecord) error {
	if store.Pool == nil {
		return fmt.Errorf("Assistant readiness store is unavailable")
	}
	if err := record.Assessment.Validate(); err != nil || record.Digest == "" {
		return fmt.Errorf("Assistant readiness record is invalid")
	}
	dimensions := make(map[string]any, len(record.Assessment.Dimensions))
	for dimension, state := range record.Assessment.Dimensions {
		dimensions[string(dimension)] = map[string]any{
			"status": state.Status, "summary": state.Summary, "evidence_refs": state.EvidenceRefs,
		}
	}
	dimensionsJSON, _ := json.Marshal(dimensions)
	blockersJSON, _ := json.Marshal(append([]string{}, record.Assessment.BlockingUnknowns...))
	assumptionsJSON, _ := json.Marshal(append([]string{}, record.Assessment.Assumptions...))
	var workspaceID, workspaceRevisionID any
	if record.Workspace != nil {
		workspaceID = record.Workspace.WorkspaceID
		workspaceRevisionID = record.Workspace.WorkspaceRevisionID
	}
	_, err := store.Pool.Exec(ctx, `
INSERT INTO assistant_readiness_assessments
  (assessment_id,conversation_id,assistant_run_id,assessment_revision,policy_revision,
   workspace_id,workspace_revision_id,dimensions,blocking_unknowns,assumptions,
   clarification_question_count,status,content_digest,evaluated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		record.Assessment.ID, record.Assessment.ConversationID, record.Assessment.AssistantRunID,
		record.Assessment.Revision, record.Assessment.PolicyRevision, workspaceID, workspaceRevisionID,
		dimensionsJSON, blockersJSON, assumptionsJSON, record.Assessment.ClarificationQuestions,
		record.Assessment.Status, record.Digest, record.Assessment.EvaluatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert immutable Assistant readiness assessment: %w", err)
	}
	return nil
}

var _ interactionapp.ReadinessRepository = Store{}
