// Package langfuse implements the M1 derived observability projection.
package langfuse

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
)

const (
	ingestionPath         = "/api/public/ingestion"
	tracePathPrefix       = "/api/public/traces/"
	observationPathPrefix = "/api/public/observations/"
	defaultHTTPTimeout    = 10 * time.Second
	maxResponseBytes      = 4 << 20
)

type IngestionEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Body      map[string]any `json:"body"`
}

type IngestionBatch struct {
	Batch []IngestionEvent `json:"batch"`
}

type RuntimeMetadata struct {
	TenantID          string   `json:"tenant_id"`
	UserID            string   `json:"user_id"`
	WorkspaceID       string   `json:"workspace_id"`
	WorkerSessionID   string   `json:"worker_session_id"`
	WorkerRunID       string   `json:"worker_run_id"`
	RuntimeInstanceID string   `json:"runtime_instance_id"`
	RuntimeGeneration uint32   `json:"runtime_generation"`
	AgentSessionID    string   `json:"agent_session_id"`
	AgentTurnIDs      []string `json:"agent_turn_ids"`
	SourceStartedAt   string   `json:"source_started_at,omitempty"`
}

type Observation struct {
	ID                  string           `json:"id"`
	TraceID             string           `json:"traceId"`
	ParentObservationID *string          `json:"parentObservationId"`
	Name                string           `json:"name"`
	Type                string           `json:"type"`
	Metadata            map[string]any   `json:"metadata"`
	Input               json.RawMessage  `json:"input"`
	Output              json.RawMessage  `json:"output"`
	StartTime           time.Time        `json:"startTime"`
	EndTime             time.Time        `json:"endTime"`
	Model               string           `json:"model"`
	UsageDetails        map[string]int64 `json:"usageDetails"`
	Usage               Usage            `json:"usage"`
	Level               string           `json:"level"`
	StatusMessage       string           `json:"statusMessage"`
}

type Usage struct {
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Total  int64  `json:"total"`
	Unit   string `json:"unit"`
}

type Trace struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Timestamp time.Time       `json:"timestamp"`
	Metadata  RuntimeMetadata `json:"metadata"`
	// ExactMetadata preserves the complete readback map for v3 verification.
	// Metadata remains the compatible v2 view.
	ExactMetadata map[string]any  `json:"-"`
	Input         json.RawMessage `json:"input"`
	Output        json.RawMessage `json:"output"`
	Observations  []Observation   `json:"observations"`
}

func (trace *Trace) UnmarshalJSON(raw []byte) error {
	type legacy Trace
	var decoded legacy
	var fields struct {
		*legacy
		Metadata map[string]any `json:"metadata"`
	}
	fields.legacy = &decoded
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	metadata, _ := json.Marshal(fields.Metadata)
	if err := json.Unmarshal(metadata, &decoded.Metadata); err != nil {
		// Keep malformed v3 identity types visible to exact collision detection.
		if _, v3 := fields.Metadata["projection_version"]; !v3 {
			return err
		}
	}
	*trace = Trace(decoded)
	trace.ExactMetadata = fields.Metadata
	return nil
}

type API interface {
	Ingest(context.Context, IngestionBatch) error
	GetTrace(context.Context, string) (Trace, error)
}

type DurableAPI interface {
	Ingest(context.Context, IngestionBatch) error
	GetTraceRoot(context.Context, string) (Trace, error)
	GetObservation(context.Context, string) (Observation, error)
}

type ClientOptions struct {
	BaseURL    string
	PublicKey  string
	SecretKey  string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    *url.URL
	publicKey  string
	secretKey  string
	httpClient *http.Client
}

func NewClient(options ClientOptions) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid Langfuse base URL")
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" || baseURL.Path != "" && baseURL.Path != "/" {
		return nil, fmt.Errorf("Langfuse base URL must be an HTTP(S) origin")
	}
	if options.PublicKey == "" || options.SecretKey == "" {
		return nil, fmt.Errorf("Langfuse public and secret keys are required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	baseURL.Path = ""
	return &Client{baseURL: baseURL, publicKey: options.PublicKey, secretKey: options.SecretKey, httpClient: httpClient}, nil
}

func (client *Client) String() string {
	if client == nil {
		return "langfuse.Client<nil>"
	}
	return fmt.Sprintf("langfuse.Client{BaseURL:%q PublicKey:[REDACTED] SecretKey:[REDACTED]}", client.baseURL)
}

func (client *Client) GoString() string { return client.String() }

func (client *Client) Ingest(ctx context.Context, batch IngestionBatch) error {
	if len(batch.Batch) == 0 {
		return fmt.Errorf("Langfuse ingestion batch is empty")
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encode Langfuse ingestion batch: %w", err)
	}
	endpoint := *client.baseURL
	endpoint.Path = ingestionPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Langfuse ingestion request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(client.publicKey, client.secretKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Langfuse ingestion request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Langfuse ingestion response")
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("Langfuse ingestion response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Langfuse ingestion returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode == http.StatusMultiStatus {
		var result struct {
			Errors []json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(body, &result); err != nil || len(result.Errors) > 0 {
			return fmt.Errorf("Langfuse ingestion returned event errors")
		}
	}
	return nil
}

type HTTPError struct {
	Operation  string
	StatusCode int
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("Langfuse %s returned HTTP %d", err.Operation, err.StatusCode)
}

func IsNotFound(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound
}

func (client *Client) GetTrace(ctx context.Context, traceID string) (Trace, error) {
	if traceID == "" {
		return Trace{}, fmt.Errorf("Langfuse trace ID is required")
	}
	endpoint := *client.baseURL
	endpoint.Path = tracePathPrefix + traceID
	endpoint.RawPath = tracePathPrefix + url.PathEscape(traceID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Trace{}, fmt.Errorf("create Langfuse trace request: %w", err)
	}
	request.SetBasicAuth(client.publicKey, client.secretKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Trace{}, fmt.Errorf("send Langfuse trace request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return Trace{}, &HTTPError{Operation: "trace query", StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Trace{}, fmt.Errorf("read Langfuse trace response failed")
	}
	var trace Trace
	if err := json.Unmarshal(body, &trace); err != nil {
		return Trace{}, fmt.Errorf("decode Langfuse trace response")
	}
	return trace, nil
}

func (client *Client) GetTraceRoot(ctx context.Context, traceID string) (Trace, error) {
	if traceID == "" {
		return Trace{}, fmt.Errorf("Langfuse trace ID is required")
	}
	endpoint := *client.baseURL
	endpoint.Path = tracePathPrefix + traceID
	endpoint.RawPath = tracePathPrefix + url.PathEscape(traceID)
	query := endpoint.Query()
	query.Set("fields", "core,io")
	endpoint.RawQuery = query.Encode()
	var trace Trace
	if err := client.getJSON(ctx, endpoint.String(), "trace root query", &trace); err != nil {
		return Trace{}, err
	}
	return trace, nil
}

func (client *Client) GetObservation(ctx context.Context, observationID string) (Observation, error) {
	if observationID == "" {
		return Observation{}, fmt.Errorf("Langfuse observation ID is required")
	}
	endpoint := *client.baseURL
	endpoint.Path = observationPathPrefix + observationID
	endpoint.RawPath = observationPathPrefix + url.PathEscape(observationID)
	var observation Observation
	if err := client.getJSON(ctx, endpoint.String(), "observation query", &observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (client *Client) getJSON(ctx context.Context, endpoint, operation string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Langfuse %s request", operation)
	}
	request.SetBasicAuth(client.publicKey, client.secretKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Langfuse %s request failed", operation)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return &HTTPError{Operation: operation, StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fmt.Errorf("read Langfuse %s response failed", operation)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode Langfuse %s response", operation)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("decode Langfuse %s response", operation)
	}
	return nil
}

var _ API = (*Client)(nil)
var _ DurableAPI = (*Client)(nil)
