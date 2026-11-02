package localproduct

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
)

const telemetrySampleTimeout = 2 * time.Second

var errTelemetrySample = errors.New("telemetry_sample_failed")

type telemetrySampler struct {
	pool             *pgxpool.Pool
	tenantID, userID string
}

// Sample returns only aggregate columns from one read-only repeatable-read
// snapshot. Cancellation covers pool acquisition, SQL and rollback. A partial
// sample is never published, and database errors never escape this boundary.
func (s telemetrySampler) Sample(parent context.Context) (telemetry.Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, telemetrySampleTimeout)
	defer cancel()
	failed := func() (telemetry.Snapshot, error) { return telemetry.Snapshot{}, errTelemetrySample }
	if s.pool == nil || s.tenantID == "" || s.userID == "" {
		return failed()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return failed()
	}
	defer tx.Rollback(ctx)
	snapshot := telemetry.Snapshot{}
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&snapshot.SampledAt); err != nil {
		return failed()
	}
	if err = s.terminals(ctx, tx, &snapshot); err != nil {
		return failed()
	}
	if err = s.states(ctx, tx, &snapshot); err != nil {
		return failed()
	}
	if err = tx.Commit(ctx); err != nil {
		return failed()
	}
	return snapshot, nil
}

func (s telemetrySampler) terminals(ctx context.Context, tx pgx.Tx, snapshot *telemetry.Snapshot) error {
	// Verified finite references become SQL inputs, never GROUP BY dimensions.
	var ids, revisions, digests, harnesses []string
	registry := config.ProductionProfileRegistry()
	for _, id := range []string{config.DeepSeekCodexOpenAIV1, config.KimiCodexAnthropicV1} {
		profile, ok := registry.Agent(id)
		if !ok || profile.ValidateDigest() != nil || profile.AgentAdapter != "codex-acp" {
			continue
		}
		ids = append(ids, profile.ID)
		revisions = append(revisions, strconv.FormatUint(profile.Revision, 10))
		digests = append(digests, profile.Digest)
		harnesses = append(harnesses, string(telemetry.HarnessCodex))
	}
	var buckets strings.Builder
	for i, bound := range telemetry.DurationBucketBounds() {
		if i > 0 {
			buckets.WriteByte(',')
		}
		buckets.WriteString("count(*) FILTER (WHERE seconds <= " + strconv.FormatFloat(bound, 'f', -1, 64) + ")")
	}
	rows, err := tx.Query(ctx, `
WITH profiles AS (SELECT * FROM unnest($3::text[],$4::text[],$5::text[],$6::text[]) AS p(id,revision,digest,harness)),
runs AS (
 SELECT r.worker_run_id,r.worker_session_id,r.status,r.started_at,r.terminal_at,coalesce(p.harness,'unknown') AS harness
 FROM worker_runs r JOIN worker_sessions s USING(worker_session_id)
 LEFT JOIN profiles p ON (r.agent_profile_id,r.agent_profile_revision,r.agent_profile_digest)=(p.id,p.revision,p.digest)
 WHERE s.tenant_id=$1 AND s.user_id=$2
), terminals AS (
 SELECT 'worker_run_terminal' AS family,status,harness,started_at,terminal_at FROM runs WHERE terminal_at IS NOT NULL
 UNION ALL
 SELECT 'agent_turn_terminal',t.status,r.harness,t.started_at,t.terminal_at
 FROM agent_turns t JOIN runs r ON t.worker_run_id=r.worker_run_id AND t.worker_session_id=r.worker_session_id
 WHERE t.terminal_at IS NOT NULL
), normalized AS (
 SELECT family,CASE WHEN status IN ('completed','failed','cancelled','timed_out') THEN status ELSE 'unknown' END AS status,harness,
 CASE WHEN started_at IS NOT NULL AND terminal_at>=started_at THEN extract(epoch FROM terminal_at-started_at)::float8 END AS seconds FROM terminals
)
SELECT family,status,harness,count(*),count(seconds),coalesce(sum(seconds),0)::float8,ARRAY[`+buckets.String()+`]::bigint[]
FROM normalized GROUP BY family,status,harness ORDER BY family,status,harness LIMIT 21`, s.tenantID, s.userID, ids, revisions, digests, harnesses)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := 0
	for rows.Next() {
		groups++
		if groups > 20 {
			return errTelemetrySample
		}
		var family telemetry.Family
		var status telemetry.Status
		var harness telemetry.Harness
		var count, durationCount int64
		var sum float64
		var bucketCounts []int64
		if err = rows.Scan(&family, &status, &harness, &count, &durationCount, &sum, &bucketCounts); err != nil {
			return err
		}
		snapshot.Counts = append(snapshot.Counts, telemetry.CountMeasurement{Family: family, Status: status, Harness: harness, Value: float64(count)})
		if durationCount > 0 {
			d := telemetry.DurationMeasurement{Family: family, Status: status, Harness: harness, Count: uint64(durationCount), SumSeconds: sum}
			if len(bucketCounts) != len(d.Buckets) {
				return errTelemetrySample
			}
			for i, n := range bucketCounts {
				d.Buckets[i] = uint64(n)
			}
			snapshot.Durations = append(snapshot.Durations, d)
		}
	}
	return rows.Err()
}

func (s telemetrySampler) states(ctx context.Context, tx pgx.Tx, snapshot *telemetry.Snapshot) error {
	rows, err := tx.Query(ctx, `
WITH sessions AS (SELECT worker_session_id,status FROM worker_sessions WHERE tenant_id=$1 AND user_id=$2),
candidates AS (
 SELECT c.kind,c.source_id,c.frame_digest,c.status,c.created_at FROM observation_candidates c
 JOIN sessions s USING(worker_session_id)
 JOIN worker_runs r ON r.worker_run_id=c.worker_run_id AND r.worker_session_id=c.worker_session_id
 WHERE c.tenant_id=$1 AND c.user_id=$2
), states AS (
 SELECT 'session_state' AS family,CASE WHEN status IN ('allocating','interactive','running','recovering','waiting_user','idle','lost','expired','closed') THEN status ELSE 'unknown' END AS status,NULL::timestamptz AS created_at FROM sessions
 UNION ALL
 SELECT 'runtime_state',CASE WHEN r.status IN ('starting','active','suspect','unhealthy','draining','terminated','lost') THEN r.status ELSE 'unknown' END,NULL::timestamptz FROM runtime_instances r JOIN sessions s USING(worker_session_id)
 UNION ALL
 SELECT 'candidate_projection',CASE WHEN status IN ('pending','running','retryable') THEN 'pending' WHEN status='published' THEN 'projected' WHEN status='blocked' THEN 'failed' ELSE 'unknown' END,created_at FROM candidates
 UNION ALL
 SELECT 'fragment_projection',CASE WHEN j.status IN ('pending','running','retryable') THEN 'pending' WHEN j.status='passed' THEN 'projected' WHEN j.status='blocked' THEN 'failed' ELSE 'unknown' END,j.created_at FROM observation_fragment_jobs j JOIN candidates c USING(kind,source_id,frame_digest)
), grouped AS (
 SELECT family,status,count(*) AS n,CASE WHEN status='pending' THEN greatest(0,extract(epoch FROM transaction_timestamp()-min(created_at)))::float8 END AS age FROM states GROUP BY family,status
 UNION ALL
 SELECT 'queue_state','queued',count(*) FILTER (WHERE allocation_wait_reason IS DISTINCT FROM 'session_capacity'),NULL::float8 FROM tasks WHERE tenant_id=$1 AND initiator_user_id=$2 AND status='queued'
 UNION ALL
 SELECT 'queue_state','capacity_wait',count(*) FILTER (WHERE allocation_wait_reason='session_capacity'),NULL::float8 FROM tasks WHERE tenant_id=$1 AND initiator_user_id=$2 AND status='queued'
)
SELECT family,status,n,age FROM grouped ORDER BY family,status LIMIT 29`, s.tenantID, s.userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := 0
	for rows.Next() {
		groups++
		if groups > 28 {
			return errTelemetrySample
		}
		var family telemetry.Family
		var status telemetry.Status
		var n int64
		var age *float64
		if err = rows.Scan(&family, &status, &n, &age); err != nil {
			return err
		}
		snapshot.Counts = append(snapshot.Counts, telemetry.CountMeasurement{Family: family, Status: status, Harness: telemetry.HarnessUnknown, Value: float64(n)})
		if age != nil {
			snapshot.OldestPending = append(snapshot.OldestPending, telemetry.AgeMeasurement{Family: family, Seconds: *age})
		}
	}
	return rows.Err()
}
