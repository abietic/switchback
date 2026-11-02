//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"testing"
)

func TestFragmentCoverageExactVersionAndSelectedSources(t *testing.T) {
	f := newFragmentFixtureMode(t, true, true)
	f.publishCore(t)
	ctx := context.Background()
	r := historicalRequest(t, f, true)
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	r.Models = nil
	if _, err := h.Request(ctx, r); err != nil {
		t.Fatal(err)
	}
	c := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local")
	got, err := c.Read(ctx, r.ActorID, r.Scope, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted != 2 || got.Sealed != 2 || got.Published != 0 || got.Verified != 0 || len(got.Sources) != 2 {
		t.Fatalf("before publication %+v", got)
	}
	publisher := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 2 {
		if err = publisher.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO observation_fragment_jobs(version,kind,source_id,frame_digest,status) SELECT 'other-generation',kind,source_id,frame_digest,'passed' FROM observation_publications`); err != nil {
		t.Fatal(err)
	}
	// A later publication in the excluded Run is not covered by an earlier
	// exact request. It remains invisible even though the scope is identical.
	if _, err = f.pool.Exec(ctx, `WITH candidate AS (
 INSERT INTO observation_candidates(kind,source_id,frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status)
 SELECT 'model','late-unselected',frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,'published' FROM observation_candidates WHERE kind='run_basis' RETURNING *)
 INSERT INTO observation_publications(kind,source_id,frame_digest,object_key,object_size,object_checksum,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
 SELECT c.kind,c.source_id,c.frame_digest,p.object_key,p.object_size,p.object_checksum,c.tenant_id,c.user_id,c.workspace_id,c.worker_session_id,c.worker_run_id FROM candidate c JOIN observation_publications p ON p.kind='run_basis'`); err != nil {
		t.Fatal(err)
	}
	got, err = c.Read(ctx, r.ActorID, r.Scope, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted != 2 || got.Published != 2 || got.Verified != 0 {
		t.Fatalf("version mixed %+v", got)
	}
	if _, err = c.Read(ctx, "other", r.Scope, r.Version); err == nil {
		t.Fatal("coverage exposed another actor")
	}
	wrong := r.Scope
	wrong.WorkspaceID = "other"
	if _, err = c.Read(ctx, r.ActorID, wrong, r.Version); err == nil {
		t.Fatal("coverage exposed another scope")
	}
	if count, err := artifactpostgres.NewFragmentJobStore(f.pool).Discover(ctx, r.Version, 10); err != nil || count != 2 {
		t.Fatalf("history unlocked unselected model: %d %v", count, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE observation_fragment_jobs SET status='passed' WHERE version=$1 AND kind='run_basis'`, r.Version); err != nil {
		t.Fatal(err)
	}
	got, err = c.Read(ctx, r.ActorID, r.Scope, r.Version)
	if err != nil || got.Verified != 1 || got.Published != 2 || got.Accepted != 2 {
		t.Fatalf("exact current verification %+v %v", got, err)
	}
}

func TestFragmentCoverageIncludesNoContentGapsAndDoesNotRepairThem(t *testing.T) {
	f := newFragmentFixtureMode(t, true, false, false, true)
	f.publishCore(t)
	ctx := context.Background()
	r := historicalRequest(t, f, true)
	before := fragmentProofSnapshot(t, f.pool)
	p := &historicalPreparer{}
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, p)
	if _, err := h.Request(ctx, r); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || !bytes.Equal(before, fragmentProofSnapshot(t, f.pool)) {
		t.Fatal("blocked proof was repaired")
	}
	c := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local")
	got, err := c.Read(ctx, r.ActorID, r.Scope, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted != 3 || got.Sealed != 0 || got.Published != 0 || got.Verified != 0 || len(got.GapCodes) == 0 || len(got.Sources) != 3 {
		t.Fatalf("no-content gap disappeared %+v", got)
	}
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte("all_models_complete")) {
		t.Fatal("coverage claims callback closure")
	}
}

func TestFragmentCoverageCountsAcceptedUnrenderableModelGap(t *testing.T) {
	f := newFragmentFixtureMode(t, true, false, false, true, true)
	f.publishCore(t)
	r := historicalRequest(t, f, true)
	reader := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local")
	got, err := reader.Read(context.Background(), r.ActorID, r.Scope, r.Version)
	if err != nil || got.Accepted != 3 || got.Sealed != 0 || len(got.GapCodes) == 0 {
		t.Fatalf("accepted unrenderable no-content source disappeared: %+v err=%v", got, err)
	}
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", nil, nil)
	if _, err = h.Request(context.Background(), r); err != nil {
		t.Fatalf("could not select honest blocked gap: %v", err)
	}
}
