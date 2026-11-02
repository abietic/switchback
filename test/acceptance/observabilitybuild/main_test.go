package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	t                                       *testing.T
	repo, commit, baseline, project, output string
	dockerCalled                            bool
	docker                                  DockerCommand
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			c.Env = append(c.Env, e)
		}
	}
	c.Env = append(c.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "init", "-q")
	// A private-history-free DAG: an empty synthetic baseline and a minimal
	// candidate. No source repository, alternates or inherited author metadata.
	testGit(t, repo, "commit", "--allow-empty", "-m", "synthetic baseline")
	baseline := testGit(t, repo, "rev-parse", "HEAD")
	f := &fixture{t: t, repo: repo, baseline: baseline, project: "switchback-obsv-fixture01", output: filepath.Join(root, "claim")}
	for _, p := range []string{"images/backend/Dockerfile", "images/worker/Dockerfile", "test/acceptance/observability/Dockerfile.worker", ".dockerignore", "go.mod", "go.sum", "source.txt"} {
		f.write(p, "fixture\n")
	}
	f.commit = testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "fixture")
	f.commit = testGit(t, repo, "rev-parse", "HEAD")
	if err := os.Mkdir(f.output, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"project": f.project, "output": f.output, "token": strings.Repeat("a", 64)})
	f.writeClaim(b)
	f.docker = func(context.Context, []string, io.Writer, io.Writer) error { f.dockerCalled = true; return nil }
	return f
}
func (f *fixture) write(p, s string) {
	f.t.Helper()
	p = filepath.Join(f.repo, p)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) writeClaim(b []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.output, "claim.json"), b, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) writeTrackedFileAndCommit(p, s string) {
	f.write(p, s)
	testGit(f.t, f.repo, "add", ".")
	testGit(f.t, f.repo, "commit", "-m", "change")
	f.commit = testGit(f.t, f.repo, "rev-parse", "HEAD")
}
func (f *fixture) invoke(args ...string) (int, string, string) {
	var out, err bytes.Buffer
	c := runWithBaseline(context.Background(), f.repo, args, &out, &err, f.docker, f.baseline)
	return c, out.String(), err.String()
}
func TestBuildRejectsNonHeadCommitBeforeDocker(t *testing.T) {
	f := newFixture(t)
	previous := f.commit
	f.writeTrackedFileAndCommit("source.txt", "changed\n")
	code, _, message := f.invoke("build", previous, f.project, f.output)
	if code != 1 || !strings.Contains(message, "HEAD") {
		t.Fatalf("stale candidate: exit=%d stderr=%q", code, message)
	}
	if f.dockerCalled {
		t.Fatal("stale candidate reached Docker boundary")
	}
}
func TestRunUsageErrors(t *testing.T) {
	f := newFixture(t)
	for _, a := range [][]string{{}, {"unknown", f.commit, f.project, f.output}, {"build", "bad", f.project, f.output}, {"build", f.commit, "bad", f.output}, {"build", f.commit, f.project, "relative"}, {"build", f.commit, f.project, f.output + "/"}} {
		c, _, m := f.invoke(a...)
		if c != 2 {
			t.Fatalf("args %q exit=%d stderr=%s", a, c, m)
		}
	}
}
func TestCompiledCLIExitStatusesAndFakeRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	dir := t.TempDir()
	binary := buildFixtureCLI(t, f.baseline)
	script := `#!/usr/bin/env bash
set -euo pipefail
umask 077
store=$(cd "$(dirname "$0")" && pwd -P)
if [[ "$1 $2" == "context show" ]]; then printf 'default\n'; exit; fi
if [[ "$1 $2" == "context inspect" ]]; then printf '{"Name":"default","Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}\n'; exit; fi
if [[ "$3" == "info" ]]; then printf '{"ID":"fixture-engine","Name":"fixture-local","OSType":"linux","Architecture":"aarch64"}\n'; exit; fi
if [[ "$3" == "buildx" ]]; then printf 'Name: default\nDriver: docker\nNodes:\nName: default\nEndpoint: default\nStatus: running\n'; exit; fi
if [[ "$3" == "image" ]]; then
 ref=$5
 if [[ "$ref" == *@sha256:* ]]; then id=sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
 elif [[ -f "$store/$ref.id" ]]; then read -r id < "$store/$ref.id"
 else printf 'No such image: %s\n' "$ref" >&2; exit 1; fi
 printf '{"Id":"%s","Os":"linux","Architecture":"arm64"}\n' "$id"; exit
fi
if [[ "$3" == "build" ]]; then
 while [[ $# -gt 0 ]]; do case "$1" in --iidfile) iid=$2; shift;; -t) tag=$2; shift;; esac; shift; done
 case "$tag" in *-backend:*) id=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;; *-worker-base:*) id=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb;; *) id=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc;; esac
 printf '%s\n' "$id" > "$iid"; printf '%s\n' "$id" > "$store/$tag.id"; printf 'fixture build output\n'; exit
fi
printf 'unexpected fake Docker argv\n' >&2; exit 99
`
	if e := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	run := func(want int, a ...string) string {
		t.Helper()
		c := exec.Command(binary, a...)
		c.Dir = f.repo
		c.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		b, e := c.CombinedOutput()
		code := 0
		if e != nil {
			ec, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatal(e)
			}
			code = ec.ExitCode()
		}
		if code != want {
			t.Fatalf("CLI %q exit=%d want=%d output=%s", a, code, want, b)
		}
		return string(b)
	}
	run(2)
	outside := exec.Command(binary)
	outside.Dir = dir
	b, e := outside.CombinedOutput()
	ec, ok := e.(*exec.ExitError)
	if !ok || ec.ExitCode() != 2 {
		t.Fatalf("outside-repository usage exit=%v output=%s", e, b)
	}
	run(2, "build", "bad", f.project, f.output)
	run(1, "check", f.commit, f.project, f.output)
	assertSuccessJSON(t, f, run(0, "build", f.commit, f.project, f.output), false)
	out := run(0, "check", f.commit, f.project, f.output)
	assertSuccessJSON(t, f, out, true)
}
func TestCompiledCLIStopsOversizeDockerOutputWhileReading(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			f := newFixture(t)
			binary := buildFixtureCLI(t, f.baseline)
			fake := t.TempDir()
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			script := "#!/bin/sh\ndd if=/dev/zero bs=65536 count=32" + redirect + " 2>/dev/null\nexit 0\n"
			if e := os.WriteFile(filepath.Join(fake, "docker"), []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			command := exec.Command(binary, "build", f.commit, f.project, f.output)
			command.Dir = f.repo
			command.Env = append(os.Environ(), "PATH="+fake+":"+os.Getenv("PATH"))
			var out, errout bytes.Buffer
			command.Stdout = &out
			command.Stderr = &errout
			e := command.Run()
			exit, ok := e.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("oversize CLI exit=%v", e)
			}
			if !strings.Contains(errout.String(), "command output limit exceeded") {
				t.Fatalf("not limited during exec copy: stderr=%q", errout.String())
			}
			if out.Len() != 0 {
				t.Fatal("oversize emitted success JSON")
			}
			if _, e := os.Stat(filepath.Join(f.output, "build", "provenance.json")); !os.IsNotExist(e) {
				t.Fatal("oversize created proof")
			}
		})
	}
}
func TestWrappersUsageAndMissingClaim(t *testing.T) {
	cwd, _ := os.Getwd()
	for _, name := range []string{"build-observability-images", "check-observability-build"} {
		script := filepath.Join(cwd, "../../../scripts", name)
		for _, a := range [][]string{{}, {"bad", "bad", "relative"}, {integrationBase, "switchback-obsv-fixture01", filepath.Join(t.TempDir(), "missing")}} {
			c := exec.Command(script, a...)
			b, e := c.CombinedOutput()
			want := 2
			if len(a) == 3 && a[0] == integrationBase {
				want = 1
			}
			ec, ok := e.(*exec.ExitError)
			if !ok || ec.ExitCode() != want {
				t.Fatalf("wrapper %s exit %v want %d output=%s", name, e, want, b)
			}
		}
	}
}

// The source overlay substitutes only a test baseline in this compiled fixture.
// The production CLI has no environment, argument or JSON baseline override.
func buildFixtureCLI(t *testing.T, baseline string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "tool")
	args := []string{"build", "-p=1", "-o", binary}
	if baseline != "" {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(cwd, "main.go")
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		old := "const integrationBase = \"" + integrationBase + "\""
		if strings.Count(string(raw), old) != 1 {
			t.Fatal("baseline overlay source differs")
		}
		replacement := filepath.Join(dir, "main.go")
		if err := os.WriteFile(replacement, []byte(strings.Replace(string(raw), old, "const integrationBase = \""+baseline+"\"", 1)), 0600); err != nil {
			t.Fatal(err)
		}
		overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{source: replacement}})
		overlayPath := filepath.Join(dir, "overlay.json")
		if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-overlay", overlayPath)
	}
	cmd := exec.Command("go", append(args, ".")...)
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "GOFLAGS=-p=1", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile %v %s", err, b)
	}
	return binary
}

func TestBuildRejectsUnrelatedBaselineBeforeDocker(t *testing.T) {
	f := newFixture(t)
	testGit(t, f.repo, "checkout", "--orphan", "unrelated")
	testGit(t, f.repo, "commit", "-m", "unrelated root")
	unrelated := testGit(t, f.repo, "rev-parse", "HEAD")
	var out, stderr bytes.Buffer
	code := runWithBaseline(context.Background(), f.repo, []string{"build", unrelated, f.project, f.output}, &out, &stderr, f.docker, f.baseline)
	if code != 1 || !strings.Contains(stderr.String(), "does not contain integration baseline") || f.dockerCalled || out.Len() != 0 {
		t.Fatalf("unrelated baseline: exit=%d docker=%v stderr=%s", code, f.dockerCalled, stderr.String())
	}
}

func TestBuildDefaultBaselineRejectsSyntheticHistory(t *testing.T) {
	f := newFixture(t)
	var out, stderr bytes.Buffer
	args := []string{"build", f.commit, f.project, f.output}
	if code := Run(context.Background(), f.repo, args, &out, &stderr, f.docker); code != 1 || !strings.Contains(stderr.String(), "does not contain integration baseline") || f.dockerCalled {
		t.Fatalf("default baseline: exit=%d docker=%v stderr=%s", code, f.dockerCalled, stderr.String())
	}
	// No overlay: the actual CLI default must retain the same fail-closed gate.
	binary := buildFixtureCLI(t, "")
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "docker"), []byte("#!/bin/sh\necho unexpected-Docker-boundary >&2\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "PATH="+fake+":"+os.Getenv("PATH"))
	raw, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 || !strings.Contains(string(raw), "does not contain integration baseline") || strings.Contains(string(raw), "unexpected-Docker-boundary") {
		t.Fatalf("compiled default baseline: %v %s", err, raw)
	}
}
