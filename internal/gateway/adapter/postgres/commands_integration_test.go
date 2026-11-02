//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	interactiontestkit "github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestGatewayExplicitRetryUsesCurrentServerOwnedAssistantProfile(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	clock := interactiontestkit.NewClock(time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
	ids := &interactiontestkit.IDs{}
	profiles := config.ProductionProfileRegistry()
	current, ok := profiles.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("current Kimi Code Assistant profile missing")
	}
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCommandService(CommandServiceOptions{
		Pool: pool, IDs: ids, Now: clock.Now, ProjectID: "project-local", Profiles: profiles,
		NetworkProfile: network,
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID: "tenant-local", WorkspaceID: "workspace-local",
			WorkspaceRevisionID: "workspace-revision-local-v1",
			ResolutionID:        "resolution-local", ResolutionRevision: 1,
			MembershipRevision: 1, Readable: true,
		},
		Assistant: config.AssistantSettings{
			Enabled: true, ModelProfile: current.Ref(),
			ReadinessPolicyRevision: config.DefaultAssistantReadinessPolicy,
			TriggerPolicyRevision:   config.DefaultAssistantTriggerPolicy,
			ContextBudgetTokens:     8192, RequestTimeout: time.Minute, MaxRecentTurns: 24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := gatewayhttp.FixedActor{TenantID: "tenant-local", UserID: "user-local"}
	created, err := service.ExecuteAssistantCommand(ctx, actor, contractsv1.ConversationCommand{
		SchemaVersion: contractsv1.ConversationCommandSchemaVersion,
		CommandType:   "create_conversation", IdempotencyKey: "create-conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := service.ExecuteAssistantCommand(ctx, actor, contractsv1.ConversationCommand{
		SchemaVersion: contractsv1.ConversationCommandSchemaVersion,
		CommandType:   "send_message", ConversationID: created.ConversationID,
		ExpectedRevision: created.ConversationRevision,
		IdempotencyKey:   "send-message", Content: "Retry this exact request.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE assistant_runs
SET status='failed',retryable=true,error_code='model_policy_conflict',completed_at=$1,
    model_profile=$2,model_profile_revision='1',model_profile_digest=$3,
    policy_revision='assistant-readiness-v0',context_budget_tokens=4096,
    revision=revision+1,updated_at=$1
WHERE assistant_run_id=$4 AND status='queued'`,
		clock.Now(), config.KimiCodeAssistantOpenAIV1,
		"sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
		sent.AssistantRunID,
	); err != nil {
		t.Fatal(err)
	}
	retryCommand := contractsv1.ConversationCommand{
		SchemaVersion: contractsv1.ConversationCommandSchemaVersion,
		CommandType:   "retry_run", ConversationID: created.ConversationID,
		ExpectedRevision: sent.ConversationRevision,
		AssistantRunID:   sent.AssistantRunID, AssistantRunRevision: 2,
		IdempotencyKey: "retry-historical-run",
	}
	retried, err := service.ExecuteAssistantCommand(ctx, actor, retryCommand)
	if err != nil {
		t.Fatal(err)
	}
	retriedAgain, err := service.ExecuteAssistantCommand(ctx, actor, retryCommand)
	if err != nil || retriedAgain != retried {
		t.Fatalf("same-key Gateway retry diverged: first=%+v second=%+v error=%v",
			retried, retriedAgain, err)
	}
	var profileID, profileRevision, profileDigest, policyRevision string
	var contextBudget int
	if err := pool.QueryRow(ctx, `
SELECT model_profile,model_profile_revision,model_profile_digest,policy_revision,context_budget_tokens
FROM assistant_runs WHERE assistant_run_id=$1`, retried.AssistantRunID).Scan(
		&profileID, &profileRevision, &profileDigest, &policyRevision, &contextBudget,
	); err != nil {
		t.Fatal(err)
	}
	if profileID != current.ID || profileRevision != "2" || profileDigest != current.Digest ||
		policyRevision != config.DefaultAssistantReadinessPolicy || contextBudget != 8192 {
		t.Fatalf("Gateway retry did not use current server policy: %s/%s/%s policy=%s budget=%d",
			profileID, profileRevision, profileDigest, policyRevision, contextBudget)
	}
}
