package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	TaskIndexViewID     = "index"
	HomeDashboardViewID = "index"

	// These identities are reserved for Materializer-owned Home projections.
	// Destructive migration code must also prove the exact Home projection
	// result; membership in this namespace alone is never ownership evidence.
	homeDashboardBootstrapEventID      = "local-home-dashboard-bootstrap-v1"
	homeDashboardBootstrapStreamID     = "local-home-dashboard-bootstrap"
	homeDashboardRefreshSourceIDPrefix = "local-home-dashboard-refresh/"
	homeDashboardUpgradeSourceIDPrefix = "local-home-dashboard-contract-upgrade-v2/"
	workerDetailUpgradeSourceIDPrefix  = "local-worker-detail-contract-upgrade-v2/"

	homeDashboardContractV1 = 1
	homeDashboardContractV2 = 2
)

// Materializer is the P0 in-process EventCenter consumer. It projects durable
// owner events into page-shaped Frontier views before Outbox publication is
// acknowledged.
type Materializer struct {
	Pool      *pgxpool.Pool
	Projector projectionapp.Projector
	Views     *projectionapp.ViewContinuityReader
	Objects   workerapp.ObjectStore
	UserID    string
	Now       func() time.Time

	homeRefreshObserved     func()
	homeRefreshLockAttempt  func(uint32, string)
	homeRefreshLockAcquired func(uint32, string) error
}

// Bootstrap creates the empty overview Views deterministically. It is safe to
// call after every Backend restart and never advances either View twice.
func (m Materializer) Bootstrap(ctx context.Context, tenantID string) error {
	if err := m.validateBootstrapDependencies(tenantID); err != nil {
		return err
	}
	if _, err := NewStore(m.Pool).ReconcileTerminalDisplayHistory(ctx, tenantID, m.UserID); err != nil {
		return err
	}
	if err := m.bootstrapTaskIndex(ctx, tenantID); err != nil {
		return err
	}
	if err := m.bootstrapHomeDashboard(ctx, tenantID); err != nil {
		return err
	}
	if err := m.upgradeHomeDashboardContract(ctx, tenantID); err != nil {
		return err
	}
	return m.upgradeWorkerDetailContracts(ctx, tenantID)
}

func (m Materializer) validateBootstrapDependencies(tenantID string) error {
	if m.Pool == nil || m.Projector.Store == nil || m.Views == nil || m.UserID == "" || m.Now == nil || tenantID == "" {
		return fmt.Errorf("projection materializer dependencies are required")
	}
	return nil
}

func (m Materializer) bootstrapTaskIndex(ctx context.Context, tenantID string) error {
	if _, err := m.Views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: tenantID, UserID: m.UserID},
		ViewType: projectionapp.ViewTaskSummary,
		ViewID:   TaskIndexViewID,
	}); err == nil {
		return nil
	} else if !errors.Is(err, projectionapp.ErrViewUnavailable) {
		return err
	}
	data, err := m.taskIndex(ctx, tenantID)
	if err != nil {
		return err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       "local-task-index-bootstrap-v1", EventType: "LocalTaskIndexBootstrap.v1",
		ProducerContext: "eventing-projection", StreamID: "local-task-index-bootstrap",
		Sequence: 1, TenantID: tenantID, AggregateType: "TaskIndex", AggregateID: TaskIndexViewID,
		TraceID: "local-task-index-bootstrap", CorrelationID: "local-product-startup",
		CausationID: "local-product-startup", IdempotencyKey: "local-task-index-bootstrap-v1",
		Visibility: "internal", OccurredAt: time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		Payload: json.RawMessage(`{"bootstrap":true}`),
	}
	_, err = m.Projector.Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID: event.EventID, SourceStreamID: event.StreamID, SourceSequence: event.Sequence,
		ProjectedAt: event.OccurredAt,
		Updates:     []projectionapp.ViewUpdate{m.update(event, projectionapp.ViewTaskSummary, TaskIndexViewID, data, nil, nil)},
	})
	return err
}

func (m Materializer) bootstrapHomeDashboard(ctx context.Context, tenantID string) error {
	if _, err := m.Views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: tenantID, UserID: m.UserID},
		ViewType: projectionapp.ViewHomeDashboard,
		ViewID:   HomeDashboardViewID,
	}); err == nil {
		return nil
	} else if !errors.Is(err, projectionapp.ErrViewUnavailable) {
		return err
	}
	bootstrapAt := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	// The v1 bootstrap event is immutable compatibility history. Even after the
	// current Home contract gained additive delivery fields, its projection
	// event must retain the exact pre-upgrade serialized shape and digest.
	data, refs, err := m.homeDashboardV1At(ctx, m.Pool, tenantID, bootstrapAt)
	if err != nil {
		return err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       homeDashboardBootstrapEventID, EventType: "LocalHomeDashboardBootstrap.v1",
		ProducerContext: "eventing-projection", StreamID: homeDashboardBootstrapStreamID,
		Sequence: 1, TenantID: tenantID, AggregateType: "HomeDashboard", AggregateID: HomeDashboardViewID,
		TraceID: homeDashboardBootstrapStreamID, CorrelationID: "local-product-startup",
		CausationID: "local-product-startup", IdempotencyKey: "local-home-dashboard-bootstrap-v1",
		Visibility: "internal", OccurredAt: bootstrapAt,
		Payload: json.RawMessage(`{"bootstrap":true}`),
	}
	_, err = m.Projector.Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID: event.EventID, SourceStreamID: event.StreamID, SourceSequence: event.Sequence,
		ProjectedAt: event.OccurredAt,
		Updates: []projectionapp.ViewUpdate{
			m.update(event, projectionapp.ViewHomeDashboard, HomeDashboardViewID, data, nil, refs),
		},
	})
	return err
}

func (m Materializer) upgradeHomeDashboardContract(
	ctx context.Context,
	tenantID string,
) (resultErr error) {
	connection, err := m.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire Home contract upgrade connection: %w", err)
	}
	lockKey := homeDashboardViewerLockKey(tenantID, m.UserID)
	if _, err := connection.Exec(ctx, `
SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return errors.Join(
			fmt.Errorf("lock Home contract upgrade: %w", err),
			discardHomeRefreshConnection(connection),
		)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return errors.Join(
			fmt.Errorf("begin Home contract upgrade snapshot: %w", err),
			releaseHomeRefreshLock(connection, lockKey),
		)
	}
	defer func() {
		if cleanupErr := cleanupHomeRefreshTransaction(
			connection,
			tx,
			lockKey,
		); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()

	needsUpgrade, epoch, cursor, err := legacyHomeDashboardContract(
		ctx, tx, tenantID, m.UserID,
	)
	if err != nil || !needsUpgrade {
		return err
	}
	projectedAt := m.Now().UTC()
	data, refs, err := m.homeDashboardAt(ctx, tx, tenantID, projectedAt)
	if err != nil {
		return err
	}
	sourceID := fmt.Sprintf(
		"%s%d/%d", homeDashboardUpgradeSourceIDPrefix, epoch, cursor,
	)
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       sourceID, EventType: "LocalHomeDashboardContractUpgraded.v2",
		ProducerContext: "eventing-projection", StreamID: sourceID, Sequence: 1,
		TenantID: tenantID, AggregateType: "HomeDashboard", AggregateID: HomeDashboardViewID,
		TraceID: sourceID, CorrelationID: "local-product-startup",
		CausationID: "local-product-startup", IdempotencyKey: sourceID,
		Visibility: "internal", OccurredAt: projectedAt,
		Payload: json.RawMessage(`{"from_contract_version":1,"to_contract_version":2}`),
	}
	update := m.update(
		event, projectionapp.ViewHomeDashboard, HomeDashboardViewID, data, nil, refs,
	)
	projectionEvent := projectionapp.ProjectionEvent{
		SourceEventID: sourceID, SourceStreamID: sourceID, SourceSequence: 1,
		ProjectedAt: projectedAt, Updates: []projectionapp.ViewUpdate{update},
	}
	if err := projectionapp.ValidateProjectionEvent(projectionEvent); err != nil {
		return err
	}
	digest, err := projectionapp.ProjectionEventDigest(projectionEvent)
	if err != nil {
		return err
	}
	if _, err := NewStore(m.Pool).applyProjectionEventTx(
		ctx, tx, projectionEvent, digest, "",
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Home contract upgrade snapshot: %w", err)
	}
	return nil
}

func legacyHomeDashboardContract(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, userID string,
) (bool, uint64, uint64, error) {
	var data json.RawMessage
	var epoch, cursor uint64
	if err := tx.QueryRow(ctx, `
SELECT data,view_epoch,view_cursor
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type='home_dashboard' AND view_id=$3
FOR UPDATE`, tenantID, userID, HomeDashboardViewID).Scan(
		&data, &epoch, &cursor,
	); err != nil {
		return false, 0, 0, fmt.Errorf("lock Home contract projection: %w", err)
	}
	var dashboard struct {
		RecentDeliveries []map[string]json.RawMessage `json:"recent_deliveries"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return false, 0, 0, fmt.Errorf("decode Home contract projection: %w", err)
	}
	for _, delivery := range dashboard.RecentDeliveries {
		if _, ok := delivery["acceptance_mode"]; !ok {
			return true, epoch, cursor, nil
		}
	}
	return false, epoch, cursor, nil
}

func (m Materializer) upgradeWorkerDetailContracts(
	ctx context.Context,
	tenantID string,
) error {
	rows, err := m.Pool.Query(ctx, `
SELECT view_id
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type='worker_detail' AND data ? 'timeline'
ORDER BY view_id`, tenantID, m.UserID)
	if err != nil {
		return fmt.Errorf("query legacy WorkerDetail contracts: %w", err)
	}
	sessionIDs := make([]string, 0)
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy WorkerDetail contract: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate legacy WorkerDetail contracts: %w", err)
	}
	rows.Close()
	for _, sessionID := range sessionIDs {
		if err := m.upgradeWorkerDetailContract(ctx, tenantID, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (m Materializer) upgradeWorkerDetailContract(
	ctx context.Context,
	tenantID, sessionID string,
) error {
	return platformpostgres.InTransaction(ctx, m.Pool, func(tx platformpostgres.Tx) error {
		var (
			data             json.RawMessage
			actions          []projectionapp.ActionRef
			refs             []projectionapp.RelatedRef
			queryFingerprint string
			epoch            uint64
			cursor           uint64
		)
		err := tx.QueryRow(ctx, `
SELECT data,available_actions,related_refs,query_fingerprint,view_epoch,view_cursor
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type='worker_detail' AND view_id=$3
FOR UPDATE`, tenantID, m.UserID, sessionID).Scan(
			&data, &actions, &refs, &queryFingerprint, &epoch, &cursor,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock WorkerDetail contract projection %q: %w", sessionID, err)
		}
		var contract map[string]json.RawMessage
		if err := json.Unmarshal(data, &contract); err != nil {
			return fmt.Errorf("decode WorkerDetail contract projection %q: %w", sessionID, err)
		}
		if _, legacy := contract["timeline"]; !legacy {
			return nil
		}
		delete(contract, "timeline")
		upgradedData, err := json.Marshal(contract)
		if err != nil {
			return fmt.Errorf("encode WorkerDetail contract projection %q: %w", sessionID, err)
		}
		projectedAt := m.Now().UTC()
		sourceID := fmt.Sprintf(
			"%s%s/%d/%d", workerDetailUpgradeSourceIDPrefix, sessionID, epoch, cursor,
		)
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       sourceID, EventType: "LocalWorkerDetailContractUpgraded.v2",
			ProducerContext: "eventing-projection", StreamID: sourceID, Sequence: 1,
			TenantID: tenantID, AggregateType: "WorkerSession", AggregateID: sessionID,
			TraceID: sourceID, CorrelationID: "local-product-startup",
			CausationID: "local-product-startup", IdempotencyKey: sourceID,
			Visibility: "internal", OccurredAt: projectedAt,
			Payload: json.RawMessage(`{"removed_field":"timeline","authoritative_history":"timeline_groups"}`),
		}
		update := m.update(
			event, projectionapp.ViewWorkerDetail, sessionID, upgradedData, actions, refs,
		)
		update.QueryFingerprint = queryFingerprint
		projectionEvent := projectionapp.ProjectionEvent{
			SourceEventID: sourceID, SourceStreamID: sourceID, SourceSequence: 1,
			ProjectedAt: projectedAt, Updates: []projectionapp.ViewUpdate{update},
		}
		if err := projectionapp.ValidateProjectionEvent(projectionEvent); err != nil {
			return err
		}
		digest, err := projectionapp.ProjectionEventDigest(projectionEvent)
		if err != nil {
			return err
		}
		_, err = NewStore(m.Pool).applyProjectionEventTx(
			ctx, tx, projectionEvent, digest, "",
		)
		return err
	})
}

func (m Materializer) Publish(ctx context.Context, event contractsv1.IntegrationEvent) error {
	if m.Pool == nil || m.Projector.Store == nil || m.UserID == "" || m.Now == nil {
		return fmt.Errorf("projection materializer dependencies are required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	digestEvent := event
	digestEvent.OccurredAt = digestEvent.OccurredAt.UTC()
	sourceDigest, err := platformpostgres.IntegrationEventDigest(digestEvent)
	if err != nil {
		return err
	}
	receipts := NewStore(m.Pool)
	receiptStatus, err := receipts.resolveSourceEventReceipt(ctx, event, sourceDigest)
	if err != nil {
		return err
	}
	if receiptStatus == sourceEventReceiptLegacyTerminal {
		return nil
	}
	if receiptStatus == sourceEventReceiptMissing {
		updates, err := m.buildUpdates(ctx, event)
		if err != nil {
			return err
		}
		_, err = m.Projector.ExecuteWithSourceDigest(ctx, projectionapp.ProjectionEvent{
			SourceEventID: event.EventID, SourceStreamID: event.StreamID,
			SourceSequence: event.Sequence, ProjectedAt: m.Now().UTC(), Updates: updates,
		}, sourceDigest)
		if err != nil {
			receiptStatus, receiptErr := receipts.resolveSourceEventReceipt(ctx, event, sourceDigest)
			if receiptErr != nil {
				return receiptErr
			}
			switch receiptStatus {
			case sourceEventReceiptMissing:
				return err
			case sourceEventReceiptLegacyTerminal:
				return nil
			}
		}
	}
	if event.AggregateType == "Conversation" {
		return nil
	}
	return m.refreshHomeDashboard(ctx, event, sourceDigest)
}

func (Materializer) SupportedIntegrationEventSchemaVersions() []string {
	return []string{
		contractsv1.IntegrationEventLegacySchemaVersion,
		contractsv1.IntegrationEventSchemaVersion,
	}
}

func (m Materializer) refreshHomeDashboard(
	ctx context.Context,
	event contractsv1.IntegrationEvent,
	sourceDigest string,
) (resultErr error) {
	refreshID := homeDashboardRefreshSourceIDPrefix + event.EventID
	refreshSource := contractsv1.IntegrationEvent{
		EventID: refreshID, StreamID: refreshID, Sequence: 1,
	}
	store := NewStore(m.Pool)
	receiptStatus, err := store.resolveSourceEventReceipt(ctx, refreshSource, sourceDigest)
	if err != nil {
		return err
	}
	if receiptStatus != sourceEventReceiptMissing {
		return nil
	}

	connection, err := m.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire Home refresh connection: %w", err)
	}
	lockKey := homeDashboardViewerLockKey(event.TenantID, m.UserID)
	if m.homeRefreshLockAttempt != nil {
		m.homeRefreshLockAttempt(connection.Conn().PgConn().PID(), lockKey)
	}
	if _, err := connection.Exec(ctx, `
SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return errors.Join(
			fmt.Errorf("lock Home refresh: %w", err),
			discardHomeRefreshConnection(connection),
		)
	}
	if m.homeRefreshLockAcquired != nil {
		if err := m.homeRefreshLockAcquired(
			connection.Conn().PgConn().PID(),
			lockKey,
		); err != nil {
			return errors.Join(
				fmt.Errorf("finish Home refresh lock acquisition: %w", err),
				discardHomeRefreshConnection(connection),
			)
		}
	}

	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return errors.Join(
			fmt.Errorf("begin Home refresh snapshot: %w", err),
			releaseHomeRefreshLock(connection, lockKey),
		)
	}
	defer func() {
		if cleanupErr := cleanupHomeRefreshTransaction(
			connection,
			tx,
			lockKey,
		); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	duplicate, err := checkSourceEventReceiptTx(ctx, tx, refreshSource, sourceDigest)
	if err != nil {
		return err
	}
	if duplicate {
		return nil
	}

	projectedAt := m.Now().UTC()
	data, refs, err := m.homeDashboardAt(ctx, tx, event.TenantID, projectedAt)
	if err != nil {
		return err
	}
	update := m.update(
		event, projectionapp.ViewHomeDashboard, HomeDashboardViewID, data, nil, refs,
	)
	update.DeltaID = refreshID + "-home-dashboard-index"
	projectionEvent := projectionapp.ProjectionEvent{
		SourceEventID: refreshID, SourceStreamID: refreshID, SourceSequence: 1,
		ProjectedAt: projectedAt, Updates: []projectionapp.ViewUpdate{update},
	}
	if err := projectionapp.ValidateProjectionEvent(projectionEvent); err != nil {
		return err
	}
	projectionDigest, err := projectionapp.ProjectionEventDigest(projectionEvent)
	if err != nil {
		return err
	}
	if _, err := store.applyProjectionEventTx(
		ctx, tx, projectionEvent, projectionDigest, sourceDigest,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Home refresh snapshot: %w", err)
	}
	return nil
}

func homeDashboardViewerLockKey(tenantID, userID string) string {
	return fmt.Sprintf(
		"home_dashboard:%d:%s:%d:%s",
		len(tenantID), tenantID, len(userID), userID,
	)
}

func cleanupHomeRefreshTransaction(
	connection *pgxpool.Conn,
	tx pgx.Tx,
	lockKey string,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	rollbackErr := tx.Rollback(rollbackCtx)
	cancel()
	if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
		return errors.Join(
			fmt.Errorf("rollback Home refresh snapshot: %w", rollbackErr),
			discardHomeRefreshConnection(connection),
		)
	}
	return releaseHomeRefreshLock(connection, lockKey)
}

func releaseHomeRefreshLock(connection *pgxpool.Conn, lockKey string) error {
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var unlocked bool
	err := connection.QueryRow(unlockCtx, `
SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey).Scan(&unlocked)
	cancel()
	if err == nil && unlocked {
		connection.Release()
		return nil
	}
	discardErr := discardHomeRefreshConnection(connection)
	if err != nil {
		return errors.Join(fmt.Errorf("unlock Home refresh: %w", err), discardErr)
	}
	return errors.Join(fmt.Errorf("unlock Home refresh: lock was not held"), discardErr)
}

func discardHomeRefreshConnection(connection *pgxpool.Conn) error {
	rawConnection := connection.Hijack()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rawConnection.Close(closeCtx); err != nil {
		return fmt.Errorf("discard Home refresh connection: %w", err)
	}
	return nil
}

func (m Materializer) PublishDisplayCheckpoint(
	ctx context.Context,
	checkpoint projectionapp.DisplayCheckpoint,
) error {
	if m.Pool == nil || m.Projector.Store == nil || m.UserID == "" || m.Now == nil ||
		checkpoint.UserID != m.UserID {
		return fmt.Errorf("DisplayCheckpoint materializer dependencies are required")
	}
	if err := projectionapp.ValidateDisplayCheckpoint(checkpoint); err != nil {
		return err
	}
	digest, err := projectionapp.DisplayCheckpointDigest(checkpoint)
	if err != nil {
		return err
	}
	sourceEventID := fmt.Sprintf(
		"display-checkpoint-%s-%d", checkpoint.DisplayStreamID, checkpoint.CheckpointVersion,
	)
	if err := NewStore(m.Pool).SaveDisplayCheckpoint(
		ctx, checkpoint, digest, sourceEventID,
	); err != nil {
		return err
	}
	data, actions, refs, _, err := m.workerDetail(
		ctx, checkpoint.TenantID, checkpoint.WorkerSessionID,
	)
	if err != nil {
		return err
	}
	source := contractsv1.IntegrationEvent{
		EventID: sourceEventID, TenantID: checkpoint.TenantID,
		AggregateType: "WorkerSession", AggregateID: checkpoint.WorkerSessionID,
		CorrelationID: sourceEventID,
	}
	update := m.update(
		source, projectionapp.ViewWorkerDetail, checkpoint.WorkerSessionID, data, actions, refs,
	)
	update.EventType = "view.stream_checkpointed.v1"
	_, err = m.Projector.Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID:  sourceEventID,
		SourceStreamID: "display/" + checkpoint.DisplayStreamID,
		SourceSequence: checkpoint.CheckpointVersion,
		ProjectedAt:    checkpoint.CheckpointedAt,
		Updates:        []projectionapp.ViewUpdate{update},
	})
	return err
}

func (m Materializer) buildUpdates(ctx context.Context, event contractsv1.IntegrationEvent) ([]projectionapp.ViewUpdate, error) {
	if _, err := NewStore(m.Pool).ReconcileTerminalDisplayHistory(ctx, event.TenantID, m.UserID); err != nil {
		return nil, err
	}
	updates := make([]projectionapp.ViewUpdate, 0)
	if event.AggregateType != "Conversation" {
		indexData, err := m.taskIndex(ctx, event.TenantID)
		if err != nil {
			return nil, err
		}
		updates = append(updates, m.update(
			event, projectionapp.ViewTaskSummary, TaskIndexViewID, indexData, nil, nil,
		))
	}

	taskIDs := make(map[string]struct{})
	sessionIDs := make(map[string]struct{})
	conversationIDs := make(map[string]struct{})
	if event.AggregateType == "Conversation" {
		conversationIDs[event.AggregateID] = struct{}{}
	}
	if event.AggregateType == "Task" {
		taskIDs[event.AggregateID] = struct{}{}
	}
	if event.AggregateType == "WorkerSession" {
		sessionIDs[event.AggregateID] = struct{}{}
		if taskID, err := m.sessionTaskID(ctx, event.TenantID, event.AggregateID); err != nil {
			return nil, err
		} else if taskID != "" {
			taskIDs[taskID] = struct{}{}
		}
	}
	for taskID := range taskIDs {
		if sessionID, err := m.taskSessionID(ctx, event.TenantID, taskID); err != nil {
			return nil, err
		} else if sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
		if conversationID, err := m.taskConversationID(ctx, event.TenantID, taskID); err != nil {
			return nil, err
		} else if conversationID != "" {
			conversationIDs[conversationID] = struct{}{}
		}
	}

	for taskID := range taskIDs {
		data, actions, refs, err := m.taskSummary(ctx, event.TenantID, taskID)
		if err != nil {
			return nil, err
		}
		updates = append(updates, m.update(event, projectionapp.ViewTaskSummary, taskID, data, actions, refs))
	}
	for sessionID := range sessionIDs {
		data, actions, refs, artifacts, err := m.workerDetail(ctx, event.TenantID, sessionID)
		if err != nil {
			return nil, err
		}
		updates = append(updates, m.update(event, projectionapp.ViewWorkerDetail, sessionID, data, actions, refs))
		for _, artifact := range artifacts {
			artifactData, artifactActions, err := m.artifactDetail(ctx, event.TenantID, artifact)
			if err != nil {
				return nil, err
			}
			update := m.update(event, projectionapp.ViewArtifactDetail, artifact.ArtifactID, artifactData, artifactActions, nil)
			update.EventType = "view.artifact_ready.v1"
			updates = append(updates, update)
		}
	}
	for conversationID := range conversationIDs {
		data, actions, refs, err := m.assistantConversation(ctx, event.TenantID, conversationID)
		if err != nil {
			return nil, err
		}
		updates = append(updates, m.update(
			event, projectionapp.ViewAssistantConversation, conversationID, data, actions, refs,
		))
	}
	return updates, nil
}

func (m Materializer) taskConversationID(ctx context.Context, tenantID, taskID string) (string, error) {
	var conversationID string
	err := m.Pool.QueryRow(ctx, `
SELECT binding.conversation_id
FROM tasks task
JOIN assistant_creation_intent_bindings binding ON binding.intent_id=task.creation_intent_id
WHERE task.tenant_id=$1 AND task.task_id=$2`, tenantID, taskID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return conversationID, err
}

func (m Materializer) assistantConversation(
	ctx context.Context,
	tenantID, conversationID string,
) (json.RawMessage, []projectionapp.ActionRef, []projectionapp.RelatedRef, error) {
	var status, workspaceID, activeRunID, draftID, readinessID, checkpointID string
	var revision, generation uint64
	var updatedAt time.Time
	err := m.Pool.QueryRow(ctx, `
SELECT status,revision,generation,COALESCE(workspace_id,''),COALESCE(active_assistant_run_id,''),
       COALESCE(current_task_draft_id,''),COALESCE(current_readiness_assessment_id,''),
       COALESCE(current_checkpoint_id,''),updated_at
FROM assistant_conversations
WHERE conversation_id=$1 AND tenant_id=$2 AND actor_user_id=$3`,
		conversationID, tenantID, m.UserID).Scan(
		&status, &revision, &generation, &workspaceID, &activeRunID,
		&draftID, &readinessID, &checkpointID, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read Assistant Conversation: %w", err)
	}
	rows, err := m.Pool.Query(ctx, `
SELECT turn_id,turn_sequence,role,content,created_at
FROM assistant_conversation_turns
WHERE conversation_id=$1 AND visible=true ORDER BY turn_sequence`, conversationID)
	if err != nil {
		return nil, nil, nil, err
	}
	turns := make([]map[string]any, 0)
	for rows.Next() {
		var turnID, role, content string
		var sequence uint64
		var createdAt time.Time
		if err := rows.Scan(&turnID, &sequence, &role, &content, &createdAt); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		turns = append(turns, map[string]any{
			"turn_id": turnID, "sequence": sequence, "role": role,
			"content": content, "created_at": createdAt.UTC().Format(time.RFC3339),
		})
	}
	rows.Close()
	var run any
	if activeRunID != "" {
		var runStatus, errorCode string
		var runRevision uint64
		var retryable bool
		if err := m.Pool.QueryRow(ctx, `
SELECT status,revision,COALESCE(error_code,''),retryable
FROM assistant_runs WHERE assistant_run_id=$1`, activeRunID).Scan(
			&runStatus, &runRevision, &errorCode, &retryable,
		); err == nil {
			run = map[string]any{
				"assistant_run_id": activeRunID, "status": runStatus, "revision": runRevision,
				"error_code": emptyOmit(errorCode), "retryable": retryable,
			}
		}
	} else {
		var runID, runStatus, errorCode string
		var runRevision uint64
		var retryable bool
		err := m.Pool.QueryRow(ctx, `
SELECT assistant_run_id,status,revision,COALESCE(error_code,''),retryable
FROM assistant_runs WHERE conversation_id=$1 ORDER BY run_sequence DESC LIMIT 1`, conversationID).Scan(
			&runID, &runStatus, &runRevision, &errorCode, &retryable,
		)
		if err == nil {
			run = map[string]any{
				"assistant_run_id": runID, "status": runStatus, "revision": runRevision,
				"error_code": emptyOmit(errorCode), "retryable": retryable,
			}
			activeRunID = runID
		}
	}
	var readiness any
	var readinessStatus string
	if readinessID != "" {
		var assessmentRevision, questionCount uint64
		var dimensions, blockers, assumptions json.RawMessage
		if err := m.Pool.QueryRow(ctx, `
SELECT assessment_revision,status,dimensions,blocking_unknowns,assumptions,clarification_question_count
FROM assistant_readiness_assessments WHERE assessment_id=$1`, readinessID).Scan(
			&assessmentRevision, &readinessStatus, &dimensions, &blockers, &assumptions, &questionCount,
		); err == nil {
			readiness = map[string]any{
				"assessment_id": readinessID, "revision": assessmentRevision, "status": readinessStatus,
				"dimensions": dimensions, "blocking_unknowns": blockers, "assumptions": assumptions,
				"clarification_questions": questionCount,
			}
		}
	}
	var draft any
	var draftRevision uint64
	var draftStatus string
	if draftID != "" {
		var snapshot, assumptions json.RawMessage
		if err := m.Pool.QueryRow(ctx, `
SELECT revision,status,task_spec_snapshot,assumptions
FROM assistant_task_drafts WHERE task_draft_id=$1`, draftID).Scan(
			&draftRevision, &draftStatus, &snapshot, &assumptions,
		); err == nil {
			draft = map[string]any{
				"task_draft_id": draftID, "revision": draftRevision, "status": draftStatus,
				"task_spec": snapshot, "assumptions": assumptions,
			}
		}
	}
	var checkpoint any
	var checkpointRevision uint64
	var checkpointStatus string
	if checkpointID != "" {
		var workingDraft, allowedActions json.RawMessage
		if err := m.Pool.QueryRow(ctx, `
SELECT revision,status,working_draft,allowed_actions
FROM assistant_clarification_checkpoints WHERE checkpoint_id=$1`, checkpointID).Scan(
			&checkpointRevision, &checkpointStatus, &workingDraft, &allowedActions,
		); err == nil {
			checkpoint = map[string]any{
				"checkpoint_id": checkpointID, "revision": checkpointRevision,
				"status": checkpointStatus, "working_draft": workingDraft,
				"allowed_actions": allowedActions,
			}
		}
	}
	refs := make([]projectionapp.RelatedRef, 0)
	handoffs := make([]map[string]any, 0)
	taskRows, err := m.Pool.Query(ctx, `
SELECT task.task_id,task.status,COALESCE(task.worker_session_id,'')
FROM tasks task
JOIN assistant_creation_intent_bindings binding ON binding.intent_id=task.creation_intent_id
WHERE binding.conversation_id=$1 ORDER BY task.created_at,task.task_id`, conversationID)
	if err != nil {
		return nil, nil, nil, err
	}
	for taskRows.Next() {
		var taskID, taskStatus, sessionID string
		if err := taskRows.Scan(&taskID, &taskStatus, &sessionID); err != nil {
			taskRows.Close()
			return nil, nil, nil, err
		}
		handoffs = append(handoffs, map[string]any{
			"task_id": taskID, "status": taskStatus, "worker_session_id": emptyOmit(sessionID),
		})
		refs = append(refs, projectionapp.RelatedRef{
			Type: "task_summary", ID: taskID, Href: "/api/v1/views/task_summary/" + taskID,
		})
		if sessionID != "" {
			refs = append(refs, projectionapp.RelatedRef{
				Type: "worker_detail", ID: sessionID, Href: "/api/v1/views/worker_detail/" + sessionID,
			})
		}
	}
	taskRows.Close()
	actions := make([]projectionapp.ActionRef, 0)
	if status == "active" {
		actions = append(actions, projectionapp.ActionRef{
			Action: "assistant.send_message", TargetID: conversationID, ExpectedRevision: revision,
		})
	}
	if draftID != "" && draftStatus == "current" && readinessID != "" &&
		(readinessStatus == "ready_confirmed" || readinessStatus == "ready_with_assumptions") {
		actions = append(actions, projectionapp.ActionRef{
			Action: "assistant.confirm_task", TargetID: draftID, ExpectedRevision: draftRevision,
		})
	}
	if checkpointID != "" && checkpointStatus == "active" {
		actions = append(actions, projectionapp.ActionRef{
			Action: "assistant.checkpoint", TargetID: checkpointID, ExpectedRevision: checkpointRevision,
		})
	}
	if runMap, ok := run.(map[string]any); ok && runMap["status"] == "failed" && runMap["retryable"] == true {
		actions = append(actions, projectionapp.ActionRef{
			Action: "assistant.retry_run", TargetID: activeRunID,
			ExpectedRevision: runMap["revision"].(uint64),
		})
	}
	data, err := marshalObject(map[string]any{
		"conversation_id": conversationID, "revision": revision, "generation": generation,
		"status": status, "workspace_id": emptyOmit(workspaceID), "updated_at": updatedAt.UTC().Format(time.RFC3339),
		"turns": turns, "assistant_run": run, "readiness": readiness, "task_draft": draft,
		"checkpoint": checkpoint, "handoffs": handoffs,
	})
	return data, actions, refs, err
}

type homeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (m Materializer) homeDashboardAt(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
	updatedAt time.Time,
) (json.RawMessage, []projectionapp.RelatedRef, error) {
	return m.homeDashboardAtContract(
		ctx, query, tenantID, updatedAt, homeDashboardContractV2,
	)
}

func (m Materializer) homeDashboardV1At(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
	updatedAt time.Time,
) (json.RawMessage, []projectionapp.RelatedRef, error) {
	return m.homeDashboardAtContract(
		ctx, query, tenantID, updatedAt, homeDashboardContractV1,
	)
}

func (m Materializer) homeDashboardAtContract(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
	updatedAt time.Time,
	contractVersion int,
) (json.RawMessage, []projectionapp.RelatedRef, error) {
	if contractVersion != homeDashboardContractV1 &&
		contractVersion != homeDashboardContractV2 {
		return nil, nil, fmt.Errorf("Home dashboard contract version is invalid")
	}
	attentionTasks, err := m.homeAttentionTasks(ctx, query, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if m.homeRefreshObserved != nil {
		m.homeRefreshObserved()
	}
	recentWorkers, err := m.homeRecentWorkers(ctx, query, tenantID)
	if err != nil {
		return nil, nil, err
	}
	recentDeliveries, err := m.homeRecentDeliveries(ctx, query, tenantID)
	if err != nil {
		return nil, nil, err
	}
	used, limit := 0, 10
	if err := query.QueryRow(ctx, `
SELECT reserved_count,quota_limit
FROM worker_session_continuity_quotas
WHERE tenant_id=$1 AND user_id=$2`, tenantID, m.UserID).Scan(&used, &limit); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("query Home continuity capacity: %w", err)
		}
	}
	refs := make([]projectionapp.RelatedRef, 0, len(attentionTasks)+len(recentWorkers)+len(recentDeliveries))
	seenRefs := make(map[string]struct{}, cap(refs))
	appendRef := func(ref projectionapp.RelatedRef) {
		key := ref.Type + "\x00" + ref.ID
		if _, ok := seenRefs[key]; ok {
			return
		}
		seenRefs[key] = struct{}{}
		refs = append(refs, ref)
	}
	for _, item := range attentionTasks {
		appendRef(projectionapp.RelatedRef{
			Type: "task_summary", ID: item.TaskID,
			Href: "/api/v1/views/task_summary/" + item.TaskID,
		})
	}
	for _, item := range recentWorkers {
		appendRef(projectionapp.RelatedRef{
			Type: "worker_detail", ID: item.WorkerSessionID,
			Href: "/api/v1/views/worker_detail/" + item.WorkerSessionID,
		})
	}
	for _, item := range recentDeliveries {
		appendRef(projectionapp.RelatedRef{
			Type: "task_summary", ID: item.TaskID,
			Href: "/api/v1/views/task_summary/" + item.TaskID,
		})
		for _, artifact := range item.Artifacts {
			artifactID := artifact["artifact_id"].(string)
			appendRef(projectionapp.RelatedRef{
				Type: "artifact_detail", ID: artifactID,
				Href: "/api/v1/views/artifact_detail/" + artifactID,
			})
		}
	}
	var encodedDeliveries any = recentDeliveries
	if contractVersion == homeDashboardContractV1 {
		legacyDeliveries := make([]homeDeliveryItemV1, 0, len(recentDeliveries))
		for _, delivery := range recentDeliveries {
			legacyDeliveries = append(legacyDeliveries, homeDeliveryItemV1{
				homeTaskItem: delivery.homeTaskItem,
				Artifacts:    delivery.Artifacts,
			})
		}
		encodedDeliveries = legacyDeliveries
	}
	data, err := marshalObject(map[string]any{
		"attention_tasks":   attentionTasks,
		"recent_workers":    recentWorkers,
		"recent_deliveries": encodedDeliveries,
		"capacity":          map[string]any{"used": used, "limit": limit},
		"updated_at":        updatedAt.UTC().Format(time.RFC3339),
	})
	return data, refs, err
}

func (m Materializer) homeAttentionTasks(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
) ([]homeTaskItem, error) {
	// SingleUserLocalProfile fixes Tenant/User server-side and has no dynamic
	// Workspace membership store; owner scope is its authoritative readability
	// boundary. A future multi-user profile must add a readable-Workspace port.
	rows, err := query.Query(ctx, `
SELECT task_id,COALESCE(task_spec_snapshot->>'title',task_id),workspace_id,status,
       COALESCE(worker_session_id,''),updated_at
FROM tasks
WHERE tenant_id=$1 AND initiator_user_id=$2
  AND status IN ('waiting_user','delivered')
ORDER BY updated_at DESC,task_id ASC
LIMIT 6`, tenantID, m.UserID)
	if err != nil {
		return nil, fmt.Errorf("query Home attention Tasks: %w", err)
	}
	return scanHomeTaskItems(rows, "Home attention Tasks")
}

func (m Materializer) homeRecentDeliveries(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
) ([]homeDeliveryItem, error) {
	rows, err := query.Query(ctx, `
SELECT task_id,COALESCE(task_spec_snapshot->>'title',task_id),workspace_id,status,
       acceptance_mode,COALESCE(worker_session_id,''),updated_at
FROM tasks
WHERE tenant_id=$1 AND initiator_user_id=$2
  AND status IN ('delivered','completed')
ORDER BY updated_at DESC,task_id ASC
LIMIT 6`, tenantID, m.UserID)
	if err != nil {
		return nil, fmt.Errorf("query Home recent deliveries: %w", err)
	}
	tasks := make([]homeDeliveryItem, 0, 6)
	for rows.Next() {
		var item homeDeliveryItem
		var updatedAt time.Time
		if err := rows.Scan(
			&item.TaskID, &item.Title, &item.Workspace, &item.Status,
			&item.AcceptanceMode, &item.WorkerSessionID, &updatedAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan Home recent deliveries: %w", err)
		}
		item.StatusTone = statusTone(item.Status)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		tasks = append(tasks, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate Home recent deliveries: %w", err)
	}
	rows.Close()
	items := make([]homeDeliveryItem, 0, len(tasks))
	for _, task := range tasks {
		artifacts, err := m.homeDeliveryArtifacts(
			ctx, query, tenantID, task.TaskID, task.Workspace,
		)
		if err != nil {
			return nil, err
		}
		task.Artifacts = artifacts
		items = append(items, task)
	}
	return items, nil
}

func (m Materializer) homeDeliveryArtifacts(
	ctx context.Context,
	query homeQueryer,
	tenantID, taskID, workspaceID string,
) ([]map[string]any, error) {
	rows, err := query.Query(ctx, `
SELECT artifact.artifact_id,artifact.artifact_type
FROM artifact_refs artifact
JOIN worker_runs run ON run.worker_run_id=artifact.worker_run_id
JOIN worker_sessions session ON session.worker_session_id=run.worker_session_id
WHERE artifact.tenant_id=$1 AND artifact.user_id=$2
  AND artifact.workspace_id=$3
  AND session.tenant_id=$1 AND session.user_id=$2 AND session.workspace_id=$3
  AND COALESCE(artifact.task_id,run.task_id,run.origin_task_id)=$4
ORDER BY artifact.visible_at ASC,artifact.artifact_id ASC`,
		tenantID, m.UserID, workspaceID, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("query Home delivery Artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]map[string]any, 0)
	for rows.Next() {
		var artifactID, artifactType string
		if err := rows.Scan(&artifactID, &artifactType); err != nil {
			return nil, fmt.Errorf("scan Home delivery Artifact: %w", err)
		}
		artifacts = append(artifacts, map[string]any{
			"artifact_id":   artifactID,
			"artifact_type": artifactType,
			"href":          "/api/v1/views/artifact_detail/" + artifactID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Home delivery Artifacts: %w", err)
	}
	return artifacts, nil
}

func scanHomeTaskItems(rows pgx.Rows, label string) ([]homeTaskItem, error) {
	defer rows.Close()
	items := make([]homeTaskItem, 0, 6)
	for rows.Next() {
		var item homeTaskItem
		var updatedAt time.Time
		if err := rows.Scan(
			&item.TaskID, &item.Title, &item.Workspace, &item.Status,
			&item.WorkerSessionID, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		item.StatusTone = statusTone(item.Status)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return items, nil
}

func (m Materializer) homeRecentWorkers(
	ctx context.Context,
	query homeQueryer,
	tenantID string,
) ([]homeWorkerItem, error) {
	rows, err := query.Query(ctx, `
SELECT session.worker_session_id,session.workspace_id,session.status,session.runtime_generation,
       COALESCE(session.bound_task_id,''),COALESCE(task.task_spec_snapshot->>'title',''),
       session.updated_at
FROM worker_sessions session
LEFT JOIN tasks task
  ON task.task_id=session.bound_task_id
 AND task.tenant_id=session.tenant_id
 AND task.initiator_user_id=session.user_id
WHERE session.tenant_id=$1 AND session.user_id=$2
ORDER BY session.updated_at DESC,session.worker_session_id ASC
LIMIT 6`, tenantID, m.UserID)
	if err != nil {
		return nil, fmt.Errorf("query Home recent Workers: %w", err)
	}
	defer rows.Close()
	items := make([]homeWorkerItem, 0, 6)
	for rows.Next() {
		var item homeWorkerItem
		var updatedAt time.Time
		if err := rows.Scan(
			&item.WorkerSessionID, &item.Workspace, &item.Status, &item.RuntimeGeneration,
			&item.BoundTaskID, &item.TaskTitle, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan Home recent Workers: %w", err)
		}
		item.StatusTone = statusTone(item.Status)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Home recent Workers: %w", err)
	}
	return items, nil
}

type homeDeliveryItem struct {
	homeTaskItem
	AcceptanceMode string           `json:"acceptance_mode"`
	Artifacts      []map[string]any `json:"artifacts"`
}

type homeDeliveryItemV1 struct {
	homeTaskItem
	Artifacts []map[string]any `json:"artifacts"`
}

type homeWorkerItem struct {
	WorkerSessionID   string `json:"worker_session_id"`
	Workspace         string `json:"workspace"`
	Status            string `json:"status"`
	StatusTone        string `json:"status_tone"`
	RuntimeGeneration uint64 `json:"runtime_generation"`
	BoundTaskID       string `json:"bound_task_id,omitempty"`
	TaskTitle         string `json:"task_title,omitempty"`
	UpdatedAt         string `json:"updated_at"`
}

type homeTaskItem struct {
	TaskID          string `json:"task_id"`
	Title           string `json:"title"`
	Workspace       string `json:"workspace"`
	Status          string `json:"status"`
	StatusTone      string `json:"status_tone"`
	WorkerSessionID string `json:"worker_session_id,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

func (m Materializer) taskIndex(ctx context.Context, tenantID string) (json.RawMessage, error) {
	rows, err := m.Pool.Query(ctx, `
SELECT task_id,COALESCE(task_spec_snapshot->>'title',task_id),workspace_id,status,
       COALESCE(worker_session_id,''),updated_at,`+currentTaskRuntimeDispatchWaitSQL+`
FROM tasks WHERE tenant_id=$1 AND initiator_user_id=$2
ORDER BY updated_at DESC,task_id DESC`, tenantID, m.UserID)
	if err != nil {
		return nil, fmt.Errorf("query Task index: %w", err)
	}
	defer rows.Close()
	items := make([]taskIndexItem, 0)
	for rows.Next() {
		var item taskIndexItem
		var waitJSON json.RawMessage
		var updatedAt time.Time
		if err := rows.Scan(&item.TaskID, &item.Title, &item.Workspace, &item.Status,
			&item.WorkerSessionID, &updatedAt, &waitJSON); err != nil {
			return nil, fmt.Errorf("scan Task index: %w", err)
		}
		item.RuntimeDispatchWait, err = decodeRuntimeDispatchWait(waitJSON)
		if err != nil {
			return nil, err
		}
		item.StatusTone = statusTone(item.Status)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Task index: %w", err)
	}
	var used int
	if err := m.Pool.QueryRow(ctx, `
SELECT count(*) FROM worker_sessions
WHERE tenant_id=$1 AND user_id=$2 AND status NOT IN ('closed','expired')`, tenantID, m.UserID).Scan(&used); err != nil {
		return nil, fmt.Errorf("query Worker capacity: %w", err)
	}
	return marshalObject(map[string]any{
		"tasks":    items,
		"capacity": map[string]any{"used": used, "limit": 10},
	})
}

func (m Materializer) taskSummary(
	ctx context.Context,
	tenantID, taskID string,
) (json.RawMessage, []projectionapp.ActionRef, []projectionapp.RelatedRef, error) {
	var title, workspaceID, status, sessionID string
	var revision uint64
	var waitJSON json.RawMessage
	var updatedAt time.Time
	err := m.Pool.QueryRow(ctx, `
SELECT COALESCE(task_spec_snapshot->>'title',task_id),workspace_id,status,
       COALESCE(worker_session_id,''),revision,updated_at,`+currentTaskRuntimeDispatchWaitSQL+`
FROM tasks WHERE tenant_id=$1 AND initiator_user_id=$2 AND task_id=$3`,
		tenantID, m.UserID, taskID).Scan(&title, &workspaceID, &status, &sessionID, &revision, &updatedAt, &waitJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read Task summary: %w", err)
	}
	actions := make([]projectionapp.ActionRef, 0, 3)
	if status == "delivered" {
		actions = append(actions,
			projectionapp.ActionRef{Action: "task.accept", TargetID: taskID, ExpectedRevision: revision},
			projectionapp.ActionRef{Action: "task.rework", TargetID: taskID, ExpectedRevision: revision},
		)
	}
	if status != "completed" && status != "failed" && status != "cancelled" {
		actions = append(actions, projectionapp.ActionRef{Action: "task.cancel", TargetID: taskID, ExpectedRevision: revision})
	}
	refs := make([]projectionapp.RelatedRef, 0, 1)
	if sessionID != "" {
		refs = append(refs, projectionapp.RelatedRef{Type: "worker_detail", ID: sessionID,
			Href: "/api/v1/views/worker_detail/" + sessionID})
	}
	wait, err := decodeRuntimeDispatchWait(waitJSON)
	if err != nil {
		return nil, nil, nil, err
	}
	values := map[string]any{
		"task_id": taskID, "title": title, "workspace": workspaceID, "status": status,
		"status_tone": statusTone(status), "worker_session_id": emptyOmit(sessionID),
		"revision": revision, "updated_at": updatedAt.UTC().Format(time.RFC3339),
	}
	if wait != nil {
		values["runtime_dispatch_wait"] = wait
	}
	data, err := marshalObject(values)
	return data, actions, refs, err
}

func (m Materializer) taskSessionID(ctx context.Context, tenantID, taskID string) (string, error) {
	var sessionID string
	err := m.Pool.QueryRow(ctx, `
SELECT COALESCE(worker_session_id,'') FROM tasks
WHERE tenant_id=$1 AND initiator_user_id=$2 AND task_id=$3`, tenantID, m.UserID, taskID).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return sessionID, err
}

func (m Materializer) sessionTaskID(ctx context.Context, tenantID, sessionID string) (string, error) {
	var taskID string
	err := m.Pool.QueryRow(ctx, `
SELECT COALESCE(task_id,origin_task_id,'') FROM worker_runs
WHERE worker_session_id=$1 AND EXISTS (
  SELECT 1 FROM worker_sessions session
  WHERE session.worker_session_id=$1 AND session.tenant_id=$2 AND session.user_id=$3)
ORDER BY created_at DESC LIMIT 1`, sessionID, tenantID, m.UserID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

type artifactRow struct {
	ArtifactID        string
	Type              string
	ObjectKey         string
	Checksum          string
	SizeBytes         int64
	RunID             string
	TurnID            string
	RuntimeGeneration uint64
	GeneratedAt       time.Time
	Completeness      string
}

func (m Materializer) workerDetail(
	ctx context.Context,
	tenantID, sessionID string,
) (json.RawMessage, []projectionapp.ActionRef, []projectionapp.RelatedRef, []artifactRow, error) {
	var status, workspaceID, boundTaskID, activeRuntimeID, activeRunID string
	var sessionRevision, runtimeGeneration uint64
	err := m.Pool.QueryRow(ctx, `
SELECT status,workspace_id,COALESCE(bound_task_id,''),COALESCE(active_runtime_instance_id,''),
       session_revision,runtime_generation,COALESCE(active_worker_run_id,'')
FROM worker_sessions WHERE worker_session_id=$1 AND tenant_id=$2 AND user_id=$3`,
		sessionID, tenantID, m.UserID).Scan(&status, &workspaceID, &boundTaskID, &activeRuntimeID,
		&sessionRevision, &runtimeGeneration, &activeRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("read WorkerSession detail: %w", err)
	}

	var runID, runStatus, profileID string
	var runRevision uint64
	var runStartedAt *time.Time
	err = m.Pool.QueryRow(ctx, `
SELECT worker_run_id,status,revision,agent_profile_id,COALESCE(started_at,created_at)
FROM worker_runs WHERE worker_session_id=$1 ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(
		&runID, &runStatus, &runRevision, &profileID, &runStartedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil, fmt.Errorf("read latest WorkerRun: %w", err)
	}
	var turnID, turnStatus string
	var turnRevision uint64
	var deadlineAt *time.Time
	if runID != "" {
		err = m.Pool.QueryRow(ctx, `
SELECT agent_turn_id,status,revision,deadline_at FROM agent_turns
WHERE worker_run_id=$1 ORDER BY created_at DESC LIMIT 1`, runID).Scan(
			&turnID, &turnStatus, &turnRevision, &deadlineAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, nil, fmt.Errorf("read latest AgentTurn: %w", err)
		}
	}
	var taskStatus string
	var taskRevision uint64
	sessionBoundTaskID := boundTaskID
	if sessionBoundTaskID == "" {
		boundTaskID, _ = m.sessionTaskID(ctx, tenantID, sessionID)
	}
	if boundTaskID != "" {
		_ = m.Pool.QueryRow(ctx, `SELECT status,revision FROM tasks WHERE task_id=$1 AND tenant_id=$2`, boundTaskID, tenantID).Scan(&taskStatus, &taskRevision)
	}
	var recovery any
	var recoveryPhase, continuityStatus, outcomeReason string
	var failedGeneration, replacementGeneration uint64
	var attemptCount uint32
	var recoveryEvidenceAt time.Time
	err = m.Pool.QueryRow(ctx, `
SELECT phase,failed_generation,COALESCE(replacement_generation,0),attempt_count,
       continuity_status,evidence_at,COALESCE(outcome_reason,'')
FROM runtime_recovery_incidents
WHERE worker_session_id=$1
ORDER BY opened_at DESC,incident_id DESC
LIMIT 1`, sessionID).Scan(
		&recoveryPhase, &failedGeneration, &replacementGeneration, &attemptCount,
		&continuityStatus, &recoveryEvidenceAt, &outcomeReason,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil, fmt.Errorf("read Runtime recovery projection: %w", err)
	}
	if err == nil {
		recoveryStatus := "recovering_runtime"
		if recoveryPhase == "recovered" {
			recoveryStatus = "runtime_recovered"
		} else if recoveryPhase == "exhausted" {
			recoveryStatus = "runtime_recovery_failed"
		}
		recovery = map[string]any{
			"status": recoveryStatus,
			"phase":  recoveryPhase, "failed_generation": failedGeneration,
			"replacement_generation": zeroOmit(replacementGeneration),
			"attempt_count":          attemptCount, "continuity_status": continuityStatus,
			"evidence_at":    recoveryEvidenceAt.UTC().Format(time.RFC3339Nano),
			"outcome_reason": emptyOmit(outcomeReason),
		}
	}

	timelinePage, err := NewStore(m.Pool).ReadWorkerTimeline(
		ctx,
		projectionapp.WorkerTimelineQuery{
			Viewer:          projectionapp.Viewer{TenantID: tenantID, UserID: m.UserID},
			WorkerSessionID: sessionID, Limit: 20,
		},
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	artifacts, err := m.artifacts(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	approvals, approvalActions, err := m.approvals(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	artifactData := make([]map[string]any, 0, len(artifacts))
	refs := make([]projectionapp.RelatedRef, 0, len(artifacts))
	stageReport := ""
	for _, artifact := range artifacts {
		artifactData = append(artifactData, artifact.summary())
		refs = append(refs, projectionapp.RelatedRef{Type: "artifact_detail", ID: artifact.ArtifactID,
			Href: "/api/v1/views/artifact_detail/" + artifact.ArtifactID})
		if artifact.Type == "report" && m.Objects != nil {
			review := m.reviewArtifact(ctx, artifact)
			if review.Manifest.Outcome == "online" && review.Manifest.Renderable {
				stageReport = string(review.Content)
			}
		}
	}

	actions := make([]projectionapp.ActionRef, 0, 5)
	if status != "closed" && status != "expired" && status != "recovering" && recoveryPhase != "exhausted" &&
		runID != "" && turnID != "" {
		if terminalRun(runStatus) && terminalTurn(turnStatus) {
			if sessionBoundTaskID == "" {
				actions = append(actions, projectionapp.ActionRef{
					Action: "worker.follow_up", TargetID: sessionID,
					ExpectedRevision: sessionRevision,
				})
			} else if taskStatus == "delivered" && taskRevision > 0 {
				actions = append(actions, projectionapp.ActionRef{
					Action: "task.continue", TargetID: sessionBoundTaskID,
					ExpectedRevision: taskRevision,
					Metadata:         json.RawMessage(`{"interaction_kinds":["chat","rework"]}`),
				})
			}
		}
		if activeRun(runStatus) && activeTurn(turnStatus) {
			actions = append(actions,
				projectionapp.ActionRef{Action: "worker.queue", TargetID: sessionID, ExpectedRevision: sessionRevision},
				projectionapp.ActionRef{Action: "worker.interrupt", TargetID: turnID, ExpectedRevision: turnRevision},
			)
		}
	}
	if sessionBoundTaskID != "" && taskStatus == "delivered" && taskRevision > 0 {
		actions = append(actions,
			projectionapp.ActionRef{Action: "task.accept", TargetID: sessionBoundTaskID, ExpectedRevision: taskRevision},
			projectionapp.ActionRef{Action: "task.rework", TargetID: sessionBoundTaskID, ExpectedRevision: taskRevision},
		)
	}
	actions = append(actions, approvalActions...)
	waiting := any(nil)
	if status == "waiting_user" {
		reason := "worker_waiting_user"
		message := "Worker needs input before it can continue."
		allowedActions := []string{"reply", "cancel"}
		if recoveryPhase == "exhausted" {
			reason = "runtime_recovery_failed"
			allowedActions = []string{}
			if attemptCount == 0 && outcomeReason == "runtime_recovery_disabled" {
				message = "自动恢复未启用，Runtime 已停止，需要处理"
			} else if attemptCount == 0 {
				message = "Runtime recovery stopped before a replacement attempt."
			} else {
				message = "Runtime recovery stopped after its single safe replacement attempt."
			}
		}
		waiting = map[string]any{"reason": reason, "message": message,
			"allowed_actions": allowedActions}
	}
	runtimeHealth := "Not started"
	var networkProfile any
	if activeRuntimeID != "" {
		var runtimeStatus, networkID, networkDigest, networkEnforcementStatus string
		var networkRevision uint64
		if m.Pool.QueryRow(ctx, `
SELECT status,network_policy_id,network_policy_revision,network_policy_digest,
       network_enforcement_status
FROM runtime_instances
WHERE runtime_instance_id=$1`, activeRuntimeID).Scan(
			&runtimeStatus, &networkID, &networkRevision, &networkDigest, &networkEnforcementStatus,
		) == nil {
			runtimeHealth = humanStatus(runtimeStatus)
			networkProfile = map[string]any{
				"id": networkID, "revision": networkRevision, "digest": networkDigest,
				"enforcement_status": networkEnforcementStatus,
			}
		}
	}
	// Existing healthy clients use these legacy "active" fields as the previous
	// terminal command target. Preserve that protocol. Only the current disabled
	// failure graph exposes actual binding/slot after safe cancellation release;
	// historical status/timeline remain visible without granting execution.
	displayRunID, displayTaskID, activeTurnID := runID, boundTaskID, turnID
	activeRunRevision, activeTurnRevision := runRevision, turnRevision
	if status == "waiting_user" && recoveryPhase == "exhausted" && outcomeReason == "runtime_recovery_disabled" && attemptCount == 0 && failedGeneration == runtimeGeneration {
		displayRunID, displayTaskID = activeRunID, sessionBoundTaskID
		if activeRunID == "" || activeRunID != runID {
			activeRunRevision, activeTurnRevision, activeTurnID = 0, 0, ""
		}
	}
	data, err := marshalObject(map[string]any{
		"worker_session_id": sessionID, "session_revision": sessionRevision, "workspace": workspaceID,
		"status": status, "status_tone": statusTone(status), "task_status": emptyOmit(taskStatus),
		"task_status_label": taskStatusLabel(taskStatus),
		"task_revision":     zeroOmit(taskRevision),
		"run_status":        emptyOmit(runStatus), "turn_status": emptyOmit(turnStatus),
		"bound_task_id": emptyOmit(displayTaskID), "active_worker_run_id": emptyOmit(displayRunID),
		"active_run_revision": zeroOmit(activeRunRevision), "active_agent_turn_id": emptyOmit(activeTurnID),
		"active_turn_revision": zeroOmit(activeTurnRevision), "runtime_health": runtimeHealth,
		"runtime_generation": runtimeGeneration, "agent_profile": profileID,
		"network_profile":  networkProfile,
		"runtime_recovery": recovery,
		"started_at":       timeOmit(runStartedAt), "deadline_at": timeOmit(deadlineAt),
		"artifacts": artifactData, "approvals": approvals,
		"timeline_groups": timelinePage.Turns, "timeline_cursor": emptyOmit(timelinePage.NextCursor),
		"timeline_has_more": timelinePage.HasMore,
		"waiting_user":      waiting, "stage_report": emptyOmit(stageReport),
	})
	return data, actions, refs, artifacts, err
}

func (m Materializer) approvals(
	ctx context.Context,
	tenantID string,
	sessionID string,
) ([]map[string]any, []projectionapp.ActionRef, error) {
	rows, err := m.Pool.Query(ctx, `SELECT approval_request_id,revision,capability,target,effect,
       reason,expires_at,scope_options
FROM worker_approval_requests
WHERE tenant_id=$1 AND user_id=$2 AND worker_session_id=$3 AND status='pending' AND expires_at>$4
ORDER BY created_at,approval_request_id`, tenantID, m.UserID, sessionID, m.Now().UTC())
	if err != nil {
		return nil, nil, fmt.Errorf("query Worker approval requests: %w", err)
	}
	defer rows.Close()
	requests := make([]map[string]any, 0)
	actions := make([]projectionapp.ActionRef, 0)
	for rows.Next() {
		var requestID, capability, target, effect, reason string
		var revision uint64
		var expiresAt time.Time
		var scopes []string
		if err := rows.Scan(&requestID, &revision, &capability, &target, &effect,
			&reason, &expiresAt, &scopes); err != nil {
			return nil, nil, fmt.Errorf("scan Worker approval request: %w", err)
		}
		requests = append(requests, map[string]any{
			"approval_request_id": requestID, "revision": revision, "capability": capability,
			"target": target, "effect": effect, "reason": reason,
			"expires_at": expiresAt.UTC().Format(time.RFC3339), "scope_options": scopes,
		})
		actions = append(actions, projectionapp.ActionRef{
			Action: "worker.approval", TargetID: requestID, ExpectedRevision: revision,
		})
	}
	return requests, actions, rows.Err()
}

func (m Materializer) artifacts(ctx context.Context, tenantID, sessionID string) ([]artifactRow, error) {
	rows, err := m.Pool.Query(ctx, `
SELECT artifact_id,artifact_type,object_key,object_checksum,size_bytes,worker_run_id,agent_turn_id,
       runtime_generation,visible_at,COALESCE(artifact_ref->>'completeness','unverified')
FROM artifact_refs WHERE tenant_id=$1 AND user_id=$2 AND worker_session_id=$3
ORDER BY visible_at,artifact_id`, tenantID, m.UserID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query Worker artifacts: %w", err)
	}
	defer rows.Close()
	result := make([]artifactRow, 0)
	for rows.Next() {
		var artifact artifactRow
		if err := rows.Scan(&artifact.ArtifactID, &artifact.Type, &artifact.ObjectKey, &artifact.Checksum,
			&artifact.SizeBytes, &artifact.RunID, &artifact.TurnID, &artifact.RuntimeGeneration,
			&artifact.GeneratedAt, &artifact.Completeness); err != nil {
			return nil, fmt.Errorf("scan Worker artifact: %w", err)
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (artifact artifactRow) summary() map[string]any {
	label := "Stage report"
	if artifact.Type == "patch" {
		label = "Patch review"
	}
	return map[string]any{
		"artifact_id": artifact.ArtifactID, "type": artifact.Type, "label": label,
		"size_bytes": artifact.SizeBytes, "checksum": artifact.Checksum,
		"completeness": artifact.Completeness, "worker_run_id": artifact.RunID,
		"agent_turn_id": artifact.TurnID, "runtime_generation": artifact.RuntimeGeneration,
		"generated_at": artifact.GeneratedAt.UTC().Format(time.RFC3339),
	}
}

func (m Materializer) artifactDetail(
	ctx context.Context,
	tenantID string,
	artifact artifactRow,
) (json.RawMessage, []projectionapp.ActionRef, error) {
	review := m.reviewArtifact(ctx, artifact)
	data := artifact.summary()
	data["availability"] = review.Manifest.Outcome
	data["review_manifest"] = review.Manifest
	data["content"] = string(review.Content)
	actions := make([]projectionapp.ActionRef, 0, 1)
	if review.Manifest.DownloadAllowed {
		data["download_url"] = "/api/v1/artifacts/" + artifact.ArtifactID + "/content"
		actions = append(actions, projectionapp.ActionRef{Action: "artifact.download", TargetID: artifact.ArtifactID, ExpectedRevision: 1})
	}
	encoded, err := marshalObject(data)
	_ = tenantID
	return encoded, actions, err
}

func (m Materializer) reviewArtifact(
	ctx context.Context,
	artifact artifactRow,
) artifactapp.ArtifactReview {
	input := artifactapp.ArtifactReviewInput{
		Type: artifact.Type, SizeBytes: artifact.SizeBytes,
	}
	if m.Objects == nil {
		input.OutcomeHint = "missing"
		return artifactapp.BuildArtifactReview(input)
	}
	if err := m.Objects.Verify(ctx, artifact.ObjectKey, artifact.Checksum); err != nil {
		input.OutcomeHint = "checksum_failed"
		if errors.Is(err, os.ErrNotExist) {
			input.OutcomeHint = "missing"
		}
		return artifactapp.BuildArtifactReview(input)
	}
	if artifact.SizeBytes > artifactapp.DefaultArtifactReviewBytes {
		input.OutcomeHint = "oversized"
		return artifactapp.BuildArtifactReview(input)
	}
	reader, err := m.Objects.Open(ctx, artifact.ObjectKey)
	if err != nil {
		input.OutcomeHint = "missing"
		return artifactapp.BuildArtifactReview(input)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, artifactapp.DefaultArtifactReviewBytes+1))
	if err != nil {
		input.OutcomeHint = "missing"
		return artifactapp.BuildArtifactReview(input)
	}
	if len(content) > artifactapp.DefaultArtifactReviewBytes {
		input.OutcomeHint = "oversized"
		return artifactapp.BuildArtifactReview(input)
	}
	input.Content = content
	return artifactapp.BuildArtifactReview(input)
}

func (m Materializer) update(
	event contractsv1.IntegrationEvent,
	viewType, viewID string,
	data json.RawMessage,
	actions []projectionapp.ActionRef,
	refs []projectionapp.RelatedRef,
) projectionapp.ViewUpdate {
	if actions == nil {
		actions = []projectionapp.ActionRef{}
	}
	if refs == nil {
		refs = []projectionapp.RelatedRef{}
	}
	payload, _ := marshalObject(map[string]any{"data": data, "available_actions": actions, "related_refs": refs})
	return projectionapp.ViewUpdate{
		DeltaID:  event.EventID + "-" + strings.ReplaceAll(viewType+"-"+viewID, "/", "-"),
		TenantID: event.TenantID, UserID: m.UserID, ViewType: viewType, ViewID: viewID,
		QueryFingerprint: "single-user-local-v1", Data: data, AvailableActions: actions,
		RelatedRefs: refs, EventType: "view.entity_upserted.v1", Critical: true,
		CommandID:       event.CorrelationID,
		ClientCommandID: event.ClientCommandID,
		SourceRefs:      []projectionapp.SourceRef{{Type: "integration_event", ID: event.EventID}},
		DeltaPayload:    payload,
	}
}

func marshalObject(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode projection data: %w", err)
	}
	return encoded, nil
}

func statusTone(status string) string {
	switch status {
	case "running", "starting", "interactive":
		return "active"
	case "waiting_user", "delivered", "idle", "allocating":
		return "waiting"
	case "completed":
		return "success"
	case "failed", "cancelled", "lost", "expired", "closed", "timed_out":
		return "danger"
	default:
		return "neutral"
	}
}

func humanStatus(value string) string {
	parts := strings.Split(value, "_")
	for index := range parts {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, " ")
}

func taskStatusLabel(status string) any {
	if status == "" {
		return nil
	}
	if status == "completed" {
		return "Accepted"
	}
	return humanStatus(status)
}

func terminalRun(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

func terminalTurn(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

func activeRun(status string) bool {
	return status == "starting" || status == "running" || status == "waiting_user"
}

func activeTurn(status string) bool {
	return status == "accepted" || status == "delivered" || status == "consumed"
}

func emptyOmit(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func zeroOmit(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func timeOmit(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

var _ interface {
	Publish(context.Context, contractsv1.IntegrationEvent) error
} = Materializer{}
