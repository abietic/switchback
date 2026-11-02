package runtimesecret

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"golang.org/x/sys/unix"
)

const (
	observationRegistrySchema   = "switchback.runtime-observation-tokens.v1"
	observationRegistryFileName = "observation-tokens.json"
	observationRegistryMaxBytes = 64 << 10
)

var errObservationRegistryUnavailable = errors.New("Runtime observation registry is unavailable")

type ObservationRegistryRequest struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	Generation        uint64
	SessionStorage    application.SessionStorage
}

type observationRegistry struct {
	SchemaVersion     string                   `json:"schema_version"`
	WorkerSessionID   string                   `json:"worker_session_id"`
	RuntimeInstanceID string                   `json:"runtime_instance_id"`
	Generation        uint64                   `json:"runtime_generation"`
	Tokens            []secretcheck.KnownToken `json:"tokens"`
}

func (observationRegistry) String() string   { return "[REDACTED]" }
func (observationRegistry) GoString() string { return "[REDACTED]" }

func (m Materializer) sealObservationRegistry(request Request, bootstrap Bootstrap) error {
	registry := observationRegistry{
		SchemaVersion:     observationRegistrySchema,
		WorkerSessionID:   request.WorkerSessionID,
		RuntimeInstanceID: request.RuntimeInstanceID,
		Generation:        request.Generation,
		Tokens: deduplicateObservationTokens([]secretcheck.KnownToken{
			{Label: "provider-credential", Value: string(request.ProviderCredential)},
			{Label: "worker-control-token", Value: bootstrap.WorkerControlToken},
			{Label: "proxy-token", Value: bootstrap.ProxyToken},
			{Label: "model-sink-token", Value: bootstrap.ModelSinkToken},
		}),
	}
	content, err := json.Marshal(registry)
	if err != nil {
		return errObservationRegistryUnavailable
	}
	path := observationRegistryPath(request.SessionStorage, request.RuntimeInstanceID, request.Generation)
	if err := m.writeRootOnly(path, append(content, '\n')); err != nil {
		return errObservationRegistryUnavailable
	}
	return nil
}

func deduplicateObservationTokens(tokens []secretcheck.KnownToken) []secretcheck.KnownToken {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]secretcheck.KnownToken, 0, len(tokens))
	for _, token := range tokens {
		if _, duplicate := seen[token.Value]; duplicate {
			continue
		}
		seen[token.Value] = struct{}{}
		result = append(result, token)
	}
	return result
}

func (m Materializer) ObservationScanner(request ObservationRegistryRequest) (*secretcheck.Scanner, error) {
	if m.OwnerUID == nil || !canonicalIdentity(request.WorkerSessionID) ||
		!canonicalIdentity(request.RuntimeInstanceID) || request.Generation == 0 ||
		!validObservationBackendRoot(request.SessionStorage.BackendRoot, request.WorkerSessionID) {
		return nil, errObservationRegistryUnavailable
	}
	runtimeLeaf := request.RuntimeInstanceID + "-g" + strconv.FormatUint(request.Generation, 10)
	directories := []string{
		request.SessionStorage.BackendRoot,
		filepath.Join(request.SessionStorage.BackendRoot, "runtimes"),
		filepath.Join(request.SessionStorage.BackendRoot, "runtimes", runtimeLeaf),
		filepath.Join(request.SessionStorage.BackendRoot, "runtimes", runtimeLeaf, backendControlRootName),
	}
	for _, directory := range directories {
		if !m.privateRootOwnedDirectory(directory) {
			return nil, errObservationRegistryUnavailable
		}
	}
	path := observationRegistryPath(request.SessionStorage, request.RuntimeInstanceID, request.Generation)
	owner, err := m.OwnerUID(path)
	if err != nil || owner != 0 {
		return nil, errObservationRegistryUnavailable
	}
	openFileOwnerUID := m.OpenFileOwnerUID
	if openFileOwnerUID == nil {
		openFileOwnerUID = OSOpenFileOwnerUID
	}
	content, err := readObservationRegistry(path, openFileOwnerUID)
	if err != nil {
		return nil, errObservationRegistryUnavailable
	}
	var registry observationRegistry
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return nil, errObservationRegistryUnavailable
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errObservationRegistryUnavailable
	}
	if registry.SchemaVersion != observationRegistrySchema ||
		registry.WorkerSessionID != request.WorkerSessionID ||
		registry.RuntimeInstanceID != request.RuntimeInstanceID ||
		registry.Generation != request.Generation {
		return nil, errObservationRegistryUnavailable
	}
	scanner, err := secretcheck.New(registry.Tokens)
	if err != nil {
		return nil, errObservationRegistryUnavailable
	}
	return scanner, nil
}

func readObservationRegistry(path string, ownerUID func(*os.File) (uint32, error)) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errObservationRegistryUnavailable
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errObservationRegistryUnavailable
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o777 != 0o400 || stat.Size <= 0 || stat.Size > observationRegistryMaxBytes {
		return nil, errObservationRegistryUnavailable
	}
	owner, err := ownerUID(file)
	if err != nil || owner != 0 {
		return nil, errObservationRegistryUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, observationRegistryMaxBytes+1))
	if err != nil || len(content) == 0 || len(content) > observationRegistryMaxBytes {
		return nil, errObservationRegistryUnavailable
	}
	return content, nil
}

func (m Materializer) privateRootOwnedDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	owner, err := m.OwnerUID(path)
	return err == nil && owner == 0
}

func validObservationBackendRoot(root, workerSessionID string) bool {
	return root != "" && filepath.IsAbs(root) && filepath.Clean(root) == root &&
		filepath.Base(root) == workerSessionID && filepath.Base(filepath.Dir(root)) == "sessions"
}

func observationRegistryPath(storage application.SessionStorage, runtimeInstanceID string, generation uint64) string {
	runtimeLeaf := runtimeInstanceID + "-g" + strconv.FormatUint(generation, 10)
	return filepath.Join(storage.BackendRoot, "runtimes", runtimeLeaf, backendControlRootName, observationRegistryFileName)
}
