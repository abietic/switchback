package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type continuityStoreStub struct {
	snapshotQuery SnapshotStoreQuery
	snapshotState StoredSnapshotState
	snapshotErr   error
	replayQuery   ReplayStoreQuery
	replayWindow  StoredReplayWindow
	replayErr     error
}

func (stub *continuityStoreStub) LoadSnapshotState(
	_ context.Context,
	query SnapshotStoreQuery,
) (StoredSnapshotState, error) {
	stub.snapshotQuery = query
	return stub.snapshotState, stub.snapshotErr
}

func (stub *continuityStoreStub) LoadReplayWindow(
	_ context.Context,
	query ReplayStoreQuery,
) (StoredReplayWindow, error) {
	stub.replayQuery = query
	return stub.replayWindow, stub.replayErr
}

func TestNewViewContinuityReaderValidatesDependenciesAndReplayLimit(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	store := &continuityStoreStub{}
	for _, test := range []struct {
		name    string
		store   ContinuityStore
		now     func() time.Time
		limit   int
		wantErr bool
	}{
		{name: "nil store", now: clock, wantErr: true},
		{name: "nil clock", store: store, wantErr: true},
		{name: "negative limit", store: store, now: clock, limit: -1, wantErr: true},
		{name: "limit above maximum", store: store, now: clock, limit: 1001, wantErr: true},
		{name: "default limit", store: store, now: clock},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewViewContinuityReader(test.store, ViewContinuityReaderOptions{
				Now: test.now, ReplayLimit: test.limit,
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("NewViewContinuityReader error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewViewContinuityReader error = %v", err)
			}
			stub := test.store.(*continuityStoreStub)
			stub.replayWindow = testReplayWindow(1, 4, 7)
			_, err = reader.Resume(context.Background(), ResumeQuery{
				Viewer: Viewer{TenantID: "tenant-1", UserID: "user-1"}, ViewType: ViewHomeDashboard, ViewID: "view-1",
				AfterEpoch: 1, AfterCursor: 4,
			})
			if err != nil {
				t.Fatalf("Resume error = %v", err)
			}
			if stub.replayQuery.Limit != DefaultProjectionReplayLimit {
				t.Fatalf("stored replay limit = %d, want %d", stub.replayQuery.Limit, DefaultProjectionReplayLimit)
			}
		})
	}
}

func TestViewContinuityReaderReadSnapshotAnchorsReceiptsAndGeneratedTime(t *testing.T) {
	generatedAt := time.Date(2026, 8, 24, 12, 0, 0, 123, time.FixedZone("test", -7*60*60))
	store := &continuityStoreStub{snapshotState: StoredSnapshotState{
		View: testContinuityView(),
		CommandReceipts: []CommandReceipt{
			{ClientCommandID: "client-a", CommandID: "command-1"},
			{ClientCommandID: "client-b", CommandID: "command-2"},
		},
	}}
	reader := newContinuityReader(t, store, func() time.Time { return generatedAt }, 0)
	query := SnapshotQuery{
		Viewer: Viewer{TenantID: "tenant-1", UserID: "user-1"}, ViewType: ViewHomeDashboard, ViewID: "view-1",
		ReconcileClientCommandIDs: []string{"client-b", "client-a", "client-b"},
	}
	snapshot, err := reader.ReadSnapshot(context.Background(), query)
	if err != nil {
		t.Fatalf("ReadSnapshot error = %v", err)
	}
	if !reflect.DeepEqual(store.snapshotQuery.ReconcileClientCommandIDs, []string{"client-a", "client-b"}) {
		t.Fatalf("stored reconciliation IDs = %#v", store.snapshotQuery.ReconcileClientCommandIDs)
	}
	if got, want := store.snapshotQuery.ReceiptNotBefore, generatedAt.UTC().Add(-CommandReceiptRetentionHorizon); !got.Equal(want) {
		t.Fatalf("receipt not before = %s, want %s", got, want)
	}
	if got, want := snapshot.GeneratedAt, generatedAt.UTC(); !got.Equal(want) {
		t.Fatalf("generated at = %s, want %s", got, want)
	}
	if !reflect.DeepEqual(snapshot.CommandReceipts, []CommandReceipt{
		{ClientCommandID: "client-a", CommandID: "command-1"},
		{ClientCommandID: "client-b", CommandID: "command-2"},
	}) {
		t.Fatalf("receipts = %#v", snapshot.CommandReceipts)
	}

	store.snapshotState.CommandReceipts = nil
	withoutReconciliation, err := reader.ReadSnapshot(context.Background(), SnapshotQuery{
		Viewer: query.Viewer, ViewType: query.ViewType, ViewID: query.ViewID,
	})
	if err != nil {
		t.Fatalf("ReadSnapshot without reconciliation error = %v", err)
	}
	if withoutReconciliation.CommandReceipts != nil {
		t.Fatalf("absent reconciliation receipts = %#v, want nil", withoutReconciliation.CommandReceipts)
	}

	emptyReconciliation, err := reader.ReadSnapshot(context.Background(), SnapshotQuery{
		Viewer: query.Viewer, ViewType: query.ViewType, ViewID: query.ViewID, ReconcileClientCommandIDs: []string{},
	})
	if err != nil {
		t.Fatalf("ReadSnapshot with empty reconciliation error = %v", err)
	}
	if emptyReconciliation.CommandReceipts == nil || len(emptyReconciliation.CommandReceipts) != 0 {
		t.Fatalf("empty reconciliation receipts = %#v, want non-nil empty", emptyReconciliation.CommandReceipts)
	}
}

func TestViewContinuityReaderReadSnapshotRejectsUntrustedStoredState(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	storageErr := errors.New("storage unavailable")
	for _, test := range []struct {
		name       string
		state      StoredSnapshotState
		now        func() time.Time
		storeErr   error
		query      SnapshotQuery
		wantReason string
		wantErr    error
	}{
		{name: "crossed tenant", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.TenantID = "tenant-2" })}, now: clock, wantReason: "stored_snapshot_identity"},
		{name: "crossed user", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.UserID = "user-2" })}, now: clock, wantReason: "stored_snapshot_identity"},
		{name: "crossed view", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.ViewID = "view-2" })}, now: clock, wantReason: "stored_snapshot_identity"},
		{name: "wrong schema", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.SchemaVersion = "v2" })}, now: clock, wantReason: "stored_snapshot_schema"},
		{name: "zero epoch", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.ViewEpoch = 0 })}, now: clock, wantReason: "stored_snapshot_position"},
		{name: "zero version", state: StoredSnapshotState{View: testContinuityViewWith(func(view *ViewProjection) { view.ProjectionVersion = 0 })}, now: clock, wantReason: "stored_snapshot_position"},
		{name: "conflicting receipts", state: StoredSnapshotState{View: testContinuityView(), CommandReceipts: []CommandReceipt{{ClientCommandID: "client-1", CommandID: "command-a"}, {ClientCommandID: "client-1", CommandID: "command-b"}}}, now: clock, query: SnapshotQuery{ReconcileClientCommandIDs: []string{"client-1"}}, wantReason: "stored_snapshot_receipts"},
		{name: "zero clock", state: StoredSnapshotState{View: testContinuityView()}, now: func() time.Time { return time.Time{} }, wantReason: "clock_zero"},
		{name: "storage error", state: StoredSnapshotState{View: testContinuityView()}, now: clock, storeErr: storageErr, wantErr: storageErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &continuityStoreStub{snapshotState: test.state, snapshotErr: test.storeErr}
			reader := newContinuityReader(t, store, test.now, 0)
			query := test.query
			query.Viewer = Viewer{TenantID: "tenant-1", UserID: "user-1"}
			query.ViewType = ViewHomeDashboard
			query.ViewID = "view-1"
			_, err := reader.ReadSnapshot(context.Background(), query)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want wrapped %v", err, test.wantErr)
				}
				return
			}
			assertProjectionIntegrity(t, err, test.wantReason)
		})
	}
}

func TestViewContinuityReaderResumeClassifiesAnchoredWindows(t *testing.T) {
	defaultQuery := ResumeQuery{
		Viewer: Viewer{TenantID: "tenant-1", UserID: "user-1"}, ViewType: ViewHomeDashboard, ViewID: "view-1",
		AfterEpoch: 1, AfterCursor: 4,
	}
	for _, test := range []struct {
		name          string
		query         ResumeQuery
		window        StoredReplayWindow
		wantCursors   []uint64
		wantControl   string
		wantReason    ContinuityReason
		wantIntegrity string
	}{
		{name: "caught up", window: testReplayWindow(1, 4, 7), wantCursors: []uint64{}},
		{name: "retained prefix", window: testReplayWindow(1, 6, 9, testContinuityDelta(5, 7), testContinuityDelta(6, 8)), wantCursors: []uint64{5, 6}},
		{name: "page prefix", window: testReplayWindow(1, 8, 11, testContinuityDelta(5, 7), testContinuityDelta(6, 8)), wantCursors: []uint64{5, 6}},
		{name: "old epoch", window: testReplayWindow(2, 4, 7), wantControl: "resync_required", wantReason: ContinuityEpochMismatch},
		{name: "future cursor", query: testResumeQuery(defaultQuery, func(query *ResumeQuery) { query.AfterCursor = 5 }), window: testReplayWindow(1, 4, 7), wantControl: "resync_required", wantReason: ContinuityCursorFuture},
		{name: "evicted", window: testReplayWindowWithEarliest(1, 6, 9, 6), wantControl: "resync_required", wantReason: ContinuityWindowEvicted},
		{name: "behind empty", window: testReplayWindow(1, 5, 8), wantControl: "resync_required", wantReason: ContinuityBehindEmpty},
		{name: "first cursor gap", window: testReplayWindow(1, 6, 9, testContinuityDelta(6, 8)), wantControl: "resync_required", wantReason: ContinuityCursorGap},
		{name: "internal cursor gap", window: testReplayWindow(1, 7, 10, testContinuityDelta(5, 7), testContinuityDelta(7, 8)), wantControl: "resync_required", wantReason: ContinuityCursorGap},
		{name: "first base gap", window: testReplayWindow(1, 6, 9, testContinuityDelta(5, 6)), wantControl: "resync_required", wantReason: ContinuityVersionGap},
		{name: "internal base gap", window: testReplayWindow(1, 6, 9, testContinuityDelta(5, 7), testContinuityDelta(6, 9)), wantControl: "resync_required", wantReason: ContinuityVersionGap},
		{name: "row identity crossover", window: testReplayWindow(1, 5, 8, testContinuityDeltaWith(5, 7, func(delta *ProjectionDelta) { delta.ViewID = "view-2" })), wantControl: "resync_required", wantReason: ContinuityCursorGap},
		{name: "row epoch crossover", window: testReplayWindow(1, 5, 8, testContinuityDeltaWith(5, 7, func(delta *ProjectionDelta) { delta.ViewEpoch = 2 })), wantControl: "resync_required", wantReason: ContinuityCursorGap},
		{name: "incompatible schema", window: testReplayWindowWithSchema("v2", 1, 4, 7), wantControl: "client_upgrade_required", wantReason: ContinuitySchemaIncompatible},
		{name: "impossible head distance", window: testReplayWindow(1, 8, 2), wantIntegrity: "head_version_underflow"},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := test.query
			if query == (ResumeQuery{}) {
				query = defaultQuery
			}
			store := &continuityStoreStub{replayWindow: test.window}
			reader := newContinuityReader(t, store, fixedContinuityClock, 2)
			result, err := reader.Resume(context.Background(), query)
			if test.wantIntegrity != "" {
				assertProjectionIntegrity(t, err, test.wantIntegrity)
				return
			}
			if err != nil {
				t.Fatalf("Resume error = %v", err)
			}
			if got, want := store.replayQuery, (ReplayStoreQuery{Viewer: query.Viewer, ViewType: query.ViewType, ViewID: query.ViewID, AfterCursor: query.AfterCursor, Limit: 2}); got != want {
				t.Fatalf("replay query = %#v, want %#v", got, want)
			}
			if test.wantControl != "" {
				if result.Control == nil || result.Control.EventType != test.wantControl || result.Reason != test.wantReason {
					t.Fatalf("resume control = %#v, reason = %q, want %s/%q", result.Control, result.Reason, test.wantControl, test.wantReason)
				}
				if result.Control.SchemaVersion != ProjectionControlSchemaVersion || result.Control.ViewType != query.ViewType || result.Control.ViewID != query.ViewID || result.Control.ViewEpoch != test.window.Head.ViewEpoch || result.Control.SnapshotCursor != test.window.Head.ViewCursor {
					t.Fatalf("control = %#v, want anchored control", result.Control)
				}
				if result.Deltas != nil {
					t.Fatalf("control deltas = %#v, want nil", result.Deltas)
				}
				return
			}
			if result.Control != nil || result.Reason != "" {
				t.Fatalf("resume result = %#v, want deltas only", result)
			}
			if result.Deltas == nil {
				t.Fatal("deltas = nil, want non-nil")
			}
			gotCursors := make([]uint64, len(result.Deltas))
			for index, delta := range result.Deltas {
				gotCursors[index] = delta.ViewCursor
			}
			if !reflect.DeepEqual(gotCursors, test.wantCursors) {
				t.Fatalf("delta cursors = %#v, want %#v", gotCursors, test.wantCursors)
			}
		})
	}
}

func TestViewContinuityReaderResumeRejectsInvalidAndUntrustedWindows(t *testing.T) {
	query := ResumeQuery{
		Viewer: Viewer{TenantID: "tenant-1", UserID: "user-1"}, ViewType: ViewHomeDashboard, ViewID: "view-1",
		AfterEpoch: 1, AfterCursor: 4,
	}
	storageErr := errors.New("storage unavailable")
	for _, test := range []struct {
		name       string
		query      ResumeQuery
		limit      int
		window     StoredReplayWindow
		storeErr   error
		wantErr    error
		wantReason string
	}{
		{name: "invalid query", query: testResumeQuery(query, func(query *ResumeQuery) { query.AfterEpoch = 0 }), wantErr: ErrInvalidViewQuery},
		{name: "more rows than limit", query: query, limit: 1, window: testReplayWindow(1, 6, 9, testContinuityDelta(5, 7), testContinuityDelta(6, 8)), wantReason: "replay_limit_exceeded"},
		{name: "malformed delta schema", query: query, window: testReplayWindow(1, 5, 8, testContinuityDeltaWith(5, 7, func(delta *ProjectionDelta) { delta.SchemaVersion = "v2" })), wantReason: "stored_delta_invalid"},
		{name: "malformed delta identity", query: query, window: testReplayWindow(1, 5, 8, testContinuityDeltaWith(5, 7, func(delta *ProjectionDelta) { delta.ViewID = "" })), wantReason: "stored_delta_invalid"},
		{name: "malformed delta payload", query: query, window: testReplayWindow(1, 5, 8, testContinuityDeltaWith(5, 7, func(delta *ProjectionDelta) { delta.Payload = json.RawMessage(`[]`) })), wantReason: "stored_delta_invalid"},
		{name: "non representable delta result version", query: query, window: testReplayWindow(1, 5, ^uint64(0), testContinuityDeltaWith(5, ^uint64(0), func(delta *ProjectionDelta) { delta.ResultProjectionVersion = 0 })), wantReason: "stored_delta_invalid"},
		{name: "storage error", query: query, window: testReplayWindow(1, 4, 7), storeErr: storageErr, wantErr: storageErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &continuityStoreStub{replayWindow: test.window, replayErr: test.storeErr}
			reader := newContinuityReader(t, store, fixedContinuityClock, test.limit)
			result, err := reader.Resume(context.Background(), test.query)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want wrapped %v", err, test.wantErr)
				}
				return
			}
			if err == nil && result.Control != nil {
				t.Fatalf("Resume returned control %s/%q, want projection integrity error", result.Control.EventType, result.Reason)
			}
			assertProjectionIntegrity(t, err, test.wantReason)
		})
	}
}

func testContinuityView() ViewProjection {
	return ViewProjection{
		SchemaVersion: ProjectionSnapshotSchemaVersion, TenantID: "tenant-1", UserID: "user-1",
		ViewType: ViewHomeDashboard, ViewID: "view-1", ViewEpoch: 1, ViewCursor: 4,
		ProjectionVersion: 7, QueryFingerprint: "fingerprint-1", Data: json.RawMessage(`{"items":[]}`),
		AvailableActions: []ActionRef{}, RelatedRefs: []RelatedRef{},
	}
}

func testContinuityViewWith(change func(*ViewProjection)) ViewProjection {
	view := testContinuityView()
	change(&view)
	return view
}

func fixedContinuityClock() time.Time {
	return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
}

func testReplayWindow(epoch, cursor, version uint64, deltas ...ProjectionDelta) StoredReplayWindow {
	return testReplayWindowWithSchema(ProjectionSnapshotSchemaVersion, epoch, cursor, version, deltas...)
}

func testReplayWindowWithSchema(schema string, epoch, cursor, version uint64, deltas ...ProjectionDelta) StoredReplayWindow {
	return StoredReplayWindow{Head: StoredViewHead{
		SchemaVersion: schema, TenantID: "tenant-1", UserID: "user-1", ViewType: ViewHomeDashboard, ViewID: "view-1",
		ViewEpoch: epoch, ViewCursor: cursor, ProjectionVersion: version,
	}, Deltas: deltas}
}

func testReplayWindowWithEarliest(epoch, cursor, version, earliest uint64, deltas ...ProjectionDelta) StoredReplayWindow {
	window := testReplayWindow(epoch, cursor, version, deltas...)
	window.EarliestCursor = &earliest
	return window
}

func testContinuityDelta(cursor, base uint64) ProjectionDelta {
	return testContinuityDeltaWith(cursor, base, func(*ProjectionDelta) {})
}

func testContinuityDeltaWith(cursor, base uint64, change func(*ProjectionDelta)) ProjectionDelta {
	delta := ProjectionDelta{
		SchemaVersion: ProjectionDeltaSchemaVersion, DeltaID: "delta-" + string(rune('a'+cursor)),
		ViewType: ViewHomeDashboard, ViewID: "view-1", ViewEpoch: 1, ViewCursor: cursor,
		BaseProjectionVersion: base, ResultProjectionVersion: base + 1,
		EventType: "view.entity_upserted.v1", SourceRefs: []SourceRef{}, Payload: json.RawMessage(`{"items":[]}`),
	}
	change(&delta)
	return delta
}

func testResumeQuery(query ResumeQuery, change func(*ResumeQuery)) ResumeQuery {
	change(&query)
	return query
}

func newContinuityReader(t *testing.T, store ContinuityStore, now func() time.Time, replayLimit int) *ViewContinuityReader {
	t.Helper()
	reader, err := NewViewContinuityReader(store, ViewContinuityReaderOptions{Now: now, ReplayLimit: replayLimit})
	if err != nil {
		t.Fatalf("NewViewContinuityReader error = %v", err)
	}
	return reader
}

func assertProjectionIntegrity(t *testing.T, err error, wantReason string) {
	t.Helper()
	if !errors.Is(err, ErrProjectionIntegrity) {
		t.Fatalf("error = %v, want projection integrity error", err)
	}
	var integrityErr ProjectionIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.Reason != wantReason {
		t.Fatalf("integrity error = %v, want reason %q", err, wantReason)
	}
}
