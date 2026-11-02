package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathPair maps one server-derived relative path into backend and host roots.
type PathPair struct {
	ContainerPath string
	HostPath      string
}

// PathMapper holds the server-owned roots used by the backend and Docker.
type PathMapper struct {
	ContainerDataRoot string
	HostDataRoot      string
}

// SessionPath derives the same safe relative path below both configured roots.
func (m PathMapper) SessionPath(sessionID string, elems ...string) (PathPair, error) {
	if err := validateRoot("container data", m.ContainerDataRoot); err != nil {
		return PathPair{}, err
	}
	if err := rejectSymlinkRoot(m.ContainerDataRoot); err != nil {
		return PathPair{}, err
	}
	if err := validateRoot("host data", m.HostDataRoot); err != nil {
		return PathPair{}, err
	}

	segments := append([]string{"sessions", sessionID}, elems...)
	for _, segment := range segments {
		if err := validatePathSegment(segment); err != nil {
			return PathPair{}, err
		}
	}
	relativePath := filepath.Join(segments...)
	containerPath := filepath.Join(m.ContainerDataRoot, relativePath)
	hostPath := filepath.Join(m.HostDataRoot, relativePath)
	if !isBelow(m.ContainerDataRoot, containerPath) || !isBelow(m.HostDataRoot, hostPath) {
		return PathPair{}, fmt.Errorf("session path escapes configured data root")
	}
	if err := rejectExistingSymlinkPrefixes(m.ContainerDataRoot, segments); err != nil {
		return PathPair{}, err
	}

	return PathPair{ContainerPath: containerPath, HostPath: hostPath}, nil
}

func validateRoot(name, root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("%s root must be a nonempty absolute path", name)
	}
	if filepath.Clean(root) != root {
		return fmt.Errorf("%s root must not contain lexical traversal", name)
	}
	return nil
}

func validatePathSegment(segment string) error {
	if segment == "" || segment == "." || segment == ".." {
		return fmt.Errorf("path element must be a nonempty non-dot name")
	}
	if filepath.IsAbs(segment) || strings.ContainsAny(segment, "/\\") || filepath.Clean(segment) != segment {
		return fmt.Errorf("path element %q must be one safe relative name", segment)
	}
	return nil
}

func rejectSymlinkRoot(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect container data root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("container data root %q is a symlink", root)
	}
	return nil
}

func isBelow(root, path string) bool {
	relativePath, err := filepath.Rel(root, path)
	return err == nil && relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
}

func rejectExistingSymlinkPrefixes(root string, segments []string) error {
	current := root
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect path prefix %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path prefix %q is a symlink", current)
		}
	}
	return nil
}
