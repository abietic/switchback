//go:build linux && (amd64 || arm64 || 386 || arm)

package qualificationio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func observeLocal(ctx context.Context) (LocalProcess, error) {
	var p LocalProcess
	p.PID = uint64(unix.Getpid())
	p.TGID = p.PID
	p.TID = uint64(unix.Gettid())
	base := fmt.Sprintf("/proc/%d", p.PID)
	task := fmt.Sprintf("%s/task/%d", base, p.TID)
	var err error
	if p.Snapshot.Stat, err = readProc(ctx, base+"/stat", 65536); err != nil {
		return p, err
	}
	if p.Snapshot.ThreadStat, err = readProc(ctx, task+"/stat", 65536); err != nil {
		return p, err
	}
	if p.Snapshot.Status, err = readProc(ctx, task+"/status", 65536); err != nil {
		return p, err
	}
	if p.Snapshot.Cgroup, err = readProc(ctx, task+"/cgroup", 65536); err != nil {
		return p, err
	}
	if p.Snapshot.MountInfo, err = readProc(ctx, task+"/mountinfo", 128<<10); err != nil {
		return p, err
	}
	boot, err := readProc(ctx, "/proc/sys/kernel/random/boot_id", 64)
	if err != nil {
		return p, err
	}
	p.Boot = strings.TrimSuffix(string(boot), "\n")
	if !uuidRE.MatchString(p.Boot) {
		return p, fmt.Errorf("qualification boot invalid")
	}
	p.StartTicks, err = statTicks(p.Snapshot.Stat, p.PID)
	if err != nil {
		return p, err
	}
	p.ThreadStartTicks, err = statTicks(p.Snapshot.ThreadStat, p.TID)
	if err != nil {
		return p, err
	}
	f, elf, err := openExecutable(ctx, "/proc/self/exe", "", true)
	if err != nil {
		return p, err
	}
	f.Close()
	p.Snapshot.ExeDigest = elf.Digest
	return p, nil
}

func verifyWorkerResources(ctx context.Context, c Control, p LocalProcess) error {
	base := "/sys/fs/cgroup"
	if string(p.Snapshot.Cgroup) != "0::/\n" {
		base += "/switchback.slice/switchback-workers.slice/docker-" + c.ContainerID + ".scope"
	}
	dir, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var fs unix.Statfs_t
	if unix.Fstatfs(dir, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("qualification cgroup filesystem invalid")
	}
	for _, v := range []struct{ name, want string }{{"cpu.max", "200000 100000\n"}, {"memory.max", "4294967296\n"}, {"memory.swap.max", "0\n"}, {"pids.max", "512\n"}} {
		fd, e := unix.Openat(dir, v.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), v.name)
		raw, e := readBounded(ctx, f, 128)
		f.Close()
		if e != nil || string(raw) != v.want {
			return fmt.Errorf("qualification resource readback invalid")
		}
	}
	return nil
}

// Fixed helper files are opened no-follow and hashed in full before actual
// exec. /proc/self/exe is the sole intentional proc magic-link exception.
func openExecutable(ctx context.Context, p, artifact string, proc bool) (*os.File, ELF, error) {
	var out ELF
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK
	if !proc {
		flags |= unix.O_NOFOLLOW
	}
	fd := -1
	var err error
	if proc {
		if p != "/proc/self/exe" {
			return nil, out, fmt.Errorf("qualification executable path invalid")
		}
		fd, err = unix.Open(p, flags, 0)
	} else {
		if p != NativeHelperPath && p != CompatHelperPath {
			return nil, out, fmt.Errorf("qualification executable path invalid")
		}
		dir, e := fixedDirectory(ctx, "/usr/local/bin")
		if e != nil {
			return nil, out, e
		}
		defer unix.Close(dir)
		name := "switchback-qualification-native"
		if p == CompatHelperPath {
			name = "switchback-qualification-compat"
		}
		fd, err = unix.Openat(dir, name, flags, 0)
	}
	if err != nil {
		return nil, out, err
	}
	f := os.NewFile(uintptr(fd), p)
	fail := func(err error) (*os.File, ELF, error) { f.Close(); return nil, ELF{}, err }
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 || before.Gid != 0 || before.Mode&0022 != 0 || before.Nlink != 1 || before.Size < 52 || before.Size > 256<<20 {
		return fail(fmt.Errorf("qualification executable unsafe"))
	}
	h := sha256.New()
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil || n != 64 {
		return fail(fmt.Errorf("qualification ELF short"))
	}
	h.Write(head)
	buf := make([]byte, 65536)
	total := int64(64)
	for {
		if err := checkedContext(ctx); err != nil {
			return fail(err)
		}
		n, e := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
			if total > before.Size {
				return fail(fmt.Errorf("qualification executable grew"))
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(e)
		}
		if n == 0 {
			return fail(io.ErrNoProgress)
		}
	}
	if unix.Fstat(fd, &after) != nil {
		return fail(fmt.Errorf("qualification executable identity unavailable"))
	}
	before.Atim = after.Atim
	if before != after || total != before.Size {
		return fail(fmt.Errorf("qualification executable changed"))
	}
	out = ELF{Artifact: artifact, Digest: fmt.Sprintf("sha256:%x", h.Sum(nil)), Class: uint64(head[4]), Machine: uint64(binary.LittleEndian.Uint16(head[18:20]))}
	size := 64
	if out.Class == 1 {
		size = 52
	}
	out.Header = append([]byte{}, head[:size]...)
	offset := 52
	if out.Class == 1 {
		offset = 40
	}
	if string(head[:4]) != "\x7fELF" || (out.Class != 1 && out.Class != 2) || head[5] != 1 || head[6] != 1 || binary.LittleEndian.Uint32(head[20:24]) != 1 || (binary.LittleEndian.Uint16(head[16:18]) != 2 && binary.LittleEndian.Uint16(head[16:18]) != 3) || int(binary.LittleEndian.Uint16(head[offset:offset+2])) != size {
		return fail(fmt.Errorf("qualification ELF invalid"))
	}
	return f, out, nil
}

func helperABI(c Control) (string, string, string, uint64, uint64, error) {
	switch runtime.GOARCH {
	case "amd64":
		if c.Architecture == "amd64" {
			return "native", "native64", "worker-native-helper", 2, 62, nil
		}
	case "arm64":
		if c.Architecture == "arm64" {
			return "native", "native64", "worker-native-helper", 2, 183, nil
		}
	case "386":
		if c.Architecture == "amd64" {
			return "compat32", "compat32", "worker-compat-helper", 1, 3, nil
		}
	case "arm":
		if c.Architecture == "arm64" {
			return "compat32", "compat32", "worker-compat-helper", 1, 40, nil
		}
	}
	return "", "", "", 0, 0, fmt.Errorf("qualification helper architecture invalid")
}
