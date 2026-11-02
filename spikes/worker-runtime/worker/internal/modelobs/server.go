// Package modelobs receives bounded LiteLLM observations on loopback.
package modelobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	modelSinkAddress = "127.0.0.1:7777"
	maxBodyBytes     = 1 << 20

	ModelSinkTokenHeader = "X-Switchback-Model-Sink-Token"
	maxBindings          = 4096
)

// Usage is the normalized token usage attached to one model observation.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// Correlation is enriched with Switchback identity from the trusted request binding.
// The callback supplies only LiteLLM's request ID and never provides Switchback fields.
type Correlation struct {
	RequestID       string `json:"request_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	WorkerSessionID string `json:"worker_session_id,omitempty"`
	WorkerRunID     string `json:"worker_run_id,omitempty"`
	AgentTurnID     string `json:"agent_turn_id,omitempty"`
}

// Observation is the typed local contract emitted by the LiteLLM callback.
type Observation struct {
	Model       string      `json:"model"`
	Route       string      `json:"route"`
	StartedAt   time.Time   `json:"started_at"`
	EndedAt     time.Time   `json:"ended_at"`
	LatencyMS   float64     `json:"latency_ms"`
	Usage       Usage       `json:"usage"`
	CostUSD     float64     `json:"cost_usd"`
	Error       string      `json:"error,omitempty"`
	Correlation Correlation `json:"correlation"`
}

// RequestBinder snapshots the active trusted Turn when LiteLLM starts a model
// request. LiteLLM can later recover only that immutable snapshot by its own
// server-generated call ID; it never supplies Switchback identity over HTTP.
type RequestBinder struct {
	mu       sync.Mutex
	active   *Correlation
	bindings map[string]binding
}

type binding struct {
	correlation Correlation
	consumed    bool
}

// NewRequestBinder creates the shared Worker-control and loopback boundary.
func NewRequestBinder() *RequestBinder {
	return &RequestBinder{bindings: make(map[string]binding)}
}

// Activate makes one exact, request-less Turn available to a pre-call hook.
func (b *RequestBinder) Activate(correlation Correlation) error {
	if !validActiveCorrelation(correlation) {
		return fmt.Errorf("complete request-less AgentTurn identity is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != nil {
		return fmt.Errorf("an AgentTurn is already active for model correlation")
	}
	copy := correlation
	b.active = &copy
	return nil
}

// Deactivate fences new bindings without invalidating already-started calls.
func (b *RequestBinder) Deactivate(correlation Correlation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil || !sameTurn(*b.active, correlation) {
		return fmt.Errorf("AgentTurn is not active for model correlation")
	}
	b.active = nil
	return nil
}

// Bind records LiteLLM's stable request identity at request start.
func (b *RequestBinder) Bind(requestID string) error {
	if !validRequestID(requestID) {
		return fmt.Errorf("valid LiteLLM request identity is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		return fmt.Errorf("no active AgentTurn is available for model correlation")
	}
	if _, ok := b.bindings[requestID]; ok {
		return fmt.Errorf("LiteLLM request identity has already been used")
	}
	if len(b.bindings) >= maxBindings {
		return fmt.Errorf("model correlation binding capacity is exhausted")
	}
	bound := *b.active
	bound.RequestID = requestID
	b.bindings[requestID] = binding{correlation: bound}
	return nil
}

// Take resolves one callback identity exactly once, preventing duplicates from
// being attributed to a later Turn.
func (b *RequestBinder) Take(requestID string) (Correlation, error) {
	if !validRequestID(requestID) {
		return Correlation{}, fmt.Errorf("valid LiteLLM request identity is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bound, ok := b.bindings[requestID]
	if !ok || bound.consumed {
		return Correlation{}, fmt.Errorf("LiteLLM request identity is not bound")
	}
	bound.consumed = true
	b.bindings[requestID] = bound
	return bound.correlation, nil
}

func validActiveCorrelation(correlation Correlation) bool {
	return correlation.RequestID == "" &&
		validIdentityPart(correlation.TraceID) &&
		validIdentityPart(correlation.WorkerSessionID) &&
		validIdentityPart(correlation.WorkerRunID) &&
		validIdentityPart(correlation.AgentTurnID)
}

func validRequestID(requestID string) bool {
	return validIdentityPart(requestID)
}

func validIdentityPart(value string) bool {
	return len(value) <= maxBodyBytes && strings.TrimSpace(value) == value && value != ""
}

func sameTurn(left, right Correlation) bool {
	return left.TraceID == right.TraceID &&
		left.WorkerSessionID == right.WorkerSessionID &&
		left.WorkerRunID == right.WorkerRunID &&
		left.AgentTurnID == right.AgentTurnID
}

// EmitFunc accepts one validated typed observation.
type EmitFunc func(Observation) error

// Server owns the fixed loopback-only model observation listener.
type Server struct {
	tokenHash [sha256.Size]byte
	binder    *RequestBinder
	emit      EmitFunc

	mu          sync.Mutex
	listener    net.Listener
	http        *http.Server
	serveErr    chan error
	serveDone   chan struct{}
	serveResult error
}

// NewServer validates dependencies without retaining the plaintext token.
func NewServer(token string, binder *RequestBinder, emit EmitFunc) (*Server, error) {
	if token == "" {
		return nil, fmt.Errorf("model sink token is required")
	}
	if emit == nil {
		return nil, fmt.Errorf("model observation emitter is required")
	}
	if binder == nil {
		return nil, fmt.Errorf("model observation binder is required")
	}
	return &Server{
		tokenHash: sha256.Sum256([]byte(token)),
		binder:    binder,
		emit:      emit,
		serveErr:  make(chan error, 1),
		serveDone: make(chan struct{}),
	}, nil
}

// Start binds exactly the fixed IPv4 loopback address before returning.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("model observation sink already started")
	}
	listener, err := net.Listen("tcp4", modelSinkAddress)
	if err != nil {
		return fmt.Errorf("start model observation sink: %w", err)
	}
	s.listener = listener
	s.http = &http.Server{
		Addr:              modelSinkAddress,
		Handler:           http.HandlerFunc(s.handle),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	go func() {
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.mu.Lock()
		s.serveResult = err
		s.mu.Unlock()
		s.serveErr <- err
		close(s.serveErr)
		close(s.serveDone)
	}()
	return nil
}

// Addr returns the invariant listener address, never a wildcard.
func (s *Server) Addr() string {
	return modelSinkAddress
}

// Errors reports an unexpected Serve failure, if one occurs.
func (s *Server) Errors() <-chan error {
	return s.serveErr
}

// Healthy atomically reports whether the exact started listener is still
// serving. A closed listener is unhealthy even after graceful shutdown.
func (s *Server) Healthy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil || s.http == nil {
		return fmt.Errorf("model observation sink is not started")
	}
	select {
	case <-s.serveDone:
		if s.serveResult != nil {
			return fmt.Errorf("model observation sink stopped unexpectedly")
		}
		return fmt.Errorf("model observation sink is stopped")
	default:
		return nil
	}
}

// Shutdown cleanly closes the listener and drains active requests.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	server := s.http
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown model observation sink: %w", err)
	}
	return nil
}

func (s *Server) handle(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	presented := sha256.Sum256([]byte(request.Header.Get(ModelSinkTokenHeader)))
	if subtle.ConstantTimeCompare(presented[:], s.tokenHash[:]) != 1 {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	if request.ContentLength > maxBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}

	body, err := readBody(response, request)
	if err != nil {
		writeRequestBodyError(response, err)
		return
	}
	switch request.URL.Path {
	case "/bind":
		s.handleBind(response, body)
	case "/":
		s.handleObservation(response, body)
	default:
		writeError(response, http.StatusNotFound, "not_found")
	}
}

func (s *Server) handleBind(response http.ResponseWriter, body []byte) {
	var binding struct {
		LiteLLMCallID string `json:"litellm_call_id"`
	}
	if err := decodeStrictJSON(body, &binding); err != nil || s.binder.Bind(binding.LiteLLMCallID) != nil {
		writeError(response, http.StatusBadRequest, "invalid_correlation")
		return
	}
	response.WriteHeader(http.StatusAccepted)
	_, _ = response.Write([]byte(`{"status":"accepted"}`))
}

func (s *Server) handleObservation(response http.ResponseWriter, body []byte) {
	var observation Observation
	if err := decodeStrictJSON(body, &observation); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_observation")
		return
	}
	if !requestOnlyCorrelation(observation.Correlation) {
		writeError(response, http.StatusBadRequest, "invalid_correlation")
		return
	}
	if err := validateObservation(observation); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_observation")
		return
	}
	correlation, err := s.binder.Take(observation.Correlation.RequestID)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_correlation")
		return
	}
	observation.Correlation = correlation
	if err := s.emit(observation); err != nil {
		writeError(response, http.StatusServiceUnavailable, "observation_unavailable")
		return
	}
	response.WriteHeader(http.StatusAccepted)
	_, _ = response.Write([]byte(`{"status":"accepted"}`))
}

func requestOnlyCorrelation(correlation Correlation) bool {
	return validRequestID(correlation.RequestID) && correlation.TraceID == "" &&
		correlation.WorkerSessionID == "" && correlation.WorkerRunID == "" &&
		correlation.AgentTurnID == ""
}

func readBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	request.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	return io.ReadAll(request.Body)
}

func writeRequestBodyError(response http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	writeError(response, http.StatusBadRequest, "invalid_observation")
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func validateObservation(observation Observation) error {
	if observation.Model == "" || observation.Route == "" {
		return fmt.Errorf("model and route are required")
	}
	if observation.StartedAt.IsZero() || observation.EndedAt.IsZero() ||
		observation.EndedAt.Before(observation.StartedAt) {
		return fmt.Errorf("valid start and end times are required")
	}
	if observation.LatencyMS < 0 || observation.CostUSD < 0 ||
		observation.Usage.InputTokens < 0 ||
		observation.Usage.OutputTokens < 0 ||
		observation.Usage.TotalTokens < 0 {
		return fmt.Errorf("numeric observation values must be nonnegative")
	}
	return nil
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": code})
}
