package runtime

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MaterializedSession contains the server-derived paths needed by a worker.
type MaterializedSession struct {
	WorkingCopy PathPair
	AgentState  PathPair
	TraceSpool  PathPair
	RuntimeLogs PathPair
}

// PathOwnership is the narrow filesystem ownership operation required by the
// materializer. Keeping it injected makes the ownership boundary testable.
type PathOwnership interface {
	Chown(string, int, int) error
}

type PathOwnershipFunc func(string, int, int) error

func (f PathOwnershipFunc) Chown(path string, uid, gid int) error {
	return f(path, uid, gid)
}

const WorkerAgentUID = 10002

// Materializer creates an isolated, baseline Git working copy from the fixture.
type Materializer struct {
	Mapper      PathMapper
	FixturePath string
	Ownership   PathOwnership
}

// Materialize creates a new session tree. Existing session trees are preserved.
func (m Materializer) Materialize(ctx context.Context, sessionID string) (result MaterializedSession, err error) {
	if m.Ownership == nil {
		return MaterializedSession{}, fmt.Errorf("path ownership operation is required")
	}
	session, err := m.Mapper.SessionPath(sessionID)
	if err != nil {
		return MaterializedSession{}, err
	}
	result.WorkingCopy, err = m.Mapper.SessionPath(sessionID, "working-copy")
	if err != nil {
		return MaterializedSession{}, err
	}
	result.AgentState, err = m.Mapper.SessionPath(sessionID, "agent-state")
	if err != nil {
		return MaterializedSession{}, err
	}
	result.TraceSpool, err = m.Mapper.SessionPath(sessionID, "trace-spool")
	if err != nil {
		return MaterializedSession{}, err
	}
	result.RuntimeLogs, err = m.Mapper.SessionPath(sessionID, "runtime-logs")
	if err != nil {
		return MaterializedSession{}, err
	}
	if err := validateFixture(m.FixturePath); err != nil {
		return MaterializedSession{}, err
	}
	if _, err := os.Lstat(session.ContainerPath); err == nil {
		return MaterializedSession{}, fmt.Errorf("session tree %q already exists", session.ContainerPath)
	} else if !os.IsNotExist(err) {
		return MaterializedSession{}, fmt.Errorf("inspect session tree %q: %w", session.ContainerPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(session.ContainerPath), 0o700); err != nil {
		return MaterializedSession{}, fmt.Errorf("create sessions root: %w", err)
	}
	if err := rejectExistingSymlinkPrefixes(m.Mapper.ContainerDataRoot, []string{"sessions"}); err != nil {
		return MaterializedSession{}, err
	}
	if err := os.Mkdir(session.ContainerPath, 0o700); err != nil {
		return MaterializedSession{}, fmt.Errorf("create session tree: %w", err)
	}
	defer func() {
		if err != nil {
			cleanupErr := os.RemoveAll(session.ContainerPath)
			if cleanupErr != nil {
				err = fmt.Errorf("%w; remove partial session tree: %v", err, cleanupErr)
			}
		}
	}()

	if err := os.Mkdir(result.WorkingCopy.ContainerPath, 0o700); err != nil {
		return MaterializedSession{}, fmt.Errorf("create working copy root: %w", err)
	}
	if err := copyFixture(m.FixturePath, result.WorkingCopy.ContainerPath); err != nil {
		return MaterializedSession{}, err
	}
	if err := initializeFixtureGit(ctx, result.WorkingCopy.ContainerPath); err != nil {
		return MaterializedSession{}, err
	}
	for _, path := range []string{
		result.AgentState.ContainerPath,
		result.TraceSpool.ContainerPath,
		result.RuntimeLogs.ContainerPath,
	} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return MaterializedSession{}, fmt.Errorf("create session directory %q: %w", path, err)
		}
	}
	codexState := filepath.Join(result.AgentState.ContainerPath, "codex")
	if err := os.Mkdir(codexState, 0o700); err != nil {
		return MaterializedSession{}, fmt.Errorf("create Codex state directory %q: %w", codexState, err)
	}
	for _, path := range []string{
		result.WorkingCopy.ContainerPath,
		result.AgentState.ContainerPath,
		result.TraceSpool.ContainerPath,
	} {
		if err := handoffAgentTree(path, m.Ownership); err != nil {
			return MaterializedSession{}, err
		}
	}
	return result, nil
}

func handoffAgentTree(root string, ownership PathOwnership) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent-owned path %q is a symlink", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect agent-owned path %q: %w", path, err)
		}
		mode := info.Mode().Perm()
		if entry.IsDir() {
			mode |= 0o700
		} else if entry.Type().IsRegular() {
			mode |= 0o200
		} else {
			return fmt.Errorf("agent-owned path %q is not a regular file or directory", path)
		}
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("make agent-owned path writable %q: %w", path, err)
		}
		if err := ownership.Chown(path, WorkerAgentUID, WorkerAgentUID); err != nil {
			return fmt.Errorf("handoff agent-owned path %q: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("handoff agent-owned tree %q: %w", root, err)
	}
	return nil
}

func validateFixture(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("fixture path must be a nonempty clean absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect fixture: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("fixture path %q is not a directory", path)
	}
	return nil
}

func copyFixture(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture path %q is a symlink", path)
		}
		relativePath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relativePath == "." {
			return nil
		}
		target := filepath.Join(destination, relativePath)
		if !isBelow(destination, target) {
			return fmt.Errorf("fixture path %q escapes working copy", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm()|0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("fixture path %q is not a regular file", path)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		mode.Perm()|0o200,
	)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, mode.Perm()|0o200)
}

func initializeFixtureGit(ctx context.Context, workingCopy string) error {
	commands := [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Switchback Spike"},
		{"config", "user.email", "spike@switchback.local"},
		{"add", "-A"},
		{"commit", "-m", "fixture: baseline"},
	}
	for _, arguments := range commands {
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Dir = workingCopy
		command.Env = sanitizedGitEnvironment(os.Environ())
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", arguments, err, output)
		}
	}
	return nil
}

func sanitizedGitEnvironment(environment []string) []string {
	sanitized := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
			continue
		}
		sanitized = append(sanitized, entry)
	}
	return sanitized
}
