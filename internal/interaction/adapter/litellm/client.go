// Package litellm implements the server-owned Assistant model adapter.
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/platform/config"
)

const (
	completionsPath      = "/v1/chat/completions"
	maxResponseBytes     = 2 << 20
	defaultTimeout       = 90 * time.Second
	decisionInstructions = `Return exactly one JSON object and no Markdown.
Use all top-level fields: schema_version, decision_type, user_visible_content, clarification_question, readiness, task_draft, checkpoint.
schema_version is "assistant.decision.v1". decision_type is clarification, ready_draft, or informational.
readiness uses this exact scalar shape: {"schema_version":"assistant.readiness_assessment.v1","assessment_id":"model-proposal","revision":1,"policy_revision":"<exact policy revision from context>","dimensions":[],"blocking_unknowns":["goal"],"assumptions":[],"clarification_questions":1,"status":"clarifying","evaluated_at":"2026-01-01T00:00:00Z"}. clarification_questions is a non-negative integer count, never an array. blocking_unknowns and assumptions are arrays of strings.
readiness.dimensions MUST be a JSON array, never an object keyed by dimension. It contains exactly seven objects shaped {"dimension":"goal","status":"known","summary":"...","evidence_refs":[]} for goal, deliverable_acceptance, scope_target, constraints, context_access, risk_approval, execution_policy exactly once. Status is known, assumed, blocking_unknown, or not_applicable. Summary is non-empty. evidence_refs contains only exact selected context refs or is an empty array.
When selected context contains an authorized workspace item with workspace_id, workspace_revision_id, resolution_id, resolution_revision, and membership_revision, context_access is known for dispatch. The Assistant intentionally has no repository contents; lack of repository body is not a blocker because the Worker inspects the isolated WorkingCopy.
For clarification: ask exactly one highest-priority blocking question, set clarification_question to it, readiness status clarifying, task_draft null, checkpoint null.
For ready_draft: clarification_question is empty, readiness is ready_confirmed or ready_with_assumptions with no blocking_unknowns, checkpoint null, and task_draft uses this exact shape: {"schema_version":"assistant.task_draft.v1","task_draft_id":"model-draft","revision":1,"initiator_user_id":"model-user","conversation_id":"model-conversation","workspace_id":"<exact workspace id from context>","workspace_revision_id":"<exact workspace revision id from context>","readiness_assessment_id":"model-proposal","task_spec_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","title":"...","goal":"...","target":{"kind":"workspace","ref":"<exact workspace revision id from context>"},"constraints":["..."],"acceptance_criteria":["..."],"assumptions":[],"task_spec":{},"created_at":"2026-01-01T00:00:00Z"}. constraints, acceptance_criteria, and assumptions are arrays of strings. Use task_spec_digest exactly; never add a digest alias.
Server code replaces all placeholder identity, revision, timestamp, digest, Workspace authority, runtime, and TaskSpec fields. Never include tools, commands, credentials, hidden reasoning, provider payloads, or additional properties.`
)

type ClientOptions struct {
	BaseURL    string
	APIKey     string
	Profile    config.AssistantModelProfile
	HTTPClient *http.Client
}

// assistantRoute is the immutable provider-facing projection of one validated
// server-owned profile. The client never retains the mutable profile policy.
type assistantRoute struct {
	profile        config.AssistantModelProfileRef
	model          string
	temperature    *float64
	responseFormat *completionResponseFormat
}

func projectAssistantRoute(profile config.AssistantModelProfile) (assistantRoute, error) {
	if err := profile.ValidateDigest(); err != nil ||
		profile.LiteLLMModel == "" ||
		profile.ResponseSchema != contractsv1.AssistantDecisionSchemaVersion ||
		profile.AutomaticFallbackProfileID != "" {
		return assistantRoute{}, fmt.Errorf("Assistant LiteLLM model profile is invalid")
	}
	responseFormat, err := requestResponseFormat(profile)
	if err != nil {
		return assistantRoute{}, fmt.Errorf("Assistant LiteLLM model profile is invalid")
	}
	return assistantRoute{
		profile:        profile.Ref(),
		model:          profile.LiteLLMModel,
		temperature:    requestTemperature(profile),
		responseFormat: responseFormat,
	}, nil
}

func (route assistantRoute) matches(request interactionapp.ModelRequest) bool {
	return request.ProfileID == route.profile.ID &&
		request.ProfileRevision == route.profile.Revision &&
		request.ProfileDigest == route.profile.Digest
}

type Client struct {
	baseURL    *url.URL
	apiKey     string
	route      assistantRoute
	httpClient *http.Client
}

func NewClient(options ClientOptions) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" ||
		(baseURL.Path != "" && baseURL.Path != "/") {
		return nil, fmt.Errorf("Assistant LiteLLM base URL must be one HTTP(S) origin")
	}
	if options.APIKey == "" || strings.ContainsAny(options.APIKey, "\r\n\x00") {
		return nil, fmt.Errorf("Assistant LiteLLM API key is required")
	}
	route, err := projectAssistantRoute(options.Profile)
	if err != nil {
		return nil, err
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	baseURL.Path = ""
	return &Client{
		baseURL: baseURL, apiKey: options.APIKey, route: route,
		httpClient: httpClient,
	}, nil
}

func (client *Client) String() string {
	if client == nil {
		return "litellm.Client<nil>"
	}
	return fmt.Sprintf("litellm.Client{BaseURL:%q APIKey:[REDACTED] Profile:%q}", client.baseURL, client.route.profile.ID)
}

func (client *Client) GoString() string { return client.String() }

type completionRequest struct {
	Model          string                    `json:"model"`
	Messages       []completionMessage       `json:"messages"`
	Temperature    *float64                  `json:"temperature,omitempty"`
	ResponseFormat *completionResponseFormat `json:"response_format,omitempty"`
}

type completionResponseFormat struct {
	Type       string                       `json:"type"`
	JSONSchema completionResponseJSONSchema `json:"json_schema"`
}

type completionResponseJSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type completionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message completionMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (client *Client) Complete(
	ctx context.Context,
	request interactionapp.ModelRequest,
) (interactionapp.ModelResult, error) {
	if client == nil || request.RunID == "" || request.ConversationID == "" ||
		!client.route.matches(request) ||
		request.PolicyRevision == "" || request.Context.PolicyRevision != request.PolicyRevision ||
		request.Context.SchemaVersion != interactionapp.ContextPackSchemaVersion {
		return interactionapp.ModelResult{}, interactionapp.ErrModelPolicyConflict
	}
	contextJSON, err := json.Marshal(request.Context)
	if err != nil {
		return interactionapp.ModelResult{}, interactionapp.ErrModelMalformed
	}
	payload, err := json.Marshal(completionRequest{
		Model: client.route.model,
		Messages: []completionMessage{
			{
				Role:    "system",
				Content: decisionInstructions,
			},
			{Role: "user", Content: string(contextJSON)},
		},
		Temperature:    client.route.temperature,
		ResponseFormat: client.route.responseFormat,
	})
	if err != nil {
		return interactionapp.ModelResult{}, interactionapp.ErrModelMalformed
	}
	endpoint := *client.baseURL
	endpoint.Path = completionsPath
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return interactionapp.ModelResult{}, interactionapp.ErrModelUpstream
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("X-Switchback-Assistant-Run-ID", request.RunID)
	startedAt := time.Now().UTC()
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return interactionapp.ModelResult{}, interactionapp.ErrModelTimeout
		}
		return interactionapp.ModelResult{}, interactionapp.ErrModelUpstream
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil || len(body) > maxResponseBytes {
		return interactionapp.ModelResult{}, interactionapp.ErrModelMalformed
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusGatewayTimeout {
		return interactionapp.ModelResult{}, interactionapp.ErrModelTimeout
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return interactionapp.ModelResult{}, interactionapp.ErrModelUpstream
	}
	var completion completionResponse
	if err := json.Unmarshal(body, &completion); err != nil || len(completion.Choices) != 1 ||
		completion.Choices[0].Message.Role != "assistant" ||
		strings.TrimSpace(completion.Choices[0].Message.Content) != completion.Choices[0].Message.Content {
		return interactionapp.ModelResult{}, interactionapp.ErrModelMalformed
	}
	decision, err := contractsv1.DecodeAssistantDecision([]byte(completion.Choices[0].Message.Content))
	if err != nil {
		return interactionapp.ModelResult{}, interactionapp.ErrModelMalformed
	}
	providerRequestID := response.Header.Get("X-Request-ID")
	if providerRequestID == "" {
		providerRequestID = completion.ID
	}
	return interactionapp.ModelResult{
		Decision: decision, ProviderRequestID: providerRequestID, Model: completion.Model,
		InputTokens: completion.Usage.PromptTokens, OutputTokens: completion.Usage.CompletionTokens,
		StartedAt: startedAt, CompletedAt: time.Now().UTC(),
	}, nil
}

func requestTemperature(profile config.AssistantModelProfile) *float64 {
	if profile.RequestPolicy == nil || profile.RequestPolicy.Temperature == nil {
		return nil
	}
	value := *profile.RequestPolicy.Temperature
	return &value
}

func requestResponseFormat(profile config.AssistantModelProfile) (*completionResponseFormat, error) {
	if profile.RequestPolicy == nil || profile.RequestPolicy.StructuredOutput == nil {
		return nil, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(contractsv1.AssistantDecisionProviderSchema(), &schema); err != nil {
		return nil, err
	}
	return &completionResponseFormat{
		Type: "json_schema",
		JSONSchema: completionResponseJSONSchema{
			Name:   "assistant_decision_v1",
			Strict: true,
			Schema: schema,
		},
	}, nil
}

var _ interactionapp.AssistantModel = (*Client)(nil)
