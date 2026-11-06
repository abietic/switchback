package createdlayeripc

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"io"
	"time"
)

const (
	magic          = "SBCLIPC1"
	requestDomain  = "switchback/created-layer-observer/request/v1\x00"
	responseDomain = "switchback/created-layer-observer/response/v1\x00"
	requestLimit   = 8192
	responseLimit  = 131072
)

type request struct {
	Version  string    `json:"version"`
	Nonce    string    `json:"nonce"`
	Deadline time.Time `json:"deadline"`
	Binding  Binding   `json:"binding"`
}
type response struct {
	Version       string                               `json:"version"`
	Nonce         string                               `json:"nonce"`
	RequestDigest string                               `json:"request_digest"`
	Binding       Binding                              `json:"binding"`
	Collection    capacityprobe.CreatedLayerCollection `json:"collection"`
}

func canonicalDecode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errWire
	}
	if d.Decode(new(any)) != io.EOF {
		return errWire
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(canonical, raw) {
		return errWire
	}
	return nil
}
func mac(key []byte, domain string, challenge, requestHash, body []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(domain))
	m.Write(challenge)
	m.Write(requestHash)
	m.Write(body)
	return m.Sum(nil)
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil || n <= 0 || n > len(b) {
			return errWire
		}
		b = b[n:]
	}
	return nil
}
func writeFrame(w io.Writer, key []byte, domain string, challenge, hash, raw []byte, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return errWire
	}
	header := make([]byte, 36)
	binary.BigEndian.PutUint32(header, uint32(len(raw)))
	copy(header[4:], mac(key, domain, challenge, hash, raw))
	if writeAll(w, header) != nil || writeAll(w, raw) != nil {
		return errWire
	}
	return nil
}
func readFrame(r io.Reader, key []byte, domain string, challenge, hash []byte, limit int) ([]byte, error) {
	var header [36]byte
	if _, e := io.ReadFull(r, header[:]); e != nil {
		return nil, errWire
	}
	n := binary.BigEndian.Uint32(header[:4])
	if n == 0 || uint64(n) > uint64(limit) {
		return nil, errWire
	}
	raw := make([]byte, int(n))
	if _, e := io.ReadFull(r, raw); e != nil || !hmac.Equal(header[4:], mac(key, domain, challenge, hash, raw)) {
		return nil, errWire
	}
	return raw, nil
}
func bodyDigest(raw []byte) string {
	h := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(h[:])
}
