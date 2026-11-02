package litellm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
)

//go:embed testdata/assistant_decision_provider_schema.json
var expectedAssistantDecisionProviderSchema []byte

func TestClientUsesOneFixedProfileAndDecodesTypedDecision(t *testing.T) {
	decision := validInformationalDecision(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != completionsPath {
			t.Fatalf("LiteLLM request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer proxy-secret" ||
			request.Header.Get("X-Switchback-Assistant-Run-ID") != "run-1" {
			t.Fatal("LiteLLM request authentication or Run identity header mismatch")
		}
		var rawPayload map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&rawPayload); err != nil {
			t.Fatal(err)
		}
		if got := sortedJSONKeys(rawPayload); !slices.Equal([]string{"messages", "model", "response_format", "temperature"}, got) {
			t.Fatalf("Kimi payload keys mismatch: got %v, want [messages model response_format temperature]", got)
		}
		if string(rawPayload["temperature"]) != "1" {
			t.Fatalf("temperature = %s, want 1", rawPayload["temperature"])
		}
		var responseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if err := json.Unmarshal(rawPayload["response_format"], &responseFormat); err != nil {
			t.Fatal(err)
		}
		if responseFormat.Type != "json_schema" ||
			responseFormat.JSONSchema.Name != "assistant_decision_v1" ||
			!responseFormat.JSONSchema.Strict {
			t.Fatalf("unexpected response format metadata: %#v", responseFormat)
		}
		wantSchema := normalizedJSON(t, expectedAssistantDecisionProviderSchema)
		if got := normalizedJSON(t, responseFormat.JSONSchema.Schema); !bytes.Equal(got, wantSchema) {
			t.Fatalf("outbound schema differs from independent fixture:\ngot  %s\nwant %s", got, wantSchema)
		}
		if owner := normalizedJSON(t, contractsv1.AssistantDecisionProviderSchema()); !bytes.Equal(owner, wantSchema) {
			t.Fatalf("contract owner schema differs from independent fixture:\ngot  %s\nwant %s", owner, wantSchema)
		}
		var payload completionRequest
		if err := json.Unmarshal(mustMarshal(t, rawPayload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "switchback-assistant-kimi-code" || len(payload.Messages) != 2 ||
			!strings.Contains(payload.Messages[0].Content, "MUST be a JSON array") ||
			!strings.Contains(payload.Messages[0].Content, `"dimension":"goal"`) ||
			!strings.Contains(payload.Messages[0].Content, `"clarification_questions":1`) ||
			!strings.Contains(payload.Messages[0].Content, "lack of repository body is not a blocker") ||
			!strings.Contains(payload.Messages[0].Content, `"task_spec_digest":"sha256:000000`) ||
			!strings.Contains(payload.Messages[0].Content, `"constraints":["..."]`) {
			t.Fatalf("unexpected completion payload: %+v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("X-Request-ID", "provider-request-1")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "completion-1", "model": "kimi-k2.5",
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": string(decision),
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20},
		})
	}))
	defer server.Close()
	profile := assistantProfile(t)
	client, err := NewClient(ClientOptions{
		BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Complete(context.Background(), modelRequest(profile))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || result.Decision.DecisionType != "informational" ||
		result.ProviderRequestID != "provider-request-1" || result.Model != "kimi-k2.5" ||
		result.InputTokens != 10 || result.OutputTokens != 20 {
		t.Fatalf("unexpected model result: calls=%d result=%+v", calls.Load(), result)
	}
	if strings.Contains(fmt.Sprintf("%+v", client), "proxy-secret") {
		t.Fatal("LiteLLM client formatting exposed its API key")
	}
}

func TestClientOmitsUndeclaredRequestPolicy(t *testing.T) {
	decision := validInformationalDecision(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if got := sortedJSONKeys(payload); !slices.Equal([]string{"messages", "model"}, got) {
			t.Fatalf("DeepSeek payload keys mismatch: got %v, want [messages model]", got)
		}
		if _, ok := payload["temperature"]; ok {
			t.Fatalf("DeepSeek payload must omit temperature: %s", payload["temperature"])
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "completion-1", "model": "deepseek-v4-pro",
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": string(decision),
			}}},
		})
	}))
	defer server.Close()
	profile, ok := config.ProductionProfileRegistry().Assistant(config.DeepSeekAssistantOpenAIV1)
	if !ok {
		t.Fatal("DeepSeek Assistant profile missing")
	}
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), modelRequest(profile)); err != nil {
		t.Fatal(err)
	}
}

func TestNewClientRejectsInvalidRequestPolicyBeforeHTTP(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.AssistantModelProfile)
	}{
		{name: "temperature", mutate: func(profile *config.AssistantModelProfile) {
			temperature := math.NaN()
			profile.RequestPolicy = &config.AssistantModelRequestPolicy{Temperature: &temperature}
		}},
		{name: "structured output mode", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.Mode = "json_object"
		}},
		{name: "decision schema", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.DecisionSchema = "assistant.decision.v0"
		}},
		{name: "provider schema id", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.ProviderSchemaID = "caller-schema"
		}},
		{name: "provider schema digest", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.ProviderSchemaDigest = "sha256:caller"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}))
			defer server.Close()
			profile := assistantProfile(t)
			test.mutate(&profile)
			if _, err := NewClient(ClientOptions{
				BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile,
			}); err == nil {
				t.Fatal("NewClient accepted invalid caller-constructed request policy")
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid policy reached HTTP %d times", requests.Load())
			}
		})
	}
}

// This processor seam lives beside the concrete client to avoid an application
// package test import cycle while exercising the actual profile guard.
func TestAssistantRunProcessorFailsClosedForRetiredKimiProfileRun(t *testing.T) {
	currentProfile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("current Kimi Code Assistant profile missing")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "proxy-secret", Profile: currentProfile})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	claim := retiredKimiRunClaim(now)
	processor, runs, model, effects := newRetiredKimiRunProcessor(t, client, now)
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || !errors.Is(model.err, interactionapp.ErrModelPolicyConflict) || requests.Load() != 0 {
		t.Fatalf("retired Kimi profile must fail at the real client guard: model=%+v requests=%d", model, requests.Load())
	}
	if model.request.ProfileID != config.KimiCodeAssistantOpenAIV1 || model.request.ProfileRevision != 1 ||
		model.request.ProfileDigest != "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c" ||
		claim.Run.ModelProfile != config.KimiCodeAssistantOpenAIV1 || claim.ModelProfileRevision != 1 ||
		claim.ModelProfileDigest != "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c" {
		t.Fatalf("retired profile evidence was changed: request=%+v claim=%+v", model.request, claim)
	}
	if runs.failure == nil || runs.failure.ConversationID != claim.Conversation.ID ||
		runs.failure.RunID != claim.Run.ID || runs.failure.ExpectedConversationRevision != claim.Conversation.Revision ||
		runs.failure.ExpectedRunRevision != claim.Run.Revision+1 ||
		runs.failure.FencingToken != claim.Run.FencingToken ||
		runs.failure.ErrorCode != interactionapp.AssistantFailureModelPolicy || !runs.failure.Retryable ||
		runs.failure.Event.EventType != "AssistantRunFailed.v1" {
		t.Fatalf("terminal failure commit mismatch: %+v", runs.failure)
	}
	var failurePayload map[string]string
	if err := json.Unmarshal(runs.failure.Event.Payload, &failurePayload); err != nil ||
		!slices.Equal(sortedStringKeys(failurePayload), []string{"assistant_run_id", "error_code"}) ||
		failurePayload["assistant_run_id"] != claim.Run.ID || failurePayload["error_code"] != interactionapp.AssistantFailureModelPolicy {
		t.Fatalf("failure event is not sanitized: payload=%s error=%v", runs.failure.Event.Payload, err)
	}
	// CommitDecision is this processor seam's only persistence command that carries
	// readiness or a TaskDraft. With it unreachable, downstream intent, Task,
	// WorkerRun, and fallback creation have no path from this claimed Run.
	if runs.decisionCalls != 0 || runs.decision != nil {
		t.Fatalf("policy conflict reached decision authority: %+v", runs.decision)
	}
	if len(effects.records) != 1 || effects.records[0].ObservabilityStatus != interactionapp.DerivedEffectDisabled ||
		effects.records[0].ObservabilityErrorCode != "" || effects.records[0].LangfuseTraceID != "" {
		t.Fatalf("retired Kimi failure must not persist provider detail: %+v", effects.records)
	}
}

func TestAssistantRunProcessorFailsClosedForDomainInvalidProviderDecision(t *testing.T) {
	profile := assistantProfile(t)
	decision := validInformationalDecision(t)
	var proposal map[string]any
	if err := json.Unmarshal(decision, &proposal); err != nil {
		t.Fatal(err)
	}
	proposal["readiness"].(map[string]any)["policy_revision"] = "caller-policy"
	decision, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contractsv1.DecodeAssistantDecision(decision); err != nil {
		t.Fatalf("fixture must cross the strict decoder before application validation: %v", err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		assertStrictResponseFormat(t, request)
		_, _ = response.Write([]byte(completionEnvelope(string(decision))))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	claim := retiredKimiRunClaim(now)
	claim.ModelProfileRevision = profile.Revision
	claim.ModelProfileDigest = profile.Digest
	processor, runs, model, _ := newRetiredKimiRunProcessor(t, client, now)
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || model.calls != 1 || model.err != nil ||
		runs.decisionCalls != 0 || runs.failure == nil ||
		runs.failure.ErrorCode != interactionapp.AssistantFailureModelPolicy ||
		!runs.failure.Retryable {
		t.Fatalf("domain-invalid decision did not converge once: requests=%d model=%+v failure=%+v",
			requests.Load(), model, runs.failure)
	}
	if strings.Contains(string(runs.failure.Event.Payload), "caller-policy") ||
		strings.Contains(string(runs.failure.Event.Payload), string(decision)) {
		t.Fatalf("domain-invalid failure persisted provider content: %s", runs.failure.Event.Payload)
	}
}

func TestAssistantRunProcessorCarriesPolicyRevisionInAuditedModelContext(t *testing.T) {
	profile := assistantProfile(t)
	decision := validInformationalDecision(t)
	var modelVisibleContext map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload completionRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if len(payload.Messages) != 2 {
			t.Errorf("completion messages = %d, want 2", len(payload.Messages))
			return
		}
		if err := json.Unmarshal([]byte(payload.Messages[1].Content), &modelVisibleContext); err != nil {
			t.Errorf("decode model-visible ContextPack: %v", err)
			return
		}
		_, _ = response.Write([]byte(completionEnvelope(string(decision))))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile})
	if err != nil {
		t.Fatal(err)
	}

	// The processor passes TimeoutAt to a real HTTP context deadline.
	now := time.Now().UTC()
	claim := retiredKimiRunClaim(now)
	claim.ModelProfileRevision = profile.Revision
	claim.ModelProfileDigest = profile.Digest
	processor, _, model, _ := newRetiredKimiRunProcessor(t, client, now)
	contexts := processor.Context.Repository.(*retiredKimiContextRecorder)
	if err := processor.ProcessAssistantRun(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if model.err != nil {
		t.Fatalf("model request failed before audited context inspection: %v", model.err)
	}

	if model.calls != 1 || len(contexts.packs) != 1 {
		t.Fatalf("processor model/persistence calls = %d/%d, want 1/1", model.calls, len(contexts.packs))
	}
	if model.request.PolicyRevision != claim.Run.PolicyRevision {
		t.Fatalf("ModelRequest policy revision = %q, want %q",
			model.request.PolicyRevision, claim.Run.PolicyRevision)
	}
	var persistedContext map[string]json.RawMessage
	if encoded, marshalErr := json.Marshal(contexts.packs[0]); marshalErr != nil {
		t.Fatal(marshalErr)
	} else if unmarshalErr := json.Unmarshal(encoded, &persistedContext); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	for _, boundary := range []struct {
		name        string
		contextPack map[string]json.RawMessage
	}{
		{name: "model-visible", contextPack: modelVisibleContext},
		{name: "persisted", contextPack: persistedContext},
	} {
		var policyRevision string
		if err := json.Unmarshal(boundary.contextPack["policy_revision"], &policyRevision); err != nil {
			t.Fatalf("%s ContextPack policy_revision is absent or invalid: %v", boundary.name, err)
		}
		if policyRevision != claim.Run.PolicyRevision {
			t.Fatalf("%s ContextPack policy_revision = %q, want %q",
				boundary.name, policyRevision, claim.Run.PolicyRevision)
		}
	}
}

type retiredKimiRunRecorder struct {
	invocationCalls int
	decisionCalls   int
	decision        *interactionapp.RunDecisionCommit
	failure         *interactionapp.RunFailureCommit
}

func (*retiredKimiRunRecorder) ClaimNextRun(context.Context, string, time.Duration, time.Time) (interactionapp.RunClaim, error) {
	return interactionapp.RunClaim{}, interactionapp.ErrNoQueuedRun
}
func (*retiredKimiRunRecorder) LoadRun(context.Context, string) (domain.AssistantRun, error) {
	return domain.AssistantRun{}, nil
}
func (runs *retiredKimiRunRecorder) MarkInvocationStarted(_ context.Context, _ string, revision, _ uint64, _ time.Time) (uint64, error) {
	runs.invocationCalls++
	return revision + 1, nil
}
func (runs *retiredKimiRunRecorder) CommitDecision(_ context.Context, commit interactionapp.RunDecisionCommit) error {
	runs.decisionCalls++
	runs.decision = &commit
	return nil
}
func (runs *retiredKimiRunRecorder) CommitFailure(_ context.Context, commit interactionapp.RunFailureCommit) error {
	runs.failure = &commit
	return nil
}
func (*retiredKimiRunRecorder) RequeueExpired(context.Context, string, uint64, uint64, time.Time) error {
	return nil
}
func (*retiredKimiRunRecorder) RecoverExpiredClaims(context.Context, time.Time) (interactionapp.RunRecoveryResult, error) {
	return interactionapp.RunRecoveryResult{}, nil
}

type retiredKimiContextRecorder struct{ packs []interactionapp.ContextPack }

func (*retiredKimiContextRecorder) LoadContextCandidates(context.Context, interactionapp.RunClaim) ([]interactionapp.ContextItem, error) {
	return []interactionapp.ContextItem{{
		Ref: "turn/user-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 1,
		Content: json.RawMessage(`{"content":"build it"}`),
	}}, nil
}
func (contexts *retiredKimiContextRecorder) SaveContextPack(_ context.Context, pack interactionapp.ContextPack) error {
	contexts.packs = append(contexts.packs, pack)
	return nil
}

type retiredKimiDrafts struct{}

func (retiredKimiDrafts) LoadCurrentTaskDraft(context.Context, string) (*interactionapp.TaskDraftRecord, error) {
	return nil, nil
}

type retiredKimiEffects struct {
	records []interactionapp.DerivedEffectRecord
}

func (effects *retiredKimiEffects) RecordAssistantDerivedEffects(_ context.Context, record interactionapp.DerivedEffectRecord) error {
	effects.records = append(effects.records, record)
	return nil
}

type retiredKimiIDs struct{ next int }

func (ids *retiredKimiIDs) NewID(kind string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", kind, ids.next), nil
}

type retiredKimiClock struct{ now time.Time }

func (clock retiredKimiClock) Now() time.Time { return clock.now }

type recordingAssistantModel struct {
	delegate interactionapp.AssistantModel
	request  interactionapp.ModelRequest
	err      error
	calls    int
}

func (model *recordingAssistantModel) Complete(ctx context.Context, request interactionapp.ModelRequest) (interactionapp.ModelResult, error) {
	model.calls++
	model.request = request
	result, err := model.delegate.Complete(ctx, request)
	model.err = err
	return result, err
}

func retiredKimiRunClaim(now time.Time) interactionapp.RunClaim {
	return interactionapp.RunClaim{
		Conversation: domain.Conversation{ID: "conversation-1", TenantID: "tenant-local", ActorUserID: "user-local", Generation: 1, Revision: 5},
		Run: domain.AssistantRun{ID: "run-1", ConversationID: "conversation-1", ModelProfile: config.KimiCodeAssistantOpenAIV1,
			PolicyRevision: "assistant-readiness-v1", Revision: 2, FencingToken: 7, TimeoutAt: now.Add(time.Minute), LeaseExpiresAt: now.Add(time.Minute)},
		ModelProfileRevision: 1, ModelProfileDigest: "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
	}
}

func newRetiredKimiRunProcessor(t *testing.T, client interactionapp.AssistantModel, now time.Time) (interactionapp.AssistantRunProcessor, *retiredKimiRunRecorder, *recordingAssistantModel, *retiredKimiEffects) {
	t.Helper()
	ids := &retiredKimiIDs{}
	clock := retiredKimiClock{now: now}
	runs := &retiredKimiRunRecorder{}
	contexts := &retiredKimiContextRecorder{}
	effects := &retiredKimiEffects{}
	model := &recordingAssistantModel{delegate: client}
	policy := interactionapp.ReadinessPolicy{
		Revision: "assistant-readiness-v1",
		AssumableDimensions: map[domain.ReadinessDimension]bool{
			domain.DimensionConstraints: true,
		},
	}
	return interactionapp.AssistantRunProcessor{
		Runs:           runs,
		Context:        interactionapp.ContextCompiler{Repository: contexts, IDs: ids, Budget: 1024},
		CurrentDrafts:  retiredKimiDrafts{},
		Model:          model,
		Validator:      interactionapp.DecisionValidator{IDs: ids, Clock: clock, Policy: policy},
		DraftBuilder:   interactionapp.TaskDraftBuilder{IDs: ids, Clock: clock},
		Clarifications: interactionapp.ClarificationPlanner{IDs: ids, Clock: clock},
		Effects:        interactionapp.PublishAssistantDerivedEffects{Store: effects, Clock: clock},
		IDs:            ids,
		Clock:          clock,
	}, runs, model, effects
}

func TestClientFailsClosedWithoutProviderFallbackOrRawBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "malformed envelope", status: http.StatusOK, body: `{"secret":"raw-upstream-secret"}`, want: interactionapp.ErrModelMalformed},
		{name: "non JSON content", status: http.StatusOK, body: completionEnvelope("not-json"), want: interactionapp.ErrModelMalformed},
		{name: "schema invalid content", status: http.StatusOK, body: completionEnvelope(schemaInvalidDecision(t)), want: interactionapp.ErrModelMalformed},
		{name: "upstream", status: http.StatusBadGateway, body: `raw-upstream-secret`, want: interactionapp.ErrModelUpstream},
		{name: "timeout status", status: http.StatusGatewayTimeout, body: `raw-upstream-secret`, want: interactionapp.ErrModelTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				assertStrictResponseFormat(t, request)
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			profile := assistantProfile(t)
			client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Complete(context.Background(), modelRequest(profile))
			if !errors.Is(err, test.want) || calls.Load() != 1 || strings.Contains(fmt.Sprint(err), "raw-upstream-secret") {
				t.Fatalf("failure mapping: calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func completionEnvelope(content string) string {
	encoded, _ := json.Marshal(map[string]any{
		"id": "completion-1",
		"choices": []any{map[string]any{"message": map[string]string{
			"role": "assistant", "content": content,
		}}},
	})
	return string(encoded)
}

func schemaInvalidDecision(t *testing.T) string {
	t.Helper()
	var decision map[string]any
	if err := json.Unmarshal(validInformationalDecision(t), &decision); err != nil {
		t.Fatal(err)
	}
	decision["unexpected"] = "raw-upstream-secret"
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertStrictResponseFormat(t *testing.T, request *http.Request) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	var responseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Strict bool `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(payload["response_format"], &responseFormat); err != nil {
		t.Fatal(err)
	}
	if responseFormat.Type != "json_schema" || !responseFormat.JSONSchema.Strict {
		t.Fatalf("failed Kimi request removed strict response format: %#v", responseFormat)
	}
}

func TestClientMapsRequestDeadlineAndProfileMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	profile := assistantProfile(t)
	client, err := NewClient(ClientOptions{
		BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile,
		HTTPClient: &http.Client{Timeout: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), modelRequest(profile)); !errors.Is(err, interactionapp.ErrModelTimeout) {
		t.Fatalf("timeout mapping = %v", err)
	}
	request := modelRequest(profile)
	request.ProfileDigest = "sha256:caller"
	if _, err := client.Complete(context.Background(), request); !errors.Is(err, interactionapp.ErrModelPolicyConflict) {
		t.Fatalf("profile mismatch = %v", err)
	}
}

func TestClientRejectsPolicyRevisionConflictsBeforeHTTP(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*interactionapp.ModelRequest)
	}{
		{
			name: "empty request policy revision",
			mutate: func(request *interactionapp.ModelRequest) {
				request.PolicyRevision = ""
			},
		},
		{
			name: "empty ContextPack policy revision",
			mutate: func(request *interactionapp.ModelRequest) {
				request.Context.PolicyRevision = ""
			},
		},
		{
			name: "mismatched request and ContextPack policy revisions",
			mutate: func(request *interactionapp.ModelRequest) {
				request.Context.PolicyRevision = "assistant-readiness-v2"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}))
			defer server.Close()
			profile := assistantProfile(t)
			client, err := NewClient(ClientOptions{
				BaseURL: server.URL, APIKey: "proxy-secret", Profile: profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := modelRequest(profile)
			test.mutate(&request)
			if _, err := client.Complete(context.Background(), request); !errors.Is(err, interactionapp.ErrModelPolicyConflict) {
				t.Fatalf("policy conflict = %v, want %v", err, interactionapp.ErrModelPolicyConflict)
			}
			if requests.Load() != 0 {
				t.Fatalf("policy conflict reached HTTP %d times", requests.Load())
			}
		})
	}
}

func assistantProfile(t *testing.T) config.AssistantModelProfile {
	t.Helper()
	profile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Assistant profile missing")
	}
	return profile
}

func modelRequest(profile config.AssistantModelProfile) interactionapp.ModelRequest {
	return interactionapp.ModelRequest{
		RunID: "run-1", ConversationID: "conversation-1",
		ProfileID: profile.ID, ProfileRevision: profile.Revision, ProfileDigest: profile.Digest,
		PolicyRevision: "assistant-readiness-v1",
		Context: interactionapp.ContextPack{
			SchemaVersion: interactionapp.ContextPackSchemaVersion, ContextPackID: "context-1",
			AssistantRunID: "run-1", ConversationID: "conversation-1",
			PolicyRevision: "assistant-readiness-v1",
			Selected:       []interactionapp.ContextItem{}, Dropped: []interactionapp.DroppedContextItem{},
			BudgetTokens: 8192, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
}

func sortedJSONKeys(payload map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(payload map[string]string) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func normalizedJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func validInformationalDecision(t *testing.T) []byte {
	t.Helper()
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range []string{
		"goal", "deliverable_acceptance", "scope_target", "constraints",
		"context_access", "risk_approval", "execution_policy",
	} {
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{
			Dimension: dimension, Status: "known", Summary: "known", EvidenceRefs: []string{},
		})
	}
	encoded, err := json.Marshal(contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "informational",
		UserVisibleContent: "No task is required.",
		Readiness: contractsv1.AssistantReadinessAssessment{
			SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
			AssessmentID:  "assessment-1", Revision: 1, PolicyRevision: "readiness-v1",
			Dimensions: dimensions, BlockingUnknowns: []string{}, Assumptions: []string{},
			Status: "ready_confirmed", EvaluatedAt: now,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
