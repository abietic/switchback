// Package process supervises trusted Worker runtime child processes.
package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

const (
	liteLLMUID = 10001
	agentUID   = 10002

	runtimePath      = "/usr/local/bin:/usr/bin:/bin"
	liteLLMBinary    = "/usr/local/bin/litellm"
	liteLLMConfig    = "/opt/switchback/worker/litellm/config.yaml"
	liteLLMPythonDir = "/opt/switchback/worker/litellm"
	liteLLMBaseURL   = "http://127.0.0.1:4000"
	codexACPBinary   = "/opt/switchback/node_modules/.bin/codex-acp"
	fakeACPBinary    = "/opt/switchback/bin/fake-acp"

	maxBootstrapBytes = 64 << 10
	maxModelKeyBytes  = 4 << 10

	readinessURL          = "http://127.0.0.1:4000/health/liveliness"
	readinessTimeout      = 30 * time.Second
	readinessPollInterval = 100 * time.Millisecond
	shutdownGracePeriod   = 5 * time.Second
	processGroupPoll      = 25 * time.Millisecond
	diagnosticOutputLimit = 32 << 10
)

// Bootstrap contains the server-owned model route and its internal tokens.
type Bootstrap struct {
	UpstreamModel   string               `json:"upstream_model"`
	UpstreamAPIBase string               `json:"upstream_api_base"`
	ProxyToken      string               `json:"proxy_token"`
	ModelSinkToken  string               `json:"model_sink_token"`
	AgentProfile    agentprofile.Profile `json:"agent_profile"`
}

func (b Bootstrap) String() string {
	return fmt.Sprintf(
		"Bootstrap{UpstreamModel:%q UpstreamAPIBase:%q AgentProfile:%q ProxyToken:[REDACTED] ModelSinkToken:[REDACTED]}",
		b.UpstreamModel,
		b.UpstreamAPIBase,
		b.AgentProfile,
	)
}

func (b Bootstrap) GoString() string {
	return b.String()
}

// SecretPaths are fixed container mount destinations supplied by trusted init.
type SecretPaths struct {
	Bootstrap string
	ModelKey  string
}

func (p SecretPaths) String() string {
	return "SecretPaths{Bootstrap:[REDACTED] ModelKey:[REDACTED]}"
}

func (p SecretPaths) GoString() string {
	return p.String()
}

// RuntimeSecrets is the in-memory result of reading the protected bootstrap
// inputs. Its formatting never exposes secret values.
type RuntimeSecrets struct {
	Bootstrap   Bootstrap
	UpstreamKey string
}

func (s RuntimeSecrets) String() string {
	return fmt.Sprintf("RuntimeSecrets{Bootstrap:%s UpstreamKey:[REDACTED]}", s.Bootstrap.String())
}

func (s RuntimeSecrets) GoString() string {
	return s.String()
}

// LoadSecrets is the production protected-file entry point. The trusted Shim
// must run as root and both final files must be root-owned.
func LoadSecrets(paths SecretPaths) (RuntimeSecrets, error) {
	return loadSecretsForIdentity(paths, 0, 0)
}

func loadSecretsForIdentity(
	paths SecretPaths,
	expectedEUID int,
	expectedOwner uint32,
) (RuntimeSecrets, error) {
	if os.Geteuid() != expectedEUID {
		return RuntimeSecrets{}, fmt.Errorf("protected file startup requires EUID %d", expectedEUID)
	}
	bootstrapBytes, err := readProtectedFile(
		"bootstrap",
		paths.Bootstrap,
		expectedOwner,
		maxBootstrapBytes,
	)
	if err != nil {
		return RuntimeSecrets{}, err
	}
	bootstrap, err := parseBootstrap(bootstrapBytes)
	if err != nil {
		return RuntimeSecrets{}, err
	}
	modelKeyBytes, err := readProtectedFile(
		"model key",
		paths.ModelKey,
		expectedOwner,
		maxModelKeyBytes,
	)
	if err != nil {
		return RuntimeSecrets{}, err
	}
	upstreamKey := strings.TrimSuffix(strings.TrimSuffix(string(modelKeyBytes), "\n"), "\r")
	if upstreamKey == "" || strings.TrimSpace(upstreamKey) != upstreamKey ||
		strings.ContainsAny(upstreamKey, "\r\n\x00") {
		return RuntimeSecrets{}, fmt.Errorf("model key file must contain one nonempty token")
	}
	return RuntimeSecrets{Bootstrap: bootstrap, UpstreamKey: upstreamKey}, nil
}

func readProtectedFile(label, path string, expectedOwner uint32, limit int64) ([]byte, error) {
	fd, err := openProtectedFile(path)
	if err != nil {
		return nil, fmt.Errorf("open protected %s path: %w", label, err)
	}
	defer unix.Close(fd)
	return readProtectedFD(label, fd, expectedOwner, limit)
}

func openProtectedFile(path string) (int, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, fmt.Errorf("protected file path must be a normalized absolute path")
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	dirFD, err := unix.Open(
		"/",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return -1, fmt.Errorf("open trusted root directory: %w", err)
	}
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			unix.Close(dirFD)
			return -1, fmt.Errorf("protected file path contains an invalid component")
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		nextFD, openErr := unix.Openat(dirFD, component, flags, 0)
		unix.Close(dirFD)
		if openErr != nil {
			return -1, fmt.Errorf("open protected path component: %w", openErr)
		}
		dirFD = nextFD
	}
	return dirFD, nil
}

func readProtectedFD(
	label string,
	fd int,
	expectedOwner uint32,
	limit int64,
) ([]byte, error) {
	if fd < 0 || limit <= 0 {
		return nil, fmt.Errorf("invalid protected %s file descriptor or limit", label)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect opened %s file: %w", label, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s file must be a regular file", label)
	}
	if stat.Uid != expectedOwner {
		return nil, fmt.Errorf("%s file must have the expected owner", label)
	}
	if stat.Mode&0o077 != 0 {
		return nil, fmt.Errorf("%s file must be owner-only", label)
	}
	if stat.Size < 0 || stat.Size > limit {
		return nil, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}

	content := make([]byte, 0, int(stat.Size))
	buffer := make([]byte, 4<<10)
	for int64(len(content)) <= limit {
		remaining := limit + 1 - int64(len(content))
		readBuffer := buffer
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		count, readErr := unix.Read(fd, readBuffer)
		if count > 0 {
			content = append(content, readBuffer[:count]...)
		}
		if readErr == nil && count == 0 {
			break
		}
		if errors.Is(readErr, unix.EINTR) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read %s file: %w", label, readErr)
		}
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}
	return content, nil
}

func parseBootstrap(content []byte) (Bootstrap, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var bootstrap Bootstrap
	if err := decoder.Decode(&bootstrap); err != nil {
		return Bootstrap{}, fmt.Errorf("decode bootstrap JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Bootstrap{}, fmt.Errorf("decode bootstrap JSON: exactly one object is required")
	}
	if bootstrap.UpstreamModel == "" || strings.TrimSpace(bootstrap.UpstreamModel) != bootstrap.UpstreamModel {
		return Bootstrap{}, fmt.Errorf("bootstrap upstream_model is required")
	}
	apiBase, err := url.Parse(bootstrap.UpstreamAPIBase)
	if err != nil ||
		(apiBase.Scheme != "http" && apiBase.Scheme != "https") ||
		apiBase.Host == "" ||
		apiBase.User != nil ||
		apiBase.RawQuery != "" ||
		apiBase.Fragment != "" {
		return Bootstrap{}, fmt.Errorf("bootstrap upstream_api_base must be an absolute HTTP URL")
	}
	if !validToken(bootstrap.ProxyToken) || !validToken(bootstrap.ModelSinkToken) {
		return Bootstrap{}, fmt.Errorf("bootstrap internal tokens are required")
	}
	if _, err := agentprofile.Parse(string(bootstrap.AgentProfile)); err != nil {
		return Bootstrap{}, fmt.Errorf("bootstrap agent_profile: %w", err)
	}
	return bootstrap, nil
}

func validToken(value string) bool {
	return value != "" &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

// ChildSpec is the complete, non-inheriting process configuration passed to a
// command adapter. Its formatting intentionally omits argv and environment.
type ChildSpec struct {
	Path string
	Args []string
	Env  []string
	UID  uint32
	GID  uint32
}

func (s ChildSpec) String() string {
	return fmt.Sprintf(
		"ChildSpec{Path:%q Args:[REDACTED] Env:[REDACTED] UID:%d GID:%d}",
		s.Path,
		s.UID,
		s.GID,
	)
}

func (s ChildSpec) GoString() string {
	return s.String()
}

// LiteLLMChildSpec constructs the only child environment that receives model
// credentials. It never reads or appends the ambient process environment.
func LiteLLMChildSpec(bootstrap Bootstrap, upstreamKey string) ChildSpec {
	return ChildSpec{
		Path: liteLLMBinary,
		Args: []string{
			"litellm",
			"--config", liteLLMConfig,
			"--host", "127.0.0.1",
			"--port", "4000",
		},
		Env: []string{
			"SWITCHBACK_SPIKE_MODEL_SINK_TOKEN=" + bootstrap.ModelSinkToken,
			"SWITCHBACK_SPIKE_PROXY_TOKEN=" + bootstrap.ProxyToken,
			"SWITCHBACK_SPIKE_UPSTREAM_API_BASE=" + bootstrap.UpstreamAPIBase,
			"SWITCHBACK_SPIKE_UPSTREAM_API_KEY=" + upstreamKey,
			"SWITCHBACK_SPIKE_UPSTREAM_MODEL=" + bootstrap.UpstreamModel,
			"HOME=/var/lib/switchback-litellm",
			"PATH=" + runtimePath,
			"PYTHONPATH=" + liteLLMPythonDir,
		},
		UID: liteLLMUID,
		GID: liteLLMUID,
	}
}

// AgentChildSpec constructs the Task 9 child baseline. Proxy authorization is
// deliberately absent because it will be sent as ACP gateway metadata.
func AgentChildSpec() ChildSpec {
	spec, _ := AgentChildSpecForProfile(agentprofile.Codex)
	return spec
}

// AgentChildSpecForProfile maps a validated profile to one immutable image path.
func AgentChildSpecForProfile(profile agentprofile.Profile) (ChildSpec, error) {
	var path string
	switch profile {
	case agentprofile.Codex:
		path = codexACPBinary
	case agentprofile.IntegrationFake:
		path = fakeACPBinary
	default:
		return ChildSpec{}, fmt.Errorf("agent child profile %q is unsupported", profile)
	}
	return ChildSpec{
		Path: path,
		Env: []string{
			"SWITCHBACK_SPIKE_MODEL_BASE_URL=" + liteLLMBaseURL,
			"HOME=/agent-state",
			"PATH=" + runtimePath,
		},
		UID: agentUID,
		GID: agentUID,
	}, nil
}

// ObservationSink is the lifecycle seam implemented by modelobs.Server.
type ObservationSink interface {
	Start() error
	Healthy() error
	Shutdown(context.Context) error
}

// ProcessLifecycleResult is the one retained result of exec.Cmd.Wait. Done is
// closed only after the leader is reaped and stdout/stderr copying is drained.
type ProcessLifecycleResult struct {
	LeaderErr error
}

// ManagedProcess exposes verified process-group state and one retained wait
// result. Leader completion alone does not establish group termination.
type ManagedProcess interface {
	Done() <-chan struct{}
	LifecycleResult() ProcessLifecycleResult
	ProcessGroupID() int
	GroupAlive() (bool, error)
	SignalGroup(syscall.Signal) error
}

// ProcessFactory starts a fully constructed child spec with diagnostic sinks.
type ProcessFactory interface {
	Start(ChildSpec, io.Writer, io.Writer) (ManagedProcess, error)
}

// ReadinessProbe checks the fixed LiteLLM liveliness endpoint.
type ReadinessProbe interface {
	Check(context.Context) error
}

// Clock is the deterministic timer seam used by readiness and shutdown.
type Clock interface {
	After(time.Duration) <-chan time.Time
}

// Diagnostic is a bounded, already-redacted startup failure record.
type Diagnostic struct {
	Code    string           `json:"code"`
	Phase   string           `json:"phase"`
	Message string           `json:"message"`
	Output  string           `json:"output,omitempty"`
	Cleanup []CleanupFailure `json:"cleanup,omitempty"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf(
		"Diagnostic{Code:%q Phase:%q Message:%q Output:%q Cleanup:%v}",
		d.Code,
		d.Phase,
		d.Message,
		d.Output,
		d.Cleanup,
	)
}

func (d Diagnostic) GoString() string {
	return d.String()
}

// DiagnosticSink receives one safe structured startup failure.
type DiagnosticSink interface {
	Record(Diagnostic)
}

// DiagnosticSinkFunc adapts a function to DiagnosticSink.
type DiagnosticSinkFunc func(Diagnostic)

func (f DiagnosticSinkFunc) Record(diagnostic Diagnostic) {
	f(diagnostic)
}

// SupervisorOptions are trusted init dependencies and protected file paths.
type SupervisorOptions struct {
	SecretPaths      SecretPaths
	PreloadedSecrets *RuntimeSecrets
	NewSink          func(string) (ObservationSink, error)
	Processes        ProcessFactory
	Readiness        ReadinessProbe
	Clock            Clock
	Diagnostics      DiagnosticSink
	AgentProfile     agentprofile.Profile
}

func (o SupervisorOptions) String() string {
	return fmt.Sprintf(
		"SupervisorOptions{SecretPaths:%s PreloadedSecrets:%t NewSink:%t Processes:%t Readiness:%t Clock:%t Diagnostics:%t}",
		o.SecretPaths.String(),
		o.PreloadedSecrets != nil,
		o.NewSink != nil,
		o.Processes != nil,
		o.Readiness != nil,
		o.Clock != nil,
		o.Diagnostics != nil,
	)
}

func (o SupervisorOptions) GoString() string {
	return o.String()
}

// ReadyState is returned only after LiteLLM liveliness succeeds.
type ReadyState struct {
	ACPReady  bool
	AgentSpec ChildSpec
}

func (r ReadyState) String() string {
	return fmt.Sprintf("ReadyState{ACPReady:%t AgentSpec:%s}", r.ACPReady, r.AgentSpec.String())
}

func (r ReadyState) GoString() string {
	return r.String()
}

// StartupError returns a structured, bounded and redacted failure record.
type StartupError struct {
	diagnostic Diagnostic
}

func (e *StartupError) Error() string {
	return e.diagnostic.Phase + " failed: " + e.diagnostic.Code
}

func (e StartupError) Format(state fmt.State, verb rune) {
	message := e.Error()
	if verb == 'q' {
		_, _ = fmt.Fprintf(state, "%q", message)
		return
	}
	_, _ = fmt.Fprint(state, message)
}

func (e *StartupError) Diagnostic() Diagnostic {
	return e.diagnostic
}

// CleanupFailure is an allowlisted cleanup diagnostic without raw OS text.
type CleanupFailure struct {
	Code   string `json:"code"`
	Target string `json:"target"`
}

// CleanupError retains every bounded shutdown failure.
type CleanupError struct {
	Failures []CleanupFailure
}

func (e *CleanupError) Error() string {
	codes := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		codes = append(codes, failure.Code)
	}
	return "worker model proxy cleanup failed: " + strings.Join(codes, ",")
}

// HasCode reports whether the structured result contains a failure code.
func (e *CleanupError) HasCode(code string) bool {
	if e == nil {
		return false
	}
	for _, failure := range e.Failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}

// Supervisor owns the strict trusted-init startup and shutdown order.
type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleStarting
	lifecycleReady
	lifecycleStopping
	lifecycleStopped
)

type Supervisor struct {
	options SupervisorOptions

	mu              sync.Mutex
	state           lifecycleState
	ready           bool
	sink            ObservationSink
	process         ManagedProcess
	stdout          *diagnosticBuffer
	stderr          *diagnosticBuffer
	loadSecrets     func(SecretPaths) (RuntimeSecrets, error)
	startupCancel   context.CancelFunc
	startupDone     chan struct{}
	startupComplete bool
	beforeSinkStart func()

	shutdownOnce sync.Once
	shutdownErr  error
}

// NewSupervisor validates the injected adapters without performing I/O.
func NewSupervisor(options SupervisorOptions) (*Supervisor, error) {
	if options.SecretPaths.Bootstrap == "" || options.SecretPaths.ModelKey == "" {
		return nil, fmt.Errorf("protected bootstrap and model key paths are required")
	}
	if options.NewSink == nil {
		return nil, fmt.Errorf("model observation sink factory is required")
	}
	if options.Processes == nil {
		return nil, fmt.Errorf("process factory is required")
	}
	if options.Readiness == nil {
		return nil, fmt.Errorf("readiness probe is required")
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.Diagnostics == nil {
		options.Diagnostics = DiagnosticSinkFunc(func(Diagnostic) {})
	}
	if options.AgentProfile == "" {
		options.AgentProfile = agentprofile.Codex
	}
	if _, err := agentprofile.Parse(string(options.AgentProfile)); err != nil {
		return nil, err
	}
	if options.PreloadedSecrets != nil {
		snapshot := cloneRuntimeSecrets(*options.PreloadedSecrets)
		options.PreloadedSecrets = &snapshot
	}
	return &Supervisor{
		options:     options,
		loadSecrets: LoadSecrets,
	}, nil
}

// Start reads secrets, starts the sink and LiteLLM, then blocks until the
// fixed liveliness endpoint succeeds or the 30-second deadline expires.
func (s *Supervisor) Start(ctx context.Context) (ReadyState, error) {
	startupCtx, err := s.beginStart(ctx)
	if err != nil {
		return ReadyState{}, err
	}

	secrets, err := s.takeSecrets()
	if err != nil {
		return s.failStart(
			"bootstrap_invalid",
			"bootstrap",
			"protected bootstrap could not be loaded",
		)
	}
	if s.isStopping() {
		return s.failStart(
			"startup_stopped",
			"lifecycle",
			"worker model proxy startup was stopped",
		)
	}
	s.stdout, s.stderr = diagnosticWriters(secrets)

	sink, err := s.options.NewSink(secrets.Bootstrap.ModelSinkToken)
	if err != nil {
		return s.failStart(
			"model_sink_start_failed",
			"model_sink",
			"model observation sink could not be created",
		)
	}
	s.mu.Lock()
	s.sink = sink
	beforeSinkStart := s.beforeSinkStart
	s.mu.Unlock()
	if beforeSinkStart != nil {
		beforeSinkStart()
	}
	if s.isStopping() {
		return s.failStart(
			"startup_stopped",
			"lifecycle",
			"worker model proxy startup was stopped",
		)
	}
	if err := sink.Start(); err != nil {
		return s.failStart(
			"model_sink_start_failed",
			"model_sink",
			"model observation sink could not start",
		)
	}
	if s.isStopping() {
		return s.failStart(
			"startup_stopped",
			"lifecycle",
			"worker model proxy startup was stopped",
		)
	}

	spec := LiteLLMChildSpec(secrets.Bootstrap, secrets.UpstreamKey)
	child, err := s.options.Processes.Start(spec, s.stdout, s.stderr)
	if child != nil {
		s.mu.Lock()
		s.process = child
		s.mu.Unlock()
	}
	if err != nil {
		return s.failStart(
			"litellm_start_failed",
			"litellm_start",
			"LiteLLM could not start",
		)
	}
	if child == nil {
		return s.failStart(
			"litellm_start_failed",
			"litellm_start",
			"LiteLLM could not start",
		)
	}
	if s.isStopping() {
		return s.failStart(
			"startup_stopped",
			"lifecycle",
			"worker model proxy startup was stopped",
		)
	}

	if err := s.waitUntilReady(startupCtx, child); err != nil {
		if s.isStopping() {
			return s.failStart(
				"startup_stopped",
				"lifecycle",
				"worker model proxy startup was stopped",
			)
		}
		code := "litellm_readiness_failed"
		if errors.Is(err, errReadinessTimeout) {
			code = "litellm_readiness_timeout"
		}
		return s.failStart(
			code,
			"readiness",
			"LiteLLM did not become ready",
		)
	}

	if err := s.publishReady(child, sink); err != nil {
		code := "litellm_readiness_failed"
		phase := "readiness"
		message := "LiteLLM did not remain ready"
		if s.isStopping() {
			code = "startup_stopped"
			phase = "lifecycle"
			message = "worker model proxy startup was stopped"
		}
		return s.failStart(code, phase, message)
	}
	s.finishStartup()
	agentSpec, err := AgentChildSpecForProfile(s.options.AgentProfile)
	if err != nil {
		return s.failStart(
			"agent_profile_invalid",
			"agent_profile",
			"Agent profile could not be resolved",
		)
	}
	return ReadyState{ACPReady: true, AgentSpec: agentSpec}, nil
}

func (s *Supervisor) takeSecrets() (RuntimeSecrets, error) {
	s.mu.Lock()
	if s.options.PreloadedSecrets != nil {
		secrets := *s.options.PreloadedSecrets
		*s.options.PreloadedSecrets = RuntimeSecrets{}
		s.options.PreloadedSecrets = nil
		s.mu.Unlock()
		return secrets, nil
	}
	s.mu.Unlock()
	return s.loadSecrets(s.options.SecretPaths)
}

func cloneRuntimeSecrets(secrets RuntimeSecrets) RuntimeSecrets {
	return RuntimeSecrets{
		Bootstrap: Bootstrap{
			UpstreamModel:   strings.Clone(secrets.Bootstrap.UpstreamModel),
			UpstreamAPIBase: strings.Clone(secrets.Bootstrap.UpstreamAPIBase),
			ProxyToken:      strings.Clone(secrets.Bootstrap.ProxyToken),
			ModelSinkToken:  strings.Clone(secrets.Bootstrap.ModelSinkToken),
			AgentProfile:    secrets.Bootstrap.AgentProfile,
		},
		UpstreamKey: strings.Clone(secrets.UpstreamKey),
	}
}

func (s *Supervisor) beginStart(ctx context.Context) (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != lifecycleNew {
		return nil, fmt.Errorf("worker model proxy supervisor already started")
	}
	s.state = lifecycleStarting
	s.startupDone = make(chan struct{})
	startupCtx, cancel := context.WithCancel(ctx)
	s.startupCancel = cancel
	return startupCtx, nil
}

func (s *Supervisor) finishStartup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupComplete {
		return
	}
	s.startupComplete = true
	if s.startupCancel != nil {
		s.startupCancel()
	}
	close(s.startupDone)
}

func (s *Supervisor) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == lifecycleStopping || s.state == lifecycleStopped
}

func (s *Supervisor) failStart(code, phase, message string) (ReadyState, error) {
	s.finishStartup()
	cleanupErr := s.cleanup(context.Background())
	return ReadyState{}, s.recordFailure(code, phase, message, cleanupErr)
}

var errReadinessTimeout = errors.New("LiteLLM readiness timeout")

func (s *Supervisor) waitUntilReady(ctx context.Context, child ManagedProcess) error {
	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()
	timeout := s.options.Clock.After(readinessTimeout)
	for {
		result := make(chan error, 1)
		go func() {
			result <- s.options.Readiness.Check(probeCtx)
		}()
		select {
		case err := <-result:
			if err == nil {
				return nil
			}
		case <-timeout:
			cancelProbe()
			return errReadinessTimeout
		case <-child.Done():
			return fmt.Errorf("LiteLLM exited before readiness")
		case <-ctx.Done():
			cancelProbe()
			return ctx.Err()
		}

		select {
		case <-s.options.Clock.After(readinessPollInterval):
		case <-timeout:
			cancelProbe()
			return errReadinessTimeout
		case <-child.Done():
			return fmt.Errorf("LiteLLM exited before readiness")
		case <-ctx.Done():
			cancelProbe()
			return ctx.Err()
		}
	}
}

func (s *Supervisor) publishReady(child ManagedProcess, sink ObservationSink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != lifecycleStarting {
		return fmt.Errorf("worker model proxy is stopping")
	}
	if s.process != child || s.sink != sink {
		return fmt.Errorf("LiteLLM lifecycle changed before readiness publication")
	}
	select {
	case <-child.Done():
		return fmt.Errorf("LiteLLM exited before readiness publication")
	default:
	}
	if child.ProcessGroupID() <= 0 {
		return fmt.Errorf("LiteLLM process group is unverified")
	}
	alive, err := child.GroupAlive()
	if err != nil || !alive {
		return fmt.Errorf("LiteLLM process group is not healthy")
	}
	if err := sink.Healthy(); err != nil {
		return fmt.Errorf("model observation sink is not healthy")
	}
	s.ready = true
	s.state = lifecycleReady
	return nil
}

// Shutdown terminates the verified LiteLLM process group before closing the
// sink. Every caller observes the same one-shot terminal cleanup result.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	return s.cleanup(ctx)
}

func (s *Supervisor) cleanup(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case lifecycleNew, lifecycleStarting, lifecycleReady:
		s.state = lifecycleStopping
	}
	if s.startupCancel != nil {
		s.startupCancel()
	}
	startupDone := s.startupDone
	s.mu.Unlock()

	s.shutdownOnce.Do(func() {
		if startupDone != nil {
			<-startupDone
		}
		s.shutdownErr = s.performCleanup(ctx)
		s.mu.Lock()
		s.state = lifecycleStopped
		s.mu.Unlock()
	})
	return s.shutdownErr
}

func (s *Supervisor) performCleanup(ctx context.Context) error {
	s.mu.Lock()
	child := s.process
	sink := s.sink
	s.ready = false
	s.mu.Unlock()

	failures := make([]CleanupFailure, 0, 2)
	if child != nil {
		if failure := s.stopProcess(ctx, child); failure != nil {
			failures = append(failures, *failure)
		}
	}
	if sink != nil {
		sinkContext, cancel := context.WithTimeout(
			context.Background(),
			shutdownGracePeriod,
		)
		if err := sink.Shutdown(sinkContext); err != nil {
			failures = append(failures, CleanupFailure{
				Code:   "model_sink_shutdown_failed",
				Target: "model_sink",
			})
		}
		cancel()
	}
	if len(failures) == 0 {
		return nil
	}
	return &CleanupError{Failures: failures}
}

func (s *Supervisor) stopProcess(
	ctx context.Context,
	child ManagedProcess,
) *CleanupFailure {
	if child.ProcessGroupID() <= 0 {
		return &CleanupFailure{
			Code:   "process_group_unverified",
			Target: "litellm_process_group",
		}
	}
	alive, err := child.GroupAlive()
	if err != nil {
		return &CleanupFailure{
			Code:   "process_group_status_failed",
			Target: "litellm_process_group",
		}
	}
	if alive {
		if err := child.SignalGroup(syscall.SIGTERM); err != nil {
			return &CleanupFailure{
				Code:   "process_group_term_failed",
				Target: "litellm_process_group",
			}
		}
	}

	terminated, failure := s.waitForProcessTermination(ctx, child, shutdownGracePeriod)
	if failure != nil || terminated {
		return failure
	}
	if err := child.SignalGroup(syscall.SIGKILL); err != nil {
		return &CleanupFailure{
			Code:   "process_group_kill_failed",
			Target: "litellm_process_group",
		}
	}
	terminated, failure = s.waitForProcessTermination(
		context.Background(),
		child,
		shutdownGracePeriod,
	)
	if failure != nil {
		return failure
	}
	if !terminated {
		return &CleanupFailure{
			Code:   "process_group_not_terminated",
			Target: "litellm_process_group",
		}
	}
	return nil
}

func (s *Supervisor) waitForProcessTermination(
	ctx context.Context,
	child ManagedProcess,
	timeout time.Duration,
) (bool, *CleanupFailure) {
	deadline := s.options.Clock.After(timeout)
	leaderDoneChannel := child.Done()
	leaderDone := false
	for {
		if !leaderDone {
			select {
			case <-leaderDoneChannel:
				leaderDone = true
				leaderDoneChannel = nil
			default:
			}
		}
		alive, err := child.GroupAlive()
		if err != nil {
			return false, &CleanupFailure{
				Code:   "process_group_status_failed",
				Target: "litellm_process_group",
			}
		}
		if leaderDone && !alive {
			return true, nil
		}

		select {
		case <-leaderDoneChannel:
			leaderDone = true
			leaderDoneChannel = nil
		case <-s.options.Clock.After(processGroupPoll):
		case <-deadline:
			return false, nil
		case <-ctx.Done():
			return false, nil
		}
	}
}

func (s *Supervisor) recordFailure(
	code,
	phase,
	message string,
	cleanupErr error,
) error {
	finalizeOutput := false
	s.mu.Lock()
	child := s.process
	s.mu.Unlock()
	if child != nil {
		select {
		case <-child.Done():
			finalizeOutput = true
		default:
		}
	}
	output := ""
	if s.stdout != nil {
		output = s.stdout.Snapshot()
		if finalizeOutput {
			output = s.stdout.Finalize()
		}
	}
	if s.stderr != nil {
		stderr := s.stderr.Snapshot()
		if finalizeOutput {
			stderr = s.stderr.Finalize()
		}
		if output != "" && stderr != "" {
			output += "\n"
		}
		output += stderr
	}
	if len(output) > diagnosticOutputLimit {
		output = output[len(output)-diagnosticOutputLimit:]
	}
	diagnostic := Diagnostic{
		Code:    code,
		Phase:   phase,
		Message: message,
		Output:  output,
	}
	var cleanup *CleanupError
	if errors.As(cleanupErr, &cleanup) {
		diagnostic.Cleanup = append(
			[]CleanupFailure(nil),
			cleanup.Failures...,
		)
	}
	s.options.Diagnostics.Record(diagnostic)
	return &StartupError{diagnostic: diagnostic}
}

type realClock struct{}

func (realClock) After(duration time.Duration) <-chan time.Time {
	return time.After(duration)
}

// HTTPReadinessProbe checks the fixed loopback endpoint with a bounded client.
type HTTPReadinessProbe struct {
	Client *http.Client
}

func (p HTTPReadinessProbe) Check(ctx context.Context) error {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, readinessURL, nil)
	if err != nil {
		return fmt.Errorf("build LiteLLM readiness request")
	}
	response, err := clientCopy.Do(request)
	if err != nil {
		return fmt.Errorf("check LiteLLM readiness")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("check LiteLLM readiness")
	}
	return nil
}

// OSProcessFactory is the production Unix command adapter.
type OSProcessFactory struct{}

func (OSProcessFactory) Start(spec ChildSpec, stdout, stderr io.Writer) (ManagedProcess, error) {
	command, err := buildCommand(spec, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return startManagedCommand(command, unix.Getpgid)
}

func startManagedCommand(
	command *exec.Cmd,
	getpgid func(int) (int, error),
) (ManagedProcess, error) {
	return startManagedCommandWithWait(command, getpgid, nil)
}

func startManagedCommandWithWait(
	command *exec.Cmd,
	getpgid func(int) (int, error),
	wait func() error,
) (ManagedProcess, error) {
	if command == nil {
		return nil, fmt.Errorf("child command is required")
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start child process")
	}
	if wait == nil {
		wait = command.Wait
	}
	leaderPID := command.Process.Pid
	pgid, err := captureVerifiedProcessGroup(leaderPID, getpgid)
	if err != nil {
		terminateUnverifiedStartedCommand(command, leaderPID, wait)
		return nil, fmt.Errorf("verify child process group")
	}
	process := &osManagedProcess{
		pid:  leaderPID,
		pgid: pgid,
		done: make(chan struct{}),
	}
	go func() {
		waitErr := wait()
		process.mu.Lock()
		process.result = ProcessLifecycleResult{LeaderErr: waitErr}
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func terminateUnverifiedStartedCommand(command *exec.Cmd, leaderPID int, wait func() error) {
	if leaderPID > 0 && command.SysProcAttr != nil && command.SysProcAttr.Setpgid {
		_ = unix.Kill(-leaderPID, syscall.SIGKILL)
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = wait()
}

func captureVerifiedProcessGroup(
	leaderPID int,
	lookup func(int) (int, error),
) (int, error) {
	if leaderPID <= 0 || lookup == nil {
		return 0, fmt.Errorf("child leader PID is invalid")
	}
	pgid, err := lookup(leaderPID)
	if err != nil || pgid <= 0 || pgid != leaderPID {
		return 0, fmt.Errorf("child process group is unverified")
	}
	return pgid, nil
}

func buildCommand(spec ChildSpec, stdout, stderr io.Writer) (*exec.Cmd, error) {
	if spec.Path == "" || len(spec.Args) == 0 || spec.Args[0] == "" {
		return nil, fmt.Errorf("child path and argv are required")
	}
	command := exec.Command(spec.Path)
	command.Args = append([]string(nil), spec.Args...)
	command.Env = append([]string(nil), spec.Env...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Credential: &syscall.Credential{
			Uid:    spec.UID,
			Gid:    spec.GID,
			Groups: []uint32{},
		},
	}
	return command, nil
}

type osManagedProcess struct {
	mu     sync.Mutex
	pid    int
	pgid   int
	done   chan struct{}
	result ProcessLifecycleResult
}

func (p *osManagedProcess) Done() <-chan struct{} {
	return p.done
}

func (p *osManagedProcess) LifecycleResult() ProcessLifecycleResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

func (p *osManagedProcess) ProcessGroupID() int {
	return p.pgid
}

func (p *osManagedProcess) GroupAlive() (bool, error) {
	if p.pgid <= 0 {
		return false, fmt.Errorf("process group is unverified")
	}
	err := unix.Kill(-p.pgid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("inspect process group")
	}
}

func (p *osManagedProcess) SignalGroup(signal syscall.Signal) error {
	if p.pgid <= 0 {
		return fmt.Errorf("process group is unverified")
	}
	err := unix.Kill(-p.pgid, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

type diagnosticBuffer struct {
	mu        sync.Mutex
	redactor  redact.Redactor
	tokens    [][]byte
	pending   []byte
	output    []byte
	finalized bool
}

func diagnosticWriters(secrets RuntimeSecrets) (*diagnosticBuffer, *diagnosticBuffer) {
	workerToken := os.Getenv("SWITCHBACK_WORKER_TOKEN")
	langfusePublic := os.Getenv("LANGFUSE_PUBLIC_KEY")
	langfuseSecret := os.Getenv("LANGFUSE_SECRET_KEY")
	known := redact.Secrets{
		UpstreamModelKey:  secrets.UpstreamKey,
		WorkerToken:       workerToken,
		LiteLLMProxyToken: secrets.Bootstrap.ProxyToken,
		ModelSinkToken:    secrets.Bootstrap.ModelSinkToken,
		LangfusePublicKey: langfusePublic,
		LangfuseSecretKey: langfuseSecret,
	}
	values := []string{
		known.UpstreamModelKey,
		known.WorkerToken,
		known.LiteLLMProxyToken,
		known.ModelSinkToken,
		known.LangfusePublicKey,
		known.LangfuseSecretKey,
	}
	return newDiagnosticBuffer(known, values), newDiagnosticBuffer(known, values)
}

func newDiagnosticBuffer(known redact.Secrets, values []string) *diagnosticBuffer {
	tokens := make([][]byte, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		tokens = append(tokens, []byte(value))
	}
	sort.Slice(tokens, func(i, j int) bool {
		return len(tokens[i]) > len(tokens[j])
	})
	return &diagnosticBuffer{
		redactor: redact.New(known),
		tokens:   tokens,
	}
}

func (b *diagnosticBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return 0, fmt.Errorf("diagnostic output is finalized")
	}
	data := append(append([]byte(nil), b.pending...), content...)
	pendingBytes := b.pendingSuffixLength(data)
	commitEnd := len(data) - pendingBytes
	b.appendBounded([]byte(b.redactor.Redact(string(data[:commitEnd]))))
	b.pending = append(b.pending[:0], data[commitEnd:]...)
	return len(content), nil
}

const possibleSecretPrefixMarker = "[REDACTED:possible_secret_prefix]"

func (b *diagnosticBuffer) Snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := append([]byte(nil), b.output...)
	if !b.finalized && len(b.pending) > 0 {
		snapshot = appendBoundedCopy(
			snapshot,
			[]byte(possibleSecretPrefixMarker),
		)
	}
	return string(snapshot)
}

func (b *diagnosticBuffer) Finalize() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return string(append([]byte(nil), b.output...))
	}
	if len(b.pending) > 0 {
		pending := string(b.pending)
		redacted := b.redactor.Redact(pending)
		if redacted == pending {
			redacted = possibleSecretPrefixMarker
		}
		b.appendBounded([]byte(redacted))
		b.pending = nil
	}
	b.finalized = true
	return string(append([]byte(nil), b.output...))
}

func (b *diagnosticBuffer) pendingSuffixLength(data []byte) int {
	longest := 0
	for _, token := range b.tokens {
		maximum := len(token) - 1
		if maximum > len(data) {
			maximum = len(data)
		}
		for length := maximum; length > longest; length-- {
			if bytes.Equal(data[len(data)-length:], token[:length]) {
				longest = length
				break
			}
		}
	}
	return longest
}

func (b *diagnosticBuffer) appendBounded(content []byte) {
	b.output = appendBoundedCopy(b.output, content)
}

func appendBoundedCopy(output, content []byte) []byte {
	if len(content) >= diagnosticOutputLimit {
		return append(output[:0], content[len(content)-diagnosticOutputLimit:]...)
	}
	overflow := len(output) + len(content) - diagnosticOutputLimit
	if overflow > 0 {
		copy(output, output[overflow:])
		output = output[:len(output)-overflow]
	}
	return append(output, content...)
}
