package capacityprobe_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// Cross-adapter coverage lives outside capacityprobe to preserve dependency direction.
func publicationDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestQuarantineBeforeBlockedStatusReaderCannotReuse(t *testing.T) {
	dir := publicationDir(t)
	old := []byte("opaque synthetic old signed snapshot bytes: preserve exactly")
	if e := os.WriteFile(filepath.Join(dir, "capacity.json"), old, 0600); e != nil {
		t.Fatal(e)
	}
	reader, e := capacitysnapshot.NewReader(dir)
	if e != nil {
		t.Fatal(e)
	}
	if raw, e := reader.Read(); e != nil || !bytes.Equal(raw, old) {
		t.Fatal("old fixture unreadable")
	}
	if e := capacityprobe.PublishBlocked(dir, capacityprobe.Collection{Schema: "switchback.capacity.untrusted_collection.v1", Status: "preparation_blocked", Trust: "none", Blockers: []string{"synthetic_not_physical"}}); e != nil {
		t.Fatal(e)
	}
	if _, e := reader.Read(); e == nil {
		t.Fatal("old capacity remains available")
	}
	archives, e := filepath.Glob(filepath.Join(dir, "quarantine-capacity-*"))
	if e != nil || len(archives) != 1 {
		t.Fatal("missing unique archive")
	}
	raw, e := os.ReadFile(archives[0])
	if e != nil || !bytes.Equal(raw, old) {
		t.Fatal("old bytes not preserved")
	}
	if _, e := os.Stat(filepath.Join(dir, "probe-status.json")); e != nil {
		t.Fatal("status unavailable")
	}
}
