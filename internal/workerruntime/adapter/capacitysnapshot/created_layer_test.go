package capacitysnapshot

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createdFixture(t *testing.T) (Trust, ed25519.PrivateKey, CreatedLayerFacts, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) {
	t.Helper()
	tr, key, _ := fixture(t)
	profile, _ := platformconfig.ProductionProfileRegistry().RuntimeResource(platformconfig.LocalStandardV1)
	cid := strings.Repeat("a", 64)
	req := workerapp.RuntimeProvisionRequest{RuntimeInstanceID: "runtime-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", Generation: 1, ResourceProfile: workerapp.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}}
	id := workerapp.CreatedRuntimeIdentity{ContainerID: cid, RuntimeInstanceID: req.RuntimeInstanceID, WorkerSessionID: req.WorkerSessionID, Generation: 1}
	node := func(path string, ino uint64) CreatedLayerNode {
		raw := make([]byte, 28)
		binary.LittleEndian.PutUint32(raw, 0x200)
		binary.LittleEndian.PutUint32(raw[12:], 99)
		return CreatedLayerNode{Path: path, FSUUID: tr.Identity.FSUUID, MountID: tr.Identity.MountID, Device: tr.Identity.Device, FSType: "xfs", Inode: ino, DeviceMajor: 8, DeviceMinor: 1, Mode: 0040755, NoFollow: true, NoSymlinkAncestors: true, MountStable: true, RawFSXAttr: raw}
	}
	quota := func(proj uint32) []byte {
		raw := make([]byte, 112)
		raw[0] = 1
		raw[1] = 2
		binary.LittleEndian.PutUint32(raw[4:], proj)
		binary.LittleEndian.PutUint64(raw[8:], 20971520)
		binary.LittleEndian.PutUint64(raw[16:], 20971520)
		binary.LittleEndian.PutUint64(raw[24:], 10000)
		binary.LittleEndian.PutUint64(raw[40:], 8)
		binary.LittleEndian.PutUint64(raw[48:], 2)
		return raw
	}
	now := time.Now().UTC()
	f := CreatedLayerFacts{SampleStartedAt: now.Add(-time.Millisecond), ObservedAt: now, Identity: tr.Identity, ManifestDigest: tr.ManifestDigest, ContainerID: cid, RuntimeID: req.RuntimeInstanceID, SessionID: req.WorkerSessionID, WorkerRunID: req.WorkerRunID, Generation: 1, ProfileID: profile.ID, ProfileRevision: profile.Revision, ProfileDigest: profile.Digest, InspectSource: "docker_engine_inspect_v1", GraphDriver: "overlay2", State: CreatedLayerState{Status: "created"}, UpperDir: "/pool/docker/overlay2/actual-driver-id/diff", Parent: node("/pool/docker/overlay2/actual-driver-id", 101), Diff: node("/pool/docker/overlay2/actual-driver-id/diff", 102), Quota: CreatedLayerQuota{Format: "linux_xfs_quotactl_v1", StateSource: "Q_XGETQSTATV", StateVersion: 1, StateFlags: 0x30, QuotaSource: "Q_XGETQUOTA", ABI: CreatedLayerQuotaABI, FSUUID: tr.Identity.FSUUID, MountID: tr.Identity.MountID, Device: tr.Identity.Device, Target: quota(99), Default0: quota(0)}}
	return tr, key, f, req, id
}
func signedCreated(t *testing.T, key ed25519.PrivateKey, payload []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(CreatedLayerEnvelope{Version: CreatedLayerVersion, KeyID: "key", Payload: payload, Signature: ed25519.Sign(key, append([]byte(CreatedLayerSignatureDomain), payload...))})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func writeCreated(t *testing.T, dir, cid string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "created-layer-"+cid+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCreatedLayerReaderAcceptsExactSignedFiniteFacts(t *testing.T) {
	tr, key, f, req, id := createdFixture(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewCreatedLayerReader(dir, tr)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(f)
	writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
	if err := r.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatal(err)
	}
}

func createdTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCreatedLayerReaderPinsTrustAndPreservesRawSignature(t *testing.T) {
	tr, key, f, req, id := createdFixture(t)
	dir := createdTempDir(t)
	// Load the existing Trust schema once. Changing its external file later has no effect.
	trustPath := filepath.Join(dir, "trust.json")
	trustBytes, _ := json.Marshal(tr)
	if err := os.WriteFile(trustPath, trustBytes, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadTrust(trustPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewCreatedLayerReader(dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.MarshalIndent(f, "", "  ")
	raw := signedCreated(t, key, payload)
	writeCreated(t, dir, id.ContainerID, raw)
	loaded.PublicKey[0] ^= 255
	loaded.Storage[0].ProjectID = 99
	loaded.Storage[0].Path = f.Parent.Path
	loaded.Core[0].ID = "tampered"
	loaded.Coverage[0].Role = "tampered"
	if err := os.WriteFile(trustPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatalf("pinned Trust changed: %v", err)
	}
	var envelope CreatedLayerEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	compact, _ := json.Marshal(f)
	envelope.Payload = compact
	changed, _ := json.Marshal(envelope)
	writeCreated(t, dir, id.ContainerID, changed)
	if err := r.CheckCreatedLayer(context.Background(), req, id); err == nil {
		t.Fatal("reencoded raw payload retained signature authority")
	}
}

func TestCreatedLayerReaderRejectsPhysicalAndOwnerDrift(t *testing.T) {
	cases := map[string]func(*CreatedLayerFacts){
		"CID":              func(f *CreatedLayerFacts) { f.ContainerID = strings.Repeat("b", 64) },
		"runtime":          func(f *CreatedLayerFacts) { f.RuntimeID = "runtime-other" },
		"session":          func(f *CreatedLayerFacts) { f.SessionID = "session-other" },
		"attempt":          func(f *CreatedLayerFacts) { f.WorkerRunID = "run-other" },
		"generation":       func(f *CreatedLayerFacts) { f.Generation++ },
		"profile_id":       func(f *CreatedLayerFacts) { f.ProfileID = "other" },
		"profile_revision": func(f *CreatedLayerFacts) { f.ProfileRevision++ },
		"profile_digest":   func(f *CreatedLayerFacts) { f.ProfileDigest = Digest([]byte("other")) },
		"Engine":           func(f *CreatedLayerFacts) { f.Identity.EngineID = "other" },
		"boot":             func(f *CreatedLayerFacts) { f.Identity.GuestBootID = "other" },
		"namespace":        func(f *CreatedLayerFacts) { f.Identity.MountNamespace = "other" },
		"FSUUID":           func(f *CreatedLayerFacts) { f.Identity.FSUUID = "other" },
		"mount":            func(f *CreatedLayerFacts) { f.Identity.MountID++ },
		"device":           func(f *CreatedLayerFacts) { f.Identity.Device = "8:2" },
		"root":             func(f *CreatedLayerFacts) { f.Identity.DockerRoot = "/other" },
		"manifest":         func(f *CreatedLayerFacts) { f.ManifestDigest = Digest([]byte("other")) },
		"stale": func(f *CreatedLayerFacts) {
			f.SampleStartedAt = f.SampleStartedAt.Add(-31 * time.Second)
			f.ObservedAt = f.ObservedAt.Add(-31 * time.Second)
		},
		"future": func(f *CreatedLayerFacts) {
			f.SampleStartedAt = f.SampleStartedAt.Add(3 * time.Second)
			f.ObservedAt = f.ObservedAt.Add(3 * time.Second)
		},
		"missing_time":             func(f *CreatedLayerFacts) { f.ObservedAt = time.Time{} },
		"zero_interval":            func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt },
		"reversed_interval":        func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt.Add(time.Second) },
		"long_interval":            func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt.Add(-31 * time.Second) },
		"inspect_source":           func(f *CreatedLayerFacts) { f.InspectSource = "configured" },
		"driver":                   func(f *CreatedLayerFacts) { f.GraphDriver = "overlayfs" },
		"running":                  func(f *CreatedLayerFacts) { f.State.Running = true },
		"paused":                   func(f *CreatedLayerFacts) { f.State.Paused = true },
		"restarting":               func(f *CreatedLayerFacts) { f.State.Restarting = true },
		"dead":                     func(f *CreatedLayerFacts) { f.State.Dead = true },
		"state":                    func(f *CreatedLayerFacts) { f.State.Status = "exited" },
		"PID":                      func(f *CreatedLayerFacts) { f.State.PID = 1 },
		"started":                  func(f *CreatedLayerFacts) { f.State.StartedAt = f.ObservedAt.Add(-time.Second) },
		"path_outside":             func(f *CreatedLayerFacts) { f.UpperDir = "/other/overlay2/actual-driver-id/diff" },
		"path_alias":               func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/../actual-driver-id/diff" },
		"link_path":                func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/l/diff" },
		"nested_path":              func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/id/extra/diff" },
		"empty_driver":             func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2//diff" },
		"parent_mapping":           func(f *CreatedLayerFacts) { f.Parent.Path = "/pool/docker/overlay2/other" },
		"diff_mapping":             func(f *CreatedLayerFacts) { f.Diff.Path = f.Parent.Path },
		"same_inode":               func(f *CreatedLayerFacts) { f.Diff.Inode = f.Parent.Inode },
		"target_ABI":               func(f *CreatedLayerFacts) { f.Quota.ABI = "unknown" },
		"target_short":             func(f *CreatedLayerFacts) { f.Quota.Target = f.Quota.Target[:111] },
		"target_version":           func(f *CreatedLayerFacts) { f.Quota.Target[0] = 2 },
		"target_flags":             func(f *CreatedLayerFacts) { f.Quota.Target[1] = 1 },
		"target_project":           func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint32(f.Quota.Target[4:], 100) },
		"default0_missing":         func(f *CreatedLayerFacts) { f.Quota.Default0 = []byte{} },
		"default0_project":         func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint32(f.Quota.Default0[4:], 1) },
		"default0_flags":           func(f *CreatedLayerFacts) { f.Quota.Default0[1] = 1 },
		"default0_raw0_no_control": func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[24:], 0) },
		"inode_exhausted":          func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[48:], 10001) },
		"byte_hard":                func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[8:], 20971521) },
		"byte_soft_zero":           func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[16:], 0) },
		"byte_soft":                func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[16:], 20971519) },
		"byte_exhausted":           func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[40:], 20971521) },
		"byte_overflow":            func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[8:], 1<<63) },
		"quota_no_ENFD":            func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x10 },
		"quota_no_ACCT":            func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x20 },
		"quota_unknown_flags":      func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x70 },
		"quota_state_source":       func(f *CreatedLayerFacts) { f.Quota.StateSource = "configured" },
		"quota_source":             func(f *CreatedLayerFacts) { f.Quota.QuotaSource = "Q_XSETPQLIM" },
		"quota_FS":                 func(f *CreatedLayerFacts) { f.Quota.FSUUID = "other" },
		"quota_mount":              func(f *CreatedLayerFacts) { f.Quota.MountID++ },
		"quota_device":             func(f *CreatedLayerFacts) { f.Quota.Device = "8:2" },
		"static_project": func(f *CreatedLayerFacts) {
			binary.LittleEndian.PutUint32(f.Parent.RawFSXAttr[12:], 1)
			binary.LittleEndian.PutUint32(f.Diff.RawFSXAttr[12:], 1)
			binary.LittleEndian.PutUint32(f.Quota.Target[4:], 1)
		},
	}
	for _, which := range []string{"parent", "diff"} {
		for name, mutate := range map[string]func(*CreatedLayerNode){
			"FS": func(n *CreatedLayerNode) { n.FSUUID = "other" }, "mount": func(n *CreatedLayerNode) { n.MountID++ }, "device": func(n *CreatedLayerNode) { n.DeviceMinor++ }, "inode0": func(n *CreatedLayerNode) { n.Inode = 0 }, "regular": func(n *CreatedLayerNode) { n.Mode = 0100644 }, "nofollow": func(n *CreatedLayerNode) { n.NoFollow = false }, "symlink": func(n *CreatedLayerNode) { n.NoSymlinkAncestors = false }, "substitution": func(n *CreatedLayerNode) { n.MountStable = false }, "raw_short": func(n *CreatedLayerNode) { n.RawFSXAttr = n.RawFSXAttr[:27] }, "project0": func(n *CreatedLayerNode) { binary.LittleEndian.PutUint32(n.RawFSXAttr[12:], 0) }, "inherit": func(n *CreatedLayerNode) { binary.LittleEndian.PutUint32(n.RawFSXAttr, 0) }, "realtime": func(n *CreatedLayerNode) { binary.LittleEndian.PutUint32(n.RawFSXAttr, 0x201) }, "realinherit": func(n *CreatedLayerNode) { binary.LittleEndian.PutUint32(n.RawFSXAttr, 0x300) }, "project_drift": func(n *CreatedLayerNode) { binary.LittleEndian.PutUint32(n.RawFSXAttr[12:], 100) },
		} {
			which, mutate := which, mutate
			cases[which+"_"+name] = func(f *CreatedLayerFacts) {
				if which == "parent" {
					mutate(&f.Parent)
				} else {
					mutate(&f.Diff)
				}
			}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := createdFixture(t)
			dir := createdTempDir(t)
			r, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&f)
			payload, _ := json.Marshal(f)
			writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
			if err := r.CheckCreatedLayer(context.Background(), req, id); err == nil {
				t.Fatal("unproven created layer accepted")
			}
		})
	}
}

func TestCreatedLayerReaderRejectsUnsafeOrAmbiguousProof(t *testing.T) {
	for _, name := range []string{"missing_file", "signature", "running_domain", "running_version", "other_key", "unsigned", "duplicate", "missing_field", "unknown_field", "trailing", "oversize", "symlink", "wrong_filename", "short_CID", "upper_CID", "traversal_CID", "request_owner", "request_profile", "cancel"} {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := createdFixture(t)
			dir := createdTempDir(t)
			r, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(f)
			raw := signedCreated(t, key, payload)
			ctx := context.Background()
			switch name {
			case "missing_file":
			case "signature":
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				e.Signature[0] ^= 255
				raw, _ = json.Marshal(e)
			case "running_domain":
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				e.Signature = ed25519.Sign(key, append([]byte(SignatureDomain), payload...))
				raw, _ = json.Marshal(e)
			case "running_version":
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				e.Version = Version
				raw, _ = json.Marshal(e)
			case "other_key":
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				e.KeyID = "self-declared"
				raw, _ = json.Marshal(e)
			case "unsigned":
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				e.Signature = []byte{}
				raw, _ = json.Marshal(e)
			case "duplicate":
				raw = signedCreated(t, key, []byte(strings.Replace(string(payload), `"generation":1`, `"generation":1,"generation":1`, 1)))
			case "missing_field":
				raw = signedCreated(t, key, []byte(strings.Replace(string(payload), `"pid":0,`, "", 1)))
			case "unknown_field":
				raw = signedCreated(t, key, []byte(strings.Replace(string(payload), `"pid":0,`, `"pid":0,"cgroup_path":"/made-up",`, 1)))
			case "trailing":
				raw = append(raw, []byte(" {}")...)
			case "oversize":
				raw = make([]byte, MaxBytes+1)
			case "short_CID":
				id.ContainerID = "aaaa"
			case "upper_CID":
				id.ContainerID = strings.Repeat("A", 64)
			case "traversal_CID":
				id.ContainerID = "../proof"
			case "request_owner":
				req.WorkerSessionID = "other"
			case "request_profile":
				req.ResourceProfile.Digest = Digest([]byte("other"))
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if name != "missing_file" && name != "traversal_CID" {
				writeCreated(t, dir, id.ContainerID, raw)
			}
			if name == "symlink" {
				leaf := filepath.Join(dir, "created-layer-"+id.ContainerID+".json")
				os.Rename(leaf, filepath.Join(dir, "other"))
				if err := os.Symlink(filepath.Join(dir, "other"), leaf); err != nil {
					t.Fatal(err)
				}
			}
			if name == "wrong_filename" {
				os.Rename(filepath.Join(dir, "created-layer-"+id.ContainerID+".json"), filepath.Join(dir, "capacity.json"))
			}
			if err := r.CheckCreatedLayer(ctx, req, id); err == nil {
				t.Fatal("unsafe or ambiguous proof accepted")
			}
		})
	}
}

// A deterministic context makes cancellation at I/O and verification boundaries
// observable without timing races or a mutable reader callback.
type createdBoundaryContext struct {
	context.Context
	calls, cancelAt int
}

func (c *createdBoundaryContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
func TestCreatedLayerReaderRechecksContextAfterIOAndVerification(t *testing.T) {
	for _, at := range []int{2, 3} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			tr, key, f, req, id := createdFixture(t)
			dir := createdTempDir(t)
			r, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(f)
			writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
			ctx := &createdBoundaryContext{Context: context.Background(), cancelAt: at}
			if err := r.CheckCreatedLayer(ctx, req, id); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation boundary lost: %v", err)
			}
		})
	}
}
func TestCreatedLayerConstructorRejectsUnsafeDirectoryAndIncompleteTrust(t *testing.T) {
	tr, _, _, _, _ := createdFixture(t)
	dir := createdTempDir(t)
	if _, err := NewCreatedLayerReader(dir, Trust{}); err == nil {
		t.Fatal("missing pinned Trust accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "nested"), "relative", dir + "/../alias", filepath.Join(dir, "missing")} {
		if _, err := NewCreatedLayerReader(path, tr); err == nil {
			t.Fatalf("unsafe directory accepted: %s", path)
		}
	}
}

func TestCreatedLayerReaderPinsStaticProjectSlice(t *testing.T) {
	tr, key, f, req, id := createdFixture(t)
	dir := createdTempDir(t)
	r, err := NewCreatedLayerReader(dir, tr)
	if err != nil {
		t.Fatal(err)
	}
	// A caller replacing a static project with the dynamic project cannot change
	// the constructor's pinned overlap decision.
	tr.Storage[0].ProjectID = 99
	payload, _ := json.Marshal(f)
	writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
	if err := r.CheckCreatedLayer(context.Background(), req, id); err != nil {
		t.Fatalf("external static project mutation changed check: %v", err)
	}
	binary.LittleEndian.PutUint32(f.Parent.RawFSXAttr[12:], 1)
	binary.LittleEndian.PutUint32(f.Diff.RawFSXAttr[12:], 1)
	binary.LittleEndian.PutUint32(f.Quota.Target[4:], 1)
	payload, _ = json.Marshal(f)
	writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
	if err := r.CheckCreatedLayer(context.Background(), req, id); err == nil {
		t.Fatal("constructor's original static project protection lost")
	}
}
