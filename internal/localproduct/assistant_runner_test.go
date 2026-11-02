package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAssistantRunnerOptionalExporterCannotPreventConstruction(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{"SWITCHBACK_DATABASE_URL": "postgres://localhost/switchback", "SWITCHBACK_DATA_ROOT": t.TempDir(), "SWITCHBACK_M2_ASSISTANT_ENABLED": "1", "SWITCHBACK_ASSISTANT_LITELLM_BASE_URL": "http://litellm:4000", "SWITCHBACK_ASSISTANT_LITELLM_API_KEY": "assistant-model-token", "LANGFUSE_BASE_URL": "not a URL", "LANGFUSE_PUBLIC_KEY": "public-token"}))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), configuration.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := newAssistantRunner(configuration, pool, config.ProductionProfileRegistry(), randomIDs{}, time.Now); err != nil {
		t.Fatalf("optional exporter prevented Assistant startup: %v", err)
	}
	configuration.AssistantLiteLLMBaseURL = "not a URL"
	if _, err := newAssistantRunner(configuration, pool, config.ProductionProfileRegistry(), randomIDs{}, time.Now); err == nil {
		t.Fatal("required model dependency was made optional")
	}
}
