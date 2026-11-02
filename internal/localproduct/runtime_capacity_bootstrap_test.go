package localproduct

import (
	"context"
	"errors"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func TestRuntimeCapacityBootstrapOrder(t *testing.T) {
	c := Config{RuntimeCapacityTrustFile: "/deployment/trust.json", RuntimeCapacityAdmissionManifestFile: "/deployment/admission-manifest.json", RuntimeCapacitySnapshotDirectory: "/deployment/snapshots"}
	for _, failure := range []string{"pending", "invalid", "cancel", "timeout", "role", "success"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			denied := errors.New(failure)
			err := awaitRuntimeCapacityBootstrap(context.Background(), c, capacityBootstrap{
				await: func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error) {
					events = append(events, "await")
					if failure != "role" && failure != "success" {
						return nil, denied
					}
					return &capacitysnapshot.DeploymentQualification{}, nil
				},
				role: func(context.Context, *capacitysnapshot.DeploymentQualification) error {
					events = append(events, "role")
					if failure == "role" {
						return denied
					}
					return nil
				},
			})
			if (err == nil) != (failure == "success") {
				t.Fatalf("unexpected result %v", err)
			}
			want := 1
			if failure == "role" || failure == "success" {
				want = 2
			}
			if len(events) != want {
				t.Fatalf("events %v", events)
			}
		})
	}
}

func TestRuntimeCapacityBootstrapNoCapacityAndPartial(t *testing.T) {
	panicOps := capacityBootstrap{await: func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error) {
		t.Fatal("unexpected wait")
		return nil, nil
	}}
	if err := awaitRuntimeCapacityBootstrap(context.Background(), Config{}, panicOps); err != nil {
		t.Fatal(err)
	}
	for mask := 1; mask < 7; mask++ {
		c := Config{}
		if mask&1 != 0 {
			c.RuntimeCapacityTrustFile = "/deployment/trust.json"
		}
		if mask&2 != 0 {
			c.RuntimeCapacitySnapshotDirectory = "/deployment/snapshots"
		}
		if mask&4 != 0 {
			c.RuntimeCapacityAdmissionManifestFile = "/deployment/admission-manifest.json"
		}
		if err := awaitRuntimeCapacityBootstrap(context.Background(), c, panicOps); err == nil {
			t.Fatalf("partial %d accepted", mask)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AwaitRuntimeCapacityBootstrap(ctx, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}

func TestRuntimeCapacityBootstrapExecutionNeedsConfiguredCapacity(t *testing.T) {
	if err := AwaitRuntimeCapacityBootstrap(context.Background(), Config{ExecutionEnabled: true}); err == nil {
		t.Fatal("execution without qualification proceeded to New")
	}
}
