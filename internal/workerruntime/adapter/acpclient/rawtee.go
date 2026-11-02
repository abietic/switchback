package acpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	directionClientToAgent = "client_to_agent"
	directionAgentToClient = "agent_to_client"
	maxRawFrameBytes       = 4 << 20
	proxyTokenMarker       = "[REDACTED:litellm_proxy_token]"
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

type rawTraceEvent struct {
	Type       string `json:"type"`
	Direction  string `json:"direction"`
	Frame      string `json:"frame"`
	UpdateType string `json:"update_type,omitempty"`
}

type traceRecorder struct {
	mu          sync.Mutex
	destination io.Writer
	proxyToken  string
}

func newTraceRecorder(destination io.Writer) *traceRecorder {
	return &traceRecorder{destination: destination}
}

func (r *traceRecorder) setProxyToken(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proxyToken = token
}

func (r *traceRecorder) record(event rawTraceEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destination == nil {
		return fmt.Errorf("ACP trace destination is required")
	}
	if event.Type == "" || event.Direction == "" {
		return fmt.Errorf("ACP trace event is incomplete")
	}
	if r.proxyToken != "" {
		event.Frame = strings.ReplaceAll(event.Frame, r.proxyToken, proxyTokenMarker)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode ACP trace event: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := writeAll(r.destination, encoded); err != nil {
		return fmt.Errorf("append ACP trace event: %w", err)
	}
	return nil
}

type frameWriter struct {
	mu        sync.Mutex
	recorder  *traceRecorder
	direction string
	pending   []byte
	poisoned  bool
}

func newFrameWriter(recorder *traceRecorder, direction string) *frameWriter {
	return &frameWriter{recorder: recorder, direction: direction}
}

func (w *frameWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return 0, fmt.Errorf("ACP raw frame writer is poisoned")
	}
	w.pending = append(w.pending, content...)
	if len(w.pending) > maxRawFrameBytes && !bytes.Contains(w.pending, []byte{'\n'}) {
		w.poison()
		return 0, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
	}
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:newline+1]...)
		if len(frame) > maxRawFrameBytes {
			w.poison()
			return 0, fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
		}
		if err := recordRawFrame(w.recorder, w.direction, frame); err != nil {
			w.poison()
			return 0, err
		}
		w.pending = w.pending[newline+1:]
	}
	return len(content), nil
}

func (w *frameWriter) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return fmt.Errorf("ACP raw frame writer is poisoned")
	}
	if len(w.pending) == 0 {
		return nil
	}
	if len(w.pending) > maxRawFrameBytes {
		w.poison()
		return fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
	}
	if err := recordRawFrame(w.recorder, w.direction, w.pending); err != nil {
		w.poison()
		return err
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
	recorder  *traceRecorder
	direction string
	pending   []byte
	poisoned  bool
}

func newRecordingWriter(target io.Writer, recorder *traceRecorder, direction string) *recordingWriter {
	return &recordingWriter{target: target, recorder: recorder, direction: direction}
}

func (w *recordingWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.poisoned {
		return 0, fmt.Errorf("ACP outbound frame writer is poisoned")
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
			w.poison()
			return currentWriteProgress(processed, previousPending, len(content)),
				fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
		}
		if err := recordRawFrame(w.recorder, w.direction, frame); err != nil {
			w.poison()
			return currentWriteProgress(processed, previousPending, len(content)), err
		}
		delivered, err := writeAll(w.target, frame)
		if err != nil {
			w.poison()
			return currentWriteProgress(processed+delivered, previousPending, len(content)),
				fmt.Errorf("write ACP peer input: %w", err)
		}
		w.pending = w.pending[newline+1:]
		processed += len(frame)
	}
	return len(content), nil
}

func (w *recordingWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.poison()
}

func (w *recordingWriter) poison() {
	w.poisoned = true
	w.pending = nil
}

func pumpRecordedOutput(source io.Reader, target *io.PipeWriter, sink *frameWriter) error {
	buffer := make([]byte, 32<<10)
	pending := make([]byte, 0, len(buffer))
	delivering := true
	deliverFrame := func(frame []byte) {
		if !delivering {
			return
		}
		if delivered, err := writeAll(target, frame); err != nil || delivered != len(frame) {
			_ = target.Close()
			delivering = false
		}
	}
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			for {
				newline := bytes.IndexByte(pending, '\n')
				if newline < 0 {
					if len(pending) > maxRawFrameBytes {
						err := fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
						_ = target.CloseWithError(err)
						_, _ = io.Copy(io.Discard, source)
						return err
					}
					break
				}
				frame := append([]byte(nil), pending[:newline+1]...)
				if len(frame) > maxRawFrameBytes {
					err := fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
					_ = target.CloseWithError(err)
					_, _ = io.Copy(io.Discard, source)
					return err
				}
				if _, err := sink.Write(frame); err != nil {
					_ = target.CloseWithError(err)
					_, _ = io.Copy(io.Discard, source)
					return err
				}
				if _, unknown := unknownSessionUpdate(frame); !unknown {
					deliverFrame(frame)
				}
				pending = pending[newline+1:]
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if len(pending) > 0 {
					if len(pending) > maxRawFrameBytes {
						err := fmt.Errorf("ACP raw frame exceeds %d bytes", maxRawFrameBytes)
						_ = target.CloseWithError(err)
						return err
					}
					if _, err := sink.Write(pending); err != nil {
						_ = target.CloseWithError(err)
						return err
					}
					if err := sink.finish(); err != nil {
						_ = target.CloseWithError(err)
						return err
					}
					if _, unknown := unknownSessionUpdate(pending); !unknown {
						deliverFrame(pending)
					}
				}
				if delivering {
					_ = target.Close()
				}
				return nil
			}
			_ = target.CloseWithError(readErr)
			return fmt.Errorf("read ACP child stdout")
		}
	}
}

func recordRawFrame(recorder *traceRecorder, direction string, frame []byte) error {
	if recorder == nil {
		return fmt.Errorf("ACP trace recorder is required")
	}
	if err := recorder.record(rawTraceEvent{
		Type: "acp.raw_frame", Direction: direction, Frame: string(frame),
	}); err != nil {
		return err
	}
	if direction != directionAgentToClient {
		return nil
	}
	updateType, unknown := unknownSessionUpdate(frame)
	if !unknown {
		return nil
	}
	return recorder.record(rawTraceEvent{
		Type: "acp.unknown_update", Direction: direction,
		Frame: string(frame), UpdateType: updateType,
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
		envelope.Method != "session/update" || envelope.Params.Update.SessionUpdate == "" {
		return "", false
	}
	_, known := knownSessionUpdates[envelope.Params.Update.SessionUpdate]
	return envelope.Params.Update.SessionUpdate, !known
}

func writeAll(destination io.Writer, content []byte) (int, error) {
	total := 0
	for len(content) > 0 {
		count, err := destination.Write(content)
		if count < 0 || count > len(content) {
			return total, io.ErrShortWrite
		}
		total += count
		content = content[count:]
		if err != nil {
			return total, err
		}
		if count == 0 {
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
