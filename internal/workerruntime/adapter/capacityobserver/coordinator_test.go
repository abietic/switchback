package capacityobserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type createdStartStore struct{}

func (createdStartStore) StartWorkerRun(_ context.Context, r workerapp.StartWorkerRunRecord) (workerapp.StartedWorkerRun, error) {
	return workerapp.StartedWorkerRun{Created: true, WorkerSessionID: r.Command.WorkerSessionID, WorkerRunID: r.WorkerRunID, RuntimeInstanceID: r.RuntimeInstanceID, RuntimeGeneration: r.Command.RuntimeGeneration, ControlChannelEpoch: r.Command.ControlChannelEpoch, Status: "starting", RunRevision: 1, SessionRevision: 2}, nil
}

type createdIDs struct{}

func (createdIDs) NewID(kind string) (string, error) {
	switch kind {
	case "worker-run":
		return "run-1", nil
	case "runtime-instance":
		return "runtime-1", nil
	}
	return kind + "-1", nil
}

type createdEnvironment struct {
	request workerapp.RuntimeProvisionRequest
}

func (e createdEnvironment) PlanRuntime(context.Context, workerapp.RuntimePreparationRequest) (workerapp.RuntimePreparationPlan, error) {
	return e, nil
}
func (e createdEnvironment) SourceBinding() workerapp.RuntimeSourcePlanBinding {
	binding, err := workerapp.NewRuntimeSourcePlanBinding("sha256:"+strings.Repeat("a", 64), nil)
	if err != nil {
		panic(err)
	}
	return binding
}
func (e createdEnvironment) Prepare(context.Context) (workerapp.RuntimeProvisionRequest, error) {
	e.request.SourcePlanDigest = e.SourceBinding().Digest
	return e.request, nil
}
func (createdEnvironment) Close() error { return nil }

type createdAuthorizer struct {
	provisionCalls int
	denyAfterProof bool
}

func (*createdAuthorizer) AuthorizeInitialPreparation(context.Context, workerapp.RuntimePreparationRequest) error {
	return nil
}
func (a *createdAuthorizer) AuthorizeInitialProvision(context.Context, workerapp.RuntimePreparationRequest, workerapp.RuntimeProvisionRequest) error {
	a.provisionCalls++
	if a.provisionCalls == 2 && a.denyAfterProof {
		return errors.New("original execution authority withdrawn")
	}
	return nil
}

type createdRecordingProvisioner struct {
	creates, starts int
	id              workerapp.CreatedRuntimeIdentity
}

func (p *createdRecordingProvisioner) Provision(ctx context.Context, r workerapp.RuntimeProvisionRequest, check workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	p.creates++
	partial := workerapp.ProvisionedRuntime{ContainerID: p.id.ContainerID, RuntimeInstanceID: r.RuntimeInstanceID, Generation: r.Generation}
	if err := check(ctx, p.id); err != nil {
		return partial, err
	}
	p.starts++
	return partial, nil
}
func (*createdRecordingProvisioner) Inspect(context.Context, string) (workerapp.RuntimeInspection, error) {
	return workerapp.RuntimeInspection{}, nil
}

func TestCoordinatorRealCheckerRetainsIdentityAndOriginalAuthorizer(t *testing.T) {
	for _, name := range []string{"incomplete_observation", "finite_success_authority_withdrawn"} {
		t.Run(name, func(t *testing.T) {
			tr, req, id, collection := observerFixture(t)
			if name == "incomplete_observation" {
				collection.Physical.ObservationComplete = false
			}
			checker, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			initialized, calls := 0, 0
			installObservation(checker, collection, &initialized, &calls)
			cmd := workerapp.StartWorkerRunCommand{TenantID: "tenant", UserID: "user", WorkerSessionID: req.WorkerSessionID, TaskID: "task", PermitID: "permit", ExpectedSessionRevision: 1, RuntimeGeneration: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`), AgentProfileID: "test-agent", AgentProfileRevision: "1", AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AgentTurnContent: json.RawMessage(`{"text":"read"}`), TurnIdempotencyKey: "turn/task/1", RuntimeImageRef: "worker:test", ResourceProfileID: req.ResourceProfile.ID, NetworkPolicyID: "m1_public_web_restricted_v1", NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1}
			req.NetworkProfile = workerapp.ProfileRef{ID: cmd.NetworkPolicyID}
			req.NetworkEnforcementStatus = cmd.NetworkEnforcementStatus
			auth := &createdAuthorizer{denyAfterProof: true}
			provisioner := &createdRecordingProvisioner{id: id}
			coordinator := workerapp.RuntimeCoordinator{Authorizer: auth, CreatedLayerChecker: checker, Starts: workerapp.StartWorkerRunService{Store: createdStartStore{}, IDs: createdIDs{}, Now: time.Now}, Environment: createdEnvironment{request: req}, Provisioner: provisioner}
			result, err := coordinator.Start(context.Background(), workerapp.StartRuntimeCommand{Run: cmd})
			if err == nil || provisioner.creates != 1 || provisioner.starts != 0 || result.Runtime.ContainerID != id.ContainerID || result.Runtime.RuntimeInstanceID != id.RuntimeInstanceID || result.Runtime.Generation != id.Generation {
				t.Fatalf("created identity/start boundary lost: result=%+v creates=%d starts=%d err=%v", result, provisioner.creates, provisioner.starts, err)
			}
			want := 1
			if name == "finite_success_authority_withdrawn" {
				want = 2
				if !strings.Contains(err.Error(), "withdrawn") {
					t.Fatalf("original authorizer refusal lost: %v", err)
				}
			}
			if auth.provisionCalls != want || initialized != 1 || calls != 1 {
				t.Fatalf("gate order: provision auth=%d init=%d IPC=%d", auth.provisionCalls, initialized, calls)
			}
		})
	}
}
