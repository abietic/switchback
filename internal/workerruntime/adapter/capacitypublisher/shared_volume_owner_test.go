package capacitypublisher

import (
	"context"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"math"
	"reflect"
	"testing"
)

func TestMapperSharedVolumeActualSequenceAndAccountingInputs(t *testing.T) {
	tr, c, o := fixture(false)
	g := config.M1SharedVolumeCapacityGuarantee()
	tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	tr.ManifestDigest = capacitysnapshot.Digest([]byte("shared-volume mapper manifest"))
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 7)
	if e != nil {
		t.Fatal(e)
	}
	expectedStorage := append([]capacitysnapshot.StorageDomain(nil), tr.Storage...)
	for n := range expectedStorage {
		expectedStorage[n].UsedBytes = 4096
		expectedStorage[n].Quota.UsedBlocks512 = 8
	}
	expected := capacitysnapshot.Facts{Sequence: 7, ObservedAt: o.Started, Identity: tr.Identity, ManifestDigest: tr.ManifestDigest, EngineCPUMilli: 8000, EngineMemoryBytes: 34359738368, Anchor: capacitysnapshot.Anchor{Path: "/pool/anchor", FSUUID: syntheticUUID, MountID: 73, Device: "8:7", FSType: "xfs", MountOptions: []string{"rw", "prjquota"}, AvailableBytes: 17179865088}, Core: tr.Core, Storage: expectedStorage, Containers: []capacitysnapshot.Container{}, Removals: []capacitysnapshot.Removal{}}
	if !reflect.DeepEqual(r.Facts, expected) {
		t.Fatalf("facts mismatch: %#v", r.Facts)
	}
	for _, id := range []string{"core-data", "product"} {
		if r.StorageInodes[id] != (capacitysnapshot.CreatedLayerInodeObservation{HardInodes: 0, UsedInodes: math.MaxUint64, Assessment: capacitysnapshot.InodeBoundUnproven}) {
			t.Fatalf("inode %s: %#v", id, r.StorageInodes[id])
		}
	}
	if len(r.LayerInodes) != 0 || !reflect.DeepEqual(o, before) {
		t.Fatal("source changed or layers invented")
	}
}
