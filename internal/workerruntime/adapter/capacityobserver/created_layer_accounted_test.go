package capacityobserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// These are synthetic adapter observations, never native/IPC provenance proof.
func TestCreatedLayerAccountedObserverPreservesRecordsAndPolicy(t *testing.T) {
	for _, accounted := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			hard, used uint64
			reject     bool
		}{
			{"positive", 7, 6, false}, {"equal", 7, 7, false}, {"zero", 0, 0, false}, {"zero_used", 0, math.MaxUint64, false}, {"over", 7, 8, true}, {"full_uint64", math.MaxUint64, math.MaxUint64, false},
		} {
			t.Run(fmt.Sprintf("accounted_%t/%s", accounted, tc.name), func(t *testing.T) {
				tr, req, id, c := observerFixture(t)
				if accounted {
					guarantee := platformconfig.M1AccountedCapacityGuarantee()
					tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: guarantee.ID, Revision: guarantee.Revision, Digest: guarantee.Digest}
					tr.ManifestDigest = capacitysnapshot.Digest([]byte("distinct accounted observer manifest"))
				}
				b := createdlayeripc.Binding{ManifestDigest: tr.ManifestDigest, ContainerID: id.ContainerID, RuntimeID: req.RuntimeInstanceID, SessionID: req.WorkerSessionID, RunID: req.WorkerRunID, Generation: req.Generation, ProfileID: req.ResourceProfile.ID, ProfileRevision: req.ResourceProfile.Revision, ProfileDigest: req.ResourceProfile.Digest}
				binary.LittleEndian.PutUint64(c.Physical.TargetQuota[24:32], tc.hard)
				binary.LittleEndian.PutUint64(c.Physical.TargetQuota[48:56], tc.used)
				binary.LittleEndian.PutUint64(c.Physical.DefaultQuota[24:32], math.MaxUint64)
				c.Physical.TargetQuota[111] = 0xa7
				c.Physical.DefaultQuota[111] = 0x5e
				f, err := observationFacts(b, c)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(f.Quota.Target, c.Physical.TargetQuota[:]) || !bytes.Equal(f.Quota.Default0, c.Physical.DefaultQuota[:]) || f.ManifestDigest != tr.ManifestDigest {
					t.Fatal("raw records or exact manifest lost")
				}
				v, err := capacitysnapshot.NewCreatedLayerVerifier(tr)
				if err != nil {
					t.Fatal(err)
				}
				got, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id)
				reject := tc.reject || !accounted && (tc.hard == 0 || tc.hard > math.MaxInt64 || tc.used > math.MaxInt64)
				if (err != nil) != reject {
					t.Fatalf("diagnostic=%+v err=%v", got, err)
				}
				if !reject && (got.HardInodes != tc.hard || got.UsedInodes != tc.used) {
					t.Fatal("counter loss")
				}
				if reject && got != (capacitysnapshot.CreatedLayerInodeObservation{}) {
					t.Fatal("rejected diagnostic escaped")
				}
				checker, err := New(tr)
				if err != nil {
					t.Fatal(err)
				}
				pinned := tr.ManifestDigest
				if accounted {
					tr.AdmissionPolicy.ID = "unknown"
					tr.AdmissionPolicy.Revision++
					tr.AdmissionPolicy.Digest = capacitysnapshot.Digest([]byte("tampered"))
				}
				initialized, calls := 0, 0
				checker.initialize = func() (observeFunc, error) {
					initialized++
					return func(_ context.Context, actual createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error) {
						calls++
						if actual != b || actual.ManifestDigest != pinned {
							t.Fatal("checker did not bind exact manifest/owner")
						}
						return c, nil
					}, nil
				}
				if (checker.CheckCreatedLayer(context.Background(), req, id) != nil) != reject || initialized != 1 || calls != 1 {
					t.Fatal("shared checker policy drift")
				}
				// The mapper detaches buffers in both directions; verification never rewrites them.
				f.Quota.Target[24] ^= 1
				f.Quota.Default0[24] ^= 1
				if binary.LittleEndian.Uint64(c.Physical.TargetQuota[24:32]) != tc.hard || binary.LittleEndian.Uint64(c.Physical.DefaultQuota[24:32]) != math.MaxUint64 {
					t.Fatal("mapped slices alias physical buffers")
				}
			})
		}
	}
}
