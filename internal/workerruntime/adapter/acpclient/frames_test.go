package acpclient_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
