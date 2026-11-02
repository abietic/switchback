// Package localfile stores immutable Assistant Conversation evidence.
package localfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

const evidenceManifestSchema = "assistant.evidence_manifest.v1"

type EvidenceStore struct {
	root    string
	scanner *secretcheck.Scanner
	now     func() time.Time
}

func NewEvidenceStore(root string, scanner *secretcheck.Scanner, now func() time.Time) (*EvidenceStore, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		scanner == nil || now == nil {
		return nil, fmt.Errorf("Assistant evidence store configuration is invalid")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Assistant evidence root must be one real directory")
	}
	return &EvidenceStore{root: root, scanner: scanner, now: now}, nil
}

type evidenceManifest struct {
	SchemaVersion          string            `json:"schema_version"`
	TenantID               string            `json:"tenant_id"`
	UserID                 string            `json:"user_id"`
	WorkspaceID            string            `json:"workspace_id"`
	ConversationID         string            `json:"conversation_id"`
	ConversationGeneration uint64            `json:"conversation_generation"`
	AssistantRunID         string            `json:"assistant_run_id"`
	BoundaryType           string            `json:"boundary_type"`
	ObjectKey              string            `json:"object_key"`
	Checksum               string            `json:"checksum"`
	ByteSize               int64             `json:"byte_size"`
	RecordCount            int               `json:"record_count"`
	Metadata               map[string]string `json:"metadata"`
	CreatedAt              time.Time         `json:"created_at"`
}

func (store *EvidenceStore) WriteConversationEvidence(
	ctx context.Context,
	boundary interactionapp.EvidenceBoundary,
) (interactionapp.EvidenceResult, error) {
	if err := validateBoundary(boundary); err != nil {
		return interactionapp.EvidenceResult{}, err
	}
	var content bytes.Buffer
	for index, record := range boundary.Records {
		if err := rejectForbiddenFields(record); err != nil {
			return interactionapp.EvidenceResult{}, fmt.Errorf("Assistant evidence record %d is not persistable: %w", index, err)
		}
		content.Write(bytes.TrimSpace(record))
		content.WriteByte('\n')
	}
	if err := store.scanner.ScanBytes(ctx, "assistant-evidence-jsonl", content.Bytes()); err != nil {
		return interactionapp.EvidenceResult{}, fmt.Errorf("Assistant evidence known-token scan failed: %w", err)
	}
	sum := sha256.Sum256(content.Bytes())
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	workspace := boundary.WorkspaceID
	if workspace == "" {
		workspace = "unresolved"
	}
	scope := []string{
		"assistant", "tenants", boundary.TenantID, "users", boundary.UserID,
		"workspaces", workspace, "conversations", boundary.ConversationID,
		fmt.Sprintf("generation-%d", boundary.ConversationGeneration),
	}
	directory, err := store.ensureDirectory(scope)
	if err != nil {
		return interactionapp.EvidenceResult{}, err
	}
	digest := strings.TrimPrefix(checksum, "sha256:")
	objectFilename := boundary.BoundaryType + "-" + digest + ".jsonl"
	objectPath := filepath.Join(directory, objectFilename)
	if err := writeImmutable(objectPath, content.Bytes()); err != nil {
		return interactionapp.EvidenceResult{}, fmt.Errorf("write Assistant evidence segment: %w", err)
	}
	objectKey := strings.Join(append(scope, objectFilename), "/")
	manifest := evidenceManifest{
		SchemaVersion: evidenceManifestSchema,
		TenantID:      boundary.TenantID, UserID: boundary.UserID, WorkspaceID: boundary.WorkspaceID,
		ConversationID: boundary.ConversationID, ConversationGeneration: boundary.ConversationGeneration,
		AssistantRunID: boundary.AssistantRunID, BoundaryType: boundary.BoundaryType,
		ObjectKey: objectKey, Checksum: checksum, ByteSize: int64(content.Len()),
		RecordCount: len(boundary.Records), Metadata: copyMetadata(boundary.Metadata),
		CreatedAt: store.now().UTC(),
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return interactionapp.EvidenceResult{}, err
	}
	if err := rejectForbiddenFields(manifestJSON); err != nil {
		return interactionapp.EvidenceResult{}, fmt.Errorf("Assistant evidence manifest is not persistable: %w", err)
	}
	if err := store.scanner.ScanBytes(ctx, "assistant-evidence-manifest", manifestJSON); err != nil {
		return interactionapp.EvidenceResult{}, fmt.Errorf("Assistant manifest known-token scan failed: %w", err)
	}
	manifestFilename := boundary.BoundaryType + "-" + digest + ".manifest.json"
	manifestPath := filepath.Join(directory, manifestFilename)
	if err := writeManifestImmutable(manifestPath, append(manifestJSON, '\n'), manifest); err != nil {
		return interactionapp.EvidenceResult{}, fmt.Errorf("write Assistant evidence manifest: %w", err)
	}
	return interactionapp.EvidenceResult{
		ObjectKey: objectKey, ManifestKey: strings.Join(append(scope, manifestFilename), "/"),
		Checksum: checksum, ByteSize: int64(content.Len()),
	}, nil
}

func writeManifestImmutable(path string, content []byte, expected evidenceManifest) error {
	if existing, err := os.ReadFile(path); err == nil {
		var manifest evidenceManifest
		if json.Unmarshal(existing, &manifest) != nil ||
			manifest.SchemaVersion != expected.SchemaVersion ||
			manifest.TenantID != expected.TenantID || manifest.UserID != expected.UserID ||
			manifest.WorkspaceID != expected.WorkspaceID ||
			manifest.ConversationID != expected.ConversationID ||
			manifest.ConversationGeneration != expected.ConversationGeneration ||
			manifest.AssistantRunID != expected.AssistantRunID ||
			manifest.BoundaryType != expected.BoundaryType ||
			manifest.ObjectKey != expected.ObjectKey || manifest.Checksum != expected.Checksum ||
			manifest.ByteSize != expected.ByteSize || manifest.RecordCount != expected.RecordCount {
			return fmt.Errorf("immutable evidence manifest conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeImmutable(path, content)
}

func validateBoundary(boundary interactionapp.EvidenceBoundary) error {
	for name, value := range map[string]string{
		"tenant": boundary.TenantID, "user": boundary.UserID,
		"Conversation": boundary.ConversationID, "AssistantRun": boundary.AssistantRunID,
	} {
		if !validSegment(value) {
			return fmt.Errorf("%s evidence identity is invalid", name)
		}
	}
	if boundary.WorkspaceID != "" && !validSegment(boundary.WorkspaceID) {
		return fmt.Errorf("Workspace evidence identity is invalid")
	}
	if boundary.ConversationGeneration == 0 || len(boundary.Records) == 0 ||
		boundary.Metadata == nil {
		return fmt.Errorf("Assistant evidence scope and records are required")
	}
	switch boundary.BoundaryType {
	case "checkpoint", "ready_draft", "task_created", "cancelled", "run_failed":
	default:
		return fmt.Errorf("Assistant evidence boundary type is invalid")
	}
	for _, record := range boundary.Records {
		var object map[string]json.RawMessage
		if !json.Valid(record) || json.Unmarshal(record, &object) != nil || object == nil {
			return fmt.Errorf("Assistant evidence records must be JSON objects")
		}
	}
	return nil
}

var forbiddenEvidenceFields = map[string]struct{}{
	"api_key": {}, "authorization": {}, "credential": {}, "credentials": {}, "secret": {},
	"environment": {}, "env": {}, "hidden_chain_of_thought": {}, "chain_of_thought": {},
	"reasoning": {}, "analysis": {}, "raw_provider_payload": {}, "provider_response": {},
}

func rejectForbiddenFields(encoded []byte) error {
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return fmt.Errorf("invalid JSON")
	}
	return walkJSON(value)
}

func walkJSON(value any) error {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if _, forbidden := forbiddenEvidenceFields[strings.ToLower(key)]; forbidden {
				return fmt.Errorf("field %q is forbidden", key)
			}
			if err := walkJSON(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range current {
			if err := walkJSON(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func (store *EvidenceStore) ensureDirectory(segments []string) (string, error) {
	current := store.root
	for _, segment := range segments {
		if !validSegment(segment) {
			return "", fmt.Errorf("Assistant evidence path segment is invalid")
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Assistant evidence directory is unsafe")
		}
	}
	return current, nil
}

func writeImmutable(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, content) {
			return fmt.Errorf("immutable evidence key conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := path + ".partial"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func validSegment(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 160 &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func copyMetadata(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

var _ interactionapp.ObjectStore = (*EvidenceStore)(nil)
