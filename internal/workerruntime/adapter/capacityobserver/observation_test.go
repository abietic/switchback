package capacityobserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
)

func mutateDirs(c *capacityprobe.CreatedLayerCollection, f func(*capacityprobe.CreatedLayerDirectory)) {
	for _, d := range []*capacityprobe.CreatedLayerDirectory{&c.Physical.Root, &c.Physical.Parent, &c.Physical.Diff} {
		f(d)
	}
}
func shiftObservation(c *capacityprobe.CreatedLayerCollection, d time.Duration) {
	c.Started = c.Started.Add(d)
	c.Ended = c.Ended.Add(d)
	for j := range c.Samples {
		c.Samples[j].Started = c.Samples[j].Started.Add(d)
		c.Samples[j].Ended = c.Samples[j].Ended.Add(d)
	}
	for j := range c.Physical.Samples {
		c.Physical.Samples[j].Started = c.Physical.Samples[j].Started.Add(d)
		c.Physical.Samples[j].Ended = c.Physical.Samples[j].Ended.Add(d)
	}
}

func TestCheckerRejectsFiniteIdentityCompletenessAndQuotaDrift(t *testing.T) {
	cases := map[string]func(*capacityprobe.CreatedLayerCollection){
		"engine":      func(c *capacityprobe.CreatedLayerCollection) { c.Engine.ID = "99999999-1234-5678-9abc-123456789abc" },
		"docker_root": func(c *capacityprobe.CreatedLayerCollection) { c.Engine.DockerRoot = "/different/docker" },
		"driver":      func(c *capacityprobe.CreatedLayerCollection) { c.Engine.Driver = "other" },
		"boot": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.BootIDBefore[0]++
			c.Physical.BootIDAfter = c.Physical.BootIDBefore
		},
		"namespace": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.MountNamespaceBefore++
			c.Physical.MountNamespaceAfter = c.Physical.MountNamespaceBefore
		},
		"mount": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) { d.Before.MountID++; d.After = d.Before })
		},
		"device": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) { d.Before.DeviceMinor++; d.After = d.Before })
		},
		"FSUUID": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) {
				d.Before.XFSUUID[0]++
				d.After = d.Before
				d.Geometry[64]++
				d.GeometryAfter = d.Geometry
			})
		},
		"schema":    func(c *capacityprobe.CreatedLayerCollection) { c.Schema = "other" },
		"status":    func(c *capacityprobe.CreatedLayerCollection) { c.Status = "ready" },
		"trust":     func(c *capacityprobe.CreatedLayerCollection) { c.Trust = "trusted" },
		"execution": func(c *capacityprobe.CreatedLayerCollection) { c.ExecutionEnabled = true },
		"capacity":  func(c *capacityprobe.CreatedLayerCollection) { c.CapacityReady = true },
		"blocker": func(c *capacityprobe.CreatedLayerCollection) {
			c.Blockers = append(c.Blockers, "physical_unavailable_or_incomplete")
		},
		"collection_incomplete":    func(c *capacityprobe.CreatedLayerCollection) { c.ObservationComplete = false },
		"physical_missing":         func(c *capacityprobe.CreatedLayerCollection) { c.Physical = nil },
		"engine_missing":           func(c *capacityprobe.CreatedLayerCollection) { c.Engine = nil },
		"inspect_missing":          func(c *capacityprobe.CreatedLayerCollection) { c.Inspect = nil },
		"physical_incomplete":      func(c *capacityprobe.CreatedLayerCollection) { c.Physical.ObservationComplete = false },
		"truncated":                func(c *capacityprobe.CreatedLayerCollection) { c.Physical.SamplesTruncated = true },
		"outer_missing":            func(c *capacityprobe.CreatedLayerCollection) { c.Samples = c.Samples[:6] },
		"outer_sequence":           func(c *capacityprobe.CreatedLayerCollection) { c.Samples[0].Sequence = 2 },
		"outer_source":             func(c *capacityprobe.CreatedLayerCollection) { c.Samples[0].Source = "unknown" },
		"outer_operation":          func(c *capacityprobe.CreatedLayerCollection) { c.Samples[2].Operation = "/v1.56/containers/other/json" },
		"outer_errno":              func(c *capacityprobe.CreatedLayerCollection) { n := uint64(2); c.Samples[3].Errno = &n },
		"physical_missing_samples": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples = nil },
		"physical_extra_samples": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Samples = append(c.Physical.Samples, c.Physical.Samples[0])
		},
		"physical_sequence":         func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[0].Sequence = 5 },
		"physical_source":           func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[0].Source = "unknown" },
		"physical_operation":        func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[1].Operation = "root_open" },
		"physical_errno_ENOENT":     func(c *capacityprobe.CreatedLayerCollection) { n := uint64(2); c.Physical.Samples[17].Errno = &n },
		"physical_unavailable":      func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[0].Outcome = "unavailable" },
		"physical_outside_interval": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[0].Started = c.Started },
		"physical_unordered": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Samples[2].Started = c.Physical.Samples[0].Started
		},
		"physical_zero_time": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Samples[0].Started = time.Time{} },
		"physical_zero_duration": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Samples[0].Ended = c.Physical.Samples[0].Started
		},
		"root_inode0": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Root.Before.Inode = 0
			c.Physical.Root.After = c.Physical.Root.Before
		},
		"parent_inode0": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.Before.Inode = 0
			c.Physical.Parent.After = c.Physical.Parent.Before
		},
		"diff_inode0": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Diff.Before.Inode = 0
			c.Physical.Diff.After = c.Physical.Diff.Before
		},
		"same_inode": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Diff.Before.Inode = c.Physical.Parent.Before.Inode
			c.Physical.Diff.After = c.Physical.Diff.Before
		},
		"inode_changed": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Parent.After.Inode++ },
		"path_changed":  func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Diff.After.CanonicalPath = "/wrong" },
		"path_before": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.Before.CanonicalPath = "/wrong"
			c.Physical.Parent.After = c.Physical.Parent.Before
		},
		"root_path": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Root.Before.CanonicalPath = "/wrong"
			c.Physical.Root.After = c.Physical.Root.Before
		},
		"uuid_after": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Diff.After.XFSUUID[1]++ },
		"uuid_geometry": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Diff.Before.XFSUUID[1]++
			c.Physical.Diff.After = c.Physical.Diff.Before
		},
		"geometry_raw_changed": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Parent.GeometryAfter[5]++ },
		"geometry_version": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) { d.Geometry[88] = 4; d.GeometryAfter = d.Geometry })
		},
		"geometry_reserved": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) { d.Geometry[120] = 1; d.GeometryAfter = d.Geometry })
		},
		"geometry_zero_uuid": func(c *capacityprobe.CreatedLayerCollection) {
			mutateDirs(c, func(d *capacityprobe.CreatedLayerDirectory) {
				clear(d.Geometry[64:80])
				d.Before.XFSUUID = [16]byte{}
				d.After = d.Before
				d.GeometryAfter = d.Geometry
			})
		},
		"geometry_mismatch": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.Geometry[32]++
			c.Physical.Parent.GeometryAfter = c.Physical.Parent.Geometry
		},
		"attrs_changed": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.Parent.FSXAttrAfter[1]++ },
		"realtime": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.FSXAttr[0] |= 1
			c.Physical.Parent.FSXAttrAfter = c.Physical.Parent.FSXAttr
		},
		"rtinherit": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.FSXAttr[1] |= 1
			c.Physical.Parent.FSXAttrAfter = c.Physical.Parent.FSXAttr
		},
		"noinherit": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.FSXAttr[1] = 0
			c.Physical.Parent.FSXAttrAfter = c.Physical.Parent.FSXAttr
		},
		"project_mismatch": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.ProjectID++ },
		"static_project": func(c *capacityprobe.CreatedLayerCollection) {
			for _, d := range []*capacityprobe.CreatedLayerDirectory{&c.Physical.Parent, &c.Physical.Diff} {
				binary.LittleEndian.PutUint32(d.FSXAttr[12:16], 1)
				d.FSXAttrAfter = d.FSXAttr
			}
			c.Physical.ProjectID = 1
			binary.LittleEndian.PutUint32(c.Physical.TargetQuota[4:8], 1)
		},
		"QSTATV_version":        func(c *capacityprobe.CreatedLayerCollection) { c.Physical.QStatV[0] = 2 },
		"quota_version":         func(c *capacityprobe.CreatedLayerCollection) { c.Physical.TargetQuota[0] = 2 },
		"quota_type":            func(c *capacityprobe.CreatedLayerCollection) { c.Physical.TargetQuota[1] = 1 },
		"quota_project":         func(c *capacityprobe.CreatedLayerCollection) { c.Physical.TargetQuota[4]++ },
		"default_project":       func(c *capacityprobe.CreatedLayerCollection) { c.Physical.DefaultQuota[4] = 1 },
		"default_ABI":           func(c *capacityprobe.CreatedLayerCollection) { c.Physical.DefaultQuota[0] = 2 },
		"quota_realtime":        func(c *capacityprobe.CreatedLayerCollection) { c.Physical.TargetQuota[72] = 1 },
		"default_realtime":      func(c *capacityprobe.CreatedLayerCollection) { c.Physical.DefaultQuota[103] = 1 },
		"quota_off":             func(c *capacityprobe.CreatedLayerCollection) { c.Physical.QStatV[2] = 0 },
		"quota_accounting_only": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.QStatV[2] = 0x10 },
		"quota_unknown_flags":   func(c *capacityprobe.CreatedLayerCollection) { c.Physical.QStatV[3] = 1 },
		"quota_zero":            func(c *capacityprobe.CreatedLayerCollection) { clear(c.Physical.TargetQuota[8:24]) },
		"not10GiB": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[8:16], 2048)
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[16:24], 2048)
		},
		"soft_ne_hard": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.TargetQuota[16]++ },
		"overflow": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[8:16], math.MaxUint64)
		},
		"inodehard0_default_positive": func(c *capacityprobe.CreatedLayerCollection) { clear(c.Physical.TargetQuota[24:32]) },
		"inodehard_overflow": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[24:32], math.MaxUint64)
		},
		"inodeused_overflow": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[48:56], math.MaxUint64)
		},
		"inode_exhausted": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[48:56], 10001)
		},
		"bytes_exhausted": func(c *capacityprobe.CreatedLayerCollection) {
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[40:48], 20971521)
		},
		"stale":        func(c *capacityprobe.CreatedLayerCollection) { shiftObservation(c, -31*time.Second) },
		"future":       func(c *capacityprobe.CreatedLayerCollection) { shiftObservation(c, 5*time.Second) },
		"interval":     func(c *capacityprobe.CreatedLayerCollection) { c.Ended = c.Started.Add(31 * time.Second) },
		"time_zero":    func(c *capacityprobe.CreatedLayerCollection) { c.Started = time.Time{} },
		"time_reverse": func(c *capacityprobe.CreatedLayerCollection) { c.Ended = c.Started },
		"metadata_mode": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Root.Before.Mode |= 0020
			c.Physical.Root.After = c.Physical.Root.Before
		},
		"metadata_uid": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Parent.Before.UID = 1000
			c.Physical.Parent.After = c.Physical.Parent.Before
		},
		"metadata_gid": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.Diff.Before.GID = 1000
			c.Physical.Diff.After = c.Physical.Diff.Before
		},
		"boot_recheck":      func(c *capacityprobe.CreatedLayerCollection) { c.Physical.BootIDAfter[0]++ },
		"namespace_recheck": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.MountNamespaceAfter++ },
		"mountinfo_recheck": func(c *capacityprobe.CreatedLayerCollection) { c.Physical.MountInfoSHA256After[0]++ },
		"mountinfo0": func(c *capacityprobe.CreatedLayerCollection) {
			c.Physical.MountInfoSHA256Before = [32]byte{}
			c.Physical.MountInfoSHA256After = [32]byte{}
		},
		"CID":     func(c *capacityprobe.CreatedLayerCollection) { c.Inspect.ID = "other" },
		"started": func(c *capacityprobe.CreatedLayerCollection) { c.Inspect.Running = true },
		"StartedAt": func(c *capacityprobe.CreatedLayerCollection) {
			c.Inspect.StartedAt = time.Now().Format(time.RFC3339Nano)
		},
		"inspect_provenance": func(c *capacityprobe.CreatedLayerCollection) { c.Inspect.Source = "unknown" },
		"engine_provenance":  func(c *capacityprobe.CreatedLayerCollection) { c.Engine.Source = "unknown" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			change(&collection)
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			if c.CheckCreatedLayer(context.Background(), req, id) == nil {
				t.Fatal("invalid finite observation accepted")
			}
			if initialized != 1 || calls != 1 {
				t.Fatal("did not exercise observation")
			}
		})
	}
}

func TestObservationTranslationPreservesRawAndDerivesIdentities(t *testing.T) {
	tr, req, _, c := observerFixture(t)
	c.Physical.TargetQuota[111] = 0xa8
	c.Physical.DefaultQuota[111] = 0xa9
	b := createdlayeripc.Binding{ManifestDigest: tr.ManifestDigest, ContainerID: cid, RuntimeID: req.RuntimeInstanceID, SessionID: req.WorkerSessionID, RunID: req.WorkerRunID, Generation: 1, ProfileID: req.ResourceProfile.ID, ProfileRevision: req.ResourceProfile.Revision, ProfileDigest: req.ResourceProfile.Digest}
	f, err := observationFacts(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if f.Identity.EngineID != engine || f.Identity.FSUUID != "01020304-0506-0708-090a-0b0c0d0e0f10" || f.Identity.GuestBootID != "10000000-0000-0000-0000-000000000001" || f.Identity.MountNamespace != "mnt:[55]" || f.Identity.Device != "8:7" || f.Identity.MountID != 73 {
		t.Fatalf("derived identity %+v", f.Identity)
	}
	if !bytes.Equal(f.Quota.Target, c.Physical.TargetQuota[:]) || !bytes.Equal(f.Quota.Default0, c.Physical.DefaultQuota[:]) || !bytes.Equal(f.Parent.RawFSXAttr, c.Physical.Parent.FSXAttr[:]) || !bytes.Equal(f.Diff.RawFSXAttr, c.Physical.Diff.FSXAttr[:]) || f.Quota.StateVersion != 1 || f.Quota.StateFlags != 0x30 {
		t.Fatal("raw facts changed")
	}
	c.Physical.TargetQuota[111]++
	c.Physical.Parent.FSXAttr[0]++
	if f.Quota.Target[111] != 0xa8 || reflect.DeepEqual(f.Parent.RawFSXAttr, c.Physical.Parent.FSXAttr[:]) {
		t.Fatal("facts alias input raw")
	}
}

func TestCheckerRejectsEveryUnavailableRequiredSampleAndRecheck(t *testing.T) {
	for _, physical := range []bool{false, true} {
		count := 7
		if physical {
			count = 33
		}
		for j := 0; j < count; j++ {
			t.Run(fmt.Sprintf("physical_%t_sample_%d", physical, j+1), func(t *testing.T) {
				tr, req, id, collection := observerFixture(t)
				var s *capacityprobe.CreatedLayerSample
				if physical {
					s = &collection.Physical.Samples[j]
				} else {
					s = &collection.Samples[j]
				}
				s.Outcome = "unavailable"
				c, err := New(tr)
				if err != nil {
					t.Fatal(err)
				}
				initialized, calls := 0, 0
				installObservation(c, collection, &initialized, &calls)
				if c.CheckCreatedLayer(context.Background(), req, id) == nil {
					t.Fatal("unavailable required read accepted")
				}
			})
		}
	}
	flags := map[string]func(*capacityprobe.CreatedLayerPhysical){
		"mount_info": func(p *capacityprobe.CreatedLayerPhysical) { p.MountInfoRechecked = false },
		"root_xfs":   func(p *capacityprobe.CreatedLayerPhysical) { p.RootXFSVerified = false },
		"same_mount": func(p *capacityprobe.CreatedLayerPhysical) { p.SameMountVerified = false },
		"boot":       func(p *capacityprobe.CreatedLayerPhysical) { p.BootIdentityRechecked = false },
		"namespace":  func(p *capacityprobe.CreatedLayerPhysical) { p.MountNamespaceRechecked = false },
	}
	for name, change := range flags {
		t.Run(name, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			change(collection.Physical)
			c, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(c, collection, &initialized, &calls)
			if c.CheckCreatedLayer(context.Background(), req, id) == nil {
				t.Fatal("missing recheck accepted")
			}
		})
	}
	for d := 0; d < 3; d++ {
		for flag := 0; flag < 4; flag++ {
			t.Run(fmt.Sprintf("directory_%d_flag_%d", d, flag), func(t *testing.T) {
				tr, req, id, collection := observerFixture(t)
				node := []*capacityprobe.CreatedLayerDirectory{&collection.Physical.Root, &collection.Physical.Parent, &collection.Physical.Diff}[d]
				switch flag {
				case 0:
					node.NoFollowWalk = false
				case 1:
					node.ReopenedNoFollow = false
				case 2:
					node.IdentityRechecked = false
				case 3:
					node.FilesystemUUIDSameFD = false
				}
				c, err := New(tr)
				if err != nil {
					t.Fatal(err)
				}
				initialized, calls := 0, 0
				installObservation(c, collection, &initialized, &calls)
				if c.CheckCreatedLayer(context.Background(), req, id) == nil {
					t.Fatal("incomplete directory accepted")
				}
			})
		}
	}
}
