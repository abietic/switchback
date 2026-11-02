package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const integrationBase = "8fdbe6b556a0602b8392a2eed0e09b9e007542d1"

type DockerCommand func(context.Context, []string, io.Writer, io.Writer) error

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, fmt.Errorf("command output limit exceeded")
	}
	return b.buffer.Write(p)
}
func (b *boundedBuffer) Len() int       { return b.buffer.Len() }
func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }
func gitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = repo
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			c.Env = append(c.Env, e)
		}
	}
	c.Env = append(c.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1")
	return c
}
func git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	c := gitCommand(ctx, repo, args...)
	errout := boundedBuffer{limit: 1 << 20}
	out := boundedBuffer{limit: 64 << 20}
	c.Stderr = &errout
	c.Stdout = &out
	e := c.Run()
	if e != nil {
		return nil, fmt.Errorf("git %v: %w: %s", args, e, errout.String())
	}
	return out.Bytes(), nil
}
func validArguments(args []string) bool {
	return len(args) == 4 && (args[0] == "build" || args[0] == "check") && commitPattern.MatchString(args[1]) && projectPattern.MatchString(args[2]) && filepath.IsAbs(args[3]) && !strings.HasSuffix(args[3], "/")
}

type verifiedResult struct {
	SchemaVersion string       `json:"schema_version"`
	Project       string       `json:"project"`
	Output        string       `json:"output"`
	Commit        string       `json:"source_commit"`
	Tree          string       `json:"source_tree"`
	Digest        string       `json:"source_manifest_sha256"`
	Images        []builtImage `json:"images"`
}

func successOutput(stdout, stderr io.Writer, b []byte) int {
	line := append(b, '\n')
	n, e := stdout.Write(line)
	if e != nil || n != len(line) {
		if e == nil {
			e = io.ErrShortWrite
		}
		fmt.Fprintln(stderr, "success output failed:", e)
		return 1
	}
	return 0
}
func Run(ctx context.Context, repository string, args []string, stdout, stderr io.Writer, docker DockerCommand) int {
	return runWithBaseline(ctx, repository, args, stdout, stderr, docker, integrationBase)
}

// baseline is an explicit in-package test seam; the CLI always uses integrationBase.
func runWithBaseline(ctx context.Context, repository string, args []string, stdout, stderr io.Writer, docker DockerCommand, baseline string) int {
	if !validArguments(args) {
		fmt.Fprintln(stderr, "usage: build|check COMMIT PROJECT CLAIM_ROOT")
		return 2
	}
	for _, p := range []string{repository, args[3]} {
		canonical, e := filepath.EvalSymlinks(p)
		if e != nil || !filepath.IsAbs(p) || filepath.Clean(p) != p || canonical != p {
			fmt.Fprintln(stderr, "root must be exact canonical path", p, e)
			return 1
		}
	}
	root, e := git(ctx, repository, "rev-parse", "--show-toplevel")
	if e != nil || strings.TrimSpace(string(root)) != repository {
		fmt.Fprintln(stderr, "repository must be Git root", e)
		return 1
	}
	if args[0] == "build" {
		if e := validateClaim(args[2], args[3]); e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
	}

	if _, e := git(ctx, repository, "merge-base", "--is-ancestor", baseline, args[1]); e != nil {
		fmt.Fprintln(stderr, "candidate does not contain integration baseline", e)
		return 1
	}
	if args[0] == "check" {
		p, e := checkBuild(ctx, repository, args[1], args[2], args[3])
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		result := verifiedResult{"observability.build_provenance_verified.v1", p.Project, p.Output, p.Commit, p.Tree, p.Digest, p.Images}
		return successOutput(stdout, stderr, jsonBytes(result))
	}
	head, e := git(ctx, repository, "rev-parse", "HEAD")
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if strings.TrimSpace(string(head)) != args[1] {
		fmt.Fprintln(stderr, "candidate must equal HEAD")
		return 1
	}
	status, e := git(ctx, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil || len(status) != 0 {
		fmt.Fprintln(stderr, "candidate must be clean", e)
		return 1
	}
	build := filepath.Join(args[3], "build")
	if e = os.Mkdir(build, 0700); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	s, e := captureSource(ctx, repository, args[1], args[2], args[3], filepath.Join(build, "source"))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = privateRoots(args[3]); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = exclusive(filepath.Join(build, "source.json"), jsonBytes(s)); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = executeBuild(ctx, docker, args[3], args[2], s); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	b, e := regularRead(args[3], "build/provenance.json", 1<<20)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return successOutput(stdout, stderr, b)
}
func main() {
	ctx := context.Background()
	if !validArguments(os.Args[1:]) {
		os.Exit(Run(ctx, "", os.Args[1:], os.Stdout, os.Stderr, nil))
	}
	cwd, e := os.Getwd()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	root, e := git(ctx, cwd, "rev-parse", "--show-toplevel")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	os.Exit(Run(ctx, strings.TrimSpace(string(root)), os.Args[1:], os.Stdout, os.Stderr, func(ctx context.Context, args []string, out, err io.Writer) error {
		c := exec.CommandContext(ctx, "docker", args...)
		c.Stdout = out
		c.Stderr = err
		return c.Run()
	}))
}
