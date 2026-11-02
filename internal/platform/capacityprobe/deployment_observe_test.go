package capacityprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These are synthetic raw procfs/nsfs facts; no native observation is executed.
func deploymentFixtureStatus(tid, tgid uint64) []byte {
	return []byte(fmt.Sprintf("Name:\tsecret-status-canary\nPid:\t%d\nTgid:\t%d\nUid:\t1000\t1001\t1002\t1003\nGid:\t2000\t2001\t2002\t2003\nCapInh:\t0000000000000001\nCapPrm:\t00000000000000A2\nCapEff:\t0000000000000003\nCapBnd:\t00000000000000FF\nCapAmb:\t0000000000000004\nNoNewPrivs:\t0\nSeccomp:\t2\n", tid, tgid))
}
func deploymentFixtureStat(tid uint64) []byte {
	return []byte(fmt.Sprintf("%d (worker (with) spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 99 20\n", tid))
}

type deploymentFixture struct {
	faultError      error
	namespaceErrors map[string]map[int]error
	next            int
	live            map[int]string
	owners          map[int]deploymentOwner
	closed          map[int]int
	operations      []string
	clocks          int
	nsCalls         int
	denied          uint32
	failAt          int
	failAfter       bool
	cancelAt        int
	cancel          context.CancelFunc
	mutate          func(string, int, any) any
	occurrences     map[string]int
	maximum         int
}

func newDeploymentFixture() *deploymentFixture {
	return &deploymentFixture{next: 10, live: map[int]string{}, owners: map[int]deploymentOwner{}, closed: map[int]int{}, occurrences: map[string]int{}}
}
func (f *deploymentFixture) op(name string, body func()) error {
	f.operations = append(f.operations, name)
	n := len(f.operations)
	f.occurrences[name]++
	if n == f.failAt && !f.failAfter {
		if f.faultError != nil {
			return f.faultError
		}
		return fmt.Errorf("secret-syscall-canary")
	}
	if n == f.cancelAt && !f.failAfter {
		f.cancel()
		return context.Canceled
	}
	body()
	if n == f.cancelAt {
		f.cancel()
	}
	if n == f.failAt {
		if f.faultError != nil {
			return f.faultError
		}
		return fmt.Errorf("secret-syscall-canary")
	}
	return nil
}
func (f *deploymentFixture) change(op string, v any) any {
	if f.mutate != nil {
		return f.mutate(op, f.occurrences[op], v)
	}
	return v
}
func (f *deploymentFixture) alloc(kind string, o deploymentOwner) int {
	f.next++
	fd := f.next
	f.live[fd] = kind
	f.owners[fd] = o
	if len(f.live) > f.maximum {
		f.maximum = len(f.live)
	}
	return fd
}
func (f *deploymentFixture) now(ctx context.Context) (v time.Time, e error) {
	e = f.op("now", func() {
		f.clocks++
		v = time.Date(2026, 10, 20, 1, 0, f.clocks, 0, time.UTC)
		v = f.change("now", v).(time.Time)
	})
	return
}
func (f *deploymentFixture) ids(ctx context.Context) (p, t uint64, e error) {
	e = f.op("ids", func() { p = 301; t = 307 })
	return
}
func (f *deploymentFixture) kernel(ctx context.Context) (v string, e error) {
	e = f.op("kernel", func() { v = f.change("kernel", "6.8.0-fixture").(string) })
	return
}
func (f *deploymentFixture) openGlobal(ctx context.Context, g deploymentGlobal) (fd int, e error) {
	fd = -1
	e = f.op(fmt.Sprintf("global:%d", g), func() { fd = f.alloc(fmt.Sprintf("global:%d", g), 0) })
	return
}
func (f *deploymentFixture) openProc(ctx context.Context, o deploymentOwner) (fd int, e error) {
	fd = -1
	e = f.op(fmt.Sprintf("proc:%d", o), func() { fd = f.alloc("proc", o) })
	return
}
func (f *deploymentFixture) openLeaf(ctx context.Context, parent int, l deploymentLeaf) (fd int, e error) {
	fd = -1
	e = f.op(fmt.Sprintf("leaf:%d:%d", f.owners[parent], l), func() { fd = f.alloc(fmt.Sprintf("leaf:%d", l), f.owners[parent]) })
	return
}
func (f *deploymentFixture) openNamespace(ctx context.Context, parent int, n deploymentNS) (fd int, e error) {
	fd = -1
	e = f.op(fmt.Sprintf("nsopen:%d:%d", f.owners[parent], n), func() { fd = f.alloc(fmt.Sprintf("ns:%d", n), f.owners[parent]) })
	return
}
func (f *deploymentFixture) namespaceStat(ctx context.Context, parent int, n deploymentNS) (v deploymentMetadata, e error) {
	name := fmt.Sprintf("nsstat:%d:%d", f.owners[parent], n)
	e = f.op(name, func() {
		f.nsCalls++
		v = deploymentMetadata{device: 4, inode: 1000 + uint64(f.owners[parent])*100 + uint64(n), mode: 0100444}
		v = f.change(name, v).(deploymentMetadata)
	})
	if e == nil && f.namespaceErrors[name] != nil {
		e = f.namespaceErrors[name][f.occurrences[name]]
	}
	if e == nil && f.denied != 0 {
		e = syscall.Errno(f.denied)
	}
	return
}
func (f *deploymentFixture) stat(ctx context.Context, fd int) (v deploymentMetadata, e error) {
	kind := f.live[fd]
	name := fmt.Sprintf("stat:%s:%d", kind, f.owners[fd])
	e = f.op(name, func() {
		v = deploymentMetadata{device: 3, inode: 200 + uint64(f.owners[fd]), mode: deploymentRegular | 0444}
		if kind == "proc" {
			v.mode = deploymentDirectory | 0555
		}
		var n int
		if _, err := fmt.Sscanf(kind, "ns:%d", &n); err == nil {
			v.device = 4
			v.inode = 1000 + uint64(f.owners[fd])*100 + uint64(n)
		}
		v = f.change(name, v).(deploymentMetadata)
	})
	return
}
func (f *deploymentFixture) statfs(ctx context.Context, fd int) (v uint64, e error) {
	name := "fs:" + f.live[fd]
	e = f.op(name, func() {
		v = deploymentProcFS
		if len(f.live[fd]) >= 3 && f.live[fd][:3] == "ns:" {
			v = deploymentNSFS
		}
		v = f.change(name, v).(uint64)
	})
	return
}
func (f *deploymentFixture) read(ctx context.Context, fd, limit int) (v []byte, e error) {
	kind := f.live[fd]
	name := fmt.Sprintf("read:%s:%d", kind, f.owners[fd])
	e = f.op(name, func() {
		tid, tgid := uint64(307), uint64(301)
		if f.owners[fd] == deploymentPID1 {
			tid = 1
			tgid = 1
		}
		switch kind {
		case "global:0":
			v = []byte("00112233-4455-6677-8899-aabbccddeeff\n")
		case "global:1":
			v = []byte("40\n")
		case "leaf:0":
			v = deploymentFixtureStatus(tid, tgid)
		case "leaf:1":
			v = deploymentFixtureStat(tid)
		case "leaf:2":
			v = []byte("0::/fixture\n")
		}
		v = f.change(name, v).([]byte)
	})
	return
}
func (f *deploymentFixture) close(fd int) { f.closed[fd]++; delete(f.live, fd) }

func TestDeploymentFixtureQualified(t *testing.T) {
	got, e := parseDeploymentStatus(deploymentFixtureStatus(307, 301))
	if e != nil || got.PID != 301 || got.TGID != 301 || got.TID != 307 || got.UIDs != [4]uint32{1000, 1001, 1002, 1003} || got.GIDs != [4]uint32{2000, 2001, 2002, 2003} || got.CapInh != "0000000000000001" || got.CapPrm != "00000000000000a2" || got.CapEff != "0000000000000003" || got.CapBnd != "00000000000000ff" || got.CapAmb != "0000000000000004" || got.NoNewPrivs != 0 || got.Seccomp != 2 {
		t.Fatalf("fixture status: %#v %v", got, e)
	}
	ticks, group, e := poolProcess(deploymentFixtureStat(307), []byte("0::/fixture\n"), 307)
	if e != nil || ticks != 99 || group != "/sys/fs/cgroup/fixture" {
		t.Fatalf("fixture stat/cgroup: %d %q %v", ticks, group, e)
	}
	pid1, e := parseDeploymentStatus(deploymentFixtureStatus(1, 1))
	if e != nil || pid1.PID != 1 || pid1.TID != 1 {
		t.Fatal(pid1, e)
	}
	f := newDeploymentFixture()
	if f.maximum != 0 || len(f.live) != 0 {
		t.Fatal("fixture starts with owned descriptors")
	}
}

func TestDeploymentFixturePrimitives(t *testing.T) {
	f := newDeploymentFixture()
	ctx := context.Background()
	start, e := f.now(ctx)
	if e != nil || start.IsZero() {
		t.Fatal(start, e)
	}
	end, e := f.now(ctx)
	if e != nil || !end.After(start) {
		t.Fatal(end, e)
	}
	p, tid, e := f.ids(ctx)
	if e != nil || p != 301 || tid != 307 {
		t.Fatal(p, tid, e)
	}
	for _, o := range []deploymentOwner{deploymentCaller, deploymentPID1} {
		fd, e := f.openProc(ctx, o)
		if e != nil {
			t.Fatal(e)
		}
		meta, e := f.stat(ctx, fd)
		fs, e2 := f.statfs(ctx, fd)
		if e != nil || e2 != nil || meta.device != 3 || meta.inode != 200+uint64(o) || meta.mode&0170000 != deploymentDirectory || fs != deploymentProcFS {
			t.Fatal(meta, fs, e, e2)
		}
		for n := deploymentUserNS; n <= deploymentCgroupNS; n++ {
			want, e := f.namespaceStat(ctx, fd, n)
			nfd, e2 := f.openNamespace(ctx, fd, n)
			got, e3 := f.stat(ctx, nfd)
			fs, e4 := f.statfs(ctx, nfd)
			if e != nil || e2 != nil || e3 != nil || e4 != nil || want != got || got.device != 4 || got.inode != 1000+uint64(o)*100+uint64(n) || fs != deploymentNSFS {
				t.Fatal(want, got, fs, e, e2, e3, e4)
			}
			f.close(nfd)
		}
		f.close(fd)
	}
	if f.maximum != 2 || len(f.live) != 0 {
		t.Fatal(f.maximum, f.live)
	}
}

func assertDeploymentComplete(t *testing.T, got DeploymentObservation, denied uint32) {
	t.Helper()
	if got.Schema != "switchback.capacity.deployment_diagnostic.v1" || got.Kind != "diagnostic_only" || got.Started != time.Date(2026, 10, 20, 1, 0, 1, 0, time.UTC) || got.Ended != time.Date(2026, 10, 20, 1, 0, 2, 0, time.UTC) || got.BootBefore != "00112233-4455-6677-8899-aabbccddeeff" || got.BootAfter != got.BootBefore || got.KernelRelease != "6.8.0-fixture" || got.CapLastCap != 40 {
		t.Fatalf("complete diagnostic: %#v", got)
	}
	for i, p := range []DeploymentProcessObservation{got.CallerBefore, got.CallerAfter, got.PID1Before, got.PID1After} {
		tid, tgid := uint64(307), uint64(301)
		if i >= 2 {
			tid = 1
			tgid = 1
		}
		want, e := parseDeploymentStatus(deploymentFixtureStatus(tid, tgid))
		if e != nil {
			t.Fatal(e)
		}
		want.StartTimeTicks = 99
		want.UnifiedCgroupPath = "/sys/fs/cgroup/fixture"
		if p != want {
			t.Fatalf("process %d: %#v want %#v", i, p, want)
		}
	}
	for i, n := range got.Namespaces {
		owners := [2]string{"caller_thread", "pid1"}
		kinds := [4]string{"user", "pid", "mnt", "cgroup"}
		want := DeploymentNamespaceValue{Device: "4", Inode: fmt.Sprint(1000 + i/4*100 + i%4), Class: "observed"}
		if denied != 0 {
			class := "EACCES"
			if denied == 1 {
				class = "EPERM"
			}
			want = DeploymentNamespaceValue{Errno: denied, Class: class}
		}
		if n.Owner != owners[i/4] || n.Kind != kinds[i%4] || n.Before != want || n.After != want {
			t.Fatalf("namespace %d: %#v want %#v", i, n, want)
		}
	}
}
func TestDeploymentComplete(t *testing.T) {
	f := newDeploymentFixture()
	got, e := observeDeployment(context.Background(), f)
	if e != nil || got.Schema != "switchback.capacity.deployment_diagnostic.v1" || got.Kind != "diagnostic_only" {
		t.Fatalf("complete diagnostic: %#v %v", got, e)
	}
	assertDeploymentComplete(t, got, 0)
	if f.nsCalls != 16 || f.maximum > 3 || len(f.live) != 0 {
		t.Fatal(f.nsCalls, f.maximum, f.live)
	}
}
func TestDeploymentStableDenied(t *testing.T) {
	for _, errno := range []uint32{13, 1} {
		t.Run(fmt.Sprint(errno), func(t *testing.T) {
			f := newDeploymentFixture()
			f.denied = errno
			got, e := observeDeployment(context.Background(), f)
			if e != nil {
				t.Fatalf("stable denied diagnostic: %#v %v", got, e)
			}
			assertDeploymentComplete(t, got, errno)
			if f.nsCalls != 16 || f.maximum > 3 || len(f.live) != 0 {
				t.Fatal(f.nsCalls, f.maximum, f.live)
			}
		})
	}
}

func deploymentAssertClosed(t *testing.T, f *deploymentFixture) {
	t.Helper()
	if len(f.live) != 0 || f.maximum > 3 {
		t.Fatalf("FD ownership: live=%v max=%d", f.live, f.maximum)
	}
	for fd := 11; fd <= f.next; fd++ {
		if f.closed[fd] != 1 {
			t.Fatalf("FD %d closed %d times", fd, f.closed[fd])
		}
	}
}
func deploymentAssertFailure(t *testing.T, f *deploymentFixture, want error) {
	t.Helper()
	got, e := observeDeployment(context.Background(), f)
	if got != (DeploymentObservation{}) || e == nil || (want != nil && e != want) {
		t.Fatalf("failure must be fixed and zero: %#v %v want %v", got, e, want)
	}
	if strings.Contains(e.Error(), "secret") {
		t.Fatal("raw error escaped")
	}
	deploymentAssertClosed(t, f)
}
func TestDeploymentStatusRequiredFields(t *testing.T) {
	good := string(deploymentFixtureStatus(307, 301))
	for _, key := range []string{"Pid", "Tgid", "Uid", "Gid", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs", "Seccomp"} {
		var line string
		for _, s := range strings.Split(good, "\n") {
			if strings.HasPrefix(s, key+":") {
				line = s + "\n"
			}
		}
		cases := map[string]string{"missing": strings.Replace(good, line, "", 1), "duplicate": good + line, "type": strings.Replace(good, line, key+":\tx\n", 1), "overflow": strings.Replace(good, line, key+":\t18446744073709551616\n", 1)}
		for label, raw := range cases {
			t.Run(key+"/"+label, func(t *testing.T) {
				p, e := parseDeploymentStatus([]byte(raw))
				if e != ErrDeploymentInvalid || p != (DeploymentProcessObservation{}) {
					t.Fatal(p, e)
				}
			})
		}
	}
	for _, key := range []string{"Uid", "Gid"} {
		for i := 0; i < 4; i++ {
			for _, bad := range []string{"4294967296", "-1", "01", "x"} {
				t.Run(fmt.Sprintf("%s-%d-%s", key, i, bad), func(t *testing.T) {
					tuple := []string{"1", "2", "3", "4"}
					tuple[i] = bad
					var lines []string
					for _, line := range strings.Split(good, "\n") {
						if strings.HasPrefix(line, key+":") {
							line = key + ":\t" + strings.Join(tuple, "\t")
						}
						lines = append(lines, line)
					}
					if _, e := parseDeploymentStatus([]byte(strings.Join(lines, "\n"))); e == nil {
						t.Fatal("tuple accepted")
					}
				})
			}
		}
	}
	for _, bad := range []string{"", strings.TrimSuffix(good, "\n"), good + "\x00\n", strings.Repeat("x", 65537), strings.Replace(good, "Pid:\t307", "Pid:\t0", 1), strings.Replace(good, "Tgid:\t301", "Tgid:\t0", 1), strings.Replace(good, "NoNewPrivs:\t0", "NoNewPrivs:\t2", 1), strings.Replace(good, "Seccomp:\t2", "Seccomp:\t3", 1)} {
		if _, e := parseDeploymentStatus([]byte(bad)); e == nil {
			t.Fatalf("status accepted: %q", bad[:min(len(bad), 100)])
		}
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		for _, cap := range []string{"000000000000001", "00000000000000001", "000000000000000g", "0000000000000001 extra"} {
			lines := strings.Split(good, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, key+":") {
					lines[i] = key + ":\t" + cap
				}
			}
			if _, e := parseDeploymentStatus([]byte(strings.Join(lines, "\n"))); e == nil {
				t.Fatal(key, cap)
			}
		}
	}
}
func TestDeploymentMalformedReadbacks(t *testing.T) {
	cases := []struct {
		name, op string
		which    int
		raw      []byte
		want     error
	}{
		{"boot-empty", "read:global:0:0", 1, []byte{}, ErrDeploymentInvalid},
		{"boot-zero", "read:global:0:0", 1, []byte("00000000-0000-0000-0000-000000000000\n"), ErrDeploymentInvalid},
		{"boot-malformed", "read:global:0:0", 1, []byte("00112233-4455-6677-8899-aabbccddeefg\n"), ErrDeploymentInvalid},
		{"boot-drift", "read:global:0:0", 2, []byte("10112233-4455-6677-8899-aabbccddeeff\n"), ErrDeploymentInvalid},
		{"boot-over", "read:global:0:0", 1, bytes.Repeat([]byte("x"), 65), ErrDeploymentInvalid},
		{"cap-empty", "read:global:1:0", 1, []byte{}, ErrDeploymentInvalid},
		{"cap-above63", "read:global:1:0", 1, []byte("64\n"), ErrDeploymentUnsupported},
		{"cap-max-uint", "read:global:1:0", 1, []byte("18446744073709551615\n"), ErrDeploymentUnsupported},
		{"cap-overflow", "read:global:1:0", 1, []byte("18446744073709551616\n"), ErrDeploymentInvalid},
		{"cap-negative", "read:global:1:0", 1, []byte("-1\n"), ErrDeploymentInvalid},
		{"cap-noncanonical", "read:global:1:0", 1, []byte("040\n"), ErrDeploymentInvalid},
		{"cap-over", "read:global:1:0", 1, bytes.Repeat([]byte("4"), 33), ErrDeploymentInvalid},
		{"cap-missing-newline", "read:global:1:0", 1, []byte("40"), ErrDeploymentInvalid},
		{"status-over", "read:leaf:0:0", 1, bytes.Repeat([]byte("x"), 65537), ErrDeploymentInvalid},
		{"stat-over", "read:leaf:1:0", 1, bytes.Repeat([]byte("x"), 4097), ErrDeploymentInvalid},
		{"group-over", "read:leaf:2:0", 1, bytes.Repeat([]byte("x"), 4097), ErrDeploymentInvalid},
		{"stat-zero-ticks", "read:leaf:1:0", 1, bytes.ReplaceAll(deploymentFixtureStat(307), []byte("99"), []byte("0")), ErrDeploymentInvalid},
		{"stat-wrong-thread", "read:leaf:1:0", 1, deploymentFixtureStat(301), ErrDeploymentInvalid},
		{"status-leader", "read:leaf:0:0", 1, deploymentFixtureStatus(301, 301), ErrDeploymentInvalid},
		{"status-wrong-tgid", "read:leaf:0:0", 1, deploymentFixtureStatus(307, 307), ErrDeploymentInvalid},
		{"pid1-wrong-tid", "read:leaf:0:1", 1, deploymentFixtureStatus(2, 1), ErrDeploymentInvalid},
		{"pid1-wrong-tgid", "read:leaf:0:1", 1, deploymentFixtureStatus(1, 2), ErrDeploymentInvalid},
		{"status-drift", "read:leaf:0:0", 2, bytes.ReplaceAll(deploymentFixtureStatus(307, 301), []byte("1000"), []byte("1009")), ErrDeploymentInvalid},
		{"canonical-status-replaced", "read:leaf:0:0", 3, deploymentFixtureStatus(308, 301), ErrDeploymentInvalid},
		{"group-invalid", "read:leaf:2:0", 1, []byte("1:cpu:/fixture\n"), ErrDeploymentInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == c.op && n == c.which {
					return c.raw
				}
				return v
			}
			deploymentAssertFailure(t, f, c.want)
		})
	}
	for _, kernel := range []string{"", "bad kernel", "bad\nsecret", strings.Repeat("x", 257), "bad\x00kernel", "../kernel"} {
		t.Run(fmt.Sprintf("kernel-%q", kernel), func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == "kernel" {
					return kernel
				}
				return v
			}
			deploymentAssertFailure(t, f, ErrDeploymentInvalid)
		})
	}
	for _, cap := range []string{"0\n", "63\n"} {
		t.Run("lastcap-"+strings.TrimSpace(cap), func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == "read:global:1:0" {
					return []byte(cap)
				}
				return v
			}
			got, e := observeDeployment(context.Background(), f)
			if e != nil || got.CapLastCap != map[string]uint32{"0\n": 0, "63\n": 63}[cap] {
				t.Fatal(got, e)
			}
			deploymentAssertClosed(t, f)
		})
	}
}
func TestDeploymentWallClock(t *testing.T) {
	start := time.Date(2026, 10, 20, 1, 0, 1, 0, time.UTC)
	for _, c := range []struct {
		name        string
		first, last time.Time
	}{{"zero-start", time.Time{}, start}, {"zero-end", start, time.Time{}}, {"equal", start, start}, {"backwards", start, start.Add(-time.Second)}} {
		t.Run(c.name, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == "now" {
					if n == 1 {
						return c.first
					}
					return c.last
				}
				return v
			}
			deploymentAssertFailure(t, f, ErrDeploymentInvalid)
		})
	}
	// Equal wall instants with and without the monotonic component are rejected.
	monotonic := time.Now()
	f := newDeploymentFixture()
	f.mutate = func(op string, n int, v any) any {
		if op == "now" {
			if n == 1 {
				return monotonic
			}
			return monotonic.Round(0)
		}
		return v
	}
	deploymentAssertFailure(t, f, ErrDeploymentInvalid)
	f = newDeploymentFixture()
	f.mutate = func(op string, n int, v any) any {
		if op == "now" {
			return monotonic.Add(time.Duration(n) * time.Second)
		}
		return v
	}
	got, e := observeDeployment(context.Background(), f)
	if e != nil || got.Started != got.Started.Round(0) || got.Ended != got.Ended.Round(0) {
		t.Fatal(got, e)
	}
	deploymentAssertClosed(t, f)
}
func TestDeploymentNamespaceFaults(t *testing.T) {
	for o := 0; o < 2; o++ {
		for n := 0; n < 4; n++ {
			op := fmt.Sprintf("nsstat:%d:%d", o, n)
			for _, field := range []string{"device", "inode", "zero-device", "zero-inode", "mode"} {
				t.Run(fmt.Sprintf("%s/%s", op, field), func(t *testing.T) {
					f := newDeploymentFixture()
					f.mutate = func(name string, count int, v any) any {
						if name == op && count == 2 {
							m := v.(deploymentMetadata)
							switch field {
							case "device":
								m.device++
							case "inode":
								m.inode++
							case "zero-device":
								m.device = 0
							case "zero-inode":
								m.inode = 0
							case "mode":
								m.mode = deploymentDirectory
							}
							return m
						}
						return v
					}
					deploymentAssertFailure(t, f, ErrDeploymentInvalid)
				})
			}
			for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM} {
				t.Run(fmt.Sprintf("%s/denied-%d", op, errno), func(t *testing.T) {
					f := newDeploymentFixture()
					f.namespaceErrors = map[string]map[int]error{op: {1: errno, 2: errno}}
					got, e := observeDeployment(context.Background(), f)
					if e != nil {
						t.Fatal(e)
					}
					index := o*4 + n
					if got.Namespaces[index].Before.Errno != uint32(errno) || got.Namespaces[index].After != got.Namespaces[index].Before {
						t.Fatal(got.Namespaces[index])
					}
					deploymentAssertClosed(t, f)
				})
				for _, which := range []int{1, 2} {
					t.Run(fmt.Sprintf("%s/permission-drift-%d-%d", op, errno, which), func(t *testing.T) {
						f := newDeploymentFixture()
						f.namespaceErrors = map[string]map[int]error{op: {which: errno}}
						deploymentAssertFailure(t, f, ErrDeploymentInvalid)
					})
				}
			}
			for _, errno := range []syscall.Errno{syscall.ENOENT, syscall.ESRCH, syscall.EIO} {
				t.Run(fmt.Sprintf("%s/fatal-%d", op, errno), func(t *testing.T) {
					f := newDeploymentFixture()
					f.namespaceErrors = map[string]map[int]error{op: {1: errno}}
					deploymentAssertFailure(t, f, ErrDeploymentUnavailable)
				})
			}
		}
	}
	f := newDeploymentFixture()
	f.namespaceErrors = map[string]map[int]error{"nsstat:0:0": {1: syscall.EACCES, 2: syscall.EPERM}}
	deploymentAssertFailure(t, f, ErrDeploymentInvalid)
	// Stat success followed by an open denial is fatal, never a denial DTO.
	trace := newDeploymentFixture()
	_, e := observeDeployment(context.Background(), trace)
	if e != nil {
		t.Fatal(e)
	}
	for i, op := range trace.operations {
		if strings.HasPrefix(op, "nsopen:") {
			for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM} {
				t.Run(fmt.Sprintf("open-denied-%d-%d", i, errno), func(t *testing.T) {
					f := newDeploymentFixture()
					f.failAt = i + 1
					f.faultError = errno
					deploymentAssertFailure(t, f, ErrDeploymentUnavailable)
				})
			}
		}
	}
}
func TestDeploymentMetadataProofs(t *testing.T) {
	for _, op := range []string{"stat:proc:0", "stat:proc:1", "stat:leaf:0:0", "stat:leaf:1:0", "stat:leaf:2:0", "stat:global:0:0", "stat:global:1:0", "stat:ns:0:0", "stat:ns:1:0", "stat:ns:2:0", "stat:ns:3:0"} {
		for _, field := range []string{"device", "inode", "mode"} {
			t.Run(op+"/"+field, func(t *testing.T) {
				f := newDeploymentFixture()
				f.mutate = func(name string, n int, v any) any {
					if name == op {
						m := v.(deploymentMetadata)
						switch field {
						case "device":
							m.device = 0
						case "inode":
							m.inode = 0
						case "mode":
							m.mode = 0120777
						}
						return m
					}
					return v
				}
				deploymentAssertFailure(t, f, ErrDeploymentInvalid)
			})
		}
	}
	for _, op := range []string{"fs:proc", "fs:leaf:0", "fs:leaf:1", "fs:leaf:2", "fs:global:0", "fs:global:1", "fs:ns:0", "fs:ns:1", "fs:ns:2", "fs:ns:3"} {
		t.Run(op, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(name string, n int, v any) any {
				if name == op {
					return uint64(0)
				}
				return v
			}
			deploymentAssertFailure(t, f, ErrDeploymentInvalid)
		})
	}
	for o := 0; o < 2; o++ {
		for _, field := range []string{"device", "inode", "mode"} {
			t.Run(fmt.Sprintf("canonical-%d-%s", o, field), func(t *testing.T) {
				f := newDeploymentFixture()
				op := fmt.Sprintf("stat:proc:%d", o)
				f.mutate = func(name string, n int, v any) any {
					if name == op && n == 2 {
						m := v.(deploymentMetadata)
						switch field {
						case "device":
							m.device++
						case "inode":
							m.inode++
						case "mode":
							m.mode++
						}
						return m
					}
					return v
				}
				deploymentAssertFailure(t, f, ErrDeploymentInvalid)
			})
		}
	}
	for _, field := range []string{"device", "inode"} {
		t.Run("ns-open-mismatch-"+field, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(name string, n int, v any) any {
				if name == "stat:ns:0:0" {
					m := v.(deploymentMetadata)
					if field == "device" {
						m.device++
					} else {
						m.inode++
					}
					return m
				}
				return v
			}
			deploymentAssertFailure(t, f, ErrDeploymentInvalid)
		})
	}
}
func TestDeploymentFaultCancellationPositions(t *testing.T) {
	baseline := newDeploymentFixture()
	_, e := observeDeployment(context.Background(), baseline)
	if e != nil {
		t.Fatal(e)
	}
	deploymentAssertClosed(t, baseline)
	for i, op := range baseline.operations {
		for _, after := range []bool{false, true} {
			for _, cancelled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%03d-%s-after%t-cancel%t", i+1, op, after, cancelled), func(t *testing.T) {
					f := newDeploymentFixture()
					f.failAfter = after
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					want := ErrDeploymentUnavailable
					if cancelled {
						f.cancelAt = i + 1
						f.cancel = cancel
						want = ErrDeploymentCancelled
					} else {
						f.failAt = i + 1
					}
					got, e := observeDeployment(ctx, f)
					if got != (DeploymentObservation{}) || e != want {
						t.Fatalf("zero/fixed error: %#v %v want %v", got, e, want)
					}
					if !reflect.DeepEqual(f.operations, baseline.operations[:i+1]) {
						t.Fatalf("operation after failure: %v want %v", f.operations, baseline.operations[:i+1])
					}
					deploymentAssertClosed(t, f)
				})
			}
		}
	}
	t.Logf("replayed %d primitive positions x fail/cancel x before/after", len(baseline.operations))
}
func TestDeploymentContextNoIO(t *testing.T) {
	f := newDeploymentFixture()
	got, e := observeDeployment(nil, f)
	if got != (DeploymentObservation{}) || e != ErrDeploymentInvalid || len(f.operations) != 0 {
		t.Fatal(got, e, f.operations)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, e = observeDeployment(ctx, f)
	if got != (DeploymentObservation{}) || e != ErrDeploymentCancelled || len(f.operations) != 0 {
		t.Fatal(got, e, f.operations)
	}
	got, e = observeDeployment(context.Background(), nil)
	if got != (DeploymentObservation{}) || e != ErrDeploymentInvalid {
		t.Fatal(got, e)
	}
}
func TestDeploymentPrivacy(t *testing.T) {
	f := newDeploymentFixture()
	got, e := observeDeployment(context.Background(), f)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(got)
	if e != nil || bytes.Contains(raw, []byte("secret")) {
		t.Fatal(string(raw), e)
	}
	f = newDeploymentFixture()
	f.failAt = 20
	got, e = observeDeployment(context.Background(), f)
	if got != (DeploymentObservation{}) || e != ErrDeploymentUnavailable || strings.Contains(e.Error(), "secret") {
		t.Fatal(got, e)
	}
	deploymentAssertClosed(t, f)
}
func TestDeploymentConcurrentObservations(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := newDeploymentFixture()
			if i%3 == 1 {
				f.denied = 13
			} else if i%3 == 2 {
				f.denied = 1
			}
			got, e := observeDeployment(context.Background(), f)
			if e != nil {
				t.Error(e)
				return
			}
			assertDeploymentComplete(t, got, f.denied)
			deploymentAssertClosed(t, f)
		}(i)
	}
	wg.Wait()
}
func TestDeploymentUnsupportedWrapper(t *testing.T) {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		return
	}
	got, e := ObserveDeployment(context.Background())
	if got != (DeploymentObservation{}) || e != ErrDeploymentUnsupported {
		t.Fatal(got, e)
	}
	got, e = ObserveDeployment(nil)
	if got != (DeploymentObservation{}) || e != ErrDeploymentInvalid {
		t.Fatal(got, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, e = ObserveDeployment(ctx)
	if got != (DeploymentObservation{}) || !errors.Is(e, ErrDeploymentCancelled) {
		t.Fatal(got, e)
	}
}
