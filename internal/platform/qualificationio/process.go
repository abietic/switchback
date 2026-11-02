package qualificationio

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"io"
	"strconv"
	"strings"
)

// LocalProcess deliberately has no host PID/status fields. Host observations
// must be made independently while the emitting thread is held at a phase.
type LocalProcess struct {
	PID              uint64       `json:"pid"`
	TID              uint64       `json:"tid"`
	TGID             uint64       `json:"tgid"`
	StartTicks       uint64       `json:"start_ticks"`
	ThreadStartTicks uint64       `json:"thread_start_ticks"`
	Boot             string       `json:"boot"`
	Snapshot         ProcSnapshot `json:"snapshot"`
}

type WriteResult struct {
	Requested  uint64 `json:"requested"`
	Written    uint64 `json:"written"`
	WriteErrno uint64 `json:"write_errno"`
	SyncErrno  uint64 `json:"sync_errno"`
}

// BackendObservation includes explicit availability: absence is not a zero
// entry count. In particular a missing-DAC helper may not read a private dir.
// The full-cap holder reports these same objects before/after the negative op.
type BackendObservation struct {
	Operation           string `json:"operation"`
	RequestedOwner      uint64 `json:"requested_owner"`
	Errno               uint64 `json:"errno"`
	Entries             uint64 `json:"entries"`
	EntriesAvailable    bool   `json:"entries_available"`
	EntriesErrno        uint64 `json:"entries_errno"`
	ChildAvailable      bool   `json:"child_available"`
	ChildErrno          uint64 `json:"child_errno"`
	ChildInode          uint64 `json:"child_inode"`
	ChildOwner          uint64 `json:"child_owner"`
	AttributesAvailable bool   `json:"attributes_available"`
	AttributesErrno     uint64 `json:"attributes_errno"`
}

// Frame is a canonical length-prefixed private observation protocol. Commands
// contain only the envelope (all observation pointers nil); they select the
// single next phase, never a path, executable, endpoint or resource amount.
type Frame struct {
	Schema           string              `json:"schema"`
	Candidate        string              `json:"candidate"`
	WindowID         string              `json:"window_id"`
	Nonce            string              `json:"nonce"`
	Token            string              `json:"token"`
	Sequence         uint64              `json:"sequence"`
	Phase            string              `json:"phase"`
	Case             string              `json:"case"`
	Process          *LocalProcess       `json:"process"`
	Parent           *LocalProcess       `json:"parent"`
	Target           *FDIdentity         `json:"target"`
	Child            *FDIdentity         `json:"child"`
	Write            *WriteResult        `json:"write"`
	Setter           *SetterIO           `json:"setter"`
	Backend          *BackendObservation `json:"backend"`
	ELF              *ELF                `json:"elf"`
	ExecErrno        uint64              `json:"exec_errno"`
	UnsupportedCause string              `json:"unsupported_cause"`
}

func NewCommand(c Control, sequence uint64, phase, testCase string) Frame {
	return Frame{Schema: FrameSchema, Candidate: c.Candidate, WindowID: c.WindowID, Nonce: c.Nonce, Token: c.Token, Sequence: sequence, Phase: phase, Case: testCase}
}

func (f Frame) Match(c Control, seq uint64, phase, testCase string) error {
	if f.Schema != FrameSchema || f.Candidate != c.Candidate || f.WindowID != c.WindowID || f.Nonce != c.Nonce || f.Token != c.Token || f.Sequence != seq || f.Phase != phase || f.Case != testCase {
		return fmt.Errorf("qualification phase identity mismatch")
	}
	return nil
}
func (f Frame) command() bool {
	return f.Process == nil && f.Parent == nil && f.Target == nil && f.Child == nil && f.Write == nil && f.Setter == nil && f.Backend == nil && f.ELF == nil && f.ExecErrno == 0 && f.UnsupportedCause == ""
}

func WriteFrame(w io.Writer, f Frame) error {
	raw, err := json.Marshal(f)
	if err != nil || len(raw) > MaxFrameBytes-4 || len(raw) == 0 {
		return fmt.Errorf("qualification frame size invalid")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, raw)
}
func ReadFrame(r io.Reader) (Frame, error) {
	var out Frame
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return out, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > MaxFrameBytes-4 {
		return out, fmt.Errorf("qualification frame size invalid")
	}
	raw := make([]byte, int(n))
	if _, err := io.ReadFull(r, raw); err != nil {
		return out, err
	}
	if err := strictCanonical(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n != len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func expectCommand(r io.Reader, c Control, seq uint64, phase, testCase string) error {
	f, err := ReadFrame(r)
	if err != nil {
		return err
	}
	if err := f.Match(c, seq, phase, testCase); err != nil {
		return err
	}
	if !f.command() {
		return fmt.Errorf("qualification command contains facts")
	}
	return nil
}

func statTicks(raw []byte, id uint64) (uint64, error) {
	s := string(raw)
	i := strings.LastIndex(s, ") ")
	open := strings.Index(s, " (")
	if open < 1 || i <= open || !strings.HasSuffix(s, "\n") {
		return 0, fmt.Errorf("qualification stat invalid")
	}
	pid, err := strconv.ParseUint(s[:open], 10, 64)
	if err != nil || pid != id {
		return 0, fmt.Errorf("qualification stat pid invalid")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("qualification stat short")
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("qualification stat ticks invalid")
	}
	return v, nil
}

func sameLocalProcess(a, b LocalProcess) bool {
	if a.PID != b.PID || a.TID != b.TID || a.TGID != b.TGID || a.StartTicks != b.StartTicks || a.ThreadStartTicks != b.ThreadStartTicks || a.Boot != b.Boot || a.Snapshot.ExeDigest != b.Snapshot.ExeDigest || string(a.Snapshot.Cgroup) != string(b.Snapshot.Cgroup) {
		return false
	}
	x, e := procstatus.Parse(a.Snapshot.Status)
	y, f := procstatus.Parse(b.Snapshot.Status)
	return e == nil && f == nil && x == y
}

func verifyProcess(c Control, p LocalProcess, mask uint64, binary string, worker bool) error {
	if !uuidRE.MatchString(p.Boot) || p.StartTicks == 0 || p.ThreadStartTicks == 0 || p.PID != p.TGID {
		return fmt.Errorf("qualification process lifetime invalid")
	}
	start, err := statTicks(p.Snapshot.Stat, p.PID)
	if err != nil || start != p.StartTicks {
		return fmt.Errorf("qualification process stat mismatch")
	}
	thread, err := statTicks(p.Snapshot.ThreadStat, p.TID)
	if err != nil || thread != p.ThreadStartTicks {
		return fmt.Errorf("qualification thread stat mismatch")
	}
	s, err := procstatus.Parse(p.Snapshot.Status)
	if err != nil {
		return err
	}
	uid := uint32(0)
	if worker {
		uid = 10002
	}
	expected := fmt.Sprintf("%016x", mask)
	if s.PID != p.PID || s.TID != p.TID || s.TGID != p.TGID || s.UIDs != [4]uint32{uid, uid, uid, uid} || s.GIDs != [4]uint32{uid, uid, uid, uid} || s.CapPrm != expected || s.CapEff != expected || s.CapBnd != expected || s.CapInh != "0000000000000000" || s.CapAmb != s.CapInh || s.NoNewPrivs != 1 || s.Seccomp != 2 || p.Snapshot.ExeDigest != binary {
		return fmt.Errorf("qualification process identity refused")
	}
	if worker {
		cg := "0::/switchback.slice/switchback-workers.slice/docker-" + c.ContainerID + ".scope\n"
		if string(p.Snapshot.Cgroup) != "0::/\n" && string(p.Snapshot.Cgroup) != cg {
			return fmt.Errorf("qualification cgroup invalid")
		}
	}
	return verifyBind(c, p.Snapshot.MountInfo)
}

func verifyBind(c Control, raw []byte) error {
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			return fmt.Errorf("qualification mount table invalid")
		}
		if f[4] != DataPath {
			continue
		}
		if found || f[2] != c.Root.Device || unescapeMount(f[3]) != c.MountRoot {
			return fmt.Errorf("qualification bind mismatch")
		}
		found = true
		ro := false
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				ro = true
			}
		}
		fs := ""
		for i := 6; i < len(f); i++ {
			if f[i] == "-" && i+1 < len(f) {
				fs = f[i+1]
				break
			}
		}
		if ro || fs != "xfs" {
			return fmt.Errorf("qualification bind filesystem invalid")
		}
	}
	if !found {
		return fmt.Errorf("qualification bind absent")
	}
	return nil
}
func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
