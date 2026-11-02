//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestPublishTurnEvidenceMakesOnlyVerifiedMetadataVisible(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-1", "artifact-patch-1"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	publisher := artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-evidence-1"}},
		Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	command := artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-turn-1",
	}

	first, err := publisher.Execute(ctx, command)
	if err != nil {
		t.Fatalf("publish Turn evidence: %v", err)
	}
	if first.TraceStreamID != "trace-stream-1" || !reflect.DeepEqual(first.ArtifactIDs, []string{"artifact-report-1", "artifact-patch-1"}) {
		t.Fatalf("publication result = %#v", first)
	}
	assertCount(t, pool, "trace_manifests", 1)
	assertCount(t, pool, "artifact_refs", 2)
	assertCount(t, pool, "evidence_object_refs", 5)
	var eventType, visibility string
	if err := pool.QueryRow(ctx, `SELECT event_type,visibility FROM outbox_events
WHERE event_id='event-evidence-1'`).Scan(&eventType, &visibility); err != nil {
		t.Fatal(err)
	}
	if eventType != "WorkerTurnEvidencePublished.v1" || visibility != "user" {
		t.Fatalf("Outbox event type=%s visibility=%s", eventType, visibility)
	}
	var objectChecksum string
	var artifactRef contractsv1.ArtifactRef
	if err := pool.QueryRow(ctx, `SELECT object_checksum,artifact_ref FROM artifact_refs
WHERE artifact_id='artifact-report-1'`).Scan(&objectChecksum, &artifactRef); err != nil {
		t.Fatal(err)
	}
	if objectChecksum != "sha256:"+artifactRef.SHA256 || artifactRef.AgentTurnID != "turn-1" {
		t.Fatalf("visible Artifact metadata checksum=%s ref=%#v", objectChecksum, artifactRef)
	}

	retryPublisher := artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-evidence-unused"}},
		Now: func() time.Time { return now.Add(3 * time.Minute) },
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET session_revision=5 WHERE worker_session_id='session-1'`); err != nil {
		t.Fatal(err)
	}
	second, err := retryPublisher.Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry publication: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("retry result=%#v, want %#v", second, first)
	}
	assertCount(t, pool, "trace_manifests", 1)
	assertCount(t, pool, "artifact_refs", 2)
	assertCount(t, pool, "outbox_events", 1)
}

func TestOrphanReconciliationPreservesPublishedObjectsAndDeletesRolledBackPublication(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-orphan", "artifact-patch-orphan"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	publisher := artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-orphan-rollback"}},
		Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	_, err = publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 999, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-stale",
	})
	if !errors.Is(err, artifactpostgres.ErrEvidenceTargetStale) {
		t.Fatalf("publication error = %v, want stale target", err)
	}
	assertCount(t, pool, "trace_manifests", 0)
	assertCount(t, pool, "artifact_refs", 0)
	assertCount(t, pool, "evidence_object_refs", 0)

	reconciler := artifactapp.OrphanReconciler{
		Objects: objects, Metadata: artifactpostgres.NewOrphanStore(pool),
		Now: func() time.Time { return now.Add(11 * time.Minute) },
	}
	result, err := reconciler.Execute(ctx, artifactapp.ReconcileOrphansCommand{
		OlderThan: now.Add(10 * time.Minute), Limit: 100,
	})
	if err != nil {
		t.Fatalf("reconcile rolled-back publication: %v", err)
	}
	if result.Observed != 5 || result.Claimed != 5 || result.Deleted != 5 || result.Failed != 0 {
		t.Fatalf("reconciliation result = %#v", result)
	}
	assertCount(t, pool, "object_reconciliation_claims", 5)
	for _, object := range append(finalized.Objects, artifactapp.FinalizedObject{Kind: "trace-manifest", Stored: finalized.ManifestObject}) {
		if err := objects.Verify(ctx, object.Stored.Key, object.Stored.Checksum); err == nil {
			t.Fatalf("orphan object %s remains after reconciliation", object.Kind)
		}
	}
}

func TestLateOldGenerationCannotPublishArtifactMetadataAfterReplacement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 26, 15, 0, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-late", "artifact-patch-late"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1",
			TraceLifecyclePolicyRef:    "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances SET status='lost',revision=3,ended_at=$2
WHERE runtime_instance_id=$1`,
		"runtime-1", now.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,
   image_ref,resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,started_at)
SELECT 'runtime-2',worker_session_id,worker_run_id,2,runtime_type,'container-2','active',
       image_ref,resource_profile_id,network_policy_id,2,1,$2,$2
FROM runtime_instances WHERE runtime_instance_id=$1`,
		"runtime-1", now.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id='runtime-2',runtime_generation=2,session_revision=5
WHERE worker_session_id='session-1'`); err != nil {
		t.Fatal(err)
	}
	_, err = (artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-late-old-generation"}},
		Now: func() time.Time { return now.Add(3 * time.Minute) },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 5, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 3,
		IdempotencyKey: "publish-late-old-generation",
	})
	if !errors.Is(err, artifactpostgres.ErrEvidenceTargetStale) {
		t.Fatalf("late old-generation publication error=%v", err)
	}
	assertCount(t, pool, "trace_manifests", 0)
	assertCount(t, pool, "artifact_refs", 0)
	assertCount(t, pool, "outbox_events", 0)
}

func TestOrphanReconciliationDoesNotClaimPublishedEvidence(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-live", "artifact-patch-live"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-live"}}, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-live",
	})
	if err != nil {
		t.Fatal(err)
	}
	orphanIdentity := workerapp.ObjectIdentity{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", Kind: "debug-orphan",
	}
	temporary, err := objects.PutTemporary(ctx, orphanIdentity, strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := objects.Finalize(ctx, orphanIdentity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (artifactapp.OrphanReconciler{
		Objects: objects, Metadata: artifactpostgres.NewOrphanStore(pool),
		Now: func() time.Time { return now.Add(11 * time.Minute) },
	}).Execute(ctx, artifactapp.ReconcileOrphansCommand{OlderThan: now.Add(10 * time.Minute), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Observed != 6 || result.Claimed != 1 || result.Deleted != 1 {
		t.Fatalf("reconciliation result = %#v", result)
	}
	if err := objects.Verify(ctx, orphan.Key, orphan.Checksum); err == nil {
		t.Fatal("unreferenced object was not deleted")
	}
	for _, object := range append(finalized.Objects, artifactapp.FinalizedObject{Kind: "trace-manifest", Stored: finalized.ManifestObject}) {
		if err := objects.Verify(ctx, object.Stored.Key, object.Stored.Checksum); err != nil {
			t.Fatalf("published object %s was deleted: %v", object.Kind, err)
		}
	}
}

func TestClaimedOrphanFencesConcurrentEvidencePublication(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-claimed", "artifact-patch-claimed"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := objects.ListCandidates(ctx, now.Add(10*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := artifactpostgres.NewOrphanStore(pool).ClaimOrphanObjects(ctx, artifactapp.ClaimOrphansRecord{
		Candidates: candidates, ClaimedAt: now.Add(11 * time.Minute), ClaimExpiresAt: now.Add(16 * time.Minute),
	})
	if err != nil || len(claimed) != 5 {
		t.Fatalf("claim finalized evidence count=%d error=%v", len(claimed), err)
	}
	_, err = (artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-claimed"}}, Now: func() time.Time { return now.Add(12 * time.Minute) },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-claimed",
	})
	if !errors.Is(err, artifactpostgres.ErrPublicationConflict) {
		t.Fatalf("publication error = %v, want reconciliation conflict", err)
	}
	assertCount(t, pool, "trace_manifests", 0)
	assertCount(t, pool, "artifact_refs", 0)
	assertCount(t, pool, "evidence_object_refs", 0)
	assertCount(t, pool, "outbox_events", 0)
}

func seedEvidenceTarget(t *testing.T, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ('tenant-local','user-local',10,1,1,'continuity-local-v1',$1)`,
		`INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,active_runtime_instance_id,bound_task_id,active_worker_run_id,session_revision,
   runtime_generation,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES ('session-1','tenant-local','user-local','workspace-demo','workspace-revision-1','working-copy-1',
  'running','runtime-1','task-1','run-1',4,1,1,$1::timestamptz + interval '48 hours',
  $1::timestamptz + interval '14 days',$1,$1)`,
		`INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ('assessment-1','tenant-local','user-local','workspace-demo','workspace-revision-1','api',
  'readiness-v1','{}'::jsonb,'[]'::jsonb,'[]'::jsonb,'ready_confirmed',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$1)`,
		`INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,consumed_by_task_id,created_at,expires_at,consumed_at)
VALUES ('intent-1','tenant-local','user-local','workspace-demo','workspace-revision-1','assessment-1',
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'trigger-v1','consumed','task-1',$1,$1::timestamptz + interval '1 hour',$1)`,
		`INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,creation_intent_id,worker_session_id,current_worker_run_id,status,
   acceptance_mode,revision,created_at,updated_at)
VALUES ('task-1','tenant-local','user-local','workspace-demo','workspace-revision-1','{}'::jsonb,
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'intent-1','session-1','run-1','running','manual',2,$1,$1)`,
		`INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at,terminal_at)
VALUES ('run-1','session-1','task-1','task_initial','{}'::jsonb,'deepseek-codex-openai-v1','1',
  'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
  'completed',3,$1,$1,$1::timestamptz + interval '1 minute')`,
		`INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at,terminal_at)
VALUES ('turn-1','session-1','run-1','agent-1','prompt','{}'::jsonb,'completed',2,'turn-1-key',
  $1,$1,$1::timestamptz + interval '24 hours',$1::timestamptz + interval '1 minute')`,
		`INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,
   image_ref,resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,started_at)
VALUES ('runtime-1','session-1','run-1',1,'docker_container','container-1','active',
  'switchback-worker@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
  'local_standard_v1','m1_public_web_restricted_v1',1,2,$1,$1)`,
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed evidence target statement %d: %v", index+1, err)
		}
	}
}

func finalizeCommand(t *testing.T, now time.Time) artifactapp.FinalizeTurnEvidenceCommand {
	t.Helper()
	return artifactapp.FinalizeTurnEvidenceCommand{
		Identity: artifactdomain.TurnIdentity{
			TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo", TaskID: "task-1",
			WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentSessionID: "agent-1",
			AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
			TraceStreamID: "trace-stream-1", LangfuseTraceID: "trace-1",
			RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
				ID: "m1_public_web_restricted_v1", EnforcementStatus: "legacy_unenforced",
			},
		},
		ServerRecordedAt: now, WorkerRecordedAt: now.Add(-time.Second),
		RawACPJSONL: []byte(`{"type":"acp.raw_frame","direction":"agent_to_client","frame":"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"agent-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"Done.\"}}}}\n"}` + "\n"),
		Patch:       []byte("diff --git a/file b/file\n"),
		SourceCheckpointRef: contractsv1.CheckpointRef{
			CheckpointID: "checkpoint-1", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
	}
}

type sequenceIDs struct {
	values []string
	next   int
}

func (ids *sequenceIDs) NewID(string) (string, error) {
	value := ids.values[ids.next]
	ids.next++
	return value, nil
}

func assertCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count=%d, want %d", table, count, want)
	}
}
