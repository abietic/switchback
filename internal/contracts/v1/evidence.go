package contractsv1

import (
	"fmt"
	"time"
)

const (
	TraceManifestSchemaVersion = "trace.manifest.v1"
	ArtifactRefSchemaVersion   = "artifact.v1"
)

type TraceSegment struct {
	Kind          string `json:"kind"`
	URI           string `json:"uri"`
	FirstSequence uint64 `json:"first_sequence"`
	LastSequence  uint64 `json:"last_sequence"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	RecordCount   int64  `json:"record_count"`
}

type LangfuseExportRef struct {
	Status  string `json:"status"`
	TraceID string `json:"trace_id"`
}

type TraceManifest struct {
	SchemaVersion      string            `json:"schema_version"`
	TraceStreamID      string            `json:"trace_stream_id"`
	TenantID           string            `json:"tenant_id"`
	UserID             string            `json:"user_id"`
	WorkspaceID        string            `json:"workspace_id"`
	WorkerSessionID    WorkerSessionID   `json:"worker_session_id"`
	WorkerRunID        WorkerRunID       `json:"worker_run_id"`
	AgentTurnID        AgentTurnID       `json:"agent_turn_id"`
	RuntimeInstanceID  RuntimeInstanceID `json:"runtime_instance_id"`
	RuntimeGeneration  uint32            `json:"runtime_generation"`
	Segments           []TraceSegment    `json:"segments"`
	FirstRecordedAt    time.Time         `json:"first_recorded_at"`
	LastRecordedAt     time.Time         `json:"last_recorded_at"`
	SchemaVersions     []string          `json:"schema_versions"`
	LifecyclePolicyRef string            `json:"lifecycle_policy_ref"`
	StorageTier        string            `json:"storage_tier"`
	Availability       string            `json:"availability"`
	AnalyticsCursor    string            `json:"analytics_cursor"`
	LangfuseExport     LangfuseExportRef `json:"langfuse_export"`
}

func DecodeTraceManifest(data []byte) (TraceManifest, error) {
	return decodeStrict(data, TraceManifest.Validate)
}

func (manifest TraceManifest) Validate() error {
	if manifest.SchemaVersion != TraceManifestSchemaVersion {
		return fmt.Errorf("unsupported TraceManifest schema %q", manifest.SchemaVersion)
	}
	for name, value := range map[string]string{
		"trace_stream_id": manifest.TraceStreamID, "tenant_id": manifest.TenantID,
		"user_id": manifest.UserID, "workspace_id": manifest.WorkspaceID,
		"worker_session_id": string(manifest.WorkerSessionID), "worker_run_id": string(manifest.WorkerRunID),
		"agent_turn_id": string(manifest.AgentTurnID), "runtime_instance_id": string(manifest.RuntimeInstanceID),
		"lifecycle_policy_ref": manifest.LifecyclePolicyRef, "analytics_cursor": manifest.AnalyticsCursor,
		"langfuse_export.status": manifest.LangfuseExport.Status, "langfuse_export.trace_id": manifest.LangfuseExport.TraceID,
	} {
		if err := requireString(name, value); err != nil {
			return err
		}
	}
	if manifest.RuntimeGeneration == 0 || len(manifest.Segments) == 0 {
		return fmt.Errorf("TraceManifest requires generation and segments")
	}
	for index, segment := range manifest.Segments {
		if err := requireString(fmt.Sprintf("segments[%d].kind", index), segment.Kind); err != nil {
			return err
		}
		if err := requireString(fmt.Sprintf("segments[%d].uri", index), segment.URI); err != nil {
			return err
		}
		if segment.FirstSequence == 0 || segment.LastSequence < segment.FirstSequence || segment.SizeBytes < 0 || segment.RecordCount <= 0 {
			return fmt.Errorf("segments[%d] has invalid range or cardinality", index)
		}
		if err := requireSHA256(fmt.Sprintf("segments[%d].sha256", index), segment.SHA256, false); err != nil {
			return err
		}
	}
	if manifest.FirstRecordedAt.IsZero() || manifest.LastRecordedAt.Before(manifest.FirstRecordedAt) {
		return fmt.Errorf("TraceManifest time range is invalid")
	}
	if err := requireStrings("schema_versions", manifest.SchemaVersions, true); err != nil {
		return err
	}
	if manifest.StorageTier != "hot" && manifest.StorageTier != "warm" && manifest.StorageTier != "cold" {
		return fmt.Errorf("unsupported storage tier %q", manifest.StorageTier)
	}
	if manifest.Availability != "online" && manifest.Availability != "restoring" && manifest.Availability != "quarantined" && manifest.Availability != "deletion_pending" && manifest.Availability != "deleted" && manifest.Availability != "error" {
		return fmt.Errorf("unsupported Trace availability %q", manifest.Availability)
	}
	return nil
}

type ArtifactPermission struct {
	Visibility     string `json:"visibility"`
	PolicyRevision string `json:"policy_revision"`
}

type CheckpointRef struct {
	CheckpointID string `json:"checkpoint_id"`
	Digest       string `json:"digest"`
}

type ArtifactRef struct {
	SchemaVersion       string             `json:"schema_version"`
	ArtifactID          string             `json:"artifact_id"`
	TenantID            string             `json:"tenant_id"`
	UserID              string             `json:"user_id"`
	WorkspaceID         string             `json:"workspace_id"`
	TaskID              TaskID             `json:"task_id"`
	WorkerSessionID     WorkerSessionID    `json:"worker_session_id"`
	WorkerRunID         WorkerRunID        `json:"worker_run_id"`
	AgentTurnID         AgentTurnID        `json:"agent_turn_id"`
	RuntimeInstanceID   RuntimeInstanceID  `json:"runtime_instance_id"`
	RuntimeGeneration   uint32             `json:"runtime_generation"`
	Type                string             `json:"type"`
	URI                 string             `json:"uri"`
	SHA256              string             `json:"sha256"`
	SizeBytes           int64              `json:"size_bytes"`
	Permission          ArtifactPermission `json:"permission"`
	RetentionPolicyRef  string             `json:"retention_policy_ref"`
	StorageTier         string             `json:"storage_tier"`
	Availability        string             `json:"availability"`
	Completeness        string             `json:"completeness"`
	CompletenessReason  string             `json:"completeness_reason"`
	SourceCheckpointRef CheckpointRef      `json:"source_checkpoint_ref"`
	AvailableActions    []string           `json:"available_actions"`
	CreatedAt           time.Time          `json:"created_at"`
}

func DecodeArtifactRef(data []byte) (ArtifactRef, error) {
	return decodeStrict(data, ArtifactRef.Validate)
}

func (artifact ArtifactRef) Validate() error {
	if artifact.SchemaVersion != ArtifactRefSchemaVersion {
		return fmt.Errorf("unsupported ArtifactRef schema %q", artifact.SchemaVersion)
	}
	for name, value := range map[string]string{
		"artifact_id": artifact.ArtifactID, "tenant_id": artifact.TenantID,
		"user_id": artifact.UserID, "workspace_id": artifact.WorkspaceID,
		"worker_session_id": string(artifact.WorkerSessionID), "worker_run_id": string(artifact.WorkerRunID),
		"agent_turn_id":       string(artifact.AgentTurnID),
		"runtime_instance_id": string(artifact.RuntimeInstanceID), "uri": artifact.URI,
		"permission.visibility":               artifact.Permission.Visibility,
		"permission.policy_revision":          artifact.Permission.PolicyRevision,
		"retention_policy_ref":                artifact.RetentionPolicyRef,
		"source_checkpoint_ref.checkpoint_id": artifact.SourceCheckpointRef.CheckpointID,
	} {
		if err := requireString(name, value); err != nil {
			return err
		}
	}
	if artifact.RuntimeGeneration == 0 || artifact.SizeBytes < 0 || artifact.CreatedAt.IsZero() {
		return fmt.Errorf("Artifact runtime generation, size, and created_at are invalid")
	}
	if artifact.Type != "report" && artifact.Type != "patch" {
		return fmt.Errorf("unsupported M1 Artifact type %q", artifact.Type)
	}
	if err := requireSHA256("artifact sha256", artifact.SHA256, false); err != nil {
		return err
	}
	if err := requireSHA256("checkpoint digest", artifact.SourceCheckpointRef.Digest, true); err != nil {
		return err
	}
	if artifact.StorageTier != "hot" && artifact.StorageTier != "warm" && artifact.StorageTier != "cold" {
		return fmt.Errorf("unsupported Artifact storage tier %q", artifact.StorageTier)
	}
	if artifact.Availability != "online" && artifact.Availability != "restoring" && artifact.Availability != "quarantined" && artifact.Availability != "deletion_pending" && artifact.Availability != "deleted" && artifact.Availability != "error" {
		return fmt.Errorf("unsupported Artifact availability %q", artifact.Availability)
	}
	if artifact.Completeness != "complete" && artifact.Completeness != "partial" {
		return fmt.Errorf("unsupported Artifact completeness %q", artifact.Completeness)
	}
	if artifact.Completeness == "partial" {
		if err := requireString("completeness_reason", artifact.CompletenessReason); err != nil {
			return err
		}
	} else if artifact.CompletenessReason != "" {
		return fmt.Errorf("complete Artifact cannot have completeness_reason")
	}
	return requireStrings("available_actions", artifact.AvailableActions, true)
}
