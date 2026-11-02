package localproduct

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Initial preflight is advisory. Original Issue/Start/consume transactions are
// the authority. Never inherit the generic execution policy here.
func (r *runtimeRunner) checkInitialCapacity(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.initialPolicy == nil {
		return workerapp.CapacityUninitialized
	}
	return r.initialPolicy.checkRuntimeStart(ctx)
}

// Constructed once at common App assembly, including execution-disabled Apps.
// Synthetic reconciliation assembly is isolated in integration test files.
func newInitialCapacitySource(ctx context.Context, c Config, pool *pgxpool.Pool) (runtimeStartPolicy, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.validateCapacityPaths(); err != nil {
		return nil, err
	}
	if c.RuntimeCapacityTrustFile == "" {
		return nil, nil
	}
	if c.RuntimeCapacityTrustFile != filepath.Join(filepath.Dir(c.RuntimeCapacityAdmissionManifestFile), "trust.json") {
		return nil, fmt.Errorf("runtime capacity trust must use fixed qualification sibling")
	}
	trust, err := capacitysnapshot.ReadTrust(c.RuntimeCapacityTrustFile)
	if err != nil {
		return nil, err
	}
	return assembleInitialCapacitySource(ctx, c, pool, trust, initialCapacityAssembly{
		qualification: func(ctx context.Context, path string, t capacitysnapshot.Trust) (initialDeploymentQualification, error) {
			q, e := capacitysnapshot.OpenDeploymentQualification(ctx, path, t)
			if e != nil {
				return nil, e
			}
			return backendDeploymentQualification{q}, nil
		},
		reader: capacitysnapshot.NewReader, observer: capacityobserver.New,
		store: func(ctx context.Context, p *pgxpool.Pool) (initialCapacityStore, error) {
			return workerpostgres.NewCapacityStore(ctx, p)
		},
	})
}

// This seam is package-private and used only by offline orchestration tests.
// The sole production caller above hardwires every real reader/role/PG owner.
type initialDeploymentQualification interface {
	metadata() capacitysnapshot.AdmissionManifestMetadata
	check(context.Context) error
}
type backendDeploymentQualification struct {
	q *capacitysnapshot.DeploymentQualification
}

func (v backendDeploymentQualification) metadata() capacitysnapshot.AdmissionManifestMetadata {
	return v.q.Metadata()
}
func (v backendDeploymentQualification) check(ctx context.Context) error {
	return checkBackendCapacityRole(ctx, v.q)
}

type initialCapacityStore interface {
	InitializeTrust(context.Context, capacitysnapshot.Trust) error
	ReconcileCapacity(context.Context, *capacitysnapshot.Reader) error
}
type initialCapacityAssembly struct {
	qualification func(context.Context, string, capacitysnapshot.Trust) (initialDeploymentQualification, error)
	reader        func(string) (*capacitysnapshot.Reader, error)
	observer      func(capacitysnapshot.Trust) (*capacityobserver.Checker, error)
	store         func(context.Context, *pgxpool.Pool) (initialCapacityStore, error)
}

func assembleInitialCapacitySource(ctx context.Context, c Config, pool *pgxpool.Pool, trust capacitysnapshot.Trust, assembly initialCapacityAssembly) (runtimeStartPolicy, error) {
	if ctx == nil || assembly.qualification == nil || assembly.reader == nil || assembly.observer == nil || assembly.store == nil {
		return nil, fmt.Errorf("runtime capacity assembly invalid")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	qualification, err := assembly.qualification(ctx, c.RuntimeCapacityAdmissionManifestFile, trust)
	if err != nil {
		return nil, err
	}
	if qualification == nil {
		return nil, fmt.Errorf("runtime capacity qualification unavailable")
	}
	if qualification.metadata().Inventory.WorkerImage != c.WorkerImage {
		return nil, fmt.Errorf("runtime capacity deployment image binding refused")
	}
	if err := qualification.check(ctx); err != nil {
		return nil, err
	}
	reader, err := assembly.reader(c.RuntimeCapacitySnapshotDirectory)
	if err != nil {
		return nil, fmt.Errorf("runtime capacity snapshot directory is unavailable or unsafe")
	}
	createdLayer, err := assembly.observer(trust)
	if err != nil {
		return nil, fmt.Errorf("runtime created-layer observer configuration refused")
	}
	store, err := assembly.store(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("runtime capacity store is unavailable")
	}
	if err := store.InitializeTrust(ctx, trust); err != nil {
		return nil, fmt.Errorf("runtime capacity trust initialization refused")
	}
	return initialCapacitySource{store: store, reader: reader, createdLayer: createdLayer, qualification: qualification}, nil
}

type initialCapacitySource struct {
	qualification initialDeploymentQualification
	store         initialCapacityStore
	reader        *capacitysnapshot.Reader
	createdLayer  *capacityobserver.Checker
}

func (s initialCapacitySource) checkRuntimeStart(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if s.qualification == nil {
		return fmt.Errorf("runtime capacity qualification unavailable")
	}
	if err := s.qualification.check(bounded); err != nil {
		return err
	}
	return s.store.ReconcileCapacity(bounded, s.reader)
}

// Reuse only the concrete source assembled from deployment Trust. Missing Trust
// or an unrelated initial policy retains the explicit unavailable checker.
func initialCreatedLayerChecker(policy runtimeStartPolicy) workerapp.InitialCreatedLayerChecker {
	if source, ok := policy.(initialCapacitySource); ok && source.createdLayer != nil {
		return source
	}
	return workerapp.UnavailableInitialCreatedLayerChecker{}
}

func (s initialCapacitySource) CheckCreatedLayer(ctx context.Context, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	return s.createdLayer.CheckCreatedLayer(ctx, request, identity)
}
