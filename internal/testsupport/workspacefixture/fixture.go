// Package workspacefixture loads the explicit M1 WorkspaceRevision used only
// by integration and end-to-end tests.
package workspacefixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const schemaVersion = "workspace.revision.fixture.v1"

type WorkspaceRevision struct {
	SchemaVersion       string `json:"schema_version"`
	TenantID            string `json:"tenant_id"`
	ActorUserID         string `json:"actor_user_id"`
	ProjectID           string `json:"project_id"`
	WorkspaceID         string `json:"workspace_id"`
	WorkspaceRevisionID string `json:"workspace_revision_id"`
	SourceRelativePath  string `json:"source_path"`
	SourcePath          string `json:"-"`
}

func Load(repositoryRoot, metadataPath string) (WorkspaceRevision, error) {
	root, err := canonicalDirectory(repositoryRoot)
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("resolve repository root: %w", err)
	}
	metadata, err := canonicalFile(metadataPath)
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("resolve WorkspaceRevision fixture: %w", err)
	}
	if !isBelow(root, metadata) {
		return WorkspaceRevision{}, fmt.Errorf("WorkspaceRevision fixture escapes repository root")
	}
	data, err := os.ReadFile(metadata)
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("read WorkspaceRevision fixture: %w", err)
	}
	var fixture WorkspaceRevision
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return WorkspaceRevision{}, fmt.Errorf("decode WorkspaceRevision fixture: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return WorkspaceRevision{}, fmt.Errorf("decode WorkspaceRevision fixture: trailing JSON value")
	}
	if err := fixture.validate(); err != nil {
		return WorkspaceRevision{}, err
	}
	if filepath.IsAbs(fixture.SourceRelativePath) || filepath.Clean(fixture.SourceRelativePath) != fixture.SourceRelativePath {
		return WorkspaceRevision{}, fmt.Errorf("fixture source_path must be a clean repository-relative path")
	}
	source, err := canonicalDirectory(filepath.Join(root, fixture.SourceRelativePath))
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("resolve fixture source: %w", err)
	}
	if !isBelow(root, source) {
		return WorkspaceRevision{}, fmt.Errorf("fixture source_path escapes repository root")
	}
	fixture.SourcePath = source
	return fixture, nil
}

func (fixture WorkspaceRevision) validate() error {
	if fixture.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported WorkspaceRevision fixture schema %q", fixture.SchemaVersion)
	}
	for name, value := range map[string]string{
		"tenant_id": fixture.TenantID, "actor_user_id": fixture.ActorUserID,
		"project_id": fixture.ProjectID, "workspace_id": fixture.WorkspaceID,
		"workspace_revision_id": fixture.WorkspaceRevisionID, "source_path": fixture.SourceRelativePath,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s must be a nonempty canonical string", name)
		}
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", canonical)
	}
	return canonical, nil
}

func canonicalFile(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", canonical)
	}
	return canonical, nil
}

func isBelow(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
