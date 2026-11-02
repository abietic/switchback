package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CapacityStore owns a single deployment pool. Construction performs no writes
// and cannot enable any production start path. Trust is separately pinned once.
type CapacityStore struct {
	pool  *pgxpool.Pool
	nonce string
}

func NewCapacityStore(ctx context.Context, pool *pgxpool.Pool) (*CapacityStore, error) {
	if pool == nil {
		return nil, app.CapacityUninitialized
	}
	s := &CapacityStore{pool: pool}
	if err := pool.QueryRow(ctx, `SELECT pool_nonce::text FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&s.nonce); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *CapacityStore) InitializeTrust(ctx context.Context, trust capacitysnapshot.Trust) error {
	if err := capacitysnapshot.ValidateTrust(trust); err != nil {
		return fmt.Errorf("deployment trust: %w", err)
	}
	raw, err := json.Marshal(trust)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		return err
	}
	var old []byte
	var oldDigest string
	if err = tx.QueryRow(ctx, `SELECT trust_bytes,COALESCE(trust_digest,'') FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&old, &oldDigest); err != nil {
		return err
	}
	if old != nil {
		if _, err := persistedCapacityTrust(old, oldDigest); err != nil {
			return err
		}
		if !bytes.Equal(old, raw) {
			return app.CapacityIdentityDrift
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime_capacity_pools SET trust_bytes=$1,trust_digest=$2,status='capacity_evidence_stale',revision=revision+1 WHERE pool_id='local'`, raw, capacitysnapshot.Digest(raw)); err != nil {
		return err
	}
	_ = h
	return tx.Commit(ctx)
}
func (s *CapacityStore) readTrust(ctx context.Context) (capacitysnapshot.Trust, error) {
	var raw []byte
	var digest string
	var trust capacitysnapshot.Trust
	if s == nil || s.pool == nil {
		return trust, app.CapacityUninitialized
	}
	if err := s.pool.QueryRow(ctx, `SELECT trust_bytes,COALESCE(trust_digest,'') FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&raw, &digest); err != nil {
		return trust, err
	}
	return persistedCapacityTrust(raw, digest)
}

// ObservePool does all signature/JSON work outside the business transaction.
// Invalid input blocks admission durably without deleting the last good sample.
func (s *CapacityStore) ObservePool(ctx context.Context, raw []byte) error {
	trust, err := s.readTrust(ctx)
	if err != nil {
		return err
	}
	verified, err := capacitysnapshot.Verify(raw, trust)
	if err != nil {
		return s.block(ctx, app.CapacityEvidenceInvalid)
	}
	f := verified.Facts()
	reason := app.CapacityWaitReason("")
	if f.Identity != trust.Identity || f.ManifestDigest != trust.ManifestDigest {
		reason = app.CapacityIdentityDrift
	} else if capacitysnapshot.CheckFacts(f, trust) != nil {
		reason = app.CapacityInventoryUnknown
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		return err
	}
	var latest int64
	var epoch, status string
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT latest_sequence,epoch::text,status,clock_timestamp() FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&latest, &epoch, &status, &now); err != nil {
		return err
	}
	if f.Sequence == latest {
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT envelope FROM runtime_capacity_snapshots WHERE pool_id='local' AND epoch=$1 AND sequence=$2`, epoch, latest).Scan(&old); err != nil {
			return err
		}
		if bytes.Equal(old, raw) {
			if status == "ready" && !fresh(f.ObservedAt, now, now) {
				status = string(app.CapacityEvidenceStale)
				if _, err = tx.Exec(ctx, `UPDATE runtime_capacity_pools SET status=$1,revision=revision+1 WHERE pool_id='local'`, status); err != nil {
					return err
				}
			}
			if err = tx.Commit(ctx); err != nil {
				return err
			}
			if status != "ready" {
				return app.CapacityWaitReason(status)
			}
			return nil
		}
		reason = app.CapacityEvidenceInvalid
	}
	if f.Sequence < latest {
		reason = app.CapacityEvidenceInvalid
	}
	if !fresh(f.ObservedAt, now, now) && reason == "" {
		reason = app.CapacityEvidenceStale
	}
	if f.Sequence > latest {
		if _, err = tx.Exec(ctx, `INSERT INTO runtime_capacity_snapshots(pool_id,epoch,sequence,envelope,payload,signature,digest,observed_at) VALUES('local',$1,$2,$3,$4,$5,$6,$7)`, epoch, f.Sequence, verified.Raw(), verified.Payload(), verified.Signature(), verified.Digest(), f.ObservedAt); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime_capacity_pools SET latest_sequence=$1 WHERE pool_id='local'`, f.Sequence); err != nil {
			return err
		}
	}
	if reason == "" {
		if err = h.budget(ctx, f, trust, false, config.RuntimeResourceProfile{}); err != nil {
			var wait app.CapacityWaitReason
			if !errors.As(err, &wait) {
				return err
			}
			reason = wait
		}
	}
	status = "ready"
	if reason != "" {
		status = string(reason)
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime_capacity_pools SET status=$1,revision=revision+1 WHERE pool_id='local'`, status); err != nil {
		return err
	}
	_ = h
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if reason != "" {
		return reason
	}
	return nil
}
func (s *CapacityStore) block(ctx context.Context, reason app.CapacityWaitReason) error {
	if s == nil || s.pool == nil {
		return app.CapacityUninitialized
	}
	_, err := s.pool.Exec(ctx, `UPDATE runtime_capacity_pools SET status=$1,revision=revision+1 WHERE pool_id='local'`, string(reason))
	if err != nil {
		return err
	}
	return reason
}

// ReconcileCapacity reopens the deployment-owned file. Read failure also fences
// old fresh evidence; no filesystem or signature I/O occurs under a pool lock.
func (s *CapacityStore) ReconcileCapacity(ctx context.Context, r *capacitysnapshot.Reader) error {
	if err := InCapacityTransaction(ctx, s.pool, func(tx pgx.Tx, h *CapacityTx) error { return h.expireUnconsumedExecutions(ctx) }); err != nil {
		return err
	}
	raw, err := r.Read()
	if err != nil {
		return s.block(ctx, app.CapacityEvidenceInvalid)
	}
	return s.ObservePool(ctx, raw)
}
func (s *CapacityStore) ReadCapacity(ctx context.Context) (app.CapacityDiagnostics, error) {
	var d app.CapacityDiagnostics
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	h, err := s.LockCapacityPool(ctx, tx)
	if err != nil {
		return d, err
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status,revision,latest_sequence,(SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL),(SELECT count(*) FROM runtime_capacity_executions WHERE released_at IS NULL) FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&status, &d.Revision, &d.LatestSequence, &d.ResidentCount, &d.ActiveExecutions); err != nil {
		return d, err
	}
	d.Status = app.CapacityWaitReason(status)
	if status == "ready" {
		if _, _, err = h.sample(ctx); err != nil {
			var reason app.CapacityWaitReason
			if !errors.As(err, &reason) {
				return d, err
			}
			d.Status = reason
		}
	}
	return d, tx.Commit(ctx)
}
func fresh(observed, received, now time.Time) bool {
	return !observed.After(received.Add(2*time.Second)) && !observed.After(now.Add(2*time.Second)) && now.Sub(observed) <= 30*time.Second && now.Sub(received) <= 30*time.Second
}
