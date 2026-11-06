package capacitysnapshot

import (
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func qualificationWorkerDelivery(env []qualificationEnv) (bool, error) {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if _, duplicate := values[entry.Name]; duplicate {
			return false, errQualification
		}
		values[entry.Name] = entry.Value
	}
	switch values["SWITCHBACK_M1_WORKER_DELIVERY_ENABLED"] {
	case "", "0":
		return false, nil
	case "1":
		if !digestPattern.MatchString(values["SWITCHBACK_WORKER_IMAGE"]) {
			return false, errQualification
		}
		for key, want := range map[string]string{
			"SWITCHBACK_M2_ASSISTANT_ENABLED":     "0",
			"SWITCHBACK_WORKER_NETWORK":           qualificationEngineNetwork,
			"SWITCHBACK_BACKEND_GRPC_TARGET":      "switchback-backend:9090",
			"SWITCHBACK_BACKEND_HTTP_BASE_URL":    "http://switchback-backend:8080",
			"SWITCHBACK_KIMI_CREDENTIAL_FILE":     qualificationKimiCredentialPath,
			"SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE": qualificationDeepSeekCredentialPath,
		} {
			if values[key] != want {
				return false, errQualification
			}
		}
		return true, nil
	default:
		return false, errQualification
	}
}

func qualificationCoreNetworking(r qualificationRole) (*network.NetworkingConfig, error) {
	alias := map[string]string{"backend": "switchback-backend", "postgres": "postgres", "web": "switchback-web"}[r.Role]
	if alias == "" {
		return nil, errQualification
	}
	endpoints := map[string]*network.EndpointSettings{"switchback-core1011_core": {Aliases: []string{alias}}}
	if r.Role == "backend" {
		enabled, err := qualificationWorkerDelivery(r.Environment)
		if err != nil {
			return nil, err
		}
		if enabled {
			endpoints[qualificationEngineNetwork] = &network.EndpointSettings{Aliases: []string{alias}}
		}
	}
	return &network.NetworkingConfig{EndpointsConfig: endpoints}, nil
}

// Pure Create options are shared by the fixed Linux caller and host fixtures.
func qualificationCoreOptions(r qualificationRole, s qualificationStaged, password string) (client.ContainerCreateOptions, error) {
	if r.Kind != "container" || !digestPattern.MatchString(r.ImageDigest) || strings.ContainsAny(password, "\x00\r\n") || password == "" {
		return client.ContainerCreateOptions{}, errQualification
	}
	pids := int64(128)
	memory := int64(768 << 20)
	if r.Role == "backend" {
		pids = 256
	}
	if r.Role == "web" {
		memory = 128 << 20
	}
	cfg := &container.Config{User: fmt.Sprintf("%d:%d", r.UID, r.UID), Labels: map[string]string{"com.switchback.qualification-window": s.Nonce, "com.switchback.qualification-static-intent": qualificationStaticIntentDigest(s)}, Env: []string{}}
	for _, v := range r.Environment {
		cfg.Env = append(cfg.Env, v.Name+"="+v.Value)
	}
	host := &container.HostConfig{ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled}, NetworkMode: "switchback-core1011_core", Resources: container.Resources{CgroupParent: "switchback-core.slice", NanoCPUs: 600000000, Memory: memory, MemorySwap: memory, PidsLimit: &pids}, Tmpfs: map[string]string{"/tmp": "size=32m,mode=1777"}}
	for _, m := range r.Mounts {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	switch r.Role {
	case "backend":
		enabled, err := qualificationWorkerDelivery(r.Environment)
		if err != nil || enabled && qualificationEnvValue(r.Environment, "SWITCHBACK_WORKER_IMAGE") != s.Binding.WorkerImageDigest {
			return client.ContainerCreateOptions{}, errQualification
		}
		cfg.Entrypoint = []string{"/usr/local/bin/switchback-backend"}
		cfg.Env = append(cfg.Env, "SWITCHBACK_DATABASE_URL="+qualificationDatabaseURL(password))
		host.CapAdd = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}
	case "postgres":
		cfg.Env = append(cfg.Env, "POSTGRES_PASSWORD="+password)
		host.Tmpfs["/var/run/postgresql"] = "size=16m,mode=0775,uid=999,gid=999"
		host.ShmSize = 64 << 20
		cfg.Healthcheck = &container.HealthConfig{Test: []string{"CMD", "pg_isready", "-U", "switchback", "-d", "switchback"}, Interval: time.Second, Timeout: time.Second, Retries: 60}
	case "web":
	default:
		return client.ContainerCreateOptions{}, errQualification
	}
	if err := qualificationCoreEnvironment(r, password, cfg.Env); err != nil {
		return client.ContainerCreateOptions{}, err
	}
	networking, err := qualificationCoreNetworking(r)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	cfg.ExposedPorts, host.PortBindings = qualificationCorePorts(r.Role)
	return client.ContainerCreateOptions{Name: "switchback-qualification-" + s.WindowID + "-" + r.Role, Image: r.ImageDigest, Config: cfg, HostConfig: host, NetworkingConfig: networking}, nil
}
