package capacitysnapshot

import (
	"context"
	"encoding/binary"
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

func TestCreatedLayerProofDenialsReachCoordinatorBeforeAuthorityAndStart(t *testing.T) {
	for _, name := range []string{"missing", "wrong_CID", "owner", "FSUUID", "project", "enforcement", "hardbytes", "stale", "signature", "schema", "good_proof_with_revoked_authority"} {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := createdFixture(t)
			dir := createdTempDir(t)
			reader, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "wrong_CID":
				f.ContainerID = strings.Repeat("b", 64)
			case "owner":
				f.RuntimeID = "other"
			case "FSUUID":
				f.Diff.FSUUID = "other"
			case "project":
				binary.LittleEndian.PutUint32(f.Quota.Target[4:], 100)
			case "enforcement":
				f.Quota.StateFlags = 0x10
			case "hardbytes":
				binary.LittleEndian.PutUint64(f.Quota.Target[8:], 123)
			case "stale":
				f.SampleStartedAt = f.SampleStartedAt.Add(-31 * time.Second)
				f.ObservedAt = f.ObservedAt.Add(-31 * time.Second)
			}
			payload, _ := json.Marshal(f)
			raw := signedCreated(t, key, payload)
			if name == "signature" {
				var envelope CreatedLayerEnvelope
				json.Unmarshal(raw, &envelope)
				envelope.Signature[0] ^= 255
				raw, _ = json.Marshal(envelope)
			}
			if name == "schema" {
				raw = signedCreated(t, key, []byte(strings.Replace(string(payload), `"pid":0,`, "", 1)))
			}
			if name != "missing" {
				writeCreated(t, dir, id.ContainerID, raw)
			}
			cmd := workerapp.StartWorkerRunCommand{TenantID: "tenant", UserID: "user", WorkerSessionID: req.WorkerSessionID, TaskID: "task", PermitID: "permit", ExpectedSessionRevision: 1, RuntimeGeneration: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`), AgentProfileID: "test-agent", AgentProfileRevision: "1", AgentProfileDigest: Digest([]byte("agent")), AgentTurnContent: json.RawMessage(`{"text":"read"}`), TurnIdempotencyKey: "turn/task/1", RuntimeImageRef: "worker:test", ResourceProfileID: req.ResourceProfile.ID, NetworkPolicyID: "m1_public_web_restricted_v1", NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1}
			req.NetworkProfile = workerapp.ProfileRef{ID: cmd.NetworkPolicyID}
			req.NetworkEnforcementStatus = cmd.NetworkEnforcementStatus
			auth := &createdAuthorizer{denyAfterProof: true}
			provisioner := &createdRecordingProvisioner{id: id}
			c := workerapp.RuntimeCoordinator{Authorizer: auth, CreatedLayerChecker: reader, Starts: workerapp.StartWorkerRunService{Store: createdStartStore{}, IDs: createdIDs{}, Now: time.Now}, Environment: createdEnvironment{request: req}, Provisioner: provisioner}
			result, err := c.Start(context.Background(), workerapp.StartRuntimeCommand{Run: cmd})
			if err == nil || provisioner.creates != 1 || provisioner.starts != 0 || result.Runtime.ContainerID != id.ContainerID {
				t.Fatalf("Start boundary or physical identity failed: creates=%d starts=%d CID=%q err=%v", provisioner.creates, provisioner.starts, result.Runtime.ContainerID, err)
			}
			wantCalls := 1
			if name == "good_proof_with_revoked_authority" {
				wantCalls = 2
				if !strings.Contains(err.Error(), "withdrawn") {
					t.Fatalf("good finite proof bypassed original authority: %v", err)
				}
			}
			if auth.provisionCalls != wantCalls {
				t.Fatalf("checker/authority order: calls=%d want=%d", auth.provisionCalls, wantCalls)
			}
		})
	}
}
