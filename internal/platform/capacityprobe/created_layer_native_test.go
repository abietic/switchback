package capacityprobe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCreatedLayerPhysicalRejectsInvalidPathBeforeIO(t *testing.T) {
	for _, path := range []string{"", "/tmp/diff", Root + "/docker/overlay2/../diff", Root + "/docker/overlay2/a/b/diff"} {
		p, err := ReadCreatedLayerPhysical(context.Background(), path)
		if err == nil || (p != nil && p.ObservationComplete) {
			t.Fatal("unsafe path accepted")
		}
	}
}

// No process, real network, Engine, /mnt operation or Linux syscall is used by
// this recording backend. It runs the production sampling state machine only.
type recordingLayerKernel struct {
	t              *testing.T
	next           int
	live           map[int]string
	reopened       map[int]bool
	opens          map[string]int
	counts         map[string]int
	events         []string
	hook           func(string, int) error
	metadata       func(int, int, *createdLayerMetadata)
	geom           func(int, int, *[256]byte)
	attr           func(int, int, *[28]byte)
	quotaRaw       func(uint32, bool, []byte)
	table          func(int) []byte
	bootValue      func(int) [16]byte
	namespaceValue func(int) uint64
	fdinfo         []byte
}

const nativeUpper = Root + "/docker/overlay2/actual-driver-17/diff"
const nativeCID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const nativeMount = "1 1 0:1 / / rw - rootfs rootfs rw\n73 1 8:7 / /mnt/lima-sbquota rw - xfs /dev/mapper/opaque rw\n"

func newRecordingLayer(t *testing.T) *recordingLayerKernel {
	return &recordingLayerKernel{t: t, next: 10, live: map[int]string{}, reopened: map[int]bool{}, opens: map[string]int{}, counts: map[string]int{}}
}
func (k *recordingLayerKernel) touch(op string, fd int) (int, error) {
	start := time.Now().UTC()
	for !time.Now().UTC().After(start) {
	}
	k.events = append(k.events, op)
	key := fmt.Sprintf("%s:%d", op, fd)
	k.counts[key]++
	if k.hook != nil {
		return k.counts[key], k.hook(op, fd)
	}
	return k.counts[key], nil
}
func (k *recordingLayerKernel) boot(context.Context) (out [16]byte, e error) {
	n, err := k.touch("boot", -1)
	e = err
	out[0] = 1
	if k.bootValue != nil {
		out = k.bootValue(n)
	}
	return
}
func (k *recordingLayerKernel) namespace(context.Context) (uint64, error) {
	n, e := k.touch("namespace", -1)
	if k.namespaceValue != nil {
		return k.namespaceValue(n), e
	}
	return 42, e
}
func (k *recordingLayerKernel) mountinfo(context.Context) ([]byte, error) {
	n, e := k.touch("mountinfo", -1)
	if k.table != nil {
		return k.table(n), e
	}
	return []byte(nativeMount), e
}
func (k *recordingLayerKernel) open(_ context.Context, p string) (int, error) {
	_, e := k.touch("open", -1)
	if e != nil {
		return -1, e
	}
	fd := k.next
	k.next++
	k.live[fd] = p
	k.opens[p]++
	k.reopened[fd] = k.opens[p] > 1
	return fd, nil
}
func (k *recordingLayerKernel) close(fd int) {
	if _, ok := k.live[fd]; !ok {
		k.t.Fatalf("double/unknown close %d", fd)
	}
	delete(k.live, fd)
}
func (k *recordingLayerKernel) stat(_ context.Context, fd int) (m createdLayerMetadata, e error) {
	n, e := k.touch("stat", fd)
	p, ok := k.live[fd]
	if !ok {
		k.t.Fatal("stat on closed FD")
	}
	ino := uint64(5)
	if p != Root {
		ino = 6
	}
	if p == nativeUpper {
		ino = 7
	}
	m = createdLayerMetadata{Inode: ino, Major: 8, Minor: 7, Mode: 0040755, MountID: 73, Nlink: 2, CtimeSec: 10, FSType: 0x58465342}
	if k.fdinfo != nil {
		m.MountID, e = createdLayerFDInfo(k.fdinfo)
	}
	if k.metadata != nil {
		k.metadata(fd, n, &m)
	}
	return m, e
}
func (k *recordingLayerKernel) geometry(_ context.Context, fd int, out *[256]byte) error {
	n, e := k.touch("geometry", fd)
	binary.LittleEndian.PutUint32(out[0:4], 4096)
	binary.LittleEndian.PutUint32(out[12:16], 4)
	binary.LittleEndian.PutUint64(out[32:40], 100000)
	binary.LittleEndian.PutUint32(out[88:92], 5)
	copy(out[64:80], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	if k.geom != nil {
		k.geom(fd, n, out)
	}
	return e
}
func (k *recordingLayerKernel) attrs(_ context.Context, fd int, out *[28]byte) error {
	n, e := k.touch("attrs", fd)
	id := uint32(731)
	if k.live[fd] == Root {
		id = 0
	}
	binary.LittleEndian.PutUint32(out[12:16], id)
	binary.LittleEndian.PutUint32(out[0:4], 0x200)
	if k.attr != nil {
		k.attr(fd, n, out)
	}
	return e
}
func (k *recordingLayerKernel) quota(_ context.Context, fd int, id uint32, status bool, out []byte) error {
	op := "quota"
	if status {
		op = "qstatv"
		if len(out) != 160 || out[0] != 1 || id != 0 {
			k.t.Fatal("QSTATV full size/input version")
		}
	} else {
		if len(out) != 112 {
			k.t.Fatal("quota full size")
		}
	}
	if k.live[fd] != path.Dir(nativeUpper) {
		k.t.Fatal("quota not on held parent")
	}
	_, e := k.touch(op, fd)
	clear(out)
	out[0] = 1
	if !status {
		out[1] = 2
		binary.LittleEndian.PutUint32(out[4:8], id)
	}
	if k.quotaRaw != nil {
		k.quotaRaw(id, status, out)
	}
	return e
}
func nativeCollection(t *testing.T, k *recordingLayerKernel, ctx context.Context) CreatedLayerCollection {
	t.Helper()
	return CollectCreatedLayer(ctx, CreatedLayerTarget{nativeCID}, CreatedLayerSources{
		Get: func(_ context.Context, p string) ([]byte, error) {
			start := time.Now().UTC()
			for !time.Now().UTC().After(start) {
			}
			switch p {
			case "/v1.56/version":
				return []byte(`{"ApiVersion":"1.56","Version":"29.9.0"}`), nil
			case "/v1.56/info":
				return []byte(`{"ID":"12345678-1234-5678-9abc-123456789abc","ServerVersion":"29.9.0","DockerRootDir":"/mnt/lima-sbquota/docker","Driver":"overlay2"}`), nil
			case "/v1.56/containers/" + nativeCID + "/json":
				return []byte(`{"Id":"` + nativeCID + `","State":{"Status":"created","Pid":0,"Running":false,"Paused":false,"Restarting":false,"Dead":false,"StartedAt":"0001-01-01T00:00:00Z"},"GraphDriver":{"Name":"overlay2","Data":{"UpperDir":"` + nativeUpper + `"}}}`), nil
			default:
				t.Fatalf("unexpected GET %q", p)
				return nil, nil
			}
		},
		Physical: func(ctx context.Context, p string) (*CreatedLayerPhysical, error) {
			return readCreatedLayerPhysical(ctx, p, k)
		},
	})
}
func assertNativeClosed(t *testing.T, k *recordingLayerKernel) {
	t.Helper()
	if len(k.live) != 0 {
		t.Fatalf("FD leak: %+v", k.live)
	}
}
func TestCreatedLayerPhysicalFiniteSequenceAndRawZeroOff(t *testing.T) {
	k := newRecordingLayer(t)
	c := nativeCollection(t, k, context.Background())
	assertNativeClosed(t, k)
	if !c.ObservationComplete || !c.Physical.ObservationComplete || c.CapacityReady || c.ExecutionEnabled || c.Status != "preparation_blocked" || c.Trust != "none" {
		t.Fatalf("finite diagnostic %+v", c)
	}
	p := c.Physical
	if p.ProjectID != 731 || p.QStatV[0] != 1 || p.QStatV[2] != 0 || p.TargetQuota[8] != 0 || p.DefaultQuota[8] != 0 || p.Root.Before.XFSUUID[0] != 1 || p.Root.Before.XFSUUID[15] != 16 {
		t.Fatal("raw zero/off or UUID bytes altered")
	}
	var got []string
	for _, s := range p.Samples {
		got = append(got, s.Operation)
		if !createdLayerPhysicalOperation(s.Operation) || s.Outcome != "observed" || s.Errno != nil {
			t.Fatalf("sample %+v", s)
		}
	}
	want := []string{"boot_id_before", "mount_namespace_before", "mountinfo_before", "root_open", "root_stat", "root_geometry", "root_fsxattr", "parent_open", "parent_stat", "parent_geometry", "parent_fsxattr", "diff_open", "diff_stat", "diff_geometry", "diff_fsxattr", "qstatv", "target_quota", "default0_quota", "boot_id_after", "mount_namespace_after", "mountinfo_after", "root_stat", "root_geometry", "root_fsxattr", "root_reopen", "parent_stat", "parent_geometry", "parent_fsxattr", "parent_reopen", "diff_stat", "diff_geometry", "diff_fsxattr", "diff_reopen"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sample order %v", got)
	}
}

func TestCreatedLayerPhysicalRejectsRealtimeInheritance(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		parentFlags, diffFlags uint32
	}{
		{"parent-only", 0x300, 0x200},
		{"diff-only", 0x200, 0x300},
		{"both", 0x300, 0x300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newRecordingLayer(t)
			var wantParent, wantDiff [28]byte
			k.attr = func(fd int, _ int, a *[28]byte) {
				switch k.live[fd] {
				case path.Dir(nativeUpper):
					binary.LittleEndian.PutUint32(a[0:4], tc.parentFlags)
					wantParent = *a
				case nativeUpper:
					binary.LittleEndian.PutUint32(a[0:4], tc.diffFlags)
					wantDiff = *a
				}
			}
			// The recording backend leaves all realtime quota fields zero. That
			// cannot override RTINHERIT's policy for newly created children.
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.Physical == nil {
				t.Fatal("partial physical observation discarded")
			}
			p := c.Physical
			if p.Parent.FSXAttr != wantParent || p.Diff.FSXAttr != wantDiff || p.ProjectID != 731 {
				t.Error("actual parent/diff raw attrs or dynamic project discarded")
			}
			if c.ObservationComplete || p.ObservationComplete {
				t.Errorf("realtime inheritance accepted: parent=%#x diff=%#x outer_complete=%v physical_complete=%v", tc.parentFlags, tc.diffFlags, c.ObservationComplete, p.ObservationComplete)
			}
			if c.Status != "preparation_blocked" || c.Trust != "none" || c.ExecutionEnabled || c.CapacityReady || len(c.Blockers) == 0 {
				t.Error("realtime inheritance gained authority")
			}
			last := p.Samples[len(p.Samples)-1]
			if last.Operation != "diff_fsxattr" || last.Outcome != "unavailable" || last.Errno != nil {
				t.Errorf("missing unavailable attribute sample: %+v", last)
			}
			physicalUnavailable := false
			for _, s := range c.Samples {
				if s.Operation == "physical" && s.Outcome == "unavailable" {
					physicalUnavailable = true
				}
			}
			if !physicalUnavailable {
				t.Error("outer physical sample was not unavailable")
			}
			for _, q := range [][112]byte{p.TargetQuota, p.DefaultQuota} {
				for _, b := range q[72:104] {
					if b != 0 {
						t.Fatal("realtime quota fixture is not zero")
					}
				}
			}
			for _, op := range k.events {
				if op == "qstatv" || op == "quota" {
					t.Error("continued quota reads after realtime inheritance rejection")
				}
			}
		})
	}
}

func TestCreatedLayerPhysicalEveryOperationFailureAndCancellation(t *testing.T) {
	base := newRecordingLayer(t)
	if !nativeCollection(t, base, context.Background()).ObservationComplete {
		t.Fatal("baseline")
	}
	total := len(base.events)
	for i := 1; i <= total; i++ {
		t.Run(fmt.Sprintf("errno-%d", i), func(t *testing.T) {
			k := newRecordingLayer(t)
			k.hook = func(_ string, _ int) error {
				if len(k.events) == i {
					return fmt.Errorf("SECRET-CANARY: %w", syscall.EPERM)
				}
				return nil
			}
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.ObservationComplete || len(k.events) != i {
				t.Fatalf("continued after operation failure %d %d", i, len(k.events))
			}
			raw, _ := json.Marshal(c)
			if strings.Contains(string(raw), "SECRET-CANARY") {
				t.Fatal("error leak")
			}
			found := false
			for _, s := range c.Physical.Samples {
				if s.Errno != nil && *s.Errno == uint64(syscall.EPERM) {
					found = true
				}
			}
			if !found {
				t.Fatal("actual errno lost")
			}
		})
		t.Run(fmt.Sprintf("cancel-%d", i), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			k := newRecordingLayer(t)
			k.hook = func(_ string, _ int) error {
				if len(k.events) == i {
					cancel()
				}
				return nil
			}
			c := nativeCollection(t, k, ctx)
			assertNativeClosed(t, k)
			if c.ObservationComplete || len(k.events) != i {
				t.Fatalf("post-cancel read %d %d", i, len(k.events))
			}
		})
	}
}
func TestCreatedLayerPhysicalDriftAndInvalidIdentity(t *testing.T) {
	cases := map[string]func(*recordingLayerKernel){
		"wrong-fs": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.FSType = 0xef53 }
		},
		"wrong-device": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.Minor = 8 }
		},
		"wrong-mount": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.MountID = 74 }
		},
		"non-directory": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.Mode = 0100755 }
		},
		"owner": func(k *recordingLayerKernel) { k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.UID = 1 } },
		"group": func(k *recordingLayerKernel) { k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.GID = 1 } },
		"writable": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.Mode |= 0020 }
		},
		"special": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.Mode |= 04000 }
		},
		"zero-inode": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.Inode = 0 }
		},
		"zero-mount": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, _ int, m *createdLayerMetadata) { m.MountID = 0 }
		},
		"held-inode": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, n int, m *createdLayerMetadata) {
				if n == 2 {
					m.Inode++
				}
			}
		},
		"held-nlink": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, n int, m *createdLayerMetadata) {
				if n == 2 {
					m.Nlink++
				}
			}
		},
		"held-ctime": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, n int, m *createdLayerMetadata) {
				if n == 2 {
					m.CtimeNsec++
				}
			}
		},
		"held-mode": func(k *recordingLayerKernel) {
			k.metadata = func(_ int, n int, m *createdLayerMetadata) {
				if n == 2 {
					m.Mode ^= 0100
				}
			}
		},
		"reopened-path-replacement-held-valid": func(k *recordingLayerKernel) {
			k.metadata = func(fd int, _ int, m *createdLayerMetadata) {
				if k.reopened[fd] {
					m.Inode++
				}
			}
		},
		"reopened-mount": func(k *recordingLayerKernel) {
			k.metadata = func(fd int, _ int, m *createdLayerMetadata) {
				if k.reopened[fd] {
					m.MountID++
				}
			}
		},
		"reopened-ctime": func(k *recordingLayerKernel) {
			k.metadata = func(fd int, _ int, m *createdLayerMetadata) {
				if k.reopened[fd] {
					m.CtimeSec++
				}
			}
		},
		"uuid-zero":         func(k *recordingLayerKernel) { k.geom = func(_ int, _ int, g *[256]byte) { clear(g[64:80]) } },
		"geometry-version":  func(k *recordingLayerKernel) { k.geom = func(_ int, _ int, g *[256]byte) { g[88] = 4 } },
		"geometry-reserved": func(k *recordingLayerKernel) { k.geom = func(_ int, _ int, g *[256]byte) { g[255] = 1 } },
		"geometry-held-drift": func(k *recordingLayerKernel) {
			k.geom = func(_ int, n int, g *[256]byte) {
				if n == 2 {
					g[64]++
				}
			}
		},
		"geometry-reopen-drift": func(k *recordingLayerKernel) {
			k.geom = func(fd int, _ int, g *[256]byte) {
				if k.reopened[fd] {
					g[64]++
				}
			}
		},
		"geometry-parent-mismatch": func(k *recordingLayerKernel) {
			k.geom = func(fd int, _ int, g *[256]byte) {
				if k.live[fd] != Root {
					g[64]++
				}
			}
		},
		"attrs-held-drift": func(k *recordingLayerKernel) {
			k.attr = func(_ int, n int, a *[28]byte) {
				if n == 2 {
					a[16]++
				}
			}
		},
		"attrs-reopen-drift": func(k *recordingLayerKernel) {
			k.attr = func(fd int, _ int, a *[28]byte) {
				if k.reopened[fd] {
					a[16]++
				}
			}
		},
		"target-parent-mismatch": func(k *recordingLayerKernel) {
			k.attr = func(fd int, _ int, a *[28]byte) {
				if k.live[fd] == nativeUpper {
					a[12]++
				}
			}
		},
		"realtime-attrs": func(k *recordingLayerKernel) { k.attr = func(_ int, _ int, a *[28]byte) { a[0] |= 1 } },
		"qstat-version": func(k *recordingLayerKernel) {
			k.quotaRaw = func(_ uint32, s bool, b []byte) {
				if s {
					b[0] = 2
					b[159] = 9
				}
			}
		},
		"quota-version": func(k *recordingLayerKernel) {
			k.quotaRaw = func(_ uint32, s bool, b []byte) {
				if !s {
					b[0] = 2
				}
			}
		},
		"quota-type": func(k *recordingLayerKernel) {
			k.quotaRaw = func(_ uint32, s bool, b []byte) {
				if !s {
					b[1] = 1
				}
			}
		},
		"quota-id": func(k *recordingLayerKernel) {
			k.quotaRaw = func(_ uint32, s bool, b []byte) {
				if !s {
					b[4]++
				}
			}
		},
		"quota-realtime": func(k *recordingLayerKernel) {
			k.quotaRaw = func(_ uint32, s bool, b []byte) {
				if !s {
					b[72] = 1
				}
			}
		},
	}
	for _, id := range []uint32{0, 100, 101, 999} {
		id := id
		cases[fmt.Sprintf("static-project-%d", id)] = func(k *recordingLayerKernel) {
			k.attr = func(fd int, _ int, a *[28]byte) {
				if k.live[fd] != Root {
					binary.LittleEndian.PutUint32(a[12:16], id)
				}
			}
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			k := newRecordingLayer(t)
			setup(k)
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.ObservationComplete || c.Physical == nil || c.Physical.ObservationComplete {
				t.Fatal("invalid/drifted observation accepted")
			}
			if name == "qstat-version" && c.Physical.QStatV[159] != 9 {
				t.Fatal("raw rejected QSTATV discarded")
			}
			if name == "geometry-held-drift" && c.Physical.Root.GeometryAfter[64] != 2 {
				t.Fatal("raw after geometry discarded")
			}
		})
	}
}
func TestCreatedLayerPhysicalMountAndFDInfoRejects(t *testing.T) {
	tables := map[string]string{
		"missing":        "1 1 0:1 / / rw - rootfs rootfs rw\n",
		"bind-root":      strings.Replace(nativeMount, "8:7 / /mnt", "8:7 /sub /mnt", 1),
		"shadow":         nativeMount + "74 73 8:7 / /mnt/lima-sbquota/docker rw - xfs /dev/other rw\n",
		"duplicate":      nativeMount + "73 1 8:7 / /mnt/lima-sbquota rw - xfs /dev/other rw\n",
		"duplicate-path": nativeMount + "74 1 8:7 / /mnt/lima-sbquota rw - xfs /dev/other rw\n",
		"malformed":      "bad\n", "unsupported-escape": strings.Replace(nativeMount, "/dev/mapper/opaque", "/dev/mapper/a\\777", 1),
		"oversized": strings.Repeat("x", MaxResponse+1), "wrong-type": strings.Replace(nativeMount, " - xfs ", " - ext4 ", 1),
	}
	for name, table := range tables {
		t.Run(name, func(t *testing.T) {
			k := newRecordingLayer(t)
			k.table = func(int) []byte { return []byte(table) }
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.ObservationComplete {
				t.Fatal("bad mount accepted")
			}
			if k.next != 10 {
				t.Fatal("opened before mount validation")
			}
		})
	}
	for name, raw := range map[string]string{"missing": "pos:\t0\n", "duplicate": "mnt_id:\t73\nmnt_id:\t73\n", "bad": "mnt_id:\tbad\n", "zero": "mnt_id:\t0\n", "oversize": strings.Repeat("x", 4097), "mismatch": "mnt_id:\t74\n"} {
		t.Run("fdinfo-"+name, func(t *testing.T) {
			k := newRecordingLayer(t)
			k.fdinfo = []byte(raw)
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.ObservationComplete {
				t.Fatal("bad fdinfo accepted")
			}
		})
	}
	t.Run("valid-fdinfo", func(t *testing.T) {
		k := newRecordingLayer(t)
		k.fdinfo = []byte("pos:\t0\nflags:\t02300000\nmnt_id:\t73\nino:\t5\n")
		if !nativeCollection(t, k, context.Background()).ObservationComplete {
			t.Fatal("actual fdinfo syntax rejected")
		}
		assertNativeClosed(t, k)
	})
	t.Run("mountinfo-after-drift", func(t *testing.T) {
		k := newRecordingLayer(t)
		k.table = func(n int) []byte {
			if n == 2 {
				return []byte(strings.Replace(nativeMount, "rw - rootfs", "ro - rootfs", 1))
			}
			return []byte(nativeMount)
		}
		c := nativeCollection(t, k, context.Background())
		assertNativeClosed(t, k)
		if c.ObservationComplete || c.Physical.MountInfoSHA256Before == c.Physical.MountInfoSHA256After {
			t.Fatal("mountinfo drift lost")
		}
	})
}
func TestCreatedLayerPhysicalQuotaENOENTIsNotDefaultZero(t *testing.T) {
	k := newRecordingLayer(t)
	k.hook = func(op string, fd int) error {
		if op == "quota" && k.counts[fmt.Sprintf("%s:%d", op, fd)] == 2 {
			return syscall.ENOENT
		}
		return nil
	}
	c := nativeCollection(t, k, context.Background())
	assertNativeClosed(t, k)
	if c.ObservationComplete || c.Physical == nil || c.Physical.DefaultQuota[0] != 1 {
		t.Fatal("partial read discarded or admitted")
	}
	s := c.Physical.Samples[len(c.Physical.Samples)-1]
	if s.Operation != "default0_quota" || s.Outcome != "unavailable" || s.Errno == nil || *s.Errno != uint64(syscall.ENOENT) {
		t.Fatalf("ENOENT became default0 facts %+v", s)
	}
}

func TestCreatedLayerPhysicalBootNamespaceAndNoIOFailures(t *testing.T) {
	for _, ctx := range []context.Context{nil, func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }()} {
		k := newRecordingLayer(t)
		p, e := readCreatedLayerPhysical(ctx, nativeUpper, k)
		if e == nil || p.ObservationComplete || len(k.events) != 0 {
			t.Fatal("invalid context read")
		}
	}
	for _, p := range []string{"/tmp/diff", Root + "/docker/overlay2/a/../b/diff"} {
		k := newRecordingLayer(t)
		_, e := readCreatedLayerPhysical(context.Background(), p, k)
		if e == nil || len(k.events) != 0 {
			t.Fatal("path prevalidation failed")
		}
	}
	for _, name := range []string{"boot-zero", "boot-after-drift", "namespace-zero", "namespace-after-drift"} {
		t.Run(name, func(t *testing.T) {
			k := newRecordingLayer(t)
			switch name {
			case "boot-zero":
				k.bootValue = func(int) [16]byte { return [16]byte{} }
			case "boot-after-drift":
				k.bootValue = func(n int) [16]byte { return [16]byte{byte(n)} }
			case "namespace-zero":
				k.namespaceValue = func(int) uint64 { return 0 }
			case "namespace-after-drift":
				k.namespaceValue = func(n int) uint64 { return uint64(n) }
			}
			c := nativeCollection(t, k, context.Background())
			assertNativeClosed(t, k)
			if c.ObservationComplete || c.Physical == nil {
				t.Fatal("identity drift accepted")
			}
		})
	}
	t.Run("flags-at-offset-two-and-full-raw", func(t *testing.T) {
		k := newRecordingLayer(t)
		k.quotaRaw = func(id uint32, status bool, b []byte) {
			if status {
				b[2] = 0x30
				b[159] = 0xa7
			} else {
				b[111] = 0xa8
			}
		}
		c := nativeCollection(t, k, context.Background())
		assertNativeClosed(t, k)
		if !c.ObservationComplete || c.Physical.QStatV[2] != 0x30 || c.Physical.QStatV[159] != 0xa7 || c.Physical.TargetQuota[111] != 0xa8 || c.Physical.DefaultQuota[111] != 0xa8 {
			t.Fatal("full ABI bytes or flags altered")
		}
	})
}
