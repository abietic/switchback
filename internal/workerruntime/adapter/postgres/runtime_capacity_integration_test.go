//go:build integration

package postgres_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeCapacityFoundationUninitializedPool(t *testing.T) {
	pool := postgrestest.Open(t)
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM runtime_capacity_pools WHERE pool_id='local' FOR UPDATE`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	s, err := workerpostgres.NewCapacityStore(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "one", "permit"); !errors.Is(err, workerapplication.CapacityUninitialized) {
		t.Fatalf("uninitialized pool authorized=%v", err)
	}
	if status != "uninitialized" {
		t.Fatalf("new pool status=%s", status)
	}
}

func TestRuntimeCapacityFoundationSignedReservation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	trust, key, facts := capacityFixture(t)
	if err = store.InitializeTrust(ctx, trust); err != nil {
		t.Fatal(err)
	}
	if err = store.ObservePool(ctx, signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
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
	result, err := h.ReserveAndBind(ctx, capacityTarget("s1"), "initial", "permit-one")
	if err != nil {
		t.Fatalf("signed complete capacity must reserve: %v", err)
	}
	if result.Token == "" || result.CommitmentID == "" || result.PermitID != "permit-one" {
		t.Fatalf("reservation=%+v", result)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func capacityFixture(t *testing.T) (capacitysnapshot.Trust, ed25519.PrivateKey, capacitysnapshot.Facts) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := capacitysnapshot.Identity{EngineID: "engine-one", DockerRoot: "/pool/docker", FSUUID: "xfs-one", GuestBootID: "boot-one", MountNamespace: "mnt:[1]", MountID: 1, Device: "8:1"}
	trust := capacitysnapshot.Trust{KeyID: "deployment-one", PublicKey: pub, ManifestDigest: capacitysnapshot.Digest([]byte("deployment manifest")), Identity: identity, AnchorPath: "/pool/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "core", CgroupPath: "/core", CPUQuotaMicros: 100000, CPUPeriodMicros: 100000, MemoryMax: 1 << 30, PIDsMax: 512}}}
	for i, id := range []string{"core", "product", "images", "logs", "build", "working_copy", "cache"} {
		trust.Storage = append(trust.Storage, capacityStorage(id, "/pool/"+id, uint64(i+1), 1<<30, 0))
		trust.Coverage = append(trust.Coverage, capacitysnapshot.StorageCoverage{Role: id, DomainID: id})
	}
	facts := capacitysnapshot.Facts{Sequence: 1, ObservedAt: time.Now().UTC(), Identity: identity, ManifestDigest: trust.ManifestDigest, EngineCPUMilli: 5000, EngineMemoryBytes: 8 << 30, Anchor: capacitysnapshot.Anchor{Path: trust.AnchorPath, FSUUID: identity.FSUUID, MountID: 1, Device: "8:1", FSType: "xfs", MountOptions: []string{"prjquota"}, AvailableBytes: 100 << 30}, Core: trust.Core, Storage: trust.Storage, Containers: []capacitysnapshot.Container{}, Removals: []capacitysnapshot.Removal{}}
	return trust, key, facts
}
func signCapacity(t *testing.T, key ed25519.PrivateKey, f capacitysnapshot.Facts) []byte {
	t.Helper()
	payload, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(capacitysnapshot.Envelope{Version: capacitysnapshot.Version, KeyID: "deployment-one", Payload: payload, Signature: ed25519.Sign(key, append([]byte(capacitysnapshot.SignatureDomain), payload...))})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func capacityTarget(session string) workerapplication.CapacityTarget {
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	return workerapplication.CapacityTarget{Kind: "task_initial", TenantID: "tenant", ActorID: "actor", SessionID: session, TaskID: "task-" + session, Generation: 1, SessionRevision: 1, TaskRevision: 1, QueueSequence: 1, QueueRevision: 1, Profile: workerapplication.CapacityProfile{ID: p.ID, Revision: p.Revision, Digest: p.Digest}}
}

func capacityStore(t *testing.T) (*pgxpool.Pool, *workerpostgres.CapacityStore, ed25519.PrivateKey, capacitysnapshot.Facts) {
	t.Helper()
	pool := postgrestest.Open(t)
	s, err := workerpostgres.NewCapacityStore(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	tr, key, f := capacityFixture(t)
	if err = s.InitializeTrust(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if err = s.ObservePool(context.Background(), signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	return pool, s, key, f
}
func reserveCapacity(t *testing.T, pool *pgxpool.Pool, s *workerpostgres.CapacityStore, target workerapplication.CapacityTarget, key, permit string) (workerapplication.CapacityReservation, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		return workerapplication.CapacityReservation{}, err
	}
	r, err := h.ReserveAndBind(ctx, target, key, permit)
	if err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}
func consumeCapacity(t *testing.T, pool *pgxpool.Pool, s *workerpostgres.CapacityStore, c workerapplication.CapacityConsumption) (workerapplication.CapacityReservation, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		return workerapplication.CapacityReservation{}, err
	}
	r, err := h.Consume(ctx, c)
	if err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}
func TestRuntimeCapacityFoundationConcurrentLastResident(t *testing.T) {
	pool, s, _, _ := capacityStore(t)
	ctx := context.Background()
	gate := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			ready <- struct{}{}
			<-gate
			h, err := s.LockCapacityPool(ctx, tx)
			if err == nil {
				_, err = h.ReserveAndBind(ctx, capacityTarget(id), id, "permit-"+id)
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}(id)
	}
	<-ready
	<-ready
	close(gate)
	success, denied := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, workerapplication.CapacityExhausted) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("success=%d denied=%d", success, denied)
	}
	d, err := s.ReadCapacity(ctx)
	if err != nil || d.ResidentCount != 1 || d.ActiveExecutions != 1 {
		t.Fatalf("diagnostics=%+v %v", d, err)
	}
}
func TestRuntimeCapacityFoundationReplayBindingsAndExpiry(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	target := capacityTarget("one")
	first, err := reserveCapacity(t, pool, s, target, "key", "permit")
	if err != nil {
		t.Fatal(err)
	}
	f.Sequence++
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	replay, err := reserveCapacity(t, pool, s, target, "key", "new-candidate-permit")
	if err != nil || replay != first {
		t.Fatalf("replay changed=%+v %v", replay, err)
	}
	for _, name := range []string{"task", "session", "generation", "actor", "task_revision", "queue", "profile"} {
		t.Run(name, func(t *testing.T) {
			bad := target
			switch name {
			case "task":
				bad.TaskID = "other"
			case "session":
				bad.SessionID = "other"
			case "generation":
				bad.Generation++
			case "actor":
				bad.ActorID = "other"
			case "task_revision":
				bad.TaskRevision++
			case "queue":
				bad.QueueSequence++
			case "profile":
				bad.Profile.Digest = "sha256:bad"
			}
			if _, e := reserveCapacity(t, pool, s, bad, "key", "permit-new"); e == nil {
				t.Fatal("target mutation accepted")
			}
		})
	}
	c := workerapplication.CapacityConsumption{Token: first.Token, PermitID: first.PermitID, RunID: "run", TurnID: "turn", RuntimeID: "runtime", Target: target}
	wrong := c
	wrong.PermitID = "other"
	if _, err = consumeCapacity(t, pool, s, wrong); err == nil {
		t.Fatal("foreign permit accepted")
	}
	result, err := consumeCapacity(t, pool, s, c)
	if err != nil {
		t.Fatal(err)
	}
	result2, err := consumeCapacity(t, pool, s, c)
	if err != nil || result2 != result {
		t.Fatalf("consume replay=%+v %v", result2, err)
	}
	for _, name := range []string{"run", "turn", "runtime", "token", "task", "session", "generation", "profile"} {
		t.Run("consume_"+name, func(t *testing.T) {
			bad := c
			switch name {
			case "run":
				bad.RunID = "different"
			case "turn":
				bad.TurnID = "different"
			case "runtime":
				bad.RuntimeID = "different"
			case "token":
				bad.Token = "not-a-token"
			case "task":
				bad.Target.TaskID = "different"
			case "session":
				bad.Target.SessionID = "different"
			case "generation":
				bad.Target.Generation++
			case "profile":
				bad.Target.Profile.Revision++
			}
			if _, e := consumeCapacity(t, pool, s, bad); e == nil {
				t.Fatal("foreign consumption accepted")
			}
		})
	}
	// Freshness is required for first consume, but committed response-loss replay
	// returns the original result, even after later evidence becomes invalid.
	if err = s.ObservePool(ctx, []byte("bad")); err == nil {
		t.Fatal("invalid sample accepted")
	}
	if again, e := consumeCapacity(t, pool, s, c); e != nil || again != result {
		t.Fatalf("durable replay=%+v %v", again, e)
	}
}
func TestRuntimeCapacityFoundationExpiredRequestAndRollback(t *testing.T) {
	pool, s, _, _ := capacityStore(t)
	ctx := context.Background()
	target := capacityTarget("one")
	tx, _ := pool.Begin(ctx)
	h, _ := s.LockCapacityPool(ctx, tx)
	if _, err := h.ReserveAndBind(ctx, target, "rolled", "rolled-permit"); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	d, _ := s.ReadCapacity(ctx)
	if d.ResidentCount != 0 || d.ActiveExecutions != 0 {
		t.Fatalf("rollback leaked %+v", d)
	}
	first, err := reserveCapacity(t, pool, s, target, "key", "permit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = reserveCapacity(t, pool, s, target, "key", "new-permit"); !errors.Is(err, workerapplication.CapacityRequestExpired) {
		t.Fatalf("expired replay=%v", err)
	}
	c := workerapplication.CapacityConsumption{Token: first.Token, PermitID: first.PermitID, RunID: "r", TurnID: "t", RuntimeID: "i", Target: target}
	if _, err = consumeCapacity(t, pool, s, c); !errors.Is(err, workerapplication.CapacityRequestExpired) {
		t.Fatalf("expired consume=%v", err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("two"), "two", "two-permit"); !errors.Is(err, workerapplication.CapacityExhausted) {
		t.Fatalf("TTL returned resident=%v", err)
	}
	d, _ = s.ReadCapacity(ctx)
	if d.ResidentCount != 1 {
		t.Fatalf("resident lost %+v", d)
	}
}
func TestRuntimeCapacityFoundationUnforgeableHandle(t *testing.T) {
	pool, s, _, _ := capacityStore(t)
	ctx := context.Background()
	var nilHandle *workerpostgres.CapacityTx
	for _, h := range []*workerpostgres.CapacityTx{nilHandle, {}} {
		if _, err := h.ReserveAndBind(ctx, capacityTarget("one"), "key", "permit"); err == nil {
			t.Fatal("forged handle")
		}
	}
	if _, err := s.LockCapacityPool(ctx, nil); err == nil {
		t.Fatal("nil transaction")
	}
	foreign := postgrestest.Open(t)
	tx, _ := foreign.Begin(ctx)
	defer tx.Rollback(ctx)
	if _, err := s.LockCapacityPool(ctx, tx); err == nil {
		t.Fatal("foreign pool transaction")
	}
	real, _ := pool.Begin(ctx)
	h, _ := s.LockCapacityPool(ctx, real)
	real.Rollback(ctx)
	if _, err := h.ReserveAndBind(ctx, capacityTarget("one"), "key", "permit"); err == nil {
		t.Fatal("closed transaction handle")
	}
}
func TestRuntimeCapacityFoundationEvidenceSequencing(t *testing.T) {
	cases := []string{"signature", "same_sequence_collision", "sequence_regression", "expired", "future", "engine", "root", "uuid", "boot", "mountns", "core", "storage", "anchor", "inventory"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			pool, s, key, f := capacityStore(t)
			ctx := context.Background()
			good := signCapacity(t, key, f)
			f.Sequence = 2
			f.ObservedAt = time.Now().UTC()
			var bad []byte
			switch name {
			case "signature":
				bad = []byte("bad")
			case "same_sequence_collision":
				f.Sequence = 1
				f.EngineCPUMilli++
			case "sequence_regression":
				f.Sequence = 3
				if e := s.ObservePool(ctx, signCapacity(t, key, f)); e != nil {
					t.Fatal(e)
				}
				f.Sequence = 2
			case "expired":
				f.ObservedAt = time.Now().Add(-31 * time.Second)
			case "future":
				f.ObservedAt = time.Now().Add(3 * time.Second)
			case "engine":
				f.Identity.EngineID = "other"
			case "root":
				f.Identity.DockerRoot = "/other"
			case "uuid":
				f.Identity.FSUUID = "other"
			case "boot":
				f.Identity.GuestBootID = "other"
			case "mountns":
				f.Identity.MountNamespace = "other"
			case "core":
				f.Core = []capacitysnapshot.CoreDomain{}
			case "storage":
				f.Storage = f.Storage[:6]
			case "anchor":
				f.Anchor.ProjectID = 1
			case "inventory":
				f.Containers = []capacitysnapshot.Container{capacityContainer("unknown", "runtime", "container")}
			}
			if bad == nil {
				bad = signCapacity(t, key, f)
			}
			if err := s.ObservePool(ctx, bad); err == nil && name != "inventory" {
				t.Fatal("bad evidence accepted")
			}
			if _, err := reserveCapacity(t, pool, s, capacityTarget("one"), "key", "permit"); err == nil {
				t.Fatal("old good evidence authorized after fault")
			}
			// Replaying a prior good sample must not erase the fault or update receive age.
			_ = s.ObservePool(ctx, good)
			if _, err := reserveCapacity(t, pool, s, capacityTarget("one"), "key", "permit"); err == nil {
				t.Fatal("old replay cleared block")
			}
		})
	}
}
func capacityContainer(session, runtime, container string) capacitysnapshot.Container {
	p := capacityTarget(session).Profile
	return capacitysnapshot.Container{ContainerID: container, RuntimeID: runtime, CgroupPath: "/workers/" + session, CPUQuotaMicros: 200000, CPUPeriodMicros: 100000, SessionID: session, Generation: 1, ProfileID: p.ID, ProfileRevision: p.Revision, ProfileDigest: p.Digest, NanoCPUs: 2_000_000_000, MemoryMax: 4 << 30, PIDsMax: 512, Layer: capacityStorage("layer-"+session, "/pool/layers/"+session, 99, 10<<30, 8<<30)}
}
func TestRuntimeCapacityFoundationStorageResidualAndResidentDedup(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	f.Sequence = 2
	f.EngineCPUMilli = 10000
	f.EngineMemoryBytes = 16 << 30
	f.Anchor.AvailableBytes = 30 << 30
	if err := s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	target := capacityTarget("one")
	r, err := reserveCapacity(t, pool, s, target, "one", "permit-one")
	if err != nil {
		t.Fatal(err)
	}
	c := workerapplication.CapacityConsumption{Token: r.Token, PermitID: r.PermitID, RunID: "run", TurnID: "turn", RuntimeID: "runtime", Target: target}
	if _, err = consumeCapacity(t, pool, s, c); err != nil {
		t.Fatal(err)
	}
	// Pending first layer consumes 10 GiB: 7+10+10 exceeds 24 GiB budget.
	if _, err = reserveCapacity(t, pool, s, capacityTarget("two"), "two", "permit-two"); !errors.Is(err, workerapplication.CapacityExhausted) {
		t.Fatalf("unseen pending budget=%v", err)
	}
	f.Sequence++
	f.Containers = []capacitysnapshot.Container{capacityContainer("one", "runtime", "container")}
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	// The same statfs sample already accounts for 8 GiB used: 7+2+10 fits.
	if _, err = reserveCapacity(t, pool, s, capacityTarget("two"), "two", "permit-two"); err != nil {
		t.Fatalf("same-sample used deducted twice: %v", err)
	}
	d, _ := s.ReadCapacity(ctx)
	if d.ResidentCount != 2 || d.ActiveExecutions != 2 {
		t.Fatalf("execution double-counted resident %+v", d)
	}
}
func TestRuntimeCapacityFoundationLegacyResidentAndTrustPin(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	// Explicit legacy business row: deliberately no capacity ledger authority.
	now := time.Now().Add(-10 * time.Minute)
	seedAllocationRequests(t, pool, 1, now)
	allocated, err := (workerapplication.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(pool), IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return now }}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO runtime_instances(runtime_instance_id,worker_session_id,generation,runtime_type,status,image_ref,resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at) VALUES('legacy-runtime',$1,1,'docker_container','starting','legacy-image','local_standard_v1','m1_public_web_restricted_v1',1,1,$2)`, allocated.WorkerSessionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveCapacity(t, pool, s, capacityTarget("new"), "new", "permit-new"); !errors.Is(err, workerapplication.CapacityExhausted) {
		t.Fatalf("unseen legacy not charged=%v", err)
	}
	if _, err := reserveCapacity(t, pool, s, capacityTarget(allocated.WorkerSessionID), "legacy", "legacy-permit"); err == nil {
		t.Fatal("legacy minted token")
	}
	tr, _, _ := capacityFixture(t)
	if err := s.InitializeTrust(ctx, tr); !errors.Is(err, workerapplication.CapacityIdentityDrift) {
		t.Fatalf("trust rekey=%v", err)
	}
	f.Sequence++
	f.Identity.EngineID = "changed"
	if err := s.ObservePool(ctx, signCapacity(t, key, f)); !errors.Is(err, workerapplication.CapacityIdentityDrift) {
		t.Fatalf("identity drift=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_capacity_executions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy execution count=%d %v", count, err)
	}
}

func TestRuntimeCapacityFoundationTerminalKeepsResidentAndRemovalNeedsSecretFence(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	// The real initial Task transaction now owns reserve/bind/consume. Recover
	// its exact original target for the foundation's release/removal assertions.
	now := time.Now().Add(-10 * time.Minute)
	dispatch, commands, ids := initialCapacityQueueAt(t, pool, 1, now)
	permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
	if err != nil {
		t.Fatal(err)
	}
	started, err := (workerapplication.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now.Add(4 * time.Minute) }}).Execute(ctx, residentStartCommand(permit))
	if err != nil {
		t.Fatal(err)
	}
	var rawTarget []byte
	var target workerapplication.CapacityTarget
	var reserved workerapplication.CapacityReservation
	if err = pool.QueryRow(ctx, `SELECT token::text,commitment_id::text,permit_id,target_bytes FROM runtime_capacity_executions WHERE permit_id=$1`, permit.PermitID).Scan(&reserved.Token, &reserved.CommitmentID, &reserved.PermitID, &rawTarget); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(rawTarget, &target); err != nil {
		t.Fatal(err)
	}
	consumption := workerapplication.CapacityConsumption{Token: reserved.Token, PermitID: reserved.PermitID, RunID: started.WorkerRunID, TurnID: started.AgentTurnID, RuntimeID: started.RuntimeInstanceID, Target: target}
	residency := workerapplication.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now.Add(5 * time.Minute) }}
	activate := workerapplication.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-real", RuntimeGeneration: 1, ControlChannelEpoch: 1, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}
	if _, err = residency.Activate(ctx, activate); !errors.Is(err, workerapplication.CapacityContainerProofPending) {
		t.Fatalf("activation without signed container proof: %v", err)
	}
	// Activation requires the signed exact-CID quota sample to be committed first.
	f.Sequence++
	f.Containers = []capacitysnapshot.Container{capacityContainer(target.SessionID, started.RuntimeInstanceID, "container-real")}
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	active, err := residency.Activate(ctx, activate)
	if err != nil {
		t.Fatal(err)
	}
	journal := workerapplication.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now.Add(6 * time.Minute) }}
	if _, err = journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	release := func(c workerapplication.CapacityConsumption) error {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		h, e := s.LockCapacityPool(ctx, tx)
		if e != nil {
			return e
		}
		if e = h.ReleaseExecution(ctx, c); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if err = release(consumption); err == nil {
		t.Fatal("nonterminal released")
	}
	if _, err = residency.CompleteStage(ctx, workerapplication.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionInteractive, RunTarget: domain.RunCompleted, TurnTarget: domain.TurnCompleted}); err != nil {
		t.Fatal(err)
	}
	f.Sequence++
	f.ObservedAt = time.Now().Add(-31 * time.Second)
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); !errors.Is(err, workerapplication.CapacityEvidenceStale) {
		t.Fatal(err)
	}
	if err = release(consumption); err != nil {
		t.Fatalf("stale capacity blocked terminal: %v", err)
	}
	if err = release(consumption); err != nil {
		t.Fatal(err)
	}
	wrong := consumption
	wrong.Target.Generation++
	if err = release(wrong); err == nil {
		t.Fatal("late generation release")
	}
	d, err := s.ReadCapacity(ctx)
	if err != nil || d.ActiveExecutions != 0 || d.ResidentCount != 1 {
		t.Fatalf("terminal diagnostics=%+v %v", d, err)
	}
	// Bind raw deletion observations to a real persistent control fence. Existing
	// cleanup booleans are deliberately set true, and must remain insufficient.
	if _, err = journal.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE runtime_instances SET status='terminated',ended_at=$2,cleanup_confirmed=true,cleanup_diagnostic='{"ContainerRemoved":true,"SecretLeaseRevoked":true,"ControlRevoked":true}' WHERE runtime_instance_id=$1`, started.RuntimeInstanceID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.Sequence++
	f.ObservedAt = time.Now().UTC()
	f.Containers = []capacitysnapshot.Container{}
	removal := capacitysnapshot.Removal{EngineID: f.Identity.EngineID, ContainerID: "container-real", RuntimeID: started.RuntimeInstanceID, SessionID: target.SessionID, Generation: 1, ChannelEpoch: 1, SecretVolumeID: "switchback-runtime-secrets-" + started.RuntimeInstanceID + "-g1", ContainerInspectHTTPStatus: 404, SecretVolumeInspectHTTPStatus: 404}
	f.Removals = []capacitysnapshot.Removal{removal}
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	confirm := func(seq int64) error {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		h, e := s.LockCapacityPool(ctx, tx)
		if e != nil {
			return e
		}
		e = h.ConfirmRemoval(ctx, reserved.CommitmentID, seq)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if err = confirm(f.Sequence); !errors.Is(err, workerapplication.CapacitySecretFenceUnproven) {
		t.Fatalf("control+404 should still need secret authority: %v", err)
	}
	if err = confirm(f.Sequence); !errors.Is(err, workerapplication.CapacitySecretFenceUnproven) {
		t.Fatalf("removal replay=%v", err)
	}
	f.Sequence++
	f.Removals[0].Generation++
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	if err = confirm(f.Sequence); !errors.Is(err, workerapplication.CapacityBindingMismatch) {
		t.Fatalf("late removal=%v", err)
	}
	// Runtime absence never releases separately retained storage commitments.
	var domains int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runtime_capacity_storage_domains`).Scan(&domains); err != nil || domains != 7 {
		t.Fatalf("persistent domains=%d %v", domains, err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("another"), "another", "another-permit"); !errors.Is(err, workerapplication.CapacityExhausted) {
		t.Fatalf("terminal/removal returned resident budget: %v", err)
	}
	// A subsequent execution in the same resident reuses its fixed upper bound.
	target.TaskRevision++
	target.QueueSequence++
	if r, e := reserveCapacity(t, pool, s, target, "next", "next-permit"); e != nil || r.CommitmentID != reserved.CommitmentID {
		t.Fatalf("resident reuse=%+v %v", r, e)
	}
}
func TestRuntimeCapacityFoundationDBClockAndSampleReplay(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	raw := signCapacity(t, key, f)
	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT received_at FROM runtime_capacity_snapshots WHERE sequence=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.ObservePool(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT received_at FROM runtime_capacity_snapshots WHERE sequence=1`).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatalf("replay refreshed age %s %s %v", before, after, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	target := capacityTarget("one")
	r, err := h.ReserveAndBind(ctx, target, "one", "permit-one")
	if err != nil {
		t.Fatal(err)
	}
	var ttl float64
	if err = tx.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-clock_timestamp()) FROM runtime_capacity_executions WHERE token=$1`, r.Token).Scan(&ttl); err != nil || ttl < 295 || ttl > 300 {
		t.Fatalf("DB TTL=%f %v", ttl, err)
	}
	// Mutation is only of an ordinary test reservation expiry, never verified
	// facts. The already-held handle must use current DB facts on each operation.
	if _, err = tx.Exec(ctx, `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, r.Token); err != nil {
		t.Fatal(err)
	}
	c := workerapplication.CapacityConsumption{Token: r.Token, PermitID: r.PermitID, RunID: "r", TurnID: "t", RuntimeID: "i", Target: target}
	if _, err = h.Consume(ctx, c); !errors.Is(err, workerapplication.CapacityRequestExpired) {
		t.Fatalf("held handle cached clock/expiry=%v", err)
	}
	tx.Rollback(ctx)
	f.Sequence++
	f.ObservedAt = time.Now().Add(-31 * time.Second)
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); !errors.Is(err, workerapplication.CapacityEvidenceStale) {
		t.Fatal(err)
	}
	if _, err = reserveCapacity(t, pool, s, target, "one", "permit-one"); !errors.Is(err, workerapplication.CapacityEvidenceStale) {
		t.Fatalf("business command bypassed DB age=%v", err)
	}
}
func TestRuntimeCapacityFoundationReadFailureBlocksOldSample(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := capacitysnapshot.NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileCapacity(ctx, reader); !errors.Is(err, workerapplication.CapacityEvidenceInvalid) {
		t.Fatalf("file failure=%v", err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "key", "permit"); err == nil {
		t.Fatal("missing file reused old sample")
	}
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err == nil {
		t.Fatal("same seq erased read failure")
	}
	f.Sequence++
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "key", "permit"); err != nil {
		t.Fatalf("same-identity next sequence cannot recover: %v", err)
	}
}

func TestRuntimeCapacityFoundationDurableContainerCollisionBlocks(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	f.Sequence++
	f.EngineCPUMilli = 32000
	f.EngineMemoryBytes = 64 << 30
	f.Anchor.AvailableBytes = 200 << 30
	if err := s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	create := func(id string, consume bool) (workerapplication.CapacityReservation, workerapplication.CapacityConsumption) {
		r, e := reserveCapacity(t, pool, s, capacityTarget(id), id, "permit-"+id)
		if e != nil {
			t.Fatal(e)
		}
		c := workerapplication.CapacityConsumption{Token: r.Token, PermitID: r.PermitID, RunID: "run-" + id, TurnID: "turn-" + id, RuntimeID: "runtime-" + id, Target: capacityTarget(id)}
		if consume {
			if _, e = consumeCapacity(t, pool, s, c); e != nil {
				t.Fatal(e)
			}
		}
		return r, c
	}
	first, _ := create("one", true)
	second, _ := create("two", true)
	pending, pendingCommand := create("pending", false)
	f.Sequence++
	f.Containers = []capacitysnapshot.Container{capacityContainer("one", "runtime-one", "container-X")}
	good := signCapacity(t, key, f)
	if err := s.ObservePool(ctx, good); err != nil {
		t.Fatal(err)
	}
	var received time.Time
	if err := pool.QueryRow(ctx, `SELECT received_at FROM runtime_capacity_snapshots WHERE sequence=$1`, f.Sequence).Scan(&received); err != nil {
		t.Fatal(err)
	}
	f.Sequence++
	conflict := capacityContainer("two", "runtime-two", "container-X")
	conflict.Layer.ProjectID = 100
	conflict.Layer.Quota.ProjectID = 100
	f.Containers = []capacitysnapshot.Container{conflict}
	err := s.ObservePool(ctx, signCapacity(t, key, f))
	var wait workerapplication.CapacityWaitReason
	if !errors.As(err, &wait) {
		t.Errorf("identity collision must be a typed block, got %v", err)
	}
	d, e := s.ReadCapacity(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if d.Status == "ready" {
		t.Error("collision rolled back block, old sample still ready")
	}
	_ = s.ObservePool(ctx, good)
	var replayReceived time.Time
	if e = pool.QueryRow(ctx, `SELECT received_at FROM runtime_capacity_snapshots WHERE sequence=$1`, f.Sequence-1).Scan(&replayReceived); e != nil || !received.Equal(replayReceived) {
		t.Fatalf("old replay changed receive time: %v", e)
	}
	if _, e = reserveCapacity(t, pool, s, capacityTarget("new"), "new", "new-permit"); e == nil {
		t.Error("new reserve reused old good sample after collision")
	}
	if _, e = consumeCapacity(t, pool, s, pendingCommand); e == nil {
		t.Error("first consume reused old good sample after collision")
	}
	var a, b string
	if e = pool.QueryRow(ctx, `SELECT COALESCE(container_id,'') FROM runtime_capacity_residents WHERE commitment_id=$1`, first.CommitmentID).Scan(&a); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `SELECT COALESCE(container_id,'') FROM runtime_capacity_residents WHERE commitment_id=$1`, second.CommitmentID).Scan(&b); e != nil {
		t.Fatal(e)
	}
	if a != "container-X" || b != "" {
		t.Fatalf("identities mutated: %q %q", a, b)
	}
	var token string
	if e = pool.QueryRow(ctx, `SELECT token::text FROM runtime_capacity_executions WHERE commitment_id=$1`, pending.CommitmentID).Scan(&token); e != nil || token != pending.Token {
		t.Fatalf("pending token changed: %s %v", token, e)
	}
	// Only a newer consistent observation can recover admission.
	f.Sequence++
	f.Containers = []capacitysnapshot.Container{capacityContainer("one", "runtime-one", "container-X")}
	if e = s.ObservePool(ctx, signCapacity(t, key, f)); e != nil {
		t.Fatal(e)
	}
	if _, e = consumeCapacity(t, pool, s, pendingCommand); e != nil {
		t.Fatalf("consistent newer sample cannot recover: %v", e)
	}
	for _, mutation := range []string{"runtime", "session", "generation"} {
		t.Run(mutation, func(t *testing.T) {
			f.Sequence++
			c := capacityContainer("one", "runtime-one", "container-X")
			switch mutation {
			case "runtime":
				c.RuntimeID = "runtime-two"
			case "session":
				c.SessionID = "two"
			case "generation":
				c.Generation++
			}
			f.Containers = []capacitysnapshot.Container{c}
			if e = s.ObservePool(ctx, signCapacity(t, key, f)); !errors.As(e, &wait) {
				t.Fatalf("mapping conflict not typed: %v", e)
			}
			d, e = s.ReadCapacity(ctx)
			if e != nil || d.Status == "ready" {
				t.Fatalf("mapping conflict diagnostics=%+v %v", d, e)
			}
		})
	}
}

func capacitySignedPayload(t *testing.T, key ed25519.PrivateKey, payload []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(capacitysnapshot.Envelope{Version: capacitysnapshot.Version, KeyID: "deployment-one", Payload: payload, Signature: ed25519.Sign(key, append([]byte(capacitysnapshot.SignatureDomain), payload...))})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestRuntimeCapacityFoundationMissingStorageEnforcementBlocks(t *testing.T) {
	pool, s, key, f := capacityStore(t)
	ctx := context.Background()
	good := signCapacity(t, key, f)
	f.Sequence++
	encoded, _ := json.Marshal(f)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	var domains []map[string]json.RawMessage
	if err := json.Unmarshal(payload["storage"], &domains); err != nil {
		t.Fatal(err)
	}
	delete(domains[0], "quota")
	payload["storage"], _ = json.Marshal(domains)
	bad, _ := json.Marshal(payload)
	if err := s.ObservePool(ctx, capacitySignedPayload(t, key, bad)); err == nil {
		t.Error("signed storage without raw enforcement became ready")
	}
	d, err := s.ReadCapacity(ctx)
	if err != nil || d.Status == "ready" {
		t.Errorf("missing enforcement diagnostics=%+v %v", d, err)
	}
	_ = s.ObservePool(ctx, good)
	if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "one", "permit"); err == nil {
		t.Fatal("missing enforcement reused older good sample")
	}
}

func capacityStorage(id, path string, project, hard, used uint64) capacitysnapshot.StorageDomain {
	return capacitysnapshot.StorageDomain{ID: id, Path: path, ProjectID: project, HardBytes: hard, UsedBytes: used, FSUUID: "xfs-one", Device: "8:1", MountID: 1, FSType: "xfs", XFlags: 0x200, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: project, HardBlocks512: hard / 512, UsedBlocks512: used / 512}}
}

func groupedCapacityFixture(t *testing.T) (capacitysnapshot.Trust, ed25519.PrivateKey, capacitysnapshot.Facts) {
	tr, key, f := capacityFixture(t)
	tr.Storage = []capacitysnapshot.StorageDomain{capacityStorage("core", "/pool/core", 1, 1<<30, 0), capacityStorage("docker_shared", "/pool/docker_shared", 999, 3<<30, 0), capacityStorage("persistent", "/pool/persistent", 1000, 3<<30, 0)}
	tr.Coverage = []capacitysnapshot.StorageCoverage{{Role: "core", DomainID: "core"}, {Role: "images", DomainID: "docker_shared"}, {Role: "logs", DomainID: "docker_shared"}, {Role: "build", DomainID: "docker_shared"}, {Role: "working_copy", DomainID: "persistent"}, {Role: "cache", DomainID: "persistent"}, {Role: "product", DomainID: "persistent"}}
	f.Storage = tr.Storage
	f.Anchor.AvailableBytes = 22 << 30
	return tr, key, f
}
func TestRuntimeCapacityFoundationGroupedStorageCoverage(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	s, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	tr, key, f := groupedCapacityFixture(t)
	if err = s.InitializeTrust(ctx, tr); err != nil {
		t.Fatalf("grouped roles with complete physical coverage rejected: %v", err)
	}
	if err = s.ObservePool(ctx, signCapacity(t, key, f)); err != nil {
		t.Fatal(err)
	}
	if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "one", "permit"); err != nil {
		t.Fatalf("physical domain charged more than once: %v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runtime_capacity_storage_domains`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("physical domain count=%d %v", count, err)
	}
	for _, name := range []string{"missing_role", "duplicate_role", "unknown_role", "unknown_domain", "physical_overlap", "project_overlap"} {
		t.Run(name, func(t *testing.T) {
			tr, _, _ := groupedCapacityFixture(t)
			switch name {
			case "missing_role":
				tr.Coverage = tr.Coverage[:6]
			case "duplicate_role":
				tr.Coverage = append(tr.Coverage, tr.Coverage[0])
			case "unknown_role":
				tr.Coverage[0].Role = "invented"
			case "unknown_domain":
				tr.Coverage[0].DomainID = "absent"
			case "physical_overlap":
				tr.Storage[1].Path = tr.Storage[0].Path + "/child"
			case "project_overlap":
				tr.Storage[1].ProjectID = tr.Storage[0].ProjectID
				tr.Storage[1].Quota.ProjectID = tr.Storage[0].ProjectID
			}
			if e := capacitysnapshot.ValidateTrust(tr); e == nil {
				t.Fatal("invalid grouped mapping accepted")
			}
		})
	}
}

func TestRuntimeCapacityFoundationRawStorageEvidence(t *testing.T) {
	cases := []string{"filesystem_uuid", "device", "mount", "filesystem_type", "inheritance", "accounting_only", "enforcement_only", "zero_enforcement", "quota_format", "quota_version", "state_version", "quota_project", "hard_readback", "used_readback", "block_overflow", "project_overflow", "realtime"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			pool, s, key, f := capacityStore(t)
			ctx := context.Background()
			good := signCapacity(t, key, f)
			f.Sequence++
			d := &f.Storage[0]
			switch name {
			case "filesystem_uuid":
				d.FSUUID = "other-filesystem"
			case "device":
				d.Device = "8:2"
			case "mount":
				d.MountID++
			case "filesystem_type":
				d.FSType = "ext4"
			case "inheritance":
				d.XFlags = 0
			case "accounting_only":
				d.Quota.StateFlags = 0x10
			case "enforcement_only":
				d.Quota.StateFlags = 0x20
			case "zero_enforcement":
				d.Quota.StateFlags = 0
			case "quota_format":
				d.Quota.Format = "configured-only"
			case "quota_version":
				d.Quota.Version = 2
			case "state_version":
				d.Quota.StateVersion = 2
			case "quota_project":
				d.Quota.ProjectID++
			case "hard_readback":
				d.Quota.HardBlocks512++
			case "used_readback":
				d.Quota.UsedBlocks512++
			case "block_overflow":
				d.Quota.HardBlocks512 = uint64(math.MaxInt64)/512 + 1
			case "project_overflow":
				d.ProjectID = uint64(math.MaxUint32) + 1
				d.Quota.ProjectID = d.ProjectID
			case "realtime":
				d.XFlags |= 1
			}
			var wait workerapplication.CapacityWaitReason
			if err := s.ObservePool(ctx, signCapacity(t, key, f)); !errors.As(err, &wait) {
				t.Fatalf("raw storage failure not typed: %v", err)
			}
			diagnostic, err := s.ReadCapacity(ctx)
			if err != nil || diagnostic.Status == "ready" {
				t.Fatalf("diagnostics=%+v %v", diagnostic, err)
			}
			_ = s.ObservePool(ctx, good)
			if _, err = reserveCapacity(t, pool, s, capacityTarget("one"), "one", "permit"); err == nil {
				t.Fatal("raw storage fault reused prior sample")
			}
		})
	}
}
func TestRuntimeCapacityFoundationDynamicLayerEnforcement(t *testing.T) {
	for _, name := range []string{"valid", "filesystem_uuid", "mount", "inheritance", "accounting_only", "quota_project", "hard_readback"} {
		t.Run(name, func(t *testing.T) {
			pool, s, key, f := capacityStore(t)
			ctx := context.Background()
			target := capacityTarget("one")
			r, err := reserveCapacity(t, pool, s, target, "one", "permit")
			if err != nil {
				t.Fatal(err)
			}
			c := workerapplication.CapacityConsumption{Token: r.Token, PermitID: r.PermitID, RunID: "run-one", TurnID: "turn-one", RuntimeID: "runtime-one", Target: target}
			if _, err = consumeCapacity(t, pool, s, c); err != nil {
				t.Fatal(err)
			}
			observed := capacityContainer("one", "runtime-one", "container-one")
			d := &observed.Layer
			switch name {
			case "filesystem_uuid":
				d.FSUUID = "other"
			case "mount":
				d.MountID++
			case "inheritance":
				d.XFlags = 0
			case "accounting_only":
				d.Quota.StateFlags = 0x10
			case "quota_project":
				d.Quota.ProjectID++
			case "hard_readback":
				d.Quota.HardBlocks512++
			}
			f.Sequence++
			f.Containers = []capacitysnapshot.Container{observed}
			err = s.ObservePool(ctx, signCapacity(t, key, f))
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var wait workerapplication.CapacityWaitReason
			if !errors.As(err, &wait) {
				t.Fatalf("layer evidence not typed: %v", err)
			}
			diagnostic, e := s.ReadCapacity(ctx)
			if e != nil || diagnostic.Status == "ready" {
				t.Fatalf("layer diagnostic=%+v %v", diagnostic, e)
			}
			var pinned string
			if e = pool.QueryRow(ctx, `SELECT COALESCE(container_id,'') FROM runtime_capacity_residents WHERE commitment_id=$1`, r.CommitmentID).Scan(&pinned); e != nil || pinned != "" {
				t.Fatalf("bad enforcement pinned identity=%s %v", pinned, e)
			}
		})
	}
}
