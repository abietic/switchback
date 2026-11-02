package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/langfuse"
	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runstate"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
)

func TestProjectionRunsAfterAuthoritativeCompletionWithoutBlockingWorkerControl(t *testing.T) {
	t.Parallel()

	projector := &blockingProjection{
		started:  make(chan langfuse.RunProjection, 1),
		contexts: make(chan context.Context, 1),
		release:  make(chan struct{}),
	}
	root, coordinator, created := newProjectionTestCoordinator(t, projector)
	identity := manifestTestIdentity(t, coordinator, created.WorkerRunID)
	firstStarted := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	prepareProjectionTurn(t, coordinator, identity, created.TurnID, firstStarted, "generation-1")
	firstTrees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	firstPointers := uploadManifestTestEvidence(t, coordinator, created, created.TurnID, firstTrees, nil)
	completeProjectionTurn(t, coordinator, identity, created.TurnID, firstStarted.Add(2*time.Second), firstPointers)
	select {
	case <-projector.started:
		t.Fatal("projection started before the authoritative two-Turn run completed")
	default:
	}

	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "second turn")
	if err != nil {
		t.Fatalf("CreateTurn() error = %v", err)
	}
	secondStarted := firstStarted.Add(3 * time.Second)
	prepareProjectionTurn(t, coordinator, identity, second.TurnID, secondStarted, "generation-2")
	secondTrees := sharedevidence.TreePair{
		BeforeTree: firstTrees.AfterTree,
		AfterTree:  strings.Repeat("c", 40),
	}
	secondPointers := uploadManifestTestEvidence(t, coordinator, created, second.TurnID, secondTrees, nil)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(projector.release) })
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	completed := make(chan workerEventResult, 1)
	go func() {
		disposition, eventErr := coordinator.HandleWorkerEvent(
			requestContext,
			identity,
			projectionCompletionEnvelope(second.TurnID, secondStarted.Add(2*time.Second), secondPointers),
		)
		completed <- workerEventResult{disposition: disposition, err: eventErr}
	}()

	var projection langfuse.RunProjection
	select {
	case projection = <-projector.started:
	case <-time.After(2 * time.Second):
		t.Fatal("projection did not start after authoritative completion")
	}
	select {
	case result := <-completed:
		if result.err != nil || result.disposition != control.EventDispositionAuthoritative {
			t.Fatalf("HandleWorkerEvent() disposition=%q error=%v", result.disposition, result.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleWorkerEvent blocked on Langfuse projection")
	}
	cancelRequest()
	select {
	case projectionContext := <-projector.contexts:
		select {
		case <-projectionContext.Done():
			t.Fatal("projection used the short-lived WorkerControl context")
		default:
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("projection did not receive a lifecycle context")
	}
	manifest := readTurnManifest(t, root, created)
	if manifest.AgentTurnID != second.TurnID || len(manifest.Objects) != 4 {
		t.Fatalf("manifest at projection start = %+v, want authoritative second turn", manifest)
	}
	snapshotResult := make(chan RunSnapshot, 1)
	go func() {
		snapshot, _ := coordinator.Snapshot(created.WorkerRunID)
		snapshotResult <- snapshot
	}()
	select {
	case snapshot := <-snapshotResult:
		if snapshot.ExecutionOutcome != string(runstate.ExecutionCompleted) ||
			snapshot.SpikeAcceptance != string(runstate.AcceptancePending) {
			t.Fatalf("in-flight projection snapshot = %+v", snapshot)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Snapshot blocked behind Langfuse network operation")
	}
	assertProjectionFacts(t, projection, created, second.TurnID)

	releaseOnce.Do(func() { close(projector.release) })
	final := awaitProjectionAcceptance(t, coordinator, created.WorkerRunID, runstate.AcceptancePassed)
	if final.ExecutionOutcome != string(runstate.ExecutionCompleted) ||
		final.SpikeAcceptance != string(runstate.AcceptancePassed) {
		t.Fatalf("final outcome/acceptance = %q/%q", final.ExecutionOutcome, final.SpikeAcceptance)
	}
}

func TestProjectionFailurePreservesCompletedExecutionAndLastSummary(t *testing.T) {
	t.Parallel()

	const lastSummary = "Langfuse verification timed out after 1m30s; last observed: observations=1 agent_turn_spans=1 matched_generations=0/2"
	projector := &blockingProjection{err: errors.New(lastSummary)}
	_, coordinator, created := newProjectionTestCoordinator(t, projector)
	identity := manifestTestIdentity(t, coordinator, created.WorkerRunID)
	firstStarted := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	prepareProjectionTurn(t, coordinator, identity, created.TurnID, firstStarted, "generation-1")
	firstTrees := sharedevidence.TreePair{
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
	}
	firstPointers := uploadManifestTestEvidence(t, coordinator, created, created.TurnID, firstTrees, nil)
	completeProjectionTurn(t, coordinator, identity, created.TurnID, firstStarted.Add(2*time.Second), firstPointers)
	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "second turn")
	if err != nil {
		t.Fatal(err)
	}
	secondStarted := firstStarted.Add(3 * time.Second)
	prepareProjectionTurn(t, coordinator, identity, second.TurnID, secondStarted, "generation-2")
	secondTrees := sharedevidence.TreePair{
		BeforeTree: firstTrees.AfterTree,
		AfterTree:  strings.Repeat("c", 40),
	}
	secondPointers := uploadManifestTestEvidence(t, coordinator, created, second.TurnID, secondTrees, nil)
	completeProjectionTurn(t, coordinator, identity, second.TurnID, secondStarted.Add(2*time.Second), secondPointers)

	snapshot := awaitProjectionAcceptance(t, coordinator, created.WorkerRunID, runstate.AcceptanceFailed)
	if snapshot.ExecutionOutcome != string(runstate.ExecutionCompleted) {
		t.Fatalf("execution outcome = %q, want completed", snapshot.ExecutionOutcome)
	}
	if snapshot.SpikeAcceptance != string(runstate.AcceptanceFailed) {
		t.Fatalf("spike acceptance = %q, want failed", snapshot.SpikeAcceptance)
	}
	if len(snapshot.Diagnostics) == 0 {
		t.Fatal("failed projection snapshot has no bounded diagnostic")
	}
	snapshotLast := snapshot.Diagnostics[len(snapshot.Diagnostics)-1]
	if snapshotLast.Kind != "langfuse_projection" ||
		snapshotLast.Code != "acceptance_failed" ||
		!strings.Contains(snapshotLast.Detail, "last observed: observations=1") ||
		len(snapshotLast.Detail) > maxDiagnosticTextRunes {
		t.Fatalf("projection snapshot diagnostic = %+v", snapshotLast)
	}
	diagnostics, err := coordinator.Diagnostics(created.WorkerRunID)
	if err != nil {
		t.Fatal(err)
	}
	last := diagnostics[len(diagnostics)-1]
	if last.Kind != "langfuse_projection" ||
		last.Code != "acceptance_failed" ||
		!strings.Contains(last.Detail, "last observed: observations=1") ||
		len(last.Detail) > maxDiagnosticTextRunes {
		t.Fatalf("projection diagnostic = %+v", last)
	}
}

type workerEventResult struct {
	disposition control.EventDisposition
	err         error
}

func awaitProjectionAcceptance(
	t *testing.T,
	coordinator *Coordinator,
	workerRunID string,
	want runstate.SpikeAcceptance,
) RunSnapshot {
	t.Helper()

	timeout := time.After(2 * time.Second)
	for {
		snapshot, err := coordinator.Snapshot(workerRunID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.SpikeAcceptance == string(want) {
			return snapshot
		}
		select {
		case <-timeout:
			t.Fatalf("spike acceptance = %q, want %q", snapshot.SpikeAcceptance, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func newProjectionTestCoordinator(
	t *testing.T,
	projector LangfuseProjector,
) (string, *Coordinator, CreatedRun) {
	t.Helper()

	root := t.TempDir()
	identifiers := []string{
		"session-1", "run-1", "turn-1", "runtime-1", "trace-1", "turn-2",
	}
	coordinator := NewCoordinator(Options{
		RootContext:  context.Background(),
		Materializer: fakeMaterializer{},
		ObjectStore:  objectstore.LocalFileObjectStore{Root: root},
		Scheduler:    newRecordingScheduler(),
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken:          func() (string, error) { return "test-worker-token", nil },
		LangfuseProjector: projector,
	})
	created, err := coordinator.CreateRun(context.Background(), CreateRunInput{
		Goal:               "project a completed run",
		AcceptanceCriteria: []string{"Langfuse is queryable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := manifestTestIdentity(t, coordinator, created.WorkerRunID)
	assertAuthoritativeProjectionEvent(t, coordinator, identity, &workercontrolv1.WorkerEnvelope{
		Meta: projectionMeta("", time.Date(2026, 7, 17, 9, 59, 58, 0, time.UTC)),
		Payload: &workercontrolv1.WorkerEnvelope_RuntimeReady{
			RuntimeReady: &workercontrolv1.RuntimeReady{},
		},
	})
	assertAuthoritativeProjectionEvent(t, coordinator, identity, &workercontrolv1.WorkerEnvelope{
		Meta: projectionMeta("", time.Date(2026, 7, 17, 9, 59, 59, 0, time.UTC)),
		Payload: &workercontrolv1.WorkerEnvelope_AgentSessionReady{
			AgentSessionReady: &workercontrolv1.AgentSessionReady{AgentSessionId: "agent-session"},
		},
	})
	return root, coordinator, created
}

func prepareProjectionTurn(
	t *testing.T,
	coordinator *Coordinator,
	identity control.Identity,
	turnID string,
	started time.Time,
	generationID string,
) {
	t.Helper()

	assertAuthoritativeProjectionEvent(t, coordinator, identity, &workercontrolv1.WorkerEnvelope{
		Meta: projectionMeta(turnID, started),
		Payload: &workercontrolv1.WorkerEnvelope_TurnStarted{
			TurnStarted: &workercontrolv1.TurnStarted{StartedAt: timestamppb.New(started)},
		},
	})
	assertAuthoritativeProjectionEvent(t, coordinator, identity, &workercontrolv1.WorkerEnvelope{
		Meta: projectionMeta(turnID, started.Add(1500*time.Millisecond)),
		Payload: &workercontrolv1.WorkerEnvelope_ModelObservation{
			ModelObservation: &workercontrolv1.ModelObservation{
				ModelObservationId: generationID,
				Model:              "openai/codex",
				Route:              "codex-primary",
				StartedAt:          timestamppb.New(started.Add(100 * time.Millisecond)),
				EndedAt:            timestamppb.New(started.Add(1400 * time.Millisecond)),
				LatencyMs:          1300,
				InputTokens:        100,
				OutputTokens:       20,
				TotalTokens:        120,
				CostUsd:            0.01,
				Status:             "ok",
				MetadataJson:       []byte(`{"request_id":"controlled","Authorization":"must-not-project"}`),
			},
		},
	})
}

func completeProjectionTurn(
	t *testing.T,
	coordinator *Coordinator,
	identity control.Identity,
	turnID string,
	ended time.Time,
	pointers []*workercontrolv1.EvidencePointer,
) {
	t.Helper()

	disposition, err := coordinator.HandleWorkerEvent(
		context.Background(),
		identity,
		projectionCompletionEnvelope(turnID, ended, pointers),
	)
	if err != nil || disposition != control.EventDispositionAuthoritative {
		t.Fatalf("TurnCompleted disposition=%q error=%v", disposition, err)
	}
}

func projectionCompletionEnvelope(
	turnID string,
	ended time.Time,
	pointers []*workercontrolv1.EvidencePointer,
) *workercontrolv1.WorkerEnvelope {
	return &workercontrolv1.WorkerEnvelope{
		Meta: projectionMeta(turnID, ended),
		Payload: &workercontrolv1.WorkerEnvelope_TurnCompleted{
			TurnCompleted: &workercontrolv1.TurnCompleted{
				AgentSessionId: "agent-session",
				Evidence:       pointers,
			},
		},
	}
}

func projectionMeta(turnID string, sentAt time.Time) *workercontrolv1.EnvelopeMeta {
	return &workercontrolv1.EnvelopeMeta{
		Identity: &workercontrolv1.Identity{AgentTurnId: turnID},
		SentAt:   timestamppb.New(sentAt),
	}
}

func assertAuthoritativeProjectionEvent(
	t *testing.T,
	coordinator *Coordinator,
	identity control.Identity,
	event *workercontrolv1.WorkerEnvelope,
) {
	t.Helper()

	disposition, err := coordinator.HandleWorkerEvent(context.Background(), identity, event)
	if err != nil || disposition != control.EventDispositionAuthoritative {
		t.Fatalf("event disposition=%q error=%v", disposition, err)
	}
}

func assertProjectionFacts(
	t *testing.T,
	projection langfuse.RunProjection,
	created CreatedRun,
	secondTurnID string,
) {
	t.Helper()

	if projection.WorkerSessionID != created.WorkerSessionID ||
		projection.WorkerRunID != created.WorkerRunID ||
		projection.RuntimeInstanceID != "runtime-1" ||
		projection.RuntimeGeneration != 1 ||
		projection.AgentSessionID != "agent-session" ||
		projection.TraceID != "trace-1" ||
		len(projection.Turns) != 2 ||
		projection.Turns[0].AgentTurnID != created.TurnID ||
		projection.Turns[1].AgentTurnID != secondTurnID {
		t.Fatalf("projection identities = %+v", projection)
	}
	for _, turn := range projection.Turns {
		if turn.StartedAt.IsZero() ||
			turn.EndedAt.IsZero() ||
			len(turn.ModelObservations) != 1 {
			t.Fatalf("projection turn facts = %+v", turn)
		}
		observation := turn.ModelObservations[0]
		if observation.ID == "" ||
			observation.Model != "openai/codex" ||
			observation.Route != "codex-primary" ||
			observation.InputTokens != 100 ||
			observation.OutputTokens != 20 ||
			observation.TotalTokens != 120 ||
			observation.CostUSD != 0.01 ||
			observation.Status != "ok" ||
			!strings.Contains(string(observation.MetadataJSON), "controlled") {
			t.Fatalf("complete ModelObservation fact = %+v", observation)
		}
	}
}

type blockingProjection struct {
	started  chan langfuse.RunProjection
	contexts chan context.Context
	release  chan struct{}
	err      error
}

func (p *blockingProjection) Project(ctx context.Context, projection langfuse.RunProjection) error {
	if p.started != nil {
		p.started <- projection
	}
	if p.contexts != nil {
		p.contexts <- ctx
	}
	if p.release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.release:
		}
	}
	return p.err
}
