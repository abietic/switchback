package localproduct

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestInitialRuntimePhysicalUnknownWithoutCIDRetainsPreparedEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, cid string
		unknown   bool
	}{
		{name: "unknown Create without CID", unknown: true},
		{name: "known CID after failed check", cid: "CID-A"},
		{name: "unknown Start with CID", cid: "CID-A", unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "working-copy")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			env := &runtimeEnvironment{prepared: map[string]preparedRuntime{"runtime-1": {Storage: workerapp.SessionStorage{WorkingCopy: workerapp.StorageMount{BackendPath: path}}}}}
			// Erroneous reconciliation would touch the unavailable database and discard
			// this environment. Every physical owner must return the original cause.
			runner := &runtimeRunner{environment: env}
			cause := errors.New(tc.name)
			started := workerapp.StartedRuntime{Ownership: workerapp.StartedWorkerRun{RuntimeInstanceID: "runtime-1"}, Runtime: workerapp.ProvisionedRuntime{RuntimeInstanceID: "runtime-1", ContainerID: tc.cid, PhysicalEffectUnknown: tc.unknown}}
			if got := runner.handleInitialRuntimeStartFailure(context.Background(), started, cause); got != cause {
				t.Fatalf("physical failure replaced: %v", got)
			}
			if _, ok := env.get("runtime-1"); !ok {
				t.Fatal("prepared environment discarded")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("working copy lost: %v", err)
			}
		})
	}
}

// A fixture reader grants only synthetic tests, never deployment proof.
type syntheticInitialCreatedLayerChecker struct{}

func (syntheticInitialCreatedLayerChecker) CheckCreatedLayer(context.Context, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) error {
	return nil
}
