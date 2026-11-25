//go:build yhc_acp_smoke

package yhcacp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/adapter/yhcacp"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const fixtureModel = "gpt-4o"
const rejectedPrompt = "Request a report with the fixture permit already spent."

type fixtureTurn struct {
	prompt string
	report string
}

type fixtureMessage struct {
	Role    string
	Content json.RawMessage
}

type fixtureCounts struct {
	foreground atomic.Int32
	background atomic.Int32
	rejected   atomic.Int32
}

func TestYHCACPReportAndRework(t *testing.T) {
	binary := os.Getenv("SWITCHBACK_TEST_YHC_BINARY")
	if binary == "" || os.Getenv("SWITCHBACK_TEST_YHC_SHA256") == "" {
		t.Fatal("set SWITCHBACK_TEST_YHC_BINARY and SWITCHBACK_TEST_YHC_SHA256 for the offline YHC smoke")
	}
	assertPinnedBinary(t, binary, os.Getenv("SWITCHBACK_TEST_YHC_SHA256"))
	t.Run("managed", func(t *testing.T) { runYHCACPReportAndRework(t, binary, true) })
	t.Run("default", func(t *testing.T) { runYHCACPReportAndRework(t, binary, false) })
}

func runYHCACPReportAndRework(t *testing.T, binary string, managed bool) {
	t.Helper()
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	proxyToken := hex.EncodeToString(credential)
	turns := []fixtureTurn{
		{"Deliver the offline experiment summary.", "# Offline experiment\n\nBaseline costs recorded."},
		{"Revise the summary to include the stress scenario.", "# Offline experiment\n\nBaseline costs and the stress scenario recorded."},
		{"Confirm the revised summary after reconnecting.", "The revised summary includes baseline costs and the stress scenario."},
	}
	var counts fixtureCounts
	extractionSeen := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleFixtureTurn(t, w, r, proxyToken, &counts, extractionSeen, turns)
	}))
	t.Cleanup(upstream.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	home := filepath.Join(root, "agent-state")
	for _, dir := range []string{workspace, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	tracePath := filepath.Join(root, "acp.jsonl")
	trace, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trace.Close() })
	start := func(generation uint64, existing string) (*acpclient.Adapter, string) {
		adapter, err := acpclient.NewPreconfigured(acpclient.Options{
			ProcessName: "yhc", WorkspaceRoot: workspace, Trace: trace,
			ExtensionFailureSession: yhcacp.FailureSession,
		}, proxyToken, func(boundToken string) *exec.Cmd {
			cmd := exec.Command(binary, "serve", "acp", "--provider", "openai", "--model", fixtureModel,
				"--base-url", upstream.URL+"/v1", "--tools=")
			if managed {
				cmd.Args = append(cmd.Args, "--disable-long-session-services")
			}
			cmd.Dir = workspace
			cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "TMPDIR=" + root,
				"PATH=/usr/bin:/bin", "PROV_API_KEY=" + boundToken, "YHC_SIMPLE=1"}
			return cmd
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := adapter.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		caps, err := adapter.Initialize(ctx, application.AgentInitializeRequest{
			WorkerSessionID: "probe-worker", RuntimeID: fmt.Sprintf("probe-runtime-%d", generation),
			Generation: generation, ProtocolVersion: "1",
		})
		if err != nil || !caps.ResumeSession {
			t.Fatalf("YHC Initialize or Resume capability: %v", err)
		}
		if err := adapter.Authenticate(ctx, proxyToken); err != nil {
			t.Fatal(err)
		}
		session, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
			WorkerSessionID: "probe-worker", WorkingCopyPath: workspace,
			AgentSessionID: existing, LoadExisting: existing != "",
		})
		if err != nil || (existing != "" && session != existing) {
			t.Fatalf("YHC OpenSession: %v", err)
		}
		return adapter, session
	}
	resident, session := start(1, "")
	assertTurn(t, ctx, resident, session, "report", turns[0])
	assertTurn(t, ctx, resident, session, "rework", turns[1])
	if !managed {
		select {
		case <-extractionSeen:
		case <-ctx.Done():
			t.Fatal("default YHC did not send its background extraction request")
		}
	}
	if err := resident.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, session := start(2, session)
	assertTurn(t, ctx, restarted, session, "after-resume", turns[2])
	if managed {
		assertRejectedTurn(t, ctx, restarted, session)
	}
	if err := restarted.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if int(counts.foreground.Load()) != len(turns) {
		t.Fatalf("foreground fixture requests = %d, want %d", counts.foreground.Load(), len(turns))
	}
	if managed && counts.background.Load() != 0 {
		t.Fatalf("managed YHC sent %d background extraction requests", counts.background.Load())
	}
	wantRejected := int32(0)
	if managed {
		wantRejected = 1
	}
	if counts.rejected.Load() != wantRejected {
		t.Fatalf("fixture provider rejections = %d, want %d", counts.rejected.Load(), wantRejected)
	}
	rawTrace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawTrace), proxyToken) || strings.Contains(string(rawTrace), `\"authenticate\"`) {
		t.Fatal("preconfigured credential or authentication request reached raw ACP Trace")
	}
	if strings.Count(string(rawTrace), `\"session/resume\"`) != 1 {
		t.Fatal("raw ACP Trace does not prove one resume")
	}
	t.Logf("ACP v1: report and rework on one process, exact session resumed after restart, %d successful foreground, %d background and %d rejected requests to the fixture", counts.foreground.Load(), counts.background.Load(), counts.rejected.Load())
}

func assertPinnedBinary(t *testing.T, path, expected string) {
	t.Helper()
	if !filepath.IsAbs(path) || len(expected) != sha256.Size*2 {
		t.Fatal("a trusted absolute YHC binary path and its SHA-256 are required")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatal("YHC binary must be a regular executable")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != expected {
		t.Fatalf("YHC binary SHA-256 = %s, want %s", got, expected)
	}
}

func assertTurn(t *testing.T, ctx context.Context, adapter *acpclient.Adapter, session, turnID string, fixture fixtureTurn) {
	t.Helper()
	updates, err := adapter.Prompt(ctx, application.AgentPromptRequest{
		AgentSessionID: session, AgentTurnID: turnID, Content: fixture.prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	var terminals int
	for update := range updates {
		switch update.Kind {
		case "agent_message_chunk":
			var chunk struct {
				Content struct{ Text string }
			}
			if err := json.Unmarshal(update.Payload, &chunk); err != nil {
				t.Fatal(err)
			}
			report.WriteString(chunk.Content.Text)
		case "terminal":
			if string(update.Payload) != `{"stop_reason":"end_turn"}` {
				t.Fatalf("%s unexpected terminal: %s", turnID, update.Payload)
			}
			terminals++
		case "error":
			t.Fatalf("%s ACP failure: %s", turnID, update.Payload)
		}
	}
	if terminals != 1 || report.String() != fixture.report {
		t.Fatalf("%s: %d terminals, report matched: %t", turnID, terminals, report.String() == fixture.report)
	}
}

func assertRejectedTurn(t *testing.T, ctx context.Context, adapter *acpclient.Adapter, session string) {
	t.Helper()
	updates, err := adapter.Prompt(ctx, application.AgentPromptRequest{
		AgentSessionID: session, AgentTurnID: "permit-denied", Content: rejectedPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var failures int
	for update := range updates {
		switch update.Kind {
		case "error":
			if string(update.Payload) != `{"code":"acp_prompt_failed"}` {
				t.Fatalf("unexpected ACP failure: %s", update.Payload)
			}
			failures++
		case "terminal":
			t.Fatalf("provider rejection became a successful ACP terminal: %s", update.Payload)
		}
	}
	if failures != 1 {
		t.Fatalf("provider rejection produced %d ACP failures, want 1", failures)
	}
}

func handleFixtureTurn(t *testing.T, w http.ResponseWriter, r *http.Request, token string, counts *fixtureCounts, extractionSeen chan<- struct{}, turns []fixtureTurn) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("unexpected fixture request (%s %s)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var request struct {
		Model  string
		Input  []fixtureMessage
		Stream bool
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || request.Model != fixtureModel {
		t.Error("invalid fixture model request")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !request.Stream && len(request.Input) > 0 && request.Input[0].Role == "system" &&
		strings.HasPrefix(request.Input[0].text(), "Extract any durable learnings from this coding session") {
		count := counts.background.Add(1)
		writeFixtureReport(w, len(turns)+int(count), "[]", false)
		if count == 1 {
			extractionSeen <- struct{}{}
		}
		return
	}
	if includesFixtureMessage(request.Input, "user", rejectedPrompt) {
		counts.rejected.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"message":"fixture permit already used","type":"invalid_request_error"}}`)
		return
	}
	index := int(counts.foreground.Add(1)) - 1
	if index >= len(turns) {
		t.Error("more foreground requests arrived than prompts sent")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	hasPrompt := includesFixtureMessage(request.Input, "user", turns[index].prompt)
	hasPriorReport := index == 0 || includesFixtureMessage(request.Input, "assistant", turns[index-1].report)
	if !hasPrompt || !hasPriorReport {
		t.Errorf("fixture request %d: current prompt present=%t, previous report present=%t", index+1, hasPrompt, hasPriorReport)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	writeFixtureReport(w, index+1, turns[index].report, request.Stream)
}

func (message fixtureMessage) text() string {
	var plain string
	if json.Unmarshal(message.Content, &plain) == nil {
		return plain
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(message.Content, &parts) != nil {
		return ""
	}
	var content strings.Builder
	for _, part := range parts {
		if part.Type != "input_text" && part.Type != "output_text" {
			return ""
		}
		content.WriteString(part.Text)
	}
	return content.String()
}

func includesFixtureMessage(messages []fixtureMessage, role, text string) bool {
	for _, message := range messages {
		if message.Role == role && message.text() == text {
			return true
		}
	}
	return false
}

func writeFixtureReport(w http.ResponseWriter, index int, text string, stream bool) {
	responseID := fmt.Sprintf("resp_fixture_%d", index)
	messageID := fmt.Sprintf("msg_fixture_%d", index)
	part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
	item := map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
	response := map[string]any{
		"id": responseID, "object": "response", "model": fixtureModel, "status": "completed",
		"output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	events := []map[string]any{
		{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}},
		{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": messageID, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}},
		{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": messageID, "delta": text},
		{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "item_id": messageID, "text": text},
		{"type": "response.content_part.done", "output_index": 0, "content_index": 0, "item_id": messageID, "part": part},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	}
	for sequence, event := range events {
		event["sequence_number"] = sequence
		encoded, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}
