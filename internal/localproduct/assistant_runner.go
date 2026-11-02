package localproduct

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionlangfuse "github.com/abietic/switchback/internal/interaction/adapter/langfuse"
	interactionlitellm "github.com/abietic/switchback/internal/interaction/adapter/litellm"
	interactionlocal "github.com/abietic/switchback/internal/interaction/adapter/localfile"
	interactionpostgres "github.com/abietic/switchback/internal/interaction/adapter/postgres"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

const assistantPollInterval = 250 * time.Millisecond

type assistantRunner struct {
	dispatcher interactionapp.AssistantRunDispatcher
}

func newAssistantRunner(
	configuration Config,
	pool *pgxpool.Pool,
	profiles config.ProfileRegistry,
	ids randomIDs,
	now func() time.Time,
) (*assistantRunner, error) {
	if pool == nil || now == nil || !configuration.Assistant.Enabled {
		return nil, fmt.Errorf("Assistant runner configuration is incomplete")
	}
	assistantProfile, err := profiles.ResolveAssistant(configuration.Assistant.ModelProfile)
	if err != nil {
		return nil, err
	}
	workerProfile, ok := profiles.Agent(config.DeepSeekCodexOpenAIV1)
	if !ok {
		return nil, fmt.Errorf("Assistant default Worker AgentProfile is unavailable")
	}
	model, err := interactionlitellm.NewClient(interactionlitellm.ClientOptions{
		BaseURL: configuration.AssistantLiteLLMBaseURL,
		APIKey:  configuration.AssistantLiteLLMAPIKey,
		Profile: assistantProfile,
		HTTPClient: &http.Client{
			Timeout: configuration.Assistant.RequestTimeout,
		},
	})
	if err != nil {
		return nil, err
	}
	knownTokens, err := newAssistantKnownTokenScanner(configuration)
	if err != nil {
		return nil, err
	}
	objects, err := interactionlocal.NewEvidenceStore(configuration.DataRoot, knownTokens, now)
	if err != nil {
		return nil, err
	}
	var observability interactionapp.ObservabilityPort
	if enabled, code := configuration.observabilityConfiguration(); enabled {
		client, err := artifactlangfuse.NewClient(artifactlangfuse.ClientOptions{
			BaseURL: configuration.LangfuseBaseURL, PublicKey: configuration.LangfusePublicKey,
			SecretKey: configuration.LangfuseSecretKey,
		})
		if err == nil {
			observability, err = interactionlangfuse.NewExporter(client, knownTokens)
		}
		if err != nil {
			slog.Warn("observability_config_invalid")
			observability = nil
		}
	} else if code != "" {
		slog.Warn(code)
	}
	clock := assistantClock{now: now}
	store := interactionpostgres.Store{
		Pool: pool, MaxRecentTurns: configuration.Assistant.MaxRecentTurns,
	}
	effects := interactionapp.PublishAssistantDerivedEffects{
		Objects: objects, Observability: observability,
		ObservabilityEnabled: observability != nil, Store: store, Clock: clock,
	}
	processor := interactionapp.AssistantRunProcessor{
		Runs:          store,
		Context:       interactionapp.ContextCompiler{Repository: store, IDs: ids, Budget: configuration.Assistant.ContextBudgetTokens},
		CurrentDrafts: store,
		Model:         model,
		Validator: interactionapp.DecisionValidator{
			IDs: ids, Clock: clock,
			Policy: interactionapp.ReadinessPolicy{
				Revision: configuration.Assistant.ReadinessPolicyRevision,
				AssumableDimensions: map[domain.ReadinessDimension]bool{
					domain.DimensionConstraints: true,
				},
			},
		},
		DraftBuilder: interactionapp.TaskDraftBuilder{
			IDs: ids, Clock: clock,
			Policy: interactionapp.TaskDraftPolicy{
				ProjectID:             configuration.ProjectID,
				TriggerPolicyRevision: configuration.Assistant.TriggerPolicyRevision,
				NetworkProfile:        configuration.NetworkProfile,
				AgentProfile:          workerProfile,
				UserVisibleTypes:      []string{"report", "patch"},
				ContextRefs: []contractsv1.ContextRef{{
					Kind: "workspace_revision", Ref: configuration.WorkspaceRevisionID,
				}},
			},
		},
		Clarifications: interactionapp.ClarificationPlanner{IDs: ids, Clock: clock},
		Effects:        effects,
		IDs:            ids, Clock: clock,
	}
	return &assistantRunner{dispatcher: interactionapp.AssistantRunDispatcher{
		Runs: store, Processor: processor, Clock: clock,
		LeaseOwner: "localproduct-assistant", LeaseDuration: configuration.Assistant.RequestTimeout + 30*time.Second,
		RecoveryEffects: &effects,
	}}, nil
}

func (runner *assistantRunner) Run(ctx context.Context) error {
	if runner == nil {
		return nil
	}
	ticker := time.NewTicker(assistantPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := runner.dispatcher.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("dispatch AssistantRun", "error", err)
			}
		}
	}
}

type assistantClock struct {
	now func() time.Time
}

func (clock assistantClock) Now() time.Time { return clock.now().UTC() }

func newAssistantKnownTokenScanner(configuration Config) (*secretcheck.Scanner, error) {
	tokens := make([]secretcheck.KnownToken, 0, 6)
	seen := make(map[string]struct{}, 6)
	appendToken := func(label, value string) {
		if value == "" {
			return
		}
		if _, duplicate := seen[value]; duplicate {
			return
		}
		seen[value] = struct{}{}
		tokens = append(tokens, secretcheck.KnownToken{Label: label, Value: value})
	}
	appendToken("assistant-litellm-proxy-key", configuration.AssistantLiteLLMAPIKey)
	appendToken("langfuse-public-key", configuration.LangfusePublicKey)
	appendToken("langfuse-secret-key", configuration.LangfuseSecretKey)
	for _, candidate := range []struct {
		label string
		path  string
	}{
		{label: "kimi-provider-key", path: configuration.KimiCredentialFile},
		{label: "deepseek-provider-key", path: configuration.DeepSeekCredentialFile},
	} {
		if candidate.path == "" {
			continue
		}
		credential, err := readProviderCredential(candidate.path)
		if err != nil {
			continue
		}
		appendToken(candidate.label, string(credential))
		zero(credential)
	}
	return secretcheck.New(tokens)
}
