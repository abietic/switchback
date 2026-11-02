package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func qualificationEngineV2Fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../../deploy/experience/engine/testdata/receipt-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestQualificationEngineV2ReadyReceipt(t *testing.T) {
	if _, err := qualificationEngineReceiptV2Decode(qualificationEngineV2Fixture(t)); err != nil {
		t.Fatal(err)
	}
}

func TestQualificationEngineV2LiveReaderRejectsHistoricalReceipt(t *testing.T) {
	f := newQualificationEngineReadbackFixture(t)
	f.files[qualificationEngineReceiptPath] = qualificationEngineFixture(t)
	if err := qualificationVerifyEngineDeploymentWith(context.Background(), f.kit, f.readers()); err == nil {
		t.Fatal("live reader authorized a historical v1 receipt")
	}
	if f.apiCalls != 0 || f.nativeCalls != 0 {
		t.Fatal("historical receipt reached native/API reader")
	}
	if _, err := qualificationEngineReceiptDecode(qualificationEngineFixture(t)); err != nil {
		t.Fatal("historical v1 route rejected valid old receipt", err)
	}
	if _, err := qualificationEngineReceiptV2Decode(qualificationEngineFixture(t)); err == nil {
		t.Fatal("current v2 decoder accepted v1")
	}
}

func TestQualificationEngineV2SharedCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../../deploy/experience/engine/testdata/listener-v2-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if Digest(raw) != "sha256:ff0b4aa5ce193c03ad0ec0bcd31fc81b78bf8ff4c97a5ea31f71f1fa28032485" {
		t.Fatal("shared corpus bytes changed")
	}
	var corpus struct {
		Schema, Provenance string
		Cases              []struct {
			Name               string
			Accepted           bool
			Listeners, Receipt json.RawMessage
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			_, le := qualificationEngineListenersV2Decode(tc.Listeners)
			_, re := qualificationEngineReceiptV2Decode(tc.Receipt)
			if (le == nil && re == nil) != tc.Accepted {
				t.Fatalf("acceptance=%v expected=%v listener=%v receipt=%v", le == nil && re == nil, tc.Accepted, le, re)
			}
			if tc.Accepted {
				accepted++
			}
		})
	}
	if len(corpus.Cases) != 51 || accepted != 3 {
		t.Fatalf("wrong shared cases=%d accepted=%d", len(corpus.Cases), accepted)
	}
	t.Logf("shared corpus cases=%d accepted=%d rejected=%d digest=%s", len(corpus.Cases), accepted, len(corpus.Cases)-accepted, Digest(raw))
}

func qualificationEngineFirstListenerV2(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../../../deploy/experience/engine/testdata/listener-v2-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct{ Listeners json.RawMessage }
	}
	if err = json.Unmarshal(raw, &corpus); err != nil || len(corpus.Cases) == 0 {
		t.Fatal("shared corpus unavailable", err)
	}
	return corpus.Cases[0].Listeners
}

func TestQualificationEngineV2StrictWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    []byte
		decode func([]byte) error
	}{
		{"receipt", qualificationEngineV2Fixture(t), func(raw []byte) error { _, err := qualificationEngineReceiptV2Decode(raw); return err }},
		{"listeners", qualificationEngineFirstListenerV2(t), func(raw []byte) error { _, err := qualificationEngineListenersV2Decode(raw); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original map[string]any
			if err := json.Unmarshal(tc.raw, &original); err != nil {
				t.Fatal(err)
			}
			var paths [][]any
			numberPaths := map[string]bool{}
			var walk func(any, []any)
			walk = func(value any, parts []any) {
				switch v := value.(type) {
				case map[string]any:
					for k, child := range v {
						next := append(append([]any{}, parts...), k)
						paths = append(paths, next)
						if _, ok := child.(float64); ok {
							numberPaths[fmt.Sprint(next)] = true
						}
						walk(child, next)
					}
				case []any:
					for i, child := range v {
						walk(child, append(append([]any{}, parts...), i))
					}
				}
			}
			walk(original, nil)
			for _, parts := range paths {
				for _, kind := range []string{"missing", "null", "float"} {
					if kind == "float" && !numberPaths[fmt.Sprint(parts)] {
						continue
					}
					t.Run(fmt.Sprint(parts)+"/"+kind, func(t *testing.T) {
						var root map[string]any
						json.Unmarshal(tc.raw, &root)
						var parent any = root
						for _, p := range parts[:len(parts)-1] {
							switch k := p.(type) {
							case string:
								parent = parent.(map[string]any)[k]
							case int:
								parent = parent.([]any)[k]
							}
						}
						m := parent.(map[string]any)
						key := parts[len(parts)-1].(string)
						switch kind {
						case "missing":
							delete(m, key)
						case "null":
							m[key] = nil
						case "float":
							n, ok := m[key].(float64)
							if !ok {
								return
							}
							m[key] = json.RawMessage(strconv.FormatFloat(n, 'f', 0, 64) + ".0")
						}
						changed, _ := json.Marshal(root)
						if err := tc.decode(changed); err == nil {
							t.Fatalf("accepted invalid %s property %v", kind, parts)
						}
					})
				}
			}
			for _, bad := range [][]byte{
				bytes.Replace(tc.raw, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1),
				append(append([]byte{}, tc.raw...), []byte(` {}`)...),
				bytes.Replace(tc.raw, []byte(`"device":`), []byte(`"extra":0,"device":`), 1),
				bytes.Replace(tc.raw, []byte(`"device":`), []byte(`"device":1,"device":`), 1),
				bytes.Replace(tc.raw, []byte(`"device":`), []byte(`"device":NaN,"device":`), 1),
			} {
				if err := tc.decode(bad); err == nil {
					t.Fatal("accepted duplicate/unknown/nonfinite/trailing wire bytes")
				}
			}
		})
	}
}

func TestQualificationEngineV2ListenerPhases(t *testing.T) {
	raw := qualificationEngineFirstListenerV2(t)
	for _, tc := range []struct {
		phase                     string
		daemons, sockets, parents int
	}{{"preflight", 0, 0, 0}, {"containerd_ready", 1, 2, 2}, {"engine_ready", 2, 6, 5}} {
		t.Run(tc.phase, func(t *testing.T) {
			var f qualificationEngineListenersV2
			if err := qualificationEngineDecode(raw, &f); err != nil {
				t.Fatal(err)
			}
			f.Phase = tc.phase
			f.Daemons = f.Daemons[:tc.daemons]
			f.Before = f.Before[:tc.daemons]
			f.After = f.After[:tc.daemons]
			f.Sockets = f.Sockets[:tc.sockets]
			f.Parents = f.Parents[:tc.parents]
			f.Unix = f.Unix[:tc.sockets]
			wire, _ := json.Marshal(f)
			if _, err := qualificationEngineListenersV2Decode(wire); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQualificationEngineV2ParentDriftAroundEveryAPI(t *testing.T) {
	for call := 1; call <= 4; call++ {
		t.Run(strconv.Itoa(call), func(t *testing.T) {
			f := newQualificationEngineReadbackFixture(t)
			io := f.readers()
			native := io.native
			io.native = func(ctx context.Context, r qualificationEngineReceiptV2) (qualificationEngineNative, error) {
				n, err := native(ctx, r)
				if f.nativeCalls == call {
					n.SocketParents[3].Inode++
				}
				return n, err
			}
			if err := qualificationVerifyEngineDeploymentWith(context.Background(), f.kit, io); err == nil {
				t.Fatal("accepted private parent drift")
			}
			if f.apiCalls != call-1 {
				t.Fatalf("API passed failing native gate: %d calls", f.apiCalls)
			}
		})
	}
}

func TestQualificationEngineV2CurrentListenerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(*qualificationEngineNative)
	}{
		{"unchanged", true, func(n *qualificationEngineNative) {}},
		{"same protected path established", true, func(n *qualificationEngineNative) {
			l := n.Listeners[3]
			l.Inode = 999
			l.Listener = false
			l.Owners = append([]qualificationEngineOwner{}, l.Owners...)
			l.Owners[0].FD = 93
			n.Listeners = append(n.Listeners, l)
		}},
		{"unrelated unix listener retained", true, func(n *qualificationEngineNative) {
			n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "unix", Path: "/run/unrelated.sock", Inode: 999, Listener: true, Owners: []qualificationEngineOwner{{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/unrelated", Cgroup: "/system.slice/unrelated.service"}}})
		}},
		{"unrelated tcp retained", true, func(n *qualificationEngineNative) {
			n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "tcp", Address: "00000000", Port: 22, Inode: 999, Listener: true, Owners: []qualificationEngineOwner{{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/sshd", Cgroup: "/system.slice/sshd.service"}}})
		}},
		{"root cgroup owner", true, func(n *qualificationEngineNative) {
			n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "unix", Path: "/run/unrelated.sock", Inode: 999, Listener: true, Owners: []qualificationEngineOwner{{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/unrelated", Cgroup: "/"}}})
		}},
		{"oversized owner path", false, func(n *qualificationEngineNative) {
			n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "unix", Path: "/run/unrelated.sock", Inode: 999, Listener: true, Owners: []qualificationEngineOwner{{PID: 55, Start: 5, FD: 9, Exe: "/" + strings.Repeat("a", 512), Cgroup: "/system.slice/unrelated.service"}}})
		}},
		{"oversized unix path", false, func(n *qualificationEngineNative) {
			n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "unix", Path: "/" + strings.Repeat("a", 512), Inode: 999, Listener: true, Owners: []qualificationEngineOwner{{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/unrelated", Cgroup: "/system.slice/unrelated.service"}}})
		}},
		{"same pathname second listener", false, func(n *qualificationEngineNative) {
			l := n.Listeners[3]
			l.Inode = 999
			l.Owners = append([]qualificationEngineOwner{}, l.Owners...)
			l.Owners[0].FD = 93
			n.Listeners = append(n.Listeners, l)
		}},
		{"foreign shared owner", false, func(n *qualificationEngineNative) {
			n.Listeners[3].Owners = append(n.Listeners[3].Owners, qualificationEngineOwner{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/unrelated", Cgroup: "/system.slice/unrelated.service"})
		}},
		{"foreign same path established", false, func(n *qualificationEngineNative) {
			l := n.Listeners[3]
			l.Inode = 999
			l.Listener = false
			l.Owners = append([]qualificationEngineOwner{}, l.Owners...)
			l.Owners = append(l.Owners, qualificationEngineOwner{PID: 55, Start: 5, FD: 9, Exe: "/usr/bin/unrelated", Cgroup: "/system.slice/unrelated.service"})
			n.Listeners = append(n.Listeners, l)
		}},
		{"daemon tcp", false, func(n *qualificationEngineNative) {
			l := n.Listeners[3]
			l.Family = "tcp"
			l.Inode = 999
			l.Address = "00000000"
			l.Port = 1234
			n.Listeners = append(n.Listeners, l)
		}},
		{"daemon tcp6", false, func(n *qualificationEngineNative) {
			l := n.Listeners[3]
			l.Family = "tcp6"
			l.Inode = 999
			l.Address = strings.Repeat("0", 32)
			l.Port = 1234
			n.Listeners = append(n.Listeners, l)
		}},
		{"parent identity", false, func(n *qualificationEngineNative) { n.SocketParents[4].Inode++ }},
		{"sixth socket identity", false, func(n *qualificationEngineNative) { n.Sockets[5].Inode++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQualificationEngineReadbackFixture(t)
			n, err := f.readers().native(context.Background(), f.receipt)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&n)
			if err := qualificationEngineCurrent(f.receipt, n); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func qualificationEngineCollectionFixture(t *testing.T) ([]qualificationEngineListener, []qualificationEngineProcess, []qualificationEngineDaemon) {
	t.Helper()
	f, err := qualificationEngineListenersV2Decode(qualificationEngineFirstListenerV2(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := qualificationEngineV2ListenerRows(f)
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]qualificationEngineProcess, len(f.Daemons))
	for i, d := range f.Daemons {
		processes[i] = qualificationEngineProcess{PID: d.PID, Start: d.Start, Exe: d.Exe, Cgroup: d.Cgroup, FDs: []qualificationEngineProcessFD{}}
	}
	for _, row := range rows {
		for _, o := range row.Owners {
			for i := range processes {
				if processes[i].PID == o.PID {
					processes[i].FDs = append(processes[i].FDs, qualificationEngineProcessFD{FD: o.FD, Target: fmt.Sprintf("socket:[%d]", row.Inode), Flags: 2, Device: 9, Inode: row.Inode})
				}
			}
		}
	}
	// Stable sockets in other families (for example netlink) remain fully covered
	// even though they are absent from Unix/TCP/TCP6 tables.
	processes[1].FDs = append(processes[1].FDs, qualificationEngineProcessFD{FD: 90, Target: "socket:[90000]", Flags: 2, Device: 9, Inode: 90000})
	return rows, processes, f.Daemons
}

func TestQualificationEngineV2CompleteCollectionClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(*[]qualificationEngineListener, *[]qualificationEngineProcess)
	}{
		{"stable complete inventory including netlink", true, func(*[]qualificationEngineListener, *[]qualificationEngineProcess) {}},
		{"process enumeration order", true, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}},
		{"socket descriptor enumeration order", true, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			fds := (*p)[1].FDs
			fds[0], fds[len(fds)-1] = fds[len(fds)-1], fds[0]
		}},
		{"new daemon unix table and fd", false, func(r *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			*r = append(*r, qualificationEngineListener{Family: "unix", Path: "/run/new.sock", Inode: 99999, Listener: true})
			(*p)[1].FDs = append((*p)[1].FDs, qualificationEngineProcessFD{FD: 91, Target: "socket:[99999]", Flags: 2, Device: 9, Inode: 99999})
		}},
		{"new daemon tcp fd outside initial tables", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			(*p)[1].FDs = append((*p)[1].FDs, qualificationEngineProcessFD{FD: 91, Target: "socket:[99999]", Flags: 2, Device: 9, Inode: 99999})
		}},
		{"new daemon tcp6 listener", false, func(r *[]qualificationEngineListener, _ *[]qualificationEngineProcess) {
			*r = append(*r, qualificationEngineListener{Family: "tcp6", Inode: 99999, Listener: true, Address: strings.Repeat("0", 32), Port: 1234})
		}},
		{"removed socket fd", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[1].FDs = (*p)[1].FDs[:1] }},
		{"reassigned unlisted netlink fd", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			fds := (*p)[1].FDs
			fds[len(fds)-1].Target = "socket:[90001]"
			fds[len(fds)-1].Inode++
		}},
		{"FD flags drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].FDs[0].Flags++ }},
		{"FD device drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].FDs[0].Device++ }},
		{"FD inode drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].FDs[0].Inode++ }},
		{"duplicate process", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { *p = append(*p, (*p)[0]) }},
		{"new process without socket FD", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) {
			*p = append(*p, qualificationEngineProcess{PID: 199, Start: 10, Exe: "/usr/bin/true", Cgroup: "/user.slice"})
		}},
		{"process exe drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].Exe += " (deleted)" }},
		{"process cgroup drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].Cgroup += "/other" }},
		{"process start drift", false, func(_ *[]qualificationEngineListener, p *[]qualificationEngineProcess) { (*p)[0].Start++ }},
		{"same pathname new table inode", false, func(r *[]qualificationEngineListener, _ *[]qualificationEngineProcess) { (*r)[0].Inode++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeRows, beforeProcesses, _ := qualificationEngineCollectionFixture(t)
			afterRows, afterProcesses, _ := qualificationEngineCollectionFixture(t)
			tc.change(&afterRows, &afterProcesses)
			if err := qualificationEngineCollectionClosure(beforeRows, afterRows, beforeProcesses, afterProcesses); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestQualificationEngineV2CollectorRoleAndOwnerSeams(t *testing.T) {
	rows, processes, daemons := qualificationEngineCollectionFixture(t)
	paths, err := qualificationEngineDiscoverSocketPaths(rows)
	if err != nil || len(paths) != 6 {
		t.Fatal(paths, err)
	}
	if err := qualificationEngineProcessPolicy(processes, daemons); err != nil {
		t.Fatal(err)
	}
	attached, err := qualificationEngineAttachOwners(rows, processes)
	if err != nil {
		t.Fatal(err)
	}
	if err := qualificationEngineListenerOwnership(daemons, mustQualificationEngineV2(t).Sockets, attached); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"missing dynamic", "duplicate dynamic", "duplicate fixed", "foreign process", "foreign writer", "foreign shim", "cross family duplicate inode"} {
		t.Run(tc, func(t *testing.T) {
			r, p, ds := qualificationEngineCollectionFixture(t)
			switch tc {
			case "missing dynamic":
				r = r[:len(r)-1]
				_, err = qualificationEngineDiscoverSocketPaths(r)
			case "duplicate dynamic":
				l := r[5]
				l.Inode++
				l.Path = "/run/switchback-engine/m1-resident/docker/libnetwork/fedcba987654.sock"
				r = append(r, l)
				_, err = qualificationEngineDiscoverSocketPaths(r)
			case "duplicate fixed":
				l := r[0]
				l.Inode = 99999
				r = append(r, l)
				err = qualificationEnginePairSocketInodes(mustQualificationEngineV2(t).Sockets, r)
			case "foreign process":
				p[1].PID = 199
				err = qualificationEngineProcessPolicy(p, ds)
			case "foreign writer":
				p = append(p, qualificationEngineProcess{PID: 199, Start: 5, Exe: "/usr/bin/writer", Cgroup: "/user.slice", FDs: []qualificationEngineProcessFD{{FD: 5, Target: "/mnt/lima-sbquota/docker/file", Flags: 1}}})
				err = qualificationEngineProcessPolicy(p, ds)
			case "foreign shim":
				p = append(p, qualificationEngineProcess{PID: 199, Start: 5, Exe: "/usr/bin/containerd-shim-runc-v2", Cgroup: "/system.slice/other"})
				err = qualificationEngineProcessPolicy(p, ds)
			case "cross family duplicate inode":
				l := r[0]
				l.Family = "tcp"
				r = append(r, l)
				_, err = qualificationEngineAttachOwners(r, p)
			}
			if err == nil {
				t.Fatal("accepted incomplete/foreign collector inventory")
			}
		})
	}
	for _, tc := range []struct {
		raw   string
		valid bool
	}{{"flags:\t02000002\n", true}, {"flags:\t02000002\nflags:\t02\n", false}, {"flags:\t08\n", false}, {"flags:\t-1\n", false}, {"pos:\t0\n", false}} {
		if _, err := qualificationEngineFDFlags([]byte(tc.raw)); (err == nil) != tc.valid {
			t.Fatalf("flags %q accepted=%v", tc.raw, err == nil)
		}
	}
}

func mustQualificationEngineV2(t *testing.T) qualificationEngineReceiptV2 {
	t.Helper()
	r, err := qualificationEngineReceiptV2Decode(qualificationEngineV2Fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestQualificationEngineV2CompleteProcessStat(t *testing.T) {
	raw := func(pid, start, flags uint64, state string) []byte {
		return []byte(fmt.Sprintf("%d (name with spaces) %s 0 0 0 0 0 %d 0 0 0 0 0 0 0 0 0 0 0 0 %d\n", pid, state, flags, start))
	}
	for _, tc := range []struct {
		name              string
		pid, start, flags uint64
		exe, state        string
		fds               int
		valid             bool
	}{
		{"ordinary", 55, 100, 0, "/usr/bin/sshd", "S", 1, true},
		{"PID1 zero start", 1, 0, 0, "/usr/lib/systemd/systemd", "S", 1, true},
		{"kernel zero start", 2, 0, 0x00200000, "", "S", 0, true},
		{"userspace zero start", 55, 0, 0, "/usr/bin/sshd", "S", 1, false},
		{"missing userspace executable", 55, 100, 0, "", "Z", 0, false},
		{"kernel unexplained fd", 2, 0, 0x00200000, "", "S", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := qualificationEngineParseProcessStat(raw(tc.pid, tc.start, tc.flags, tc.state))
			if err != nil {
				t.Fatal(err)
			}
			if before.PID != tc.pid || before.Start != tc.start || before.Flags != tc.flags {
				t.Fatalf("wrong process stat %+v", before)
			}
			if err := qualificationEngineProcessStatStable(before, before, tc.exe, tc.fds); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	before, err := qualificationEngineParseProcessStat(raw(55, 100, 0, "R"))
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.State = "S"
	if err := qualificationEngineProcessStatStable(before, after, "/usr/bin/sshd", 1); err != nil {
		t.Fatal(err)
	}
	after.State = "Z"
	if err := qualificationEngineProcessStatStable(before, after, "/usr/bin/sshd", 1); err == nil {
		t.Fatal("accepted state drift")
	}
	after = before
	after.Flags = 1
	if err := qualificationEngineProcessStatStable(before, after, "/usr/bin/sshd", 1); err == nil {
		t.Fatal("accepted flag drift")
	}
	for _, bad := range [][]byte{[]byte("55 (x)"), raw(0, 100, 0, "S"), raw(55, 100, 0, "SS"), []byte("55 (x) S\x00\n")} {
		if _, err := qualificationEngineParseProcessStat(bad); err == nil {
			t.Fatal("accepted malformed process stat")
		}
	}
}
