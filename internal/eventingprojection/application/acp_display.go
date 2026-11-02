package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultDisplayTextBytes       = 512 << 10
	defaultDisplayToolOutputBytes = 16 << 10
	defaultDisplayLocations       = 32
)

var displaySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY)=\S+`),
}

var modelMetadataDisplayWarning = regexp.MustCompile(
	`(?s)^\s*Warning:\s*Model metadata for .*? not found\.\s*Defaulting to fallback metadata; this can degrade performance and cause issues\.\s*`,
)

type DisplayStreamIdentity struct {
	TenantID          string
	UserID            string
	WorkspaceID       string
	WorkerSessionID   string
	WorkerRunID       string
	AgentTurnID       string
	AgentSessionID    string
	RuntimeGeneration uint64
}

type ACPDisplayPolicy struct {
	MaxTextBytes       int
	MaxToolOutputBytes int
	MaxLocations       int
}

type NormalizedDisplayUpdate struct {
	Sequence   uint64
	Kind       string
	Payload    json.RawMessage
	ObservedAt time.Time
}

type DisplayEmission struct {
	Transient  *TransientDisplayDelta
	Checkpoint *DisplayCheckpoint
}

type DisplayArtifact struct {
	ArtifactID string
	Type       string
	Label      string
	Href       string
}

type ACPDisplayStream struct {
	identity           DisplayStreamIdentity
	policy             ACPDisplayPolicy
	displayStreamID    string
	checkpointVersion  uint64
	transientSequence  uint64
	lastUpdateSequence uint64
	updateDigests      map[uint64][32]byte
	parts              []DisplayPart
	partIndexes        map[string]int
	toolStartedAt      map[string]time.Time
	status             string
	startedAt          time.Time
}

func NewACPDisplayStream(
	identity DisplayStreamIdentity,
	policy ACPDisplayPolicy,
) (*ACPDisplayStream, error) {
	for name, value := range map[string]string{
		"tenant_id": identity.TenantID, "user_id": identity.UserID,
		"workspace_id": identity.WorkspaceID, "worker_session_id": identity.WorkerSessionID,
		"worker_run_id": identity.WorkerRunID, "agent_turn_id": identity.AgentTurnID,
		"agent_session_id": identity.AgentSessionID,
	} {
		if !canonical(value) {
			return nil, fmt.Errorf("%s is invalid", name)
		}
	}
	if identity.RuntimeGeneration == 0 {
		return nil, fmt.Errorf("Runtime generation is required")
	}
	if policy.MaxTextBytes == 0 {
		policy.MaxTextBytes = defaultDisplayTextBytes
	}
	if policy.MaxToolOutputBytes == 0 {
		policy.MaxToolOutputBytes = defaultDisplayToolOutputBytes
	}
	if policy.MaxLocations == 0 {
		policy.MaxLocations = defaultDisplayLocations
	}
	if policy.MaxTextBytes < 1024 || policy.MaxToolOutputBytes < 256 ||
		policy.MaxLocations < 1 || policy.MaxLocations > 256 {
		return nil, fmt.Errorf("ACP Display policy is invalid")
	}
	return &ACPDisplayStream{
		identity: identity, policy: policy,
		displayStreamID: stableDisplayID(
			"display", identity.AgentTurnID, identity.AgentSessionID,
			fmt.Sprintf("%d", identity.RuntimeGeneration),
		),
		updateDigests: make(map[uint64][32]byte), partIndexes: make(map[string]int),
		toolStartedAt: make(map[string]time.Time), status: "streaming",
	}, nil
}

func (stream *ACPDisplayStream) DisplayStreamID() string { return stream.displayStreamID }

func (stream *ACPDisplayStream) Start(at time.Time) (DisplayCheckpoint, error) {
	if at.IsZero() || stream.checkpointVersion != 0 {
		return DisplayCheckpoint{}, fmt.Errorf("Display stream start is invalid")
	}
	stream.startedAt = at.UTC()
	stream.checkpointVersion = 1
	return stream.checkpoint(at), nil
}

func (stream *ACPDisplayStream) Apply(update NormalizedDisplayUpdate) (DisplayEmission, error) {
	if stream.checkpointVersion == 0 || update.Sequence == 0 || update.ObservedAt.IsZero() ||
		!canonical(update.Kind) || !jsonObject(update.Payload) {
		return DisplayEmission{}, fmt.Errorf("normalized Display update is invalid")
	}
	digest := sha256.Sum256(append([]byte(update.Kind+"\x00"), update.Payload...))
	if previous, exists := stream.updateDigests[update.Sequence]; exists {
		if previous == digest {
			return DisplayEmission{}, nil
		}
		return DisplayEmission{}, ErrDisplayCheckpointConflict
	}
	if update.Sequence <= stream.lastUpdateSequence {
		return DisplayEmission{}, ErrDisplayCheckpointConflict
	}
	stream.lastUpdateSequence = update.Sequence
	stream.updateDigests[update.Sequence] = digest

	switch update.Kind {
	case "agent_thought_chunk":
		return DisplayEmission{}, nil
	case "agent_message_chunk":
		return stream.applyAgentText(update)
	case "tool_call", "tool_call_update":
		return stream.applyTool(update)
	case "plan", "plan_update", "activity":
		return stream.applyActivity(update)
	case "approval":
		return stream.applyApproval(update)
	case "artifact":
		return stream.applyArtifactUpdate(update)
	case "error":
		return stream.applyError(update)
	case "terminal":
		return stream.applyTerminal(update)
	default:
		return DisplayEmission{}, nil
	}
}

func (stream *ACPDisplayStream) Terminate(stopReason string, at time.Time) (DisplayEmission, error) {
	if strings.TrimSpace(stopReason) == "" {
		return DisplayEmission{}, fmt.Errorf("Display terminal reason is required")
	}
	return stream.Apply(NormalizedDisplayUpdate{
		Sequence:   stream.lastUpdateSequence + 1,
		Kind:       "terminal",
		Payload:    mustJSON(map[string]string{"stop_reason": stopReason}),
		ObservedAt: at,
	})
}

func (stream *ACPDisplayStream) AppendArtifacts(
	artifacts []DisplayArtifact,
	at time.Time,
) (DisplayCheckpoint, error) {
	if at.IsZero() || stream.checkpointVersion == 0 {
		return DisplayCheckpoint{}, fmt.Errorf("Artifact display boundary is invalid")
	}
	for _, artifact := range artifacts {
		if !canonical(artifact.ArtifactID) || (artifact.Type != "report" && artifact.Type != "patch") ||
			!strings.HasPrefix(artifact.Href, "/api/v1/") {
			return DisplayCheckpoint{}, fmt.Errorf("Artifact display input is invalid")
		}
		payload := map[string]any{
			"artifact_id": artifact.ArtifactID, "type": artifact.Type,
			"label": bounded(redactDisplayText(artifact.Label), 512),
			"href":  artifact.Href,
		}
		stream.upsertPart(
			stableDisplayID("part-artifact", stream.displayStreamID, artifact.ArtifactID),
			"artifact", "completed", payload, at.UTC(),
		)
	}
	stream.checkpointVersion++
	return stream.checkpoint(at), nil
}

func (stream *ACPDisplayStream) applyAgentText(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var payload struct {
		MessageID string `json:"messageId"`
		Content   struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(update.Payload, &payload); err != nil {
		return DisplayEmission{}, fmt.Errorf("decode Agent message chunk: %w", err)
	}
	text := payload.Content.Text
	if text == "" {
		text = payload.Text
	}
	if text == "" {
		return DisplayEmission{}, nil
	}
	sourceID := payload.MessageID
	if !canonical(sourceID) {
		sourceID = "assistant-response"
	}
	cleaned := modelMetadataDisplayWarning.ReplaceAllString(text, "")
	hasDiagnostic := cleaned != text
	text = cleaned
	if hasDiagnostic {
		stream.upsertPart(
			stableDisplayID("part-activity", stream.displayStreamID, sourceID, "model-metadata"),
			"activity", "completed", map[string]any{
				"title":  "Model routing diagnostic",
				"status": "completed",
				"code":   "model_metadata_fallback",
				"detail": "The model provider used fallback model metadata.",
			}, update.ObservedAt.UTC(),
		)
	}
	if text == "" {
		stream.checkpointVersion++
		checkpoint := stream.checkpoint(update.ObservedAt)
		return DisplayEmission{Checkpoint: &checkpoint}, nil
	}
	partID := stableDisplayID("part-text", stream.displayStreamID, sourceID)
	content := text
	if index, exists := stream.partIndexes[partID]; exists {
		var previous struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(stream.parts[index].Payload, &previous)
		content = previous.Content + text
	}
	content = bounded(redactDisplayText(content), stream.policy.MaxTextBytes)
	stream.upsertPart(partID, "text", "streaming", map[string]any{
		"format": "markdown", "content": content,
	}, update.ObservedAt.UTC())
	stream.transientSequence++
	delta := TransientDisplayDelta{
		SchemaVersion: TransientDeltaSchemaVersion, DisplayStreamID: stream.displayStreamID,
		MessageID: sourceID, PartID: partID, BaseCheckpointVersion: stream.checkpointVersion,
		TransientSequence: stream.transientSequence, DeltaType: "text_delta",
		Payload:   mustJSON(map[string]any{"text": redactDisplayText(text)}),
		EmittedAt: update.ObservedAt.UTC(),
	}
	emission := DisplayEmission{Transient: &delta}
	if hasDiagnostic {
		stream.checkpointVersion++
		checkpoint := stream.checkpoint(update.ObservedAt)
		emission.Checkpoint = &checkpoint
	}
	return emission, nil
}

func (stream *ACPDisplayStream) applyTool(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(update.Payload, &payload); err != nil {
		return DisplayEmission{}, fmt.Errorf("decode Tool update: %w", err)
	}
	toolCallID := jsonString(payload["toolCallId"])
	if !canonical(toolCallID) {
		return DisplayEmission{}, nil
	}
	partID := stableDisplayID("part-tool", stream.displayStreamID, toolCallID)
	existing := map[string]any{}
	if index, ok := stream.partIndexes[partID]; ok {
		_ = json.Unmarshal(stream.parts[index].Payload, &existing)
	}
	status := jsonString(payload["status"])
	if status == "" {
		status, _ = existing["status"].(string)
	}
	lifecycle := normalizePartLifecycle(status)
	if update.Kind == "tool_call" && stream.toolStartedAt[toolCallID].IsZero() {
		stream.toolStartedAt[toolCallID] = update.ObservedAt.UTC()
	}
	kind := jsonString(payload["kind"])
	if kind == "" {
		kind, _ = existing["kind"].(string)
	}
	title := jsonString(payload["title"])
	if title == "" {
		title, _ = existing["title"].(string)
	}
	if kind == "execute" {
		title = "Run command"
	}
	toolPayload := map[string]any{
		"tool_call_id": toolCallID, "kind": bounded(kind, 128),
		"title": bounded(redactDisplayText(title), 1024), "status": lifecycle,
		"evidence_refs": []map[string]any{{
			"type": "trace_cursor", "update_sequence": update.Sequence,
		}},
	}
	if rawInput, ok := payload["rawInput"]; ok {
		toolPayload["arguments"] = safeToolArguments(rawInput)
	} else if value, ok := existing["arguments"]; ok {
		toolPayload["arguments"] = value
	} else {
		toolPayload["arguments"] = map[string]string{}
	}
	if locations, ok := payload["locations"]; ok {
		toolPayload["locations"] = safeToolLocations(locations, stream.policy.MaxLocations)
	} else if value, ok := existing["locations"]; ok {
		toolPayload["locations"] = value
	} else {
		toolPayload["locations"] = []map[string]string{}
	}
	if rawOutput, ok := payload["rawOutput"]; ok {
		output, truncated := safeToolOutput(rawOutput, stream.policy.MaxToolOutputBytes)
		toolPayload["output"], toolPayload["output_truncated"] = output, truncated
	} else if value, ok := existing["output"]; ok {
		toolPayload["output"] = value
		toolPayload["output_truncated"] = existing["output_truncated"]
	}
	if started := stream.toolStartedAt[toolCallID]; !started.IsZero() {
		toolPayload["duration_ms"] = max(int64(0), update.ObservedAt.Sub(started).Milliseconds())
	}
	stream.upsertPart(partID, "tool", lifecycle, toolPayload, update.ObservedAt.UTC())
	if update.Kind == "tool_call" || lifecycle == "completed" || lifecycle == "failed" ||
		lifecycle == "waiting_user" || lifecycle == "cancelled" {
		stream.checkpointVersion++
		checkpoint := stream.checkpoint(update.ObservedAt)
		return DisplayEmission{Checkpoint: &checkpoint}, nil
	}
	stream.transientSequence++
	delta := TransientDisplayDelta{
		SchemaVersion: TransientDeltaSchemaVersion, DisplayStreamID: stream.displayStreamID,
		PartID: partID, BaseCheckpointVersion: stream.checkpointVersion,
		TransientSequence: stream.transientSequence, DeltaType: "progress_pulse",
		Payload:   mustJSON(map[string]any{"status": lifecycle}),
		EmittedAt: update.ObservedAt.UTC(),
	}
	return DisplayEmission{Transient: &delta}, nil
}

func (stream *ACPDisplayStream) applyActivity(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(update.Payload, &input)
	sourceID := jsonString(input["planId"])
	if !canonical(sourceID) {
		sourceID = fmt.Sprintf("activity-%d", update.Sequence)
	}
	payload := map[string]any{
		"title":  bounded(redactDisplayText(firstString(input, "title", "summary")), 1024),
		"status": normalizePartLifecycle(jsonString(input["status"])),
	}
	stream.upsertPart(
		stableDisplayID("part-activity", stream.displayStreamID, sourceID),
		"activity", payload["status"].(string), payload, update.ObservedAt.UTC(),
	)
	stream.checkpointVersion++
	checkpoint := stream.checkpoint(update.ObservedAt)
	return DisplayEmission{Checkpoint: &checkpoint}, nil
}

func (stream *ACPDisplayStream) applyApproval(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(update.Payload, &input)
	requestID := jsonString(input["approval_request_id"])
	if !canonical(requestID) {
		return DisplayEmission{}, nil
	}
	payload := map[string]any{
		"approval_request_id": requestID,
		"capability":          bounded(jsonString(input["capability"]), 256),
		"target":              bounded(redactDisplayText(jsonString(input["target"])), 1024),
		"effect":              bounded(jsonString(input["effect"]), 256),
		"decision":            bounded(jsonString(input["decision"]), 64),
	}
	status := "waiting_user"
	if payload["decision"] != "" {
		status = "completed"
	}
	stream.upsertPart(
		stableDisplayID("part-approval", stream.displayStreamID, requestID),
		"approval", status, payload, update.ObservedAt.UTC(),
	)
	stream.checkpointVersion++
	checkpoint := stream.checkpoint(update.ObservedAt)
	return DisplayEmission{Checkpoint: &checkpoint}, nil
}

func (stream *ACPDisplayStream) applyArtifactUpdate(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var input struct {
		ArtifactID string `json:"artifact_id"`
		Type       string `json:"type"`
		Label      string `json:"label"`
		Href       string `json:"href"`
	}
	if json.Unmarshal(update.Payload, &input) != nil {
		return DisplayEmission{}, nil
	}
	checkpoint, err := stream.AppendArtifacts([]DisplayArtifact{{
		ArtifactID: input.ArtifactID, Type: input.Type, Label: input.Label, Href: input.Href,
	}}, update.ObservedAt)
	if err != nil {
		return DisplayEmission{}, err
	}
	return DisplayEmission{Checkpoint: &checkpoint}, nil
}

func (stream *ACPDisplayStream) applyError(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(update.Payload, &input)
	code := jsonString(input["code"])
	if !canonical(code) {
		code = "agent_execution_failed"
	}
	stream.upsertPart(
		stableDisplayID("part-error", stream.displayStreamID, fmt.Sprintf("%d", update.Sequence)),
		"error", "failed", map[string]any{
			"code": code, "message": "Agent execution failed.",
			"recovery": "Review the safe diagnostics and retry from the current Worker state.",
		}, update.ObservedAt.UTC(),
	)
	stream.status = "failed"
	stream.checkpointVersion++
	checkpoint := stream.checkpoint(update.ObservedAt)
	return DisplayEmission{Checkpoint: &checkpoint}, nil
}

func (stream *ACPDisplayStream) applyTerminal(
	update NormalizedDisplayUpdate,
) (DisplayEmission, error) {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(update.Payload, &input)
	stopReason := jsonString(input["stop_reason"])
	stream.status = terminalDisplayStatus(stopReason)
	for index := range stream.parts {
		if stream.parts[index].PartType == "text" &&
			stream.parts[index].LifecycleStatus == "streaming" {
			stream.parts[index].LifecycleStatus = stream.status
			stream.parts[index].PartRevision++
			stream.parts[index].UpdatedAt = update.ObservedAt.UTC()
		}
	}
	stream.checkpointVersion++
	checkpoint := stream.checkpoint(update.ObservedAt)
	return DisplayEmission{Checkpoint: &checkpoint}, nil
}

func (stream *ACPDisplayStream) upsertPart(
	partID, partType, lifecycle string,
	payload map[string]any,
	at time.Time,
) {
	if index, exists := stream.partIndexes[partID]; exists {
		part := &stream.parts[index]
		part.PartRevision++
		part.LifecycleStatus = lifecycle
		part.Payload = mustJSON(payload)
		part.UpdatedAt = at
		return
	}
	part := DisplayPart{
		SchemaVersion: DisplayPartSchemaVersion, PartID: partID, PartType: partType,
		PartRevision: 1, Ordinal: uint64(len(stream.parts)), LifecycleStatus: lifecycle,
		Critical:   partType == "approval" || partType == "error",
		Visibility: mustJSON(map[string]any{"audience": "user"}),
		Provenance: mustJSON(map[string]any{
			"source": "acp", "runtime_generation": stream.identity.RuntimeGeneration,
			"agent_session_id": stream.identity.AgentSessionID,
		}),
		AvailableActions: []ActionRef{}, Payload: mustJSON(payload), CreatedAt: at, UpdatedAt: at,
	}
	stream.partIndexes[partID] = len(stream.parts)
	stream.parts = append(stream.parts, part)
}

func (stream *ACPDisplayStream) checkpoint(at time.Time) DisplayCheckpoint {
	parts := make([]DisplayPart, len(stream.parts))
	copy(parts, stream.parts)
	return DisplayCheckpoint{
		SchemaVersion: DisplayCheckpointSchemaVersion, DisplayStreamID: stream.displayStreamID,
		TenantID: stream.identity.TenantID, UserID: stream.identity.UserID,
		WorkspaceID: stream.identity.WorkspaceID, WorkerSessionID: stream.identity.WorkerSessionID,
		WorkerRunID: stream.identity.WorkerRunID, AgentTurnID: stream.identity.AgentTurnID,
		CheckpointVersion: stream.checkpointVersion, Status: stream.status, Parts: parts,
		LastObservedTransientSequence: stream.transientSequence,
		TraceCursorRef: mustJSON(map[string]any{
			"runtime_generation": stream.identity.RuntimeGeneration,
			"agent_session_id":   stream.identity.AgentSessionID,
			"update_sequence":    stream.lastUpdateSequence,
		}),
		DurabilityPolicyRef: mustJSON(map[string]any{
			"policy_id": "display-semantic-boundaries-v1", "revision": 1,
		}),
		CheckpointedAt: at.UTC(),
	}
}

func stableDisplayID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:12])
}

func safeToolArguments(raw json.RawMessage) map[string]string {
	result := map[string]string{}
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return result
	}
	for _, key := range []string{"cwd", "path", "query", "pattern", "file"} {
		if value := jsonString(input[key]); value != "" {
			result[key] = bounded(redactDisplayText(value), 4096)
		}
	}
	return result
}

func safeToolLocations(raw json.RawMessage, limit int) []map[string]string {
	var input []struct {
		Path string `json:"path"`
		Line *int   `json:"line"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return []map[string]string{}
	}
	result := make([]map[string]string, 0, min(len(input), limit))
	for _, location := range input[:min(len(input), limit)] {
		value := map[string]string{"path": bounded(redactDisplayText(location.Path), 4096)}
		if location.Line != nil {
			value["line"] = fmt.Sprintf("%d", *location.Line)
		}
		result = append(result, value)
	}
	return result
}

func safeToolOutput(raw json.RawMessage, limit int) (string, bool) {
	var input struct {
		FormattedOutput string `json:"formatted_output"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return "", false
	}
	value := redactDisplayText(input.FormattedOutput)
	if len(value) <= limit {
		return value, false
	}
	return bounded(value, limit), true
}

func redactDisplayText(value string) string {
	for _, pattern := range displaySecretPatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	return value
}

func bounded(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "\n[truncated]"
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func firstString(input map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if value := jsonString(input[key]); value != "" {
			return value
		}
	}
	return "Activity"
}

func normalizePartLifecycle(value string) string {
	switch value {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "waiting_user", "pending_approval":
		return "waiting_user"
	default:
		return "streaming"
	}
}

func terminalDisplayStatus(stopReason string) string {
	switch {
	case strings.Contains(strings.ToLower(stopReason), "cancel"):
		return "cancelled"
	case strings.Contains(strings.ToLower(stopReason), "timeout"),
		strings.Contains(strings.ToLower(stopReason), "deadline"):
		return "timed_out"
	default:
		return "completed"
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
