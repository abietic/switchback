package localstorage_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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

func TestMaterializerF1BootstrapBeforeHandoff(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "fresh")
	var order []string
	materializer := localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(path string, uid, gid int) error {
		if uid != localstorage.WorkerAgentUID || gid != localstorage.WorkerAgentUID {
			t.Fatalf("handoff uid=%d gid=%d", uid, gid)
		}
		content, err := os.ReadFile(filepath.Join(session, "working-copy", "initialized"))
		if err != nil || string(content) != "complete" {
			t.Fatalf("handoff before initialization: %q %v", content, err)
		}
		order = append(order, filepath.Base(path))
		return nil
	})}
	storage, err := materializer.MaterializeWithBootstrap("fresh", func(workingCopy string) error {
		if len(order) != 0 {
			t.Fatalf("early handoff: %v", order)
		}
		for _, path := range []string{root, filepath.Dir(session), session, workingCopy, filepath.Join(session, "agent-state"), filepath.Join(session, "trace-spool")} {
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			stat := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || (path != root && info.Mode().Perm() != 0o700) || info.Mode().Perm()&0o022 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Uid == localstorage.WorkerAgentUID {
				t.Fatalf("private phase %s: mode=%v uid=%d", path, info.Mode(), stat.Uid)
			}
		}
		order = append(order, "bootstrap")
		return os.WriteFile(filepath.Join(workingCopy, "initialized"), []byte("complete"), 0o640)
	})
	if err != nil {
		t.Fatal(err)
	}
	if storage.BackendRoot != session || !reflect.DeepEqual(order, []string{"bootstrap", "working-copy", "agent-state", "trace-spool"}) {
		t.Fatalf("storage=%+v order=%v", storage, order)
	}
	t.Logf("actual_backend_euid=%d callback=1 handoff=3 order=%v", os.Geteuid(), order)
}

func TestMaterializerF1BootstrapFailuresCleanTree(t *testing.T) {
	for _, fail := range []string{"callback", "handoff", "root-mode"} {
		t.Run(fail, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			callbacks := 0
			want := errors.New("controlled " + fail)
			materializer := localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(string, int, int) error { calls++; return want })}
			storage, err := materializer.MaterializeWithBootstrap("failed", func(path string) error {
				callbacks++
				if err := os.WriteFile(filepath.Join(path, "partial"), []byte("partial"), 0o600); err != nil {
					return err
				}
				if fail == "callback" {
					return want
				}
				if fail == "root-mode" {
					return os.Chmod(path, 0o750)
				}
				return nil
			})
			expectedCalls := 0
			if fail == "handoff" {
				expectedCalls = 1
			}
			if err == nil || fail != "root-mode" && !errors.Is(err, want) || calls != expectedCalls || callbacks != 1 || storage != (application.SessionStorage{}) {
				t.Fatalf("error=%v handoffs=%d callbacks=%d storage=%+v", err, calls, callbacks, storage)
			}
			if _, err := os.Lstat(filepath.Join(root, "sessions", "failed")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial tree remains: %v", err)
			}
			t.Logf("callbacks=%d handoffs=%d partial_tree_absent=true", callbacks, calls)
		})
	}
}

func TestMaterializerF1RejectsExistingAndUnsafeParents(t *testing.T) {
	for _, kind := range []string{"existing", "backend-writable", "sessions-writable", "sessions-symlink", "nil-callback"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			session := filepath.Join(root, "sessions", "fresh")
			callbacks, handoffs := 0, 0
			if kind == "existing" {
				if err := os.MkdirAll(session, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(session, "sentinel"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "backend-writable" {
				if err := os.Chmod(root, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "sessions-writable" {
				if err := os.Mkdir(filepath.Dir(session), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Dir(session), 0o777); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "sessions-symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Dir(session)); err != nil {
					t.Fatal(err)
				}
			}
			callback := func(string) error { callbacks++; return nil }
			if kind == "nil-callback" {
				callback = nil
			}
			storage, err := (localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(string, int, int) error { handoffs++; return nil })}).MaterializeWithBootstrap("fresh", callback)
			if err == nil || callbacks != 0 || handoffs != 0 || storage != (application.SessionStorage{}) {
				t.Fatalf("error=%v callbacks=%d handoffs=%d storage=%+v", err, callbacks, handoffs, storage)
			}
			if kind == "existing" {
				content, err := os.ReadFile(filepath.Join(session, "sentinel"))
				if err != nil || string(content) != "keep" {
					t.Fatalf("existing data changed: %q %v", content, err)
				}
			} else if _, err := os.Lstat(session); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("session created: %v", err)
			}
		})
	}
}

func TestMaterializerF1AncestorRenameBoundary(t *testing.T) {
	for _, kind := range []string{"writable", "sticky", "alias", "indirect-unsafe-alias"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "backend")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o777)
			if kind == "sticky" {
				mode |= os.ModeSticky
			}
			if kind == "alias" || kind == "indirect-unsafe-alias" {
				mode = 0o755
			}
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			if kind == "alias" {
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(parent, link); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(link, "backend")
			}

			if kind == "indirect-unsafe-alias" {
				unsafe := t.TempDir()
				if err := os.Chmod(unsafe, 0o777); err != nil {
					t.Fatal(err)
				}
				intermediate := filepath.Join(unsafe, "next")
				if err := os.Symlink(parent, intermediate); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(intermediate, link); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(link, "backend")
			}
			callbacks, handoffs := 0, 0
			_, err := (localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(string, int, int) error { handoffs++; return nil })}).MaterializeWithBootstrap("fresh", func(string) error { callbacks++; return nil })
			if kind == "writable" || kind == "indirect-unsafe-alias" {
				if err == nil || callbacks != 0 || handoffs != 0 {
					t.Fatalf("unsafe ancestor: error=%v callbacks=%d handoffs=%d", err, callbacks, handoffs)
				}
				if _, err := os.Lstat(filepath.Join(root, "sessions")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unsafe ancestor caused write: %v", err)
				}
			} else if err != nil || callbacks != 1 || handoffs != 3 {
				t.Fatalf("safe ancestor: error=%v callbacks=%d handoffs=%d", err, callbacks, handoffs)
			}
		})
	}
}

// Parent traversal is rejected even when the current resolved tree is protected:
// cleaning a raw target would skip symlinks consumed before its ".." component.
func TestMaterializerI2RejectsParentTraversingAliases(t *testing.T) {
	for _, kind := range []string{"unsafe-intermediate", "safe-parent-traversal"} {
		t.Run(kind, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			safe, unsafe, actual := filepath.Join(base, "safe"), filepath.Join(base, "unsafe"), filepath.Join(base, "actual")
			backend := filepath.Join(actual, "trusted", "backend")
			for _, path := range []string{safe, unsafe, filepath.Join(actual, "sub"), backend} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unsafe-intermediate" {
				if err := os.Chmod(unsafe, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			for _, link := range [][2]string{
				{filepath.Join(unsafe, "hop"), filepath.Join(actual, "sub")},
				{filepath.Join(safe, "inner"), filepath.Join(unsafe, "hop")},
				{filepath.Join(safe, "outer"), filepath.Join(safe, "inner") + "/../trusted"},
			} {
				if err := os.Symlink(link[1], link[0]); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(safe, "outer", "backend")
			resolved, err := filepath.EvalSymlinks(root)
			if err != nil || resolved != backend {
				t.Fatalf("fixture resolution=%q want=%q error=%v", resolved, backend, err)
			}
			callbacks, handoffs := 0, 0
			storage, err := (localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(string, int, int) error { handoffs++; return nil })}).MaterializeWithBootstrap("fresh", func(string) error { callbacks++; return nil })
			_, sessionsErr := os.Lstat(filepath.Join(backend, "sessions"))
			t.Logf("resolved=%q error=%v callbacks=%d handoffs=%d sessions_error=%v", resolved, err, callbacks, handoffs, sessionsErr)
			if err == nil || !strings.Contains(err.Error(), "parent traversal") || callbacks != 0 || handoffs != 0 || storage != (application.SessionStorage{}) {
				t.Errorf("parent-traversing alias accepted or wrong rejection: error=%v callbacks=%d handoffs=%d storage=%+v", err, callbacks, handoffs, storage)
			}
			if !errors.Is(sessionsErr, os.ErrNotExist) {
				t.Errorf("rejected bootstrap wrote sessions: %v", sessionsErr)
			}
			if kind == "safe-parent-traversal" {
				// The stricter alias contract belongs only to the new bootstrap flow.
				legacy := localstorage.Materializer{BackendDataRoot: root, EngineDataRoot: "/srv/switchback-data", Ownership: localstorage.OwnershipFunc(func(string, int, int) error { handoffs++; return nil })}
				created, err := legacy.Materialize("legacy")
				if err != nil || handoffs != 3 {
					t.Fatalf("legacy Materialize: error=%v handoffs=%d", err, handoffs)
				}
				reopened, err := legacy.OpenExisting("legacy")
				if err != nil || reopened != created || handoffs != 3 {
					t.Fatalf("legacy OpenExisting: error=%v storage=%+v handoffs=%d", err, reopened, handoffs)
				}
			}
		})
	}
}
