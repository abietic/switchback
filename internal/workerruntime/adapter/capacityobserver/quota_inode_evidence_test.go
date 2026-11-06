package capacityobserver

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func inodeQuotaFixture(hard, used uint64) capacityprobe.Quota {
	raw := make([]byte, 112)
	raw[0] = 1
	raw[1] = 2
	binary.LittleEndian.PutUint32(raw[4:8], 41)
	blocks, count := uint64(2000), uint64(8)
	binary.LittleEndian.PutUint64(raw[8:16], blocks)
	binary.LittleEndian.PutUint64(raw[24:32], hard)
	binary.LittleEndian.PutUint64(raw[40:48], count)
	binary.LittleEndian.PutUint64(raw[48:56], used)
	// Distinct opaque bytes ensure provenance covers the complete record.
	raw[100] = 0xa7
	raw[111] = 0x5e
	return capacityprobe.Quota{RawHex: hex.EncodeToString(raw), Status: "observed", ABI: "linux_v6.8_fs_disk_quota_112_le", UnitBytes: 512, HardBlocks: &blocks, UsedBlocks: &count, HardInodes: &hard, UsedInodes: &used}
}
func TestMapQuotaInodeEvidencePreservesCountsAndProvenance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hard, used uint64
	}{{"zero", 0, 0}, {"zero_used", 0, 17}, {"equal", 100, 100}, {"over", 100, 101}, {"max", math.MaxUint64, math.MaxUint64}, {"max_used", 1, math.MaxUint64}} {
		t.Run(tc.name, func(t *testing.T) {
			q := inodeQuotaFixture(tc.hard, tc.used)
			before := q.RawHex
			got, err := MapQuotaInodeEvidence(q, 41)
			if err != nil {
				t.Fatal(err)
			}
			if got.ProjectID != 41 || got.HardBlocks512 != 2000 || got.UsedBlocks512 != 8 || got.HardInodes != tc.hard || got.UsedInodes != tc.used || hex.EncodeToString(got.Raw[:]) != before {
				t.Fatal("raw counters or bytes changed")
			}
			h := sha256.Sum256(got.Raw[:])
			if got.RawDigest != "sha256:"+hex.EncodeToString(h[:]) {
				t.Fatal("full raw digest missing")
			}
			*q.HardInodes = 13
			*q.UsedInodes = 14
			*q.HardBlocks = 15
			*q.UsedBlocks = 16
			q.RawHex = "changed"
			if got.HardInodes != tc.hard || got.UsedInodes != tc.used || got.Raw[111] != 0x5e {
				t.Fatal("mapper retained aliases")
			}
			copy := got
			copy.Raw[0] = 0
			if got.Raw[0] != 1 {
				t.Fatal("raw result aliased")
			}
		})
	}
	typ := reflect.TypeOf(QuotaInodeEvidence{})
	for _, name := range []string{"StateFlags", "StateVersion", "Trust", "Facts", "CapacityReady", "ExecutionEnabled", "RemainingInodes", "RemainingBytes"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("small observation manufactures %s", name)
		}
	}
}
func TestMapQuotaInodeEvidenceRejectsUnavailableOrInconsistent(t *testing.T) {
	for _, name := range []string{"zero", "status", "abi", "unit", "empty", "hex", "odd", "short", "long", "version", "flags", "raw_project", "expected_zero", "expected_overflow", "nil_hard_blocks", "nil_used_blocks", "nil_hard_inodes", "nil_used_inodes", "hard_blocks", "used_blocks", "hard_inodes", "used_inodes"} {
		t.Run(name, func(t *testing.T) {
			q := inodeQuotaFixture(100, 7)
			project := uint64(41)
			raw, _ := hex.DecodeString(q.RawHex)
			switch name {
			case "zero":
				q = capacityprobe.Quota{}
			case "status":
				q.Status = "unavailable"
			case "abi":
				q.ABI = "other"
			case "unit":
				q.UnitBytes = 4096
			case "empty":
				q.RawHex = ""
			case "hex":
				q.RawHex = "xx" + strings.Repeat("00", 111)
			case "odd":
				q.RawHex = q.RawHex[:223]
			case "short":
				raw = raw[:111]
			case "long":
				raw = append(raw, 0)
			case "version":
				raw[0] = 2
			case "flags":
				raw[1] = 1
			case "raw_project":
				raw[4]++
			case "expected_zero":
				project = 0
			case "expected_overflow":
				project = uint64(math.MaxUint32) + 1
			case "nil_hard_blocks":
				q.HardBlocks = nil
			case "nil_used_blocks":
				q.UsedBlocks = nil
			case "nil_hard_inodes":
				q.HardInodes = nil
			case "nil_used_inodes":
				q.UsedInodes = nil
			case "hard_blocks":
				*q.HardBlocks++
			case "used_blocks":
				*q.UsedBlocks++
			case "hard_inodes":
				*q.HardInodes++
			case "used_inodes":
				*q.UsedInodes++
			}
			switch name {
			case "short", "long", "version", "flags", "raw_project":
				q.RawHex = hex.EncodeToString(raw)
			}
			before := q.RawHex
			got, err := MapQuotaInodeEvidence(q, project)
			if err == nil || got != (QuotaInodeEvidence{}) {
				t.Fatal("invalid observation accepted or partial result exposed")
			}
			if q.RawHex != before {
				t.Fatal("input mutated")
			}
		})
	}
}
