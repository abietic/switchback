package application

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrProjectionIntegrity = errors.New("View projection integrity is invalid")

type ProjectionIntegrityError struct {
	Reason string
}

func (err ProjectionIntegrityError) Error() string {
	return ErrProjectionIntegrity.Error() + ": " + err.Reason
}

func (err ProjectionIntegrityError) Unwrap() error {
	return ErrProjectionIntegrity
}

const (
	DefaultProjectionReplayLimit = 128
	MaxProjectionReplayLimit     = 1000
)

type SnapshotQuery struct {
	Viewer                    Viewer
	ViewType                  string
	ViewID                    string
	ReconcileClientCommandIDs []string
}

type ResumeQuery struct {
	Viewer      Viewer
	ViewType    string
	ViewID      string
	AfterEpoch  uint64
	AfterCursor uint64
}

type ContinuityReason string

const (
	ContinuitySchemaIncompatible ContinuityReason = "schema_incompatible"
	ContinuityEpochMismatch      ContinuityReason = "epoch_mismatch"
	ContinuityCursorFuture       ContinuityReason = "cursor_future"
	ContinuityWindowEvicted      ContinuityReason = "window_evicted"
	ContinuityCursorGap          ContinuityReason = "cursor_gap"
	ContinuityVersionGap         ContinuityReason = "version_gap"
	ContinuityBehindEmpty        ContinuityReason = "behind_empty"
)

type ResumeResult struct {
	Deltas  []ProjectionDelta
	Control *ProjectionControlEvent
	Reason  ContinuityReason
}

type SnapshotStoreQuery struct {
	Viewer                    Viewer
	ViewType                  string
	ViewID                    string
	ReconcileClientCommandIDs []string
	ReceiptNotBefore          time.Time
}

type StoredSnapshotState struct {
	View            ViewProjection
	CommandReceipts []CommandReceipt
}

type ReplayStoreQuery struct {
	Viewer      Viewer
	ViewType    string
	ViewID      string
	AfterCursor uint64
	Limit       int
}

type StoredViewHead struct {
	SchemaVersion     string
	TenantID          string
	UserID            string
	ViewType          string
	ViewID            string
	ViewEpoch         uint64
	ViewCursor        uint64
	ProjectionVersion uint64
}

type StoredReplayWindow struct {
	Head           StoredViewHead
	EarliestCursor *uint64
	Deltas         []ProjectionDelta
}

type ContinuityStore interface {
	LoadSnapshotState(context.Context, SnapshotStoreQuery) (StoredSnapshotState, error)
	LoadReplayWindow(context.Context, ReplayStoreQuery) (StoredReplayWindow, error)
}

type ViewContinuityReaderOptions struct {
	Now         func() time.Time
	ReplayLimit int
}

type ViewContinuityReader struct {
	store       ContinuityStore
	now         func() time.Time
	replayLimit int
}

func NewViewContinuityReader(
	store ContinuityStore,
	options ViewContinuityReaderOptions,
) (*ViewContinuityReader, error) {
	if store == nil || options.Now == nil {
		return nil, fmt.Errorf("View continuity reader dependencies are required")
	}
	limit := options.ReplayLimit
	if limit == 0 {
		limit = DefaultProjectionReplayLimit
	}
	if limit < 1 || limit > MaxProjectionReplayLimit {
		return nil, fmt.Errorf("View continuity reader replay limit is invalid")
	}
	return &ViewContinuityReader{store: store, now: options.Now, replayLimit: limit}, nil
}

func (reader *ViewContinuityReader) ReadSnapshot(
	ctx context.Context,
	query SnapshotQuery,
) (ProjectionSnapshot, error) {
	if !validContinuityIdentity(query.Viewer, query.ViewType, query.ViewID) {
		return ProjectionSnapshot{}, ErrInvalidViewQuery
	}

	var normalizedIDs []string
	var err error
	if query.ReconcileClientCommandIDs != nil {
		normalizedIDs, err = normalizeCommandReceiptIDs(query.ReconcileClientCommandIDs)
		if err != nil {
			return ProjectionSnapshot{}, err
		}
	}

	generatedAt := reader.now().UTC()
	if generatedAt.IsZero() {
		return ProjectionSnapshot{}, ProjectionIntegrityError{Reason: "clock_zero"}
	}
	state, err := reader.store.LoadSnapshotState(ctx, SnapshotStoreQuery{
		Viewer:                    query.Viewer,
		ViewType:                  query.ViewType,
		ViewID:                    query.ViewID,
		ReconcileClientCommandIDs: normalizedIDs,
		ReceiptNotBefore:          generatedAt.Add(-CommandReceiptRetentionHorizon),
	})
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("load View snapshot state: %w", err)
	}
	view := state.View
	if view.TenantID != query.Viewer.TenantID || view.UserID != query.Viewer.UserID ||
		view.ViewType != query.ViewType || view.ViewID != query.ViewID {
		return ProjectionSnapshot{}, ProjectionIntegrityError{Reason: "stored_snapshot_identity"}
	}
	if view.SchemaVersion != ProjectionSnapshotSchemaVersion {
		return ProjectionSnapshot{}, ProjectionIntegrityError{Reason: "stored_snapshot_schema"}
	}
	if view.ViewEpoch == 0 || view.ProjectionVersion == 0 {
		return ProjectionSnapshot{}, ProjectionIntegrityError{Reason: "stored_snapshot_position"}
	}

	snapshot := ProjectionSnapshot{
		SchemaVersion:     ProjectionSnapshotSchemaVersion,
		ViewType:          view.ViewType,
		ViewID:            view.ViewID,
		TenantID:          view.TenantID,
		ViewEpoch:         view.ViewEpoch,
		ViewCursor:        view.ViewCursor,
		ProjectionVersion: view.ProjectionVersion,
		QueryFingerprint:  view.QueryFingerprint,
		GeneratedAt:       generatedAt,
		Data:              view.Data,
		AvailableActions:  view.AvailableActions,
		RelatedRefs:       view.RelatedRefs,
	}
	if query.ReconcileClientCommandIDs != nil {
		snapshot.CommandReceipts, err = normalizeCommandReceipts(normalizedIDs, state.CommandReceipts)
		if err != nil {
			return ProjectionSnapshot{}, ProjectionIntegrityError{Reason: "stored_snapshot_receipts"}
		}
	}
	return snapshot, nil
}

func validContinuityIdentity(viewer Viewer, viewType, viewID string) bool {
	return canonical(viewer.TenantID) && canonical(viewer.UserID) && validViewType(viewType) && canonical(viewID)
}

func (reader *ViewContinuityReader) Resume(
	ctx context.Context,
	query ResumeQuery,
) (ResumeResult, error) {
	if !validContinuityIdentity(query.Viewer, query.ViewType, query.ViewID) || query.AfterEpoch == 0 {
		return ResumeResult{}, ErrInvalidViewQuery
	}
	window, err := reader.store.LoadReplayWindow(ctx, ReplayStoreQuery{
		Viewer: query.Viewer, ViewType: query.ViewType, ViewID: query.ViewID,
		AfterCursor: query.AfterCursor, Limit: reader.replayLimit,
	})
	if err != nil {
		return ResumeResult{}, fmt.Errorf("load View replay window: %w", err)
	}
	if window.Head.TenantID != query.Viewer.TenantID || window.Head.UserID != query.Viewer.UserID ||
		window.Head.ViewType != query.ViewType || window.Head.ViewID != query.ViewID {
		return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_head_identity"}
	}
	if window.Head.ViewEpoch == 0 || window.Head.ProjectionVersion == 0 {
		return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_head_position"}
	}
	if window.Head.SchemaVersion != ProjectionSnapshotSchemaVersion {
		return continuityControl("client_upgrade_required", query, window.Head, ContinuitySchemaIncompatible), nil
	}
	if query.AfterEpoch != window.Head.ViewEpoch {
		return continuityControl("resync_required", query, window.Head, ContinuityEpochMismatch), nil
	}
	if query.AfterCursor > window.Head.ViewCursor {
		return continuityControl("resync_required", query, window.Head, ContinuityCursorFuture), nil
	}
	if window.EarliestCursor != nil && (*window.EarliestCursor == 0 || *window.EarliestCursor > window.Head.ViewCursor) {
		return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_replay_window"}
	}
	if query.AfterCursor == window.Head.ViewCursor {
		if len(window.Deltas) != 0 {
			return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_replay_window"}
		}
		return ResumeResult{Deltas: []ProjectionDelta{}}, nil
	}

	// The prior comparison proves incrementing the requested cursor cannot wrap.
	nextCursor := query.AfterCursor + 1
	if window.EarliestCursor != nil && nextCursor < *window.EarliestCursor {
		return continuityControl("resync_required", query, window.Head, ContinuityWindowEvicted), nil
	}
	if len(window.Deltas) > reader.replayLimit {
		return ResumeResult{}, ProjectionIntegrityError{Reason: "replay_limit_exceeded"}
	}
	distance := window.Head.ViewCursor - query.AfterCursor
	if window.Head.ProjectionVersion < distance {
		return ResumeResult{}, ProjectionIntegrityError{Reason: "head_version_underflow"}
	}
	expectedBase := window.Head.ProjectionVersion - distance
	if len(window.Deltas) == 0 {
		return continuityControl("resync_required", query, window.Head, ContinuityBehindEmpty), nil
	}
	previousCursor := query.AfterCursor
	previousResult := expectedBase
	for index, delta := range window.Deltas {
		if !validStoredDelta(delta) || delta.ViewCursor > window.Head.ViewCursor {
			return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_delta_invalid"}
		}
		if delta.ViewType != query.ViewType || delta.ViewID != query.ViewID || delta.ViewEpoch != window.Head.ViewEpoch {
			return continuityControl("resync_required", query, window.Head, ContinuityCursorGap), nil
		}
		if previousCursor == ^uint64(0) || delta.ViewCursor != previousCursor+1 {
			return continuityControl("resync_required", query, window.Head, ContinuityCursorGap), nil
		}
		if delta.BaseProjectionVersion == ^uint64(0) {
			return ResumeResult{}, ProjectionIntegrityError{Reason: "stored_delta_invalid"}
		}
		if delta.BaseProjectionVersion != previousResult ||
			delta.ResultProjectionVersion != delta.BaseProjectionVersion+1 {
			return continuityControl("resync_required", query, window.Head, ContinuityVersionGap), nil
		}
		if index == 0 && delta.BaseProjectionVersion != expectedBase {
			return continuityControl("resync_required", query, window.Head, ContinuityVersionGap), nil
		}
		previousCursor = delta.ViewCursor
		previousResult = delta.ResultProjectionVersion
	}
	if previousCursor == window.Head.ViewCursor && previousResult != window.Head.ProjectionVersion {
		return continuityControl("resync_required", query, window.Head, ContinuityVersionGap), nil
	}
	return ResumeResult{Deltas: window.Deltas}, nil
}

func validStoredDelta(delta ProjectionDelta) bool {
	return delta.SchemaVersion == ProjectionDeltaSchemaVersion && canonical(delta.DeltaID) &&
		validViewType(delta.ViewType) && canonical(delta.ViewID) && canonical(delta.EventType) && jsonObject(delta.Payload)
}

func continuityControl(
	eventType string,
	query ResumeQuery,
	head StoredViewHead,
	reason ContinuityReason,
) ResumeResult {
	return ResumeResult{
		Control: &ProjectionControlEvent{
			SchemaVersion:  ProjectionControlSchemaVersion,
			EventType:      eventType,
			ViewType:       query.ViewType,
			ViewID:         query.ViewID,
			ViewEpoch:      head.ViewEpoch,
			SnapshotCursor: head.ViewCursor,
		},
		Reason: reason,
	}
}
