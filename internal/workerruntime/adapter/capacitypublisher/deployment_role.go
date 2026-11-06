package capacitypublisher

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"runtime"
	"strings"
)

func checkPublisherRole(ctx context.Context, q *capacitysnapshot.DeploymentQualification) error {
	if e := q.Recheck(ctx); e != nil {
		return e
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	expected, e := q.RoleBinding("publisher")
	if e != nil {
		return e
	}
	role, e := capacityprobe.ObserveRole(ctx)
	if e != nil {
		return e
	}
	mask := fmt.Sprintf("%016x", expected.Capabilities)
	p := role.Process
	if expected.Architecture != runtime.GOARCH || role.BinaryDigest != expected.BinaryDigest || role.Boot != expected.GuestBootID || expected.Capabilities != 0x280000 || p.UIDs != [4]uint32{} || p.CapPrm != mask || p.CapEff != mask || p.CapBnd != mask || p.CapInh != "0000000000000000" || p.CapAmb != "0000000000000000" || p.NoNewPrivs != 1 || p.Seccomp != 2 {
		return errPublisherDeployment
	}
	v, e := capacityprobe.ObserveDeployment(ctx)
	if e != nil {
		return e
	}
	if v.BootBefore != expected.GuestBootID || v.BootAfter != v.BootBefore || v.CallerBefore != v.CallerAfter || v.PID1Before != v.PID1After || v.CallerBefore.UnifiedCgroupPath != strings.TrimPrefix(expected.CgroupPath, "/sys/fs/cgroup") || v.CallerBefore.CapEff != mask || v.CallerBefore.CapPrm != mask || v.CallerBefore.CapBnd != mask || v.CallerBefore.CapInh != "0000000000000000" || v.CallerBefore.CapAmb != "0000000000000000" || v.CallerBefore.UIDs != [4]uint32{} || v.CallerBefore.NoNewPrivs != 1 || v.CallerBefore.Seccomp != 2 {
		return errPublisherDeployment
	}
	for i, n := range v.Namespaces {
		if n.Kind == "mnt" && n.Before.Inode != expected.MountNamespace {
			return errPublisherDeployment
		}
		if n.Before != n.After || n.Before.Class != "observed" || n.Before.Errno != 0 || n.Before.Inode == "" || n.Before.Inode != v.Namespaces[i%4].Before.Inode || n.Before.Device != v.Namespaces[i%4].Before.Device {
			return errPublisherDeployment
		}
	}
	for _, d := range expected.Core {
		actual, e := capacityprobe.ObserveRoleCgroup(ctx, d.CgroupPath)
		if e != nil {
			return e
		}
		mapped, e := cgroup(actual)
		if e != nil || mapped.CgroupPath != d.CgroupPath || mapped.CPUQuotaMicros != d.CPUQuotaMicros || mapped.CPUPeriodMicros != d.CPUPeriodMicros || mapped.MemoryMax != d.MemoryMax || mapped.PIDsMax != d.PIDsMax {
			return errPublisherDeployment
		}
	}
	return q.Recheck(ctx)
}

// Inspect directory identities only; key bytes and journal opening remain later.
func checkPublisherPrivateDirectories(ctx context.Context, c *publisherConfig) error {
	if ctx == nil || ctx.Err() != nil || c == nil || c.qualification == nil {
		return errPublisherDeployment
	}
	if e := c.qualification.Recheck(ctx); e != nil {
		return e
	}
	expected, e := c.qualification.RoleBinding("publisher")
	if e != nil {
		return e
	}
	actual, e := capacityprobe.ObserveRolePrivateDirectories(ctx)
	if e != nil {
		return e
	}
	return validatePublisherPrivateDirectories(expected.Private, actual)
}
func validatePublisherPrivateDirectories(expected []capacitysnapshot.DeploymentPrivateDirectoryBinding, actual []capacityprobe.RolePrivateDirectory) error {
	if len(expected) != 2 || len(actual) != 2 {
		return errPublisherDeployment
	}
	seen := map[string]bool{}
	for _, a := range actual {
		if seen[a.Path] {
			return errPublisherDeployment
		}
		seen[a.Path] = true
		found := false
		for _, e := range expected {
			if a.Path != e.Path {
				continue
			}
			found = true
			if a.Device != e.Device || a.Root != e.Root || a.Inode != e.Inode || a.MountID != e.MountID || a.Mode != e.Mode || a.UID != e.UID || a.GID != e.GID {
				return errPublisherDeployment
			}
		}
		if !found {
			return errPublisherDeployment
		}
	}
	return nil
}
