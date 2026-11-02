//go:build linux && (amd64 || arm64)

package capacitypublisher

import (
	"fmt"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type nativePublisherFiles struct{}

func newPublisherFiles() publisherFiles { return nativePublisherFiles{} }
func publisherNativeMeta(s unix.Stat_t) publisherMeta {
	return publisherMeta{device: uint64(s.Dev), inode: s.Ino, mode: s.Mode, uid: s.Uid, gid: s.Gid, links: uint64(s.Nlink), size: s.Size, stamp: fmt.Sprintf("%d:%d:%d:%d", s.Mtim.Sec, s.Mtim.Nsec, s.Ctim.Sec, s.Ctim.Nsec)}
}
func (n nativePublisherFiles) stat(fd int) (publisherMeta, error) {
	var s unix.Stat_t
	if e := unix.Fstat(fd, &s); e != nil {
		return publisherMeta{}, errPublisherUnavailable
	}
	return publisherNativeMeta(s), nil
}
func (n nativePublisherFiles) directory(p string) (int, publisherMeta, error) {
	if !publisherCanonical(p) || p == "/" {
		return -1, publisherMeta{}, errPublisherInvalid
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, publisherMeta{}, errPublisherUnavailable
	}
	for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(p, "/"), "/")...) {
		if part != "" {
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			ce := unix.Close(fd)
			if e != nil || ce != nil {
				if e == nil {
					unix.Close(next)
				}
				return -1, publisherMeta{}, errPublisherUnavailable
			}
			fd = next
		}
		m, e := n.stat(fd)
		if e != nil || m.uid != 0 || m.gid != 0 || m.mode&unix.S_IFMT != unix.S_IFDIR || m.mode&0022 != 0 {
			unix.Close(fd)
			return -1, publisherMeta{}, errPublisherInvalid
		}
	}
	m, e := n.stat(fd)
	if e != nil {
		unix.Close(fd)
		return -1, publisherMeta{}, e
	}
	return fd, m, nil
}
func (n nativePublisherFiles) leaf(dir int, name string) (int, publisherMeta, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return -1, publisherMeta{}, errPublisherInvalid
	}
	// O_PATH pins the inode without opening FIFO/device data. Only a validated
	// regular root-owned read-only inode may be reopened via our own live FD.
	pin, e := unix.Openat(dir, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, publisherMeta{}, errPublisherUnavailable
	}
	pm, e := n.stat(pin)
	if e != nil || !pm.leafOK(capacitysnapshot.MaxBytes) {
		unix.Close(pin)
		return -1, publisherMeta{}, errPublisherInvalid
	}
	fd, e := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		unix.Close(pin)
		return -1, publisherMeta{}, errPublisherUnavailable
	}
	m, e := n.stat(fd)
	ce := unix.Close(pin)
	if e != nil || ce != nil || m != pm {
		unix.Close(fd)
		return -1, publisherMeta{}, errPublisherInvalid
	}
	return fd, m, nil
}
func (n nativePublisherFiles) read(fd, limit int) ([]byte, error) {
	if limit <= 0 || limit > capacitysnapshot.MaxBytes {
		return nil, errPublisherInvalid
	}
	raw := make([]byte, 0, limit+1)
	buf := make([]byte, min(8192, limit+1))
	defer clear(buf)
	for len(raw) <= limit {
		n, e := unix.Pread(fd, buf[:min(len(buf), limit+1-len(raw))], int64(len(raw)))
		if n > 0 {
			raw = append(raw, buf[:n]...)
		}
		if e != nil {
			clear(raw)
			return nil, errPublisherUnavailable
		}
		if n == 0 {
			return raw, nil
		}
	}
	clear(raw)
	return nil, errPublisherInvalid
}
func (n nativePublisherFiles) list(dir int) ([]string, error) {
	fd, e := unix.Openat(dir, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, errPublisherUnavailable
	}
	f := os.NewFile(uintptr(fd), "publisher-config")
	names, e := f.Readdirnames(8)
	ce := f.Close()
	if (e != nil && e != io.EOF) || ce != nil || len(names) > 7 {
		return nil, errPublisherInvalid
	}
	return names, nil
}
func (nativePublisherFiles) close(fd int) error { return unix.Close(fd) }
