package localproduct

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestInitialCreatedLayerSourceKeepsProductionUnavailableWithoutTrust(t *testing.T) {
	policy, err := newInitialCapacitySource(context.Background(), Config{}, nil)
	if err != nil || policy != nil {
		t.Fatalf("missing Trust changed initial source: %v", err)
	}
	for _, source := range []runtimeStartPolicy{policy, initialCapacitySource{}, unavailableRuntimeStartPolicy{}} {
		err := initialCreatedLayerChecker(source).CheckCreatedLayer(context.Background(), workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{})
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("missing concrete deployment checker did not reject: %v", err)
		}
	}
}
func TestInitialCreatedLayerSourceUsesOnlyConstructedConcreteIPCChecker(t *testing.T) {
	// Zero Checker rejects before IPC/key I/O; concrete transport and pinned
	// deployment verification are exercised in the capacityobserver adapter tests.
	source := initialCapacitySource{createdLayer: new(capacityobserver.Checker)}
	err := initialCreatedLayerChecker(source).CheckCreatedLayer(context.Background(), workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{})
	if err == nil || !strings.Contains(err.Error(), "unconfigured") {
		t.Fatalf("concrete IPC checker was not used: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.CheckCreatedLayer(ctx, workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{}); err != context.Canceled {
		t.Fatalf("source lost context cancellation: %v", err)
	}
}

func TestInitialCreatedLayerZeroSourceDirectlyRejectsAndPreservesCancellation(t *testing.T) {
	source := initialCapacitySource{}
	if err := source.CheckCreatedLayer(context.Background(), workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{}); err == nil || !strings.Contains(err.Error(), "unconfigured") {
		t.Fatalf("zero source did not fail closed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.CheckCreatedLayer(ctx, workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{}); err != context.Canceled {
		t.Fatalf("zero source lost cancellation: %v", err)
	}
}
