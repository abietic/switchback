//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

type replayDeltaBarrier struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	begins      []string
}

func newReplayDeltaBarrier() *replayDeltaBarrier {
	return &replayDeltaBarrier{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (barrier *replayDeltaBarrier) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	normalized := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.HasPrefix(normalized, "begin") {
		barrier.mu.Lock()
		barrier.begins = append(barrier.begins, normalized)
		barrier.mu.Unlock()
	}
	if strings.Contains(normalized, "from projection_deltas") &&
		strings.Contains(normalized, "order by view_cursor") {
		barrier.startedOnce.Do(func() { close(barrier.started) })
		select {
		case <-barrier.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*replayDeltaBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (barrier *replayDeltaBarrier) releaseReader() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

func (barrier *replayDeltaBarrier) beginStatements() []string {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return append([]string(nil), barrier.begins...)
}

func TestViewContinuityResumeAnchorsHeadAcrossConcurrentCommit(t *testing.T) {
	runViewContinuityRace(t, 10, []uint64{1, 2, 3}, []uint64{2})
}

func TestViewContinuityResumePreservesRetentionFloorAcrossConcurrentPrune(t *testing.T) {
	runViewContinuityRace(t, 1, []uint64{3}, []uint64{2})
}

func runViewContinuityRace(
	t *testing.T,
	retentionLimit int,
	wantLiveCursors, wantReplayCursors []uint64,
) {
	t.Helper()
	pool := postgrestest.Open(t)
	barrier := newReplayDeltaBarrier()
	t.Cleanup(barrier.releaseReader)
	readerPool := postgrestest.OpenSiblingWithTracer(t, pool, barrier)
	store, err := projectionpostgres.NewStoreWithReplayLimits(pool, retentionLimit, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	projector := projectionapp.Projector{Store: store}
	fixedNow := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	for sequence := uint64(1); sequence <= 2; sequence++ {
		if _, err := projector.Execute(
			context.Background(),
			continuityRaceEvent(sequence, fixedNow.Add(time.Duration(sequence)*time.Second)),
		); err != nil {
			t.Fatalf("project cursor %d: %v", sequence, err)
		}
	}
	if retentionLimit == 1 {
		assertReplayCursors(t, pool, []uint64{2})
	}

	readerStore := projectionpostgres.NewStore(readerPool)
	reader, err := projectionapp.NewViewContinuityReader(
		readerStore,
		projectionapp.ViewContinuityReaderOptions{
			Now:         func() time.Time { return fixedNow },
			ReplayLimit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	type replayOutcome struct {
		result projectionapp.ResumeResult
		err    error
	}
	result := make(chan replayOutcome, 1)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRead()
	go func() {
		resumed, err := reader.Resume(readCtx, projectionapp.ResumeQuery{
			Viewer:      projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
			ViewType:    projectionapp.ViewWorkerDetail,
			ViewID:      "session-continuity-race",
			AfterEpoch:  1,
			AfterCursor: 1,
		})
		result <- replayOutcome{result: resumed, err: err}
	}()

	select {
	case <-barrier.started:
	case outcome := <-result:
		t.Fatalf("reader completed before replay delta barrier: result=%#v error=%v", outcome.result, outcome.err)
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not reach replay delta barrier")
	}

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 10*time.Second)
	_, writeErr := projector.Execute(writeCtx, continuityRaceEvent(3, fixedNow.Add(3*time.Second)))
	cancelWrite()
	if writeErr != nil {
		t.Fatalf("writer did not commit before reader release: %v", writeErr)
	}
	assertViewHeadCursor(t, pool, 3)
	assertReplayCursors(t, pool, wantLiveCursors)

	barrier.releaseReader()
	var outcome replayOutcome
	select {
	case outcome = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not complete after replay delta release")
	}
	if outcome.err != nil {
		t.Fatalf("resume View continuity: %v", outcome.err)
	}
	gotCursors := resumeResultCursors(outcome.result)
	if !slices.Equal(gotCursors, wantReplayCursors) {
		t.Fatalf("replay cursors=%v, want %v", gotCursors, wantReplayCursors)
	}
	if got := barrier.beginStatements(); !slices.Equal(got, []string{"begin isolation level repeatable read read only"}) {
		t.Fatalf("reader BEGIN statements=%v", got)
	}
}

func continuityRaceEvent(sequence uint64, at time.Time) projectionapp.ProjectionEvent {
	update := viewUpdate(
		projectionapp.ViewWorkerDetail,
		"session-continuity-race",
		fmt.Sprintf("delta-continuity-race-%d", sequence),
	)
	return projectionapp.ProjectionEvent{
		SourceEventID:  fmt.Sprintf("source-continuity-race-%d", sequence),
		SourceStreamID: "worker-continuity-race-stream",
		SourceSequence: sequence,
		ProjectedAt:    at,
		Updates:        []projectionapp.ViewUpdate{update},
	}
}

func resumeResultCursors(result projectionapp.ResumeResult) []uint64 {
	cursors := make([]uint64, 0, len(result.Deltas))
	for _, delta := range result.Deltas {
		cursors = append(cursors, delta.ViewCursor)
	}
	return cursors
}

func assertViewHeadCursor(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, want uint64) {
	t.Helper()
	var got uint64
	if err := pool.QueryRow(context.Background(), `
SELECT view_cursor FROM view_projections
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND view_type='worker_detail' AND view_id='session-continuity-race'`).Scan(&got); err != nil {
		t.Fatalf("read live View head: %v", err)
	}
	if got != want {
		t.Fatalf("live View head cursor=%d, want %d", got, want)
	}
}

func assertReplayCursors(t *testing.T, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, want []uint64) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
SELECT view_cursor FROM projection_deltas
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND view_type='worker_detail' AND view_id='session-continuity-race'
ORDER BY view_cursor`)
	if err != nil {
		t.Fatalf("query live replay cursors: %v", err)
	}
	defer rows.Close()
	got := make([]uint64, 0, len(want))
	for rows.Next() {
		var cursor uint64
		if err := rows.Scan(&cursor); err != nil {
			t.Fatalf("scan live replay cursor: %v", err)
		}
		got = append(got, cursor)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate live replay cursors: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("live replay cursors=%v, want %v", got, want)
	}
}
