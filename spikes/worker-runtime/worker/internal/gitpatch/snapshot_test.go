package gitpatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotsProduceIncrementalTurnPatches(t *testing.T) {
	t.Parallel()

	repository := newTestRepository(t)
	beforeTurnOne, err := Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatalf("snapshot before turn one: %v", err)
	}

	writeFile(t, filepath.Join(repository, "turn-one.txt"), "turn one\n")
	afterTurnOne, err := Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatalf("snapshot after turn one: %v", err)
	}
	turnOnePatch, err := Diff(context.Background(), repository, beforeTurnOne, afterTurnOne)
	if err != nil {
		t.Fatalf("diff turn one: %v", err)
	}
	if !strings.Contains(string(turnOnePatch), "turn-one.txt") {
		t.Fatalf("turn one patch does not contain turn-one.txt:\n%s", turnOnePatch)
	}

	beforeTurnTwo, err := Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatalf("snapshot before turn two: %v", err)
	}
	if beforeTurnTwo != afterTurnOne {
		t.Fatalf("turn two before tree = %q, want exact turn one after tree %q", beforeTurnTwo, afterTurnOne)
	}
	writeFile(t, filepath.Join(repository, "turn-two.txt"), "turn two\n")
	afterTurnTwo, err := Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatalf("snapshot after turn two: %v", err)
	}
	turnTwoPatch, err := Diff(context.Background(), repository, beforeTurnTwo, afterTurnTwo)
	if err != nil {
		t.Fatalf("diff turn two: %v", err)
	}
	if !strings.Contains(string(turnTwoPatch), "turn-two.txt") {
		t.Fatalf("turn two patch does not contain turn-two.txt:\n%s", turnTwoPatch)
	}
	if strings.Contains(string(turnTwoPatch), "turn-one.txt") {
		t.Fatalf("turn two patch repeats turn one:\n%s", turnTwoPatch)
	}
}

func TestSnapshotDoesNotMutateHEADIndexOrWorkingCopy(t *testing.T) {
	t.Parallel()

	repository := newTestRepository(t)
	writeFile(t, filepath.Join(repository, "staged.txt"), "staged\n")
	git(t, repository, "add", "staged.txt")
	writeFile(t, filepath.Join(repository, "tracked.txt"), "working change\n")
	writeFile(t, filepath.Join(repository, "untracked.txt"), "untracked\n")

	beforeHEAD := git(t, repository, "rev-parse", "HEAD")
	beforeIndex := git(t, repository, "write-tree")
	beforeStatus := git(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	beforeTracked := readFile(t, filepath.Join(repository, "tracked.txt"))
	beforeUntracked := readFile(t, filepath.Join(repository, "untracked.txt"))

	if _, err := Snapshot(context.Background(), repository); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if got := git(t, repository, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Fatalf("HEAD = %q, want unchanged %q", got, beforeHEAD)
	}
	if got := git(t, repository, "write-tree"); got != beforeIndex {
		t.Fatalf("real index tree = %q, want unchanged %q", got, beforeIndex)
	}
	if got := git(t, repository, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
		t.Fatalf("status changed:\n got: %q\nwant: %q", got, beforeStatus)
	}
	if got := readFile(t, filepath.Join(repository, "tracked.txt")); got != beforeTracked {
		t.Fatalf("tracked WorkingCopy = %q, want unchanged %q", got, beforeTracked)
	}
	if got := readFile(t, filepath.Join(repository, "untracked.txt")); got != beforeUntracked {
		t.Fatalf("untracked WorkingCopy = %q, want unchanged %q", got, beforeUntracked)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()

	repository := t.TempDir()
	git(t, repository, "init", "--quiet")
	git(t, repository, "config", "user.email", "worker@example.test")
	git(t, repository, "config", "user.name", "Worker Test")
	writeFile(t, filepath.Join(repository, "tracked.txt"), "base\n")
	git(t, repository, "add", "tracked.txt")
	git(t, repository, "commit", "--quiet", "-m", "base")
	return repository
}

func git(t *testing.T, repository string, args ...string) string {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
