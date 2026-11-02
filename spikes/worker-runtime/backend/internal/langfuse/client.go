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
	ingestionPath      = "/api/public/ingestion"
	tracePathPrefix    = "/api/public/traces/"
	defaultHTTPTimeout = 10 * time.Second
	maxResponseBytes   = 4 << 20
)

// IngestionEvent is the v3.217.0 legacy public-ingestion event envelope.
type IngestionEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Body      map[string]any `json:"body"`
}

// IngestionBatch is one legacy public-ingestion request.
type IngestionBatch struct {
	Batch []IngestionEvent `json:"batch"`
}

type Observation struct {
	ID                  string         `json:"id"`
	TraceID             string         `json:"traceId"`
	ParentObservationID *string        `json:"parentObservationId"`
	Name                string         `json:"name"`
	Type                string         `json:"type"`
	Model               *string        `json:"model"`
	Metadata            map[string]any `json:"metadata"`
}

type RuntimeMetadata struct {
	WorkerSessionID   string   `json:"worker_session_id"`
	WorkerRunID       string   `json:"worker_run_id"`
	RuntimeInstanceID string   `json:"runtime_instance_id"`
	RuntimeGeneration uint32   `json:"runtime_generation"`
	AgentSessionID    string   `json:"agent_session_id"`
	AgentTurnIDs      []string `json:"agent_turn_ids"`
}

type Trace struct {
	ID           string          `json:"id"`
	Metadata     RuntimeMetadata `json:"metadata"`
	Observations []Observation   `json:"observations"`
}

type ClientOptions struct {
	BaseURL    string
	PublicKey  string
	SecretKey  string
	HTTPClient *http.Client
}

// Client accesses only Langfuse's public HTTP API. Its formatter never exposes
// credentials, including when a test or logger uses the Go-syntax formatter.
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
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid Langfuse base URL scheme")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" ||
		(baseURL.Path != "" && baseURL.Path != "/") {
		return nil, fmt.Errorf("Langfuse base URL must contain only scheme and authority")
	}
	if options.PublicKey == "" || options.SecretKey == "" {
		return nil, fmt.Errorf("Langfuse public and secret keys are required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	baseURL.Path = ""
	return &Client{
		baseURL:    baseURL,
		publicKey:  options.PublicKey,
		secretKey:  options.SecretKey,
		httpClient: httpClient,
	}, nil
}

func (c *Client) String() string {
	if c == nil {
		return "langfuse.Client<nil>"
	}
	return fmt.Sprintf("langfuse.Client{BaseURL:%q PublicKey:[REDACTED] SecretKey:[REDACTED]}", c.baseURL)
}

func (c *Client) GoString() string {
	return c.String()
}

func (c *Client) Ingest(ctx context.Context, batch IngestionBatch) error {
	if len(batch.Batch) == 0 {
		return fmt.Errorf("Langfuse ingestion batch is empty")
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encode Langfuse ingestion batch: %w", err)
	}
	endpoint := *c.baseURL
	endpoint.Path = ingestionPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Langfuse ingestion request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(c.publicKey, c.secretKey)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Langfuse ingestion request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Langfuse ingestion response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("Langfuse ingestion response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Langfuse ingestion returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode == http.StatusMultiStatus {
		var result struct {
			Errors []json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("decode Langfuse ingestion response")
		}
		if len(result.Errors) > 0 {
			return fmt.Errorf("Langfuse ingestion returned %d event error(s)", len(result.Errors))
		}
	}
	return nil
}

type HTTPError struct {
	Operation  string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Langfuse %s returned HTTP %d", e.Operation, e.StatusCode)
}

func IsNotFound(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound
}

func (c *Client) GetTrace(ctx context.Context, traceID string) (Trace, error) {
	if traceID == "" {
		return Trace{}, fmt.Errorf("Langfuse trace ID is required")
	}
	endpoint := *c.baseURL
	endpoint.Path = tracePathPrefix + traceID
	endpoint.RawPath = tracePathPrefix + url.PathEscape(traceID)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Trace{}, fmt.Errorf("create Langfuse trace request: %w", err)
	}
	request.SetBasicAuth(c.publicKey, c.secretKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Trace{}, fmt.Errorf("send Langfuse trace request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return Trace{}, &HTTPError{Operation: "trace query", StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Trace{}, fmt.Errorf("read Langfuse trace response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return Trace{}, fmt.Errorf("Langfuse trace response exceeds %d bytes", maxResponseBytes)
	}
	var trace Trace
	if err := json.Unmarshal(body, &trace); err != nil {
		return Trace{}, fmt.Errorf("decode Langfuse trace response")
	}
	return trace, nil
}
