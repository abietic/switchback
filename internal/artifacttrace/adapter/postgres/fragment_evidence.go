package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
)

type fragmentQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func fragmentProofError() error {
	return &artifactapp.ProjectionError{Code: "fragment_proof_invalid", Permanent: true}
}
func fragmentPending() error { return &artifactapp.ProjectionError{Code: "parent_basis_pending"} }

func (s *ObservationLedger) ReadFragmentEvidence(ctx context.Context, ref artifactapp.FragmentRef) (artifactapp.FragmentEvidence, error) {
	if s == nil || s.pool == nil {
		return artifactapp.FragmentEvidence{}, fragmentProofError()
	}
	return readFragmentEvidence(ctx, s.pool, ref)
}

func (s *ObservationLedger) ReadFragmentParent(ctx context.Context, child artifactapp.FragmentRef, kind string) (artifactapp.FragmentEvidence, error) {
	e, err := s.ReadFragmentEvidence(ctx, child)
	if err != nil {
		return artifactapp.FragmentEvidence{}, err
	}
	f, err := artifactdomain.DecodeObservationFragment(e.Frame)
	if err != nil {
		return artifactapp.FragmentEvidence{}, fragmentProofError()
	}
	if f.Kind == artifactdomain.FragmentRunBasis || kind != "run_basis" && (f.Kind != artifactdomain.FragmentModel || kind != "turn_basis") {
		return artifactapp.FragmentEvidence{}, fragmentProofError()
	}
	return readFragmentBasisParent(ctx, s.pool, f, artifactdomain.FragmentKind(kind))
}

func readFragmentBasisParent(ctx context.Context, q fragmentQueryer, child artifactdomain.ObservationFragment, kind artifactdomain.FragmentKind) (artifactapp.FragmentEvidence, error) {
	var turn *artifactdomain.TurnIdentity
	if kind == artifactdomain.FragmentTurnBasis {
		turn = child.Turn
	}
	id, err := artifactdomain.ObservationBasisSourceID(kind, child.Scope, turn)
	if err != nil {
		return artifactapp.FragmentEvidence{}, fragmentProofError()
	}
	parent, err := readFragmentEvidence(ctx, q, artifactapp.FragmentRef{Kind: string(kind), SourceID: id})
	var typed *artifactapp.ProjectionError
	if !errors.As(err, &typed) || typed.Code != "parent_basis_pending" || parent.Source.SourceID != "" {
		return parent, err
	}
	// Before first freeze a missing parent is normal. Once its owning settlement
	// froze, absence of the expected immutable candidate is an integrity failure.
	var frozen bool
	if kind == artifactdomain.FragmentTurnBasis {
		err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_turn_settlements WHERE worker_session_id=$1 AND worker_run_id=$2 AND agent_turn_id=$3 AND phase_state<>'{}'::jsonb)`, child.Scope.WorkerSessionID, child.Scope.WorkerRunID, child.Turn.AgentTurnID).Scan(&frozen)
	} else {
		err = q.QueryRow(ctx, `WITH initial AS (SELECT t.agent_turn_id FROM agent_turns t JOIN worker_runs r USING(worker_run_id) WHERE r.worker_run_id=$1 AND r.worker_session_id=$2 AND t.worker_session_id=$2 AND t.created_at=r.created_at AND t.mode IN ('prompt','queue'))
 SELECT EXISTS(SELECT 1 FROM worker_turn_settlements o WHERE o.worker_run_id=$1 AND o.worker_session_id=$2 AND o.phase_state<>'{}'::jsonb AND ((SELECT count(*) FROM initial)<>1 OR o.agent_turn_id IN (SELECT agent_turn_id FROM initial)))`, child.Scope.WorkerRunID, child.Scope.WorkerSessionID).Scan(&frozen)
	}
	if err != nil {
		return parent, err
	}
	if frozen {
		return parent, fragmentProofError()
	}
	return parent, fragmentPending()
}

// This exact historical read also runs inside Publish after object I/O. Only
// publication/reference rows are locked. Historical first-freeze and applied
// event facts are immutable; locking live owners here would invert their order.
func readFragmentEvidence(ctx context.Context, q fragmentQueryer, ref artifactapp.FragmentRef) (artifactapp.FragmentEvidence, error) {
	var e artifactapp.FragmentEvidence
	var scope artifactdomain.ObservationScope
	err := q.QueryRow(ctx, `SELECT kind,source_id,frame_digest,frame,receipt,provenance,status,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id
 FROM observation_candidates WHERE kind=$1 AND source_id=$2 FOR SHARE`, ref.Kind, ref.SourceID).Scan(&e.Source.Kind, &e.Source.SourceID, &e.Source.FrameDigest, &e.Frame, &e.Receipt, &e.Provenance, &e.Status, &scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, fragmentPending()
	}
	if err != nil {
		return e, err
	}
	if e.Status == "blocked" {
		return e, &artifactapp.ProjectionError{Code: "parent_basis_unavailable", Permanent: true}
	}
	f, err := artifactdomain.DecodeObservationFragment(e.Frame)
	r, re := artifactapp.DecodeObservationScanReceipt(e.Receipt, e.Frame)
	id, ie := artifactdomain.ObservationFragmentID(f)
	if err != nil || re != nil || ie != nil || id != ref.SourceID || f.Scope != scope || string(f.Kind) != ref.Kind || e.Source.FrameDigest != artifactdomain.Checksum(e.Frame) || ref.FrameDigest != "" && ref.FrameDigest != e.Source.FrameDigest {
		return e, fragmentProofError()
	}
	if f.Kind == artifactdomain.FragmentModel {
		err = loadModelFragmentCore(ctx, q, &e, f, r)
	} else {
		err = loadBasisFragmentCore(ctx, q, &e, f, r)
	}
	if err != nil {
		return e, err
	}
	if e.Status == "published" {
		var matches bool
		err = q.QueryRow(ctx, `SELECT object_key,object_size,object_checksum,
 frame_digest=$3 AND tenant_id=$4 AND user_id=$5 AND workspace_id=$6 AND worker_session_id=$7 AND worker_run_id=$8
 FROM observation_publications WHERE kind=$1 AND source_id=$2 FOR SHARE`, e.Source.Kind, e.Source.SourceID, e.Source.FrameDigest, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&e.Object.Key, &e.Object.Size, &e.Object.Checksum, &matches)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (!matches || e.Object.Size != int64(len(e.Frame)) || e.Object.Checksum != e.Source.FrameDigest || e.Object.Key != fragmentObjectKey(f, e.Source.FrameDigest)) {
			return e, fragmentProofError()
		}
		if err != nil {
			return e, err
		}
		if err = checkFragmentObjectRef(ctx, q, e, e.Object); err != nil {
			return e, err
		}
	}
	return e, nil
}

func loadBasisFragmentCore(ctx context.Context, q fragmentQueryer, e *artifactapp.FragmentEvidence, f artifactdomain.ObservationFragment, r artifactapp.ObservationScanReceipt) error {
	var marker struct {
		CaptureKind string `json:"capture_kind"`
	}
	if json.Unmarshal(e.Provenance, &marker) != nil {
		return fragmentProofError()
	}
	if marker.CaptureKind != "" {
		p, err := validateHistoricalProvenance(ctx, q, *e, f)
		if err != nil {
			return err
		}
		return loadBasisFragmentCoreBinding(ctx, q, e, f, r, basisProvenance{SchemaVersion: p.SchemaVersion, SettlementID: p.SettlementID, CheckpointDigest: p.CheckpointDigest, InputDigest: p.InputDigest, ReportDigest: p.ReportDigest}, true)
	}
	var p basisProvenance
	if decodeFragmentProvenance(e.Provenance, &p) != nil {
		return fragmentProofError()
	}
	return loadBasisFragmentCoreBinding(ctx, q, e, f, r, p, false)
}

func loadBasisFragmentCoreBinding(ctx context.Context, q fragmentQueryer, e *artifactapp.FragmentEvidence, f artifactdomain.ObservationFragment, r artifactapp.ObservationScanReceipt, p basisProvenance, historical bool) error {
	if p.SchemaVersion != "observation.provenance.v1" || p.SettlementID == "" || !historical && p.LeaseFence == 0 || r.Provenance != (artifactapp.ObservationProvenance{CheckpointDigest: p.CheckpointDigest, InputDigest: p.InputDigest, ReportDigest: p.ReportDigest}) {
		return fragmentProofError()
	}
	s := f.Scope
	i := artifactdomain.TurnIdentity{TenantID: s.TenantID, UserID: s.UserID, WorkspaceID: s.WorkspaceID, WorkerSessionID: contractsv1.WorkerSessionID(s.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(s.WorkerRunID)}
	var fence uint64
	var checkpoint []byte
	var start, created, end time.Time
	err := q.QueryRow(ctx, `SELECT o.agent_turn_id,o.agent_session_id,o.runtime_instance_id,o.runtime_generation,o.lease_fence,o.phase_state,
 t.content,t.started_at,r.created_at,o.source_occurred_at,COALESCE(r.task_id,''),runtime.network_policy_id,runtime.network_policy_revision,runtime.network_policy_digest,runtime.network_enforcement_status
 FROM worker_turn_settlements o JOIN worker_sessions s USING(worker_session_id)
 JOIN worker_runs r ON r.worker_run_id=o.worker_run_id AND r.worker_session_id=o.worker_session_id
 JOIN agent_turns t ON t.agent_turn_id=o.agent_turn_id AND t.worker_run_id=o.worker_run_id AND t.worker_session_id=o.worker_session_id AND t.agent_session_id=o.agent_session_id
 JOIN runtime_instances runtime ON runtime.runtime_instance_id=o.runtime_instance_id AND runtime.worker_session_id=o.worker_session_id AND runtime.generation=o.runtime_generation
 WHERE o.settlement_id=$1 AND o.tenant_id=$2 AND s.tenant_id=$2 AND s.user_id=$3 AND s.workspace_id=$4 AND o.worker_session_id=$5 AND o.worker_run_id=$6
`, p.SettlementID, s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID).Scan(&i.AgentTurnID, &i.AgentSessionID, &i.RuntimeInstanceID, &i.RuntimeGeneration, &fence, &checkpoint, &e.Core.Input, &start, &created, &end, &i.TaskID, &i.RuntimeNetwork.ID, &i.RuntimeNetwork.Revision, &i.RuntimeNetwork.Digest, &i.RuntimeNetwork.EnforcementStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return fragmentProofError()
	}
	if err != nil {
		return err
	}
	i.TraceStreamID = "trace-" + string(i.AgentTurnID)
	i.LangfuseTraceID = "langfuse-" + string(i.AgentTurnID)
	var frozen struct {
		Frozen *workerapp.FrozenArtifactMetadata `json:"frozen_artifacts"`
		Input  struct {
			SourceCheckpointID     string `json:"source_checkpoint_id"`
			SourceCheckpointDigest string `json:"source_checkpoint_digest"`
		} `json:"evidence_input"`
	}
	if json.Unmarshal(checkpoint, &frozen) != nil || frozen.Frozen == nil || p.LeaseFence > fence || p.InputDigest != artifactdomain.Checksum(e.Core.Input) || f.InputJSON != string(e.Core.Input) || r.Scope.WorkerSessionID != s.WorkerSessionID || r.Scope.RuntimeInstanceID != string(i.RuntimeInstanceID) || r.Scope.RuntimeGeneration != uint64(i.RuntimeGeneration) {
		return fragmentProofError()
	}
	if f.Kind == artifactdomain.FragmentRunBasis {
		var initial []string
		if err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(agent_turn_id),ARRAY[]::text[]) FROM agent_turns WHERE worker_run_id=$1 AND worker_session_id=$2 AND created_at=$3 AND mode IN ('prompt','queue')`, s.WorkerRunID, s.WorkerSessionID, created).Scan(&initial); err != nil {
			return err
		}
		if len(initial) != 1 || initial[0] != f.InitialTurnID || f.InitialTurnID != string(i.AgentTurnID) || !f.StartedAt.Equal(created) {
			return fragmentProofError()
		}
	} else {
		var report string
		if f.Turn == nil || *f.Turn != i || !f.StartedAt.Equal(start) || f.EndedAt == nil || !f.EndedAt.Equal(end) || json.Unmarshal([]byte(f.OutputJSON), &report) != nil || artifactdomain.Checksum([]byte(report)) != p.ReportDigest {
			return fragmentProofError()
		}
	}
	var rawManifest, rawReport []byte
	var matched bool
	err = q.QueryRow(ctx, `SELECT manifest,manifest_object_key,manifest_checksum,manifest_size_bytes,
 tenant_id=$2 AND user_id=$3 AND workspace_id=$4 AND worker_session_id=$5 AND worker_run_id=$6 AND agent_turn_id=$7 AND runtime_instance_id=$8 AND runtime_generation=$9
 FROM trace_manifests WHERE trace_stream_id=$1 FOR SHARE`, i.TraceStreamID, s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID, i.AgentTurnID, i.RuntimeInstanceID, i.RuntimeGeneration).Scan(&rawManifest, &e.Core.ManifestObject.Key, &e.Core.ManifestObject.Checksum, &e.Core.ManifestObject.Size, &matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return fragmentPending()
	}
	if err != nil {
		return err
	}
	if !matched {
		return fragmentProofError()
	}
	m, err := contractsv1.DecodeTraceManifest(rawManifest)
	if err != nil || m.TenantID != s.TenantID || m.UserID != s.UserID || m.WorkspaceID != s.WorkspaceID || m.WorkerSessionID != i.WorkerSessionID || m.WorkerRunID != i.WorkerRunID || m.AgentTurnID != i.AgentTurnID || m.RuntimeInstanceID != i.RuntimeInstanceID || m.RuntimeGeneration != i.RuntimeGeneration || m.TraceStreamID != i.TraceStreamID || m.LangfuseExport.TraceID != i.LangfuseTraceID {
		return fragmentProofError()
	}
	e.Core.Manifest = m
	var object workerapp.StoredObject
	err = q.QueryRow(ctx, `SELECT artifact_ref,object_key,object_checksum,size_bytes,
 trace_stream_id=$2 AND tenant_id=$3 AND user_id=$4 AND workspace_id=$5 AND worker_session_id=$6 AND worker_run_id=$7 AND agent_turn_id=$8 AND runtime_instance_id=$9 AND runtime_generation=$10 AND artifact_type='report' AND COALESCE(task_id,'')=$11
 FROM artifact_refs WHERE artifact_id=$1 FOR SHARE`, frozen.Frozen.ReportArtifactID, i.TraceStreamID, s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID, i.AgentTurnID, i.RuntimeInstanceID, i.RuntimeGeneration, i.TaskID).Scan(&rawReport, &object.Key, &object.Checksum, &object.Size, &matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return fragmentProofError()
	}
	if err != nil {
		return err
	}
	report, decodeErr := contractsv1.DecodeArtifactRef(rawReport)
	if decodeErr != nil || !matched || report.ArtifactID != frozen.Frozen.ReportArtifactID || report.TenantID != s.TenantID || report.UserID != s.UserID || report.WorkspaceID != s.WorkspaceID || report.WorkerSessionID != i.WorkerSessionID || report.WorkerRunID != i.WorkerRunID || report.AgentTurnID != i.AgentTurnID || report.RuntimeInstanceID != i.RuntimeInstanceID || report.RuntimeGeneration != i.RuntimeGeneration || report.TaskID != i.TaskID || report.Type != "report" || report.URI != "object://"+object.Key || report.SizeBytes != object.Size || "sha256:"+report.SHA256 != object.Checksum || object.Checksum != p.ReportDigest || !report.CreatedAt.Equal(frozen.Frozen.CreatedAt) || report.SourceCheckpointRef.CheckpointID != frozen.Input.SourceCheckpointID || report.SourceCheckpointRef.Digest != frozen.Input.SourceCheckpointDigest {
		return fragmentProofError()
	}
	e.Core.Report = report
	e.Core.ReportDigest = p.ReportDigest
	return nil
}

func loadModelFragmentCore(ctx context.Context, q fragmentQueryer, e *artifactapp.FragmentEvidence, f artifactdomain.ObservationFragment, r artifactapp.ObservationScanReceipt) error {
	var p struct {
		SchemaVersion       string `json:"schema_version"`
		EnvelopeDigest      string `json:"envelope_digest"`
		StartEnvelopeDigest string `json:"start_envelope_digest"`
	}
	var marker struct {
		CaptureKind string `json:"capture_kind"`
	}
	if json.Unmarshal(e.Provenance, &marker) != nil {
		return fragmentProofError()
	}
	if marker.CaptureKind != "" {
		h, err := validateHistoricalProvenance(ctx, q, *e, f)
		if err != nil {
			return err
		}
		p.SchemaVersion = h.SchemaVersion
		p.EnvelopeDigest = h.EnvelopeDigest
		p.StartEnvelopeDigest = h.StartEnvelopeDigest
	} else if decodeFragmentProvenance(e.Provenance, &p) != nil {
		return fragmentProofError()
	}
	if p.SchemaVersion != "observation.provenance.v1" || r.Provenance != (artifactapp.ObservationProvenance{EnvelopeDigest: p.EnvelopeDigest}) {
		return fragmentProofError()
	}
	read := func(direction, digest string) (workerapp.RuntimeControlMessage, error) {
		var m workerapp.RuntimeControlMessage
		err := q.QueryRow(ctx, `SELECT direction,sequence,message_id,idempotency_key,fingerprint,envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,'') FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction=$2 AND fingerprint=$3`, f.Turn.RuntimeInstanceID, direction, digest).Scan(&m.Direction, &m.Sequence, &m.MessageID, &m.IdempotencyKey, &m.Fingerprint, &m.Envelope, &m.Outcome, &m.EffectStatus, &m.ApplyIdempotencyKey, &m.ApplyOutcomeDigest)
		if errors.Is(err, pgx.ErrNoRows) {
			return m, fragmentProofError()
		}
		return m, err
	}
	event, err := read("event", p.EnvelopeDigest)
	if err != nil {
		return err
	}
	start, err := read("command", p.StartEnvelopeDigest)
	if err != nil {
		return err
	}
	if workerapp.ValidateModelObservationBinding(event, start) != nil || event.EffectStatus != "applied" || event.ApplyIdempotencyKey != event.IdempotencyKey || event.ApplyOutcomeDigest != artifactdomain.Checksum([]byte(`{"status":"model_observation_recorded"}`)) || !artifactapp.ProjectionJSONEqual(event.Outcome, []byte(`{"status":"model_observation_recorded"}`)) {
		return fragmentProofError()
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(event.Envelope)
	if err != nil {
		return fragmentProofError()
	}
	frame, err := artifactapp.ModelObservationFragment(f.Scope, envelope)
	if err != nil {
		return fragmentProofError()
	}
	raw, err := artifactdomain.EncodeObservationFragment(frame)
	if err != nil || !bytes.Equal(raw, e.Frame) {
		return fragmentProofError()
	}
	for _, kind := range []artifactdomain.FragmentKind{artifactdomain.FragmentRunBasis, artifactdomain.FragmentTurnBasis} {
		parent, err := readFragmentBasisParent(ctx, q, f, kind)
		if err != nil {
			return err
		}
		if parent.Status != "published" {
			return fragmentPending()
		}
		e.Parents = append(e.Parents, parent.Source)
		if kind == artifactdomain.FragmentTurnBasis {
			e.Core = parent.Core
		}
	}
	return nil
}

func decodeFragmentProvenance(raw []byte, destination any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(destination)
}

func checkFragmentObjectRef(ctx context.Context, q fragmentQueryer, e artifactapp.FragmentEvidence, o workerapp.StoredObject) error {
	var same bool
	err := q.QueryRow(ctx, `SELECT trace_stream_id=$2 AND tenant_id=$3 AND user_id=$4 AND workspace_id=$5 AND object_kind=$6 AND object_checksum=$7 AND size_bytes=$8 FROM evidence_object_refs WHERE object_key=$1 FOR SHARE`, o.Key, e.Core.Manifest.TraceStreamID, e.Core.Manifest.TenantID, e.Core.Manifest.UserID, e.Core.Manifest.WorkspaceID, artifactapp.ObservationFragmentObjectKind, o.Checksum, o.Size).Scan(&same)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !same {
		return fragmentProofError()
	}
	return err
}

func fragmentObjectKey(f artifactdomain.ObservationFragment, digest string) string {
	turn := f.InitialTurnID
	if f.Turn != nil {
		turn = string(f.Turn.AgentTurnID)
	}
	return strings.Join([]string{"objects", "tenants", f.Scope.TenantID, "users", f.Scope.UserID, "workspaces", f.Scope.WorkspaceID, "sessions", f.Scope.WorkerSessionID, "runs", f.Scope.WorkerRunID, "turns", turn, artifactapp.ObservationFragmentObjectKind, "sha256", strings.TrimPrefix(digest, "sha256:")}, "/")
}
