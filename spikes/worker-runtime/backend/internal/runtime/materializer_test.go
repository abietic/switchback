package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializerCreatesCleanGitWorkingCopyWithoutMutatingFixture(t *testing.T) {
	fixture := fixtureDirectory(t)
	before := treeDigest(t, fixture)
	containerRoot := t.TempDir()
	hostRoot := t.TempDir()

	materialized, err := (Materializer{
		Mapper:      PathMapper{ContainerDataRoot: containerRoot, HostDataRoot: hostRoot},
		FixturePath: fixture,
		Ownership:   PathOwnershipFunc(func(string, int, int) error { return nil }),
	}).Materialize(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	if got, want := materialized.WorkingCopy.ContainerPath, filepath.Join(containerRoot, "sessions", "session-1", "working-copy"); got != want {
		t.Fatalf("WorkingCopy.ContainerPath = %q, want %q", got, want)
	}
	if got, want := materialized.WorkingCopy.HostPath, filepath.Join(hostRoot, "sessions", "session-1", "working-copy"); got != want {
		t.Fatalf("WorkingCopy.HostPath = %q, want %q", got, want)
	}
	for _, directory := range []string{
		materialized.WorkingCopy.ContainerPath,
		materialized.AgentState.ContainerPath,
		materialized.TraceSpool.ContainerPath,
		materialized.RuntimeLogs.ContainerPath,
	} {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			t.Fatalf("required directory %q stat = %v, info = %+v", directory, err, info)
		}
	}

	command := exec.Command("git", "log", "-1", "--format=%an|%ae|%s")
	command.Dir = materialized.WorkingCopy.ContainerPath
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git log error = %v", err)
	}
	if got, want := strings.TrimSpace(string(output)), "Switchback Spike|spike@switchback.local|fixture: baseline"; got != want {
		t.Fatalf("git baseline = %q, want %q", got, want)
	}
	if got, want := treeDigest(t, fixture), before; got != want {
		t.Fatalf("fixture digest after materialization = %s, want %s", got, want)
	}
}

func TestMaterializerSanitizesInheritedGitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "hostile author")
	t.Setenv("GIT_AUTHOR_EMAIL", "hostile-author@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "hostile committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "hostile-committer@example.invalid")

	materialized, err := (Materializer{
		Mapper: PathMapper{
			ContainerDataRoot: t.TempDir(),
			HostDataRoot:      t.TempDir(),
		},
		FixturePath: fixtureDirectory(t),
		Ownership:   PathOwnershipFunc(func(string, int, int) error { return nil }),
	}).Materialize(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	workingCopy := materialized.WorkingCopy.ContainerPath
	assertGitOutput(t, workingCopy, []string{"log", "-1", "--format=%an|%ae|%cn|%ce|%s"}, "Switchback Spike|spike@switchback.local|Switchback Spike|spike@switchback.local|fixture: baseline")
	assertGitOutput(t, workingCopy, []string{"branch", "--show-current"}, "main")
	assertGitOutput(t, workingCopy, []string{"status", "--porcelain"}, "")
}

func TestMaterializerCleansOnlyItsFailedPartialSession(t *testing.T) {
	containerRoot := t.TempDir()
	mapper := PathMapper{ContainerDataRoot: containerRoot, HostDataRoot: t.TempDir()}
	invalidFixture := filepath.Join(t.TempDir(), "fixture")
	if err := os.Mkdir(invalidFixture, 0o700); err != nil {
		t.Fatalf("Mkdir(invalid fixture) error = %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(invalidFixture, "escaped-link")); err != nil {
		t.Fatalf("Symlink(invalid fixture) error = %v", err)
	}
	materializer := Materializer{
		Mapper:      mapper,
		FixturePath: invalidFixture,
		Ownership:   PathOwnershipFunc(func(string, int, int) error { return nil }),
	}

	if _, err := materializer.Materialize(context.Background(), "failed-session"); err == nil {
		t.Fatal("Materialize() error = nil, want missing fixture failure")
	}
	if _, err := os.Stat(filepath.Join(containerRoot, "sessions", "failed-session")); !os.IsNotExist(err) {
		t.Fatalf("partial session tree stat error = %v, want not exist", err)
	}

	existing := filepath.Join(containerRoot, "sessions", "existing-session")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatalf("MkdirAll(existing) error = %v", err)
	}
	marker := filepath.Join(existing, "keep")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	existingMaterializer := Materializer{
		Mapper:      mapper,
		FixturePath: fixtureDirectory(t),
		Ownership:   PathOwnershipFunc(func(string, int, int) error { return nil }),
	}
	if _, err := existingMaterializer.Materialize(context.Background(), "existing-session"); err == nil {
		t.Fatal("Materialize() error = nil, want existing session rejection")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve" {
		t.Fatalf("pre-existing session marker = %q, err = %v, want preserved", got, err)
	}
}

func TestMaterializerHandsAgentTreesToUID10002AndKeepsRuntimeLogsBackendOwned(t *testing.T) {
	fixture := t.TempDir()
	readOnlyDirectory := filepath.Join(fixture, "readonly")
	if err := os.Mkdir(readOnlyDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(readonly fixture) error = %v", err)
	}
	fixtureFile := filepath.Join(readOnlyDirectory, "readonly.txt")
	if err := os.WriteFile(fixtureFile, []byte("fixture"), 0o400); err != nil {
		t.Fatalf("WriteFile(fixture) error = %v", err)
	}
	if err := os.Chmod(readOnlyDirectory, 0o500); err != nil {
		t.Fatalf("Chmod(readonly fixture) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDirectory, 0o700) })
	containerRoot := t.TempDir()
	ownership := &recordingPathOwnership{calls: make(map[string]pathOwnershipCall)}

	materialized, err := (Materializer{
		Mapper: PathMapper{
			ContainerDataRoot: containerRoot,
			HostDataRoot:      t.TempDir(),
		},
		FixturePath: fixture,
		Ownership:   ownership,
	}).Materialize(context.Background(), "session-ownership")
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	agentPaths := []string{
		materialized.WorkingCopy.ContainerPath,
		filepath.Join(materialized.WorkingCopy.ContainerPath, "readonly"),
		filepath.Join(materialized.WorkingCopy.ContainerPath, "readonly", "readonly.txt"),
		filepath.Join(materialized.WorkingCopy.ContainerPath, ".git"),
		filepath.Join(materialized.WorkingCopy.ContainerPath, ".git", "config"),
		materialized.AgentState.ContainerPath,
		materialized.TraceSpool.ContainerPath,
	}
	for _, path := range agentPaths {
		call, ok := ownership.calls[path]
		if !ok {
			t.Errorf("ownership handoff did not include %q", path)
			continue
		}
		if call.uid != 10002 || call.gid != 10002 {
			t.Errorf("ownership for %q = %d:%d, want 10002:10002", path, call.uid, call.gid)
		}
	}
	if _, ok := ownership.calls[materialized.RuntimeLogs.ContainerPath]; ok {
		t.Fatalf("RuntimeLogs %q was handed to the agent", materialized.RuntimeLogs.ContainerPath)
	}
	info, err := os.Stat(filepath.Join(materialized.WorkingCopy.ContainerPath, "readonly", "readonly.txt"))
	if err != nil {
		t.Fatalf("Stat(materialized fixture) error = %v", err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("materialized fixture mode = %o, want owner write permission", info.Mode().Perm())
	}
}

func TestMaterializerCreatesPrivateCodexStateOwnedByWorkerAgent(t *testing.T) {
	containerRoot := t.TempDir()
	ownership := &recordingPathOwnership{calls: make(map[string]pathOwnershipCall)}

	materialized, err := (Materializer{
		Mapper: PathMapper{
			ContainerDataRoot: containerRoot,
			HostDataRoot:      t.TempDir(),
		},
		FixturePath: fixtureDirectory(t),
		Ownership:   ownership,
	}).Materialize(context.Background(), "session-codex-state")
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	codexState := filepath.Join(materialized.AgentState.ContainerPath, "codex")
	info, err := os.Lstat(codexState)
	if err != nil {
		t.Fatalf("Lstat(Codex state) error = %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Codex state mode = %v, want real directory", info.Mode())
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("Codex state permissions = %o, want %o", got, want)
	}
	call, ok := ownership.calls[codexState]
	if !ok {
		t.Fatalf("ownership handoff did not include Codex state %q", codexState)
	}
	if call.uid != WorkerAgentUID || call.gid != WorkerAgentUID {
		t.Fatalf(
			"Codex state ownership = %d:%d, want %d:%d",
			call.uid,
			call.gid,
			WorkerAgentUID,
			WorkerAgentUID,
		)
	}
}

func TestMaterializerRollsBackWhenCodexStateOwnershipHandoffFails(t *testing.T) {
	containerRoot := t.TempDir()
	sessionRoot := filepath.Join(containerRoot, "sessions", "session-codex-ownership-failure")
	codexState := filepath.Join(sessionRoot, "agent-state", "codex")
	handoffErr := errors.New("Codex ownership denied")

	_, err := (Materializer{
		Mapper: PathMapper{
			ContainerDataRoot: containerRoot,
			HostDataRoot:      t.TempDir(),
		},
		FixturePath: fixtureDirectory(t),
		Ownership: PathOwnershipFunc(func(path string, _, _ int) error {
			if path == codexState {
				return handoffErr
			}
			return nil
		}),
	}).Materialize(context.Background(), "session-codex-ownership-failure")
	if !errors.Is(err, handoffErr) {
		t.Fatalf("Materialize() error = %v, want wrapping %v", err, handoffErr)
	}
	if _, err := os.Lstat(sessionRoot); !os.IsNotExist(err) {
		t.Fatalf("partial session tree stat error = %v, want not exist", err)
	}
}

func TestMaterializerRejectsSymlinkSubstitutedForCodexState(t *testing.T) {
	containerRoot := t.TempDir()
	sessionRoot := filepath.Join(containerRoot, "sessions", "session-codex-symlink")
	agentState := filepath.Join(sessionRoot, "agent-state")
	codexState := filepath.Join(agentState, "codex")
	externalState := t.TempDir()
	externalMarker := filepath.Join(externalState, "keep")
	if err := os.WriteFile(externalMarker, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("WriteFile(external marker) error = %v", err)
	}

	swapped := false
	_, err := (Materializer{
		Mapper: PathMapper{
			ContainerDataRoot: containerRoot,
			HostDataRoot:      t.TempDir(),
		},
		FixturePath: fixtureDirectory(t),
		Ownership: PathOwnershipFunc(func(path string, _, _ int) error {
			if path != agentState || swapped {
				return nil
			}
			if err := os.Remove(codexState); err != nil {
				return err
			}
			if err := os.Symlink(externalState, codexState); err != nil {
				return err
			}
			swapped = true
			return nil
		}),
	}).Materialize(context.Background(), "session-codex-symlink")
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("Materialize() error = %v, want symlink rejection", err)
	}
	if !swapped {
		t.Fatal("Codex state was not substituted for the negative test")
	}
	if _, err := os.Lstat(sessionRoot); !os.IsNotExist(err) {
		t.Fatalf("partial session tree stat error = %v, want not exist", err)
	}
	if got, err := os.ReadFile(externalMarker); err != nil || string(got) != "preserve" {
		t.Fatalf("external marker = %q, err = %v, want preserved", got, err)
	}
}

type pathOwnershipCall struct {
	uid int
	gid int
}

type recordingPathOwnership struct {
	calls map[string]pathOwnershipCall
}

func (r *recordingPathOwnership) Chown(path string, uid, gid int) error {
	r.calls[path] = pathOwnershipCall{uid: uid, gid: gid}
	return nil
}

func fixtureDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", "go-repository"))
	if err != nil {
		t.Fatalf("Abs(fixture) error = %v", err)
	}
	return path
}

func treeDigest(t *testing.T, directory string) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "tree-digest"))
	if err != nil {
		t.Fatalf("Abs(tree-digest) error = %v", err)
	}
	output, err := exec.Command(script, directory).Output()
	if err != nil {
		t.Fatalf("tree-digest error = %v", err)
	}
	return strings.TrimSpace(string(output))
}

func assertGitOutput(t *testing.T, directory string, arguments []string, want string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v error = %v", arguments, err)
	}
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("git %v = %q, want %q", arguments, got, want)
	}
}
