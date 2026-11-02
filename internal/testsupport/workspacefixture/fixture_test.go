package workspacefixture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/switchback/internal/testsupport/workspacefixture"
)

func TestLoadExplicitM1WorkspaceRevision(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..", "..")
	fixture, err := workspacefixture.Load(
		repositoryRoot,
		filepath.Join(repositoryRoot, "test", "fixtures", "m1", "workspace-revision.json"),
	)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if fixture.WorkspaceID != "workspace-demo" || fixture.WorkspaceRevisionID != "workspace-revision-1" {
		t.Fatalf("unexpected WorkspaceRevision identity: %+v", fixture)
	}
	if _, err := os.Stat(fixture.SourcePath); err != nil {
		t.Fatalf("stat source fixture: %v", err)
	}
}

func TestWorkspaceFixtureRejectsEscapingSourcePath(t *testing.T) {
	repositoryRoot := t.TempDir()
	metadata := filepath.Join(repositoryRoot, "workspace-revision.json")
	if err := os.WriteFile(metadata, []byte(`{
  "schema_version":"workspace.revision.fixture.v1",
  "tenant_id":"tenant-local",
  "actor_user_id":"user-local",
  "project_id":"project-switchback",
  "workspace_id":"workspace-demo",
  "workspace_revision_id":"workspace-revision-1",
  "source_path":"../outside"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspacefixture.Load(repositoryRoot, metadata); err == nil {
		t.Fatal("escaping source path was accepted")
	}
}
