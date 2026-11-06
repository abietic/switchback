package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func runCreatedLayer(args []string) int {
	return runCreatedLayerDiagnostic(args, runtime.GOOS, runtime.GOARCH, os.Stdout, os.Stderr, func(ctx context.Context, t capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection {
		return capacityprobe.CollectCreatedLayer(ctx, t, capacityprobe.CreatedLayerSources{Get: capacityprobe.NewCreatedLayerEngineReader(), Physical: capacityprobe.ReadCreatedLayerPhysical})
	})
}
func runCreatedLayerDiagnostic(args []string, goos, goarch string, out, diagnostics io.Writer, collect func(context.Context, capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection) int {
	if len(args) != 1 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(args[0]) {
		fmt.Fprintln(diagnostics, "usage: switchback-capacity-probe collect-created-layer <full lowercase CID>")
		return 2
	}
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		fmt.Fprintln(diagnostics, "created layer observation requires Linux amd64 or arm64")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := collect(ctx, capacityprobe.CreatedLayerTarget{ContainerID: args[0]})
	if e := json.NewEncoder(out).Encode(c); e != nil {
		fmt.Fprintln(diagnostics, "created layer diagnostic output unavailable")
		return 2
	}
	// Finite observation never grants Trust, capacity readiness or execution.
	return 1
}
