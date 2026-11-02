package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type ProjectionRunRoot struct {
	TenantID, UserID, WorkspaceID, WorkerSessionID, WorkerRunID string
	StartedAt                                                   time.Time
	Input                                                       json.RawMessage
}

type ProjectionSourceMetadata struct {
	Root           ProjectionRunRoot
	RootManifest   contractsv1.TraceManifest
	Manifest       contractsv1.TraceManifest
	ManifestObject workerapp.StoredObject
	Report         contractsv1.ArtifactRef
}

type ProjectionSourceMetadataStore interface {
	LoadProjectionSourceMetadata(context.Context, ProjectionSourceRef) (ProjectionSourceMetadata, error)
}

type SourceProjection struct {
	SchemaVersion string
	Source        ProjectionSourceRef
	Root          ProjectionRunRoot
	Turn          TurnObservationSource
	Output        json.RawMessage
}

type ProjectionSourceLoader struct {
	Metadata ProjectionSourceMetadataStore
	Objects  workerapp.ObjectStore
}

func (loader ProjectionSourceLoader) Load(ctx context.Context, job ProjectionJob) (SourceProjection, error) {
	if loader.Metadata == nil || loader.Objects == nil {
		return SourceProjection{}, projectionFailure("source_dependencies_invalid")
	}
	metadata, err := loader.Metadata.LoadProjectionSourceMetadata(ctx, job.Source)
	if err != nil {
		return SourceProjection{}, normalizeProjectionError(ctx, err, "source_metadata_unavailable")
	}
	if err := metadata.RootManifest.Validate(); err != nil ||
		metadata.RootManifest.TenantID != metadata.Root.TenantID || metadata.RootManifest.UserID != metadata.Root.UserID ||
		metadata.RootManifest.WorkspaceID != metadata.Root.WorkspaceID || string(metadata.RootManifest.WorkerSessionID) != metadata.Root.WorkerSessionID ||
		string(metadata.RootManifest.WorkerRunID) != metadata.Root.WorkerRunID {
		return SourceProjection{}, projectionFailure("root_manifest_invalid")
	}
	if err := metadata.Report.Validate(); err != nil {
		return SourceProjection{}, projectionFailure("report_metadata_invalid")
	}
	manifestBytes, err := readVerifiedObject(ctx, loader.Objects, metadata.ManifestObject, "manifest_object_unavailable", "manifest_object_missing", "manifest_object_invalid")
	if err != nil {
		return SourceProjection{}, err
	}
	decodedManifest, err := contractsv1.DecodeTraceManifest(manifestBytes)
	if err != nil || !reflect.DeepEqual(decodedManifest, metadata.Manifest) {
		return SourceProjection{}, projectionFailure("manifest_object_invalid")
	}
	turn, err := ReadTurnObservationSource(ctx, loader.Objects, metadata.Manifest)
	if err != nil {
		return SourceProjection{}, normalizeProjectionError(ctx, err, "source_object_unavailable")
	}
	if turn.ErrorCode != "" {
		return SourceProjection{}, projectionFailure("source_incomplete")
	}
	rootTurn, err := ReadTurnObservationSource(ctx, loader.Objects, metadata.RootManifest)
	if err != nil {
		return SourceProjection{}, normalizeProjectionError(ctx, err, "root_source_unavailable")
	}
	if rootTurn.ErrorCode != "" {
		return SourceProjection{}, projectionFailure("root_source_incomplete")
	}
	if !ProjectionJSONEqual(rootTurn.Input, metadata.Root.Input) {
		return SourceProjection{}, projectionFailure("root_input_mismatch")
	}
	reportKey, ok := strings.CutPrefix(metadata.Report.URI, "object://")
	if !ok || reportKey == "" {
		return SourceProjection{}, projectionFailure("report_object_invalid")
	}
	reportBytes, err := readVerifiedObject(ctx, loader.Objects, workerapp.StoredObject{Key: reportKey, Size: metadata.Report.SizeBytes, Checksum: "sha256:" + metadata.Report.SHA256}, "report_object_unavailable", "report_object_missing", "report_object_invalid")
	if err != nil {
		return SourceProjection{}, err
	}
	if !utf8.Valid(reportBytes) {
		return SourceProjection{}, projectionFailure("report_encoding_invalid")
	}
	output, err := json.Marshal(string(reportBytes))
	if err != nil {
		return SourceProjection{}, projectionFailure("report_object_invalid")
	}
	return SourceProjection{SchemaVersion: job.SchemaVersion, Source: job.Source, Root: metadata.Root, Turn: turn, Output: output}, nil
}

func projectionFailure(code string) error { return &ProjectionError{Code: code, Permanent: true} }
func normalizeProjectionError(ctx context.Context, err error, fallback string) error {
	var typed *ProjectionError
	if errors.As(err, &typed) {
		return &ProjectionError{Code: typed.Code, Permanent: typed.Permanent}
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return retryableProjectionError("source_load_cancelled")
	}
	return retryableProjectionError(fallback)
}
func readVerifiedObject(ctx context.Context, store workerapp.ObjectStore, object workerapp.StoredObject, unavailableCode, missingCode, invalidCode string) ([]byte, error) {
	if object.Key == "" || object.Size < 0 || !strings.HasPrefix(object.Checksum, "sha256:") {
		return nil, projectionFailure(invalidCode)
	}
	reader, err := store.Open(ctx, object.Key)
	if err != nil {
		if ctx.Err() != nil {
			return nil, retryableProjectionError("source_load_cancelled")
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, projectionFailure(missingCode)
		}
		return nil, retryableProjectionError(unavailableCode)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, object.Size+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, retryableProjectionError("source_load_cancelled")
		}
		return nil, retryableProjectionError(unavailableCode)
	}
	if int64(len(data)) != object.Size {
		return nil, projectionFailure(invalidCode)
	}
	digest := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(digest[:]) != object.Checksum {
		return nil, projectionFailure(invalidCode)
	}
	return bytes.Clone(data), nil
}
