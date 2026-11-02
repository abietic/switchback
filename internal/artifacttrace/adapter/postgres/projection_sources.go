package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
)

func (store *ProjectionJobStore) LoadProjectionSourceMetadata(ctx context.Context, source artifactapp.ProjectionSourceRef) (artifactapp.ProjectionSourceMetadata, error) {
	if store == nil || store.pool == nil || source.TraceStreamID == "" || source.ManifestChecksum == "" {
		return artifactapp.ProjectionSourceMetadata{}, &artifactapp.ProjectionError{Code: "source_identity_invalid", Permanent: true}
	}
	var result artifactapp.ProjectionSourceMetadata
	var anchorCount int
	err := store.pool.QueryRow(ctx, `SELECT
 current.tenant_id,current.user_id,current.workspace_id,current.worker_session_id,current.worker_run_id,
 run.started_at,initial.content,anchor.anchor_count,root.manifest,current.manifest,
 current.manifest_object_key,current.manifest_size_bytes,current.manifest_checksum,report.artifact_ref
FROM trace_manifests current
JOIN worker_runs run ON run.worker_run_id=current.worker_run_id AND run.worker_session_id=current.worker_session_id
JOIN LATERAL (SELECT count(*)::int anchor_count FROM agent_turns candidate
 WHERE candidate.worker_run_id=run.worker_run_id AND candidate.worker_session_id=run.worker_session_id
 AND candidate.created_at=run.created_at AND candidate.mode IN ('prompt','queue')) anchor ON true
JOIN agent_turns initial ON initial.worker_run_id=run.worker_run_id AND initial.worker_session_id=run.worker_session_id
 AND initial.created_at=run.created_at AND initial.mode IN ('prompt','queue')
JOIN trace_manifests root ON root.agent_turn_id=initial.agent_turn_id
 AND root.tenant_id=current.tenant_id AND root.user_id=current.user_id AND root.workspace_id=current.workspace_id
JOIN artifact_refs report ON report.trace_stream_id=current.trace_stream_id AND report.agent_turn_id=current.agent_turn_id
 AND report.artifact_type='report' AND report.tenant_id=current.tenant_id AND report.user_id=current.user_id
 AND report.workspace_id=current.workspace_id AND report.worker_session_id=current.worker_session_id
 AND report.worker_run_id=current.worker_run_id AND report.runtime_instance_id=current.runtime_instance_id
 AND report.runtime_generation=current.runtime_generation
WHERE current.trace_stream_id=$1 AND current.manifest_checksum=$2`, source.TraceStreamID, source.ManifestChecksum).Scan(
		&result.Root.TenantID, &result.Root.UserID, &result.Root.WorkspaceID, &result.Root.WorkerSessionID, &result.Root.WorkerRunID,
		&result.Root.StartedAt, &result.Root.Input, &anchorCount, &result.RootManifest, &result.Manifest,
		&result.ManifestObject.Key, &result.ManifestObject.Size, &result.ManifestObject.Checksum, &result.Report)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.ProjectionSourceMetadata{}, &artifactapp.ProjectionError{Code: "source_metadata_not_found", Permanent: true}
	}
	if err != nil {
		return artifactapp.ProjectionSourceMetadata{}, fmt.Errorf("load projection source metadata: %w", err)
	}
	if anchorCount != 1 {
		return artifactapp.ProjectionSourceMetadata{}, &artifactapp.ProjectionError{Code: "root_turn_ambiguous", Permanent: true}
	}
	if result.Root.StartedAt.IsZero() {
		return artifactapp.ProjectionSourceMetadata{}, &artifactapp.ProjectionError{Code: "root_started_at_missing", Permanent: true}
	}
	return result, nil
}

var _ artifactapp.ProjectionSourceMetadataStore = (*ProjectionJobStore)(nil)
