// Package config loads the local-only configuration for the runtime spike.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/distribution/reference"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
)

const (
	defaultListenAddr     = "127.0.0.1:8080"
	defaultGRPCListenAddr = "127.0.0.1:9090"
	defaultWorkerNetwork  = "none"
	dataRootEnv           = "SWITCHBACK_SPIKE_DATA_ROOT"
	hostDataRootEnv       = "SWITCHBACK_SPIKE_HOST_DATA_ROOT"
	listenAddrEnv         = "SWITCHBACK_SPIKE_LISTEN_ADDR"
	grpcListenAddrEnv     = "SWITCHBACK_SPIKE_GRPC_LISTEN_ADDR"
	fixturePathEnv        = "SWITCHBACK_SPIKE_FIXTURE_PATH"
	workerImageEnv        = "SWITCHBACK_SPIKE_WORKER_IMAGE"
	workerNetworkEnv      = "SWITCHBACK_SPIKE_WORKER_NETWORK"
	backendGRPCTargetEnv  = "SWITCHBACK_SPIKE_BACKEND_GRPC_TARGET"
	backendHTTPBaseURLEnv = "SWITCHBACK_SPIKE_BACKEND_HTTP_BASE_URL"
	modelKeyFileEnv       = "SWITCHBACK_SPIKE_MODEL_KEY_FILE"
	upstreamModelEnv      = "SWITCHBACK_SPIKE_UPSTREAM_MODEL"
	upstreamAPIBaseEnv    = "SWITCHBACK_SPIKE_UPSTREAM_API_BASE"
	agentProfileEnv       = "SWITCHBACK_SPIKE_AGENT_PROFILE"
	integrationEnv        = "SWITCHBACK_SPIKE_INTEGRATION"
	langfuseBaseURLEnv    = "LANGFUSE_BASE_URL"
	langfusePublicKeyEnv  = "LANGFUSE_PUBLIC_KEY"
	langfuseSecretKeyEnv  = "LANGFUSE_SECRET_KEY"
)

var workerNetworkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var safeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Config contains only server-owned local runtime settings.
type Config struct {
	ListenAddr         string
	GRPCListenAddr     string
	ContainerDataRoot  string
	HostDataRoot       string
	ObjectStoreRoot    string
	FixturePath        string
	WorkerImage        string
	WorkerNetwork      string
	BackendGRPCTarget  string
	BackendHTTPBaseURL string
	ModelKeyFile       string
	HostModelKeyFile   string
	UpstreamModel      string
	UpstreamAPIBase    string
	AgentProfile       agentprofile.Profile
	LangfuseBaseURL    string
	LangfusePublicKey  string
	LangfuseSecretKey  string
}

// Load accepts only server-owned deployment inputs. Client requests never
// select runtime paths, model routes, Worker endpoints, or observability keys.
func Load() (Config, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("get working directory: %w", err)
	}
	workingDirectory, err = filepath.Abs(workingDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("make working directory absolute: %w", err)
	}

	dataRoot := os.Getenv(dataRootEnv)
	if dataRoot == "" {
		dataRoot = filepath.Join(workingDirectory, ".spike-data")
	}
	hostDataRoot := os.Getenv(hostDataRootEnv)
	if hostDataRoot == "" {
		hostDataRoot = dataRoot
	}
	fixturePath := os.Getenv(fixturePathEnv)
	if fixturePath == "" {
		fixturePath = filepath.Join(workingDirectory, "fixtures", "go-repository")
	}
	listenAddr := os.Getenv(listenAddrEnv)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	grpcListenAddr := os.Getenv(grpcListenAddrEnv)
	if grpcListenAddr == "" {
		grpcListenAddr = defaultGRPCListenAddr
	}
	if err := validateAbsolutePath("data root", dataRoot); err != nil {
		return Config{}, err
	}
	if err := validateAbsolutePath("host data root", hostDataRoot); err != nil {
		return Config{}, err
	}
	if err := validateAbsolutePath("fixture path", fixturePath); err != nil {
		return Config{}, err
	}
	if err := validateBindAddress("listen address", listenAddr); err != nil {
		return Config{}, err
	}
	if err := validateBindAddress("gRPC listen address", grpcListenAddr); err != nil {
		return Config{}, err
	}
	workerImage := os.Getenv(workerImageEnv)
	if err := validateWorkerImage(workerImage); err != nil {
		return Config{}, err
	}
	workerNetwork := os.Getenv(workerNetworkEnv)
	if workerNetwork == "" {
		workerNetwork = defaultWorkerNetwork
	}
	if err := validateWorkerNetwork(workerNetwork); err != nil {
		return Config{}, err
	}
	backendGRPCTarget := os.Getenv(backendGRPCTargetEnv)
	if err := validateGRPCTarget(backendGRPCTarget); err != nil {
		return Config{}, fmt.Errorf("Backend gRPC target: %w", err)
	}
	backendHTTPBaseURL := os.Getenv(backendHTTPBaseURLEnv)
	if err := validateHTTPOrigin("Backend HTTP base URL", backendHTTPBaseURL); err != nil {
		return Config{}, err
	}
	modelKeyFile := os.Getenv(modelKeyFileEnv)
	if modelKeyFile == "" {
		modelKeyFile = filepath.Join(dataRoot, "env", "upstream-model.key")
	}
	if err := validateAbsolutePath("model key file", modelKeyFile); err != nil {
		return Config{}, err
	}
	modelKeyRelative, err := filepath.Rel(dataRoot, modelKeyFile)
	if err != nil || modelKeyRelative == "." || modelKeyRelative == ".." || strings.HasPrefix(modelKeyRelative, ".."+string(filepath.Separator)) {
		return Config{}, fmt.Errorf("model key file must be below data root")
	}
	hostModelKeyFile := filepath.Join(hostDataRoot, modelKeyRelative)
	if !isBelow(hostDataRoot, hostModelKeyFile) {
		return Config{}, fmt.Errorf("derived host model key file escapes host data root")
	}
	upstreamModel := os.Getenv(upstreamModelEnv)
	if upstreamModel == "" || strings.TrimSpace(upstreamModel) != upstreamModel {
		return Config{}, fmt.Errorf("upstream model is required")
	}
	upstreamAPIBase := os.Getenv(upstreamAPIBaseEnv)
	if err := validateUpstreamAPIBase(upstreamAPIBase); err != nil {
		return Config{}, err
	}
	agentProfile := agentprofile.Codex
	if value, explicit := os.LookupEnv(agentProfileEnv); explicit {
		agentProfile, err = agentprofile.Parse(value)
		if err != nil {
			return Config{}, err
		}
	}
	if agentProfile == agentprofile.IntegrationFake && os.Getenv(integrationEnv) != "1" {
		return Config{}, fmt.Errorf("integration-fake agent profile requires SWITCHBACK_SPIKE_INTEGRATION=1")
	}
	langfuseBaseURL := os.Getenv(langfuseBaseURLEnv)
	if err := validateHTTPOrigin("Langfuse base URL", langfuseBaseURL); err != nil {
		return Config{}, err
	}
	langfusePublicKey := os.Getenv(langfusePublicKeyEnv)
	langfuseSecretKey := os.Getenv(langfuseSecretKeyEnv)
	if !safeTokenPattern.MatchString(langfusePublicKey) || !safeTokenPattern.MatchString(langfuseSecretKey) {
		return Config{}, fmt.Errorf("Langfuse public and secret keys are required")
	}

	return Config{
		ListenAddr:         listenAddr,
		GRPCListenAddr:     grpcListenAddr,
		ContainerDataRoot:  dataRoot,
		HostDataRoot:       hostDataRoot,
		ObjectStoreRoot:    filepath.Join(dataRoot, "objects"),
		FixturePath:        fixturePath,
		WorkerImage:        workerImage,
		WorkerNetwork:      workerNetwork,
		BackendGRPCTarget:  backendGRPCTarget,
		BackendHTTPBaseURL: backendHTTPBaseURL,
		ModelKeyFile:       modelKeyFile,
		HostModelKeyFile:   hostModelKeyFile,
		UpstreamModel:      upstreamModel,
		UpstreamAPIBase:    upstreamAPIBase,
		AgentProfile:       agentProfile,
		LangfuseBaseURL:    langfuseBaseURL,
		LangfusePublicKey:  langfusePublicKey,
		LangfuseSecretKey:  langfuseSecretKey,
	}, nil
}

func validateAbsolutePath(name, value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a nonempty clean absolute path", name)
	}
	return nil
}

func validateWorkerImage(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("worker image must be a nonempty immutable reference without surrounding whitespace")
	}
	if sha256DigestPattern.MatchString(value) {
		return nil
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return fmt.Errorf("worker image must be a valid Docker image reference")
	}
	if _, tagged := named.(reference.NamedTagged); tagged {
		return fmt.Errorf("worker image must not retain a floating tag")
	}
	canonicalRef, isCanonical := named.(reference.Canonical)
	if !isCanonical || !sha256DigestPattern.MatchString(canonicalRef.Digest().String()) {
		return fmt.Errorf("worker image must be digest-pinned")
	}
	return nil
}

func validateWorkerNetwork(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !workerNetworkNamePattern.MatchString(value) {
		return fmt.Errorf("worker network must be a valid Docker network name")
	}
	if value == "host" || strings.HasPrefix(value, "container:") {
		return fmt.Errorf("worker network must not share a host or container namespace")
	}
	return nil
}

func validateBindAddress(name, value string) error {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
		return fmt.Errorf("%s host must be an IP-literal loopback or unspecified address", name)
	}
	return nil
}

func validateGRPCTarget(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, "://") {
		return fmt.Errorf("must be one host:port authority")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("must be one host:port authority")
	}
	return nil
}

func validateHTTPOrigin(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("%s must be an HTTP(S) origin", name)
	}
	return nil
}

func validateUpstreamAPIBase(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("upstream API base must be an HTTP(S) URL")
	}
	return nil
}

func isBelow(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
