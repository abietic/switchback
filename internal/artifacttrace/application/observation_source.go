package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	ObservationSourceSchemaVersion = "turn.observation.source.v1"
	ObservationSourceSegmentKind   = "turn-observation-source"
)

type TurnObservationSource struct {
	SchemaVersion     string                       `json:"schema_version"`
	Identity          artifactdomain.TurnIdentity  `json:"identity"`
	StartedAt         time.Time                    `json:"started_at"`
	EndedAt           time.Time                    `json:"ended_at"`
	Input             json.RawMessage              `json:"input"`
	ModelObservations []workerapp.ModelObservation `json:"model_observations"`
	ErrorCode         string                       `json:"error_code,omitempty"`
}

func EncodeTurnObservationSource(source TurnObservationSource) ([]byte, error) {
	source.ModelObservations = append([]workerapp.ModelObservation(nil), source.ModelObservations...)
	sort.Slice(source.ModelObservations, func(i, j int) bool {
		return source.ModelObservations[i].ObservationID < source.ModelObservations[j].ObservationID
	})
	if err := validateTurnObservationSource(source); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(source); err != nil {
		return nil, errors.New("encode turn observation source failed")
	}
	return output.Bytes(), nil
}

func DecodeTurnObservationSource(data []byte) (TurnObservationSource, error) {
	if len(data) == 0 {
		return TurnObservationSource{}, errors.New("turn observation source is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var source TurnObservationSource
	if err := decoder.Decode(&source); err != nil {
		return TurnObservationSource{}, errors.New("decode turn observation source failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return TurnObservationSource{}, errors.New("turn observation source contains multiple records")
		}
		return TurnObservationSource{}, errors.New("decode trailing turn observation source data failed")
	}
	if err := validateTurnObservationSource(source); err != nil {
		return TurnObservationSource{}, err
	}
	return source, nil
}

func ReadTurnObservationSource(ctx context.Context, store workerapp.ObjectStore, manifest contractsv1.TraceManifest) (TurnObservationSource, error) {
	if store == nil {
		return TurnObservationSource{}, permanentProjectionError("source_store_invalid")
	}
	var segment *contractsv1.TraceSegment
	for index := range manifest.Segments {
		if manifest.Segments[index].Kind != ObservationSourceSegmentKind {
			continue
		}
		if segment != nil {
			return TurnObservationSource{}, permanentProjectionError("source_segment_duplicate")
		}
		segment = &manifest.Segments[index]
	}
	if segment == nil {
		return TurnObservationSource{}, permanentProjectionError("source_segment_missing")
	}
	if segment.SizeBytes < 0 || segment.RecordCount != 1 || segment.FirstSequence != 1 || segment.LastSequence != 1 {
		return TurnObservationSource{}, permanentProjectionError("source_segment_metadata_invalid")
	}
	key, ok := strings.CutPrefix(segment.URI, "object://")
	if !ok || key == "" {
		return TurnObservationSource{}, permanentProjectionError("source_segment_uri_invalid")
	}
	reader, err := store.Open(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			return TurnObservationSource{}, retryableProjectionError("source_read_cancelled")
		}
		if errors.Is(err, fs.ErrNotExist) {
			return TurnObservationSource{}, permanentProjectionError("source_object_missing")
		}
		return TurnObservationSource{}, retryableProjectionError("source_object_unavailable")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, segment.SizeBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return TurnObservationSource{}, retryableProjectionError("source_read_cancelled")
		}
		return TurnObservationSource{}, retryableProjectionError("source_object_unavailable")
	}
	if int64(len(data)) != segment.SizeBytes {
		return TurnObservationSource{}, permanentProjectionError("source_size_mismatch")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != segment.SHA256 {
		return TurnObservationSource{}, permanentProjectionError("source_checksum_mismatch")
	}
	source, err := DecodeTurnObservationSource(data)
	if err != nil {
		return TurnObservationSource{}, permanentProjectionError("source_payload_invalid")
	}
	if err := validateSourceManifestIdentity(source.Identity, manifest); err != nil {
		return TurnObservationSource{}, permanentProjectionError("source_identity_mismatch")
	}
	return source, nil
}

func validateTurnObservationSource(source TurnObservationSource) error {
	if source.SchemaVersion != ObservationSourceSchemaVersion {
		return errors.New("observation source schema is unsupported")
	}
	if source.StartedAt.IsZero() || source.EndedAt.Before(source.StartedAt) {
		return errors.New("observation source time range is invalid")
	}
	if err := validateObservationSourceIdentity(source.Identity); err != nil {
		return err
	}
	marker := source.ErrorCode != ""
	if marker && source.ErrorCode != "source_incomplete" && source.ErrorCode != "credential_detected" {
		return errors.New("observation source error code is invalid")
	}
	if marker && (!isNullJSON(source.Input) || len(source.ModelObservations) != 0) {
		return errors.New("observation source error marker contains payload")
	}
	if !marker && (isNullJSON(source.Input) || len(source.ModelObservations) == 0) {
		return errors.New("normal observation source requires input and model observations")
	}
	if len(source.Input) != 0 && !json.Valid(source.Input) {
		return errors.New("observation source input is invalid JSON")
	}
	seen := make(map[string]struct{}, len(source.ModelObservations))
	for index, observation := range source.ModelObservations {
		if observation.ObservationID == "" {
			return fmt.Errorf("model observation %d identity is required", index)
		}
		if _, exists := seen[observation.ObservationID]; exists {
			return fmt.Errorf("model observation %d identity is duplicated", index)
		}
		seen[observation.ObservationID] = struct{}{}
		if observation.WorkerSessionID != string(source.Identity.WorkerSessionID) || observation.WorkerRunID != string(source.Identity.WorkerRunID) || observation.AgentTurnID != string(source.Identity.AgentTurnID) || observation.RuntimeID != string(source.Identity.RuntimeInstanceID) || observation.Generation != uint64(source.Identity.RuntimeGeneration) {
			return fmt.Errorf("model observation %d identity mismatch", index)
		}
		if observation.Model == "" || observation.Route == "" || observation.Status == "" ||
			isNullJSON(observation.Input) || !json.Valid(observation.Input) || len(observation.Output) == 0 || !json.Valid(observation.Output) ||
			observation.Status == "ok" && isNullJSON(observation.Output) ||
			observation.InputTokens < 0 || observation.OutputTokens < 0 ||
			observation.StartedAt.IsZero() || observation.EndedAt.Before(observation.StartedAt) {
			return fmt.Errorf("model observation %d is incomplete", index)
		}
	}
	return nil
}

func isNullJSON(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func validateObservationSourceIdentity(identity artifactdomain.TurnIdentity) error {
	if err := artifactdomain.ValidateTurnIdentity(identity); err != nil {
		return errors.New("observation source identity is invalid")
	}
	return nil
}

func validateSourceManifestIdentity(identity artifactdomain.TurnIdentity, manifest contractsv1.TraceManifest) error {
	if identity.TraceStreamID != manifest.TraceStreamID || identity.TenantID != manifest.TenantID || identity.UserID != manifest.UserID || identity.WorkspaceID != manifest.WorkspaceID || identity.WorkerSessionID != manifest.WorkerSessionID || identity.WorkerRunID != manifest.WorkerRunID || identity.AgentTurnID != manifest.AgentTurnID || identity.RuntimeInstanceID != manifest.RuntimeInstanceID || identity.RuntimeGeneration != manifest.RuntimeGeneration {
		return errors.New("observation source identity mismatch with TraceManifest")
	}
	return nil
}
