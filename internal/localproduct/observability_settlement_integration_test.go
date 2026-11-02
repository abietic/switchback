//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const observationFixtureHeartbeatInterval = 15 * time.Second

// The only fake boundaries are Runtime/Agent I/O and the remote exporter. The
// settlement coordinator, queued-turn service, source finalizer, PG jobs and
// checksum-verifying loader all remain the production implementations.
func TestBlockedDurableProjectionDoesNotDelayTerminalSettlementOrNextTurn(t *testing.T) {
	pool := postgrestest.Open(t)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, sourceRoot := t.TempDir(), t.TempDir()
	credential := filepath.Join(t.TempDir(), "provider-key")
	if err := os.WriteFile(credential, []byte("fixture-provider-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	address := reserveRuntimeRunnerAddress(t)
	app, err := newWithPool(context.Background(), Config{
		Profile: profile, NetworkProfile: integrationNetworkProfile(t),
		DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"), DataRoot: dataRoot, EngineDataRoot: dataRoot,
		WorkspaceSourceRoot: sourceRoot, ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID,
		WorkspaceRevisionID: defaultWorkspaceRevisionID,
		WorkerImage:         "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkerNetwork:       "switchback-observability-fixture", GRPCAddress: address, BackendGRPCTarget: address,
		BackendHTTPBaseURL: "http://127.0.0.1:8080", DeepSeekCredentialFile: credential, ExecutionEnabled: true,
	}, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.runtime.environment.storage.Ownership = localstorage.OwnershipFunc(func(string, int, int) error { return nil })
	app.runtime.environment.secrets.Ownership = runtimesecret.OwnershipFunc(func(string, int, int) error { return nil })
	app.runtime.environment.secrets.OwnerUID = func(string) (uint32, error) { return 0, nil }
	app.runtime.environment.secrets.OpenFileOwnerUID = func(*os.File) (uint32, error) { return 0, nil }
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &observationFixtureRuntime{ctx: ctx, runner: app.runtime, address: address, starts: make(chan observationFixtureTurn, 1), failures: make(chan error, 1)}
	runtime.installInspectionFixture(t)
	app.runtime.starts.CreatedLayerChecker = syntheticInitialCreatedLayerChecker{}
	app.runtime.starts.Provisioner = runtime
	allowSyntheticRuntimeStarts(t, app)
	stopped := make(chan error, 1)
	go func() { stopped <- app.runtime.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-stopped:
			if err != nil {
				t.Errorf("runtime shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runtime did not stop")
		}
	})
	jobs := artifactpostgres.NewProjectionJobStore(pool)
	if err := jobs.Activate(ctx, artifactapp.DurableProjectionSchemaVersion); err != nil {
		t.Fatal(err)
	}
	taskID := createRuntimeRunnerTask(t, app.Handler(), pool, "observability-two-turn", "Produce a deterministic report")
	current := runtime.next(t)
	queue := func(turn observationFixtureTurn) string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var applied bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='event' AND envelope->>'message_id'=$2 AND effect_status='applied')`, string(turn.start.Envelope.RuntimeInstanceID), "model-"+string(turn.start.Envelope.AgentTurnID)).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Runtime active/model event did not apply")
			}
			time.Sleep(time.Millisecond)
		}
		var sessionRevision, runRevision, turnRevision uint64
		if err := pool.QueryRow(ctx, `SELECT session.session_revision,run.revision,turn.revision
FROM agent_turns turn JOIN worker_runs run USING(worker_run_id)
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
WHERE turn.agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)).Scan(&sessionRevision, &runRevision, &turnRevision); err != nil {
			t.Fatal(err)
		}
		accepted, err := (workerapp.WorkerUserCommandService{Store: workerpostgres.NewWorkerUserCommandStore(pool), IDs: randomIDs{}, Now: time.Now}).Execute(ctx, workerapp.SubmitWorkerUserCommand{
			TenantID: profile.TenantID, UserID: profile.ActorUserID,
			WorkerSessionID: string(turn.start.Envelope.WorkerSessionID), WorkerRunID: string(turn.start.Envelope.WorkerRunID),
			AgentTurnID: string(turn.start.Envelope.AgentTurnID), CommandType: "queue", Content: "Continue with another deterministic report",
			ExpectedSessionRevision: sessionRevision, ExpectedRunRevision: runRevision, ExpectedTurnRevision: turnRevision,
			IdempotencyKey: "queue-" + string(turn.start.Envelope.AgentTurnID),
		})
		if err != nil {
			t.Fatal(err)
		}
		return accepted.CommandID
	}
	settled := func(turn observationFixtureTurn) time.Time {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var completed *time.Time
			err := pool.QueryRow(ctx, `SELECT completed_at FROM worker_turn_settlements WHERE agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)).Scan(&completed)
			if err == nil && completed != nil {
				return *completed
			}
			select {
			case err := <-runtime.failures:
				t.Fatal(err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("settlement failed to complete for %s: %v", turn.start.Envelope.AgentTurnID, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// Finish and explicitly accept the bootstrap Task. Samples then exercise
	// standalone Runs through the same resident Session's actual queued path.
	// Task-bound queue currently conflicts with delivered Task settlement; this
	// fixture must neither alter that state nor misrepresent it as covered.
	current.finish(t)
	settled(current)
	post := func(path string, body map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	var taskRevision uint64
	if err := pool.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id=$1`, taskID).Scan(&taskRevision); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/tasks/"+taskID+"/accept", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "fixture-accept-bootstrap", "expected_task_revision": taskRevision, "message": "Accept bootstrap report"})
	var sessionRevision, runRevision, turnRevision uint64
	if err := pool.QueryRow(ctx, `SELECT session.session_revision,run.revision,turn.revision FROM agent_turns turn JOIN worker_runs run USING(worker_run_id) JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id WHERE turn.agent_turn_id=$1`, string(current.start.Envelope.AgentTurnID)).Scan(&sessionRevision, &runRevision, &turnRevision); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/worker-sessions/"+string(current.start.Envelope.WorkerSessionID)+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "fixture-standalone-followup", "expected_session_revision": sessionRevision, "worker_run_id": current.start.Envelope.WorkerRunID, "expected_run_revision": runRevision, "agent_turn_id": current.start.Envelope.AgentTurnID, "expected_turn_revision": turnRevision, "content": "Produce standalone report"})
	bootstrapProjection := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
	if err := (&artifactapp.ProjectionWorker{Jobs: jobs, Loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: app.runtime.objects}, Projector: bootstrapProjection}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bootstrapProjection.entered:
	default:
		t.Fatal("bootstrap source did not export")
	}
	current = runtime.next(t)
	var healthySettlement, outageSettlement, healthyDispatch, outageDispatch []time.Duration
	for pair := 0; pair < 20; pair++ {
		for _, outage := range []bool{false, true} {
			// A separately identified warm-up source establishes exporter entry
			// before the measured terminal; no scheduler timing assumption.
			warmup := current
			queue(warmup)
			warmup.finish(t)
			current = runtime.next(t)
			settled(warmup)
			gate := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
			if outage {
				gate.release = make(chan struct{})
			}
			probe := &warmupProjectionProbe{ProjectionJobStore: jobs}
			worker := &artifactapp.ProjectionWorker{Jobs: probe, Loader: warmupProjectionLoaderProbe{loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: app.runtime.objects}, probe: probe}, Projector: gate}
			projectionDone := make(chan error, 1)
			go func() { projectionDone <- worker.Tick(ctx) }()
			warmSource, err := waitWarmupProjection(gate.entered, projectionDone, probe)
			if err != nil {
				t.Fatalf("pair=%d outage=%t warmup=%s: %v", pair, outage, warmup.start.Envelope.AgentTurnID, err)
			}
			if warmSource.Turn.Identity.AgentTurnID != warmup.start.Envelope.AgentTurnID {
				t.Fatalf("wrong warmup source: %+v", warmSource.Source)
			}
			if !outage {
				if err := <-projectionDone; err != nil {
					t.Fatal(err)
				}
			}
			measured := current
			queueID := queue(measured)
			terminalAt := measured.finish(t)
			current = runtime.next(t)
			completedAt := settled(measured)
			settlementLatency, dispatchLatency := completedAt.Sub(terminalAt), current.receivedAt.Sub(terminalAt)
			if settlementLatency < 0 || dispatchLatency < 0 || settlementLatency > 5*time.Second || dispatchLatency > 5*time.Second {
				t.Fatalf("latency out of bounds: %s %s", settlementLatency, dispatchLatency)
			}
			var nextRun, nextTurn, queueStatus string
			if err := pool.QueryRow(ctx, `SELECT status,outcome->>'worker_run_id',outcome->>'agent_turn_id' FROM worker_user_commands WHERE command_id=$1`, queueID).Scan(&queueStatus, &nextRun, &nextTurn); err != nil {
				t.Fatal(err)
			}
			if queueStatus != "applied" || nextRun != string(current.start.Envelope.WorkerRunID) || nextTurn != string(current.start.Envelope.AgentTurnID) || measured.start.Envelope.RuntimeGeneration != current.start.Envelope.RuntimeGeneration {
				t.Fatalf("next dispatch not exact queued continuation: %s %s %s", queueStatus, nextRun, nextTurn)
			}
			if outage {
				select {
				case err := <-projectionDone:
					t.Fatalf("exporter stopped blocking before settlement/dispatch: %v", err)
				default:
				}
				var status string
				if err := pool.QueryRow(ctx, `SELECT status FROM observability_projection_jobs WHERE trace_stream_id=$1`, warmSource.Source.TraceStreamID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "running" {
					t.Fatalf("outage job status=%s", status)
				}
				close(gate.release)
				if err := <-projectionDone; err != nil {
					t.Fatal(err)
				}
				outageSettlement = append(outageSettlement, settlementLatency)
				outageDispatch = append(outageDispatch, dispatchLatency)
			} else {
				healthySettlement = append(healthySettlement, settlementLatency)
				healthyDispatch = append(healthyDispatch, dispatchLatency)
			}
			// Restart a real worker/loader after release. Publication alone must
			// discover and export the measured source, without runtime enqueue.
			recovered := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
			restarted := &artifactapp.ProjectionWorker{Jobs: jobs, Loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: app.runtime.objects}, Projector: recovered}
			if err := restarted.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case source := <-recovered.entered:
				if source.Turn.Identity.AgentTurnID != measured.start.Envelope.AgentTurnID || len(source.Turn.ModelObservations) != 1 {
					t.Fatalf("recovered wrong source: %+v", source.Turn)
				}
				var modelID, sourceDigest string
				var sourceSize int64
				if err := pool.QueryRow(ctx, `SELECT model.envelope->'payload'->>'ObservationID',settlement.phase_state->'evidence_input'->>'observation_source_digest',(settlement.phase_state->'evidence_input'->>'observation_source_size')::bigint
FROM worker_turn_settlements settlement JOIN runtime_control_messages terminal ON terminal.runtime_instance_id=settlement.runtime_instance_id AND terminal.direction='event' AND terminal.envelope->>'agent_turn_id'=settlement.agent_turn_id AND terminal.envelope->>'message_type'='agent_turn.terminal'
JOIN runtime_control_messages model ON model.runtime_instance_id=terminal.runtime_instance_id AND model.direction='event' AND model.effect_status='applied' AND model.sequence<=terminal.sequence AND model.envelope->>'message_type'='model.observation' AND model.envelope->>'worker_session_id'=terminal.envelope->>'worker_session_id' AND model.envelope->>'worker_run_id'=terminal.envelope->>'worker_run_id' AND model.envelope->>'agent_session_id'=terminal.envelope->>'agent_session_id' AND model.envelope->>'agent_turn_id'=terminal.envelope->>'agent_turn_id' AND model.envelope->>'runtime_generation'=terminal.envelope->>'runtime_generation'
WHERE settlement.agent_turn_id=$1`, string(measured.start.Envelope.AgentTurnID)).Scan(&modelID, &sourceDigest, &sourceSize); err != nil {
					t.Fatal(err)
				}
				encoded, err := artifactapp.EncodeTurnObservationSource(source.Turn)
				if err != nil || modelID != "model-"+string(measured.start.Envelope.AgentTurnID) || source.Turn.ModelObservations[0].ObservationID != modelID || sourceDigest != runtimeSettlementBytesDigest(encoded) || sourceSize != int64(len(encoded)) {
					t.Fatal("published source differs from frozen checkpoint/applied journal cutoff")
				}
				var status, checksum string
				if err := pool.QueryRow(ctx, `SELECT status,manifest_checksum FROM observability_projection_jobs WHERE trace_stream_id=$1`, source.Source.TraceStreamID).Scan(&status, &checksum); err != nil {
					t.Fatal(err)
				}
				if status != "passed" || checksum != source.Source.ManifestChecksum {
					t.Fatalf("recovered source proof=%s %s", status, checksum)
				}
				t.Logf("RAW pair=%d outage=%t session=%s generation=%d turn=%s next_turn=%s settlement_ms=%.3f dispatch_ms=%.3f source=%s checksum=%s", pair, outage, measured.start.Envelope.WorkerSessionID, measured.start.Envelope.RuntimeGeneration, measured.start.Envelope.AgentTurnID, current.start.Envelope.AgentTurnID, float64(settlementLatency)/float64(time.Millisecond), float64(dispatchLatency)/float64(time.Millisecond), source.Source.TraceStreamID, checksum)
			default:
				t.Fatal("restarted worker did not export published measured source")
			}
		}
	}
	current.finish(t)
	settled(current)
	var heartbeatSequence, heartbeatControlSequence uint64
	var heartbeatStatus string
	heartbeatDeadline := time.Now().Add(3 * time.Second)
	for {
		err := pool.QueryRow(ctx, `SELECT observation.heartbeat_sequence,observation.last_control_sequence,watchdog.status
FROM runtime_heartbeat_observations observation
JOIN runtime_first_heartbeat_watchdogs watchdog USING(runtime_instance_id)
WHERE observation.runtime_instance_id=$1`, string(current.start.Envelope.RuntimeInstanceID)).Scan(&heartbeatSequence, &heartbeatControlSequence, &heartbeatStatus)
		if err == nil {
			break
		}
		if time.Now().After(heartbeatDeadline) {
			t.Fatalf("default-cadence fixture heartbeat was not persisted: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if heartbeatSequence == 0 || heartbeatControlSequence == 0 || heartbeatStatus != "satisfied" {
		t.Fatalf("default-cadence fixture heartbeat proof is invalid: sequence=%d control=%d watchdog=%s", heartbeatSequence, heartbeatControlSequence, heartbeatStatus)
	}
	p95 := func(samples []time.Duration) time.Duration {
		sorted := append([]time.Duration(nil), samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		return sorted[(95*len(sorted)+99)/100-1]
	}
	t.Logf("P95 settlement healthy=%s outage=%s dispatch healthy=%s outage=%s", p95(healthySettlement), p95(outageSettlement), p95(healthyDispatch), p95(outageDispatch))
	if p95(outageSettlement)-p95(healthySettlement) > time.Second || p95(outageDispatch)-p95(healthyDispatch) > time.Second {
		t.Fatal("outage p95 delta exceeds 1s")
	}
}

type warmupProjectionProbe struct {
	artifactapp.ProjectionJobStore
	mu            sync.Mutex
	stage         string
	started       time.Time
	discovered    int
	claimed       bool
	job           artifactapp.ProjectionJob
	loadCode      string
	loadPermanent bool
	completion    artifactapp.ProjectionCompletion
}

func (p *warmupProjectionProbe) begin(stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage, p.started = stage, time.Now()
}

func (p *warmupProjectionProbe) summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("stage=%s elapsed=%s discovered=%d claimed=%t source=%s checksum=%s token=%d attempts=%d loader_code=%s permanent=%t status=%s finish_code=%s", p.stage, time.Since(p.started), p.discovered, p.claimed, p.job.Source.TraceStreamID, p.job.Source.ManifestChecksum, p.job.LeaseToken, p.job.Attempts, p.loadCode, p.loadPermanent, p.completion.Status, p.completion.ErrorCode)
}

func (p *warmupProjectionProbe) Activate(ctx context.Context, version string) error {
	p.begin("activate")
	return p.ProjectionJobStore.Activate(ctx, version)
}

func (p *warmupProjectionProbe) Discover(ctx context.Context, version string, limit int) (int, error) {
	p.begin("discover")
	n, err := p.ProjectionJobStore.Discover(ctx, version, limit)
	p.mu.Lock()
	p.discovered = n
	p.mu.Unlock()
	return n, err
}

func (p *warmupProjectionProbe) Claim(ctx context.Context, version string, lease time.Duration) (artifactapp.ProjectionJob, bool, error) {
	p.begin("claim")
	job, ok, err := p.ProjectionJobStore.Claim(ctx, version, lease)
	p.mu.Lock()
	p.job, p.claimed = job, ok
	p.mu.Unlock()
	return job, ok, err
}

func (p *warmupProjectionProbe) Finish(ctx context.Context, job artifactapp.ProjectionJob, completion artifactapp.ProjectionCompletion) error {
	p.begin("finish")
	p.mu.Lock()
	p.completion = completion
	p.mu.Unlock()
	err := p.ProjectionJobStore.Finish(ctx, job, completion)
	return err
}

type warmupProjectionLoaderProbe struct {
	loader artifactapp.ProjectionSourceLoader
	probe  *warmupProjectionProbe
}

func (p warmupProjectionLoaderProbe) Load(ctx context.Context, job artifactapp.ProjectionJob) (artifactapp.SourceProjection, error) {
	p.probe.begin("load")
	source, err := p.loader.Load(ctx, job)
	var typed *artifactapp.ProjectionError
	if errors.As(err, &typed) {
		p.probe.mu.Lock()
		p.probe.loadCode, p.probe.loadPermanent = typed.Code, typed.Permanent
		p.probe.mu.Unlock()
	}
	if err == nil {
		p.probe.begin("export")
	}
	return source, err
}

var errWarmupProjectionFinished = errors.New("warmup projection finished before exporter entry")
var errWarmupProjectionTimeout = errors.New("warmup never reached exporter: Tick still running")

func waitWarmupProjection(entered <-chan artifactapp.SourceProjection, done chan error, probe *warmupProjectionProbe) (artifactapp.SourceProjection, error) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	// A healthy exporter can finish before this wait begins. Entry is the
	// proof; Tick nil alone also means no work or a handled loader failure.
	finished := func(tickErr error) (artifactapp.SourceProjection, error) {
		select {
		case source := <-entered:
			// Keep the original completion/error for the assertions after entry.
			// The fixture's one-shot Tick uses a capacity-one done channel.
			done <- tickErr
			return source, nil
		default:
			return artifactapp.SourceProjection{}, fmt.Errorf("%w: %s tick_error=%v", errWarmupProjectionFinished, probe.summary(), tickErr)
		}
	}
	select {
	case source := <-entered:
		return source, nil
	case tickErr := <-done:
		return finished(tickErr)
	case <-timer.C:
		// Resolve results ready at the deadline before declaring a live Tick.
		select {
		case source := <-entered:
			return source, nil
		default:
		}
		select {
		case tickErr := <-done:
			return finished(tickErr)
		default:
		}
		return artifactapp.SourceProjection{}, fmt.Errorf("%w: %s", errWarmupProjectionTimeout, probe.summary())
	}
}

// This controlled no-work case reproduces the lost diagnostic branch, not the
// unknown cause of the historical intermittent warmup failure.
func TestWarmupProjectionReportsCompletedTickWithoutExporter(t *testing.T) {
	pool := postgrestest.Open(t)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jobs := artifactpostgres.NewProjectionJobStore(pool)
	probe := &warmupProjectionProbe{ProjectionJobStore: jobs}
	gate := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
	done := make(chan error, 1)
	worker := &artifactapp.ProjectionWorker{Jobs: probe, Loader: warmupProjectionLoaderProbe{loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: objects}, probe: probe}, Projector: gate}
	// Actual Tick has returned nil with no eligible job; nil is not export proof.
	done <- worker.Tick(context.Background())
	at := time.Now()
	_, err = waitWarmupProjection(gate.entered, done, probe)
	if !errors.Is(err, errWarmupProjectionFinished) || !strings.Contains(err.Error(), "claimed=false") {
		t.Fatalf("completed no-work Tick was classified as %v", err)
	}
	if elapsed := time.Since(at); elapsed > time.Second {
		t.Fatalf("completed no-work Tick waited %s", elapsed)
	}
}

func TestWarmupProjectionReportsHandledLoaderFailure(t *testing.T) {
	f := newSupplementFixture(t, nil)
	// The fixture's real scanner sees this credential in actual prompt input.
	// No sealed source, job, marker or execution result is changed by this test.
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "warmup-safe-marker", "Inspect fixture-provider-credential")
	turn, callback := f.next()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	jobs := artifactpostgres.NewProjectionJobStore(f.pool)
	probe := &warmupProjectionProbe{ProjectionJobStore: jobs}
	gate := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
	done := make(chan error, 1)
	worker := &artifactapp.ProjectionWorker{Jobs: probe, Loader: warmupProjectionLoaderProbe{loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: f.app.runtime.objects}, probe: probe}, Projector: gate}
	done <- worker.Tick(f.ctx)
	metadata, err := jobs.LoadProjectionSourceMetadata(f.ctx, probe.job.Source)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := artifactapp.ReadTurnObservationSource(f.ctx, f.app.runtime.objects, metadata.Manifest)
	if err != nil || sealed.ErrorCode != "credential_detected" {
		t.Fatalf("actual input did not produce expected safe marker: code=%s err=%v", sealed.ErrorCode, err)
	}
	_, err = waitWarmupProjection(gate.entered, done, probe)
	if !errors.Is(err, errWarmupProjectionFinished) || !strings.Contains(err.Error(), "loader_code=source_incomplete") || !strings.Contains(err.Error(), "status=blocked") || !strings.Contains(err.Error(), "source=trace-"+string(turn.start.Envelope.AgentTurnID)) {
		t.Fatalf("handled loader failure was classified as %v", err)
	}
}

func TestWarmupProjectionPreservesEnteredSourceAndTickCompletion(t *testing.T) {
	f := newSupplementFixture(t, nil)
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "warmup-completed-export", "Produce a deterministic report")
	turn, callback := f.next()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	jobs := artifactpostgres.NewProjectionJobStore(f.pool)
	probe := &warmupProjectionProbe{ProjectionJobStore: jobs}
	gate := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 1)}
	done := make(chan error, 1)
	worker := &artifactapp.ProjectionWorker{Jobs: probe, Loader: warmupProjectionLoaderProbe{loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: f.app.runtime.objects}, probe: probe}, Projector: gate}
	done <- worker.Tick(f.ctx)
	// Both channels are already ready from the actual worker/loader/exporter.
	// Re-enter this wait with the same real source to cover both select orders;
	// this is channel-order verification, not another export/publication claim.
	for range 100 {
		source, err := waitWarmupProjection(gate.entered, done, probe)
		if err != nil || source.Turn.Identity.AgentTurnID != turn.start.Envelope.AgentTurnID || source.Source != probe.job.Source {
			t.Fatalf("completed real export lost its exact source: source=%+v err=%v", source.Source, err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			done <- err
		default:
			t.Fatal("entry wait consumed the original Tick completion")
		}
		if err := gate.ExportAndVerify(f.ctx, source); err != nil {
			t.Fatal(err)
		}
	}
}

type observationFixtureProjector struct {
	entered chan artifactapp.SourceProjection
	release chan struct{}
}

func (p *observationFixtureProjector) ExportAndVerify(ctx context.Context, source artifactapp.SourceProjection) error {
	p.entered <- source
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type observationFixtureTurn struct {
	start      workerapp.WorkerControlCommand
	client     *controlgrpc.Client
	ctx        context.Context
	receivedAt time.Time
}

func (turn observationFixtureTurn) finish(t *testing.T) time.Time {
	t.Helper()
	e := turn.start.Envelope
	e.Sequence = 0
	e.MessageID = "terminal-" + string(e.AgentTurnID)
	e.IdempotencyKey = e.MessageID
	e.MessageType = "agent_turn.terminal"
	e.Payload = json.RawMessage(`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`)
	at := time.Now()
	if err := turn.client.PublishTurnUpdate(turn.ctx, workerapp.WorkerControlEvent{Envelope: e}, true); err != nil {
		t.Fatal(err)
	}
	return at
}

type observationFixtureRuntime struct {
	readyTransform func(*runtimeReadyPayload)
	ctx            context.Context
	runner         *runtimeRunner
	address        string
	starts         chan observationFixtureTurn
	failures       chan error
	// Tests may tighten the cadence to exercise the real authenticated path;
	// zero retains the production 15-second Runtime heartbeat contract.
	heartbeatInterval time.Duration
	// A manual test clock preserves the ticker when nil; closing it stops the pump.
	heartbeatTicks <-chan time.Time
	// A test-only start gate may defer an accelerated cadence until durable
	// Runtime startup has dispatched a real command. Nil starts the default pump
	// with the Runtime lifecycle, including when no AgentTurn exists.
	heartbeatStart <-chan struct{}
	// Optional test-owned callback scheduling; production control I/O stays real.
	onModel func(context.Context, workerapp.WorkerControlCommand, *controlgrpc.Client, workerapp.WorkerControlEvent) error
	// Selected synthetic ACP response, persisted before terminal into the real raw spool.
	rawResponse func(contractsv1.RuntimeControlEnvelope) string
	// Opt-in test lifecycle ownership; the historical fixture default is unchanged.
	joinClient bool
	inventory  *observationFixtureDocker
}

// All Runtime I/O shares this inventory; PostgreSQL and control authority stay real.
type observationFixtureDocker struct {
	recoveryAbsentDocker
	mu              sync.Mutex
	requests        map[string]workerapp.RuntimeProvisionRequest
	exited          map[string]bool
	inspectionError error
	inspectCalls    int
	removes         []string
}

func (r *observationFixtureRuntime) installInspectionFixture(t *testing.T) {
	t.Helper()
	r.inventory = &observationFixtureDocker{requests: make(map[string]workerapp.RuntimeProvisionRequest), exited: make(map[string]bool)}
	p, err := dockeradapter.NewProvisioner(r.inventory, dockeradapter.Options{Image: r.runner.configuration.WorkerImage, Network: r.runner.configuration.WorkerNetwork, EngineDataRoot: r.runner.configuration.EngineDataRoot, BackendGRPCTarget: r.address, BackendHTTPBaseURL: r.runner.configuration.BackendHTTPBaseURL, HostPathVerifier: r.inventory})
	if err != nil {
		t.Fatal(err)
	}
	r.runner.provisioner = p
	r.runner.cleanup.Cleaner = p
	r.runner.recovery.Provisioner = r
}

func (e *observationFixtureDocker) ContainerInspect(_ context.Context, target string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inspectCalls++
	if e.inspectionError != nil {
		return client.ContainerInspectResult{}, e.inspectionError
	}
	for id, request := range e.requests {
		if target != "switchback-runtime-"+id && target != "fixture-"+id {
			continue
		}
		return client.ContainerInspectResult{Container: container.InspectResponse{ID: "fixture-" + id, Config: &container.Config{Labels: map[string]string{"com.switchback.managed": "worker-runtime", "com.switchback.runtime-instance-id": id, "com.switchback.runtime-generation": strconv.FormatUint(request.Generation, 10)}}, State: &container.State{Running: !e.exited[id], ExitCode: 1}}}, nil
	}
	return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
}

func (e *observationFixtureDocker) ContainerRemove(_ context.Context, target string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.requests {
		if target == "fixture-"+id {
			delete(e.requests, id)
			delete(e.exited, id)
			e.removes = append(e.removes, target)
			return client.ContainerRemoveResult{}, nil
		}
	}
	return client.ContainerRemoveResult{}, cerrdefs.ErrNotFound
}
func (*observationFixtureDocker) ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return client.ContainerCreateResult{}, errors.New("unexpected Docker adapter provisioning; fixture Runtime owns creation")
}
func (*observationFixtureDocker) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, errors.New("unexpected Docker adapter start")
}
func (*observationFixtureDocker) ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult {
	panic("unexpected Docker adapter wait; fixture Runtime owns client lifetime")
}
func (*observationFixtureDocker) VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	return client.VolumeCreateResult{}, errors.New("unexpected Docker adapter volume creation")
}
func (*observationFixtureDocker) VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	return client.VolumeRemoveResult{}, cerrdefs.ErrNotFound
}

func TestObservationFixtureRuntimeInspectionMatchesProvisionedIdentity(t *testing.T) {
	f := newSupplementFixture(t, nil)
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "fixture-inspection-identity", "Keep a healthy resident Runtime")
	turn := f.runtime.next(t)
	inspection, err := f.app.runtime.provisioner.Inspect(f.ctx, string(turn.start.Envelope.RuntimeInstanceID))
	if err != nil || !inspection.Exists || !inspection.Running || inspection.ContainerID != "fixture-"+string(turn.start.Envelope.RuntimeInstanceID) || inspection.Generation != uint64(turn.start.Envelope.RuntimeGeneration) || inspection.ObservedAt.IsZero() {
		t.Fatalf("provisioned fixture is not observed healthy with exact identity: inspection=%+v err=%v", inspection, err)
	}
}

func TestObservationFixtureDockerInspectionAndCleanup(t *testing.T) {
	ctx := context.Background()
	e := &observationFixtureDocker{requests: map[string]workerapp.RuntimeProvisionRequest{
		"runtime-fixture-one": {RuntimeInstanceID: "runtime-fixture-one", Generation: 3},
		"runtime-fixture-two": {RuntimeInstanceID: "runtime-fixture-two", Generation: 7},
	}, exited: map[string]bool{"runtime-fixture-two": true}}
	p, err := dockeradapter.NewProvisioner(e, dockeradapter.Options{Image: "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Network: "fixture-network", EngineDataRoot: t.TempDir(), BackendGRPCTarget: "fixture-backend:9090", BackendHTTPBaseURL: "http://fixture-backend:8080", HostPathVerifier: e})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := p.Inspect(ctx, "runtime-fixture-one")
	if err != nil || !healthy.Exists || !healthy.Running || healthy.ContainerID != "fixture-runtime-fixture-one" || healthy.Generation != 3 || healthy.ExitCode != nil || healthy.ObservedAt.IsZero() {
		t.Fatalf("healthy inspection=%+v err=%v", healthy, err)
	}
	exited, err := p.Inspect(ctx, "runtime-fixture-two")
	if err != nil || !exited.Exists || exited.Running || exited.Generation != 7 || exited.ExitCode == nil || *exited.ExitCode != 1 {
		t.Fatalf("exit inspection=%+v err=%v", exited, err)
	}
	missing, err := p.Inspect(ctx, "runtime-fixture-missing")
	if err != nil || missing.Exists || missing.Running || missing.ObservedAt.IsZero() {
		t.Fatalf("missing inspection=%+v err=%v", missing, err)
	}
	e.mu.Lock()
	e.inspectionError = errors.New("controlled fixture inspection failure")
	e.mu.Unlock()
	if _, err := p.Inspect(ctx, "runtime-fixture-one"); err == nil {
		t.Fatal("inspection error was swallowed")
	}
	e.mu.Lock()
	e.inspectionError = nil
	e.mu.Unlock()
	for _, request := range []workerapp.RuntimeCleanupRequest{
		{RuntimeInstanceID: "runtime-fixture-one", Generation: 4, ExpectedContainerID: "fixture-runtime-fixture-one"},
		{RuntimeInstanceID: "runtime-fixture-one", Generation: 3, ExpectedContainerID: "fixture-runtime-other"},
	} {
		if observation, err := p.Cleanup(ctx, request); err == nil || observation.Confirmed {
			t.Fatalf("conflicting cleanup was confirmed: %+v err=%v", observation, err)
		}
	}
	observation, err := p.Cleanup(ctx, workerapp.RuntimeCleanupRequest{RuntimeInstanceID: "runtime-fixture-one", Generation: 3, ExpectedContainerID: "fixture-runtime-fixture-one"})
	if err != nil || !observation.Confirmed || observation.ContainerExists || observation.SecretVolumeExists {
		t.Fatalf("exact cleanup=%+v err=%v", observation, err)
	}
	missing, err = p.Inspect(ctx, "runtime-fixture-one")
	if err != nil || missing.Exists {
		t.Fatalf("removed runtime still exists: %+v err=%v", missing, err)
	}
	exited, err = p.Inspect(ctx, "runtime-fixture-two")
	if err != nil || !exited.Exists || exited.Generation != 7 {
		t.Fatalf("cleanup touched another runtime: %+v err=%v", exited, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.removes) != 1 || e.removes[0] != "fixture-runtime-fixture-one" {
		t.Fatalf("cleanup targets=%v", e.removes)
	}
}

func TestObservationFixtureRuntimeHeartbeatsRemainIndependentAndMonotonic(t *testing.T) {
	modelScheduled := make(chan workerapp.WorkerControlCommand, 1)
	heartbeatStart := make(chan struct{})
	fixture := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatInterval = 10 * time.Millisecond
		f.runtime.heartbeatStart = heartbeatStart
		f.runtime.onModel = func(ctx context.Context, start workerapp.WorkerControlCommand, _ *controlgrpc.Client, _ workerapp.WorkerControlEvent) error {
			select {
			case modelScheduled <- start:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(heartbeatStart)
			<-ctx.Done()
			return ctx.Err()
		}
	})
	createRuntimeRunnerTask(t, fixture.app.Handler(), fixture.pool, "fixture-heartbeat-independent", "Keep Runtime heartbeat independent")
	start := supplementReceive(t, fixture.ctx, modelScheduled)

	deadline := time.Now().Add(3 * time.Second)
	for {
		var heartbeatSequence, controlSequence uint64
		var watchdogStatus string
		var firstHeartbeatSequence, firstHeartbeatControlSequence uint64
		var applied int
		err := fixture.pool.QueryRow(fixture.ctx, `SELECT observation.heartbeat_sequence,
observation.last_control_sequence,watchdog.status,
watchdog.first_heartbeat_sequence,watchdog.first_heartbeat_control_sequence,
(SELECT count(*) FROM runtime_control_messages message
 WHERE message.runtime_instance_id=observation.runtime_instance_id
   AND message.direction='event' AND message.envelope->>'kind'='heartbeat'
   AND message.effect_status='applied')
FROM runtime_heartbeat_observations observation
JOIN runtime_first_heartbeat_watchdogs watchdog USING(runtime_instance_id)
WHERE observation.runtime_instance_id=$1`, string(start.Envelope.RuntimeInstanceID)).Scan(
			&heartbeatSequence, &controlSequence, &watchdogStatus,
			&firstHeartbeatSequence, &firstHeartbeatControlSequence, &applied,
		)
		if err == nil && heartbeatSequence >= 2 && applied >= 2 {
			if watchdogStatus != "satisfied" || firstHeartbeatSequence != 1 ||
				firstHeartbeatControlSequence == 0 || controlSequence <= firstHeartbeatControlSequence {
				t.Fatalf("heartbeat proof is not monotonic and satisfied: sequence=%d control=%d watchdog=%s first_sequence=%d first_control=%d applied=%d", heartbeatSequence, controlSequence, watchdogStatus, firstHeartbeatSequence, firstHeartbeatControlSequence, applied)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture heartbeat did not persist twice while model scheduling was blocked: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	fixture.stop()
	var stoppedSequence uint64
	if err := fixture.pool.QueryRow(context.Background(), `SELECT heartbeat_sequence FROM runtime_heartbeat_observations WHERE runtime_instance_id=$1`, string(start.Envelope.RuntimeInstanceID)).Scan(&stoppedSequence); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	var afterStopSequence uint64
	if err := fixture.pool.QueryRow(context.Background(), `SELECT heartbeat_sequence FROM runtime_heartbeat_observations WHERE runtime_instance_id=$1`, string(start.Envelope.RuntimeInstanceID)).Scan(&afterStopSequence); err != nil {
		t.Fatal(err)
	}
	if afterStopSequence != stoppedSequence {
		t.Fatalf("fixture heartbeat outlived owned cancellation/join: stopped=%d after=%d", stoppedSequence, afterStopSequence)
	}
}

func (r *observationFixtureRuntime) Inspect(ctx context.Context, id string) (workerapp.RuntimeInspection, error) {
	return r.runner.provisioner.Inspect(ctx, id)
}
func (r *observationFixtureRuntime) Provision(ctx context.Context, request workerapp.RuntimeProvisionRequest, check workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	partial := fixtureCreatedRuntime(request)
	r.inventory.mu.Lock()
	r.inventory.requests[request.RuntimeInstanceID] = request
	r.inventory.mu.Unlock()
	if err := publishSyntheticFixtureContainer(ctx, r.runner, request, partial.ContainerID); err != nil {
		return partial, err
	}
	if err := fixtureRuntimePreStart(ctx, request, partial, check); err != nil {
		return partial, err
	}
	if err := r.ctx.Err(); err != nil {
		return partial, err
	}
	if err := ctx.Err(); err != nil {
		return partial, err
	}
	go func() {
		if err := r.run(request); err != nil && r.ctx.Err() == nil {
			r.failures <- err
		}
	}()
	partial.StartedAt = time.Now()
	return partial, nil
}
func (r *observationFixtureRuntime) next(t *testing.T) observationFixtureTurn {
	t.Helper()
	select {
	case turn := <-r.starts:
		return turn
	case err := <-r.failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		rows, err := r.runner.pool.Query(context.Background(), `SELECT phase,jsonb_build_object('domain',phase_state->'domain','plan',phase_state->'continuation_plan','runtime',runtime.revision,'session',session.session_revision,'run',run.revision,'turn',turn.revision,'task',task.status) FROM worker_turn_settlements settlement JOIN runtime_instances runtime USING(runtime_instance_id) JOIN worker_sessions session ON session.worker_session_id=settlement.worker_session_id JOIN worker_runs run ON run.worker_run_id=settlement.worker_run_id JOIN agent_turns turn ON turn.agent_turn_id=settlement.agent_turn_id LEFT JOIN tasks task ON task.task_id=run.task_id`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var phase string
				var state []byte
				_ = rows.Scan(&phase, &state)
				t.Logf("stalled phase=%s revisions=%s", phase, state)
			}
		}
		t.Fatal("next Turn was not dispatched within 5s")
	}
	return observationFixtureTurn{}
}
func (r *observationFixtureRuntime) run(request workerapp.RuntimeProvisionRequest) error {
	prepared, ok := r.runner.environment.get(request.RuntimeInstanceID)
	if !ok {
		return fmt.Errorf("missing prepared runtime")
	}
	connection, err := grpc.NewClient(r.address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer connection.Close()
	client, err := controlgrpc.NewClient(controlgrpc.NewWorkerControlRPCClient(connection), prepared.Credential)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	clientExited := false
	defer func() {
		if r.joinClient {
			cancel()
			_ = connection.Close()
			if !clientExited {
				<-done
			}
		}
	}()
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready {
			return fmt.Errorf("runtime not ready")
		}
	case err := <-done:
		clientExited = true
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	readyPayload := runtimeReadyPayload{AgentSessionID: "agent-observation-fixture", AgentProfileID: request.AgentProfile.ID, AgentProfileRevision: request.AgentProfile.Revision, AgentProfileDigest: request.AgentProfile.Digest, Capabilities: workerapp.AgentCapabilities{ProtocolVersion: "1", Prompt: true, Cancel: true}}
	if r.readyTransform != nil {
		r.readyTransform(&readyPayload)
	}
	ready, _ := json.Marshal(readyPayload)
	if err := client.Publish(ctx, workerapp.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{MessageID: "ready-observation-fixture", IdempotencyKey: "ready-observation-fixture", WorkerSessionID: contractsv1.WorkerSessionID(request.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(request.WorkerRunID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(request.RuntimeInstanceID), RuntimeGeneration: uint32(request.Generation), AgentSessionID: "agent-observation-fixture", AgentTurnID: "runtime-startup", ChannelEpoch: prepared.Credential.Identity.ChannelEpoch, MessageType: "runtime.ready", Payload: ready}}); err != nil {
		return err
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatDone <- r.runHeartbeats(heartbeatCtx, request, prepared.Credential.Identity.ChannelEpoch, client)
	}()
	heartbeatExited := false
	defer func() {
		cancelHeartbeat()
		if !heartbeatExited {
			<-heartbeatDone
		}
	}()
	for {
		select {
		case start := <-client.Commands():
			at := time.Now()
			frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": string(start.Envelope.AgentTurnID), "method": "session/prompt", "params": map[string]any{"sessionId": start.Envelope.AgentSessionID, "_meta": map[string]any{"switchback": map[string]any{"agent_turn_id": start.Envelope.AgentTurnID, "runtime_id": start.Envelope.RuntimeInstanceID, "runtime_generation": start.Envelope.RuntimeGeneration, "worker_session_id": start.Envelope.WorkerSessionID}}}})
			record, _ := json.Marshal(map[string]any{"type": "acp.raw_frame", "direction": "client_to_agent", "frame": string(frame)})
			if r.rawResponse != nil {
				response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": start.Envelope.AgentSessionID,
					"_meta":     map[string]any{"switchback": map[string]any{"agent_turn_id": start.Envelope.AgentTurnID, "runtime_id": start.Envelope.RuntimeInstanceID, "runtime_generation": start.Envelope.RuntimeGeneration, "worker_session_id": start.Envelope.WorkerSessionID}},
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": r.rawResponse(start.Envelope)}},
				}})
				if err != nil {
					return err
				}
				responseRecord, err := json.Marshal(map[string]any{"type": "acp.raw_frame", "direction": "agent_to_client", "frame": string(response)})
				if err != nil {
					return err
				}
				record = append(append(record, '\n'), responseRecord...)
			}
			trace, err := os.OpenFile(filepath.Join(prepared.Storage.TraceSpool.BackendPath, "acp.raw.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := trace.Write(append(record, '\n'))
			closeErr := trace.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := client.BeginTurn(start); err != nil {
				return err
			}
			id := string(start.Envelope.AgentTurnID)
			if err := client.MarkPromptIntent(id); err != nil {
				return err
			}
			if err := client.MarkPromptActive(id); err != nil {
				return err
			}
			e := start.Envelope
			if len(start.Payload) > 0 {
				e.Payload = start.Payload
			}
			digest, err := contractsv1.RuntimeControlEnvelopeDigest(e)
			if err != nil {
				return err
			}
			active, _ := json.Marshal(struct {
				ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
			}{contractsv1.RuntimeControlActiveTurn{WorkerRunID: e.WorkerRunID, AgentSessionID: e.AgentSessionID, AgentTurnID: e.AgentTurnID, StartCommandSequence: e.Sequence, StartMessageID: e.MessageID, StartIdempotencyKey: e.IdempotencyKey, StartEnvelopeDigest: digest, Phase: contractsv1.RuntimeControlTurnEffectActive}})
			e.Sequence = 0
			e.MessageID = "active-" + id
			e.IdempotencyKey = e.MessageID
			e.MessageType = "agent_turn.effect"
			e.Payload = active
			if err := client.PublishTurnUpdate(ctx, workerapp.WorkerControlEvent{Envelope: e}, false); err != nil {
				return err
			}
			model, _ := json.Marshal(workerapp.ModelObservation{ObservationID: "model-" + id, WorkerSessionID: string(e.WorkerSessionID), WorkerRunID: string(e.WorkerRunID), AgentTurnID: id, RuntimeID: string(e.RuntimeInstanceID), Generation: uint64(e.RuntimeGeneration), Model: "fixture-model", Route: "fixture", Input: json.RawMessage(`{"text":"report"}`), Output: json.RawMessage(`{"text":"done"}`), StartedAt: at, EndedAt: time.Now(), Status: "ok"})
			e.MessageID = "model-" + id
			e.IdempotencyKey = e.MessageID
			e.MessageType = "model.observation"
			e.Payload = model
			if r.onModel != nil {
				if err := r.onModel(ctx, start, client, workerapp.WorkerControlEvent{Envelope: e}); err != nil {
					return err
				}
			} else if err := client.PublishTurnUpdate(ctx, workerapp.WorkerControlEvent{Envelope: e}, false); err != nil {
				return err
			}
			select {
			case r.starts <- observationFixtureTurn{start: start, client: client, ctx: ctx, receivedAt: at}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case err := <-done:
			clientExited = true
			return err
		case err := <-heartbeatDone:
			heartbeatExited = true
			if err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *observationFixtureRuntime) runHeartbeats(
	ctx context.Context,
	request workerapp.RuntimeProvisionRequest,
	channelEpoch uint64,
	client *controlgrpc.Client,
) error {
	interval := r.heartbeatInterval
	if interval <= 0 {
		interval = observationFixtureHeartbeatInterval
	}
	if r.heartbeatStart != nil {
		select {
		case <-r.heartbeatStart:
		case <-ctx.Done():
			return nil
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ticks := r.heartbeatTicks
	if ticks == nil {
		ticks = ticker.C
	}
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, open := <-ticks:
			if !open {
				return nil
			}
			sequence++
			payload, err := json.Marshal(struct {
				HeartbeatSequence uint64 `json:"heartbeat_sequence"`
			}{HeartbeatSequence: sequence})
			if err != nil {
				return err
			}
			messageID := fmt.Sprintf("heartbeat-observation-fixture-%d", sequence)
			event := workerapp.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{
				SchemaVersion: contractsv1.RuntimeControlSchemaVersion,
				MessageID:     messageID,
				Kind:          "heartbeat",
				WorkerSessionID: contractsv1.WorkerSessionID(
					request.WorkerSessionID,
				),
				WorkerRunID: contractsv1.WorkerRunID(request.WorkerRunID),
				RuntimeInstanceID: contractsv1.RuntimeInstanceID(
					request.RuntimeInstanceID,
				),
				RuntimeGeneration: uint32(request.Generation),
				AgentSessionID:    "agent-observation-fixture",
				AgentTurnID:       "runtime-heartbeat",
				ChannelEpoch:      channelEpoch,
				IdempotencyKey:    messageID,
				MessageType:       "runtime.heartbeat",
				Payload:           payload,
			}}
			if err := client.TryPublishHeartbeat(event); err != nil &&
				!errors.Is(err, controlgrpc.ErrBackpressure) {
				return fmt.Errorf("publish observation fixture heartbeat: %w", err)
			}
		}
	}
}
