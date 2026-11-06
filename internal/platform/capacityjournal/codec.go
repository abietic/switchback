// Package capacityjournal is the unchanged publication-store wire codec. It
// performs no I/O and does not interpret or authorize Trust.
package capacityjournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
)

const Size = 96

var ErrInvalid = errors.New("invalid_capacity_journal")

// Binding consumes the canonical encoding/json bytes of the complete Trust.
func Binding(canonicalTrust []byte) [32]byte {
	return sha256.Sum256(append([]byte("switchback/capacity/publication-store/v1\x00"), canonicalTrust...))
}
func Encode(binding [32]byte, high int64) ([Size]byte, error) {
	var b [Size]byte
	if high < 0 {
		return b, ErrInvalid
	}
	copy(b[:8], "SBCAPSEQ")
	binary.LittleEndian.PutUint32(b[8:12], 1)
	copy(b[16:48], binding[:])
	binary.LittleEndian.PutUint64(b[48:56], uint64(high))
	sum := sha256.Sum256(b[:64])
	copy(b[64:], sum[:])
	return b, nil
}
func Decode(raw []byte, binding [32]byte) (int64, error) {
	if len(raw) != Size || string(raw[:8]) != "SBCAPSEQ" || binary.LittleEndian.Uint32(raw[8:12]) != 1 || !bytes.Equal(raw[16:48], binding[:]) {
		return 0, ErrInvalid
	}
	for _, v := range raw[12:16] {
		if v != 0 {
			return 0, ErrInvalid
		}
	}
	for _, v := range raw[56:64] {
		if v != 0 {
			return 0, ErrInvalid
		}
	}
	sum := sha256.Sum256(raw[:64])
	if !bytes.Equal(sum[:], raw[64:]) {
		return 0, ErrInvalid
	}
	n := binary.LittleEndian.Uint64(raw[48:56])
	if n > math.MaxInt64 {
		return 0, ErrInvalid
	}
	return int64(n), nil
}
