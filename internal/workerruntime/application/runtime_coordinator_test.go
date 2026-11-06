package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeCoordinatorCommitsOwnershipBeforeEnvironmentOrDocker(t *testing.T) {
	store := &recordingStartStore{}
	ids := &sequenceIDs{values: []string{
		"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
	}}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		CreatedLayerChecker: syntheticCreatedLayerChecker{},
		Authorizer:          &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts: application.StartWorkerRunService{
			Store: store, IDs: ids,
			Now: func() time.Time { return time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC) },
		},
		Environment: preparer,
		Provisioner: provisioner,
	}

	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{
		Run: validStartWorkerRunCommand(),
	})
	if err != nil {
		t.Fatalf("start Runtime: %v", err)
	}
	if result.Ownership.WorkerRunID != "worker-run-1" ||
		result.Ownership.RuntimeInstanceID != "runtime-1" ||
		result.Ownership.AgentSessionID != "agent-session-1" ||
		result.Ownership.AgentTurnID != "agent-turn-1" ||
		result.Runtime.ContainerID != "container-1" {
		t.Fatalf("start result = %+v", result)
	}
	if !store.committed || !preparer.prepared || !provisioner.provisioned {
		t.Fatalf("ordering store=%v prepare=%v provision=%v", store.committed, preparer.prepared, provisioner.provisioned)
	}
}

func TestRuntimeCoordinatorDoesNotTouchEnvironmentWhenOwnershipCommitFails(t *testing.T) {
	sentinel := errors.New("commit failed")
	store := &recordingStartStore{err: sentinel}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		CreatedLayerChecker: syntheticCreatedLayerChecker{},
		Authorizer:          &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	_, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if !errors.Is(err, sentinel) {
		t.Fatalf("start error = %v", err)
	}
	if preparer.prepared || provisioner.provisioned {
		t.Fatalf("external work occurred after failed commit: prepare=%v provision=%v", preparer.prepared, provisioner.provisioned)
	}
}

func TestRuntimeCoordinatorRejectsPreparedIdentityDriftBeforeDocker(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, driftRuntimeID: true}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		CreatedLayerChecker: syntheticCreatedLayerChecker{},
		Authorizer:          &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err == nil {
		t.Fatal("drifted Runtime preparation unexpectedly reached Docker")
	}
	if result.Ownership.RuntimeInstanceID != "runtime-1" || result.Ownership.WorkerRunID != "worker-run-1" {
		t.Fatalf("committed ownership was lost on post-commit failure: %+v", result)
	}
	if provisioner.provisioned {
		t.Fatal("Docker was called with drifted Runtime identity")
	}
}

func TestRuntimeCoordinatorRejectsPreparedNetworkEvidenceDriftBeforeDocker(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, driftNetwork: true}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		CreatedLayerChecker: syntheticCreatedLayerChecker{},
		Authorizer:          &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1",
				"deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{
		Run: validStartWorkerRunCommand(),
	})
	if err == nil {
		t.Fatal("drifted Runtime network evidence unexpectedly reached Docker")
	}
	if result.Ownership.RuntimeInstanceID != "runtime-1" || result.Ownership.WorkerRunID != "worker-run-1" {
		t.Fatalf("committed ownership was lost on network drift: %+v", result)
	}
	if provisioner.provisioned {
		t.Fatal("Docker was called with drifted Runtime network evidence")
	}
}

func TestRuntimeCoordinatorReturnsCommittedOwnershipWhenEnvironmentPreparationFails(t *testing.T) {
	sentinel := errors.New("prepare failed")
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, err: sentinel}
	coordinator := application.RuntimeCoordinator{
		CreatedLayerChecker: syntheticCreatedLayerChecker{},
		Authorizer:          &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: &orderingProvisioner{prepared: &preparer.prepared},
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if !errors.Is(err, sentinel) || result.Ownership.RuntimeInstanceID != "runtime-1" {
		t.Fatalf("post-commit failure result=%+v error=%v", result, err)
	}
}

func validStartWorkerRunCommand() application.StartWorkerRunCommand {
	return application.StartWorkerRunCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: "worker-session-1",
		TaskID: "task-1", PermitID: "permit-1", ExpectedSessionRevision: 2,
		RuntimeGeneration: 1, TriggerType: "task_initial",
		StartSnapshot:  json.RawMessage(`{"working_copy_checkpoint":"checkpoint-1","working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`),
		AgentProfileID: "deepseek-codex-openai-v1", AgentProfileRevision: "1",
		AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AgentTurnContent:   json.RawMessage(`{"text":"fix it"}`),
		TurnIdempotencyKey: "turn/task-1/revision-1",
		RuntimeImageRef:    "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ResourceProfileID:  "local_standard_v1", NetworkPolicyID: "m1_public_web_restricted_v1",
		NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1,
	}
}

type sequenceIDs struct {
	values []string
	index  int
}

func (ids *sequenceIDs) NewID(string) (string, error) {
	if ids.index >= len(ids.values) {
		return "", errors.New("no test ID")
	}
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}

type recordingStartStore struct {
	committed bool
	replay    bool
	err       error
}

func (store *recordingStartStore) StartWorkerRun(_ context.Context, record application.StartWorkerRunRecord) (application.StartedWorkerRun, error) {
	if store.err != nil {
		return application.StartedWorkerRun{}, store.err
	}
	store.committed = true
	return application.StartedWorkerRun{
		Created:     !store.replay,
		WorkerRunID: record.WorkerRunID, WorkerSessionID: record.Command.WorkerSessionID,
		TaskID: record.Command.TaskID, RuntimeInstanceID: record.RuntimeInstanceID,
		AgentSessionID: record.AgentSessionID, AgentTurnID: record.AgentTurnID,
		RuntimeGeneration:   record.Command.RuntimeGeneration,
		ControlChannelEpoch: record.Command.ControlChannelEpoch,
		Status:              "starting", RunRevision: 1, SessionRevision: 3,
	}, nil
}

type orderingPreparer struct {
	committed      *bool
	prepared       bool
	driftRuntimeID bool
	driftNetwork   bool
	err            error
}

func (preparer *orderingPreparer) PrepareRuntime(_ context.Context, request application.RuntimePreparationRequest) (application.RuntimeProvisionRequest, error) {
	if preparer.committed == nil || !*preparer.committed {
		return application.RuntimeProvisionRequest{}, errors.New("environment prepared before ownership commit")
	}
	preparer.prepared = true
	if preparer.err != nil {
		return application.RuntimeProvisionRequest{}, preparer.err
	}
	runtimeID := request.Ownership.RuntimeInstanceID
	if preparer.driftRuntimeID {
		runtimeID = "runtime-drift"
	}
	networkID := request.Command.NetworkPolicyID
	if preparer.driftNetwork {
		networkID = "caller-network"
	}
	return application.RuntimeProvisionRequest{
		SourcePlanDigest:  emptyPreparationBinding().Digest,
		WorkerSessionID:   request.Ownership.WorkerSessionID,
		WorkerRunID:       request.Ownership.WorkerRunID,
		RuntimeInstanceID: runtimeID,
		Generation:        request.Ownership.RuntimeGeneration,
		NetworkProfile: application.ProfileRef{
			ID: networkID, Revision: request.Command.NetworkPolicyRevision,
			Digest: request.Command.NetworkPolicyDigest,
		},
		NetworkEnforcementStatus: request.Command.NetworkEnforcementStatus,
	}, nil
}

type orderingProvisioner struct {
	prepared    *bool
	provisioned bool
}

func (provisioner *orderingProvisioner) Provision(ctx context.Context, request application.RuntimeProvisionRequest, check application.RuntimePreStartCheck) (application.ProvisionedRuntime, error) {
	if provisioner.prepared == nil || !*provisioner.prepared {
		return application.ProvisionedRuntime{}, errors.New("Docker called before environment preparation")
	}
	provisioner.provisioned = true
	if err := check(ctx, application.CreatedRuntimeIdentity{ContainerID: "container-1", RuntimeInstanceID: request.RuntimeInstanceID, WorkerSessionID: request.WorkerSessionID, Generation: request.Generation}); err != nil {
		return application.ProvisionedRuntime{RuntimeInstanceID: request.RuntimeInstanceID, ContainerID: "container-1", Generation: request.Generation}, err
	}
	return application.ProvisionedRuntime{
		RuntimeInstanceID: request.RuntimeInstanceID,
		ContainerID:       "container-1", Generation: request.Generation,
	}, nil
}

func (*orderingProvisioner) Inspect(context.Context, string) (application.RuntimeInspection, error) {
	return application.RuntimeInspection{}, nil
}

func TestInitialExecutionCoordinatorMissingAuthorityDeniesEffects(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Starts:      application.StartWorkerRunService{Store: store, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now},
		Environment: preparer, Provisioner: provisioner,
	}
	_, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err == nil || preparer.prepared || provisioner.provisioned {
		t.Fatalf("missing mandatory authority: err=%v prepare=%v provision=%v", err, preparer.prepared, provisioner.provisioned)
	}
}

// This boundary spy checks ordering and the exact committed identities; the
// PostgreSQL integration tests exercise real durable eligibility decisions.
type orderingExecutionAuthorizer struct{ committed, prepared *bool }

func (a *orderingExecutionAuthorizer) AuthorizeInitialPreparation(_ context.Context, r application.RuntimePreparationRequest) error {
	if !*a.committed || *a.prepared || r.Ownership.WorkerRunID == "" {
		return errors.New("invalid preparation ordering")
	}
	return nil
}
func (a *orderingExecutionAuthorizer) AuthorizeInitialProvision(_ context.Context, r application.RuntimePreparationRequest, p application.RuntimeProvisionRequest) error {
	if !*a.committed || !*a.prepared || p.RuntimeInstanceID != r.Ownership.RuntimeInstanceID {
		return errors.New("invalid provision ordering")
	}
	return nil
}

// Synthetic physical reader only at the application test seam.
type syntheticCreatedLayerChecker struct{}

func (syntheticCreatedLayerChecker) CheckCreatedLayer(context.Context, application.RuntimeProvisionRequest, application.CreatedRuntimeIdentity) error {
	return nil
}

func TestRuntimeCoordinatorMissingCreatedLayerCheckerDeniesPreparation(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Authorizer:  &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared},
		Starts:      application.StartWorkerRunService{Store: store, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now},
		Environment: preparer, Provisioner: provisioner,
	}
	_, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err == nil || preparer.prepared || provisioner.provisioned {
		t.Fatalf("missing physical checker err=%v preparation=%v provision=%v", err, preparer.prepared, provisioner.provisioned)
	}
}

type boundaryCreatedChecker struct {
	journal *[]string
	err     error
	cancel  context.CancelFunc
}

func (c *boundaryCreatedChecker) CheckCreatedLayer(_ context.Context, r application.RuntimeProvisionRequest, id application.CreatedRuntimeIdentity) error {
	*c.journal = append(*c.journal, "quota")
	if id.ContainerID != "CID-A" || id.RuntimeInstanceID != r.RuntimeInstanceID || id.WorkerSessionID != r.WorkerSessionID || id.Generation != r.Generation {
		return errors.New("wrong physical request")
	}
	if c.cancel != nil {
		c.cancel()
	}
	return c.err
}

type boundaryAuthorizer struct {
	journal *[]string
	calls   int
	revoke  bool
}

func (a *boundaryAuthorizer) AuthorizeInitialPreparation(context.Context, application.RuntimePreparationRequest) error {
	return nil
}
func (a *boundaryAuthorizer) AuthorizeInitialProvision(context.Context, application.RuntimePreparationRequest, application.RuntimeProvisionRequest) error {
	a.calls++
	if a.calls == 2 {
		*a.journal = append(*a.journal, "authority")
		if a.revoke {
			return errors.New("authority revoked")
		}
	}
	return nil
}

type boundaryInitialProvisioner struct {
	journal *[]string
	unknown bool
	idDrift bool
}

func (p *boundaryInitialProvisioner) Provision(ctx context.Context, r application.RuntimeProvisionRequest, check application.RuntimePreStartCheck) (application.ProvisionedRuntime, error) {
	out := application.ProvisionedRuntime{RuntimeInstanceID: r.RuntimeInstanceID, Generation: r.Generation, ContainerID: "CID-A"}
	if p.unknown {
		out.ContainerID = ""
		out.PhysicalEffectUnknown = true
		return out, errors.New("Create reply lost")
	}
	*p.journal = append(*p.journal, "create")
	id := application.CreatedRuntimeIdentity{ContainerID: "CID-A", RuntimeInstanceID: r.RuntimeInstanceID, WorkerSessionID: r.WorkerSessionID, Generation: r.Generation}
	if p.idDrift {
		id.RuntimeInstanceID = "runtime-B"
	}
	if err := check(ctx, id); err != nil {
		return out, err
	}
	*p.journal = append(*p.journal, "start")
	return out, nil
}
func (*boundaryInitialProvisioner) Inspect(context.Context, string) (application.RuntimeInspection, error) {
	return application.RuntimeInspection{}, nil
}

func TestRuntimeCoordinatorPreStartRechecksAuthorityAndForwardsPhysicalFailure(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		quotaErr                       error
		cancel, revoke, unknown, drift bool
		want                           []string
	}{
		{name: "valid", want: []string{"create", "quota", "authority", "start"}},
		{name: "missing quota proof", quotaErr: errors.New("ENOENT"), want: []string{"create", "quota"}},
		{name: "revoked after quota", revoke: true, want: []string{"create", "quota", "authority"}},
		{name: "cancel after quota", cancel: true, want: []string{"create", "quota"}},
		{name: "unknown Create without CID", unknown: true},
		{name: "drifted created owner", drift: true, want: []string{"create"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := []string(nil)
			store := &recordingStartStore{}
			preparer := &orderingPreparer{committed: &store.committed}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checker := &boundaryCreatedChecker{journal: &journal, err: tc.quotaErr}
			if tc.cancel {
				checker.cancel = cancel
			}
			authorizer := &boundaryAuthorizer{journal: &journal, revoke: tc.revoke}
			c := application.RuntimeCoordinator{CreatedLayerChecker: checker, Authorizer: authorizer, Starts: application.StartWorkerRunService{Store: store, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now}, Environment: preparer, Provisioner: &boundaryInitialProvisioner{journal: &journal, unknown: tc.unknown, idDrift: tc.drift}}
			got, err := c.Start(ctx, application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
			if !reflect.DeepEqual(journal, tc.want) {
				t.Fatalf("order=%v want=%v err=%v", journal, tc.want, err)
			}
			if tc.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("denied physical/authority proof reached start")
			}
			if got.Ownership.RuntimeInstanceID != "runtime" || got.Runtime.RuntimeInstanceID != "runtime" || got.Runtime.Generation != 1 {
				t.Fatalf("partial ownership lost: %+v", got)
			}
			if tc.unknown {
				if !got.Runtime.PhysicalEffectUnknown || got.Runtime.ContainerID != "" {
					t.Fatalf("unknown effect lost: %+v", got)
				}
			} else if got.Runtime.ContainerID != "CID-A" {
				t.Fatalf("actual CID lost: %+v", got)
			}
		})
	}
}

func TestRuntimeCoordinatorReplayDoesNotRequireNewPhysicalCheckerOrRepeatEffects(t *testing.T) {
	store := &recordingStartStore{replay: true}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	var typedNil *boundaryCreatedChecker
	c := application.RuntimeCoordinator{CreatedLayerChecker: typedNil, Authorizer: &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared}, Starts: application.StartWorkerRunService{Store: store, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now}, Environment: preparer, Provisioner: provisioner}
	_, err := c.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err != application.InitialRuntimeStartReplayPending || preparer.prepared || provisioner.provisioned {
		t.Fatalf("replay err=%v prepare=%v provision=%v", err, preparer.prepared, provisioner.provisioned)
	}
}

func TestRuntimeCoordinatorTypedNilCreatedCheckerDeniesBeforePreparation(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	var checker *boundaryCreatedChecker
	c := application.RuntimeCoordinator{CreatedLayerChecker: checker, Authorizer: &orderingExecutionAuthorizer{committed: &store.committed, prepared: &preparer.prepared}, Starts: application.StartWorkerRunService{Store: store, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now}, Environment: preparer, Provisioner: provisioner}
	got, err := c.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err == nil || preparer.prepared || provisioner.provisioned || got.Ownership.RuntimeInstanceID != "runtime" {
		t.Fatalf("typed nil checker result=%+v err=%v effects=%v/%v", got, err, preparer.prepared, provisioner.provisioned)
	}
}
func TestUnavailableInitialCreatedLayerCheckerExplicitlyRejects(t *testing.T) {
	if err := (application.UnavailableInitialCreatedLayerChecker{}).CheckCreatedLayer(context.Background(), application.RuntimeProvisionRequest{}, application.CreatedRuntimeIdentity{}); err == nil {
		t.Fatal("production unconfigured checker accepted")
	}
}

// The existing effect spy is retained behind a readonly plan seam.
type orderingPreparationPlan struct {
	preparer *orderingPreparer
	request  application.RuntimePreparationRequest
}

func (preparer *orderingPreparer) PlanRuntime(_ context.Context, request application.RuntimePreparationRequest) (application.RuntimePreparationPlan, error) {
	if preparer.committed == nil || !*preparer.committed {
		return nil, errors.New("plan before owner commit")
	}
	return &orderingPreparationPlan{preparer: preparer, request: request}, nil
}
func (*orderingPreparationPlan) SourceBinding() application.RuntimeSourcePlanBinding {
	return emptyPreparationBinding()
}
func (p *orderingPreparationPlan) Prepare(ctx context.Context) (application.RuntimeProvisionRequest, error) {
	return p.preparer.PrepareRuntime(ctx, p.request)
}
func (*orderingPreparationPlan) Close() error { return nil }
