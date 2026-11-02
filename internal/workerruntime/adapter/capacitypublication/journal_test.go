//go:build linux || darwin

package capacitypublication

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

const journalGolden = "53424341505345510100000000000000111111111111111111111111111111111111111111111111111111111111111107000000000000000000000000000000262852e7d7a91adba609219472df41551461952c82ebb377706b91c5bc0ba27d"

func TestJournalIndependentGolden(t *testing.T) {
	var binding [32]byte
	for i := range binding {
		binding[i] = 0x11
	}
	literal, e := hex.DecodeString(journalGolden)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := encodeJournal(binding, 7)
	if e != nil || !bytes.Equal(encoded[:], literal) {
		t.Fatalf("encode did not match independent literal: %x %v", encoded, e)
	}
	n, e := decodeJournal(literal, binding)
	if n != 7 || e != nil {
		t.Fatalf("decode=%d %v", n, e)
	}
}
func TestJournalMutationMatrix(t *testing.T) {
	literal, _ := hex.DecodeString(journalGolden)
	var binding [32]byte
	for i := range binding {
		binding[i] = 0x11
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{{"magic", func(b []byte) []byte { b[0] ^= 1; return b }}, {"version", func(b []byte) []byte { b[8] = 2; return b }}, {"reserved_header", func(b []byte) []byte { b[12] = 1; return b }}, {"binding", func(b []byte) []byte { b[16] ^= 1; return b }}, {"reserved_tail", func(b []byte) []byte { b[56] = 1; return b }}, {"hash", func(b []byte) []byte { b[95] ^= 1; return b }}, {"short", func(b []byte) []byte { return b[:95] }}, {"long", func(b []byte) []byte { return append(b, 0) }}, {"overflow", func(b []byte) []byte {
		binary.LittleEndian.PutUint64(b[48:56], uint64(math.MaxInt64)+1)
		sum := sha256.Sum256(b[:64])
		copy(b[64:], sum[:])
		return b
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			if n, e := decodeJournal(tc.mutate(bytes.Clone(literal)), binding); n != 0 || !errors.Is(e, ErrPublicationStateUnavailable) {
				t.Fatalf("n=%d error=%v", n, e)
			}
		})
	}
	if _, e := encodeJournal(binding, -1); !errors.Is(e, ErrPublicationStateUnavailable) {
		t.Fatal("negative encoded")
	}
	b, e := encodeJournal(binding, math.MaxInt64)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := decodeJournal(b[:], binding); n != math.MaxInt64 || e != nil {
		t.Fatal("max not represented")
	}
	binding[0] = 0
	if _, e := decodeJournal(literal, binding); !errors.Is(e, ErrPublicationStateUnavailable) {
		t.Fatal("wrong binding accepted")
	}
}
func TestJournalBindingDomainIncludesActualNUL(t *testing.T) {
	f := newFixture(t)
	raw, e := json.Marshal(f.trust)
	if e != nil {
		t.Fatal(e)
	}
	want := sha256.Sum256(append([]byte{'s', 'w', 'i', 't', 'c', 'h', 'b', 'a', 'c', 'k', '/', 'c', 'a', 'p', 'a', 'c', 'i', 't', 'y', '/', 'p', 'u', 'b', 'l', 'i', 'c', 'a', 't', 'i', 'o', 'n', '-', 's', 't', 'o', 'r', 'e', '/', 'v', '1', 0}, raw...))
	if got := trustBinding(f.trust); got != want {
		t.Fatal("domain mismatch")
	}
}
