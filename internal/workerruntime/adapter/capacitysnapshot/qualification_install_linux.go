//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"os"
	"path/filepath"
	"reflect"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

func qualificationInstallRename(old int, from string, to int, name string) error {
	return unix.Renameat2(old, from, to, name, unix.RENAME_NOREPLACE)
}
func installQualificationFixed(ctx context.Context) error {
	dir, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer dir.close()
	sourceFD, sourceIDs, e := openQualificationDirectory(ctx, qualificationGuestCandidateRoot)
	if e != nil {
		return e
	}
	defer sourceFD.Close()
	sourceStable := qualificationStableDirectories(sourceIDs)
	remaining, e := QualificationWindowRemaining(ctx)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	return installQualification(ctx, qualificationInstallOps{
		claim:     func(ctx context.Context) error { return dir.claim(ctx, "install") },
		remaining: func(ctx context.Context) error { _, e := QualificationWindowRemaining(ctx); return e },
		load: func(ctx context.Context) (map[string][]byte, error) {
			fresh, ids, e := openQualificationDirectory(ctx, qualificationGuestCandidateRoot)
			if e != nil {
				return nil, e
			}
			fresh.Close()
			if !reflect.DeepEqual(sourceStable, qualificationStableDirectories(ids)) {
				return nil, fmt.Errorf("source ancestors changed")
			}
			return qualificationInstallMaterialStable(ctx, dir)
		},
		directory: openQualificationDirectory, stat: qualificationStat, leaf: readQualificationLeaf,
		view: qualificationInstallObserverView, state: func(ctx context.Context, f map[string][]byte) error {
			return qualificationCheckPublicationBootstrap(ctx, dir, f)
		},
		current: func(ctx context.Context) error { return qualificationInstallCurrent(ctx, dir) },
		write:   func(f *os.File, b []byte) (int, error) { return f.Write(b) }, sync: func(f *os.File) error { return f.Sync() }, rename: qualificationInstallRename,
		final: qualificationInstallDirectory, public: qualificationObserverDirectory, writer: qualificationObserverWriter,
	})
}
func qualificationInstallObserverView(ctx context.Context, public, writer *os.File) error {
	p, e := qualificationStat(ctx, int(public.Fd()))
	if e != nil {
		return e
	}
	w, e := qualificationStat(ctx, int(writer.Fd()))
	if e != nil {
		return e
	}
	if p.Dev != w.Dev || p.Ino != w.Ino || p.Uid != 0 || w.Uid != 0 || p.Gid != 0 || w.Gid != 0 {
		return fmt.Errorf("observer views are not same directory inode")
	}
	raw, e := qualificationReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if e != nil {
		return e
	}
	mounts, e := qualificationMountInfo(raw)
	if e != nil {
		return e
	}
	pm, pok := mounts[qualificationObserverDirectory]
	wm, wok := mounts[qualificationObserverWriter]
	if !pok || !wok || !pm.ReadOnly || wm.ReadOnly || pm.MountID == wm.MountID || pm.Root != wm.Root || pm.Device != wm.Device {
		return fmt.Errorf("observer public RO/private RW bind mismatch")
	}
	var ps, ws unix.Statfs_t
	if unix.Fstatfs(int(public.Fd()), &ps) != nil || unix.Fstatfs(int(writer.Fd()), &ws) != nil || ps.Flags&unix.ST_RDONLY == 0 || ws.Flags&unix.ST_RDONLY != 0 {
		return fmt.Errorf("observer view access drift")
	}
	return qualificationContext(ctx)
}
func qualificationInstallCurrent(ctx context.Context, d qualificationCandidateDirectory) error {
	var c qualificationCandidateRecord
	var s qualificationStaged
	var w qualificationWindowOpen
	var n qualificationNativeObservation
	for _, x := range []struct {
		name string
		v    any
	}{{"candidate.json", &c}, {"staged.json", &s}, {"window-open.json", &w}, {"native-observations.json", &n}} {
		if e := d.decode(ctx, x.name, x.v); e != nil {
			return e
		}
	}
	for _, r := range c.Source.Inputs.Roles {
		now, e := qualificationObserveFrozenRole(ctx, r)
		if e != nil {
			return e
		}
		var before *qualificationFrozenProcess
		for i := range c.Processes {
			if c.Processes[i].Role == r.Role {
				before = &c.Processes[i]
			}
		}
		if before == nil || before.PID != now.PID || before.StartTicks != now.StartTicks || before.Snapshot.ExecutableDigest != now.Snapshot.ExecutableDigest || before.Snapshot.Boot != now.Snapshot.Boot || !reflect.DeepEqual(before.Snapshot.Cgroup, now.Snapshot.Cgroup) || !reflect.DeepEqual(before.Snapshot.MountInfo, now.Snapshot.MountInfo) {
			return fmt.Errorf("waiting role changed before installation")
		}
		status, se := procstatus.Parse(now.Snapshot.Status)
		if se != nil || !roleMatchesUID(status, r.UID, r.Capabilities) {
			return errQualification
		}
		if e = qualificationInspectFrozenContainer(ctx, r, now, w.Nonce, c.StaticIntentDigest); e != nil {
			return e
		}
	}
	if e := validateQualificationCleanup(n.Cleanup, c.TestProject); e != nil {
		return e
	}
	engine, e := qualificationNewEngine()
	if e != nil {
		return e
	}
	defer engine.Close()
	for _, h := range n.Cleanup.Helpers {
		_, e := engine.ContainerInspect(ctx, h.CID, client.ContainerInspectOptions{})
		if !cerrdefs.IsNotFound(e) {
			return fmt.Errorf("qualification helper cleanup changed")
		}
	}
	root, e := qualificationTestRoot(c.Trust, w.ID)
	if e != nil {
		return e
	}
	parent, e := qualificationOpenArtifactParent(ctx, filepath.Dir(root))
	if e != nil {
		return e
	}
	defer parent.Close()
	var st unix.Stat_t
	if e = unix.Fstatat(int(parent.Fd()), filepath.Base(root), &st, unix.AT_SYMLINK_NOFOLLOW); e != unix.ENOENT {
		return fmt.Errorf("qualification objects remain or unknown")
	}
	pool, e := qualificationOpenPoolRoot(ctx, filepath.Dir(filepath.Dir(root)))
	if e != nil {
		return e
	}
	defer unix.Close(pool)
	for _, id := range []uint32{c.TestProject, c.TestProject + 1} {
		raw, e := qualificationQuotaRead(ctx, pool, id)
		if e != nil || !qualificationUnusedQuota(raw, id) {
			return fmt.Errorf("qualification quota cleanup changed")
		}
	}
	var prepared, precommit qualificationPGPreparation
	if d.decode(ctx, "pg-prepared.json", &prepared) != nil || d.decode(ctx, "pg-precommit.json", &precommit) != nil || !reflect.DeepEqual(prepared, precommit) || prepared.Nonce != s.Nonce || prepared.StaticDigest != c.StaticIntentDigest {
		return fmt.Errorf("PG preparation binding changed")
	}
	files, e := qualificationInstallMaterial(ctx, d)
	if e != nil {
		return e
	}
	if e = qualificationInstallPrivateCurrent(ctx, files); e != nil {
		return e
	}
	return qualificationContext(ctx)
}

func qualificationInstallPrivateCurrent(ctx context.Context, files map[string][]byte) error {
	var trust Trust
	if strictJSON(files["trust.json"], &trust) != nil {
		return errQualification
	}
	result, e := validateDeploymentQualification(trust, files)
	if e != nil {
		return e
	}
	for _, want := range result.private {
		actual, e := qualificationPrivateDirectory(ctx, want.Path)
		if e != nil || !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("publisher private identity changed")
		}
	}
	for _, path := range []string{qualificationPublicationPrivate, qualificationPublicationPublic} {
		f, e := qualificationOpenPoolRoot(ctx, path)
		if e != nil {
			return e
		}
		actual, e := qualificationNativeFD(ctx, f, path)
		unix.Close(f)
		if e != nil || actual.FSUUID != trust.Identity.FSUUID || actual.Device != trust.Identity.Device || actual.MountID != trust.Identity.MountID {
			return errQualification
		}
	}
	return nil
}

func qualificationPublicationNativeFS() qualificationPublicationFS {
	return qualificationPublicationFS{openQualificationDirectory, qualificationStat, qualificationPublicationPrivate, qualificationPublicationPublic}
}
func qualificationCheckPublicationBootstrap(ctx context.Context, d qualificationCandidateDirectory, f map[string][]byte) error {
	return qualificationPublicationCheck(ctx, d, qualificationPublicationNativeFS(), f)
}
func bootstrapQualificationPublicationFixed(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	remaining, e := QualificationWindowRemaining(ctx)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	files, e := qualificationInstallMaterial(ctx, d)
	if e != nil {
		return e
	}
	if e = qualificationInstallPrivateCurrent(ctx, files); e != nil {
		return e
	}
	if e = qualificationBootstrapPublication(ctx, d, qualificationPublicationNativeFS()); e != nil {
		return e
	}
	_, e = QualificationWindowRemaining(ctx)
	return e
}

// RecordQualificationPublicationEmpty is pre-Create, before the runtime budget.
func RecordQualificationPublicationEmpty(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	return qualificationPublicationRecordEmpty(ctx, d, qualificationPublicationNativeFS())
}

func qualificationInstallMaterialStable(ctx context.Context, d qualificationCandidateDirectory) (map[string][]byte, error) {
	before, e := qualificationStat(ctx, int(d.file.Fd()))
	if e != nil {
		return nil, e
	}
	check := func() error {
		again, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
		if e != nil {
			return e
		}
		defer again.close()
		after, e := qualificationStat(ctx, int(again.file.Fd()))
		if e != nil || qualificationStableDirectory(qualificationIdentity(before)) != qualificationStableDirectory(qualificationIdentity(after)) {
			return fmt.Errorf("source candidate parent changed")
		}
		return nil
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = qualificationInstallCandidateNames(d); e != nil {
		return nil, e
	}
	var s qualificationStaged
	if e = d.decode(ctx, "staged.json", &s); e != nil {
		return nil, e
	}
	if e = qualificationCheckStagedArtifacts(ctx, s, qualificationHashFile); e != nil {
		return nil, e
	}
	files, e := qualificationInstallMaterial(ctx, d)
	if e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	return files, nil
}
