package localproduct

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
)

type continuousWorkerOutput struct {
	n      atomic.Int64
	closed atomic.Bool
}

func (r *continuousWorkerOutput) Read(p []byte) (int, error) {
	if r.closed.Load() {
		return 0, io.EOF
	}
	r.n.Add(int64(len(p)))
	return len(p), nil
}
func (r *continuousWorkerOutput) Close() error { r.closed.Store(true); return nil }
func TestTelemetryWorkerOutputRateWaitIsBoundedAndCancellable(t *testing.T) {
	r := &continuousWorkerOutput{}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	reason := consumeWorkerOutput(ctx, r, true, telemetry.Scope{}, func(telemetry.Event) {})
	if reason != workerOutputCancelled || !r.closed.Load() || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("rate wait cancellation failed: %s", reason)
	}
	if n := r.n.Load(); n > 80<<10 || n < 16<<10 {
		t.Fatalf("rate limit consumed %d bytes in 250ms", n)
	}
}

func workerOutputFrame(source byte, payload string) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = source
	binary.BigEndian.PutUint32(b[4:8], uint32(len(payload)))
	copy(b[8:], payload)
	return b
}

type fragmentedOutput struct{ io.Reader }

func (r fragmentedOutput) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestTelemetryWorkerOutputSuppressesFragmentedSensitiveBytes(t *testing.T) {
	const secret = "credential-SENTINEL-do-not-export"
	data := append(workerOutputFrame(1, secret), workerOutputFrame(2, "err")...)
	var events []telemetry.Event
	reason := consumeWorkerOutput(context.Background(), io.NopCloser(fragmentedOutput{bytes.NewReader(data)}), false, telemetry.Scope{WorkerSessionID: "s", RuntimeInstanceID: "r", RuntimeGeneration: 1}, func(e telemetry.Event) { events = append(events, e) })
	if reason != workerOutputEOF {
		t.Fatalf("reason %s", reason)
	}
	var stdout, stderr int64
	for _, e := range events {
		if e.OutputSource == telemetry.OutputStdout {
			stdout += *e.Bytes
		}
		if e.OutputSource == telemetry.OutputStderr {
			stderr += *e.Bytes
		}
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), secret) {
			t.Fatal("raw credential exported")
		}
	}
	if stdout != 33 || stderr != 3 {
		t.Fatalf("observed bytes: %d/%d", stdout, stderr)
	}
}

func TestTelemetryWorkerOutputRejectsMalformedFrames(t *testing.T) {
	for name, data := range map[string][]byte{
		"source": workerOutputFrame(9, "secret"), "reserved": {1, 1, 0, 0, 0, 0, 0, 0},
		"huge": {1, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff}, "header": {1, 0}, "truncated": {1, 0, 0, 0, 0, 0, 0, 5, 'x'},
	} {
		t.Run(name, func(t *testing.T) {
			reason := consumeWorkerOutput(context.Background(), io.NopCloser(bytes.NewReader(data)), false, telemetry.Scope{}, func(telemetry.Event) {})
			if reason != workerOutputInvalid {
				t.Fatalf("reason %s", reason)
			}
		})
	}
}

func TestTelemetryWorkerOutputCancellationClosesBlockedRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan workerOutputReason, 1)
	go func() { done <- consumeWorkerOutput(ctx, reader, false, telemetry.Scope{}, func(telemetry.Event) {}) }()
	cancel()
	select {
	case reason := <-done:
		if reason != workerOutputCancelled {
			t.Fatalf("reason %s", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close blocked reader")
	}
	if _, err := writer.Write([]byte("secret")); err == nil {
		t.Fatal("real reader still open")
	}
}

func TestTelemetryWorkerTTYAggregatesWhileReadIsBlocked(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan telemetry.Event, 4)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		consumeWorkerOutput(ctx, reader, true, telemetry.Scope{}, func(e telemetry.Event) { seen <- e })
	}()
	writer.Write([]byte("secret"))
	select {
	case e := <-seen:
		if e.OutputSource != telemetry.OutputCombined || *e.Bytes != 6 {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("missing periodic aggregation")
	}
	cancel()
	wg.Wait()
}
