package qualificationio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
)

type ioctlRequests struct {
	getAttr, setAttr, getFlags, setFlags uint64
	flagsBytes                           int
}

func validateHelperStart(c Control, f Frame, abi string) (string, error) {
	commands := map[uint64]string{4: "native:FS_IOC_FSSETXATTR", 5: "native:FS_IOC_SETFLAGS", 6: "compat32:FS_IOC_FSSETXATTR", 7: "compat32:FS_IOC_SETFLAGS"}
	testCase, ok := commands[f.Sequence]
	if !ok || f.Case != testCase || (abi == "native" && f.Sequence > 5) || (abi == "compat32" && f.Sequence < 6) || (abi != "native" && abi != "compat32") || f.Match(c, f.Sequence, "ioctl-start", testCase) != nil || !f.command() {
		return "", fmt.Errorf("qualification ioctl start invalid")
	}
	if f.Sequence == 4 || f.Sequence == 6 {
		return "FS_IOC_FSSETXATTR", nil
	}
	return "FS_IOC_SETFLAGS", nil
}

func compatExecOutcome(abi string, err error) (uint64, string, error) {
	var errno syscall.Errno
	if abi != "compat32" || !errors.As(err, &errno) || errno != syscall.ENOEXEC {
		return 0, "", fmt.Errorf("qualification helper exec failed: %w", err)
	}
	return uint64(errno), "execve_ENOEXEC", nil
}

func setterRequests(abi string) (ioctlRequests, error) {
	r := ioctlRequests{getAttr: 0x801c581f, setAttr: 0x401c5820, getFlags: 0x80086601, setFlags: 0x40086602, flagsBytes: 8}
	if abi == "compat32" {
		r.getFlags = 0x80046601
		r.setFlags = 0x40046602
		r.flagsBytes = 4
	} else if abi != "native64" {
		return r, fmt.Errorf("qualification ABI invalid")
	}
	return r, nil
}
func setterAttempt(command string, attrs, flags []byte) ([]byte, error) {
	switch command {
	case "FS_IOC_FSSETXATTR":
		if len(attrs) != 28 || binary.LittleEndian.Uint32(attrs)&0x200 == 0 {
			return nil, fmt.Errorf("qualification inherited project missing")
		}
		id := binary.LittleEndian.Uint32(attrs[12:16])
		if id == 0 || id >= 0xfffffffe {
			return nil, fmt.Errorf("qualification project invalid")
		}
		out := append([]byte{}, attrs...)
		binary.LittleEndian.PutUint32(out[12:16], id+1)
		return out, nil
	case "FS_IOC_SETFLAGS":
		if len(flags) != 4 && len(flags) != 8 {
			return nil, fmt.Errorf("qualification flags width invalid")
		}
		v := binary.LittleEndian.Uint32(flags)
		if v&0x20000000 == 0 {
			return nil, fmt.Errorf("qualification inherited flag missing")
		}
		out := append([]byte{}, flags...)
		binary.LittleEndian.PutUint32(out, v&^0x20000000)
		return out, nil
	}
	return nil, fmt.Errorf("qualification setter invalid")
}

func stableFD(a, b FDIdentity) bool {
	return a.FD == b.FD && a.Path == b.Path && a.Device == b.Device && a.FSUUID == b.FSUUID && a.Inode == b.Inode && a.MountID == b.MountID && a.Owner == b.Owner && a.Mode == b.Mode && a.ProjectID == b.ProjectID
}

func expectedELF(c Control, abi string, e ELF) error {
	class, machine, size, digest, artifact := uint64(2), uint64(62), 64, c.NativeDigest, "worker-native-helper"
	if c.Architecture == "arm64" {
		machine = 183
	}
	if abi == "compat32" {
		class = 1
		size = 52
		machine = 3
		digest = c.CompatDigest
		artifact = "worker-compat-helper"
		if c.Architecture == "arm64" {
			machine = 40
		}
	}
	if e.Class != class || e.Machine != machine || len(e.Header) != size || e.Digest != digest || e.Artifact != artifact {
		return fmt.Errorf("qualification helper ELF mismatch")
	}
	h := e.Header
	offset := 52
	if class == 1 {
		offset = 40
	}
	if string(h[:4]) != "\x7fELF" || uint64(h[4]) != class || h[5] != 1 || h[6] != 1 || (binary.LittleEndian.Uint16(h[16:18]) != 2 && binary.LittleEndian.Uint16(h[16:18]) != 3) || uint64(binary.LittleEndian.Uint16(h[18:20])) != machine || binary.LittleEndian.Uint32(h[20:24]) != 1 || int(binary.LittleEndian.Uint16(h[offset:offset+2])) != size {
		return fmt.Errorf("qualification helper ELF header invalid")
	}
	return nil
}
