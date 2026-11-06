package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"math"
	"strconv"
	"strings"
	"time"
)

// CapacityTx is deliberately non-constructible outside this package. It captures
// its transaction; operations cannot be redirected to a foreign transaction.
// Lock order: capacity pool BEFORE authority, Task, Session, queue or Run locks.
type CapacityTx struct {
	tx    pgx.Tx
	nonce string
	xid   string
}

func (s *CapacityStore) LockCapacityPool(ctx context.Context, tx pgx.Tx) (*CapacityTx, error) {
	if s == nil || s.nonce == "" || tx == nil {
		return nil, app.CapacityBindingMismatch
	}
	var nonce, xid string
	err := tx.QueryRow(ctx, `SELECT pool_nonce::text,pg_current_xact_id()::text FROM runtime_capacity_pools WHERE pool_id='local' FOR UPDATE`).Scan(&nonce, &xid)
	if err != nil {
		return nil, err
	}
	if nonce != s.nonce {
		return nil, app.CapacityBindingMismatch
	}
	return &CapacityTx{tx: tx, nonce: nonce, xid: xid}, nil
}
func (h *CapacityTx) valid(ctx context.Context) error {
	if h == nil || h.tx == nil || h.nonce == "" || h.xid == "" {
		return app.CapacityBindingMismatch
	}
	var xid, nonce string
	err := h.tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text,pool_nonce::text FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&xid, &nonce)
	if err != nil {
		return err
	}
	if xid != h.xid || nonce != h.nonce {
		return app.CapacityBindingMismatch
	}
	return nil
}
func (h *CapacityTx) sample(ctx context.Context) (capacitysnapshot.Facts, capacitysnapshot.Trust, error) {
	var f capacitysnapshot.Facts
	var t capacitysnapshot.Trust
	if err := h.valid(ctx); err != nil {
		return f, t, err
	}
	var status, trustDigest string
	var trust, raw []byte
	var received, now time.Time
	err := h.tx.QueryRow(ctx, `SELECT p.status,p.trust_bytes,COALESCE(p.trust_digest,''),s.payload,s.received_at,clock_timestamp() FROM runtime_capacity_pools p LEFT JOIN runtime_capacity_snapshots s ON s.pool_id=p.pool_id AND s.epoch=p.epoch AND s.sequence=p.latest_sequence WHERE p.pool_id='local'`).Scan(&status, &trust, &trustDigest, &raw, &received, &now)
	if err != nil {
		// Uninitialized row intentionally has no snapshot timestamps.
		var n int
		if e := h.tx.QueryRow(ctx, `SELECT latest_sequence FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&n); e == nil && n == 0 {
			return f, t, app.CapacityUninitialized
		}
		return f, t, err
	}
	if status != "ready" {
		return f, t, app.CapacityWaitReason(status)
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		return f, t, err
	}
	if t, err = persistedCapacityTrust(trust, trustDigest); err != nil {
		return f, t, err
	}
	if !fresh(f.ObservedAt, received, now) {
		return f, t, app.CapacityEvidenceStale
	}
	if capacitysnapshot.CheckFacts(f, t) != nil {
		return f, t, app.CapacityInventoryUnknown
	}
	return f, t, nil
}
func (h *CapacityTx) ReserveAndBind(ctx context.Context, target app.CapacityTarget, idempotencyKey, permitID string) (app.CapacityReservation, error) {
	var out app.CapacityReservation
	if err := h.valid(ctx); err != nil {
		return out, err
	}
	if target.Kind != "task_initial" {
		return out, app.CapacityPathNotIntegrated
	}
	if !validTarget(target) || !capacityText(idempotencyKey) || !capacityText(permitID) {
		return out, app.CapacityBindingMismatch
	}
	p, err := capacityProfile(target.Profile)
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(target)
	digest := capacitysnapshot.Digest(raw)
	out, oldDigest, err := h.findReservation(ctx, `tenant_id=$1 AND idempotency_key=$2`, target.TenantID, idempotencyKey)
	if err == nil {
		if oldDigest != digest {
			return out, app.CapacityBindingMismatch
		}
		if out.RunID == "" {
			if out.Released {
				return out, app.CapacityRequestExpired
			}
			var expired bool
			if err = h.tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, out.ExpiresAt).Scan(&expired); err != nil {
				return out, err
			}
			if expired {
				return out, app.CapacityRequestExpired
			}
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	f, t, err := h.sample(ctx)
	if err != nil {
		return out, err
	}
	if err = h.validateResidentIdentities(ctx, f); err != nil {
		return out, err
	}
	if err = h.importLegacy(ctx, t); err != nil {
		return out, err
	}
	var residentID string
	var oldProfile []byte
	var removed bool
	var origin string
	err = h.tx.QueryRow(ctx, `SELECT commitment_id::text,profile_bytes,removal_sequence IS NOT NULL,origin FROM runtime_capacity_residents WHERE pool_id='local' AND worker_session_id=$1 AND generation=$2`, target.SessionID, target.Generation).Scan(&residentID, &oldProfile, &removed, &origin)
	newResident := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !newResident {
		return out, err
	}
	profile, _ := json.Marshal(target.Profile)
	if !newResident && (!bytes.Equal(profile, oldProfile) || removed || origin != "reserved") {
		return out, app.CapacityBindingMismatch
	}
	if err = h.budget(ctx, f, t, newResident, p); err != nil {
		return out, err
	}
	// TTL only revokes unconsumed execution. Its resident commitment is retained.
	if err = h.expireUnconsumedExecutions(ctx); err != nil {
		return out, err
	}
	var active bool
	if err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_executions WHERE pool_id='local' AND worker_session_id=$1 AND released_at IS NULL)`, target.SessionID).Scan(&active); err != nil {
		return out, err
	}
	if active {
		return out, app.CapacityBindingMismatch
	}
	if newResident {
		err = h.tx.QueryRow(ctx, `INSERT INTO runtime_capacity_residents(pool_id,worker_session_id,generation,profile_bytes,engine_id,cpu_milli,memory_bytes,layer_bytes) VALUES('local',$1,$2,$3,$4,$5,$6,$7) RETURNING commitment_id::text`, target.SessionID, target.Generation, profile, t.Identity.EngineID, p.CPUUnits*1000, p.MemoryBytes, p.WritableLayerBytes).Scan(&residentID)
		if err != nil {
			return out, err
		}
	}
	err = h.tx.QueryRow(ctx, `INSERT INTO runtime_capacity_executions(pool_id,tenant_id,idempotency_key,target_digest,target_bytes,worker_session_id,commitment_id,snapshot_sequence,permit_id) SELECT 'local',$1,$2,$3,$4,$5,$6,$7,$8 WHERE EXISTS(SELECT 1 FROM runtime_capacity_pools p JOIN runtime_capacity_snapshots s ON s.pool_id=p.pool_id AND s.epoch=p.epoch AND s.sequence=p.latest_sequence WHERE p.pool_id='local' AND p.status='ready' AND s.observed_at>=clock_timestamp()-interval '30 seconds' AND s.received_at>=clock_timestamp()-interval '30 seconds') RETURNING token::text,expires_at`, target.TenantID, idempotencyKey, digest, raw, target.SessionID, residentID, f.Sequence, permitID).Scan(&out.Token, &out.ExpiresAt)
	out.CommitmentID = residentID
	out.PermitID = permitID
	out.SnapshotSequence = f.Sequence
	out.Guarantee = copyCapacityGuarantee(t.AdmissionPolicy)
	return out, err
}
func (h *CapacityTx) findReservation(ctx context.Context, where string, args ...any) (app.CapacityReservation, string, error) {
	var r app.CapacityReservation
	var digest string
	err := h.tx.QueryRow(ctx, `SELECT token::text,commitment_id::text,permit_id,snapshot_sequence,expires_at,COALESCE(run_id,''),COALESCE(turn_id,''),COALESCE(runtime_id,''),released_at IS NOT NULL,target_digest FROM runtime_capacity_executions WHERE pool_id='local' AND `+where, args...).Scan(&r.Token, &r.CommitmentID, &r.PermitID, &r.SnapshotSequence, &r.ExpiresAt, &r.RunID, &r.TurnID, &r.RuntimeID, &r.Released, &digest)
	if err != nil {
		return r, digest, err
	}
	trust, err := h.originalTrust(ctx)
	if err != nil {
		return r, digest, err
	}
	r.Guarantee = copyCapacityGuarantee(trust.AdmissionPolicy)
	return r, digest, nil
}
func (h *CapacityTx) Consume(ctx context.Context, c app.CapacityConsumption) (app.CapacityReservation, error) {
	var out app.CapacityReservation
	if err := h.valid(ctx); err != nil {
		return out, err
	}
	if !validTarget(c.Target) || !capacityText(c.Token) || !capacityText(c.PermitID) || !capacityText(c.RunID) || !capacityText(c.TurnID) || !capacityText(c.RuntimeID) {
		return out, app.CapacityBindingMismatch
	}
	out, digest, err := h.findReservation(ctx, `token::text=$1`, c.Token)
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(c.Target)
	if digest != capacitysnapshot.Digest(raw) || c.PermitID != out.PermitID {
		return out, app.CapacityBindingMismatch
	}
	if out.RunID != "" {
		if out.RunID != c.RunID || out.TurnID != c.TurnID || out.RuntimeID != c.RuntimeID {
			return out, app.CapacityBindingMismatch
		}
		return out, nil // committed response-loss replay never reserves or starts again
	}
	if out.Released {
		return out, app.CapacityRequestExpired
	}
	var expired bool
	if err = h.tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, out.ExpiresAt).Scan(&expired); err != nil {
		return out, err
	}
	if expired {
		return out, app.CapacityRequestExpired
	}
	f, t, err := h.sample(ctx)
	if err != nil {
		return out, err
	}
	p, err := capacityProfile(c.Target.Profile)
	if err != nil {
		return out, err
	}
	if err = h.budget(ctx, f, t, false, p); err != nil {
		return out, err
	}
	var runtime string
	if err = h.tx.QueryRow(ctx, `SELECT COALESCE(runtime_instance_id,'') FROM runtime_capacity_residents WHERE commitment_id=$1`, out.CommitmentID).Scan(&runtime); err != nil {
		return out, err
	}
	if runtime != "" && runtime != c.RuntimeID {
		return out, app.CapacityBindingMismatch
	}
	if _, err = h.tx.Exec(ctx, `UPDATE runtime_capacity_residents SET runtime_instance_id=$2 WHERE commitment_id=$1`, out.CommitmentID, c.RuntimeID); err != nil {
		return out, err
	}
	if err = h.tx.QueryRow(ctx, `UPDATE runtime_capacity_executions SET run_id=$2,turn_id=$3,runtime_id=$4,consumed_at=clock_timestamp() WHERE token=$1 AND expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM runtime_capacity_pools p JOIN runtime_capacity_snapshots s ON s.pool_id=p.pool_id AND s.epoch=p.epoch AND s.sequence=p.latest_sequence WHERE p.pool_id='local' AND p.status='ready' AND s.observed_at>=clock_timestamp()-interval '30 seconds' AND s.received_at>=clock_timestamp()-interval '30 seconds') RETURNING token::text`, c.Token, c.RunID, c.TurnID, c.RuntimeID).Scan(&out.Token); err != nil {
		return out, err
	}
	out.RunID = c.RunID
	out.TurnID = c.TurnID
	out.RuntimeID = c.RuntimeID
	return out, nil
}

// ReleaseExecution is settlement-only: it validates persisted terminal facts
// and never needs a fresh host sample. It does not release resident or storage.
func (h *CapacityTx) ReleaseExecution(ctx context.Context, c app.CapacityConsumption) error {
	if err := h.valid(ctx); err != nil {
		return err
	}
	out, digest, err := h.findSettlementReservation(ctx, `token::text=$1`, c.Token)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(c.Target)
	if digest != capacitysnapshot.Digest(raw) || out.PermitID != c.PermitID || out.RunID != c.RunID || out.TurnID != c.TurnID || out.RuntimeID != c.RuntimeID || out.RunID == "" {
		return app.CapacityBindingMismatch
	}
	var snapshot []byte
	if err = h.tx.QueryRow(ctx, `SELECT start_snapshot FROM worker_runs WHERE worker_run_id=$1`, c.RunID).Scan(&snapshot); err != nil {
		return err
	}
	if !settlementGuaranteeMatches(snapshot, out.Guarantee) {
		return app.CapacityBindingMismatch
	}
	if out.Released {
		return nil
	}
	var terminal bool
	err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turns t JOIN worker_runs r ON r.worker_run_id=t.worker_run_id JOIN runtime_instances i ON i.runtime_instance_id=$3 WHERE t.agent_turn_id=$1 AND r.worker_run_id=$2 AND t.worker_session_id=$4 AND r.worker_session_id=$4 AND i.worker_session_id=$4 AND i.generation=$5 AND t.status IN ('completed','cancelled','timed_out','failed'))`, c.TurnID, c.RunID, c.RuntimeID, c.Target.SessionID, c.Target.Generation).Scan(&terminal)
	if err != nil {
		return err
	}
	if !terminal {
		return app.CapacityBindingMismatch
	}
	_, err = h.tx.Exec(ctx, `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='turn_terminal' WHERE token=$1 AND released_at IS NULL`, c.Token)
	return err
}

// ConfirmRemoval deliberately cannot release a resident until the deployment
// has durable, independently verifiable secret-authority revocation. Current
// legacy cleanup JSON booleans do not provide that fact. Exact raw inspection
// and the control fence are checked first to make the missing seam explicit.
func (h *CapacityTx) ConfirmRemoval(ctx context.Context, commitmentID string, sequence int64) error {
	if err := h.valid(ctx); err != nil {
		return err
	}
	var session, runtime, container, engine string
	var generation int64
	err := h.tx.QueryRow(ctx, `SELECT worker_session_id,generation,COALESCE(runtime_instance_id,''),COALESCE(container_id,''),engine_id FROM runtime_capacity_residents WHERE commitment_id::text=$1 AND removal_sequence IS NULL`, commitmentID).Scan(&session, &generation, &runtime, &container, &engine)
	if err != nil {
		return err
	}
	if runtime == "" || container == "" {
		return app.CapacityBindingMismatch
	}
	var raw []byte
	var observed, received time.Time
	err = h.tx.QueryRow(ctx, `SELECT s.payload,s.observed_at,s.received_at FROM runtime_capacity_snapshots s JOIN runtime_capacity_pools p ON p.pool_id=s.pool_id AND p.epoch=s.epoch WHERE s.pool_id='local' AND s.sequence=$1`, sequence).Scan(&raw, &observed, &received)
	if err != nil {
		return err
	}
	var f capacitysnapshot.Facts
	if err = json.Unmarshal(raw, &f); err != nil {
		return err
	}
	trust, err := h.originalTrust(ctx)
	if err != nil {
		return err
	}
	if capacitysnapshot.CheckFacts(f, trust) != nil {
		return app.CapacityEvidenceInvalid
	}
	for _, r := range f.Removals {
		if r.EngineID != engine || r.SessionID != session || r.RuntimeID != runtime || r.ContainerID != container || r.Generation != uint64(generation) {
			continue
		}
		// Secret-volume identity follows the existing provisioner naming contract.
		if r.SecretVolumeID != "switchback-runtime-secrets-"+runtime+"-g"+strconv.FormatInt(generation, 10) {
			return app.CapacityBindingMismatch
		}
		var fenced bool
		err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_instances i JOIN runtime_control_channels c ON c.runtime_instance_id=i.runtime_instance_id WHERE i.runtime_instance_id=$1 AND i.worker_session_id=$2 AND i.generation=$3 AND i.container_id=$4 AND i.status='terminated' AND c.runtime_generation=$3 AND c.channel_epoch=$5 AND c.status='fenced' AND c.fenced_at<=$6 AND i.ended_at<=$6 AND c.fenced_at<=$7 AND i.ended_at<=$7)`, runtime, session, generation, container, r.ChannelEpoch, observed, received).Scan(&fenced)
		if err != nil {
			return err
		}
		if !fenced {
			return app.CapacityBindingMismatch
		}
		return app.CapacitySecretFenceUnproven
	}
	return app.CapacityBindingMismatch
}
func capacityText(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r\t")
}
func validTarget(t app.CapacityTarget) bool {
	if t.Kind != "task_initial" || !capacityText(t.TenantID) || !capacityText(t.ActorID) || !capacityText(t.SessionID) || !capacityText(t.TaskID) {
		return false
	}
	for _, n := range []uint64{t.Generation, t.SessionRevision, t.TaskRevision, t.QueueSequence, t.QueueRevision} {
		if n == 0 || n > math.MaxInt64 {
			return false
		}
	}
	return true
}
