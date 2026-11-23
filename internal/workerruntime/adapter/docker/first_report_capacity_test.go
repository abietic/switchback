package docker

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func TestFirstReportCapacityRejectsUnrelatedOrWritableMounts(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, scenario := range []struct {
		name       string
		container  string
		attachment container.MountPoint
		want       bool
	}{
		{"exact", id, container.MountPoint{Type: mount.TypeBind, Source: "/var/lib/docker/.switchback-capacity", Destination: firstReportCapacityMount}, true},
		{"writable", id, container.MountPoint{Type: mount.TypeBind, Source: "/var/lib/docker/.switchback-capacity", Destination: firstReportCapacityMount, RW: true}, false},
		{"other filesystem", id, container.MountPoint{Type: mount.TypeBind, Source: "/var/tmp/other", Destination: firstReportCapacityMount}, false},
		{"other container", strings.Repeat("b", 64), container.MountPoint{Type: mount.TypeBind, Source: "/var/lib/docker/.switchback-capacity", Destination: firstReportCapacityMount}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			self := container.InspectResponse{ID: scenario.container, State: &container.State{Running: true}, Mounts: []container.MountPoint{scenario.attachment}}
			if err := verifyFirstReportMount(self, id[:12], "/var/lib/docker"); (err == nil) != scenario.want {
				t.Fatalf("unexpected capacity mount verdict: %v", err)
			}
		})
	}
}
