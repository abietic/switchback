//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestLangfuseFailureRecordsDegradationWithoutChangingCompletedRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 13, 0, 0, 0, time.UTC)
	projection := publishObservabilityEvidence(t, pool, now)
	service := artifactapp.ObservabilityProjectionService{
		Projector: failingProjection{}, Store: artifactpostgres.NewObservabilityStore(pool),
		Now: func() time.Time { return now.Add(3 * time.Minute) },
	}

	result, err := service.Execute(ctx, artifactapp.ProjectRunObservabilityCommand{
		Projection: projection, ExecutionOutcome: "completed",
	})
	if err != nil {
		t.Fatalf("derived projection failure changed execution result: %v", err)
	}
	if result.ExecutionOutcome != "completed" || result.ObservabilityStatus != artifactapp.ObservabilityDegraded {
		t.Fatalf("result = %#v", result)
	}
	assertObservabilityRow(t, pool, "degraded", "langfuse_projection_failed", "completed", 1)
	var runStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM worker_runs WHERE worker_run_id='run-1'`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" {
		t.Fatalf("WorkerRun status = %q", runStatus)
	}
	resultContract := failurematrix.Result{
		Variant: "langfuse-unavailable", ExecutionOutcome: "completed", AcceptanceOutcome: "failed",
		Assertions: []string{"execution_preserved", "observability_degraded", "reprojection_required"},
	}
	failurematrix.AssertAndRecord(t, resultContract, resultContract)

	service.Projector = successfulProjection{}
	service.Now = func() time.Time { return now.Add(4 * time.Minute) }
	result, err = service.Execute(ctx, artifactapp.ProjectRunObservabilityCommand{
		Projection: projection, ExecutionOutcome: "completed",
	})
	if err != nil || result.ObservabilityStatus != artifactapp.ObservabilityPassed {
		t.Fatalf("reprojection result=%#v error=%v", result, err)
	}
	assertObservabilityRow(t, pool, "passed", "", "completed", 2)
}

func publishObservabilityEvidence(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
) artifactapp.RunObservabilityProjection {
	t.Helper()
	ctx := context.Background()
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{
		Store: objects, IDs: &sequenceIDs{values: []string{"artifact-observe-report", "artifact-observe-patch"}},
		Now: func() time.Time { return now.Add(time.Minute) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}).Execute(ctx, finalizeCommand(t, now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (artifactapp.ArtifactMetadataPublisher{
		Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool),
		IDs: &sequenceIDs{values: []string{"event-observe"}}, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}).Execute(ctx, artifactapp.PublishTurnEvidenceCommand{
		Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-observe",
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifactapp.RunObservabilityProjection{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, AgentSessionID: "agent-1", TraceID: "trace-1",
		Turns: []artifactapp.CompletedTurnObservation{{
			AgentTurnID: "turn-1", StartedAt: now, EndedAt: now.Add(time.Minute),
			Input: json.RawMessage(`{"text":"complete-turn-input"}`), Output: json.RawMessage(`"complete-turn-output"`),
			TraceManifest: finalized.Manifest,
			ModelObservations: []workerapp.ModelObservation{{
				ObservationID: "model-observe-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
				AgentTurnID: "turn-1", RuntimeID: "runtime-1", Generation: 1,
				Model: "deepseek/deepseek-chat", Route: "deepseek-codex-openai-v1",
				InputTokens: 10, OutputTokens: 2, StartedAt: now, EndedAt: now.Add(time.Minute), Status: "ok",
				Input:  json.RawMessage(`{"messages":[{"role":"user","content":"complete-model-input"}]}`),
				Output: json.RawMessage(`{"choices":[{"message":{"content":"complete-model-output"}}]}`),
			}},
		}},
	}
}

func assertObservabilityRow(
	t *testing.T,
	pool *pgxpool.Pool,
	wantStatus, wantErrorCode, wantOutcome string,
	wantRevision uint64,
) {
	t.Helper()
	var status, outcome string
	var errorCode *string
	var revision uint64
	var verifiedAt *time.Time
	if err := pool.QueryRow(context.Background(), `
SELECT status,error_code,execution_outcome,revision,verified_at
FROM run_observability_projections WHERE worker_run_id='run-1'`).Scan(
		&status, &errorCode, &outcome, &revision, &verifiedAt,
	); err != nil {
		t.Fatal(err)
	}
	gotErrorCode := ""
	if errorCode != nil {
		gotErrorCode = *errorCode
	}
	if status != wantStatus || gotErrorCode != wantErrorCode || outcome != wantOutcome || revision != wantRevision {
		t.Fatalf("projection row status=%q error=%q outcome=%q revision=%d", status, gotErrorCode, outcome, revision)
	}
	if (wantStatus == artifactapp.ObservabilityPassed) != (verifiedAt != nil) {
		t.Fatalf("verified_at presence = %t for status %q", verifiedAt != nil, wantStatus)
	}
}

type failingProjection struct{}

func (failingProjection) ExportAndVerify(context.Context, artifactapp.RunObservabilityProjection) error {
	return errors.New("Langfuse unavailable with sk-sensitive-raw")
}

type successfulProjection struct{}

func (successfulProjection) ExportAndVerify(context.Context, artifactapp.RunObservabilityProjection) error {
	return nil
}
