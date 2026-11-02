//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	gatewaypostgres "github.com/abietic/switchback/internal/gateway/adapter/postgres"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/adapter/modelobs"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type supplementBarrier struct {
	reached chan struct{}
	release chan struct{}
}

func newSupplementBarrier() supplementBarrier {
	return supplementBarrier{make(chan struct{}), make(chan struct{})}
}
func (b supplementBarrier) block(ctx context.Context) error {
	close(b.reached)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Notifications happen only after the delegated database transaction commits.
// Buffered channels never hold the runtime on an assertion made by the test.
type supplementJournal struct {
	*workerpostgres.RuntimeControlJournal
	accepted     chan workerapp.RuntimeControlEventAcceptance
	applied      chan workerapp.RuntimeControlEventAcceptance
	afterReserve func(context.Context, workerapp.ReserveRuntimeControlCommandRecord, workerapp.RuntimeControlMessage) error
	afterApply   func(context.Context, workerapp.ApplyRuntimeControlEventRecord, workerapp.RuntimeControlEventAcceptance) error
	beforeApply  func(context.Context, workerapp.ApplyRuntimeControlEventRecord) error
}

func (s *supplementJournal) ReserveRuntimeControlCommand(ctx context.Context, r workerapp.ReserveRuntimeControlCommandRecord) (workerapp.RuntimeControlMessage, error) {
	v, err := s.RuntimeControlJournal.ReserveRuntimeControlCommand(ctx, r)
	if err == nil && s.afterReserve != nil {
		if err = s.afterReserve(ctx, r, v); err != nil {
			return workerapp.RuntimeControlMessage{}, err
		}
	}
	return v, err
}

func (s *supplementJournal) AcceptRuntimeControlEvent(ctx context.Context, r workerapp.AcceptRuntimeControlEventRecord) (workerapp.RuntimeControlEventAcceptance, error) {
	v, err := s.RuntimeControlJournal.AcceptRuntimeControlEvent(ctx, r)
	if err == nil {
		s.accepted <- v
	}
	return v, err
}
func (s *supplementJournal) ApplyRuntimeControlEvent(ctx context.Context, r workerapp.ApplyRuntimeControlEventRecord) (workerapp.RuntimeControlEventAcceptance, error) {
	if s.beforeApply != nil {
		if err := s.beforeApply(ctx, r); err != nil {
			return workerapp.RuntimeControlEventAcceptance{}, err
		}
	}
	v, err := s.RuntimeControlJournal.ApplyRuntimeControlEvent(ctx, r)
	if err == nil {
		s.applied <- v
		if s.afterApply != nil {
			err = s.afterApply(ctx, r, v)
		}
	}
	return v, err
}

type supplementSettlements struct {
	*workerpostgres.TurnSettlementRepository
	completed   chan workerapp.TurnSettlement
	afterFreeze func(context.Context, workerapp.TurnSettlement) error
}

func (s *supplementSettlements) AdvanceTurnSettlement(ctx context.Context, r workerapp.AdvanceTurnSettlementRecord) (workerapp.TurnSettlement, error) {
	v, err := s.TurnSettlementRepository.AdvanceTurnSettlement(ctx, r)
	if err == nil && v.Phase == workerapp.TurnSettlementCompleted {
		s.completed <- v
	}
	return v, err
}
func (s *supplementSettlements) FreezeTurnSettlementCheckpoint(ctx context.Context, r workerapp.FreezeTurnSettlementCheckpointRecord) (workerapp.TurnSettlement, error) {
	v, err := s.TurnSettlementRepository.FreezeTurnSettlementCheckpoint(ctx, r)
	if err == nil && s.afterFreeze != nil {
		err = s.afterFreeze(ctx, v)
	}
	return v, err
}

type supplementCallback struct {
	event       workerapp.WorkerControlEvent
	client      *controlgrpc.Client
	correlation modelobs.Correlation
}
type supplementFixture struct {
	manualInitial  bool
	manualReady    chan struct{}
	publishInitial func(workerapp.RuntimeProvisionRequest)

	t           *testing.T
	ctx         context.Context
	cancel      context.CancelFunc
	app         *App
	pool        *pgxpool.Pool
	runtime     *observationFixtureRuntime
	journal     *supplementJournal
	settlements *supplementSettlements
	callbacks   chan supplementCallback
	binder      *modelobs.RequestBinder
	stopped     chan error
	stopOnce    sync.Once
	bootstrap   observationFixtureTurn
	provisioner *supplementProvisioner
	startPolicy *syntheticRuntimeStartPolicy
}

// Synthetic execution is available only in test code. The atomic switch lets
// an already running fake Turn finish while the real production denial applies
// to its successor; no production constructor/config can opt into this policy.
type syntheticRuntimeStartPolicy struct {
	deny      atomic.Bool
	inventory *runtimecapacity.FileInventory
}

func (policy *syntheticRuntimeStartPolicy) checkRuntimeStart(ctx context.Context) error {
	if policy.deny.Load() {
		return (unavailableRuntimeStartPolicy{}).checkRuntimeStart(ctx)
	}
	return ctx.Err()
}

func allowSyntheticRuntimeStarts(t *testing.T, app *App) *syntheticRuntimeStartPolicy {
	t.Helper()
	inventory := runtimecapacity.NewFileInventory(t)
	c := app.configuration
	c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = inventory.TrustPath, inventory.Directory
	source, err := newSyntheticInitialCapacitySource(context.Background(), c, app.pool)
	if err != nil {
		t.Fatal(err)
	}
	app.configuration, app.initialPolicy = c, source
	if app.runtime != nil {
		app.runtime.configuration, app.runtime.initialPolicy = c, source
	}
	policy := &syntheticRuntimeStartPolicy{inventory: inventory}
	installRuntimeStartPolicyFixture(t, app, policy)
	return policy
}

func installRuntimeStartPolicyFixture(t *testing.T, app *App, policy runtimeStartPolicy) {
	t.Helper()
	c := app.configuration
	commands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{Pool: app.pool, IDs: randomIDs{}, Now: time.Now, ProjectID: c.ProjectID, Profiles: config.ProductionProfileRegistry(), NetworkProfile: c.NetworkProfile, Assistant: c.Assistant, Workspace: taskapp.WorkspaceRevisionEvidence{TenantID: c.Profile.TenantID, WorkspaceID: c.WorkspaceID, WorkspaceRevisionID: c.WorkspaceRevisionID, ResolutionID: "resolution-local", ResolutionRevision: 1, MembershipRevision: 1, Readable: true}})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{Actor: gatewayhttp.FixedActor{TenantID: c.Profile.TenantID, UserID: c.Profile.ActorUserID}, Commands: runtimeGuardedCommands{commands: commands, policy: policy}, Assistant: commands})
	if err != nil {
		t.Fatal(err)
	}
	handler := app.handler.(productHandler)
	handler.gateway = gateway
	app.handler = handler
	if app.runtime != nil {
		app.runtime.startPolicy = policy
	}
}

// The fixture owns the simulated client's lifetime independently from the real
// Backend. The stopping latch makes Add/Wait safe even if startup is in flight.
type supplementProvisioner struct {
	*observationFixtureRuntime
	mu             sync.Mutex
	stopping       bool
	wg             sync.WaitGroup
	cancel         context.CancelFunc
	publishInitial func(workerapp.RuntimeProvisionRequest)
}

func (p *supplementProvisioner) Provision(ctx context.Context, request workerapp.RuntimeProvisionRequest, check workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	partial := fixtureCreatedRuntime(request)
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return partial, context.Canceled
	}
	p.wg.Add(1)
	p.mu.Unlock()
	// Reserve ownership while the external check is in flight. A rejection owns
	// Done here; only successful startup transfers it to the simulated client.
	clientStarted := false
	defer func() {
		if !clientStarted {
			p.wg.Done()
		}
	}()
	p.inventory.mu.Lock()
	p.inventory.requests[request.RuntimeInstanceID] = request
	p.inventory.mu.Unlock()
	if p.publishInitial != nil {
		p.publishInitial(request)
	} else if err := publishSyntheticFixtureContainer(ctx, p.runner, request, partial.ContainerID); err != nil {
		return partial, err
	}
	if err := fixtureRuntimePreStart(ctx, request, partial, check); err != nil {
		return partial, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return partial, context.Canceled
	}
	if err := p.ctx.Err(); err != nil {
		return partial, err
	}
	if err := ctx.Err(); err != nil {
		return partial, err
	}
	go func() {
		defer p.wg.Done()
		if err := p.run(request); err != nil && p.ctx.Err() == nil {
			select {
			case p.failures <- err:
			case <-p.ctx.Done():
			}
		}
	}()
	clientStarted = true
	partial.StartedAt = time.Now()
	return partial, nil
}
func (p *supplementProvisioner) stop() <-chan struct{} {
	p.mu.Lock()
	p.stopping = true
	p.cancel()
	p.mu.Unlock()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	return done
}

// All wrappers are installed before opening a control stream. In particular,
// there is only one Hub, acceptor and participant, shared by all runtime owners.
func newSupplementFixture(t *testing.T, configure func(*supplementFixture)) *supplementFixture {
	return newSupplementFixtureWithOptions(t, supplementFixtureOptions{}, configure)
}

// Selected public experience tests configure production sources before App
// assembly. Historical fixtures retain their explicit synthetic start policy.
type supplementFixtureOptions struct {
	configuration     func(*Config)
	productionInitial bool
}

func newSupplementFixtureWithOptions(t *testing.T, options supplementFixtureOptions, configure func(*supplementFixture)) *supplementFixture {
	t.Helper()
	pool := postgrestest.Open(t)
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	root, source := t.TempDir(), t.TempDir()
	credential := filepath.Join(t.TempDir(), "provider-key")
	if err = os.WriteFile(credential, []byte("fixture-provider-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	address := reserveRuntimeRunnerAddress(t)
	configuration := Config{Profile: profile, NetworkProfile: integrationNetworkProfile(t), DatabaseURL: os.Getenv("SWITCHBACK_TEST_DATABASE_URL"), DataRoot: root, EngineDataRoot: root, WorkspaceSourceRoot: source, ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID, WorkspaceRevisionID: defaultWorkspaceRevisionID, WorkerImage: "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkerNetwork: "switchback-supplement-fixture", GRPCAddress: address, BackendGRPCTarget: address, BackendHTTPBaseURL: "http://127.0.0.1:8080", DeepSeekCredentialFile: credential, ExecutionEnabled: true, LangfuseBaseURL: "http://127.0.0.1:1", LangfusePublicKey: "synthetic-supplement-pk", LangfuseSecretKey: "synthetic-supplement-sk"}
	if options.configuration != nil {
		options.configuration(&configuration)
	}
	app, err := newWithPool(context.Background(), configuration, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	f := &supplementFixture{t: t, ctx: ctx, cancel: cancel, app: app, pool: pool, callbacks: make(chan supplementCallback, 32), binder: modelobs.NewRequestBinder(), stopped: make(chan error, 1)}
	r := app.runtime
	if !options.productionInitial {
		f.startPolicy = allowSyntheticRuntimeStarts(t, app)
	}
	r.environment.storage.Ownership = localstorage.OwnershipFunc(func(string, int, int) error { return nil })
	r.environment.secrets.Ownership = runtimesecret.OwnershipFunc(func(string, int, int) error { return nil })
	r.environment.secrets.OwnerUID = func(string) (uint32, error) { return 0, nil }
	r.environment.secrets.OpenFileOwnerUID = func(*os.File) (uint32, error) { return 0, nil }
	store, err := workerpostgres.NewRuntimeControlJournalWithObservation(pool, &artifactpostgres.ObservationCaptureStore{})
	if err != nil {
		t.Fatal(err)
	}
	f.journal = &supplementJournal{RuntimeControlJournal: store, accepted: make(chan workerapp.RuntimeControlEventAcceptance, 128), applied: make(chan workerapp.RuntimeControlEventAcceptance, 128)}
	hub := controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{Store: f.journal, Now: time.Now})
	if err = hub.SetEventObserver(localRuntimeControlObserver{heartbeat: workerapp.RuntimeHeartbeatObserver{Service: workerapp.RuntimeHeartbeatService{Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: randomIDs{}}}}); err != nil {
		t.Fatal(err)
	}
	if err = hub.SetModelObservationAcceptor(workerapp.ModelObservationAcceptor{Preparer: r}); err != nil {
		t.Fatal(err)
	}
	r.hub, r.environment.hub, r.reconciler.Control = hub, hub, hub
	r.cleanup.Control = runtimeObservationCleanupControl{WorkerControl: hub, runner: r}
	f.settlements = &supplementSettlements{TurnSettlementRepository: r.settlements.Store.(*workerpostgres.TurnSettlementRepository), completed: make(chan workerapp.TurnSettlement, 32)}
	r.settlements.Store = f.settlements
	clientCtx, cancelClient := context.WithCancel(ctx)
	f.runtime = &observationFixtureRuntime{ctx: clientCtx, runner: r, address: address, starts: make(chan observationFixtureTurn, 32), failures: make(chan error, 32), joinClient: true}
	f.runtime.installInspectionFixture(t)
	f.runtime.onModel = func(ctx context.Context, start workerapp.WorkerControlCommand, client *controlgrpc.Client, event workerapp.WorkerControlEvent) error {
		c := modelobs.Correlation{TraceID: "trace-" + string(start.Envelope.AgentTurnID), WorkerSessionID: string(start.Envelope.WorkerSessionID), WorkerRunID: string(start.Envelope.WorkerRunID), AgentTurnID: string(start.Envelope.AgentTurnID)}
		if err := f.binder.Activate(c); err != nil {
			return err
		}
		if err := f.binder.Bind(event.Envelope.MessageID); err != nil {
			return err
		}
		select {
		case f.callbacks <- supplementCallback{event, client, c}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.provisioner = &supplementProvisioner{observationFixtureRuntime: f.runtime, cancel: cancelClient}
	r.starts.CreatedLayerChecker = syntheticInitialCreatedLayerChecker{}
	r.starts.Provisioner = f.provisioner
	r.recovery.Provisioner = f.provisioner
	// newWithPool owns v3 activation. We drive its workers deterministically
	// through Tick; no second Run/background owner is started by this fixture.
	if _, ok := app.observability.(fragmentBackgroundRunners); !ok {
		t.Fatal("production v3 runners were not assembled")
	}
	if err := artifactpostgres.NewProjectionJobStore(pool).Activate(ctx, artifactapp.DurableProjectionSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(f)
	}
	f.provisioner.publishInitial = f.publishInitial
	if f.manualInitial {
		f.manualReady = make(chan struct{})
		go func() { f.stopped <- f.runManualInitialFixture() }()
		supplementReceive(t, ctx, f.manualReady)
	} else {
		go func() { f.stopped <- r.Run(ctx) }()
	}
	t.Cleanup(f.stop)
	return f
}
func (f *supplementFixture) stop() {
	f.stopOnce.Do(func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		defer f.cancel()
		select {
		case <-f.provisioner.stop():
		case <-timer.C:
			f.shutdownFailure("client Run/connection join")
			return
		}
		f.cancel()
		select {
		case err := <-f.stopped:
			if err != nil {
				f.t.Errorf("runtime shutdown: %v", err)
			}
		case <-timer.C:
			f.shutdownFailure("Backend join after client Run/connection closed")
		}
	})
}
func (f *supplementFixture) shutdownFailure(stage string) {
	stack := make([]byte, 256<<10)
	n := goruntime.Stack(stack, true)
	f.t.Logf("runtime shutdown stage=%s goroutines:\n%s", stage, stack[:n])
	f.t.Errorf("runtime did not stop within total 5s: %s", stage)
}

func supplementReceive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var zero T
	return zero
}
func (f *supplementFixture) next() (observationFixtureTurn, supplementCallback) {
	f.t.Helper()
	if f.manualInitial {
		f.driveInitialTick()
	}
	turn := f.runtime.next(f.t)
	callback := supplementReceive(f.t, f.ctx, f.callbacks)
	if callback.event.Envelope.AgentTurnID != turn.start.Envelope.AgentTurnID {
		f.t.Fatal("callback binding moved to another Turn")
	}
	f.waitApplied("active-" + string(turn.start.Envelope.AgentTurnID))
	// Piggyback ACK is committed by Accept, not by receiving fixture.starts.
	for {
		a := supplementReceive(f.t, f.ctx, f.journal.accepted)
		if a.Channel.WorkerSessionID == string(turn.start.Envelope.WorkerSessionID) && a.Channel.RuntimeInstanceID == string(turn.start.Envelope.RuntimeInstanceID) && a.Channel.RuntimeGeneration == uint64(turn.start.Envelope.RuntimeGeneration) && a.Channel.ChannelEpoch == turn.start.Envelope.ChannelEpoch && a.Channel.CommandAcknowledgedSequence >= turn.start.Envelope.Sequence {
			break
		}
	}
	var id, key, digest string
	if err := f.pool.QueryRow(f.ctx, `SELECT message_id,idempotency_key,fingerprint FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='command' AND sequence=$2`, string(turn.start.Envelope.RuntimeInstanceID), turn.start.Envelope.Sequence).Scan(&id, &key, &digest); err != nil {
		f.t.Fatal(err)
	}
	envelope := turn.start.Envelope
	if len(turn.start.Payload) > 0 {
		envelope.Payload = turn.start.Payload
	}
	wantDigest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	supplementMust(f.t, err)
	if id != envelope.MessageID || key != envelope.IdempotencyKey || digest != wantDigest {
		f.t.Fatal("Start ACK is not bound to exact command")
	}
	return turn, callback
}
func (f *supplementFixture) waitApplied(id string) workerapp.RuntimeControlEventAcceptance {
	f.t.Helper()
	for {
		v := supplementReceive(f.t, f.ctx, f.journal.applied)
		if v.Message.MessageID == id {
			return v
		}
	}
}
func (f *supplementFixture) finish(turn observationFixtureTurn, callback supplementCallback) {
	f.t.Helper()
	if err := f.binder.Deactivate(callback.correlation); err != nil {
		f.t.Fatal(err)
	}
	turn.finish(f.t)
}
func (f *supplementFixture) settled(turn observationFixtureTurn) workerapp.TurnSettlement {
	f.t.Helper()
	for {
		v := supplementReceive(f.t, f.ctx, f.settlements.completed)
		if v.AgentTurnID == string(turn.start.Envelope.AgentTurnID) {
			return v
		}
	}
}
func (f *supplementFixture) send(callback supplementCallback) {
	f.t.Helper()
	c, err := f.binder.Take(callback.event.Envelope.MessageID)
	if err != nil {
		f.t.Fatal(err)
	}
	if c.AgentTurnID != string(callback.event.Envelope.AgentTurnID) || c.WorkerRunID != string(callback.event.Envelope.WorkerRunID) {
		f.t.Fatal("late request identity was rebound")
	}
	if err = callback.client.Publish(f.ctx, callback.event); err != nil {
		f.t.Fatal(err)
	}
	f.waitApplied(callback.event.Envelope.MessageID)
}
func (f *supplementFixture) post(path string, body map[string]any) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
	if w.Code != http.StatusAccepted {
		f.t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
}
func (f *supplementFixture) revisions(turn observationFixtureTurn) (s, r, a uint64) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, `SELECT s.session_revision,r.revision,a.revision FROM agent_turns a JOIN worker_runs r USING(worker_run_id) JOIN worker_sessions s ON s.worker_session_id=a.worker_session_id WHERE a.agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)).Scan(&s, &r, &a); err != nil {
		f.t.Fatal(err)
	}
	return
}
func (f *supplementFixture) standalone() (observationFixtureTurn, supplementCallback) {
	f.t.Helper()
	task := createRuntimeRunnerTask(f.t, f.app.Handler(), f.pool, "supplement-bootstrap", "Produce a deterministic report")
	turn, callback := f.next()
	f.bootstrap = turn
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	var revision uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT revision FROM tasks WHERE task_id=$1`, task).Scan(&revision); err != nil {
		f.t.Fatal(err)
	}
	f.post("/api/v1/tasks/"+task+"/accept", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "accept-bootstrap", "expected_task_revision": revision, "message": "Accept bootstrap report"})
	s, r, a := f.revisions(turn)
	f.post("/api/v1/worker-sessions/"+string(turn.start.Envelope.WorkerSessionID)+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": "standalone-followup", "expected_session_revision": s, "worker_run_id": turn.start.Envelope.WorkerRunID, "expected_run_revision": r, "agent_turn_id": turn.start.Envelope.AgentTurnID, "expected_turn_revision": a, "content": "Produce standalone report"})
	return f.next()
}
func (f *supplementFixture) queue(turn observationFixtureTurn) string {
	f.t.Helper()
	s, r, a := f.revisions(turn)
	result, err := (workerapp.WorkerUserCommandService{Store: workerpostgres.NewWorkerUserCommandStore(f.pool), IDs: randomIDs{}, Now: time.Now}).Execute(f.ctx, workerapp.SubmitWorkerUserCommand{TenantID: f.app.runtime.configuration.Profile.TenantID, UserID: f.app.runtime.configuration.Profile.ActorUserID, WorkerSessionID: string(turn.start.Envelope.WorkerSessionID), WorkerRunID: string(turn.start.Envelope.WorkerRunID), AgentTurnID: string(turn.start.Envelope.AgentTurnID), CommandType: "queue", Content: "Continue report", ExpectedSessionRevision: s, ExpectedRunRevision: r, ExpectedTurnRevision: a, IdempotencyKey: "queue-" + string(turn.start.Envelope.AgentTurnID)})
	if err != nil {
		f.t.Fatal(err)
	}
	return result.CommandID
}
func (f *supplementFixture) assertContinuation(old, next observationFixtureTurn, queueID string) {
	f.t.Helper()
	a, b := old.start.Envelope, next.start.Envelope
	if a.WorkerSessionID != b.WorkerSessionID || a.RuntimeInstanceID != b.RuntimeInstanceID || a.RuntimeGeneration != b.RuntimeGeneration || a.AgentTurnID == b.AgentTurnID {
		f.t.Fatal("continuation changed resident runtime identity")
	}
	var status, run, turn string
	if err := f.pool.QueryRow(f.ctx, `SELECT status,outcome->>'worker_run_id',outcome->>'agent_turn_id' FROM worker_user_commands WHERE command_id=$1`, queueID).Scan(&status, &run, &turn); err != nil {
		f.t.Fatal(err)
	}
	if status != "applied" || run != string(b.WorkerRunID) || turn != string(b.AgentTurnID) {
		f.t.Fatal("next Start is not exact queued continuation")
	}
}
func (f *supplementFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}
func (f *supplementFixture) jsonRow(query string, args ...any) []byte {
	f.t.Helper()
	var b []byte
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&b); err != nil {
		f.t.Fatal(err)
	}
	return b
}
func (f *supplementFixture) assertNoModel(c supplementCallback) {
	f.t.Helper()
	if n := f.count(`SELECT count(*) FROM runtime_control_messages WHERE message_id=$1`, c.event.Envelope.MessageID); n != 0 {
		f.t.Fatalf("future callback entered journal: %d", n)
	}
}
func (f *supplementFixture) assertModel(c supplementCallback) {
	f.t.Helper()
	if n := f.count(`SELECT count(*) FROM observation_candidates WHERE kind='model' AND worker_run_id=$1 AND convert_from(frame,'UTF8')::jsonb->>'observation_id'=$2`, string(c.event.Envelope.WorkerRunID), c.event.Envelope.MessageID); n != 1 {
		f.t.Fatal("model supplement was not durably accepted")
	}
}

func supplementMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Only actual PostgreSQL lease/retry deadlines may use elapsed-time waiting.
// This helper is never used to infer terminal, ACK, Apply or HTTP progress.
func (f *supplementFixture) awaitDatabaseTime(at time.Time) {
	f.t.Helper()
	var now time.Time
	supplementMust(f.t, f.pool.QueryRow(f.ctx, `SELECT clock_timestamp()`).Scan(&now))
	wait := at.Sub(now)
	if wait > 10*time.Second {
		f.t.Fatalf("unexpected protocol wait %s", wait)
	}
	if wait >= 0 {
		timer := time.NewTimer(wait + time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-f.ctx.Done():
			f.t.Fatal(f.ctx.Err())
		}
	}
	var eligible bool
	supplementMust(f.t, f.pool.QueryRow(f.ctx, `SELECT clock_timestamp()>$1`, at).Scan(&eligible))
	if !eligible {
		f.t.Fatal("database deadline not reached")
	}
	f.t.Logf("real PostgreSQL deadline wait=%s", wait)
}
