package capacitypublisher

import (
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func TestMapperCgroupScalars(t *testing.T) {
	cases := []struct{ name, field, value string }{
		{"cpu_max", "CPU", "max 100000"}, {"cpu_negative", "CPU", "-1 100000"}, {"cpu_hex", "CPU", "0x10 100000"}, {"cpu_exponent", "CPU", "2e5 100000"}, {"cpu_extra", "CPU", "200000 100000 1"}, {"cpu_empty", "CPU", ""}, {"cpu_oversize", "CPU", string(make([]byte, 129))}, {"quota_overflow", "CPU", "18446744073709551615 1"}, {"period_overflow", "CPU", "200000 18446744073709551615"}, {"period_zero", "CPU", "200000 0"}, {"cpu_remainder", "CPU", "200001 100000"}, {"cpu_ratio", "CPU", "100000 100000"},
		{"memory_max", "Memory", "max"}, {"memory_negative", "Memory", "-1"}, {"memory_hex", "Memory", "0x1000"}, {"memory_empty", "Memory", ""}, {"memory_extra", "Memory", "4294967296 1"}, {"memory_mismatch", "Memory", "4294967295"}, {"memory_overflow", "Memory", "9223372036854775808"}, {"memory_zero", "Memory", "0"}, {"pids_max", "PIDs", "max"}, {"pids_negative", "PIDs", "-512"}, {"pids_exponent", "PIDs", "5.12e2"}, {"pids_mismatch", "PIDs", "511"}, {"pids_empty", "PIDs", ""}, {"swap_nonzero", "Swap", "1"}, {"swap_max", "Swap", "max"}, {"swap_hex", "Swap", "0x0"}, {"threaded", "Type", "threaded"}, {"type_empty", "Type", ""}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			f := &o.Workers[0].Cgroup.FilesBefore
			switch tc.field {
			case "CPU":
				f.CPU = tc.value
			case "Memory":
				f.Memory = tc.value
			case "PIDs":
				f.PIDs = tc.value
			case "Swap":
				f.Swap = tc.value
			case "Type":
				f.Type = tc.value
			}
			o.Workers[0].Cgroup.FilesAfter = *f
			rejectObservation(t, m, o)
		})
	}
}
func TestMapperCgroupIdentityAndProcess(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*capacityprobe.PoolObservation)
	}{
		{"cgroup_fstype", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Cgroup.Before.FSType = 1
			o.Workers[0].Cgroup.After = o.Workers[0].Cgroup.Before
		}}, {"cgroup_device_zero", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Cgroup.Before.Device = 0
			o.Workers[0].Cgroup.After = o.Workers[0].Cgroup.Before
		}}, {"cgroup_inode_zero", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Cgroup.Before.Inode = 0
			o.Workers[0].Cgroup.After = o.Workers[0].Cgroup.Before
		}}, {"cgroup_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Cgroup.After.Inode++ }}, {"scalar_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Cgroup.FilesAfter.Memory = "1" }}, {"node_alias", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Cgroup.Before = o.Core[0].Cgroup.Before
			o.Workers[0].Cgroup.After = o.Core[0].Cgroup.After
		}}, {"core_upper", func(o *capacityprobe.PoolObservation) {
			o.Core[0].Cgroup.FilesBefore.CPU = "120001 100000"
			o.Core[0].Cgroup.FilesAfter = o.Core[0].Cgroup.FilesBefore
		}}, {"core_members_missing", func(o *capacityprobe.PoolObservation) { o.Core[0].Members = nil }}, {"core_members_duplicate", func(o *capacityprobe.PoolObservation) {
			o.Core[0].Members = append(o.Core[0].Members, o.Core[0].Members[0])
		}}, {"core_members_limit", func(o *capacityprobe.PoolObservation) {
			o.Core[0].Members = make([]capacityprobe.PoolProcessReadback, 33)
		}},
		{"wrong_parent", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Process.CgroupPathBefore = syntheticWorker + "-escape"
			o.Workers[0].Process.CgroupPathAfter = o.Workers[0].Process.CgroupPathBefore
		}}, {"pid_wrong", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Process.PIDBefore = 201
			o.Workers[0].Process.PIDAfter = 201
		}}, {"pid_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Process.PIDAfter++ }}, {"ticks_zero", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Process.StartTimeTicksBefore = 0
			o.Workers[0].Process.StartTimeTicksAfter = 0
		}}, {"ticks_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Process.StartTimeTicksAfter++ }}, {"process_cid", func(o *capacityprobe.PoolObservation) { o.Workers[0].Process.ContainerID = syntheticCID }}, {"process_path_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Process.CgroupPathAfter += "/other" }}, {"core_process_parent", func(o *capacityprobe.PoolObservation) {
			o.Core[0].Members[0].CgroupPathBefore = syntheticWorker
			o.Core[0].Members[0].CgroupPathAfter = syntheticWorker
		}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { m, o := newFixtureMapper(t, true); tc.mutate(&o); rejectObservation(t, m, o) })
	}
}
