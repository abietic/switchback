package localproduct

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/client"
)

const workerOutputReaders = 10
const workerOutputDiscovery = 5 * time.Second
const workerOutputFreshness = 7 * time.Second
const workerOutputShutdown = 2 * time.Second

var errWorkerOutputAuthority = errors.New("worker_output_authority_unavailable")

type workerOutputAuthority struct {
	scope                                      telemetry.Scope
	containerID, resourceProfile, agentProfile string
	networkID, networkDigest, networkStatus    string
	networkRevision                            uint64
}

func fmtUint(n uint64) string { return strconv.FormatUint(n, 10) }

// Discovery projects only server-owned identity columns, never content, errors,
// environment or command JSON. Runtime's creation Run is used solely to verify
// its immutable AgentProfile, never to attribute resident process output.
func discoverWorkerOutput(parent context.Context, pool *pgxpool.Pool, tenant, user string) ([]workerOutputAuthority, error) {
	ctx, cancel := context.WithTimeout(parent, telemetrySampleTimeout)
	defer cancel()
	if pool == nil || tenant == "" || user == "" {
		return nil, errWorkerOutputAuthority
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, errWorkerOutputAuthority
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.tenant_id,s.workspace_id,s.worker_session_id,r.runtime_instance_id,r.generation,r.container_id,
r.resource_profile_id,a.agent_profile_id,a.agent_profile_revision,a.agent_profile_digest,
r.network_policy_id,r.network_policy_revision,r.network_policy_digest,r.network_enforcement_status
FROM worker_sessions s JOIN runtime_instances r ON r.runtime_instance_id=s.active_runtime_instance_id AND r.worker_session_id=s.worker_session_id AND r.generation=s.runtime_generation
JOIN worker_runs a ON a.worker_run_id=r.worker_run_id AND a.worker_session_id=s.worker_session_id
WHERE s.tenant_id=$1 AND s.user_id=$2 AND s.status IN ('interactive','running','waiting_user','idle') AND s.terminal_at IS NULL
AND s.absolute_expires_at>transaction_timestamp() AND r.status='active' AND r.ended_at IS NULL AND r.container_id IS NOT NULL
ORDER BY s.worker_session_id LIMIT 11`, tenant, user)
	if err != nil {
		return nil, errWorkerOutputAuthority
	}
	var result []workerOutputAuthority
	for rows.Next() {
		var a workerOutputAuthority
		var resource, agent, revision, digest string
		if rows.Scan(&a.scope.TenantID, &a.scope.WorkspaceID, &a.scope.WorkerSessionID, &a.scope.RuntimeInstanceID, &a.scope.RuntimeGeneration, &a.containerID, &resource, &agent, &revision, &digest, &a.networkID, &a.networkRevision, &a.networkDigest, &a.networkStatus) != nil {
			rows.Close()
			return nil, errWorkerOutputAuthority
		}
		registry := config.ProductionProfileRegistry()
		rp, rok := registry.RuntimeResource(resource)
		ap, aok := registry.Agent(agent)
		_, networkErr := config.ProductionRuntimeNetworkProfiles().ResolveStored(config.RuntimeNetworkEvidence{ID: a.networkID, Revision: a.networkRevision, Digest: a.networkDigest, EnforcementStatus: a.networkStatus})
		if !rok || !aok || rp.ValidateDigest() != nil || ap.ValidateDigest() != nil || fmtUint(ap.Revision) != revision || ap.Digest != digest || networkErr != nil || a.networkStatus != config.NetworkEnforcementActive || a.containerID == "" {
			rows.Close()
			return nil, errWorkerOutputAuthority
		}
		a.resourceProfile = rp.ID + "@" + fmtUint(rp.Revision) + ":" + rp.Digest
		a.agentProfile = ap.ID + "@" + fmtUint(ap.Revision) + ":" + ap.Digest
		result = append(result, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || tx.Commit(ctx) != nil {
		return nil, errWorkerOutputAuthority
	}
	return result, nil
}

type workerOutputDocker interface {
	inspect(context.Context, workerOutputAuthority) (bool, error)
	ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error)
}
type workerOutputEngine struct{ *client.Client }

func (d workerOutputEngine) inspect(ctx context.Context, a workerOutputAuthority) (bool, error) {
	result, err := d.ContainerInspect(ctx, a.containerID, client.ContainerInspectOptions{})
	if err != nil || result.Container.Config == nil || result.Container.State == nil || !result.Container.State.Running {
		return false, errWorkerOutputAuthority
	}
	return result.Container.Config.Tty, verifyWorkerOutputLabels(a, result.Container.ID, result.Container.Config.Labels)
}
func verifyWorkerOutputLabels(a workerOutputAuthority, id string, labels map[string]string) error {
	if id == "" || id != a.containerID {
		return errWorkerOutputAuthority
	}
	for k, v := range map[string]string{"com.switchback.managed": "worker-runtime", "com.switchback.worker-session-id": a.scope.WorkerSessionID, "com.switchback.runtime-instance-id": a.scope.RuntimeInstanceID, "com.switchback.runtime-generation": fmtUint(a.scope.RuntimeGeneration), "com.switchback.resource-profile": a.resourceProfile, "com.switchback.agent-profile": a.agentProfile, "com.switchback.network-policy-id": a.networkID, "com.switchback.network-policy-revision": fmtUint(a.networkRevision), "com.switchback.network-policy-digest": a.networkDigest, "com.switchback.network-enforcement-status": a.networkStatus} {
		if v == "" || labels[k] != v {
			return errWorkerOutputAuthority
		}
	}
	return nil
}

type workerOutputCollector struct {
	discover            func(context.Context) ([]workerOutputAuthority, error)
	docker              workerOutputDocker
	emit                func(telemetry.Event)
	metrics             *telemetry.Metrics
	interval, freshness time.Duration // defaults are fixed production bounds; tests accelerate time
}
type workerOutputReader struct {
	a       workerOutputAuthority
	cancel  context.CancelFunc
	done    chan struct{}
	timer   *time.Timer
	mu      sync.Mutex
	stopped bool
}

func (r *workerOutputReader) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.stopped = true
		r.cancel()
	}
	if r.timer != nil {
		r.timer.Stop()
	}
}
func (r *workerOutputReader) renew(until time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || time.Until(until) <= 0 {
		return false
	}
	r.timer.Reset(time.Until(until))
	return true
}

func (c workerOutputCollector) run(ctx context.Context) {
	if c.interval <= 0 {
		c.interval = workerOutputDiscovery
	}
	if c.freshness <= 0 {
		c.freshness = workerOutputFreshness
	}
	readers := map[string]*workerOutputReader{}
	defer func() {
		for _, r := range readers {
			r.stop()
		}
		deadline := time.NewTimer(workerOutputShutdown)
		defer deadline.Stop()
		for _, r := range readers {
			select {
			case <-r.done:
			case <-deadline.C:
				c.metrics.WorkerCollectorLost(telemetry.WorkerShutdownLoss)
				c.metrics.WorkerCollectorState(len(readers), false)
				return
			}
		}
		c.metrics.WorkerCollectorState(0, false)
	}()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		discoveryCtx, cancel := context.WithTimeout(ctx, telemetrySampleTimeout)
		candidates, err := c.discover(discoveryCtx)
		type verified struct {
			a   workerOutputAuthority
			tty bool
		}
		valid := map[string]verified{}
		if err == nil {
			if len(candidates) > workerOutputReaders {
				c.metrics.WorkerCollectorLost(telemetry.WorkerCapacityLoss)
				candidates = candidates[:workerOutputReaders]
			}
			for _, a := range candidates {
				tty, inspectErr := c.docker.inspect(discoveryCtx, a)
				if inspectErr != nil {
					c.metrics.WorkerCollectorLost(telemetry.WorkerAuthorityLoss)
					continue
				}
				valid[a.scope.WorkerSessionID] = verified{a, tty}
			}
		} else {
			c.metrics.WorkerCollectorLost(telemetry.WorkerAuthorityLoss)
		}
		cancel()
		for id, r := range readers {
			v, ok := valid[id]
			if !ok || v.a != r.a || !r.renew(started.Add(c.freshness)) {
				r.stop()
			}
			select {
			case <-r.done:
				delete(readers, id)
			default:
			}
		}
		for id, v := range valid {
			if _, ok := readers[id]; ok {
				continue
			}
			if len(readers) >= workerOutputReaders || ctx.Err() != nil {
				break
			}
			readerCtx, stop := context.WithCancel(ctx)
			r := &workerOutputReader{a: v.a, cancel: stop, done: make(chan struct{})}
			r.mu.Lock()
			r.timer = time.AfterFunc(time.Until(started.Add(c.freshness)), func() { c.metrics.WorkerCollectorLost(telemetry.WorkerAuthorityLoss); r.stop() })
			r.mu.Unlock()
			readers[id] = r
			go func() {
				defer close(r.done)
				defer r.stop()
				stream, err := c.docker.ContainerLogs(readerCtx, r.a.containerID, client.ContainerLogsOptions{Tail: "0", Follow: true, ShowStdout: true, ShowStderr: true})
				if err != nil {
					c.metrics.WorkerCollectorLost(telemetry.WorkerStreamLoss)
					return
				}
				reason := consumeWorkerOutput(readerCtx, stream, v.tty, r.a.scope, c.emit)
				if reason == workerOutputInvalid {
					c.metrics.WorkerCollectorLost(telemetry.WorkerFrameLoss)
				} else {
					c.metrics.WorkerCollectorLost(telemetry.WorkerStreamLoss)
				}
			}()
		}
		c.metrics.WorkerCollectorState(len(readers), err == nil && len(valid) == len(candidates) && time.Since(started) < c.freshness)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
