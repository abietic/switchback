package capacitysnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
)

type qualificationStageOps struct {
	read    func(context.Context, string, any) error
	put     func(context.Context, string, any) error
	claim   func(context.Context, string) error
	boot    func(context.Context) (string, error)
	mono    func() (int64, error)
	now     func() time.Time
	observe func(context.Context, qualificationRole) (qualificationFrozenProcess, error)
	inspect func(context.Context, qualificationRole, qualificationFrozenProcess, string, string) error
	hash    func(context.Context, string, int64) (string, error)
}

func qualificationNativeStageOps(dir qualificationCandidateDirectory) qualificationStageOps {
	return qualificationStageOps{
		read: dir.decode, put: dir.put, claim: dir.claim,
		boot: qualificationGuestBoot, mono: qualificationMonotonicNS, now: time.Now,
		observe: qualificationObserveFrozenRole, inspect: qualificationInspectFrozenContainer,
		hash: qualificationHashFile,
	}
}

// BeginNativeQualification is run once immediately before the first final
// waiting role Start. Its monotonic origin is immutable across subcommands.
func BeginNativeQualification(ctx context.Context) error {
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return err
	}
	defer dir.close()
	return beginNativeQualification(ctx, qualificationNativeStageOps(dir))
}

func beginNativeQualification(ctx context.Context, k qualificationStageOps) error {
	if err := k.claim(ctx, "begin"); err != nil {
		return err
	}
	var staged qualificationStaged
	if err := k.read(ctx, "staged.json", &staged); err != nil {
		return err
	}
	if err := qualificationValidateStaged(staged); err != nil {
		return err
	}
	if err := qualificationValidateCreateBoundStaged(staged); err != nil {
		return err
	}
	if _, err := qualificationValidateStaticIntent(ctx, k.read, staged); err != nil {
		return err
	}
	boot, err := k.boot(ctx)
	if err != nil {
		return err
	}
	mono, err := k.mono()
	if err != nil {
		return err
	}
	if mono <= 0 || !qualificationUUIDPattern.MatchString(boot) {
		return errQualification
	}
	w := qualificationWindowOpen{
		Schema: windowOpenSchema, ID: staged.WindowID, Nonce: staged.Nonce,
		StagedDigest: qualificationDigest(staged), GuestBoot: boot,
		Started: k.now().UTC().Format(time.RFC3339Nano), MonotonicNS: mono,
	}
	if _, err := qualificationRemaining(w, boot, mono, k.now()); err != nil {
		return err
	}
	return k.put(ctx, "window-open.json", w)
}

func qualificationValidateStaged(s qualificationStaged) error {
	if s.Schema != stagedSchema || !qualificationWindowLabel(s.WindowID) ||
		!containerIDPattern.MatchString(s.Nonce) || s.TestProject == 0 || s.TestProject >= 0xfffffffe ||
		s.Source.Schema != "switchback.capacity.source_manifest.v1" ||
		s.Inventory.Schema != DeploymentInventoryVersion || s.Trust.AdmissionPolicy == nil ||
		len(s.Source.Inputs.Roles) == 0 || len(s.Source.Inputs.Roles) > 32 ||
		len(s.Artifacts) == 0 || len(s.Artifacts) > 32 || len(s.Writers) != len(qualificationWriterIDs) {
		return errQualification
	}
	if s.Trust.ManifestDigest != "" || s.Binding.WorkerImageDigest != s.Inventory.WorkerImageDigest {
		return errQualification
	}
	// Staging never declares an expected runtime mount root/device/ID. Prepare
	// fills these from actual waiting process mountinfo exactly once.
	for _, role := range s.Source.Inputs.Roles {
		for _, m := range role.Mounts {
			if m.Root != "" || m.Device != "" || m.MountID != 0 {
				return errQualification
			}
		}
	}
	return nil
}

func qualificationValidateCreateBoundStaged(s qualificationStaged) error {
	for _, role := range s.Source.Inputs.Roles {
		if role.Kind == "container" && (!containerIDPattern.MatchString(role.ContainerID) || !absolute(role.CgroupPath) ||
			role.CgroupPath != qualificationContainerCgroupPath(filepath.Dir(role.CgroupPath), role.ContainerID)) {
			return errQualification
		}
	}
	for _, binding := range s.Binding.Core {
		if !containerIDPattern.MatchString(binding.ContainerID) {
			return errQualification
		}
	}
	return nil
}

// PrepareNativeQualification observes the already waiting final processes and
// freezes their actual mount IDs, roots and devices in one candidate write.
func PrepareNativeQualification(ctx context.Context) error {
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return err
	}
	defer dir.close()
	return prepareNativeQualification(ctx, qualificationNativeStageOps(dir))
}

func prepareNativeQualification(ctx context.Context, k qualificationStageOps) error {
	if err := k.claim(ctx, "prepare"); err != nil {
		return err
	}
	var s qualificationStaged
	var w qualificationWindowOpen
	if err := k.read(ctx, "staged.json", &s); err != nil {
		return err
	}
	if err := k.read(ctx, "window-open.json", &w); err != nil {
		return err
	}
	if err := qualificationValidateStaged(s); err != nil || qualificationValidateCreateBoundStaged(s) != nil || w.ID != s.WindowID || w.Nonce != s.Nonce || w.StagedDigest != qualificationDigest(s) {
		return errQualification
	}
	staticDigest, err := qualificationValidateStaticIntent(ctx, k.read, s)
	if err != nil {
		return err
	}
	boot, err := k.boot(ctx)
	if err != nil {
		return err
	}
	mono, err := k.mono()
	if err != nil {
		return err
	}
	remaining, err := qualificationRemaining(w, boot, mono, k.now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := qualificationCheckStagedArtifacts(ctx, s, k.hash); err != nil {
		return err
	}
	c := qualificationCandidateRecord{Schema: candidateSchema, OpenDigest: qualificationDigest(w), StagedDigest: qualificationDigest(s), StaticIntentDigest: staticDigest, TestProject: s.TestProject, Source: qualificationClone(s.Source), Inventory: qualificationClone(s.Inventory), Trust: qualificationClone(s.Trust), Binding: qualificationClone(s.Binding)}
	for i := range c.Source.Inputs.Roles {
		role := &c.Source.Inputs.Roles[i]
		process, err := k.observe(ctx, *role)
		if err != nil {
			return fmt.Errorf("qualification role %s absent: %w", role.Role, err)
		}
		if err := k.inspect(ctx, *role, process, w.Nonce, staticDigest); err != nil {
			return fmt.Errorf("qualification role %s inspect: %w", role.Role, err)
		}
		if process.Role != role.Role || process.PID == 0 || process.StartTicks == 0 || process.Snapshot.Boot != boot || process.Snapshot.ExecutableDigest != role.BinaryDigest {
			return errQualification
		}
		p, err := procstatus.Parse(process.Snapshot.Status)
		if err != nil || !roleMatchesUID(p, role.UID, role.Capabilities) || uint64(p.NoNewPrivs) != role.NoNewPrivileges || uint64(p.Seccomp) != role.Seccomp ||
			string(process.Snapshot.Cgroup) != "0::"+strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup")+"\n" {
			return errQualification
		}
		mounts, err := qualificationMountInfo(process.Snapshot.MountInfo)
		if err != nil {
			return err
		}
		for j := range role.Mounts {
			frozen := &role.Mounts[j]
			actual, ok := mounts[frozen.Target]
			if !ok || actual.ReadOnly != frozen.ReadOnly || actual.Device == "" || actual.Root == "" || actual.MountID == 0 {
				return errQualification
			}
			frozen.Device, frozen.Root, frozen.MountID = actual.Device, actual.Root, actual.MountID
		}
		c.Processes = append(c.Processes, process)
	}
	c.Inventory.DeploymentInputsDigest = qualificationDigest(c.Source.Inputs)
	c.Inventory.SourceManifestDigest = qualificationDigest(c.Source)
	manifest, err := qualificationManifestPrefix(c)
	if err != nil {
		return err
	}
	if err := validateQualificationSource(c.Source, c.Inventory, c.Trust, c.Binding.Core); err != nil {
		return err
	}
	if err := validateQualificationV2Source(c.Source, c.Inventory); err != nil {
		return err
	}
	c.Fingerprint, err = qualificationCandidate(c.Trust, manifest, c.Binding)
	if err != nil {
		return err
	}
	if _, err := qualificationRemaining(w, boot, mustQualificationMono(k.mono), k.now()); err != nil {
		return err
	}
	return k.put(ctx, "candidate.json", c)
}

func mustQualificationMono(read func() (int64, error)) int64 {
	n, err := read()
	if err != nil {
		return -1
	}
	return n
}

func qualificationCheckStagedArtifacts(ctx context.Context, s qualificationStaged, hash func(context.Context, string, int64) (string, error)) error {
	builds := map[string]qualificationBuild{}
	for _, b := range s.Source.Builds {
		builds[b.Role] = b
	}
	seen := map[string]bool{}
	for _, artifact := range s.Artifacts {
		build, ok := builds[artifact.Role]
		if !ok || seen[artifact.Role] || !absolute(artifact.Path) || build.BinaryDigest != artifact.Digest {
			return errQualification
		}
		actual, err := hash(ctx, artifact.Path, 512<<20)
		if err != nil || actual != artifact.Digest {
			return errQualification
		}
		seen[artifact.Role] = true
	}
	for _, build := range s.Source.Builds {
		if build.Role != "host-producer" && !strings.HasPrefix(build.Role, "writer-") && !seen[build.Role] {
			return errQualification
		}
	}
	return qualificationContext(ctx)
}

func qualificationOwnExecutable(ctx context.Context, source qualificationSource, role string) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	digest, err := qualificationHashFile(ctx, path, 512<<20)
	if err != nil {
		return err
	}
	for _, build := range source.Builds {
		if build.Role == role && build.BinaryDigest == digest {
			return nil
		}
	}
	return errQualification
}
