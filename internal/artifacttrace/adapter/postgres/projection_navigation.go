package postgres

import (
	"context"
	"encoding/json"
	"errors"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProjectionNavigationStore struct {
	pool          *pgxpool.Pool
	tenant, actor string
	compiler      artifactapp.ProjectionNavigationCompiler
}

func NewProjectionNavigationStore(pool *pgxpool.Pool, tenant, actor string, compiler artifactapp.ProjectionNavigationCompiler) *ProjectionNavigationStore {
	return &ProjectionNavigationStore{pool: pool, tenant: tenant, actor: actor, compiler: compiler}
}

func (s *ProjectionNavigationStore) ReadProjectionNavigation(ctx context.Context, query artifactapp.ProjectionNavigationQuery) (artifactapp.ProjectionNavigation, error) {
	out := artifactapp.ProjectionNavigation{Status: artifactapp.ProjectionNavigationIdentityUnavailable}
	if s == nil || s.pool == nil || s.compiler == nil || query.ActorID == "" || query.ActorID != s.actor || query.Scope.TenantID != s.tenant || query.Scope.UserID != s.actor {
		return out, artifactapp.ErrHistoricalSelection
	}
	version, scope := artifactapp.FragmentProjectionVersion, query.Scope
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	live, selected, err := fragmentCoverageSelection(ctx, tx, scope, query.ActorID, version)
	if err != nil {
		return out, err
	}
	accepted, err := coverageAcceptedBasisSources(ctx, tx, scope)
	if err != nil {
		return out, err
	}
	rootID, err := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentRunBasis, scope, nil)
	if err != nil {
		return out, artifactapp.ErrHistoricalSelection
	}
	refs := []artifactapp.FragmentRef{{Kind: "run_basis", SourceID: rootID}}
	if query.AgentTurnID != "" {
		i := artifactdomain.TurnIdentity{TenantID: scope.TenantID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID, WorkerSessionID: contractsv1.WorkerSessionID(scope.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(scope.WorkerRunID)}
		err = tx.QueryRow(ctx, `SELECT agent_turn_id,agent_session_id,runtime_instance_id,runtime_generation FROM worker_turn_settlements WHERE agent_turn_id=$1 AND worker_run_id=$2 AND worker_session_id=$3 AND tenant_id=$4 AND phase_state<>'{}'::jsonb`, query.AgentTurnID, scope.WorkerRunID, scope.WorkerSessionID, scope.TenantID).Scan(&i.AgentTurnID, &i.AgentSessionID, &i.RuntimeInstanceID, &i.RuntimeGeneration)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		id, err := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, scope, &i)
		if err != nil {
			return out, fragmentProofError()
		}
		refs = append(refs, artifactapp.FragmentRef{Kind: "turn_basis", SourceID: id})
	}
	loaded := artifactapp.LoadedFragmentProjection{Version: version}
	for _, ref := range refs {
		key := ref.Kind + "/" + ref.SourceID
		if _, ok := accepted[key]; !ok {
			return out, nil
		}
		chosen, explicit := selected[key]
		if !live && !explicit {
			return out, nil
		}
		frame, receipt, current, status, err := readNavigationBasis(ctx, tx, scope, ref)
		if err != nil {
			return out, err
		}
		if !live && chosen.FrameDigest != current.FrameDigest {
			return out, nil
		}
		if status != artifactapp.ProjectionNavigationVerified {
			out.Status = status
			return out, nil
		}
		loaded.Frames = append(loaded.Frames, frame)
		loaded.Receipts = append(loaded.Receipts, receipt)
		loaded.Source = current
	}
	identity, err := s.compiler.CompileProjectionNavigation(ctx, loaded)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		var proof *artifactapp.ProjectionError
		if errors.As(err, &proof) && proof.Code == "fragment_proof_invalid" {
			out.Status = artifactapp.ProjectionNavigationBlocked
			return out, nil
		}
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Status, out.Identity = artifactapp.ProjectionNavigationVerified, &identity
	return out, nil
}

// Dedicated nonlocking metadata reader. Publication's FOR SHARE reader and its
// ObjectStore verifier must not run inside this REPEATABLE READ READ ONLY path.
func readNavigationBasis(ctx context.Context, tx pgx.Tx, scope artifactdomain.ObservationScope, ref artifactapp.FragmentRef) ([]byte, []byte, artifactapp.FragmentRef, artifactapp.ProjectionNavigationStatus, error) {
	var frame, receipt, provenance []byte
	var candidateScope artifactdomain.ObservationScope
	var state, code, objectKey, objectChecksum, jobState, jobDigest, jobCode string
	var objectSize int64
	var publication, publicationMatches, conflict bool
	err := tx.QueryRow(ctx, `SELECT c.frame_digest,c.frame,c.receipt,c.provenance,c.status,COALESCE(c.error_code,''),c.tenant_id,c.user_id,c.workspace_id,c.worker_session_id,c.worker_run_id,
 p.source_id IS NOT NULL,COALESCE(p.frame_digest=c.frame_digest AND p.tenant_id=c.tenant_id AND p.user_id=c.user_id AND p.workspace_id=c.workspace_id AND p.worker_session_id=c.worker_session_id AND p.worker_run_id=c.worker_run_id,false),COALESCE(p.object_key,''),COALESCE(p.object_size,0),COALESCE(p.object_checksum,''),
 COALESCE(j.status,''),COALESCE(j.frame_digest,''),COALESCE(j.error_code,''),EXISTS(SELECT 1 FROM observation_fragment_conflicts x WHERE x.kind=c.kind AND x.source_id=c.source_id)
 FROM observation_candidates c LEFT JOIN observation_publications p ON p.kind=c.kind AND p.source_id=c.source_id
 LEFT JOIN observation_fragment_jobs j ON j.kind=c.kind AND j.source_id=c.source_id AND j.version=$3
 WHERE c.kind=$1 AND c.source_id=$2`, ref.Kind, ref.SourceID, artifactapp.FragmentProjectionVersion).Scan(&ref.FrameDigest, &frame, &receipt, &provenance, &state, &code, &candidateScope.TenantID, &candidateScope.UserID, &candidateScope.WorkspaceID, &candidateScope.WorkerSessionID, &candidateScope.WorkerRunID, &publication, &publicationMatches, &objectKey, &objectSize, &objectChecksum, &jobState, &jobDigest, &jobCode, &conflict)
	status := artifactapp.ProjectionNavigationBlocked
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ref, status, nil
	}
	if err != nil {
		return nil, nil, ref, status, err
	}
	if candidateScope != scope || conflict || state == "blocked" || code != "" && state != "retryable" {
		return nil, nil, ref, status, nil
	}
	f, fe := artifactdomain.DecodeObservationFragment(frame)
	r, re := artifactapp.DecodeObservationScanReceipt(receipt, frame)
	id, ie := artifactdomain.ObservationFragmentID(f)
	if fe != nil || re != nil || ie != nil || f.Scope != scope || string(f.Kind) != ref.Kind || id != ref.SourceID || ref.FrameDigest != artifactdomain.Checksum(frame) {
		return nil, nil, ref, status, nil
	}
	if err = validateNavigationBasisCore(ctx, tx, ref, f, r, provenance); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ref, status, ctx.Err()
		}
		var proof *artifactapp.ProjectionError
		if errors.As(err, &proof) && proof.Code == "parent_basis_pending" && state != "published" && !publication && jobState == "" {
			return nil, nil, ref, artifactapp.ProjectionNavigationPending, nil
		}
		if errors.As(err, &proof) || errors.Is(err, artifactapp.ErrHistoricalSelection) {
			return nil, nil, ref, status, nil
		}
		return nil, nil, ref, status, err
	}
	if state != "published" {
		if (state == "pending" || state == "running" || state == "retryable") && !publication {
			status = artifactapp.ProjectionNavigationPending
		}
		return nil, nil, ref, status, nil
	}
	if !publication || !publicationMatches || objectSize != int64(len(frame)) || objectChecksum != ref.FrameDigest || objectKey != fragmentObjectKey(f, ref.FrameDigest) {
		return nil, nil, ref, status, nil
	}
	turnID := f.InitialTurnID
	if f.Turn != nil {
		turnID = string(f.Turn.AgentTurnID)
	}
	var objectMatches bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evidence_object_refs WHERE object_key=$1 AND trace_stream_id=$2 AND tenant_id=$3 AND user_id=$4 AND workspace_id=$5 AND object_kind=$6 AND object_checksum=$7 AND size_bytes=$8)`, objectKey, "trace-"+turnID, scope.TenantID, scope.UserID, scope.WorkspaceID, artifactapp.ObservationFragmentObjectKind, objectChecksum, objectSize).Scan(&objectMatches)
	if err != nil {
		return nil, nil, ref, status, err
	}
	if !objectMatches || jobDigest != "" && jobDigest != ref.FrameDigest {
		return nil, nil, ref, status, nil
	}
	if jobState == "blocked" || jobState == "passed" && jobCode != "" {
		return nil, nil, ref, status, nil
	}
	if jobState != "passed" {
		return nil, nil, ref, artifactapp.ProjectionNavigationPending, nil
	}
	return frame, receipt, ref, artifactapp.ProjectionNavigationVerified, nil
}

func validateNavigationBasisCore(ctx context.Context, tx pgx.Tx, ref artifactapp.FragmentRef, f artifactdomain.ObservationFragment, r artifactapp.ObservationScanReceipt, provenance []byte) error {
	var marker struct {
		CaptureKind string `json:"capture_kind"`
	}
	if json.Unmarshal(provenance, &marker) != nil {
		return fragmentProofError()
	}
	var p basisProvenance
	if marker.CaptureKind != "" {
		h, err := validateHistoricalProvenance(ctx, tx, artifactapp.FragmentEvidence{Source: ref, Frame: nil, Provenance: provenance}, f)
		if err != nil {
			return err
		}
		p = basisProvenance{SchemaVersion: h.SchemaVersion, SettlementID: h.SettlementID, CheckpointDigest: h.CheckpointDigest, InputDigest: h.InputDigest, ReportDigest: h.ReportDigest}
	} else if decodeFragmentProvenance(provenance, &p) != nil {
		return fragmentProofError()
	}
	turnID := f.InitialTurnID
	if f.Turn != nil {
		turnID = string(f.Turn.AgentTurnID)
	}
	var checksum string
	if err := tx.QueryRow(ctx, `SELECT manifest_checksum FROM trace_manifests WHERE trace_stream_id=$1`, "trace-"+turnID).Scan(&checksum); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fragmentPending()
		}
		return err
	}
	h, err := readHistoryTurn(ctx, tx, f.Scope, artifactapp.HistoricalTurnRef{AgentTurnID: turnID, ManifestChecksum: checksum}, false)
	if err != nil {
		return err
	}
	i := h.Identity
	metadata := basisCoreMetadata{Turn: h}
	if err = tx.QueryRow(ctx, `SELECT lease_fence FROM worker_turn_settlements WHERE settlement_id=$1`, h.SettlementID).Scan(&metadata.LeaseFence); err != nil {
		return err
	}
	var manifestErr, reportErr error
	metadata.Manifest, manifestErr = contractsv1.DecodeTraceManifest(h.Manifest)
	metadata.Report, reportErr = contractsv1.DecodeArtifactRef(h.ReportRef)
	if manifestErr != nil || reportErr != nil || json.Unmarshal(h.Checkpoint, &metadata.Frozen) != nil {
		return fragmentProofError()
	}
	s := f.Scope
	// JSON identities and relational owner columns must agree independently;
	// readHistoryTurn deliberately leaves these checks to its caller.
	var metadataMatches bool
	err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM trace_manifests WHERE trace_stream_id=$1 AND tenant_id=$2 AND user_id=$3 AND workspace_id=$4 AND worker_session_id=$5 AND worker_run_id=$6 AND agent_turn_id=$7 AND runtime_instance_id=$8 AND runtime_generation=$9) AND
 EXISTS(SELECT 1 FROM artifact_refs WHERE artifact_id=$10 AND trace_stream_id=$1 AND tenant_id=$2 AND user_id=$3 AND workspace_id=$4 AND worker_session_id=$5 AND worker_run_id=$6 AND agent_turn_id=$7 AND runtime_instance_id=$8 AND runtime_generation=$9 AND artifact_type='report' AND COALESCE(task_id,'')=$11)`, i.TraceStreamID, s.TenantID, s.UserID, s.WorkspaceID, i.WorkerSessionID, i.WorkerRunID, i.AgentTurnID, i.RuntimeInstanceID, i.RuntimeGeneration, metadata.Report.ArtifactID, i.TaskID).Scan(&metadataMatches)
	if err != nil {
		return err
	}
	metadata.ManifestMatches, metadata.ReportMatches = metadataMatches, metadataMatches
	_, err = bindBasisFragmentCore(f, r, p, marker.CaptureKind != "", metadata)
	return err
}

var _ artifactapp.ProjectionNavigationReader = (*ProjectionNavigationStore)(nil)
