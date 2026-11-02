package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	secretInputBootstrap = "/opt/switchback/secret-input/bootstrap.json"
	secretInputModel     = "/opt/switchback/secret-input/model"
	secretOutputRoot     = "/opt/switchback/secret-output"
)

const populateSecretVolumeScript = `set -eu
output=/opt/switchback/secret-output
bootstrap=/opt/switchback/secret-input/bootstrap.json
model=/opt/switchback/secret-input/model
[ -f "$bootstrap" ] && [ ! -L "$bootstrap" ]
[ -f "$model" ] && [ ! -L "$model" ]
[ -z "$(find "$output" -mindepth 1 -maxdepth 1 -print -quit)" ]
umask 077
cat "$bootstrap" > "$output/bootstrap.json"
cat "$model" > "$output/model"
chmod 0400 "$output/bootstrap.json" "$output/model"
[ "$(stat -c '%u:%g:%a' "$output/bootstrap.json")" = "0:0:400" ]
[ "$(stat -c '%u:%g:%a' "$output/model")" = "0:0:400" ]`

func (p *Provisioner) stageSecretVolume(ctx context.Context, request application.RuntimeProvisionRequest) (name string, resultErr error) {
	name = secretVolumeName(request)
	created, err := p.engine.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: name, Driver: "local", Labels: secretResourceLabels(request),
	})
	if err != nil {
		return "", dockerError("create Runtime secret volume", err)
	}
	if created.Volume.Name != name || !reflect.DeepEqual(created.Volume.Labels, secretResourceLabels(request)) {
		return "", fmt.Errorf("Runtime secret volume identity conflict")
	}
	removeVolume := true
	// Capture the validated identity before an error return clears named result name.
	defer func(volumeName string) {
		if !removeVolume {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, cleanupErr := p.engine.VolumeRemove(cleanupCtx, volumeName, client.VolumeRemoveOptions{Force: true})
		resultErr = errors.Join(resultErr, cleanupErr)
	}(created.Volume.Name)

	populator, err := p.engine.ContainerCreate(ctx, p.secretPopulatorOptions(request, name))
	if err != nil {
		return "", dockerError("create Runtime secret populator", err)
	}
	if populator.ID == "" {
		return "", fmt.Errorf("create Runtime secret populator returned an empty id")
	}
	removePopulator := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, err := p.engine.ContainerRemove(cleanupCtx, populator.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	if _, err := p.engine.ContainerStart(ctx, populator.ID, client.ContainerStartOptions{}); err != nil {
		return "", errors.Join(dockerError("start Runtime secret populator", err), removePopulator())
	}
	wait := p.engine.ContainerWait(ctx, populator.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
		return "", errors.Join(ctx.Err(), removePopulator())
	case waitErr, ok := <-wait.Error:
		if !ok || waitErr == nil {
			waitErr = fmt.Errorf("Runtime secret populator wait returned no result")
		}
		return "", errors.Join(dockerError("wait for Runtime secret populator", waitErr), removePopulator())
	case result, ok := <-wait.Result:
		if !ok || result.Error != nil {
			return "", errors.Join(fmt.Errorf("Runtime secret populator wait did not confirm success"), removePopulator())
		}
		if result.StatusCode != 0 {
			return "", errors.Join(fmt.Errorf("Runtime secret populator rejected its inputs"), removePopulator())
		}
	}
	if err := removePopulator(); err != nil {
		return "", dockerError("remove Runtime secret populator", err)
	}
	if err := removeTransientSecretInputs(request.Secrets); err != nil {
		return "", err
	}
	removeVolume = false
	return name, nil
}

func (p *Provisioner) secretPopulatorOptions(request application.RuntimeProvisionRequest, volumeName string) client.ContainerCreateOptions {
	pids := int64(32)
	return client.ContainerCreateOptions{
		Name:  "switchback-runtime-secret-populator-" + request.RuntimeInstanceID + "-g" + strconv.FormatUint(request.Generation, 10),
		Image: p.options.Image,
		Config: &container.Config{
			User:       "0:0",
			Labels:     secretPopulatorLabels(request),
			WorkingDir: "/",
			Entrypoint: []string{"/bin/sh"},
			Cmd:        []string{"-eu", "-c", populateSecretVolumeScript},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:     container.NetworkMode("none"),
			Privileged:      false,
			ReadonlyRootfs:  true,
			PublishAllPorts: false,
			CapDrop:         []string{"ALL"},
			SecurityOpt:     []string{"no-new-privileges"},
			RestartPolicy:   container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Resources: container.Resources{
				CgroupParent: "switchback-core.slice",
				NanoCPUs:     100_000_000,
				Memory:       67_108_864,
				MemorySwap:   67_108_864,
				PidsLimit:    &pids,
			},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: request.Secrets.Bootstrap.EngineSource, Target: secretInputBootstrap, ReadOnly: true},
				{Type: mount.TypeBind, Source: request.Secrets.ModelCredential.EngineSource, Target: secretInputModel, ReadOnly: true},
				{Type: mount.TypeVolume, Source: volumeName, Target: secretOutputRoot, VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
			},
		},
	}
}

func secretPopulatorLabels(request application.RuntimeProvisionRequest) map[string]string {
	labels := secretResourceLabels(request)
	labels[labelManaged] = "runtime-secret-populator"
	return labels
}

func removeTransientSecretInputs(resources application.RuntimeSecretResources) error {
	for _, path := range []string{resources.Bootstrap.BackendPath, resources.ModelCredential.BackendPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove transient Runtime secret input")
		}
	}
	if err := os.Remove(resources.BackendRoot); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove transient Runtime secret input root")
	}
	return nil
}
