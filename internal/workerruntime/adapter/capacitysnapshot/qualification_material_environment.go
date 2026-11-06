package capacitysnapshot

import (
	"fmt"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"net/netip"
	"net/url"
	"strings"
)

const qualificationDatabaseMarker = "SWITCHBACK_QUALIFICATION_DATABASE_URL_DIGEST"
const qualificationPasswordMarker = "SWITCHBACK_QUALIFICATION_POSTGRES_PASSWORD_DIGEST"

func qualificationBackendEnvironment() []qualificationEnv {
	return []qualificationEnv{{"SWITCHBACK_CONTAINERIZED", "1"}, {"SWITCHBACK_WEB_ADDRESS", "0.0.0.0:8080"}, {"SWITCHBACK_LOCAL_TENANT_ID", "tenant-local"}, {"SWITCHBACK_LOCAL_ACTOR_ID", "user-local"}, {"SWITCHBACK_DATA_ROOT", "/var/lib/switchback"}, {"SWITCHBACK_ENGINE_DATA_ROOT", "/mnt/lima-sbquota/product"}, {"SWITCHBACK_WORKSPACE_SOURCE_ROOT", "/opt/switchback/workspace-source"}, {"SWITCHBACK_M1_WORKER_DELIVERY_ENABLED", "0"}, {"SWITCHBACK_M2_ASSISTANT_ENABLED", "0"}, {"SWITCHBACK_WORKER_NETWORK", qualificationEngineNetwork}, {"SWITCHBACK_GRPC_ADDRESS", "0.0.0.0:9090"}, {"SWITCHBACK_BACKEND_GRPC_TARGET", "switchback-backend:9090"}, {"SWITCHBACK_BACKEND_HTTP_BASE_URL", "http://switchback-backend:8080"}, {"SWITCHBACK_RUNTIME_CAPACITY_TRUST_FILE", "/etc/switchback/runtime-capacity/trust.json"}, {"SWITCHBACK_RUNTIME_CAPACITY_ADMISSION_MANIFEST_FILE", "/etc/switchback/runtime-capacity/admission-manifest.json"}, {"SWITCHBACK_RUNTIME_CAPACITY_SNAPSHOT_DIRECTORY", "/run/switchback-capacity"}}
}

func qualificationDatabaseURL(password string) string {
	return "postgresql://" + url.UserPassword("switchback", password).String() + "@postgres:5432/switchback?sslmode=disable"
}
func qualificationEnvValue(env []qualificationEnv, key string) string {
	v := ""
	for _, e := range env {
		if e.Name == key {
			if v != "" {
				return ""
			}
			v = e.Value
		}
	}
	return v
}
func qualificationCoreEnvironment(role qualificationRole, password string, actual []string) error {
	if role.Role != "backend" && role.Role != "postgres" {
		return nil
	}
	if !containerIDPattern.MatchString(password) {
		return errQualification
	}
	values := map[string]string{}
	for _, entry := range actual {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" {
			return errQualification
		}
		if _, exists := values[k]; exists {
			return errQualification
		}
		values[k] = v
	}
	for _, e := range role.Environment {
		if values[e.Name] != e.Value {
			return fmt.Errorf("declared core environment changed")
		}
	}
	if role.Role == "backend" {
		want := qualificationDatabaseURL(password)
		marker := qualificationEnvValue(role.Environment, qualificationDatabaseMarker)
		if marker != Digest([]byte(want)) || values[qualificationDatabaseMarker] != marker || values["SWITCHBACK_DATABASE_URL"] != want || Digest([]byte(values["SWITCHBACK_DATABASE_URL"])) != marker {
			return fmt.Errorf("Backend database credential/target binding changed")
		}
	}
	if role.Role == "postgres" {
		marker := qualificationEnvValue(role.Environment, qualificationPasswordMarker)
		if marker != Digest([]byte(password)) || values[qualificationPasswordMarker] != marker || values["POSTGRES_PASSWORD"] != password || Digest([]byte(values["POSTGRES_PASSWORD"])) != marker || values["POSTGRES_USER"] != "switchback" || values["POSTGRES_DB"] != "postgres" || values["PGDATA"] != "/var/lib/postgresql/data/pgdata" || values["POSTGRES_INITDB_WALDIR"] != "/var/lib/postgresql/wal" {
			return fmt.Errorf("PG credential/target binding changed")
		}
	}
	return nil
}
func qualificationCorePorts(role string) (network.PortSet, network.PortMap) {
	host := ""
	switch role {
	case "backend":
		host = "48081"
	case "web":
		host = "44175"
	default:
		return nil, nil
	}
	port := network.MustParsePort("8080/tcp")
	return network.PortSet{port: {}}, network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: host}}}
}

// Worker-enabled qualification checks both container configuration and the
// matching Engine network membership. Create-only containers have configured
// endpoints; address/endpoint allocation is required only once running.
func qualificationCoreNetworkReadback(role qualificationRole, actual container.InspectResponse, identities []qualificationEngineNetworkIdentity, networks map[string]network.Inspect) error {
	if role.Kind != "container" || actual.ID != role.ContainerID || actual.HostConfig == nil ||
		actual.HostConfig.NetworkMode != "switchback-core1011_core" || actual.State == nil || actual.NetworkSettings == nil ||
		(!actual.State.Running && actual.State.Status != container.StateCreated) || len(identities) != 2 || len(networks) != 2 {
		return errQualification
	}
	expected, err := qualificationCoreNetworking(role)
	if err != nil || len(actual.NetworkSettings.Networks) != len(expected.EndpointsConfig) {
		return errQualification
	}
	for index, identity := range identities {
		if qualificationEngineNetworkCheck(identity, index) != nil {
			return errQualification
		}
		current, ok := networks[identity.Name]
		if !ok || current.ID != identity.ID || current.Name != identity.Name {
			return errQualification
		}
		want, selected := expected.EndpointsConfig[identity.Name]
		if !selected {
			if _, connected := current.Containers[actual.ID]; connected {
				return errQualification
			}
			continue
		}
		endpoint := actual.NetworkSettings.Networks[identity.Name]
		if endpoint == nil || len(endpoint.Aliases) != 1 || endpoint.Aliases[0] != want.Aliases[0] || endpoint.IPAMConfig != nil ||
			len(endpoint.Links) != 0 || len(endpoint.DriverOpts) != 0 || endpoint.GwPriority != 0 ||
			(endpoint.NetworkID != "" && endpoint.NetworkID != identity.ID) || endpoint.GlobalIPv6Address.IsValid() || endpoint.IPv6Gateway.IsValid() || endpoint.GlobalIPv6PrefixLen != 0 {
			return errQualification
		}
		member, joined := current.Containers[actual.ID]
		if !actual.State.Running {
			if joined || endpoint.EndpointID != "" || endpoint.IPAddress.IsValid() || endpoint.Gateway.IsValid() || endpoint.IPPrefixLen != 0 {
				return errQualification
			}
			continue
		}
		prefix := netip.MustParsePrefix(identity.IPAM.Config[0].Subnet)
		gateway := netip.MustParseAddr(identity.IPAM.Config[0].Gateway)
		gatewayMatches := endpoint.Gateway == gateway || identity.Internal && !endpoint.Gateway.IsValid()
		if endpoint.NetworkID != identity.ID || !containerIDPattern.MatchString(endpoint.EndpointID) || !endpoint.IPAddress.Is4() ||
			!prefix.Contains(endpoint.IPAddress) || endpoint.IPAddress == prefix.Addr() || !prefix.Contains(endpoint.IPAddress.Next()) ||
			endpoint.IPPrefixLen != prefix.Bits() || !gatewayMatches || endpoint.IPAddress == gateway ||
			!joined || member.EndpointID != endpoint.EndpointID || member.Name != strings.TrimPrefix(actual.Name, "/") ||
			member.IPv4Address != netip.PrefixFrom(endpoint.IPAddress, endpoint.IPPrefixLen) || member.IPv6Address.IsValid() {
			return errQualification
		}
	}
	return nil
}
