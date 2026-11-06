package localproduct

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// AwaitRuntimeCapacityBootstrap is the pre-New startup barrier. It opens no PG
// connection or runtime dependency. New retains its own qualification checks.
func AwaitRuntimeCapacityBootstrap(ctx context.Context, c Config) error {
	return awaitRuntimeCapacityBootstrap(ctx, c, capacityBootstrap{capacitysnapshot.AwaitDeploymentQualification, checkBackendCapacityRole})
}

type capacityBootstrap struct {
	await func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error)
	role  func(context.Context, *capacitysnapshot.DeploymentQualification) error
}

func awaitRuntimeCapacityBootstrap(ctx context.Context, c Config, ops capacityBootstrap) error {
	if ctx == nil {
		return fmt.Errorf("runtime capacity bootstrap context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateCapacityPaths(); err != nil {
		return err
	}
	// Existing non-execution local configurations do not acquire a new dependency.
	if c.RuntimeCapacityTrustFile == "" {
		if c.ExecutionEnabled {
			return fmt.Errorf("runtime capacity bootstrap configuration required for execution")
		}
		return nil
	}
	if c.RuntimeCapacityTrustFile != filepath.Join(filepath.Dir(c.RuntimeCapacityAdmissionManifestFile), "trust.json") || ops.await == nil || ops.role == nil {
		return fmt.Errorf("runtime capacity bootstrap configuration invalid")
	}
	q, err := ops.await(ctx, c.RuntimeCapacityAdmissionManifestFile, c.RuntimeCapacityTrustFile)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("runtime capacity bootstrap qualification unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ops.role(ctx, q)
}
