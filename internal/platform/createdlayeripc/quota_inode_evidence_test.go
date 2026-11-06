package createdlayeripc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

// The IPC contract already contains complete quota records; no new wire fields.
func TestIPCQuotaInodeFullRawPreserved(t *testing.T) {
	c, s := fixturePair(t)
	collection := partialCollector(context.Background(), fixtureBinding())
	p := &capacityprobe.CreatedLayerPhysical{}
	for j := range p.TargetQuota {
		p.TargetQuota[j] = byte(j)
		p.DefaultQuota[j] = byte(255 - j)
	}
	binary.LittleEndian.PutUint64(p.TargetQuota[24:32], 0)
	binary.LittleEndian.PutUint64(p.TargetQuota[48:56], ^uint64(0))
	binary.LittleEndian.PutUint64(p.DefaultQuota[24:32], ^uint64(0))
	binary.LittleEndian.PutUint64(p.DefaultQuota[48:56], 17)
	collection.Physical = p
	s.collect = func(context.Context, Binding) capacityprobe.CreatedLayerCollection { return collection }
	got, err := exchange(t, c, s, context.Background(), fixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.Physical == nil || got.Physical.TargetQuota != p.TargetQuota || got.Physical.DefaultQuota != p.DefaultQuota {
		t.Fatal("IPC dropped/rewrote full inode raw bytes")
	}
	if got.CapacityReady || got.ExecutionEnabled || got.Trust != "none" {
		t.Fatal("transport granted authority")
	}
}

func TestIPCAuthenticatedInodeByteTampering(t *testing.T) {
	for _, target := range []bool{true, false} {
		for _, offset := range []int{24, 48} {
			name := "default0"
			if target {
				name = "target"
			}
			if offset == 24 {
				name += "_hard"
			} else {
				name += "_used"
			}
			t.Run(name, func(t *testing.T) {
				var original capacityprobe.CreatedLayerPhysical
				original.TargetQuota[24] = 17
				original.TargetQuota[48] = 23
				original.DefaultQuota[24] = 31
				original.DefaultQuota[48] = 43
				payload, _ := json.Marshal(original)
				changed := original
				if target {
					changed.TargetQuota[offset]++
				} else {
					changed.DefaultQuota[offset]++
				}
				tampered, _ := json.Marshal(changed)
				if len(tampered) != len(payload) || bytes.Equal(tampered, payload) {
					t.Fatal("ineffective byte mutation")
				}
				challenge := bytes.Repeat([]byte{9}, 32)
				hash := sha256.Sum256([]byte("exact request"))
				for _, mutate := range []bool{false, true} {
					var frame bytes.Buffer
					if err := writeFrame(&frame, fixtureKey(), responseDomain, challenge, hash[:], payload, responseLimit); err != nil {
						t.Fatal(err)
					}
					wire := bytes.Clone(frame.Bytes())
					if mutate {
						copy(wire[36:], tampered)
					}
					a, b := net.Pipe()
					done := make(chan struct{})
					go func() { defer close(done); defer b.Close(); _ = writeAll(b, wire) }()
					got, err := readFrame(a, fixtureKey(), responseDomain, challenge, hash[:], responseLimit)
					a.Close()
					<-done
					if mutate {
						if err != errWire {
							t.Fatal("inode bytes changed after MAC accepted")
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						var decoded capacityprobe.CreatedLayerPhysical
						if canonicalDecode(got, &decoded) != nil || decoded.TargetQuota != original.TargetQuota || decoded.DefaultQuota != original.DefaultQuota {
							t.Fatal("valid authenticated bytes changed")
						}
					}
				}
			})
		}
	}
}
