package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"encoding/json"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"reflect"
	"testing"
	"time"
)

// A recording wrapper observes the production coordinator order without changing authority.
type accountedRecordingChecker struct {
	reader *CreatedLayerReader
	events *[]string
}

func (c accountedRecordingChecker) CheckCreatedLayer(ctx context.Context, r workerapp.RuntimeProvisionRequest, id workerapp.CreatedRuntimeIdentity) error {
	*c.events = append(*c.events, "finite_check")
	return c.reader.CheckCreatedLayer(ctx, r, id)
}

type accountedRecordingAuthorizer struct {
	events   *[]string
	delegate *createdAuthorizer
}

func (a accountedRecordingAuthorizer) AuthorizeInitialPreparation(ctx context.Context, r workerapp.RuntimePreparationRequest) error {
	return a.delegate.AuthorizeInitialPreparation(ctx, r)
}
func (a accountedRecordingAuthorizer) AuthorizeInitialProvision(ctx context.Context, r workerapp.RuntimePreparationRequest, p workerapp.RuntimeProvisionRequest) error {
	*a.events = append(*a.events, "authorize")
	return a.delegate.AuthorizeInitialProvision(ctx, r, p)
}

type accountedRecordingProvisioner struct {
	events   *[]string
	delegate *createdRecordingProvisioner
}

func (p accountedRecordingProvisioner) Provision(ctx context.Context, r workerapp.RuntimeProvisionRequest, check workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	*p.events = append(*p.events, "create")
	runtime, err := p.delegate.Provision(ctx, r, check)
	if err == nil {
		*p.events = append(*p.events, "start")
	}
	return runtime, err
}
func (p accountedRecordingProvisioner) Inspect(ctx context.Context, id string) (workerapp.RuntimeInspection, error) {
	return p.delegate.Inspect(ctx, id)
}

func TestCreatedLayerAccountedCoordinatorPreservesAuthorityBeforeOneStart(t *testing.T) {
	cases := map[string]func(*CreatedLayerFacts){
		"accepted":          func(*CreatedLayerFacts) {},
		"revoked_authority": func(*CreatedLayerFacts) {},
		"legacy_zero":       func(*CreatedLayerFacts) {},
		"byte":              func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[8:], 1) },
		"byte_soft":         func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[16:], 0) },
		"byte_used":         func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[40:], 20971521) },
		"owner":             func(f *CreatedLayerFacts) { f.RuntimeID = "wrong" },
		"manifest":          func(f *CreatedLayerFacts) { f.ManifestDigest = Digest([]byte("old")) },
		"quota_project":     func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint32(f.Quota.Target[4:], 100) },
		"quota_enforcement": func(f *CreatedLayerFacts) { f.Quota.StateFlags = 0x10 },
		"quota_raw":         func(f *CreatedLayerFacts) { f.Quota.Target = f.Quota.Target[:111] },
		"inode_over":        func(f *CreatedLayerFacts) { binary.LittleEndian.PutUint64(f.Quota.Target[24:], 1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := accountedCreatedFixture(t)
			if name == "legacy_zero" {
				tr.AdmissionPolicy = nil
			}
			binary.LittleEndian.PutUint64(f.Quota.Target[24:], 0)
			mutate(&f)
			dir := createdTempDir(t)
			reader, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(f)
			writeCreated(t, dir, id.ContainerID, signedCreated(t, key, payload))
			cmd := workerapp.StartWorkerRunCommand{TenantID: "tenant", UserID: "user", WorkerSessionID: req.WorkerSessionID, TaskID: "task", PermitID: "permit", ExpectedSessionRevision: 1, RuntimeGeneration: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`), AgentProfileID: "test-agent", AgentProfileRevision: "1", AgentProfileDigest: Digest([]byte("agent")), AgentTurnContent: json.RawMessage(`{"text":"read"}`), TurnIdempotencyKey: "turn/task/1", RuntimeImageRef: "worker:test", ResourceProfileID: req.ResourceProfile.ID, NetworkPolicyID: "m1_public_web_restricted_v1", NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1}
			req.NetworkProfile = workerapp.ProfileRef{ID: cmd.NetworkPolicyID}
			req.NetworkEnforcementStatus = cmd.NetworkEnforcementStatus
			events := []string{}
			auth := &createdAuthorizer{denyAfterProof: name == "revoked_authority"}
			provisioner := &createdRecordingProvisioner{id: id}
			c := workerapp.RuntimeCoordinator{Authorizer: accountedRecordingAuthorizer{&events, auth}, CreatedLayerChecker: accountedRecordingChecker{reader, &events}, Starts: workerapp.StartWorkerRunService{Store: createdStartStore{}, IDs: createdIDs{}, Now: time.Now}, Environment: createdEnvironment{request: req}, Provisioner: accountedRecordingProvisioner{&events, provisioner}}
			result, err := c.Start(context.Background(), workerapp.StartRuntimeCommand{Run: cmd})
			accepted := name == "accepted"
			if (err == nil) != accepted || provisioner.creates != 1 || result.Runtime.ContainerID != id.ContainerID {
				t.Fatalf("create/outcome drift: %+v err=%v", result, err)
			}
			wantStarts, wantCalls := 0, 1
			wantEvents := []string{"authorize", "create", "finite_check"}
			if accepted || name == "revoked_authority" {
				wantCalls = 2
				wantEvents = append(wantEvents, "authorize")
			}
			if accepted {
				wantStarts = 1
				wantEvents = append(wantEvents, "start")
			}
			if provisioner.starts != wantStarts || auth.provisionCalls != wantCalls || !reflect.DeepEqual(events, wantEvents) {
				t.Fatalf("starts=%d auth=%d order=%v want=%v", provisioner.starts, auth.provisionCalls, events, wantEvents)
			}
		})
	}
}
