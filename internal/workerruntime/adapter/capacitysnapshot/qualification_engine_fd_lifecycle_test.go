package capacitysnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestQualificationEngineFDTuple(t *testing.T) {
	tests := []struct {
		raw                 string
		valid               bool
		flags, mount, inode uint64
	}{
		{"pos:\t0\nflags:\t02000002\nmnt_id:\t0\nino:\t123\n", true, 02000002, 0, 123},
		{"flags:\t02\nmnt_id:\t1\nino:\t123\nflags:\t02\n", false, 0, 0, 0},
		{"flags:\t02\nmnt_id:\t1\n", false, 0, 0, 0},
		{"flags:\t08\nmnt_id:\t1\nino:\t2\n", false, 0, 0, 0},
		{"flags:\t40000000000\nmnt_id:\t1\nino:\t2\n", false, 0, 0, 0},
	}
	for _, tc := range tests {
		got, err := qualificationEngineDecodeFDTuple([]byte(tc.raw))
		if (err == nil) != tc.valid {
			t.Errorf("%q: %v", tc.raw, err)
			continue
		}
		if tc.valid && (got.flags != tc.flags || got.mount != tc.mount || got.inode != tc.inode) {
			t.Errorf("%q: %+v", tc.raw, got)
		}
	}
}

type engineFDCase struct {
	ID             string                      `json:"id"`
	FDInfoAfter    string                      `json:"fdinfo_after"`
	FDInfo         string                      `json:"fdinfo"`
	PinInfo        string                      `json:"pin_fdinfo"`
	Pin            qualificationEngineFDObject `json:"pin"`
	Before         qualificationEngineFDObject `json:"source_before"`
	After          qualificationEngineFDObject `json:"source_after"`
	PrimitiveError *struct {
		Primitive string `json:"primitive"`
		Errno     int    `json:"errno"`
	} `json:"primitive_error"`
	Expected string `json:"expected"`
}

func engineFDCases(t *testing.T) []engineFDCase {
	t.Helper()
	raw, e := os.ReadFile("../../../../deploy/experience/engine/testdata/fd-lifecycle-corpus.json")
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "4b7d3b1324226bd67f653a7a222f896d3c24bcd2457c26cd43f867bb3b53dae4" {
		t.Fatal("corpus bytes changed")
	}
	var c struct {
		Schema string         `json:"schema"`
		Cases  []engineFDCase `json:"cases"`
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	if c.Schema != "switchback.engine.fd_lifecycle.corpus.v1" || len(c.Cases) != 23 {
		t.Fatal("corpus shape")
	}
	return c.Cases
}

type engineFDFixture struct {
	c                        engineFDCase
	link, stat, info, closes int
	hook                     func(string) error
	afterInfo                string
}

func (f *engineFDFixture) err(op string) error {
	if f.hook != nil {
		if e := f.hook(op); e != nil {
			return e
		}
	}
	if f.c.PrimitiveError != nil && f.c.PrimitiveError.Primitive == op {
		e := syscall.Errno(f.c.PrimitiveError.Errno)
		if op == "pin-open" || len(op) >= 7 && op[:7] == "source-" {
			return qualificationEngineSourceError(op, e)
		}
		return e
	}
	return nil
}
func (f *engineFDFixture) ops() qualificationEngineFDOps {
	return qualificationEngineFDOps{
		openPin: func(context.Context, string) (int, error) { return 77, f.err("pin-open") },
		pinStat: func(context.Context, int) (qualificationEngineFDObject, error) { return f.c.Pin, f.err("pin-fstat") },
		pinLink: func(context.Context, int) (string, error) { return f.c.Pin.Target, f.err("pin-readlink") },
		pinInfo: func(context.Context, int) ([]byte, error) { return []byte(f.c.PinInfo), f.err("pin-fdinfo") },
		sourceLink: func(context.Context, string) (string, error) {
			f.link++
			o := f.c.Before
			op := "source-readlink-before"
			if f.link == 2 {
				o = f.c.After
				op = "source-readlink-after"
			}
			return o.Target, f.err(op)
		},
		sourceStat: func(context.Context, string) (qualificationEngineFDObject, error) {
			f.stat++
			o := f.c.Before
			op := "source-stat-before"
			if f.stat == 2 {
				o = f.c.After
				op = "source-stat-after"
			}
			return o, f.err(op)
		},
		sourceInfo: func(context.Context, string) ([]byte, error) {
			f.info++
			raw := f.c.FDInfo
			if f.info == 2 && f.c.FDInfoAfter != "" {
				raw = f.c.FDInfoAfter
			}
			if f.info == 2 && f.afterInfo != "" {
				raw = f.afterInfo
			}
			return []byte(raw), f.err("source-fdinfo-open")
		},
		closePin: func(context.Context, int) error { f.closes++; return f.err("pin-close") },
	}
}
func TestQualificationEngineFDCorpus23(t *testing.T) {
	counts := map[string]int{}
	for _, c := range engineFDCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			f := &engineFDFixture{c: c}
			got, e := qualificationEngineSampleFD(context.Background(), 1, 8, f.ops())
			kind := "stable"
			if e != nil {
				kind = "hard_failure"
				if _, ok := e.(*qualificationEngineFDRace); ok {
					kind = "sampling_race"
				}
			}
			if kind != c.Expected {
				t.Fatalf("got %s %v want %s", kind, e, c.Expected)
			}
			if e == nil {
				tuple, _ := qualificationEngineDecodeFDTuple([]byte(c.FDInfo))
				if got.Flags != tuple.flags || got.Mount != tuple.mount {
					t.Fatal("source flags/mount lost")
				}
			}
			wantClose := 1
			if c.PrimitiveError != nil && c.PrimitiveError.Primitive == "pin-open" {
				wantClose = 0
			}
			if f.closes != wantClose {
				t.Fatal("pin cleanup", f.closes)
			}
			counts[kind]++
		})
	}
	t.Logf("corpus23 outcomes: %+v", counts)
}

// Captured metadata /proc/1/stat sha256:91bbfe0aa90a3dbf53b03166b30f41157deac648b78b63ba24b11bde18d4172d; no live kernel claim.
const engineCapturedPID1Stat = "1 (systemd) S 0 1 1 0 -1 4194560 1345799 26659359 94 1404 18744 12731 55107 46230 20 0 1 0 0 23179264 3296 18446744073709551615 189414581927936 189414582012388 281474754235968 0 0 0 671173123 4096 1260 1 0 0 17 2 0 0 0 0 0 189414582120352 189414582124656 189415117893632 281474754240281 281474754240332 281474754240332 281474754240479 0\n"

// Captured metadata /proc/1/fdinfo/8 sha256:01316418d32e65bb4ae081740485a6ca3b78137c745b9d2533768890badaef08; no live kernel claim.
const engineCapturedFD8 = "pos:\t0\nflags:\t02004002\nmnt_id:\t16\nino:\t1062\nclockid: 0\nticks: 0\nsettime flags: 03\nit_value: (7430049841, 93705461)\nit_interval: (0, 0)\n"

// Captured metadata /proc/1/fdinfo/97 sha256:6d2c5e50e64e1461db0f1a411240ed0cd8b3afb68625ba27d9a143d22e4d3e2c; no live kernel claim.
const engineCapturedFD97 = "pos:\t791\nflags:\t02400002\nmnt_id:\t0\nino:\t1578\n"

type engineProcessFixture struct {
	ps        []qualificationEngineProcess
	scan      int
	events    []string
	hook      func(int, uint64, uint64, string) error
	mutate    func(int, uint64, uint64, *engineFDFixture)
	rawHook   func(int, uint64, string, []byte) []byte
	namesHook func([]string) []string
}

func (f *engineProcessFixture) event(pid, fd uint64, op string) error {
	f.events = append(f.events, fmt.Sprintf("%d/%d/%d/%s", f.scan, pid, fd, op))
	if f.hook != nil {
		return f.hook(f.scan, pid, fd, op)
	}
	return nil
}
func engineProcessStat(p qualificationEngineProcess) []byte {
	if p.PID == 1 {
		return []byte(engineCapturedPID1Stat)
	}
	fields := strings.Fields("S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")
	fields[19] = strconv.FormatUint(p.Start, 10)
	return []byte(fmt.Sprintf("%d (fixture) %s\n", p.PID, strings.Join(fields, " ")))
}
func (f *engineProcessFixture) ops() qualificationEngineProcessOps {
	find := func(pid uint64) qualificationEngineProcess {
		for _, p := range f.ps {
			if p.PID == pid {
				return p
			}
		}
		panic("missing fixture PID")
	}
	return qualificationEngineProcessOps{
		pids: func(context.Context) ([]uint64, error) {
			if e := f.event(0, 0, "pids"); e != nil {
				return nil, e
			}
			out := []uint64{}
			for _, p := range f.ps {
				out = append(out, p.PID)
			}
			return out, nil
		},
		read: func(_ context.Context, pid uint64, name string, _ int64) ([]byte, error) {
			if e := f.event(pid, 0, name); e != nil {
				return nil, e
			}
			p := find(pid)
			raw := engineProcessStat(p)
			if name == "cgroup" {
				raw = []byte("0::" + p.Cgroup + "\n")
			}
			if f.rawHook != nil {
				raw = f.rawHook(f.scan, pid, name, raw)
			}
			return raw, nil
		},
		exe: func(_ context.Context, pid uint64) (string, error) { return find(pid).Exe, f.event(pid, 0, "exe") },
		openFDs: func(_ context.Context, pid uint64) (qualificationEngineFDDirectory, error) {
			if e := f.event(pid, 0, "dir-open"); e != nil {
				return nil, e
			}
			return &engineProcessDir{f: f, p: find(pid)}, nil
		},
	}
}
func (f *engineProcessFixture) collect(ctx context.Context) ([]qualificationEngineProcess, error) {
	f.scan++
	return qualificationEngineProcessesWith(ctx, f.ops())
}

type engineProcessDir struct {
	f                  *engineProcessFixture
	p                  qualificationEngineProcess
	enumerated, closed bool
	active             *engineFDFixture
	fd                 uint64
}

func (d *engineProcessDir) names(_ context.Context, limit int) ([]string, error) {
	if d.closed {
		return nil, fmt.Errorf("closed directory")
	}
	if e := d.f.event(d.p.PID, 0, "dir-enumerate"); e != nil {
		return nil, e
	}
	out := []string{}
	for _, fd := range d.p.FDs {
		out = append(out, strconv.FormatUint(fd.FD, 10))
	}
	sort.Strings(out)
	if d.f.namesHook != nil {
		out = d.f.namesHook(out)
	}
	d.enumerated = true
	return out, nil
}
func (d *engineProcessDir) close(context.Context) error {
	d.closed = true
	return d.f.event(d.p.PID, 0, "dir-close")
}
func (d *engineProcessDir) metadata() qualificationEngineFDOps {
	return qualificationEngineFDOps{
		openPin: func(ctx context.Context, name string) (int, error) {
			if !d.enumerated || d.closed {
				panic("pin outside held enumerated directory")
			}
			fd, _ := strconv.ParseUint(name, 10, 64)
			d.fd = fd
			for _, row := range d.p.FDs {
				if row.FD == fd {
					mode := uint32(0100000)
					if strings.HasPrefix(row.Target, "socket:") {
						mode = 0140000
					}
					obj := qualificationEngineFDObject{Target: row.Target, Device: row.Device, Inode: row.Inode, Mode: mode}
					info := fmt.Sprintf("flags: %o\nmnt_id: %d\nino: %d\n", row.Flags, row.Mount, row.Inode)
					if d.p.PID == 1 && fd == 8 {
						info = engineCapturedFD8
					}
					if d.p.PID == 1 && fd == 97 {
						info = engineCapturedFD97
					}
					pin := fmt.Sprintf("flags: 010200000\nmnt_id: %d\nino: %d\n", row.Mount, row.Inode)
					d.active = &engineFDFixture{c: engineFDCase{Pin: obj, Before: obj, After: obj, FDInfo: info, PinInfo: pin}, hook: func(op string) error { return d.f.event(d.p.PID, fd, op) }}
					if d.f.mutate != nil {
						d.f.mutate(d.f.scan, d.p.PID, fd, d.active)
					}
					return d.active.ops().openPin(ctx, name)
				}
			}
			panic("unknown FD")
		},
		pinStat: func(ctx context.Context, fd int) (qualificationEngineFDObject, error) {
			return d.active.ops().pinStat(ctx, fd)
		},
		pinLink:    func(ctx context.Context, fd int) (string, error) { return d.active.ops().pinLink(ctx, fd) },
		pinInfo:    func(ctx context.Context, fd int) ([]byte, error) { return d.active.ops().pinInfo(ctx, fd) },
		sourceLink: func(ctx context.Context, name string) (string, error) { return d.active.ops().sourceLink(ctx, name) },
		sourceStat: func(ctx context.Context, name string) (qualificationEngineFDObject, error) {
			return d.active.ops().sourceStat(ctx, name)
		},
		sourceInfo: func(ctx context.Context, name string) ([]byte, error) { return d.active.ops().sourceInfo(ctx, name) },
		closePin:   func(ctx context.Context, fd int) error { return d.active.ops().closePin(ctx, fd) },
	}
}

type engineNativeFixture struct {
	r                                  qualificationEngineReceiptV2
	process                            engineProcessFixture
	tables                             []qualificationEngineListener
	tableCalls, bootCalls, socketCalls int
	tablesHook                         func(int, []qualificationEngineListener) []qualificationEngineListener
	anchorHook                         func(string, *qualificationEngineNative)
}

func newEngineNativeFixture(t *testing.T) *engineNativeFixture {
	r := mustQualificationEngineV2(t)
	f := &engineNativeFixture{r: r}
	// Real process loop uses observed PID1 stat/fdinfo; object target/stat are
	// explicit synthetic counterfactuals, not asserted historical identities.
	f.process.ps = []qualificationEngineProcess{{PID: 1, Start: 0, Exe: "/usr/lib/systemd/systemd", Cgroup: "/init.scope", FDs: []qualificationEngineProcessFD{
		{FD: 8, Target: "anon_inode:[timerfd]", Flags: 02004002, Device: 16, Inode: 1062, Mount: 16},
		{FD: 97, Target: "/memfd:systemd (deleted)", Flags: 02400002, Device: 1, Inode: 1578, Mount: 0},
	}}}
	for _, d := range r.Daemons {
		p := qualificationEngineProcess{PID: d.PID, Start: d.Start, Exe: d.Exe, Cgroup: d.Cgroup, FDs: []qualificationEngineProcessFD{}}
		for i, s := range r.Sockets {
			if s.OwnerPID == d.PID {
				p.FDs = append(p.FDs, qualificationEngineProcessFD{FD: uint64(10 + i), Target: fmt.Sprintf("socket:[%d]", s.KernelInode), Flags: 02000002, Device: 9, Inode: s.KernelInode, Mount: 8})
			}
		}
		f.process.ps = append(f.process.ps, p)
	}
	for _, s := range r.Sockets {
		f.tables = append(f.tables, qualificationEngineListener{Family: "unix", Path: s.Path, Inode: s.KernelInode, Listener: true})
	}
	return f
}
func (f *engineNativeFixture) anchor(op string) qualificationEngineNative {
	n := qualificationEngineAnchor(qualificationEngineNative{Boot: f.r.Boot, Filesystem: f.r.Filesystem, RuntimeParent: f.r.RuntimeParent, Daemons: f.r.Daemons, Sockets: f.r.Sockets, SocketParents: f.r.SocketParents})
	if f.anchorHook != nil {
		f.anchorHook(op, &n)
	}
	return n
}
func (f *engineNativeFixture) ops() qualificationEngineNativeOps {
	return qualificationEngineNativeOps{
		boot: func(context.Context) (string, error) { f.bootCalls++; return f.anchor("boot").Boot, nil },
		filesystem: func(context.Context) (qualificationEngineFilesystem, qualificationEngineRuntimeParent, error) {
			n := f.anchor("filesystem")
			return n.Filesystem, n.RuntimeParent, nil
		},
		daemon: func(_ context.Context, i int, _ string) (qualificationEngineDaemon, error) {
			return f.anchor("daemon").Daemons[i], nil
		},
		tables: func(context.Context) ([]qualificationEngineListener, error) {
			f.tableCalls++
			rows := append([]qualificationEngineListener{}, f.tables...)
			if f.tablesHook != nil {
				rows = f.tablesHook(f.tableCalls, rows)
			}
			return rows, nil
		},
		sockets: func(context.Context, []string, []qualificationEngineDaemon) (qualificationEngineSocketSample, error) {
			f.socketCalls++
			n := f.anchor("socket")
			return qualificationEngineSocketSample{Sockets: n.Sockets, Parents: n.SocketParents}, nil
		},
		processes: f.process.collect,
	}
}
func (f *engineNativeFixture) collect(ctx context.Context) (qualificationEngineNative, error) {
	return qualificationEngineCollectNativeWith(ctx, f.r, f.ops())
}
func engineRace(op string) error { return qualificationEngineSourceError(op, syscall.ENOENT) }
func TestQualificationEngineFDTwoAttempts(t *testing.T) {
	for _, op := range []string{"pin-open", "source-readlink-before", "source-stat-before", "source-fdinfo-open", "source-readlink-after", "source-stat-after"} {
		t.Run(op, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			f.process.hook = func(scan int, pid, fd uint64, at string) error {
				if scan == 1 && pid == 1 && fd == 8 && at == op {
					return engineRace(at)
				}
				return nil
			}
			got, e := f.collect(context.Background())
			if e != nil || !got.Complete || len(got.Attempts) != 2 || f.process.scan != 3 {
				t.Fatalf("fresh scan: complete=%v attempts=%+v scans=%d err=%v", got.Complete, got.Attempts, f.process.scan, e)
			}
			if got.Attempts[0].Complete || !got.Attempts[1].Complete {
				t.Fatal("attempt accounting")
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "Attempts") || strings.Contains(string(raw), "source-fdinfo") {
				t.Fatal("private attempt escaped JSON")
			}
			t.Logf("attempts=[1,2], real process scans=%d, primitive calls=%d", f.process.scan, len(f.process.events))
		})
	}
}
func TestQualificationEngineFDHardFailures(t *testing.T) {
	for _, kind := range []string{"permission", "pin-fstat", "pin-readlink", "pin-info", "pin-close", "dir-close", "source-close", "observer-enoent", "observer-wrapped-race", "race-and-pin-close", "race-and-dir-close", "flags", "second-flags", "second-mount", "second-fdinfo-gone", "pid", "process-flags", "exe", "cgroup", "budget"} {
		t.Run(kind, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			identityReads := 0
			f.process.hook = func(scan int, pid, fd uint64, op string) error {
				if pid != 1 {
					return nil
				}
				if kind == "budget" && op == "dir-enumerate" {
					return fmt.Errorf("FD budget")
				}
				if kind == "dir-close" && op == "dir-close" {
					return syscall.EIO
				}
				if kind == "race-and-dir-close" {
					if fd == 8 && op == "source-fdinfo-open" {
						return engineRace(op)
					}
					if op == "dir-close" {
						return syscall.EIO
					}
				}
				if fd != 8 {
					return nil
				}
				if (kind == "race-and-pin-close" || kind == "pid" || kind == "process-flags" || kind == "exe" || kind == "cgroup") && op == "source-fdinfo-open" {
					return engineRace(op)
				}
				if kind == "race-and-pin-close" && op == "pin-close" {
					return syscall.EIO
				}
				faults := map[string]string{"permission": "source-fdinfo-open", "pin-fstat": "pin-fstat", "pin-readlink": "pin-readlink", "pin-info": "pin-fdinfo", "pin-close": "pin-close", "source-close": "source-fdinfo-open", "observer-enoent": "source-fdinfo-open", "observer-wrapped-race": "source-fdinfo-open"}
				if op == faults[kind] {
					switch kind {
					case "source-close":
						return qualificationEngineCleanup(engineRace(op), syscall.EIO)
					case "observer-enoent":
						return fmt.Errorf("recorder: %w", syscall.ENOENT)
					case "observer-wrapped-race":
						return fmt.Errorf("observer: %w", &qualificationEngineFDRace{ProcessStable: true})
					default:
						return syscall.EACCES
					}
				}
				return nil
			}
			f.process.rawHook = func(_ int, pid uint64, name string, raw []byte) []byte {
				if pid == 1 && name == "stat" {
					identityReads++
					if identityReads == 2 {
						if kind == "pid" {
							return []byte(strings.Replace(string(raw), "1 (systemd)", "2 (systemd)", 1))
						}
						if kind == "process-flags" {
							return []byte(strings.Replace(string(raw), "4194560", "4194561", 1))
						}
					}
				}
				if pid == 1 && name == "cgroup" && identityReads >= 2 && kind == "cgroup" {
					return []byte("0::/other\n")
				}
				return raw
			}
			f.process.mutate = func(_ int, pid, fd uint64, x *engineFDFixture) {
				if pid == 1 && fd == 8 {
					if kind == "flags" {
						x.c.FDInfo = "flags: 08\nmnt_id: 16\nino: 1062\n"
					}
					if kind == "second-flags" {
						x.afterInfo = "flags: 02\nmnt_id: 16\nino: 1062\n"
					}
					if kind == "second-mount" {
						x.afterInfo = "flags: 02004002\nmnt_id: 17\nino: 1062\n"
					}
					if kind == "second-fdinfo-gone" {
						old := x.hook
						x.hook = func(op string) error {
							if op == "source-fdinfo-open" && x.info == 2 {
								return engineRace(op)
							}
							return old(op)
						}
					}
				}
			}
			ops := f.ops()
			if kind == "exe" {
				ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
					f.process.scan++
					pops := f.process.ops()
					orig := pops.exe
					calls := 0
					pops.exe = func(ctx context.Context, pid uint64) (string, error) {
						s, e := orig(ctx, pid)
						if pid == 1 {
							calls++
							if calls == 2 {
								s = "/usr/bin/replacement"
							}
						}
						return s, e
					}
					return qualificationEngineProcessesWith(ctx, pops)
				}
			}
			got, e := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
			want := 1
			if kind == "second-mount" || kind == "second-fdinfo-gone" {
				want = 2
			}
			if e == nil || got.Complete || len(got.Attempts) != want {
				t.Fatalf("complete=%v attempts=%d err=%v", got.Complete, len(got.Attempts), e)
			}
			var terminal *qualificationEngineCollectionFailure
			if !errors.As(e, &terminal) || len(terminal.Attempts) != want {
				t.Fatal("terminal diagnostics missing")
			}
			if kind == "race-and-pin-close" || kind == "race-and-dir-close" {
				var race *qualificationEngineFDRace
				var cleanup *qualificationEngineCleanupFailure
				if !errors.As(e, &race) || !errors.As(e, &cleanup) || !errors.Is(e, syscall.EIO) {
					t.Fatalf("primary/close not preserved: %v", e)
				}
			}
		})
	}
}
func TestQualificationEngineFDInvalidNativeTargets(t *testing.T) {
	for _, which := range []string{"pin", "before", "after"} {
		for _, target := range []string{"", string([]byte{0xff, 0xfe}), "bad\x00target", strings.Repeat("x", 4097)} {
			t.Run(fmt.Sprintf("%s/%x/%d", which, []byte(target[:min(len(target), 2)]), len(target)), func(t *testing.T) {
				f := newEngineNativeFixture(t)
				f.process.mutate = func(_ int, pid, fd uint64, x *engineFDFixture) {
					if pid == 1 && fd == 8 {
						switch which {
						case "pin":
							x.c.Pin.Target = target
						case "before":
							x.c.Before.Target = target
						case "after":
							x.c.After.Target = target
						}
					}
				}
				got, e := f.collect(context.Background())
				if e == nil || len(got.Attempts) != 1 || got.Complete {
					t.Fatalf("invalid target retried: %+v %v", got.Attempts, e)
				}
			})
		}
	}
}
func TestQualificationEngineFDNativePolicyBeforeRace(t *testing.T) {
	for _, kind := range []string{"unowned-tcp-initial", "unowned-unix-initial", "unowned-tcp-closing", "unowned-unix-closing", "partial-foreign-daemon", "partial-writer", "partial-sensitive", "partial-foreign-owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			f.process.hook = func(scan int, pid, fd uint64, op string) error {
				if op == "source-fdinfo-open" && pid == 1 && fd == 97 && (scan == 2 || strings.HasPrefix(kind, "partial")) {
					return engineRace(op)
				}
				return nil
			}
			if strings.HasPrefix(kind, "unowned") {
				f.tablesHook = func(call int, rows []qualificationEngineListener) []qualificationEngineListener {
					active := call == 1
					if strings.HasSuffix(kind, "closing") {
						active = call == 2
					}
					if active {
						l := qualificationEngineListener{Family: "tcp", Address: "0100007F", Port: 9090, Inode: 12345, Listener: true}
						if strings.Contains(kind, "unix") {
							l.Family = "unix"
							l.Path = "/ordinary.sock"
						}
						return append(rows, l)
					}
					return rows
				}
				if strings.HasSuffix(kind, "closing") {
					f.process.hook = nil
				}
			}
			if kind == "partial-foreign-daemon" {
				f.process.ps[0].Exe = "/usr/bin/dockerd"
			}
			if kind == "partial-writer" {
				f.process.ps[0].FDs[0].Target = "/mnt/lima-sbquota/docker/foreign"
			}
			if kind == "partial-sensitive" {
				f.tables = append(f.tables, qualificationEngineListener{Family: "unix", Path: "/tmp/docker-hidden", Inode: 12345, Listener: true})
			}
			if kind == "partial-foreign-owner" {
				s := f.r.Sockets[0]
				f.process.ps[0].FDs = append([]qualificationEngineProcessFD{{FD: 1, Target: fmt.Sprintf("socket:[%d]", s.KernelInode), Inode: s.KernelInode, Device: 9, Mount: 8, Flags: 2}}, f.process.ps[0].FDs...)
			}
			got, e := f.collect(context.Background())
			if e == nil || got.Complete || len(got.Attempts) != 1 {
				t.Fatalf("negative was erased: attempts=%+v err=%v", got.Attempts, e)
			}
			if strings.HasSuffix(kind, "initial") && f.process.scan != 1 {
				t.Fatal("initial negative reached closing scan")
			}
		})
	}
}
func TestQualificationEngineFDAnchorsAndDeadline(t *testing.T) {
	for _, kind := range []string{"boot", "filesystem", "runtime-parent", "daemon", "socket", "socket-parent", "second-boot", "second-socket", "deadline", "second-race"} {
		t.Run(kind, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.process.hook = func(scan int, pid, fd uint64, op string) error {
				if pid == 1 && fd == 8 && op == "source-fdinfo-open" && (scan == 1 || kind == "second-race") {
					if kind == "deadline" {
						cancel()
					}
					return engineRace(op)
				}
				return nil
			}
			f.anchorHook = func(op string, n *qualificationEngineNative) {
				if f.process.scan == 0 {
					return
				}
				if strings.HasPrefix(kind, "second-") && f.bootCalls < 3 {
					return
				}
				switch kind {
				case "boot", "second-boot":
					n.Boot = "00000000-0000-4000-8000-000000000000"
				case "filesystem":
					n.Filesystem.RootInode++
				case "runtime-parent":
					n.RuntimeParent.Inode++
				case "daemon":
					n.Daemons[0].Start++
				case "socket", "second-socket":
					n.Sockets[0].Inode++
				case "socket-parent":
					n.SocketParents[0].Inode++
				}
			}
			got, e := f.collect(ctx)
			want := 1
			if strings.HasPrefix(kind, "second-") {
				want = 2
			}
			if e == nil || got.Complete || len(got.Attempts) != want {
				t.Fatalf("anchor/deadline accepted: attempts=%+v err=%v", got.Attempts, e)
			}
			if kind == "deadline" && !errors.Is(e, context.Canceled) {
				t.Fatal("deadline cause lost")
			}
		})
	}
}
func TestQualificationEngineFDCoverageFreshAttempt(t *testing.T) {
	f := newEngineNativeFixture(t)
	// Add an owned ordinary TCP listener only to closing table/scan, then keep
	// the second full attempt stable. Every scan still runs the real process loop.
	original := f.process.collect
	ops := f.ops()
	ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
		if f.process.scan == 1 {
			f.process.ps[0].FDs = append(f.process.ps[0].FDs, qualificationEngineProcessFD{FD: 98, Target: "socket:[12345]", Inode: 12345, Device: 9, Mount: 8, Flags: 2})
		}
		return original(ctx)
	}
	f.tablesHook = func(call int, rows []qualificationEngineListener) []qualificationEngineListener {
		if call >= 2 {
			return append(rows, qualificationEngineListener{Family: "tcp", Address: "0100007F", Port: 9090, Inode: 12345, Listener: true})
		}
		return rows
	}
	got, e := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
	if e != nil || !got.Complete || len(got.Attempts) != 2 || f.process.scan != 4 {
		t.Fatalf("coverage fresh retry: %+v %v", got.Attempts, e)
	}
}
func TestQualificationEngineFDBoundsAndCleanup(t *testing.T) {
	for _, kind := range []string{"fd-count", "fd-malformed", "fd-duplicate", "process-count", "expired-context", "max-tuple"} {
		t.Run(kind, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			ops := f.process.ops()
			ctx := context.Background()
			switch kind {
			case "fd-count":
				f.process.namesHook = func([]string) []string { return make([]string, 131073) }
			case "fd-malformed":
				f.process.namesHook = func([]string) []string { return []string{"8x"} }
			case "fd-duplicate":
				f.process.namesHook = func([]string) []string { return []string{"8", "8"} }
			case "process-count":
				ops.pids = func(context.Context) ([]uint64, error) { return make([]uint64, 65537), nil }
			case "expired-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "max-tuple":
				tuple, e := qualificationEngineDecodeFDTuple([]byte("flags: 37777777777\nmnt_id: 18446744073709551615\nino: 18446744073709551615\n"))
				if e != nil || tuple.flags != 1<<32-1 || tuple.inode != 1<<64-1 {
					t.Fatal(tuple, e)
				}
				return
			}
			_, e := qualificationEngineProcessesWith(ctx, ops)
			if e == nil {
				t.Fatal("bound accepted")
			}
			for _, event := range f.process.events {
				if strings.HasSuffix(event, "pin-open") {
					t.Fatal("pin before complete validated enumeration")
				}
			}
		})
	}
}

func TestQualificationEngineFDCancellationAlwaysCloses(t *testing.T) {
	for _, point := range []string{"pin-fstat", "source-fdinfo-open", "pin-close", "dir-enumerate"} {
		t.Run(point, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.process.hook = func(_ int, pid, fd uint64, op string) error {
				if pid == 1 && op == point && (fd == 8 || point == "dir-enumerate") {
					cancel()
				}
				if pid == 1 && (op == "pin-close" || op == "dir-close") {
					return syscall.EIO
				}
				return nil
			}
			got, e := f.collect(ctx)
			if e == nil || got.Complete || len(got.Attempts) != 1 || !errors.Is(e, context.Canceled) || !errors.Is(e, syscall.EIO) {
				t.Fatalf("cancel/close causes: %+v %v", got.Attempts, e)
			}
			counts := map[string]int{}
			for _, event := range f.process.events {
				parts := strings.Split(event, "/")
				counts[parts[len(parts)-1]]++
			}
			if counts["dir-close"] != 1 {
				t.Fatal("owned directory leaked on cancellation", counts)
			}
			want := 1
			if point == "dir-enumerate" {
				want = 0
			}
			if counts["pin-close"] != want {
				t.Fatal("owned pin leaked on cancellation", counts)
			}
		})
	}
}

func TestQualificationEngineFDCapturedRawFacts(t *testing.T) {
	for _, pair := range [][2]string{
		{engineCapturedPID1Stat, "91bbfe0aa90a3dbf53b03166b30f41157deac648b78b63ba24b11bde18d4172d"},
		{engineCapturedFD8, "01316418d32e65bb4ae081740485a6ca3b78137c745b9d2533768890badaef08"},
		{engineCapturedFD97, "6d2c5e50e64e1461db0f1a411240ed0cd8b3afb68625ba27d9a143d22e4d3e2c"},
	} {
		if fmt.Sprintf("%x", sha256.Sum256([]byte(pair[0]))) != pair[1] {
			t.Fatal("captured raw bytes changed")
		}
	}
	f := newEngineNativeFixture(t)
	ps, e := f.process.collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if ps[0].PID != 1 || ps[0].Start != 0 || ps[0].FDs[0].Flags != 02004002 || ps[0].FDs[1].Flags != 02400002 || ps[0].FDs[1].Mount != 0 {
		t.Fatalf("captured source facts lost: %+v", ps[0])
	}
}

func TestQualificationEngineFDNativeRetryDoesNotReplayAPI(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			readback := newQualificationEngineReadbackFixture(t)
			f := newEngineNativeFixture(t)
			readers := readback.readers()
			nativeCalls := 0
			attemptCount := 0
			readers.native = func(ctx context.Context, r qualificationEngineReceiptV2) (qualificationEngineNative, error) {
				nativeCalls++
				f.r = r
				f.process.hook = func(scan int, pid, fd uint64, op string) error {
					if pid == 1 && fd == 8 && op == "source-fdinfo-open" && (scan == 1 || fail) {
						return engineRace(op)
					}
					return nil
				}
				n, e := f.collect(ctx)
				attemptCount += len(n.Attempts)
				return n, e
			}
			e := qualificationVerifyEngineDeploymentWith(context.Background(), readback.kit, readers)
			if fail {
				if e == nil || readback.apiCalls != 0 || nativeCalls != 1 || attemptCount != 2 {
					t.Fatalf("failed native reached API: %d/%d/%d %v", readback.apiCalls, nativeCalls, attemptCount, e)
				}
			} else {
				if e != nil || readback.apiCalls != 3 || nativeCalls != 4 || attemptCount != 5 {
					t.Fatalf("API replay: %d/%d/%d %v", readback.apiCalls, nativeCalls, attemptCount, e)
				}
			}
		})
	}
}

func TestQualificationEngineFDScaleBounded(t *testing.T) {
	f := newEngineNativeFixture(t)
	for i := uint64(0); i < 3000; i++ {
		f.process.ps[0].FDs = append(f.process.ps[0].FDs, qualificationEngineProcessFD{FD: 100 + i, Target: fmt.Sprintf("socket:[%d]", 100000+i), Device: 9, Inode: 100000 + i, Mount: 8, Flags: 02000002})
	}
	f.process.hook = func(scan int, pid, fd uint64, op string) error {
		if scan == 1 && pid == 1 && fd == 97 && op == "source-fdinfo-open" {
			return engineRace(op)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	n, e := f.collect(ctx)
	if e != nil || !n.Complete || len(n.Attempts) != 2 {
		t.Fatal(n.Attempts, e)
	}
	opens, closes := 0, 0
	for _, event := range f.process.events {
		if strings.HasSuffix(event, "/pin-open") {
			opens++
		}
		if strings.HasSuffix(event, "/pin-close") {
			closes++
		}
	}
	if opens != closes {
		t.Fatal("pins leaked", opens, closes)
	}
	t.Logf("3000 extra socket FDs; %d real process scans; %d metadata operations; %d pins closed; elapsed=%s", f.process.scan, len(f.process.events), closes, time.Since(start))
}

func TestQualificationEngineFDSourceMembership(t *testing.T) {
	for _, leaf := range []string{"fd_lifecycle.py", "test_fd_lifecycle.py", "testdata/fd-lifecycle-corpus.json"} {
		for _, kind := range []string{"missing", "wrong-path"} {
			t.Run(leaf+"/"+kind, func(t *testing.T) {
				f := newQualificationEngineSourceLeafFixtureWith(t, func(paths []string) []string {
					out := []string{}
					for _, p := range paths {
						if p == qualificationEngineSourcePrefix+leaf {
							if kind == "wrong-path" {
								out = append(out, p+".bak")
							}
							continue
						}
						out = append(out, p)
					}
					return out
				})
				e := qualificationEngineBind(context.Background(), f.kit, f.receipt, f.readers().read)
				if e == nil {
					t.Fatal("unbound helper accepted")
				}
				want := "unknown Engine source leaf"
				if kind == "missing" {
					want = "required Engine source leaf missing: " + leaf
				}
				if e.Error() != want {
					t.Fatalf("wrong failure: %v", e)
				}
			})
		}
	}
}

func TestQualificationEngineFDNativeDirectoryCleanup(t *testing.T) {
	for _, kind := range []string{"stable", "child-open", "child-stat", "ancestor-close", "cancel-after-open", "cancel-at-close", "context-and-two-close-errors"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := []int{}
			closed := []int{}
			ops := qualificationEngineDirectoryOps{
				openRoot: func(context.Context) (int, error) { opened = append(opened, 10); return 10, nil },
				openChild: func(_ context.Context, fd int, name string) (int, error) {
					if kind == "child-open" {
						return -1, syscall.EACCES
					}
					opened = append(opened, fd+1)
					if kind == "cancel-after-open" {
						cancel()
					}
					return fd + 1, nil
				},
				identity: func(_ context.Context, fd int) (string, error) {
					if fd == 11 && kind == "child-stat" {
						return "", syscall.EPERM
					}
					return strconv.Itoa(fd), nil
				},
				close: func(_ context.Context, fd int) error {
					closed = append(closed, fd)
					if fd == 10 && (kind == "cancel-at-close" || kind == "context-and-two-close-errors") {
						cancel()
					}
					if kind == "ancestor-close" && fd == 10 {
						return syscall.EIO
					}
					if kind == "context-and-two-close-errors" {
						if fd == 10 {
							return syscall.EIO
						}
						return syscall.EBADF
					}
					return nil
				},
			}
			fd, ids, e := qualificationEngineOpenDirectoryWith(ctx, "/run/private", ops)
			if kind == "stable" {
				if e != nil || fd != 12 || len(ids) != 3 || len(closed) != 2 {
					t.Fatal(fd, ids, closed, e)
				}
				_ = ops.close(ctx, fd)
			} else {
				if e == nil {
					t.Fatal("cleanup fault accepted")
				}
				if strings.HasPrefix(kind, "cancel") || strings.HasPrefix(kind, "context") {
					if !errors.Is(e, context.Canceled) {
						t.Fatal("cancellation lost", e)
					}
				}
			}
			if fmt.Sprint(opened) != fmt.Sprint(closed) {
				t.Fatalf("owned handles not all closed: opened=%v closed=%v err=%v", opened, closed, e)
			}
			if kind == "context-and-two-close-errors" && (!errors.Is(e, syscall.EIO) || !errors.Is(e, syscall.EBADF)) {
				t.Fatal("cleanup causes lost", e)
			}
		})
	}
}

func TestQualificationEngineFDProcessGroupFraming(t *testing.T) {
	f := newEngineNativeFixture(t)
	f.process.rawHook = func(_ int, _ uint64, name string, raw []byte) []byte {
		if name == "cgroup" {
			return []byte("0::/init.scope\ntrailing")
		}
		return raw
	}
	_, e := f.process.collect(context.Background())
	if e == nil {
		t.Fatal("cgroup without terminal newline accepted")
	}
	for _, event := range f.process.events {
		if strings.HasSuffix(event, "pin-open") {
			t.Fatal("invalid process metadata reached FD scan")
		}
	}
}

func TestQualificationEngineFDReviewAnchorHardNegative(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, kind := range []string{"hidden-unix", "invalid-port", "protected-mismatch", "ordinary-partial-control"} {
			t.Run(fmt.Sprintf("retry=%v/%s", retry, kind), func(t *testing.T) {
				f := newEngineNativeFixture(t)
				call := 3
				if retry {
					call = 2
					f.process.hook = func(scan int, pid, fd uint64, op string) error {
						if scan == 1 && pid == 1 && fd == 8 && op == "source-fdinfo-open" {
							return engineRace(op)
						}
						return nil
					}
				}
				f.tablesHook = func(n int, rows []qualificationEngineListener) []qualificationEngineListener {
					if n != call {
						return rows
					}
					row := qualificationEngineListener{Family: "unix", Path: "/tmp/docker-hidden", Inode: 12345, Listener: true}
					switch kind {
					case "invalid-port":
						row.Family = "tcp"
						row.Address = "0100007F"
						row.Port = 65536
					case "protected-mismatch":
						row.Path = f.r.Sockets[0].Path
					case "ordinary-partial-control":
						row.Path = "/tmp/ordinary.sock"
					}
					return append(rows, row)
				}
				got, err := f.collect(context.Background())
				if kind == "ordinary-partial-control" {
					want := 1
					if retry {
						want = 2
					}
					if err != nil || !got.Complete || len(got.Attempts) != want {
						t.Fatalf("partial owner coverage required: %+v %v", got.Attempts, err)
					}
					return
				}
				if err == nil || got.Complete || len(got.Attempts) != 1 || f.tableCalls != call {
					t.Fatalf("anchor negative erased: complete=%v attempts=%d tables=%d err=%v", got.Complete, len(got.Attempts), f.tableCalls, err)
				}
				if !errors.Is(err, errQualification) || !errors.Is(got.Attempts[0].Cause, errQualification) {
					t.Fatalf("policy cause lost: %v", err)
				}
				if retry {
					var race *qualificationEngineFDRace
					if !errors.As(err, &race) || !errors.As(got.Attempts[0].Cause, &race) || !errors.Is(err, syscall.ENOENT) {
						t.Fatalf("primary race lost: %v", err)
					}
				}
			})
		}
	}
}

func TestQualificationEngineFDReviewPolicyRetainsCleanup(t *testing.T) {
	for _, scan := range []int{1, 2} {
		for _, negative := range []string{"foreign-daemon", "writer"} {
			for _, primary := range []string{"race", "permission"} {
				for _, closes := range []string{"pin", "directory", "both"} {
					t.Run(fmt.Sprintf("scan%d/%s/%s/%s", scan, negative, primary, closes), func(t *testing.T) {
						f := newEngineNativeFixture(t)
						pinErr := fmt.Errorf("pin close: %w", syscall.EIO)
						dirErr := fmt.Errorf("directory close: %w", syscall.EIO)
						f.process.hook = func(n int, pid, fd uint64, op string) error {
							if n != scan || pid != 1 {
								return nil
							}
							if fd == 97 && op == "source-fdinfo-open" {
								if primary == "race" {
									return engineRace(op)
								}
								return syscall.EACCES
							}
							if fd == 97 && op == "pin-close" && closes != "directory" {
								return pinErr
							}
							if op == "dir-close" && closes != "pin" {
								return dirErr
							}
							return nil
						}
						ops := f.ops()
						ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
							if f.process.scan+1 == scan {
								if negative == "foreign-daemon" {
									f.process.ps[0].Exe = "/usr/bin/dockerd"
								} else {
									f.process.ps[0].FDs[0].Target = "/mnt/lima-sbquota/docker/foreign"
								}
							}
							return f.process.collect(ctx)
						}
						got, err := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
						if err == nil || got.Complete || len(got.Attempts) != 1 || f.process.scan != scan {
							t.Fatalf("combined failure retried: %+v scans=%d %v", got.Attempts, f.process.scan, err)
						}
						var failure *qualificationEngineCollectionFailure
						if !errors.As(err, &failure) || len(failure.Attempts) != 1 {
							t.Fatal("terminal diagnostics missing")
						}
						for _, cause := range []error{err, got.Attempts[0].Cause, failure.Attempts[0].Cause} {
							policy := "foreign Engine process"
							if negative == "writer" {
								policy = "writer"
							}
							if !strings.Contains(cause.Error(), policy) {
								t.Fatalf("policy lost: %v", cause)
							}
							var cleanup *qualificationEngineCleanupFailure
							if !errors.As(cause, &cleanup) || !errors.Is(cause, syscall.EIO) {
								t.Fatalf("cleanup lost: %v", cause)
							}
							if closes != "directory" && !errors.Is(cause, pinErr) {
								t.Fatalf("pin failure lost: %v", cause)
							}
							if closes != "pin" && !errors.Is(cause, dirErr) {
								t.Fatalf("directory failure lost: %v", cause)
							}
							if primary == "race" {
								var race *qualificationEngineFDRace
								if !errors.As(cause, &race) || !errors.Is(cause, syscall.ENOENT) {
									t.Fatalf("race lost: %v", cause)
								}
							} else if !errors.Is(cause, syscall.EACCES) {
								t.Fatalf("acquisition lost: %v", cause)
							}
						}
					})
				}
			}
		}
	}
}

func TestQualificationEngineFDReviewRaceRetainsAnchorFailure(t *testing.T) {
	for _, scan := range []int{1, 2} {
		for _, kind := range []string{"table-cleanup", "boot-drift"} {
			t.Run(fmt.Sprintf("scan%d/%s", scan, kind), func(t *testing.T) {
				f := newEngineNativeFixture(t)
				f.process.hook = func(n int, pid, fd uint64, op string) error {
					if n == scan && pid == 1 && fd == 8 && op == "source-fdinfo-open" {
						return engineRace(op)
					}
					return nil
				}
				anchorErr := errors.New("anchor table acquisition")
				ops := f.ops()
				if kind == "table-cleanup" {
					base := ops.tables
					ops.tables = func(ctx context.Context) ([]qualificationEngineListener, error) {
						rows, err := base(ctx)
						if f.tableCalls == 2 {
							return rows, qualificationEngineCleanup(anchorErr, syscall.EIO)
						}
						return rows, err
					}
				} else {
					f.anchorHook = func(op string, n *qualificationEngineNative) {
						if op == "boot" && f.bootCalls == 2 {
							n.Boot = "changed"
						}
					}
				}
				got, err := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
				if err == nil || got.Complete || len(got.Attempts) != 1 || f.process.scan != scan {
					t.Fatalf("anchor failure retried: %+v %v", got.Attempts, err)
				}
				for _, cause := range []error{err, got.Attempts[0].Cause} {
					var race *qualificationEngineFDRace
					if !errors.As(cause, &race) || !race.ProcessStable || !errors.Is(cause, syscall.ENOENT) {
						t.Fatalf("race lost: %v", cause)
					}
					if kind == "table-cleanup" {
						if !errors.Is(cause, anchorErr) || !errors.Is(cause, syscall.EIO) {
							t.Fatalf("anchor/cleanup lost: %v", cause)
						}
					} else if !strings.Contains(cause.Error(), "boot anchor drift") {
						t.Fatalf("anchor drift lost: %v", cause)
					}
				}
			})
		}
	}
}

func TestQualificationEngineFDWholeAnchorKnownOwners(t *testing.T) {
	for _, seam := range []string{"final", "initial-race", "closing-race-before-owner", "closing-race-after-owner", "coverage-race"} {
		for _, family := range []string{"tcp", "tcp6"} {
			t.Run(seam+"/"+family, func(t *testing.T) {
				f := newEngineNativeFixture(t)
				f.process.ps[1].FDs = append(f.process.ps[1].FDs, qualificationEngineProcessFD{FD: 999, Target: "socket:[987654]", Inode: 987654, Device: 9, Mount: 8, Flags: 2})
				call := 3
				scan := 0
				failPID := uint64(1)
				switch seam {
				case "initial-race":
					call = 2
					scan = 1
					failPID = f.process.ps[2].PID
				case "closing-race-before-owner":
					call = 2
					scan = 2
				case "closing-race-after-owner":
					call = 2
					scan = 2
					failPID = f.process.ps[2].PID
				case "coverage-race":
					call = 4
				}
				f.process.hook = func(n int, pid, fd uint64, op string) error {
					if scan > 0 && n == scan && pid == failPID && op == "source-fdinfo-open" {
						return engineRace(op)
					}
					return nil
				}
				f.tablesHook = func(n int, rows []qualificationEngineListener) []qualificationEngineListener {
					if n == call {
						return append(rows, qualificationEngineListener{Family: family, Address: "0100007F", Port: 9090, Inode: 987654, Listener: true})
					}
					return rows
				}
				ops := f.ops()
				if seam == "coverage-race" {
					ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
						if f.process.scan == 1 {
							f.process.ps[0].FDs = append(f.process.ps[0].FDs, qualificationEngineProcessFD{FD: 998, Target: "socket:[123456]", Inode: 123456, Device: 9, Mount: 8, Flags: 2})
						}
						return f.process.collect(ctx)
					}
				}
				got, err := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
				if err == nil || got.Complete || len(got.Attempts) != 1 || f.tableCalls != call || !errors.Is(err, errQualification) {
					t.Fatalf("known owner negative lost: complete=%v attempts=%+v calls=%d want=%d err=%v", got.Complete, got.Attempts, f.tableCalls, call, err)
				}
				if scan > 0 || seam == "coverage-race" {
					var race *qualificationEngineFDRace
					if !errors.As(err, &race) {
						t.Fatal("primary race lost", err)
					}
				}
			})
		}
	}
}

func TestQualificationEngineFDWholeCombinedFlagsObjectDrift(t *testing.T) {
	for _, drift := range []string{"mount", "inode", "target"} {
		t.Run(drift, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			f.process.mutate = func(scan int, pid, fd uint64, tuple *engineFDFixture) {
				if scan != 1 || pid != 1 || fd != 8 {
					return
				}
				tuple.afterInfo = strings.Replace(tuple.c.FDInfo, "flags:\t02004002", "flags:\t01", 1)
				switch drift {
				case "mount":
					tuple.c.FDInfo = strings.Replace(tuple.c.FDInfo, "mnt_id:\t16", "mnt_id:\t17", 1)
				case "inode":
					tuple.c.FDInfo = strings.Replace(tuple.c.FDInfo, "ino:\t1062", "ino:\t1063", 1)
				case "target":
					tuple.c.Before.Target = "socket:[987654]"
				}
			}
			got, err := f.collect(context.Background())
			if err == nil || got.Complete || len(got.Attempts) != 1 || !strings.Contains(err.Error(), "flags drift") {
				t.Fatal(got.Attempts, err)
			}
			var race *qualificationEngineFDRace
			if errors.As(err, &race) {
				t.Fatal("hard failure classified race", err)
			}
		})
	}
}

func TestQualificationEngineFDWholePriorAttemptOwnerAndAnchorErrors(t *testing.T) {
	for _, kind := range []string{"prior-attempt", "table-cleanup"} {
		t.Run(kind, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			f.process.ps[1].FDs = append(f.process.ps[1].FDs, qualificationEngineProcessFD{FD: 999, Target: "socket:[987654]", Inode: 987654, Device: 9, Mount: 8, Flags: 2})
			call := 3
			if kind == "prior-attempt" {
				call = 5
				f.process.hook = func(n int, pid, fd uint64, op string) error {
					if n == 1 && pid == f.process.ps[2].PID && op == "source-fdinfo-open" {
						return engineRace(op)
					}
					return nil
				}
			}
			f.tablesHook = func(n int, rows []qualificationEngineListener) []qualificationEngineListener {
				if n == call {
					return append(rows, qualificationEngineListener{Family: "tcp", Address: "0100007F", Port: 9090, Inode: 987654, Listener: true})
				}
				return rows
			}
			ops := f.ops()
			if kind == "prior-attempt" {
				ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
					if f.process.scan == 1 {
						f.process.ps[1].FDs = f.process.ps[1].FDs[:len(f.process.ps[1].FDs)-1]
					}
					return f.process.collect(ctx)
				}
			} else {
				table := ops.tables
				ops.tables = func(ctx context.Context) ([]qualificationEngineListener, error) {
					rows, e := table(ctx)
					if f.tableCalls == call {
						return rows, qualificationEngineCleanup(syscall.EACCES, syscall.EIO)
					}
					return rows, e
				}
			}
			got, err := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
			if err == nil || got.Complete || !errors.Is(err, errQualification) || f.tableCalls != call {
				t.Fatal(got.Attempts, f.tableCalls, err)
			}
			if kind == "prior-attempt" {
				if len(got.Attempts) != 2 {
					t.Fatal(got.Attempts)
				}
				var race *qualificationEngineFDRace
				if !errors.As(got.Attempts[0].Cause, &race) {
					t.Fatal("first race missing")
				}
			} else if len(got.Attempts) != 1 || !errors.Is(err, syscall.EACCES) || !errors.Is(err, syscall.EIO) {
				t.Fatal("acquisition/cleanup missing", got.Attempts, err)
			}
		})
	}
}

func TestQualificationEngineFDWholeRawTablesRetainKnownOwners(t *testing.T) {
	for _, seam := range []string{"closing", "initial-after-partial-owner", "initial-after-partial-before-owner", "initial-after-coverage"} {
		for _, family := range []string{"tcp", "tcp6"} {
			for _, tableFailure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/table-failure=%v", seam, family, tableFailure), func(t *testing.T) {
					f := newEngineNativeFixture(t)
					fd := qualificationEngineProcessFD{FD: 999, Target: "socket:[987654]", Inode: 987654, Device: 9, Mount: 8, Flags: 2}
					f.process.ps[1].FDs = append(f.process.ps[1].FDs, fd)
					call, scans, attempts := 2, 2, 1
					switch seam {
					case "initial-after-partial-owner":
						call, scans, attempts = 3, 1, 2
					case "initial-after-partial-before-owner":
						call, scans, attempts = 3, 2, 2
					case "initial-after-coverage":
						call, scans, attempts = 5, 2, 2
					}
					f.process.hook = func(n int, pid, _ uint64, op string) error {
						if op == "source-fdinfo-open" && (seam == "initial-after-partial-owner" && n == 1 && pid == f.process.ps[2].PID || seam == "initial-after-partial-before-owner" && n == 2 && pid == 1) {
							return engineRace(op)
						}
						return nil
					}
					f.tablesHook = func(n int, rows []qualificationEngineListener) []qualificationEngineListener {
						// The negative disappears before any subsequent anchor table.
						// For initial tables it also appears at ordinary closing, so
						// complete current owners alone would allow attempt two.
						if n == call || seam != "closing" && n == call+1 {
							return append(rows, qualificationEngineListener{Family: family, Address: "0100007F", Port: 9090, Inode: fd.Inode, Listener: true})
						}
						return rows
					}
					ops := f.ops()
					ops.processes = func(ctx context.Context) ([]qualificationEngineProcess, error) {
						if f.process.scan == 1 {
							// Move the previously observed daemon FD to a real ordinary
							// process; current complete owner validation must pass.
							f.process.ps[1].FDs = f.process.ps[1].FDs[:len(f.process.ps[1].FDs)-1]
							f.process.ps[0].FDs = append(f.process.ps[0].FDs, fd)
						}
						return f.process.collect(ctx)
					}
					if tableFailure {
						table := ops.tables
						ops.tables = func(ctx context.Context) ([]qualificationEngineListener, error) {
							rows, err := table(ctx)
							if f.tableCalls == call {
								return rows, qualificationEngineCleanup(syscall.EACCES, syscall.EIO)
							}
							return rows, err
						}
					}
					got, err := qualificationEngineCollectNativeWith(context.Background(), f.r, ops)
					if err == nil || got.Complete || !errors.Is(err, errQualification) || len(got.Attempts) != attempts || f.tableCalls != call || f.process.scan != scans {
						t.Fatalf("raw table lost earlier owner: complete=%v attempts=%+v tables=%d scans=%d err=%v", got.Complete, got.Attempts, f.tableCalls, f.process.scan, err)
					}
					if tableFailure && (!errors.Is(err, syscall.EACCES) || !errors.Is(err, syscall.EIO)) {
						t.Fatal("table primary/cleanup cause lost", err)
					}
					if attempts == 2 {
						var race *qualificationEngineFDRace
						if !errors.As(got.Attempts[0].Cause, &race) || !race.ProcessStable {
							t.Fatal("original retry cause lost", got.Attempts)
						}
					}
				})
			}
		}
	}

}

func TestQualificationEngineFDWholeRawTablesOrdinaryOwner(t *testing.T) {
	for _, family := range []string{"tcp", "tcp6"} {
		t.Run(family, func(t *testing.T) {
			f := newEngineNativeFixture(t)
			f.process.ps[0].FDs = append(f.process.ps[0].FDs, qualificationEngineProcessFD{FD: 999, Target: "socket:[987654]", Inode: 987654, Device: 9, Mount: 8, Flags: 2})
			f.tables = append(f.tables, qualificationEngineListener{Family: family, Address: "0100007F", Port: 9090, Inode: 987654, Listener: true})
			got, err := f.collect(context.Background())
			if err != nil || !got.Complete || len(got.Attempts) != 1 || f.process.scan != 2 || f.tableCalls != 3 {
				t.Fatal("unrelated complete ordinary owner rejected", got.Attempts, f.tableCalls, f.process.scan, err)
			}
		})
	}
}
