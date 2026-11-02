package langfuse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

const (
	FragmentProjectionVersion = "langfuse.projection.v3"
	FragmentCompilerRevision  = "langfuse.fragment.compiler.v1"
)

// FragmentCompiler has no clock, client, registry or publication dependency.
// The exact returned batch bytes are what a scan receipt attests to.
type FragmentCompiler struct{}

func (FragmentCompiler) CompileObservationFragment(ctx context.Context, frame []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, errors.New("projection_cancelled")
	}
	f, err := artifactdomain.DecodeObservationFragment(frame)
	if err != nil || !safeUsage(f.InputTokens, f.OutputTokens) {
		return nil, errors.New("source_invalid")
	}
	s := f.Scope
	seed := []string{s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID}
	traceID := fragmentID("run", seed...)
	sum := sha256.Sum256(frame)
	metadata := map[string]any{"tenant_id": s.TenantID, "user_id": s.UserID, "workspace_id": s.WorkspaceID, "worker_session_id": s.WorkerSessionID, "worker_run_id": s.WorkerRunID, "source_role": string(f.Kind), "source_started_at": f.StartedAt.UTC().Format(time.RFC3339Nano), "projection_version": FragmentProjectionVersion, "compiler_revision": FragmentCompilerRevision, "frame_sha256": "sha256:" + hex.EncodeToString(sum[:])}
	input, err := encodeJSONText(json.RawMessage(f.InputJSON))
	if err != nil {
		return nil, errors.New("source_invalid")
	}
	body := map[string]any{"id": traceID, "name": "worker.run", "timestamp": transportTime(f.StartedAt), "input": input}
	id, eventType := traceID, "trace-create"
	if f.Kind == artifactdomain.FragmentRunBasis {
		metadata["initial_turn_id"] = f.InitialTurnID
	} else {
		i := f.Turn
		seed = append(seed, string(i.AgentSessionID), string(i.AgentTurnID), string(i.RuntimeInstanceID), strconv.FormatUint(uint64(i.RuntimeGeneration), 10))
		turnID := fragmentID("turn", seed...)
		id, eventType = turnID, "span-create"
		metadata["agent_session_id"], metadata["agent_turn_id"], metadata["runtime_instance_id"], metadata["runtime_generation"] = i.AgentSessionID, i.AgentTurnID, i.RuntimeInstanceID, i.RuntimeGeneration
		metadata["source_ended_at"] = f.EndedAt.UTC().Format(time.RFC3339Nano)
		output, err := encodeJSONText(json.RawMessage(f.OutputJSON))
		if err != nil {
			return nil, errors.New("source_invalid")
		}
		body = map[string]any{"id": id, "traceId": traceID, "name": "agent.turn", "startTime": transportTime(f.StartedAt), "endTime": transportTime(*f.EndedAt), "input": input, "output": output, "level": "DEFAULT"}
		if f.Kind == artifactdomain.FragmentModel {
			id, eventType = fragmentID("model", append(seed, f.ObservationID)...), "generation-create"
			body["id"], body["name"], body["parentObservationId"], body["model"], body["statusMessage"] = id, "model.generation", turnID, f.Model, f.Status
			body["usageDetails"] = map[string]int64{"input": f.InputTokens, "output": f.OutputTokens, "total": f.InputTokens + f.OutputTokens}
			if f.Status != "ok" {
				body["level"] = "ERROR"
			}
			metadata["source_observation_id"], metadata["model_route"], metadata["status"] = f.ObservationID, f.Route, f.Status
		}
	}
	transportMetadata, err := encodeMetadataForTransport(metadata)
	if err != nil {
		return nil, errors.New("source_invalid")
	}
	body["metadata"] = transportMetadata
	wire, err := json.Marshal(IngestionBatch{Batch: []IngestionEvent{{ID: fragmentID("event:"+string(f.Kind), id), Timestamp: transportTime(f.StartedAt), Type: eventType, Body: body}}})
	if err != nil {
		return nil, errors.New("source_invalid")
	}
	if ctx.Err() != nil {
		return nil, errors.New("projection_cancelled")
	}
	return wire, nil
}

func fragmentID(role string, parts ...string) string {
	encoded, _ := json.Marshal(append([]string{FragmentProjectionVersion, role}, parts...))
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}
