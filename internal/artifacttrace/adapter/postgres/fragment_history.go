package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FragmentHistoryStore is an internal, fixed-actor M1 service. It is deliberately
// not registered as an HTTP endpoint or a worker capability.
type FragmentHistoryStore struct {
	pool          *pgxpool.Pool
	tenant, actor string
	objects       workerapp.ObjectStore
	preparer      artifactapp.HistoricalObservationPreparer
}

func NewFragmentHistoryStore(pool *pgxpool.Pool, tenant, actor string, objects workerapp.ObjectStore, preparer artifactapp.HistoricalObservationPreparer) *FragmentHistoryStore {
	return &FragmentHistoryStore{pool: pool, tenant: tenant, actor: actor, objects: objects, preparer: preparer}
}

// Historical provenance is explicitly distinct from first-freeze provenance:
// no lease fence, no claim that scanning happened at the original freeze.
type historicalProvenance struct {
	SchemaVersion       string `json:"schema_version"`
	CaptureKind         string `json:"capture_kind"`
	RequestDigest       string `json:"request_digest"`
	SettlementID        string `json:"settlement_id"`
	CheckpointDigest    string `json:"checkpoint_digest"`
	InputDigest         string `json:"input_digest"`
	ReportDigest        string `json:"report_digest"`
	ManifestChecksum    string `json:"manifest_checksum"`
	EnvelopeDigest      string `json:"envelope_digest,omitempty"`
	StartEnvelopeDigest string `json:"start_envelope_digest,omitempty"`
}
type historyTurn struct {
	Identity                               artifactdomain.TurnIdentity
	Input, Checkpoint, Manifest, ReportRef []byte
	SettlementID, CheckpointDigest         string
	Start, Created, End                    time.Time
	ManifestObject, ReportObject           workerapp.StoredObject
	InitialIDs                             []string
}
type historyItem struct {
	Candidate observationCandidateRecord
	Turn      historyTurn
	Capture   artifactapp.HistoricalObservationCapture
	Existing  bool
}
type historySelectionMember struct {
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	FrameDigest string `json:"frame_digest"`
}

func (s *FragmentHistoryStore) Request(ctx context.Context, request artifactapp.RequestHistoricalFragments) (artifactapp.HistoricalFragmentSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var zero artifactapp.HistoricalFragmentSelection
	if s == nil || s.pool == nil || s.tenant == "" || s.actor == "" || request.ActorID != s.actor || request.Scope.UserID != s.actor || request.Scope.TenantID != s.tenant {
		return zero, artifactapp.ErrHistoricalSelection
	}
	raw, digest, err := artifactapp.CanonicalHistoricalRequest(request)
	if err != nil {
		return zero, err
	}
	// Decode the canonical order once. Existing requests need no registry or I/O.
	if err = json.Unmarshal(raw, &request); err != nil {
		return zero, artifactapp.ErrHistoricalSelection
	}
	if found, err := existingHistoryRequest(ctx, s.pool, request, raw, digest); found || err != nil {
		return historySelection(digest), err
	}
	items, err := s.readHistoryItems(ctx, s.pool, request, false)
	if err != nil {
		return zero, err
	}
	for n := range items {
		item := &items[n]
		if item.Existing {
			continue
		}
		if s.preparer == nil || s.objects == nil {
			return zero, artifactapp.ErrHistoricalSelection
		}
		// The same bounded trusted capture used by live intake sees only the
		// original runtime scope. No registry is opened or created in SQL.
		prepareCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		prepared := s.preparer.PrepareHistoricalObservation(prepareCtx, item.Capture)
		cancel()
		if prepared.ErrorCode != "" || !bytes.Equal(prepared.Frame, item.Capture.Frame) {
			return zero, artifactapp.ErrHistoricalSelection
		}
		receipt, e := artifactapp.DecodeObservationScanReceipt(prepared.Receipt, prepared.Frame)
		if e != nil || receipt.Scope != item.Capture.Scope || receipt.Provenance != item.Capture.Provenance {
			return zero, fragmentProofError()
		}
		item.Candidate.Frame = prepared.Frame
		item.Candidate.Receipt = prepared.Receipt
		p := historicalProvenance{SchemaVersion: "observation.provenance.v1", CaptureKind: "historical_request", RequestDigest: digest, SettlementID: item.Turn.SettlementID, CheckpointDigest: item.Turn.CheckpointDigest, InputDigest: artifactdomain.Checksum(item.Turn.Input), ReportDigest: item.Turn.ReportObject.Checksum, ManifestChecksum: item.Turn.ManifestObject.Checksum, EnvelopeDigest: item.Capture.Provenance.EnvelopeDigest}
		if p.EnvelopeDigest != "" {
			var normal struct {
				StartEnvelopeDigest string `json:"start_envelope_digest"`
			}
			_ = json.Unmarshal(item.Candidate.Provenance, &normal)
			p.StartEnvelopeDigest = normal.StartEnvelopeDigest
		}
		item.Candidate.Provenance, _ = json.Marshal(p)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	// History is optional. Bound waits in shared metadata readers and unique-key
	// insertion too; this setting is transaction-local and never affects owners.
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`); err != nil {
		return zero, err
	}
	// Serializes concurrent same-key requests before the final read, while
	// actual core rows are locked below against changes during this transaction.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, s.actor+"/"+request.IdempotencyKey); err != nil {
		return zero, err
	}
	if found, e := existingHistoryRequest(ctx, tx, request, raw, digest); found || e != nil {
		return historySelection(digest), e
	}
	// Domain commit takes settlement before Session; terminal Apply/replay takes
	// Session before settlement. No blocking order fits both. Acquire every core
	// row with NOWAIT, rolling this optional request back on any contention.
	// Both Turns and all exact snapshot/provenance checks remain mandatory.
	locked, err := tx.Query(ctx, `SELECT settlement_id FROM worker_turn_settlements WHERE tenant_id=$1 AND worker_session_id=$2 AND worker_run_id=$3 AND agent_turn_id=ANY($4) ORDER BY settlement_id FOR SHARE NOWAIT`, request.Scope.TenantID, request.Scope.WorkerSessionID, request.Scope.WorkerRunID, []string{request.Initial.AgentTurnID, request.Target.AgentTurnID})
	if err != nil {
		return zero, err
	}
	for locked.Next() {
		var id string
		if err = locked.Scan(&id); err != nil {
			locked.Close()
			return zero, err
		}
	}
	err = locked.Err()
	locked.Close()
	if err != nil {
		return zero, err
	}
	current, err := s.readHistoryItems(ctx, tx, request, true)
	if err != nil {
		return zero, err
	}
	if len(current) != len(items) {
		return zero, fragmentProofError()
	}
	members := make([]historySelectionMember, 0, len(items))
	for n, item := range items {
		fresh := current[n]
		if !item.Existing && item.Candidate.Source.Kind != "model" && reflect.DeepEqual(item.Turn, fresh.Turn) {
			fresh.Capture.Frame = item.Capture.Frame
			fresh.Candidate.Source.FrameDigest = item.Candidate.Source.FrameDigest
		}
		if !reflect.DeepEqual(item.Turn, fresh.Turn) || item.Candidate.Source != fresh.Candidate.Source || item.Existing != fresh.Existing || !bytes.Equal(item.Capture.Frame, fresh.Capture.Frame) || !reflect.DeepEqual(item.Capture.Provenance, fresh.Capture.Provenance) {
			return zero, fragmentProofError()
		}
		if !item.Existing && item.Candidate.Source.Kind != "model" {
			frame, e := artifactdomain.DecodeObservationFragment(item.Candidate.Frame)
			if e != nil {
				return zero, e
			}
			receipt, e := artifactapp.DecodeObservationScanReceipt(item.Candidate.Receipt, item.Candidate.Frame)
			if e != nil {
				return zero, e
			}
			p := basisProvenance{SchemaVersion: "observation.provenance.v1", SettlementID: item.Turn.SettlementID, CheckpointDigest: item.Turn.CheckpointDigest, InputDigest: artifactdomain.Checksum(item.Turn.Input), ReportDigest: item.Turn.ReportObject.Checksum}
			var evidence artifactapp.FragmentEvidence
			if e = loadBasisFragmentCoreBinding(ctx, tx, &evidence, frame, receipt, p, true); e != nil {
				return zero, e
			}
		}
		if !item.Existing {
			conflict, e := insertObservationCandidate(ctx, tx, item.Candidate)
			if e != nil {
				return zero, e
			}
			if conflict {
				return zero, artifactapp.ErrFragmentSourceConflict
			}
		}
		ref := item.Candidate.Source
		members = append(members, historySelectionMember{ref.Kind, ref.SourceID, ref.FrameDigest})
	}
	selection, _ := json.Marshal(members)
	if _, err = tx.Exec(ctx, `INSERT INTO observation_fragment_requests(tenant_id,actor_user_id,idempotency_key,request_bytes,request_digest,version,source_set) VALUES($1,$2,$3,$4,$5,$6,$7)`, s.tenant, s.actor, request.IdempotencyKey, raw, digest, request.Version, selection); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return historySelection(digest), nil
}
func historySelection(digest string) artifactapp.HistoricalFragmentSelection {
	return artifactapp.HistoricalFragmentSelection{RequestID: "history-" + strings.TrimPrefix(digest, "sha256:"), RequestDigest: digest}
}
func existingHistoryRequest(ctx context.Context, q fragmentQueryer, r artifactapp.RequestHistoricalFragments, raw []byte, digest string) (bool, error) {
	var stored []byte
	var got, tenant, version string
	err := q.QueryRow(ctx, `SELECT request_bytes,request_digest,tenant_id,version FROM observation_fragment_requests WHERE actor_user_id=$1 AND idempotency_key=$2`, r.ActorID, r.IdempotencyKey).Scan(&stored, &got, &tenant, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(raw, stored) || got != digest || tenant != r.Scope.TenantID || version != r.Version {
		return true, artifactapp.ErrHistoricalSelection
	}
	return true, nil
}

func readHistoryTurn(ctx context.Context, q fragmentQueryer, s artifactdomain.ObservationScope, ref artifactapp.HistoricalTurnRef, lock bool) (historyTurn, error) {
	var h historyTurn
	i := &h.Identity
	i.TenantID = s.TenantID
	i.UserID = s.UserID
	i.WorkspaceID = s.WorkspaceID
	i.WorkerSessionID = contractsv1.WorkerSessionID(s.WorkerSessionID)
	i.WorkerRunID = contractsv1.WorkerRunID(s.WorkerRunID)
	query := `SELECT t.agent_turn_id,t.agent_session_id,o.runtime_instance_id,o.runtime_generation,COALESCE(r.task_id,''),t.content,t.started_at,r.created_at,o.source_occurred_at,o.settlement_id,o.phase_state,o.phase_state_digest,
 rt.network_policy_id,rt.network_policy_revision,rt.network_policy_digest,rt.network_enforcement_status,
 m.manifest,m.manifest_object_key,m.manifest_checksum,m.manifest_size_bytes,a.artifact_ref,a.object_key,a.object_checksum,a.size_bytes,
 (SELECT COALESCE(array_agg(initial.agent_turn_id ORDER BY initial.agent_turn_id),ARRAY[]::text[]) FROM agent_turns initial WHERE initial.worker_run_id=r.worker_run_id AND initial.worker_session_id=s.worker_session_id AND initial.created_at=r.created_at AND initial.mode IN ('prompt','queue'))
 FROM worker_sessions s JOIN worker_runs r USING(worker_session_id)
 JOIN agent_turns t ON t.worker_run_id=r.worker_run_id AND t.worker_session_id=s.worker_session_id
 JOIN worker_turn_settlements o ON o.agent_turn_id=t.agent_turn_id AND o.agent_session_id=t.agent_session_id AND o.worker_run_id=r.worker_run_id AND o.worker_session_id=s.worker_session_id AND o.tenant_id=s.tenant_id
 JOIN runtime_instances rt ON rt.runtime_instance_id=o.runtime_instance_id AND rt.worker_session_id=s.worker_session_id AND rt.generation=o.runtime_generation
 JOIN trace_manifests m ON m.agent_turn_id=t.agent_turn_id AND m.worker_run_id=r.worker_run_id AND m.worker_session_id=s.worker_session_id
 JOIN artifact_refs a ON a.artifact_id=o.phase_state->'frozen_artifacts'->>'report_artifact_id'
 WHERE s.tenant_id=$1 AND s.user_id=$2 AND s.workspace_id=$3 AND s.worker_session_id=$4 AND r.worker_run_id=$5 AND t.agent_turn_id=$6 AND m.manifest_checksum=$7 AND o.phase_state<>'{}'::jsonb`
	if lock {
		query += ` FOR SHARE OF s,r,t,o,rt,m,a NOWAIT`
	}
	err := q.QueryRow(ctx, query, s.TenantID, s.UserID, s.WorkspaceID, s.WorkerSessionID, s.WorkerRunID, ref.AgentTurnID, ref.ManifestChecksum).Scan(&i.AgentTurnID, &i.AgentSessionID, &i.RuntimeInstanceID, &i.RuntimeGeneration, &i.TaskID, &h.Input, &h.Start, &h.Created, &h.End, &h.SettlementID, &h.Checkpoint, &h.CheckpointDigest, &i.RuntimeNetwork.ID, &i.RuntimeNetwork.Revision, &i.RuntimeNetwork.Digest, &i.RuntimeNetwork.EnforcementStatus, &h.Manifest, &h.ManifestObject.Key, &h.ManifestObject.Checksum, &h.ManifestObject.Size, &h.ReportRef, &h.ReportObject.Key, &h.ReportObject.Checksum, &h.ReportObject.Size, &h.InitialIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, artifactapp.ErrHistoricalSelection
	}
	if err != nil {
		return h, err
	}
	i.TraceStreamID = "trace-" + string(i.AgentTurnID)
	i.LangfuseTraceID = "langfuse-" + string(i.AgentTurnID)
	_, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(h.Checkpoint)
	if err != nil || digest != h.CheckpointDigest {
		return h, fragmentProofError()
	}
	return h, nil
}

func (s *FragmentHistoryStore) readHistoryItems(ctx context.Context, q fragmentQueryer, r artifactapp.RequestHistoricalFragments, lock bool) ([]historyItem, error) {
	initial, err := readHistoryTurn(ctx, q, r.Scope, r.Initial, lock)
	if err != nil {
		return nil, err
	}
	if len(initial.InitialIDs) != 1 || initial.InitialIDs[0] != r.Initial.AgentTurnID {
		return nil, artifactapp.ErrHistoricalSelection
	}
	target := initial
	if r.Target != r.Initial {
		target, err = readHistoryTurn(ctx, q, r.Scope, r.Target, lock)
		if err != nil {
			return nil, err
		}
	}
	var historical bool
	if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_fragment_run_exclusions WHERE version=$1 AND worker_run_id=$2 AND tenant_id=$3 AND user_id=$4 AND workspace_id=$5 AND worker_session_id=$6)`, r.Version, r.Scope.WorkerRunID, r.Scope.TenantID, r.Scope.UserID, r.Scope.WorkspaceID, r.Scope.WorkerSessionID).Scan(&historical); err != nil {
		return nil, err
	}
	var items []historyItem
	for _, b := range []struct {
		kind artifactdomain.FragmentKind
		turn historyTurn
	}{{artifactdomain.FragmentRunBasis, initial}, {artifactdomain.FragmentTurnBasis, target}} {
		h := b.turn
		var identity *artifactdomain.TurnIdentity
		if b.kind == artifactdomain.FragmentTurnBasis {
			identity = &h.Identity
		}
		id, e := artifactdomain.ObservationBasisSourceID(b.kind, r.Scope, identity)
		if e != nil {
			return nil, e
		}
		item := historyItem{Turn: h, Candidate: observationCandidateRecord{Source: artifactapp.FragmentRef{Kind: string(b.kind), SourceID: id}, Scope: r.Scope}}
		existing, e := readHistoryCandidate(ctx, q, &item)
		if e != nil {
			return nil, e
		}
		item.Existing = existing
		if !existing {
			if !historical {
				return nil, fragmentProofError()
			}
			frame := artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: b.kind, Scope: r.Scope, Turn: identity, StartedAt: h.Start, InputJSON: string(h.Input)}
			if b.kind == artifactdomain.FragmentRunBasis {
				frame.InitialTurnID = r.Initial.AgentTurnID
				frame.StartedAt = h.Created
			} else {
				frame.EndedAt = &h.End
			}
			// SQL revalidation does not touch objects. It checks the exact snapshot
			// and reuses the already verified report bytes via the frame digest.
			var report []byte
			if !lock {
				var e error
				report, e = s.readHistoryObjects(ctx, h)
				if e != nil {
					return nil, e
				}
				if b.kind == artifactdomain.FragmentTurnBasis {
					out, _ := json.Marshal(string(report))
					frame.OutputJSON = string(out)
				}
			} else if b.kind == artifactdomain.FragmentTurnBasis {
				// Filled by the caller's immutable prepared frame below; only metadata
				// is returned on this pass, avoiding ObjectStore in the transaction.
				frame.OutputJSON = `""`
			}
			raw, e := artifactdomain.EncodeObservationFragment(frame)
			if e != nil {
				return nil, e
			}
			item.Capture = artifactapp.HistoricalObservationCapture{Frame: raw, Scope: artifactapp.ObservationScanScope{WorkerSessionID: r.Scope.WorkerSessionID, RuntimeInstanceID: string(h.Identity.RuntimeInstanceID), RuntimeGeneration: uint64(h.Identity.RuntimeGeneration)}, Provenance: artifactapp.ObservationProvenance{CheckpointDigest: h.CheckpointDigest, InputDigest: artifactdomain.Checksum(h.Input), ReportDigest: h.ReportObject.Checksum}}
			item.Capture.Evidence = map[string]any{"input": json.RawMessage(h.Input), "report": string(report)}
			item.Candidate.Source.FrameDigest = artifactdomain.Checksum(raw)
		}
		items = append(items, item)
	}
	for _, ref := range r.Models {
		item, e := readHistoryModel(ctx, q, r.Scope, target, ref, lock)
		if e != nil {
			return nil, e
		}
		existing, e := readHistoryCandidate(ctx, q, &item)
		if e != nil {
			return nil, e
		}
		item.Existing = existing
		if !existing && !historical {
			return nil, fragmentProofError()
		}
		if !existing && len(item.Capture.Frame) == 0 {
			return nil, artifactapp.ErrHistoricalSelection
		}
		items = append(items, item)
	}
	return items, nil
}

func readHistoryCandidate(ctx context.Context, q fragmentQueryer, item *historyItem) (bool, error) {
	var c observationCandidateRecord
	var status string
	var conflict bool
	err := q.QueryRow(ctx, `SELECT frame_digest,frame,receipt,provenance,status,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,EXISTS(SELECT 1 FROM observation_fragment_conflicts x WHERE x.kind=c.kind AND x.source_id=c.source_id) FROM observation_candidates c WHERE kind=$1 AND source_id=$2 FOR SHARE`, item.Candidate.Source.Kind, item.Candidate.Source.SourceID).Scan(&c.Source.FrameDigest, &c.Frame, &c.Receipt, &c.Provenance, &status, &c.Scope.TenantID, &c.Scope.UserID, &c.Scope.WorkspaceID, &c.Scope.WorkerSessionID, &c.Scope.WorkerRunID, &conflict)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if conflict || c.Scope != item.Candidate.Scope {
		return false, fragmentProofError()
	}
	c.Source.Kind = item.Candidate.Source.Kind
	c.Source.SourceID = item.Candidate.Source.SourceID
	if status != "blocked" {
		if c.Source.Kind == "model" {
			// Core accepted facts were checked independently, even if basis
			// publication is still pending. Do not require that later stage here.
			if !bytes.Equal(c.Frame, item.Capture.Frame) {
				return false, fragmentProofError()
			}
			r, e := artifactapp.DecodeObservationScanReceipt(c.Receipt, c.Frame)
			if e != nil || r.Provenance != item.Capture.Provenance {
				return false, fragmentProofError()
			}
			var marker struct {
				CaptureKind string `json:"capture_kind"`
			}
			if json.Unmarshal(c.Provenance, &marker) != nil {
				return false, fragmentProofError()
			}
			if marker.CaptureKind != "" {
				f, e := artifactdomain.DecodeObservationFragment(c.Frame)
				if e != nil {
					return false, e
				}
				if _, e = validateHistoricalProvenance(ctx, q, artifactapp.FragmentEvidence{Source: c.Source, Frame: c.Frame, Receipt: c.Receipt, Provenance: c.Provenance}, f); e != nil {
					return false, e
				}
			} else {
				var p struct {
					Schema   string `json:"schema_version"`
					Envelope string `json:"envelope_digest"`
					Start    string `json:"start_envelope_digest"`
				}
				var expected struct {
					Start string `json:"start_envelope_digest"`
				}
				if decodeFragmentProvenance(c.Provenance, &p) != nil || json.Unmarshal(item.Candidate.Provenance, &expected) != nil || p.Schema != "observation.provenance.v1" || p.Envelope != item.Capture.Provenance.EnvelopeDigest || p.Start != expected.Start {
					return false, fragmentProofError()
				}
			}
		} else if _, e := readFragmentEvidence(ctx, q, c.Source); e != nil {
			return false, e
		}
	}
	item.Candidate = c
	return true, nil
}

func readHistoryModel(ctx context.Context, q fragmentQueryer, scope artifactdomain.ObservationScope, h historyTurn, ref artifactapp.HistoricalModelRef, lock bool) (historyItem, error) {
	item := historyItem{Turn: h}
	read := func(direction string, selected bool) (workerapp.RuntimeControlMessage, error) {
		var m workerapp.RuntimeControlMessage
		query := `SELECT direction,sequence,message_id,idempotency_key,fingerprint,envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,'') FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction=$2`
		args := []any{h.Identity.RuntimeInstanceID, direction}
		if selected {
			query += ` AND message_id=$3 AND sequence=$4 AND fingerprint=$5`
			args = append(args, ref.MessageID, ref.Sequence, ref.EnvelopeDigest)
		} else {
			query += ` AND envelope->>'message_type'='agent_turn.start' AND envelope->>'agent_turn_id'=$3`
			args = append(args, h.Identity.AgentTurnID)
		}
		if lock {
			query += ` FOR SHARE NOWAIT`
		}
		err := q.QueryRow(ctx, query, args...).Scan(&m.Direction, &m.Sequence, &m.MessageID, &m.IdempotencyKey, &m.Fingerprint, &m.Envelope, &m.Outcome, &m.EffectStatus, &m.ApplyIdempotencyKey, &m.ApplyOutcomeDigest)
		return m, err
	}
	event, err := read("event", true)
	if err != nil {
		return item, artifactapp.ErrHistoricalSelection
	}
	start, err := read("command", false)
	if err != nil {
		return item, artifactapp.ErrHistoricalSelection
	}
	var starts int
	if err = q.QueryRow(ctx, `SELECT count(*) FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='command' AND envelope->>'message_type'='agent_turn.start' AND envelope->>'agent_turn_id'=$2`, h.Identity.RuntimeInstanceID, h.Identity.AgentTurnID).Scan(&starts); err != nil {
		return item, err
	}
	if starts != 1 || workerapp.ValidateModelObservationBinding(event, start) != nil || event.EffectStatus != "applied" || event.ApplyIdempotencyKey != event.IdempotencyKey || event.ApplyOutcomeDigest != artifactdomain.Checksum([]byte(`{"status":"model_observation_recorded"}`)) || !artifactapp.ProjectionJSONEqual(event.Outcome, []byte(`{"status":"model_observation_recorded"}`)) {
		return item, artifactapp.ErrHistoricalSelection
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(event.Envelope)
	if err != nil || envelope.AgentTurnID != h.Identity.AgentTurnID || envelope.AgentSessionID != h.Identity.AgentSessionID || envelope.RuntimeGeneration != h.Identity.RuntimeGeneration {
		return item, artifactapp.ErrHistoricalSelection
	}
	f, err := artifactapp.ModelObservationFragment(scope, envelope)
	if err != nil {
		return item, err
	}
	// Accepted does not imply renderable. Keep the same logical ID for an
	// immutable no-content gap; never manufacture a frame to recapture it.
	raw, renderErr := artifactdomain.EncodeObservationFragment(f)
	id, err := artifactapp.ModelObservationSourceID(f)
	if err != nil {
		return item, err
	}
	p, _ := json.Marshal(map[string]string{"schema_version": "observation.provenance.v1", "envelope_digest": event.Fingerprint, "start_envelope_digest": start.Fingerprint})
	digest := event.Fingerprint
	if renderErr == nil {
		digest = artifactdomain.Checksum(raw)
	}
	item.Candidate = observationCandidateRecord{Source: artifactapp.FragmentRef{Kind: "model", SourceID: id, FrameDigest: digest}, Scope: scope, Provenance: p}
	item.Capture = artifactapp.HistoricalObservationCapture{Frame: raw, Evidence: envelope, Scope: artifactapp.ObservationScanScope{WorkerSessionID: scope.WorkerSessionID, RuntimeInstanceID: string(h.Identity.RuntimeInstanceID), RuntimeGeneration: uint64(h.Identity.RuntimeGeneration)}, Provenance: artifactapp.ObservationProvenance{EnvelopeDigest: event.Fingerprint}}
	return item, nil
}

func (s *FragmentHistoryStore) readHistoryObjects(ctx context.Context, h historyTurn) ([]byte, error) {
	if s.objects == nil {
		return nil, artifactapp.ErrHistoricalSelection
	}
	read := func(o workerapp.StoredObject) ([]byte, error) {
		if o.Size < 0 || o.Size > 8<<20 {
			return nil, fragmentProofError()
		}
		r, e := s.objects.Open(ctx, o.Key)
		if e != nil {
			return nil, artifactapp.ErrHistoricalSelection
		}
		defer r.Close()
		raw, e := io.ReadAll(io.LimitReader(r, o.Size+1))
		if e != nil || int64(len(raw)) != o.Size || artifactdomain.Checksum(raw) != o.Checksum {
			return nil, fragmentProofError()
		}
		return raw, nil
	}
	m, err := read(h.ManifestObject)
	if err != nil {
		return nil, err
	}
	decoded, err := contractsv1.DecodeTraceManifest(m)
	stored, e := contractsv1.DecodeTraceManifest(h.Manifest)
	if err != nil || e != nil || !reflect.DeepEqual(decoded, stored) {
		return nil, fragmentProofError()
	}
	return read(h.ReportObject)
}

// The reader checks the immutable canonical request and exact selected source,
// then shares the normal core checks. A bare candidate/receipt cannot establish
// historical authority. Later cumulative checkpoint extensions are permitted.
func validateHistoricalProvenance(ctx context.Context, q fragmentQueryer, e artifactapp.FragmentEvidence, f artifactdomain.ObservationFragment) (historicalProvenance, error) {
	var p historicalProvenance
	if decodeFragmentProvenance(e.Provenance, &p) != nil || p.SchemaVersion != "observation.provenance.v1" || p.CaptureKind != "historical_request" || p.RequestDigest == "" || p.CheckpointDigest == "" {
		return p, fragmentProofError()
	}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT r.request_bytes FROM observation_fragment_requests r WHERE r.request_digest=$1 AND r.version=$2 AND r.tenant_id=$3 AND r.actor_user_id=$4
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(r.source_set) x WHERE x->>'kind'=$5 AND x->>'source_id'=$6 AND x->>'frame_digest'=$7)
 AND EXISTS(SELECT 1 FROM observation_fragment_run_exclusions ex WHERE ex.version=r.version AND ex.worker_run_id=$8 AND ex.tenant_id=$3 AND ex.user_id=$4 AND ex.workspace_id=$9 AND ex.worker_session_id=$10)`, p.RequestDigest, artifactapp.FragmentProjectionVersion, f.Scope.TenantID, f.Scope.UserID, e.Source.Kind, e.Source.SourceID, e.Source.FrameDigest, f.Scope.WorkerRunID, f.Scope.WorkspaceID, f.Scope.WorkerSessionID).Scan(&raw)
	if err != nil {
		return p, fragmentProofError()
	}
	var r artifactapp.RequestHistoricalFragments
	if json.Unmarshal(raw, &r) != nil || r.Scope != f.Scope {
		return p, fragmentProofError()
	}
	canonical, digest, err := artifactapp.CanonicalHistoricalRequest(r)
	if err != nil || digest != p.RequestDigest || !bytes.Equal(canonical, raw) {
		return p, fragmentProofError()
	}
	ref := r.Target
	if f.Kind == artifactdomain.FragmentRunBasis {
		ref = r.Initial
	}
	h, err := readHistoryTurn(ctx, q, r.Scope, ref, false)
	if err != nil {
		return p, err
	}
	if h.SettlementID != p.SettlementID || h.ManifestObject.Checksum != p.ManifestChecksum || artifactdomain.Checksum(h.Input) != p.InputDigest || h.ReportObject.Checksum != p.ReportDigest {
		return p, fragmentProofError()
	}
	if f.Kind == artifactdomain.FragmentModel {
		matched := false
		for _, m := range r.Models {
			if m.EnvelopeDigest != p.EnvelopeDigest {
				continue
			}
			item, modelErr := readHistoryModel(ctx, q, r.Scope, h, m, false)
			if modelErr != nil {
				return p, modelErr
			}
			if item.Candidate.Source != e.Source || !bytes.Equal(item.Capture.Frame, e.Frame) {
				return p, fragmentProofError()
			}
			var normal struct {
				Start string `json:"start_envelope_digest"`
			}
			if json.Unmarshal(item.Candidate.Provenance, &normal) != nil || normal.Start != p.StartEnvelopeDigest {
				return p, fragmentProofError()
			}
			matched = true
		}
		if !matched {
			return p, fragmentProofError()
		}
	}
	return p, nil
}
