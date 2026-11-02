//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestOutboxDispatcherPublishesClaimedBatchAndMarksIt(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	seedOutboxEvent(t, pool, integrationEvent("event-2", "task/task-1", 2, now.Add(time.Second), `{"step":2}`))
	seedOutboxEvent(t, pool, integrationEvent("event-1", "task/task-1", 1, now, `{"step":1}`))
	publisher := &recordingPublisher{}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: publisher, LeaseOwner: "dispatcher-1",
		LeaseDuration: time.Minute, BatchSize: 10, Now: advancingClock(now.Add(time.Minute)),
	}

	published, err := dispatcher.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("published=%d, want 2", published)
	}
	got := publisher.Events()
	if len(got) != 2 || got[0].EventID != "event-1" || got[1].EventID != "event-2" {
		t.Fatalf("publish order=%v", eventIDs(got))
	}
	var marked, attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE published_at IS NOT NULL),sum(attempt_count) FROM outbox_events`).Scan(&marked, &attempts); err != nil {
		t.Fatal(err)
	}
	if marked != 2 || attempts != 2 {
		t.Fatalf("marked=%d attempts=%d", marked, attempts)
	}
}

func TestOutboxDispatcherReleasesFailedPublishForImmediateRetry(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 30, 0, 0, time.UTC)
	seedOutboxEvent(t, pool, integrationEvent("event-retry", "task/task-retry", 1, now, `{"status":"queued"}`))
	publisher := &recordingPublisher{failuresRemaining: 1}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: publisher, LeaseOwner: "dispatcher-retry",
		LeaseDuration: time.Minute, BatchSize: 1, Now: advancingClock(now.Add(time.Minute)),
	}

	if published, err := dispatcher.RunOnce(ctx); published != 0 || err == nil {
		t.Fatalf("first run published=%d error=%v", published, err)
	}
	var leaseOwner *string
	var lastError string
	if err := pool.QueryRow(ctx, `SELECT lease_owner,last_error_code FROM outbox_events WHERE event_id='event-retry'`).Scan(&leaseOwner, &lastError); err != nil {
		t.Fatal(err)
	}
	if leaseOwner != nil || lastError == "" {
		t.Fatalf("failed event lease=%v last_error=%q", leaseOwner, lastError)
	}
	if published, err := dispatcher.RunOnce(ctx); published != 1 || err != nil {
		t.Fatalf("retry published=%d error=%v", published, err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM outbox_events WHERE event_id='event-retry'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d, want 2", attempts)
	}
}

func TestOutboxDispatcherDoesNotOvertakeLeasedStreamHead(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 45, 0, 0, time.UTC)
	streamID := "conversation/conversation-ordered"
	seedOutboxEvent(t, pool, integrationEvent("event-head", streamID, 1, now, `{"step":1}`))
	seedOutboxEvent(t, pool, integrationEvent("event-next", streamID, 2, now.Add(time.Second), `{"step":2}`))
	blocking := &blockingPublisher{
		started: make(chan contractsv1.IntegrationEvent, 1),
		release: make(chan struct{}),
	}
	first := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: blocking, LeaseOwner: "dispatcher-head",
		LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return now.Add(10 * time.Second) },
	}
	secondPublisher := &recordingPublisher{}
	second := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: secondPublisher, LeaseOwner: "dispatcher-next",
		LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return now.Add(10 * time.Second) },
	}
	type result struct {
		count int
		err   error
	}
	firstResult := make(chan result, 1)
	go func() {
		count, err := first.RunOnce(ctx)
		firstResult <- result{count: count, err: err}
	}()
	if event := <-blocking.started; event.EventID != "event-head" {
		t.Fatalf("first dispatcher claimed %s", event.EventID)
	}
	if count, err := second.RunOnce(ctx); err != nil || count != 0 {
		t.Fatalf("second dispatcher overtook leased head: count=%d error=%v", count, err)
	}
	close(blocking.release)
	if completed := <-firstResult; completed.err != nil || completed.count != 1 {
		t.Fatalf("first dispatcher completion: count=%d error=%v", completed.count, completed.err)
	}
	if count, err := second.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("second dispatcher did not publish successor: count=%d error=%v", count, err)
	}
	if events := secondPublisher.Events(); len(events) != 1 || events[0].EventID != "event-next" {
		t.Fatalf("successor publish order = %v", eventIDs(events))
	}
}

func TestOutboxDispatcherPublishesMixedIntegrationEventVersionsToCurrentConsumer(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 50, 0, 0, time.UTC)
	legacy := integrationEvent("event-v1", "task/task-v1", 1, now, `{}`)
	legacy.SchemaVersion = contractsv1.IntegrationEventLegacySchemaVersion
	legacy.AggregateID = "task-v1"
	current := integrationEvent("event-v2", "task/task-v2", 1, now.Add(time.Second), `{}`)
	current.AggregateID = "task-v2"
	seedOutboxEvent(t, pool, legacy)
	seedOutboxEvent(t, pool, current)
	publisher := &schemaRecordingPublisher{
		recordingPublisher: recordingPublisher{},
		supportedVersions: []string{
			contractsv1.IntegrationEventLegacySchemaVersion,
			contractsv1.IntegrationEventSchemaVersion,
		},
	}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: publisher, LeaseOwner: "dispatcher-current",
		LeaseDuration: time.Minute, BatchSize: 2, Now: advancingClock(now.Add(time.Minute)),
	}

	published, err := dispatcher.RunOnce(ctx)
	if err != nil || published != 2 {
		t.Fatalf("mixed-version publish count=%d error=%v", published, err)
	}
	if got := eventIDs(publisher.Events()); len(got) != 2 || got[0] != "event-v1" || got[1] != "event-v2" {
		t.Fatalf("published events=%v", got)
	}
}

func TestOutboxDispatcherFailsClosedBeforePublishingV2ToLegacyConsumer(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 55, 0, 0, time.UTC)
	seedOutboxEvent(t, pool, integrationEvent("event-v2-legacy-consumer", "task/task-v2-legacy", 1, now, `{}`))
	publisher := &schemaRecordingPublisher{
		recordingPublisher: recordingPublisher{},
		supportedVersions:  []string{contractsv1.IntegrationEventLegacySchemaVersion},
	}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: publisher, LeaseOwner: "dispatcher-legacy",
		LeaseDuration: time.Minute, BatchSize: 1, Now: advancingClock(now.Add(time.Minute)),
	}

	published, err := dispatcher.RunOnce(ctx)
	if err == nil || published != 0 {
		t.Fatalf("legacy consumer publish count=%d error=%v", published, err)
	}
	if got := publisher.Events(); len(got) != 0 {
		t.Fatalf("legacy consumer received v2 events=%v", eventIDs(got))
	}
	var marked bool
	var leaseOwner *string
	var lastError string
	if err := pool.QueryRow(ctx, `
SELECT published_at IS NOT NULL,lease_owner,last_error_code
FROM outbox_events WHERE event_id='event-v2-legacy-consumer'`).Scan(&marked, &leaseOwner, &lastError); err != nil {
		t.Fatal(err)
	}
	if marked || leaseOwner != nil || lastError == "" {
		t.Fatalf("v2 event marked=%t lease=%v last_error=%q", marked, leaseOwner, lastError)
	}
}

func TestOutboxPersistenceRejectsV1ClientCommandIdentity(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 4, 57, 0, 0, time.UTC)
	legacy := integrationEvent("event-v1-no-client", "task/task-v1-no-client", 1, now, `{}`)
	legacy.SchemaVersion = contractsv1.IntegrationEventLegacySchemaVersion
	seedOutboxEvent(t, pool, legacy)

	if _, err := pool.Exec(ctx, `
UPDATE outbox_events SET client_command_id='v2-only-client-command'
WHERE event_id='event-v1-no-client'`); err == nil {
		t.Fatal("durable event.v1 accepted event.v2-only client command identity")
	}
}

func TestConsumerReceiptsDeduplicateSemanticEventIdentity(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE projection_test (event_id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC)
	receipts := platformpostgres.ConsumerReceipts{Pool: pool, Now: func() time.Time { return now }}
	event := integrationEvent("event-consume", "task/task-consume", 1, now, `{"a":1,"b":2}`)
	mutation := func(ctx context.Context, tx platformpostgres.Tx, event contractsv1.IntegrationEvent) error {
		_, err := tx.Exec(ctx, `INSERT INTO projection_test (event_id) VALUES ($1)`, event.EventID)
		return err
	}

	applied, err := receipts.Apply(ctx, "worker-detail-projector", event, mutation)
	if err != nil || !applied {
		t.Fatalf("first apply=%t error=%v", applied, err)
	}
	event.Payload = []byte(`{"b":2,"a":1}`)
	applied, err = receipts.Apply(ctx, "worker-detail-projector", event, mutation)
	if err != nil || applied {
		t.Fatalf("duplicate apply=%t error=%v", applied, err)
	}
	assertTableCount(t, pool, "projection_test", 1)
	assertTableCount(t, pool, "consumer_receipts", 1)
}

func TestConsumerReceiptsRejectGapBeforeMutation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 5, 30, 0, 0, time.UTC)
	receipts := platformpostgres.ConsumerReceipts{Pool: pool, Now: func() time.Time { return now }}
	mutationCalled := false
	applied, err := receipts.Apply(ctx, "allocator", integrationEvent("event-gap", "task/task-gap", 2, now, `{}`),
		func(context.Context, platformpostgres.Tx, contractsv1.IntegrationEvent) error {
			mutationCalled = true
			return nil
		})
	if applied || !errors.Is(err, platformpostgres.ErrConsumerSequenceGap) {
		t.Fatalf("applied=%t error=%v", applied, err)
	}
	if mutationCalled {
		t.Fatal("gap event invoked consumer mutation")
	}
	assertTableCount(t, pool, "consumer_receipts", 0)
}

func TestConsumerReceiptsRejectReusedIdentityOrStreamPosition(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 6, 0, 0, 0, time.UTC)
	receipts := platformpostgres.ConsumerReceipts{Pool: pool, Now: func() time.Time { return now }}
	mutation := func(context.Context, platformpostgres.Tx, contractsv1.IntegrationEvent) error { return nil }
	original := integrationEvent("event-integrity", "task/task-integrity", 1, now, `{"value":1}`)
	if applied, err := receipts.Apply(ctx, "projector", original, mutation); err != nil || !applied {
		t.Fatalf("seed apply=%t error=%v", applied, err)
	}

	reusedID := original
	reusedID.Payload = []byte(`{"value":2}`)
	if applied, err := receipts.Apply(ctx, "projector", reusedID, mutation); applied || !errors.Is(err, platformpostgres.ErrConsumerStreamConflict) {
		t.Fatalf("reused id apply=%t error=%v", applied, err)
	}
	reusedPosition := original
	reusedPosition.EventID = "event-other"
	if applied, err := receipts.Apply(ctx, "projector", reusedPosition, mutation); applied || !errors.Is(err, platformpostgres.ErrConsumerStreamConflict) {
		t.Fatalf("reused position apply=%t error=%v", applied, err)
	}
	assertTableCount(t, pool, "consumer_receipts", 1)
}

func integrationEvent(eventID, streamID string, sequence uint64, occurredAt time.Time, payload string) contractsv1.IntegrationEvent {
	return contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       eventID, EventType: "TestEvent.v1", ProducerContext: "test",
		StreamID: streamID, Sequence: sequence, TenantID: "tenant-local",
		AggregateType: "Task", AggregateID: "task-test", TraceID: "trace-test",
		CorrelationID: "correlation-test", CausationID: "causation-test",
		IdempotencyKey: eventID, Visibility: "internal", OccurredAt: occurredAt,
		Payload: []byte(payload),
	}
}

func seedOutboxEvent(t *testing.T, pool *pgxpool.Pool, event contractsv1.IntegrationEvent) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO owner_stream_positions (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ($1,$2,$3,$4,$5,1,$6)
ON CONFLICT (stream_id) DO UPDATE SET next_sequence=GREATEST(owner_stream_positions.next_sequence,EXCLUDED.next_sequence)`,
		event.StreamID, event.ProducerContext, event.AggregateType, event.AggregateID, event.Sequence+1, event.OccurredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
		event.TraceID, event.CorrelationID, event.CausationID, event.IdempotencyKey,
		event.Visibility, event.Payload, event.OccurredAt); err != nil {
		t.Fatal(err)
	}
}

type recordingPublisher struct {
	mu                sync.Mutex
	events            []contractsv1.IntegrationEvent
	failuresRemaining int
}

type schemaRecordingPublisher struct {
	recordingPublisher
	supportedVersions []string
}

func (publisher *schemaRecordingPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return append([]string(nil), publisher.supportedVersions...)
}

type blockingPublisher struct {
	started chan contractsv1.IntegrationEvent
	release chan struct{}
}

func (publisher *blockingPublisher) Publish(_ context.Context, event contractsv1.IntegrationEvent) error {
	publisher.started <- event
	<-publisher.release
	return nil
}

func (*blockingPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return []string{
		contractsv1.IntegrationEventLegacySchemaVersion,
		contractsv1.IntegrationEventSchemaVersion,
	}
}

func (publisher *recordingPublisher) Publish(_ context.Context, event contractsv1.IntegrationEvent) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.failuresRemaining > 0 {
		publisher.failuresRemaining--
		return fmt.Errorf("test publisher unavailable")
	}
	publisher.events = append(publisher.events, event)
	return nil
}

func (*recordingPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return []string{
		contractsv1.IntegrationEventLegacySchemaVersion,
		contractsv1.IntegrationEventSchemaVersion,
	}
}

func (publisher *recordingPublisher) Events() []contractsv1.IntegrationEvent {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return append([]contractsv1.IntegrationEvent(nil), publisher.events...)
}

func eventIDs(events []contractsv1.IntegrationEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.EventID)
	}
	return ids
}

func advancingClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	now := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Millisecond)
		return now
	}
}

func assertTableCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}
