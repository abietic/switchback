package localproduct

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifacthttp "github.com/abietic/switchback/internal/artifacttrace/http"
	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontierhttp "github.com/abietic/switchback/internal/frontier/http"
	frontiersse "github.com/abietic/switchback/internal/frontier/sse"
	gatewaypostgres "github.com/abietic/switchback/internal/gateway/adapter/postgres"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

type App struct {
	configuration    Config
	initialPolicy    runtimeStartPolicy
	pool             *pgxpool.Pool
	ownsPool         bool
	handler          http.Handler
	outbox           platformpostgres.OutboxDispatcher
	runtime          *runtimeRunner
	assistant        *assistantRunner
	observability    observabilityBackgroundRunner
	telemetry        *telemetryRunner
	fragmentHistory  artifactapp.FragmentHistory
	fragmentCoverage artifactapp.FragmentCoverageReader
}

func New(ctx context.Context, configuration Config) (*App, error) {
	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configuration.DataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("prepare Switchback data root: %w", err)
	}
	pool, err := pgxpool.New(ctx, configuration.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	fail := func(err error) (*App, error) {
		pool.Close()
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return fail(fmt.Errorf("ping PostgreSQL: %w", err))
	}
	app, err := newWithPool(ctx, configuration, pool)
	if err != nil {
		return fail(err)
	}
	app.ownsPool = true
	return app, nil
}

func newWithPool(ctx context.Context, configuration Config, pool *pgxpool.Pool) (*App, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL pool is required")
	}
	if err := requireSchema(ctx, pool, configuration.Assistant.Enabled); err != nil {
		return nil, err
	}
	initialPolicy, err := newInitialCapacitySource(ctx, configuration, pool)
	if err != nil {
		return nil, err
	}
	objects, err := artifactlocal.NewObjectStore(configuration.DataRoot)
	if err != nil {
		return nil, err
	}
	ids := randomIDs{}
	now := time.Now
	profiles := config.ProductionProfileRegistry()
	commands, err := gatewaypostgres.NewCommandService(gatewaypostgres.CommandServiceOptions{
		Pool: pool, IDs: ids, Now: now, ProjectID: configuration.ProjectID, Profiles: profiles,
		NetworkProfile: configuration.NetworkProfile, Assistant: configuration.Assistant,
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID: configuration.Profile.TenantID, WorkspaceID: configuration.WorkspaceID,
			WorkspaceRevisionID: configuration.WorkspaceRevisionID, ResolutionID: "resolution-local",
			ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
	})
	if err != nil {
		return nil, err
	}
	projectionStore := projectionpostgres.NewStore(pool)
	viewer := projectionapp.Viewer{TenantID: configuration.Profile.TenantID, UserID: configuration.Profile.ActorUserID}
	views, err := projectionapp.NewViewContinuityReader(
		projectionStore,
		projectionapp.ViewContinuityReaderOptions{Now: now, ReplayLimit: 128},
	)
	if err != nil {
		return nil, fmt.Errorf("compose Frontier View reader: %w", err)
	}
	transientDisplay, err := projectionapp.NewTransientBroker(projectionapp.TransientPolicy{
		MaxItems: 256, MaxBytes: 1 << 20,
	})
	if err != nil {
		return nil, err
	}
	stream, err := frontiersse.NewHandler(frontiersse.Options{
		Viewer: viewer, Views: views,
		Transient: transientDisplay,
	})
	if err != nil {
		return nil, err
	}
	frontier, err := frontierhttp.NewHandler(frontierhttp.Options{
		Viewer: viewer, Views: views,
		Streams: stream, Timeline: projectionStore,
		Diagnostics: projectionStore, WorkspaceID: configuration.WorkspaceID,
		GrafanaBrowserBaseURL:  configuration.GrafanaBrowserBaseURL,
		LangfuseBrowserBaseURL: configuration.LangfuseBrowserBaseURL, LangfuseProjectID: configuration.LangfuseProjectID,
		ProjectionNavigation: artifactpostgres.NewProjectionNavigationStore(pool, configuration.Profile.TenantID, configuration.Profile.ActorUserID, artifactlangfuse.ProjectionNavigationCompiler{}),
	})
	if err != nil {
		return nil, err
	}
	materializer := projectionpostgres.Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: projectionStore}, Objects: objects,
		Views: views, UserID: configuration.Profile.ActorUserID, Now: now,
	}
	artifactContent, err := artifacthttp.NewContentHandler(artifacthttp.FixedViewer{
		TenantID: configuration.Profile.TenantID, UserID: configuration.Profile.ActorUserID,
	}, artifactpostgres.ContentReader{Pool: pool}, objects)
	if err != nil {
		return nil, err
	}
	if err := materializer.Bootstrap(ctx, configuration.Profile.TenantID); err != nil {
		return nil, fmt.Errorf("bootstrap Frontier Task index: %w", err)
	}
	app := &App{configuration: configuration, pool: pool, initialPolicy: initialPolicy,
		telemetry: newTelemetryRunner(configuration),
		handler:   productHandler{frontier: frontier, artifactContent: artifactContent}}
	taskStore := taskpostgres.NewStore(pool)
	app.outbox = platformpostgres.OutboxDispatcher{
		Pool: pool,
		Publisher: runtimeFailureCancellationPublisher{Pool: pool, Actions: taskapp.TaskActionService{Store: taskStore, IDs: ids, Now: now}, Next: runtimeFailureEventPublisher{
			Consumer: taskapp.RuntimeFailureConsumer{Reader: taskStore, Pause: taskapp.TaskPauseService{Store: taskStore, IDs: ids, Now: now}},
			Next:     materializer,
		}},
		LeaseOwner: "localproduct-outbox", LeaseDuration: 30 * time.Second, BatchSize: 64, Now: now,
	}
	if configuration.Assistant.Enabled {
		app.assistant, err = newAssistantRunner(configuration, pool, profiles, ids, now)
		if err != nil {
			return nil, fmt.Errorf("compose local Assistant: %w", err)
		}
	}
	app.observability = activateObservability(ctx, newObservabilityRunner(configuration, pool, objects))
	if configuration.ExecutionEnabled {
		app.runtime, err = newRuntimeRunner(configuration, pool, objects, materializer, transientDisplay, initialPolicy)
		if err != nil {
			return nil, fmt.Errorf("compose local Worker Runtime: %w", err)
		}
	}
	var startPolicy runtimeStartPolicy = unavailableRuntimeStartPolicy{}
	if app.runtime != nil {
		startPolicy = app.runtime.startPolicy
	}
	gateway, err := gatewayhttp.NewHandlerWithOptions(gatewayhttp.HandlerOptions{
		Actor:     gatewayhttp.FixedActor{TenantID: configuration.Profile.TenantID, UserID: configuration.Profile.ActorUserID},
		Commands:  runtimeGuardedCommands{commands: commands, policy: startPolicy},
		Assistant: commands,
	})
	if err != nil {
		app.Close()
		return nil, err
	}
	app.handler = productHandler{gateway: gateway, frontier: frontier, artifactContent: artifactContent}
	app.fragmentHistory = artifactpostgres.NewFragmentHistoryStore(pool, configuration.Profile.TenantID, configuration.Profile.ActorUserID, objects, app.runtime)
	app.fragmentCoverage = artifactpostgres.NewFragmentCoverageStore(pool, configuration.Profile.TenantID, configuration.Profile.ActorUserID)
	return app, nil
}

func (app *App) Handler() http.Handler { return app.handler }

func (app *App) Close() {
	if app != nil && app.runtime != nil {
		app.runtime.Close()
	}
	if app != nil && app.pool != nil && app.ownsPool {
		app.pool.Close()
	}
}

func (app *App) Run(ctx context.Context) error {
	if app == nil || app.pool == nil {
		return fmt.Errorf("local product is not configured")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if app.telemetry != nil {
		done := make(chan struct{})
		go func() { defer close(done); app.telemetry.Run(runCtx) }()
		defer func() { cancel(); <-done }()
	}
	if app.observability != nil {
		done := make(chan struct{})
		go func() { defer close(done); app.observability.Run(runCtx) }()
		defer func() { cancel(); <-done }()
	}
	errors := make(chan error, 3)
	go func() { errors <- app.runOutbox(runCtx) }()
	if app.runtime != nil {
		go func() { errors <- app.runtime.Run(runCtx) }()
	}
	if app.assistant != nil {
		go func() { errors <- app.assistant.Run(runCtx) }()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errors:
		return err
	}
}

func (app *App) runOutbox(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := app.outbox.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("dispatch Outbox to Frontier projection", "error", err)
			}
		}
	}
}

func requireSchema(ctx context.Context, pool *pgxpool.Pool, assistantEnabled bool) error {
	var ready bool
	if err := pool.QueryRow(ctx, `
SELECT to_regclass('tasks') IS NOT NULL
   AND to_regclass('view_projections') IS NOT NULL
	   AND to_regclass('artifact_refs') IS NOT NULL
	   AND to_regclass('worker_user_commands') IS NOT NULL
	   AND to_regclass('worker_approval_requests') IS NOT NULL
	   AND to_regclass('display_checkpoints') IS NOT NULL
	   AND to_regclass('worker_timeline_turns') IS NOT NULL
	   AND to_regclass('runtime_control_channels') IS NOT NULL
	   AND to_regclass('runtime_control_messages') IS NOT NULL
	   AND to_regclass('worker_turn_settlements') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("inspect PostgreSQL schema: %w", err)
	}
	if !ready {
		return fmt.Errorf("PostgreSQL schema is incomplete; apply Switchback migrations before startup")
	}
	if !assistantEnabled {
		return nil
	}
	if err := pool.QueryRow(ctx, `
SELECT to_regclass('assistant_conversations') IS NOT NULL
   AND to_regclass('assistant_runs') IS NOT NULL
   AND to_regclass('assistant_context_packs') IS NOT NULL
   AND to_regclass('assistant_task_drafts') IS NOT NULL
   AND to_regclass('assistant_readiness_assessments') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("inspect Assistant PostgreSQL schema: %w", err)
	}
	if !ready {
		return fmt.Errorf("Assistant PostgreSQL schema is incomplete; apply Switchback migrations before startup")
	}
	return nil
}

type productHandler struct {
	gateway         http.Handler
	frontier        http.Handler
	artifactContent http.Handler
}

func (handler productHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/v1/artifacts/") && strings.HasSuffix(request.URL.Path, "/content") {
		handler.artifactContent.ServeHTTP(response, request)
		return
	}
	if request.URL.Path != "/api/v1/views" && len(request.URL.Path) >= len("/api/v1/views/") &&
		request.URL.Path[:len("/api/v1/views/")] == "/api/v1/views/" {
		handler.frontier.ServeHTTP(response, request)
		return
	}
	handler.gateway.ServeHTTP(response, request)
}
