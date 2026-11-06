package localproduct

import (
	"context"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"strings"
	"testing"
)

func TestRuntimeCapacityManifestRequiredWithTrust(t *testing.T) {
	if err := (Config{RuntimeCapacityTrustFile: "/deployment/trust.json", RuntimeCapacitySnapshotDirectory: "/deployment/snapshots"}).validateCapacityPaths(); err == nil {
		t.Fatal("Trust and snapshots without admission manifest accepted")
	}
}

func TestRuntimeCapacityManifestPathsEverySubset(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		c := Config{}
		if mask&1 != 0 {
			c.RuntimeCapacityTrustFile = "/deploy/trust"
		}
		if mask&2 != 0 {
			c.RuntimeCapacitySnapshotDirectory = "/deploy/snapshots"
		}
		if mask&4 != 0 {
			c.RuntimeCapacityAdmissionManifestFile = "/deploy/manifest"
		}
		err := c.validateCapacityPaths()
		if (err == nil) != (mask == 0 || mask == 7) {
			t.Fatalf("path group %d: %v", mask, err)
		}
	}
	for _, p := range []string{"relative", "/", "/deploy/../manifest", " /manifest", "/manifest\n", "/manifest\x00"} {
		c := Config{RuntimeCapacityTrustFile: "/deploy/trust", RuntimeCapacitySnapshotDirectory: "/deploy/snapshots", RuntimeCapacityAdmissionManifestFile: p}
		if err := c.validateCapacityPaths(); err == nil {
			t.Fatalf("invalid manifest path %q accepted", p)
		}
	}
}
func TestRuntimeCapacityQualificationRequiredBeforeReconciliation(t *testing.T) {
	for _, q := range []*capacitysnapshot.DeploymentQualification{nil, {}} {
		if err := checkBackendCapacityRole(context.Background(), q); err == nil {
			t.Fatal("invalid qualification accepted")
		}
	}
	// Nil store/reader would panic if reconciliation were reached.
	if err := (initialCapacitySource{}).checkRuntimeStart(context.Background()); err == nil {
		t.Fatal("missing qualification reached reconciliation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newInitialCapacitySource(ctx, Config{}, nil); err != context.Canceled {
		t.Fatal("constructor lost cancellation", err)
	}
	if err := checkBackendCapacityRole(ctx, &capacitysnapshot.DeploymentQualification{}); err != context.Canceled {
		t.Fatal("role check lost cancellation", err)
	}
}
func TestRuntimeCapacityConfiguredManifestFailsBeforeOriginalStore(t *testing.T) {
	c := Config{RuntimeCapacityTrustFile: "/private/tmp/absent-trust", RuntimeCapacitySnapshotDirectory: "/private/tmp/snapshots", RuntimeCapacityAdmissionManifestFile: "/private/tmp/absent-manifest"}
	source, err := newInitialCapacitySource(context.Background(), c, nil)
	if err == nil || source != nil || strings.Contains(err.Error(), "store") {
		t.Fatal("configured invalid files reached original store", err)
	}
	if err := initialCreatedLayerChecker(source).CheckCreatedLayer(context.Background(), workerapp.RuntimeProvisionRequest{}, workerapp.CreatedRuntimeIdentity{}); err == nil {
		t.Fatal("failed assembly created checker authority")
	}
}
func TestRuntimeCapacityTrustMustBeActualFrozenSibling(t *testing.T) {
	c := Config{RuntimeCapacityTrustFile: "/other/trust.json", RuntimeCapacityAdmissionManifestFile: "/deployment/admission-manifest.json", RuntimeCapacitySnapshotDirectory: "/deployment/snapshots"}
	if _, e := newInitialCapacitySource(context.Background(), c, nil); e == nil || !strings.Contains(e.Error(), "fixed qualification sibling") {
		t.Fatal("different configured trust path accepted", e)
	}
}
