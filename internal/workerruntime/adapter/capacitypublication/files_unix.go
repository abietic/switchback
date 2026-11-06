//go:build linux || darwin

package capacitypublication

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// nativeFiles owns only fixed relative leaves and descriptors it creates. The
// Darwin implementation exists for ordinary-file unit mechanics; Open rejects it.
type nativeFiles struct{}

func newNativeFiles() fileSystem { return nativeFiles{} }
func (nativeFiles) openDirectory(path string) (int, fileMeta, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, fileMeta{}, ErrPublicationInvalid
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fileMeta{}, err
	}
	for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...) {
		if part != "" {
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			ce := unix.Close(fd)
			fd = -1
			if e != nil {
				return -1, fileMeta{}, e
			}
			fd = next
			if ce != nil {
				_ = unix.Close(fd)
				return -1, fileMeta{}, ce
			}
		}
		var st unix.Stat_t
		if err = unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return -1, fileMeta{}, err
		}
		if uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 || st.Gid != 0 || uint32(st.Mode)&0022 != 0 {
			_ = unix.Close(fd)
			return -1, fileMeta{}, ErrPublicationStateUnavailable
		}
	}
	m, err := (nativeFiles{}).stat(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, fileMeta{}, err
	}
	return fd, m, nil
}
func (n nativeFiles) canonical(path string, want fileMeta) error {
	fd, m, e := n.openDirectory(path)
	if e != nil {
		return e
	}
	ce := n.close(fd)
	if !sameDirectory(m, want) {
		return ErrPublicationStateUnavailable
	}
	return ce
}
func metaFromStat(s unix.Stat_t) fileMeta {
	return fileMeta{device: uint64(s.Dev), inode: uint64(s.Ino), kind: uint32(s.Mode) & unix.S_IFMT, mode: uint32(s.Mode) & 07777, uid: s.Uid, gid: s.Gid, nlink: uint64(s.Nlink), size: s.Size}
}
func (nativeFiles) stat(fd int) (fileMeta, error) {
	var s unix.Stat_t
	if e := unix.Fstat(fd, &s); e != nil {
		return fileMeta{}, e
	}
	m := metaFromStat(s)
	var fs unix.Statfs_t
	if e := unix.Fstatfs(fd, &fs); e != nil {
		return fileMeta{}, e
	}
	m.fsType = uint64(fs.Type)
	m.deviceName = fmt.Sprintf("%d:%d", unix.Major(m.device), unix.Minor(m.device))
	if runtime.GOOS == "linux" {
		id, e := ownedMountID(fd)
		if e != nil {
			return fileMeta{}, e
		}
		m.mountID = id
	}
	return m, nil
}

// /proc paths are generated solely from a live owned descriptor, never user input.
func ownedMountID(fd int) (uint64, error) {
	f, e := os.Open("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if e != nil {
		return 0, e
	}
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	ce := f.Close()
	if e != nil {
		return 0, e
	}
	if ce != nil {
		return 0, ce
	}
	if len(b) > 4096 {
		return 0, ErrPublicationStateUnavailable
	}
	var id uint64
	seen := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "mnt_id:") {
			if seen {
				return 0, ErrPublicationStateUnavailable
			}
			seen = true
			v := strings.TrimSpace(strings.TrimPrefix(line, "mnt_id:"))
			id, e = strconv.ParseUint(v, 10, 64)
			if e != nil || id == 0 {
				return 0, ErrPublicationStateUnavailable
			}
		}
	}
	if !seen {
		return 0, ErrPublicationStateUnavailable
	}
	return id, nil
}
func (n nativeFiles) openLeaf(dir int, name string) (int, fileMeta, error) {
	if !fixedLeaf(name) {
		return -1, fileMeta{}, ErrPublicationInvalid
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if runtime.GOOS == "linux" {
		const linuxOPath = 0x200000
		pin, e := unix.Openat(dir, name, linuxOPath|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return -1, fileMeta{}, e
		}
		pm, e := n.stat(pin)
		if e != nil || pm.kind != regularKind {
			_ = n.close(pin)
			if e != nil {
				return -1, fileMeta{}, e
			}
			return -1, fileMeta{}, ErrPublicationStateUnavailable
		}
		fd, e := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			_ = n.close(pin)
			return -1, fileMeta{}, e
		}
		m, e := n.stat(fd)
		ce := n.close(pin)
		if e != nil || ce != nil || m != pm {
			_ = n.close(fd)
			if e != nil {
				return -1, fileMeta{}, e
			}
			if ce != nil {
				return -1, fileMeta{}, ce
			}
			return -1, fileMeta{}, ErrPublicationStateUnavailable
		}
		return fd, m, nil
	}
	// Darwin mechanics: lstatat rejects special files before any data open;
	// NONBLOCK and the post-open identity check bound a replacement race.
	var st unix.Stat_t
	if e := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return -1, fileMeta{}, e
	}
	before := metaFromStat(st)
	if before.kind != regularKind {
		return -1, fileMeta{}, ErrPublicationStateUnavailable
	}
	fd, e := unix.Openat(dir, name, flags, 0)
	if e != nil {
		return -1, fileMeta{}, e
	}
	m, e := n.stat(fd)
	if e != nil || m.device != before.device || m.inode != before.inode || m.kind != before.kind {
		_ = n.close(fd)
		if e != nil {
			return -1, fileMeta{}, e
		}
		return -1, fileMeta{}, ErrPublicationStateUnavailable
	}
	return fd, m, nil
}
func (n nativeFiles) entry(dir int, name string) (fileMeta, error) {
	fd, m, e := n.openLeaf(dir, name)
	if e != nil {
		return fileMeta{}, e
	}
	return m, n.close(fd)
}
func (n nativeFiles) create(dir int, name string) (int, error) {
	if name != "sequence.next" && name != "capacity.next" {
		return -1, ErrPublicationInvalid
	}
	return unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
}
func (nativeFiles) read(fd, limit int) ([]byte, error) {
	// Pread leaves the descriptor offset stable; the bound includes one overflow byte.
	out := make([]byte, 0, limit+1)
	buf := make([]byte, min(8192, limit+1))
	for len(out) <= limit {
		n, e := unix.Pread(fd, buf[:min(len(buf), limit+1-len(out))], int64(len(out)))
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if e != nil {
			return nil, e
		}
		if n == 0 {
			return out, nil
		}
	}
	return nil, ErrPublicationStateUnavailable
}
func (nativeFiles) write(fd int, b []byte) (int, error) { return unix.Write(fd, b) }
func (nativeFiles) chmod(fd int, mode uint32) error     { return unix.Fchmod(fd, mode) }
func (nativeFiles) sync(fd int) error                   { return unix.Fsync(fd) }
func (nativeFiles) lock(fd int) error {
	e := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(e, unix.EWOULDBLOCK) || errors.Is(e, unix.EAGAIN) {
		return ErrPublicationBusy
	}
	return e
}
func (nativeFiles) rename(dir int, from, to string) error {
	if !((from == "sequence.next" && to == "sequence.bin") || (from == "capacity.next" && to == "capacity.json")) {
		return ErrPublicationInvalid
	}
	return unix.Renameat(dir, from, dir, to)
}
func (nativeFiles) unlink(dir int, name string) error {
	if name != "sequence.next" && name != "capacity.next" && name != "capacity.json" {
		return ErrPublicationInvalid
	}
	return unix.Unlinkat(dir, name, 0)
}
func (nativeFiles) close(fd int) error { return unix.Close(fd) }
func (nativeFiles) list(dir int) ([]string, error) {
	fd, e := unix.Openat(dir, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "publication-directory")
	names, e := f.Readdirnames(9)
	ce := f.Close()
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	if len(names) > 8 {
		return nil, ErrPublicationStateUnavailable
	}
	return names, nil
}
