//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func navigationQuery(t *testing.T, f *fragmentFixture, turn bool) artifactapp.ProjectionNavigationQuery {
	t.Helper()
	r := historicalRequest(t, f, false)
	q := artifactapp.ProjectionNavigationQuery{ActorID: r.ActorID, Scope: r.Scope}
	if turn {
		q.AgentTurnID = f.started.AgentTurnID
	}
	return q
}

func finishNavigationJobs(t *testing.T, f *fragmentFixture, version string, includeModel bool) {
	t.Helper()
	ctx := context.Background()
	jobs := artifactpostgres.NewFragmentJobStore(f.pool)
	if _, err := jobs.Discover(ctx, version, 20); err != nil {
		t.Fatal(err)
	}
	for {
		job, ok, err := jobs.Claim(ctx, version, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
		completion := artifactapp.ProjectionCompletion{Status: "passed"}
		if job.Source.Kind == "model" && !includeModel {
			completion = artifactapp.ProjectionCompletion{Status: "blocked", ErrorCode: "fragment_proof_invalid"}
		}
		if err = jobs.Finish(ctx, job, completion); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionNavigationScopeVersionAndNoContentFailClosed(t *testing.T) {
	f := newFragmentFixture(t, false)
	publishNavigationBasis(t, f)
	finishNavigationJobs(t, f, "langfuse.projection.v2", false)
	reader := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	query := navigationQuery(t, f, true)
	got, err := reader.ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationPending || got.Identity != nil {
		t.Fatalf("wrong version proved identity: %+v %v", got, err)
	}
	for _, field := range []string{"actor", "tenant", "user", "workspace", "session", "run", "turn"} {
		t.Run(field, func(t *testing.T) {
			wrong := query
			switch field {
			case "actor":
				wrong.ActorID = "other"
			case "tenant":
				wrong.Scope.TenantID = "other"
			case "user":
				wrong.Scope.UserID = "other"
			case "workspace":
				wrong.Scope.WorkspaceID = "other"
			case "session":
				wrong.Scope.WorkerSessionID = "other"
			case "run":
				wrong.Scope.WorkerRunID = "other"
			case "turn":
				wrong.AgentTurnID = "other"
			}
			got, err := reader.ReadProjectionNavigation(context.Background(), wrong)
			if got.Identity != nil || got.Status == artifactapp.ProjectionNavigationVerified || field != "turn" && err == nil {
				t.Fatalf("scope exposed identity %+v err=%v", got, err)
			}
		})
	}
	blocked := newFragmentFixtureMode(t, false, false, false, true)
	blocked.publishCore(t)
	got, err = artifactpostgres.NewProjectionNavigationStore(blocked.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(context.Background(), navigationQuery(t, blocked, true))
	if err != nil || got.Status != artifactapp.ProjectionNavigationBlocked || got.Identity != nil {
		t.Fatalf("no-content: %+v %v", got, err)
	}
}

func TestProjectionNavigationHistoryRequiresExplicitSelectionAndDoesNotRepair(t *testing.T) {
	f := newFragmentFixtureMode(t, false, true)
	f.publishCore(t)
	reader := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	query := navigationQuery(t, f, true)
	before := fragmentProofSnapshot(t, f.pool)
	got, err := reader.ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationIdentityUnavailable || got.Identity != nil || !bytes.Equal(before, fragmentProofSnapshot(t, f.pool)) {
		t.Fatalf("excluded read repaired/linked: %+v %v", got, err)
	}
	history := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	if _, err := history.Request(context.Background(), historicalRequest(t, f, false)); err != nil {
		t.Fatal(err)
	}
	publishNavigationBasis(t, f)
	finishNavigationJobs(t, f, fragmentVersion, false)
	before = fragmentProofSnapshot(t, f.pool)
	got, err = reader.ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || got.Identity == nil || !bytes.Equal(before, fragmentProofSnapshot(t, f.pool)) {
		t.Fatalf("selected history: %+v %v", got, err)
	}
	coverage, err := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local").Read(context.Background(), query.ActorID, query.Scope, fragmentVersion)
	if err != nil || coverage.Accepted != 2 || coverage.Verified != 2 || !reflect.DeepEqual(got.Identity.Sources, coverage.Sources) {
		t.Fatalf("coverage selection disagrees: %+v %+v %v", got, coverage, err)
	}
}

func TestProjectionNavigationHistoricalRequestDatabaseFailureReturnsError(t *testing.T) {
	f := newFragmentFixtureMode(t, false, true)
	f.publishCore(t)
	ctx := context.Background()
	history := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	if _, err := history.Request(ctx, historicalRequest(t, f, false)); err != nil {
		t.Fatal(err)
	}
	publishNavigationBasis(t, f)
	finishNavigationJobs(t, f, fragmentVersion, false) // synthetic passed; no remote readback
	query := navigationQuery(t, f, true)
	baseline := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	if got, err := baseline.ReadProjectionNavigation(ctx, query); err != nil || got.Identity == nil {
		t.Fatalf("valid historical navigation prerequisite: %+v %v", got, err)
	}

	var schema string
	if err := f.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	// This short-lived role can read coverage's request columns, but not the
	// request_digest used only by the subsequent provenance request query.
	// All immutable successful evidence stays intact.
	role := pgx.Identifier{schema + "_navigation_reader"}.Sanitize()
	schemaID := pgx.Identifier{schema}.Sanitize()
	if _, err := f.pool.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, sql := range []string{
			"REVOKE ALL ON ALL TABLES IN SCHEMA " + schemaID + " FROM " + role,
			"REVOKE SELECT (tenant_id,actor_user_id,version,request_bytes,source_set) ON observation_fragment_requests FROM " + role,
			"REVOKE USAGE ON SCHEMA " + schemaID + " FROM " + role,
			"DROP ROLE " + role,
		} {
			if _, err := f.pool.Exec(cleanupCtx, sql); err != nil {
				t.Errorf("clean isolated navigation reader role: %v", err)
			}
		}
	})
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA " + schemaID + " TO " + role,
		"GRANT SELECT ON ALL TABLES IN SCHEMA " + schemaID + " TO " + role,
		"REVOKE SELECT ON observation_fragment_requests FROM " + role,
		"GRANT SELECT (tenant_id,actor_user_id,version,request_bytes,source_set) ON observation_fragment_requests TO " + role,
	} {
		if _, err := f.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	config := f.pool.Config().Copy()
	previousAfterConnect := config.AfterConnect
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := previousAfterConnect(ctx, conn); err != nil {
			return err
		}
		_, err := conn.Exec(ctx, "SET ROLE "+role)
		return err
	}
	restricted, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restricted.Close) // registered last: closes before role cleanup
	coverage, err := artifactpostgres.NewFragmentCoverageStore(restricted, "tenant-local", "user-local").Read(ctx, query.ActorID, query.Scope, fragmentVersion)
	if err != nil || coverage.Verified != 2 {
		t.Fatalf("restricted coverage prerequisite: %+v %v", coverage, err)
	}
	reader := artifactpostgres.NewProjectionNavigationStore(restricted, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	got, err := reader.ReadProjectionNavigation(ctx, query)
	var dbError *pgconn.PgError
	if got.Identity != nil || !errors.As(err, &dbError) || dbError.Code != "42501" {
		t.Fatalf("historical request database failure must propagate without identity: %+v err=%v", got, err)
	}
	t.Logf("coverage verified=%d; provenance request returned PostgreSQL %s: %s; identity=%v", coverage.Verified, dbError.Code, dbError.Message, got.Identity)
}

func TestProjectionNavigationExcludedPassedBasisNeedsExactHistorySelection(t *testing.T) {
	f := newFragmentFixture(t, false)
	publishNavigationBasis(t, f)
	finishNavigationJobs(t, f, fragmentVersion, false)
	query := navigationQuery(t, f, true)
	// Negative eligibility fixture: add a new exclusion record; published
	// candidates and passed jobs remain immutable and untouched.
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO observation_fragment_run_exclusions(version,worker_run_id,tenant_id,user_id,workspace_id,worker_session_id) VALUES($1,$2,$3,$4,$5,$6)`, fragmentVersion, query.Scope.WorkerRunID, query.Scope.TenantID, query.Scope.UserID, query.Scope.WorkspaceID, query.Scope.WorkerSessionID); err != nil {
		t.Fatal(err)
	}
	reader := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	got, err := reader.ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationIdentityUnavailable || got.Identity != nil {
		t.Fatalf("passed bypassed exclusion: %+v %v", got, err)
	}
	history := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	if _, err = history.Request(context.Background(), historicalRequest(t, f, false)); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || got.Identity == nil {
		t.Fatalf("explicit selected existing passed basis: %+v %v", got, err)
	}
}

func TestProjectionNavigationModelGapDoesNotEraseBasisIdentity(t *testing.T) {
	f := newFragmentFixture(t, true)
	f.publishCore(t)
	p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 3 {
		if err := p.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	finishNavigationJobs(t, f, fragmentVersion, false)
	query := navigationQuery(t, f, true)
	got, err := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || got.Identity == nil || len(got.Identity.Sources) != 2 {
		t.Fatalf("model gap erased basis: %+v %v", got, err)
	}
	coverage, err := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local").Read(context.Background(), query.ActorID, query.Scope, fragmentVersion)
	if err != nil || coverage.Accepted != 3 || coverage.Verified != 2 || len(coverage.GapCodes) == 0 {
		t.Fatalf("coverage pretended complete: %+v %v", coverage, err)
	}
	raw, _ := json.Marshal(got)
	for _, forbidden := range []string{"input_json", "output_json", "receipt", "frame\"", "all_models_complete", "verified_at", "project_id", "endpoint"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("navigation leaked/claimed %s: %s", forbidden, raw)
		}
	}
}

func TestProjectionNavigationLaterTurnKeepsInitialRootAndOriginalRuntime(t *testing.T) {
	first := newFragmentFixture(t, false)
	publishNavigationBasis(t, first)
	finishNavigationJobs(t, first, fragmentVersion, false)
	reader := artifactpostgres.NewProjectionNavigationStore(first.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	initial, err := reader.ReadProjectionNavigation(context.Background(), navigationQuery(t, first, true))
	if err != nil || initial.Identity == nil {
		t.Fatalf("initial %+v %v", initial, err)
	}
	f := seedFragmentLaterTurn(t, first)
	f.journal.Now = func() time.Time { return f.now }
	e := f.envelope("event", 2, "navigation-later-terminal", "agent_turn.terminal", json.RawMessage(`{}`))
	e.AckSequence = f.start.Sequence
	if _, err = f.journal.AcceptEvent(context.Background(), workerapp.AcceptRuntimeControlEventCommand{Authority: f.authority, Sequence: 2, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: fragmentJSON(t, e)}); err != nil {
		t.Fatal(err)
	}
	f.settlements.Now = func() time.Time { return f.now }
	id, _ := workerapp.TurnSettlementID(f.started.AgentTurnID)
	f.current, err = f.settlements.Claim(context.Background(), workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: 1, OwnerID: "navigation-later-owner"})
	if err != nil {
		t.Fatal(err)
	}
	f.freezeAndFinalize(t)
	f.publishCore(t)
	p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	if err = p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishNavigationJobs(t, f, fragmentVersion, false)
	// Session's current generation may advance while immutable Turn source keeps
	// its original runtime. This changes no sealed candidate/publication/job.
	if _, err = f.pool.Exec(context.Background(), `UPDATE worker_sessions SET runtime_generation=runtime_generation+1 WHERE worker_session_id=$1`, f.started.WorkerSessionID); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadProjectionNavigation(context.Background(), navigationQuery(t, f, true))
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || got.Identity == nil || got.Identity.TraceID != initial.Identity.TraceID || got.Identity.SelectedObservationID == initial.Identity.SelectedObservationID || got.Identity.Sources[0] != initial.Identity.Sources[0] {
		t.Fatalf("later Turn lost initial root/original source: %+v initial=%+v %v", got, initial, err)
	}
}

func publishNavigationBasis(t *testing.T, f *fragmentFixture) {
	t.Helper()
	f.publishCore(t)
	p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 2 {
		if err := p.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionNavigationPublicationInProgressRemainsPendingWithoutIdentity(t *testing.T) {
	f := newFragmentFixture(t, false)
	f.publishCore(t)
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	for range 2 {
		if _, ok, err := ledger.ClaimCandidate(context.Background(), time.Minute); err != nil || !ok {
			t.Fatalf("claim candidate: %v %v", ok, err)
		}
	}
	got, err := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(context.Background(), navigationQuery(t, f, true))
	if err != nil || got.Status != artifactapp.ProjectionNavigationPending || got.Identity != nil {
		t.Fatalf("publication in progress must remain pending without identity: %+v err=%v", got, err)
	}
}

func TestProjectionNavigationCorePublicationNotYetAvailableIsPending(t *testing.T) {
	f := newFragmentFixture(t, false)
	query := artifactapp.ProjectionNavigationQuery{ActorID: "user-local", Scope: artifactdomain.ObservationScope{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo", WorkerSessionID: f.started.WorkerSessionID, WorkerRunID: f.started.WorkerRunID}, AgentTurnID: f.started.AgentTurnID}
	got, err := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(context.Background(), query)
	if err != nil || got.Status != artifactapp.ProjectionNavigationPending || got.Identity != nil {
		t.Fatalf("sealed source before core publication must be pending: %+v %v", got, err)
	}
}

func TestProjectionNavigationRejectsNewForgedProofRows(t *testing.T) {
	for _, mode := range []string{"malformed_frame", "malformed_receipt", "publication_checksum", "publication_size"} {
		t.Run(mode, func(t *testing.T) {
			// Negative only: the excluded fixture has no candidates. Insert a new
			// forged proof once; never mutate immutable sealed or published rows.
			f := newFragmentFixtureMode(t, false, true)
			f.publishCore(t)
			ctx := context.Background()
			query := navigationQuery(t, f, false)
			var input []byte
			var created time.Time
			if err := f.pool.QueryRow(ctx, `SELECT t.content,r.created_at FROM agent_turns t JOIN worker_runs r USING(worker_run_id) WHERE t.agent_turn_id=$1`, f.started.AgentTurnID).Scan(&input, &created); err != nil {
				t.Fatal(err)
			}
			basis := artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentRunBasis, Scope: query.Scope, InitialTurnID: f.started.AgentTurnID, StartedAt: created, InputJSON: string(input)}
			frame, err := artifactdomain.EncodeObservationFragment(basis)
			if err != nil {
				t.Fatal(err)
			}
			p := artifactapp.ObservationProvenance{CheckpointDigest: f.firstDigest, InputDigest: artifactdomain.Checksum(input), ReportDigest: "sha256:" + f.finalized.ArtifactRefs[0].SHA256}
			for _, ref := range f.finalized.ArtifactRefs {
				if ref.Type == "report" {
					p.ReportDigest = "sha256:" + ref.SHA256
				}
			}
			prepared := (artifactapp.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentScanner{}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: f.started.WorkerSessionID, RuntimeInstanceID: f.started.RuntimeInstanceID, RuntimeGeneration: f.started.RuntimeGeneration}}).Prepare(ctx, frame, map[string]string{"negative": "forged-proof"}, p)
			if prepared.ErrorCode != "" {
				t.Fatal(prepared.ErrorCode)
			}
			receipt := prepared.Receipt
			if mode == "malformed_frame" {
				frame = []byte(`{}`)
			}
			if mode == "malformed_receipt" {
				receipt = []byte(`{}`)
			}
			id, _ := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentRunBasis, query.Scope, nil)
			digest := artifactdomain.Checksum(frame)
			objectIdentity := workerapp.ObjectIdentity{TenantID: query.Scope.TenantID, UserID: query.Scope.UserID, WorkspaceID: query.Scope.WorkspaceID, WorkerSessionID: query.Scope.WorkerSessionID, WorkerRunID: query.Scope.WorkerRunID, AgentTurnID: f.started.AgentTurnID, Kind: artifactapp.ObservationFragmentObjectKind}
			temporary, err := f.objects.PutTemporary(ctx, objectIdentity, bytes.NewReader(frame))
			if err != nil {
				t.Fatal(err)
			}
			object, err := f.objects.Finalize(ctx, objectIdentity, temporary)
			if err != nil {
				t.Fatal(err)
			}
			provenance := fragmentJSON(t, map[string]any{"schema_version": "observation.provenance.v1", "settlement_id": f.current.SettlementID, "lease_fence": f.current.LeaseFence, "checkpoint_digest": p.CheckpointDigest, "input_digest": p.InputDigest, "report_digest": p.ReportDigest})
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `INSERT INTO observation_candidates(kind,source_id,frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status) VALUES('run_basis',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'published')`, id, digest, frame, receipt, query.Scope.TenantID, query.Scope.UserID, query.Scope.WorkspaceID, query.Scope.WorkerSessionID, query.Scope.WorkerRunID, provenance); err != nil {
				t.Fatal(err)
			}
			objectSum, size := digest, len(frame)
			if mode == "publication_checksum" {
				objectSum = artifactdomain.Checksum([]byte("different-object"))
			}
			if mode == "publication_size" {
				size++
			}
			if _, err = tx.Exec(ctx, `INSERT INTO observation_publications(kind,source_id,frame_digest,object_key,object_size,object_checksum,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id) SELECT kind,source_id,frame_digest,$4,$2,$3,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id FROM observation_candidates WHERE source_id=$1`, id, size, objectSum, object.Key); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO evidence_object_refs(object_key,trace_stream_id,tenant_id,user_id,workspace_id,object_kind,object_checksum,size_bytes,visible_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp())`, object.Key, "trace-"+f.started.AgentTurnID, query.Scope.TenantID, query.Scope.UserID, query.Scope.WorkspaceID, artifactapp.ObservationFragmentObjectKind, object.Checksum, object.Size); err != nil {
				t.Fatal(err)
			}
			r := historicalRequest(t, f, false)
			raw, requestDigest, err := artifactapp.CanonicalHistoricalRequest(r)
			if err != nil {
				t.Fatal(err)
			}
			members := fragmentJSON(t, []map[string]string{{"kind": "run_basis", "source_id": id, "frame_digest": digest}})
			if _, err = tx.Exec(ctx, `INSERT INTO observation_fragment_requests(tenant_id,actor_user_id,idempotency_key,request_bytes,request_digest,version,source_set) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.Scope.TenantID, r.ActorID, r.IdempotencyKey, raw, requestDigest, r.Version, members); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			finishNavigationJobs(t, f, fragmentVersion, false)
			got, err := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(ctx, query)
			if err != nil || got.Status != artifactapp.ProjectionNavigationBlocked || got.Identity != nil {
				t.Fatalf("forged %s proved identity: %+v %v", mode, got, err)
			}
		})
	}
}

func TestProjectionNavigationDoesNotWaitForPublicationOwnerLocks(t *testing.T) {
	f := newFragmentFixture(t, false)
	publishNavigationBasis(t, f)
	finishNavigationJobs(t, f, fragmentVersion, false)
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, table := range []string{"observation_candidates", "observation_publications", "observation_fragment_jobs", "worker_turn_settlements", "trace_manifests", "artifact_refs"} {
		rows, err := tx.Query(context.Background(), "SELECT 1 FROM "+table+" FOR UPDATE")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{}).ReadProjectionNavigation(ctx, navigationQuery(t, f, true))
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || got.Identity == nil {
		t.Fatalf("read-only navigation waited on owner locks: %+v %v", got, err)
	}
}

// Finish below represents synthetic historical passed authority. This fixture
// never calls Langfuse and does not prove remote readback or current availability.
func TestProjectionNavigationRequiresExactRootAndSelectedTurnPassed(t *testing.T) {
	f := newFragmentFixture(t, false)
	publishNavigationBasis(t, f)
	ctx := context.Background()
	reader := artifactpostgres.NewProjectionNavigationStore(f.pool, "tenant-local", "user-local", lf.ProjectionNavigationCompiler{})
	turnQuery, rootQuery := navigationQuery(t, f, true), navigationQuery(t, f, false)
	got, err := reader.ReadProjectionNavigation(ctx, turnQuery)
	if err != nil || got.Status != artifactapp.ProjectionNavigationPending || got.Identity != nil {
		t.Fatalf("without passed: %+v %v", got, err)
	}
	jobs := artifactpostgres.NewFragmentJobStore(f.pool)
	if n, err := jobs.Discover(ctx, fragmentVersion, 10); err != nil || n != 2 {
		t.Fatalf("discover=%d %v", n, err)
	}
	var selected artifactapp.FragmentJob
	for range 2 {
		job, ok, err := jobs.Claim(ctx, fragmentVersion, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if job.Source.Kind == "turn_basis" {
			selected = job
			continue
		}
		if err := jobs.Finish(ctx, job, artifactapp.ProjectionCompletion{Status: "passed"}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := reader.ReadProjectionNavigation(ctx, rootQuery)
	if err != nil || root.Status != artifactapp.ProjectionNavigationVerified || root.Identity == nil {
		t.Fatalf("root: %+v %v", root, err)
	}
	got, err = reader.ReadProjectionNavigation(ctx, turnQuery)
	if err != nil || got.Status != artifactapp.ProjectionNavigationPending || got.Identity != nil {
		t.Fatalf("root passed replaced selected Turn: %+v %v", got, err)
	}
	if err := jobs.Finish(ctx, selected, artifactapp.ProjectionCompletion{Status: "passed"}); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ReadProjectionNavigation(ctx, turnQuery)
	loaded, loadErr := (artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}).Load(ctx, selected)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	want, compileErr := (lf.ProjectionNavigationCompiler{}).CompileProjectionNavigation(ctx, loaded)
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	if err != nil || got.Status != artifactapp.ProjectionNavigationVerified || !reflect.DeepEqual(got.Identity, &want) || got.Identity.TraceID != root.Identity.TraceID {
		t.Fatalf("exact Turn: %+v want=%+v err=%v", got, want, err)
	}
}
