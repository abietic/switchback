package application

import (
	"encoding/json"
	"errors"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

// RuntimeObservationCapture is trusted Backend-only data, never Worker wire.
type RuntimeObservationCapture struct {
	Sealed   []byte                        `json:"-"`
	SafeCode RuntimeObservationCaptureCode `json:"-"`
}
type RuntimeObservationCaptureCode string

const (
	ObservationCaptureUnavailable RuntimeObservationCaptureCode = "observation_capture_unavailable"
	ObservationCaptureTimeout     RuntimeObservationCaptureCode = "observation_capture_timeout"
	ObservationCaptureInvalid     RuntimeObservationCaptureCode = "observation_capture_invalid"
)

func (c RuntimeObservationCapture) Clone() RuntimeObservationCapture {
	c.Sealed = append([]byte(nil), c.Sealed...)
	return c
}

// ValidateModelObservationBinding checks the historical execution proof, not
// the Session's current Turn. Current runtime/channel authority is a separate
// journal lock and must already have succeeded.
func ValidateModelObservationBinding(event, start RuntimeControlMessage) error {
	decode := func(m RuntimeControlMessage) (contractsv1.RuntimeControlEnvelope, error) {
		e, err := contractsv1.DecodeRuntimeControlEnvelope(m.Envelope)
		digest, digestErr := contractsv1.RuntimeControlEnvelopeDigest(e)
		if err != nil || digestErr != nil || digest != m.Fingerprint || e.Sequence != m.Sequence || e.MessageID != m.MessageID || e.IdempotencyKey != m.IdempotencyKey {
			return e, errors.New("model_observation_binding_invalid")
		}
		return e, nil
	}
	e, err := decode(event)
	if err != nil {
		return err
	}
	s, err := decode(start)
	if err != nil {
		return err
	}
	if event.Direction != RuntimeControlDirectionEvent || start.Direction != RuntimeControlDirectionCommand || e.Kind != "event" || e.MessageType != "model.observation" || s.Kind != "command" || s.MessageType != "agent_turn.start" || (start.EffectStatus != "active" && start.EffectStatus != "terminal") || s.WorkerSessionID != e.WorkerSessionID || s.WorkerRunID != e.WorkerRunID || s.AgentSessionID != e.AgentSessionID || s.AgentTurnID != e.AgentTurnID || s.RuntimeInstanceID != e.RuntimeInstanceID || s.RuntimeGeneration != e.RuntimeGeneration || s.ChannelEpoch != e.ChannelEpoch {
		return errors.New("model_observation_binding_invalid")
	}
	// Start payload is the existing prompt protocol. Optional identity fields
	// must never contradict the enclosing immutable command.
	var fields map[string]json.RawMessage
	if json.Unmarshal(s.Payload, &fields) != nil {
		return errors.New("model_observation_binding_invalid")
	}
	for key, want := range map[string]string{"worker_session_id": string(s.WorkerSessionID), "worker_run_id": string(s.WorkerRunID), "agent_session_id": string(s.AgentSessionID), "agent_turn_id": string(s.AgentTurnID), "runtime_instance_id": string(s.RuntimeInstanceID)} {
		if raw, ok := fields[key]; ok {
			var got string
			if json.Unmarshal(raw, &got) != nil || got != want {
				return errors.New("model_observation_binding_invalid")
			}
		}
	}
	for key, want := range map[string]string{"WorkerSessionID": string(s.WorkerSessionID), "WorkerRunID": string(s.WorkerRunID), "AgentSessionID": string(s.AgentSessionID), "AgentTurnID": string(s.AgentTurnID), "RuntimeID": string(s.RuntimeInstanceID)} {
		if raw, ok := fields[key]; ok {
			var got string
			if json.Unmarshal(raw, &got) != nil || got != want {
				return errors.New("model_observation_binding_invalid")
			}
		}
	}
	if raw, ok := fields["runtime_generation"]; ok {
		var got uint32
		if json.Unmarshal(raw, &got) != nil || got != s.RuntimeGeneration {
			return errors.New("model_observation_binding_invalid")
		}
	}
	if raw, ok := fields["Generation"]; ok {
		var got uint32
		if json.Unmarshal(raw, &got) != nil || got != s.RuntimeGeneration {
			return errors.New("model_observation_binding_invalid")
		}
	}
	var m ModelObservation
	if json.Unmarshal(e.Payload, &m) != nil || m.ObservationID == "" || m.WorkerSessionID != string(e.WorkerSessionID) || m.WorkerRunID != string(e.WorkerRunID) || m.AgentTurnID != string(e.AgentTurnID) || m.RuntimeID != string(e.RuntimeInstanceID) || m.Generation != uint64(e.RuntimeGeneration) {
		return errors.New("model_observation_binding_invalid")
	}
	if validateCanonical("observation_id", m.ObservationID) != nil {
		return errors.New("model_observation_binding_invalid")
	}
	return nil
}
