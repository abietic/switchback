//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These fixtures use the production reservation, consumption, original owner
// and signed-sample transaction paths. No substitute DB authority is installed.
func initialSourceBinding(t *testing.T, entries []app.RuntimeSourcePlanEntry) app.RuntimeSourcePlanBinding {
	if t != nil {
		t.Helper()
	}
	b, err := app.NewRuntimeSourcePlanBinding("sha256:"+strings.Repeat("a", 64), entries)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return b
}
func sourcePlanPGFixture(t *testing.T, snapshotDigest string) (*pgxpool.Pool, app.RuntimePreparationRequest, app.RuntimeProvisionRequest) {
	t.Helper()
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Now().UTC()
	runtimecapacity.Initialize(t, pool)
	dispatch, commands, ids := initialCapacityQueueAt(t, pool, 1, now)
	permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
	if err != nil {
		t.Fatal(err)
	}
	command := residentStartCommand(permit)
	if snapshotDigest != "" {
		command.StartSnapshot = []byte(`{"working_copy":"checkpoint-1","working_copy_checkpoint_digest":"` + snapshotDigest + `"}`)
	}
	ownership, err := (app.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now.Add(4 * time.Minute) }}).Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	r := app.RuntimePreparationRequest{Ownership: ownership, Command: command, SourcePlan: initialSourceBinding(t, nil)}
	profile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	p := app.RuntimeProvisionRequest{WorkerSessionID: ownership.WorkerSessionID, WorkerRunID: ownership.WorkerRunID, RuntimeInstanceID: ownership.RuntimeInstanceID, Generation: ownership.RuntimeGeneration, ResourceProfile: app.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}, NetworkProfile: app.ProfileRef{ID: command.NetworkPolicyID, Revision: command.NetworkPolicyRevision, Digest: command.NetworkPolicyDigest}, NetworkEnforcementStatus: command.NetworkEnforcementStatus, SourcePlanDigest: r.SourcePlan.Digest}
	return pool, r, p
}
func sourcePlanPGPin(t *testing.T, pool *pgxpool.Pool, run string) ([]byte, *string, *time.Time) {
	t.Helper()
	var raw []byte
	var digest *string
	var at *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT source_plan_bytes,source_plan_digest,source_plan_bound_at FROM runtime_capacity_executions WHERE run_id=$1`, run).Scan(&raw, &digest, &at); err != nil {
		t.Fatal(err)
	}
	return raw, digest, at
}
func assertSourcePlanPGUnpinned(t *testing.T, pool *pgxpool.Pool, r app.RuntimePreparationRequest) {
	t.Helper()
	raw, digest, at := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	if raw != nil || digest != nil || at != nil {
		t.Fatalf("failed authorization pinned: %q %v %v", raw, digest, at)
	}
}
func TestRuntimeSourcePlanPGFirstPinRepeatAndProvision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, r, p := sourcePlanPGFixture(t, "")
	a := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool)
	if err := a.AuthorizeInitialPreparation(ctx, r); err != nil {
		t.Fatal(err)
	}
	raw, digest, at := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	if string(raw) != r.SourcePlan.Manifest || digest == nil || *digest != r.SourcePlan.Digest || at == nil || at.IsZero() {
		t.Fatalf("pin=%q %v %v", raw, digest, at)
	}
	var clockAdvanced bool
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()>$1`, *at).Scan(&clockAdvanced); err != nil || !clockAdvanced {
		t.Fatalf("repeat clock did not advance: %v %v", clockAdvanced, err)
	}
	if err := a.AuthorizeInitialPreparation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizeInitialProvision(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	raw2, digest2, at2 := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	if string(raw2) != string(raw) || digest2 == nil || *digest2 != *digest || at2 == nil || !at2.Equal(*at) {
		t.Fatal("same binding changed original evidence/time")
	}
	// An unrelated pool connection must not be needed inside a held transaction.
	cfg := pool.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	one := workerpostgres.NewInitialRuntimeExecutionAuthorizer(single)
	if err = one.AuthorizeInitialPreparation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = one.AuthorizeInitialProvision(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	_, _, at3 := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	if at3 == nil || !at3.Equal(*at) {
		t.Fatal("single-connection repeat rebound pin")
	}
}
func TestRuntimeSourcePlanPGRejectsWithoutPinAndMismatchedInputs(t *testing.T) {
	for _, name := range []string{"provision_without_pin", "missing_plan", "noncanonical", "snapshot_drift", "different_binding", "prepared_digest", "stale_sample", "revoked_task"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			snapshot := ""
			if name == "snapshot_drift" {
				snapshot = "sha256:" + strings.Repeat("b", 64)
			}
			pool, r, p := sourcePlanPGFixture(t, snapshot)
			a := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool)
			var err error
			switch name {
			case "provision_without_pin":
				err = a.AuthorizeInitialProvision(ctx, r, p)
			case "missing_plan":
				r.SourcePlan = app.RuntimeSourcePlanBinding{}
				err = a.AuthorizeInitialPreparation(ctx, r)
			case "noncanonical":
				r.SourcePlan.Manifest += " "
				err = a.AuthorizeInitialPreparation(ctx, r)
			case "snapshot_drift":
				err = a.AuthorizeInitialPreparation(ctx, r)
			case "different_binding", "prepared_digest":
				if err = a.AuthorizeInitialPreparation(ctx, r); err != nil {
					t.Fatal(err)
				}
				if name == "different_binding" {
					r.SourcePlan = initialSourceBinding(t, []app.RuntimeSourcePlanEntry{{Path: "empty", Kind: "directory", Mode: 0o700}})
					err = a.AuthorizeInitialPreparation(ctx, r)
				} else {
					p.SourcePlanDigest = "sha256:" + strings.Repeat("b", 64)
					err = a.AuthorizeInitialProvision(ctx, r, p)
				}
			case "stale_sample":
				runtimecapacity.ObserveStale(t, pool)
				err = a.AuthorizeInitialPreparation(ctx, r)
			case "revoked_task":
				if _, err = pool.Exec(ctx, `UPDATE tasks SET status='cancelled'`); err != nil {
					t.Fatal(err)
				}
				err = a.AuthorizeInitialPreparation(ctx, r)
			}
			if err == nil {
				t.Fatalf("accepted %s", name)
			}
			if name != "different_binding" && name != "prepared_digest" {
				assertSourcePlanPGUnpinned(t, pool, r)
			} else {
				raw, _, _ := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
				if string(raw) != initialSourceBinding(t, nil).Manifest {
					t.Fatal("mismatch changed pin")
				}
			}
		})
	}
}
func TestRuntimeSourcePlanPGCommitFailureRollsBackPin(t *testing.T) {
	pool, r, _ := sourcePlanPGFixture(t, "")
	ctx := context.Background()
	// This deferred SQL fault fires after the pin UPDATE, at the real commit.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION source_plan_commit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'source-plan deferred commit fault'; END $$;
 CREATE CONSTRAINT TRIGGER source_plan_commit_fault AFTER UPDATE ON runtime_capacity_executions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION source_plan_commit_fault()`); err != nil {
		t.Fatal(err)
	}
	if err := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool).AuthorizeInitialPreparation(ctx, r); err == nil || !strings.Contains(err.Error(), "deferred commit fault") {
		t.Fatalf("commit error=%v", err)
	}
	assertSourcePlanPGUnpinned(t, pool, r)
	var consumed bool
	if err := pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM runtime_capacity_executions WHERE run_id=$1`, r.Ownership.WorkerRunID).Scan(&consumed); err != nil || !consumed {
		t.Fatalf("original owner lost: %v consumed=%v", err, consumed)
	}
}
func TestRuntimeSourcePlanPGConcurrentDifferentDirectoriesOneWinner(t *testing.T) {
	pool, r, _ := sourcePlanPGFixture(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	other := r
	other.SourcePlan = initialSourceBinding(t, []app.RuntimeSourcePlanEntry{{Path: "empty", Kind: "directory", Mode: 0o700}})
	old1, _ := r.SourcePlan.LegacyFileDigest()
	old2, _ := other.SourcePlan.LegacyFileDigest()
	if old1 != old2 || r.SourcePlan.Digest == other.SourcePlan.Digest {
		t.Fatal("not directory-only competition")
	}
	type outcome struct {
		binding app.RuntimeSourcePlanBinding
		err     error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, request := range []app.RuntimePreparationRequest{r, other} {
		wg.Add(1)
		go func(request app.RuntimePreparationRequest) {
			defer wg.Done()
			<-start
			err := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool).AuthorizeInitialPreparation(ctx, request)
			results <- outcome{binding: request.SourcePlan, err: err}
		}(request)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	var winner app.RuntimeSourcePlanBinding
	for out := range results {
		if out.err == nil {
			wins++
			winner = out.binding
		} else if !errors.Is(out.err, app.CapacityBindingMismatch) {
			t.Fatalf("unexpected loser: %v", out.err)
		}
	}
	raw, digest, at := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	if wins != 1 || string(raw) != winner.Manifest || digest == nil || *digest != winner.Digest || at == nil {
		t.Fatalf("wins=%d pin=%q %v %v", wins, raw, digest, at)
	}
}
func TestRuntimeSourcePlanPGImmutableTriggerAndReleaseRetention(t *testing.T) {
	pool, r, _ := sourcePlanPGFixture(t, "")
	ctx := context.Background()
	a := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool)
	if err := a.AuthorizeInitialPreparation(ctx, r); err != nil {
		t.Fatal(err)
	}
	raw, digest, at := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	for _, statement := range []string{
		`UPDATE runtime_capacity_executions SET source_plan_bytes=source_plan_bytes||'x'::bytea`,
		`UPDATE runtime_capacity_executions SET source_plan_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`,
		`UPDATE runtime_capacity_executions SET source_plan_bound_at=source_plan_bound_at+interval '1 second'`,
		`UPDATE runtime_capacity_executions SET source_plan_bytes=NULL,source_plan_digest=NULL,source_plan_bound_at=NULL`,
		`DELETE FROM runtime_capacity_executions`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("changed durable pin: %s", statement)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime_capacity_executions SET source_plan_bytes=source_plan_bytes,source_plan_digest=source_plan_digest,source_plan_bound_at=source_plan_bound_at`); err != nil {
		t.Fatal(err)
	}
	var token, permit string
	var targetBytes []byte
	if err := pool.QueryRow(ctx, `SELECT token::text,permit_id,target_bytes FROM runtime_capacity_executions WHERE run_id=$1`, r.Ownership.WorkerRunID).Scan(&token, &permit, &targetBytes); err != nil {
		t.Fatal(err)
	}
	var target app.CapacityTarget
	if err := json.Unmarshal(targetBytes, &target); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='failed',terminal_at=clock_timestamp(),terminal_reason='source_prepare_failed' WHERE agent_turn_id=$1`, r.Ownership.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if err := workerpostgres.InCapacityTransaction(ctx, pool, func(_ pgx.Tx, h *workerpostgres.CapacityTx) error {
		return h.ReleaseExecution(ctx, app.CapacityConsumption{Token: token, PermitID: permit, RunID: r.Ownership.WorkerRunID, TurnID: r.Ownership.AgentTurnID, RuntimeID: r.Ownership.RuntimeInstanceID, Target: target})
	}); err != nil {
		t.Fatal(err)
	}
	raw2, digest2, at2 := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
	var released bool
	var residents int
	if err := pool.QueryRow(ctx, `SELECT released_at IS NOT NULL,(SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL) FROM runtime_capacity_executions WHERE run_id=$1`, r.Ownership.WorkerRunID).Scan(&released, &residents); err != nil {
		t.Fatal(err)
	}
	if !released || residents != 1 || string(raw2) != string(raw) || digest2 == nil || *digest2 != *digest || at2 == nil || !at2.Equal(*at) {
		t.Fatal("release erased pin or refunded resident commitment")
	}
}
func TestRuntimeSourcePlanPGConstraintRequiresCompleteConsumedPin(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	runtimecapacity.Initialize(t, pool)
	dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
	permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
	if err != nil {
		t.Fatal(err)
	}
	b := initialSourceBinding(t, nil)
	cases := []struct {
		statement string
		args      []any
	}{
		{`UPDATE runtime_capacity_executions SET source_plan_bytes=$2 WHERE permit_id=$1`, []any{permit.PermitID, []byte(b.Manifest)}},
		{`UPDATE runtime_capacity_executions SET source_plan_bytes=$2,source_plan_digest=$3,source_plan_bound_at=clock_timestamp() WHERE permit_id=$1`, []any{permit.PermitID, []byte(b.Manifest), b.Digest}},
	}
	for _, tc := range cases {
		_, err = pool.Exec(ctx, tc.statement, tc.args...)
		var constraint *pgconn.PgError
		if !errors.As(err, &constraint) || constraint.Code != "23514" {
			t.Fatalf("unconsumed/partial pin did not fail actual CHECK: %v", err)
		}
	}
}
