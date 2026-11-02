package localstorage_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestMaterializerCreatesServerDerivedPrivateSessionStorage(t *testing.T) {
	backendRoot := t.TempDir()
	engineRoot := filepath.Join(string(filepath.Separator), "srv", "switchback-data")
	ownership := &recordingOwnership{}

	storage, err := (localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  engineRoot,
		Ownership:       ownership,
	}).Materialize("worker-session-1")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	sessionRoot := filepath.Join(backendRoot, "sessions", "worker-session-1")
	if storage.BackendRoot != sessionRoot || storage.EngineRoot != filepath.Join(engineRoot, "sessions", "worker-session-1") {
		t.Fatalf("unexpected roots: %+v", storage)
	}
	assertMount(t, storage.WorkingCopy, filepath.Join(sessionRoot, "working-copy"), filepath.Join(engineRoot, "sessions", "worker-session-1", "working-copy"), "/workspace")
	assertMount(t, storage.AgentState, filepath.Join(sessionRoot, "agent-state"), filepath.Join(engineRoot, "sessions", "worker-session-1", "agent-state"), "/var/lib/switchback/agent-state")
	assertMount(t, storage.TraceSpool, filepath.Join(sessionRoot, "trace-spool"), filepath.Join(engineRoot, "sessions", "worker-session-1", "trace-spool"), "/var/lib/switchback/trace-spool")

	for _, path := range []string{storage.BackendRoot, storage.WorkingCopy.BackendPath, storage.AgentState.BackendPath, storage.TraceSpool.BackendPath} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			t.Fatalf("path %q mode = %v, want private real directory", path, info.Mode())
		}
	}
	if !ownership.has(storage.WorkingCopy.BackendPath, localstorage.WorkerAgentUID, localstorage.WorkerAgentUID) ||
		!ownership.has(storage.AgentState.BackendPath, localstorage.WorkerAgentUID, localstorage.WorkerAgentUID) ||
		!ownership.has(storage.TraceSpool.BackendPath, localstorage.WorkerAgentUID, localstorage.WorkerAgentUID) {
		t.Fatalf("agent storage ownership calls = %+v", ownership.calls)
	}
	if ownership.has(storage.BackendRoot, localstorage.WorkerAgentUID, localstorage.WorkerAgentUID) {
		t.Fatal("session root must remain Backend-owned")
	}
}

func TestMaterializerRejectsUntrustedPathsAndPreservesExistingSession(t *testing.T) {
	for _, sessionID := range []string{"", ".", "..", "../escape", "/absolute", "a/b", "a\\b", " spaced"} {
		t.Run(strings.ReplaceAll(sessionID, "/", "_"), func(t *testing.T) {
			_, err := (localstorage.Materializer{
				BackendDataRoot: t.TempDir(),
				EngineDataRoot:  "/srv/switchback-data",
				Ownership:       localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
			}).Materialize(sessionID)
			if err == nil {
				t.Fatalf("unsafe session id %q was accepted", sessionID)
			}
		})
	}

	backendRoot := t.TempDir()
	sessionRoot := filepath.Join(backendRoot, "sessions", "existing")
	if err := os.MkdirAll(sessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(sessionRoot, "keep")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  "/srv/switchback-data",
		Ownership:       localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
	}).Materialize("existing")
	if err == nil {
		t.Fatal("existing Session storage was overwritten")
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "preserve" {
		t.Fatalf("existing marker = %q, err = %v", got, readErr)
	}
}

func TestMaterializerRejectsSymlinkPrefixAndRollsBackPartialTree(t *testing.T) {
	backendRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(backendRoot, "sessions")); err != nil {
		t.Fatal(err)
	}
	_, err := (localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  "/srv/switchback-data",
		Ownership:       localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
	}).Materialize("worker-session-1")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink prefix error = %v", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: entries=%v err=%v", entries, readErr)
	}

	backendRoot = t.TempDir()
	wantErr := errors.New("ownership denied")
	_, err = (localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  "/srv/switchback-data",
		Ownership: localstorage.OwnershipFunc(func(path string, _, _ int) error {
			if strings.HasSuffix(path, "agent-state") {
				return wantErr
			}
			return nil
		}),
	}).Materialize("failed")
	if !errors.Is(err, wantErr) {
		t.Fatalf("materialize error = %v, want %v", err, wantErr)
	}
	if _, statErr := os.Lstat(filepath.Join(backendRoot, "sessions", "failed")); !os.IsNotExist(statErr) {
		t.Fatalf("partial Session tree remains: %v", statErr)
	}
}

func TestOpenExistingReconstructsStorageWithoutChangingDurableBytes(t *testing.T) {
	backendRoot := t.TempDir()
	materializer := localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  "/srv/switchback-data",
		Ownership:       localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
	}
	created, err := materializer.Materialize("worker-session-1")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(created.WorkingCopy.BackendPath, "preserve.txt")
	if err := os.WriteFile(marker, []byte("durable state"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := materializer.OpenExisting("worker-session-1")
	if err != nil {
		t.Fatal(err)
	}
	if reopened != created {
		t.Fatalf("reopened=%+v created=%+v", reopened, created)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "durable state" {
		t.Fatalf("durable marker=%q err=%v", content, err)
	}
}

func TestOpenExistingRejectsMissingOrSymlinkedMount(t *testing.T) {
	backendRoot := t.TempDir()
	materializer := localstorage.Materializer{
		BackendDataRoot: backendRoot,
		EngineDataRoot:  "/srv/switchback-data",
		Ownership:       localstorage.OwnershipFunc(func(string, int, int) error { return nil }),
	}
	storage, err := materializer.Materialize("worker-session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(storage.AgentState.BackendPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), storage.AgentState.BackendPath); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.OpenExisting("worker-session-1"); err == nil {
		t.Fatal("symlinked Agent state mount was accepted")
	}
}

type ownershipCall struct {
	path string
	uid  int
	gid  int
}

type recordingOwnership struct {
	calls []ownershipCall
}

func (r *recordingOwnership) Chown(path string, uid, gid int) error {
	r.calls = append(r.calls, ownershipCall{path: path, uid: uid, gid: gid})
	return nil
}

func (r *recordingOwnership) has(path string, uid, gid int) bool {
	for _, call := range r.calls {
		if call.path == path && call.uid == uid && call.gid == gid {
			return true
		}
	}
	return false
}

func assertMount(t *testing.T, got application.StorageMount, backend, engine, worker string) {
	t.Helper()
	if got.BackendPath != backend || got.EngineSource != engine || got.WorkerPath != worker {
		t.Fatalf("mount = %+v, want backend=%q engine=%q worker=%q", got, backend, engine, worker)
	}
}
