package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

type OrphanStore struct {
	pool *pgxpool.Pool
}

func NewOrphanStore(pool *pgxpool.Pool) *OrphanStore {
	return &OrphanStore{pool: pool}
}

func (store *OrphanStore) ClaimOrphanObjects(
	ctx context.Context,
	record artifactapp.ClaimOrphansRecord,
) ([]artifactapp.ClaimedObject, error) {
	if store == nil || store.pool == nil {
		return nil, fmt.Errorf("orphan reconciliation PostgreSQL pool is required")
	}
	if record.ClaimedAt.IsZero() || !record.ClaimExpiresAt.After(record.ClaimedAt) {
		return nil, fmt.Errorf("orphan reconciliation claim lease is invalid")
	}
	var claimed []artifactapp.ClaimedObject
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		for _, candidate := range record.Candidates {
			var key string
			err := tx.QueryRow(ctx, `
INSERT INTO object_reconciliation_claims
  (object_key,object_state,object_checksum,size_bytes,modified_at,status,attempt_count,
   claimed_at,claim_expires_at)
SELECT $1,$2,$3,$4,$5,'claimed',1,$6,$7
WHERE NOT EXISTS (SELECT 1 FROM evidence_object_refs WHERE object_key=$1)
ON CONFLICT (object_key) DO UPDATE
SET object_state=EXCLUDED.object_state,object_checksum=EXCLUDED.object_checksum,
    size_bytes=EXCLUDED.size_bytes,modified_at=EXCLUDED.modified_at,status='claimed',
    attempt_count=object_reconciliation_claims.attempt_count+1,error_code=NULL,
    claimed_at=EXCLUDED.claimed_at,claim_expires_at=EXCLUDED.claim_expires_at,completed_at=NULL
WHERE NOT EXISTS (SELECT 1 FROM evidence_object_refs WHERE object_key=EXCLUDED.object_key)
  AND (object_reconciliation_claims.status IN ('failed','deleted')
       OR object_reconciliation_claims.claim_expires_at <= EXCLUDED.claimed_at)
RETURNING object_key`,
				candidate.Key, candidate.State, candidate.Checksum, candidate.Size,
				candidate.ModifiedAt, record.ClaimedAt, record.ClaimExpiresAt,
			).Scan(&key)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("claim ObjectStore candidate: %w", err)
			}
			if key != candidate.Key {
				return fmt.Errorf("claimed ObjectStore candidate identity changed")
			}
			claimed = append(claimed, candidate)
		}
		return nil
	})
	return claimed, err
}

func (store *OrphanStore) CompleteOrphanObject(
	ctx context.Context,
	completion artifactapp.OrphanCompletion,
) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("orphan reconciliation PostgreSQL pool is required")
	}
	if completion.Status != artifactapp.ReconciliationDeleted && completion.Status != artifactapp.ReconciliationFailed {
		return fmt.Errorf("unsupported orphan reconciliation completion")
	}
	commandTag, err := store.pool.Exec(ctx, `
UPDATE object_reconciliation_claims
SET status=$2,error_code=NULLIF($3,''),completed_at=$4
WHERE object_key=$1 AND object_state=$5 AND object_checksum=$6 AND size_bytes=$7 AND status='claimed'`,
		completion.Candidate.Key, completion.Status, completion.ErrorCode, completion.CompletedAt,
		completion.Candidate.State, completion.Candidate.Checksum, completion.Candidate.Size,
	)
	if err != nil {
		return fmt.Errorf("complete ObjectStore reconciliation claim: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("ObjectStore reconciliation claim is stale")
	}
	return nil
}

var _ artifactapp.OrphanMetadataStore = (*OrphanStore)(nil)
