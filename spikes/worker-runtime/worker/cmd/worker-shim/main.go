package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/internal/redact"
	"switchback.local/spikes/worker-runtime/worker/internal/acp"
	workercontrol "switchback.local/spikes/worker-runtime/worker/internal/control"
	"switchback.local/spikes/worker-runtime/worker/internal/modelobs"
	"switchback.local/spikes/worker-runtime/worker/internal/process"
	"switchback.local/spikes/worker-runtime/worker/internal/upload"
)

const (
	bootstrapPath   = "/run/switchback-secrets/bootstrap.json"
	modelKeyPath    = "/run/switchback-secrets/model"
	acpTracePath    = "/trace-spool/acp.jsonl"
	workingCopyPath = "/workspace"
	agentStatePath  = "/agent-state"
	traceSpoolPath  = "/trace-spool"

	evidenceHTTPTimeout = 15 * time.Second

	envWorkerSessionID   = "SWITCHBACK_WORKER_SESSION_ID"
	envWorkerRunID       = "SWITCHBACK_WORKER_RUN_ID"
	envRuntimeInstance   = "SWITCHBACK_RUNTIME_INSTANCE_ID"
	envRuntimeGeneration = "SWITCHBACK_RUNTIME_GENERATION"
	envWorkerToken       = "SWITCHBACK_WORKER_TOKEN"
	envBackendGRPC       = "SWITCHBACK_BACKEND_GRPC_TARGET"
	envBackendHTTP       = "SWITCHBACK_BACKEND_HTTP_BASE_URL"
)

type runtimeConfig struct {
	Identity           workercontrol.RuntimeIdentity
	WorkerToken        string
	BackendGRPCTarget  string
	BackendHTTPBaseURL string
}

func (c runtimeConfig) String() string {
	return fmt.Sprintf(
		"runtimeConfig{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d BackendGRPCTarget:%q BackendHTTPBaseURL:%q WorkerToken:[REDACTED]}",
		c.Identity.WorkerSessionID,
		c.Identity.WorkerRunID,
		c.Identity.RuntimeInstanceID,
		c.Identity.RuntimeGeneration,
		c.BackendGRPCTarget,
		c.BackendHTTPBaseURL,
	)
}

func (c runtimeConfig) GoString() string {
	return c.String()
}

func loadRuntimeConfig(lookup func(string) (string, bool)) (runtimeConfig, error) {
	if lookup == nil {
		return runtimeConfig{}, fmt.Errorf("runtime environment lookup is required")
	}
	required := []string{
		envWorkerSessionID,
		envWorkerRunID,
		envRuntimeInstance,
		envRuntimeGeneration,
		envWorkerToken,
		envBackendGRPC,
		envBackendHTTP,
	}
	values := make(map[string]string, len(required))
	for _, key := range required {
		value, ok := lookup(key)
		if !ok || value == "" {
			return runtimeConfig{}, fmt.Errorf("%s is required", key)
		}
		values[key] = value
	}
	generation, err := strconv.ParseUint(values[envRuntimeGeneration], 10, 32)
	if err != nil || generation != 1 {
		return runtimeConfig{}, fmt.Errorf("%s must equal 1", envRuntimeGeneration)
	}
	config := runtimeConfig{
		Identity: workercontrol.RuntimeIdentity{
			WorkerSessionID:   values[envWorkerSessionID],
			WorkerRunID:       values[envWorkerRunID],
			RuntimeInstanceID: values[envRuntimeInstance],
			RuntimeGeneration: uint32(generation),
		},
		WorkerToken:        values[envWorkerToken],
		BackendGRPCTarget:  values[envBackendGRPC],
		BackendHTTPBaseURL: values[envBackendHTTP],
	}
	if err := validateRuntimeConfig(config); err != nil {
		return runtimeConfig{}, err
	}
	return config, nil
}

func validateRuntimeConfig(config runtimeConfig) error {
	identities := map[string]string{
		envWorkerSessionID: config.Identity.WorkerSessionID,
		envWorkerRunID:     config.Identity.WorkerRunID,
		envRuntimeInstance: config.Identity.RuntimeInstanceID,
	}
	for key, value := range identities {
		if !validRuntimeValue(value) {
			return fmt.Errorf("%s is invalid", key)
		}
	}
	if config.Identity.RuntimeGeneration != 1 {
		return fmt.Errorf("%s must equal 1", envRuntimeGeneration)
	}
	if !validRuntimeToken(config.WorkerToken) {
		return fmt.Errorf("%s is invalid", envWorkerToken)
	}
	if err := validateGRPCTarget(config.BackendGRPCTarget); err != nil {
		return fmt.Errorf("%s is invalid: %w", envBackendGRPC, err)
	}
	if err := validateHTTPBaseURL(config.BackendHTTPBaseURL); err != nil {
		return fmt.Errorf("%s is invalid: %w", envBackendHTTP, err)
	}
	return nil
}

func validateGRPCTarget(target string) error {
	if target == "" || target != strings.TrimSpace(target) || strings.Contains(target, "://") {
		return fmt.Errorf("target must be one host:port authority")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return fmt.Errorf("target must be one host:port authority")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return fmt.Errorf("target port is invalid")
	}
	return nil
}

func validateHTTPBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("base URL must be absolute HTTP without userinfo, query, or fragment")
	}
	return nil
}

func validRuntimeValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "/\\\r\n\t ")
}

func validRuntimeToken(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, " \t\r\n\x00")
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	config, err := loadRuntimeConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithConfig(root, config, productionRunDependencies())
}

type startedRuntime struct {
	Agent         workercontrol.Agent
	AgentProfile  agentprofile.Profile
	Capabilities  acp.CapabilitySnapshot
	WorkingCopy   string
	AgentState    string
	TraceSpool    string
	Trace         *os.File
	Observations  <-chan modelobs.Observation
	RequestBinder *modelobs.RequestBinder
	Secrets       redact.Secrets
	Shutdown      func(context.Context) error
}

type runDependencies struct {
	StartRuntime func(context.Context, runtimeConfig) (*startedRuntime, error)
	DialControl  func(context.Context, string) (*grpc.ClientConn, error)
	HTTPClient   *http.Client
}

func productionRunDependencies() runDependencies {
	return runDependencies{
		StartRuntime: startProductionRuntime,
		DialControl: func(_ context.Context, target string) (*grpc.ClientConn, error) {
			return grpc.NewClient(
				target,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
		},
		HTTPClient: &http.Client{Timeout: evidenceHTTPTimeout},
	}
}

func runWithConfig(
	ctx context.Context,
	config runtimeConfig,
	dependencies runDependencies,
) (result error) {
	if err := validateRuntimeConfig(config); err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("Worker runtime context is required")
	}
	if dependencies.StartRuntime == nil || dependencies.DialControl == nil {
		return fmt.Errorf("Worker runtime dependencies are incomplete")
	}
	if dependencies.HTTPClient == nil {
		dependencies.HTTPClient = &http.Client{Timeout: evidenceHTTPTimeout}
	}

	started, err := dependencies.StartRuntime(ctx, config)
	if err != nil {
		return fmt.Errorf("start Worker runtime: %w", err)
	}
	if err := validateStartedRuntime(started); err != nil {
		return errors.Join(err, shutdownStartedRuntime(started))
	}
	defer func() {
		result = errors.Join(result, shutdownStartedRuntime(started))
	}()

	uploader, err := upload.NewClient(
		config.BackendHTTPBaseURL,
		config.WorkerToken,
		dependencies.HTTPClient,
	)
	if err != nil {
		return err
	}
	connection, err := dependencies.DialControl(ctx, config.BackendGRPCTarget)
	if err != nil {
		return fmt.Errorf("dial Backend WorkerControl: %w", err)
	}
	defer func() {
		result = errors.Join(result, connection.Close())
	}()

	client, err := workercontrol.NewClient(workercontrol.Options{
		RPC:           workercontrolv1.NewWorkerControlClient(connection),
		Identity:      config.Identity,
		WorkerToken:   config.WorkerToken,
		ShimVersion:   "switchback-worker-shim-m1",
		Agent:         started.Agent,
		AgentProfile:  started.AgentProfile,
		Capabilities:  started.Capabilities,
		WorkingCopy:   started.WorkingCopy,
		AgentState:    started.AgentState,
		TraceSpool:    started.TraceSpool,
		Trace:         started.Trace,
		Uploader:      uploader,
		Secrets:       started.Secrets,
		Observations:  started.Observations,
		RequestBinder: started.RequestBinder,
	})
	if err != nil {
		return fmt.Errorf("construct WorkerControl client: %w", err)
	}
	return client.Run(ctx)
}

func validateStartedRuntime(started *startedRuntime) error {
	if started == nil || started.Agent == nil || started.Trace == nil || started.Shutdown == nil {
		return fmt.Errorf("started Worker runtime is incomplete")
	}
	if _, err := agentprofile.Parse(string(started.AgentProfile)); err != nil {
		return fmt.Errorf("started Worker runtime agent profile: %w", err)
	}
	for label, path := range map[string]string{
		"WorkingCopy": started.WorkingCopy,
		"AgentState":  started.AgentState,
		"TraceSpool":  started.TraceSpool,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("started Worker runtime %s path is invalid", label)
		}
	}
	return nil
}

func shutdownStartedRuntime(started *startedRuntime) error {
	if started == nil || started.Shutdown == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return started.Shutdown(ctx)
}

func startProductionRuntime(
	ctx context.Context,
	config runtimeConfig,
) (*startedRuntime, error) {

	secretPaths := process.SecretPaths{
		Bootstrap: bootstrapPath,
		ModelKey:  modelKeyPath,
	}
	runtimeSecrets, err := process.LoadSecrets(secretPaths)
	if err != nil {
		return nil, err
	}
	agentProfile := runtimeSecrets.Bootstrap.AgentProfile
	proxyToken := runtimeSecrets.Bootstrap.ProxyToken
	evidenceSecrets := redact.Secrets{
		UpstreamModelKey:  runtimeSecrets.UpstreamKey,
		WorkerToken:       config.WorkerToken,
		LiteLLMProxyToken: runtimeSecrets.Bootstrap.ProxyToken,
		ModelSinkToken:    runtimeSecrets.Bootstrap.ModelSinkToken,
		LangfusePublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"),
		LangfuseSecretKey: os.Getenv("LANGFUSE_SECRET_KEY"),
	}

	observations := make(chan modelobs.Observation, 128)
	requestBinder := modelobs.NewRequestBinder()
	supervisor, err := process.NewSupervisor(process.SupervisorOptions{
		SecretPaths:      secretPaths,
		PreloadedSecrets: &runtimeSecrets,
		NewSink: func(token string) (process.ObservationSink, error) {
			return modelobs.NewServer(token, requestBinder, func(observation modelobs.Observation) error {
				select {
				case observations <- observation:
					return nil
				default:
					return fmt.Errorf("model observation buffer is full")
				}
			})
		},
		Processes: process.OSProcessFactory{},
		Readiness: process.HTTPReadinessProbe{},
		Diagnostics: process.DiagnosticSinkFunc(func(diagnostic process.Diagnostic) {
			encoded, marshalErr := json.Marshal(diagnostic)
			if marshalErr == nil {
				log.Printf("worker model proxy startup diagnostic: %s", encoded)
			}
		}),
		AgentProfile: agentProfile,
	})
	runtimeSecrets = process.RuntimeSecrets{}
	if err != nil {
		return nil, err
	}
	ready, err := supervisor.Start(ctx)
	if err != nil {
		return nil, err
	}
	if !ready.ACPReady {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return nil, errors.Join(
			fmt.Errorf("worker model proxy is not ready"),
			supervisor.Shutdown(shutdownContext),
		)
	}

	traceFile, err := os.OpenFile(acpTracePath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return nil, errors.Join(
			fmt.Errorf("open ACP trace: %w", err),
			supervisor.Shutdown(shutdownContext),
		)
	}
	adapter := acp.NewProcessAdapter()
	capabilities, err := adapter.Start(ctx, acp.StartConfig{
		AgentProfile: agentProfile,
		ProxyToken:   proxyToken,
		Trace:        traceFile,
	})
	if err != nil {
		_ = traceFile.Close()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return nil, errors.Join(err, supervisor.Shutdown(shutdownContext))
	}
	return &startedRuntime{
		Agent:         adapter,
		AgentProfile:  agentProfile,
		Capabilities:  capabilities,
		WorkingCopy:   workingCopyPath,
		AgentState:    agentStatePath,
		TraceSpool:    traceSpoolPath,
		Trace:         traceFile,
		Observations:  observations,
		RequestBinder: requestBinder,
		Secrets:       evidenceSecrets,
		Shutdown: func(shutdownContext context.Context) error {
			adapterErr := adapter.Close(shutdownContext)
			traceErr := traceFile.Close()
			supervisorErr := supervisor.Shutdown(shutdownContext)
			return errors.Join(adapterErr, traceErr, supervisorErr)
		},
	}, nil
}
