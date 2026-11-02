package langfuse

import (
	"context"
	"errors"
	"testing"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

func TestExporterWritesOneSanitizedAssistantTraceSpanAndGeneration(t *testing.T) {
	api := &recordingAPI{}
	scanner := &recordingScanner{}
	exporter, err := NewExporter(api, scanner)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	observation := interactionapp.ModelObservation{
		TenantID: "tenant-local", UserID: "user-local", AssistantRunID: "assistant-run-1",
		ConversationID: "conversation-1", ProfileID: "kimi-assistant-openai-v1",
		ProfileRevision:   1,
		ProfileDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProviderRequestID: "provider-request-1", Model: "kimi/model",
		InputTokens: 12, OutputTokens: 5, StartedAt: now, CompletedAt: now.Add(time.Second),
		Status: "ok",
	}
	if err := exporter.RecordAssistantModel(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if scanner.calls != 1 || len(api.batches) != 1 || len(api.batches[0].Batch) != 3 {
		t.Fatalf("scanner calls=%d batches=%d events=%d",
			scanner.calls, len(api.batches), len(api.batches[0].Batch))
	}
	events := api.batches[0].Batch
	if events[0].Type != "trace-create" || events[1].Type != "span-create" ||
		events[2].Type != "generation-create" ||
		events[0].Body["id"] != "assistant/assistant-run-1" ||
		events[2].Body["model"] != "kimi/model" {
		t.Fatalf("Assistant Langfuse batch = %+v", events)
	}
	if _, exists := events[2].Body["input"]; exists {
		t.Fatal("Assistant Langfuse generation unexpectedly contains raw model input")
	}
	if _, exists := events[2].Body["output"]; exists {
		t.Fatal("Assistant Langfuse generation unexpectedly contains raw provider output")
	}
}

func TestExporterFailsClosedBeforeIngestionWhenKnownTokenScanFails(t *testing.T) {
	api := &recordingAPI{}
	exporter, err := NewExporter(api, &recordingScanner{err: errors.New("known token")})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	err = exporter.RecordAssistantModel(context.Background(), interactionapp.ModelObservation{
		TenantID: "tenant-local", UserID: "user-local", AssistantRunID: "assistant-run-1",
		ConversationID: "conversation-1", ProfileID: "assistant-profile", ProfileRevision: 1,
		ProfileDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProviderRequestID: "provider-request-1", Model: "provider/model",
		StartedAt: now, CompletedAt: now.Add(time.Second), Status: "ok",
	})
	if err == nil || len(api.batches) != 0 {
		t.Fatalf("known-token failure reached Langfuse: batches=%d error=%v", len(api.batches), err)
	}
}

type recordingAPI struct {
	batches []artifactlangfuse.IngestionBatch
}

func (api *recordingAPI) Ingest(_ context.Context, batch artifactlangfuse.IngestionBatch) error {
	api.batches = append(api.batches, batch)
	return nil
}

func (*recordingAPI) GetTrace(context.Context, string) (artifactlangfuse.Trace, error) {
	return artifactlangfuse.Trace{}, nil
}

type recordingScanner struct {
	calls int
	err   error
}

func (scanner *recordingScanner) ScanJSON(context.Context, string, any) error {
	scanner.calls++
	return scanner.err
}
