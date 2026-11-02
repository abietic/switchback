package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// LocalHostPathVerifier is used when Backend can address the same absolute
// data root as Docker Engine, such as the local M1 bind-mount profile.
type LocalHostPathVerifier struct{}

func (LocalHostPathVerifier) VerifyHostPaths(ctx context.Context, root string, requirements []HostPathRequirement) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return fmt.Errorf("Docker HostPath root must be a non-root clean absolute path")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect Docker HostPath root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("Docker HostPath root must be a real directory")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve Docker HostPath root: %w", err)
	}
	for _, requirement := range requirements {
		path := requirement.Path
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || !isBelow(root, path) {
			return fmt.Errorf("Docker HostPath must be a clean child of the configured root")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("derive Docker HostPath relative path: %w", err)
		}
		current := root
		for _, segment := range splitPath(relative) {
			current = filepath.Join(current, segment)
			info, err := os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect Docker HostPath: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("Docker HostPath contains a symlink")
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect Docker HostPath: %w", err)
		}
		switch requirement.Kind {
		case HostPathDirectory:
			if !info.IsDir() {
				return fmt.Errorf("Docker HostPath must be an existing directory")
			}
		case HostPathRegularFile:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("Docker HostPath must be an existing regular file")
			}
		default:
			return fmt.Errorf("Docker HostPath kind is unsupported")
		}
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil || !isBelow(canonicalRoot, canonicalPath) {
			return fmt.Errorf("Docker HostPath escapes its canonical root")
		}
	}
	return nil
}

func splitPath(path string) []string {
	var segments []string
	for path != "." && path != "" {
		directory, leaf := filepath.Split(path)
		if leaf != "" {
			segments = append([]string{leaf}, segments...)
		}
		path = filepath.Clean(directory)
	}
	return segments
}

var _ HostPathVerifier = LocalHostPathVerifier{}
