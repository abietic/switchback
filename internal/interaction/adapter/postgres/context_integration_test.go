//go:build integration

package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestContextRepositoryUsesConfiguredRecentTurnWindow(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool, MaxRecentTurns: 2}
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	conversation, err := domain.NewConversation("conversation-context-window", "tenant-local", "user-local", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(context.Background(), conversation); err != nil {
			return err
		}
		for sequence := uint64(1); sequence <= 3; sequence++ {
			if err := tx.InsertTurn(context.Background(), domain.Turn{ID: "turn-context-" + string(rune('0'+sequence)),
				ConversationID: conversation.ID, Sequence: sequence, Role: domain.TurnUser, Content: "message", Visible: true,
				CorrelationID: "command-context", CreatedAt: now.Add(time.Duration(sequence) * time.Second)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, err := store.LoadContextCandidates(context.Background(), interactionapp.RunClaim{
		Conversation: conversation, Run: domain.AssistantRun{ID: "run-context", PolicyRevision: "assistant-readiness-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := 0
	for _, item := range items {
		if item.Kind == "conversation_turn" {
			turns++
		}
	}
	if turns != 2 {
		t.Fatalf("configured ContextPack recent turns = %d, want 2", turns)
	}
}

func TestContextCompilerPersistsPolicyRevisionForAudit(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool, MaxRecentTurns: 2}
	ctx := context.Background()
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	conversation, err := domain.NewConversation("conversation-context-policy", "tenant-local", "user-local", now)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Kimi Code Assistant profile missing")
	}
	var claim interactionapp.RunClaim
	if err := store.WithinTransaction(ctx, func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(ctx, conversation); err != nil {
			return err
		}
		loaded, err := tx.LoadConversation(ctx, "tenant-local", "user-local", conversation.ID, true)
		if err != nil {
			return err
		}
		accepted, err := loaded.AcceptMessage(
			loaded.Revision,
			"turn-context-policy", "run-context-policy", "command-context-policy", "build it",
			profile.ID, "assistant-readiness-v1", 8192, now.Add(time.Second), now.Add(time.Minute),
		)
		if err != nil {
			return err
		}
		if err := tx.InsertTurn(ctx, accepted.Turn); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, accepted.Run, strconv.FormatUint(profile.Revision, 10), profile.Digest); err != nil {
			return err
		}
		if err := tx.UpdateConversation(ctx, loaded, conversation.Revision); err != nil {
			return err
		}
		claim = interactionapp.RunClaim{
			Conversation: loaded, Run: accepted.Run,
			ModelProfileRevision: profile.Revision, ModelProfileDigest: profile.Digest,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ids := &testkit.IDs{}
	pack, err := (interactionapp.ContextCompiler{Repository: store, IDs: ids, Budget: 8192}).Compile(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	var storedPolicyRevision, storedSchemaVersion, storedDigest string
	if err := pool.QueryRow(ctx, `
SELECT compiled_payload->>'policy_revision',schema_version,content_digest
FROM assistant_context_packs
WHERE context_pack_id=$1`, pack.ContextPackID).Scan(
		&storedPolicyRevision, &storedSchemaVersion, &storedDigest,
	); err != nil {
		t.Fatal(err)
	}
	if pack.PolicyRevision != claim.Run.PolicyRevision ||
		storedPolicyRevision != claim.Run.PolicyRevision ||
		storedSchemaVersion != interactionapp.ContextPackSchemaVersion ||
		storedDigest != pack.Digest {
		t.Fatalf("persisted ContextPack policy evidence mismatch: pack=%+v stored=%q/%q/%q",
			pack, storedPolicyRevision, storedSchemaVersion, storedDigest)
	}
}
