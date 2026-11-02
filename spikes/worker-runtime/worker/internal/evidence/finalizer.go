// Package evidence builds the four immutable objects for one AgentTurn.
package evidence

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

const maxJSONLRecordBytes = 32 << 20

type Input struct {
	Identity        sharedevidence.TurnIdentity
	ServerTimestamp time.Time
	WorkerTimestamp time.Time
	BeforeTree      string
	AfterTree       string
	RawACPJSONL     []byte
	Patch           []byte
	NoFilesChanged  bool
	Secrets         redact.Secrets
}

type Object struct {
	Kind        string
	ContentType string
	Payload     []byte
	SHA256      string
	Bytes       int64
}

type Bundle struct {
	Identity   sharedevidence.TurnIdentity
	BeforeTree string
	AfterTree  string
	Objects    []Object
}

type rawTraceEvent struct {
	Type       string `json:"type"`
	Direction  string `json:"direction,omitempty"`
	Frame      string `json:"frame,omitempty"`
	UpdateType string `json:"update_type,omitempty"`
}

type normalizedRecord struct {
	SchemaVersion   string                      `json:"schema_version"`
	Sequence        uint64                      `json:"sequence"`
	ServerTimestamp string                      `json:"server_timestamp"`
	WorkerTimestamp string                      `json:"worker_timestamp"`
	Identity        sharedevidence.TurnIdentity `json:"identity"`
	Kind            string                      `json:"kind"`
	Payload         any                         `json:"payload"`
}

type rpcFrame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type sessionUpdateParams struct {
	Update json.RawMessage `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type verification struct {
	ID      string
	Title   string
	Command string
	Status  string
}

func Build(input Input) (Bundle, error) {
	if err := validateInput(input); err != nil {
		return Bundle{}, err
	}
	events, err := decodeRawTrace(input.RawACPJSONL)
	if err != nil {
		return Bundle{}, err
	}
	normalized, err := buildNormalized(input, events)
	if err != nil {
		return Bundle{}, err
	}
	report, err := buildReport(events, input.NoFilesChanged)
	if err != nil {
		return Bundle{}, err
	}

	redactor := redact.New(input.Secrets)
	objects := []Object{
		newObject(sharedevidence.KindReport, "text/markdown; charset=utf-8", []byte(redactor.Redact(report))),
		newObject(sharedevidence.KindRawACP, "application/x-ndjson", []byte(redactor.Redact(string(input.RawACPJSONL)))),
		newObject(sharedevidence.KindTrace, "application/x-ndjson", []byte(redactor.Redact(string(normalized)))),
		newObject(sharedevidence.KindPatch, "text/x-diff; charset=utf-8", []byte(redactor.Redact(string(input.Patch)))),
	}
	return Bundle{
		Identity:   input.Identity,
		BeforeTree: input.BeforeTree,
		AfterTree:  input.AfterTree,
		Objects:    objects,
	}, nil
}

func validateInput(input Input) error {
	identities := []string{
		input.Identity.WorkerSessionID,
		input.Identity.WorkerRunID,
		input.Identity.RuntimeInstanceID,
		input.Identity.AgentSessionID,
		input.Identity.AgentTurnID,
		input.Identity.TraceID,
	}
	for _, identity := range identities {
		if strings.TrimSpace(identity) == "" {
			return fmt.Errorf("all turn identities are required")
		}
	}
	if input.Identity.RuntimeGeneration == 0 {
		return fmt.Errorf("runtime generation is required")
	}
	if input.ServerTimestamp.IsZero() || input.WorkerTimestamp.IsZero() {
		return fmt.Errorf("server and worker timestamps are required")
	}
	if !validObjectID(input.BeforeTree) || !validObjectID(input.AfterTree) {
		return fmt.Errorf("before and after tree IDs must be 40 or 64 lowercase hexadecimal characters")
	}
	if len(input.RawACPJSONL) == 0 {
		return fmt.Errorf("raw ACP JSONL is required")
	}
	if len(input.Patch) == 0 && !input.NoFilesChanged {
		return fmt.Errorf("empty patch requires an explicit no files changed terminal report")
	}
	if len(input.Patch) > 0 && input.NoFilesChanged {
		return fmt.Errorf("nonempty patch contradicts no files changed")
	}
	return nil
}

func decodeRawTrace(content []byte) ([]rawTraceEvent, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64<<10), maxJSONLRecordBytes)
	var events []rawTraceEvent
	for scanner.Scan() {
		var event rawTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode raw ACP JSONL record %d: %w", len(events)+1, err)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("raw ACP JSONL record %d has no type", len(events)+1)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan raw ACP JSONL: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("raw ACP JSONL contains no records")
	}
	return events, nil
}

func buildNormalized(input Input, events []rawTraceEvent) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for index, event := range events {
		payload := any(map[string]any{"update_type": event.UpdateType})
		if event.Type == "acp.raw_frame" {
			var frame any
			if err := json.Unmarshal([]byte(strings.TrimSpace(event.Frame)), &frame); err != nil {
				return nil, fmt.Errorf("decode ACP frame in record %d: %w", index+1, err)
			}
			payload = map[string]any{
				"direction": event.Direction,
				"frame":     frame,
			}
		}
		record := normalizedRecord{
			SchemaVersion:   sharedevidence.NormalizedTraceSchemaVersion,
			Sequence:        uint64(index + 1),
			ServerTimestamp: input.ServerTimestamp.UTC().Format(time.RFC3339Nano),
			WorkerTimestamp: input.WorkerTimestamp.UTC().Format(time.RFC3339Nano),
			Identity:        input.Identity,
			Kind:            event.Type,
			Payload:         payload,
		}
		if err := encoder.Encode(record); err != nil {
			return nil, fmt.Errorf("encode normalized trace record %d: %w", index+1, err)
		}
	}
	return output.Bytes(), nil
}

func buildReport(events []rawTraceEvent, noFilesChanged bool) (string, error) {
	var message strings.Builder
	verifications := make(map[string]*verification)
	var verificationOrder []string
	for index, event := range events {
		if event.Type != "acp.raw_frame" || event.Direction != "agent_to_client" {
			continue
		}
		var frame rpcFrame
		if err := json.Unmarshal([]byte(strings.TrimSpace(event.Frame)), &frame); err != nil {
			return "", fmt.Errorf("decode report ACP frame in record %d: %w", index+1, err)
		}
		if frame.Method != "session/update" {
			continue
		}
		var params sessionUpdateParams
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			return "", fmt.Errorf("decode session update params in record %d: %w", index+1, err)
		}
		var update sessionUpdate
		if err := json.Unmarshal(params.Update, &update); err != nil {
			return "", fmt.Errorf("decode session update in record %d: %w", index+1, err)
		}
		switch update.SessionUpdate {
		case "agent_message_chunk":
			var content contentBlock
			if err := json.Unmarshal(update.Content, &content); err != nil {
				return "", fmt.Errorf("decode agent message in record %d: %w", index+1, err)
			}
			if content.Type == "text" {
				message.WriteString(content.Text)
			}
		case "tool_call":
			call := &verification{
				ID:      update.ToolCallID,
				Title:   strings.TrimSpace(update.Title),
				Command: commandFromRawInput(update.RawInput),
				Status:  strings.TrimSpace(update.Status),
			}
			if call.ID == "" {
				call.ID = fmt.Sprintf("record-%d", index+1)
			}
			if _, exists := verifications[call.ID]; !exists {
				verificationOrder = append(verificationOrder, call.ID)
			}
			verifications[call.ID] = call
		case "tool_call_update":
			call := verifications[update.ToolCallID]
			if call == nil {
				call = &verification{ID: update.ToolCallID}
				verifications[update.ToolCallID] = call
				verificationOrder = append(verificationOrder, update.ToolCallID)
			}
			if title := strings.TrimSpace(update.Title); title != "" {
				call.Title = title
			}
			if command := commandFromRawInput(update.RawInput); command != "" {
				call.Command = command
			}
			if status := strings.TrimSpace(update.Status); status != "" {
				call.Status = status
			}
		}
	}

	var report strings.Builder
	report.WriteString(strings.TrimSpace(message.String()))
	if noFilesChanged {
		if report.Len() > 0 {
			report.WriteString("\n\n")
		}
		report.WriteString("No files changed.")
	}
	if report.Len() > 0 {
		report.WriteString("\n\n")
	}
	report.WriteString("## Verification Appendix\n")
	written := 0
	for _, id := range verificationOrder {
		call := verifications[id]
		if call == nil || !isVerification(call) {
			continue
		}
		report.WriteString("- ")
		if call.Title != "" {
			report.WriteString(call.Title)
		}
		if call.Command != "" {
			if call.Title != "" {
				report.WriteString(": ")
			}
			fmt.Fprintf(&report, "`%s`", strings.ReplaceAll(call.Command, "`", "'"))
		}
		if call.Status != "" {
			fmt.Fprintf(&report, " (%s)", call.Status)
		}
		report.WriteByte('\n')
		written++
	}
	if written == 0 {
		report.WriteString("- No commands or tests were observed in ACP updates.\n")
	}
	return report.String(), nil
}

func isVerification(call *verification) bool {
	if call.Command != "" {
		return true
	}
	title := strings.ToLower(call.Title)
	for _, marker := range []string{"test", "verify", "verification", "lint", "vet", "build"} {
		if strings.Contains(title, marker) {
			return true
		}
	}
	return false
}

func commandFromRawInput(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		switch value := input[key].(type) {
		case string:
			return strings.TrimSpace(value)
		case []any:
			parts := make([]string, 0, len(value))
			for _, part := range value {
				if text, ok := part.(string); ok {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

func newObject(kind, contentType string, payload []byte) Object {
	digest := sha256.Sum256(payload)
	return Object{
		Kind:        kind,
		ContentType: contentType,
		Payload:     payload,
		SHA256:      hex.EncodeToString(digest[:]),
		Bytes:       int64(len(payload)),
	}
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}
