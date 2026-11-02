package qualificationio

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Codec-only untrusted material, intentionally not a qualification success.
func mountCodecMaterial() SetterPayload {
	mounts := []byte("77 0 8:1 /data /qualification-data rw - xfs /dev/test rw\n")
	s := ProcSnapshot{MountInfo: mounts, Stat: []byte{}, ThreadStat: []byte{}, Status: []byte{}, Cgroup: []byte{}}
	return SetterPayload{HostTestRoot: "/product/.qualification/once", ABIs: []SetterCase{{ParentProcess: ProcessEvidence{HostBefore: s, HostAfter: s, NamespaceBefore: s, NamespaceAfter: s}, Execution: SetterExecution{Executors: []ProcessEvidence{}, Facts: []SetterFacts{}, ExecOutput: []byte{}}}}}
}

func TestMountTablesDeterministicLosslessCodec(t *testing.T) {
	p := mountCodecMaterial()
	a, err := EncodeSetterPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeSetterPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	if !bytes.Equal(ar, br) || len(a.MountTables) != 1 {
		t.Fatal("nondeterministic/unshared raw table")
	}
	decoded, err := DecodeSetterPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.ABIs[0].ParentProcess.HostBefore.MountInfo, p.ABIs[0].ParentProcess.HostBefore.MountInfo) || !bytes.Equal(decoded.ABIs[0].ParentProcess.NamespaceAfter.MountInfo, p.ABIs[0].ParentProcess.NamespaceAfter.MountInfo) {
		t.Fatal("raw bytes lost")
	}
}

func TestMountTablesCodecBoundsAndBadRefs(t *testing.T) {
	p := mountCodecMaterial()
	p.ABIs[0].ParentProcess.HostBefore.MountInfo = make([]byte, maxSetterPayloadBytes+1)
	if _, err := EncodeSetterPayload(p); err == nil {
		t.Fatal("oversized raw table encoded")
	}
	for _, bad := range []string{"digest", "ref", "unused", "duplicate"} {
		t.Run(bad, func(t *testing.T) {
			wire, err := EncodeSetterPayload(mountCodecMaterial())
			if err != nil {
				t.Fatal(err)
			}
			switch bad {
			case "digest":
				wire.MountTables[0].Raw[0]++
			case "ref":
				wire.ABIs[0].ParentProcess.HostBefore.MountInfoRef = "missing"
			case "unused":
				wire.MountTables = append([]MountTable{{Digest: mountDigest([]byte("extra")), Raw: []byte("extra")}}, wire.MountTables...)
			case "duplicate":
				wire.MountTables = append(wire.MountTables, wire.MountTables[0])
			}
			if _, err := DecodeSetterPayload(wire); err == nil {
				t.Fatal("bad raw references accepted")
			}
		})
	}
}

func TestMountTablesCanonicalEmptyExecOutput(t *testing.T) {
	p := mountCodecMaterial()
	p.ABIs[0].Execution.ExecOutput = nil
	encoded, err := EncodeSetterPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"exec_output":null`)) {
		t.Fatal("actual empty exec output encoded as forbidden null")
	}
}
