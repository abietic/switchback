package capacitysnapshot

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func executionProfileNetworkFixture(t *testing.T, running bool) (qualificationRole, container.InspectResponse, []qualificationEngineNetworkIdentity, map[string]network.Inspect) {
	t.Helper()
	role, _, _ := executionProfileBackendRole(t, true)
	role.ContainerID = strings.Repeat("4", 64)
	receipt, err := qualificationEngineReceiptDecode(qualificationEngineFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	c := container.InspectResponse{ID: role.ContainerID, Name: "/switchback-qualification-fixture-backend", HostConfig: &container.HostConfig{NetworkMode: "switchback-core1011_core"},
		State: &container.State{Status: container.StateCreated}, NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{}}}
	actual := map[string]network.Inspect{}
	if running {
		c.State = &container.State{Status: container.StateRunning, Running: true}
	}
	for index, identity := range receipt.Networks {
		endpoint := &network.EndpointSettings{Aliases: []string{"switchback-backend"}}
		actual[identity.Name] = network.Inspect{Network: network.Network{Name: identity.Name, ID: identity.ID}, Containers: map[string]network.EndpointResource{}}
		if running {
			prefix := netip.MustParsePrefix(identity.IPAM.Config[0].Subnet)
			endpoint.NetworkID, endpoint.EndpointID = identity.ID, strings.Repeat(string(rune('b'+index)), 64)
			endpoint.IPAddress = prefix.Addr().Next().Next()
			endpoint.IPPrefixLen, endpoint.Gateway = prefix.Bits(), netip.MustParseAddr(identity.IPAM.Config[0].Gateway)
			actual[identity.Name].Containers[c.ID] = network.EndpointResource{Name: strings.TrimPrefix(c.Name, "/"), EndpointID: endpoint.EndpointID, IPv4Address: netip.PrefixFrom(endpoint.IPAddress, endpoint.IPPrefixLen)}
		}
		c.NetworkSettings.Networks[identity.Name] = endpoint
	}
	return role, c, receipt.Networks, actual
}

func TestQualificationExecutionProfileNetworkReadback(t *testing.T) {
	for _, running := range []bool{false, true} {
		role, actual, identities, networks := executionProfileNetworkFixture(t, running)
		if err := qualificationCoreNetworkReadback(role, actual, identities, networks); err != nil {
			t.Fatalf("valid %s endpoints rejected: %v", actual.State.Status, err)
		}
	}
	for _, failure := range []string{"missing_network", "extra_network", "wrong_network_name", "wrong_network_id", "wrong_primary", "host_network", "missing_alias", "wrong_alias", "extra_alias", "duplicate_alias", "ipam_override", "links", "driver_options", "gateway_priority", "missing_endpoint", "foreign_ip", "gateway", "prefix", "ipv6", "missing_membership", "endpoint_id", "endpoint_ip", "endpoint_name", "network_identity", "missing_settings", "missing_state"} {
		t.Run(failure, func(t *testing.T) {
			role, actual, identities, networks := executionProfileNetworkFixture(t, true)
			worker := actual.NetworkSettings.Networks["switchback-m1-worker-control"]
			switch failure {
			case "missing_network":
				delete(actual.NetworkSettings.Networks, "switchback-m1-worker-control")
			case "extra_network":
				actual.NetworkSettings.Networks["foreign"] = worker.Copy()
			case "wrong_network_name":
				delete(actual.NetworkSettings.Networks, "switchback-m1-worker-control")
				actual.NetworkSettings.Networks["other-control"] = worker
			case "wrong_network_id":
				worker.NetworkID = strings.Repeat("f", 64)
			case "wrong_primary":
				actual.HostConfig.NetworkMode = "switchback-m1-worker-control"
			case "host_network":
				actual.HostConfig.NetworkMode = "host"
			case "missing_alias":
				worker.Aliases = nil
			case "wrong_alias":
				worker.Aliases = []string{"backend"}
			case "extra_alias":
				worker.Aliases = append(worker.Aliases, "foreign")
			case "duplicate_alias":
				worker.Aliases = append(worker.Aliases, "switchback-backend")
			case "ipam_override":
				worker.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: worker.IPAddress}
			case "links":
				worker.Links = []string{"foreign"}
			case "driver_options":
				worker.DriverOpts = map[string]string{"foreign": "yes"}
			case "gateway_priority":
				worker.GwPriority = 1
			case "missing_endpoint":
				worker.EndpointID = ""
			case "foreign_ip":
				worker.IPAddress = netip.MustParseAddr("10.99.0.4")
			case "gateway":
				worker.Gateway = netip.MustParseAddr("10.99.0.1")
			case "prefix":
				worker.IPPrefixLen++
			case "ipv6":
				worker.GlobalIPv6Address = netip.MustParseAddr("fd00::2")
			case "missing_membership":
				delete(networks["switchback-m1-worker-control"].Containers, role.ContainerID)
			case "endpoint_id", "endpoint_ip", "endpoint_name":
				member := networks["switchback-m1-worker-control"].Containers[role.ContainerID]
				if failure == "endpoint_id" {
					member.EndpointID = strings.Repeat("e", 64)
				} else if failure == "endpoint_ip" {
					member.IPv4Address = netip.MustParsePrefix("10.99.0.3/24")
				} else {
					member.Name = "foreign"
				}
				networks["switchback-m1-worker-control"].Containers[role.ContainerID] = member
			case "network_identity":
				wrong := networks["switchback-m1-worker-control"]
				wrong.ID = strings.Repeat("d", 64)
				networks["switchback-m1-worker-control"] = wrong
			case "missing_settings":
				actual.NetworkSettings = nil
			case "missing_state":
				actual.State = nil
			}
			if err := qualificationCoreNetworkReadback(role, actual, identities, networks); err == nil {
				t.Fatal("accepted changed actual network/endpoint")
			}
		})
	}
}

func TestQualificationExecutionProfileInternalNetworkNeedsNoDefaultGateway(t *testing.T) {
	role, actual, identities, networks := executionProfileNetworkFixture(t, true)
	actual.NetworkSettings.Networks["switchback-core1011_core"].Gateway = netip.Addr{}
	if err := qualificationCoreNetworkReadback(role, actual, identities, networks); err != nil {
		t.Fatal("internal Core endpoint may omit a default gateway", err)
	}
}

func TestQualificationExecutionProfileRejectsImageDriftBeforeCreate(t *testing.T) {
	role, staged, password := executionProfileBackendRole(t, true)
	for i := range role.Environment {
		if role.Environment[i].Name == "SWITCHBACK_WORKER_IMAGE" {
			role.Environment[i].Value = "sha256:" + strings.Repeat("9", 64)
		}
	}
	if _, err := qualificationCoreOptions(role, staged, password); err == nil {
		t.Fatal("Create accepted a Worker image different from the verified intent binding")
	}
}
