package capacitysnapshot

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
)

// InodeEvidenceAssessment describes counters only. Even observed/equal does not
// establish remaining physical capacity, all-quota coverage or permission.
type InodeEvidenceAssessment string

const (
	InodeBoundUnproven InodeEvidenceAssessment = "inode_bound_unproven"
	InodesObserved     InodeEvidenceAssessment = "observed"
	InodesOverLimit    InodeEvidenceAssessment = "over_limit"
)

// VerifyEvidenceV2 authenticates original bytes and checks schema and explicit
// deployment metadata. It does not prove native syscall provenance or admission.
func VerifyEvidenceV2(raw []byte, trust Trust) (VerifiedEvidenceV2, error) {
	if err := ValidateTrust(trust); err != nil {
		return VerifiedEvidenceV2{}, err
	}
	var e Envelope
	if err := strictJSON(raw, &e); err != nil {
		return VerifiedEvidenceV2{}, err
	}
	if e.Version != VersionV2 || e.KeyID != trust.KeyID || len(e.Signature) != ed25519.SignatureSize {
		return VerifiedEvidenceV2{}, fmt.Errorf("unsupported v2 signature envelope")
	}
	if !ed25519.Verify(ed25519.PublicKey(trust.PublicKey), append([]byte(SignatureDomainV2), e.Payload...), e.Signature) {
		return VerifiedEvidenceV2{}, fmt.Errorf("invalid v2 signature")
	}
	var f FactsV2
	if err := strictJSON(e.Payload, &f); err != nil {
		return VerifiedEvidenceV2{}, err
	}
	if f.Sequence <= 0 || f.ObservedAt.IsZero() {
		return VerifiedEvidenceV2{}, fmt.Errorf("invalid sample clock/sequence")
	}
	if err := checkV2DeploymentMetadata(f, trust); err != nil {
		return VerifiedEvidenceV2{}, err
	}
	return VerifiedEvidenceV2{envelope: e, facts: f, raw: bytes.Clone(raw), digest: Digest(raw)}, nil
}

func AssessInodeEvidence(q StorageQuotaV2) InodeEvidenceAssessment {
	if q.HardInodes == 0 {
		return InodeBoundUnproven
	}
	if q.UsedInodes > q.HardInodes {
		return InodesOverLimit
	}
	return InodesObserved
}

// This private projection reuses existing deployment/units validation only. It
// never constructs v1 Verified or escapes as downgraded Facts. The full v2 signed
// payload and all counters remain independently retained by VerifiedEvidenceV2.
func checkV2DeploymentMetadata(f FactsV2, t Trust) error {
	metadata := Facts{Sequence: f.Sequence, ObservedAt: f.ObservedAt, Identity: f.Identity, ManifestDigest: f.ManifestDigest, EngineCPUMilli: f.EngineCPUMilli, EngineMemoryBytes: f.EngineMemoryBytes, Anchor: f.Anchor, Core: f.Core, Removals: f.Removals}
	for _, d := range f.Storage {
		metadata.Storage = append(metadata.Storage, v2DomainMetadata(d))
	}
	for _, c := range f.Containers {
		metadata.Containers = append(metadata.Containers, Container{ContainerID: c.ContainerID, RuntimeID: c.RuntimeID, SessionID: c.SessionID, Generation: c.Generation, ProfileID: c.ProfileID, ProfileRevision: c.ProfileRevision, ProfileDigest: c.ProfileDigest, NanoCPUs: c.NanoCPUs, MemoryMax: c.MemoryMax, PIDsMax: c.PIDsMax, Layer: v2DomainMetadata(c.Layer), CgroupPath: c.CgroupPath, CPUQuotaMicros: c.CPUQuotaMicros, CPUPeriodMicros: c.CPUPeriodMicros})
	}
	return CheckFacts(metadata, t)
}
func v2DomainMetadata(d StorageDomainV2) StorageDomain {
	q := d.Quota
	return StorageDomain{FSUUID: d.FSUUID, Device: d.Device, MountID: d.MountID, FSType: d.FSType, XFlags: d.XFlags, Quota: StorageQuota{Format: q.Format, StateVersion: q.StateVersion, StateFlags: q.StateFlags, Version: q.Version, Flags: q.Flags, ProjectID: q.ProjectID, HardBlocks512: q.HardBlocks512, UsedBlocks512: q.UsedBlocks512}, ID: d.ID, Path: d.Path, ProjectID: d.ProjectID, HardBytes: d.HardBytes, UsedBytes: d.UsedBytes}
}
