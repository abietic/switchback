//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

// CollectNativeQualification is the fixed deployment-owned collector. Its
// phase claim is exclusive; any unknown syscall, Engine, helper, or cleanup
// result consumes this window and cannot be resumed by another invocation.
func CollectNativeQualification(ctx context.Context) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errQualification
	}
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return err
	}
	defer dir.close()
	return collectNativeQualification(ctx, dir)
}

func collectNativeQualification(ctx context.Context, dir qualificationCandidateDirectory) error {
	return collectNativeQualificationWith(ctx, dir, qualificationNativeCollectOps{
		architecture: runtime.GOARCH,
		boot:         qualificationGuestBoot,
		mono:         qualificationMonotonicNS,
		now:          time.Now,
		self:         qualificationOwnExecutable,
		hash:         qualificationHashFile,
		waitHost:     qualificationWaitHostBefore,
		sample:       qualificationSampleActualNative,
	})
}

// The production sampler performs every live Engine/proc/syscall operation.
// A private test seam can replace only these raw fact-producing operations;
// the surrounding collect claim/bind/deadline/mapper remains the same.
func qualificationSampleActualNative(ctx context.Context, dir qualificationCandidateDirectory, candidate qualificationCandidateRecord, opened qualificationWindowOpen) (facts qualificationNativeFacts, cleanup qualificationCleanup, sampleErr error) {
	start := time.Now()
	nsStart, err := qualificationNamespaceStart(ctx, candidate)
	if err != nil {
		return facts, cleanup, err
	}
	rootPath, err := qualificationTestRoot(candidate.Trust, opened.ID)
	if err != nil {
		return facts, cleanup, err
	}
	poolPath := filepath.Dir(filepath.Dir(rootPath))
	poolFD, err := qualificationOpenPoolRoot(ctx, poolPath)
	if err != nil {
		return facts, cleanup, err
	}
	defer func() {
		if poolFD >= 0 {
			_ = unix.Close(poolFD)
		}
	}()
	initial, err := qualificationQuotaRead(ctx, poolFD, candidate.TestProject)
	if err != nil || !qualificationUnusedQuota(initial, candidate.TestProject) {
		return facts, cleanup, errQualification
	}
	successor, err := qualificationQuotaRead(ctx, poolFD, candidate.TestProject+1)
	if err != nil || !qualificationUnusedQuota(successor, candidate.TestProject+1) {
		return facts, cleanup, errQualification
	}
	test, err := qualificationCreateTestDirectory(ctx, candidate.Trust, opened.ID)
	if err != nil {
		return facts, cleanup, err
	}
	defer test.close()
	if test.Path != rootPath || test.Identity.MountID != candidate.Trust.Identity.MountID || test.Identity.ProjectID == 0 || test.Identity.ProjectID > math.MaxUint32 {
		return facts, cleanup, errQualification
	}
	mountRoot, err := qualificationWorkerMountRoot(ctx, test.Path, test.Identity)
	if err != nil {
		return facts, cleanup, err
	}
	engine, err := qualificationNewEngine()
	if err != nil {
		return facts, cleanup, err
	}
	defer engine.Close()
	runner := qualificationBackendRunner{engine: engine, dir: dir, candidate: candidate, window: opened, test: test, mountRoot: mountRoot}
	backendStart := time.Now()
	handoff, err := runner.handoffCases(ctx)
	if err != nil {
		return facts, cleanup, err
	}
	backendEnd := time.Now()
	h, err := runner.beginWorkerHandoff(ctx, uint32(test.Identity.ProjectID))
	if err != nil {
		return facts, cleanup, err
	}
	worker := qualificationWorkerRunner{backend: &runner, handoff: &h, poolFD: poolFD}
	quotaStart := time.Now()
	if err := worker.quotaRun(ctx); err != nil {
		return facts, cleanup, err
	}
	quotaEnd := time.Now()
	if err := runner.finishWorkerHandoff(ctx, &h); err != nil {
		return facts, cleanup, err
	}
	if err := qualificationContext(ctx); err != nil {
		return facts, cleanup, err
	}
	// All helper CIDs are already proven gone and all target/child host-view
	// FDs are closed before unlinking the finite test root or reading final
	// project accounting. The separate product-root FD is retained only for
	// quota readback.
	if err := unix.Close(test.Root); err != nil {
		return facts, cleanup, err
	}
	test.Root = -1
	if err := unix.Unlinkat(test.Parent, opened.ID, unix.AT_REMOVEDIR); err != nil {
		return facts, cleanup, err
	}
	if err := unix.Close(test.Parent); err != nil {
		return facts, cleanup, err
	}
	test.Parent = -1
	if len(runner.cleanups) != 9 {
		return facts, cleanup, errQualification
	}
	preFinal, err := qualificationQuotaRead(ctx, poolFD, candidate.TestProject)
	if err != nil || !qualificationOwnedQuotaReadyForReset(preFinal, candidate.TestProject) {
		return facts, cleanup, errQualification
	}
	if err := qualificationQuotaSetHard(ctx, poolFD, candidate.TestProject, 0); err != nil {
		return facts, cleanup, err
	}
	final, err := qualificationQuotaRead(ctx, poolFD, candidate.TestProject)
	if err != nil || !qualificationUnusedQuota(final, candidate.TestProject) {
		return facts, cleanup, errQualification
	}
	cleanup = qualificationCleanup{Complete: true, Project: candidate.TestProject, Initial: initial, Successor: successor, Final: final,
		Helpers: runner.cleanups, RootRemoved: true, FDsClosed: true}
	if err := validateQualificationCleanup(cleanup, candidate.TestProject); err != nil {
		return facts, cleanup, err
	}
	readonlyStart := time.Now()
	namespaces, reach, err := qualificationObserveCoreRoles(ctx, candidate, opened)
	if err != nil {
		return facts, cleanup, err
	}
	namespaces.Namespaces, err = qualificationMergeNamespaceWindow(nsStart, namespaces.Namespaces)
	if err != nil {
		return facts, cleanup, err
	}
	core, err := qualificationReadCoreLimits(ctx, candidate)
	if err != nil {
		return facts, cleanup, err
	}
	loaded, readonlyEnd, err := qualificationLoadedAtEnd(func() (qualificationLoaded, error) {
		return qualificationLoadedFromActual(ctx, candidate, test.Identity, worker.identity, worker.worker)
	}, time.Now)
	if err != nil {
		return facts, cleanup, err
	}
	settersRaw, err := worker.setterPayload()
	if err != nil {
		return facts, cleanup, err
	}
	facts = qualificationNativeFacts{
		Loaded: loaded, Namespaces: namespaces, Core: core, Quota: worker.quota, SettersRaw: settersRaw,
		Handoff: handoff, Reachability: reach, SampleStart: start, ReadonlyStart: readonlyStart, ReadonlyEnd: readonlyEnd,
		BackendStart: backendStart, BackendEnd: backendEnd, QuotaStart: quotaStart, QuotaEnd: quotaEnd,
	}
	return facts, cleanup, qualificationContext(ctx)
}

func qualificationWaitHostBefore(ctx context.Context, dir qualificationCandidateDirectory, candidate qualificationCandidateRecord, opened qualificationWindowOpen) error {
	var before qualificationHostBefore
	for {
		err := dir.decode(ctx, "host-before.json", &before)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := qualificationPoll(ctx); err != nil {
			return err
		}
	}
	if before.Schema != "switchback.qualification.host_before.v1" || before.Candidate != candidate.Fingerprint || before.OpenDigest != candidate.OpenDigest || !qualificationUUIDPattern.MatchString(before.HostBoot) || len(before.Checks) != 2 {
		return errQualification
	}
	start, err := time.Parse(time.RFC3339Nano, opened.Started)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339Nano, before.Started)
	if err != nil || end.Before(start) || end.After(time.Now()) {
		return errQualification
	}
	for i, id := range []string{"diskutil-info-before", "apfs-list-before"} {
		check := before.Checks[i]
		from, e := time.Parse(time.RFC3339Nano, check.Observation.Started)
		to, ee := time.Parse(time.RFC3339Nano, check.Observation.Ended)
		if check.ID != id || check.Observation.WindowID != opened.ID || check.Observation.Source != id || e != nil || ee != nil || from.Before(start) || to.After(end) || !to.After(from) || Digest(check.Observation.Data) != check.Observation.Digest {
			return errQualification
		}
		if _, e := qualificationPlist(check.Observation.Data); e != nil {
			return e
		}
	}
	return qualificationContext(ctx)
}

func qualificationOwnedQuotaReadyForReset(raw []byte, id uint32) bool {
	if !createdQuotaRecord(raw, id) || binary.LittleEndian.Uint64(raw[8:16]) == 0 {
		return false
	}
	copyRaw := append([]byte(nil), raw...)
	for i := 8; i < 16; i++ {
		copyRaw[i] = 0
	}
	return qualificationUnusedQuota(copyRaw, id)
}

func qualificationLoadedFromActual(ctx context.Context, c qualificationCandidateRecord, test qualificationio.FDIdentity, worker qualificationWorker, s *qualificationHelperSession) (qualificationLoaded, error) {
	var loaded qualificationLoaded
	if s == nil || worker.PID == 0 || s.engineID == "" || s.engineRoot == "" {
		return loaded, errQualification
	}
	_, mountNS, err := qualificationNamespaceAt(ctx, 1, 1, "mnt")
	if err != nil {
		return loaded, err
	}
	identity := Identity{EngineID: s.engineID, DockerRoot: s.engineRoot, FSUUID: test.FSUUID,
		GuestBootID: worker.Boot, MountNamespace: strconv.FormatUint(mountNS, 10), MountID: test.MountID, Device: test.Device}
	if identity != c.Trust.Identity {
		return loaded, fmt.Errorf("qualification actual identity drift")
	}
	for _, build := range c.Source.Builds {
		switch build.Role {
		case "engine":
			loaded.EngineArtifact = build.BinaryDigest
		case "kernel":
			loaded.KernelArtifact = build.BinaryDigest
		}
	}
	if loaded.EngineArtifact == "" || loaded.KernelArtifact == "" {
		return loaded, errQualification
	}
	loaded.Worker, loaded.Identity, loaded.Filesystem, loaded.Roles = worker, identity, "xfs", qualificationClone(c.Source.Inputs.Roles)
	return loaded, qualificationContext(ctx)
}
