package localfile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func TestEvidenceStoreWritesChecksumAddressedJSONLAndManifest(t *testing.T) {
	root := t.TempDir()
	store := evidenceStore(t, root)
	boundary := interactionapp.EvidenceBoundary{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "",
		ConversationID: "conversation-1", ConversationGeneration: 1,
		AssistantRunID: "run-1", BoundaryType: "ready_draft",
		Records: []json.RawMessage{
			json.RawMessage(`{"type":"turn","content":"visible"}`),
			json.RawMessage(`{"type":"readiness","status":"ready_confirmed"}`),
		},
		Metadata: map[string]string{"policy_revision": "readiness-v1"},
	}
	first, err := store.WriteConversationEvidence(context.Background(), boundary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.WriteConversationEvidence(context.Background(), boundary)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.Contains(first.ObjectKey, "/workspaces/unresolved/") ||
		!strings.Contains(first.ObjectKey, strings.TrimPrefix(first.Checksum, "sha256:")) ||
		!strings.HasSuffix(first.ManifestKey, ".manifest.json") {
		t.Fatalf("unexpected evidence result: %+v", first)
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(first.ObjectKey)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "\n") != 2 || int64(len(content)) != first.ByteSize {
		t.Fatalf("invalid JSONL content: %q", content)
	}
	manifest, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(first.ManifestKey)))
	if err != nil || !strings.Contains(string(manifest), `"schema_version":"assistant.evidence_manifest.v1"`) {
		t.Fatalf("invalid evidence manifest: %s error=%v", manifest, err)
	}
}

func TestEvidenceStoreRejectsSecretsEnvironmentAndHiddenReasoning(t *testing.T) {
	store := evidenceStore(t, t.TempDir())
	tests := []json.RawMessage{
		json.RawMessage(`{"content":"known-provider-secret"}`),
		json.RawMessage(`{"environment":{"KEY":"value"}}`),
		json.RawMessage(`{"reasoning":"hidden"}`),
		json.RawMessage(`{"raw_provider_payload":{"value":"raw"}}`),
	}
	for _, record := range tests {
		boundary := interactionapp.EvidenceBoundary{
			TenantID: "tenant-local", UserID: "user-local", ConversationID: "conversation-1",
			ConversationGeneration: 1, AssistantRunID: "run-1", BoundaryType: "run_failed",
			Records: []json.RawMessage{record}, Metadata: map[string]string{},
		}
		if _, err := store.WriteConversationEvidence(context.Background(), boundary); err == nil {
			t.Fatalf("forbidden evidence was persisted: %s", record)
		}
	}
}

func TestEvidenceStoreKeepsTenantAndConversationScopesSeparate(t *testing.T) {
	root := t.TempDir()
	store := evidenceStore(t, root)
	base := interactionapp.EvidenceBoundary{
		TenantID: "tenant-a", UserID: "user-local", WorkspaceID: "workspace-a",
		ConversationID: "conversation-1", ConversationGeneration: 1,
		AssistantRunID: "run-1", BoundaryType: "checkpoint",
		Records:  []json.RawMessage{json.RawMessage(`{"state":"checkpoint"}`)},
		Metadata: map[string]string{},
	}
	first, err := store.WriteConversationEvidence(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	base.TenantID = "tenant-b"
	second, err := store.WriteConversationEvidence(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if first.ObjectKey == second.ObjectKey ||
		!strings.Contains(first.ObjectKey, "/tenants/tenant-a/") ||
		!strings.Contains(second.ObjectKey, "/tenants/tenant-b/") {
		t.Fatalf("evidence scopes collided: first=%+v second=%+v", first, second)
	}
}

func evidenceStore(t *testing.T, root string) *EvidenceStore {
	t.Helper()
	scanner, err := secretcheck.New([]secretcheck.KnownToken{
		{Label: "provider-key", Value: "known-provider-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewEvidenceStore(root, scanner, func() time.Time {
		return time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
