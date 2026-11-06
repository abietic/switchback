package qualificationio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
)

func TestExactHelperCaseOrderAndExecFailures(t *testing.T) {
	c := testControl()
	for seq, testCase := range map[uint64]string{4: "native:FS_IOC_FSSETXATTR", 5: "native:FS_IOC_SETFLAGS", 6: "compat32:FS_IOC_FSSETXATTR", 7: "compat32:FS_IOC_SETFLAGS"} {
		abi := "native"
		if seq >= 6 {
			abi = "compat32"
		}
		f := NewCommand(c, seq, "ioctl-start", testCase)
		if _, err := validateHelperStart(c, f, abi); err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*Frame){func(f *Frame) { f.Sequence++ }, func(f *Frame) { f.Case = "native:FS_IOC_FSSETXATTR:extra" }, func(f *Frame) { f.WindowID = "old" }, func(f *Frame) { f.Nonce = "other" }, func(f *Frame) { f.Token = "other" }, func(f *Frame) { f.Phase = "setter-operate" }, func(f *Frame) { f.Process = &LocalProcess{} }} {
			bad := f
			change(&bad)
			if _, err := validateHelperStart(c, bad, abi); err == nil {
				t.Fatal("accepted out-of-order/mixed helper start")
			}
		}
	}
	for _, errno := range []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.EIO, syscall.ECANCELED, syscall.ETIMEDOUT} {
		if _, _, err := compatExecOutcome("compat32", &os.PathError{Op: "fork/exec", Path: CompatHelperPath, Err: errno}); err == nil {
			t.Fatalf("treated %v as unsupported", errno)
		}
	}
	if _, _, err := compatExecOutcome("native", syscall.ENOEXEC); err == nil {
		t.Fatal("native ENOEXEC accepted")
	}
	n, cause, err := compatExecOutcome("compat32", &os.PathError{Op: "fork/exec", Path: "/proc/self/fd/5", Err: syscall.ENOEXEC})
	if err != nil || n != 8 || cause != "execve_ENOEXEC" {
		t.Fatalf("actual ENOEXEC classification %d %s %v", n, cause, err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func TestProtocolEOFTimeoutCancelAndBoundedWrites(t *testing.T) {
	c := testControl()
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded, errors.New("channel loss")} {
		if expectCommand(errorReader{err}, c, 1, "quota-denied", "quota-denied") == nil {
			t.Fatal("accepted failed channel")
		}
	}
	f := NewCommand(c, 1, "quota-denied", "quota-denied")
	f.Process = &LocalProcess{Snapshot: ProcSnapshot{MountInfo: make([]byte, MaxFrameBytes)}}
	var out bytes.Buffer
	if WriteFrame(&out, f) == nil || out.Len() != 0 {
		t.Fatal("oversized frame partially written")
	}
	var short bytes.Buffer
	WriteFrame(&short, NewCommand(c, 1, "quota-denied", "quota-denied"))
	raw := short.Bytes()
	for _, n := range []int{1, 3, 4, len(raw) - 1} {
		if _, err := ReadFrame(bytes.NewReader(raw[:n])); err == nil {
			t.Fatal("truncated frame accepted")
		}
	}
}

func TestBackendCapabilitiesAndUnavailableObservations(t *testing.T) {
	w := testControl()
	for _, testCase := range []string{"empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff"} {
		c := w
		c.Role = "backend"
		c.BackendCase = testCase
		c.NativeDigest = ""
		c.CompatDigest = ""
		c.Target.Owner = 0
		c.Target.Mode = 0040755
		c.Child.Owner = 0
		if testCase == "empty" {
			c.Child = Object{}
		}
		if negativeCase(testCase) {
			c.Target.Owner = 10002
			c.Child.Owner = 10002
			c.Target.Mode = uint64(0040000 | backendMode(testCase))
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s %v", testCase, err)
		}
		if negativeCase(testCase) && backendMask(testCase)&^uint64(0xb) != 0 {
			t.Fatal("negative helper gained caps")
		}
	}
	for _, testCase := range []string{"empty", "nonempty", "private", "sticky", "worker-handoff"} {
		want := uint64(0)
		if testCase == "nonempty" || testCase == "worker-handoff" {
			want = 1
		}
		if backendCleanupEntries(testCase) != want {
			t.Fatal("cleanup accepted missing/extra entries")
		}
	}
	f := NewCommand(w, 0, "backend-before", "without-dac-override")
	f.Backend = &BackendObservation{Operation: "unlink_private_child", EntriesAvailable: false, EntriesErrno: 13, ChildAvailable: false, ChildErrno: 13, AttributesAvailable: false, AttributesErrno: 9}
	var b bytes.Buffer
	if err := WriteFrame(&b, f); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&b)
	if err != nil || got.Backend == nil || got.Backend.EntriesAvailable || got.Backend.EntriesErrno != 13 || got.Backend.ChildAvailable {
		t.Fatal("unavailable observation became fabricated empty directory")
	}
}

func TestFrozenHelperELFIdentityAndHeader(t *testing.T) {
	c := testControl()
	for _, arch := range []string{"amd64", "arm64"} {
		c.Architecture = arch
		for _, abi := range []string{"native", "compat32"} {
			class, machine, size, digest, artifact := uint64(2), uint16(62), 64, c.NativeDigest, "worker-native-helper"
			if arch == "arm64" {
				machine = 183
			}
			if abi == "compat32" {
				class, machine, size, digest, artifact = 1, 3, 52, c.CompatDigest, "worker-compat-helper"
				if arch == "arm64" {
					machine = 40
				}
			}
			h := make([]byte, size)
			copy(h, []byte{0x7f, 'E', 'L', 'F'})
			h[4] = byte(class)
			h[5] = 1
			h[6] = 1
			binary.LittleEndian.PutUint16(h[16:], 2)
			binary.LittleEndian.PutUint16(h[18:], machine)
			binary.LittleEndian.PutUint32(h[20:], 1)
			offset := 52
			if class == 1 {
				offset = 40
			}
			binary.LittleEndian.PutUint16(h[offset:], uint16(size))
			e := ELF{Artifact: artifact, Digest: digest, Header: h, Class: class, Machine: uint64(machine)}
			if err := expectedELF(c, abi, e); err != nil {
				t.Fatal(err)
			}
			bad := e
			bad.Digest = c.BinaryDigest
			if expectedELF(c, abi, bad) == nil {
				t.Fatal("parent aliased as helper")
			}
			bad = e
			bad.Header = append([]byte{}, h...)
			bad.Header[18]++
			if expectedELF(c, abi, bad) == nil {
				t.Fatal("machine header mismatch accepted")
			}
		}
	}
}
