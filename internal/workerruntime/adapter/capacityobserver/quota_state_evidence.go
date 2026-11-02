package capacityobserver

import (
	"encoding/binary"
	"fmt"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// QuotaFileState preserves one original quota-file slot without claiming iget success.
type QuotaFileState struct {
	Inode       uint64
	NBlocksRaw  uint64
	NextentsRaw uint32
	Accounting  bool
	Enforcement bool
}

// QuotaStateEvidence is diagnostic parsing evidence, never capacity or admission authority.
// Raw block quantities have no byte conversion; the three slots are not an inventory.
type QuotaStateEvidence struct {
	ABI       string
	Version   int8
	Flags     uint16
	InCoreDQs uint32
	User      QuotaFileState
	Group     QuotaFileState
	Project   QuotaFileState
	Sample    capacityprobe.CreatedLayerSample
	Raw       [160]byte
	RawDigest string
}

func MapQuotaStateEvidence(p capacityprobe.CreatedLayerPhysical) (QuotaStateEvidence, error) {
	if p.SamplesTruncated || len(p.Samples) > 64 {
		return QuotaStateEvidence{}, fmt.Errorf("qstatv sample collection truncated or oversized")
	}
	index := -1
	for i, sample := range p.Samples {
		if sample.Operation == "qstatv" {
			if index >= 0 {
				return QuotaStateEvidence{}, fmt.Errorf("qstatv sample duplicated")
			}
			index = i
		}
	}
	if index < 0 {
		return QuotaStateEvidence{}, fmt.Errorf("qstatv sample absent")
	}
	// Validate only the successful prefix. Later read failures do not erase this
	// observation, while the scan above still prevents a hidden later duplicate.
	for i := 0; i <= index; i++ {
		sample := p.Samples[i]
		if sample.Sequence != i+1 || sample.Source != "linux_syscall" || sample.Outcome != "observed" || sample.Errno != nil || sample.Started.IsZero() || sample.Ended.IsZero() || !sample.Ended.After(sample.Started) {
			return QuotaStateEvidence{}, fmt.Errorf("qstatv sample prefix invalid at sequence %d", i+1)
		}
		if i > 0 && sample.Started.Before(p.Samples[i-1].Ended) {
			return QuotaStateEvidence{}, fmt.Errorf("qstatv sample prefix overlaps or reverses at sequence %d", i+1)
		}
	}
	raw := p.QStatV
	flags := binary.LittleEndian.Uint16(raw[2:4])
	if raw[0] != 1 || flags == 0 || flags & ^uint16(0x3f) != 0 {
		return QuotaStateEvidence{}, fmt.Errorf("qstatv version or flags unsupported")
	}
	for i, b := range raw {
		if (i == 1 || (i >= 28 && i < 32) || (i >= 52 && i < 56) || (i >= 76 && i < 80) || i >= 98) && b != 0 {
			return QuotaStateEvidence{}, fmt.Errorf("qstatv padding or reserved byte nonzero at %d", i)
		}
	}
	slot := func(base int, accounting, enforcement uint16) QuotaFileState {
		return QuotaFileState{
			Inode:       binary.LittleEndian.Uint64(raw[base : base+8]),
			NBlocksRaw:  binary.LittleEndian.Uint64(raw[base+8 : base+16]),
			NextentsRaw: binary.LittleEndian.Uint32(raw[base+16 : base+20]),
			Accounting:  flags&accounting != 0,
			Enforcement: flags&enforcement != 0,
		}
	}
	return QuotaStateEvidence{
		ABI: "linux_v6.8_fs_quota_statv_v1_160_le", Version: 1, Flags: flags,
		InCoreDQs: binary.LittleEndian.Uint32(raw[4:8]),
		User:      slot(8, 0x01, 0x02), Group: slot(32, 0x04, 0x08), Project: slot(56, 0x10, 0x20),
		Sample: p.Samples[index], Raw: raw, RawDigest: capacitysnapshot.Digest(raw[:]),
	}, nil
}
