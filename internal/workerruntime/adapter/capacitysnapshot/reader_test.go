package capacitysnapshot

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReaderRejectsUnsafeEntriesAndReadsAtomicReplacement(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "capacity.json")
	if _, err = r.Read(); err == nil {
		t.Fatal("missing file accepted")
	}
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte("x"), 0600)
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(path)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(); err == nil {
		t.Fatal("directory accepted")
	}
	os.Remove(path)
	if err = syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(); err == nil {
		t.Fatal("FIFO accepted")
	}
	os.Remove(path)
	if err = os.WriteFile(path, make([]byte, MaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(); err == nil {
		t.Fatal("large file accepted")
	}
	if err = os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if b, e := r.Read(); e != nil || string(b) != "first" {
		t.Fatalf("first=%q %v", b, e)
	}
	replacement := filepath.Join(dir, "incoming")
	os.WriteFile(replacement, []byte("second"), 0600)
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if b, e := r.Read(); e != nil || string(b) != "second" {
		t.Fatalf("replacement=%q %v", b, e)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(dir, link)
	if _, err = NewReader(link); err == nil {
		t.Fatal("directory symlink accepted")
	}
}
