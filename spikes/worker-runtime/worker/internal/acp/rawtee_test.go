package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"switchback.local/spikes/worker-runtime/worker/internal/trace"
)

func TestRecordingWriterRecordsBeforePeerCanDecode(t *testing.T) {
	t.Parallel()

	recorded := false
	recorder := recorderFunc(func(event trace.Event) error {
		if event.Type != trace.TypeRawFrame || event.Frame != "{\"jsonrpc\":\"2.0\"}\n" {
			return fmt.Errorf("unexpected event: %+v", event)
		}
		recorded = true
		return nil
	})
	target := writerFunc(func(content []byte) (int, error) {
		if !recorded {
			return 0, fmt.Errorf("peer received bytes before trace recording")
		}
		return len(content), nil
	})

	writer := NewRecordingWriter(target, recorder, directionClientToAgent)
	if _, err := writer.Write([]byte("{\"jsonrpc\":\"2.0\"}\n")); err != nil {
		t.Fatalf("write recorded frame: %v", err)
	}
}

func TestRecordingWriterHandlesShortWriteProgress(t *testing.T) {
	t.Parallel()

	frame := []byte("{\"id\":1}\n")
	targetCalls := 0
	target := writerFunc(func(content []byte) (int, error) {
		targetCalls++
		if targetCalls == 1 {
			return 3, nil
		}
		return len(content), nil
	})
	recordCalls := 0
	recorder := recorderFunc(func(trace.Event) error {
		recordCalls++
		return nil
	})

	writer := NewRecordingWriter(target, recorder, directionClientToAgent)
	if written, err := writer.Write(frame); err != nil || written != len(frame) {
		t.Fatalf("Write() = %d, %v, want %d, nil", written, err, len(frame))
	}
	if targetCalls != 2 || recordCalls != 1 {
		t.Fatalf("calls = target:%d recorder:%d, want 2 and 1", targetCalls, recordCalls)
	}
}

func TestRecordingWriterPoisonsAfterTerminalFailure(t *testing.T) {
	t.Parallel()

	frame := []byte("{\"id\":1}\n")
	tests := []struct {
		name            string
		content         []byte
		target          func([]byte) (int, error)
		recorder        func(trace.Event) error
		wantWritten     int
		wantTargetCalls int
		wantRecordCalls int
	}{
		{
			name:    "partial target error",
			content: frame,
			target: func([]byte) (int, error) {
				return 3, io.ErrClosedPipe
			},
			recorder:        func(trace.Event) error { return nil },
			wantWritten:     3,
			wantTargetCalls: 1,
			wantRecordCalls: 1,
		},
		{
			name:    "zero target write",
			content: frame,
			target: func([]byte) (int, error) {
				return 0, nil
			},
			recorder:        func(trace.Event) error { return nil },
			wantTargetCalls: 1,
			wantRecordCalls: 1,
		},
		{
			name:    "recorder error",
			content: frame,
			target: func(content []byte) (int, error) {
				return len(content), nil
			},
			recorder: func(trace.Event) error {
				return io.ErrClosedPipe
			},
			wantRecordCalls: 1,
		},
		{
			name:            "oversize pending frame",
			content:         []byte(strings.Repeat("x", maxRawFrameBytes+1)),
			target:          func(content []byte) (int, error) { return len(content), nil },
			recorder:        func(trace.Event) error { return nil },
			wantTargetCalls: 0,
			wantRecordCalls: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetCalls := 0
			recordCalls := 0
			writer := NewRecordingWriter(
				writerFunc(func(content []byte) (int, error) {
					targetCalls++
					return test.target(content)
				}),
				recorderFunc(func(event trace.Event) error {
					recordCalls++
					return test.recorder(event)
				}),
				directionClientToAgent,
			)

			written, err := writer.Write(test.content)
			if err == nil || written != test.wantWritten {
				t.Fatalf("first Write() = %d, %v, want %d, error", written, err, test.wantWritten)
			}
			if targetCalls != test.wantTargetCalls || recordCalls != test.wantRecordCalls {
				t.Fatalf(
					"first calls = target:%d recorder:%d, want %d and %d",
					targetCalls,
					recordCalls,
					test.wantTargetCalls,
					test.wantRecordCalls,
				)
			}
			if written, err := writer.Write(frame); err == nil || written != 0 {
				t.Fatalf("poisoned Write() = %d, %v, want 0, error", written, err)
			}
			if targetCalls != test.wantTargetCalls || recordCalls != test.wantRecordCalls {
				t.Fatalf(
					"poisoned reuse changed calls to target:%d recorder:%d",
					targetCalls,
					recordCalls,
				)
			}
		})
	}
}

func TestFrameWriterPoisonsAfterRecorderOrOversizeFailure(t *testing.T) {
	t.Parallel()

	frame := []byte("{\"id\":1}\n")
	tests := []struct {
		name            string
		content         []byte
		recorder        func(trace.Event) error
		wantRecordCalls int
	}{
		{
			name:    "recorder error",
			content: frame,
			recorder: func(trace.Event) error {
				return io.ErrClosedPipe
			},
			wantRecordCalls: 1,
		},
		{
			name:            "oversize pending frame",
			content:         []byte(strings.Repeat("x", maxRawFrameBytes+1)),
			recorder:        func(trace.Event) error { return nil },
			wantRecordCalls: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recordCalls := 0
			writer := NewFrameWriter(
				recorderFunc(func(event trace.Event) error {
					recordCalls++
					return test.recorder(event)
				}),
				directionAgentToClient,
			)
			if written, err := writer.Write(test.content); err == nil || written != 0 {
				t.Fatalf("first Write() = %d, %v, want 0, error", written, err)
			}
			if recordCalls != test.wantRecordCalls {
				t.Fatalf("record calls = %d, want %d", recordCalls, test.wantRecordCalls)
			}
			if written, err := writer.Write(frame); err == nil || written != 0 {
				t.Fatalf("poisoned Write() = %d, %v, want 0, error", written, err)
			}
			if recordCalls != test.wantRecordCalls {
				t.Fatalf("poisoned reuse changed recorder calls to %d", recordCalls)
			}
		})
	}
}

func TestFrameWriterPreservesCompleteFramingAndUnknownUpdate(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	recorder, err := trace.NewJSONLRecorder(&output)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	writer := NewFrameWriter(recorder, directionAgentToClient)
	first := "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"update\":{\"sessionUpdate\":\"future_update\"}}}\n"
	second := "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"
	if _, err := writer.Write([]byte(first[:17])); err != nil {
		t.Fatalf("write first fragment: %v", err)
	}
	if _, err := writer.Write([]byte(first[17:] + second)); err != nil {
		t.Fatalf("write remaining frames: %v", err)
	}

	records := decodeRawTrace(t, output.Bytes())
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3", len(records))
	}
	if records[0].Type != trace.TypeRawFrame || records[0].Frame != first {
		t.Fatalf("first raw record = %+v", records[0])
	}
	if records[1].Type != trace.TypeUnknownUpdate ||
		records[1].UpdateType != "future_update" ||
		records[1].Frame != first {
		t.Fatalf("unknown update record = %+v", records[1])
	}
	if records[2].Type != trace.TypeRawFrame || records[2].Frame != second {
		t.Fatalf("second raw record = %+v", records[2])
	}
}

func TestKnownSessionUpdateDoesNotProduceUnknownRecord(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	recorder, err := trace.NewJSONLRecorder(&output)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	frame := "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"ok\"}}}}\n"
	if _, err := NewFrameWriter(recorder, directionAgentToClient).Write([]byte(frame)); err != nil {
		t.Fatalf("record known update: %v", err)
	}
	records := decodeRawTrace(t, output.Bytes())
	if len(records) != 1 || records[0].Type != trace.TypeRawFrame {
		t.Fatalf("known update records = %+v", records)
	}
}

func TestPumpRecordedOutputRecordsBeforeSDKDelivery(t *testing.T) {
	t.Parallel()

	frame := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n")
	var recorded atomic.Bool
	rawSink := writerFunc(func(content []byte) (int, error) {
		recorded.Store(true)
		return len(content), nil
	})
	sdkReader, sdkWriter := io.Pipe()
	readResult := make(chan error, 1)
	go func() {
		content := make([]byte, len(frame))
		if _, err := io.ReadFull(sdkReader, content); err != nil {
			readResult <- err
			return
		}
		if !recorded.Load() {
			readResult <- fmt.Errorf("SDK received bytes before raw recording")
			return
		}
		if !bytes.Equal(content, frame) {
			readResult <- fmt.Errorf("SDK content = %q, want %q", content, frame)
			return
		}
		readResult <- nil
	}()

	if err := pumpRecordedOutput(bytes.NewReader(frame), sdkWriter, rawSink); err != nil {
		t.Fatalf("pumpRecordedOutput() error = %v", err)
	}
	if err := <-readResult; err != nil {
		t.Fatal(err)
	}
}

func TestPumpRecordedOutputRecordsUnterminatedEOFFrameBeforeSDKDecode(t *testing.T) {
	t.Parallel()

	sdkReader, sdkWriter := io.Pipe()
	t.Cleanup(func() {
		_ = sdkReader.Close()
		_ = sdkWriter.Close()
	})
	recordedFrames := make(chan trace.Event, 1)
	sdkResult := make(chan sdkFrameObservation, 1)
	client := &rawBeforeSDKClient{
		protocolClient: &protocolClient{workspaceRoot: workspaceRoot},
		recordedFrames: recordedFrames,
		result:         sdkResult,
	}
	_ = sdk.NewClientSideConnection(client, io.Discard, sdkReader)
	rawSink := NewFrameWriter(recorderFunc(func(event trace.Event) error {
		recordedFrames <- event
		return nil
	}), directionAgentToClient)

	response, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": "session-one",
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": "unterminated final frame",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode SDK notification: %v", err)
	}
	if bytes.HasSuffix(response, []byte{'\n'}) {
		t.Fatal("test response unexpectedly has a trailing newline")
	}

	if err := pumpRecordedOutput(bytes.NewReader(response), sdkWriter, rawSink); err != nil {
		t.Fatalf("pumpRecordedOutput() error = %v", err)
	}
	select {
	case result := <-sdkResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.event.Frame != string(response) {
			t.Fatalf("recorded final frame = %q, want %q", result.event.Frame, response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SDK scanner did not accept the unterminated final frame")
	}
}

func TestPumpRecordedOutputDrainsAfterRecorderFailure(t *testing.T) {
	t.Parallel()

	source := &chunkReader{chunks: [][]byte{
		[]byte("{\"jsonrpc\":\"2.0\",\"id\":1}\n"),
		[]byte("{\"jsonrpc\":\"2.0\",\"id\":2}\n"),
	}}
	recordCalls := 0
	rawSink := writerFunc(func([]byte) (int, error) {
		recordCalls++
		return 0, fmt.Errorf("raw-provider-secret")
	})
	sdkReader, sdkWriter := io.Pipe()

	err := pumpRecordedOutput(source, sdkWriter, rawSink)
	if !errors.Is(err, errACPStdoutRecording) {
		t.Fatalf("pumpRecordedOutput() error = %v, want fixed recording error", err)
	}
	if strings.Contains(err.Error(), "raw-provider-secret") {
		t.Fatalf("pumpRecordedOutput() exposed recorder error: %v", err)
	}
	if source.readChunks != len(source.chunks) {
		t.Fatalf(
			"source chunks read = %d, want %d through EOF",
			source.readChunks,
			len(source.chunks),
		)
	}
	if recordCalls != 1 {
		t.Fatalf("record calls = %d, want one before drain/discard", recordCalls)
	}
	if _, readErr := io.ReadAll(sdkReader); !errors.Is(readErr, errACPStdoutRecording) {
		t.Fatalf("SDK relay error = %v, want fixed recording error", readErr)
	}
}

type recorderFunc func(trace.Event) error

func (f recorderFunc) Record(event trace.Event) error {
	return f(event)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(content []byte) (int, error) {
	return f(content)
}

type sdkFrameObservation struct {
	event trace.Event
	err   error
}

type rawBeforeSDKClient struct {
	*protocolClient
	recordedFrames <-chan trace.Event
	result         chan<- sdkFrameObservation
}

func (c *rawBeforeSDKClient) SessionUpdate(
	_ context.Context,
	_ sdk.SessionNotification,
) error {
	select {
	case event := <-c.recordedFrames:
		if event.Type != trace.TypeRawFrame ||
			event.Direction != directionAgentToClient {
			c.result <- sdkFrameObservation{err: fmt.Errorf("raw event = %+v", event)}
			return nil
		}
		c.result <- sdkFrameObservation{event: event}
	default:
		c.result <- sdkFrameObservation{
			err: fmt.Errorf("SDK decoded final frame before raw recording"),
		}
	}
	return nil
}

type chunkReader struct {
	chunks     [][]byte
	readChunks int
}

func (r *chunkReader) Read(content []byte) (int, error) {
	if r.readChunks == len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.readChunks]
	r.readChunks++
	return copy(content, chunk), nil
}

func decodeRawTrace(t *testing.T, content []byte) []trace.Event {
	t.Helper()
	var records []trace.Event
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		var event trace.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode trace event: %v", err)
		}
		records = append(records, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace: %v", err)
	}
	return records
}
