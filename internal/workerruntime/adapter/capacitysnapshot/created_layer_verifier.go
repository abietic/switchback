package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"fmt"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var createdContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// CreatedLayerVerifier pins a validated deployment and the production profile.
// Successful finite facts never confer capacity or execution authority.
type CreatedLayerVerifier struct {
	trust      Trust
	profile    platformconfig.RuntimeResourceProfile
	configured bool
}

func NewCreatedLayerVerifier(trust Trust) (*CreatedLayerVerifier, error) {
	if err := ValidateTrust(trust); err != nil {
		return nil, err
	}
	trust.PublicKey = append([]byte(nil), trust.PublicKey...)
	trust.Core = append([]CoreDomain(nil), trust.Core...)
	trust.Storage = append([]StorageDomain(nil), trust.Storage...)
	trust.Coverage = append([]StorageCoverage(nil), trust.Coverage...)
	if trust.AdmissionPolicy != nil {
		policy := *trust.AdmissionPolicy
		trust.AdmissionPolicy = &policy
	}
	profile, ok := platformconfig.ProductionProfileRegistry().RuntimeResource(platformconfig.LocalStandardV1)
	if !ok || profile.ValidateDigest() != nil || profile.WritableLayerBytes != 10<<30 {
		return nil, fmt.Errorf("created-layer server profile unavailable")
	}
	return &CreatedLayerVerifier{trust: trust, profile: profile, configured: true}, nil
}

func (v *CreatedLayerVerifier) ValidateRequest(ctx context.Context, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	if ctx == nil {
		return fmt.Errorf("created-layer context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || !v.configured {
		return fmt.Errorf("created-layer verifier unconfigured")
	}
	if !createdContainerID.MatchString(identity.ContainerID) || !label(request.RuntimeInstanceID) || !label(request.WorkerSessionID) || !label(request.WorkerRunID) || request.Generation == 0 || request.Generation > math.MaxInt64 || identity.RuntimeInstanceID != request.RuntimeInstanceID || identity.WorkerSessionID != request.WorkerSessionID || identity.Generation != request.Generation {
		return fmt.Errorf("created-layer request identity invalid")
	}
	ref := request.ResourceProfile
	if ref.ID != v.profile.ID || ref.Revision != v.profile.Revision || ref.Digest != v.profile.Digest {
		return fmt.Errorf("created-layer request profile invalid")
	}
	return nil
}

// CreatedLayerInodeObservation reports finite counters only. It does not prove a
// signature, native deployment, remaining capacity or permission to execute.
type CreatedLayerInodeObservation struct {
	HardInodes uint64
	UsedInodes uint64
	Assessment InodeEvidenceAssessment
}

func (v *CreatedLayerVerifier) VerifyCreatedLayerFacts(ctx context.Context, f CreatedLayerFacts, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	_, err := v.AssessCreatedLayerFacts(ctx, f, request, identity)
	return err
}

// AssessCreatedLayerFacts performs the full finite-fact checks before exposing
// counters. Every rejected request or observation returns a zero diagnostic.
func (v *CreatedLayerVerifier) AssessCreatedLayerFacts(ctx context.Context, f CreatedLayerFacts, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) (CreatedLayerInodeObservation, error) {
	if err := v.ValidateRequest(ctx, request, identity); err != nil {
		return CreatedLayerInodeObservation{}, err
	}
	observation, err := v.assessFacts(f, request, identity, time.Now())
	if cancel := ctx.Err(); cancel != nil {
		return CreatedLayerInodeObservation{}, cancel
	}
	return observation, err
}

func (v *CreatedLayerVerifier) verifyFacts(f CreatedLayerFacts, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity, now time.Time) error {
	_, err := v.assessFacts(f, request, identity, now)
	return err
}

func (v *CreatedLayerVerifier) assessFacts(f CreatedLayerFacts, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity, now time.Time) (CreatedLayerInodeObservation, error) {
	if f.Identity != v.trust.Identity || f.ManifestDigest != v.trust.ManifestDigest || f.ContainerID != identity.ContainerID || f.RuntimeID != request.RuntimeInstanceID || f.SessionID != request.WorkerSessionID || f.WorkerRunID != request.WorkerRunID || f.Generation != request.Generation || f.ProfileID != v.profile.ID || f.ProfileRevision != v.profile.Revision || f.ProfileDigest != v.profile.Digest {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer owner or deployment identity drift")
	}
	// Same server freshness policy as running snapshots: 30s, at most 2s future.
	// A positive bounded interval describes finite collection, not an atomic FS snapshot.
	if f.SampleStartedAt.IsZero() || f.ObservedAt.IsZero() || !f.ObservedAt.After(f.SampleStartedAt) || f.ObservedAt.Sub(f.SampleStartedAt) > 30*time.Second || now.Sub(f.SampleStartedAt) > 30*time.Second || f.SampleStartedAt.After(now.Add(2*time.Second)) || f.ObservedAt.After(now.Add(2*time.Second)) {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer observation stale or invalid")
	}
	state := f.State
	if f.InspectSource != "docker_engine_inspect_v1" || f.GraphDriver != "overlay2" || state.Status != "created" || state.PID != 0 || state.Running || state.Paused || state.Restarting || state.Dead || !state.StartedAt.IsZero() {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer container already started or unknown")
	}
	prefix := filepath.Join(v.trust.Identity.DockerRoot, "overlay2") + "/"
	if !absolute(f.UpperDir) || !strings.HasPrefix(f.UpperDir, prefix) {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer UpperDir invalid")
	}
	tail := strings.TrimPrefix(f.UpperDir, prefix)
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || !label(parts[0]) || parts[0] == "l" || parts[0] == "." || parts[0] == ".." || parts[1] != "diff" || f.Parent.Path != filepath.Dir(f.UpperDir) || f.Diff.Path != f.UpperDir {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer parent/diff mapping invalid")
	}
	parent, err := v.checkNode(f.Parent)
	if err != nil {
		return CreatedLayerInodeObservation{}, err
	}
	diff, err := v.checkNode(f.Diff)
	if err != nil {
		return CreatedLayerInodeObservation{}, err
	}
	if parent != diff || f.Parent.Inode == f.Diff.Inode {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer inheritance or inode identity invalid")
	}
	// Static ancestor projects may differ; path ancestry does not prove coverage.
	for _, d := range v.trust.Storage {
		if d.ProjectID == uint64(parent) {
			return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer overlaps static quota project")
		}
	}
	return v.assessQuota(f.Quota, parent)
}

func (v *CreatedLayerVerifier) checkNode(n CreatedLayerNode) (uint32, error) {
	i := v.trust.Identity
	device := strconv.FormatUint(n.DeviceMajor, 10) + ":" + strconv.FormatUint(n.DeviceMinor, 10)
	if !absolute(n.Path) || n.FSUUID != i.FSUUID || n.MountID != i.MountID || n.Device != i.Device || device != i.Device || n.FSType != "xfs" || n.Inode == 0 || !bounded(n.Inode) || n.DeviceMajor > math.MaxUint32 || n.DeviceMinor > math.MaxUint32 || n.Mode > math.MaxUint32 || n.Mode&0170000 != 0040000 || n.UID > math.MaxUint32 || n.GID > math.MaxUint32 || !n.NoFollow || !n.NoSymlinkAncestors || !n.MountStable || len(n.RawFSXAttr) != 28 {
		return 0, fmt.Errorf("created-layer physical inode or mount identity invalid")
	}
	flags := binary.LittleEndian.Uint32(n.RawFSXAttr[0:4])
	project := binary.LittleEndian.Uint32(n.RawFSXAttr[12:16])
	if project == 0 || flags&0x200 == 0 || flags&0x101 != 0 {
		return 0, fmt.Errorf("created-layer project inheritance invalid")
	}
	return project, nil
}

func (v *CreatedLayerVerifier) assessQuota(q CreatedLayerQuota, project uint32) (CreatedLayerInodeObservation, error) {
	i := v.trust.Identity
	if q.Format != "linux_xfs_quotactl_v1" || q.StateSource != "Q_XGETQSTATV" || q.StateVersion != 1 || q.StateFlags > math.MaxUint16 || q.StateFlags&^uint64(0x3f) != 0 || q.StateFlags&0x30 != 0x30 || q.QuotaSource != "Q_XGETQUOTA" || q.ABI != CreatedLayerQuotaABI || q.FSUUID != i.FSUUID || q.MountID != i.MountID || q.Device != i.Device || !createdQuotaRecord(q.Target, project) || !createdQuotaRecord(q.Default0, 0) {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer raw quota identity or enforcement invalid")
	}
	target := q.Target
	hard := binary.LittleEndian.Uint64(target[8:16])
	soft := binary.LittleEndian.Uint64(target[16:24])
	used := binary.LittleEndian.Uint64(target[40:48])
	inodeHard := binary.LittleEndian.Uint64(target[24:32])
	inodeUsed := binary.LittleEndian.Uint64(target[48:56])
	if hard > math.MaxInt64/512 || hard*512 != uint64(v.profile.WritableLayerBytes) || soft != hard || used > hard {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer byte quota mismatch")
	}
	observation := CreatedLayerInodeObservation{
		HardInodes: inodeHard,
		UsedInodes: inodeUsed,
		Assessment: AssessInodeEvidence(StorageQuotaV2{HardInodes: inodeHard, UsedInodes: inodeUsed}),
	}
	// A nonnil policy was validated against the exact registry and copied by
	// the constructor. Raw counters and default0 cannot select this branch.
	if v.trust.AdmissionPolicy == nil {
		if !positive(inodeHard) || !bounded(inodeUsed) || inodeUsed > inodeHard {
			return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer target inode hard limit unproven or exhausted")
		}
	} else if observation.Assessment == InodesOverLimit {
		return CreatedLayerInodeObservation{}, fmt.Errorf("created-layer target inode hard limit exhausted")
	}
	return observation, nil
}
func createdQuotaRecord(raw []byte, project uint32) bool {
	return len(raw) == 112 && raw[0] == 1 && raw[1] == 2 && binary.LittleEndian.Uint32(raw[4:8]) == project
}
