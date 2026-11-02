//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	gatewaypostgres "github.com/abietic/switchback/internal/gateway/adapter/postgres"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/workspacefixture"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMaterializerBootstrapsEmptyHomeDashboard(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 23, 19, 0, 0, 0, time.UTC)
	store := projectionpostgres.NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}

	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(24 * time.Hour)
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatalf("repeat bootstrap: %v", err)
	}

	clock = now
	home, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewHomeDashboard,
		ViewID:   projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		AttentionTasks   []map[string]any `json:"attention_tasks"`
		RecentWorkers    []map[string]any `json:"recent_workers"`
		RecentDeliveries []map[string]any `json:"recent_deliveries"`
		Capacity         struct {
			Used  int `json:"used"`
			Limit int `json:"limit"`
		} `json:"capacity"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(home.Data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.AttentionTasks == nil || dashboard.RecentWorkers == nil ||
		dashboard.RecentDeliveries == nil {
		t.Fatalf("Home lists must be non-nil: %s", home.Data)
	}
	if len(dashboard.AttentionTasks) != 0 || len(dashboard.RecentWorkers) != 0 ||
		len(dashboard.RecentDeliveries) != 0 {
		t.Fatalf("empty Home contains records: %s", home.Data)
	}
	bootstrapAt := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if dashboard.Capacity.Used != 0 || dashboard.Capacity.Limit != 10 ||
		dashboard.UpdatedAt != bootstrapAt.Format(time.RFC3339) {
		t.Fatalf("empty Home capacity/update time: %s", home.Data)
	}
	if home.ViewEpoch != 1 || home.ViewCursor != 1 || home.ProjectionVersion != 1 {
		t.Fatalf("repeat empty Home bootstrap rewrote projection epoch=%d cursor=%d version=%d",
			home.ViewEpoch, home.ViewCursor, home.ProjectionVersion)
	}
	var taskBootstrapReceipts, homeBootstrapReceipts, homeUpgradeReceipts int
	if err := pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE source_event_id='local-task-index-bootstrap-v1'),
  count(*) FILTER (WHERE source_event_id='local-home-dashboard-bootstrap-v1'),
  count(*) FILTER (WHERE source_event_id LIKE 'local-home-dashboard-contract-upgrade-v2/%')
FROM projection_event_receipts`).Scan(
		&taskBootstrapReceipts, &homeBootstrapReceipts, &homeUpgradeReceipts,
	); err != nil {
		t.Fatal(err)
	}
	if taskBootstrapReceipts != 1 || homeBootstrapReceipts != 1 || homeUpgradeReceipts != 0 {
		t.Fatalf("bootstrap receipts task=%d home=%d upgrade=%d",
			taskBootstrapReceipts, homeBootstrapReceipts, homeUpgradeReceipts)
	}
}

func TestMaterializerBootstrapRejectsMissingViewsBeforeWriting(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	store := projectionpostgres.NewStore(pool)
	clock := time.Date(2026, time.July, 23, 19, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		UserID: "user-local", Now: now,
	}

	type writeCounts struct {
		views, deltas, receipts, streams, timeline int
	}
	readCounts := func() writeCounts {
		t.Helper()
		var counts writeCounts
		if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM view_projections),
  (SELECT count(*) FROM projection_deltas),
  (SELECT count(*) FROM projection_event_receipts),
  (SELECT count(*) FROM projection_source_streams),
  (SELECT count(*) FROM worker_timeline_turns)`).Scan(
			&counts.views, &counts.deltas, &counts.receipts, &counts.streams, &counts.timeline,
		); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	before := readCounts()
	if err := materializer.Bootstrap(ctx, "tenant-local"); err == nil {
		t.Fatal("Bootstrap without Views unexpectedly succeeded")
	}
	if after := readCounts(); after != before {
		t.Fatalf("Bootstrap without Views wrote projection state: before=%+v after=%+v", before, after)
	}
}

func TestHomeDashboardMigrationRoundTripRebootsSyntheticIdentity(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 23, 19, 0, 0, 0, time.UTC)
	store := projectionpostgres.NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "migrations", "000019_home_dashboard_view.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(content), "-- migrate:down")
	if !ok {
		t.Fatal("Home migration lacks down marker")
	}
	down = strings.TrimSpace(down)
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatalf("migrate Home down: %v", err)
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatalf("migrate Home up: %v", err)
	}
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatalf("bootstrap after Home migration round trip: %v", err)
	}
	var receipts, streams int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_event_receipts
   WHERE source_event_id='local-home-dashboard-bootstrap-v1'),
  (SELECT count(*) FROM projection_source_streams
   WHERE source_stream_id='local-home-dashboard-bootstrap')`).Scan(&receipts, &streams); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || streams != 1 {
		t.Fatalf("Home bootstrap identities after round trip receipts=%d streams=%d", receipts, streams)
	}
}

func TestOutboxMaterializerBuildsTaskIndexAndSummaryFromCommittedState(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 23, 20, 0, 0, 0, time.UTC)
	ids := &materializerIDs{}
	fixture, err := workspacefixture.Load(filepath.Join("..", "..", "..", ".."),
		filepath.Join("..", "..", "..", "..", "test", "fixtures", "m1", "workspace-revision.json"))
	if err != nil {
		t.Fatal(err)
	}
	commands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{
		Pool: pool, IDs: ids, Now: func() time.Time { return now }, ProjectID: fixture.ProjectID,
		NetworkProfile: trustedNetworkProfile(t),
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID: fixture.TenantID, WorkspaceID: fixture.WorkspaceID,
			WorkspaceRevisionID: fixture.WorkspaceRevisionID, ResolutionID: "resolution-local",
			ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		Profiles: config.ProductionProfileRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := commands.CreateTask(ctx, gatewayhttp.FixedActor{
		TenantID: fixture.TenantID, UserID: fixture.ActorUserID,
	}, gatewayhttp.DirectTaskInput{
		SchemaVersion: "gateway.direct_task_input.v1", IdempotencyKey: "materializer-create",
		ProjectID: fixture.ProjectID, WorkspaceID: fixture.WorkspaceID,
		WorkspaceRevisionID: fixture.WorkspaceRevisionID, Title: "Materialize a real Task",
		Goal:               "Prove the production Outbox consumer creates Frontier views.",
		Target:             contractsv1.TaskTarget{Kind: "workspace", Ref: fixture.WorkspaceRevisionID},
		Constraints:        []string{"Keep the projection deterministic."},
		AcceptanceCriteria: []string{"Task index and summary snapshots are readable."},
		AgentProfileID:     config.DeepSeekCodexOpenAIV1, AcceptanceMode: "manual",
		UserVisibleTypes: []string{"report", "patch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := strings.TrimPrefix(created.ViewRef, "/api/v1/views/task_summary/")
	seedHomeDashboardRows(t, pool, fixture.TenantID, fixture.ActorUserID, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := projectionpostgres.NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store}, Objects: objects,
		Views: views, UserID: fixture.ActorUserID, Now: nowFn,
	}
	if err := materializer.Bootstrap(ctx, fixture.TenantID); err != nil {
		t.Fatal(err)
	}
	dispatcher := platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: materializer,
		LeaseOwner: "materializer-test", LeaseDuration: time.Minute, BatchSize: 16,
		Now: func() time.Time { return now },
	}
	published, err := dispatcher.RunOnce(ctx)
	if err != nil || published != 1 {
		t.Fatalf("publish committed Task event count=%d error=%v", published, err)
	}
	viewer := projectionapp.Viewer{TenantID: fixture.TenantID, UserID: fixture.ActorUserID}
	clock = now
	index, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewTaskSummary, ViewID: projectionpostgres.TaskIndexViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var indexData struct {
		Tasks []struct {
			TaskID string `json:"task_id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(index.Data, &indexData); err != nil || len(indexData.Tasks) != 11 {
		t.Fatalf("Task index data=%s error=%v", index.Data, err)
	}
	var indexedCreatedTask bool
	for _, item := range indexData.Tasks {
		if item.TaskID == taskID && item.Title == "Materialize a real Task" && item.Status == "queued" {
			indexedCreatedTask = true
		}
		if item.TaskID == "task-other-user" || item.TaskID == "task-other-tenant" {
			t.Fatalf("Task index leaked another scope: %s", index.Data)
		}
	}
	if !indexedCreatedTask {
		t.Fatalf("Task index omitted created Task: %s", index.Data)
	}
	home, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		AttentionTasks []struct {
			TaskID string `json:"task_id"`
		} `json:"attention_tasks"`
		RecentWorkers []struct {
			WorkerSessionID string `json:"worker_session_id"`
		} `json:"recent_workers"`
		RecentDeliveries []struct {
			TaskID         string `json:"task_id"`
			AcceptanceMode string `json:"acceptance_mode"`
		} `json:"recent_deliveries"`
		Capacity struct {
			Used  int `json:"used"`
			Limit int `json:"limit"`
		} `json:"capacity"`
	}
	if err := json.Unmarshal(home.Data, &dashboard); err != nil {
		t.Fatal(err)
	}
	assertHomeIDs(t, "attention Tasks", []string{"task-a", "task-b", "task-d", "task-e", "task-g", "task-h"},
		func() []string {
			ids := make([]string, 0, len(dashboard.AttentionTasks))
			for _, item := range dashboard.AttentionTasks {
				ids = append(ids, item.TaskID)
			}
			return ids
		}())
	assertHomeIDs(t, "recent Workers", []string{"worker-a", "worker-b", "worker-c", "worker-d", "worker-e", "worker-f"},
		func() []string {
			ids := make([]string, 0, len(dashboard.RecentWorkers))
			for _, item := range dashboard.RecentWorkers {
				ids = append(ids, item.WorkerSessionID)
			}
			return ids
		}())
	assertHomeIDs(t, "recent deliveries", []string{"task-b", "task-c", "task-d", "task-f", "task-g", "task-h"},
		func() []string {
			ids := make([]string, 0, len(dashboard.RecentDeliveries))
			for _, item := range dashboard.RecentDeliveries {
				ids = append(ids, item.TaskID)
			}
			return ids
		}())
	for _, delivery := range dashboard.RecentDeliveries {
		want := "manual"
		if delivery.TaskID == "task-c" {
			want = "auto"
		}
		if delivery.AcceptanceMode != want {
			t.Fatalf("Home delivery %s acceptance_mode=%q, want %s",
				delivery.TaskID, delivery.AcceptanceMode, want)
		}
	}
	if dashboard.Capacity.Used != 7 || dashboard.Capacity.Limit != 12 {
		t.Fatalf("Home capacity=%+v, want quota row 7/12; data=%s", dashboard.Capacity, home.Data)
	}
	refCounts := make(map[string]int)
	for _, ref := range home.RelatedRefs {
		refCounts[ref.Type+":"+ref.ID+":"+ref.Href]++
	}
	for _, id := range []string{"task-a", "task-b", "task-c", "task-d", "task-e", "task-f", "task-g", "task-h"} {
		key := "task_summary:" + id + ":/api/v1/views/task_summary/" + id
		if refCounts[key] != 1 {
			t.Fatalf("Home Task ref %q count=%d refs=%+v", key, refCounts[key], home.RelatedRefs)
		}
	}
	for _, id := range []string{"worker-a", "worker-b", "worker-c", "worker-d", "worker-e", "worker-f"} {
		key := "worker_detail:" + id + ":/api/v1/views/worker_detail/" + id
		if refCounts[key] != 1 {
			t.Fatalf("Home Worker ref %q count=%d refs=%+v", key, refCounts[key], home.RelatedRefs)
		}
	}
	summary, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewTaskSummary, ViewID: taskID,
	})
	if err != nil || len(summary.AvailableActions) != 1 || summary.AvailableActions[0].Action != "task.cancel" {
		t.Fatalf("Task summary=%+v error=%v", summary, err)
	}
	seedHomeArtifact(t, pool, fixture.TenantID, fixture.ActorUserID, now)
	artifactEvent := homeOwnerEvent(
		fixture.TenantID, "home-artifact-event-1", "worker-session/worker-artifact",
		"WorkerSession", "worker-artifact", now,
	)
	seedPublishedHomeOutboxEvent(t, pool, artifactEvent)
	clock = now.Add(time.Hour)
	if err := materializer.Publish(ctx, artifactEvent); err != nil {
		t.Fatal(err)
	}
	refreshedHome, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshedHome.ProjectionVersion <= home.ProjectionVersion {
		t.Fatalf("Home version=%d, want greater than %d after Artifact owner event",
			refreshedHome.ProjectionVersion, home.ProjectionVersion)
	}
	var refreshedDashboard struct {
		RecentDeliveries []struct {
			TaskID    string `json:"task_id"`
			Artifacts []struct {
				ArtifactID   string `json:"artifact_id"`
				ArtifactType string `json:"artifact_type"`
				Href         string `json:"href"`
			} `json:"artifacts"`
		} `json:"recent_deliveries"`
	}
	if err := json.Unmarshal(refreshedHome.Data, &refreshedDashboard); err != nil {
		t.Fatal(err)
	}
	var artifactFound bool
	for _, delivery := range refreshedDashboard.RecentDeliveries {
		if delivery.TaskID != "task-b" {
			continue
		}
		if len(delivery.Artifacts) == 1 &&
			delivery.Artifacts[0].ArtifactID == "artifact-home" &&
			delivery.Artifacts[0].ArtifactType == "report" &&
			delivery.Artifacts[0].Href == "/api/v1/views/artifact_detail/artifact-home" {
			artifactFound = true
		}
	}
	if !artifactFound {
		t.Fatalf("Home delivery omitted canonical Artifact: %s", refreshedHome.Data)
	}
	var artifactRefCount int
	for _, ref := range refreshedHome.RelatedRefs {
		if ref.Type == "artifact_detail" && ref.ID == "artifact-home" &&
			ref.Href == "/api/v1/views/artifact_detail/artifact-home" {
			artifactRefCount++
		}
	}
	if artifactRefCount != 1 {
		t.Fatalf("Home Artifact refs=%+v", refreshedHome.RelatedRefs)
	}

	clock = now.Add(2 * time.Hour)
	if err := materializer.Publish(ctx, artifactEvent); err != nil {
		t.Fatal(err)
	}
	duplicateHome, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicateHome.ProjectionVersion != refreshedHome.ProjectionVersion {
		t.Fatalf("duplicate owner event advanced Home version from %d to %d",
			refreshedHome.ProjectionVersion, duplicateHome.ProjectionVersion)
	}
	conflictingEvent := artifactEvent
	conflictingEvent.Payload = json.RawMessage(`{"fixture":false}`)
	if err := materializer.Publish(ctx, conflictingEvent); !errors.Is(err, projectionpostgres.ErrProjectionEventConflict) {
		t.Fatalf("reused event identity error=%v, want projection content conflict", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE tasks SET updated_at=$2 WHERE task_id=$1`, "task-a", now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	driftEvent := homeOwnerEvent(
		fixture.TenantID, "home-task-event-1", "task/task-a",
		"Task", "task-a", now.Add(3*time.Hour),
	)
	if err := materializer.Publish(ctx, driftEvent); err != nil {
		t.Fatal(err)
	}
	driftedHome, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if driftedHome.ProjectionVersion <= duplicateHome.ProjectionVersion {
		t.Fatalf("new owner event did not advance Home from %d to %d",
			duplicateHome.ProjectionVersion, driftedHome.ProjectionVersion)
	}
	if _, err := pool.Exec(ctx, `
UPDATE projection_event_receipts
SET source_event_digest=NULL
WHERE source_event_id=$1`, artifactEvent.EventID); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Publish(ctx, artifactEvent); err != nil {
		t.Fatalf("legacy semantic duplicate after Home drift: %v", err)
	}
	replayedAfterDrift, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayedAfterDrift.ProjectionVersion != driftedHome.ProjectionVersion {
		t.Fatalf("legacy semantic duplicate after drift advanced Home from %d to %d",
			driftedHome.ProjectionVersion, replayedAfterDrift.ProjectionVersion)
	}
	var backfilledSourceDigest string
	if err := pool.QueryRow(ctx, `
SELECT source_event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, artifactEvent.EventID).Scan(&backfilledSourceDigest); err != nil {
		t.Fatal(err)
	}
	wantSourceDigest, err := platformpostgres.IntegrationEventDigest(artifactEvent)
	if err != nil {
		t.Fatal(err)
	}
	if backfilledSourceDigest != wantSourceDigest {
		t.Fatalf("legacy source digest=%q, want %q", backfilledSourceDigest, wantSourceDigest)
	}
	if _, err := pool.Exec(ctx, `
UPDATE projection_event_receipts
SET source_event_digest=NULL
WHERE source_event_id=$1`, artifactEvent.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
DELETE FROM outbox_events
WHERE event_id=$1`, artifactEvent.EventID); err != nil {
		t.Fatal(err)
	}
	legacyIdentityConflict := artifactEvent
	legacyIdentityConflict.StreamID = "worker-session/reused-artifact-event"
	legacyIdentityConflict.Sequence = 2
	if err := materializer.Publish(ctx, legacyIdentityConflict); !errors.Is(err, projectionpostgres.ErrProjectionEventConflict) {
		t.Fatalf("legacy event identity reuse error=%v, want projection conflict", err)
	}
	if err := materializer.Publish(ctx, artifactEvent); err != nil {
		t.Fatalf("terminal legacy receipt without source bytes: %v", err)
	}

	concurrentEvent := homeOwnerEvent(
		fixture.TenantID, "home-task-event-concurrent", "task/task-b-concurrent",
		"Task", "task-b", now.Add(4*time.Hour),
	)
	startConcurrent := make(chan struct{})
	concurrentErrors := make(chan error, 2)
	for range 2 {
		go func() {
			<-startConcurrent
			concurrentErrors <- materializer.Publish(ctx, concurrentEvent)
		}()
	}
	close(startConcurrent)
	for range 2 {
		if err := <-concurrentErrors; err != nil {
			t.Fatalf("concurrent semantic duplicate: %v", err)
		}
	}
	concurrentHome, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if concurrentHome.ProjectionVersion != replayedAfterDrift.ProjectionVersion+1 {
		t.Fatalf("concurrent duplicate Home version=%d, want %d",
			concurrentHome.ProjectionVersion, replayedAfterDrift.ProjectionVersion+1)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO assistant_conversations
  (conversation_id,tenant_id,actor_user_id,generation,status,next_turn_sequence,next_run_sequence,
   revision,created_at,updated_at)
VALUES ('conversation-home',$1,$2,1,'active',1,1,1,$3,$3)`,
		fixture.TenantID, fixture.ActorUserID, now); err != nil {
		t.Fatal(err)
	}
	conversationEvent := homeOwnerEvent(
		fixture.TenantID, "home-conversation-event-1", "conversation/conversation-home",
		"Conversation", "conversation-home", now,
	)
	if err := materializer.Publish(ctx, conversationEvent); err != nil {
		t.Fatal(err)
	}
	conversationHome, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: projectionpostgres.HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conversationHome.ProjectionVersion != concurrentHome.ProjectionVersion {
		t.Fatalf("Conversation-only event advanced Home version from %d to %d",
			concurrentHome.ProjectionVersion, conversationHome.ProjectionVersion)
	}
	if replayed, err := dispatcher.RunOnce(ctx); err != nil || replayed != 0 {
		t.Fatalf("Outbox replay count=%d error=%v", replayed, err)
	}
}

func seedPublishedHomeOutboxEvent(
	t *testing.T,
	pool *pgxpool.Pool,
	event contractsv1.IntegrationEvent,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO owner_stream_positions
  (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ($1,$2,$3,$4,$5,1,$6)`,
		event.StreamID, event.ProducerContext, event.AggregateType, event.AggregateID,
		event.Sequence+1, event.OccurredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,
   aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
   visibility,payload,occurred_at,published_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType,
		event.AggregateID, event.TraceID, event.CorrelationID, event.CausationID,
		event.IdempotencyKey, event.Visibility, event.Payload, event.OccurredAt); err != nil {
		t.Fatal(err)
	}
}

func seedHomeArtifact(
	t *testing.T,
	pool *pgxpool.Pool,
	tenantID, userID string,
	now time.Time,
) {
	t.Helper()
	ctx := context.Background()
	fixtures := []struct {
		statement string
		args      []any
	}{
		{statement: `INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,bound_task_id,session_revision,runtime_generation,quota_reservation_revision,
   idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES ('worker-artifact',$1,$2,'workspace-home','workspace-revision-home','copy-worker-artifact',
  'idle','task-b',2,1,1,$3::timestamptz + interval '48 hours',
  $3::timestamptz + interval '14 days',$3,$3::timestamptz + interval '12 minutes')`,
			args: []any{tenantID, userID, now}},
		{statement: `INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at,terminal_at)
VALUES ('run-artifact','worker-artifact','task-b','task_initial','{}'::jsonb,
  'deepseek-codex-openai-v1','1',
  'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
  'completed',3,$1,$1,$1::timestamptz + interval '1 minute')`,
			args: []any{now}},
		{statement: `INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at,terminal_at)
VALUES ('turn-artifact','worker-artifact','run-artifact','agent-artifact','prompt','{}'::jsonb,
  'completed',2,'turn-artifact-key',$1,$1,$1::timestamptz + interval '24 hours',
  $1::timestamptz + interval '1 minute')`,
			args: []any{now}},
		{statement: `INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,
   image_ref,resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,started_at)
VALUES ('runtime-artifact','worker-artifact','run-artifact',1,'docker_container',
  'container-artifact','active',
  'switchback-worker@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
  'local_standard_v1','trusted_full_network_v1',1,2,$1,$1)`,
			args: []any{now}},
		{statement: `INSERT INTO trace_manifests
  (trace_stream_id,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,
   runtime_instance_id,runtime_generation,manifest_object_key,manifest_checksum,manifest_size_bytes,
   manifest,publication_idempotency_key,visible_at)
VALUES ('trace-home',$1,$2,'workspace-home','worker-artifact','run-artifact','turn-artifact',
  'runtime-artifact',1,'traces/home.json',
  'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',2,
  '{}'::jsonb,'trace-home',$3)`,
			args: []any{tenantID, userID, now}},
		{statement: `INSERT INTO artifact_refs
  (artifact_id,trace_stream_id,tenant_id,user_id,workspace_id,task_id,worker_session_id,
   worker_run_id,agent_turn_id,runtime_instance_id,runtime_generation,artifact_type,object_key,
   object_checksum,size_bytes,artifact_ref,visible_at)
VALUES ('artifact-home','trace-home',$1,$2,'workspace-home',NULL,'worker-artifact',
  'run-artifact','turn-artifact','runtime-artifact',1,'report','artifacts/home.md',
  'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',4,
  '{}'::jsonb,$3::timestamptz + interval '2 minutes')`,
			args: []any{tenantID, userID, now}},
		{statement: `INSERT INTO artifact_refs
  (artifact_id,trace_stream_id,tenant_id,user_id,workspace_id,task_id,worker_session_id,
   worker_run_id,agent_turn_id,runtime_instance_id,runtime_generation,artifact_type,object_key,
   object_checksum,size_bytes,artifact_ref,visible_at)
VALUES ('artifact-other-scope','trace-home','tenant-other','user-other','workspace-home',NULL,
  'worker-artifact','run-artifact','turn-artifact','runtime-artifact',1,'patch',
  'artifacts/other.patch',
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',4,
  '{}'::jsonb,$1::timestamptz + interval '1 minute')`,
			args: []any{now}},
	}
	for index, fixture := range fixtures {
		if _, err := pool.Exec(ctx, fixture.statement, fixture.args...); err != nil {
			t.Fatalf("seed Home Artifact statement %d: %v", index+1, err)
		}
	}
}

func homeOwnerEvent(
	tenantID, eventID, streamID, aggregateType, aggregateID string,
	at time.Time,
) contractsv1.IntegrationEvent {
	eventType := "AssistantConversationChanged.v1"
	producerContext := "interaction"
	visibility := "internal"
	if aggregateType == "WorkerSession" {
		eventType = "WorkerTurnEvidencePublished.v1"
		producerContext = "worker-runtime"
		visibility = "user"
	} else if aggregateType == "Task" {
		eventType = "TaskTransitioned.v1"
		producerContext = "task-orchestration"
		visibility = "user"
	}
	return contractsv1.IntegrationEvent{
		SchemaVersion:   contractsv1.IntegrationEventSchemaVersion,
		EventID:         eventID,
		EventType:       eventType,
		ProducerContext: producerContext,
		StreamID:        streamID,
		Sequence:        1,
		TenantID:        tenantID,
		AggregateType:   aggregateType,
		AggregateID:     aggregateID,
		TraceID:         eventID,
		CorrelationID:   eventID,
		CausationID:     eventID,
		IdempotencyKey:  eventID,
		Visibility:      visibility,
		OccurredAt:      at,
		Payload:         json.RawMessage(`{"fixture":true}`),
	}
}

func seedHomeDashboardRows(
	t *testing.T,
	pool *pgxpool.Pool,
	tenantID, userID string,
	now time.Time,
) {
	t.Helper()
	ctx := context.Background()
	tasks := []struct {
		id, tenantID, userID, status string
		offset                       time.Duration
	}{
		{id: "task-a", tenantID: tenantID, userID: userID, status: "waiting_user", offset: 10 * time.Minute},
		{id: "task-b", tenantID: tenantID, userID: userID, status: "delivered", offset: 10 * time.Minute},
		{id: "task-c", tenantID: tenantID, userID: userID, status: "completed", offset: 9 * time.Minute},
		{id: "task-d", tenantID: tenantID, userID: userID, status: "delivered", offset: 8 * time.Minute},
		{id: "task-e", tenantID: tenantID, userID: userID, status: "waiting_user", offset: 7 * time.Minute},
		{id: "task-f", tenantID: tenantID, userID: userID, status: "completed", offset: 6 * time.Minute},
		{id: "task-g", tenantID: tenantID, userID: userID, status: "delivered", offset: 5 * time.Minute},
		{id: "task-h", tenantID: tenantID, userID: userID, status: "delivered", offset: 4 * time.Minute},
		{id: "task-i", tenantID: tenantID, userID: userID, status: "completed", offset: 3 * time.Minute},
		{id: "task-j", tenantID: tenantID, userID: userID, status: "waiting_user", offset: 2 * time.Minute},
		{id: "task-other-user", tenantID: tenantID, userID: "user-other", status: "delivered", offset: 30 * time.Minute},
		{id: "task-other-tenant", tenantID: "tenant-other", userID: userID, status: "delivered", offset: 30 * time.Minute},
	}
	for _, task := range tasks {
		assessmentID := "assessment-" + task.id
		intentID := "intent-" + task.id
		acceptanceMode := "manual"
		if task.id == "task-c" {
			acceptanceMode = "auto"
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ($1,$2,$3,'workspace-home','workspace-revision-home','api','readiness-v1',
  '{}'::jsonb,'[]'::jsonb,'[]'::jsonb,'ready_confirmed',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$4)`,
			assessmentID, task.tenantID, task.userID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,consumed_by_task_id,created_at,expires_at,consumed_at)
VALUES ($1,$2,$3,'workspace-home','workspace-revision-home',$4,
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'trigger-v1','consumed',$5,$6,$6::timestamptz + interval '1 hour',$6)`,
			intentID, task.tenantID, task.userID, assessmentID, task.id, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,creation_intent_id,status,acceptance_mode,revision,created_at,updated_at)
VALUES ($1,$2,$3,'workspace-home','workspace-revision-home',jsonb_build_object('title','Home ' || $1),
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  $4,$5,$6,2,$7,$8)`,
			task.id, task.tenantID, task.userID, intentID, task.status,
			acceptanceMode, now, now.Add(task.offset)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ($1,$2,12,7,2,'continuity-home-v1',$3)`, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	workers := []struct {
		id, tenantID, userID string
		offset               time.Duration
	}{
		{id: "worker-a", tenantID: tenantID, userID: userID, offset: 10 * time.Minute},
		{id: "worker-b", tenantID: tenantID, userID: userID, offset: 10 * time.Minute},
		{id: "worker-c", tenantID: tenantID, userID: userID, offset: 9 * time.Minute},
		{id: "worker-d", tenantID: tenantID, userID: userID, offset: 8 * time.Minute},
		{id: "worker-e", tenantID: tenantID, userID: userID, offset: 7 * time.Minute},
		{id: "worker-f", tenantID: tenantID, userID: userID, offset: 6 * time.Minute},
		{id: "worker-g", tenantID: tenantID, userID: userID, offset: 5 * time.Minute},
		{id: "worker-h", tenantID: tenantID, userID: userID, offset: 4 * time.Minute},
		{id: "worker-other-user", tenantID: tenantID, userID: "user-other", offset: 30 * time.Minute},
		{id: "worker-other-tenant", tenantID: "tenant-other", userID: userID, offset: 30 * time.Minute},
	}
	for index, worker := range workers {
		if _, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,bound_task_id,session_revision,runtime_generation,quota_reservation_revision,
   idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES ($1,$2,$3,'workspace-home','workspace-revision-home',$4,
  'idle',$5,2,$6,1,$7::timestamptz + interval '48 hours',
  $7::timestamptz + interval '14 days',$7,$8)`,
			worker.id, worker.tenantID, worker.userID, "copy-"+worker.id,
			"task-"+string(rune('a'+index%8)), index+1, now, now.Add(worker.offset)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertHomeIDs(t *testing.T, label string, want, got []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s=%v, want %v", label, got, want)
	}
}

type materializerIDs struct {
	mu   sync.Mutex
	next uint64
}

func (ids *materializerIDs) NewID(kind string) (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("%s-%03d", kind, ids.next), nil
}

func trustedNetworkProfile(t *testing.T) config.RuntimeNetworkProfile {
	t.Helper()
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
