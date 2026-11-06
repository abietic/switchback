//go:build integration

package localproduct

import (
	"context"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"net"
	"time"

	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc"
)

// Selected Task2i initial-positive fixtures alone opt into signed SYNTHETIC
// Engine facts and a manually driven scheduler. Real Issue/Start/consume,
// Activate and control journal authority remain in place. No recovery positive.
func enableInitialCapacityFixture(f *supplementFixture) *runtimecapacity.FileInventory {
	inventory := runtimecapacity.NewFileInventory(f.t)
	c := f.app.configuration
	c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = inventory.TrustPath, inventory.Directory
	source, err := newSyntheticInitialCapacitySource(f.ctx, c, f.pool)
	supplementMust(f.t, err)
	f.app.configuration, f.app.runtime.configuration = c, c
	f.app.initialPolicy, f.app.runtime.initialPolicy = source, source
	installRuntimeStartPolicyFixture(f.t, f.app, unavailableRuntimeStartPolicy{})
	f.manualInitial = true
	f.publishInitial = func(r workerapp.RuntimeProvisionRequest) {
		inventory.Observe(r.WorkerSessionID, r.RuntimeInstanceID, "fixture-"+r.RuntimeInstanceID, r.Generation)
	}
	return inventory
}

func (f *supplementFixture) runManualInitialFixture() error {
	listener, err := net.Listen("tcp", f.app.runtime.configuration.GRPCAddress)
	if err != nil {
		close(f.manualReady)
		return err
	}
	defer listener.Close()
	server := grpc.NewServer()
	controlgrpc.RegisterWorkerControlRPCServer(server, controlgrpc.NewServer(f.app.runtime.hub))
	exited := make(chan error, 1)
	go func() { exited <- server.Serve(listener) }()
	f.app.runtime.startupReady.Store(true)
	close(f.manualReady)
	<-f.ctx.Done()
	f.app.runtime.turnSupervisors.stopAndWait()
	server.Stop()
	<-exited
	return nil
}

func (f *supplementFixture) driveInitialTick() {
	f.t.Helper()
	f.t.Log("owner trace: manual tick entered")
	// Both calls finish before next() observes a prompt. The supervisor is the
	// sole remaining asynchronous owner and is joined at the settlement barrier.
	supplementMust(f.t, f.app.runtime.startNextQueuedTask(f.ctx))
	supplementMust(f.t, f.app.runtime.executeNextDeliveredTurn(f.ctx))
	f.t.Log("owner trace: manual tick exited; supervisor scheduled")
}

// SYNTHETIC legacy file assembly exists only in integration test compilation.
// It exercises original reconciliation/PG owner semantics; no production factory
// accepts it or calls it, and it does not produce a deployment validation result.
func newSyntheticInitialCapacitySource(ctx context.Context, c Config, pool *pgxpool.Pool) (runtimeStartPolicy, error) {
	trust, err := capacitysnapshot.ReadTrust(c.RuntimeCapacityTrustFile)
	if err != nil {
		return nil, err
	}
	reader, err := capacitysnapshot.NewReader(c.RuntimeCapacitySnapshotDirectory)
	if err != nil {
		return nil, err
	}
	observer, err := capacityobserver.New(trust)
	if err != nil {
		return nil, err
	}
	store, err := workerpostgres.NewCapacityStore(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err = store.InitializeTrust(ctx, trust); err != nil {
		return nil, err
	}
	return syntheticInitialCapacitySource{initialCapacitySource{store: store, reader: reader, createdLayer: observer}}, nil
}

func installSyntheticInitialCapacitySource(f *supplementFixture, inventory *runtimecapacity.FileInventory) {
	c := f.app.configuration
	c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = inventory.TrustPath, inventory.Directory
	source, err := newSyntheticInitialCapacitySource(f.ctx, c, f.pool)
	supplementMust(f.t, err)
	f.app.configuration, f.app.runtime.configuration = c, c
	f.app.initialPolicy, f.app.runtime.initialPolicy = source, source
}

// Test-only wrapper deliberately tests the original PG reconciliation seam.
// It is never returned by the production factory or treated as qualification.
type syntheticInitialCapacitySource struct{ initialCapacitySource }

func (s syntheticInitialCapacitySource) checkRuntimeStart(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.store.ReconcileCapacity(bounded, s.reader)
}
