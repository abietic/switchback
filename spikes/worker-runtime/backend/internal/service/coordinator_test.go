package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runstate"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
)

func TestCreateRunDetachesAcceptedWorkFromRequestContext(t *testing.T) {
	scheduler := newRecordingScheduler()
	coordinator := newTestCoordinator(t, scheduler)
	requestContext, cancel := context.WithCancel(context.Background())

	created, err := coordinator.CreateRun(requestContext, CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
		Constraints:        []string{"stay scoped"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	cancel()

	select {
	case call := <-scheduler.initial:
		if call.Context.Err() != nil {
			t.Fatalf("initial command context was cancelled with request: %v", call.Context.Err())
		}
		if call.Work.workerSessionID != created.WorkerSessionID || call.Work.turnID != created.TurnID {
			t.Fatalf("initial command IDs = session %q turn %q, want session %q turn %q", call.Work.workerSessionID, call.Work.turnID, created.WorkerSessionID, created.TurnID)
		}
		if call.Work.goal != "fix the fixture" || len(call.Work.acceptanceCriteria) != 1 || call.Work.acceptanceCriteria[0] != "tests pass" {
			t.Fatal("initial command lost goal or acceptance context")
		}
		if len(call.Work.constraints) != 1 || call.Work.constraints[0] != "stay scoped" || call.Work.message != "" {
			t.Fatal("initial command lost constraint or message context")
		}
		assertControlledHandoff(t, call.Work, created.WorkerSessionID)
	case <-context.Background().Done():
		t.Fatal("unreachable")
	}
}

func TestCreateTurnRequiresExactIdleSessionAndSchedulesOnce(t *testing.T) {
	scheduler := newRecordingScheduler()
	coordinator := newTestCoordinator(t, scheduler)
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "next change"); !IsConflict(err) {
		t.Fatalf("CreateTurn() before first turn completion error = %v, want conflict", err)
	}

	makeFirstTurnIdle(t, coordinator, created)
	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "add a boundary test")
	if err != nil {
		t.Fatalf("CreateTurn() error = %v", err)
	}
	if second.TurnID == created.TurnID {
		t.Fatalf("second turn ID = %q, want a new ID", second.TurnID)
	}
	if _, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "competing turn"); !IsConflict(err) {
		t.Fatalf("CreateTurn() with accepted pending turn error = %v, want conflict", err)
	}
	if _, err := coordinator.CreateTurn(context.Background(), "another-session", "wrong target"); !IsNotFound(err) {
		t.Fatalf("CreateTurn() wrong session error = %v, want not found", err)
	}

	select {
	case call := <-scheduler.turn:
		if call.Work.workerRunID != created.WorkerRunID || call.Work.turnID != second.TurnID {
			t.Fatalf("turn command IDs = run %q turn %q, want run %q turn %q", call.Work.workerRunID, call.Work.turnID, created.WorkerRunID, second.TurnID)
		}
		if call.Work.message != "add a boundary test" {
			t.Fatalf("turn command message = %q, want trimmed user message", call.Work.message)
		}
		assertControlledHandoff(t, call.Work, created.WorkerSessionID)
	case <-context.Background().Done():
		t.Fatal("unreachable")
	}
}

func TestSnapshotIsImmutableAndCleanupIsIdempotent(t *testing.T) {
	scheduler := newRecordingScheduler()
	coordinator := newTestCoordinator(t, scheduler)
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	first, err := coordinator.Snapshot(created.WorkerRunID)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	first.Turns = append(first.Turns, TurnSnapshot{ID: "mutated"})
	first.Goal = "mutated"
	first.AcceptanceCriteria[0] = "mutated"
	second, err := coordinator.Snapshot(created.WorkerRunID)
	if err != nil {
		t.Fatalf("Snapshot() second error = %v", err)
	}
	if second.Goal != "fix the fixture" || len(second.AcceptanceCriteria) != 1 || second.AcceptanceCriteria[0] != "tests pass" || len(second.Turns) != 0 || second.ExecutionOutcome != string(runstate.ExecutionPending) || second.SpikeAcceptance != string(runstate.AcceptancePending) {
		t.Fatalf("second snapshot = %+v, want isolated pending public state", second)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	if err := coordinator.Cleanup(requestContext, created.WorkerSessionID); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	cancel()

	const callers = 11
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := coordinator.Cleanup(context.Background(), created.WorkerSessionID); err != nil {
				t.Errorf("Cleanup() error = %v", err)
			}
		}()
	}
	group.Wait()
	call := <-scheduler.cleanup
	if call.Context.Err() != nil {
		t.Fatalf("cleanup command context was cancelled with request: %v", call.Context.Err())
	}
	if got := scheduler.cleanupCalls(); got != 1 {
		t.Fatalf("cleanup calls = %d, want exactly one", got)
	}
	assertControlledHandoff(t, call.Work, created.WorkerSessionID)
}

func TestCreateRunRollsBackOnlyNewMaterializationOnDuplicateGeneratedRunID(t *testing.T) {
	materializer := newTrackingMaterializer()
	identifiers := []string{
		"session-accepted", "run-duplicate", "turn-accepted", "runtime-accepted", "trace-accepted",
		"session-new", "run-duplicate", "turn-new", "runtime-new", "trace-new",
	}
	coordinator := NewCoordinator(Options{
		RootContext:  context.Background(),
		Materializer: materializer,
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "worker-token", nil },
	})

	accepted, err := coordinator.CreateRun(context.Background(), CreateRunInput{Goal: "first", AcceptanceCriteria: []string{"pass"}})
	if err != nil {
		t.Fatalf("first CreateRun() error = %v", err)
	}
	if _, err := coordinator.CreateRun(context.Background(), CreateRunInput{Goal: "second", AcceptanceCriteria: []string{"pass"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second CreateRun() error = %v, want duplicate conflict", err)
	}
	if !materializer.isActive(accepted.WorkerSessionID) {
		t.Fatalf("accepted session %q was rolled back", accepted.WorkerSessionID)
	}
	if materializer.isActive("session-new") {
		t.Fatal("newly materialized duplicate run session was not rolled back")
	}
	if got := materializer.rollbackIDs(); len(got) != 1 || got[0] != "session-new" {
		t.Fatalf("rollback IDs = %v, want only session-new", got)
	}
	if _, err := coordinator.Snapshot(accepted.WorkerRunID); err != nil {
		t.Fatalf("accepted prior run disappeared after compensation: %v", err)
	}
}

func TestCreateRunJoinsRollbackFailureWithRegistrationFailure(t *testing.T) {
	rollbackErr := errors.New("rollback failed")
	materializer := newTrackingMaterializer()
	materializer.rollbackErr = rollbackErr
	identifiers := []string{
		"session-accepted", "run-duplicate", "turn-accepted", "runtime-accepted", "trace-accepted",
		"session-new", "run-duplicate", "turn-new", "runtime-new", "trace-new",
	}
	coordinator := NewCoordinator(Options{
		Materializer: materializer,
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "worker-token", nil },
	})
	input := CreateRunInput{Goal: "fix", AcceptanceCriteria: []string{"pass"}}
	if _, err := coordinator.CreateRun(context.Background(), input); err != nil {
		t.Fatalf("first CreateRun() error = %v", err)
	}
	_, err := coordinator.CreateRun(context.Background(), input)
	if !errors.Is(err, ErrConflict) || !errors.Is(err, rollbackErr) {
		t.Fatalf("second CreateRun() error = %v, want joined conflict and rollback failure", err)
	}
}

func TestCreateRunDoesNotRollbackMaterializationThatWasRejected(t *testing.T) {
	materializeErr := errors.New("session tree already exists")
	materializer := &rejectingMaterializer{err: materializeErr}
	coordinator := NewCoordinator(Options{
		Materializer: materializer,
		NewID:        func() string { return "generated-id" },
		NewToken:     func() (string, error) { return "worker-token", nil },
	})
	_, err := coordinator.CreateRun(context.Background(), CreateRunInput{Goal: "fix", AcceptanceCriteria: []string{"pass"}})
	if !errors.Is(err, materializeErr) {
		t.Fatalf("CreateRun() error = %v, want materialization rejection", err)
	}
	if materializer.rollbackCalled {
		t.Fatal("rollback was called after Materialize rejected a pre-existing session")
	}
}

func TestStoreEvidenceDoesNotHoldCoordinatorLockDuringPutAndRevalidatesCleanup(t *testing.T) {
	store := &blockingEvidenceStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	scheduler := newRecordingScheduler()
	identifiers := []string{"session-1", "run-1", "turn-1", "runtime-1", "trace-1"}
	coordinator := NewCoordinator(Options{
		Materializer: fakeMaterializer{},
		ObjectStore:  store,
		Scheduler:    scheduler,
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "test-worker-token", nil },
	})
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{Goal: "fix", AcceptanceCriteria: []string{"pass"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	authorization, err := coordinator.AuthorizeEvidenceUpload(created.WorkerSessionID, created.WorkerRunID, created.TurnID, "test-worker-token")
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	storeResult := make(chan error, 1)
	go func() {
		_, err := coordinator.StoreEvidence(context.Background(), authorization, objectstore.EvidenceTrace, []byte("contains test-worker-token"))
		storeResult <- err
	}()
	<-store.started

	operationDone := make(chan error, 2)
	go func() {
		_, err := coordinator.Snapshot(created.WorkerRunID)
		operationDone <- err
	}()
	go func() { operationDone <- coordinator.Cleanup(context.Background(), created.WorkerSessionID) }()
	for range 2 {
		select {
		case err := <-operationDone:
			if err != nil {
				t.Fatalf("operation while Put blocked failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Snapshot/Cleanup blocked behind EvidenceStore.Put")
		}
	}
	close(store.release)
	if err := <-storeResult; !errors.Is(err, ErrForbidden) {
		t.Fatalf("StoreEvidence() after concurrent cleanup error = %v, want forbidden revalidation", err)
	}
	if got := string(store.payload()); got != "contains [REDACTED:worker_token]" {
		t.Fatalf("EvidenceStore payload = %q, want dynamic token redaction", got)
	}
	coordinator.mu.RLock()
	evidenceCount := len(coordinator.byRun[created.WorkerRunID].evidence)
	coordinator.mu.RUnlock()
	if evidenceCount != 0 {
		t.Fatalf("retained evidence refs = %d after cleanup won revalidation, want 0", evidenceCount)
	}
}

func TestModelObservationRequiresExactActiveTurn(t *testing.T) {
	_, coordinator, created := newManifestTestCoordinator(t)
	identity := manifestTestIdentity(t, coordinator, created.WorkerRunID)
	observation := &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			Identity: &workercontrolv1.Identity{AgentTurnId: created.TurnID},
		},
		Payload: &workercontrolv1.WorkerEnvelope_ModelObservation{
			ModelObservation: &workercontrolv1.ModelObservation{
				ModelObservationId: "observation-1",
				Model:              "codex",
				Route:              "codex-primary",
				StartedAt:          timestamppb.New(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)),
				EndedAt:            timestamppb.New(time.Date(2026, 7, 17, 10, 0, 1, 0, time.UTC)),
				LatencyMs:          1000,
				InputTokens:        10,
				OutputTokens:       2,
				TotalTokens:        12,
				CostUsd:            0.001,
				Status:             "ok",
				MetadataJson:       []byte(`{"request_id":"observation-1"}`),
			},
		},
	}
	if disposition, err := coordinator.HandleWorkerEvent(
		context.Background(),
		identity,
		observation,
	); !errors.Is(err, ErrConflict) || disposition != control.EventDispositionInvalid {
		t.Fatalf(
			"pending-turn ModelObservation disposition=%q error=%v, want invalid conflict",
			disposition,
			err,
		)
	}

	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	if disposition, err := coordinator.HandleWorkerEvent(
		context.Background(),
		identity,
		observation,
	); err != nil || disposition != control.EventDispositionAuthoritative {
		t.Fatalf(
			"active-turn ModelObservation disposition=%q error=%v, want authoritative",
			disposition,
			err,
		)
	}

	observation.Meta.Identity.AgentTurnId = "other-turn"
	if disposition, err := coordinator.HandleWorkerEvent(
		context.Background(),
		identity,
		observation,
	); !errors.Is(err, ErrConflict) || disposition != control.EventDispositionInvalid {
		t.Fatalf(
			"wrong-turn ModelObservation disposition=%q error=%v, want invalid conflict",
			disposition,
			err,
		)
	}
}

func TestEvidenceSlotExactRetryReturnsOriginalRefWithoutSecondPut(t *testing.T) {
	store := &countingEvidenceStore{}
	coordinator, created := newEvidenceSlotTestCoordinator(t, store)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		created.WorkerSessionID,
		created.WorkerRunID,
		created.TurnID,
		"test-worker-token",
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	trees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	first, err := coordinator.StoreEvidenceWithTrees(
		context.Background(),
		authorization,
		objectstore.EvidenceReport,
		[]byte("stable report"),
		trees,
	)
	if err != nil {
		t.Fatalf("first StoreEvidenceWithTrees() error = %v", err)
	}
	retry, err := coordinator.StoreEvidenceWithTrees(
		context.Background(),
		authorization,
		objectstore.EvidenceReport,
		[]byte("stable report"),
		trees,
	)
	if err != nil {
		t.Fatalf("retry StoreEvidenceWithTrees() error = %v", err)
	}
	if retry != first {
		t.Fatalf("retry ref = %+v, want original %+v", retry, first)
	}
	if calls := store.callCount(); calls != 1 {
		t.Fatalf("EvidenceStore.Put calls = %d, want exactly 1", calls)
	}
}

func TestEvidenceSlotRejectsPayloadOrTreeConflictBeforePut(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		trees   sharedevidence.TreePair
	}{
		{
			name:    "different payload",
			payload: []byte("different report"),
			trees: sharedevidence.TreePair{
				BeforeTree: strings.Repeat("a", 40),
				AfterTree:  strings.Repeat("b", 40),
			},
		},
		{
			name:    "different tree pair",
			payload: []byte("stable report"),
			trees: sharedevidence.TreePair{
				BeforeTree: strings.Repeat("a", 40),
				AfterTree:  strings.Repeat("c", 40),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &countingEvidenceStore{}
			coordinator, created := newEvidenceSlotTestCoordinator(t, store)
			startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
			authorization, err := coordinator.AuthorizeEvidenceUpload(
				created.WorkerSessionID,
				created.WorkerRunID,
				created.TurnID,
				"test-worker-token",
			)
			if err != nil {
				t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
			}
			originalTrees := sharedevidence.TreePair{
				BeforeTree: strings.Repeat("a", 40),
				AfterTree:  strings.Repeat("b", 40),
			}
			if _, err := coordinator.StoreEvidenceWithTrees(
				context.Background(),
				authorization,
				objectstore.EvidenceReport,
				[]byte("stable report"),
				originalTrees,
			); err != nil {
				t.Fatalf("first StoreEvidenceWithTrees() error = %v", err)
			}
			if _, err := coordinator.StoreEvidenceWithTrees(
				context.Background(),
				authorization,
				objectstore.EvidenceReport,
				test.payload,
				test.trees,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("conflicting StoreEvidenceWithTrees() error = %v, want conflict", err)
			}
			if calls := store.callCount(); calls != 1 {
				t.Fatalf("EvidenceStore.Put calls = %d after conflict, want exactly 1", calls)
			}
		})
	}
}

func TestEvidenceSlotConcurrentExactRetryUsesOnePut(t *testing.T) {
	store := &gatedEvidenceStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	coordinator, created := newEvidenceSlotTestCoordinator(t, store)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		created.WorkerSessionID,
		created.WorkerRunID,
		created.TurnID,
		"test-worker-token",
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	trees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	results := make(chan objectstore.StoredObject, 2)
	failures := make(chan error, 2)
	storeOnce := func() {
		stored, err := coordinator.StoreEvidenceWithTrees(
			context.Background(),
			authorization,
			objectstore.EvidenceReport,
			[]byte("stable report"),
			trees,
		)
		if err != nil {
			failures <- err
			return
		}
		results <- stored
	}
	go storeOnce()
	<-store.started
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		storeOnce()
	}()
	<-secondStarted
	close(store.release)

	refs := make([]objectstore.StoredObject, 0, 2)
	for range 2 {
		select {
		case err := <-failures:
			t.Fatalf("StoreEvidenceWithTrees() error = %v", err)
		case ref := <-results:
			refs = append(refs, ref)
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent exact evidence retry did not finish")
		}
	}
	if refs[0] != refs[1] {
		t.Fatalf("concurrent refs = %+v, want exact match", refs)
	}
	if calls := store.callCount(); calls != 1 {
		t.Fatalf("EvidenceStore.Put calls = %d, want exactly 1", calls)
	}
}

func TestEvidenceSlotPutFailureReleasesReservationForWaitingRetry(t *testing.T) {
	store := &retryEvidenceStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	coordinator, created := newEvidenceSlotTestCoordinator(t, store)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		created.WorkerSessionID,
		created.WorkerRunID,
		created.TurnID,
		"test-worker-token",
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	trees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	results := make(chan objectstore.StoredObject, 2)
	failures := make(chan error, 2)
	storeOnce := func() {
		stored, err := coordinator.StoreEvidenceWithTrees(
			context.Background(),
			authorization,
			objectstore.EvidenceReport,
			[]byte("stable report"),
			trees,
		)
		if err != nil {
			failures <- err
			return
		}
		results <- stored
	}
	go storeOnce()
	<-store.started
	go storeOnce()
	close(store.release)

	var succeeded objectstore.StoredObject
	var failed error
	for range 2 {
		select {
		case err := <-failures:
			failed = err
		case stored := <-results:
			succeeded = stored
		case <-time.After(2 * time.Second):
			t.Fatal("Put failure retry did not finish")
		}
	}
	if failed == nil || !strings.Contains(failed.Error(), "injected Put failure") {
		t.Fatalf("first StoreEvidenceWithTrees() error = %v, want injected failure", failed)
	}
	if succeeded.Path == "" {
		t.Fatal("waiting exact retry did not persist evidence after reservation release")
	}
	if calls := store.callCount(); calls != 2 {
		t.Fatalf("EvidenceStore.Put calls = %d, want failed attempt plus one retry", calls)
	}
}

func TestEvidenceSlotConflictCannotReplaceStoredObjectOrManifestRef(t *testing.T) {
	root, coordinator, created := newManifestTestCoordinator(t)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		created.WorkerSessionID,
		created.WorkerRunID,
		created.TurnID,
		"test-worker-token",
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	trees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	first, err := coordinator.StoreEvidenceWithTrees(
		context.Background(),
		authorization,
		objectstore.EvidenceReport,
		[]byte("authoritative report"),
		trees,
	)
	if err != nil {
		t.Fatalf("first StoreEvidenceWithTrees() error = %v", err)
	}
	beforeConflict, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read first report: %v", err)
	}
	if _, err := coordinator.StoreEvidenceWithTrees(
		context.Background(),
		authorization,
		objectstore.EvidenceReport,
		[]byte("replacement report"),
		trees,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting StoreEvidenceWithTrees() error = %v, want conflict", err)
	}
	afterConflict, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read report after conflict: %v", err)
	}
	if string(afterConflict) != string(beforeConflict) {
		t.Fatalf("conflicting evidence replaced object bytes: got %q want %q", afterConflict, beforeConflict)
	}

	pointers := []*workercontrolv1.EvidencePointer{{
		Kind:       string(objectstore.EvidenceReport),
		ObjectPath: first.Path,
		Sha256:     first.SHA256,
		SizeBytes:  first.Bytes,
	}}
	pointers = append(pointers, uploadManifestTestEvidence(
		t,
		coordinator,
		created,
		created.TurnID,
		trees,
		[]objectstore.EvidenceKind{
			objectstore.EvidenceRawACP,
			objectstore.EvidenceTrace,
			objectstore.EvidencePatch,
		},
	)...)
	completeManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID, pointers)
	manifest := readTurnManifest(t, root, created)
	if manifest.Objects[0].Kind != string(objectstore.EvidenceReport) ||
		manifest.Objects[0].SHA256 != first.SHA256 {
		t.Fatalf("manifest report ref = %+v, want original SHA %q", manifest.Objects[0], first.SHA256)
	}
}

func TestSuccessfulTurnsAtomicallyReplaceRunLevelLatestManifest(t *testing.T) {
	t.Parallel()

	root, coordinator, created := newManifestTestCoordinator(t)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	firstTrees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	firstPointers := uploadManifestTestEvidence(t, coordinator, created, created.TurnID, firstTrees, nil)
	completeManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID, firstPointers)
	firstManifest := readTurnManifest(t, root, created)
	assertManifestTurn(t, firstManifest, created.TurnID, firstTrees)

	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "second turn")
	if err != nil {
		t.Fatalf("CreateTurn() error = %v", err)
	}
	startManifestTestTurn(t, coordinator, created.WorkerRunID, second.TurnID)
	secondTrees := sharedevidence.TreePair{
		BeforeTree: firstTrees.AfterTree,
		AfterTree:  strings.Repeat("c", 40),
	}
	secondPointers := uploadManifestTestEvidence(t, coordinator, created, second.TurnID, secondTrees, nil)
	completeManifestTestTurn(t, coordinator, created.WorkerRunID, second.TurnID, secondPointers)
	secondManifest := readTurnManifest(t, root, created)
	assertManifestTurn(t, secondManifest, second.TurnID, secondTrees)
	if secondManifest.BeforeTree != firstManifest.AfterTree {
		t.Fatalf("turn two before tree = %q, want exact turn one after tree %q", secondManifest.BeforeTree, firstManifest.AfterTree)
	}
}

func newEvidenceSlotTestCoordinator(
	t *testing.T,
	store EvidenceStore,
) (*Coordinator, CreatedRun) {
	t.Helper()
	identifiers := []string{"session-1", "run-1", "turn-1", "runtime-1", "trace-1"}
	coordinator := NewCoordinator(Options{
		RootContext:  context.Background(),
		Materializer: fakeMaterializer{},
		ObjectStore:  store,
		Scheduler:    newRecordingScheduler(),
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "test-worker-token", nil },
	})
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{
		Goal:               "persist evidence",
		AcceptanceCriteria: []string{"evidence slot is idempotent"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return coordinator, created
}

func TestFailedOrIncompleteSecondTurnPreservesLastAuthoritativeManifest(t *testing.T) {
	t.Parallel()

	root, coordinator, created := newManifestTestCoordinator(t)
	startManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID)
	firstTrees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	firstPointers := uploadManifestTestEvidence(t, coordinator, created, created.TurnID, firstTrees, nil)
	completeManifestTestTurn(t, coordinator, created.WorkerRunID, created.TurnID, firstPointers)
	firstManifestBytes := readManifestBytes(t, root, created)

	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "second turn")
	if err != nil {
		t.Fatalf("CreateTurn() error = %v", err)
	}
	startManifestTestTurn(t, coordinator, created.WorkerRunID, second.TurnID)
	secondTrees := sharedevidence.TreePair{
		BeforeTree: firstTrees.AfterTree,
		AfterTree:  strings.Repeat("c", 40),
	}
	partial := uploadManifestTestEvidence(
		t,
		coordinator,
		created,
		second.TurnID,
		secondTrees,
		[]objectstore.EvidenceKind{
			objectstore.EvidenceReport,
			objectstore.EvidenceRawACP,
			objectstore.EvidenceTrace,
		},
	)
	mismatchedPatch := uploadManifestTestEvidence(
		t,
		coordinator,
		created,
		second.TurnID,
		sharedevidence.TreePair{
			BeforeTree: secondTrees.BeforeTree,
			AfterTree:  strings.Repeat("d", 40),
		},
		[]objectstore.EvidenceKind{objectstore.EvidencePatch},
	)
	mismatched := append(append([]*workercontrolv1.EvidencePointer(nil), partial...), mismatchedPatch...)
	identity := manifestTestIdentity(t, coordinator, created.WorkerRunID)
	invalidCompletion := manifestTestEnvelope(second.TurnID, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{
			AgentSessionId: "agent-session",
			Evidence:       mismatched,
		},
	})
	if _, err := coordinator.HandleWorkerEvent(context.Background(), identity, invalidCompletion); err == nil {
		t.Fatal("tree-mismatched TurnCompleted unexpectedly succeeded")
	}
	if got := readManifestBytes(t, root, created); string(got) != string(firstManifestBytes) {
		t.Fatal("tree-mismatched TurnCompleted replaced the last authoritative manifest")
	}

	failed := manifestTestEnvelope(second.TurnID, &workercontrolv1.WorkerEnvelope_TurnFailed{
		TurnFailed: &workercontrolv1.TurnFailed{
			Code:            "evidence_upload_failed",
			Detail:          "partial upload",
			PartialEvidence: partial,
		},
	})
	if _, err := coordinator.HandleWorkerEvent(context.Background(), identity, failed); err != nil {
		t.Fatalf("TurnFailed error = %v", err)
	}
	if got := readManifestBytes(t, root, created); string(got) != string(firstManifestBytes) {
		t.Fatal("TurnFailed replaced the last authoritative manifest")
	}
}

func newManifestTestCoordinator(t *testing.T) (string, *Coordinator, CreatedRun) {
	t.Helper()

	root := t.TempDir()
	identifiers := []string{
		"session-1", "run-1", "turn-1", "runtime-1", "trace-1", "turn-2",
	}
	coordinator := NewCoordinator(Options{
		RootContext:  context.Background(),
		Materializer: fakeMaterializer{},
		ObjectStore: objectstore.LocalFileObjectStore{
			Root: root,
		},
		Scheduler: newRecordingScheduler(),
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "test-worker-token", nil },
	})
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{
		Goal:               "persist evidence",
		AcceptanceCriteria: []string{"manifest is authoritative"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return root, coordinator, created
}

func startManifestTestTurn(t *testing.T, coordinator *Coordinator, runID, turnID string) {
	t.Helper()

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	managed := coordinator.byRun[runID]
	run := managed.store.Snapshot()
	var err error
	if run.State == runstate.StateProvisioning {
		run, err = managed.store.Advance(run.Revision, runstate.StateConnecting)
		if err == nil {
			run, err = managed.store.BindAgentSession(run.Revision, "agent-session")
		}
		if err == nil {
			run, err = managed.store.Advance(run.Revision, runstate.StateReady)
		}
	}
	if err == nil {
		run, err = managed.store.StartTurn(run.Revision, turnID)
	}
	if err != nil {
		t.Fatalf("start turn %q: %v", turnID, err)
	}
	recordProjectionTurnStarted(
		managed,
		turnID,
		time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
	)
	delete(managed.pendingTurns, turnID)
}

func uploadManifestTestEvidence(
	t *testing.T,
	coordinator *Coordinator,
	created CreatedRun,
	turnID string,
	trees sharedevidence.TreePair,
	kinds []objectstore.EvidenceKind,
) []*workercontrolv1.EvidencePointer {
	t.Helper()

	if kinds == nil {
		kinds = []objectstore.EvidenceKind{
			objectstore.EvidenceReport,
			objectstore.EvidenceRawACP,
			objectstore.EvidenceTrace,
			objectstore.EvidencePatch,
		}
	}
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		created.WorkerSessionID,
		created.WorkerRunID,
		turnID,
		"test-worker-token",
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	pointers := make([]*workercontrolv1.EvidencePointer, 0, len(kinds))
	for _, kind := range kinds {
		stored, err := coordinator.StoreEvidenceWithTrees(
			context.Background(),
			authorization,
			kind,
			[]byte("evidence "+kind),
			trees,
		)
		if err != nil {
			t.Fatalf("StoreEvidenceWithTrees(%q) error = %v", kind, err)
		}
		pointers = append(pointers, &workercontrolv1.EvidencePointer{
			Kind:       string(kind),
			ObjectPath: stored.Path,
			Sha256:     stored.SHA256,
			SizeBytes:  stored.Bytes,
		})
	}
	return pointers
}

func completeManifestTestTurn(
	t *testing.T,
	coordinator *Coordinator,
	runID, turnID string,
	pointers []*workercontrolv1.EvidencePointer,
) {
	t.Helper()

	identity := manifestTestIdentity(t, coordinator, runID)
	event := manifestTestEnvelope(turnID, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{
			AgentSessionId: "agent-session",
			Evidence:       pointers,
		},
	})
	if _, err := coordinator.HandleWorkerEvent(context.Background(), identity, event); err != nil {
		t.Fatalf("TurnCompleted(%q) error = %v", turnID, err)
	}
}

func manifestTestIdentity(t *testing.T, coordinator *Coordinator, runID string) control.Identity {
	t.Helper()
	snapshot, err := coordinator.Snapshot(runID)
	if err != nil {
		t.Fatal(err)
	}
	return control.Identity{
		WorkerSessionID:   snapshot.WorkerSessionID,
		WorkerRunID:       snapshot.WorkerRunID,
		RuntimeInstanceID: snapshot.RuntimeInstanceID,
		RuntimeGeneration: snapshot.RuntimeGeneration,
	}
}

func manifestTestEnvelope(turnID string, payload any) *workercontrolv1.WorkerEnvelope {
	event := &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			Identity: &workercontrolv1.Identity{AgentTurnId: turnID},
		},
	}
	switch typed := payload.(type) {
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		event.Payload = typed
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		event.Payload = typed
	default:
		panic(fmt.Sprintf("unsupported test payload %T", payload))
	}
	return event
}

func readTurnManifest(t *testing.T, root string, created CreatedRun) sharedevidence.TurnManifest {
	t.Helper()
	var manifest sharedevidence.TurnManifest
	if err := json.Unmarshal(readManifestBytes(t, root, created), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}

func readManifestBytes(t *testing.T, root string, created CreatedRun) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, created.WorkerSessionID, created.WorkerRunID, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return content
}

func assertManifestTurn(
	t *testing.T,
	manifest sharedevidence.TurnManifest,
	turnID string,
	trees sharedevidence.TreePair,
) {
	t.Helper()
	if manifest.SchemaVersion != sharedevidence.TurnManifestSchemaVersion ||
		manifest.WorkerSessionID != "session-1" ||
		manifest.WorkerRunID != "run-1" ||
		manifest.RuntimeInstanceID != "runtime-1" ||
		manifest.RuntimeGeneration != 1 ||
		manifest.AgentSessionID != "agent-session" ||
		manifest.AgentTurnID != turnID ||
		manifest.TraceID != "trace-1" ||
		manifest.BeforeTree != trees.BeforeTree ||
		manifest.AfterTree != trees.AfterTree {
		t.Fatalf("manifest identity/tree fields = %+v", manifest)
	}
	if len(manifest.Objects) != 4 {
		t.Fatalf("manifest objects = %d, want 4", len(manifest.Objects))
	}
	wantKinds := []string{"report", "raw-acp", "trace", "patch"}
	for index, object := range manifest.Objects {
		if object.Kind != wantKinds[index] ||
			!strings.Contains(object.Path, filepath.Join("turns", turnID)) ||
			len(object.SHA256) != 64 ||
			object.Bytes <= 0 {
			t.Fatalf("manifest object %d = %+v", index, object)
		}
	}
}

func makeFirstTurnIdle(t *testing.T, coordinator *Coordinator, created CreatedRun) {
	t.Helper()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	managed := coordinator.byRun[created.WorkerRunID]
	run := managed.store.Snapshot()
	var err error
	run, err = managed.store.Advance(run.Revision, runstate.StateConnecting)
	if err != nil {
		t.Fatalf("connect run: %v", err)
	}
	run, err = managed.store.BindAgentSession(run.Revision, "agent-session")
	if err != nil {
		t.Fatalf("bind agent: %v", err)
	}
	run, err = managed.store.Advance(run.Revision, runstate.StateReady)
	if err != nil {
		t.Fatalf("ready run: %v", err)
	}
	run, err = managed.store.StartTurn(run.Revision, created.TurnID)
	if err != nil {
		t.Fatalf("start first turn: %v", err)
	}
	if _, err = managed.store.CompleteTurn(run.Revision, created.TurnID); err != nil {
		t.Fatalf("complete first turn: %v", err)
	}
	delete(managed.pendingTurns, created.TurnID)
}

func newTestCoordinator(t *testing.T, scheduler *recordingScheduler) *Coordinator {
	t.Helper()
	identifiers := []string{"session-1", "run-1", "turn-1", "runtime-1", "trace-1", "turn-2", "turn-3"}
	return NewCoordinator(Options{
		RootContext:  context.Background(),
		Materializer: fakeMaterializer{},
		Scheduler:    scheduler,
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "test-worker-token", nil },
	})
}

type fakeMaterializer struct{}

func (fakeMaterializer) Materialize(context.Context, string) (runtime.MaterializedSession, error) {
	return distinctiveMaterializedSession("session-1"), nil
}

func (fakeMaterializer) Rollback(context.Context, string, runtime.MaterializedSession) error {
	return nil
}

func distinctiveMaterializedSession(sessionID string) runtime.MaterializedSession {
	pair := func(name string) runtime.PathPair {
		return runtime.PathPair{
			ContainerPath: fmt.Sprintf("/container-distinctive/sessions/%s/%s", sessionID, name),
			HostPath:      fmt.Sprintf("/host-distinctive/sessions/%s/%s", sessionID, name),
		}
	}
	return runtime.MaterializedSession{
		WorkingCopy: pair("working-copy"),
		AgentState:  pair("agent-state"),
		TraceSpool:  pair("trace-spool"),
		RuntimeLogs: pair("runtime-logs"),
	}
}

func assertControlledHandoff(t *testing.T, handoff schedulerHandoff, sessionID string) {
	t.Helper()
	want := distinctiveMaterializedSession(sessionID)
	if handoff.workerToken != "test-worker-token" {
		t.Fatal("scheduler handoff did not receive the exact worker token")
	}
	if handoff.workerRunID != "run-1" || handoff.runtimeInstanceID != "runtime-1" || handoff.runtimeGeneration != 1 {
		t.Fatalf("scheduler scoped identity = run %q runtime %q generation %d", handoff.workerRunID, handoff.runtimeInstanceID, handoff.runtimeGeneration)
	}
	if handoff.traceID != "trace-1" {
		t.Fatalf("scheduler trace identity = %q, want trace-1", handoff.traceID)
	}
	if handoff.materialized != want {
		t.Fatal("scheduler did not receive the exact server-owned materialized paths")
	}
}

type rejectingMaterializer struct {
	err            error
	rollbackCalled bool
}

func (m *rejectingMaterializer) Materialize(context.Context, string) (runtime.MaterializedSession, error) {
	return runtime.MaterializedSession{}, m.err
}

func (m *rejectingMaterializer) Rollback(context.Context, string, runtime.MaterializedSession) error {
	m.rollbackCalled = true
	return nil
}

type trackingMaterializer struct {
	mu          sync.Mutex
	active      map[string]runtime.MaterializedSession
	rolledBack  []string
	rollbackErr error
}

type blockingEvidenceStore struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	got     []byte
}

type countingEvidenceStore struct {
	mu    sync.Mutex
	calls int
}

type gatedEvidenceStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

type retryEvidenceStore struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (s *retryEvidenceStore) Put(
	identity objectstore.ObjectIdentity,
	payload []byte,
) (objectstore.StoredObject, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.started)
		<-s.release
		return objectstore.StoredObject{}, errors.New("injected Put failure")
	}
	digest := sha256.Sum256(payload)
	return objectstore.StoredObject{
		Path:   filepath.Join("turns", identity.AgentTurnID, string(identity.Kind)),
		SHA256: fmt.Sprintf("%x", digest),
		Bytes:  int64(len(payload)),
	}, nil
}

func (s *retryEvidenceStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *gatedEvidenceStore) Put(
	identity objectstore.ObjectIdentity,
	payload []byte,
) (objectstore.StoredObject, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	<-s.release
	digest := sha256.Sum256(payload)
	return objectstore.StoredObject{
		Path:   filepath.Join("turns", identity.AgentTurnID, string(identity.Kind)),
		SHA256: fmt.Sprintf("%x", digest),
		Bytes:  int64(len(payload)),
	}, nil
}

func (s *gatedEvidenceStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *countingEvidenceStore) Put(
	identity objectstore.ObjectIdentity,
	payload []byte,
) (objectstore.StoredObject, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	digest := sha256.Sum256(payload)
	return objectstore.StoredObject{
		Path:   filepath.Join("turns", identity.AgentTurnID, string(identity.Kind)),
		SHA256: fmt.Sprintf("%x", digest),
		Bytes:  int64(len(payload)),
	}, nil
}

func (s *countingEvidenceStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *blockingEvidenceStore) Put(_ objectstore.ObjectIdentity, payload []byte) (objectstore.StoredObject, error) {
	s.mu.Lock()
	s.got = append([]byte(nil), payload...)
	s.mu.Unlock()
	close(s.started)
	<-s.release
	return objectstore.StoredObject{SHA256: "stored", Bytes: int64(len(payload))}, nil
}

func (s *blockingEvidenceStore) payload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.got...)
}

func newTrackingMaterializer() *trackingMaterializer {
	return &trackingMaterializer{active: make(map[string]runtime.MaterializedSession)}
}

func (m *trackingMaterializer) Materialize(_ context.Context, sessionID string) (runtime.MaterializedSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	materialized := distinctiveMaterializedSession(sessionID)
	m.active[sessionID] = materialized
	return materialized, nil
}

func (m *trackingMaterializer) Rollback(_ context.Context, sessionID string, materialized runtime.MaterializedSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolledBack = append(m.rolledBack, sessionID)
	if m.rollbackErr != nil {
		return m.rollbackErr
	}
	if current, ok := m.active[sessionID]; ok && current == materialized {
		delete(m.active, sessionID)
	}
	return nil
}

func (m *trackingMaterializer) isActive(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.active[sessionID]
	return ok
}

func (m *trackingMaterializer) rollbackIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.rolledBack...)
}

type recordedCall struct {
	Context context.Context
	Work    schedulerHandoff
}

type recordingScheduler struct {
	initial chan recordedCall
	turn    chan recordedCall
	cleanup chan recordedCall
	mu      sync.Mutex
	cleaned int
}

func newRecordingScheduler() *recordingScheduler {
	return &recordingScheduler{
		initial: make(chan recordedCall, 4),
		turn:    make(chan recordedCall, 4),
		cleanup: make(chan recordedCall, 4),
	}
}

func (s *recordingScheduler) Initial(ctx context.Context, work schedulerHandoff) error {
	s.initial <- recordedCall{Context: ctx, Work: work}
	return nil
}

func (s *recordingScheduler) Turn(ctx context.Context, work schedulerHandoff) error {
	s.turn <- recordedCall{Context: ctx, Work: work}
	return nil
}

func (s *recordingScheduler) Cleanup(ctx context.Context, work schedulerHandoff) error {
	s.mu.Lock()
	s.cleaned++
	s.mu.Unlock()
	s.cleanup <- recordedCall{Context: ctx, Work: work}
	return nil
}

func (s *recordingScheduler) cleanupCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleaned
}
