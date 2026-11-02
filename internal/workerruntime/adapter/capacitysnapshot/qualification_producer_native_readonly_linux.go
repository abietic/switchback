//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func qualificationNamespaceAt(ctx context.Context, pid, tid uint64, kind string) (uint64, uint64, error) {
	if pid == 0 || pid > 1<<31-1 || (kind != "user" && kind != "pid" && kind != "mnt" && kind != "cgroup") || qualificationContext(ctx) != nil {
		return 0, 0, errQualification
	}
	if tid == 0 || tid > 1<<31-1 {
		return 0, 0, errQualification
	}
	fd, err := unix.Open(filepath.Join("/proc", strconv.FormatUint(pid, 10), "task", strconv.FormatUint(tid, 10), "ns", kind), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	var s unix.Stat_t
	err = unix.Fstat(fd, &s)
	if err != nil || s.Dev == 0 || s.Ino == 0 {
		return 0, 0, errQualification
	}
	return uint64(s.Dev), s.Ino, qualificationContext(ctx)
}

func qualificationNamespaceStart(ctx context.Context, c qualificationCandidateRecord) ([]qualificationNamespace, error) {
	publisher := uint64(0)
	for _, p := range c.Processes {
		if p.Role == "publisher" {
			publisher = p.PID
		}
	}
	if publisher == 0 {
		return nil, errQualification
	}
	var result []qualificationNamespace
	for _, owner := range []struct {
		name string
		pid  uint64
	}{{"publisher_thread", publisher}, {"pid1", 1}} {
		for _, kind := range []string{"user", "pid", "mnt", "cgroup"} {
			dev, ino, err := qualificationNamespaceAt(ctx, owner.pid, owner.pid, kind)
			if err != nil {
				return nil, err
			}
			result = append(result, qualificationNamespace{Owner: owner.name, Kind: kind, Device: dev, Inode: ino})
		}
	}
	return result, qualificationContext(ctx)
}

func qualificationMergeNamespaceWindow(before, after []qualificationNamespace) ([]qualificationNamespace, error) {
	if len(before) != 8 || len(after) != 8 {
		return nil, errQualification
	}
	out := make([]qualificationNamespace, 8)
	for i := range before {
		if before[i].Owner != after[i].Owner || before[i].Kind != after[i].Kind || before[i].Device == 0 || before[i].Inode == 0 ||
			after[i].Device == 0 || after[i].Inode == 0 || after[i].Device != after[i].AfterDevice || after[i].Inode != after[i].AfterInode {
			return nil, errQualification
		}
		out[i] = before[i]
		out[i].AfterDevice, out[i].AfterInode = after[i].AfterDevice, after[i].AfterInode
	}
	return out, nil
}

// A second observation of each frozen role is required; simply copying the
// candidate into the native receipt would not prove that roles survived the
// finite window or that their mount namespaces retained the same bindings.
func qualificationObserveCoreRoles(ctx context.Context, c qualificationCandidateRecord, w qualificationWindowOpen) (qualificationNamespaces, qualificationReachability, error) {
	var ns qualificationNamespaces
	reach := qualificationReachability{Roles: qualificationClone(c.Source.Inputs.Roles), Writers: qualificationClone(c.Binding.Core),
		PrivateSources: []string{"/mnt/lima-sbquota/core/capacity-publication-private", "/mnt/lima-sbquota/core/capacity-publisher-keys"}}
	if len(c.Processes) != len(c.Source.Inputs.Roles) {
		return ns, reach, errQualification
	}
	publisher := uint64(0)
	for i, role := range c.Source.Inputs.Roles {
		before := c.Processes[i]
		if before.Role != role.Role || before.PID == 0 || before.StartTicks == 0 {
			return ns, reach, errQualification
		}
		after, err := qualificationObserveFrozenRole(ctx, role)
		if err != nil || after.PID != before.PID || after.StartTicks != before.StartTicks || after.Snapshot.Boot != before.Snapshot.Boot || after.Snapshot.ExecutableDigest != before.Snapshot.ExecutableDigest {
			return ns, reach, errQualification
		}
		if err = qualificationInspectFrozenContainer(ctx, role, after, w.Nonce, c.StaticIntentDigest); err != nil {
			return ns, reach, err
		}
		ns.Roles = append(ns.Roles, qualificationNativeRole{Role: role.Role, StatusBefore: before.Snapshot.Status, StatusAfter: after.Snapshot.Status,
			CgroupBefore: strings.TrimSuffix(strings.TrimPrefix(string(before.Snapshot.Cgroup), "0::"), "\n"),
			CgroupAfter:  strings.TrimSuffix(strings.TrimPrefix(string(after.Snapshot.Cgroup), "0::"), "\n"),
			BinaryDigest: after.Snapshot.ExecutableDigest, Boot: after.Snapshot.Boot})
		reach.Observed = append(reach.Observed, qualificationMountObservation{Role: role.Role, Raw: after.Snapshot.MountInfo})
		if role.Kind == "host_process" {
			reach.Processes = append(reach.Processes, qualificationProcessWriter{Role: role.Role, PID: after.PID, StartTime: after.StartTicks, Cgroup: strings.TrimSuffix(strings.TrimPrefix(string(after.Snapshot.Cgroup), "0::"), "\n"), BinaryDigest: after.Snapshot.ExecutableDigest})
		}
		if role.Role == "publisher" {
			publisher = before.PID
		}
	}
	if publisher == 0 {
		return ns, reach, errQualification
	}
	for _, owner := range []struct {
		name string
		pid  uint64
	}{{"publisher_thread", publisher}, {"pid1", 1}} {
		for _, kind := range []string{"user", "pid", "mnt", "cgroup"} {
			dev, ino, err := qualificationNamespaceAt(ctx, owner.pid, owner.pid, kind)
			if err != nil {
				return ns, reach, err
			}
			adev, aino, err := qualificationNamespaceAt(ctx, owner.pid, owner.pid, kind)
			if err != nil {
				return ns, reach, err
			}
			ns.Namespaces = append(ns.Namespaces, qualificationNamespace{Owner: owner.name, Kind: kind, Device: dev, Inode: ino, AfterDevice: adev, AfterInode: aino})
		}
	}
	for _, p := range reach.PrivateSources {
		v, err := qualificationPrivateDirectory(ctx, p)
		if err != nil {
			return ns, reach, err
		}
		reach.Private = append(reach.Private, v)
	}
	return ns, reach, qualificationContext(ctx)
}

func qualificationPrivateDirectory(ctx context.Context, p string) (qualificationPrivateMount, error) {
	var out qualificationPrivateMount
	if !absolute(p) {
		return out, errQualification
	}
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return out, err
	}
	defer unix.Close(fd)
	actual, err := qualificationNativeFD(ctx, fd, p)
	if err != nil || actual.Mode != 0040700 || actual.Owner != 0 {
		return out, errQualification
	}
	var s unix.Stat_t
	if err := unix.Fstat(fd, &s); err != nil || s.Gid != 0 {
		return out, errQualification
	}
	mountsRaw, err := qualificationReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if err != nil {
		return out, err
	}
	mounts, err := qualificationMountInfo(mountsRaw)
	if err != nil {
		return out, err
	}
	var covering qualificationMount
	for _, m := range mounts {
		if (p == m.Target || strings.HasPrefix(p, strings.TrimSuffix(m.Target, "/")+"/")) && len(m.Target) > len(covering.Target) {
			covering = m
		}
	}
	if covering.Target == "" || covering.Device != actual.Device || covering.MountID != actual.MountID {
		return out, errQualification
	}
	root := filepath.Join(covering.Root, strings.TrimPrefix(strings.TrimPrefix(p, covering.Target), "/"))
	if !absolute(root) {
		return out, errQualification
	}
	out = qualificationPrivateMount{Path: p, Root: root, Device: actual.Device, Inode: actual.Inode, MountID: actual.MountID, Mode: actual.Mode, UID: actual.Owner, GID: uint64(s.Gid)}
	return out, qualificationContext(ctx)
}

func qualificationReadCoreLimits(ctx context.Context, c qualificationCandidateRecord) (qualificationCore, error) {
	out := qualificationCore{Domains: qualificationClone(c.Trust.Core), Members: qualificationClone(c.Binding.Core)}
	for _, d := range c.Trust.Core {
		if !absolute(d.CgroupPath) || !strings.HasPrefix(d.CgroupPath, "/sys/fs/cgroup/switchback.slice/") {
			return out, errQualification
		}
		for _, item := range []struct {
			name string
			into *[]string
		}{{"cpu.max", &out.CPU}, {"memory.max", &out.Memory}, {"pids.max", &out.PIDs}, {"memory.swap.max", &out.Swap}} {
			p := filepath.Join(d.CgroupPath, item.name)
			f, err := os.Open(p)
			if err != nil {
				return out, err
			}
			raw, err := qualificationReadBounded(ctx, f, 256)
			f.Close()
			if err != nil {
				return out, fmt.Errorf("qualification cgroup %s: %w", item.name, err)
			}
			*item.into = append(*item.into, string(raw))
		}
	}
	return out, qualificationContext(ctx)
}
