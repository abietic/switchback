package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
)

const defaultRecentContextTurnLimit = 20

// LoadContextCandidates constructs only the M2 bounded ContextPack inputs.
// It deliberately has no access to Worker traces, artifacts, Truth, or
// Inspiration; later retrieval must enter through an explicitly governed port.
func (store Store) LoadContextCandidates(ctx context.Context, claim interactionapp.RunClaim) ([]interactionapp.ContextItem, error) {
	if store.Pool == nil || claim.Conversation.ID == "" || claim.Run.ID == "" {
		return nil, fmt.Errorf("Assistant context claim is incomplete")
	}
	items := []interactionapp.ContextItem{
		contextItem("assistant-rules/m2-bounded-context-v1", "rules", 100, map[string]any{
			"version": "m2-bounded-context-v1", "scope": "conversation_generation_only",
			"excluded": []string{"truth", "inspiration", "worker_trace", "artifact_body", "skill_content"},
		}),
		contextItem("readiness-policy/"+claim.Run.PolicyRevision, "policy", 100, map[string]any{
			"revision": claim.Run.PolicyRevision, "dimensions": domain.RequiredReadinessDimensions(),
		}),
		contextItem("schema/"+contractsv1.TaskSpecSchemaVersion, "schema", 100, map[string]any{
			"task_spec_schema_version":          contractsv1.TaskSpecSchemaVersion,
			"assistant_decision_schema_version": contractsv1.AssistantDecisionSchemaVersion,
		}),
		contextItem("model-profile/"+claim.Run.ModelProfile, "model_profile", 95, map[string]any{
			"id": claim.Run.ModelProfile, "revision": claim.ModelProfileRevision, "digest": claim.ModelProfileDigest,
		}),
	}
	if workspace := claim.Conversation.Workspace; workspace != nil {
		items = append(items, contextItem("workspace/"+workspace.WorkspaceID, "workspace", 90, map[string]any{
			"workspace_id": workspace.WorkspaceID, "workspace_revision_id": workspace.WorkspaceRevisionID,
			"resolution_id": workspace.ResolutionID, "resolution_revision": workspace.ResolutionRevision,
			"membership_revision": workspace.MembershipRevision,
		}))
	}
	if draft, err := store.LoadCurrentTaskDraft(ctx, claim.Conversation.ID); err != nil {
		return nil, err
	} else if draft != nil {
		items = append(items, interactionapp.ContextItem{Ref: "task-draft/" + draft.Draft.ID, Kind: "task_draft", Priority: 85,
			EstimatedTokens: estimateTokens(draft.Draft.TaskSpecSnapshot), Content: append(json.RawMessage(nil), draft.Draft.TaskSpecSnapshot...)})
	}
	if readiness, err := store.loadCurrentReadinessContext(ctx, claim.Conversation.ID); err != nil {
		return nil, err
	} else if readiness != nil {
		items = append(items, *readiness)
	}
	if checkpoint, err := store.loadCurrentCheckpointContext(ctx, claim.Conversation.ID); err != nil {
		return nil, err
	} else if checkpoint != nil {
		items = append(items, *checkpoint)
	}
	turns, err := store.loadRecentContextTurns(ctx, claim.Conversation.ID)
	if err != nil {
		return nil, err
	}
	items = append(items, turns...)
	return items, nil
}

func (store Store) SaveContextPack(ctx context.Context, pack interactionapp.ContextPack) error {
	if store.Pool == nil || pack.SchemaVersion != interactionapp.ContextPackSchemaVersion || pack.ContextPackID == "" ||
		pack.AssistantRunID == "" || pack.ConversationID == "" || pack.PolicyRevision == "" ||
		pack.Digest == "" || pack.BudgetTokens <= 0 ||
		pack.EstimatedTokens < 0 || pack.EstimatedTokens > pack.BudgetTokens {
		return fmt.Errorf("Assistant ContextPack is invalid")
	}
	selected, err := json.Marshal(pack.Selected)
	if err != nil {
		return fmt.Errorf("encode Assistant ContextPack selection: %w", err)
	}
	dropped, err := json.Marshal(pack.Dropped)
	if err != nil {
		return fmt.Errorf("encode Assistant ContextPack drops: %w", err)
	}
	compiled, err := json.Marshal(pack)
	if err != nil {
		return fmt.Errorf("encode Assistant ContextPack payload: %w", err)
	}
	_, err = store.Pool.Exec(ctx, `
INSERT INTO assistant_context_packs
  (context_pack_id,assistant_run_id,conversation_id,schema_version,selected_items,dropped_items,
   compiled_payload,estimated_tokens,budget_tokens,content_digest,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())`,
		pack.ContextPackID, pack.AssistantRunID, pack.ConversationID, pack.SchemaVersion, selected, dropped,
		compiled, pack.EstimatedTokens, pack.BudgetTokens, pack.Digest)
	if err != nil {
		return fmt.Errorf("persist Assistant ContextPack: %w", err)
	}
	return nil
}

func (store Store) LoadCurrentTaskDraft(ctx context.Context, conversationID string) (*interactionapp.TaskDraftRecord, error) {
	if store.Pool == nil || conversationID == "" {
		return nil, fmt.Errorf("Assistant current TaskDraft target is invalid")
	}
	var draft domain.TaskDraft
	var status string
	var snapshot, assumptions json.RawMessage
	err := store.Pool.QueryRow(ctx, `
SELECT task_draft_id,conversation_id,initiator_user_id,revision,COALESCE(parent_task_draft_id,''),
       readiness_assessment_id,workspace_id,workspace_revision_id,task_spec_snapshot,task_spec_digest,
       assumptions,status,COALESCE(invalidated_at,'epoch'::timestamptz),created_at
FROM assistant_task_drafts
WHERE conversation_id=$1 AND status='current'`, conversationID).Scan(
		&draft.ID, &draft.ConversationID, &draft.InitiatorUserID, &draft.Revision, &draft.ParentTaskDraftID,
		&draft.ReadinessAssessmentID, &draft.WorkspaceID, &draft.WorkspaceRevisionID, &snapshot, &draft.TaskSpecDigest,
		&assumptions, &status, &draft.InvalidatedAt, &draft.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current Assistant TaskDraft: %w", err)
	}
	draft.Status = domain.TaskDraftStatus(status)
	draft.TaskSpecSnapshot = append([]byte(nil), snapshot...)
	if err := json.Unmarshal(assumptions, &draft.Assumptions); err != nil {
		return nil, fmt.Errorf("decode TaskDraft assumptions: %w", err)
	}
	draft.InvalidatedAt = time.Time{} // The selected row is necessarily current.
	if err := draft.Validate(); err != nil {
		return nil, fmt.Errorf("stored Assistant TaskDraft is invalid: %w", err)
	}
	var taskSpec contractsv1.TaskSpec
	if err := json.Unmarshal(snapshot, &taskSpec); err != nil {
		return nil, fmt.Errorf("stored Assistant TaskDraft TaskSpec is invalid")
	}
	if err := taskSpec.Validate(); err != nil {
		return nil, fmt.Errorf("stored Assistant TaskDraft TaskSpec is invalid")
	}
	return &interactionapp.TaskDraftRecord{Draft: draft, TaskSpec: taskSpec}, nil
}

func (store Store) loadRecentContextTurns(ctx context.Context, conversationID string) ([]interactionapp.ContextItem, error) {
	rows, err := store.Pool.Query(ctx, `
SELECT turn_id,turn_sequence,role,content,correlation_id,created_at
FROM (
  SELECT turn_id,turn_sequence,role,content,correlation_id,created_at
  FROM assistant_conversation_turns
  WHERE conversation_id=$1 AND visible=true
  ORDER BY turn_sequence DESC
  LIMIT $2
) recent
ORDER BY turn_sequence`, conversationID, store.recentContextTurnLimit())
	if err != nil {
		return nil, fmt.Errorf("load recent Assistant turns: %w", err)
	}
	defer rows.Close()
	items := make([]interactionapp.ContextItem, 0)
	for rows.Next() {
		var id, role, content, correlation string
		var sequence uint64
		var createdAt time.Time
		if err := rows.Scan(&id, &sequence, &role, &content, &correlation, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, contextItem("turn/"+id, "conversation_turn", 50+int(sequence%10), map[string]any{
			"turn_id": id, "sequence": sequence, "role": role, "content": content, "correlation_id": correlation, "created_at": createdAt.UTC(),
		}))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (store Store) loadCurrentReadinessContext(ctx context.Context, conversationID string) (*interactionapp.ContextItem, error) {
	var id string
	var payload json.RawMessage
	err := store.Pool.QueryRow(ctx, `
SELECT assessment_id,jsonb_build_object('assessment_id',assessment_id,'revision',assessment_revision,
  'policy_revision',policy_revision,'dimensions',dimensions,'blocking_unknowns',blocking_unknowns,
  'assumptions',assumptions,'clarification_questions',clarification_question_count,'status',status)
FROM assistant_readiness_assessments WHERE conversation_id=$1 ORDER BY evaluated_at DESC,assessment_id DESC LIMIT 1`, conversationID).Scan(&id, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current Assistant readiness: %w", err)
	}
	item := interactionapp.ContextItem{Ref: "readiness/" + id, Kind: "readiness", Priority: 85, EstimatedTokens: estimateTokens(payload), Content: payload}
	return &item, nil
}

func (store Store) loadCurrentCheckpointContext(ctx context.Context, conversationID string) (*interactionapp.ContextItem, error) {
	var id string
	var payload json.RawMessage
	err := store.Pool.QueryRow(ctx, `
SELECT checkpoint_id,jsonb_build_object('checkpoint_id',checkpoint_id,'revision',revision,'working_draft',working_draft,
  'assumptions',assumptions,'blockers',blockers,'evidence_refs',evidence_refs,'allowed_actions',allowed_actions,'status',status)
FROM assistant_clarification_checkpoints WHERE conversation_id=$1 ORDER BY created_at DESC,checkpoint_id DESC LIMIT 1`, conversationID).Scan(&id, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current Assistant checkpoint: %w", err)
	}
	item := interactionapp.ContextItem{Ref: "checkpoint/" + id, Kind: "checkpoint", Priority: 85, EstimatedTokens: estimateTokens(payload), Content: payload}
	return &item, nil
}

func contextItem(ref, kind string, priority int, content any) interactionapp.ContextItem {
	encoded, _ := json.Marshal(content)
	return interactionapp.ContextItem{Ref: ref, Kind: kind, Priority: priority, EstimatedTokens: estimateTokens(encoded), Content: encoded}
}

func estimateTokens(content []byte) int { return max(1, (len(content)+3)/4) }

func (store Store) recentContextTurnLimit() int {
	if store.MaxRecentTurns > 0 {
		return store.MaxRecentTurns
	}
	return defaultRecentContextTurnLimit
}

var _ interactionapp.ContextRepository = Store{}
var _ interactionapp.CurrentTaskDraftReader = Store{}
