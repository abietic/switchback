package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
)

type ObservationCaptureStore struct{}

type basisProvenance struct {
	SchemaVersion    string `json:"schema_version"`
	SettlementID     string `json:"settlement_id"`
	CheckpointDigest string `json:"checkpoint_digest"`
	LeaseFence       uint64 `json:"lease_fence"`
	InputDigest      string `json:"input_digest"`
	ReportDigest     string `json:"report_digest,omitempty"`
}

func (*ObservationCaptureStore) CommitTurnBasis(ctx context.Context, tx platformpostgres.Tx, current workerapp.TurnSettlement, record workerapp.FreezeTurnSettlementCheckpointRecord) error {
	if tx == nil || current.SettlementID != record.Command.SettlementID {
		return errors.New("observation_locked_evidence_invalid")
	}
	var applicable bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_fragment_activations WHERE version='langfuse.projection.v3') AND NOT EXISTS(SELECT 1 FROM observation_fragment_run_exclusions WHERE version='langfuse.projection.v3' AND worker_run_id=$1)`, current.WorkerRunID).Scan(&applicable); err != nil {
		return err
	}
	if !applicable {
		return nil
	}
	var scope artifactdomain.ObservationScope
	var input []byte
	var startedAt, runCreatedAt time.Time
	var taskID string
	var network artifactdomain.RuntimeNetworkEvidence
	if err := tx.QueryRow(ctx, `SELECT s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id,r.worker_run_id, t.content,t.started_at,r.created_at,COALESCE(r.task_id,''),runtime.network_policy_id,runtime.network_policy_revision,runtime.network_policy_digest,runtime.network_enforcement_status
 FROM worker_runs r JOIN worker_sessions s USING(worker_session_id)
 JOIN agent_turns t ON t.worker_run_id=r.worker_run_id AND t.worker_session_id=s.worker_session_id
 JOIN runtime_instances runtime ON runtime.worker_session_id=s.worker_session_id
 WHERE r.worker_run_id=$1 AND s.worker_session_id=$2 AND s.tenant_id=$3 AND t.agent_turn_id=$4 AND t.agent_session_id=$5 AND runtime.runtime_instance_id=$6 AND runtime.generation=$7`, current.WorkerRunID, current.WorkerSessionID, current.TenantID, current.AgentTurnID, current.AgentSessionID, current.RuntimeInstanceID, current.RuntimeGeneration).Scan(&scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID, &input, &startedAt, &runCreatedAt, &taskID, &network.ID, &network.Revision, &network.Digest, &network.EnforcementStatus); err != nil {
		return err
	}
	identity := artifactdomain.TurnIdentity{TenantID: scope.TenantID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID, TaskID: contractsv1.TaskID(taskID), WorkerSessionID: contractsv1.WorkerSessionID(scope.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(scope.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(current.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(current.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(current.RuntimeInstanceID), RuntimeGeneration: uint32(current.RuntimeGeneration), TraceStreamID: "trace-" + current.AgentTurnID, LangfuseTraceID: "langfuse-" + current.AgentTurnID, RuntimeNetwork: network}
	var initialIDs []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(agent_turn_id),ARRAY[]::text[]) FROM agent_turns WHERE worker_run_id=$1 AND worker_session_id=$2 AND created_at=$3 AND mode IN ('prompt','queue')`, current.WorkerRunID, current.WorkerSessionID, runCreatedAt).Scan(&initialIDs); err != nil {
		return err
	}
	kinds := []artifactdomain.FragmentKind{artifactdomain.FragmentTurnBasis}
	if len(initialIDs) != 1 || initialIDs[0] == current.AgentTurnID {
		kinds = append(kinds, artifactdomain.FragmentRunBasis)
	}
	first := len(current.PhaseState) == 0 || string(current.PhaseState) == "{}"
	var prepared []artifactapp.PreparedObservation
	gapCode := "basis_scan_unavailable"
	if capture := record.Command.ObservationCapture; first && capture != nil {
		gapCode = "capture_invalid"
		if len(capture.Sealed) > 0 && capture.SafeCode == "" {
			if bundle, err := artifactapp.DecodeSealedObservationBundle(capture.Sealed); err == nil && len(bundle.Observations) == len(kinds) {
				prepared = bundle.Observations
			}
		} else if len(capture.Sealed) == 0 {
			switch capture.SafeCode {
			case workerapp.ObservationCaptureUnavailable:
				gapCode = "basis_scan_unavailable"
			case workerapp.ObservationCaptureTimeout:
				gapCode = "capture_cancelled"
			}
		}
	}
	var frozenReportDigest string
	for index, kind := range kinds {
		var turnIdentity *artifactdomain.TurnIdentity
		if kind == artifactdomain.FragmentTurnBasis {
			turnIdentity = &identity
		}
		id, err := artifactdomain.ObservationBasisSourceID(kind, scope, turnIdentity)
		if err != nil {
			return err
		}
		if kind == artifactdomain.FragmentRunBasis && len(initialIDs) != 1 {
			if err := commitAmbiguousRunBasisGap(ctx, tx, scope, id, current, record, input, first); err != nil {
				return err
			}
			continue
		}
		if !first {
			var raw, receipt, provenance []byte
			var digest string
			err := tx.QueryRow(ctx, `SELECT frame,receipt,frame_digest,provenance FROM observation_candidates WHERE kind=$1 AND source_id=$2 AND tenant_id=$3 AND user_id=$4 AND workspace_id=$5 AND worker_session_id=$6 AND worker_run_id=$7`, kind, id, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&raw, &receipt, &digest, &provenance)
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("observation_receipt_binding_invalid")
			}
			if err != nil {
				return err
			}
			var p basisProvenance
			// Even accepted can already contain a committed extension. The immutable
			// receipt attests to the first digest, never the latest phase-state digest.
			if json.Unmarshal(provenance, &p) != nil || p.SchemaVersion != "observation.provenance.v1" || p.SettlementID != current.SettlementID || p.LeaseFence == 0 || p.InputDigest != basisDigest(input) || p.CheckpointDigest == "" {
				return errors.New("observation_receipt_binding_invalid")
			}
			if len(raw) == 0 && (len(receipt) != 0 || digest != p.CheckpointDigest) {
				return errors.New("observation_receipt_binding_invalid")
			}
			if len(raw) > 0 {
				f, err := artifactdomain.DecodeObservationFragment(raw)
				r, receiptErr := artifactapp.DecodeObservationScanReceipt(receipt, raw)
				gotID, idErr := artifactdomain.ObservationFragmentID(f)
				if err != nil || receiptErr != nil || idErr != nil || gotID != id || digest != basisDigest(raw) || r.Provenance.CheckpointDigest != p.CheckpointDigest || r.Provenance.InputDigest != p.InputDigest || r.Provenance.ReportDigest != p.ReportDigest || f.InputJSON != string(input) || r.Scope.WorkerSessionID != current.WorkerSessionID || r.Scope.RuntimeInstanceID != current.RuntimeInstanceID || r.Scope.RuntimeGeneration != current.RuntimeGeneration {
					return errors.New("observation_receipt_binding_invalid")
				}
				if kind == artifactdomain.FragmentTurnBasis {
					var report string
					if f.Turn == nil || *f.Turn != identity || !f.StartedAt.Equal(startedAt) || f.EndedAt == nil || !f.EndedAt.Equal(current.Source.OccurredAt) || json.Unmarshal([]byte(f.OutputJSON), &report) != nil || basisDigest([]byte(report)) != p.ReportDigest {
						return errors.New("observation_receipt_binding_invalid")
					}
					frozenReportDigest = p.ReportDigest
				} else if len(initialIDs) != 1 || f.InitialTurnID != current.AgentTurnID || !f.StartedAt.Equal(runCreatedAt) || frozenReportDigest == "" || p.ReportDigest != frozenReportDigest {
					return errors.New("observation_receipt_binding_invalid")
				}
			}
			continue
		}
		p := basisProvenance{SchemaVersion: "observation.provenance.v1", SettlementID: current.SettlementID, CheckpointDigest: record.CheckpointDigest, LeaseFence: current.LeaseFence, InputDigest: basisDigest(input)}
		c := observationCandidateRecord{Source: artifactapp.FragmentRef{Kind: string(kind), SourceID: id, FrameDigest: record.CheckpointDigest}, Scope: scope, ErrorCode: gapCode}
		if len(prepared) > 0 {
			item := prepared[index]
			if item.ErrorCode != "" {
				c.ErrorCode = item.ErrorCode
			} else {
				f, frameErr := artifactdomain.DecodeObservationFragment(item.Frame)
				r, receiptErr := artifactapp.DecodeObservationScanReceipt(item.Receipt, item.Frame)
				valid := frameErr == nil && receiptErr == nil && f.Kind == kind && f.Scope == scope && f.InputJSON == string(input) && r.Scope.WorkerSessionID == current.WorkerSessionID && r.Scope.RuntimeInstanceID == current.RuntimeInstanceID && r.Scope.RuntimeGeneration == current.RuntimeGeneration && r.Provenance.CheckpointDigest == record.CheckpointDigest && r.Provenance.InputDigest == p.InputDigest && r.Provenance.EnvelopeDigest == ""
				if kind == artifactdomain.FragmentRunBasis {
					valid = valid && len(initialIDs) == 1 && f.InitialTurnID == current.AgentTurnID && f.StartedAt.Equal(runCreatedAt) && frozenReportDigest != "" && r.Provenance.ReportDigest == frozenReportDigest
				} else {
					var report string
					valid = valid && f.Turn != nil && *f.Turn == identity && f.StartedAt.Equal(startedAt) && f.EndedAt != nil && f.EndedAt.Equal(current.Source.OccurredAt) && json.Unmarshal([]byte(f.OutputJSON), &report) == nil && basisDigest([]byte(report)) == r.Provenance.ReportDigest
				}
				if valid {
					c.Frame = append([]byte(nil), item.Frame...)
					c.Receipt = append([]byte(nil), item.Receipt...)
					c.Source.FrameDigest = basisDigest(c.Frame)
					c.ErrorCode = ""
					p.ReportDigest = r.Provenance.ReportDigest
					if kind == artifactdomain.FragmentTurnBasis {
						frozenReportDigest = p.ReportDigest
					}
				}
			}
		}
		c.Provenance, err = json.Marshal(p)
		if err != nil {
			return err
		}
		conflict, err := insertObservationCandidate(ctx, tx, c)
		if err != nil {
			return err
		}
		if conflict {
			return errors.New("observation_receipt_binding_invalid")
		}
	}
	return nil
}

// An ambiguous Run has one immutable gap, even when several Turns freeze.
// The unique insert waits for a concurrent first writer without taking another
// settlement lock. The separate read then sees that writer's committed proof.
func commitAmbiguousRunBasisGap(ctx context.Context, tx platformpostgres.Tx, scope artifactdomain.ObservationScope, id string, current workerapp.TurnSettlement, record workerapp.FreezeTurnSettlementCheckpointRecord, input []byte, first bool) error {
	expected := basisProvenance{SchemaVersion: "observation.provenance.v1", SettlementID: current.SettlementID, CheckpointDigest: record.CheckpointDigest, LeaseFence: current.LeaseFence, InputDigest: basisDigest(input)}
	if first {
		provenance, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO observation_candidates(kind,source_id,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status,error_code)
 VALUES('run_basis',$1,$2,$3,$4,$5,$6,$7,$8,'blocked','source_invalid') ON CONFLICT(kind,source_id) DO NOTHING`, id, record.CheckpointDigest, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID, provenance); err != nil {
			return err
		}
	}
	var provenance []byte
	var digest string
	var gap bool
	err := tx.QueryRow(ctx, `SELECT provenance,frame_digest,frame IS NULL AND receipt IS NULL AND error_code='source_invalid'
 FROM observation_candidates WHERE kind='run_basis' AND source_id=$1 AND tenant_id=$2 AND user_id=$3 AND workspace_id=$4 AND worker_session_id=$5 AND worker_run_id=$6`, id, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&provenance, &digest, &gap)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("observation_receipt_binding_invalid")
	}
	if err != nil {
		return err
	}
	var p basisProvenance
	if json.Unmarshal(provenance, &p) != nil || !gap || p.SchemaVersion != "observation.provenance.v1" || p.SettlementID == "" || p.LeaseFence == 0 || p.CheckpointDigest == "" || p.CheckpointDigest != digest || p.ReportDigest != "" {
		return errors.New("observation_receipt_binding_invalid")
	}
	// Revalidate the original settlement and its Turn input, not the Turn that
	// happens to be freezing now. Checkpoint extensions and newer retry leases
	// cannot rewrite this historical first-freeze attestation.
	var originalInput []byte
	var originalFence uint64
	var frozen, turnProof bool
	original := artifactdomain.TurnIdentity{TenantID: scope.TenantID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID, WorkerSessionID: contractsv1.WorkerSessionID(scope.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(scope.WorkerRunID)}
	err = tx.QueryRow(ctx, `SELECT t.content,o.lease_fence,o.phase_state<>'{}'::jsonb,o.agent_turn_id,o.agent_session_id,o.runtime_instance_id,o.runtime_generation
 FROM worker_turn_settlements o JOIN worker_sessions s USING(worker_session_id)
 JOIN agent_turns t ON t.agent_turn_id=o.agent_turn_id AND t.worker_run_id=o.worker_run_id AND t.worker_session_id=o.worker_session_id AND t.agent_session_id=o.agent_session_id
 WHERE o.settlement_id=$1 AND o.tenant_id=$2 AND s.tenant_id=$2 AND s.user_id=$3 AND s.workspace_id=$4 AND o.worker_session_id=$5 AND o.worker_run_id=$6`, p.SettlementID, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&originalInput, &originalFence, &frozen, &original.AgentTurnID, &original.AgentSessionID, &original.RuntimeInstanceID, &original.RuntimeGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("observation_receipt_binding_invalid")
	}
	if err != nil {
		return err
	}
	if p.InputDigest != basisDigest(originalInput) || p.LeaseFence > originalFence || (!frozen && (!first || p != expected)) {
		return errors.New("observation_receipt_binding_invalid")
	}
	originalID, err := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, scope, &original)
	if err != nil {
		return errors.New("observation_receipt_binding_invalid")
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_candidates WHERE kind='turn_basis' AND source_id=$1
 AND tenant_id=$2 AND user_id=$3 AND workspace_id=$4 AND worker_session_id=$5 AND worker_run_id=$6 AND provenance @> $7::jsonb)`, originalID, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID, provenance).Scan(&turnProof); err != nil {
		return err
	}
	if !turnProof {
		return errors.New("observation_receipt_binding_invalid")
	}
	return nil
}

func basisDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func (*ObservationCaptureStore) CommitModelObservation(ctx context.Context, tx platformpostgres.Tx, event, start workerapp.RuntimeControlMessage, record workerapp.ApplyRuntimeControlEventRecord) error {
	if tx == nil || workerapp.ValidateModelObservationBinding(event, start) != nil || (event.EffectStatus != "received" && event.EffectStatus != "applied") || record.EffectDisposition != "" || record.Command.Sequence != event.Sequence || record.Command.MessageID != event.MessageID || record.Command.IdempotencyKey != event.IdempotencyKey || string(record.Command.Outcome) != `{"status":"model_observation_recorded"}` || record.OutcomeDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(record.Command.Outcome)) {
		return errors.New("observation_locked_evidence_invalid")
	}
	e, err := contractsv1.DecodeRuntimeControlEnvelope(event.Envelope)
	if err != nil {
		return errors.New("observation_locked_evidence_invalid")
	}
	a := record.Command.Authority
	if a.WorkerSessionID != string(e.WorkerSessionID) || a.RuntimeInstanceID != string(e.RuntimeInstanceID) || a.RuntimeGeneration != uint64(e.RuntimeGeneration) || a.ChannelEpoch != e.ChannelEpoch {
		return errors.New("observation_locked_evidence_invalid")
	}
	var scope artifactdomain.ObservationScope
	err = tx.QueryRow(ctx, `SELECT s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id,r.worker_run_id FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1 AND s.worker_session_id=$2`, string(e.WorkerRunID), string(e.WorkerSessionID)).Scan(&scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID)
	if err != nil {
		return err
	}
	f, err := artifactapp.ModelObservationFragment(scope, e)
	if err != nil {
		return errors.New("observation_locked_evidence_invalid")
	}
	id, err := artifactapp.ModelObservationSourceID(f)
	if err != nil {
		return errors.New("observation_locked_evidence_invalid")
	}
	canonical, frameErr := artifactdomain.EncodeObservationFragment(f)
	digest := event.Fingerprint
	if frameErr == nil {
		digest = fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	}
	provenance, _ := json.Marshal(struct {
		SchemaVersion       string `json:"schema_version"`
		EnvelopeDigest      string `json:"envelope_digest"`
		StartEnvelopeDigest string `json:"start_envelope_digest"`
	}{"observation.provenance.v1", event.Fingerprint, start.Fingerprint})
	if event.EffectStatus == "applied" {
		// Read and verify only. Even a new valid capture cannot repair a missing
		// receipt on replay or overwrite a previously committed no-content gap.
		var original string
		var raw, receipt []byte
		var same, conflict bool
		err = tx.QueryRow(ctx, `SELECT c.frame_digest,c.frame,c.receipt,c.provenance=$2::jsonb,
   EXISTS(SELECT 1 FROM observation_fragment_conflicts x WHERE x.kind=c.kind AND x.source_id=c.source_id AND x.original_digest=c.frame_digest AND x.conflicting_digest=$3)
   FROM observation_candidates c WHERE c.kind='model' AND c.source_id=$1 AND c.tenant_id=$4 AND c.user_id=$5 AND c.workspace_id=$6 AND c.worker_session_id=$7 AND c.worker_run_id=$8`, id, provenance, digest, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&original, &raw, &receipt, &same, &conflict)
		if err != nil {
			return err
		}
		if !same && !conflict {
			return errors.New("observation_receipt_binding_invalid")
		}
		if same && original != digest {
			return errors.New("observation_receipt_binding_invalid")
		}
		if len(raw) > 0 {
			if original != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
				return errors.New("observation_receipt_binding_invalid")
			}
			if _, err := artifactapp.DecodeObservationScanReceipt(receipt, raw); err != nil {
				return err
			}
			originalFrame, err := artifactdomain.DecodeObservationFragment(raw)
			if err != nil || originalFrame.Scope != scope {
				return errors.New("observation_receipt_binding_invalid")
			}
			originalID, err := artifactapp.ModelObservationSourceID(originalFrame)
			if err != nil || originalID != id {
				return errors.New("observation_receipt_binding_invalid")
			}
		}
		return nil
	}
	c := observationCandidateRecord{Source: artifactapp.FragmentRef{Kind: "model", SourceID: id, FrameDigest: digest}, Scope: scope, Provenance: provenance, ErrorCode: "capture_invalid"}
	capture := record.Command.ObservationCapture
	if capture == nil {
		return errors.New("observation_capture_required")
	}
	if len(capture.Sealed) > 0 && capture.SafeCode == "" && frameErr == nil {
		b, err := artifactapp.DecodeSealedObservationBundle(capture.Sealed)
		if err == nil && len(b.Observations) == 1 {
			p := b.Observations[0]
			if p.ErrorCode != "" && p.ErrorCode != "basis_scan_unavailable" {
				c.ErrorCode = p.ErrorCode
			} else if bytes.Equal(p.Frame, canonical) {
				r, err := artifactapp.DecodeObservationScanReceipt(p.Receipt, p.Frame)
				if err == nil && r.Provenance.EnvelopeDigest == event.Fingerprint && r.Provenance.CheckpointDigest == "" && r.Provenance.InputDigest == "" && r.Provenance.ReportDigest == "" {
					c.Frame = append([]byte(nil), p.Frame...)
					c.Receipt = append([]byte(nil), p.Receipt...)
					c.ErrorCode = ""
				}
			}
		}
	} else if len(capture.Sealed) == 0 {
		switch capture.SafeCode {
		case workerapp.ObservationCaptureUnavailable:
			c.ErrorCode = "scanner_unavailable"
		case workerapp.ObservationCaptureTimeout:
			c.ErrorCode = "capture_cancelled"
		}
	}
	// Source conflicts are durably recorded safe outcomes, not retry errors.
	_, err = insertObservationCandidate(ctx, tx, c)
	return err
}
