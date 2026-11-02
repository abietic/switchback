package capacitypublisher

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// quota parses a deliberately narrow projection; it does not create native evidence.
func quota(q capacityprobe.PoolQuotaReadback, project uint64, worker bool, started, ended time.Time) (capacitysnapshot.StorageQuota, capacitysnapshot.CreatedLayerInodeObservation, error) {
	bad := func() (capacitysnapshot.StorageQuota, capacitysnapshot.CreatedLayerInodeObservation, error) {
		return capacitysnapshot.StorageQuota{}, capacitysnapshot.CreatedLayerInodeObservation{}, errQuota
	}
	if q.StateSamplesTruncated || len(q.StateSamples) == 0 || len(q.StateSamples) > 64 {
		return bad()
	}
	for n, s := range q.StateSamples {
		if s.Sequence != n+1 || s.Source != "linux_syscall" || s.Outcome != "observed" || s.Errno != nil || !label(s.Operation) || s.Started.IsZero() || !s.Ended.After(s.Started) || s.Started.Before(started) || s.Ended.After(ended) || (n > 0 && s.Started.Before(q.StateSamples[n-1].Ended)) {
			return bad()
		}
	}
	state, e := capacityobserver.MapQuotaStateEvidence(capacityprobe.CreatedLayerPhysical{QStatV: q.QStatV, Samples: q.StateSamples, SamplesTruncated: q.StateSamplesTruncated})
	if e != nil || state.Flags&0x30 != 0x30 {
		return bad()
	}
	target, e := capacityobserver.MapQuotaInodeEvidence(q.Target, project)
	if e != nil {
		return bad()
	}
	def := q.Default0
	if def[0] != 1 || def[1] != 2 || binary.LittleEndian.Uint32(def[4:8]) != 0 {
		return bad()
	}
	if target.HardBlocks512 == 0 || target.HardBlocks512 > math.MaxInt64/512 || target.UsedBlocks512 > math.MaxInt64/512 || target.UsedBlocks512 > target.HardBlocks512 {
		return bad()
	}
	if worker && (target.HardBlocks512*512 != 10<<30 || binary.LittleEndian.Uint64(target.Raw[16:24]) != target.HardBlocks512) {
		return bad()
	}
	if target.HardInodes > 0 && target.UsedInodes > target.HardInodes {
		return bad()
	}
	inodes := capacitysnapshot.CreatedLayerInodeObservation{HardInodes: target.HardInodes, UsedInodes: target.UsedInodes, Assessment: capacitysnapshot.AssessInodeEvidence(capacitysnapshot.StorageQuotaV2{HardInodes: target.HardInodes, UsedInodes: target.UsedInodes})}
	return capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: uint64(state.Version), StateFlags: uint64(state.Flags), Version: uint64(target.Raw[0]), Flags: uint64(target.Raw[1]), ProjectID: target.ProjectID, HardBlocks512: target.HardBlocks512, UsedBlocks512: target.UsedBlocks512}, inodes, nil
}
