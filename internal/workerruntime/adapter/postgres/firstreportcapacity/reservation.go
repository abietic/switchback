// Package firstreportcapacity binds the isolated first report's single capacity
// reservation to the existing PostgreSQL dispatch permit transaction.
package firstreportcapacity

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

var engineIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,127}$`)

func Reference(permitID string) string { return "first-report/" + permitID }

// Reserve runs after the permit insert in the same transaction. A unique Engine
// commitment makes competing initial Tasks contend in PostgreSQL. A conflicting
// reservation or a stale observation rolls back the permit and queue changes.
func Reserve(ctx context.Context, tx platformpostgres.Tx, permitID, taskID string, revision uint64, evidence *application.FirstReportCapacityEvidence) error {
	profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok || !fits(evidence, profile) {
		return application.ErrFirstReportCapacityUnavailable
	}
	var inserted string
	err := tx.QueryRow(ctx, `
INSERT INTO first_report_capacity_reservations (
  permit_id,engine_id,task_id,task_revision,resource_profile_id,resource_profile_digest,
  available_nano_cpus,available_memory_bytes,available_storage_bytes,observed_at,reserved_at
)
SELECT $1::text,$2::text,$3::text,$4::bigint,$5::text,$6::text,
       $7::bigint,$8::bigint,$9::bigint,$10::timestamptz,sample.now
FROM (SELECT clock_timestamp() AS now) sample
WHERE $10::timestamptz BETWEEN sample.now - interval '30 seconds' AND sample.now
ON CONFLICT (engine_id) DO NOTHING
RETURNING permit_id`, permitID, evidence.EngineID, taskID, revision, profile.ID, profile.Digest,
		evidence.AvailableNanoCPUs, evidence.AvailableMemoryBytes, evidence.AvailableStorageBytes,
		evidence.ObservedAt).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrFirstReportCapacityUnavailable
	}
	if err != nil {
		return fmt.Errorf("reserve first-report capacity: %w", err)
	}
	// Check again after taking the unique constraint lock, which can wait for
	// another transaction. The caller rolls back if the sample aged out.
	var fresh bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz >= clock_timestamp() - interval '30 seconds'`, evidence.ObservedAt).Scan(&fresh); err != nil {
		return fmt.Errorf("recheck first-report capacity evidence: %w", err)
	}
	if !fresh {
		return application.ErrFirstReportCapacityUnavailable
	}
	return nil
}

// Verify is called before writing the first WorkerRun. It returns the Task
// revision that must still match when the same transaction advances the Task.
func Verify(ctx context.Context, tx platformpostgres.Tx, permitID, taskID, resourceProfileID string) (uint64, error) {
	profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok || resourceProfileID != profile.ID {
		return 0, application.ErrFirstReportCapacityUnavailable
	}
	var revision uint64
	err := tx.QueryRow(ctx, `
SELECT r.task_revision
FROM first_report_capacity_reservations r
JOIN session_dispatch_permits p ON p.permit_id=r.permit_id
WHERE r.permit_id=$1 AND r.task_id=$2 AND p.capacity_admission_ref=$3
  AND r.resource_profile_id=$4 AND r.resource_profile_digest=$5
  AND r.observed_at BETWEEN clock_timestamp() - interval '30 seconds' AND clock_timestamp()`,
		permitID, taskID, Reference(permitID), profile.ID, profile.Digest).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, application.ErrFirstReportCapacityUnavailable
	}
	if err != nil {
		return 0, fmt.Errorf("verify first-report capacity reservation: %w", err)
	}
	return revision, nil
}

func fits(evidence *application.FirstReportCapacityEvidence, profile config.RuntimeResourceProfile) bool {
	return evidence != nil && engineIDPattern.MatchString(evidence.EngineID) && !evidence.ObservedAt.IsZero() &&
		evidence.AvailableNanoCPUs > 0 && evidence.AvailableMemoryBytes > 0 && evidence.AvailableStorageBytes > 0 &&
		fourFifths(evidence.AvailableNanoCPUs) >= profile.CPUUnits*1_000_000_000 &&
		fourFifths(evidence.AvailableMemoryBytes) >= profile.MemoryBytes &&
		fourFifths(evidence.AvailableStorageBytes) >= profile.WritableLayerBytes
}

func fourFifths(total int64) int64 { return (total/5)*4 + (total%5)*4/5 }
