// Package localstorage materializes server-owned WorkerSession storage.
package localstorage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

const WorkerAgentUID = 10002

type Ownership interface {
	Chown(path string, uid, gid int) error
}

type OwnershipFunc func(path string, uid, gid int) error

func (f OwnershipFunc) Chown(path string, uid, gid int) error {
	return f(path, uid, gid)
}

type OSOwnership struct{}

func (OSOwnership) Chown(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}

type Materializer struct {
	// BackendDataRoot is the path visible inside switchback-backend.
	BackendDataRoot string
	// EngineDataRoot is the same storage root as addressed by Docker Engine.
	EngineDataRoot string
	Ownership      Ownership
}

func (m Materializer) Materialize(workerSessionID string) (storage application.SessionStorage, err error) {
	if m.Ownership == nil {
		return application.SessionStorage{}, fmt.Errorf("storage ownership operation is required")
	}
	if err := validateExistingBackendRoot(m.BackendDataRoot); err != nil {
		return application.SessionStorage{}, err
	}
	if err := validateRoot("Docker Engine data", m.EngineDataRoot); err != nil {
		return application.SessionStorage{}, err
	}
	if err := validateIdentity(workerSessionID); err != nil {
		return application.SessionStorage{}, err
	}

	segments := []string{"sessions", workerSessionID}
	backendSessionRoot := filepath.Join(append([]string{m.BackendDataRoot}, segments...)...)
	engineSessionRoot := filepath.Join(append([]string{m.EngineDataRoot}, segments...)...)
	if !isWithin(m.BackendDataRoot, backendSessionRoot) || !isWithin(m.EngineDataRoot, engineSessionRoot) {
		return application.SessionStorage{}, fmt.Errorf("WorkerSession storage escapes configured data root")
	}
	if err := rejectExistingSymlinkPrefixes(m.BackendDataRoot, segments); err != nil {
		return application.SessionStorage{}, err
	}
	if _, statErr := os.Lstat(backendSessionRoot); statErr == nil {
		return application.SessionStorage{}, fmt.Errorf("WorkerSession storage %q already exists", backendSessionRoot)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return application.SessionStorage{}, fmt.Errorf("inspect WorkerSession storage: %w", statErr)
	}

	sessionsRoot := filepath.Join(m.BackendDataRoot, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
		return application.SessionStorage{}, fmt.Errorf("create sessions root: %w", err)
	}
	if err := os.Chmod(sessionsRoot, 0o700); err != nil {
		return application.SessionStorage{}, fmt.Errorf("protect sessions root: %w", err)
	}
	if err := rejectExistingSymlinkPrefixes(m.BackendDataRoot, []string{"sessions"}); err != nil {
		return application.SessionStorage{}, err
	}
	if err := os.Mkdir(backendSessionRoot, 0o700); err != nil {
		return application.SessionStorage{}, fmt.Errorf("create WorkerSession storage: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := os.RemoveAll(backendSessionRoot); cleanupErr != nil {
			err = fmt.Errorf("%w; remove partial WorkerSession storage: %v", err, cleanupErr)
		}
	}()

	storage = application.SessionStorage{
		BackendRoot: backendSessionRoot,
		EngineRoot:  engineSessionRoot,
		WorkingCopy: mount(backendSessionRoot, engineSessionRoot, "working-copy", "/workspace"),
		AgentState:  mount(backendSessionRoot, engineSessionRoot, "agent-state", "/var/lib/switchback/agent-state"),
		TraceSpool:  mount(backendSessionRoot, engineSessionRoot, "trace-spool", "/var/lib/switchback/trace-spool"),
	}
	for _, storageMount := range []application.StorageMount{storage.WorkingCopy, storage.AgentState, storage.TraceSpool} {
		if err := os.Mkdir(storageMount.BackendPath, 0o700); err != nil {
			return application.SessionStorage{}, fmt.Errorf("create WorkerSession directory %q: %w", storageMount.BackendPath, err)
		}
		if err := m.Ownership.Chown(storageMount.BackendPath, WorkerAgentUID, WorkerAgentUID); err != nil {
			return application.SessionStorage{}, fmt.Errorf("handoff WorkerSession directory %q: %w", storageMount.BackendPath, err)
		}
	}
	return storage, nil
}

// OpenExisting reconstructs only server-derived mount identities. It never
// creates, copies, cleans, or changes ownership of durable Session data.
func (m Materializer) OpenExisting(workerSessionID string) (application.SessionStorage, error) {
	if err := validateExistingBackendRoot(m.BackendDataRoot); err != nil {
		return application.SessionStorage{}, err
	}
	if err := validateRoot("Docker Engine data", m.EngineDataRoot); err != nil {
		return application.SessionStorage{}, err
	}
	if err := validateIdentity(workerSessionID); err != nil {
		return application.SessionStorage{}, err
	}
	segments := []string{"sessions", workerSessionID}
	if err := rejectExistingSymlinkPrefixes(m.BackendDataRoot, segments); err != nil {
		return application.SessionStorage{}, err
	}
	backendRoot := filepath.Join(m.BackendDataRoot, "sessions", workerSessionID)
	engineRoot := filepath.Join(m.EngineDataRoot, "sessions", workerSessionID)
	storage := application.SessionStorage{
		BackendRoot: backendRoot,
		EngineRoot:  engineRoot,
		WorkingCopy: mount(backendRoot, engineRoot, "working-copy", "/workspace"),
		AgentState:  mount(backendRoot, engineRoot, "agent-state", "/var/lib/switchback/agent-state"),
		TraceSpool:  mount(backendRoot, engineRoot, "trace-spool", "/var/lib/switchback/trace-spool"),
	}
	for _, path := range []string{
		storage.BackendRoot,
		storage.WorkingCopy.BackendPath,
		storage.AgentState.BackendPath,
		storage.TraceSpool.BackendPath,
	} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return application.SessionStorage{}, fmt.Errorf(
				"existing WorkerSession storage path %q is not a real directory",
				path,
			)
		}
	}
	return storage, nil
}

func mount(backendRoot, engineRoot, name, workerPath string) application.StorageMount {
	return application.StorageMount{
		BackendPath:  filepath.Join(backendRoot, name),
		EngineSource: filepath.Join(engineRoot, name),
		WorkerPath:   workerPath,
	}
}

func validateExistingBackendRoot(root string) error {
	if err := validateRoot("Backend data", root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect Backend data root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Backend data root %q is a symlink", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("Backend data root %q is not a directory", root)
	}
	return nil
}

func validateRoot(name, root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("%s root must be a nonempty clean absolute path", name)
	}
	return nil
}

func validateIdentity(value string) error {
	if value == "" || value == "." || value == ".." || len(value) > 160 || strings.TrimSpace(value) != value {
		return fmt.Errorf("WorkerSession id must be one canonical path segment")
	}
	for index, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || (index > 0 && (char == '-' || char == '_' || char == '.')) {
			continue
		}
		return fmt.Errorf("WorkerSession id must be one canonical path segment")
	}
	return nil
}

func rejectExistingSymlinkPrefixes(root string, segments []string) error {
	current := root
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect storage path prefix %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("storage path prefix %q is a symlink", current)
		}
	}
	return nil
}

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
