package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
)

type observationScanner interface {
	ScanJSON(context.Context, string, any) error
}

func runtimeObservationIdentity(turn pendingTurn) artifactdomain.TurnIdentity {
	return artifactdomain.TurnIdentity{TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID, TaskID: contractsv1.TaskID(turn.TaskID), WorkerSessionID: contractsv1.WorkerSessionID(turn.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(turn.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(turn.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(turn.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(turn.RuntimeInstanceID), RuntimeGeneration: uint32(turn.RuntimeGeneration), TraceStreamID: "trace-" + turn.AgentTurnID, LangfuseTraceID: "langfuse-" + turn.AgentTurnID, RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{ID: turn.NetworkPolicyID, Revision: turn.NetworkPolicyRevision, Digest: turn.NetworkPolicyDigest, EnforcementStatus: turn.NetworkEnforcementStatus}}
}

// captureRuntimeObservationSource reads only durable applied events. A missing
// source cut or scanner produces optional degradation; a database failure is
// still a core failure and must not be disguised as missing observation data.
func (runner *runtimeRunner) captureRuntimeObservationSource(ctx context.Context, live *liveRuntime, turn pendingTurn, endedAt, recordedAt time.Time, cut uint64, raw, patch []byte) ([]byte, error) {
	var scanners []observationScanner
	materializer := runtimesecret.Materializer{OwnerUID: runtimesecret.OSOwnerUID}
	if runner.environment != nil {
		materializer = runner.environment.secrets
	}
	runtimeScanner, registryErr := materializer.ObservationScanner(runtimesecret.ObservationRegistryRequest{WorkerSessionID: turn.WorkerSessionID, RuntimeInstanceID: turn.RuntimeInstanceID, Generation: turn.RuntimeGeneration, SessionStorage: live.storage})
	if registryErr == nil {
		scanners = append(scanners, runtimeScanner)
	}
	if scanner, err := newRuntimeKnownTokenScanner(runner.configuration); err == nil {
		scanners = append(scanners, scanner)
	}
	complete := registryErr == nil && cut > 0
	var events []contractsv1.RuntimeControlEnvelope
	if cut > 0 && runner.pool != nil {
		err := runner.loadRuntimeObservationTurn(ctx, &turn)
		if errors.Is(err, pgx.ErrNoRows) {
			complete = false
		} else if err != nil {
			return nil, errors.New("load observation turn evidence failed")
		}
		rows, err := runner.pool.Query(ctx, `SELECT envelope FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='event' AND effect_status='applied' AND sequence<=$2 AND envelope->>'message_type'='model.observation' AND envelope->>'worker_session_id'=$3 AND envelope->>'worker_run_id'=$4 AND envelope->>'agent_session_id'=$5 AND envelope->>'agent_turn_id'=$6 AND envelope->>'runtime_generation'=$7 ORDER BY sequence`, turn.RuntimeInstanceID, cut, turn.WorkerSessionID, turn.WorkerRunID, turn.AgentSessionID, turn.AgentTurnID, fmtGeneration(turn.RuntimeGeneration))
		if err != nil {
			return nil, errors.New("load observation journal failed")
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			if err := rows.Scan(&encoded); err != nil {
				return nil, errors.New("read observation journal failed")
			}
			var event contractsv1.RuntimeControlEnvelope
			if json.Unmarshal(encoded, &event) != nil {
				complete = false
				continue
			}
			events = append(events, event)
		}
		if rows.Err() != nil {
			return nil, errors.New("read observation journal failed")
		}
	} else {
		complete = false
	}
	// This is the same pure builder the finalizer uses, over the frozen ACP range.
	report, err := runtimeObservationReport(turn, recordedAt, raw, patch)
	if err != nil {
		complete = false
	}
	return encodeRuntimeObservationSource(ctx, turn, endedAt, cut, events, scanners, complete, report), nil
}

func fmtGeneration(generation uint64) string {
	data, _ := json.Marshal(generation)
	return string(data)
}

func encodeRuntimeObservationSource(ctx context.Context, turn pendingTurn, endedAt time.Time, cut uint64, events []contractsv1.RuntimeControlEnvelope, scanners []observationScanner, complete bool, report []byte) []byte {
	source := artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: runtimeObservationIdentity(turn), StartedAt: turn.StartedAt, EndedAt: endedAt, Input: turn.Content}
	if !complete || cut == 0 || len(scanners) == 0 {
		source.ErrorCode = "source_incomplete"
	}
	models := make(map[string]workerapp.ModelObservation)
	credentialDetected := false
	for _, event := range events {
		if event.Sequence > cut {
			continue
		}
		for _, scanner := range scanners {
			if err := scanner.ScanJSON(ctx, "turn.observation.event", event); err != nil {
				if secretcheck.IsExposure(err) {
					credentialDetected = true
				} else {
					source.ErrorCode = "source_incomplete"
				}
			}
		}
		if event.MessageType != "model.observation" || string(event.WorkerSessionID) != turn.WorkerSessionID || string(event.WorkerRunID) != turn.WorkerRunID || string(event.AgentSessionID) != turn.AgentSessionID || string(event.AgentTurnID) != turn.AgentTurnID || string(event.RuntimeInstanceID) != turn.RuntimeInstanceID || uint64(event.RuntimeGeneration) != turn.RuntimeGeneration {
			source.ErrorCode = "source_incomplete"
			continue
		}
		var model workerapp.ModelObservation
		if json.Unmarshal(event.Payload, &model) != nil {
			source.ErrorCode = "source_incomplete"
			continue
		}
		if previous, ok := models[model.ObservationID]; ok && !reflect.DeepEqual(previous, model) {
			source.ErrorCode = "source_incomplete"
		}
		models[model.ObservationID] = model
	}
	for _, model := range models {
		source.ModelObservations = append(source.ModelObservations, model)
	}
	if credentialDetected {
		source.ErrorCode = "credential_detected"
	}
	for _, scanner := range scanners {
		for _, value := range []any{source, string(report)} {
			if err := scanner.ScanJSON(ctx, "turn.observation.source", value); err != nil {
				if secretcheck.IsExposure(err) {
					source.ErrorCode = "credential_detected"
				} else if source.ErrorCode != "credential_detected" {
					source.ErrorCode = "source_incomplete"
				}
			}
		}
	}
	if _, err := artifactapp.EncodeTurnObservationSource(source); err != nil && source.ErrorCode == "" {
		source.ErrorCode = "source_incomplete"
	}
	if source.ErrorCode != "" {
		source.Input = json.RawMessage("null")
		source.ModelObservations = nil
	}
	// Required identity metadata can also contain a known token. Never seal it.
	for _, scanner := range scanners {
		if err := scanner.ScanJSON(ctx, "turn.observation.marker", source); err != nil {
			slog.Warn("observation_source_omitted")
			return nil
		}
	}
	encoded, err := artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		slog.Warn("observation_source_omitted")
		return nil
	}
	return encoded
}
