package capacityobserver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func sharedObserverFixture(t *testing.T) (capacitysnapshot.Trust, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity, capacityprobe.CreatedLayerCollection) {
	t.Helper()
	tr, req, id, collection := observerFixture(t)
	g := config.M1SharedVolumeCapacityGuarantee()
	tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	resource, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	manifest := capacitysnapshot.AdmissionManifest{Schema: capacitysnapshot.AdmissionManifestVersion, Guarantee: *tr.AdmissionPolicy, Identity: tr.Identity, ResourceProfile: resource.Ref(), NetworkProfile: config.RuntimeResourceProfileRef{ID: network.ID, Revision: network.Revision, Digest: network.Digest}, KeyID: tr.KeyID, PublicKey: tr.PublicKey, SourceManifestDigest: capacitysnapshot.Digest([]byte("shared observer source")), DeploymentInventoryDigest: capacitysnapshot.Digest([]byte("shared observer inventory")), HostBoundaryReceiptDigest: capacitysnapshot.Digest([]byte("shared observer host receipt")), NativeEnforcementReceiptDigest: capacitysnapshot.Digest([]byte("shared observer native receipt"))}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	tr.ManifestDigest = capacitysnapshot.Digest(raw)
	if _, err = capacitysnapshot.ParseAdmissionManifest(raw, tr); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(collection.Physical.TargetQuota[24:32], 0)
	return tr, req, id, collection
}

// The only substitution is the existing private observation seam. Real Checker
// binding, observation validation and created-layer verifier all execute.
func TestSharedVolumeOriginalCheckerBeforeCoordinatorStart(t *testing.T) {
	for _, name := range []string{"accepted", "revoked_authority", "wrong_cid", "wrong_resource", "wrong_source", "wrong_manifest"} {
		t.Run(name, func(t *testing.T) {
			tr, req, id, collection := sharedObserverFixture(t)
			checker, err := New(tr)
			if err != nil {
				t.Fatal(err)
			}
			pinned := tr.ManifestDigest
			switch name {
			case "wrong_cid":
				collection.Inspect.ID = strings.Repeat("b", 64)
			case "wrong_resource":
				req.ResourceProfile.Digest = capacitysnapshot.Digest([]byte("other resource"))
			case "wrong_source":
				collection.Samples[3].Source = "configured"
			case "wrong_manifest":
				checker.manifestDigest = capacitysnapshot.Digest([]byte("other manifest"))
			}
			// Caller mutations cannot change policy pinned by New.
			tr.AdmissionPolicy.ID = "caller-mutated"
			tr.ManifestDigest = capacitysnapshot.Digest([]byte("caller-mutated"))
			events := []string{}
			initialized, calls := 0, 0
			checker.initialize = func() (observeFunc, error) {
				initialized++
				return func(_ context.Context, b createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
					calls++
					events = append(events, "observe")
					want := createdlayeripc.Binding{ManifestDigest: pinned, ContainerID: id.ContainerID, RuntimeID: req.RuntimeInstanceID, SessionID: req.WorkerSessionID, RunID: req.WorkerRunID, Generation: req.Generation, ProfileID: req.ResourceProfile.ID, ProfileRevision: req.ResourceProfile.Revision, ProfileDigest: req.ResourceProfile.Digest}
					if name == "wrong_manifest" {
						want.ManifestDigest = checker.manifestDigest
					}
					if b != want {
						t.Fatalf("actual binding=%+v want=%+v", b, want)
					}
					return collection, nil
				}, nil
			}
			cmd := workerapp.StartWorkerRunCommand{TenantID: "tenant", UserID: "user", WorkerSessionID: req.WorkerSessionID, TaskID: "task", PermitID: "permit", ExpectedSessionRevision: 1, RuntimeGeneration: 1, TriggerType: "task_initial", StartSnapshot: json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`), AgentProfileID: "test-agent", AgentProfileRevision: "1", AgentProfileDigest: capacitysnapshot.Digest([]byte("agent")), AgentTurnContent: json.RawMessage(`{"text":"read"}`), TurnIdempotencyKey: "turn/task/1", RuntimeImageRef: "worker:test", ResourceProfileID: req.ResourceProfile.ID, NetworkPolicyID: "m1_public_web_restricted_v1", NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1}
			req.NetworkProfile = workerapp.ProfileRef{ID: cmd.NetworkPolicyID}
			req.NetworkEnforcementStatus = cmd.NetworkEnforcementStatus
			auth := &sharedObserverAuthorizer{events: &events, delegate: &createdAuthorizer{denyAfterProof: name == "revoked_authority"}}
			provisioner := &createdRecordingProvisioner{id: id}
			coordinator := workerapp.RuntimeCoordinator{Authorizer: auth, CreatedLayerChecker: checker, Starts: workerapp.StartWorkerRunService{Store: createdStartStore{}, IDs: createdIDs{}, Now: time.Now}, Environment: createdEnvironment{request: req}, Provisioner: provisioner}
			result, err := coordinator.Start(context.Background(), workerapp.StartRuntimeCommand{Run: cmd})
			accepted := name == "accepted"
			if (err == nil) != accepted || provisioner.creates != 1 || result.Runtime.ContainerID != id.ContainerID {
				t.Fatalf("creates=%d result=%+v error=%v", provisioner.creates, result, err)
			}
			wantStarts, wantAuth, wantCalls := 0, 1, 1
			wantEvents := []string{"authorize", "observe"}
			if name == "wrong_resource" {
				wantCalls = 0
				wantEvents = []string{"authorize"}
			}
			if accepted || name == "revoked_authority" {
				wantAuth = 2
				wantEvents = append(wantEvents, "authorize")
			}
			if accepted {
				wantStarts = 1
			}
			if provisioner.starts != wantStarts || auth.delegate.provisionCalls != wantAuth || initialized != wantCalls || calls != wantCalls || !reflect.DeepEqual(events, wantEvents) {
				t.Fatalf("starts=%d auth=%d init=%d calls=%d events=%v want=%v", provisioner.starts, auth.delegate.provisionCalls, initialized, calls, events, wantEvents)
			}
		})
	}
}

type sharedObserverAuthorizer struct {
	events   *[]string
	delegate *createdAuthorizer
}

func (a *sharedObserverAuthorizer) AuthorizeInitialPreparation(ctx context.Context, r workerapp.RuntimePreparationRequest) error {
	return a.delegate.AuthorizeInitialPreparation(ctx, r)
}
func (a *sharedObserverAuthorizer) AuthorizeInitialProvision(ctx context.Context, r workerapp.RuntimePreparationRequest, p workerapp.RuntimeProvisionRequest) error {
	*a.events = append(*a.events, "authorize")
	return a.delegate.AuthorizeInitialProvision(ctx, r, p)
}
