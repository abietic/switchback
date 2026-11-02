package domain

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

const (
	TraceRecordSchemaVersion = "trace.record.v1"
	KindRawACP               = "trace-raw-acp"
	KindNormalized           = "trace-normalized"
	KindObservationSource    = "turn-observation-source"
	KindReport               = "report"
	KindPatch                = "patch"
	maxJSONLRecordBytes      = 32 << 20
)

// TurnIdentity is copied into every finalized Trace record and ArtifactRef.
type TurnIdentity struct {
	TenantID          string                        `json:"tenant_id"`
	UserID            string                        `json:"user_id"`
	WorkspaceID       string                        `json:"workspace_id"`
	TaskID            contractsv1.TaskID            `json:"task_id"`
	WorkerSessionID   contractsv1.WorkerSessionID   `json:"worker_session_id"`
	WorkerRunID       contractsv1.WorkerRunID       `json:"worker_run_id"`
	AgentSessionID    contractsv1.AgentSessionID    `json:"agent_session_id"`
	AgentTurnID       contractsv1.AgentTurnID       `json:"agent_turn_id"`
	RuntimeInstanceID contractsv1.RuntimeInstanceID `json:"runtime_instance_id"`
	RuntimeGeneration uint32                        `json:"runtime_generation"`
	RuntimeNetwork    RuntimeNetworkEvidence        `json:"runtime_network_profile"`
	TraceStreamID     string                        `json:"trace_stream_id"`
	LangfuseTraceID   string                        `json:"langfuse_trace_id"`
}

type RuntimeNetworkEvidence struct {
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	Digest            string `json:"digest"`
	EnforcementStatus string `json:"enforcement_status"`
}

type TraceRecord struct {
	SchemaVersion    string          `json:"schema_version"`
	Sequence         uint64          `json:"sequence"`
	ServerRecordedAt time.Time       `json:"server_recorded_at"`
	WorkerRecordedAt time.Time       `json:"worker_recorded_at"`
	Identity         TurnIdentity    `json:"identity"`
	Kind             string          `json:"kind"`
	Payload          json.RawMessage `json:"payload"`
}

type BuildTurnEvidenceInput struct {
	Identity               TurnIdentity
	ServerRecordedAt       time.Time
	WorkerRecordedAt       time.Time
	RawACPJSONL            []byte
	Patch                  []byte
	NoFilesChanged         bool
	ObservationSourceJSONL []byte
}

type EvidenceObject struct {
	Kind          string
	ContentType   string
	Payload       []byte
	Checksum      string
	SizeBytes     int64
	FirstSequence uint64
	LastSequence  uint64
	RecordCount   int64
}

type TurnEvidenceBundle struct {
	Identity        TurnIdentity
	Objects         []EvidenceObject
	FirstRecordedAt time.Time
	LastRecordedAt  time.Time
	LastSequence    uint64
}

type rawTraceEvent struct {
	Type       string `json:"type"`
	Direction  string `json:"direction,omitempty"`
	Frame      string `json:"frame,omitempty"`
	UpdateType string `json:"update_type,omitempty"`
}

type rpcFrame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
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

func BuildTurnEvidence(input BuildTurnEvidenceInput) (TurnEvidenceBundle, error) {
	if err := validateInput(input); err != nil {
		return TurnEvidenceBundle{}, err
	}
	events, err := decodeRawTrace(input.RawACPJSONL, input.Identity.AgentSessionID)
	if err != nil {
		return TurnEvidenceBundle{}, err
	}
	raw, normalized, err := buildTraceRecords(input, events)
	if err != nil {
		return TurnEvidenceBundle{}, err
	}
	report, err := buildReport(events, input.NoFilesChanged)
	if err != nil {
		return TurnEvidenceBundle{}, err
	}
	count := int64(len(events))
	lastSequence := uint64(len(events))
	return TurnEvidenceBundle{
		Identity: input.Identity,
		Objects: appendObservationSource([]EvidenceObject{
			newTraceObject(KindRawACP, raw, count, lastSequence),
			newTraceObject(KindNormalized, normalized, count, lastSequence),
		}, input.ObservationSourceJSONL, []EvidenceObject{
			newObject(KindReport, "text/markdown; charset=utf-8", report),
			newObject(KindPatch, "text/x-diff; charset=utf-8", input.Patch),
		}...),
		FirstRecordedAt: input.ServerRecordedAt.UTC(),
		LastRecordedAt:  input.ServerRecordedAt.UTC(),
		LastSequence:    lastSequence,
	}, nil
}

func appendObservationSource(objects []EvidenceObject, source []byte, rest ...EvidenceObject) []EvidenceObject {
	if len(source) != 0 {
		objects = append(objects, newTraceObject(KindObservationSource, source, 1, 1))
	}
	return append(objects, rest...)
}

func Checksum(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateInput(input BuildTurnEvidenceInput) error {
	if err := ValidateTurnIdentity(input.Identity); err != nil {
		return err
	}
	if input.ServerRecordedAt.IsZero() || input.WorkerRecordedAt.IsZero() {
		return fmt.Errorf("server and worker Trace timestamps are required")
	}
	if len(input.RawACPJSONL) == 0 {
		return fmt.Errorf("raw ACP JSONL is required")
	}
	if len(input.Patch) == 0 && !input.NoFilesChanged {
		return fmt.Errorf("empty patch requires explicit no files changed evidence")
	}
	if len(input.Patch) > 0 && input.NoFilesChanged {
		return fmt.Errorf("nonempty patch contradicts no files changed evidence")
	}
	return nil
}

// ValidateTurnIdentity enforces the identity contract shared by every sealed
// evidence object for a Turn.
func ValidateTurnIdentity(identity TurnIdentity) error {
	for name, value := range map[string]string{
		"tenant_id": identity.TenantID, "user_id": identity.UserID,
		"workspace_id":        identity.WorkspaceID,
		"worker_session_id":   string(identity.WorkerSessionID),
		"worker_run_id":       string(identity.WorkerRunID),
		"agent_session_id":    string(identity.AgentSessionID),
		"agent_turn_id":       string(identity.AgentTurnID),
		"runtime_instance_id": string(identity.RuntimeInstanceID),
		"trace_stream_id":     identity.TraceStreamID, "langfuse_trace_id": identity.LangfuseTraceID,
	} {
		if !canonical(value) {
			return fmt.Errorf("%s must be canonical", name)
		}
	}
	if identity.TaskID != "" && !canonical(string(identity.TaskID)) {
		return fmt.Errorf("task_id must be canonical when present")
	}
	if identity.RuntimeGeneration == 0 {
		return fmt.Errorf("runtime generation is required")
	}
	if err := validateRuntimeNetworkEvidence(identity.RuntimeNetwork); err != nil {
		return err
	}
	return nil
}

func validateRuntimeNetworkEvidence(evidence RuntimeNetworkEvidence) error {
	switch evidence.ID {
	case contractsv1.RuntimeNetworkPolicyTrustedFullV1:
		if evidence.Revision == 0 || !validSHA256Digest(evidence.Digest) ||
			evidence.EnforcementStatus != "active" {
			return fmt.Errorf("current runtime network evidence is invalid")
		}
	case contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1:
		if evidence.Revision != 0 || evidence.Digest != "" ||
			evidence.EnforcementStatus != "legacy_unenforced" {
			return fmt.Errorf("legacy runtime network evidence is invalid")
		}
	default:
		return fmt.Errorf("runtime network profile is invalid")
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 160 &&
		!strings.ContainsAny(value, " \t/\\\r\n\x00")
}

func decodeRawTrace(content []byte, agentSessionID contractsv1.AgentSessionID) ([]rawTraceEvent, error) {
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
		if event.Type == "acp.raw_frame" {
			if event.Direction != "client_to_agent" && event.Direction != "agent_to_client" {
				return nil, fmt.Errorf("raw ACP JSONL record %d has invalid direction", len(events)+1)
			}
			if err := validateACPFrameIdentity(event.Frame, agentSessionID); err != nil {
				return nil, fmt.Errorf("raw ACP JSONL record %d: %w", len(events)+1, err)
			}
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

func validateACPFrameIdentity(encoded string, agentSessionID contractsv1.AgentSessionID) error {
	var frame rpcFrame
	if err := json.Unmarshal([]byte(strings.TrimSpace(encoded)), &frame); err != nil {
		return fmt.Errorf("decode ACP frame: %w", err)
	}
	if len(frame.Params) == 0 {
		return nil
	}
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(frame.Params, &params); err != nil {
		return fmt.Errorf("decode ACP frame params: %w", err)
	}
	if params.SessionID != "" && params.SessionID != string(agentSessionID) {
		return fmt.Errorf("AgentSession identity mismatch")
	}
	return nil
}

func buildTraceRecords(input BuildTurnEvidenceInput, events []rawTraceEvent) ([]byte, []byte, error) {
	var rawOutput bytes.Buffer
	var normalizedOutput bytes.Buffer
	rawEncoder := json.NewEncoder(&rawOutput)
	normalizedEncoder := json.NewEncoder(&normalizedOutput)
	for index, event := range events {
		rawPayload, err := json.Marshal(event)
		if err != nil {
			return nil, nil, fmt.Errorf("encode raw Trace payload %d: %w", index+1, err)
		}
		normalizedPayload, err := normalizeEvent(event)
		if err != nil {
			return nil, nil, fmt.Errorf("normalize ACP Trace record %d: %w", index+1, err)
		}
		record := TraceRecord{
			SchemaVersion: TraceRecordSchemaVersion, Sequence: uint64(index + 1),
			ServerRecordedAt: input.ServerRecordedAt.UTC(), WorkerRecordedAt: input.WorkerRecordedAt.UTC(),
			Identity: input.Identity, Kind: event.Type,
		}
		record.Payload = rawPayload
		if err := rawEncoder.Encode(record); err != nil {
			return nil, nil, fmt.Errorf("encode raw Trace record %d: %w", index+1, err)
		}
		record.Payload = normalizedPayload
		if err := normalizedEncoder.Encode(record); err != nil {
			return nil, nil, fmt.Errorf("encode normalized Trace record %d: %w", index+1, err)
		}
	}
	return rawOutput.Bytes(), normalizedOutput.Bytes(), nil
}

func normalizeEvent(event rawTraceEvent) (json.RawMessage, error) {
	if event.Type != "acp.raw_frame" {
		return json.Marshal(map[string]any{"update_type": event.UpdateType})
	}
	var frame any
	if err := json.Unmarshal([]byte(strings.TrimSpace(event.Frame)), &frame); err != nil {
		return nil, fmt.Errorf("decode ACP frame: %w", err)
	}
	return json.Marshal(map[string]any{"direction": event.Direction, "frame": frame})
}

func buildReport(events []rawTraceEvent, noFilesChanged bool) ([]byte, error) {
	var message strings.Builder
	verifications := make(map[string]*verification)
	var order []string
	for index, event := range events {
		if event.Type != "acp.raw_frame" || event.Direction != "agent_to_client" {
			continue
		}
		var frame rpcFrame
		if err := json.Unmarshal([]byte(strings.TrimSpace(event.Frame)), &frame); err != nil {
			return nil, fmt.Errorf("decode report ACP frame in record %d: %w", index+1, err)
		}
		if frame.Method != "session/update" {
			continue
		}
		var params sessionUpdateParams
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			return nil, fmt.Errorf("decode session update params in record %d: %w", index+1, err)
		}
		var update sessionUpdate
		if err := json.Unmarshal(params.Update, &update); err != nil {
			return nil, fmt.Errorf("decode session update in record %d: %w", index+1, err)
		}
		switch update.SessionUpdate {
		case "agent_message_chunk":
			var content contentBlock
			if err := json.Unmarshal(update.Content, &content); err != nil {
				return nil, fmt.Errorf("decode agent message in record %d: %w", index+1, err)
			}
			if content.Type == "text" {
				message.WriteString(content.Text)
			}
		case "tool_call", "tool_call_update":
			call := verifications[update.ToolCallID]
			if call == nil {
				call = &verification{ID: update.ToolCallID}
				if call.ID == "" {
					call.ID = fmt.Sprintf("record-%d", index+1)
				}
				verifications[call.ID] = call
				order = append(order, call.ID)
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
	report.WriteString(stripKnownModelMetadataWarning(message.String()))
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
	for _, id := range order {
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
	return []byte(report.String()), nil
}

func stripKnownModelMetadataWarning(value string) string {
	const prefix = "Warning: Model metadata for "
	const suffix = "Defaulting to fallback metadata; this can degrade performance and cause issues."
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, prefix) {
		return trimmed
	}
	end := strings.Index(trimmed, suffix)
	if end < 0 {
		return trimmed
	}
	return strings.TrimSpace(trimmed[end+len(suffix):])
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

func newTraceObject(kind string, payload []byte, count int64, lastSequence uint64) EvidenceObject {
	object := newObject(kind, "application/x-ndjson", payload)
	object.FirstSequence = 1
	object.LastSequence = lastSequence
	object.RecordCount = count
	return object
}

func newObject(kind, contentType string, payload []byte) EvidenceObject {
	return EvidenceObject{
		Kind: kind, ContentType: contentType, Payload: append([]byte(nil), payload...),
		Checksum: Checksum(payload), SizeBytes: int64(len(payload)),
	}
}
