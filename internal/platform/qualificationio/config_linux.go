//go:build linux && (amd64 || arm64 || 386 || arm)

package qualificationio

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func checkedContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("qualification context required")
	}
	return ctx.Err()
}

// fixedDirectory walks only a compile-time path; /proc magic links are handled
// separately by the bounded current-process observer, never by this reader.
func fixedDirectory(ctx context.Context, p string) (int, error) {
	if err := checkedContext(ctx); err != nil {
		return -1, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(p, "/"), "/")...) {
		if part != "" {
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if e != nil {
				return -1, e
			}
			fd = next
		}
		var s unix.Stat_t
		if unix.Fstat(fd, &s) != nil || s.Uid != 0 || s.Gid != 0 || s.Mode&0022 != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(fd)
			return -1, fmt.Errorf("qualification ancestor unsafe")
		}
		if err := checkedContext(ctx); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

func readControl(ctx context.Context) (Control, error) {
	var zero Control
	dir, err := fixedDirectory(ctx, "/qualification")
	if err != nil {
		return zero, err
	}
	defer unix.Close(dir)
	var mount unix.Statfs_t
	if unix.Fstatfs(dir, &mount) != nil || mount.Flags&unix.ST_RDONLY == 0 {
		return zero, fmt.Errorf("qualification control mount must be readonly")
	}
	var ds unix.Stat_t
	if unix.Fstat(dir, &ds) != nil || ds.Mode&0777 != 0755 {
		return zero, fmt.Errorf("qualification control directory mode invalid")
	}
	fd, err := unix.Openat(dir, "control.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, err
	}
	f := os.NewFile(uintptr(fd), ControlPath)
	defer f.Close()
	var before, after, reopened unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != 0444 || before.Uid != 0 || before.Gid != 0 || before.Nlink != 1 || before.Size < 1 || before.Size > MaxFrameBytes {
		return zero, fmt.Errorf("qualification control unsafe")
	}
	raw, err := readBounded(ctx, f, MaxFrameBytes)
	if err != nil {
		return zero, err
	}
	if unix.Fstat(fd, &after) != nil || int64(len(raw)) != before.Size {
		return zero, fmt.Errorf("qualification control changed")
	}
	before.Atim = after.Atim
	if before != after {
		return zero, fmt.Errorf("qualification control changed")
	}
	next, err := unix.Openat(dir, "control.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, err
	}
	defer unix.Close(next)
	if unix.Fstat(next, &reopened) != nil {
		return zero, fmt.Errorf("qualification control identity unavailable")
	}
	reopened.Atim = after.Atim
	if reopened != after {
		return zero, fmt.Errorf("qualification control replaced")
	}
	fresh, err := fixedDirectory(ctx, "/qualification")
	if err != nil {
		return zero, err
	}
	defer unix.Close(fresh)
	var directoryAfter unix.Stat_t
	if unix.Fstat(fresh, &directoryAfter) != nil {
		return zero, fmt.Errorf("qualification control directory unavailable")
	}
	ds.Atim = directoryAfter.Atim
	if ds != directoryAfter {
		return zero, fmt.Errorf("qualification control directory replaced")
	}
	return DecodeControl(raw)
}

func readBounded(ctx context.Context, r io.Reader, limit int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32768)
	for {
		if err := checkedContext(ctx); err != nil {
			return nil, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			if len(out)+n > limit {
				return nil, fmt.Errorf("qualification input oversized")
			}
			out = append(out, buf[:n]...)
		}
		if err == io.EOF {
			return out, checkedContext(ctx)
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

func readProc(ctx context.Context, p string, limit int) ([]byte, error) {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, fmt.Errorf("qualification proc filesystem invalid")
	}
	return readBounded(ctx, f, limit)
}

// fdChannel never detaches a blocking reader. Poll/read/write share the fixed
// monotonic context deadline, and a closed/broken/unknown channel fails.
type fdChannel struct {
	ctx context.Context
	fd  int
}

func (p fdChannel) ready(events int16) error {
	for {
		if err := checkedContext(p.ctx); err != nil {
			return err
		}
		v := []unix.PollFd{{Fd: int32(p.fd), Events: events}}
		n, err := unix.Poll(v, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if v[0].Revents&events != 0 {
			return nil
		}
		return fmt.Errorf("qualification channel closed")
	}
}
func (p fdChannel) Read(b []byte) (int, error) {
	for {
		if err := p.ready(unix.POLLIN); err != nil {
			return 0, err
		}
		n, err := unix.Read(p.fd, b)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err == nil && n == 0 {
			return 0, io.EOF
		}
		return n, err
	}
}
func (p fdChannel) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		if err := p.ready(unix.POLLOUT); err != nil {
			return total, err
		}
		n, err := unix.Write(p.fd, b[total:])
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if n < 0 && err != nil {
			n = 0
		}
		if n < 0 || n > len(b)-total {
			return total, fmt.Errorf("qualification channel write count invalid")
		}
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func newFDChannel(ctx context.Context, f *os.File) (fdChannel, error) {
	if f == nil {
		return fdChannel{}, fmt.Errorf("qualification channel required")
	}
	fd := int(f.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		return fdChannel{}, err
	}
	return fdChannel{ctx, fd}, nil
}
