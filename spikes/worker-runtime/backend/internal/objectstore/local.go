// Package objectstore persists the Spike's fixed evidence vocabulary locally.
package objectstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"switchback.local/spikes/worker-runtime/internal/redact"
)

// EvidenceKind is the closed vocabulary of authoritative Spike evidence.
type EvidenceKind string

const (
	EvidenceRawACP     EvidenceKind = "raw-acp"
	EvidenceTrace      EvidenceKind = "trace"
	EvidenceReport     EvidenceKind = "report"
	EvidencePatch      EvidenceKind = "patch"
	EvidenceManifest   EvidenceKind = "manifest"
	EvidenceAcceptance EvidenceKind = "acceptance"
)

// ParseUploadEvidenceKind accepts only evidence kinds permitted at the upload boundary.
func ParseUploadEvidenceKind(value string) (EvidenceKind, error) {
	switch EvidenceKind(value) {
	case EvidenceRawACP, EvidenceTrace, EvidenceReport, EvidencePatch:
		return EvidenceKind(value), nil
	default:
		return "", fmt.Errorf("unsupported upload evidence kind %q", value)
	}
}

// ObjectIdentity derives an object path from server-issued run identities.
type ObjectIdentity struct {
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
	Kind            EvidenceKind
}

// StoredObject describes an atomically persisted object.
type StoredObject struct {
	Path   string
	SHA256 string
	Bytes  int64
}

// LocalFileObjectStore writes evidence below one server-owned local root.
type LocalFileObjectStore struct {
	Root     string
	Redactor redact.Redactor
	rename   func(string, string) error
}

// Put redacts, hashes, and atomically stores one allowed evidence object.
func (s LocalFileObjectStore) Put(identity ObjectIdentity, payload []byte) (StoredObject, error) {
	relativePath, err := objectRelativePath(identity)
	if err != nil {
		return StoredObject{}, err
	}
	if err := validateRoot(s.Root); err != nil {
		return StoredObject{}, err
	}
	target := filepath.Join(s.Root, relativePath)
	if !isBelow(s.Root, target) {
		return StoredObject{}, fmt.Errorf("object path escapes store root")
	}
	segments := strings.Split(relativePath, string(filepath.Separator))
	if err := rejectExistingSymlinkPrefixes(s.Root, segments); err != nil {
		return StoredObject{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return StoredObject{}, fmt.Errorf("create object directory: %w", err)
	}
	if err := rejectExistingSymlinkPrefixes(s.Root, segments); err != nil {
		return StoredObject{}, err
	}

	redacted := []byte(s.Redactor.Redact(string(payload)))
	digest := sha256.Sum256(redacted)
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+"-*")
	if err != nil {
		return StoredObject{}, fmt.Errorf("create temporary object: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(redacted); err != nil {
		temporary.Close()
		return StoredObject{}, fmt.Errorf("write temporary object: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return StoredObject{}, fmt.Errorf("sync temporary object: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return StoredObject{}, fmt.Errorf("close temporary object: %w", err)
	}
	rename := s.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(temporaryName, target); err != nil {
		return StoredObject{}, fmt.Errorf("rename temporary object: %w", err)
	}
	return StoredObject{
		Path:   target,
		SHA256: hex.EncodeToString(digest[:]),
		Bytes:  int64(len(redacted)),
	}, nil
}

func objectRelativePath(identity ObjectIdentity) (string, error) {
	for _, value := range []string{identity.WorkerSessionID, identity.WorkerRunID} {
		if err := validateID(value); err != nil {
			return "", err
		}
	}
	base := filepath.Join(identity.WorkerSessionID, identity.WorkerRunID)
	switch identity.Kind {
	case EvidenceRawACP, EvidenceTrace, EvidenceReport, EvidencePatch:
		if err := validateID(identity.AgentTurnID); err != nil {
			return "", err
		}
		filename := map[EvidenceKind]string{
			EvidenceRawACP: "raw-acp.jsonl",
			EvidenceTrace:  "trace.jsonl",
			EvidenceReport: "report.md",
			EvidencePatch:  "changes.patch",
		}[identity.Kind]
		return filepath.Join(base, "turns", identity.AgentTurnID, filename), nil
	case EvidenceManifest:
		if identity.AgentTurnID != "" {
			return "", fmt.Errorf("manifest must not carry an agent turn ID")
		}
		return filepath.Join(base, "manifest.json"), nil
	case EvidenceAcceptance:
		if identity.AgentTurnID != "" {
			return "", fmt.Errorf("acceptance summary must not carry an agent turn ID")
		}
		return filepath.Join(base, "acceptance", "summary.json"), nil
	default:
		return "", fmt.Errorf("unsupported evidence kind %q", identity.Kind)
	}
}

func validateRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("object store root must be a nonempty clean absolute path")
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("object store root %q is a symlink", root)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect object store root: %w", err)
	}
	return nil
}

func validateID(value string) error {
	if value == "" || value == "." || value == ".." || filepath.IsAbs(value) || strings.ContainsAny(value, "/\\") || filepath.Clean(value) != value {
		return fmt.Errorf("identity %q must be one safe nonempty path segment", value)
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
			return fmt.Errorf("inspect object path prefix %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("object path prefix %q is a symlink", current)
		}
	}
	return nil
}
