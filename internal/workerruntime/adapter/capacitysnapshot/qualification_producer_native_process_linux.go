//go:build linux

package capacitysnapshot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

func qualificationReadProc(ctx context.Context, pid uint64, leaf string, limit int64) ([]byte, error) {
	if pid == 0 || pid > 1<<31-1 || (leaf != "stat" && leaf != "status" && leaf != "cgroup" && leaf != "mountinfo") || limit <= 0 || limit > 65536 {
		return nil, errQualification
	}
	if err := qualificationContext(ctx); err != nil {
		return nil, err
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/%s", pid, leaf))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) == 0 || int64(len(b)) > limit || b[len(b)-1] != '\n' {
		return nil, errQualification
	}
	return b, qualificationContext(ctx)
}

func qualificationHashProcExecutable(ctx context.Context, pid uint64) (string, error) {
	p := fmt.Sprintf("/proc/%d/exe", pid)
	name, err := os.Readlink(p)
	if err != nil || strings.HasSuffix(name, " (deleted)") {
		return "", errQualification
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return qualificationHashOpenExecutable(ctx, f)
}

// qualificationHashOpenExecutable hashes an already-open executable. The caller
// retains ownership and its existing close/error policy.
func qualificationHashOpenExecutable(ctx context.Context, f *os.File) (string, error) {
	var before, after unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > 512<<20 {
		return "", errQualification
	}
	h := sha256.New()
	b := make([]byte, 32768)
	var copied int64
	for {
		if err := qualificationContext(ctx); err != nil {
			return "", err
		}
		n, err := f.Read(b)
		copied += int64(n)
		if copied > before.Size {
			return "", errQualification
		}
		if _, e := h.Write(b[:n]); e != nil {
			return "", e
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if err := unix.Fstat(int(f.Fd()), &after); err != nil || qualificationIdentity(before) != qualificationIdentity(after) || copied != before.Size {
		return "", errQualification
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), qualificationContext(ctx)
}

func qualificationObserveFrozenRole(ctx context.Context, role qualificationRole) (qualificationFrozenProcess, error) {
	webLeader, e := qualificationCoreEngineLeader(ctx, role)
	if e != nil {
		return qualificationFrozenProcess{}, e
	}
	if !absolute(role.CgroupPath) || !digestPattern.MatchString(role.BinaryDigest) || role.Role == "" {
		return qualificationFrozenProcess{}, errQualification
	}
	entries, err := os.ReadDir("/proc")
	if err != nil || len(entries) > 65536 {
		return qualificationFrozenProcess{}, errQualification
	}
	boot, err := qualificationGuestBoot(ctx)
	if err != nil {
		return qualificationFrozenProcess{}, err
	}
	expectedGroup := "0::" + strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup") + "\n"
	var selected qualificationFrozenProcess
	for _, ent := range entries {
		if err := qualificationContext(ctx); err != nil {
			return qualificationFrozenProcess{}, err
		}
		pid, err := strconv.ParseUint(ent.Name(), 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		cgroup, err := qualificationReadProc(ctx, pid, "cgroup", 4096)
		if err != nil || string(cgroup) != expectedGroup {
			continue
		}
		if !qualificationCandidateRolePID(role, pid, webLeader) {
			continue
		}
		stat, err := qualificationReadProc(ctx, pid, "stat", 4096)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		actualPID, _, ticks, err := qualificationProcStat(stat)
		if err != nil || actualPID != pid {
			return qualificationFrozenProcess{}, errQualification
		}
		status, err := qualificationReadProc(ctx, pid, "status", 65536)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		p, err := procstatus.Parse(status)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		if !roleMatchesUID(p, role.UID, role.Capabilities) || p.NoNewPrivs != uint32(role.NoNewPrivileges) || p.Seccomp != uint32(role.Seccomp) {
			continue
		}
		digest, err := qualificationHashProcExecutable(ctx, pid)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		if digest != role.BinaryDigest {
			continue
		}
		mounts, err := qualificationReadProc(ctx, pid, "mountinfo", 65536)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		statAgain, err := qualificationReadProc(ctx, pid, "stat", 4096)
		if err != nil {
			return qualificationFrozenProcess{}, err
		}
		pidAgain, _, ticksAgain, err := qualificationProcStat(statAgain)
		if err != nil || pidAgain != pid || ticksAgain != ticks || selected.PID != 0 {
			return qualificationFrozenProcess{}, errQualification
		}
		selected = qualificationFrozenProcess{Role: role.Role, PID: pid, StartTicks: ticks,
			Snapshot: qualificationProcReadback{Stat: stat, Status: status, Cgroup: cgroup, MountInfo: mounts, ExecutableDigest: digest, Boot: boot}}
	}
	if selected.PID == 0 {
		return qualificationFrozenProcess{}, errQualification
	}
	return selected, qualificationContext(ctx)
}

// Inspect is independent of /proc. It binds the exact pre-created CID and
// frozen image to the selected live role process; Engine PID may be tini.
func qualificationInspectFrozenContainer(ctx context.Context, role qualificationRole, process qualificationFrozenProcess, nonce, staticDigest string) error {
	if role.Kind == "host_process" {
		if role.ContainerID != "" || role.ImageDigest != "" {
			return errQualification
		}
		return nil
	}
	if role.Kind != "container" || !containerIDPattern.MatchString(role.ContainerID) || !digestPattern.MatchString(role.ImageDigest) || !digestPattern.MatchString(staticDigest) {
		return errQualification
	}
	engine, err := qualificationFixedEngineClient()
	if err != nil {
		return err
	}
	defer engine.Close()
	result, err := engine.ContainerInspect(ctx, role.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	i := result.Container
	if i.ID != role.ContainerID || i.Image != role.ImageDigest || i.State == nil || !i.State.Running || i.State.Paused || i.State.Restarting || i.State.Dead || i.State.Pid <= 0 ||
		i.Config == nil || i.HostConfig == nil || i.Config.Labels["com.switchback.qualification-window"] != nonce ||
		i.Config.Labels["com.switchback.qualification-static-intent"] != staticDigest ||
		i.HostConfig.Privileged || string(i.HostConfig.NetworkMode) == "host" {
		return errQualification
	}
	if err := qualificationProcessDescendsFrom(ctx, process.PID, uint64(i.State.Pid)); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range i.Mounts {
		for _, frozen := range role.Mounts {
			if m.Destination != frozen.Target {
				continue
			}
			if seen[frozen.Target] || m.Source != frozen.Source || m.RW == frozen.ReadOnly {
				return errQualification
			}
			seen[frozen.Target] = true
		}
	}
	for _, m := range role.Mounts {
		if !seen[m.Target] {
			return errQualification
		}
	}
	if e := qualificationVerifyInstalledRoleEnvironment(ctx, role, i.Config.Env); e != nil {
		return e
	}
	return qualificationContext(ctx)
}

func qualificationProcessDescendsFrom(ctx context.Context, child, root uint64) error {
	if child == 0 || root == 0 {
		return errQualification
	}
	for depth := 0; depth < 64; depth++ {
		if child == root {
			return nil
		}
		stat, err := qualificationReadProc(ctx, child, "stat", 4096)
		if err != nil {
			return err
		}
		pid, parent, _, err := qualificationProcStat(stat)
		if err != nil || pid != child || parent == 0 || parent == child {
			return errQualification
		}
		child = parent
	}
	return errQualification
}

func qualificationProcPath(pid uint64, leaf string) string {
	return filepath.Join("/proc", strconv.FormatUint(pid, 10), leaf)
}
