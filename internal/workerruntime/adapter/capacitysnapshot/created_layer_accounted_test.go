package capacitysnapshot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"math"
	"strings"
	"testing"
	"time"
)

func TestCreatedLayerAccountedExecutableRegression(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hard, used uint64
	}{
		{"zero_target", 0, 2},
		{"full_uint64", math.MaxUint64, math.MaxUint64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, f, req, id := createdFixture(t)
			tr.AdmissionPolicy = knownAdmissionPolicy()
			tr.ManifestDigest = Digest([]byte("distinct accounted created-layer manifest"))
			f.ManifestDigest = tr.ManifestDigest
			binary.LittleEndian.PutUint64(f.Quota.Target[24:32], tc.hard)
			binary.LittleEndian.PutUint64(f.Quota.Target[48:56], tc.used)
			v, err := NewCreatedLayerVerifier(tr)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.VerifyCreatedLayerFacts(context.Background(), f, req, id); err != nil {
				t.Fatalf("exact accounted-policy finite evidence rejected: %v", err)
			}
		})
	}
}

func accountedCreatedFixture(t *testing.T) (Trust, ed25519.PrivateKey, CreatedLayerFacts, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) {
	t.Helper()
	tr, key, f, req, id := createdFixture(t)
	tr.AdmissionPolicy = knownAdmissionPolicy()
	tr.ManifestDigest = Digest([]byte("distinct accounted created-layer manifest"))
	f.ManifestDigest = tr.ManifestDigest
	return tr, key, f, req, id
}

func TestCreatedLayerAccountedDiagnosticCounterMatrix(t *testing.T) {
	for _, policy := range []string{"accounted", "legacy"} {
		for _, tc := range []struct {
			name       string
			hard, used uint64
			assessment InodeEvidenceAssessment
			reject     bool
		}{
			{"zero_zero", 0, 0, InodeBoundUnproven, false},
			{"zero_used", 0, 17, InodeBoundUnproven, false},
			{"zero_max", 0, math.MaxUint64, InodeBoundUnproven, false},
			{"positive_below", 7, 6, InodesObserved, false},
			{"positive_equal", 7, 7, InodesObserved, false},
			{"positive_over", 7, 8, InodesOverLimit, true},
			{"maxint_equal", math.MaxInt64, math.MaxInt64, InodesObserved, false},
			{"beyond_int_equal", math.MaxInt64 + 1, math.MaxInt64 + 1, InodesObserved, false},
			{"beyond_int_below", math.MaxInt64 + 1, 1, InodesObserved, false},
			{"max_equal", math.MaxUint64, math.MaxUint64, InodesObserved, false},
			{"max_below", math.MaxUint64, math.MaxInt64 + 1, InodesObserved, false},
			{"beyond_int_over", math.MaxInt64 + 1, math.MaxUint64, InodesOverLimit, true},
		} {
			for _, def := range []uint64{0, 19, math.MaxUint64} {
				t.Run(fmt.Sprintf("%s/%s/default_%d", policy, tc.name, def), func(t *testing.T) {
					tr, _, f, req, id := accountedCreatedFixture(t)
					if policy == "legacy" {
						tr.AdmissionPolicy = nil
					}
					binary.LittleEndian.PutUint64(f.Quota.Target[24:32], tc.hard)
					binary.LittleEndian.PutUint64(f.Quota.Target[48:56], tc.used)
					binary.LittleEndian.PutUint64(f.Quota.Default0[24:32], def)
					binary.LittleEndian.PutUint64(f.Quota.Default0[48:56], math.MaxUint64)
					f.Quota.Target[111] = 0xa7
					f.Quota.Default0[111] = 0x5e
					target, default0 := bytes.Clone(f.Quota.Target), bytes.Clone(f.Quota.Default0)
					v, err := NewCreatedLayerVerifier(tr)
					if err != nil {
						t.Fatal(err)
					}
					got, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id)
					reject := tc.reject || policy == "legacy" && (tc.hard == 0 || tc.hard > math.MaxInt64 || tc.used > math.MaxInt64)
					if (err != nil) != reject {
						t.Fatalf("reject=%v got=%+v err=%v", reject, got, err)
					}
					if reject {
						if got != (CreatedLayerInodeObservation{}) {
							t.Fatalf("failed evidence leaked %+v", got)
						}
					} else if got != (CreatedLayerInodeObservation{HardInodes: tc.hard, UsedInodes: tc.used, Assessment: tc.assessment}) {
						t.Fatalf("counter loss: %+v", got)
					}
					if (v.VerifyCreatedLayerFacts(context.Background(), f, req, id) != nil) != reject {
						t.Fatal("error-only policy drift")
					}
					if !bytes.Equal(target, f.Quota.Target) || !bytes.Equal(default0, f.Quota.Default0) {
						t.Fatal("raw records mutated")
					}
				})
			}
		}
	}
}

func TestCreatedLayerAccountedConstructorPinsExactPolicy(t *testing.T) {
	tr, _, f, req, id := accountedCreatedFixture(t)
	v, err := NewCreatedLayerVerifier(tr)
	if err != nil {
		t.Fatal(err)
	}
	pinnedPolicy := *knownAdmissionPolicy()
	tr.AdmissionPolicy.ID = "unknown"
	tr.AdmissionPolicy.Revision++
	tr.AdmissionPolicy.Digest = Digest([]byte("wrong"))
	if v.trust.AdmissionPolicy == tr.AdmissionPolicy || *v.trust.AdmissionPolicy != pinnedPolicy {
		t.Fatal("constructor retained mutable caller policy reference")
	}
	tr.ManifestDigest = Digest([]byte("replacement"))
	binary.LittleEndian.PutUint64(f.Quota.Target[24:32], 0)
	if _, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id); err != nil {
		t.Fatalf("caller changed pinned policy: %v", err)
	}
	f.ManifestDigest = tr.ManifestDigest
	if got, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id); err == nil || got != (CreatedLayerInodeObservation{}) {
		t.Fatal("replacement manifest accepted")
	}
	for _, field := range []string{"id", "revision", "digest", "empty"} {
		t.Run(field, func(t *testing.T) {
			tr, _, _, _, _ := accountedCreatedFixture(t)
			switch field {
			case "id":
				tr.AdmissionPolicy.ID = "unknown"
			case "revision":
				tr.AdmissionPolicy.Revision++
			case "digest":
				tr.AdmissionPolicy.Digest = Digest([]byte("wrong"))
			case "empty":
				tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{}
			}
			if _, err := NewCreatedLayerVerifier(tr); err == nil {
				t.Fatal("unknown policy constructed")
			}
		})
	}
	legacy, _, lf, lr, li := createdFixture(t)
	lv, err := NewCreatedLayerVerifier(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacy.AdmissionPolicy = knownAdmissionPolicy()
	binary.LittleEndian.PutUint64(lf.Quota.Target[24:32], 0)
	if got, err := lv.AssessCreatedLayerFacts(context.Background(), lf, lr, li); err == nil || got != (CreatedLayerInodeObservation{}) {
		t.Fatal("legacy nil policy upgraded")
	}
	if _, err := NewCreatedLayerVerifier(Trust{}); err == nil {
		t.Fatal("unconfigured Trust constructed")
	}
}

func TestCreatedLayerAccountedDiagnosticContextAndRequestFailures(t *testing.T) {
	tr, _, f, req, id := accountedCreatedFixture(t)
	v, err := NewCreatedLayerVerifier(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nil_context", "cancel_before", "cancel_after", "nil_verifier", "zero_verifier", "request_owner", "identity", "profile", "generation"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Context(context.Background())
			verifier := v
			r, i := req, id
			switch name {
			case "nil_context":
				ctx = nil
			case "cancel_before":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "cancel_after":
				ctx = &createdBoundaryContext{Context: ctx, cancelAt: 2}
			case "nil_verifier":
				verifier = nil
			case "zero_verifier":
				verifier = &CreatedLayerVerifier{}
			case "request_owner":
				r.WorkerSessionID = "other"
			case "identity":
				i.ContainerID = "invalid"
			case "profile":
				r.ResourceProfile.Digest = Digest([]byte("wrong"))
			case "generation":
				r.Generation = 0
			}
			got, err := verifier.AssessCreatedLayerFacts(ctx, f, r, i)
			if err == nil || got != (CreatedLayerInodeObservation{}) {
				t.Fatalf("failure leaked %+v err=%v", got, err)
			}
			if strings.HasPrefix(name, "cancel_") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

func TestCreatedLayerAccountedDiagnosticRejectsCorruptEvidence(t *testing.T) {
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
		"missing_time":        func(f *CreatedLayerFacts) { f.ObservedAt = time.Time{} },
		"zero_interval":       func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt },
		"reversed_interval":   func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt.Add(time.Second) },
		"long_interval":       func(f *CreatedLayerFacts) { f.SampleStartedAt = f.ObservedAt.Add(-31 * time.Second) },
		"inspect_source":      func(f *CreatedLayerFacts) { f.InspectSource = "configured" },
		"driver":              func(f *CreatedLayerFacts) { f.GraphDriver = "overlayfs" },
		"running":             func(f *CreatedLayerFacts) { f.State.Running = true },
		"paused":              func(f *CreatedLayerFacts) { f.State.Paused = true },
		"restarting":          func(f *CreatedLayerFacts) { f.State.Restarting = true },
		"dead":                func(f *CreatedLayerFacts) { f.State.Dead = true },
		"state":               func(f *CreatedLayerFacts) { f.State.Status = "exited" },
		"PID":                 func(f *CreatedLayerFacts) { f.State.PID = 1 },
		"started":             func(f *CreatedLayerFacts) { f.State.StartedAt = f.ObservedAt.Add(-time.Second) },
		"path_outside":        func(f *CreatedLayerFacts) { f.UpperDir = "/other/overlay2/actual-driver-id/diff" },
		"path_alias":          func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/../actual-driver-id/diff" },
		"link_path":           func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/l/diff" },
		"nested_path":         func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2/id/extra/diff" },
		"empty_driver":        func(f *CreatedLayerFacts) { f.UpperDir = "/pool/docker/overlay2//diff" },
		"parent_mapping":      func(f *CreatedLayerFacts) { f.Parent.Path = "/pool/docker/overlay2/other" },
		"diff_mapping":        func(f *CreatedLayerFacts) { f.Diff.Path = f.Parent.Path },
		"same_inode":          func(f *CreatedLayerFacts) { f.Diff.Inode = f.Parent.Inode },
		"target_ABI":          func(f *CreatedLayerFacts) { f.Quota.ABI = "unknown" },
		"target_short":        func(f *CreatedLayerFacts) { f.Quota.Target = f.Quota.Target[:111] },
		"target_version":      func(f *CreatedLayerFacts) { f.Quota.Target[0] = 2 },
		"target_flags":        func(f *CreatedLayerFacts) { f.Quota.Target[1] = 1 },
		"target_project":      func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint32(f.Quota.Target[4:], 100) },
		"default0_missing":    func(f *CreatedLayerFacts) { f.Quota.Default0 = []byte{} },
		"default0_project":    func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint32(f.Quota.Default0[4:], 1) },
		"default0_flags":      func(f *CreatedLayerFacts) { f.Quota.Default0[1] = 1 },
		"inode_exhausted":     func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[48:], 10001) },
		"byte_hard":           func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[8:], 20971521) },
		"byte_soft_zero":      func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[16:], 0) },
		"byte_soft":           func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[16:], 20971519) },
		"byte_exhausted":      func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[40:], 20971521) },
		"byte_overflow":       func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[8:], 1<<63) },
		"quota_no_ENFD":       func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x10 },
		"quota_no_ACCT":       func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x20 },
		"quota_unknown_flags": func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x70 },
		"quota_state_source":  func(f *CreatedLayerFacts) { f.Quota.StateSource = "configured" },
		"quota_source":        func(f *CreatedLayerFacts) { f.Quota.QuotaSource = "Q_XSETPQLIM" },
		"quota_FS":            func(f *CreatedLayerFacts) { f.Quota.FSUUID = "other" },
		"quota_mount":         func(f *CreatedLayerFacts) { f.Quota.MountID++ },
		"quota_device":        func(f *CreatedLayerFacts) { f.Quota.Device = "8:2" },
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
	cases["default0_short"] = func(f *CreatedLayerFacts) { f.Quota.Default0 = f.Quota.Default0[:111] }
	cases["default0_version"] = func(f *CreatedLayerFacts) { f.Quota.Default0[0] = 2 }
	cases["quota_format"] = func(f *CreatedLayerFacts) { f.Quota.Format = "unknown" }
	cases["quota_state_version"] = func(f *CreatedLayerFacts) { f.Quota.StateVersion = 2 }
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tr, _, f, req, id := accountedCreatedFixture(t)
			binary.LittleEndian.PutUint64(f.Quota.Target[24:32], 0)
			// The explicit over-limit mutation requires a finite positive bound.
			if name == "inode_exhausted" {
				binary.LittleEndian.PutUint64(f.Quota.Target[24:32], 10000)
			}
			mutate(&f)
			v, err := NewCreatedLayerVerifier(tr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id)
			if err == nil || got != (CreatedLayerInodeObservation{}) {
				t.Fatalf("corrupt evidence accepted/leaked: %+v err=%v", got, err)
			}
			if v.VerifyCreatedLayerFacts(context.Background(), f, req, id) == nil {
				t.Fatal("error-only path accepted corrupt evidence")
			}
		})
	}
}

func TestCreatedLayerAccountedSignedPathRetainsSchemaDomainAndManifest(t *testing.T) {
	for _, name := range []string{"valid", "old_manifest", "running_domain", "running_version", "other_key", "unknown_field", "missing_field", "reencoded_payload"} {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := accountedCreatedFixture(t)
			binary.LittleEndian.PutUint64(f.Quota.Target[24:32], 0)
			binary.LittleEndian.PutUint64(f.Quota.Target[48:56], math.MaxUint64)
			dir := createdTempDir(t)
			reader, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			// Mutating caller reference cannot modify the reader's policy.
			tr.AdmissionPolicy.ID = "unknown"
			if name == "old_manifest" {
				legacy, _, _, _, _ := createdFixture(t)
				f.ManifestDigest = legacy.ManifestDigest
			}
			payload, _ := json.Marshal(f)
			if name == "unknown_field" {
				payload = []byte(strings.Replace(string(payload), `"pid":0,`, `"pid":0,"extra":1,`, 1))
			}
			if name == "missing_field" {
				payload = []byte(strings.Replace(string(payload), `"pid":0,`, "", 1))
			}
			raw := signedCreated(t, key, payload)
			if name == "running_domain" || name == "running_version" || name == "other_key" || name == "reencoded_payload" {
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				switch name {
				case "running_domain":
					e.Signature = ed25519.Sign(key, append([]byte(SignatureDomain), payload...))
				case "running_version":
					e.Version = Version
				case "other_key":
					e.KeyID = "other"
				case "reencoded_payload":
					e.Payload, _ = json.MarshalIndent(f, "", "  ")
				}
				raw, _ = json.Marshal(e)
			}
			writeCreated(t, dir, id.ContainerID, raw)
			err = reader.CheckCreatedLayer(context.Background(), req, id)
			if (err != nil) != (name != "valid") {
				t.Fatalf("signed path %s: %v", name, err)
			}
		})
	}
}
