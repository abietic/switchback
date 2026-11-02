package createdlayeripc

import (
	"context"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"net"
	"testing"
)

const fixtureQualificationCgroup = CoreCgroupPath + "/switchback-qualification-observer.service"

func qualificationPlacementFiles() *recordFiles {
	r := newRecordFiles()
	old := "/sys/fs/cgroup" + ObserverCgroupPath
	next := "/sys/fs/cgroup" + fixtureQualificationCgroup
	copyDir := *r.nodes[old]
	copyDir.ino += 1000
	r.nodes[next] = &copyDir
	for _, name := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max"} {
		copyFile := *r.nodes[old+"/"+name]
		copyFile.ino += 1000
		r.nodes[next+"/"+name] = &copyFile
		r.data[next+"/"+name] = append([]byte(nil), r.data[old+"/"+name]...)
	}
	r.data["/proc/42/cgroup"] = []byte("0::" + fixtureQualificationCgroup + "\n")
	delete(r.nodes, socketPath)
	return r
}

func TestQualificationObserverPlacementBeforeBind(t *testing.T) {
	r := qualificationPlacementFiles()
	s, err := NewQualificationServer(fixtureKey(), fixtureBinding().ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if binds, err := exerciseQualificationPlacement(t, s, r); err != nil || binds != 1 {
		t.Fatal(binds, err)
	}
}

func exerciseQualificationPlacement(t *testing.T, s *Server, r *recordFiles) (int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binds := 0
	s.listen = func(policy observerPlacement) (net.Listener, func(), error) {
		return listenFixedFor(endpointOps{files: r, pid: 42, missing: func(err error) bool { return err != nil }, bind: func(int) (net.Listener, error) {
			binds++
			r.nodes[socketPath] = &fileIdentity{1, 999, 1, modeSocket | 0600, 0, 0}
			cancel()
			return &pipeListener{closed: make(chan struct{})}, nil
		}, chmod: func(int) error { return nil }, unlink: func(int) error { delete(r.nodes, socketPath); return nil }}, policy)
	}
	err := s.Serve(ctx)
	assertClosed(t, r)
	return binds, err

}

func TestQualificationAndLegacyPlacementPoliciesStayDisjoint(t *testing.T) {
	for _, test := range []struct {
		name, cgroup string
		legacy       bool
		good         bool
	}{
		{"qualification", fixtureQualificationCgroup, false, true},
		{"legacy", ObserverCgroupPath, true, true},
		{"qualification-refuses-legacy", ObserverCgroupPath, false, false},
		{"legacy-refuses-qualification", fixtureQualificationCgroup, true, false},
		{"wrong-unit", CoreCgroupPath + "/other.service", false, false},
		{"sibling", CoreCgroupPath + "/switchback-qualification-observer-other.service", false, false},
		{"subpath", fixtureQualificationCgroup + "/child", false, false},
		{"malformed", fixtureQualificationCgroup + "\n1:x:y", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := qualificationPlacementFiles()
			r.data["/proc/42/cgroup"] = []byte("0::" + test.cgroup + "\n")
			ctor := NewQualificationServer
			if test.legacy {
				ctor = NewServer
			}
			s, err := ctor(fixtureKey(), fixtureBinding().ManifestDigest)
			if err != nil {
				t.Fatal(err)
			}
			binds, err := exerciseQualificationPlacement(t, s, r)
			if (err == nil) != test.good || binds != map[bool]int{true: 1, false: 0}[test.good] {
				t.Fatal(binds, err)
			}
		})
	}
	for _, name := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max"} {
		for _, failure := range []string{"wrong", "unlimited", "missing", "owner", "writable"} {
			t.Run(name+"-"+failure, func(t *testing.T) {
				r := qualificationPlacementFiles()
				p := "/sys/fs/cgroup" + fixtureQualificationCgroup + "/" + name
				switch failure {
				case "wrong":
					r.data[p] = []byte("1\n")
				case "unlimited":
					r.data[p] = []byte("max\n")
				case "missing":
					delete(r.nodes, p)
				case "owner":
					r.nodes[p].uid = 1
				case "writable":
					r.nodes[p].mode |= 0020
				}
				s, err := NewQualificationServer(fixtureKey(), fixtureBinding().ManifestDigest)
				if err != nil {
					t.Fatal(err)
				}
				binds, err := exerciseQualificationPlacement(t, s, r)
				if err == nil || binds != 0 {
					t.Fatal("unsafe qualification resource reached bind", binds, err)
				}
			})
		}
	}
}

func TestQualificationPlacementPreservesClientAndServerPeerPolicy(t *testing.T) {
	for _, bad := range []string{"none", "client-uid", "client-gid", "client-pid", "server-uid", "server-gid", "server-pid"} {
		t.Run(bad, func(t *testing.T) {
			s, e := NewQualificationServer(fixtureKey(), fixtureBinding().ManifestDigest)
			if e != nil {
				t.Fatal(e)
			}
			if binds, e := exerciseQualificationPlacement(t, s, qualificationPlacementFiles()); e != nil || binds != 1 {
				t.Fatal(binds, e)
			}
			c, e := NewClient(fixtureKey(), fixtureBinding().ManifestDigest)
			if e != nil {
				t.Fatal(e)
			}
			peer := func(server bool) func(net.Conn, bool) error {
				return func(_ net.Conn, side bool) error {
					if side != server {
						t.Fatal("wrong peer direction")
					}
					uid, gid, pid := uint32(0), uint32(0), int32(42)
					prefix := "client-"
					if server {
						prefix = "server-"
					}
					switch bad {
					case prefix + "uid":
						uid = 1
					case prefix + "gid":
						gid = 1
					case prefix + "pid":
						pid = -1
					}
					if !peerAllowed(uid, gid, pid, server) {
						return errNative
					}
					return nil
				}
			}
			c.peer, s.peer = peer(false), peer(true)
			samples := 0
			s.collect = func(ctx context.Context, b Binding) capacityprobe.CreatedLayerCollection {
				samples++
				return partialCollector(ctx, b)
			}
			_, e = exchange(t, c, s, context.Background(), fixtureBinding())
			if (e == nil) != (bad == "none") || samples != map[bool]int{true: 1, false: 0}[bad == "none"] {
				t.Fatal(bad, samples, e)
			}
		})
	}
}
