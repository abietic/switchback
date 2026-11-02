package createdlayeripc

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"testing"
)

type recordFiles struct {
	paths          map[int]string
	nodes          map[string]*fileIdentity
	held           map[int]*fileIdentity
	contents       map[int][]byte
	data           map[string][]byte
	offset         map[int]int
	next           int
	opened, closed int
	fail           string
	cg             bool
	drift          bool
	reads          int
}

func newRecordFiles() *recordFiles {
	r := &recordFiles{paths: map[int]string{}, nodes: map[string]*fileIdentity{}, held: map[int]*fileIdentity{}, contents: map[int][]byte{}, data: map[string][]byte{}, offset: map[int]int{}, cg: true}
	dirs := []string{"/", "/run", "/run/switchback-observer-auth", "/etc", "/etc/switchback", "/etc/switchback/created-layer-observer", "/run/switchback-created-layer", "/proc", "/proc/42", "/sys", "/sys/fs", "/sys/fs/cgroup", "/sys/fs/cgroup/switchback.slice", "/sys/fs/cgroup" + CoreCgroupPath, "/sys/fs/cgroup" + ObserverCgroupPath}
	for i, p := range dirs {
		r.nodes[p] = &fileIdentity{1, uint64(i + 1), 1, modeDir | 0755, 0, 0}
	}
	p := "/run/switchback-created-layer"
	v := r.nodes[p]
	v.mode = modeDir | 0700
	r.nodes[p] = v
	for _, p := range []string{clientKeyPath, serverKeyPath} {
		r.nodes[p] = &fileIdentity{1, 100, 1, modeRegular | 0400, 0, 0}
		r.data[p] = bytes.Repeat([]byte{23}, 32)
	}
	r.nodes[socketPath] = &fileIdentity{1, 101, 1, modeSocket | 0600, 0, 0}
	r.nodes["/proc/42/cgroup"] = &fileIdentity{1, 102, 1, modeRegular | 0444, 0, 0}
	r.data["/proc/42/cgroup"] = []byte("0::" + ObserverCgroupPath + "\n")
	for i, e := range []struct{ n, v string }{{"cpu.max", "20000 100000"}, {"memory.max", "134217728"}, {"memory.swap.max", "0"}, {"pids.max", "64"}} {
		p := "/sys/fs/cgroup" + ObserverCgroupPath + "/" + e.n
		r.nodes[p] = &fileIdentity{2, uint64(200 + i), 1, modeRegular | 0644, 0, 0}
		r.data[p] = []byte(e.v + "\n")
	}
	return r
}
func (r *recordFiles) open(parent int, n string, dir bool) (int, error) {
	p := n
	if parent >= 0 {
		p = path.Join(r.paths[parent], n)
	}
	v, ok := r.nodes[p]
	if !ok || r.fail == "open:"+p || (dir && v.mode&modeType != modeDir) || (!dir && v.mode&modeType != modeRegular) {
		return -1, errors.New("secret-canary")
	}
	r.next++
	fd := r.next
	r.paths[fd] = p
	r.held[fd] = v
	r.contents[fd] = r.data[p]
	r.opened++
	return fd, nil
}
func (r *recordFiles) stat(fd int) (fileIdentity, error) {
	p := r.paths[fd]
	if r.fail == "stat:"+p {
		return fileIdentity{}, errors.New("secret-canary")
	}
	return *r.held[fd], nil
}
func (r *recordFiles) leaf(fd int, n string) (fileIdentity, error) {
	p := path.Join(r.paths[fd], n)
	node, ok := r.nodes[p]
	if !ok {
		return fileIdentity{}, errors.New("missing")
	}
	v := *node
	if r.drift && r.reads > 0 {
		v.ino++
	}
	return v, nil
}
func (r *recordFiles) read(fd int, b []byte) (int, error) {
	r.reads++
	p := r.paths[fd]
	if r.fail == "read:"+p {
		return 0, errors.New("secret-canary")
	}
	v := r.contents[fd]
	off := r.offset[fd]
	if off >= len(v) {
		return 0, io.EOF
	}
	n := copy(b, v[off:])
	r.offset[fd] += n
	return n, nil
}
func (r *recordFiles) close(fd int) {
	if _, ok := r.paths[fd]; !ok {
		panic("double close")
	}
	delete(r.paths, fd)
	delete(r.held, fd)
	delete(r.contents, fd)
	delete(r.offset, fd)
	r.closed++
}
func (r *recordFiles) cgroup2(int) bool { return r.cg }
func assertClosed(t *testing.T, r *recordFiles) {
	t.Helper()
	if r.opened != r.closed || len(r.paths) != 0 || len(r.held) != 0 || len(r.contents) != 0 || len(r.offset) != 0 {
		t.Fatalf("FD leak: %d/%d", r.opened, r.closed)
	}
}
func TestSecureKeysRecorder(t *testing.T) {
	for _, kind := range []string{"good", "symlink", "writable_ancestor", "wrongowner", "wronggroup", "link", "permissions", "oversize", "short", "zero", "drift", "readerror", "openerror", "staterror"} {
		t.Run(kind, func(t *testing.T) {
			r := newRecordFiles()
			p := clientKeyPath
			v := r.nodes[p]
			switch kind {
			case "symlink":
				v.mode = 0120400
			case "wrongowner":
				v.uid = 1
			case "wronggroup":
				v.gid = 1
			case "link":
				v.nlink = 2
			case "permissions":
				v.mode = modeRegular | 0600
			case "writable_ancestor":
				d := r.nodes["/run"]
				d.mode |= 0020
				r.nodes["/run"] = d
			case "oversize":
				r.data[p] = make([]byte, 33)
			case "short":
				r.data[p] = make([]byte, 31)
			case "zero":
				r.data[p] = make([]byte, 32)
			case "drift":
				r.drift = true
			case "readerror":
				r.fail = "read:" + p
			case "openerror":
				r.fail = "open:" + p
			case "staterror":
				r.fail = "stat:" + p
			}
			r.nodes[p] = v
			key, e := readSecureKey(r, p)
			if (e == nil) != (kind == "good") {
				t.Fatal("key policy", kind, e)
			}
			if kind == "good" && !bytes.Equal(key, fixtureKey()) {
				t.Fatal("wrong key")
			}
			if e != nil && strings.Contains(e.Error(), "secret-canary") {
				t.Fatal("leak")
			}
			assertClosed(t, r)
		})
	}
}
func TestPlacementRecorderExactLimitsAndFilesystem(t *testing.T) {
	for _, kind := range []string{"good", "path", "multiple", "cpu.max", "memory.max", "memory.swap.max", "pids.max", "plainfs", "missing", "symlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			r := newRecordFiles()
			base := "/sys/fs/cgroup" + ObserverCgroupPath
			switch kind {
			case "path":
				r.data["/proc/42/cgroup"] = []byte("0::/elsewhere\n")
			case "multiple":
				r.data["/proc/42/cgroup"] = append(r.data["/proc/42/cgroup"], []byte("1:x:y\n")...)
			case "cpu.max", "memory.max", "memory.swap.max", "pids.max":
				r.data[base+"/"+kind] = []byte("max\n")
			case "plainfs":
				r.cg = false
			case "missing":
				delete(r.nodes, base+"/memory.max")
			case "symlink":
				v := r.nodes[base+"/cpu.max"]
				v.mode = 0120444
				r.nodes[base+"/cpu.max"] = v
			case "writable":
				v := r.nodes[base]
				v.mode |= 0002
				r.nodes[base] = v
			}
			e := placement(r, 42)
			if (e == nil) != (kind == "good") {
				t.Fatal("placement", kind, e)
			}
			assertClosed(t, r)
		})
	}
}
func TestSocketIdentityAndDriftRecorder(t *testing.T) {
	for _, kind := range []string{"good", "owner", "group", "link", "mode", "symlink", "private", "leaf_only", "parent_only"} {
		t.Run(kind, func(t *testing.T) {
			r := newRecordFiles()
			v := r.nodes[socketPath]
			switch kind {
			case "owner":
				v.uid = 1
			case "group":
				v.gid = 1
			case "link":
				v.nlink = 2
			case "mode":
				v.mode = modeSocket | 0660
			case "symlink":
				v.mode = 0120600
			case "private":
				p := "/run/switchback-created-layer"
				d := r.nodes[p]
				d.mode = modeDir | 0755
				r.nodes[p] = d
			}
			r.nodes[socketPath] = v
			fd, e := walk(r, "/run/switchback-created-layer")
			if e != nil {
				t.Fatal(e)
			}
			original := fileIdentity{1, 101, 1, modeSocket | 0600, 0, 0}
			originalParent, err := r.stat(fd)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "leaf_only" {
				v.ino++
				r.nodes[socketPath] = v
			}
			if kind == "parent_only" {
				before, err := r.stat(fd)
				if err != nil {
					t.Fatal(err)
				}
				replacement := *r.nodes["/run/switchback-created-layer"]
				replacement.ino++
				r.nodes["/run/switchback-created-layer"] = &replacement
				leaf, err := socketIdentity(r, fd)
				if err != nil || leaf != original {
					t.Fatal("parent-only fixture changed leaf", err)
				}
				held, err := r.stat(fd)
				if err != nil || held != before {
					t.Fatal("recorder cannot retain opened parent identity", err)
				}
				reopened, err := walk(r, "/run/switchback-created-layer")
				if err != nil {
					t.Fatal(err)
				}
				current, err := r.stat(reopened)
				r.close(reopened)
				if err != nil || current.ino == held.ino || current.dev != held.dev {
					t.Fatal("recorder did not expose replacement parent", err)
				}
			}
			if kind == "leaf_only" {
				parent, err := r.stat(fd)
				if err != nil || parent != originalParent {
					t.Fatal("leaf-only fixture changed parent", err)
				}
			}
			ok := sameSocket(r, fd, original)
			r.close(fd)
			if ok != (kind == "good") {
				t.Fatal("socket policy", kind)
			}
			assertClosed(t, r)
		})
	}
}
func TestPeerPolicy(t *testing.T) {
	for _, server := range []bool{false, true} {
		for _, pid := range []int32{-1, 0, 1, 42} {
			for _, uid := range []uint32{0, 1} {
				for _, gid := range []uint32{0, 1} {
					want := uid == 0 && gid == 0 && pid >= 0 && (!server || pid > 0)
					if peerAllowed(uid, gid, pid, server) != want {
						t.Fatal("peer", strconv.Itoa(int(pid)))
					}
				}
			}
		}
	}
}
func TestListenGateAndOwnedCleanupRecorder(t *testing.T) {
	for _, kind := range []string{"good", "placement", "existing", "chmod", "drift", "cleanup_leaf_only", "cleanup_parent_only"} {
		t.Run(kind, func(t *testing.T) {
			r := newRecordFiles()
			if kind != "existing" {
				delete(r.nodes, socketPath)
			}
			if kind == "placement" {
				r.cg = false
			}
			binds, unlinks, boundParent := 0, 0, 0
			ln := &pipeListener{closed: make(chan struct{})}
			ops := endpointOps{files: r, pid: 42, missing: func(e error) bool { return e != nil }, bind: func(p int) (net.Listener, error) {
				binds++
				boundParent = p
				if len(r.paths) == 0 {
					t.Fatal("parent not held")
				}
				r.nodes[socketPath] = &fileIdentity{1, 999, 1, modeSocket | 0755, 0, 0}
				return ln, nil
			}, chmod: func(p int) error {
				if kind == "chmod" {
					return errors.New("secret-canary")
				}
				v := r.nodes[socketPath]
				v.mode = modeSocket | 0600
				if kind == "drift" {
					v.ino++
				}
				r.nodes[socketPath] = v
				return nil
			}, unlink: func(p int) error { unlinks++; delete(r.nodes, socketPath); return nil }}
			got, cleanup, e := listenFixed(ops)
			if e == nil {
				if kind != "good" && kind != "cleanup_leaf_only" && kind != "cleanup_parent_only" {
					t.Fatal("accepted", kind)
				}
				if kind == "cleanup_leaf_only" {
					v := r.nodes[socketPath]
					v.ino++
					r.nodes[socketPath] = v
				}
				if kind == "cleanup_parent_only" {
					originalLeaf, err := socketIdentity(r, boundParent)
					if err != nil {
						t.Fatal(err)
					}
					replacement := *r.nodes["/run/switchback-created-layer"]
					replacement.ino++
					r.nodes["/run/switchback-created-layer"] = &replacement
					if *r.nodes[socketPath] != originalLeaf {
						t.Fatal("parent cleanup fixture changed leaf")
					}
				}
				got.Close()
				cleanup()
			} else if kind == "good" || kind == "cleanup_leaf_only" || kind == "cleanup_parent_only" {
				t.Fatal(e)
			}
			if (kind == "placement" || kind == "existing") && binds != 0 {
				t.Fatal("listen before identity")
			}
			want := 0
			if kind == "good" || kind == "chmod" {
				want = 1
			}
			if unlinks != want {
				t.Fatal("unlink policy", kind, unlinks)
			}
			if binds > 0 {
				select {
				case <-ln.closed:
				default:
					t.Fatal("listener not closed")
				}
			}
			if kind == "cleanup_leaf_only" || kind == "cleanup_parent_only" {
				if _, exists := r.nodes[socketPath]; !exists {
					t.Fatal("replaced identity removed")
				}
			}
			assertClosed(t, r)
		})
	}
}

func TestRecorderHeldNodeSeesInPlaceMetadataButNotReplacement(t *testing.T) {
	r := newRecordFiles()
	fd, err := walk(r, "/run/switchback-created-layer")
	if err != nil {
		t.Fatal(err)
	}
	original := r.nodes["/run/switchback-created-layer"]
	original.mode |= 01000
	got, err := r.stat(fd)
	if err != nil || got != *original {
		t.Fatal("opened FD lost in-place metadata update", err)
	}
	replacement := *original
	replacement.ino++
	r.nodes["/run/switchback-created-layer"] = &replacement
	got, err = r.stat(fd)
	if err != nil || got != *original {
		t.Fatal("opened FD rebound to replacement", err)
	}
	reopened, err := walk(r, "/run/switchback-created-layer")
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.stat(reopened)
	if err != nil || got != replacement {
		t.Fatal("new FD did not bind replacement", err)
	}
	r.close(reopened)
	r.close(fd)
	assertClosed(t, r)
}
