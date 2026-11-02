//go:build integration

// Package runtimecapacity supplies explicit SYNTHETIC signed host facts for
// integration tests. It uses production trust/Observe APIs and grants no bypass.
package runtimecapacity

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/platform/config"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
)

func Initialize(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	trust, key, facts := capacityFixture(t)
	if err = store.InitializeTrust(ctx, trust); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err = pool.QueryRow(ctx, `SELECT latest_sequence FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	facts.Sequence = seq + 1
	if err = store.ObservePool(ctx, signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
	}
}
func capacityFixture(t *testing.T) (capacitysnapshot.Trust, ed25519.PrivateKey, capacitysnapshot.Facts) {
	t.Helper()
	seed := sha256.Sum256([]byte("SYNTHETIC INTEGRATION TEST CAPACITY KEY - NOT DEPLOYMENT TRUST"))
	key := ed25519.NewKeyFromSeed(seed[:])
	pub := key.Public().(ed25519.PublicKey)
	identity := capacitysnapshot.Identity{EngineID: "engine-one", DockerRoot: "/pool/docker", FSUUID: "xfs-one", GuestBootID: "boot-one", MountNamespace: "mnt:[1]", MountID: 1, Device: "8:1"}
	trust := capacitysnapshot.Trust{KeyID: "deployment-one", PublicKey: pub, ManifestDigest: capacitysnapshot.Digest([]byte("deployment manifest")), Identity: identity, AnchorPath: "/pool/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "core", CgroupPath: "/core", CPUQuotaMicros: 100000, CPUPeriodMicros: 100000, MemoryMax: 1 << 30, PIDsMax: 512}}}
	for i, id := range []string{"core", "product", "images", "logs", "build", "working_copy", "cache"} {
		trust.Storage = append(trust.Storage, capacityStorage(id, "/pool/"+id, uint64(i+1), 1<<30, 0))
		trust.Coverage = append(trust.Coverage, capacitysnapshot.StorageCoverage{Role: id, DomainID: id})
	}
	facts := capacitysnapshot.Facts{Sequence: 1, ObservedAt: time.Now().UTC(), Identity: identity, ManifestDigest: trust.ManifestDigest, EngineCPUMilli: 64000, EngineMemoryBytes: 128 << 30, Anchor: capacitysnapshot.Anchor{Path: trust.AnchorPath, FSUUID: identity.FSUUID, MountID: 1, Device: "8:1", FSType: "xfs", MountOptions: []string{"prjquota"}, AvailableBytes: 100 << 30}, Core: trust.Core, Storage: trust.Storage, Containers: []capacitysnapshot.Container{}, Removals: []capacitysnapshot.Removal{}}
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
func capacityStorage(id, path string, project, hard, used uint64) capacitysnapshot.StorageDomain {
	return capacitysnapshot.StorageDomain{ID: id, Path: path, ProjectID: project, HardBytes: hard, UsedBytes: used, FSUUID: "xfs-one", Device: "8:1", MountID: 1, FSType: "xfs", XFlags: 0x200, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: project, HardBlocks512: hard / 512, UsedBlocks512: used / 512}}
}

// ObserveContainer publishes explicit synthetic actual-container enforcement
// through the same signature verification and Observe API as deployment facts.
func ObserveContainer(t *testing.T, pool *pgxpool.Pool, session, runtime, container string, generation uint64) {
	t.Helper()
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	_, key, facts := capacityFixture(t)
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if err = pool.QueryRow(ctx, `SELECT latest_sequence+1 FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&facts.Sequence); err != nil {
		t.Fatal(err)
	}
	facts.Containers = []capacitysnapshot.Container{{SessionID: session, RuntimeID: runtime, ContainerID: container, Generation: generation, ProfileID: p.ID, ProfileRevision: p.Revision, ProfileDigest: p.Digest, NanoCPUs: 2_000_000_000, MemoryMax: 4 << 30, PIDsMax: 512, CPUQuotaMicros: 200000, CPUPeriodMicros: 100000, CgroupPath: "/worker/" + runtime, Layer: capacityStorage("layer-"+runtime, "/pool/layer/"+container, 100, 10<<30, 0)}}
	if err = store.ObservePool(ctx, signCapacity(t, key, facts)); err != nil {
		t.Fatal(err)
	}
}

func ObserveStale(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	_, key, facts := capacityFixture(t)
	facts.ObservedAt = time.Now().UTC().Add(-time.Minute)
	if err = pool.QueryRow(ctx, `SELECT latest_sequence+1 FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&facts.Sequence); err != nil {
		t.Fatal(err)
	}
	if err = store.ObservePool(ctx, signCapacity(t, key, facts)); !errors.Is(err, app.CapacityEvidenceStale) {
		t.Fatalf("stale observation: %v", err)
	}
}

// Inventory is an explicit per-fixture SYNTHETIC Engine authority. Every Observe
// contains all containers published by this fixture, with distinct layer projects.
// It is never installed by postgrestest or production composition.
type Inventory struct {
	t          *testing.T
	pool       *pgxpool.Pool
	containers []capacitysnapshot.Container
}

func NewInventory(t *testing.T, pool *pgxpool.Pool) *Inventory {
	t.Helper()
	Initialize(t, pool)
	return &Inventory{t: t, pool: pool}
}
func (f *Inventory) Observe(session, runtime, container string, generation uint64) {
	f.t.Helper()
	ctx := context.Background()
	store, err := workerpostgres.NewCapacityStore(ctx, f.pool)
	if err != nil {
		f.t.Fatal(err)
	}
	_, key, facts := capacityFixture(f.t)
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	f.containers = append(f.containers, capacitysnapshot.Container{SessionID: session, RuntimeID: runtime, ContainerID: container, Generation: generation, ProfileID: p.ID, ProfileRevision: p.Revision, ProfileDigest: p.Digest, NanoCPUs: 2_000_000_000, MemoryMax: 4 << 30, PIDsMax: 512, CPUQuotaMicros: 200000, CPUPeriodMicros: 100000, CgroupPath: "/worker/" + runtime, Layer: capacityStorage("layer-"+runtime, "/pool/layer/"+container, uint64(100+len(f.containers)), 10<<30, 0)})
	facts.Containers = append([]capacitysnapshot.Container(nil), f.containers...)
	if err = f.pool.QueryRow(ctx, `SELECT latest_sequence+1 FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&facts.Sequence); err != nil {
		f.t.Fatal(err)
	}
	if err = store.ObservePool(ctx, signCapacity(f.t, key, facts)); err != nil {
		f.t.Fatal(err)
	}
}

// FileInventory publishes SYNTHETIC physics only. It does not write a capacity
// ledger: production composition must read/verify/reconcile these fixed files.
type FileInventory struct {
	t                    *testing.T
	mu                   sync.Mutex
	TrustPath, Directory string
	trust                capacitysnapshot.Trust
	key                  ed25519.PrivateKey
	facts                capacitysnapshot.Facts
}

func NewFileInventory(t *testing.T) *FileInventory {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trust, key, facts := capacityFixture(t)
	f := &FileInventory{t: t, TrustPath: filepath.Join(dir, "trust.json"), Directory: dir, trust: trust, key: key, facts: facts}
	raw, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.TrustPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.Publish(nil)
	return f
}
func (f *FileInventory) Publish(change func(*capacitysnapshot.Facts)) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.facts.Sequence++
	f.facts.ObservedAt = time.Now().UTC()
	if change != nil {
		change(&f.facts)
	}
	raw := signCapacity(f.t, f.key, f.facts)
	path := filepath.Join(f.Directory, "incoming")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(f.Directory, "capacity.json")); err != nil {
		f.t.Fatal(err)
	}
}
func (f *FileInventory) Observe(session, runtime, container string, generation uint64) {
	f.Publish(func(facts *capacitysnapshot.Facts) {
		p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
		facts.Containers = append(facts.Containers, capacitysnapshot.Container{SessionID: session, RuntimeID: runtime, ContainerID: container, Generation: generation, ProfileID: p.ID, ProfileRevision: p.Revision, ProfileDigest: p.Digest, NanoCPUs: 2_000_000_000, MemoryMax: 4 << 30, PIDsMax: 512, CPUQuotaMicros: 200000, CPUPeriodMicros: 100000, CgroupPath: "/worker/" + runtime, Layer: capacityStorage("layer-"+runtime, "/pool/layer/"+container, uint64(100+len(facts.Containers)), 10<<30, 0)})
	})
}
