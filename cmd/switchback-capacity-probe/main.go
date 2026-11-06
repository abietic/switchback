// switchback-capacity-probe is a blocked-only diagnostic collector, never a signer.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func run(args []string) int {
	if len(args) > 0 && args[0] == "collect-created-layer" {
		return runCreatedLayer(args[1:])
	}
	// Fixed deployment-owned paths; no Task-controlled CLI path, endpoint or command.
	if len(args) != 1 || args[0] != "collect" {
		fmt.Fprintln(os.Stderr, "usage: switchback-capacity-probe collect (requires separately authorized Linux deployment)")
		return 2
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "actual collection requires Linux")
		return 2
	}
	m, err := capacityprobe.LoadDeploymentManifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "deployment manifest rejected or unavailable")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collection := capacityprobe.Collect(ctx, m, capacityprobe.Sources{Get: capacityprobe.NewEngineReader(), Linux: capacityprobe.ReadLinux})
	if err := capacityprobe.PublishBlocked("/mnt/lima-sbquota/core/blocked-status", collection); err != nil {
		fmt.Fprintln(os.Stderr, "publication failed; external admission shutdown and Trust revocation are unproven")
		return 2
	}
	fmt.Println("preparation_blocked: untrusted diagnostics published; no admission authority")
	return 1
}
func main() { os.Exit(run(os.Args[1:])) }
