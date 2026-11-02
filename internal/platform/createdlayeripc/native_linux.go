//go:build linux && (amd64 || arm64)

package createdlayeripc

import (
	"context"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"strconv"
)

type linuxFiles struct{}

func (linuxFiles) open(parent int, name string, dir bool) (int, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if dir {
		flags |= unix.O_DIRECTORY
	}
	if parent < 0 {
		parent = unix.AT_FDCWD
	}
	return unix.Openat(parent, name, flags, 0)
}
func identity(st unix.Stat_t) fileIdentity {
	return fileIdentity{uint64(st.Dev), st.Ino, uint64(st.Nlink), st.Mode, st.Uid, st.Gid}
}
func (linuxFiles) stat(fd int) (fileIdentity, error) {
	var st unix.Stat_t
	e := unix.Fstat(fd, &st)
	return identity(st), e
}
func (linuxFiles) leaf(fd int, name string) (fileIdentity, error) {
	var st unix.Stat_t
	e := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return identity(st), e
}
func (linuxFiles) read(fd int, b []byte) (int, error) { return unix.Read(fd, b) }
func (linuxFiles) close(fd int)                       { _ = unix.Close(fd) }
func (linuxFiles) cgroup2(fd int) bool {
	var st unix.Statfs_t
	return unix.Fstatfs(fd, &st) == nil && st.Type == unix.CGROUP2_SUPER_MAGIC
}
func nativeKey(path string) ([]byte, error) { return readSecureKey(linuxFiles{}, path) }
func nativePeer(conn net.Conn, server bool) error {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return errNative
	}
	raw, e := u.SyscallConn()
	if e != nil {
		return errNative
	}
	valid := false
	e = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		valid = e == nil && peerAllowed(cred.Uid, cred.Gid, cred.Pid, server)
	})
	if e != nil || !valid {
		return errNative
	}
	return nil
}
func nativeDial(ctx context.Context) (net.Conn, error) {
	fs := linuxFiles{}
	p, e := walk(fs, "/run/switchback-created-layer")
	if e != nil {
		return nil, errNative
	}
	defer fs.close(p)
	st, e := socketIdentity(fs, p)
	if e != nil {
		return nil, errNative
	}
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", "/proc/self/fd/"+strconv.Itoa(p)+"/observer.sock")
	if e != nil {
		return nil, errNative
	}
	if !sameSocket(fs, p, st) {
		conn.Close()
		return nil, errNative
	}
	return conn, nil
}
func nativeListen(policy observerPlacement) (net.Listener, func(), error) {
	return listenFixedFor(endpointOps{files: linuxFiles{}, pid: os.Getpid(), missing: func(e error) bool { return e == unix.ENOENT }, bind: func(p int) (net.Listener, error) {
		ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: "/proc/self/fd/" + strconv.Itoa(p) + "/observer.sock", Net: "unix"})
		if e != nil {
			return nil, e
		}
		ln.SetUnlinkOnClose(false)
		return ln, nil
	}, chmod: func(p int) error { return unix.Fchmodat(p, "observer.sock", 0600, unix.AT_SYMLINK_NOFOLLOW) }, unlink: func(p int) error { return unix.Unlinkat(p, "observer.sock", 0) }}, policy)
}
