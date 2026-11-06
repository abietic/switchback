package capacityprobe

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func blockedFixture() Collection {
	return Collection{Schema: "switchback.capacity.untrusted_collection.v1", Status: "preparation_blocked", Trust: "none", Blockers: []string{"synthetic_not_physical"}}
}
func privateDir(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestUnsafePublicationPreservesOldEvidence(t *testing.T) {
	for _, variant := range []string{"directory_mode", "symlink_status", "replaced_directory"} {
		t.Run(variant, func(t *testing.T) {
			parent := privateDir(t)
			dir := filepath.Join(parent, "publication")
			os.Mkdir(dir, 0700)
			old := []byte("old bytes")
			os.WriteFile(filepath.Join(dir, "capacity.json"), old, 0600)
			original := dir
			var hook func()
			if variant == "directory_mode" {
				os.Chmod(dir, 0500)
				defer os.Chmod(dir, 0700)
			}
			if variant == "symlink_status" {
				os.Symlink(filepath.Join(dir, "capacity.json"), filepath.Join(dir, "probe-status.json"))
			}
			if variant == "replaced_directory" {
				hook = func() {
					original = filepath.Join(parent, "moved")
					if e := os.Rename(dir, original); e != nil {
						t.Fatal(e)
					}
					os.Mkdir(dir, 0700)
				}
			}
			if e := publishBlocked(dir, blockedFixture(), hook); e == nil {
				t.Fatal("unsafe publication reported success")
			}
			raw, e := os.ReadFile(filepath.Join(original, "capacity.json"))
			if e != nil || !bytes.Equal(raw, old) {
				t.Fatal("old bytes lost")
			}
			if variant != "symlink_status" {
				if _, e := os.Stat(filepath.Join(dir, "probe-status.json")); !os.IsNotExist(e) {
					t.Fatal("false blocked status success")
				}
			}
		})
	}
}
func TestPublicationNeverSignsOrAcceptsReady(t *testing.T) {
	dir := privateDir(t)
	c := blockedFixture()
	c.Status = "ready"
	if e := PublishBlocked(dir, c); e == nil {
		t.Fatal("ready publication supported")
	}
	if e := PublishBlocked(dir, blockedFixture()); e != nil {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "probe-status.json" {
		t.Fatal("unexpected authority artifact")
	}
}

func TestOpenDirectoryErrorPreservesOnlyErrno(t *testing.T) {
	const canary = "DIRECTORY_PATH_CANARY"
	_, err := openDirectory(filepath.Join(privateDir(t), canary))
	var errno syscall.Errno
	if !errors.As(err, &errno) || errno != syscall.ENOENT || strings.Contains(err.Error(), canary) {
		t.Fatal("missing directory errno lost or path leaked")
	}
}
