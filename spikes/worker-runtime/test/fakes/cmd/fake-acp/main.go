package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

const fakeSessionID = sdk.SessionId("fake-session-1")

type fakeAgent struct {
	connection *sdk.AgentSideConnection
	client     *http.Client
	processCtx context.Context
	controlURL string
	readers    sync.WaitGroup
	inflight   map[string]bool
	closing    bool

	mu            sync.Mutex
	authenticated bool
	gatewayURL    string
	authorization string
	workingCopy   string
	turn          int
}

var _ sdk.Agent = (*fakeAgent)(nil)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	agent := &fakeAgent{client: &http.Client{Timeout: 10 * time.Second}, processCtx: ctx, controlURL: "http://observation-fake-model:8080", inflight: map[string]bool{}}
	connection := sdk.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.connection = connection
	select {
	case <-connection.Done():
	case <-ctx.Done():
	}
	cancel()
	agent.mu.Lock()
	agent.closing = true
	agent.mu.Unlock()
	agent.client.CloseIdleConnections()
	agent.readers.Wait()
}

func (a *fakeAgent) Initialize(
	_ context.Context,
	request sdk.InitializeRequest,
) (sdk.InitializeResponse, error) {
	if request.ProtocolVersion != sdk.ProtocolVersionNumber {
		return sdk.InitializeResponse{}, fmt.Errorf("unsupported protocol version")
	}
	logEvent(map[string]any{"event": "initialize"})
	return sdk.InitializeResponse{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		AuthMethods: []sdk.AuthMethod{{
			Agent: &sdk.AuthMethodAgent{Id: "gateway", Name: "Switchback Gateway"},
		}},
		AgentCapabilities: sdk.AgentCapabilities{},
	}, nil
}

func (a *fakeAgent) Authenticate(
	_ context.Context,
	request sdk.AuthenticateRequest,
) (sdk.AuthenticateResponse, error) {
	if request.MethodId != "gateway" {
		return sdk.AuthenticateResponse{}, fmt.Errorf("gateway authentication is required")
	}
	gateway, ok := request.Meta["gateway"].(map[string]any)
	if !ok {
		return sdk.AuthenticateResponse{}, fmt.Errorf("gateway metadata is required")
	}
	baseURL, _ := gateway["baseUrl"].(string)
	headers, ok := gateway["headers"].(map[string]any)
	if !ok {
		return sdk.AuthenticateResponse{}, fmt.Errorf("gateway headers are required")
	}
	authorization, _ := headers["Authorization"].(string)
	if baseURL == "" || !strings.HasPrefix(authorization, "Bearer ") {
		return sdk.AuthenticateResponse{}, fmt.Errorf("gateway route is incomplete")
	}
	a.mu.Lock()
	a.gatewayURL = strings.TrimSuffix(baseURL, "/")
	a.authorization = authorization
	a.authenticated = true
	a.mu.Unlock()
	logEvent(map[string]any{"event": "authenticate", "authorization_present": true})
	return sdk.AuthenticateResponse{}, nil
}

func (a *fakeAgent) NewSession(
	_ context.Context,
	request sdk.NewSessionRequest,
) (sdk.NewSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.authenticated {
		return sdk.NewSessionResponse{}, fmt.Errorf("fake ACP is not authenticated")
	}
	if a.workingCopy != "" {
		return sdk.NewSessionResponse{}, fmt.Errorf("fake ACP supports one session")
	}
	if !filepath.IsAbs(request.Cwd) || filepath.Clean(request.Cwd) != request.Cwd {
		return sdk.NewSessionResponse{}, fmt.Errorf("working directory must be absolute")
	}
	a.workingCopy = request.Cwd
	logEvent(map[string]any{"event": "new_session", "session_id": fakeSessionID})
	return sdk.NewSessionResponse{SessionId: fakeSessionID}, nil
}

func (a *fakeAgent) Prompt(
	ctx context.Context,
	request sdk.PromptRequest,
) (sdk.PromptResponse, error) {
	a.mu.Lock()
	if request.SessionId != fakeSessionID || a.workingCopy == "" {
		a.mu.Unlock()
		return sdk.PromptResponse{}, fmt.Errorf("fake ACP session does not match")
	}
	a.turn++
	turn := a.turn
	workingCopy := a.workingCopy
	gatewayURL := a.gatewayURL
	authorization := a.authorization
	a.mu.Unlock()
	promptJSON, _ := json.Marshal(request.Prompt)
	marker := regexp.MustCompile(`switchback-obsv-late:([a-z0-9]{8,64})`).FindSubmatch(promptJSON)
	if turn < 1 || (turn > 2 && len(marker) == 0) {
		return sdk.PromptResponse{}, fmt.Errorf("fake ACP supports exactly two turns")
	}
	correlation, err := promptCorrelation(request.Meta)
	if err != nil {
		return sdk.PromptResponse{}, err
	}
	logEvent(map[string]any{
		"event":      "prompt",
		"turn":       turn,
		"session_id": request.SessionId,
	})
	if err := a.connection.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: request.SessionId,
		Update:    sdk.UpdateAgentMessageText(fmt.Sprintf("running deterministic turn %d", turn)),
	}); err != nil {
		return sdk.PromptResponse{}, err
	}
	toolID := sdk.ToolCallId(fmt.Sprintf("fake-edit-%d", turn))
	if err := a.connection.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: request.SessionId,
		Update: sdk.StartToolCall(
			toolID,
			fmt.Sprintf("Apply deterministic fixture edit %d", turn),
			sdk.WithStartKind(sdk.ToolKindEdit),
			sdk.WithStartStatus(sdk.ToolCallStatusInProgress),
		),
	}); err != nil {
		return sdk.PromptResponse{}, err
	}
	if len(marker) > 0 {
		if len(correlation) != 0 {
			return sdk.PromptResponse{}, fmt.Errorf("controlled stream requires current metadata")
		}
		if err := a.startControlledModelStream(gatewayURL, authorization, string(marker[1]), request.Meta); err != nil {
			return sdk.PromptResponse{}, err
		}
	} else {
		if err := callModel(ctx, a.client, gatewayURL, authorization, correlation, turn); err != nil {
			return sdk.PromptResponse{}, err
		}
	}
	if err := applyControlledEdit(workingCopy, turn, len(marker) > 0); err != nil {
		return sdk.PromptResponse{}, err
	}
	if err := a.connection.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: request.SessionId,
		Update: sdk.UpdateToolCall(
			toolID,
			sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted),
			sdk.WithUpdateRawOutput(map[string]any{"turn": turn, "changed": true}),
		),
	}); err != nil {
		return sdk.PromptResponse{}, err
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

func applyControlledEdit(path string, turn int, controlled bool) error {
	if controlled && turn > 2 {
		return nil
	}
	return applyEdit(path, turn)
}

// Only this explicit test fixture keeps an already-started response reader
// alive across EndTurn. Its owner is the ACP process and its original 10s
// request deadline, never the next Turn or an unbounded background context.
func (a *fakeAgent) startControlledModelStream(gatewayURL, authorization, nonce string, meta map[string]any) error {
	if a.processCtx == nil {
		return fmt.Errorf("fixture process owner missing")
	}
	if _, err := promptCorrelation(meta); err != nil {
		return err
	}
	s := meta["switchback"].(map[string]any)
	scope := struct {
		Nonce      string `json:"nonce"`
		Session    string `json:"worker_session_id"`
		Runtime    string `json:"runtime_id"`
		Generation uint32 `json:"runtime_generation"`
		Turn       string `json:"agent_turn_id"`
		Ordinal    int    `json:"call_ordinal"`
	}{nonce, s["worker_session_id"].(string), s["runtime_id"].(string), uint32(s["runtime_generation"].(float64)), s["agent_turn_id"].(string), 1}
	scopeJSON, _ := json.Marshal(scope)
	key := base64.RawURLEncoding.EncodeToString(scopeJSON)
	a.mu.Lock()
	if a.inflight == nil {
		a.inflight = map[string]bool{}
	}
	if a.closing || a.processCtx.Err() != nil || a.inflight[key] || len(a.inflight) >= 8 {
		a.mu.Unlock()
		return fmt.Errorf("fixture duplicate/inflight limit")
	}
	a.inflight[key] = true
	a.readers.Add(1)
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.processCtx, 10*time.Second)
	ready := make(chan error, 1)
	go func() {
		defer a.readers.Done()
		defer cancel()
		defer func() { a.mu.Lock(); delete(a.inflight, key); a.mu.Unlock() }()
		data, _ := json.Marshal(map[string]any{"model": "switchback-codex", "stream": true, "input": "switchback-obsv-stream:" + key})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/responses", bytes.NewReader(data))
		if err != nil {
			ready <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authorization)
		res, err := a.client.Do(req)
		if err != nil {
			ready <- fmt.Errorf("fixture stream request failed")
			return
		}
		defer res.Body.Close()
		ids := res.Header.Values("x-litellm-call-id")
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || len(ids) != 1 || ids[0] == "" || len(ids[0]) > 256 || strings.ContainsAny(ids[0], " ,\r\n\t") {
			ready <- fmt.Errorf("fixture real response identity/content type missing")
			return
		}
		id := ids[0]
		scanner := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		signaled, completed := false, false
		responseID := ""
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var event struct {
				Type     string `json:"type"`
				Response struct {
					ID     string          `json:"id"`
					Status string          `json:"status"`
					Output json.RawMessage `json:"output"`
					Usage  struct {
						Input  int `json:"input_tokens"`
						Output int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(payload), &event) != nil {
				err = fmt.Errorf("invalid SSE event")
				break
			}
			if !signaled {
				if event.Type != "response.created" || event.Response.ID == "" || event.Response.Status != "in_progress" {
					err = fmt.Errorf("first SSE event not nonterminal response.created")
					break
				}
				responseID = event.Response.ID
				if err = a.fixtureControl(ctx, "register", key, id, false); err != nil {
					break
				}
				signaled = true
				ready <- nil
				continue
			}
			if event.Type == "response.completed" {
				if completed || event.Response.ID != responseID || event.Response.Status != "completed" || len(event.Response.Output) == 0 || string(event.Response.Output) == "[]" || event.Response.Usage.Input != 11 || event.Response.Usage.Output != 7 {
					err = fmt.Errorf("completed SSE content/identity mismatch")
					break
				}
				completed = true
			}
			if event.Type == "error" || event.Type == "response.failed" || event.Type == "response.incomplete" {
				err = fmt.Errorf("SSE failure")
				break
			}
		}
		if scanner.Err() != nil || !completed {
			err = fmt.Errorf("fixture stream ended before completion")
		}
		if !signaled {
			if err == nil {
				err = fmt.Errorf("fixture stream missing first event")
			}
			ready <- err
			return
		}
		// The control request reports only a safe outcome and the pre-registered
		// ID. Cancellation cannot accidentally be recorded as a successful model.
		reportCtx, reportCancel := context.WithTimeout(a.processCtx, time.Second)
		defer reportCancel()
		_ = a.fixtureControl(reportCtx, "result", key, id, err == nil && completed)
	}()
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return fmt.Errorf("fixture first event deadline")
	}
}

func (a *fakeAgent) fixtureControl(ctx context.Context, action, key, id string, completed bool) error {
	data, _ := json.Marshal(map[string]any{"key": key, "expected_observation_id": id, "completed": completed})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.controlURL+"/fixture/"+action, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("fixture ledger unavailable")
	}
	defer res.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 8192))
	if err != nil {
		return fmt.Errorf("fixture ledger read failed")
	}
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return fmt.Errorf("fixture ledger rejected %s", action)
	}
	return nil
}

func callModel(
	ctx context.Context,
	client *http.Client,
	gatewayURL, authorization string,
	correlation map[string]string,
	turn int,
) error {
	body, err := json.Marshal(map[string]any{
		"model": "switchback-codex",
		"input": fmt.Sprintf("deterministic integration turn %d", turn),
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		gatewayURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	headers := map[string]string{
		"x-request-id":                   correlation["request_id"],
		"x-switchback-trace-id":          correlation["trace_id"],
		"x-switchback-worker-session-id": correlation["worker_session_id"],
		"x-switchback-worker-run-id":     correlation["worker_run_id"],
		"x-switchback-agent-turn-id":     correlation["agent_turn_id"],
	}
	for name, value := range headers {
		if len(correlation) == 0 {
			break
		} // Current shim's model sink owns correlation binding.
		if value == "" {
			return fmt.Errorf("prompt correlation %s is required", name)
		}
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call LiteLLM: %w", err)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return readErr
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("LiteLLM status %d", response.StatusCode)
	}
	return nil
}

func promptCorrelation(meta map[string]any) (map[string]string, error) {
	switchback, ok := meta["switchback"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Switchback prompt metadata is required")
	}
	_, legacyRequest := switchback["request_id"]
	_, legacyTrace := switchback["trace_id"]
	_, legacyRun := switchback["worker_run_id"]
	if !legacyRequest && !legacyTrace && !legacyRun {
		// Production ACP metadata deliberately omits model request/run/trace IDs.
		// Its LiteLLM callback binds its own call ID to the shim's active Turn.
		for _, key := range []string{"agent_turn_id", "worker_session_id", "runtime_id"} {
			value, _ := switchback[key].(string)
			if value == "" {
				return nil, fmt.Errorf("Switchback prompt metadata %s is required", key)
			}
		}
		generation, ok := switchback["runtime_generation"].(float64)
		if !ok || generation < 1 || generation != float64(uint32(generation)) {
			return nil, fmt.Errorf("Switchback runtime generation is invalid")
		}
		return map[string]string{}, nil
	}
	correlation := make(map[string]string, len(switchback))
	for _, key := range []string{
		"request_id",
		"trace_id",
		"worker_session_id",
		"worker_run_id",
		"agent_turn_id",
	} {
		value, _ := switchback[key].(string)
		if value == "" {
			return nil, fmt.Errorf("Switchback prompt metadata %s is required", key)
		}
		correlation[key] = value
	}
	return correlation, nil
}

func applyEdit(workingCopy string, turn int) error {
	switch turn {
	case 1:
		path := filepath.Join(workingCopy, "counter", "counter.go")
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		const defective = "return current - delta // Intentional Spike fixture defect."
		const repaired = "return current + delta"
		if !strings.Contains(string(content), defective) {
			return fmt.Errorf("counter fixture defect is absent")
		}
		return os.WriteFile(path, []byte(strings.Replace(string(content), defective, repaired, 1)), 0o644)
	case 2:
		path := filepath.Join(workingCopy, "counter", "counter_test.go")
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		const boundary = `

func TestAddZeroDelta(t *testing.T) {
	if got := Add(7, 0); got != 7 {
		t.Fatalf("Add(7, 0) = %d, want 7", got)
	}
}
`
		if strings.Contains(string(content), "TestAddZeroDelta") {
			return fmt.Errorf("zero-delta test already exists")
		}
		return os.WriteFile(path, append(content, []byte(boundary)...), 0o644)
	default:
		return fmt.Errorf("unsupported turn %d", turn)
	}
}

func logEvent(event map[string]any) {
	encoded, err := json.Marshal(event)
	if err == nil {
		_, _ = fmt.Fprintf(os.Stderr, "SWITCHBACK_FAKE_ACP %s\n", encoded)
	}
}

func (*fakeAgent) Cancel(context.Context, sdk.CancelNotification) error { return nil }
func (*fakeAgent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}
func (*fakeAgent) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, nil
}
func (*fakeAgent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}
func (*fakeAgent) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, nil
}
func (*fakeAgent) SetSessionConfigOption(
	context.Context,
	sdk.SetSessionConfigOptionRequest,
) (sdk.SetSessionConfigOptionResponse, error) {
	return sdk.SetSessionConfigOptionResponse{}, nil
}
func (*fakeAgent) SetSessionMode(
	context.Context,
	sdk.SetSessionModeRequest,
) (sdk.SetSessionModeResponse, error) {
	return sdk.SetSessionModeResponse{}, nil
}
