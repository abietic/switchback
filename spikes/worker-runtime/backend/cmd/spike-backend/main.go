package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"switchback.local/spikes/worker-runtime/backend/internal/config"
	"switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/httpapi"
	"switchback.local/spikes/worker-runtime/backend/internal/langfuse"
	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	"switchback.local/spikes/worker-runtime/backend/internal/service"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, path := range []string{cfg.ContainerDataRoot, cfg.ObjectStoreRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine, err := runtime.NewDockerClient()
	if err != nil {
		return fmt.Errorf("construct Docker client: %w", err)
	}
	defer engine.Close()
	application, err := newApplication(root, cfg, engine, os.ReadFile)
	if err != nil {
		return err
	}
	return serve(root, cfg, application)
}

type application struct {
	registry    *control.Registry
	coordinator *service.Coordinator
	provisioner runtime.Provisioner
	projector   service.LangfuseProjector
}

func newApplication(
	root context.Context,
	cfg config.Config,
	engine runtime.DockerEngine,
	readModelKey func(string) ([]byte, error),
) (*application, error) {
	if readModelKey == nil {
		return nil, fmt.Errorf("read model key dependency is required")
	}
	modelKey, err := readTrustedModelKey(cfg.ModelKeyFile, readModelKey)
	if err != nil {
		return nil, err
	}
	mapper := runtime.PathMapper{
		ContainerDataRoot: cfg.ContainerDataRoot,
		HostDataRoot:      cfg.HostDataRoot,
	}
	hostPathVerifier, err := runtime.NewDockerHostPathVerifier(root, engine, cfg.WorkerImage)
	if err != nil {
		return nil, fmt.Errorf("construct Docker HostPath verifier: %w", err)
	}
	provisioner, err := runtime.NewDockerProvisioner(engine, runtime.DockerOptions{
		Image:              cfg.WorkerImage,
		Network:            cfg.WorkerNetwork,
		Mapper:             mapper,
		FixturePath:        cfg.FixturePath,
		BackendGRPCTarget:  cfg.BackendGRPCTarget,
		BackendHTTPBaseURL: cfg.BackendHTTPBaseURL,
		HostPathVerifier:   hostPathVerifier,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Docker provisioner: %w", err)
	}
	resolveSupplemental, err := service.NewTrustedSupplementalPathResolver(service.TrustedSupplementalResolverOptions{
		Mapper: mapper,
		ModelSecretSource: runtime.PathPair{
			ContainerPath: cfg.ModelKeyFile,
			HostPath:      cfg.HostModelKeyFile,
		},
		ModelKey:        []byte(modelKey),
		UpstreamModel:   cfg.UpstreamModel,
		UpstreamAPIBase: cfg.UpstreamAPIBase,
		AgentProfile:    cfg.AgentProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("construct trusted supplemental resolver: %w", err)
	}
	langfuseClient, err := langfuse.NewClient(langfuse.ClientOptions{
		BaseURL:   cfg.LangfuseBaseURL,
		PublicKey: cfg.LangfusePublicKey,
		SecretKey: cfg.LangfuseSecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Langfuse client: %w", err)
	}
	exporter, err := langfuse.NewExporter(langfuseClient, langfuse.ExporterOptions{})
	if err != nil {
		return nil, fmt.Errorf("construct Langfuse exporter: %w", err)
	}
	verifier, err := langfuse.NewVerifier(langfuseClient, langfuse.VerifierOptions{})
	if err != nil {
		return nil, fmt.Errorf("construct Langfuse verifier: %w", err)
	}
	projector, err := langfuse.NewProjector(exporter, verifier, langfuse.ProjectorOptions{})
	if err != nil {
		return nil, fmt.Errorf("construct Langfuse projector: %w", err)
	}
	registry := control.NewRegistry()
	scheduler, err := service.NewRuntimeScheduler(
		registry,
		service.NewRuntimeProvisionerAdapter(provisioner, resolveSupplemental),
		cfg.AgentProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("construct runtime scheduler: %w", err)
	}
	coordinator := service.NewCoordinator(service.Options{
		RootContext: root,
		Materializer: serverMaterializer{Materializer: runtime.Materializer{
			Mapper:      mapper,
			FixturePath: cfg.FixturePath,
			Ownership:   runtime.PathOwnershipFunc(os.Chown),
		}},
		ObjectStore: objectstore.LocalFileObjectStore{
			Root: cfg.ObjectStoreRoot,
			Redactor: redact.New(redact.Secrets{
				UpstreamModelKey:  modelKey,
				LangfusePublicKey: cfg.LangfusePublicKey,
				LangfuseSecretKey: cfg.LangfuseSecretKey,
			}),
		},
		Scheduler:         scheduler,
		LangfuseProjector: projector,
	})
	return &application{
		registry:    registry,
		coordinator: coordinator,
		provisioner: provisioner,
		projector:   projector,
	}, nil
}

func serve(root context.Context, cfg config.Config, application *application) error {
	if application == nil || application.registry == nil || application.coordinator == nil {
		return fmt.Errorf("Backend application is incomplete")
	}
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewHandler(application.coordinator),
		ReadHeaderTimeout: 5 * time.Second,
	}
	grpcListener, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		return fmt.Errorf("listen for worker control: %w", err)
	}
	defer grpcListener.Close()
	grpcServer := grpc.NewServer()
	workercontrolv1.RegisterWorkerControlServer(grpcServer, control.NewServer(application.registry, application.coordinator))

	errCh := make(chan error, 2)
	go func() { errCh <- httpServer.ListenAndServe() }()
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	select {
	case err := <-errCh:
		grpcServer.Stop()
		_ = httpServer.Close()
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-root.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		grpcServer.Stop()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return err
		}
		return nil
	}
}

func readTrustedModelKey(path string, readFile func(string) ([]byte, error)) (string, error) {
	content, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("read root-only model key: %w", err)
	}
	if len(content) == 0 || len(content) > 4<<10 {
		return "", fmt.Errorf("root-only model key file must contain one bounded token")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("root-only model key file must contain one bounded token")
	}
	return value, nil
}

type serverMaterializer struct {
	runtime.Materializer
}

func (m serverMaterializer) Rollback(_ context.Context, sessionID string, _ runtime.MaterializedSession) error {
	session, err := m.Mapper.SessionPath(sessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(session.ContainerPath)
}
