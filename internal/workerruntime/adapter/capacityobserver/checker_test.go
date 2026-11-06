package capacityobserver

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const cid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const engine = "12345678-1234-5678-9abc-123456789abc"
const upper = "/mnt/lima-sbquota/docker/overlay2/actual-driver-17/diff"

// Entirely synthetic finite fixture; no real key, IPC, Engine or filesystem read.
func observerFixture(t *testing.T) (capacitysnapshot.Trust, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity, capacityprobe.CreatedLayerCollection) {
	t.Helper()
	profile, _ := platformconfig.ProductionProfileRegistry().RuntimeResource(platformconfig.LocalStandardV1)
	req := workerapp.RuntimeProvisionRequest{RuntimeInstanceID: "runtime-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", Generation: 1, ResourceProfile: workerapp.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}}
	id := workerapp.CreatedRuntimeIdentity{ContainerID: cid, RuntimeInstanceID: req.RuntimeInstanceID, WorkerSessionID: req.WorkerSessionID, Generation: 1}
	ident := capacitysnapshot.Identity{EngineID: engine, DockerRoot: "/mnt/lima-sbquota/docker", FSUUID: "01020304-0506-0708-090a-0b0c0d0e0f10", GuestBootID: "10000000-0000-0000-0000-000000000001", MountNamespace: "mnt:[55]", MountID: 73, Device: "8:7"}
	seed := sha256.Sum256([]byte("synthetic observer fixture only"))
	key := ed25519.NewKeyFromSeed(seed[:])
	tr := capacitysnapshot.Trust{KeyID: "synthetic", PublicKey: key.Public().(ed25519.PublicKey), ManifestDigest: capacitysnapshot.Digest([]byte("synthetic manifest")), Identity: ident, AnchorPath: "/mnt/lima-sbquota/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "aggregate-core", CgroupPath: createdlayeripc.CoreCgroupPath, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000, MemoryMax: 1 << 30, PIDsMax: 512}}}
	for j, role := range []string{"core", "product", "images", "logs", "build", "working_copy", "cache"} {
		project := uint64(j + 1)
		tr.Storage = append(tr.Storage, capacitysnapshot.StorageDomain{ID: role, Path: "/mnt/lima-sbquota/static/" + role, ProjectID: project, HardBytes: 1 << 30, FSUUID: ident.FSUUID, MountID: 73, Device: "8:7", FSType: "xfs", XFlags: 0x200, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: project, HardBlocks512: (1 << 30) / 512}})
		tr.Coverage = append(tr.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: role})
	}
	uuid := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	node := func(p string, ino uint64) capacityprobe.CreatedLayerDirectory {
		n := capacityprobe.CreatedLayerIdentity{CanonicalPath: p, Inode: ino, DeviceMajor: 8, DeviceMinor: 7, Mode: 0040755, MountID: 73, XFSUUID: uuid}
		d := capacityprobe.CreatedLayerDirectory{Before: n, After: n, NoFollowWalk: true, ReopenedNoFollow: true, IdentityRechecked: true, FilesystemUUIDSameFD: true}
		binary.LittleEndian.PutUint32(d.Geometry[:4], 4096)
		binary.LittleEndian.PutUint32(d.Geometry[12:16], 4)
		binary.LittleEndian.PutUint64(d.Geometry[32:40], 100000)
		binary.LittleEndian.PutUint32(d.Geometry[88:92], 5)
		copy(d.Geometry[64:80], uuid[:])
		d.GeometryAfter = d.Geometry
		binary.LittleEndian.PutUint32(d.FSXAttr[:4], 0x200)
		if ino != 31 {
			binary.LittleEndian.PutUint32(d.FSXAttr[12:16], 731)
		}
		d.FSXAttrAfter = d.FSXAttr
		return d
	}
	now := time.Now().UTC().Add(-time.Second)
	physical := &capacityprobe.CreatedLayerPhysical{Root: node("/mnt/lima-sbquota", 31), Parent: node(strings.TrimSuffix(upper, "/diff"), 47), Diff: node(upper, 48), ProjectID: 731, BootIDBefore: [16]byte{0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, MountNamespaceBefore: 55, MountInfoSHA256Before: [32]byte{9}, MountInfoRechecked: true, RootXFSVerified: true, SameMountVerified: true, BootIdentityRechecked: true, MountNamespaceRechecked: true, ObservationComplete: true}
	physical.BootIDAfter = physical.BootIDBefore
	physical.MountNamespaceAfter = 55
	physical.MountInfoSHA256After = physical.MountInfoSHA256Before
	physical.QStatV[0] = 1
	physical.QStatV[2] = 0x30
	for j, q := range []*[112]byte{&physical.TargetQuota, &physical.DefaultQuota} {
		q[0] = 1
		q[1] = 2
		if j == 0 {
			binary.LittleEndian.PutUint32(q[4:8], 731)
		}
		binary.LittleEndian.PutUint64(q[8:16], 20971520)
		binary.LittleEndian.PutUint64(q[16:24], 20971520)
		binary.LittleEndian.PutUint64(q[24:32], 10000)
		binary.LittleEndian.PutUint64(q[40:48], 8)
		binary.LittleEndian.PutUint64(q[48:56], 2)
	}
	c := capacityprobe.CreatedLayerCollection{Schema: "switchback.capacity.untrusted_created_layer_collection.v1", Status: "preparation_blocked", Trust: "none", ObservationComplete: true, Started: now, Ended: now.Add(200 * time.Millisecond), Engine: &capacityprobe.CreatedLayerEngine{ID: engine, APIVersion: "1.56", Version: "29.9.0", DockerRoot: ident.DockerRoot, Driver: "overlay2", Source: "engine_get_v1.56_version_info"}, Inspect: &capacityprobe.CreatedLayerInspect{ID: cid, Status: "created", StartedAt: "0001-01-01T00:00:00Z", Driver: "overlay2", UpperDir: upper, ParentDir: strings.TrimSuffix(upper, "/diff"), Source: "engine_get_v1.56_container_inspect"}, Physical: physical, Blockers: []string{"unsigned_diagnostic_only", "quota_compliance_and_admission_unproven", "ABA_and_external_writers_unexcluded", "all_time_capacity_unknown"}}
	endpoint := "/v1.56/containers/" + cid + "/json"
	for j, op := range []string{"/v1.56/version", "/v1.56/info", endpoint, "physical", endpoint, "/v1.56/version", "/v1.56/info"} {
		start := now.Add(time.Duration(j+1) * time.Millisecond)
		end := start.Add(time.Microsecond)
		if j == 3 {
			end = now.Add(100 * time.Millisecond)
		}
		if j > 3 {
			start = now.Add(time.Duration(100+j) * time.Millisecond)
			end = start.Add(time.Microsecond)
		}
		source := "engine_get_v1.56"
		if j == 3 {
			source = "linux_syscall"
		}
		c.Samples = append(c.Samples, capacityprobe.CreatedLayerSample{Sequence: j + 1, Operation: op, Source: source, Started: start, Ended: end, Outcome: "observed"})
	}
	operations := []string{"boot_id_before", "mount_namespace_before", "mountinfo_before", "root_open", "root_stat", "root_geometry", "root_fsxattr", "parent_open", "parent_stat", "parent_geometry", "parent_fsxattr", "diff_open", "diff_stat", "diff_geometry", "diff_fsxattr", "qstatv", "target_quota", "default0_quota", "boot_id_after", "mount_namespace_after", "mountinfo_after", "root_stat", "root_geometry", "root_fsxattr", "root_reopen", "parent_stat", "parent_geometry", "parent_fsxattr", "parent_reopen", "diff_stat", "diff_geometry", "diff_fsxattr", "diff_reopen"}
	for j, op := range operations {
		start := now.Add(time.Duration(5+j) * time.Millisecond)
		physical.Samples = append(physical.Samples, capacityprobe.CreatedLayerSample{Sequence: j + 1, Operation: op, Source: "linux_syscall", Started: start, Ended: start.Add(time.Microsecond), Outcome: "observed"})
	}
	return tr, req, id, c
}

func installObservation(c *Checker, collection capacityprobe.CreatedLayerCollection, initializations, calls *int) {
	c.initialize = func() (observeFunc, error) {
		*initializations++
		return func(_ context.Context, _ createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
			*calls++
			return collection, nil
		}, nil
	}
}

func TestCheckerPublicRejectsBeforeCommunicationAndConstructsWithoutIO(t *testing.T) {
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	initialized, calls := 0, 0
	installObservation(c, collection, &initialized, &calls)
	bad := req
	bad.Generation = 0
	if c.CheckCreatedLayer(context.Background(), bad, id) == nil || initialized != 0 || calls != 0 {
		t.Fatal("invalid request performed communication")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(c.CheckCreatedLayer(ctx, req, id), context.Canceled) || initialized != 0 {
		t.Fatal("canceled request performed initialization")
	}
	if err = c.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatal(err)
	}
	if err = c.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatal(err)
	}
	if initialized != 1 || calls != 2 {
		t.Fatalf("lazy once=%d observations=%d", initialized, calls)
	}
	var zero Checker
	if zero.CheckCreatedLayer(context.Background(), req, id) == nil {
		t.Fatal("zero checker accepted")
	}
}

func TestCheckerRejectsIPCUnrepresentableRequestBeforeInitialization(t *testing.T) {
	for _, bad := range []string{"contains space", "contains/slash", strings.Repeat("a", 129)} {
		t.Run(bad, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			req.RuntimeInstanceID = bad
			id.RuntimeInstanceID = bad
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			if c.CheckCreatedLayer(context.Background(), req, id) == nil || initialized != 0 || calls != 0 {
				t.Fatalf("unrepresentable IPC request reached initialization/transport: init=%d calls=%d", initialized, calls)
			}
		})
	}
}

func TestCheckerExactAggregateCoreBeforeCommunicationAndPinsTrust(t *testing.T) {
	for _, p := range []string{"/core", "/switchback.slice", "/switchback.slice/switchback-core.slice/child", "/switchback.slice/switchback-core.slice-other", "/switchback.slice/switchback-workers.slice", createdlayeripc.ObserverCgroupPath} {
		t.Run(p, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			tr.Core[0].CgroupPath = p
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			// Late caller mutation cannot manufacture the missing aggregate coverage.
			tr.Core[0].CgroupPath = createdlayeripc.CoreCgroupPath
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			if c.CheckCreatedLayer(context.Background(), req, id) == nil || initialized != 0 || calls != 0 {
				t.Fatal("missing exact aggregate core communicated")
			}
		})
	}
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	tr.Identity.EngineID = "changed"
	tr.ManifestDigest = capacitysnapshot.Digest([]byte("changed"))
	tr.PublicKey[0] ^= 255
	tr.Core[0].CgroupPath = "/changed"
	tr.Storage[0].ProjectID = 731
	tr.Coverage[0].Role = "changed"
	initialized, calls := 0, 0
	installObservation(c, collection, &initialized, &calls)
	if err = c.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatalf("caller changed pinned policy: %v", err)
	}
	if _, err = New(capacitysnapshot.Trust{}); err == nil {
		t.Fatal("empty Trust accepted")
	}
}

func TestCheckerInitializationCancellationAndErrorAreDifferent(t *testing.T) {
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	initialized, calls := 0, 0
	c.initialize = func() (observeFunc, error) {
		initialized++
		cancel()
		return func(context.Context, createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
			calls++
			return collection, nil
		}, nil
	}
	if !errors.Is(c.CheckCreatedLayer(ctx, req, id), context.Canceled) || calls != 0 {
		t.Fatal("canceled initialization reached transport")
	}
	if err = c.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatal(err)
	}
	if initialized != 1 || calls != 1 {
		t.Fatal("request cancellation poisoned/retried initialization")
	}
	c, err = New(tr)
	if err != nil {
		t.Fatal(err)
	}
	initialized = 0
	c.initialize = func() (observeFunc, error) { initialized++; return nil, errors.New("synthetic private failure") }
	for j := 0; j < 2; j++ {
		if err = c.CheckCreatedLayer(context.Background(), req, id); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("initialization failure accepted/leaked")
		}
	}
	if initialized != 1 {
		t.Fatal("key/client failure retried")
	}
	c, err = New(tr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	c.initialize = func() (observeFunc, error) {
		return func(context.Context, createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
			cancel()
			return collection, nil
		}, nil
	}
	if !errors.Is(c.CheckCreatedLayer(ctx, req, id), context.Canceled) {
		t.Fatal("post observation cancellation lost")
	}
}

func TestCheckerBindsExactAuthenticatedRequest(t *testing.T) {
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	c.initialize = func() (observeFunc, error) {
		return func(_ context.Context, b createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
			seen = true
			want := createdlayeripc.Binding{ManifestDigest: tr.ManifestDigest, ContainerID: cid, RuntimeID: "runtime-1", SessionID: "session-1", RunID: "run-1", Generation: 1, ProfileID: req.ResourceProfile.ID, ProfileRevision: req.ResourceProfile.Revision, ProfileDigest: req.ResourceProfile.Digest}
			if b != want {
				t.Fatalf("binding=%+v", b)
			}
			return collection, nil
		}, nil
	}
	if err = c.CheckCreatedLayer(context.Background(), req, id); err != nil || !seen {
		t.Fatalf("binding: %v", err)
	}
}

func TestCheckerInvalidRequestMatrixMakesZeroCommunication(t *testing.T) {
	cases := map[string]func(*workerapp.RuntimeProvisionRequest, *workerapp.CreatedRuntimeIdentity){
		"CID_missing": func(_ *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) { id.ContainerID = "" },
		"CID_partial": func(_ *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			id.ContainerID = cid[:12]
		},
		"runtime": func(_ *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			id.RuntimeInstanceID = "other"
		},
		"session": func(_ *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			id.WorkerSessionID = "other"
		},
		"generation":  func(_ *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) { id.Generation++ },
		"run_missing": func(r *workerapp.RuntimeProvisionRequest, _ *workerapp.CreatedRuntimeIdentity) { r.WorkerRunID = "" },
		"session_missing": func(r *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			r.WorkerSessionID = ""
			id.WorkerSessionID = ""
		},
		"generation0": func(r *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			r.Generation = 0
			id.Generation = 0
		},
		"generation_overflow": func(r *workerapp.RuntimeProvisionRequest, id *workerapp.CreatedRuntimeIdentity) {
			r.Generation = 1 << 63
			id.Generation = r.Generation
		},
		"profile_id": func(r *workerapp.RuntimeProvisionRequest, _ *workerapp.CreatedRuntimeIdentity) {
			r.ResourceProfile.ID = "other"
		},
		"profile_revision": func(r *workerapp.RuntimeProvisionRequest, _ *workerapp.CreatedRuntimeIdentity) {
			r.ResourceProfile.Revision++
		},
		"profile_digest": func(r *workerapp.RuntimeProvisionRequest, _ *workerapp.CreatedRuntimeIdentity) {
			r.ResourceProfile.Digest = capacitysnapshot.Digest([]byte("other"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			change(&req, &id)
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			if c.CheckCreatedLayer(context.Background(), req, id) == nil || initialized != 0 || calls != 0 {
				t.Fatal("invalid request reached key/IPC")
			}
		})
	}
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	initialized, calls := 0, 0
	installObservation(c, collection, &initialized, &calls)
	if c.CheckCreatedLayer(nil, req, id) == nil || initialized != 0 {
		t.Fatal("missing context initialized")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if !errors.Is(c.CheckCreatedLayer(ctx, req, id), context.DeadlineExceeded) || initialized != 0 {
		t.Fatal("expired request initialized")
	}
	var typedNil *Checker
	if typedNil.CheckCreatedLayer(context.Background(), req, id) == nil {
		t.Fatal("nil checker accepted")
	}
}

func TestCheckerConcurrentLegalChecksInitializeOnlyOnce(t *testing.T) {
	tr, req, id, collection := observerFixture(t)
	c, err := New(tr)
	if err != nil {
		t.Fatal(err)
	}
	var initialized, calls atomic.Int32
	c.initialize = func() (observeFunc, error) {
		initialized.Add(1)
		return func(context.Context, createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
			calls.Add(1)
			return collection, nil
		}, nil
	}
	var wg sync.WaitGroup
	for j := 0; j < 16; j++ {
		wg.Go(func() {
			if err := c.CheckCreatedLayer(context.Background(), req, id); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if initialized.Load() != 1 || calls.Load() != 16 {
		t.Fatalf("init=%d calls=%d", initialized.Load(), calls.Load())
	}
}

// Deterministic cancellation between public validation stages, with no IO races.
type stagedCancellation struct {
	context.Context
	calls, at int
}

func (c *stagedCancellation) Err() error {
	c.calls++
	if c.calls >= c.at {
		return context.Canceled
	}
	return nil
}

func TestCheckerRechecksCancellationImmediatelyAroundInitializationAndValidation(t *testing.T) {
	for _, stage := range []string{"before_once", "after_invalid_observation"} {
		t.Run(stage, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			at := 3
			if stage == "after_invalid_observation" {
				collection.ObservationComplete = false
				at = 6
			}
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			ctx := &stagedCancellation{Context: context.Background(), at: at}
			if !errors.Is(c.CheckCreatedLayer(ctx, req, id), context.Canceled) {
				t.Fatal("cancellation replaced by observation error")
			}
			if stage == "before_once" && initialized != 0 {
				t.Fatal("context canceled before initialization still initialized")
			}
		})
	}
}
