package localproduct

import (
	"context"
	"log/slog"
	"sync"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type observabilityBackgroundRunner interface{ Run(context.Context) }

type fragmentBackgroundRunners struct {
	Publisher *artifactapp.FragmentPublisher
	Projector *artifactapp.FragmentWorker
}

func (r fragmentBackgroundRunners) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); r.Publisher.Run(ctx) }()
	go func() { defer workers.Done(); r.Projector.Run(ctx) }()
	workers.Wait()
}

// Activation is a synchronous assembly boundary before exposing the Handler or
// starting any dispatch owner. It performs no Langfuse or ObjectStore I/O.
func activateObservability(ctx context.Context, runner observabilityBackgroundRunner) observabilityBackgroundRunner {
	if r, ok := runner.(fragmentBackgroundRunners); ok {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := r.Projector.Jobs.Activate(ctx, artifactapp.FragmentProjectionVersion); err != nil {
			slog.Warn("observability_activation_unavailable")
			return nil
		}
	}
	return runner
}

func newObservabilityRunner(configuration Config, pool *pgxpool.Pool, objects workerapp.ObjectStore) observabilityBackgroundRunner {
	enabled, code := configuration.observabilityConfiguration()
	if !enabled {
		if code != "" {
			slog.Warn(code)
		}
		return nil
	}
	client, err := artifactlangfuse.NewClient(artifactlangfuse.ClientOptions{BaseURL: configuration.LangfuseBaseURL, PublicKey: configuration.LangfusePublicKey, SecretKey: configuration.LangfuseSecretKey})
	if err != nil {
		slog.Warn("observability_config_invalid")
		return nil
	}
	scanner, err := newRuntimeKnownTokenScanner(configuration)
	if err != nil {
		slog.Warn("observability_scanner_unavailable")
		return nil
	}
	projector, err := artifactlangfuse.NewFragmentProjector(client, scanner)
	if err != nil {
		slog.Warn("observability_config_invalid")
		return nil
	}
	jobs := artifactpostgres.NewFragmentJobStore(pool)
	ledger := artifactpostgres.NewObservationLedger(pool)
	return fragmentBackgroundRunners{
		Publisher: &artifactapp.FragmentPublisher{Store: ledger, Objects: objects},
		Projector: &artifactapp.FragmentWorker{Jobs: jobs, Loader: artifactapp.FragmentLoader{Metadata: ledger, Objects: objects}, Projector: projector, Report: func(code string) { slog.Warn(code) }},
	}
}
