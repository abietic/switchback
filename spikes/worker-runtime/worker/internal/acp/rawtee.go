package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"switchback.local/spikes/worker-runtime/worker/internal/trace"
)

const (
	directionClientToAgent = "client_to_agent"
	directionAgentToClient = "agent_to_client"
	maxRawFrameBytes       = 4 << 20
)

var (
	errRawFrameWriterPoisoned = errors.New("ACP raw frame writer is poisoned")
	errRawFrameRecording      = errors.New("ACP raw frame recording failed")
	errPeerInputWrite         = errors.New("ACP peer input write failed")
	errACPStdoutRecording     = errors.New("ACP stdout recording failed")
	errACPStdoutDrain         = errors.New("ACP stdout drain failed")
)

var knownSessionUpdates = map[string]struct{}{
	"agent_message_chunk":       {},
	"agent_thought_chunk":       {},
	"available_commands_update": {},
	"config_option_update":      {},
	"current_mode_update":       {},
	"plan":                      {},
	"plan_removed":              {},
	"plan_update":               {},
	"session_info_update":       {},
	"tool_call":                 {},
	"tool_call_update":          {},
	"usage_update":              {},
	"user_message_chunk":        {},
}

type frameWriter struct {
	mu        sync.Mutex
	recorder  trace.Recorder
	direction string
	pending   []byte
	poisoned  bool
}

type frameFinalizer interface {
	finish() error
}

// NewFrameWriter records complete newline-delimited JSON-RPC frames.
func NewFrameWriter(recorder trace.Recorder, direction string) io.Writer {
	return &frameWriter{recorder: recorder, direction: direction}
}

func (w *frameWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return 0, errRawFrameWriterPoisoned
	}
	if w.recorder == nil {
		w.poison()
		return 0, errRawFrameRecording
	}
	previousPending := len(w.pending)
	w.pending = append(w.pending, content...)
	if len(w.pending) > maxRawFrameBytes && !bytes.Contains(w.pending, []byte{'\n'}) {
		w.poison()
		return 0, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
	}
	processed := 0
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:newline+1]...)
		if len(frame) > maxRawFrameBytes {
			written := currentWriteProgress(processed, previousPending, len(content))
			w.poison()
			return written, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
		}
		if err := recordFrame(w.recorder, w.direction, frame); err != nil {
			written := currentWriteProgress(processed, previousPending, len(content))
			w.poison()
			return written, errRawFrameRecording
		}
		w.pending = w.pending[newline+1:]
		processed += len(frame)
	}
	return len(content), nil
}

func (w *frameWriter) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return errRawFrameWriterPoisoned
	}
	if len(w.pending) == 0 {
		return nil
	}
	if len(w.pending) > maxRawFrameBytes {
		w.poison()
		return fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
	}
	frame := append([]byte(nil), w.pending...)
	if err := recordFrame(w.recorder, w.direction, frame); err != nil {
		w.poison()
		return errRawFrameRecording
	}
	w.pending = nil
	return nil
}

func (w *frameWriter) poison() {
	w.poisoned = true
	w.pending = nil
}

type recordingWriter struct {
	mu        sync.Mutex
	target    io.Writer
	recorder  trace.Recorder
	direction string
	pending   []byte
	poisoned  bool
}

// NewRecordingWriter writes to target and records each successfully written frame.
func NewRecordingWriter(target io.Writer, recorder trace.Recorder, direction string) io.Writer {
	return newRecordingWriter(target, recorder, direction)
}

func newRecordingWriter(
	target io.Writer,
	recorder trace.Recorder,
	direction string,
) *recordingWriter {
	return &recordingWriter{
		target:    target,
		recorder:  recorder,
		direction: direction,
	}
}

func (w *recordingWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.poison()
}

func (w *recordingWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return 0, errRawFrameWriterPoisoned
	}
	if w.target == nil || w.recorder == nil {
		w.poison()
		return 0, errPeerInputWrite
	}
	previousPending := len(w.pending)
	w.pending = append(w.pending, content...)
	if len(w.pending) > maxRawFrameBytes && !bytes.Contains(w.pending, []byte{'\n'}) {
		w.poison()
		return 0, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
	}
	processed := 0
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:newline+1]...)
		if len(frame) > maxRawFrameBytes {
			written := currentWriteProgress(processed, previousPending, len(content))
			w.poison()
			return written, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
		}
		if err := recordFrame(w.recorder, w.direction, frame); err != nil {
			written := currentWriteProgress(processed, previousPending, len(content))
			w.poison()
			return written, errRawFrameRecording
		}
		delivered, err := writeAll(w.target, frame)
		if err != nil {
			written := currentWriteProgress(
				processed+delivered,
				previousPending,
				len(content),
			)
			w.poison()
			return written, errPeerInputWrite
		}
		w.pending = w.pending[newline+1:]
		processed += len(frame)
	}
	return len(content), nil
}

func (w *recordingWriter) poison() {
	w.poisoned = true
	w.pending = nil
}

func writeAll(target io.Writer, content []byte) (int, error) {
	total := 0
	for len(content) > 0 {
		written, err := target.Write(content)
		if written < 0 {
			written = 0
		}
		if written > len(content) {
			written = len(content)
			err = io.ErrShortWrite
		}
		if written > 0 {
			content = content[written:]
			total += written
		}
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func currentWriteProgress(processed, previousPending, contentLength int) int {
	current := processed - previousPending
	if current < 0 {
		return 0
	}
	if current > contentLength {
		return contentLength
	}
	return current
}

func pumpRecordedOutput(
	source io.Reader,
	target *io.PipeWriter,
	rawSink io.Writer,
) error {
	buffer := make([]byte, 32<<10)
	pending := make([]byte, 0, len(buffer))
	recording := true
	delivering := true
	var result error
	failRecording := func() {
		recording = false
		pending = nil
		result = errACPStdoutRecording
		if delivering {
			_ = target.CloseWithError(errACPStdoutRecording)
			delivering = false
		}
	}
	deliverFrame := func(frame []byte) {
		if !delivering {
			return
		}
		delivered, deliverErr := writeAll(target, frame)
		if deliverErr != nil || delivered != len(frame) {
			_ = target.Close()
			delivering = false
		}
	}
	recordAndDeliver := func(frame []byte, final bool) bool {
		recorded, recordErr := rawSink.Write(frame)
		if recordErr != nil || recorded != len(frame) {
			failRecording()
			return false
		}
		if final {
			if finalizer, ok := rawSink.(frameFinalizer); ok {
				if err := finalizer.finish(); err != nil {
					failRecording()
					return false
				}
			}
		}
		deliverFrame(frame)
		return true
	}
	for {
		read, readErr := source.Read(buffer)
		if read > 0 && recording {
			pending = append(pending, buffer[:read]...)
			for recording {
				newline := bytes.IndexByte(pending, '\n')
				if newline < 0 {
					if len(pending) > maxRawFrameBytes {
						failRecording()
					}
					break
				}
				frameLength := newline + 1
				if frameLength > maxRawFrameBytes {
					failRecording()
					break
				}
				frame := append([]byte(nil), pending[:frameLength]...)
				pending = pending[frameLength:]
				if !recordAndDeliver(frame, false) {
					break
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF && recording && len(pending) > 0 {
				if len(pending) > maxRawFrameBytes {
					failRecording()
				} else {
					frame := append([]byte(nil), pending...)
					pending = nil
					recordAndDeliver(frame, true)
				}
			}
			if readErr != io.EOF && result == nil {
				result = errACPStdoutDrain
			}
			break
		}
	}
	if delivering {
		if result != nil {
			_ = target.CloseWithError(result)
		} else {
			_ = target.Close()
		}
	}
	return result
}

func recordFrame(recorder trace.Recorder, direction string, frame []byte) error {
	if err := recorder.Record(trace.Event{
		Type:      trace.TypeRawFrame,
		Direction: direction,
		Frame:     string(frame),
	}); err != nil {
		return err
	}
	if direction != directionAgentToClient {
		return nil
	}
	updateType, ok := unknownSessionUpdate(frame)
	if !ok {
		return nil
	}
	return recorder.Record(trace.Event{
		Type:       trace.TypeUnknownUpdate,
		Direction:  direction,
		Frame:      string(frame),
		UpdateType: updateType,
	})
}

func unknownSessionUpdate(frame []byte) (string, bool) {
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(frame), &envelope); err != nil ||
		envelope.Method != "session/update" ||
		envelope.Params.Update.SessionUpdate == "" {
		return "", false
	}
	updateType := envelope.Params.Update.SessionUpdate
	_, known := knownSessionUpdates[updateType]
	return updateType, !known
}
