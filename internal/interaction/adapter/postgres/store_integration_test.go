//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestTransactionPersistsOrderedMessageIdempotencyAndOutbox(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, err := domain.NewConversation("conversation-1", "tenant-local", "user-local", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTransaction(ctx, func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(ctx, conversation); err != nil {
			return err
		}
		loaded, err := tx.LoadConversation(ctx, "tenant-local", "user-local", "conversation-1", true)
		if err != nil {
			return err
		}
		accepted, err := loaded.AcceptMessage(1, "turn-1", "assistant-run-1", "correlation-1", "Do work",
			"kimi-assistant-openai-v1", "assistant-readiness-v1", 8192, now.Add(time.Second), now.Add(time.Minute))
		if err != nil {
			return err
		}
		if err := tx.InsertTurn(ctx, accepted.Turn); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, accepted.Run, "1",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			return err
		}
		if err := tx.UpdateConversation(ctx, loaded, 1); err != nil {
			return err
		}
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: "event-1",
			EventType: "AssistantMessageAccepted.v1", ProducerContext: "interaction",
			StreamID: "conversation-1", TenantID: "tenant-local", AggregateType: "Conversation",
			AggregateID: "conversation-1", TraceID: "trace-1", CorrelationID: "correlation-1",
			CausationID: "command-1", IdempotencyKey: "message-1", Visibility: "user",
			Payload: json.RawMessage(`{"turn_id":"turn-1"}`), OccurredAt: now.Add(time.Second),
		}
		if err := tx.InsertOutboxEvent(ctx, event); err != nil {
			return err
		}
		return tx.InsertIdempotencyResult(ctx, "tenant-local", "user-local", "assistant.send_message",
			"message-1", interactionapp.IdempotencyResult{
				RequestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ResourceType:  "Conversation", ResourceID: "conversation-1", Status: 202,
				Response: json.RawMessage(`{"turn_id":"turn-1","assistant_run_id":"assistant-run-1"}`),
			}, now.Add(time.Second))
	}); err != nil {
		t.Fatal(err)
	}

	assertCount(t, pool, "assistant_conversations", 1)
	assertCount(t, pool, "assistant_conversation_turns", 1)
	assertCount(t, pool, "assistant_runs", 1)
	assertCount(t, pool, "outbox_events", 1)
	assertCount(t, pool, "idempotency_results", 1)

	if err := store.WithinTransaction(ctx, func(tx interactionapp.Transaction) error {
		conversation, err := tx.LoadConversation(ctx, "tenant-local", "user-local", "conversation-1", false)
		if err != nil {
			return err
		}
		if conversation.Revision != 2 || conversation.NextTurnSequence != 2 || conversation.NextRunSequence != 2 {
			t.Fatalf("unexpected persisted Conversation: %+v", conversation)
		}
		result, found, err := tx.FindIdempotencyResult(ctx, "tenant-local", "user-local",
			"assistant.send_message", "message-1")
		if err != nil || !found || result.ResourceID != "conversation-1" {
			t.Fatalf("idempotency result: found=%v result=%+v error=%v", found, result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionRollsBackAllAssistantMessageState(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := domain.NewConversation("conversation-rollback", "tenant-local", "user-local", now)
	errExpected := context.Canceled
	err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(context.Background(), conversation); err != nil {
			return err
		}
		return errExpected
	})
	if err != errExpected {
		t.Fatalf("transaction error = %v", err)
	}
	assertCount(t, pool, "assistant_conversations", 0)
}

func TestSendConversationMessageIsModelFreeAndIdempotent(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := domain.NewConversation("conversation-service", "tenant-local", "user-local", now)
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		return tx.InsertConversation(context.Background(), conversation)
	}); err != nil {
		t.Fatal(err)
	}
	profile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Assistant profile missing")
	}
	service := interactionapp.SendConversationMessage{
		Transactions: store, IDs: &testkit.IDs{}, Clock: testkit.NewClock(now.Add(time.Second)),
		ModelProfile: profile.Ref(), PolicyRevision: config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192, RunTimeout: 90 * time.Second,
	}
	command := interactionapp.SendConversationMessageCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: "conversation-service", ExpectedConversationRevision: 1,
		IdempotencyKey: "send-service-1", Content: "Implement one health endpoint.",
	}
	first, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Status != "accepted" || first.ConversationRevision != 2 {
		t.Fatalf("idempotent response changed: first=%+v second=%+v", first, second)
	}
	command.Content = "different request"
	if _, err := service.Execute(context.Background(), command); err != interactionapp.ErrIdempotencyConflict {
		t.Fatalf("idempotency conflict = %v", err)
	}
	assertCount(t, pool, "assistant_conversation_turns", 1)
	assertCount(t, pool, "assistant_runs", 1)
	assertCount(t, pool, "outbox_events", 1)
	assertCount(t, pool, "idempotency_results", 1)
}

func TestConcurrentConversationMessagesAcceptExactlyOneExpectedRevision(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, err := domain.NewConversation("conversation-message-race", "tenant-local", "user-local", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		return tx.InsertConversation(context.Background(), conversation)
	}); err != nil {
		t.Fatal(err)
	}
	profile, _ := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	service := interactionapp.SendConversationMessage{
		Transactions: store, IDs: &testkit.IDs{}, Clock: testkit.NewClock(now.Add(time.Second)),
		ModelProfile: profile.Ref(), PolicyRevision: config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192, RunTimeout: time.Minute,
	}
	type result struct {
		value interactionapp.SendConversationMessageResult
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for index, content := range []string{"first intent", "competing intent"} {
		go func(index int, content string) {
			<-start
			value, err := service.Execute(context.Background(), interactionapp.SendConversationMessageCommand{
				Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
				ConversationID: "conversation-message-race", ExpectedConversationRevision: 1,
				IdempotencyKey: fmt.Sprintf("message-race-%d", index+1), Content: content,
			})
			results <- result{value: value, err: err}
		}(index, content)
	}
	close(start)
	successes, stale := 0, 0
	for range 2 {
		current := <-results
		switch {
		case current.err == nil:
			successes++
			if current.value.ConversationRevision != 2 {
				t.Fatalf("accepted concurrent message = %+v", current.value)
			}
		case errors.Is(current.err, domain.ErrStaleRevision):
			stale++
		default:
			t.Fatalf("concurrent message error = %v", current.err)
		}
	}
	var turns, runs, events int
	if err := pool.QueryRow(context.Background(), `
SELECT (SELECT count(*) FROM assistant_conversation_turns WHERE conversation_id='conversation-message-race'),
       (SELECT count(*) FROM assistant_runs WHERE conversation_id='conversation-message-race'),
       (SELECT count(*) FROM outbox_events WHERE aggregate_id='conversation-message-race')`,
	).Scan(&turns, &runs, &events); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || stale != 1 || turns != 1 || runs != 1 || events != 1 {
		t.Fatalf("concurrent messages successes=%d stale=%d turns=%d runs=%d events=%d",
			successes, stale, turns, runs, events)
	}
}

func TestAssistantRunQueueSerializesConversationAndRecoversSafely(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := domain.NewConversation("conversation-queue", "tenant-local", "user-local", now)
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		return tx.InsertConversation(context.Background(), conversation)
	}); err != nil {
		t.Fatal(err)
	}
	profile, _ := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	clock := testkit.NewClock(now.Add(time.Second))
	service := interactionapp.SendConversationMessage{
		Transactions: store, IDs: &testkit.IDs{}, Clock: clock, ModelProfile: profile.Ref(),
		PolicyRevision: config.DefaultAssistantReadinessPolicy, ContextBudgetTokens: 8192,
		RunTimeout: 90 * time.Second,
	}
	first, err := service.Execute(context.Background(), interactionapp.SendConversationMessageCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: "conversation-queue", ExpectedConversationRevision: 1,
		IdempotencyKey: "queue-1", Content: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	second, err := service.Execute(context.Background(), interactionapp.SendConversationMessageCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: "conversation-queue", ExpectedConversationRevision: 2,
		IdempotencyKey: "queue-2", Content: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimNextRun(context.Background(), "dispatcher-1", time.Second, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.ID != first.AssistantRunID || len(claim.Turns) != 2 {
		t.Fatalf("first queue claim = %+v", claim)
	}
	if _, err := store.ClaimNextRun(context.Background(), "dispatcher-2", time.Second, clock.Now()); err != interactionapp.ErrNoQueuedRun {
		t.Fatalf("second active claim = %v", err)
	}
	clock.Advance(2 * time.Second)
	recovered, err := store.RecoverExpiredClaims(context.Background(), clock.Now())
	if err != nil || recovered.Count != 1 {
		t.Fatalf("recover claims: count=%d error=%v", recovered.Count, err)
	}
	reclaimed, err := store.ClaimNextRun(context.Background(), "dispatcher-2", time.Second, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Run.ID != first.AssistantRunID || reclaimed.Run.FencingToken != 2 {
		t.Fatalf("safe pre-invocation recovery did not reclaim original Run: %+v", reclaimed.Run)
	}
	if _, err := store.MarkInvocationStarted(
		context.Background(), reclaimed.Run.ID, reclaimed.Run.Revision, reclaimed.Run.FencingToken, clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	recovered, err = store.RecoverExpiredClaims(context.Background(), clock.Now())
	if err != nil || recovered.Count != 1 || len(recovered.Failures) != 1 {
		t.Fatalf("recover unknown model effect: result=%+v error=%v", recovered, err)
	}
	pendingRecovery, err := store.RecoverExpiredClaims(context.Background(), clock.Now())
	if err != nil || pendingRecovery.Count != 0 || len(pendingRecovery.Failures) != 1 ||
		pendingRecovery.Failures[0].AssistantRunID != first.AssistantRunID {
		t.Fatalf("pending recovery effect was not crash-retryable: result=%+v error=%v", pendingRecovery, err)
	}
	var status, errorCode string
	if err := pool.QueryRow(context.Background(), `
SELECT status,COALESCE(error_code,'') FROM assistant_runs WHERE assistant_run_id=$1`,
		first.AssistantRunID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorCode != "model_effect_unknown" {
		t.Fatalf("unknown model effect was retried: status=%s error=%s", status, errorCode)
	}
	var recoveryEvents int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FROM outbox_events
WHERE aggregate_id='conversation-queue'
  AND event_type IN ('AssistantRunClaimed.v1','AssistantRunRequeued.v1','AssistantRunRecoveryFailed.v1')`,
	).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 4 {
		t.Fatalf("AssistantRun lifecycle events = %d, want 4", recoveryEvents)
	}
	next, err := store.ClaimNextRun(context.Background(), "dispatcher-3", time.Second, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if next.Run.ID != second.AssistantRunID {
		t.Fatalf("stable queued order skipped second Run: %+v", next.Run)
	}
}

func TestAssistantRunQueueFencesConcurrentDuplicateClaims(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := domain.NewConversation("conversation-concurrent", "tenant-local", "user-local", now)
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(context.Background(), conversation); err != nil {
			return err
		}
		accepted, err := conversation.AcceptMessage(
			1, "turn-concurrent", "run-concurrent", "command-concurrent", "work",
			config.KimiAssistantOpenAIV1, config.DefaultAssistantReadinessPolicy, 8192,
			now.Add(time.Second), now.Add(time.Minute),
		)
		if err != nil {
			return err
		}
		if err := tx.InsertTurn(context.Background(), accepted.Turn); err != nil {
			return err
		}
		if err := tx.InsertRun(context.Background(), accepted.Run, "1",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			return err
		}
		return tx.UpdateConversation(context.Background(), conversation, 1)
	}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		claim interactionapp.RunClaim
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, owner := range []string{"dispatcher-a", "dispatcher-b"} {
		go func(owner string) {
			<-start
			claim, err := store.ClaimNextRun(context.Background(), owner, 30*time.Second, now.Add(time.Second))
			results <- result{claim: claim, err: err}
		}(owner)
	}
	close(start)
	successes, empty := 0, 0
	for range 2 {
		current := <-results
		switch current.err {
		case nil:
			successes++
			if current.claim.Run.ID != "run-concurrent" {
				t.Fatalf("claimed wrong Run: %+v", current.claim)
			}
		case interactionapp.ErrNoQueuedRun:
			empty++
		default:
			t.Fatalf("concurrent claim failed unexpectedly: %v", current.err)
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("concurrent claims: successes=%d empty=%d", successes, empty)
	}
}

func TestCommitDecisionPublishesOrderedIdempotentConversationProjection(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	clock := testkit.NewClock(startedAt)
	conversation, _ := domain.NewConversation(
		"conversation-decision", "tenant-local", "user-local", clock.Now(),
	)
	if err := store.WithinTransaction(ctx, func(tx interactionapp.Transaction) error {
		return tx.InsertConversation(ctx, conversation)
	}); err != nil {
		t.Fatal(err)
	}
	profile, _ := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	service := interactionapp.SendConversationMessage{
		Transactions: store, IDs: &testkit.IDs{}, Clock: clock,
		ModelProfile: profile.Ref(), PolicyRevision: config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192, RunTimeout: time.Minute,
	}
	clock.Advance(time.Second)
	accepted, err := service.Execute(ctx, interactionapp.SendConversationMessageCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: "conversation-decision", ExpectedConversationRevision: 1,
		IdempotencyKey: "decision-message-1", Content: "Build the requested feature.",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	claim, err := store.ClaimNextRun(ctx, "assistant-dispatcher", time.Minute, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	runRevision, err := store.MarkInvocationStarted(
		ctx, claim.Run.ID, claim.Run.Revision, claim.Run.FencingToken, clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	readiness, decision := clarificationDecision(
		claim, "assessment-decision", clock.Now(),
	)
	projectionPayload, err := json.Marshal(contractsv1.AssistantProjectionEvent{
		SchemaVersion: contractsv1.AssistantProjectionSchemaVersion,
		EventType:     "clarification_ready", ConversationID: claim.Conversation.ID,
		ConversationRevision: claim.Conversation.Revision + 1, AssistantRunID: claim.Run.ID,
		CommandID: accepted.CommandID, ProjectionRef: accepted.ProjectionRef,
		Payload:    json.RawMessage(`{"readiness_assessment_id":"assessment-decision"}`),
		OccurredAt: clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       "event-decision", EventType: "AssistantDecisionCommitted.v1",
		ProducerContext: "interaction", StreamID: "conversation/" + claim.Conversation.ID,
		Sequence: 1, TenantID: "tenant-local", AggregateType: "Conversation",
		AggregateID: claim.Conversation.ID, TraceID: claim.Run.ID,
		CorrelationID: accepted.CommandID, CausationID: accepted.CommandID,
		IdempotencyKey: "assistant-decision/" + claim.Run.ID, Visibility: "user",
		Payload: projectionPayload, OccurredAt: clock.Now(),
	}
	if err := store.CommitDecision(ctx, interactionapp.RunDecisionCommit{
		ConversationID:               claim.Conversation.ID,
		ExpectedConversationRevision: claim.Conversation.Revision,
		RunID:                        claim.Run.ID, ExpectedRunRevision: runRevision,
		FencingToken: claim.Run.FencingToken,
		AssistantTurn: domain.Turn{
			ID: "turn-assistant-decision", CorrelationID: accepted.CommandID,
		},
		Decision: decision, Readiness: readiness, Event: event, OccurredAt: clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var runStatus, invocationState string
	var conversationRevision, nextTurnSequence uint64
	var activeRunID, readinessID string
	if err := pool.QueryRow(ctx, `
SELECT run.status,run.invocation_state,conversation.revision,conversation.next_turn_sequence,
       COALESCE(conversation.active_assistant_run_id,''),
       COALESCE(conversation.current_readiness_assessment_id,'')
FROM assistant_runs run
JOIN assistant_conversations conversation ON conversation.conversation_id=run.conversation_id
WHERE run.assistant_run_id=$1`, claim.Run.ID).Scan(
		&runStatus, &invocationState, &conversationRevision, &nextTurnSequence,
		&activeRunID, &readinessID,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || invocationState != "response_received" ||
		conversationRevision != claim.Conversation.Revision+1 || nextTurnSequence != 3 ||
		activeRunID != "" || readinessID != readiness.Assessment.ID {
		t.Fatalf(
			"decision state did not converge: run=%s invocation=%s revision=%d next=%d active=%q readiness=%q",
			runStatus, invocationState, conversationRevision, nextTurnSequence, activeRunID, readinessID,
		)
	}
	effectsResult, err := (interactionapp.PublishAssistantDerivedEffects{
		Objects:       &testkit.ObjectStore{Err: errors.New("object store unavailable")},
		Observability: &testkit.Observability{Err: errors.New("Langfuse unavailable")},
		Store:         store, Clock: clock,
	}).Execute(ctx, interactionapp.PublishAssistantDerivedEffectsCommand{
		Evidence: &interactionapp.EvidenceBoundary{
			TenantID: "tenant-local", UserID: "user-local", ConversationID: claim.Conversation.ID,
			ConversationGeneration: claim.Conversation.Generation, AssistantRunID: claim.Run.ID,
			BoundaryType: "run_failed", Records: []json.RawMessage{json.RawMessage(`{"status":"completed"}`)},
			Metadata: map[string]string{},
		},
		Observation: &interactionapp.ModelObservation{
			TenantID: "tenant-local", UserID: "user-local", AssistantRunID: claim.Run.ID,
			ConversationID: claim.Conversation.ID, ProfileID: profile.ID,
			ProfileRevision: profile.Revision, ProfileDigest: profile.Digest,
			ProviderRequestID: "provider-request-decision", Model: "provider/model",
			StartedAt: clock.Now().Add(-time.Second), CompletedAt: clock.Now(), Status: "ok",
		},
	})
	if err != nil || effectsResult.EvidenceStatus != interactionapp.DerivedEffectDegraded ||
		effectsResult.ObservabilityStatus != interactionapp.DerivedEffectDegraded {
		t.Fatalf("record degraded Assistant effects: result=%+v error=%v", effectsResult, err)
	}
	effects, err := store.LoadAssistantDerivedEffectStatus(ctx, claim.Run.ID)
	if err != nil || effects.EvidenceStatus != interactionapp.DerivedEffectDegraded ||
		effects.ObservabilityStatus != interactionapp.DerivedEffectDegraded {
		t.Fatalf("Assistant effect status=%+v error=%v", effects, err)
	}
	if err := pool.QueryRow(ctx, `
SELECT status FROM assistant_runs WHERE assistant_run_id=$1`, claim.Run.ID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" {
		t.Fatalf("derived effect failure rewrote AssistantRun outcome to %s", runStatus)
	}
	var sequences []uint64
	if err := pool.QueryRow(ctx, `
SELECT array_agg(sequence ORDER BY sequence)
FROM outbox_events WHERE stream_id=$1`, event.StreamID).Scan(&sequences); err != nil {
		t.Fatal(err)
	}
	if len(sequences) != 3 || sequences[0] != 1 || sequences[1] != 2 || sequences[2] != 3 {
		t.Fatalf("Conversation owner sequence = %v", sequences)
	}

	projectionStore := projectionpostgres.NewStore(pool)
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: projectionStore},
		UserID: "user-local", Now: func() time.Time { return clock.Now() },
	}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: materializer, LeaseOwner: "projection-test",
		LeaseDuration: time.Minute, BatchSize: 16, Now: func() time.Time { return clock.Now() },
	}
	if published, err := dispatcher.RunOnce(ctx); err != nil || published != 3 {
		t.Fatalf("publish Assistant events: count=%d error=%v", published, err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		projectionStore,
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return clock.Now() }},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewAssistantConversation,
		ViewID:   claim.Conversation.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Revision uint64 `json:"revision"`
		Turns    []struct {
			Role string `json:"role"`
		} `json:"turns"`
		AssistantRun struct {
			Status string `json:"status"`
		} `json:"assistant_run"`
		Readiness struct {
			Status string `json:"status"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(snapshot.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Revision != conversationRevision || len(data.Turns) != 2 ||
		data.Turns[1].Role != "assistant" || data.AssistantRun.Status != "completed" ||
		data.Readiness.Status != "clarifying" {
		t.Fatalf("Assistant Conversation projection = %s", snapshot.Data)
	}
	event.Sequence = 3
	if err := materializer.Publish(ctx, event); err != nil {
		t.Fatalf("idempotent projection replay: %v", err)
	}
	replayed, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewAssistantConversation,
		ViewID:   claim.Conversation.ID,
	})
	if err != nil || replayed.ViewCursor != snapshot.ViewCursor {
		t.Fatalf("projection replay advanced cursor: before=%d after=%d error=%v",
			snapshot.ViewCursor, replayed.ViewCursor, err)
	}
}

func TestAssistantReadinessIsImmutableAndAllowsUnresolvedWorkspace(t *testing.T) {
	pool := postgrestest.Open(t)
	store := Store{Pool: pool}
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	conversation, _ := domain.NewConversation("conversation-readiness", "tenant-local", "user-local", now)
	if err := store.WithinTransaction(context.Background(), func(tx interactionapp.Transaction) error {
		if err := tx.InsertConversation(context.Background(), conversation); err != nil {
			return err
		}
		accepted, err := conversation.AcceptMessage(
			1, "turn-readiness", "run-readiness", "command-readiness", "clarify",
			config.KimiAssistantOpenAIV1, config.DefaultAssistantReadinessPolicy, 8192,
			now.Add(time.Second), now.Add(time.Minute),
		)
		if err != nil {
			return err
		}
		if err := tx.InsertTurn(context.Background(), accepted.Turn); err != nil {
			return err
		}
		if err := tx.InsertRun(context.Background(), accepted.Run, "1",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			return err
		}
		return tx.UpdateConversation(context.Background(), conversation, 1)
	}); err != nil {
		t.Fatal(err)
	}
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions[dimension] = domain.DimensionState{Status: domain.DimensionKnown, Summary: "known"}
	}
	dimensions[domain.DimensionContextAccess] = domain.DimensionState{
		Status: domain.DimensionBlockingUnknown, Summary: "Workspace is unresolved",
	}
	record := interactionapp.ReadinessRecord{
		Assessment: domain.TaskReadinessAssessment{
			ID: "assessment-readiness", ConversationID: "conversation-readiness",
			AssistantRunID: "run-readiness", Revision: 1,
			PolicyRevision: config.DefaultAssistantReadinessPolicy, Dimensions: dimensions,
			BlockingUnknowns: []string{"context_access"}, Assumptions: []string{},
			Status: domain.ReadinessClarifying, EvaluatedAt: now.Add(time.Second),
		},
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if err := store.SaveReadiness(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReadiness(context.Background(), record); err == nil {
		t.Fatal("immutable readiness assessment was overwritten")
	}
	assertCount(t, pool, "assistant_readiness_assessments", 1)
	var workspaceCount int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FROM assistant_readiness_assessments
WHERE assessment_id='assessment-readiness' AND workspace_id IS NULL`).Scan(&workspaceCount); err != nil {
		t.Fatal(err)
	}
	if workspaceCount != 1 {
		t.Fatal("unresolved Workspace readiness was not persisted")
	}
	checkpoint := domain.ClarificationCheckpoint{
		ID: "checkpoint-readiness", ConversationID: "conversation-readiness", Revision: 1,
		WorkingDraft:          json.RawMessage(`{"summary":"partial"}`),
		ReadinessAssessmentID: "assessment-readiness", Assumptions: []string{},
		Blockers: []string{"context_access"}, EvidenceRefs: []string{"turn-readiness"},
		AllowedActions: []domain.CheckpointAction{
			domain.CheckpointContinue, domain.CheckpointNarrowScope, domain.CheckpointCancel,
		},
		Status: domain.CheckpointActive, CreatedAt: now.Add(2 * time.Second),
	}
	if err := store.SaveCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ApplyCheckpointAction(
		context.Background(), checkpoint.ConversationID, checkpoint.ID, 1,
		domain.CheckpointContinue, now.Add(3*time.Second),
	)
	if err != nil || resolved.Revision != 2 || resolved.Status != domain.CheckpointResolved {
		t.Fatalf("apply checkpoint: checkpoint=%+v error=%v", resolved, err)
	}
	if _, err := store.ApplyCheckpointAction(
		context.Background(), checkpoint.ConversationID, checkpoint.ID, 1,
		domain.CheckpointContinue, now.Add(4*time.Second),
	); err != domain.ErrStaleRevision {
		t.Fatalf("stale checkpoint action = %v", err)
	}
}

func clarificationDecision(
	claim interactionapp.RunClaim,
	assessmentID string,
	now time.Time,
) (interactionapp.ReadinessRecord, contractsv1.AssistantDecision) {
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState)
	contractDimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		status := domain.DimensionKnown
		summary := "Known"
		if dimension == domain.DimensionContextAccess {
			status = domain.DimensionBlockingUnknown
			summary = "Workspace is unresolved"
		}
		dimensions[dimension] = domain.DimensionState{Status: status, Summary: summary}
		contractDimensions = append(contractDimensions, contractsv1.ReadinessDimensionState{
			Dimension: string(dimension), Status: string(status), Summary: summary,
			EvidenceRefs: []string{},
		})
	}
	assessment := domain.TaskReadinessAssessment{
		ID: assessmentID, ConversationID: claim.Conversation.ID, AssistantRunID: claim.Run.ID,
		Revision: claim.Conversation.Revision, PolicyRevision: config.DefaultAssistantReadinessPolicy,
		Dimensions: dimensions, BlockingUnknowns: []string{"context_access"},
		Assumptions: []string{}, ClarificationQuestions: 1,
		Status: domain.ReadinessClarifying, EvaluatedAt: now,
	}
	readiness := contractsv1.AssistantReadinessAssessment{
		SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
		AssessmentID:  assessment.ID, Revision: assessment.Revision,
		PolicyRevision: assessment.PolicyRevision, Dimensions: contractDimensions,
		BlockingUnknowns: append([]string{}, assessment.BlockingUnknowns...),
		Assumptions:      []string{}, ClarificationQuestions: assessment.ClarificationQuestions,
		Status: string(assessment.Status), EvaluatedAt: now,
	}
	return interactionapp.ReadinessRecord{
			Assessment: assessment,
			Digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}, contractsv1.AssistantDecision{
			SchemaVersion:         contractsv1.AssistantDecisionSchemaVersion,
			DecisionType:          "clarification",
			UserVisibleContent:    "Which authorized Workspace should this work use?",
			ClarificationQuestion: "Which authorized Workspace should this work use?",
			Readiness:             readiness,
		}
}

func assertCount(t *testing.T, pool *pgxpool.Pool, table string, expected int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("%s count = %d, want %d", table, count, expected)
	}
}
