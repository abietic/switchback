package capacityprobe

import (
	"context"
	"errors"
	"time"
)

// PoolPlan selects deployment-owned layout, never observed limits or authority.
// CollectPool detaches its slices before doing any IO.
type PoolPlan struct {
	AnchorPath string
	Core       []PoolCorePlan
	Storage    []PoolStoragePlan
}
type PoolCorePlan struct {
	ID, CgroupPath string
	ContainerIDs   []string
}
type PoolStoragePlan struct{ ID, Path string }

var (
	ErrPoolUnstable    = errors.New("pool_unstable")
	ErrPoolUnavailable = errors.New("pool_unavailable")
	ErrPoolInvalid     = errors.New("pool_invalid")
)

type poolFailure struct{ reason, cause error }

func (e *poolFailure) Error() string { return e.reason.Error() }
func (e *poolFailure) Unwrap() []error {
	if e.cause != nil {
		return []error{e.reason, e.cause}
	}
	return []error{e.reason}
}
func poolError(ctx context.Context, e error) error {
	reason := ErrPoolUnavailable
	if errors.Is(e, ErrPoolInvalid) {
		reason = ErrPoolInvalid
	}
	if errors.Is(e, ErrPoolUnstable) {
		reason = ErrPoolUnstable
	}
	var cause error
	if ctx != nil {
		cause = ctx.Err()
	}
	return &poolFailure{reason, cause}
}
func poolCall(ctx context.Context, call func() error) error {
	if ctx == nil {
		return ErrPoolInvalid
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	e := call()
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	return e
}

type poolGet func(context.Context, string) ([]byte, error)

const poolWindow = 25 * time.Second
const poolCoreRoot = "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
const poolWorkerRoot = "/sys/fs/cgroup/switchback.slice/switchback-workers.slice"
