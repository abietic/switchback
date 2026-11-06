package capacitypublisher

import (
	"context"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// Kept separate from configuration/key/Store construction: only the fixed
// manifest leaf may be absent while the same final publisher process waits.
func bootstrapPublisher(ctx context.Context, await func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error)) (*capacitysnapshot.DeploymentQualification, error) {
	if ctx == nil || ctx.Err() != nil || await == nil {
		return nil, publisherError(ctx, errPublisherDeployment)
	}
	q, err := await(ctx, publisherConfigPath+"/admission-manifest.json", publisherConfigPath+"/trust.json")
	if err != nil || q == nil || ctx.Err() != nil {
		return nil, publisherError(ctx, errPublisherDeployment)
	}
	return q, nil
}
