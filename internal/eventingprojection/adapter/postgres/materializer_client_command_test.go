//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	interactionpostgres "github.com/abietic/switchback/internal/interaction/adapter/postgres"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAssistantProductionWriterMaterializesExactClientCommandReceipt(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 30, 4, 30, 0, 0, time.UTC)
	const (
		tenantID       = "tenant-assistant-command"
		userID         = "user-assistant-command"
		conversationID = "conversation-assistant-command"
		rawClientID    = "web-assistant-raw-command"
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO assistant_conversations
  (conversation_id,tenant_id,actor_user_id,generation,status,next_turn_sequence,next_run_sequence,
   revision,created_at,updated_at)
VALUES ($1,$2,$3,1,'active',1,1,1,$4,$4)`,
		conversationID, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	profile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Assistant profile missing")
	}
	result, err := (interactionapp.SendConversationMessage{
		Transactions: interactionpostgres.Store{Pool: pool},
		IDs:          &testkit.IDs{},
		Clock:        testkit.NewClock(now),
		ModelProfile: profile.Ref(), PolicyRevision: config.DefaultAssistantReadinessPolicy,
		ContextBudgetTokens: 8192, RunTimeout: time.Minute,
	}).Execute(ctx, interactionapp.SendConversationMessageCommand{
		Actor:          taskapp.Actor{TenantID: tenantID, UserID: userID},
		ConversationID: conversationID, ExpectedConversationRevision: 1,
		IdempotencyKey: rawClientID, Content: "Prove exact projection command correlation.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var eventID, eventSchema, eventClientID, eventIdempotencyKey, eventCommandID string
	if err := pool.QueryRow(ctx, `
SELECT event_id,schema_version,client_command_id,idempotency_key,correlation_id
FROM outbox_events WHERE aggregate_id=$1`, conversationID).Scan(
		&eventID, &eventSchema, &eventClientID, &eventIdempotencyKey, &eventCommandID,
	); err != nil {
		t.Fatal(err)
	}
	if eventSchema != contractsv1.IntegrationEventSchemaVersion ||
		eventClientID != rawClientID ||
		eventIdempotencyKey != "message-accepted/"+rawClientID ||
		eventCommandID != result.CommandID {
		t.Fatalf(
			"outbox schema=%q identities client=%q idempotency=%q command=%q result=%q",
			eventSchema, eventClientID, eventIdempotencyKey, eventCommandID, result.CommandID,
		)
	}

	store := projectionpostgres.NewStore(pool)
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		UserID: userID, Now: func() time.Time { return now },
	}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: materializer, LeaseOwner: "assistant-command-materializer",
		LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return now },
	}
	if count, err := dispatcher.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("dispatch Assistant owner event count=%d error=%v", count, err)
	}
	var deltaClientID, deltaCommandID string
	if err := pool.QueryRow(ctx, `
SELECT client_command_id,command_id FROM projection_deltas
WHERE view_type=$1 AND view_id=$2`, projectionapp.ViewAssistantConversation, conversationID).Scan(
		&deltaClientID, &deltaCommandID,
	); err != nil {
		t.Fatal(err)
	}
	if deltaClientID != rawClientID || deltaCommandID != result.CommandID {
		t.Fatalf("delta identities client=%q command=%q", deltaClientID, deltaCommandID)
	}
	var receiptCommandID, receiptSourceEventID string
	if err := pool.QueryRow(ctx, `
SELECT command_id,source_event_id FROM projection_command_receipts
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4 AND client_command_id=$5`,
		tenantID, userID, projectionapp.ViewAssistantConversation, conversationID, rawClientID,
	).Scan(&receiptCommandID, &receiptSourceEventID); err != nil {
		t.Fatal(err)
	}
	if receiptCommandID != result.CommandID || receiptSourceEventID != eventID {
		t.Fatalf("receipt command=%q source=%q", receiptCommandID, receiptSourceEventID)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshotQuery := projectionapp.SnapshotQuery{
		Viewer:                    projectionapp.Viewer{TenantID: tenantID, UserID: userID},
		ViewType:                  projectionapp.ViewAssistantConversation,
		ViewID:                    conversationID,
		ReconcileClientCommandIDs: []string{rawClientID, "foreign-client-command"},
	}
	snapshot, err := reader.ReadSnapshot(ctx, snapshotQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CommandReceipts) != 1 ||
		snapshot.CommandReceipts[0].ClientCommandID != rawClientID ||
		snapshot.CommandReceipts[0].CommandID != result.CommandID {
		t.Fatalf("snapshot receipts=%+v", snapshot.CommandReceipts)
	}
	const futureClientID = "future-client-command"
	if _, err := pool.Exec(ctx, `
INSERT INTO projection_command_receipts
  (tenant_id,user_id,view_type,view_id,client_command_id,command_id,view_epoch,view_cursor,
   projection_version,source_event_id,applied_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		tenantID, userID, projectionapp.ViewAssistantConversation, conversationID,
		futureClientID, "future-server-command", snapshot.ViewEpoch, snapshot.ViewCursor+1,
		snapshot.ProjectionVersion+1, "future-source-event", now,
	); err != nil {
		t.Fatal(err)
	}
	snapshotQuery.ReconcileClientCommandIDs = []string{rawClientID, futureClientID}
	anchored, err := reader.ReadSnapshot(ctx, snapshotQuery)
	if err != nil || len(anchored.CommandReceipts) != 1 ||
		anchored.CommandReceipts[0].ClientCommandID != rawClientID {
		t.Fatalf("anchored snapshot receipts=%+v error=%v", anchored.CommandReceipts, err)
	}
	snapshotQuery.Viewer.UserID = "foreign-user"
	if _, err := reader.ReadSnapshot(ctx, snapshotQuery); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("foreign snapshot error=%v", err)
	}

	conflicting := contractsv1.IntegrationEvent{
		SchemaVersion:   contractsv1.IntegrationEventSchemaVersion,
		EventID:         "event-assistant-command-conflict",
		EventType:       "AssistantMessageAccepted.v1",
		ProducerContext: "interaction",
		StreamID:        "conversation/" + conversationID,
		Sequence:        2,
		TenantID:        tenantID,
		AggregateType:   "Conversation",
		AggregateID:     conversationID,
		TraceID:         "conversation/" + conversationID,
		CorrelationID:   "server-command-conflict",
		CausationID:     rawClientID,
		ClientCommandID: rawClientID,
		IdempotencyKey:  "message-accepted/conflict",
		Visibility:      "user",
		OccurredAt:      now.Add(time.Minute),
		Payload:         []byte(`{"conflict":true}`),
	}
	if err := materializer.Publish(ctx, conflicting); err == nil {
		t.Fatal("conflicting receipt mapping was accepted")
	}
	var cursorAfterConflict uint64
	if err := pool.QueryRow(ctx, `
SELECT view_cursor FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4`,
		tenantID, userID, projectionapp.ViewAssistantConversation, conversationID,
	).Scan(&cursorAfterConflict); err != nil {
		t.Fatal(err)
	}
	if cursorAfterConflict != snapshot.ViewCursor {
		t.Fatalf("conflicting receipt partially advanced View cursor=%d want=%d",
			cursorAfterConflict, snapshot.ViewCursor)
	}
	var conflictingReceiptCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM projection_event_receipts WHERE source_event_id=$1`,
		conflicting.EventID).Scan(&conflictingReceiptCount); err != nil {
		t.Fatal(err)
	}
	if conflictingReceiptCount != 0 {
		t.Fatal("conflicting receipt transaction persisted source receipt")
	}
}

func TestMaterializerPublishCarriesClientCommandIdentityAcrossOwnerUpdates(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 30, 5, 0, 0, 0, time.UTC)
	const (
		tenantID = "tenant-client-command"
		userID   = "user-client-command"
	)
	seedHomeDashboardRows(t, pool, tenantID, userID, now)
	seedHomeArtifact(t, pool, tenantID, userID, now)
	if _, err := pool.Exec(ctx, `
INSERT INTO assistant_conversations
  (conversation_id,tenant_id,actor_user_id,generation,status,next_turn_sequence,next_run_sequence,
   revision,created_at,updated_at)
VALUES ('conversation-client-command',$1,$2,1,'active',1,1,1,$3,$3)`,
		tenantID, userID, now); err != nil {
		t.Fatal(err)
	}

	materializer := projectionpostgres.Materializer{
		Pool: pool,
		Projector: projectionapp.Projector{
			Store: projectionpostgres.NewStore(pool),
		},
		UserID: userID,
		Now:    func() time.Time { return now },
	}

	workerEvent := clientCommandOwnerEvent(
		tenantID,
		"event-client-command-worker",
		"worker-session/worker-artifact",
		"WorkerSession",
		"worker-artifact",
		"server-command-worker",
		"client-command-worker",
		now,
	)
	if err := materializer.Publish(ctx, workerEvent); err != nil {
		t.Fatalf("publish Worker owner event: %v", err)
	}
	assertPublishedClientCommandUpdates(t, pool, workerEvent, []string{
		projectionapp.ViewArtifactDetail + ":artifact-home",
		projectionapp.ViewHomeDashboard + ":" + projectionpostgres.HomeDashboardViewID,
		projectionapp.ViewTaskSummary + ":" + projectionpostgres.TaskIndexViewID,
		projectionapp.ViewTaskSummary + ":task-b",
		projectionapp.ViewWorkerDetail + ":worker-artifact",
	})

	conversationEvent := clientCommandOwnerEvent(
		tenantID,
		"event-client-command-conversation",
		"conversation/conversation-client-command",
		"Conversation",
		"conversation-client-command",
		"server-command-conversation",
		"client-command-conversation",
		now.Add(time.Minute),
	)
	if err := materializer.Publish(ctx, conversationEvent); err != nil {
		t.Fatalf("publish Conversation owner event: %v", err)
	}
	assertPublishedClientCommandUpdates(t, pool, conversationEvent, []string{
		projectionapp.ViewAssistantConversation + ":conversation-client-command",
	})
}

func TestReplayPruningDoesNotRemoveCommandReceipts(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	store, err := projectionpostgres.NewStoreWithReplayLimits(pool, 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	projector := projectionapp.Projector{Store: store}
	now := time.Date(2026, time.July, 30, 5, 30, 0, 0, time.UTC)
	for sequence, identity := range []struct {
		client string
		server string
	}{
		{client: "client-prune-1", server: "server-prune-1"},
		{client: "client-prune-2", server: "server-prune-2"},
	} {
		number := uint64(sequence + 1)
		_, err := projector.Execute(ctx, projectionapp.ProjectionEvent{
			SourceEventID:  fmt.Sprintf("source-prune-%d", number),
			SourceStreamID: "source-prune-stream",
			SourceSequence: number,
			ProjectedAt:    now.Add(time.Duration(sequence) * time.Minute),
			Updates: []projectionapp.ViewUpdate{{
				DeltaID:          fmt.Sprintf("delta-prune-%d", number),
				TenantID:         "tenant-prune",
				UserID:           "user-prune",
				ViewType:         projectionapp.ViewWorkerDetail,
				ViewID:           "worker-prune",
				QueryFingerprint: "scope-v1",
				Data:             []byte(fmt.Sprintf(`{"revision":%d}`, number)),
				AvailableActions: []projectionapp.ActionRef{},
				RelatedRefs:      []projectionapp.RelatedRef{},
				EventType:        "view.entity_upserted.v1",
				Critical:         true,
				CommandID:        identity.server,
				ClientCommandID:  identity.client,
				SourceRefs:       []projectionapp.SourceRef{},
				DeltaPayload:     []byte(fmt.Sprintf(`{"data":{"revision":%d}}`, number)),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var deltaCount, receiptCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM projection_deltas
WHERE tenant_id='tenant-prune' AND user_id='user-prune'
  AND view_type='worker_detail' AND view_id='worker-prune'`).Scan(&deltaCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM projection_command_receipts
WHERE tenant_id='tenant-prune' AND user_id='user-prune'
  AND view_type='worker_detail' AND view_id='worker-prune'`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if deltaCount != 1 || receiptCount != 2 {
		t.Fatalf("deltas=%d receipts=%d want 1/2", deltaCount, receiptCount)
	}
}

func TestPublicSnapshotExcludesReceiptsOutsideThirtyDayHorizon(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	store := projectionpostgres.NewStore(pool)
	now := time.Date(2026, time.July, 30, 6, 0, 0, 0, time.UTC)
	projectCommandReceipt(t, store, 1, now.Add(-projectionapp.CommandReceiptRetentionHorizon-time.Second),
		"client-expired-reader", "server-expired-reader")

	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:                    projectionapp.Viewer{TenantID: "tenant-receipt-policy", UserID: "user-receipt-policy"},
		ViewType:                  projectionapp.ViewWorkerDetail,
		ViewID:                    "worker-receipt-policy",
		ReconcileClientCommandIDs: []string{"client-expired-reader"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CommandReceipts) != 0 {
		t.Fatalf("snapshot returned expired receipts=%+v", snapshot.CommandReceipts)
	}
}

func TestCommandReceiptCleanupIsTransactionalBoundedAndOutrunsOneEventAddition(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	store := projectionpostgres.NewStore(pool)
	now := time.Date(2026, time.July, 30, 6, 30, 0, 0, time.UTC)
	if projectionapp.CommandReceiptCleanupBatchSize <= 1 {
		t.Fatalf("cleanup batch=%d must exceed one receipt added per View update",
			projectionapp.CommandReceiptCleanupBatchSize)
	}
	projectCommandReceipt(t, store, 1, now.Add(-time.Hour), "", "")
	const expiredBacklog = 300
	if _, err := pool.Exec(ctx, `
INSERT INTO projection_command_receipts
  (tenant_id,user_id,view_type,view_id,client_command_id,command_id,view_epoch,view_cursor,
   projection_version,source_event_id,applied_at)
SELECT 'tenant-receipt-policy','user-receipt-policy','worker_detail','worker-receipt-policy',
       'client-expired-'||number,'server-expired-'||number,1,1,1,
       'source-expired-'||number,$1
FROM generate_series(1,$2) AS number`,
		now.Add(-projectionapp.CommandReceiptRetentionHorizon-time.Second), expiredBacklog); err != nil {
		t.Fatal(err)
	}

	projectCommandReceipt(t, store, 2, now, "client-current", "server-current")

	var total, expired int
	if err := pool.QueryRow(ctx, `
SELECT count(*),count(*) FILTER (WHERE applied_at<$1)
FROM projection_command_receipts
WHERE tenant_id='tenant-receipt-policy' AND user_id='user-receipt-policy'
  AND view_type='worker_detail' AND view_id='worker-receipt-policy'`,
		now.Add(-projectionapp.CommandReceiptRetentionHorizon)).Scan(&total, &expired); err != nil {
		t.Fatal(err)
	}
	wantExpired := expiredBacklog - projectionapp.CommandReceiptCleanupBatchSize
	if total != wantExpired+1 || expired != wantExpired {
		t.Fatalf("first receipt cleanup total=%d expired=%d, want=%d/%d",
			total, expired, wantExpired+1, wantExpired)
	}

	projectCommandReceipt(t, store, 3, now.Add(time.Minute), "client-current-2", "server-current-2")
	if err := pool.QueryRow(ctx, `
SELECT count(*),count(*) FILTER (WHERE applied_at<$1)
FROM projection_command_receipts
WHERE tenant_id='tenant-receipt-policy' AND user_id='user-receipt-policy'
  AND view_type='worker_detail' AND view_id='worker-receipt-policy'`,
		now.Add(time.Minute).Add(-projectionapp.CommandReceiptRetentionHorizon)).Scan(&total, &expired); err != nil {
		t.Fatal(err)
	}
	if total != 2 || expired != 0 {
		t.Fatalf("converged receipt cleanup total=%d expired=%d, want two recent receipts", total, expired)
	}
}

func TestCommandReceiptCleanupNeverDeletesRecentReceiptsForCount(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	store := projectionpostgres.NewStore(pool)
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	projectCommandReceipt(t, store, 1, now.Add(-time.Hour), "", "")
	const recentReceiptCount = 1027
	if _, err := pool.Exec(ctx, `
INSERT INTO projection_command_receipts
  (tenant_id,user_id,view_type,view_id,client_command_id,command_id,view_epoch,view_cursor,
   projection_version,source_event_id,applied_at)
SELECT 'tenant-receipt-policy','user-receipt-policy','worker_detail','worker-receipt-policy',
       'client-bounded-'||number,'server-bounded-'||number,1,1,1,
       'source-bounded-'||number,$1::timestamptz-(number * interval '1 second')
FROM generate_series(1,$2) AS number`,
		now, recentReceiptCount); err != nil {
		t.Fatal(err)
	}

	projectCommandReceipt(t, store, 2, now.Add(time.Minute), "client-latest", "server-latest")

	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM projection_command_receipts
WHERE tenant_id='tenant-receipt-policy' AND user_id='user-receipt-policy'
  AND view_type='worker_detail' AND view_id='worker-receipt-policy'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != recentReceiptCount+1 {
		t.Fatalf("recent receipt count=%d want=%d", count, recentReceiptCount+1)
	}
}

func projectCommandReceipt(
	t *testing.T,
	store *projectionpostgres.Store,
	sequence uint64,
	projectedAt time.Time,
	clientCommandID, commandID string,
) {
	t.Helper()
	_, err := (projectionapp.Projector{Store: store}).Execute(context.Background(), projectionapp.ProjectionEvent{
		SourceEventID:  fmt.Sprintf("source-receipt-policy-%d", sequence),
		SourceStreamID: "source-receipt-policy",
		SourceSequence: sequence,
		ProjectedAt:    projectedAt,
		Updates: []projectionapp.ViewUpdate{{
			DeltaID:          fmt.Sprintf("delta-receipt-policy-%d", sequence),
			TenantID:         "tenant-receipt-policy",
			UserID:           "user-receipt-policy",
			ViewType:         projectionapp.ViewWorkerDetail,
			ViewID:           "worker-receipt-policy",
			QueryFingerprint: "scope-v1",
			Data:             []byte(fmt.Sprintf(`{"revision":%d}`, sequence)),
			AvailableActions: []projectionapp.ActionRef{},
			RelatedRefs:      []projectionapp.RelatedRef{},
			EventType:        "view.entity_upserted.v1",
			Critical:         true,
			CommandID:        commandID,
			ClientCommandID:  clientCommandID,
			SourceRefs:       []projectionapp.SourceRef{},
			DeltaPayload:     []byte(fmt.Sprintf(`{"data":{"revision":%d}}`, sequence)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func clientCommandOwnerEvent(
	tenantID, eventID, streamID, aggregateType, aggregateID string,
	correlationID, idempotencyKey string,
	at time.Time,
) contractsv1.IntegrationEvent {
	event := homeOwnerEvent(tenantID, eventID, streamID, aggregateType, aggregateID, at)
	event.CorrelationID = correlationID
	event.ClientCommandID = idempotencyKey
	return event
}

func assertPublishedClientCommandUpdates(
	t *testing.T,
	pool *pgxpool.Pool,
	event contractsv1.IntegrationEvent,
	wantOwners []string,
) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
SELECT view_type,view_id,command_id,client_command_id
FROM projection_deltas
WHERE client_command_id=$1
ORDER BY view_type,view_id`, event.ClientCommandID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	gotOwners := make([]string, 0, len(wantOwners))
	for rows.Next() {
		var viewType, viewID, commandID, clientCommandID string
		if err := rows.Scan(&viewType, &viewID, &commandID, &clientCommandID); err != nil {
			t.Fatal(err)
		}
		if commandID != event.CorrelationID || clientCommandID != event.ClientCommandID {
			t.Fatalf(
				"%s:%s command identity=(%q,%q), want=(%q,%q)",
				viewType,
				viewID,
				commandID,
				clientCommandID,
				event.CorrelationID,
				event.ClientCommandID,
			)
		}
		gotOwners = append(gotOwners, fmt.Sprintf("%s:%s", viewType, viewID))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(wantOwners)
	if got, want := fmt.Sprint(gotOwners), fmt.Sprint(wantOwners); got != want {
		t.Fatalf("published owners=%s, want=%s", got, want)
	}
	var receiptCount int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FROM projection_command_receipts
WHERE client_command_id=$1 AND command_id=$2`,
		event.ClientCommandID, event.CorrelationID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != len(wantOwners) {
		t.Fatalf("receipt count=%d want=%d", receiptCount, len(wantOwners))
	}
}
