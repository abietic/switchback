package capacitypublisher

import (
	"context"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"testing"
)

func TestPublisherMissingQualificationBeforePrivatePaths(t *testing.T) {
	c, _, _ := publisherFixture(t, false)
	for _, q := range []*capacitysnapshot.DeploymentQualification{nil, {}} {
		c.qualification = q
		if e := verifyPublisherDeployment(context.Background(), c); e == nil {
			t.Fatal("structural metadata qualified")
		}
	}

}
