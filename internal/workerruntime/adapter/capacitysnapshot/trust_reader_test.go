package capacitysnapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestTrustReaderStrictBoundedAndReplacement(t *testing.T) {
	trust, _, _ := fixture(t)
	raw, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trust.json")
	write := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, b := range map[string][]byte{
		"malformed": []byte("{"), "null": []byte("null"), "oversize": make([]byte, MaxBytes+1),
		"duplicate":         []byte(strings.Replace(string(raw), `"key_id":"key"`, `"key_id":"key","key_id":"key"`, 1)),
		"escaped_duplicate": []byte(strings.Replace(string(raw), `"key_id":"key"`, `"key_id":"key","\u006bey_id":"key"`, 1)),
		"alias":             []byte(strings.Replace(string(raw), `"key_id"`, `"KEY_ID"`, 1)),
		"unknown":           []byte(strings.Replace(string(raw), `"key_id":"key"`, `"key_id":"key","unsafe":1`, 1)),
		"null_key":          []byte(strings.Replace(string(raw), `"public_key":"`+string(mustJSONKey(t, trust.PublicKey))+`"`, `"public_key":null`, 1)),
		"invalid_trust":     []byte(strings.Replace(string(raw), `"anchor_path":"/pool/anchor"`, `"anchor_path":"relative"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			write(b)
			if _, err := ReadTrust(path); err == nil {
				t.Fatal("unsafe trust accepted")
			} else if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "public_key") {
				t.Fatal("trust details leaked")
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrust(path); err == nil {
		t.Fatal("missing accepted")
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrust(path); err == nil {
		t.Fatal("FIFO accepted")
	}
	os.Remove(path)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrust(path); err == nil {
		t.Fatal("leaf symlink accepted")
	}
	os.Remove(path)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrust(filepath.Join(link, "target")); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
	write(raw)
	got, err := ReadTrust(path)
	if err != nil || got.KeyID != trust.KeyID {
		t.Fatal("valid trust refused", err)
	}
	trust.KeyID = "replacement"
	replaced, _ := json.Marshal(trust)
	incoming := filepath.Join(dir, "incoming")
	if err := os.WriteFile(incoming, replaced, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(incoming, path); err != nil {
		t.Fatal(err)
	}
	got, err = ReadTrust(path)
	if err != nil || got.KeyID != "replacement" {
		t.Fatal("atomic replacement not reopened", err)
	}
}
func mustJSONKey(t *testing.T, b []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw[1 : len(raw)-1]
}
