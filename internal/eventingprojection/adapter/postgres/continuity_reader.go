package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

var _ projectionapp.ContinuityStore = (*Store)(nil)

func (store *Store) LoadSnapshotState(
	ctx context.Context,
	query projectionapp.SnapshotStoreQuery,
) (projectionapp.StoredSnapshotState, error) {
	if store == nil || store.pool == nil {
		return projectionapp.StoredSnapshotState{}, fmt.Errorf("projection PostgreSQL pool is required")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return projectionapp.StoredSnapshotState{}, fmt.Errorf("begin View snapshot state read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var state projectionapp.StoredSnapshotState
	err = tx.QueryRow(ctx, `
SELECT schema_version,tenant_id,user_id,view_type,view_id,view_epoch,view_cursor,
       projection_version,query_fingerprint,data,available_actions,related_refs,updated_at
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4`,
		query.Viewer.TenantID, query.Viewer.UserID, query.ViewType, query.ViewID,
	).Scan(
		&state.View.SchemaVersion, &state.View.TenantID, &state.View.UserID,
		&state.View.ViewType, &state.View.ViewID, &state.View.ViewEpoch,
		&state.View.ViewCursor, &state.View.ProjectionVersion, &state.View.QueryFingerprint,
		&state.View.Data, &state.View.AvailableActions, &state.View.RelatedRefs,
		&state.View.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return projectionapp.StoredSnapshotState{}, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return projectionapp.StoredSnapshotState{}, fmt.Errorf("read View snapshot state: %w", err)
	}

	if query.ReconcileClientCommandIDs != nil {
		if query.ReceiptNotBefore.IsZero() {
			return projectionapp.StoredSnapshotState{}, fmt.Errorf("projection command receipt lower time bound is required")
		}
		rows, err := tx.Query(ctx, `
SELECT client_command_id,command_id
FROM projection_command_receipts
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
  AND view_epoch=$5 AND view_cursor<=$6 AND projection_version<=$7
  AND applied_at>=$8
  AND client_command_id=ANY($9::text[])
ORDER BY client_command_id`,
			query.Viewer.TenantID, query.Viewer.UserID, query.ViewType, query.ViewID,
			state.View.ViewEpoch, state.View.ViewCursor, state.View.ProjectionVersion,
			query.ReceiptNotBefore, query.ReconcileClientCommandIDs,
		)
		if err != nil {
			return projectionapp.StoredSnapshotState{}, fmt.Errorf("read View snapshot command receipts: %w", err)
		}
		state.CommandReceipts = make([]projectionapp.CommandReceipt, 0, len(query.ReconcileClientCommandIDs))
		for rows.Next() {
			var receipt projectionapp.CommandReceipt
			if err := rows.Scan(&receipt.ClientCommandID, &receipt.CommandID); err != nil {
				rows.Close()
				return projectionapp.StoredSnapshotState{}, fmt.Errorf("scan View snapshot command receipt: %w", err)
			}
			state.CommandReceipts = append(state.CommandReceipts, receipt)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return projectionapp.StoredSnapshotState{}, fmt.Errorf("iterate View snapshot command receipts: %w", err)
		}
		rows.Close()
	}
	if err := tx.Commit(ctx); err != nil {
		return projectionapp.StoredSnapshotState{}, fmt.Errorf("commit View snapshot state read: %w", err)
	}
	return state, nil
}

func (store *Store) LoadReplayWindow(
	ctx context.Context,
	query projectionapp.ReplayStoreQuery,
) (projectionapp.StoredReplayWindow, error) {
	if store == nil || store.pool == nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("projection PostgreSQL pool is required")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("begin View replay window read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var window projectionapp.StoredReplayWindow
	err = tx.QueryRow(ctx, `
SELECT schema_version,tenant_id,user_id,view_type,view_id,
       view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4`,
		query.Viewer.TenantID, query.Viewer.UserID, query.ViewType, query.ViewID,
	).Scan(
		&window.Head.SchemaVersion, &window.Head.TenantID, &window.Head.UserID,
		&window.Head.ViewType, &window.Head.ViewID, &window.Head.ViewEpoch,
		&window.Head.ViewCursor, &window.Head.ProjectionVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return projectionapp.StoredReplayWindow{}, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("read View replay head: %w", err)
	}

	var earliest sql.NullInt64
	if err := tx.QueryRow(ctx, `
SELECT min(view_cursor)
FROM projection_deltas
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
  AND view_epoch=$5`,
		query.Viewer.TenantID, query.Viewer.UserID, query.ViewType, query.ViewID,
		window.Head.ViewEpoch,
	).Scan(&earliest); err != nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("read View replay floor: %w", err)
	}
	if earliest.Valid && earliest.Int64 >= 0 {
		cursor := uint64(earliest.Int64)
		window.EarliestCursor = &cursor
	}

	rows, err := tx.Query(ctx, `
SELECT delta_id,view_type,view_id,view_epoch,view_cursor,
       base_projection_version,result_projection_version,event_type,
       critical,command_id,client_command_id,source_refs,payload,
       encoded_size_bytes
FROM projection_deltas
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
  AND view_epoch=$5
  AND view_cursor>$6
  AND view_cursor<=$7
ORDER BY view_cursor
LIMIT $8`,
		query.Viewer.TenantID, query.Viewer.UserID, query.ViewType, query.ViewID,
		window.Head.ViewEpoch, query.AfterCursor, window.Head.ViewCursor, query.Limit,
	)
	if err != nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("query View replay window: %w", err)
	}
	window.Deltas = make([]projectionapp.ProjectionDelta, 0, query.Limit)
	for rows.Next() {
		var delta projectionapp.ProjectionDelta
		var commandID, clientCommandID *string
		if err := rows.Scan(
			&delta.DeltaID, &delta.ViewType, &delta.ViewID, &delta.ViewEpoch,
			&delta.ViewCursor, &delta.BaseProjectionVersion, &delta.ResultProjectionVersion,
			&delta.EventType, &delta.Critical, &commandID, &clientCommandID,
			&delta.SourceRefs, &delta.Payload, &delta.EncodedSizeBytes,
		); err != nil {
			rows.Close()
			return projectionapp.StoredReplayWindow{}, fmt.Errorf("scan View replay window: %w", err)
		}
		delta.SchemaVersion = projectionapp.ProjectionDeltaSchemaVersion
		if commandID != nil {
			delta.CommandID = *commandID
		}
		if clientCommandID != nil {
			delta.ClientCommandID = *clientCommandID
		}
		window.Deltas = append(window.Deltas, delta)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("iterate View replay window: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return projectionapp.StoredReplayWindow{}, fmt.Errorf("commit View replay window read: %w", err)
	}
	return window, nil
}
