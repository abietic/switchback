package localproduct

import "testing"

func TestRuntimeCapacityConfigPaths(t *testing.T) {
	for _, c := range []Config{{RuntimeCapacityTrustFile: "/trust"}, {RuntimeCapacitySnapshotDirectory: "/snapshots"}, {RuntimeCapacityTrustFile: "relative", RuntimeCapacitySnapshotDirectory: "/snapshots"}, {RuntimeCapacityTrustFile: "/a/../trust", RuntimeCapacitySnapshotDirectory: "/snapshots"}, {RuntimeCapacityTrustFile: "/trust", RuntimeCapacitySnapshotDirectory: "/"}} {
		if err := c.validateCapacityPaths(); err == nil {
			t.Fatal("invalid pair accepted")
		}
	}
	for _, c := range []Config{{}, {RuntimeCapacityTrustFile: "/trust", RuntimeCapacitySnapshotDirectory: "/snapshots", RuntimeCapacityAdmissionManifestFile: "/manifest"}} {
		if err := c.validateCapacityPaths(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRuntimeCapacityConfigEnvironment(t *testing.T) {
	c, err := LoadConfig(mapLookup(map[string]string{"SWITCHBACK_DATABASE_URL": "postgres://localhost/switchback", "SWITCHBACK_DATA_ROOT": t.TempDir(), "SWITCHBACK_RUNTIME_CAPACITY_TRUST_FILE": "/deployment/trust.json", "SWITCHBACK_RUNTIME_CAPACITY_SNAPSHOT_DIRECTORY": "/deployment/capacity", "SWITCHBACK_RUNTIME_CAPACITY_ADMISSION_MANIFEST_FILE": "/deployment/admission.json"}))
	if err != nil || c.RuntimeCapacityTrustFile != "/deployment/trust.json" || c.RuntimeCapacitySnapshotDirectory != "/deployment/capacity" || c.RuntimeCapacityAdmissionManifestFile != "/deployment/admission.json" {
		t.Fatalf("paired deployment environment was not loaded: %v", err)
	}
}
