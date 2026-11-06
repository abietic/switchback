package acpclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

type traceBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	failAuth bool
}

func (b *traceBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failAuth && bytes.Contains(p, []byte(`authenticate`)) {
		return 0, io.ErrClosedPipe
	}
	return b.b.Write(p)
}
func (b *traceBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.b.Bytes())
}

const knownFrame = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"peer-ok"}}}}` + "\n"
const unknownFrame = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"future_update","future":"literal"}}}` + "\n"
const invalidKnownFrame = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":false}}}}` + "\n"

func TestCoreUnknownBurstDoesNotConsumeNotificationCredits(t *testing.T) {
	root := t.TempDir()
	var trace traceBuffer
	a, ctx := newPeerAdapter(t, root, "unknown-burst", &trace)
	openPeer(t, a, ctx, root)
	updates := promptPeer(t, a, ctx)
	if len(updates) != 2 || updates[0].Kind != "agent_message_chunk" || updates[1].Kind != "terminal" {
		t.Fatalf("unknown burst broke known update/terminal: %+v", updates)
	}
	decoder := json.NewDecoder(bytes.NewReader(trace.Bytes()))
	warnings := 0
	for {
		var record struct {
			Type string `json:"type"`
		}
		if decoder.Decode(&record) != nil {
			break
		}
		if record.Type == "acp.unknown_update" {
			warnings++
		}
	}
	if warnings != 2048 {
		t.Fatalf("unknown raw warnings = %d, want 2048", warnings)
	}
}

func TestCoreInvalidKnownNotificationFailsClosedAfterRawTrace(t *testing.T) {
	root := t.TempDir()
	var trace traceBuffer
	a, ctx := newPeerAdapter(t, root, "invalid-known", &trace)
	openPeer(t, a, ctx, root)
	updates := promptPeer(t, a, ctx)
	for _, update := range updates {
		if update.Kind == "terminal" {
			t.Fatalf("invalid known notification produced successful terminal: %+v", updates)
		}
	}
	if !hasFrame(trace.Bytes(), invalidKnownFrame, "acp.raw_frame") {
		t.Fatal("invalid known frame was not authoritatively recorded")
	}
	if _, err := a.Prompt(ctx, promptRequest()); err == nil {
		t.Fatal("invalid known notification did not fence later Prompt")
	}
}

func TestCoreCancelledPromptWithoutWatermarkFencesAdmittedCallbacks(t *testing.T) {
	testCancelledPromptScope(t, "cancel-next-turn", 256)
}

func TestCoreCancelledPromptWithoutStopProofFencesWaitingRawPrefix(t *testing.T) {
	testCancelledPromptScope(t, "cancel-waiting-prefix", 257)
}

func TestCoreRealCancelResponseAllowsNextTurn(t *testing.T) {
	root := t.TempDir()
	a, ctx := newPeerAdapter(t, root, "cancel-resume", io.Discard)
	openPeer(t, a, ctx, root)
	request := promptRequest()
	updates, err := a.Prompt(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Kind != "agent_message_chunk" {
			t.Fatalf("initial update: %+v", update)
		}
	case <-ctx.Done():
		t.Fatal("first Prompt never reached peer")
	}
	if err := a.Cancel(ctx, request.AgentSessionID, request.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Kind != "terminal" || string(update.Payload) != `{"stop_reason":"cancelled"}` {
			t.Fatalf("real cancel response: %+v", update)
		}
	case <-ctx.Done():
		t.Fatal("Cancel response never arrived")
	}
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("Cancel produced more than one terminal")
		}
	case <-ctx.Done():
		t.Fatal("cancelled update stream never closed")
	}
	request.AgentTurnID = "next-turn"
	next, err := a.Prompt(ctx, request)
	if err != nil {
		t.Fatalf("proven cancelled response fenced next Turn: %v", err)
	}
	var got []application.AgentUpdate
	for {
		select {
		case update, open := <-next:
			if !open {
				if len(got) != 2 || got[0].Kind != "agent_message_chunk" || got[1].Kind != "terminal" || string(got[1].Payload) != `{"stop_reason":"end_turn"}` {
					t.Fatalf("next Turn: %+v", got)
				}
				return
			}
			got = append(got, update)
		case <-ctx.Done():
			t.Fatal("next Turn never completed")
		}
	}
}

func TestCoreCancelThenEOFDoesNotSynthesizeCancelledStopProof(t *testing.T) {
	root := t.TempDir()
	a, ctx := newPeerAdapter(t, root, "cancel-eof", io.Discard)
	openPeer(t, a, ctx, root)
	request := promptRequest()
	updates, err := a.Prompt(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Kind != "agent_message_chunk" {
			t.Fatalf("initial update: %+v", update)
		}
	case <-ctx.Done():
		t.Fatal("Prompt never reached peer")
	}
	if err := a.Cancel(ctx, request.AgentSessionID, request.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	select {
	case update, open := <-updates:
		if !open || update.Kind != "error" || !bytes.Contains(update.Payload, []byte(`"code":"acp_eof"`)) {
			t.Fatalf("EOF after Cancel was fabricated as stop proof: %+v, open=%v", update, open)
		}
	case <-ctx.Done():
		t.Fatal("EOF failed to end Prompt")
	}
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("EOF produced more than one outcome")
		}
	case <-ctx.Done():
		t.Fatal("EOF did not close stream")
	}
	request.AgentTurnID = "next-turn"
	if _, err := a.Prompt(ctx, request); err == nil {
		t.Fatal("EOF without cancelled response allowed next Prompt")
	}
}

func testCancelledPromptScope(t *testing.T, scenario string, prefix int) {
	root := t.TempDir()
	var trace traceBuffer
	a, ctx := newPeerAdapter(t, root, scenario, &trace)
	openPeer(t, a, ctx, root)
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	oldUpdates, err := a.Prompt(promptCtx, promptRequest())
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		decoder := json.NewDecoder(bytes.NewReader(trace.Bytes()))
		count := 0
		for {
			var record struct {
				Type  string `json:"type"`
				Frame string `json:"frame"`
			}
			if decoder.Decode(&record) != nil {
				break
			}
			if record.Type == "acp.raw_frame" && strings.Contains(record.Frame, `"method":"session/update"`) {
				count++
			}
		}
		if count == prefix {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("old notification prefix never reached raw tee")
		}
	}
	// At most 128 updates are buffered; the remaining admitted callbacks are
	// blocked/queued when the exact Prompt context is cancelled.
	cancelPrompt()
	for {
		select {
		case _, open := <-oldUpdates:
			if !open {
				goto closed
			}
		case <-ctx.Done():
			t.Fatal("cancelled Prompt channel did not close")
		}
	}
closed:
	request := application.AgentPromptRequest{AgentSessionID: "fixture-session", AgentTurnID: "next-turn", Content: "next"}
	if _, err := a.Prompt(ctx, request); err == nil {
		t.Fatal("ctx-aborted Prompt without real stop proof allowed session reuse while old raw frame waited for credit")
	}
}

func TestCoreFragmentedAndUnknownFramesAreTracedBeforeDelivery(t *testing.T) {
	root := t.TempDir()
	var trace traceBuffer
	a, ctx := newPeerAdapter(t, root, "frames", &trace)
	openPeer(t, a, ctx, root)
	initial, err := os.ReadFile(filepath.Join(root, "initialize-frame"))
	if err != nil || !hasFrame(trace.Bytes(), string(initial), "acp.raw_frame") {
		t.Fatalf("fragmented initialize raw frame missing: %v", err)
	}
	ch, err := a.Prompt(ctx, promptRequest())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for u := range ch {
		kinds = append(kinds, u.Kind)
		if u.Kind == "agent_message_chunk" && !hasFrame(trace.Bytes(), knownFrame, "acp.raw_frame") {
			t.Fatal("typed update delivered before literal Trace frame")
		}
	}
	if len(kinds) != 2 || kinds[0] != "agent_message_chunk" || kinds[1] != "terminal" {
		t.Fatalf("kinds %v", kinds)
	}
	if !hasFrame(trace.Bytes(), unknownFrame, "acp.unknown_update") {
		t.Fatal("unknown literal frame absent")
	}
}
func hasFrame(content []byte, frame, kind string) bool {
	d := json.NewDecoder(bytes.NewReader(content))
	for {
		var e struct {
			Type  string `json:"type"`
			Frame string `json:"frame"`
		}
		if d.Decode(&e) != nil {
			return false
		}
		if e.Type == kind && e.Frame == frame {
			return true
		}
	}
}
