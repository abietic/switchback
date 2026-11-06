//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

const qualificationCoreCgroup = "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
const qualificationPostgresImage = "docker.io/library/postgres@sha256:c274743e5423a554d3ebe3fcf73e489460397f538a1383d191a9c54774b04a49"

func qualificationGenerateMaterialFixed(ctx context.Context, phase string) error {
	switch phase {
	case "verify-runtime-preflight":
		return qualificationRuntimePreflightVerify(ctx)
	case "runtime-preflight":
		return qualificationRuntimePreflightFixed(ctx)
	case "prepare-identity":
		return qualificationPreparePublicIdentity(ctx)
	case "host-preflight":
		return qualificationGenerateHostMaterial(ctx)
	case "intent-input":
		return qualificationGenerateGuestIntent(ctx)
	case "create-core":
		return qualificationCreateCore(ctx)
	case "staged":
		return qualificationWriteStaged(ctx)
	case "prepare-core":
		return qualificationPrepareCore(ctx)
	case "start-waiting":
		return qualificationStartWaiting(ctx)
	case "receive":
		return qualificationTransferReceive(ctx)
	case "check-pg":
		return qualificationCheckPGFixed(ctx)
	case "prepare-env-binds":
		return qualificationPrepareEnvBinds(ctx)
	}
	if strings.HasPrefix(phase, "emit-") {
		return qualificationTransferEmit(ctx, strings.TrimPrefix(phase, "emit-"))
	}
	return fmt.Errorf("qualification material phase invalid")
}
func qualificationGenerateGuestIntent(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "material-intent"); e != nil {
		return e
	}
	var runtimeProof qualificationRuntimePreflight
	if e = d.decode(ctx, "runtime-preflight.json", &runtimeProof); e != nil {
		return e
	}
	k, e := qualificationMaterialKit(ctx, qualificationMaterialRead)
	if e != nil {
		return e
	}
	for _, t := range k.Templates {
		base := filepath.Base(t.Path)
		unit := ""
		if base == "switchback-capacity-publisher.service" {
			unit = "switchback-qualification-publisher.service"
		}
		if base == "switchback-created-layer-observer.service" {
			unit = "switchback-qualification-observer.service"
		}
		if unit != "" {
			state, e := qualificationHostExec(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", unit)
			if e != nil || string(state) != "inactive\n" {
				return fmt.Errorf("qualification role unit is not inactive")
			}
			hash, e := qualificationHashFile(ctx, "/etc/systemd/system/"+unit, MaxBytes)
			if e != nil || hash != t.Digest {
				return fmt.Errorf("installed new qualification unit differs")
			}
		}
	}
	var h qualificationHostMaterial
	var pub qualificationPublicIdentity
	if e = d.decode(ctx, "host-material.json", &h); e != nil {
		return e
	}
	if e = d.decode(ctx, "public-identity.json", &pub); e != nil {
		return e
	}
	if pub.Schema != "switchback.qualification.public_identity.v1" || !label(pub.KeyID) || len(pub.PublicKey) != 32 {
		return errQualification
	}
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return e
	}
	defer engine.Close()
	info, e := engine.Info(ctx, client.InfoOptions{})
	if e != nil || info.Info.ID != runtimeProof.EngineID || uint64(info.Info.NCPU)*1000 != runtimeProof.EngineCPUMilli || uint64(info.Info.MemTotal) != runtimeProof.EngineMemory || info.Info.DockerRootDir != "/mnt/lima-sbquota/docker" || info.Info.Driver != "overlay2" || info.Info.KernelVersion != "6.8.0-142-generic" {
		return fmt.Errorf("fixed Engine/root/driver unavailable")
	}
	for _, name := range []string{"switchback-core1011_core", qualificationEngineNetwork} {
		if _, e = engine.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); e != nil {
			return e
		}
	}
	images := map[string]string{}
	external := []qualificationArtifact{}
	roleBinary := map[string]string{}
	for _, x := range []struct{ role, ref, target string }{{"backend", "switchback-backend:qualification-candidate", "/usr/local/bin/switchback-backend"}, {"worker", "switchback-worker:qualification-candidate", "/usr/local/bin/worker-shim"}, {"postgres", qualificationPostgresImage, "/usr/local/bin/postgres"}, {"web", "switchback-web:qualification-candidate", "/usr/sbin/nginx"}} {
		i, e := engine.ImageInspect(ctx, x.ref)
		if e != nil || !digestPattern.MatchString(i.ID) || i.Architecture != runtime.GOARCH || i.Os != "linux" {
			return fmt.Errorf("required exact local image unavailable: %s", x.role)
		}
		if x.role == "web" && i.Config.User != "101:101" {
			return fmt.Errorf("web image must retain declared UID101:101")
		}
		if x.role != "postgres" && (i.Config.Labels["com.switchback.qualification-source"] != qualificationDigest(k.Source) || i.Config.Labels["com.switchback.qualification-templates"] != qualificationDigest(k.Templates)) {
			return fmt.Errorf("image source-context labels differ from exact kit")
		}
		images[x.role] = i.ID
		if x.role == "worker" {
			images["worker-native-helper"] = i.ID
			images["worker-compat-helper"] = i.ID
		}
		target := x.target
		if x.role == "postgres" {
			for _, env := range i.Config.Env {
				if env == "PG_MAJOR=18" {
					target = "/usr/lib/postgresql/18/bin/postgres"
				} else if env == "PG_MAJOR=17" {
					target = "/usr/lib/postgresql/17/bin/postgres"
				}
			}
		}
		// Even current-source images are independently exported from this exact ID;
		// comparison catches an old image silently kept under the candidate tag.
		a, e := qualificationImageArtifact(ctx, engine.ClientVersion(), i.ID, target, x.role, d)
		if e != nil {
			return e
		}
		roleBinary[x.role] = a.Digest
		kitMatch := false
		for _, built := range k.Artifacts {
			if built.Role == x.role {
				kitMatch = true
				if built.Digest != a.Digest {
					return fmt.Errorf("image executable differs from exact kit")
				}
			}
		}
		if !kitMatch {
			external = append(external, a)
		}
	}
	workerHelpers := []struct{ role, path string }{{"worker-native-helper", "/usr/local/bin/switchback-qualification-native"}, {"worker-compat-helper", "/usr/local/bin/switchback-qualification-compat"}}
	for _, x := range workerHelpers {
		a, e := qualificationImageArtifact(ctx, engine.ClientVersion(), images["worker"], x.path, x.role, d)
		if e != nil {
			return e
		}
		match := false
		for _, b := range k.Artifacts {
			if b.Role == x.role && b.Digest == a.Digest {
				match = true
			}
		}
		if !match {
			return fmt.Errorf("Worker helper image differs")
		}
	}
	// Select once, after exact image verification, and freeze the complete
	// environment below. Create and qualification never re-read this selector.
	executionProfile, e := qualificationReadExecutionProfile(ctx)
	if e != nil {
		return e
	}
	backendEnvironment, e := qualificationExecutionBackendEnvironment(executionProfile, images["worker"])
	if e != nil {
		return e
	}
	boot, e := qualificationGuestBoot(ctx)
	if e != nil {
		return e
	}
	_, ns, e := qualificationNamespaceAt(ctx, 1, 1, "mnt")
	if e != nil {
		return e
	}
	pool, e := qualificationOpenPoolRoot(ctx, "/mnt/lima-sbquota")
	if e != nil {
		return e
	}
	defer unix.Close(pool)
	fd, e := qualificationNativeFD(ctx, pool, "/mnt/lima-sbquota")
	if e != nil {
		return e
	}
	guarantee := config.M1SharedVolumeCapacityGuarantee()
	policy := workerapp.CapacityGuaranteeRef{ID: guarantee.ID, Revision: guarantee.Revision, Digest: guarantee.Digest} // exact shared-volume guarantee only.
	tr := Trust{KeyID: pub.KeyID, PublicKey: pub.PublicKey, Identity: Identity{EngineID: info.Info.ID, DockerRoot: info.Info.DockerRootDir, FSUUID: fd.FSUUID, GuestBootID: boot, MountNamespace: strconv.FormatUint(ns, 10), MountID: fd.MountID, Device: fd.Device}, AnchorPath: "/mnt/lima-sbquota/anchor", Core: []CoreDomain{{ID: "core", CgroupPath: qualificationCoreCgroup, CPUQuotaMicros: 120000, CPUPeriodMicros: 100000, MemoryMax: 3 << 30, PIDsMax: 1024}}, AdmissionPolicy: &policy}
	for _, x := range []struct{ id, path string }{{"core", "/mnt/lima-sbquota/core"}, {"product", "/mnt/lima-sbquota/product"}, {"engine", "/mnt/lima-sbquota/docker"}} {
		f, e := qualificationOpenPoolRoot(ctx, x.path)
		if e != nil {
			return e
		}
		ident, e := qualificationNativeFD(ctx, f, x.path)
		if e != nil {
			unix.Close(f)
			return e
		}
		state, e := qualificationQuotaState(ctx, f)
		if e != nil {
			unix.Close(f)
			return e
		}
		raw, e := qualificationQuotaRead(ctx, f, uint32(ident.ProjectID))
		unix.Close(f)
		if e != nil || ident.ProjectID == 0 || binary.LittleEndian.Uint64(raw[8:16]) == 0 {
			return errQualification
		}
		q := StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: uint64(state[0]), StateFlags: uint64(binary.LittleEndian.Uint16(state[2:4])), Version: uint64(raw[0]), Flags: uint64(raw[1]), ProjectID: ident.ProjectID, HardBlocks512: binary.LittleEndian.Uint64(raw[8:16])}
		tr.Storage = append(tr.Storage, StorageDomain{FSUUID: ident.FSUUID, Device: ident.Device, MountID: ident.MountID, FSType: "xfs", XFlags: uint64(binary.LittleEndian.Uint32(ident.FSXAttr[:4])), Quota: q, ID: x.id, Path: x.path, ProjectID: ident.ProjectID, HardBytes: q.HardBlocks512 * 512})
	}
	for _, x := range []struct{ role, domain string }{{"core", "core"}, {"product", "product"}, {"images", "engine"}, {"logs", "engine"}, {"build", "engine"}, {"working_copy", "product"}, {"cache", "product"}} {
		tr.Coverage = append(tr.Coverage, StorageCoverage{x.role, x.domain})
	}
	// A temporary digest validates prefix structure only; it is never written or
	// used as authorization. The final manifest is derived by assemble alone.
	check := qualificationClone(tr)
	check.ManifestDigest = Digest([]byte("prefix-structure-validation"))
	if e = ValidateTrust(check); e != nil {
		return e
	}
	for _, x := range []struct{ role, path string }{{"engine", "/usr/bin/dockerd"}, {"kernel", "/boot/vmlinuz-6.8.0-142-generic"}} {
		hash, e := qualificationHashFile(ctx, x.path, 512<<20)
		if e != nil {
			return e
		}
		external = append(external, qualificationArtifact{x.role, x.path, hash})
	}
	nonce, e := qualificationRandomToken()
	if e != nil {
		return e
	}
	password, e := qualificationReadPassword(ctx)
	if e != nil {
		return e
	}
	defer clear(password)
	roles := []qualificationRole{}
	for _, x := range []struct {
		role, kind string
		uid, caps  uint64
	}{{"backend", "container", 0, 11}, {"postgres", "container", 999, 0}, {"web", "container", 101, 0}, {"publisher", "host_process", 0, 0x280000}, {"observer", "host_process", 0, 0x200000}} {
		binaryHash := roleBinary[x.role]
		if binaryHash == "" {
			for _, a := range k.Artifacts {
				if a.Role == x.role {
					binaryHash = a.Digest
				}
			}
		}
		r := qualificationRole{Role: x.role, Kind: x.kind, ImageDigest: images[x.role], BinaryDigest: binaryHash, CoreID: "core", CgroupPath: qualificationCoreCgroup, UID: x.uid, Capabilities: x.caps, NoNewPrivileges: 1, Seccomp: 2, Mounts: []qualificationMount{}, Environment: []qualificationEnv{}}
		if x.kind == "host_process" {
			unit := "switchback-qualification-publisher.service"
			if x.role == "observer" {
				unit = "switchback-qualification-observer.service"
			}
			r.CgroupPath = filepath.Join(qualificationCoreCgroup, unit)
			r.Mounts = []qualificationMount{{Source: qualificationObserverDirectory, Target: qualificationObserverDirectory, ReadOnly: true}, {Source: "/mnt/lima-sbquota", Target: "/mnt/lima-sbquota"}}
		} else {
			r.Mounts = qualificationCoreMounts(x.role)
			if x.role == "backend" {
				r.Environment = append(backendEnvironment, qualificationEnv{qualificationDatabaseMarker, Digest([]byte(qualificationDatabaseURL(string(password))))})
			}
		}
		if x.role == "postgres" {
			r.Environment = []qualificationEnv{{"POSTGRES_USER", "switchback"}, {"POSTGRES_DB", "postgres"}, {"POSTGRES_INITDB_WALDIR", "/var/lib/postgresql/wal"}, {"PGDATA", "/var/lib/postgresql/data/pgdata"}, {qualificationPasswordMarker, Digest(password)}}
		}
		sort.Slice(r.Mounts, func(i, j int) bool { return r.Mounts[i].Target < r.Mounts[j].Target })
		sort.Slice(r.Environment, func(i, j int) bool { return r.Environment[i].Name < r.Environment[j].Name })
		roles = append(roles, r)
	}
	s, e := qualificationBuildIntent(k, h, tr, roles, external, images, nonce)
	if e != nil {
		return e
	}
	return d.put(ctx, "intent-input.json", s)
}
func qualificationCoreMounts(role string) []qualificationMount {
	if role == "postgres" {
		return []qualificationMount{{Source: "/mnt/lima-sbquota/core/postgres/data", Target: "/var/lib/postgresql/data"}, {Source: "/mnt/lima-sbquota/core/postgres/wal", Target: "/var/lib/postgresql/wal"}, {Source: qualificationKitRoot + "/source/deploy/experience/init-postgres.sh", Target: "/docker-entrypoint-initdb.d/10-switchback-init.sh", ReadOnly: true}, {Source: qualificationKitRoot + "/source/migrations", Target: "/opt/switchback/migrations", ReadOnly: true}, {Source: qualificationKitRoot + "/source/deploy/local/migrate-postgres.sh", Target: "/opt/switchback/migrate-postgres.sh", ReadOnly: true}}
	}
	if role != "backend" {
		return []qualificationMount{}
	}
	return []qualificationMount{{Source: "/mnt/lima-sbquota/product", Target: "/var/lib/switchback"}, {Source: "/run/switchback-engine/docker.sock", Target: "/var/run/docker.sock"}, {Source: "/mnt/lima-sbquota/core/source", Target: "/opt/switchback/workspace-source", ReadOnly: true}, {Source: qualificationPublicationPublic, Target: "/run/switchback-capacity", ReadOnly: true}, {Source: qualificationInstallDirectory, Target: "/etc/switchback/runtime-capacity", ReadOnly: true}, {Source: qualificationObserverDirectory, Target: qualificationObserverDirectory, ReadOnly: true}, {Source: "/run/switchback-created-layer", Target: "/run/switchback-created-layer", ReadOnly: true}, {Source: qualificationObserverDirectory + "/key", Target: "/run/switchback-observer-auth/key", ReadOnly: true}}
}
func qualificationWriteStaged(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "material-staged"); e != nil {
		return e
	}
	var s qualificationStaged
	var created qualificationCreated
	if e = d.decode(ctx, "intent-input.json", &s); e != nil {
		return e
	}
	if e = d.decode(ctx, "created.json", &created); e != nil {
		return e
	}
	staged, e := qualificationApplyCreated(s, created)
	if e != nil {
		return e
	}
	return d.put(ctx, "staged.json", staged)
}
func qualificationCreateCore(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "create-core"); e != nil {
		return e
	}
	var s qualificationStaged
	var intent qualificationStaticIntent
	if e = d.decode(ctx, "intent-input.json", &s); e != nil {
		return e
	}
	if e = d.decode(ctx, "static-intent.json", &intent); e != nil {
		return e
	}
	if qualificationValidateIntentInput(s) != nil || intent.Digest != qualificationStaticIntentDigest(s) || intent.InputDigest != qualificationDigest(s) {
		return errQualification
	}
	for _, path := range []string{"/mnt/lima-sbquota/core/postgres/data", "/mnt/lima-sbquota/core/postgres/wal"} {
		fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), path)
		var st unix.Stat_t
		se := unix.Fstat(fd, &st)
		le := qualificationInstallNames(f, nil)
		f.Close()
		if se != nil || le != nil || st.Uid != 999 || st.Gid != 999 || st.Mode&0777 != 0700 {
			return fmt.Errorf("PG requires fresh candidate-owned UID999 empty directories")
		}
	}
	password, e := qualificationReadPassword(ctx)
	if e != nil {
		return e
	}
	defer clear(password)
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return e
	}
	defer engine.Close()
	out := qualificationCreated{Schema: "switchback.qualification.created.v1", StaticDigest: intent.Digest, Nonce: s.Nonce}
	for _, r := range s.Source.Inputs.Roles {
		if r.Kind != "container" {
			continue
		}
		o, e := qualificationCoreOptions(r, s, string(password))
		if e != nil {
			return e
		}
		created, e := engine.ContainerCreate(ctx, o)
		if e != nil {
			return fmt.Errorf("core Create unknown; abandon: %w", e)
		}
		if !containerIDPattern.MatchString(created.ID) {
			return errQualification
		}
		receipt := qualificationCreatedRole{r.Role, created.ID, r.ImageDigest}
		if e = d.put(ctx, "created-"+r.Role+".json", receipt); e != nil {
			return e
		}
		i, e := engine.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if e != nil || i.Container.ID != created.ID || i.Container.Image != r.ImageDigest || i.Container.State == nil || i.Container.State.Status != container.StateCreated || i.Container.State.Pid != 0 || i.Container.Config == nil || i.Container.Config.Labels["com.switchback.qualification-window"] != s.Nonce || i.Container.Config.Labels["com.switchback.qualification-static-intent"] != intent.Digest {
			return fmt.Errorf("created core identity changed")
		}
		out.Roles = append(out.Roles, receipt)
	}
	bound, e := qualificationApplyCreated(s, out)
	if e != nil {
		return e
	}
	if e = qualificationVerifyCreatedEnvironment(ctx, engine, bound, password); e != nil {
		return e
	}
	return d.put(ctx, "created.json", out)
}
func qualificationPrepareEnvBinds(ctx context.Context) error {
	if e := qualificationContext(ctx); e != nil {
		return e
	}
	public, _, e := openQualificationDirectory(ctx, qualificationObserverDirectory)
	if e != nil {
		return e
	}
	defer public.Close()
	writer, _, e := openQualificationDirectory(ctx, qualificationObserverWriter)
	if e != nil {
		return e
	}
	defer writer.Close()
	if e = qualificationInstallNames(writer, nil); e != nil {
		return e
	}
	publicNames, e := qualificationInstallList(public)
	if e != nil || len(publicNames) > 1 || (len(publicNames) == 1 && publicNames[0] != "key") {
		return fmt.Errorf("observer public configuration is not fresh")
	}
	raw, e := qualificationReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if e != nil {
		return e
	}
	mounts, e := qualificationMountInfo(raw)
	if e != nil {
		return e
	}
	if _, exists := mounts[qualificationObserverDirectory]; exists {
		return fmt.Errorf("public observer bind already exists")
	}
	if _, exists := mounts[qualificationObserverWriter]; exists {
		return fmt.Errorf("private observer bind already exists")
	}
	if e = unix.Mount(qualificationObserverDirectory, qualificationObserverWriter, "", unix.MS_BIND, ""); e != nil {
		return fmt.Errorf("private bind outcome unknown: %w", e)
	}
	if e = unix.Mount(qualificationObserverDirectory, qualificationObserverDirectory, "", unix.MS_BIND, ""); e != nil {
		return fmt.Errorf("public bind outcome unknown: %w", e)
	}
	if e = unix.Mount("", qualificationObserverDirectory, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); e != nil {
		return fmt.Errorf("public RO remount outcome unknown: %w", e)
	}
	// Reopen after the binds: old descriptors intentionally still refer to their
	// original mounts and cannot prove public-RO/private-RW access semantics.
	p, _, e := openQualificationDirectory(ctx, qualificationObserverDirectory)
	if e != nil {
		return e
	}
	defer p.Close()
	w, _, e := openQualificationDirectory(ctx, qualificationObserverWriter)
	if e != nil {
		return e
	}
	defer w.Close()
	return qualificationInstallObserverView(ctx, p, w)
}

func qualificationPrepareCore(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "prepare-core"); e != nil {
		return e
	}
	var s qualificationStaged
	if e = d.decode(ctx, "staged.json", &s); e != nil {
		return e
	}
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return e
	}
	defer engine.Close()
	password, e := qualificationReadPassword(ctx)
	if e != nil {
		return e
	}
	defer clear(password)
	if e = qualificationVerifyCreatedEnvironment(ctx, engine, s, password); e != nil {
		return e
	}
	for _, r := range s.Source.Inputs.Roles {
		if r.Kind != "container" || r.Role == "backend" {
			continue
		}
		if e = qualificationCoreOwned(ctx, engine, r, s, false); e != nil {
			return e
		}
		if _, e = engine.ContainerStart(ctx, r.ContainerID, client.ContainerStartOptions{}); e != nil {
			return fmt.Errorf("core Start unknown; abandon: %w", e)
		}
		if r.Role != "postgres" {
			continue
		}
		// Readiness polling is read-only. Create/Start/migration are each issued once.
		for {
			if e = qualificationContext(ctx); e != nil {
				return e
			}
			i, e := engine.ContainerInspect(ctx, r.ContainerID, client.ContainerInspectOptions{})
			if e != nil || i.Container.State == nil || !i.Container.State.Running {
				return fmt.Errorf("new PG stopped")
			}
			if i.Container.State.Health != nil && string(i.Container.State.Health.Status) == "healthy" {
				break
			}
			if e = qualificationPoll(ctx); e != nil {
				return e
			}
		}
		if _, e = qualificationHostExec(ctx, "/usr/bin/docker", "--host", qualificationEngineSocket, "exec", "--user", "999:999", "--env", "PGUSER=switchback", "--env", "PGDATABASE=switchback", r.ContainerID, "/bin/sh", "/opt/switchback/migrate-postgres.sh"); e != nil {
			return e
		}
		raw, e := qualificationHostExec(ctx, "/usr/bin/docker", "--host", qualificationEngineSocket, "exec", "--user", "999:999", r.ContainerID, "psql", "--no-psqlrc", "-U", "switchback", "-d", "switchback", "-v", "ON_ERROR_STOP=1", "-At", "-c", qualificationPGInitialQuery)
		if e != nil {
			return e
		}
		var proof qualificationPGPreparation
		if e = qualificationParsePGInitial(raw, &proof); e != nil {
			return e
		}
		proof.Schema = "switchback.qualification.pg_prepared.v1"
		proof.CID = r.ContainerID
		proof.Nonce = s.Nonce
		proof.StaticDigest = qualificationStaticIntentDigest(s)
		i, e := engine.ContainerInspect(ctx, r.ContainerID, client.ContainerInspectOptions{})
		if e != nil || i.Container.State == nil || i.Container.State.Pid <= 0 {
			return errQualification
		}
		proof.PID = uint64(i.Container.State.Pid)
		stat, e := qualificationReadProc(ctx, proof.PID, "stat", 4096)
		if e != nil {
			return e
		}
		_, _, proof.Start, e = qualificationProcStat(stat)
		if e != nil {
			return e
		}
		if e = d.put(ctx, "pg-prepared.json", proof); e != nil {
			return e
		}
	}
	return qualificationContext(ctx)
}
func qualificationStartWaiting(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "start-waiting"); e != nil {
		return e
	}
	remaining, e := QualificationWindowRemaining(ctx)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	var s qualificationStaged
	if e = d.decode(ctx, "staged.json", &s); e != nil {
		return e
	}
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return e
	}
	defer engine.Close()
	password, e := qualificationReadPassword(ctx)
	if e != nil {
		return e
	}
	defer clear(password)
	if e = qualificationVerifyCreatedEnvironment(ctx, engine, s, password); e != nil {
		return e
	}
	for _, r := range s.Source.Inputs.Roles {
		if r.Role != "backend" {
			continue
		}
		if e = qualificationCoreOwned(ctx, engine, r, s, false); e != nil {
			return e
		}
		if _, e = engine.ContainerStart(ctx, r.ContainerID, client.ContainerStartOptions{}); e != nil {
			return fmt.Errorf("Backend Start unknown; abandon: %w", e)
		}
	}
	for _, unit := range []string{"switchback-qualification-publisher.service", "switchback-qualification-observer.service"} {
		if _, e = qualificationHostExec(ctx, "/usr/bin/systemctl", "start", unit); e != nil {
			return fmt.Errorf("role Start unknown; abandon: %w", e)
		}
	}
	_, e = QualificationWindowRemaining(ctx)
	return e
}
func qualificationCoreOwned(ctx context.Context, engine *client.Client, r qualificationRole, s qualificationStaged, running bool) error {
	i, e := engine.ContainerInspect(ctx, r.ContainerID, client.ContainerInspectOptions{})
	if e != nil {
		return e
	}
	c := i.Container
	if c.ID != r.ContainerID || c.Image != r.ImageDigest || c.Config == nil || c.HostConfig == nil || c.Config.Labels["com.switchback.qualification-window"] != s.Nonce || c.Config.Labels["com.switchback.qualification-static-intent"] != qualificationStaticIntentDigest(s) || c.State == nil || c.State.Running != running || (!running && c.State.Status != container.StateCreated) {
		return errQualification
	}
	for _, role := range s.Source.Inputs.Roles {
		if role.Role != "backend" {
			continue
		}
		enabled, err := qualificationWorkerDelivery(role.Environment)
		if err != nil {
			return err
		}
		if enabled {
			return qualificationVerifyExecutionCoreNetworks(ctx, engine, s)
		}
	}
	return nil
}

func qualificationCheckPGFixed(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "pg-precommit"); e != nil {
		return e
	}
	var p qualificationPGPreparation
	var c qualificationCandidateRecord
	if e = d.decode(ctx, "pg-prepared.json", &p); e != nil {
		return e
	}
	if e = d.decode(ctx, "candidate.json", &c); e != nil {
		return e
	}
	var role *qualificationRole
	for i := range c.Source.Inputs.Roles {
		if c.Source.Inputs.Roles[i].Role == "postgres" {
			role = &c.Source.Inputs.Roles[i]
		}
	}
	if role == nil || role.ContainerID != p.CID || p.StaticDigest != c.StaticIntentDigest {
		return errQualification
	}
	remaining, e := QualificationWindowRemaining(ctx)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	raw, e := qualificationHostExec(ctx, "/usr/bin/docker", "--host", qualificationEngineSocket, "exec", "--user", "999:999", p.CID, "psql", "--no-psqlrc", "-U", "switchback", "-d", "switchback", "-v", "ON_ERROR_STOP=1", "-At", "-c", qualificationPGInitialQuery)
	if e != nil {
		return e
	}
	var now qualificationPGPreparation
	if e = qualificationParsePGInitial(raw, &now); e != nil || now.PoolNonce != p.PoolNonce || now.Epoch != p.Epoch {
		return errQualification
	}
	current, e := qualificationObserveFrozenRole(ctx, *role)
	if e != nil || current.PID != p.PID || current.StartTicks != p.Start {
		return errQualification
	}
	return d.put(ctx, "pg-precommit.json", p)
}
