package capacityobserver

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// QuotaInodeEvidence is a detached single-record observation, not a complete
// pool snapshot. It makes no QSTATV, freshness, Trust or admission assertion.
// Inode counters include reservations; they are neither bytes nor live file counts.
type QuotaInodeEvidence struct {
	ProjectID     uint64
	HardBlocks512 uint64
	UsedBlocks512 uint64
	HardInodes    uint64
	UsedInodes    uint64
	Raw           [112]byte
	RawDigest     string
}

func MapQuotaInodeEvidence(q capacityprobe.Quota, expectedProjectID uint64) (QuotaInodeEvidence, error) {
	if q.Status != "observed" || q.ABI != "linux_v6.8_fs_disk_quota_112_le" || q.UnitBytes != 512 || expectedProjectID == 0 || expectedProjectID > math.MaxUint32 || len(q.RawHex) != 224 || q.HardBlocks == nil || q.UsedBlocks == nil || q.HardInodes == nil || q.UsedInodes == nil {
		return QuotaInodeEvidence{}, fmt.Errorf("quota observation unavailable or incompatible")
	}
	raw, err := hex.DecodeString(q.RawHex)
	if err != nil || len(raw) != 112 {
		return QuotaInodeEvidence{}, fmt.Errorf("quota raw encoding invalid")
	}
	if raw[0] != 1 || raw[1] != 2 || uint64(binary.LittleEndian.Uint32(raw[4:8])) != expectedProjectID {
		return QuotaInodeEvidence{}, fmt.Errorf("quota ABI or project mismatch")
	}
	hard, used := binary.LittleEndian.Uint64(raw[8:16]), binary.LittleEndian.Uint64(raw[40:48])
	ih, iu := binary.LittleEndian.Uint64(raw[24:32]), binary.LittleEndian.Uint64(raw[48:56])
	if hard != *q.HardBlocks || used != *q.UsedBlocks || ih != *q.HardInodes || iu != *q.UsedInodes {
		return QuotaInodeEvidence{}, fmt.Errorf("quota decoded counters mismatch raw")
	}
	observation := QuotaInodeEvidence{ProjectID: expectedProjectID, HardBlocks512: hard, UsedBlocks512: used, HardInodes: ih, UsedInodes: iu, RawDigest: capacitysnapshot.Digest(raw)}
	copy(observation.Raw[:], raw)
	return observation, nil
}
