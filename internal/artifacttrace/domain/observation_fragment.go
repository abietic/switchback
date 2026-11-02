package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type FragmentKind string

const (
	FragmentRunBasis  FragmentKind = "run_basis"
	FragmentTurnBasis FragmentKind = "turn_basis"
	FragmentModel     FragmentKind = "model"
)

type ObservationScope struct {
	TenantID        string `json:"tenant_id"`
	UserID          string `json:"user_id"`
	WorkspaceID     string `json:"workspace_id"`
	WorkerSessionID string `json:"worker_session_id"`
	WorkerRunID     string `json:"worker_run_id"`
}

type ObservationFragment struct {
	SchemaVersion string           `json:"schema_version"`
	Kind          FragmentKind     `json:"kind"`
	Scope         ObservationScope `json:"scope"`
	InitialTurnID string           `json:"initial_turn_id,omitempty"`
	Turn          *TurnIdentity    `json:"turn,omitempty"`
	ObservationID string           `json:"observation_id,omitempty"`
	StartedAt     time.Time        `json:"started_at"`
	EndedAt       *time.Time       `json:"ended_at,omitempty"`
	InputJSON     string           `json:"input_json"`
	OutputJSON    string           `json:"output_json,omitempty"`
	Model         string           `json:"model,omitempty"`
	Route         string           `json:"route,omitempty"`
	Status        string           `json:"status,omitempty"`
	InputTokens   int64            `json:"input_tokens,omitempty"`
	OutputTokens  int64            `json:"output_tokens,omitempty"`
}

func EncodeObservationFragment(frame ObservationFragment) ([]byte, error) {
	if !validObservationFragment(frame) {
		return nil, errors.New("source_invalid")
	}
	data, err := json.Marshal(frame)
	if err != nil || len(data) > maxJSONLRecordBytes {
		return nil, errors.New("source_invalid")
	}
	return data, nil
}

func DecodeObservationFragment(data []byte) (ObservationFragment, error) {
	var frame ObservationFragment
	if len(data) > maxJSONLRecordBytes || !validObservationJSON(string(data), false) {
		return ObservationFragment{}, errors.New("source_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&frame) != nil || !validObservationFragment(frame) {
		return ObservationFragment{}, errors.New("source_invalid")
	}
	// Reject forbidden fields even when explicitly null, empty or zero.
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ObservationFragment{}, errors.New("source_invalid")
	}
	if !observationFieldsAllowed(fields, "schema_version kind scope initial_turn_id turn observation_id started_at ended_at input_json output_json model route status input_tokens output_tokens") || !observationNestedFieldsAllowed(fields["scope"], "tenant_id user_id workspace_id worker_session_id worker_run_id") {
		return ObservationFragment{}, errors.New("source_invalid")
	}
	if frame.Turn != nil {
		var turnFields map[string]json.RawMessage
		if json.Unmarshal(fields["turn"], &turnFields) != nil || !observationFieldsAllowed(turnFields, "tenant_id user_id workspace_id task_id worker_session_id worker_run_id agent_session_id agent_turn_id runtime_instance_id runtime_generation runtime_network_profile trace_stream_id langfuse_trace_id") {
			return ObservationFragment{}, errors.New("source_invalid")
		}
		if network, ok := turnFields["runtime_network_profile"]; ok && !observationNestedFieldsAllowed(network, "id revision digest enforcement_status") {
			return ObservationFragment{}, errors.New("source_invalid")
		}
	}
	for key := range fields {
		if frame.Kind == FragmentRunBasis && (key == "turn" || key == "ended_at" || key == "output_json") || frame.Kind != FragmentRunBasis && key == "initial_turn_id" || frame.Kind != FragmentModel && (key == "observation_id" || key == "model" || key == "route" || key == "status" || key == "input_tokens" || key == "output_tokens") {
			return ObservationFragment{}, errors.New("source_invalid")
		}
	}
	return frame, nil
}

func ObservationFragmentID(frame ObservationFragment) (string, error) {
	if !validObservationFragment(frame) {
		return "", errors.New("source_invalid")
	}
	return observationFragmentIdentity(frame), nil
}

// ObservationBasisSourceID identifies no-content basis evidence without
// inventing input, report, time or an initial Turn to construct a valid frame.
func ObservationBasisSourceID(kind FragmentKind, scope ObservationScope, turn *TurnIdentity) (string, error) {
	for _, value := range []string{scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID} {
		if !canonical(value) || !utf8.ValidString(value) {
			return "", errors.New("scope_invalid")
		}
	}
	if kind == FragmentRunBasis && turn != nil || kind == FragmentTurnBasis && turn == nil || kind != FragmentRunBasis && kind != FragmentTurnBasis {
		return "", errors.New("source_invalid")
	}
	if turn != nil {
		if turn.TenantID != scope.TenantID || turn.UserID != scope.UserID || turn.WorkspaceID != scope.WorkspaceID || string(turn.WorkerSessionID) != scope.WorkerSessionID || string(turn.WorkerRunID) != scope.WorkerRunID || turn.RuntimeGeneration == 0 {
			return "", errors.New("scope_invalid")
		}
		for _, value := range []string{string(turn.AgentTurnID), string(turn.AgentSessionID), string(turn.RuntimeInstanceID)} {
			if !canonical(value) || !utf8.ValidString(value) {
				return "", errors.New("scope_invalid")
			}
		}
	}
	return observationFragmentIdentity(ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: kind, Scope: scope, Turn: turn}), nil
}

func observationFragmentIdentity(frame ObservationFragment) string {
	s := frame.Scope
	seed := []string{frame.SchemaVersion, string(frame.Kind), s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID}
	if frame.Turn != nil {
		i := frame.Turn
		seed = append(seed, string(i.AgentTurnID), string(i.AgentSessionID), string(i.RuntimeInstanceID), strconv.FormatUint(uint64(i.RuntimeGeneration), 10))
	}
	if frame.Kind == FragmentModel {
		seed = append(seed, frame.ObservationID)
	}
	return observationIdentityDigest(seed)
}

func validObservationFragment(f ObservationFragment) bool {
	if len(f.InputJSON) > maxJSONLRecordBytes || len(f.OutputJSON) > maxJSONLRecordBytes || len(f.InputJSON)+len(f.OutputJSON) > maxJSONLRecordBytes {
		return false
	}
	s := f.Scope
	for _, value := range []string{s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID} {
		if !canonical(value) || !utf8.ValidString(value) {
			return false
		}
	}
	if f.SchemaVersion != "observation.fragment.v1" || f.StartedAt.IsZero() || !validObservationJSON(f.InputJSON, false) {
		return false
	}
	if f.Kind == FragmentRunBasis {
		return canonical(f.InitialTurnID) && utf8.ValidString(f.InitialTurnID) && f.Turn == nil && f.EndedAt == nil && f.OutputJSON == "" && emptyModelFields(f)
	}
	if f.Kind != FragmentTurnBasis && f.Kind != FragmentModel {
		return false
	}
	i := f.Turn
	if i == nil || f.InitialTurnID != "" || i.TenantID != s.TenantID || i.UserID != s.UserID || i.WorkspaceID != s.WorkspaceID || string(i.WorkerSessionID) != s.WorkerSessionID || string(i.WorkerRunID) != s.WorkerRunID || i.RuntimeGeneration == 0 {
		return false
	}
	for _, value := range []string{string(i.AgentTurnID), string(i.AgentSessionID), string(i.RuntimeInstanceID)} {
		if !canonical(value) || !utf8.ValidString(value) {
			return false
		}
	}
	for _, value := range []string{string(i.TaskID), i.TraceStreamID, i.LangfuseTraceID, i.RuntimeNetwork.ID, i.RuntimeNetwork.Digest, i.RuntimeNetwork.EnforcementStatus} {
		if !utf8.ValidString(value) {
			return false
		}
	}
	if f.EndedAt == nil || f.EndedAt.IsZero() || f.EndedAt.Before(f.StartedAt) {
		return false
	}
	if f.Kind == FragmentTurnBasis {
		return emptyModelFields(f) && validObservationJSON(f.OutputJSON, false)
	}
	for _, value := range []string{f.ObservationID, f.Model, f.Route, f.Status} {
		if !canonical(value) || !utf8.ValidString(value) {
			return false
		}
	}
	const maxSafeInteger int64 = 1<<53 - 1
	return f.InputTokens >= 0 && f.OutputTokens >= 0 && f.InputTokens <= maxSafeInteger && f.OutputTokens <= maxSafeInteger-f.InputTokens && validObservationJSON(f.OutputJSON, f.Status != "ok")
}

func observationFieldsAllowed(fields map[string]json.RawMessage, names string) bool {
	for field := range fields {
		if !strings.Contains(" "+names+" ", " "+field+" ") {
			return false
		}
	}
	return true
}

func observationNestedFieldsAllowed(raw json.RawMessage, names string) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && fields != nil && observationFieldsAllowed(fields, names)
}

func emptyModelFields(f ObservationFragment) bool {
	return f.ObservationID == "" && f.Model == "" && f.Route == "" && f.Status == "" && f.InputTokens == 0 && f.OutputTokens == 0
}

// Token traversal detects duplicate keys at every depth without decoding JSON
// numbers through float64 or rewriting the original I/O text.
func validObservationJSON(raw string, allowNull bool) bool {
	if !utf8.ValidString(raw) || !json.Valid([]byte(raw)) || !allowNull && strings.TrimSpace(raw) == "null" {
		return false
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if !uniqueObservationJSONValue(d) {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}

func uniqueObservationJSONValue(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	if delim == '{' {
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueObservationJSONValue(d) {
				return false
			}
		}
	} else if delim == '[' {
		for d.More() {
			if !uniqueObservationJSONValue(d) {
				return false
			}
		}
	} else {
		return false
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

func observationSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func observationIdentityDigest(parts []string) string {
	data, err := json.Marshal(parts)
	if err != nil {
		panic("string identity encoding failed")
	}
	return observationSHA256(data)
}
