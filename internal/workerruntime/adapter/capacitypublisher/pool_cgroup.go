package capacitypublisher

import (
	"math"
	"strings"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func cgroup(c capacityprobe.PoolCgroupReadback) (capacitysnapshot.CoreDomain, error) {
	bad := func() (capacitysnapshot.CoreDomain, error) { return capacitysnapshot.CoreDomain{}, errResource }
	if !absolute(c.Path) || !inside(c.Path, "/sys/fs/cgroup") || c.Before != c.After || c.Before.FSType != 0x63677270 || c.Before.Device == 0 || c.Before.Inode == 0 || c.FilesBefore != c.FilesAfter {
		return bad()
	}
	f := c.FilesBefore
	for _, s := range []string{f.CPU, f.Memory, f.Swap, f.PIDs, f.Type} {
		if len(s) == 0 || len(s) > 128 || strings.ContainsAny(s, "\x00\r") {
			return bad()
		}
	}
	cpu := strings.Fields(f.CPU)
	if len(cpu) != 2 {
		return bad()
	}
	quota, qok := decimal(cpu[0], math.MaxInt64/1000)
	period, pok := decimal(cpu[1], math.MaxInt64)
	memory, mok := decimal(strings.TrimSpace(f.Memory), math.MaxInt64)
	pids, iok := decimal(strings.TrimSpace(f.PIDs), math.MaxInt64)
	swap, sok := decimal(strings.TrimSpace(f.Swap), math.MaxInt64)
	if !qok || !pok || !mok || !iok || !sok || quota == 0 || period == 0 || memory == 0 || pids == 0 || swap != 0 || strings.TrimSpace(f.Type) != "domain" {
		return bad()
	}
	return capacitysnapshot.CoreDomain{CgroupPath: c.Path, CPUQuotaMicros: quota, CPUPeriodMicros: period, MemoryMax: memory, PIDsMax: pids}, nil
}
func process(p capacityprobe.PoolProcessReadback, i capacityprobe.PoolContainerInspect, parent string) error {
	if p.ContainerID != i.ID || p.PIDBefore == 0 || p.PIDBefore != p.PIDAfter || p.PIDBefore != i.PID || p.StartTimeTicksBefore == 0 || p.StartTimeTicksBefore != p.StartTimeTicksAfter || p.CgroupPathBefore != p.CgroupPathAfter || !absolute(p.CgroupPathBefore) || !inside(p.CgroupPathBefore, parent) {
		return errInventory
	}
	return nil
}
