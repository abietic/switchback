//go:build integration

package postgres_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SYNTHETIC signed facts use the original owner APIs. No production deployment
// factory or native validation result is replaced; these tests require real PG.
func guaranteePGStore(t *testing.T) (*pgxpool.Pool, *workerpostgres.CapacityStore, capacitysnapshot.Trust, ed25519.PrivateKey, capacitysnapshot.Facts) {
	t.Helper()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	trust, key, facts := capacityFixture(t)
	g := config.M1AccountedCapacityGuarantee()
	trust.AdmissionPolicy = &app.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	if err = store.InitializeTrust(ctx, trust); err != nil {
		t.Fatal(err)
	}
	if err = store.ObservePool(ctx, signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
	}
	return pool, store, trust, key, facts
}
func TestCapacityGuaranteePGReserveConsumeReplay(t *testing.T) {
	pool, store, trust, _, _ := guaranteePGStore(t)
	ctx := context.Background()
	target := capacityTarget("guaranteed")
	first, err := reserveCapacity(t, pool, store, target, "original", "permit")
	if err != nil {
		t.Fatal(err)
	}
	if first.Guarantee == nil || *first.Guarantee != *trust.AdmissionPolicy {
		t.Fatal("new reservation lacked original owner guarantee")
	}
	first.Guarantee.ID = "caller-mutated"
	replay, err := reserveCapacity(t, pool, store, target, "original", "discarded-permit")
	if err != nil || replay.Guarantee == nil || *replay.Guarantee != *trust.AdmissionPolicy || replay.Token != first.Token {
		t.Fatal("replay changed original token/ref", err)
	}
	c := app.CapacityConsumption{Token: replay.Token, PermitID: replay.PermitID, RunID: "run", TurnID: "turn", RuntimeID: "runtime", Target: target}
	consumed, err := consumeCapacity(t, pool, store, c)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Guarantee == nil || *consumed.Guarantee != *trust.AdmissionPolicy {
		t.Fatal("consumption lost guarantee")
	}
	if err = store.ObservePool(ctx, []byte("invalid later evidence")); !errors.Is(err, app.CapacityEvidenceInvalid) {
		t.Fatal(err)
	}
	again, err := consumeCapacity(t, pool, store, c)
	if err != nil || !reflect.DeepEqual(consumed, again) || consumed.Guarantee == again.Guarantee {
		t.Fatal("no-effect response-loss replay changed identity or reused mutable pointer", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE runtime_capacity_pools SET trust_digest='sha256:corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeCapacity(t, pool, store, c); !errors.Is(err, app.CapacityEvidenceInvalid) {
		t.Fatal("bad persisted digest accepted on consumed replay", err)
	}
	assertInitialCapacityCount(t, pool, "runtime_capacity_executions", 1)
}
func TestCapacityGuaranteePGOriginalPoolImmutableAndRollback(t *testing.T) {
	pool, store, trust, _, _ := guaranteePGStore(t)
	ctx := context.Background()
	for _, name := range []string{"manifest", "policy"} {
		t.Run(name, func(t *testing.T) {
			bad := trust
			if name == "manifest" {
				bad.ManifestDigest = capacitysnapshot.Digest([]byte("other"))
			} else {
				bad.AdmissionPolicy = nil
			}
			if err := store.InitializeTrust(ctx, bad); !errors.Is(err, app.CapacityIdentityDrift) {
				t.Fatal("initialized pool policy/manifest changed", err)
			}
		})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	h, err := store.LockCapacityPool(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.ReserveAndBind(ctx, capacityTarget("rolled"), "rolled", "permit-rolled")
	if err != nil || got.Guarantee == nil {
		t.Fatal("reserve with guarantee", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertInitialCapacityCount(t, pool, "runtime_capacity_executions", 0)
	assertInitialCapacityCount(t, pool, "runtime_capacity_residents", 0)
	// A legacy initialized pool cannot be upgraded in place either.
	legacyPool, legacy, _, _ := capacityStore(t)
	if err = legacy.InitializeTrust(ctx, trust); !errors.Is(err, app.CapacityIdentityDrift) {
		t.Fatal("legacy pool upgraded", err)
	}
	legacyReservation, err := reserveCapacity(t, legacyPool, legacy, capacityTarget("legacy"), "legacy", "legacy-permit")
	if err != nil || legacyReservation.Guarantee != nil {
		t.Fatal("legacy reservation upgraded", err)
	}
}
func guaranteePGSource(t *testing.T) (*pgxpool.Pool, app.RuntimePreparationRequest, app.RuntimeProvisionRequest) {
	t.Helper()
	pool, _, _, _, _ := guaranteePGStore(t)
	ctx := context.Background()
	dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
	permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
	if err != nil {
		t.Fatal(err)
	}
	cmd := residentStartCommand(permit)
	ownership, err := (app.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}).Execute(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	r := app.RuntimePreparationRequest{Ownership: ownership, Command: cmd, SourcePlan: initialSourceBinding(t, nil)}
	profile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	p := app.RuntimeProvisionRequest{WorkerSessionID: ownership.WorkerSessionID, WorkerRunID: ownership.WorkerRunID, RuntimeInstanceID: ownership.RuntimeInstanceID, Generation: ownership.RuntimeGeneration, ResourceProfile: app.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}, NetworkProfile: app.ProfileRef{ID: cmd.NetworkPolicyID, Revision: cmd.NetworkPolicyRevision, Digest: cmd.NetworkPolicyDigest}, NetworkEnforcementStatus: cmd.NetworkEnforcementStatus, SourcePlanDigest: r.SourcePlan.Digest}
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT start_snapshot FROM worker_runs WHERE worker_run_id=$1`, ownership.WorkerRunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Capacity struct{ Reservation app.CapacityReservation } `json:"runtime_capacity"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Capacity.Reservation.Guarantee == nil {
		t.Fatal("server start snapshot did not carry ref")
	}
	return pool, r, p
}
func TestCapacityGuaranteePGSnapshotSourcePinAndPreStartDenial(t *testing.T) {
	for _, field := range []string{"id", "revision", "digest", "missing", "null"} {
		t.Run(field, func(t *testing.T) {
			pool, r, p := guaranteePGSource(t)
			ctx := context.Background()
			a := workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool)
			if err := a.AuthorizeInitialPreparation(ctx, r); err != nil {
				t.Fatal(err)
			}
			before, digest, at := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
			var sql string
			switch field {
			case "id":
				sql = `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,Guarantee,id}','"other"')`
			case "revision":
				sql = `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,Guarantee,revision}','2')`
			case "digest":
				sql = `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,Guarantee,digest}','"wrong"')`
			case "missing":
				sql = `UPDATE worker_runs SET start_snapshot=start_snapshot #- '{runtime_capacity,Reservation,Guarantee}'`
			case "null":
				sql = `UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,Guarantee}','null')`
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := a.AuthorizeInitialPreparation(ctx, r); !errors.Is(err, app.CapacityBindingMismatch) {
				t.Fatal("tampered guarantee reached preparation", err)
			}
			if err := a.AuthorizeInitialProvision(ctx, r, p); !errors.Is(err, app.CapacityBindingMismatch) {
				t.Fatal("tampered guarantee reached create/pre-Start authorization", err)
			}
			after, digest2, at2 := sourcePlanPGPin(t, pool, r.Ownership.WorkerRunID)
			if string(before) != string(after) || digest == nil || digest2 == nil || *digest != *digest2 || at == nil || at2 == nil || !at.Equal(*at2) {
				t.Fatal("rejection changed original source pin")
			}
		})
	}
}
func TestCapacityGuaranteePGSourcePinRollback(t *testing.T) {
	pool, r, _ := guaranteePGSource(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_guarantee_pin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'pin rollback'; END $$; CREATE TRIGGER reject_guarantee_pin BEFORE UPDATE OF source_plan_bytes ON runtime_capacity_executions FOR EACH ROW EXECUTE FUNCTION reject_guarantee_pin()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool).AuthorizeInitialPreparation(ctx, r); err == nil {
		t.Fatal("source pin trigger did not fail")
	}
	assertSourcePlanPGUnpinned(t, pool, r)
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_guarantee_pin ON runtime_capacity_executions; DROP FUNCTION reject_guarantee_pin()`); err != nil {
		t.Fatal(err)
	}
	if err = workerpostgres.NewInitialRuntimeExecutionAuthorizer(pool).AuthorizeInitialPreparation(ctx, r); err != nil {
		t.Fatal("rollback destroyed reusable original authority", err)
	}
}

func TestCapacityGuaranteePGConcurrentLastReserve(t *testing.T) {
	pool, store, _, _, _ := guaranteePGStore(t)
	ctx := context.Background()
	gate := make(chan struct{})
	out := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func(name string) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				out <- err
				return
			}
			defer tx.Rollback(ctx)
			<-gate
			h, err := store.LockCapacityPool(ctx, tx)
			if err == nil {
				var r app.CapacityReservation
				r, err = h.ReserveAndBind(ctx, capacityTarget(name), name, "permit-"+name)
				if err == nil && r.Guarantee == nil {
					err = app.CapacityBindingMismatch
				}
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			out <- err
		}(name)
	}
	close(gate)
	success, denied := 0, 0
	for range 2 {
		err := <-out
		if err == nil {
			success++
		} else if errors.Is(err, app.CapacityExhausted) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("last capacity: success=%d denied=%d", success, denied)
	}
	assertInitialCapacityCount(t, pool, "runtime_capacity_executions", 1)
}
func TestCapacityGuaranteePGTerminalSettlementOriginalRefWithoutFreshSample(t *testing.T) {
	for _, scenario := range []string{"valid", "tampered_snapshot", "missing_original"} {
		t.Run(scenario, func(t *testing.T) {
			pool, store, _, key, facts := guaranteePGStore(t)
			ctx := context.Background()
			dispatch, commands, ids := initialCapacityQueue(t, pool, 1)
			permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
			if err != nil {
				t.Fatal(err)
			}
			started, err := (app.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: time.Now}).Execute(ctx, residentStartCommand(permit))
			if err != nil {
				t.Fatal(err)
			}
			facts.Sequence++
			facts.ObservedAt = time.Now().UTC()
			facts.Containers = []capacitysnapshot.Container{capacityContainer(started.WorkerSessionID, started.RuntimeInstanceID, "container")}
			if err = store.ObservePool(ctx, signCapacity(t, key, facts)); err != nil {
				t.Fatal(err)
			}
			residency := workerpostgres.NewRuntimeResidencyStore(pool)
			active, err := residency.ActivateResidentRuntime(ctx, app.ActivateResidentRuntimeRecord{Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container", RuntimeGeneration: 1, ControlChannelEpoch: started.ControlChannelEpoch, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.ObservePool(ctx, []byte("later invalid evidence")); !errors.Is(err, app.CapacityEvidenceInvalid) {
				t.Fatal(err)
			}
			if scenario == "tampered_snapshot" {
				if _, err = pool.Exec(ctx, `UPDATE worker_runs SET start_snapshot=start_snapshot #- '{runtime_capacity,Reservation,Guarantee}'`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing_original" {
				if _, err = pool.Exec(ctx, `UPDATE runtime_capacity_pools SET trust_bytes=NULL,trust_digest=NULL`); err != nil {
					t.Fatal(err)
				}
			}
			record := terminalCapacityRecord(started, active)
			record.Command.TurnTarget = domain.TurnCompleted
			_, err = residency.CompleteResidentStage(ctx, record)
			if scenario != "valid" {
				if !errors.Is(err, app.CapacityBindingMismatch) {
					t.Fatal("settlement accepted mismatched guarantee", err)
				}
				assertTerminalCapacity(t, pool, 0)
			} else {
				if err != nil {
					t.Fatal("settlement required fresh sample", err)
				}
				assertTerminalCapacity(t, pool, 1)
			}
		})
	}
}
