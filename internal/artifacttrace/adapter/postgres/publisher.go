// Package postgres persists visible Artifact and Trace metadata.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

var (
	ErrEvidenceTargetNotFound = errors.New("evidence publication target not found")
	ErrEvidenceTargetStale    = errors.New("evidence publication target revision is stale")
	ErrPublicationConflict    = errors.New("evidence publication conflicts with existing metadata")
)

type MetadataStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewMetadataStore(pool *pgxpool.Pool) *MetadataStore {
	return &MetadataStore{pool: pool}
}

func (store *MetadataStore) PublishTurnEvidence(
	ctx context.Context,
	record artifactapp.PublishTurnEvidenceRecord,
) (artifactapp.PublishedTurnEvidence, error) {
	if store == nil || store.pool == nil {
		return artifactapp.PublishedTurnEvidence{}, fmt.Errorf("Artifact metadata PostgreSQL pool is required")
	}
	var result artifactapp.PublishedTurnEvidence
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var err error
		result, err = store.publishTurnEvidenceTx(ctx, tx, record)
		return err
	})
	return result, err
}

func (store *MetadataStore) publishTurnEvidenceTx(ctx context.Context, tx platformpostgres.Tx, record artifactapp.PublishTurnEvidenceRecord) (result artifactapp.PublishedTurnEvidence, err error) {
	err = func() error {
		manifest := record.Command.Evidence.Manifest
		existing, found, err := findExistingPublication(ctx, tx, record)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		target, err := lockPublicationTarget(ctx, tx, manifest)
		if err != nil {
			return err
		}
		if target.SessionRevision != record.Command.ExpectedSessionRevision ||
			target.RunRevision != record.Command.ExpectedRunRevision ||
			target.TurnRevision != record.Command.ExpectedTurnRevision ||
			target.RuntimeRevision != record.Command.ExpectedRuntimeRevision {
			return ErrEvidenceTargetStale
		}
		if target.RuntimeGeneration != manifest.RuntimeGeneration || !terminalTurn(target.TurnStatus) {
			return ErrEvidenceTargetStale
		}
		if target.ActiveRuntimeInstanceID != string(manifest.RuntimeInstanceID) ||
			target.SessionRuntimeGeneration != manifest.RuntimeGeneration {
			return ErrEvidenceTargetStale
		}
		for _, ref := range record.Command.Evidence.ArtifactRefs {
			if string(ref.TaskID) != target.TaskID {
				return fmt.Errorf("%w: Artifact Task differs from WorkerRun", ErrPublicationConflict)
			}
		}
		inserted, err := insertTraceManifest(ctx, tx, record)
		if err != nil {
			return err
		}
		if !inserted {
			result, found, err = findExistingPublication(ctx, tx, record)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("idempotent evidence publication disappeared")
			}
			return nil
		}
		if err := insertArtifactRefs(ctx, tx, record); err != nil {
			return err
		}
		if err := insertEvidenceObjectRefs(ctx, tx, record); err != nil {
			return err
		}
		if err := store.appendPublishedEvent(ctx, tx, record); err != nil {
			return err
		}
		result = publicationResult(record.Command.Evidence)
		return nil
	}()
	return result, err
}

type publicationTarget struct {
	TaskID                   string
	TurnStatus               string
	RuntimeGeneration        uint32
	ActiveRuntimeInstanceID  string
	SessionRuntimeGeneration uint32
	SessionRevision          uint64
	RunRevision              uint64
	TurnRevision             uint64
	RuntimeRevision          uint64
}

func lockPublicationTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	manifest contractsv1.TraceManifest,
) (publicationTarget, error) {
	var target publicationTarget
	var taskID *string
	err := tx.QueryRow(ctx, `
SELECT run.task_id,turn.status,runtime.generation,
       COALESCE(session.active_runtime_instance_id,''),session.runtime_generation,
       session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=$3
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1
  AND session.tenant_id=$5 AND session.user_id=$6 AND session.workspace_id=$7
FOR UPDATE OF session,run,turn,runtime`,
		manifest.WorkerSessionID, manifest.WorkerRunID, manifest.AgentTurnID,
		manifest.RuntimeInstanceID, manifest.TenantID, manifest.UserID, manifest.WorkspaceID,
	).Scan(
		&taskID, &target.TurnStatus, &target.RuntimeGeneration,
		&target.ActiveRuntimeInstanceID, &target.SessionRuntimeGeneration,
		&target.SessionRevision, &target.RunRevision, &target.TurnRevision, &target.RuntimeRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return publicationTarget{}, ErrEvidenceTargetNotFound
	}
	if err != nil {
		return publicationTarget{}, fmt.Errorf("lock evidence publication target: %w", err)
	}
	if taskID != nil {
		target.TaskID = *taskID
	}
	return target, nil
}

func insertTraceManifest(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.PublishTurnEvidenceRecord,
) (bool, error) {
	manifest := record.Command.Evidence.Manifest
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("encode TraceManifest metadata: %w", err)
	}
	commandTag, err := tx.Exec(ctx, `
INSERT INTO trace_manifests
  (trace_stream_id,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,
   agent_turn_id,runtime_instance_id,runtime_generation,manifest_object_key,manifest_checksum,
   manifest_size_bytes,manifest,publication_idempotency_key,visible_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (tenant_id,publication_idempotency_key) DO NOTHING`,
		manifest.TraceStreamID, manifest.TenantID, manifest.UserID, manifest.WorkspaceID,
		manifest.WorkerSessionID, manifest.WorkerRunID, manifest.AgentTurnID,
		manifest.RuntimeInstanceID, manifest.RuntimeGeneration,
		record.Command.Evidence.ManifestObject.Key,
		record.Command.Evidence.ManifestObject.Checksum,
		record.Command.Evidence.ManifestObject.Size,
		encoded, record.Command.IdempotencyKey, record.Now,
	)
	if err != nil {
		return false, fmt.Errorf("insert visible TraceManifest metadata: %w", err)
	}
	return commandTag.RowsAffected() == 1, nil
}

func insertArtifactRefs(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.PublishTurnEvidenceRecord,
) error {
	traceStreamID := record.Command.Evidence.Manifest.TraceStreamID
	for _, ref := range record.Command.Evidence.ArtifactRefs {
		encoded, err := json.Marshal(ref)
		if err != nil {
			return fmt.Errorf("encode ArtifactRef %s: %w", ref.ArtifactID, err)
		}
		key := strings.TrimPrefix(ref.URI, "object://")
		var taskID any
		if ref.TaskID != "" {
			taskID = string(ref.TaskID)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO artifact_refs
  (artifact_id,trace_stream_id,tenant_id,user_id,workspace_id,task_id,worker_session_id,
   worker_run_id,agent_turn_id,runtime_instance_id,runtime_generation,artifact_type,
   object_key,object_checksum,size_bytes,artifact_ref,visible_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			ref.ArtifactID, traceStreamID, ref.TenantID, ref.UserID, ref.WorkspaceID, taskID,
			ref.WorkerSessionID, ref.WorkerRunID, ref.AgentTurnID, ref.RuntimeInstanceID,
			ref.RuntimeGeneration, ref.Type, key, "sha256:"+ref.SHA256, ref.SizeBytes,
			encoded, record.Now,
		)
		if err != nil {
			return fmt.Errorf("insert visible ArtifactRef %s: %w", ref.ArtifactID, err)
		}
	}
	return nil
}

type evidenceObjectRef struct {
	Key      string
	Kind     string
	Checksum string
	Size     int64
}

func insertEvidenceObjectRefs(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.PublishTurnEvidenceRecord,
) error {
	manifest := record.Command.Evidence.Manifest
	refs, err := publicationObjectRefs(record.Command.Evidence)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.Key)
	}
	var claimedKey string
	err = tx.QueryRow(ctx, `
SELECT object_key FROM object_reconciliation_claims
WHERE status='claimed' AND object_key=ANY($1)
ORDER BY object_key LIMIT 1`, keys).Scan(&claimedKey)
	if err == nil {
		return fmt.Errorf("%w: object is already claimed for reconciliation", ErrPublicationConflict)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check object reconciliation claims: %w", err)
	}
	for _, ref := range refs {
		_, err := tx.Exec(ctx, `
INSERT INTO evidence_object_refs
  (object_key,trace_stream_id,tenant_id,user_id,workspace_id,object_kind,
   object_checksum,size_bytes,visible_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			ref.Key, manifest.TraceStreamID, manifest.TenantID, manifest.UserID,
			manifest.WorkspaceID, ref.Kind, ref.Checksum, ref.Size, record.Now,
		)
		if err != nil {
			return fmt.Errorf("insert evidence object reference %s: %w", ref.Kind, err)
		}
	}
	return nil
}

func publicationObjectRefs(evidence artifactapp.FinalizedTurnEvidence) ([]evidenceObjectRef, error) {
	refs := []evidenceObjectRef{{
		Key: evidence.ManifestObject.Key, Kind: "trace-manifest",
		Checksum: evidence.ManifestObject.Checksum, Size: evidence.ManifestObject.Size,
	}}
	for _, segment := range evidence.Manifest.Segments {
		refs = append(refs, evidenceObjectRef{
			Key: strings.TrimPrefix(segment.URI, "object://"), Kind: segment.Kind,
			Checksum: "sha256:" + segment.SHA256, Size: segment.SizeBytes,
		})
	}
	for _, artifact := range evidence.ArtifactRefs {
		refs = append(refs, evidenceObjectRef{
			Key: strings.TrimPrefix(artifact.URI, "object://"), Kind: artifact.Type,
			Checksum: "sha256:" + artifact.SHA256, Size: artifact.SizeBytes,
		})
	}
	seen := make(map[string]struct{}, len(refs))
	kindCounts := make(map[string]int, len(refs))
	for _, ref := range refs {
		if ref.Key == "" || !strings.HasPrefix(ref.Checksum, "sha256:") || ref.Size < 0 {
			return nil, fmt.Errorf("invalid evidence object publication reference")
		}
		if _, exists := seen[ref.Key]; exists {
			return nil, fmt.Errorf("duplicate evidence object publication reference")
		}
		seen[ref.Key] = struct{}{}
		kindCounts[ref.Kind]++
	}
	for _, kind := range []string{"trace-manifest", artifactdomain.KindRawACP, artifactdomain.KindNormalized, artifactdomain.KindReport, artifactdomain.KindPatch} {
		if kindCounts[kind] != 1 {
			return nil, fmt.Errorf("terminal evidence publication requires exactly one %s object", kind)
		}
		delete(kindCounts, kind)
	}
	if count := kindCounts[artifactapp.ObservationSourceSegmentKind]; count > 1 {
		return nil, fmt.Errorf("terminal evidence publication permits at most one observation source object")
	} else if count == 1 {
		delete(kindCounts, artifactapp.ObservationSourceSegmentKind)
	}
	if len(kindCounts) != 0 || (len(refs) != 5 && len(refs) != 6) {
		return nil, fmt.Errorf("terminal evidence publication contains unsupported objects")
	}
	return refs, nil
}

func findExistingPublication(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.PublishTurnEvidenceRecord,
) (artifactapp.PublishedTurnEvidence, bool, error) {
	manifest := record.Command.Evidence.Manifest
	var traceStreamID string
	var storedManifest contractsv1.TraceManifest
	err := tx.QueryRow(ctx, `
SELECT trace_stream_id,manifest FROM trace_manifests
WHERE tenant_id=$1 AND publication_idempotency_key=$2`,
		manifest.TenantID, record.Command.IdempotencyKey,
	).Scan(&traceStreamID, &storedManifest)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.PublishedTurnEvidence{}, false, nil
	}
	if err != nil {
		return artifactapp.PublishedTurnEvidence{}, false, fmt.Errorf("read idempotent evidence publication: %w", err)
	}
	if traceStreamID != manifest.TraceStreamID || !reflect.DeepEqual(storedManifest, manifest) {
		return artifactapp.PublishedTurnEvidence{}, false, ErrPublicationConflict
	}
	rows, err := tx.Query(ctx, `
SELECT artifact_id FROM artifact_refs
WHERE trace_stream_id=$1
ORDER BY CASE artifact_type WHEN 'report' THEN 1 ELSE 2 END`, traceStreamID)
	if err != nil {
		return artifactapp.PublishedTurnEvidence{}, false, fmt.Errorf("read idempotent ArtifactRefs: %w", err)
	}
	defer rows.Close()
	var artifactIDs []string
	for rows.Next() {
		var artifactID string
		if err := rows.Scan(&artifactID); err != nil {
			return artifactapp.PublishedTurnEvidence{}, false, err
		}
		artifactIDs = append(artifactIDs, artifactID)
	}
	if err := rows.Err(); err != nil {
		return artifactapp.PublishedTurnEvidence{}, false, err
	}
	want := publicationResult(record.Command.Evidence)
	if !reflect.DeepEqual(artifactIDs, want.ArtifactIDs) {
		return artifactapp.PublishedTurnEvidence{}, false, ErrPublicationConflict
	}
	return artifactapp.PublishedTurnEvidence{TraceStreamID: traceStreamID, ArtifactIDs: artifactIDs}, true, nil
}

func (store *MetadataStore) appendPublishedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record artifactapp.PublishTurnEvidenceRecord,
) error {
	manifest := record.Command.Evidence.Manifest
	stream := platformpostgres.OwnerStream{
		StreamID:     "worker-session/" + string(manifest.WorkerSessionID),
		OwnerContext: "worker-runtime", AggregateType: "WorkerSession",
		AggregateID: string(manifest.WorkerSessionID),
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		TraceStreamID string   `json:"trace_stream_id"`
		WorkerRunID   string   `json:"worker_run_id"`
		AgentTurnID   string   `json:"agent_turn_id"`
		ArtifactIDs   []string `json:"artifact_ids"`
	}{
		TraceStreamID: manifest.TraceStreamID, WorkerRunID: string(manifest.WorkerRunID),
		AgentTurnID: string(manifest.AgentTurnID), ArtifactIDs: publicationResult(record.Command.Evidence).ArtifactIDs,
	})
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: "WorkerTurnEvidencePublished.v1",
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: manifest.TenantID, AggregateType: "WorkerSession",
		AggregateID: string(manifest.WorkerSessionID), TraceID: manifest.LangfuseExport.TraceID,
		CorrelationID: string(manifest.AgentTurnID), CausationID: string(manifest.WorkerRunID),
		IdempotencyKey: "turn-evidence-published/" + record.Command.IdempotencyKey,
		Visibility:     "user", OccurredAt: record.Now, Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
		event.TraceID, event.CorrelationID, event.CausationID, event.IdempotencyKey,
		event.Visibility, event.Payload, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("insert evidence publication Outbox event: %w", err)
	}
	return nil
}

func terminalTurn(status string) bool {
	return status == "completed" || status == "cancelled" || status == "timed_out" || status == "failed"
}

func publicationResult(evidence artifactapp.FinalizedTurnEvidence) artifactapp.PublishedTurnEvidence {
	artifactIDs := make([]string, 0, len(evidence.ArtifactRefs))
	for _, ref := range evidence.ArtifactRefs {
		artifactIDs = append(artifactIDs, ref.ArtifactID)
	}
	return artifactapp.PublishedTurnEvidence{
		TraceStreamID: evidence.Manifest.TraceStreamID, ArtifactIDs: artifactIDs,
	}
}

var _ artifactapp.ArtifactMetadataStore = (*MetadataStore)(nil)
