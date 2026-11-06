package localproduct

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"runtime"
)

func checkBackendCapacityRole(ctx context.Context, q *capacitysnapshot.DeploymentQualification) error {
	if e := q.Recheck(ctx); e != nil {
		return e
	}
	expected, e := q.RoleBinding("backend")
	if e != nil {
		return e
	}
	current, e := capacityprobe.ObserveRole(ctx)
	if e != nil {
		return e
	}
	p := current.Process
	if expected.Architecture != runtime.GOARCH || expected.Capabilities != 0xb || current.BinaryDigest != expected.BinaryDigest || current.Boot != expected.GuestBootID || p.UIDs != [4]uint32{} || p.CapPrm != "000000000000000b" || p.CapEff != "000000000000000b" || p.CapBnd != "000000000000000b" || p.CapInh != "0000000000000000" || p.CapAmb != "0000000000000000" || p.NoNewPrivs != 1 || p.Seccomp != 2 {
		return fmt.Errorf("runtime capacity current backend role refused")
	}
	return q.Recheck(ctx)
}
