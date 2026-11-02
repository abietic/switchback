package qualificationio

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// These wire types belong only to private native qualification v2. Raw helper
// DTOs retain complete bytes. A receipt stores every unique mount table once.
type MountTable struct {
	Digest string `json:"digest"`
	Raw    []byte `json:"raw"`
}

type EncodedProcSnapshot struct {
	Stat         []byte `json:"stat"`
	ThreadStat   []byte `json:"thread_stat"`
	Status       []byte `json:"status"`
	Cgroup       []byte `json:"cgroup"`
	MountInfoRef string `json:"mountinfo_ref"`
	ExeDigest    string `json:"exe_digest"`
}

type EncodedProcessEvidence struct {
	PID              uint64              `json:"pid"`
	TID              uint64              `json:"tid"`
	TGID             uint64              `json:"tgid"`
	StartTicks       uint64              `json:"start_ticks"`
	ThreadStartTicks uint64              `json:"thread_start_ticks"`
	NamespacePID     uint64              `json:"namespace_pid"`
	NamespaceTID     uint64              `json:"namespace_tid"`
	NamespaceTGID    uint64              `json:"namespace_tgid"`
	Boot             string              `json:"boot"`
	HostBefore       EncodedProcSnapshot `json:"host_before"`
	HostAfter        EncodedProcSnapshot `json:"host_after"`
	NamespaceBefore  EncodedProcSnapshot `json:"namespace_before"`
	NamespaceAfter   EncodedProcSnapshot `json:"namespace_after"`
}

type EncodedFDProof struct {
	HostMountInfoBeforeRef string     `json:"host_mountinfo_before_ref"`
	HostMountInfoAfterRef  string     `json:"host_mountinfo_after_ref"`
	HostBefore             FDIdentity `json:"host_before"`
	HostAfter              FDIdentity `json:"host_after"`
	NamespaceBefore        FDIdentity `json:"namespace_before"`
	NamespaceAfter         FDIdentity `json:"namespace_after"`
}

type EncodedSetterFacts struct {
	TargetFD EncodedFDProof `json:"target_fd"`
	IOFD     EncodedFDProof `json:"io_fd"`
	IO       SetterIO       `json:"io"`
}

type EncodedSetterExecution struct {
	ELF              ELF                      `json:"elf"`
	Executors        []EncodedProcessEvidence `json:"executors"`
	Facts            []EncodedSetterFacts     `json:"facts"`
	ExecErrno        uint64                   `json:"exec_errno"`
	ExecOutput       []byte                   `json:"exec_output"`
	UnsupportedCause string                   `json:"unsupported_cause"`
}

type EncodedSetterCase struct {
	Worker        WorkerParent           `json:"worker"`
	ParentProcess EncodedProcessEvidence `json:"parent_process"`
	Architecture  string                 `json:"architecture"`
	ABI           string                 `json:"abi"`
	Command       string                 `json:"command"`
	Execution     EncodedSetterExecution `json:"execution"`
}

type EncodedSetterPayload struct {
	HostTestRoot string              `json:"host_test_root"`
	ABIs         []EncodedSetterCase `json:"abis"`
	MountTables  []MountTable        `json:"mount_tables"`
}

const maxSetterPayloadBytes = 256 << 10
const maxMountReferences = 48

type mountEncoder struct {
	tables map[string][]byte
	err    error
}

func mountDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func (e *mountEncoder) add(raw []byte) string {
	if len(raw) == 0 || len(raw) > maxSetterPayloadBytes {
		e.err = fmt.Errorf("mount table size invalid")
		return ""
	}
	digest := mountDigest(raw)
	e.tables[digest] = raw
	return digest
}

type mountDecoder struct {
	tables map[string][]byte
	used   map[string]bool
	refs   int
	err    error
}

func (d *mountDecoder) get(ref string) []byte {
	d.refs++
	raw, ok := d.tables[ref]
	if !ok || d.refs > maxMountReferences {
		d.err = fmt.Errorf("mount table reference invalid")
		return nil
	}
	d.used[ref] = true
	return raw
}

// EncodeSetterPayload is an untrusted, deterministic raw-to-wire codec. It
// never creates a verified qualification handle or relaxes receipt limits.
func EncodeSetterPayload(p SetterPayload) (EncodedSetterPayload, error) {
	if len(p.ABIs) > 4 {
		return EncodedSetterPayload{}, fmt.Errorf("setter cases oversized")
	}
	e := mountEncoder{tables: map[string][]byte{}}
	out := e.payload(p)
	if e.err != nil {
		return EncodedSetterPayload{}, e.err
	}
	if len(e.tables) > maxMountReferences {
		return EncodedSetterPayload{}, fmt.Errorf("mount table count invalid")
	}
	total := 0
	for digest, raw := range e.tables {
		total += len(raw)
		out.MountTables = append(out.MountTables, MountTable{Digest: digest, Raw: raw})
	}
	if total > maxSetterPayloadBytes {
		return EncodedSetterPayload{}, fmt.Errorf("mount table aggregate oversized")
	}
	sort.Slice(out.MountTables, func(i, j int) bool { return out.MountTables[i].Digest < out.MountTables[j].Digest })
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > maxSetterPayloadBytes {
		return EncodedSetterPayload{}, fmt.Errorf("setter payload oversized")
	}
	return out, nil
}

// DecodeSetterPayload resolves only exact, used digest refs. Callers must first
// strictly decode the wire type (including required/unknown/duplicate fields).
// Decoded refs share immutable raw bytes; no repeated expanded JSON is built.
func DecodeSetterPayload(p EncodedSetterPayload) (SetterPayload, error) {
	if len(p.ABIs) > 4 || len(p.MountTables) == 0 || len(p.MountTables) > maxMountReferences {
		return SetterPayload{}, fmt.Errorf("mount table inventory invalid")
	}
	d := mountDecoder{tables: map[string][]byte{}, used: map[string]bool{}}
	total := 0
	for i, t := range p.MountTables {
		if len(t.Raw) == 0 || len(t.Raw) > maxSetterPayloadBytes || t.Digest != mountDigest(t.Raw) || (i > 0 && p.MountTables[i-1].Digest >= t.Digest) {
			return SetterPayload{}, fmt.Errorf("mount table entry invalid")
		}
		total += len(t.Raw)
		d.tables[t.Digest] = t.Raw
	}
	if total > maxSetterPayloadBytes {
		return SetterPayload{}, fmt.Errorf("mount table aggregate oversized")
	}
	out := d.payload(p)
	if d.err != nil {
		return SetterPayload{}, d.err
	}
	if len(d.used) != len(d.tables) {
		return SetterPayload{}, fmt.Errorf("unused mount table")
	}
	return out, nil
}

func (e *mountEncoder) snapshot(v ProcSnapshot) EncodedProcSnapshot {
	out := EncodedProcSnapshot{}
	out.Stat = v.Stat
	out.ThreadStat = v.ThreadStat
	out.Status = v.Status
	out.Cgroup = v.Cgroup
	out.MountInfoRef = e.add(v.MountInfo)
	out.ExeDigest = v.ExeDigest
	return out
}

func (e *mountEncoder) process(v ProcessEvidence) EncodedProcessEvidence {
	out := EncodedProcessEvidence{}
	out.PID = v.PID
	out.TID = v.TID
	out.TGID = v.TGID
	out.StartTicks = v.StartTicks
	out.ThreadStartTicks = v.ThreadStartTicks
	out.NamespacePID = v.NamespacePID
	out.NamespaceTID = v.NamespaceTID
	out.NamespaceTGID = v.NamespaceTGID
	out.Boot = v.Boot
	out.HostBefore = e.snapshot(v.HostBefore)
	out.HostAfter = e.snapshot(v.HostAfter)
	out.NamespaceBefore = e.snapshot(v.NamespaceBefore)
	out.NamespaceAfter = e.snapshot(v.NamespaceAfter)
	return out
}

func (e *mountEncoder) fd(v FDProof) EncodedFDProof {
	out := EncodedFDProof{}
	out.HostMountInfoBeforeRef = e.add(v.HostMountInfoBefore)
	out.HostMountInfoAfterRef = e.add(v.HostMountInfoAfter)
	out.HostBefore = v.HostBefore
	out.HostAfter = v.HostAfter
	out.NamespaceBefore = v.NamespaceBefore
	out.NamespaceAfter = v.NamespaceAfter
	return out
}

func (e *mountEncoder) facts(v SetterFacts) EncodedSetterFacts {
	out := EncodedSetterFacts{}
	out.TargetFD = e.fd(v.TargetFD)
	out.IOFD = e.fd(v.IOFD)
	out.IO = v.IO
	return out
}

func (e *mountEncoder) execution(v SetterExecution) EncodedSetterExecution {
	out := EncodedSetterExecution{}
	out.ELF = v.ELF
	out.Executors = make([]EncodedProcessEvidence, len(v.Executors))
	for i, x := range v.Executors {
		out.Executors[i] = e.process(x)
	}
	out.Facts = make([]EncodedSetterFacts, len(v.Facts))
	for i, x := range v.Facts {
		out.Facts[i] = e.facts(x)
	}
	out.ExecErrno = v.ExecErrno
	// Empty stdout is honest for failed exec; canonicalize its wire bytes.
	out.ExecOutput = append([]byte{}, v.ExecOutput...)
	out.UnsupportedCause = v.UnsupportedCause
	return out
}

func (e *mountEncoder) caseValue(v SetterCase) EncodedSetterCase {
	out := EncodedSetterCase{}
	out.Worker = v.Worker
	out.ParentProcess = e.process(v.ParentProcess)
	out.Architecture = v.Architecture
	out.ABI = v.ABI
	out.Command = v.Command
	out.Execution = e.execution(v.Execution)
	return out
}

func (e *mountEncoder) payload(v SetterPayload) EncodedSetterPayload {
	out := EncodedSetterPayload{}
	out.HostTestRoot = v.HostTestRoot
	out.ABIs = make([]EncodedSetterCase, len(v.ABIs))
	for i, x := range v.ABIs {
		out.ABIs[i] = e.caseValue(x)
	}
	out.MountTables = []MountTable{}
	return out
}

func (d *mountDecoder) snapshot(v EncodedProcSnapshot) ProcSnapshot {
	out := ProcSnapshot{}
	out.Stat = v.Stat
	out.ThreadStat = v.ThreadStat
	out.Status = v.Status
	out.Cgroup = v.Cgroup
	out.MountInfo = d.get(v.MountInfoRef)
	out.ExeDigest = v.ExeDigest
	return out
}

func (d *mountDecoder) process(v EncodedProcessEvidence) ProcessEvidence {
	out := ProcessEvidence{}
	out.PID = v.PID
	out.TID = v.TID
	out.TGID = v.TGID
	out.StartTicks = v.StartTicks
	out.ThreadStartTicks = v.ThreadStartTicks
	out.NamespacePID = v.NamespacePID
	out.NamespaceTID = v.NamespaceTID
	out.NamespaceTGID = v.NamespaceTGID
	out.Boot = v.Boot
	out.HostBefore = d.snapshot(v.HostBefore)
	out.HostAfter = d.snapshot(v.HostAfter)
	out.NamespaceBefore = d.snapshot(v.NamespaceBefore)
	out.NamespaceAfter = d.snapshot(v.NamespaceAfter)
	return out
}

func (d *mountDecoder) fd(v EncodedFDProof) FDProof {
	out := FDProof{}
	out.HostMountInfoBefore = d.get(v.HostMountInfoBeforeRef)
	out.HostMountInfoAfter = d.get(v.HostMountInfoAfterRef)
	out.HostBefore = v.HostBefore
	out.HostAfter = v.HostAfter
	out.NamespaceBefore = v.NamespaceBefore
	out.NamespaceAfter = v.NamespaceAfter
	return out
}

func (d *mountDecoder) facts(v EncodedSetterFacts) SetterFacts {
	out := SetterFacts{}
	out.TargetFD = d.fd(v.TargetFD)
	out.IOFD = d.fd(v.IOFD)
	out.IO = v.IO
	return out
}

func (d *mountDecoder) execution(v EncodedSetterExecution) SetterExecution {
	out := SetterExecution{}
	out.ELF = v.ELF
	out.Executors = make([]ProcessEvidence, len(v.Executors))
	for i, x := range v.Executors {
		out.Executors[i] = d.process(x)
	}
	out.Facts = make([]SetterFacts, len(v.Facts))
	for i, x := range v.Facts {
		out.Facts[i] = d.facts(x)
	}
	out.ExecErrno = v.ExecErrno
	out.ExecOutput = v.ExecOutput
	out.UnsupportedCause = v.UnsupportedCause
	return out
}

func (d *mountDecoder) caseValue(v EncodedSetterCase) SetterCase {
	out := SetterCase{}
	out.Worker = v.Worker
	out.ParentProcess = d.process(v.ParentProcess)
	out.Architecture = v.Architecture
	out.ABI = v.ABI
	out.Command = v.Command
	out.Execution = d.execution(v.Execution)
	return out
}

func (d *mountDecoder) payload(v EncodedSetterPayload) SetterPayload {
	out := SetterPayload{}
	out.HostTestRoot = v.HostTestRoot
	out.ABIs = make([]SetterCase, len(v.ABIs))
	for i, x := range v.ABIs {
		out.ABIs[i] = d.caseValue(x)
	}
	return out
}
