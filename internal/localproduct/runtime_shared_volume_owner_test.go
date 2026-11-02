package localproduct

import (
	"context"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

type sharedVolumeOwnerCapture struct {
	capacityAssemblyStore
	trust capacitysnapshot.Trust
}

func (s *sharedVolumeOwnerCapture) InitializeTrust(ctx context.Context, tr capacitysnapshot.Trust) error {
	s.trust = tr
	return s.capacityAssemblyStore.InitializeTrust(ctx, tr)
}
func TestRuntimeCapacitySharedVolumeReachesOriginalStaleOwner(t *testing.T) {
	events := []string{}
	q := &capacityAssemblyQualification{events: &events}
	store := &sharedVolumeOwnerCapture{capacityAssemblyStore: capacityAssemblyStore{events: &events}}
	g := config.M1SharedVolumeCapacityGuarantee()
	tr := capacitysnapshot.Trust{AdmissionPolicy: &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}}
	assembly := initialCapacityAssembly{
		qualification: func(context.Context, string, capacitysnapshot.Trust) (initialDeploymentQualification, error) {
			return q, nil
		},
		reader:   func(string) (*capacitysnapshot.Reader, error) { return nil, nil },
		observer: func(capacitysnapshot.Trust) (*capacityobserver.Checker, error) { return nil, nil },
		store:    func(context.Context, *pgxpool.Pool) (initialCapacityStore, error) { return store, nil },
	}
	source, err := assembleInitialCapacitySource(context.Background(), Config{WorkerImage: "worker"}, nil, tr, assembly)
	if err != nil {
		t.Fatal(err)
	}
	if !store.initialized || store.trust.AdmissionPolicy == nil || *store.trust.AdmissionPolicy != *tr.AdmissionPolicy {
		t.Fatal("assembly lost exact guarantee before original stale owner")
	}
	// Assembly alone supplies no constructed IPC checker and cannot authorize Start.
	if initialCreatedLayerChecker(source).CheckCreatedLayer(context.Background(), workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{}) == nil {
		t.Fatal("stale assembly authorized Start")
	}
}
