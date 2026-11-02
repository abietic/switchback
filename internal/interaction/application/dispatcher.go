package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RunProcessor interface {
	ProcessAssistantRun(context.Context, RunClaim) error
}

type AssistantRunDispatcher struct {
	Runs            AssistantRunRepository
	Processor       RunProcessor
	Clock           Clock
	LeaseOwner      string
	LeaseDuration   time.Duration
	RecoveryEffects *PublishAssistantDerivedEffects
}

func (dispatcher AssistantRunDispatcher) RunOnce(ctx context.Context) (bool, error) {
	if dispatcher.Runs == nil || dispatcher.Processor == nil || dispatcher.Clock == nil ||
		dispatcher.LeaseOwner == "" || dispatcher.LeaseDuration <= 0 {
		return false, fmt.Errorf("AssistantRun dispatcher dependencies are required")
	}
	now := dispatcher.Clock.Now().UTC()
	recovery, err := dispatcher.Runs.RecoverExpiredClaims(ctx, now)
	if err != nil {
		return false, fmt.Errorf("recover expired AssistantRun claims: %w", err)
	}
	if dispatcher.RecoveryEffects != nil {
		for _, failure := range recovery.Failures {
			record, _ := json.Marshal(map[string]string{
				"assistant_run_id": failure.AssistantRunID,
				"status":           "failed",
				"error_code":       failure.ErrorCode,
			})
			if _, err := dispatcher.RecoveryEffects.Execute(ctx, PublishAssistantDerivedEffectsCommand{
				Evidence: &EvidenceBoundary{
					TenantID: failure.TenantID, UserID: failure.UserID,
					WorkspaceID: failure.WorkspaceID, ConversationID: failure.ConversationID,
					ConversationGeneration: failure.ConversationGeneration,
					AssistantRunID:         failure.AssistantRunID, BoundaryType: "run_failed",
					Records: []json.RawMessage{record},
					Metadata: map[string]string{
						"error_code":      failure.ErrorCode,
						"policy_revision": failure.PolicyRevision,
					},
				},
			}); err != nil {
				return false, fmt.Errorf("publish recovered AssistantRun effects: %w", err)
			}
		}
	}
	claim, err := dispatcher.Runs.ClaimNextRun(ctx, dispatcher.LeaseOwner, dispatcher.LeaseDuration, now)
	if errors.Is(err, ErrNoQueuedRun) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := dispatcher.Processor.ProcessAssistantRun(ctx, claim); err != nil {
		return true, err
	}
	return true, nil
}
