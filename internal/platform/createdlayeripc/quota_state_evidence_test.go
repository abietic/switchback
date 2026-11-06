package createdlayeripc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

// These are existing opaque transport fields. Nonzero reserved bytes here are
// deliberate: transport preserves bytes; interpretation belongs to the mapper.
func ipcQuotaStateFixture() capacityprobe.CreatedLayerPhysical {
	var p capacityprobe.CreatedLayerPhysical
	for i := range p.QStatV {
		p.QStatV[i] = byte(16 + i%64)
	}
	return p
}

func TestIPCQuotaStateFullRawPreserved(t *testing.T) {
	c, s := fixturePair(t)
	collection := partialCollector(context.Background(), fixtureBinding())
	p := ipcQuotaStateFixture()
	collection.Physical = &p
	s.collect = func(context.Context, Binding) capacityprobe.CreatedLayerCollection { return collection }
	got, err := exchange(t, c, s, context.Background(), fixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.Physical == nil || got.Physical.QStatV != p.QStatV {
		t.Fatal("IPC dropped or rewrote the complete 160-byte qstatv")
	}
	if got.CapacityReady || got.ExecutionEnabled || got.Trust != "none" {
		t.Fatal("transport granted authority")
	}
}

// No deadlines can masquerade as a rejection: a successful complete writer is
// required in every experiment, including each authentication failure.
func ipcQuotaStateTransfer(t *testing.T, wire []byte, challenge, hash []byte) ([]byte, error) {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan error, 1)
	go func() { defer b.Close(); done <- writeAll(b, wire) }()
	raw, err := readFrame(a, fixtureKey(), responseDomain, challenge, hash, responseLimit)
	a.Close()
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("incomplete wire transfer cannot prove rejection: %v (reader %v)", writeErr, err)
	}
	return raw, err
}

func ipcQuotaStateFrame(t *testing.T, payload, challenge, hash []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	if err := writeFrame(&frame, fixtureKey(), responseDomain, challenge, hash, payload, responseLimit); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(frame.Bytes())
}

func ipcQuotaStateControl(t *testing.T, p capacityprobe.CreatedLayerPhysical, challenge, hash []byte) []byte {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ipcQuotaStateTransfer(t, ipcQuotaStateFrame(t, payload, challenge, hash), challenge, hash)
	if err != nil {
		t.Fatalf("authenticated control failed: %v", err)
	}
	var decoded capacityprobe.CreatedLayerPhysical
	if err = canonicalDecode(raw, &decoded); err != nil || decoded.QStatV != p.QStatV {
		t.Fatalf("canonical control changed full qstatv: %v", err)
	}
	return payload
}

func TestIPCAuthenticatedQuotaStateByteTampering(t *testing.T) {
	for _, offset := range []int{8, 16, 24, 32, 40, 48, 56, 64, 72, 159} {
		t.Run(fmt.Sprintf("offset_%03d", offset), func(t *testing.T) {
			p := ipcQuotaStateFixture()
			challenge := bytes.Repeat([]byte{9}, 32)
			hash := sha256.Sum256([]byte("exact qstatv request"))
			payload := ipcQuotaStateControl(t, p, challenge, hash[:])
			changed := p
			changed.QStatV[offset]++
			tampered, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if len(tampered) != len(payload) || bytes.Equal(tampered, payload) {
				t.Fatal("ineffective same-length mutation")
			}
			wire := ipcQuotaStateFrame(t, payload, challenge, hash[:])
			copy(wire[36:], tampered)
			raw, err := ipcQuotaStateTransfer(t, wire, challenge, hash[:])
			if err != errWire || raw != nil {
				t.Fatalf("post-MAC qstatv mutation accepted: %v", err)
			}
			t.Logf("complete frame rejected by authentication: %v", err)
		})
	}
}

func TestIPCAuthenticatedQuotaStateWrongLength(t *testing.T) {
	for _, length := range []int{0, 159, 161} {
		t.Run(fmt.Sprintf("length_%d", length), func(t *testing.T) {
			p := ipcQuotaStateFixture()
			challenge := bytes.Repeat([]byte{11}, 32)
			hash := sha256.Sum256([]byte("exact qstatv length request"))
			payload := ipcQuotaStateControl(t, p, challenge, hash[:])
			array, err := json.Marshal(p.QStatV)
			if err != nil {
				t.Fatal(err)
			}
			// A literal numeric array retains byte-array JSON semantics (a []byte
			// would become base64 and test an unrelated type mismatch).
			values := make([]int, length)
			for i := range values {
				if i < len(p.QStatV) {
					values[i] = int(p.QStatV[i])
				} else {
					values[i] = 7
				}
			}
			wrongArray, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			needle := append([]byte(`"qstatv":`), array...)
			replacement := append([]byte(`"qstatv":`), wrongArray...)
			if bytes.Count(payload, needle) != 1 {
				t.Fatal("qstatv field not isolated")
			}
			malformed := bytes.Replace(payload, needle, replacement, 1)
			raw, err := ipcQuotaStateTransfer(t, ipcQuotaStateFrame(t, malformed, challenge, hash[:]), challenge, hash[:])
			if err != nil || !bytes.Equal(raw, malformed) {
				t.Fatalf("correctly authenticated malformed control did not reach decoder: %v", err)
			}
			var decoded capacityprobe.CreatedLayerPhysical
			err = canonicalDecode(raw, &decoded)
			if err != errWire {
				t.Fatalf("canonical decoder accepted %d-byte qstatv: %v", length, err)
			}
			t.Logf("authenticated %d-element array rejected by canonical decoder: %v", length, err)
		})
	}
}
