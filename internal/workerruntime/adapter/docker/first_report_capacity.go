package docker

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

const firstReportCapacityMount = "/run/switchback-capacity"
const firstReportCapacitySourceDir = ".switchback-capacity"

var dockerHostnamePattern = regexp.MustCompile(`^([a-f0-9]{12}|[a-f0-9]{64})$`)

type firstReportCapacityEngine interface {
	engineInfoReader
	DaemonHost() string
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}

func (p *Provisioner) SampleFirstReportCapacity(ctx context.Context, expectedEngineID string) (application.FirstReportCapacityEvidence, error) {
	engine, ok := p.engine.(firstReportCapacityEngine)
	if !ok || isNilInterface(engine) {
		return application.FirstReportCapacityEvidence{}, fmt.Errorf("Docker Engine cannot prove first-report capacity")
	}
	return SampleFirstReportCapacity(ctx, engine, expectedEngineID)
}

// SampleFirstReportCapacity reads the local daemon and the observer's own
// read-only mount of a deployment-owned empty directory on the daemon's XFS.
// The deployment must provision that directory on the same backing mount.
func SampleFirstReportCapacity(ctx context.Context, engine firstReportCapacityEngine, expectedEngineID string) (application.FirstReportCapacityEvidence, error) {
	result := application.FirstReportCapacityEvidence{}
	switch engine.DaemonHost() {
	case "unix:///var/run/docker.sock", "unix:///run/docker.sock":
	default:
		return result, fmt.Errorf("first-report capacity requires the local Docker Unix socket")
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := readWritableLayerEngine(bounded, engine)
	if err != nil {
		return result, err
	}
	if expectedEngineID == "" || info.ID != expectedEngineID {
		return result, fmt.Errorf("first-report capacity Engine does not match the deployment identity")
	}
	if info.NCPU <= 0 || int64(info.NCPU) > math.MaxInt64/1_000_000_000 || info.MemTotal <= 0 {
		return result, fmt.Errorf("first-report Engine CPU or memory capacity is unknown")
	}
	hostname, err := os.Hostname()
	if err != nil || !dockerHostnamePattern.MatchString(hostname) {
		return result, fmt.Errorf("first-report capacity requires a Docker container identity")
	}
	self, err := engine.ContainerInspect(bounded, hostname, client.ContainerInspectOptions{})
	if err != nil {
		return result, fmt.Errorf("inspect first-report capacity observer: %w", err)
	}
	if err := verifyFirstReportMount(self.Container, hostname, info.DockerRootDir); err != nil {
		return result, err
	}
	available, err := readFirstReportStorage()
	if err != nil {
		return result, err
	}
	if err := bounded.Err(); err != nil {
		return result, err
	}
	return application.FirstReportCapacityEvidence{
		EngineID: info.ID, ObservedAt: time.Now().UTC(),
		AvailableNanoCPUs: int64(info.NCPU) * 1_000_000_000, AvailableMemoryBytes: info.MemTotal,
		AvailableStorageBytes: available,
	}, nil
}

func verifyFirstReportMount(self container.InspectResponse, hostname, engineRoot string) error {
	if len(self.ID) != 64 || !strings.HasPrefix(self.ID, hostname) || self.State == nil || !self.State.Running {
		return fmt.Errorf("Docker did not confirm the first-report capacity observer identity")
	}
	if !filepath.IsAbs(engineRoot) || filepath.Clean(engineRoot) != engineRoot || engineRoot == "/" {
		return fmt.Errorf("first-report Docker data-root is invalid")
	}
	for _, attachment := range self.Mounts {
		if attachment.Destination != firstReportCapacityMount {
			continue
		}
		if attachment.Type != mount.TypeBind || attachment.RW || attachment.Source != filepath.Join(engineRoot, firstReportCapacitySourceDir) {
			break
		}
		return nil
	}
	return fmt.Errorf("first-report capacity requires the Engine-owned read-only storage bind")
}
