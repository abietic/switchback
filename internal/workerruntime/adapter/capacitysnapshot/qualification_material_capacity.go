package capacitysnapshot

import (
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
)

type qualificationRuntimePreflight struct {
	Schema                string `json:"schema"`
	EngineID              string `json:"engine_id"`
	EngineCPUMilli        uint64 `json:"engine_cpu_milli"`
	EngineMemory          uint64 `json:"engine_memory"`
	CoreCPUMilli          uint64 `json:"core_cpu_milli"`
	CoreMemory            uint64 `json:"core_memory"`
	WorkerCPUMilli        uint64 `json:"worker_cpu_milli"`
	WorkerMemory          uint64 `json:"worker_memory"`
	NecessaryCapacityOnly bool   `json:"necessary_capacity_only"`
}

func qualificationRuntimeBudget(engine string, cpus, memory uint64, core []CoreDomain) (qualificationRuntimePreflight, error) {
	r := qualificationRuntimePreflight{Schema: "switchback.qualification.runtime_preflight.v1", EngineID: engine, NecessaryCapacityOnly: true}
	if !label(engine) || cpus == 0 || cpus > 65536 || memory == 0 || memory > 1<<60 {
		return r, errQualification
	}
	r.EngineCPUMilli = cpus * 1000
	r.EngineMemory = memory
	p, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok {
		return r, errQualification
	}
	r.WorkerCPUMilli = uint64(p.CPUUnits) * 1000
	r.WorkerMemory = uint64(p.MemoryBytes)
	for _, c := range core {
		if c.CPUPeriodMicros == 0 || c.CPUQuotaMicros > 1<<40 || c.MemoryMax > 1<<60 {
			return r, errQualification
		}
		r.CoreCPUMilli += (c.CPUQuotaMicros*1000 + c.CPUPeriodMicros - 1) / c.CPUPeriodMicros
		r.CoreMemory += c.MemoryMax
	}
	if r.CoreCPUMilli+r.WorkerCPUMilli > r.EngineCPUMilli*4/5 || r.CoreMemory+r.WorkerMemory > r.EngineMemory*4/5 {
		return r, fmt.Errorf("first Worker exceeds existing aggregate 80 percent admission budget")
	}
	return r, nil
}
func qualificationCandidateRolePID(role qualificationRole, pid, engineLeader uint64) bool {
	if role.Role != "web" && role.Role != "postgres" {
		return true
	}
	want := uint64(101)
	if role.Role == "postgres" {
		want = 999
	}
	return role.Kind == "container" && role.UID == want && engineLeader > 0 && pid == engineLeader
}
