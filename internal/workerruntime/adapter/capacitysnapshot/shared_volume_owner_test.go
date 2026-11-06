package capacitysnapshot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sharedCreatedFixture(t *testing.T) (Trust, ed25519.PrivateKey, CreatedLayerFacts, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) {
	t.Helper()
	tr, key, f, req, id := accountedCreatedFixture(t)
	g := config.M1SharedVolumeCapacityGuarantee()
	tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	tr.ManifestDigest = Digest([]byte("distinct shared-volume created-layer manifest"))
	f.ManifestDigest = tr.ManifestDigest
	return tr, key, f, req, id
}
func TestCreatedLayerSharedVolumeDiagnosticCounterMatrix(t *testing.T) {
	for _, policy := range []string{"accounted", "legacy"} {
		for _, tc := range []struct {
			name       string
			hard, used uint64
			assessment InodeEvidenceAssessment
			reject     bool
		}{
			{"zero_zero", 0, 0, InodeBoundUnproven, false},
			{"zero_used", 0, 17, InodeBoundUnproven, false},
			{"zero_max", 0, math.MaxUint64, InodeBoundUnproven, false},
			{"positive_below", 7, 6, InodesObserved, false},
			{"positive_equal", 7, 7, InodesObserved, false},
			{"positive_over", 7, 8, InodesOverLimit, true},
			{"maxint_equal", math.MaxInt64, math.MaxInt64, InodesObserved, false},
			{"beyond_int_equal", math.MaxInt64 + 1, math.MaxInt64 + 1, InodesObserved, false},
			{"beyond_int_below", math.MaxInt64 + 1, 1, InodesObserved, false},
			{"max_equal", math.MaxUint64, math.MaxUint64, InodesObserved, false},
			{"max_below", math.MaxUint64, math.MaxInt64 + 1, InodesObserved, false},
			{"beyond_int_over", math.MaxInt64 + 1, math.MaxUint64, InodesOverLimit, true},
		} {
			for _, def := range []uint64{0, 19, math.MaxUint64} {
				t.Run(fmt.Sprintf("%s/%s/default_%d", policy, tc.name, def), func(t *testing.T) {
					tr, _, f, req, id := sharedCreatedFixture(t)
					if policy == "legacy" {
						tr.AdmissionPolicy = nil
					}
					binary.LittleEndian.PutUint64(f.Quota.Target[24:32], tc.hard)
					binary.LittleEndian.PutUint64(f.Quota.Target[48:56], tc.used)
					binary.LittleEndian.PutUint64(f.Quota.Default0[24:32], def)
					binary.LittleEndian.PutUint64(f.Quota.Default0[48:56], math.MaxUint64)
					f.Quota.Target[111] = 0xa7
					f.Quota.Default0[111] = 0x5e
					target, default0 := bytes.Clone(f.Quota.Target), bytes.Clone(f.Quota.Default0)
					v, err := NewCreatedLayerVerifier(tr)
					if err != nil {
						t.Fatal(err)
					}
					got, err := v.AssessCreatedLayerFacts(context.Background(), f, req, id)
					reject := tc.reject || policy == "legacy" && (tc.hard == 0 || tc.hard > math.MaxInt64 || tc.used > math.MaxInt64)
					if (err != nil) != reject {
						t.Fatalf("reject=%v got=%+v err=%v", reject, got, err)
					}
					if reject {
						if got != (CreatedLayerInodeObservation{}) {
							t.Fatalf("failed evidence leaked %+v", got)
						}
					} else if got != (CreatedLayerInodeObservation{HardInodes: tc.hard, UsedInodes: tc.used, Assessment: tc.assessment}) {
						t.Fatalf("counter loss: %+v", got)
					}
					if (v.VerifyCreatedLayerFacts(context.Background(), f, req, id) != nil) != reject {
						t.Fatal("error-only policy drift")
					}
					if !bytes.Equal(target, f.Quota.Target) || !bytes.Equal(default0, f.Quota.Default0) {
						t.Fatal("raw records mutated")
					}
				})
			}
		}
	}
}

func TestCreatedLayerSharedVolumeSignedPathRetainsSchemaDomainAndManifest(t *testing.T) {
	for _, name := range []string{"valid", "old_manifest", "running_domain", "running_version", "other_key", "unknown_field", "missing_field", "reencoded_payload"} {
		t.Run(name, func(t *testing.T) {
			tr, key, f, req, id := sharedCreatedFixture(t)
			binary.LittleEndian.PutUint64(f.Quota.Target[24:32], 0)
			binary.LittleEndian.PutUint64(f.Quota.Target[48:56], math.MaxUint64)
			dir := createdTempDir(t)
			reader, err := NewCreatedLayerReader(dir, tr)
			if err != nil {
				t.Fatal(err)
			}
			// Mutating caller reference cannot modify the reader's policy.
			tr.AdmissionPolicy.ID = "unknown"
			if name == "old_manifest" {
				legacy, _, _, _, _ := createdFixture(t)
				f.ManifestDigest = legacy.ManifestDigest
			}
			payload, _ := json.Marshal(f)
			if name == "unknown_field" {
				payload = []byte(strings.Replace(string(payload), `"pid":0,`, `"pid":0,"extra":1,`, 1))
			}
			if name == "missing_field" {
				payload = []byte(strings.Replace(string(payload), `"pid":0,`, "", 1))
			}
			raw := signedCreated(t, key, payload)
			if name == "running_domain" || name == "running_version" || name == "other_key" || name == "reencoded_payload" {
				var e CreatedLayerEnvelope
				json.Unmarshal(raw, &e)
				switch name {
				case "running_domain":
					e.Signature = ed25519.Sign(key, append([]byte(SignatureDomain), payload...))
				case "running_version":
					e.Version = Version
				case "other_key":
					e.KeyID = "other"
				case "reencoded_payload":
					e.Payload, _ = json.MarshalIndent(f, "", "  ")
				}
				raw, _ = json.Marshal(e)
			}
			writeCreated(t, dir, id.ContainerID, raw)
			err = reader.CheckCreatedLayer(context.Background(), req, id)
			if (err != nil) != (name != "valid") {
				t.Fatalf("signed path %s: %v", name, err)
			}
		})
	}
}

func TestCreatedLayerSharedVolumeCoordinatorPreservesAuthorityBeforeOneStart(t *testing.T) {
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
			tr, key, f, req, id := sharedCreatedFixture(t)
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

// The guarantee remains manifest metadata, never a new signed raw field.
func TestSharedVolumeSignedRawManifestBoundary(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(fmt.Sprint(v2), func(t *testing.T) {
			var tr Trust
			var key ed25519.PrivateKey
			var payload []byte
			version, domain := Version, SignatureDomain
			if v2 {
				tr, key, payload = inodeV2Fixture(t)
				version, domain = VersionV2, SignatureDomainV2
			} else {
				var f Facts
				tr, key, f = fixture(t)
				payload = admissionMarshal(t, f)
			}
			oldManifest := tr.ManifestDigest
			g := config.M1SharedVolumeCapacityGuarantee()
			tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
			// Bind a complete original manifest to this actual signing key and identity.
			_, manifest, _ := admissionFixture(t)
			manifest.Guarantee = *tr.AdmissionPolicy
			manifest.Identity = tr.Identity
			manifest.KeyID = tr.KeyID
			manifest.PublicKey = tr.PublicKey
			manifestRaw := admissionMarshal(t, manifest)
			tr.ManifestDigest = Digest(manifestRaw)
			if _, err := ParseAdmissionManifest(manifestRaw, tr); err != nil {
				t.Fatal(err)
			}
			verify := func(raw []byte, trust Trust) error {
				if v2 {
					_, err := VerifyEvidenceV2(raw, trust)
					return err
				}
				verified, err := Verify(raw, trust)
				if err != nil {
					return err
				}
				return CheckFacts(verified.Facts(), trust)
			}
			oldSigned := admissionSigned(t, key, payload, version, domain)
			if verify(oldSigned, tr) == nil {
				t.Fatal("old manifest silently acquired shared-volume guarantee")
			}
			payload = []byte(strings.Replace(string(payload), oldManifest, tr.ManifestDigest, 1))
			raw := admissionSigned(t, key, payload, version, domain)
			if err := verify(raw, tr); err != nil {
				t.Fatal(err)
			}
			oldTrust := tr
			oldTrust.ManifestDigest = oldManifest
			oldTrust.AdmissionPolicy = knownAdmissionPolicy()
			if verify(raw, oldTrust) == nil {
				t.Fatal("shared manifest accepted by old pinned Trust")
			}
			mutated := manifest
			mutated.Guarantee = *knownAdmissionPolicy()
			if _, err := ParseAdmissionManifest(admissionMarshal(t, mutated), tr); err == nil {
				t.Fatal("old guarantee substituted into new manifest")
			}
			injected := []byte(strings.TrimSuffix(string(payload), "}") + `,"admission_policy":` + string(admissionMarshal(t, tr.AdmissionPolicy)) + `}`)
			if verify(admissionSigned(t, key, injected, version, domain), tr) == nil {
				t.Fatal("raw schema acquired admission policy field")
			}
		})
	}
}
