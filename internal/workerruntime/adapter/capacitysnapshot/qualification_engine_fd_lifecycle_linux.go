//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Only these fixed bindings are selected by live collection. No constructor,
// environment switch, target-content reads, pidfd_getfd or writable pin exists.
type qualificationEngineLinuxFDDirectory struct {
	file *os.File
	pid  uint64
}

func (d *qualificationEngineLinuxFDDirectory) names(ctx context.Context, limit int) ([]string, error) {
	if e := qualificationContext(ctx); e != nil {
		return nil, e
	}
	names, e := d.file.Readdirnames(limit)
	if e == io.EOF {
		e = nil
	}
	return names, e
}
func (d *qualificationEngineLinuxFDDirectory) close(ctx context.Context) error {
	return qualificationEngineCleanup(qualificationContext(ctx), d.file.Close())
}
func qualificationEngineLinuxFDObject(st unix.Stat_t) qualificationEngineFDObject {
	return qualificationEngineFDObject{Device: uint64(st.Dev), Inode: st.Ino, Mode: st.Mode}
}
func (d *qualificationEngineLinuxFDDirectory) metadata() qualificationEngineFDOps {
	return qualificationEngineFDOps{
		openPin: func(ctx context.Context, name string) (int, error) {
			if e := qualificationContext(ctx); e != nil {
				return -1, e
			}
			fd, e := unix.Openat(int(d.file.Fd()), name, unix.O_PATH|unix.O_CLOEXEC, 0)
			return fd, qualificationEngineSourceError("pin-open", e)
		},
		pinStat: func(ctx context.Context, fd int) (qualificationEngineFDObject, error) {
			if e := qualificationContext(ctx); e != nil {
				return qualificationEngineFDObject{}, e
			}
			var st unix.Stat_t
			e := unix.Fstat(fd, &st)
			return qualificationEngineLinuxFDObject(st), e
		},
		pinLink: func(ctx context.Context, fd int) (string, error) {
			return qualificationEngineLinuxReadlink(ctx, unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), "")
		},
		pinInfo: func(ctx context.Context, fd int) ([]byte, error) {
			return qualificationEngineLinuxMetadataRead(ctx, "/proc/self/fdinfo/"+strconv.Itoa(fd), 16384, "")
		},
		sourceLink: func(ctx context.Context, name string) (string, error) {
			return qualificationEngineLinuxReadlink(ctx, int(d.file.Fd()), name, "source-readlink")
		},
		sourceStat: func(ctx context.Context, name string) (qualificationEngineFDObject, error) {
			if e := qualificationContext(ctx); e != nil {
				return qualificationEngineFDObject{}, e
			}
			var st unix.Stat_t
			e := unix.Fstatat(int(d.file.Fd()), name, &st, 0)
			return qualificationEngineLinuxFDObject(st), qualificationEngineSourceError("source-stat", e)
		},
		sourceInfo: func(ctx context.Context, name string) ([]byte, error) {
			return qualificationEngineLinuxMetadataRead(ctx, qualificationProcPath(d.pid, "fdinfo")+"/"+name, 16384, "source-fdinfo")
		},
		closePin: func(ctx context.Context, fd int) error {
			return qualificationEngineCleanup(qualificationContext(ctx), unix.Close(fd))
		},
	}
}
func qualificationEngineLinuxReadlink(ctx context.Context, dir int, name, source string) (string, error) {
	if e := qualificationContext(ctx); e != nil {
		return "", e
	}
	buf := make([]byte, 4097)
	n, e := unix.Readlinkat(dir, name, buf)
	if e != nil {
		if source != "" {
			e = qualificationEngineSourceError(source, e)
		}
		return "", e
	}
	if n > 4096 {
		return "", errQualification
	}
	return string(buf[:n]), nil
}

// Only proc metadata paths assembled by the fixed binding reach this helper.
// The open/read syscall is classified before cleanup; cleanup never becomes a
// retry even when the primary error is a source disappearance.
func qualificationEngineLinuxMetadataRead(ctx context.Context, name string, limit int64, source string) (raw []byte, err error) {
	if err = qualificationContext(ctx); err != nil {
		return
	}
	fd, e := unix.Open(name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if e != nil {
		if source != "" {
			e = qualificationEngineSourceError(source+"-open", e)
		}
		return nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() {
		err = qualificationEngineCleanup(err, f.Close())
		if ce := qualificationContext(ctx); ce != nil {
			err = qualificationEngineCleanup(err, ce)
		}
	}()
	buf := make([]byte, 4096)
	for {
		if err = qualificationContext(ctx); err != nil {
			return
		}
		n, e := f.Read(buf)
		raw = append(raw, buf[:n]...)
		if int64(len(raw)) > limit {
			return nil, errQualification
		}
		if e == io.EOF {
			return raw, nil
		}
		if e != nil {
			// os.File.Read adds PathError; only unwrap that known syscall boundary.
			cause := e
			if pe, ok := e.(*os.PathError); ok {
				cause = pe.Err
			}
			if source != "" && cause == syscall.ENOENT {
				e = &qualificationEngineSourceVanished{Primitive: source + "-read", Cause: e}
			}
			return nil, e
		}
		if n == 0 {
			return nil, errQualification
		}
	}
}
func qualificationEngineLinuxProcessOps() qualificationEngineProcessOps {
	return qualificationEngineProcessOps{
		pids: qualificationEnginePIDs,
		read: func(ctx context.Context, pid uint64, name string, limit int64) ([]byte, error) {
			return qualificationEngineLinuxMetadataRead(ctx, qualificationProcPath(pid, name), limit, "")
		},
		exe: func(ctx context.Context, pid uint64) (string, error) {
			s, e := qualificationEngineLinuxReadlink(ctx, unix.AT_FDCWD, qualificationProcPath(pid, "exe"), "")
			if e == syscall.ENOENT {
				return "", nil
			}
			if e == nil {
				e = qualificationEngineFDTarget(s)
			}
			return s, e
		},
		openFDs: func(ctx context.Context, pid uint64) (qualificationEngineFDDirectory, error) {
			if e := qualificationContext(ctx); e != nil {
				return nil, e
			}
			fd, e := unix.Open(qualificationProcPath(pid, "fd"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if e != nil {
				return nil, e
			}
			return &qualificationEngineLinuxFDDirectory{file: os.NewFile(uintptr(fd), "proc-fd"), pid: pid}, nil
		},
	}
}
func qualificationEngineCollectNative(ctx context.Context, r qualificationEngineReceiptV2) (qualificationEngineNative, error) {
	return qualificationEngineCollectNativeWith(ctx, r, qualificationEngineNativeOps{
		boot: qualificationEngineLinuxBoot, filesystem: qualificationEngineFilesystemRead, daemon: qualificationEngineObserveDaemon,
		tables: qualificationEngineReadTables, sockets: qualificationEngineSampleSockets, processes: qualificationEngineProcesses,
	})
}

// Engine-specific wrappers preserve metadata and executable-hash close errors;
// unrelated qualification callers retain their existing helpers unchanged.
func qualificationEngineLinuxBoot(ctx context.Context) (string, error) {
	raw, e := qualificationEngineLinuxMetadataRead(ctx, "/proc/sys/kernel/random/boot_id", 64, "")
	if e != nil {
		return "", e
	}
	boot := strings.TrimSpace(string(raw))
	if !qualificationUUIDPattern.MatchString(boot) {
		return "", errQualification
	}
	return boot, qualificationContext(ctx)
}
func qualificationEngineLinuxReadProc(ctx context.Context, pid uint64, leaf string, limit int64) ([]byte, error) {
	if pid == 0 || pid > 1<<31-1 || (leaf != "stat" && leaf != "status" && leaf != "cgroup" && leaf != "mountinfo") || limit <= 0 || limit > 65536 {
		return nil, errQualification
	}
	raw, e := qualificationEngineLinuxMetadataRead(ctx, qualificationProcPath(pid, leaf), limit, "")
	if e != nil {
		return nil, e
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, errQualification
	}
	return raw, nil
}
func qualificationEngineLinuxHashExecutable(ctx context.Context, pid uint64) (result string, err error) {
	p := fmt.Sprintf("/proc/%d/exe", pid)
	name, err := qualificationEngineLinuxReadlink(ctx, unix.AT_FDCWD, p, "")
	if err != nil || strings.HasSuffix(name, " (deleted)") {
		return "", errQualification
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() {
		err = qualificationEngineCleanup(err, f.Close())
		if ce := qualificationContext(ctx); ce != nil {
			err = qualificationEngineCleanup(err, ce)
		}
	}()
	return qualificationHashOpenExecutable(ctx, f)
}

func qualificationEngineCloseFile(ctx context.Context, f *os.File) error {
	return qualificationEngineCleanup(qualificationContext(ctx), f.Close())
}
func qualificationEngineLinuxOpenDirectory(ctx context.Context, path string) (*os.File, []string, error) {
	fd, ids, e := qualificationEngineOpenDirectoryWith(ctx, path, qualificationEngineDirectoryOps{
		openRoot: func(ctx context.Context) (int, error) {
			if e := qualificationContext(ctx); e != nil {
				return -1, e
			}
			return unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		},
		openChild: func(ctx context.Context, fd int, name string) (int, error) {
			if e := qualificationContext(ctx); e != nil {
				return -1, e
			}
			return unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		},
		identity: func(ctx context.Context, fd int) (string, error) {
			st, e := qualificationStat(ctx, fd)
			if e != nil {
				return "", e
			}
			if st.Uid != 0 || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR {
				return "", fmt.Errorf("qualification ancestor unsafe")
			}
			return qualificationIdentity(st), nil
		},
		close: func(ctx context.Context, fd int) error {
			return qualificationEngineCleanup(qualificationContext(ctx), unix.Close(fd))
		},
	})
	if e != nil {
		return nil, nil, e
	}
	return os.NewFile(uintptr(fd), path), ids, nil
}
