package localproduct

import (
	"context"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workerTerminalWindow = time.Minute
const workerTerminalLimit = 1024

// workerTerminalObserver is an optional, lossy view of committed terminal rows.
// No execution hook, durable cursor or domain counter is maintained here.
// Startup excludes history. Late commits are visible only inside the 60-second
// lookback; overflow, outages and restart can lose logs. The bounded dedup set
// reduces repeated in-process observations, not an exactly-once guarantee.
type workerTerminalObserver struct {
	pool         *pgxpool.Pool
	tenant, user string
	started      time.Time
	now          func() time.Time
	seen         map[string]time.Time
	metrics      *telemetry.Metrics
	emit         func(telemetry.Event)
}

func newWorkerTerminalObserver(pool *pgxpool.Pool, tenant, user string, metrics *telemetry.Metrics, emit func(telemetry.Event), now func() time.Time) *workerTerminalObserver {
	return &workerTerminalObserver{pool: pool, tenant: tenant, user: user, started: now(), now: now, seen: map[string]time.Time{}, metrics: metrics, emit: emit}
}
func (o *workerTerminalObserver) run(ctx context.Context) {
	o.sample(ctx)
	ticker := time.NewTicker(workerOutputDiscovery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.sample(ctx)
		}
	}
}
func (o *workerTerminalObserver) sample(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, telemetrySampleTimeout)
	defer cancel()
	through := o.now()
	cutoff := through.Add(-workerTerminalWindow)
	if cutoff.Before(o.started) {
		cutoff = o.started
	}
	for key, at := range o.seen {
		if at.Before(cutoff) {
			delete(o.seen, key)
		}
	}
	events, err := o.read(ctx, cutoff, through)
	if err != nil {
		o.metrics.WorkerCollectorLost(telemetry.WorkerLifecycleLoss)
		return
	}
	if len(events) > workerTerminalLimit {
		o.metrics.WorkerCollectorLost(telemetry.WorkerLifecycleOverflow)
		events = events[:workerTerminalLimit]
	}
	for _, e := range events {
		key := string(e.Event) + "/" + e.Scope.WorkerRunID + "/" + e.Scope.AgentTurnID
		if _, ok := o.seen[key]; ok {
			continue
		}
		if len(o.seen) >= workerTerminalLimit {
			o.metrics.WorkerCollectorLost(telemetry.WorkerLifecycleOverflow)
			break
		}
		o.seen[key] = e.TS
		o.emit(e)
	}
}
func (o *workerTerminalObserver) read(ctx context.Context, cutoff, through time.Time) ([]telemetry.Event, error) {
	if o.pool == nil || o.tenant == "" || o.user == "" {
		return nil, errTelemetrySample
	}
	tx, err := o.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, errTelemetrySample
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH terminals AS (
SELECT 'run_terminal' AS kind,s.tenant_id,s.workspace_id,s.worker_session_id,r.worker_run_id,'' AS agent_turn_id,r.status,r.started_at,r.terminal_at
FROM worker_runs r JOIN worker_sessions s USING(worker_session_id)
WHERE s.tenant_id=$1 AND s.user_id=$2 AND r.terminal_at >= $3
UNION ALL
SELECT 'turn_terminal',s.tenant_id,s.workspace_id,s.worker_session_id,r.worker_run_id,t.agent_turn_id,t.status,t.started_at,t.terminal_at
FROM agent_turns t JOIN worker_runs r ON r.worker_run_id=t.worker_run_id AND r.worker_session_id=t.worker_session_id JOIN worker_sessions s ON s.worker_session_id=r.worker_session_id
WHERE s.tenant_id=$1 AND s.user_id=$2 AND t.terminal_at >= $3
)
SELECT kind,tenant_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,status,terminal_at,
CASE WHEN started_at IS NOT NULL AND terminal_at>=started_at THEN (extract(epoch FROM terminal_at-started_at)*1000)::bigint END
FROM terminals WHERE terminal_at <= $4 ORDER BY terminal_at DESC,kind,worker_run_id,agent_turn_id LIMIT 1025`, o.tenant, o.user, cutoff, through)
	if err != nil {
		return nil, errTelemetrySample
	}
	var events []telemetry.Event
	for rows.Next() {
		e := telemetry.Event{Level: telemetry.LevelInfo, Service: telemetry.Service, SchemaVersion: telemetry.SchemaVersion, Component: telemetry.ComponentWorker}
		var status string
		if rows.Scan(&e.Event, &e.Scope.TenantID, &e.Scope.WorkspaceID, &e.Scope.WorkerSessionID, &e.Scope.WorkerRunID, &e.Scope.AgentTurnID, &status, &e.TS, &e.DurationMillis) != nil {
			rows.Close()
			return nil, errTelemetrySample
		}
		switch status {
		case "completed":
			e.Code = telemetry.CodeOK
		case "cancelled":
			e.Code = telemetry.CodeCancelled
		case "failed":
			e.Code = telemetry.CodeFailed
		case "timed_out":
			e.Code = telemetry.CodeTimedOut
		default:
			e.Code = telemetry.CodeUnknown
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || tx.Commit(ctx) != nil {
		return nil, errTelemetrySample
	}
	return events, nil
}
