package createdlayeripc

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

const (
	modeType    = 0170000
	modeDir     = 0040000
	modeRegular = 0100000
	modeSocket  = 0140000
)

type fileIdentity struct {
	dev, ino, nlink uint64
	mode, uid, gid  uint32
}
type fileOps interface {
	open(int, string, bool) (int, error)
	stat(int) (fileIdentity, error)
	leaf(int, string) (fileIdentity, error)
	read(int, []byte) (int, error)
	close(int)
	cgroup2(int) bool
}

func secureDirectory(s fileIdentity, private bool) bool {
	return s.mode&modeType == modeDir && s.uid == 0 && s.gid == 0 && s.mode&0022 == 0 && (!private || s.mode&0777 == 0700)
}

// walk pins each directory with O_NOFOLLOW; every exit closes all acquired FDs.
func walk(fs fileOps, path string) (int, error) {
	fd, e := fs.open(-1, "/", true)
	if e != nil {
		return -1, errNative
	}
	fail := func() (int, error) { fs.close(fd); return -1, errNative }
	st, e := fs.stat(fd)
	if e != nil || !secureDirectory(st, false) {
		return fail()
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if name == "" || name == "." || name == ".." {
			return fail()
		}
		next, e := fs.open(fd, name, true)
		if e != nil {
			return fail()
		}
		fs.close(fd)
		fd = next
		st, e = fs.stat(fd)
		if e != nil || !secureDirectory(st, false) {
			return fail()
		}
	}
	return fd, nil
}
func boundedRead(fs fileOps, fd, limit int) ([]byte, error) {
	raw := make([]byte, limit+1)
	n := 0
	for n < len(raw) {
		k, e := fs.read(fd, raw[n:])
		if k < 0 || k > len(raw)-n {
			return nil, errNative
		}
		n += k
		if e == io.EOF || (k == 0 && e == nil) {
			break
		}
		if e != nil {
			return nil, errNative
		}
	}
	if n > limit {
		return nil, errNative
	}
	return raw[:n], nil
}
func readSecureKey(fs fileOps, path string) ([]byte, error) {
	cut := strings.LastIndex(path, "/")
	parent, e := walk(fs, path[:cut])
	if e != nil {
		return nil, errNative
	}
	defer fs.close(parent)
	before, e := fs.leaf(parent, path[cut+1:])
	if e != nil {
		return nil, errNative
	}
	fd, e := fs.open(parent, path[cut+1:], false)
	if e != nil {
		return nil, errNative
	}
	defer fs.close(fd)
	st, e := fs.stat(fd)
	if e != nil || st != before || st.mode&modeType != modeRegular || st.mode&07777 != 0400 || st.uid != 0 || st.gid != 0 || st.nlink != 1 {
		return nil, errNative
	}
	raw, e := boundedRead(fs, fd, 32)
	if e != nil || len(raw) != 32 {
		return nil, errNative
	}
	after, e := fs.stat(fd)
	leaf, e2 := fs.leaf(parent, path[cut+1:])
	if e != nil || e2 != nil || after != st || leaf != st {
		return nil, errNative
	}
	if bytes.Equal(raw, make([]byte, 32)) {
		return nil, errNative
	}
	return raw, nil
}
func socketIdentity(fs fileOps, parent int) (fileIdentity, error) {
	st, e := fs.stat(parent)
	if e != nil || !secureDirectory(st, true) {
		return fileIdentity{}, errNative
	}
	leaf, e := fs.leaf(parent, "observer.sock")
	if e != nil || leaf.mode&modeType != modeSocket || leaf.mode&07777 != 0600 || leaf.uid != 0 || leaf.gid != 0 || leaf.nlink != 1 {
		return fileIdentity{}, errNative
	}
	return leaf, nil
}
func sameSocket(fs fileOps, parent int, identity fileIdentity) bool {
	s, e := socketIdentity(fs, parent)
	if e != nil || s != identity {
		return false
	}
	again, e := walk(fs, "/run/switchback-created-layer")
	if e != nil {
		return false
	}
	defer fs.close(again)
	a, e := fs.stat(parent)
	b, e2 := fs.stat(again)
	return e == nil && e2 == nil && a == b
}

type observerPlacement uint8

const (
	legacyObserverPlacement observerPlacement = iota
	qualificationObserverPlacement
)

func (p observerPlacement) cgroupPath() string {
	switch p {
	case legacyObserverPlacement:
		return ObserverCgroupPath
	case qualificationObserverPlacement:
		return QualificationObserverCgroupPath
	default:
		return ""
	}
}

func placement(fs fileOps, pid int) error {
	return placementFor(fs, pid, legacyObserverPlacement)
}

func placementFor(fs fileOps, pid int, policy observerPlacement) error {
	cgroup := policy.cgroupPath()
	if cgroup == "" {
		return errNative
	}

	p, e := walk(fs, "/proc/"+strconv.Itoa(pid))
	if e != nil {
		return errNative
	}
	defer fs.close(p)
	f, e := fs.open(p, "cgroup", false)
	if e != nil {
		return errNative
	}
	raw, e := boundedRead(fs, f, 4096)
	fs.close(f)
	if e != nil || string(raw) != "0::"+cgroup+"\n" {
		return errNative
	}
	d, e := walk(fs, "/sys/fs/cgroup"+cgroup)
	if e != nil {
		return errNative
	}
	defer fs.close(d)
	if !fs.cgroup2(d) {
		return errNative
	}
	for _, entry := range []struct{ name, value string }{{"cpu.max", "20000 100000"}, {"memory.max", "134217728"}, {"memory.swap.max", "0"}, {"pids.max", "64"}} {
		fd, e := fs.open(d, entry.name, false)
		if e != nil {
			return errNative
		}
		st, se := fs.stat(fd)
		raw, re := boundedRead(fs, fd, 128)
		fs.close(fd)
		if se != nil || st.mode&modeType != modeRegular || st.uid != 0 || st.gid != 0 || st.mode&0022 != 0 || re != nil || string(raw) != entry.value+"\n" {
			return errNative
		}
	}
	return nil
}

func peerAllowed(uid, gid uint32, pid int32, server bool) bool {
	return uid == 0 && gid == 0 && pid >= 0 && (!server || pid > 0)
}
