package capacityobserver

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// Independent LP64 LE fixture: literal byte positions from the approved ABI,
// deliberately no shared encoder/constants with the mapper.
func quotaStateFixture() capacityprobe.CreatedLayerPhysical {
	raw := [160]byte{
		0: 1, 2: 0x3f, 4: 0x98, 5: 0xba, 6: 0xdc, 7: 0xfe,
		8: 0xef, 9: 0xcd, 10: 0xab, 11: 0x89, 12: 0x67, 13: 0x45, 14: 0x23, 15: 0xf1,
		16: 0xff, 17: 0xff, 18: 0xff, 19: 0xff, 20: 0xff, 21: 0xff, 22: 0xff, 23: 0xff,
		24: 0xff, 25: 0xff, 26: 0xff, 27: 0xff,
		32: 0x88, 33: 0x77, 34: 0x66, 35: 0x55, 36: 0x44, 37: 0x33, 38: 0x22, 39: 0x81,
		40: 0x08, 41: 0x07, 42: 0x06, 43: 0x05, 44: 0x04, 45: 0x03, 46: 0x02, 47: 0x91,
		48: 0x11, 49: 0x22, 50: 0x33, 51: 0x84,
		56: 0x18, 57: 0x27, 58: 0x36, 59: 0x45, 60: 0x54, 61: 0x63, 62: 0x72, 63: 0xa1,
		64: 0x81, 65: 0x72, 66: 0x63, 67: 0x54, 68: 0x45, 69: 0x36, 70: 0x27, 71: 0xb8,
		72: 0x99, 73: 0x88, 74: 0x77, 75: 0xc6,
		80: 0x81, 81: 0x82, 82: 0x83, 83: 0x84, 84: 0x85, 85: 0x86, 86: 0x87, 87: 0x88,
		88: 0x89, 89: 0x8a, 90: 0x8b, 91: 0x8c, 92: 0x8d, 93: 0x8e, 94: 0x8f, 95: 0x90,
		96: 0x91, 97: 0x92,
	}
	start := time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC)
	return capacityprobe.CreatedLayerPhysical{QStatV: raw, Samples: []capacityprobe.CreatedLayerSample{
		{Sequence: 1, Operation: "open", Source: "linux_syscall", Outcome: "observed", Started: start, Ended: start.Add(time.Microsecond)},
		{Sequence: 2, Operation: "qstatv", Source: "linux_syscall", Outcome: "observed", Started: start.Add(2 * time.Microsecond), Ended: start.Add(3 * time.Microsecond)},
	}}
}

func TestMapQuotaStateEvidencePreservesLiteralSlots(t *testing.T) {
	p := quotaStateFixture()
	before := quotaStateFixture()
	got, err := MapQuotaStateEvidence(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ABI != "linux_v6.8_fs_quota_statv_v1_160_le" || got.Version != 1 || got.Flags != 0x3f || got.InCoreDQs != 0xfedcba98 {
		t.Fatalf("header changed: %+v", got)
	}
	if got.User != (QuotaFileState{0xf123456789abcdef, 0xffffffffffffffff, 0xffffffff, true, true}) ||
		got.Group != (QuotaFileState{0x8122334455667788, 0x9102030405060708, 0x84332211, true, true}) ||
		got.Project != (QuotaFileState{0xa172635445362718, 0xb827364554637281, 0xc6778899, true, true}) {
		t.Fatalf("slots changed: %+v", got)
	}
	if got.Raw != p.QStatV || got.Sample != p.Samples[1] {
		t.Fatal("raw/sample provenance changed")
	}
	// SHA256 expectation is frozen independently alongside the RED scaffold.
	const wantDigest = "sha256:369e234670e7d86503c2f7ac4b942160ee1d5dac75bf43ba6206891bf0a8977d"
	if got.RawDigest != wantDigest {
		t.Fatal("full raw digest changed:", got.RawDigest)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("input mutated")
	}
}

func quotaStateClone(p capacityprobe.CreatedLayerPhysical) capacityprobe.CreatedLayerPhysical {
	p.Samples = append([]capacityprobe.CreatedLayerSample(nil), p.Samples...)
	for i := range p.Samples {
		if p.Samples[i].Errno != nil {
			value := *p.Samples[i].Errno
			p.Samples[i].Errno = &value
		}
	}
	return p
}

func quotaStateMapUnchanged(t *testing.T, p capacityprobe.CreatedLayerPhysical) QuotaStateEvidence {
	t.Helper()
	before := quotaStateClone(p)
	got, err := MapQuotaStateEvidence(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("success mutated input")
	}
	return got
}

func TestMapQuotaStateEvidenceKnownFlagVectors(t *testing.T) {
	// Expected pairs are independently specified UAPI USR/GROUP/PROJ ACCT/ENFD bits.
	for _, tc := range []struct {
		flags  uint16
		states [6]bool
	}{
		{0x01, [6]bool{true, false, false, false, false, false}},
		{0x02, [6]bool{false, true, false, false, false, false}},
		{0x03, [6]bool{true, true, false, false, false, false}},
		{0x04, [6]bool{false, false, true, false, false, false}},
		{0x08, [6]bool{false, false, false, true, false, false}},
		{0x0c, [6]bool{false, false, true, true, false, false}},
		{0x10, [6]bool{false, false, false, false, true, false}},
		{0x20, [6]bool{false, false, false, false, false, true}},
		{0x30, [6]bool{false, false, false, false, true, true}},
		{0x15, [6]bool{true, false, true, false, true, false}},
		{0x2a, [6]bool{false, true, false, true, false, true}},
		{0x3f, [6]bool{true, true, true, true, true, true}},
	} {
		t.Run(fmt.Sprintf("%02x", tc.flags), func(t *testing.T) {
			p := quotaStateFixture()
			binary.LittleEndian.PutUint16(p.QStatV[2:4], tc.flags)
			got := quotaStateMapUnchanged(t, p)
			states := [6]bool{got.User.Accounting, got.User.Enforcement, got.Group.Accounting, got.Group.Enforcement, got.Project.Accounting, got.Project.Enforcement}
			if got.Flags != tc.flags || states != tc.states {
				t.Fatalf("known diagnostic flags normalized or misassigned: %x %v", got.Flags, states)
			}
			if got.User.Inode != 0xf123456789abcdef || got.Group.Inode != 0x8122334455667788 || got.Project.Inode != 0xa172635445362718 {
				t.Fatal("inactive slot filtered")
			}
		})
	}
}

func TestMapQuotaStateEvidencePreservesSlotEdgeRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values [3]QuotaFileState
	}{
		{"zero", [3]QuotaFileState{}},
		{"nullfsino", [3]QuotaFileState{{Inode: ^uint64(0)}, {Inode: ^uint64(0)}, {Inode: ^uint64(0)}}},
		{"duplicate_identity", [3]QuotaFileState{{Inode: 17, NBlocksRaw: 1, NextentsRaw: 2}, {Inode: 17, NBlocksRaw: 3, NextentsRaw: 4}, {Inode: 17, NBlocksRaw: 5, NextentsRaw: 6}}},
		{"nonzero_inode_zero_counters", [3]QuotaFileState{{Inode: 1}, {Inode: 2}, {Inode: 3}}},
		{"full_width", [3]QuotaFileState{{Inode: ^uint64(0), NBlocksRaw: ^uint64(0), NextentsRaw: ^uint32(0)}, {Inode: ^uint64(0), NBlocksRaw: ^uint64(0), NextentsRaw: ^uint32(0)}, {Inode: ^uint64(0), NBlocksRaw: ^uint64(0), NextentsRaw: ^uint32(0)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := quotaStateFixture()
			// Only user accounting is active, but group/project raw records remain.
			p.QStatV[2] = 1
			for i, base := range []int{8, 32, 56} {
				binary.LittleEndian.PutUint64(p.QStatV[base:base+8], tc.values[i].Inode)
				binary.LittleEndian.PutUint64(p.QStatV[base+8:base+16], tc.values[i].NBlocksRaw)
				binary.LittleEndian.PutUint32(p.QStatV[base+16:base+20], tc.values[i].NextentsRaw)
			}
			got := quotaStateMapUnchanged(t, p)
			want := tc.values
			want[0].Accounting = true
			if [3]QuotaFileState{got.User, got.Group, got.Project} != want {
				t.Fatalf("records filtered, scaled, capped or deduplicated: %+v", got)
			}
		})
	}
}

func TestMapQuotaStateEvidenceDetachedDiagnostics(t *testing.T) {
	p := quotaStateFixture()
	got := quotaStateMapUnchanged(t, p)
	frozen := got
	p.QStatV[8] = 0
	p.QStatV[80] = 0
	p.Samples[1].Operation = "replaced"
	p.Samples[1].Started = time.Time{}
	p.Samples[1].Outcome = "failed"
	n := uint64(5)
	p.Samples[1].Errno = &n
	if got != frozen {
		t.Fatal("input aliases escaped")
	}
	copied := got
	copied.Raw[97]++
	copied.Sample.Operation = "changed"
	if got != frozen {
		t.Fatal("result copy aliases escaped")
	}
	// Opaque semantic timer/warn data must affect full-record provenance, not slots.
	for i := 80; i < 98; i++ {
		changed := quotaStateFixture()
		changed.QStatV[i] ^= 0x40
		diagnostic := quotaStateMapUnchanged(t, changed)
		if diagnostic.User != got.User || diagnostic.Group != got.Group || diagnostic.Project != got.Project || diagnostic.Raw[i] != changed.QStatV[i] || diagnostic.RawDigest == got.RawDigest {
			t.Fatalf("opaque byte %d lost or interpreted", i)
		}
	}
	for _, verified := range []any{capacitysnapshot.Verified{}, capacitysnapshot.VerifiedEvidenceV2{}} {
		if reflect.TypeOf(got).ConvertibleTo(reflect.TypeOf(verified)) {
			t.Fatal("diagnostics convertible to verified authority")
		}
	}
}

func TestMapQuotaStateEvidenceRejectsMalformedLayout(t *testing.T) {
	cases := []struct {
		name   string
		change func(*capacityprobe.CreatedLayerPhysical)
	}{
		{"zero_version", func(p *capacityprobe.CreatedLayerPhysical) { p.QStatV[0] = 0 }},
		{"next_version", func(p *capacityprobe.CreatedLayerPhysical) { p.QStatV[0] = 2 }},
		{"negative_version", func(p *capacityprobe.CreatedLayerPhysical) { p.QStatV[0] = 0xff }},
		{"zero_flags", func(p *capacityprobe.CreatedLayerPhysical) { p.QStatV[2] = 0 }},
	}
	for bit := 6; bit < 16; bit++ {
		bit := bit
		cases = append(cases, struct {
			name   string
			change func(*capacityprobe.CreatedLayerPhysical)
		}{fmt.Sprintf("unknown_bit_%d", bit), func(p *capacityprobe.CreatedLayerPhysical) {
			binary.LittleEndian.PutUint16(p.QStatV[2:4], 0x3f|uint16(1<<bit))
		}})
	}
	// Isolate every reserved/padding position while all semantic fields stay valid.
	for _, span := range [][2]int{{1, 2}, {28, 32}, {52, 56}, {76, 80}, {98, 160}} {
		for pos := span[0]; pos < span[1]; pos++ {
			pos := pos
			cases = append(cases, struct {
				name   string
				change func(*capacityprobe.CreatedLayerPhysical)
			}{fmt.Sprintf("padding_%03d", pos), func(p *capacityprobe.CreatedLayerPhysical) { p.QStatV[pos] = 1 }})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := quotaStateFixture()
			tc.change(&p)
			quotaStateRejectUnchanged(t, p)
		})
	}
}

func quotaStateRejectUnchanged(t *testing.T, p capacityprobe.CreatedLayerPhysical) {
	t.Helper()
	before := quotaStateClone(p)
	got, err := MapQuotaStateEvidence(p)
	if err == nil || got != (QuotaStateEvidence{}) {
		t.Fatalf("invalid evidence returned value or no error: %+v %v", got, err)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("rejection mutated input")
	}
}

func TestMapQuotaStateEvidenceRejectsInvalidSamples(t *testing.T) {
	cases := []struct {
		name   string
		change func(*capacityprobe.CreatedLayerPhysical)
	}{
		{"absent", func(p *capacityprobe.CreatedLayerPhysical) { p.Samples = nil }},
		{"wrong_operation", func(p *capacityprobe.CreatedLayerPhysical) { p.Samples[1].Operation = "qget_target" }},
		{"duplicate", func(p *capacityprobe.CreatedLayerPhysical) { p.Samples = append(p.Samples, p.Samples[1]) }},
		{"truncated", func(p *capacityprobe.CreatedLayerPhysical) { p.SamplesTruncated = true }},
		{"over_64", func(p *capacityprobe.CreatedLayerPhysical) {
			p.Samples = append(p.Samples, make([]capacityprobe.CreatedLayerSample, 63)...)
		}},
	}
	for _, index := range []int{0, 1} {
		for _, problem := range []string{"source", "outcome", "errno_zero", "errno_failure", "sequence_zero", "sequence_wrong", "start_zero", "end_zero", "equal", "reversed"} {
			index, problem := index, problem
			cases = append(cases, struct {
				name   string
				change func(*capacityprobe.CreatedLayerPhysical)
			}{fmt.Sprintf("prefix_%d_%s", index, problem), func(p *capacityprobe.CreatedLayerPhysical) {
				s := &p.Samples[index]
				switch problem {
				case "source":
					s.Source = "fixture"
				case "outcome":
					s.Outcome = "unavailable"
				case "errno_zero":
					n := uint64(0)
					s.Errno = &n
				case "errno_failure":
					n := uint64(5)
					s.Errno = &n
				case "sequence_zero":
					s.Sequence = 0
				case "sequence_wrong":
					s.Sequence = 42
				case "start_zero":
					s.Started = time.Time{}
				case "end_zero":
					s.Ended = time.Time{}
				case "equal":
					s.Ended = s.Started
				case "reversed":
					s.Ended = s.Started.Add(-time.Nanosecond)
				}
			}})
		}
	}
	cases = append(cases,
		struct {
			name   string
			change func(*capacityprobe.CreatedLayerPhysical)
		}{"neighbor_overlap", func(p *capacityprobe.CreatedLayerPhysical) {
			p.Samples[1].Started = p.Samples[0].Started.Add(time.Nanosecond)
		}},
		struct {
			name   string
			change func(*capacityprobe.CreatedLayerPhysical)
		}{"neighbor_reversal", func(p *capacityprobe.CreatedLayerPhysical) {
			p.Samples[1].Started = p.Samples[0].Started.Add(-2 * time.Microsecond)
			p.Samples[1].Ended = p.Samples[0].Started.Add(-time.Microsecond)
		}},
		struct {
			name   string
			change func(*capacityprobe.CreatedLayerPhysical)
		}{"preinitialized_failed_syscall", func(p *capacityprobe.CreatedLayerPhysical) {
			p.QStatV = [160]byte{0: 1, 2: 1}
			p.Samples[1].Outcome = "unavailable"
			n := uint64(5)
			p.Samples[1].Errno = &n
		}},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { p := quotaStateFixture(); tc.change(&p); quotaStateRejectUnchanged(t, p) })
	}
}

func TestMapQuotaStateEvidenceIgnoresLaterOrdinaryFailures(t *testing.T) {
	for _, operation := range []string{"qget_target", "qget_default0", "root_identity_recheck"} {
		t.Run(operation, func(t *testing.T) {
			p := quotaStateFixture()
			n := uint64(5)
			p.Samples = append(p.Samples, capacityprobe.CreatedLayerSample{Sequence: 3, Operation: operation, Source: "linux_syscall", Outcome: "unavailable", Started: p.Samples[1].Ended, Ended: p.Samples[1].Ended.Add(time.Microsecond), Errno: &n})
			p.ObservationComplete = false
			got := quotaStateMapUnchanged(t, p)
			if got.Sample != p.Samples[1] || got.Raw != p.QStatV {
				t.Fatal("later failure erased successful qstatv")
			}
			p.Samples = append(p.Samples, capacityprobe.CreatedLayerSample{Operation: "qstatv"})
			quotaStateRejectUnchanged(t, p)
		})
	}
	t.Run("exactly_64", func(t *testing.T) {
		p := quotaStateFixture()
		p.Samples = append(p.Samples, make([]capacityprobe.CreatedLayerSample, 62)...)
		quotaStateMapUnchanged(t, p)
	})
	t.Run("qstatv_first", func(t *testing.T) {
		p := quotaStateFixture()
		p.Samples = p.Samples[1:]
		p.Samples[0].Sequence = 1
		quotaStateMapUnchanged(t, p)
	})
	t.Run("touching_intervals", func(t *testing.T) {
		p := quotaStateFixture()
		p.Samples[1].Started = p.Samples[0].Ended
		quotaStateMapUnchanged(t, p)
	})
}
