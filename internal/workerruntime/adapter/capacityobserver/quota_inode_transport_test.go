package capacityobserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func TestObservationInodeRawTranslationAndSeparateCreatedGate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hard, used uint64
		reject     bool
	}{{"finite", 10000, 2, false}, {"hard_zero_default_positive", 0, 17, true}, {"over_limit", 10000, 10001, true}, {"max_counter", ^uint64(0), ^uint64(0), true}} {
		t.Run(tc.name, func(t *testing.T) {
			tr, req, id, c := observerFixture(t)
			b := createdlayeripc.Binding{ManifestDigest: tr.ManifestDigest, ContainerID: id.ContainerID, RuntimeID: req.RuntimeInstanceID, SessionID: req.WorkerSessionID, RunID: req.WorkerRunID, Generation: req.Generation, ProfileID: req.ResourceProfile.ID, ProfileRevision: req.ResourceProfile.Revision, ProfileDigest: req.ResourceProfile.Digest}
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[24:32], tc.hard)
			binary.LittleEndian.PutUint64(c.Physical.TargetQuota[48:56], tc.used)
			c.Physical.TargetQuota[111] = 0xa7
			c.Physical.DefaultQuota[111] = 0x5e
			f, err := observationFacts(b, c)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(f.Quota.Target, c.Physical.TargetQuota[:]) || !bytes.Equal(f.Quota.Default0, c.Physical.DefaultQuota[:]) || f.Identity != tr.Identity || binary.LittleEndian.Uint64(f.Quota.Default0[24:32]) != 10000 {
				t.Fatal("translation rewrote target/default bytes or identity")
			}
			// Offline evidence accepts true zero/over-limit counters without relaxing the existing gate.
			q := inodeQuotaFixture(tc.hard, tc.used)
			q.RawHex = hex.EncodeToString(c.Physical.TargetQuota[:])
			*q.HardBlocks = 20971520
			observation, err := MapQuotaInodeEvidence(q, 731)
			if err != nil {
				t.Fatal(err)
			}
			assessment := capacitysnapshot.AssessInodeEvidence(capacitysnapshot.StorageQuotaV2{HardInodes: observation.HardInodes, UsedInodes: observation.UsedInodes})
			if tc.hard == 0 && assessment != capacitysnapshot.InodeBoundUnproven {
				t.Fatal("zero target gained bound from default")
			}
			v, err := capacitysnapshot.NewCreatedLayerVerifier(tr)
			if err != nil {
				t.Fatal(err)
			}
			err = v.VerifyCreatedLayerFacts(context.Background(), f, req, id)
			if (err != nil) != tc.reject {
				t.Fatalf("created-layer policy changed: %v", err)
			}
			// Returned slices are detached in both directions.
			f.Quota.Target[24] ^= 1
			f.Quota.Default0[24] ^= 1
			if binary.LittleEndian.Uint64(c.Physical.TargetQuota[24:32]) != tc.hard || binary.LittleEndian.Uint64(c.Physical.DefaultQuota[24:32]) != 10000 {
				t.Fatal("translation aliased source")
			}
		})
	}
}
