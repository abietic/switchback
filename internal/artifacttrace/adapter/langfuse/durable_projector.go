package langfuse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	secretcheck "github.com/abietic/switchback/internal/platform/secretcheck"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type DurableProjector struct {
	api     DurableAPI
	scanner KnownTokenScanner
}

func NewDurableProjector(api DurableAPI, scanner KnownTokenScanner) (*DurableProjector, error) {
	if api == nil || scanner == nil {
		return nil, errors.New("Langfuse durable API and known-token scanner are required")
	}
	return &DurableProjector{api: api, scanner: scanner}, nil
}

type durableExpected struct {
	traceID      string
	rootName     string
	rootTime     time.Time
	rootMetadata RuntimeMetadata
	rootInput    json.RawMessage
	observations map[string]Observation
}

func (projector *DurableProjector) ExportAndVerify(ctx context.Context, source artifactapp.SourceProjection) error {
	if err := validateDurableSource(source); err != nil {
		return projectionError("source_invalid", true)
	}
	if err := projector.scan(ctx, "langfuse.source_projection", source); err != nil {
		return err
	}
	batch, expected, err := projector.buildBatch(source)
	if err != nil {
		return projectionError("source_invalid", true)
	}
	if err := projector.scan(ctx, "langfuse.ingestion.batch", batch); err != nil {
		return err
	}
	if err := projector.api.Ingest(ctx, batch); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return projectionError("projection_cancelled", false)
		}
		return projectionError("ingestion_unavailable", false)
	}
	return projector.verify(ctx, expected)
}

func (projector *DurableProjector) scan(ctx context.Context, source string, value any) error {
	if err := projector.scanner.ScanJSON(ctx, source, value); err != nil {
		switch {
		case secretcheck.IsExposure(err):
			return projectionError("credential_detected", true)
		case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return projectionError("projection_cancelled", false)
		default:
			return projectionError("scanner_unavailable", false)
		}
	}
	return nil
}

func (projector *DurableProjector) buildBatch(source artifactapp.SourceProjection) (IngestionBatch, durableExpected, error) {
	identity := source.Turn.Identity
	scope := []string{source.Root.TenantID, source.Root.UserID, source.Root.WorkspaceID, source.Root.WorkerSessionID, source.Root.WorkerRunID}
	traceID := durableRootID(scope)
	rootMetadata := RuntimeMetadata{TenantID: source.Root.TenantID, UserID: source.Root.UserID, WorkspaceID: source.Root.WorkspaceID, WorkerSessionID: source.Root.WorkerSessionID, WorkerRunID: source.Root.WorkerRunID, SourceStartedAt: source.Root.StartedAt.Format(time.RFC3339Nano)}
	rootMetadataBody := map[string]any{"tenant_id": rootMetadata.TenantID, "user_id": rootMetadata.UserID, "workspace_id": rootMetadata.WorkspaceID, "worker_session_id": rootMetadata.WorkerSessionID, "worker_run_id": rootMetadata.WorkerRunID, "source_started_at": rootMetadata.SourceStartedAt}
	rootMetadataTransport, err := encodeMetadataForTransport(rootMetadataBody)
	if err != nil {
		return IngestionBatch{}, durableExpected{}, err
	}
	eventTimestamp := transportTime(source.Root.StartedAt)
	rootInput, err := encodeJSONText(source.Root.Input)
	if err != nil {
		return IngestionBatch{}, durableExpected{}, err
	}
	turnInput, err := encodeJSONText(source.Turn.Input)
	if err != nil {
		return IngestionBatch{}, durableExpected{}, err
	}
	turnOutput, err := encodeJSONText(source.Output)
	if err != nil {
		return IngestionBatch{}, durableExpected{}, err
	}
	events := []IngestionEvent{{
		ID: durableID("event:root", append(scope, traceID)...), Timestamp: eventTimestamp, Type: "trace-create",
		Body: map[string]any{"id": traceID, "name": "worker.run", "timestamp": eventTimestamp, "input": rootInput, "metadata": rootMetadataTransport},
	}}
	expected := durableExpected{traceID: traceID, rootName: "worker.run", rootTime: eventTimestamp, rootMetadata: rootMetadata, rootInput: rootInput, observations: make(map[string]Observation, 1+len(source.Turn.ModelObservations))}
	turnID := durableID("turn", identitySeed(identity)...)
	turnMetadata := sourceMetadata(source, "turn", "")
	turn := Observation{ID: turnID, TraceID: traceID, Name: "agent.turn", Type: "SPAN", Metadata: turnMetadata, Input: turnInput, Output: turnOutput, StartTime: transportTime(source.Turn.StartedAt), EndTime: transportTime(source.Turn.EndedAt), Level: "DEFAULT"}
	turnBody, err := observationBodyForTransport(turn)
	if err != nil {
		return IngestionBatch{}, durableExpected{}, err
	}
	events = append(events, IngestionEvent{ID: durableID("event:turn", turnID), Timestamp: eventTimestamp, Type: "span-create", Body: turnBody})
	expected.observations[turnID] = turn
	// Copy before sorting so the immutable source snapshot is never mutated.
	sortedModels := append([]workerapp.ModelObservation(nil), source.Turn.ModelObservations...)
	sort.Slice(sortedModels, func(i, j int) bool { return sortedModels[i].ObservationID < sortedModels[j].ObservationID })
	for _, model := range sortedModels {
		modelInput, err := encodeJSONText(model.Input)
		if err != nil {
			return IngestionBatch{}, durableExpected{}, err
		}
		modelOutput, err := encodeJSONText(model.Output)
		if err != nil {
			return IngestionBatch{}, durableExpected{}, err
		}
		modelID := durableID("model", append(identitySeed(identity), model.ObservationID)...)
		metadata := sourceMetadata(source, "model", model.ObservationID)
		metadata["model_route"] = model.Route
		metadata["status"] = model.Status
		metadata["source_started_at"] = model.StartedAt.Format(time.RFC3339Nano)
		metadata["source_ended_at"] = model.EndedAt.Format(time.RFC3339Nano)
		level := "DEFAULT"
		if model.Status != "ok" {
			level = "ERROR"
		}
		total := model.InputTokens + model.OutputTokens
		observation := Observation{ID: modelID, TraceID: traceID, ParentObservationID: &turnID, Name: "model.generation", Type: "GENERATION", Metadata: metadata, Input: modelInput, Output: modelOutput, StartTime: transportTime(model.StartedAt), EndTime: transportTime(model.EndedAt), Model: model.Model, UsageDetails: map[string]int64{"input": model.InputTokens, "output": model.OutputTokens, "total": total}, Usage: Usage{Input: model.InputTokens, Output: model.OutputTokens, Total: total, Unit: "TOKENS"}, Level: level, StatusMessage: model.Status}
		modelBody, err := observationBodyForTransport(observation)
		if err != nil {
			return IngestionBatch{}, durableExpected{}, err
		}
		events = append(events, IngestionEvent{ID: durableID("event:model", modelID), Timestamp: eventTimestamp, Type: "generation-create", Body: modelBody})
		expected.observations[modelID] = observation
	}
	return IngestionBatch{Batch: events}, expected, nil
}

func encodeJSONText(value json.RawMessage) (json.RawMessage, error) {
	encoded, err := json.Marshal(struct {
		Encoding string `json:"encoding"`
		JSON     string `json:"json"`
	}{Encoding: "json-text.v1", JSON: string(value)})
	return json.RawMessage(encoded), err
}

func encodeMetadataForTransport(metadata map[string]any) (map[string]any, error) {
	encoded := make(map[string]any, len(metadata))
	for key, value := range metadata {
		body, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		encoded[key] = string(body)
	}
	return encoded, nil
}

func (projector *DurableProjector) verify(ctx context.Context, expected durableExpected) error {
	trace, err := projector.api.GetTraceRoot(ctx, expected.traceID)
	if err != nil {
		return readbackError(ctx, err)
	}
	if trace.ID != expected.traceID || trace.Name != expected.rootName || !trace.Timestamp.Equal(expected.rootTime) || !durableRootMetadataEqual(trace.Metadata, expected.rootMetadata) || !artifactapp.ProjectionJSONEqual(trace.Input, expected.rootInput) || !nullOrEmptyJSON(trace.Output) {
		return projectionError("projection_collision", true)
	}
	ids := make([]string, 0, len(expected.observations))
	for id := range expected.observations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		actual, err := projector.api.GetObservation(ctx, id)
		if err != nil {
			return readbackError(ctx, err)
		}
		if !durableObservationEqual(actual, expected.observations[id]) {
			return projectionError("projection_collision", true)
		}
	}
	return nil
}

func durableRootMetadataEqual(left, right RuntimeMetadata) bool {
	return runtimeMetadataEqual(left, right) && left.SourceStartedAt == right.SourceStartedAt
}

func readbackError(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return projectionError("projection_cancelled", false)
	}
	if IsNotFound(err) {
		return projectionError("projection_not_ready", false)
	}
	return projectionError("readback_unavailable", false)
}
func projectionError(code string, permanent bool) error {
	return &artifactapp.ProjectionError{Code: code, Permanent: permanent}
}

func validateDurableSource(source artifactapp.SourceProjection) error {
	if source.SchemaVersion != artifactapp.DurableProjectionSchemaVersion || source.Source.TraceStreamID == "" || !validChecksum(source.Source.ManifestChecksum) || source.Turn.SchemaVersion != artifactapp.ObservationSourceSchemaVersion || source.Root.StartedAt.IsZero() || !validSourceJSON(source.Root.Input) || !validSourceJSON(source.Output) || artifactdomain.ValidateTurnIdentity(source.Turn.Identity) != nil {
		return errors.New("invalid")
	}
	i := source.Turn.Identity
	if source.Source.TraceStreamID != i.TraceStreamID || source.Root.TenantID != i.TenantID || source.Root.UserID != i.UserID || source.Root.WorkspaceID != i.WorkspaceID || source.Root.WorkerSessionID != string(i.WorkerSessionID) || source.Root.WorkerRunID != string(i.WorkerRunID) || source.Turn.StartedAt.IsZero() || source.Turn.EndedAt.Before(source.Turn.StartedAt) || !validSourceJSON(source.Turn.Input) || len(source.Turn.ModelObservations) == 0 {
		return errors.New("invalid")
	}
	seen := map[string]bool{}
	for _, m := range source.Turn.ModelObservations {
		if m.ObservationID == "" || seen[m.ObservationID] || m.WorkerSessionID != string(i.WorkerSessionID) || m.WorkerRunID != string(i.WorkerRunID) || m.AgentTurnID != string(i.AgentTurnID) || m.RuntimeID != string(i.RuntimeInstanceID) || m.Generation != uint64(i.RuntimeGeneration) || m.Model == "" || m.Route == "" || m.Status == "" || !safeUsage(m.InputTokens, m.OutputTokens) || m.StartedAt.IsZero() || m.EndedAt.Before(m.StartedAt) || !validSourceJSON(m.Input) || !validSourceJSON(m.Output) {
			return errors.New("invalid")
		}
		seen[m.ObservationID] = true
	}
	return nil
}
func validSourceJSON(value json.RawMessage) bool {
	return !nullOrEmptyJSON(value) && utf8.Valid(value) && json.Valid(value)
}
func safeUsage(input, output int64) bool {
	const maxSafeInteger int64 = 1<<53 - 1
	return input >= 0 && output >= 0 && input <= maxSafeInteger && output <= maxSafeInteger-input
}
func validChecksum(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && func() bool { _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); return err == nil }()
}
func nullOrEmptyJSON(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
func transportTime(value time.Time) time.Time { return value.UTC().Truncate(time.Millisecond) }
func durableRootID(scope []string) string {
	encoded, _ := json.Marshal(scope)
	sum := sha256.Sum256([]byte(artifactapp.DurableProjectionSchemaVersion + "\x00run\x00" + string(encoded)))
	return hex.EncodeToString(sum[:16])
}
func durableID(role string, parts ...string) string {
	seed := append([]string{artifactapp.DurableProjectionSchemaVersion, role}, parts...)
	encoded, _ := json.Marshal(seed)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}
func identitySeed(i artifactdomain.TurnIdentity) []string {
	return []string{i.TenantID, i.UserID, i.WorkspaceID, string(i.WorkerSessionID), string(i.WorkerRunID), string(i.AgentSessionID), string(i.AgentTurnID), string(i.RuntimeInstanceID), strconv.FormatUint(uint64(i.RuntimeGeneration), 10), i.TraceStreamID}
}
func sourceMetadata(source artifactapp.SourceProjection, role, observationID string) map[string]any {
	i := source.Turn.Identity
	m := map[string]any{"tenant_id": i.TenantID, "user_id": i.UserID, "workspace_id": i.WorkspaceID, "worker_session_id": i.WorkerSessionID, "worker_run_id": i.WorkerRunID, "agent_session_id": i.AgentSessionID, "agent_turn_id": i.AgentTurnID, "runtime_instance_id": i.RuntimeInstanceID, "runtime_generation": i.RuntimeGeneration, "trace_stream_id": i.TraceStreamID, "manifest_checksum": source.Source.ManifestChecksum, "source_role": role, "source_started_at": source.Turn.StartedAt.Format(time.RFC3339Nano), "source_ended_at": source.Turn.EndedAt.Format(time.RFC3339Nano)}
	if observationID != "" {
		m["source_observation_id"] = observationID
	}
	return m
}
func observationBody(o Observation) map[string]any {
	b := map[string]any{"id": o.ID, "traceId": o.TraceID, "name": o.Name, "startTime": o.StartTime, "endTime": o.EndTime, "input": o.Input, "output": o.Output, "metadata": o.Metadata, "level": o.Level}
	if o.ParentObservationID != nil {
		b["parentObservationId"] = *o.ParentObservationID
	}
	if o.Type == "GENERATION" {
		b["model"] = o.Model
		b["usageDetails"] = o.UsageDetails
		b["statusMessage"] = o.StatusMessage
	}
	return b
}
func observationBodyForTransport(o Observation) (map[string]any, error) {
	metadata, err := encodeMetadataForTransport(o.Metadata)
	if err != nil {
		return nil, err
	}
	body := observationBody(o)
	body["metadata"] = metadata
	return body, nil
}
func durableObservationEqual(a, b Observation) bool {
	return a.ID == b.ID && a.TraceID == b.TraceID && pointerEqual(a.ParentObservationID, b.ParentObservationID) && a.Name == b.Name && a.Type == b.Type && a.StartTime.Equal(b.StartTime) && a.EndTime.Equal(b.EndTime) && a.Model == b.Model && a.Level == b.Level && a.StatusMessage == b.StatusMessage && metadataEqual(a.Metadata, b.Metadata) && artifactapp.ProjectionJSONEqual(a.Input, b.Input) && artifactapp.ProjectionJSONEqual(a.Output, b.Output) && usageEqual(a, b)
}
func pointerEqual(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func metadataEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return artifactapp.ProjectionJSONEqual(x, y)
}
func usageEqual(a, b Observation) bool {
	if a.Type != "GENERATION" {
		return true
	}
	input, ok1 := usageValue(a, "input")
	output, ok2 := usageValue(a, "output")
	total, ok3 := usageValue(a, "total")
	return ok1 && ok2 && ok3 && input == b.Usage.Input && output == b.Usage.Output && total == b.Usage.Total
}
func usageValue(o Observation, key string) (int64, bool) {
	if o.UsageDetails != nil {
		v, ok := o.UsageDetails[key]
		return v, ok
	}
	switch key {
	case "input":
		return o.Usage.Input, true
	case "output":
		return o.Usage.Output, true
	case "total":
		return o.Usage.Total, true
	}
	return 0, false
}

var _ artifactapp.SourceProjector = (*DurableProjector)(nil)
