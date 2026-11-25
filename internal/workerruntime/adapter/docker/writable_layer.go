package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type engineInfoReader interface {
	Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error)
}

func checkWritableLayerEngine(ctx context.Context, engine Engine) error {
	reader, ok := engine.(engineInfoReader)
	if !ok || isNilInterface(reader) {
		return fmt.Errorf("Worker writable-layer quota requires Docker Engine Info")
	}
	_, err := readWritableLayerEngine(ctx, reader)
	return err
}

func readWritableLayerEngine(ctx context.Context, reader engineInfoReader) (system.Info, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := reader.Info(bounded, client.InfoOptions{})
	if err != nil {
		return system.Info{}, fmt.Errorf("inspect Docker writable-layer support: %w", err)
	}
	if err := bounded.Err(); err != nil {
		return system.Info{}, fmt.Errorf("inspect Docker writable-layer support: %w", err)
	}
	info := result.Info
	if info.OSType != "linux" || info.Driver != "overlay2" {
		return system.Info{}, fmt.Errorf("Worker writable-layer quota requires classic Linux overlay2 on XFS (os=%q driver=%q)", info.OSType, info.Driver)
	}
	xfs := false
	for _, entry := range info.DriverStatus {
		switch strings.ToLower(strings.TrimSpace(entry[0])) {
		case "driver-type":
			return system.Info{}, fmt.Errorf("Worker writable-layer quota requires the classic storage driver")
		case "backing filesystem":
			if !strings.EqualFold(strings.TrimSpace(entry[1]), "xfs") {
				return system.Info{}, fmt.Errorf("Worker writable-layer quota requires an XFS backing filesystem")
			}
			xfs = true
		}
	}
	if !xfs {
		return system.Info{}, fmt.Errorf("Worker writable-layer quota requires XFS backing filesystem evidence")
	}
	return info, nil
}

func writableLayerLimit(request application.RuntimeProvisionRequest, profile config.RuntimeResourceProfile) string {
	if config.IsFirstReportAgentProfile(request.AgentProfile.ID) {
		return strconv.FormatInt(profile.WritableLayerBytes, 10)
	}
	return ""
}
