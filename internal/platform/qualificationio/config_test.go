package qualificationio

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/config"
	"io"
	"strings"
	"testing"
)

func testControl() Control {
	p, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	n, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	o := Object{Device: "8:1", FSUUID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Inode: 1, Owner: 10002, Mode: 0040700, ProjectID: 1234}
	c := Control{Schema: ControlSchema, Candidate: "sha256:" + strings.Repeat("a", 64), WindowID: "window-1", Nonce: strings.Repeat("b", 64), Token: strings.Repeat("c", 64), Architecture: "amd64", Role: "worker", ContainerID: strings.Repeat("d", 64), ImageDigest: "sha256:" + strings.Repeat("e", 64), BinaryDigest: "sha256:" + strings.Repeat("f", 64), Profile: p.Ref(), Network: n.Evidence(), HostTestRoot: "/product/.qualification/window-1", MountRoot: "/product/.qualification/window-1", Root: o, Target: o, Child: o, NativeDigest: "sha256:" + strings.Repeat("1", 64), CompatDigest: "sha256:" + strings.Repeat("2", 64)}
	c.Target.Inode = 2
	c.Root.Owner = 0
	c.Root.Mode = 0040755
	c.Child.Inode = 3
	c.Child.Mode = 0100600
	return c
}

func TestControlStrictFiniteCodec(t *testing.T) {
	c := testControl()
	raw, err := EncodeControl(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeControl(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("null"), append(append([]byte{}, raw...), raw...), bytes.Replace(raw, []byte(`"schema":`), []byte(`"extra":0,"schema":`), 1), bytes.Replace(raw, []byte(`"role":"worker"`), []byte(`"role":"backend","role":"worker"`), 1), bytes.Replace(raw, []byte(`"backend_case":""`), []byte(`"backend_case":null`), 1), bytes.Replace(raw, []byte(`,"backend_case":""`), nil, 1)} {
		if _, err = DecodeControl(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for name, change := range map[string]func(*Control){"path": func(c *Control) { c.HostTestRoot = "/tmp/elsewhere" }, "traversal": func(c *Control) { c.WindowID = ".." }, "caps-profile": func(c *Control) { c.Profile.Revision++ }, "network": func(c *Control) { c.Network.EnforcementStatus = "legacy_unenforced" }, "alias-helper": func(c *Control) { c.NativeDigest = c.BinaryDigest }, "same-object": func(c *Control) { c.Child.Inode = c.Target.Inode }, "directory-child": func(c *Control) { c.Child.Mode = 0040700 }, "unknown-case": func(c *Control) { c.Role = "backend"; c.BackendCase = "shell" }} {
		t.Run(name, func(t *testing.T) {
			v := c
			change(&v)
			if v.Validate() == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestFrameBoundedExactCommands(t *testing.T) {
	c := testControl()
	f := NewCommand(c, 1, "quota-denied", "quota-denied")
	var wire bytes.Buffer
	if err := WriteFrame(&wire, f); err != nil {
		t.Fatal(err)
	}
	if err := expectCommand(&wire, c, 1, "quota-denied", "quota-denied"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Frame){func(f *Frame) { f.Token = "other" }, func(f *Frame) { f.WindowID = "old" }, func(f *Frame) { f.Case = "other" }, func(f *Frame) { f.Phase = "other" }, func(f *Frame) { f.Sequence = 2 }, func(f *Frame) { f.Target = &FDIdentity{} }} {
		v := f
		change(&v)
		var b bytes.Buffer
		WriteFrame(&b, v)
		if expectCommand(&b, c, 1, f.Phase, f.Case) == nil {
			t.Fatal("accepted mixed/replayed command")
		}
	}
	for _, bad := range [][]byte{nil, {0, 0}, {0, 0, 0, 0}, {0, 4, 0, 1}, {0, 0, 0, 2, '{', '}'}} {
		if _, err := ReadFrame(bytes.NewReader(bad)); err == nil {
			t.Fatal("accepted short/oversized/missing envelope")
		}
	}
	raw, _ := json.Marshal(f)
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"phase":`), []byte(`"phase":"old","phase":`), 1), append(raw, []byte(` {}`)...), bytes.Replace(raw, []byte(`"phase":`), []byte(`"unknown":0,"phase":`), 1)} {
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, uint32(len(bad)))
		b.Write(bad)
		if _, err := ReadFrame(&b); err == nil {
			t.Fatal("accepted noncanonical frame")
		}
	}
	if WriteFrame(zeroWriter{}, f) != io.ErrShortWrite {
		t.Fatal("short write accepted")
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) {
	if len(b) > 1 {
		return len(b) - 1, nil
	}
	return 1, nil
}
func TestFrameRejectPositiveShortWriter(t *testing.T) {
	c := testControl()
	if err := WriteFrame(shortWriter{}, NewCommand(c, 1, "quota-denied", "quota-denied")); err != io.ErrShortWrite {
		t.Fatalf("short writer accepted: %v", err)
	}
}

func TestSetterActualABIBuffers(t *testing.T) {
	for _, abi := range []string{"native64", "compat32"} {
		r, err := setterRequests(abi)
		if err != nil {
			t.Fatal(err)
		}
		size := 8
		if abi == "compat32" {
			size = 4
		}
		if r.flagsBytes != size {
			t.Fatal("wrong long width")
		}
		attr := make([]byte, 28)
		binary.LittleEndian.PutUint32(attr, 0x200)
		binary.LittleEndian.PutUint32(attr[12:], 1234)
		attempt, err := setterAttempt("FS_IOC_FSSETXATTR", attr, nil)
		if err != nil || binary.LittleEndian.Uint32(attempt[12:]) != 1235 {
			t.Fatal("setter did not change project")
		}
		flags := make([]byte, size)
		binary.LittleEndian.PutUint32(flags, 0x20000000)
		attempt, err = setterAttempt("FS_IOC_SETFLAGS", nil, flags)
		if err != nil || binary.LittleEndian.Uint32(attempt) != 0 {
			t.Fatal("setter did not clear inheritance")
		}
	}
}
