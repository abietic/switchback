package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FragmentCoverageStore struct {
	pool          *pgxpool.Pool
	tenant, actor string
}

func NewFragmentCoverageStore(pool *pgxpool.Pool, tenant, actor string) *FragmentCoverageStore {
	return &FragmentCoverageStore{pool: pool, tenant: tenant, actor: actor}
}

func (s *FragmentCoverageStore) Read(ctx context.Context, actor string, scope artifactdomain.ObservationScope, version string) (artifactapp.FragmentCoverage, error) {
	out := artifactapp.FragmentCoverage{Version: version, Sources: []artifactapp.FragmentRef{}, GapCodes: []string{}}
	if s == nil || s.pool == nil || actor == "" || actor != s.actor || scope.UserID != actor || scope.TenantID != s.tenant || version == "" {
		return out, artifactapp.ErrHistoricalSelection
	}
	// Every source and status is read from one database snapshot. No pagination
	// or READ COMMITTED totals can mix publication/projection moments.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	live, selected, err := fragmentCoverageSelection(ctx, tx, scope, actor, version)
	if err != nil {
		return out, err
	}
	accepted, err := coverageAcceptedSources(ctx, tx, scope)
	if err != nil {
		return out, err
	}
	gaps := map[string]bool{}
	for key, ref := range accepted {
		chosen, explicit := selected[key]
		if !live && !explicit {
			continue
		}
		var frame, receipt []byte
		var digest, status, code, jobCode string
		var published, verified bool
		err = tx.QueryRow(ctx, `SELECT c.frame_digest,c.frame,c.receipt,c.status,COALESCE(c.error_code,''),
 EXISTS(SELECT 1 FROM observation_publications p WHERE p.kind=c.kind AND p.source_id=c.source_id AND p.frame_digest=c.frame_digest),
 EXISTS(SELECT 1 FROM observation_fragment_jobs j WHERE j.version=$3 AND j.kind=c.kind AND j.source_id=c.source_id AND j.frame_digest=c.frame_digest AND j.status='passed'),
 COALESCE((SELECT j.error_code FROM observation_fragment_jobs j WHERE j.version=$3 AND j.kind=c.kind AND j.source_id=c.source_id AND j.frame_digest=c.frame_digest),'')
 FROM observation_candidates c WHERE c.kind=$1 AND c.source_id=$2 AND c.tenant_id=$4 AND c.user_id=$5 AND c.workspace_id=$6 AND c.worker_session_id=$7 AND c.worker_run_id=$8`, ref.Kind, ref.SourceID, version, scope.TenantID, scope.UserID, scope.WorkspaceID, scope.WorkerSessionID, scope.WorkerRunID).Scan(&digest, &frame, &receipt, &status, &code, &published, &verified, &jobCode)
		if errors.Is(err, pgx.ErrNoRows) {
			out.Accepted++
			if explicit {
				ref.FrameDigest = chosen.FrameDigest
			}
			out.Sources = append(out.Sources, ref)
			gaps["fragment_proof_invalid"] = true
			continue
		}
		if err != nil {
			return out, err
		}
		ref.FrameDigest = digest
		if explicit && !live && chosen.FrameDigest != digest {
			continue
		}
		out.Accepted++
		out.Sources = append(out.Sources, ref)
		if _, e := artifactapp.DecodeObservationScanReceipt(receipt, frame); e == nil {
			out.Sealed++
		} else if status != "blocked" {
			gaps["fragment_proof_invalid"] = true
		}
		if published {
			out.Published++
		}
		if verified {
			out.Verified++
		}
		if code != "" {
			gaps[code] = true
		}
		if jobCode != "" {
			gaps[jobCode] = true
		}
		if status == "blocked" && code == "" {
			gaps["parent_basis_unavailable"] = true
		}
	}
	for code := range gaps {
		out.GapCodes = append(out.GapCodes, code)
	}
	sort.Strings(out.GapCodes)
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.SourceID < b.SourceID
	})
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// Shared snapshot eligibility for coverage and identity navigation. An excluded
// Run is visible only through an explicit exact source/digest selection.
func fragmentCoverageSelection(ctx context.Context, tx pgx.Tx, scope artifactdomain.ObservationScope, actor, version string) (bool, map[string]artifactapp.FragmentRef, error) {
	var authorized, live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1 AND s.worker_session_id=$2 AND s.tenant_id=$3 AND s.user_id=$4 AND s.workspace_id=$5),
 EXISTS(SELECT 1 FROM observation_fragment_activations WHERE version=$6) AND NOT EXISTS(SELECT 1 FROM observation_fragment_run_exclusions WHERE version=$6 AND worker_run_id=$1)`, scope.WorkerRunID, scope.WorkerSessionID, scope.TenantID, scope.UserID, scope.WorkspaceID, version).Scan(&authorized, &live)
	if err != nil {
		return false, nil, err
	}
	if !authorized {
		return false, nil, artifactapp.ErrHistoricalSelection
	}
	selected := map[string]artifactapp.FragmentRef{}
	rows, err := tx.Query(ctx, `SELECT request_bytes,source_set FROM observation_fragment_requests WHERE tenant_id=$1 AND actor_user_id=$2 AND version=$3`, scope.TenantID, actor, version)
	if err != nil {
		return false, nil, err
	}
	for rows.Next() {
		var raw, sourceSet []byte
		if err = rows.Scan(&raw, &sourceSet); err != nil {
			rows.Close()
			return false, nil, err
		}
		var r artifactapp.RequestHistoricalFragments
		var members []historySelectionMember
		if json.Unmarshal(raw, &r) != nil || r.Scope != scope {
			continue
		}
		if json.Unmarshal(sourceSet, &members) != nil {
			rows.Close()
			return false, nil, fragmentProofError()
		}
		for _, m := range members {
			selected[m.Kind+"/"+m.SourceID] = artifactapp.FragmentRef{Kind: m.Kind, SourceID: m.SourceID, FrameDigest: m.FrameDigest}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, nil, err
	}
	return live, selected, nil
}

// Start from frozen basis facts and accepted journal outcomes, not candidates,
// publications or jobs. This preserves honest no-content/missing-proof gaps.
func coverageAcceptedSources(ctx context.Context, tx pgx.Tx, s artifactdomain.ObservationScope) (map[string]artifactapp.FragmentRef, error) {
	refs, err := coverageAcceptedBasisSources(ctx, tx, s)
	if err != nil {
		return nil, err
	}
	return coverageAcceptedModels(ctx, tx, s, refs)
}

func coverageAcceptedBasisSources(ctx context.Context, tx pgx.Tx, s artifactdomain.ObservationScope) (map[string]artifactapp.FragmentRef, error) {
	refs := map[string]artifactapp.FragmentRef{}
	add := func(kind artifactdomain.FragmentKind, id string) {
		refs[string(kind)+"/"+id] = artifactapp.FragmentRef{Kind: string(kind), SourceID: id}
	}
	rows, err := tx.Query(ctx, `SELECT o.agent_turn_id,o.agent_session_id,o.runtime_instance_id,o.runtime_generation,
 ((t.created_at=r.created_at AND t.mode IN ('prompt','queue')) OR
 (SELECT count(*) FROM agent_turns initial WHERE initial.worker_run_id=r.worker_run_id AND initial.worker_session_id=r.worker_session_id AND initial.created_at=r.created_at AND initial.mode IN ('prompt','queue'))<>1)
 FROM worker_turn_settlements o JOIN agent_turns t ON t.agent_turn_id=o.agent_turn_id AND t.worker_run_id=o.worker_run_id AND t.worker_session_id=o.worker_session_id
 JOIN worker_runs r ON r.worker_run_id=o.worker_run_id AND r.worker_session_id=o.worker_session_id
 WHERE o.worker_run_id=$1 AND o.worker_session_id=$2 AND o.tenant_id=$3 AND o.phase_state<>'{}'::jsonb`, s.WorkerRunID, s.WorkerSessionID, s.TenantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		i := artifactdomain.TurnIdentity{TenantID: s.TenantID, UserID: s.UserID, WorkspaceID: s.WorkspaceID, WorkerSessionID: contractsv1.WorkerSessionID(s.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(s.WorkerRunID)}
		var initial bool
		if err = rows.Scan(&i.AgentTurnID, &i.AgentSessionID, &i.RuntimeInstanceID, &i.RuntimeGeneration, &initial); err != nil {
			rows.Close()
			return nil, err
		}
		id, e := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, s, &i)
		if e != nil {
			rows.Close()
			return nil, e
		}
		add(artifactdomain.FragmentTurnBasis, id)
		if initial {
			id, e = artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentRunBasis, s, nil)
			if e != nil {
				rows.Close()
				return nil, e
			}
			add(artifactdomain.FragmentRunBasis, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func coverageAcceptedModels(ctx context.Context, tx pgx.Tx, s artifactdomain.ObservationScope, refs map[string]artifactapp.FragmentRef) (map[string]artifactapp.FragmentRef, error) {
	rows, err := tx.Query(ctx, `SELECT m.envelope,m.message_id,m.sequence,m.fingerprint FROM runtime_control_messages m WHERE m.direction='event' AND m.effect_status='applied' AND m.outcome::jsonb='{"status":"model_observation_recorded"}'::jsonb AND m.envelope->>'message_type'='model.observation' AND m.envelope->>'worker_run_id'=$1 AND m.envelope->>'worker_session_id'=$2`, s.WorkerRunID, s.WorkerSessionID)
	if err != nil {
		return nil, err
	}
	type model struct {
		raw []byte
		ref artifactapp.HistoricalModelRef
	}
	var models []model
	for rows.Next() {
		var m model
		if err = rows.Scan(&m.raw, &m.ref.MessageID, &m.ref.Sequence, &m.ref.EnvelopeDigest); err != nil {
			rows.Close()
			return nil, err
		}
		models = append(models, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		envelope, e := contractsv1.DecodeRuntimeControlEnvelope(m.raw)
		if e != nil {
			return nil, fragmentProofError()
		}
		h := historyTurn{Identity: artifactdomain.TurnIdentity{AgentTurnID: envelope.AgentTurnID, AgentSessionID: envelope.AgentSessionID, RuntimeInstanceID: envelope.RuntimeInstanceID, RuntimeGeneration: envelope.RuntimeGeneration}}
		item, e := readHistoryModel(ctx, tx, s, h, m.ref, false)
		if e != nil {
			return nil, e
		}
		refs["model/"+item.Candidate.Source.SourceID] = item.Candidate.Source
	}
	return refs, nil
}
