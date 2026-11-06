//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

const qualificationPasswordPath = "/mnt/lima-sbquota/core/postgres-password"

func qualificationReadPassword(ctx context.Context) ([]byte, error) {
	parent, e := qualificationOpenArtifactParent(ctx, filepath.Dir(qualificationPasswordPath))
	if e != nil {
		return nil, e
	}
	defer parent.Close()
	fd, e := unix.Openat(int(parent.Fd()), filepath.Base(qualificationPasswordPath), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), qualificationPasswordPath)
	defer f.Close()
	before, e := qualificationStat(ctx, fd)
	if e != nil || before.Uid != 0 || before.Gid != 0 || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != 0600 || before.Nlink != 1 || before.Size != 64 {
		return nil, errQualification
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65))
	if e != nil || len(raw) != 64 || !containerIDPattern.Match(raw) {
		clear(raw)
		return nil, errQualification
	}
	after, e := qualificationStat(ctx, fd)
	if e != nil || qualificationIdentity(before) != qualificationIdentity(after) {
		clear(raw)
		return nil, errQualification
	}
	fresh, e := unix.Openat(int(parent.Fd()), filepath.Base(qualificationPasswordPath), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		clear(raw)
		return nil, e
	}
	again, e := qualificationStat(ctx, fresh)
	unix.Close(fresh)
	if e != nil || qualificationIdentity(before) != qualificationIdentity(again) || qualificationContext(ctx) != nil {
		clear(raw)
		return nil, errQualification
	}
	return raw, nil
}
func qualificationVerifyInstalledRoleEnvironment(ctx context.Context, role qualificationRole, env []string) error {
	if role.Role != "backend" && role.Role != "postgres" {
		return nil
	}
	password, e := qualificationReadPassword(ctx)
	if e != nil {
		return e
	}
	defer clear(password)
	if e = qualificationCoreEnvironment(role, string(password), env); e != nil {
		return e
	}
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	var staged qualificationStaged
	if e = d.decode(ctx, "staged.json", &staged); e != nil {
		return e
	}
	return qualificationVerifyCoreNetwork(ctx, staged)
}
func qualificationVerifyCoreNetwork(ctx context.Context, s qualificationStaged) error {
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return e
	}
	defer engine.Close()
	var backend, pg string
	var backendEnvironment []qualificationEnv
	for _, r := range s.Source.Inputs.Roles {
		if r.Role == "backend" {
			backend = r.ContainerID
			backendEnvironment = r.Environment
		}
		if r.Role == "postgres" {
			pg = r.ContainerID
		}
	}
	if !containerIDPattern.MatchString(backend) || !containerIDPattern.MatchString(pg) || backend == pg {
		return errQualification
	}
	workerDelivery, e := qualificationWorkerDelivery(backendEnvironment)
	if e != nil {
		return e
	}
	if workerDelivery {
		return qualificationVerifyExecutionCoreNetworks(ctx, engine, s)
	}
	b, e := engine.ContainerInspect(ctx, backend, client.ContainerInspectOptions{})
	if e != nil {
		return e
	}
	p, e := engine.ContainerInspect(ctx, pg, client.ContainerInspectOptions{})
	if e != nil {
		return e
	}
	if b.Container.ID != backend || p.Container.ID != pg || b.Container.HostConfig == nil || p.Container.HostConfig == nil || string(b.Container.HostConfig.NetworkMode) != "switchback-core1011_core" || string(p.Container.HostConfig.NetworkMode) != "switchback-core1011_core" {
		return fmt.Errorf("fixed database network binding changed")
	}
	// The backend is still Create-only during PG preparation; exact primary
	// network mode is already required. Once both run, pin the same actual network
	// ID and the exact frozen PG endpoint's postgres alias.
	if b.Container.State != nil && p.Container.State != nil && b.Container.State.Running && p.Container.State.Running {
		if b.Container.NetworkSettings == nil || p.Container.NetworkSettings == nil {
			return errQualification
		}
		bn := b.Container.NetworkSettings.Networks["switchback-core1011_core"]
		pn := p.Container.NetworkSettings.Networks["switchback-core1011_core"]
		if bn == nil || pn == nil || bn.NetworkID == "" || bn.NetworkID != pn.NetworkID {
			return errQualification
		}
		alias := false
		for _, v := range pn.Aliases {
			if v == "postgres" {
				alias = true
			}
		}
		if !alias {
			return errQualification
		}
	}
	return qualificationContext(ctx)
}
func qualificationVerifyCreatedEnvironment(ctx context.Context, engine *client.Client, s qualificationStaged, password []byte) error {
	for _, r := range s.Source.Inputs.Roles {
		if r.Role != "backend" && r.Role != "postgres" {
			continue
		}
		i, e := engine.ContainerInspect(ctx, r.ContainerID, client.ContainerInspectOptions{})
		if e != nil {
			return e
		}
		c := i.Container
		if c.ID != r.ContainerID || c.Image != r.ImageDigest || c.Config == nil || c.HostConfig == nil || c.Config.Labels["com.switchback.qualification-window"] != s.Nonce || c.Config.Labels["com.switchback.qualification-static-intent"] != qualificationStaticIntentDigest(s) {
			return errQualification
		}
		if e = qualificationCoreEnvironment(r, string(password), c.Config.Env); e != nil {
			return e
		}
	}
	return qualificationVerifyCoreNetwork(ctx, s)
}
