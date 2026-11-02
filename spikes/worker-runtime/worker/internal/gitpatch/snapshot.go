// Package gitpatch snapshots a dirty Git WorkingCopy without mutating it.
package gitpatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Snapshot writes the current WorkingCopy into a temporary Git index and
// returns the resulting tree object ID.
func Snapshot(ctx context.Context, repository string) (string, error) {
	index, err := os.CreateTemp("", "switchback-turn-index-*")
	if err != nil {
		return "", fmt.Errorf("create temporary Git index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", fmt.Errorf("close temporary Git index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("prepare temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)

	environment := gitEnvironment(indexPath)
	for _, args := range [][]string{
		{"read-tree", "HEAD"},
		{"add", "-A"},
	} {
		if _, err := runGit(ctx, repository, environment, args...); err != nil {
			return "", err
		}
	}
	tree, err := runGit(ctx, repository, environment, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(tree)), nil
}

// Diff returns the binary-safe patch between two exact tree snapshots.
func Diff(ctx context.Context, repository, beforeTree, afterTree string) ([]byte, error) {
	if !validObjectID(beforeTree) || !validObjectID(afterTree) {
		return nil, fmt.Errorf("Git tree IDs must be 40 or 64 lowercase hexadecimal characters")
	}
	return runGit(
		ctx,
		repository,
		nil,
		"diff",
		"--binary",
		"--no-ext-diff",
		beforeTree,
		afterTree,
		"--",
	)
}

func runGit(
	ctx context.Context,
	repository string,
	environment []string,
	args ...string,
) ([]byte, error) {
	trustedArgs := append(
		[]string{"-c", "safe.directory=" + repository},
		args...,
	)
	command := exec.CommandContext(ctx, "git", trustedArgs...)
	command.Dir = repository
	if environment == nil {
		environment = gitEnvironment("")
	}
	command.Env = environment
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitError.Stderr)))
	}
	return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func gitEnvironment(indexPath string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_INDEX_FILE=") || strings.HasPrefix(entry, "LC_ALL=") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "LC_ALL=C")
	if indexPath != "" {
		environment = append(environment, "GIT_INDEX_FILE="+indexPath)
	}
	return environment
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}
