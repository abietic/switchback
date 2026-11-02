//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifacthttp "github.com/abietic/switchback/internal/artifacttrace/http"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestArtifactReviewUsesAuthorizedChecksumVerifiedObjectContent(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 25, 17, 0, 0, 0, time.UTC)
	seedDeliveredBoundTask(t, ctx, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := "Warning: Model metadata for `switchback-codex` not found. " +
		"Defaulting to fallback metadata; this can degrade performance and cause issues. " +
		"## Result\nDone."
	identity := workerapp.ObjectIdentity{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		Kind: "report",
	}
	temporary, err := objects.PutTemporary(ctx, identity, strings.NewReader(report))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := objects.Finalize(ctx, identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO trace_manifests
 (trace_stream_id,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,
  runtime_instance_id,runtime_generation,manifest_object_key,manifest_checksum,manifest_size_bytes,
  manifest,publication_idempotency_key,visible_at)
VALUES
 ('trace-1','tenant-local','user-local','workspace-local','session-1','run-1','turn-1',
  'runtime-1',1,'manifest-object',$1,2,'{}','publish-1',$2)`,
		"sha256:"+strings.Repeat("a", 64), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO artifact_refs
 (artifact_id,trace_stream_id,tenant_id,user_id,workspace_id,task_id,worker_session_id,
  worker_run_id,agent_turn_id,runtime_instance_id,runtime_generation,artifact_type,object_key,
  object_checksum,size_bytes,artifact_ref,visible_at)
VALUES
 ('artifact-report-1','trace-1','tenant-local','user-local','workspace-local','task-1',
  'session-1','run-1','turn-1','runtime-1',1,'report',$1,$2,$3,
  '{"availability":"online","completeness":"complete"}',$4)`,
		stored.Key, stored.Checksum, stored.Size, now,
	); err != nil {
		t.Fatal(err)
	}

	materializer := Materializer{Pool: pool, Objects: objects, UserID: "user-local", Now: func() time.Time { return now }}
	artifacts, err := materializer.artifacts(ctx, "tenant-local", "session-1")
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v error=%v", artifacts, err)
	}
	raw, actions, err := materializer.artifactDetail(ctx, "tenant-local", artifacts[0])
	if err != nil {
		t.Fatal(err)
	}
	var detail artifactReviewDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Availability != "online" || !detail.Review.Renderable ||
		!detail.Review.DownloadAllowed || detail.Content != "## Result\nDone." ||
		len(detail.Review.Diagnostics) != 1 ||
		detail.Review.Diagnostics[0].Code != "model_metadata_fallback" ||
		detail.DownloadURL == "" || len(actions) != 1 {
		t.Fatalf("Artifact detail=%+v actions=%+v", detail, actions)
	}
	if _, err := (artifactpostgres.ContentReader{Pool: pool}).ReadArtifactContent(
		ctx,
		artifacthttp.FixedViewer{TenantID: "tenant-local", UserID: "other-user"},
		"artifact-report-1",
	); err == nil {
		t.Fatal("cross-user Artifact content read succeeded")
	}

	checksumFailed := artifacts[0]
	checksumFailed.Checksum = "sha256:" + strings.Repeat("0", 64)
	raw, actions, err = materializer.artifactDetail(ctx, "tenant-local", checksumFailed)
	if err != nil {
		t.Fatal(err)
	}
	detail = artifactReviewDetail{}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Availability != "checksum_failed" || detail.Review.DownloadAllowed ||
		detail.Content != "" || len(actions) != 0 {
		t.Fatalf("checksum-failed detail=%+v actions=%+v", detail, actions)
	}

	missing := artifacts[0]
	missing.ObjectKey = strings.TrimSuffix(stored.Key, strings.TrimPrefix(stored.Checksum, "sha256:")) +
		strings.Repeat("0", 64)
	missing.Checksum = "sha256:" + strings.Repeat("0", 64)
	raw, _, err = materializer.artifactDetail(ctx, "tenant-local", missing)
	if err != nil {
		t.Fatal(err)
	}
	detail = artifactReviewDetail{}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Availability != "missing" {
		t.Fatalf("missing detail=%+v", detail)
	}
}

type artifactReviewDetail struct {
	Availability string `json:"availability"`
	Content      string `json:"content"`
	DownloadURL  string `json:"download_url"`
	Review       struct {
		Outcome         string `json:"outcome"`
		Renderable      bool   `json:"renderable"`
		DownloadAllowed bool   `json:"download_allowed"`
		Diagnostics     []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	} `json:"review_manifest"`
}
