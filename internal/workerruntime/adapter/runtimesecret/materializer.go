// Package runtimesecret materializes per-Runtime bootstrap and model inputs.
package runtimesecret

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	LiteLLMUID = 10001
	AgentUID   = 10002

	workerSecretRoot      = "/run/switchback-secrets"
	workerBootstrapPath   = workerSecretRoot + "/bootstrap.json"
	workerModelSecretPath = workerSecretRoot + "/model"

	backendControlRootName       = "control-plane"
	backendControlCredentialName = "live-credential.json"
)

var ErrLiveRuntimeCredentialUnavailable = errors.New("live Runtime credential is unavailable")

type ProviderCredential []byte

func (ProviderCredential) String() string   { return "[REDACTED]" }
func (ProviderCredential) GoString() string { return "[REDACTED]" }

type Request struct {
	WorkerSessionID     string
	WorkerRunID         string
	RuntimeInstanceID   string
	Generation          uint64
	ChannelEpoch        uint64
	WorkerControlToken  string
	AgentSessionID      string
	LoadExistingSession bool
	SessionStorage      application.SessionStorage
	AgentProfile        application.ProfileRef
	ProviderCredential  ProviderCredential
}

type Bootstrap struct {
	SchemaVersion        string `json:"schema_version"`
	RuntimeInstanceID    string `json:"runtime_instance_id"`
	RuntimeGeneration    uint64 `json:"runtime_generation"`
	WorkerSessionID      string `json:"worker_session_id"`
	WorkerRunID          string `json:"worker_run_id"`
	ChannelEpoch         uint64 `json:"channel_epoch"`
	WorkerControlToken   string `json:"worker_control_token"`
	AgentProfileID       string `json:"agent_profile_id"`
	AgentProfileRevision uint64 `json:"agent_profile_revision"`
	AgentProfileDigest   string `json:"agent_profile_digest"`
	AgentSessionID       string `json:"agent_session_id,omitempty"`
	LoadExistingSession  bool   `json:"load_existing_session,omitempty"`
	LiteLLMModel         string `json:"litellm_model"`
	UpstreamAPIBase      string `json:"upstream_api_base"`
	UpstreamProtocol     string `json:"upstream_protocol"`
	ProxyToken           string `json:"proxy_token"`
	ModelSinkToken       string `json:"model_sink_token"`
	LiteLLMUID           int    `json:"litellm_uid"`
	AgentUID             int    `json:"agent_uid"`
	ModelSecretPath      string `json:"model_secret_path"`
}

type liveCredential struct {
	SchemaVersion        string `json:"schema_version"`
	RuntimeInstanceID    string `json:"runtime_instance_id"`
	RuntimeGeneration    uint64 `json:"runtime_generation"`
	WorkerSessionID      string `json:"worker_session_id"`
	WorkerRunID          string `json:"worker_run_id"`
	ChannelEpoch         uint64 `json:"channel_epoch"`
	WorkerControlToken   string `json:"worker_control_token"`
	AgentProfileID       string `json:"agent_profile_id"`
	AgentProfileRevision uint64 `json:"agent_profile_revision"`
	AgentProfileDigest   string `json:"agent_profile_digest"`
	AgentSessionID       string `json:"agent_session_id,omitempty"`
	LoadExistingSession  bool   `json:"load_existing_session,omitempty"`
}

type ExistingRequest struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	Generation        uint64
	ChannelEpoch      uint64
	AgentSessionID    string
	SessionStorage    application.SessionStorage
	AgentProfile      application.ProfileRef
}

// LiveExistingRequest identifies a Runtime container that survived a Backend
// restart. The original generation-one bootstrap legitimately has no
// AgentSessionID because ACP establishes it after the container starts.
type LiveExistingRequest struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	Generation        uint64
	ChannelEpoch      uint64
	AgentSessionID    string
	SessionStorage    application.SessionStorage
	AgentProfile      application.ProfileRef
}

func (bootstrap Bootstrap) String() string {
	return fmt.Sprintf(
		"Bootstrap{RuntimeInstanceID:%q RuntimeGeneration:%d WorkerSessionID:%q WorkerRunID:%q ChannelEpoch:%d AgentProfileID:%q ProxyToken:[REDACTED] ModelSinkToken:[REDACTED] WorkerControlToken:[REDACTED]}",
		bootstrap.RuntimeInstanceID,
		bootstrap.RuntimeGeneration,
		bootstrap.WorkerSessionID,
		bootstrap.WorkerRunID,
		bootstrap.ChannelEpoch,
		bootstrap.AgentProfileID,
	)
}

func (bootstrap Bootstrap) GoString() string { return bootstrap.String() }

type Ownership interface {
	Chown(path string, uid, gid int) error
}

type OwnershipFunc func(path string, uid, gid int) error

func (f OwnershipFunc) Chown(path string, uid, gid int) error { return f(path, uid, gid) }

type Materializer struct {
	Ownership        Ownership
	OwnerUID         func(path string) (uint32, error)
	OpenFileOwnerUID func(file *os.File) (uint32, error)
	NewToken         func() (string, error)
}

func (m Materializer) Materialize(request Request) (resources application.RuntimeSecretResources, resultErr error) {
	if m.Ownership == nil || m.OwnerUID == nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("Runtime secret ownership operations are required")
	}
	if m.NewToken == nil {
		m.NewToken = randomToken
	}
	if err := validateRequest(request); err != nil {
		return application.RuntimeSecretResources{}, err
	}
	profile, err := config.ProductionProfileRegistry().ResolveAgent(config.AgentProfileRef{
		ID: request.AgentProfile.ID, Revision: request.AgentProfile.Revision, Digest: request.AgentProfile.Digest,
	})
	if err != nil {
		return application.RuntimeSecretResources{}, err
	}
	proxyToken, err := m.NewToken()
	if err != nil || !validToken(proxyToken) {
		return application.RuntimeSecretResources{}, fmt.Errorf("generate Runtime proxy token")
	}
	modelSinkToken, err := m.NewToken()
	if err != nil || !validToken(modelSinkToken) {
		return application.RuntimeSecretResources{}, fmt.Errorf("generate Runtime model sink token")
	}

	runtimeLeaf := request.RuntimeInstanceID + "-g" + strconv.FormatUint(request.Generation, 10)
	backendRuntimeRoot := filepath.Join(request.SessionStorage.BackendRoot, "runtimes", runtimeLeaf)
	engineRuntimeRoot := filepath.Join(request.SessionStorage.EngineRoot, "runtimes", runtimeLeaf)
	backendSecretRoot := filepath.Join(backendRuntimeRoot, "secret-input")
	engineSecretRoot := filepath.Join(engineRuntimeRoot, "secret-input")
	backendControlRoot := filepath.Join(backendRuntimeRoot, backendControlRootName)
	if _, err := os.Lstat(backendRuntimeRoot); err == nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("Runtime secret resources already exist")
	} else if !os.IsNotExist(err) {
		return application.RuntimeSecretResources{}, fmt.Errorf("inspect Runtime secret resources: %w", err)
	}
	runtimesRoot := filepath.Dir(backendRuntimeRoot)
	if info, err := os.Lstat(runtimesRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return application.RuntimeSecretResources{}, fmt.Errorf("Runtime resources root must be a real directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(runtimesRoot, 0o700); err != nil {
			return application.RuntimeSecretResources{}, fmt.Errorf("create Runtime resources root: %w", err)
		}
	} else {
		return application.RuntimeSecretResources{}, fmt.Errorf("inspect Runtime resources root: %w", err)
	}
	if err := os.Mkdir(backendRuntimeRoot, 0o700); err != nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("create Runtime resource tree: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if cleanupErr := os.RemoveAll(backendRuntimeRoot); cleanupErr != nil {
			resultErr = fmt.Errorf("%w; remove partial Runtime secret resources: %v", resultErr, cleanupErr)
		}
	}()
	if err := os.Mkdir(backendSecretRoot, 0o700); err != nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("create Runtime secret input root: %w", err)
	}
	if err := os.Mkdir(backendControlRoot, 0o700); err != nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("create Runtime control credential root: %w", err)
	}

	resources = application.RuntimeSecretResources{
		BackendRoot: backendSecretRoot,
		EngineRoot:  engineSecretRoot,
		Bootstrap: application.StorageMount{
			BackendPath:  filepath.Join(backendSecretRoot, "bootstrap.json"),
			EngineSource: filepath.Join(engineSecretRoot, "bootstrap.json"),
			WorkerPath:   workerBootstrapPath,
		},
		ModelCredential: application.StorageMount{
			BackendPath:  filepath.Join(backendSecretRoot, "model"),
			EngineSource: filepath.Join(engineSecretRoot, "model"),
			WorkerPath:   workerModelSecretPath,
		},
	}
	bootstrap := Bootstrap{
		SchemaVersion:        "switchback.runtime-bootstrap.v1",
		RuntimeInstanceID:    request.RuntimeInstanceID,
		RuntimeGeneration:    request.Generation,
		WorkerSessionID:      request.WorkerSessionID,
		WorkerRunID:          request.WorkerRunID,
		ChannelEpoch:         request.ChannelEpoch,
		WorkerControlToken:   request.WorkerControlToken,
		AgentSessionID:       request.AgentSessionID,
		LoadExistingSession:  request.LoadExistingSession,
		AgentProfileID:       profile.ID,
		AgentProfileRevision: profile.Revision,
		AgentProfileDigest:   profile.Digest,
		LiteLLMModel:         profile.LiteLLMModel,
		UpstreamAPIBase:      profile.APIBase,
		UpstreamProtocol:     profile.UpstreamProtocol,
		ProxyToken:           proxyToken,
		ModelSinkToken:       modelSinkToken,
		LiteLLMUID:           LiteLLMUID,
		AgentUID:             AgentUID,
		ModelSecretPath:      workerModelSecretPath,
	}
	bootstrapBytes, err := json.Marshal(bootstrap)
	if err != nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("encode Runtime bootstrap: %w", err)
	}
	liveCredentialBytes, err := json.Marshal(liveCredential{
		SchemaVersion:        "switchback.runtime-live-credential.v1",
		RuntimeInstanceID:    bootstrap.RuntimeInstanceID,
		RuntimeGeneration:    bootstrap.RuntimeGeneration,
		WorkerSessionID:      bootstrap.WorkerSessionID,
		WorkerRunID:          bootstrap.WorkerRunID,
		ChannelEpoch:         bootstrap.ChannelEpoch,
		WorkerControlToken:   bootstrap.WorkerControlToken,
		AgentProfileID:       bootstrap.AgentProfileID,
		AgentProfileRevision: bootstrap.AgentProfileRevision,
		AgentProfileDigest:   bootstrap.AgentProfileDigest,
		AgentSessionID:       bootstrap.AgentSessionID,
		LoadExistingSession:  bootstrap.LoadExistingSession,
	})
	if err != nil {
		return application.RuntimeSecretResources{}, fmt.Errorf("encode Runtime live credential: %w", err)
	}
	if err := m.writeRootOnly(resources.ModelCredential.BackendPath, request.ProviderCredential); err != nil {
		return application.RuntimeSecretResources{}, err
	}
	if err := m.writeRootOnly(resources.Bootstrap.BackendPath, append(bootstrapBytes, '\n')); err != nil {
		return application.RuntimeSecretResources{}, err
	}
	if err := m.writeRootOnly(
		filepath.Join(backendControlRoot, backendControlCredentialName),
		append(liveCredentialBytes, '\n'),
	); err != nil {
		return application.RuntimeSecretResources{}, err
	}
	if err := m.sealObservationRegistry(request, bootstrap); err != nil {
		slog.Warn("Runtime observation registry unavailable", "code", "observation_registry_unavailable")
	}
	return resources, nil
}

func (m Materializer) OpenExisting(
	request ExistingRequest,
) (application.RuntimeSecretResources, Bootstrap, error) {
	if request.WorkerSessionID == "" || request.WorkerRunID == "" ||
		request.RuntimeInstanceID == "" || request.Generation == 0 ||
		request.ChannelEpoch == 0 || request.AgentSessionID == "" {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("existing Runtime secret identity is incomplete")
	}
	resources, bootstrap, err := m.readExisting(
		request.RuntimeInstanceID,
		request.Generation,
		request.SessionStorage,
	)
	if err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{}, err
	}
	if bootstrap.SchemaVersion != "switchback.runtime-bootstrap.v1" ||
		bootstrap.WorkerSessionID != request.WorkerSessionID ||
		bootstrap.WorkerRunID != request.WorkerRunID ||
		bootstrap.RuntimeInstanceID != request.RuntimeInstanceID ||
		bootstrap.RuntimeGeneration != request.Generation ||
		bootstrap.ChannelEpoch != request.ChannelEpoch ||
		bootstrap.AgentSessionID != request.AgentSessionID ||
		!bootstrap.LoadExistingSession ||
		bootstrap.AgentProfileID != request.AgentProfile.ID ||
		bootstrap.AgentProfileRevision != request.AgentProfile.Revision ||
		bootstrap.AgentProfileDigest != request.AgentProfile.Digest ||
		!validToken(bootstrap.WorkerControlToken) {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("existing Runtime bootstrap identity is stale")
	}
	return resources, bootstrap, nil
}

// OpenLiveExisting reads the Backend-only credential escrow for a Runtime that
// survived a Backend restart. The transient Docker inputs are intentionally
// absent after provisioning; this method never recreates them or rotates a
// token.
func (m Materializer) OpenLiveExisting(
	request LiveExistingRequest,
) (application.RuntimeSecretResources, Bootstrap, error) {
	if request.WorkerSessionID == "" || request.WorkerRunID == "" ||
		request.RuntimeInstanceID == "" || request.Generation == 0 ||
		request.ChannelEpoch == 0 || request.AgentSessionID == "" {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("live Runtime secret identity is incomplete")
	}
	resources, bootstrap, err := m.readLiveExisting(
		request.RuntimeInstanceID,
		request.Generation,
		request.SessionStorage,
	)
	if err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{}, err
	}
	if bootstrap.SchemaVersion != "switchback.runtime-bootstrap.v1" ||
		bootstrap.WorkerSessionID != request.WorkerSessionID ||
		bootstrap.WorkerRunID != request.WorkerRunID ||
		bootstrap.RuntimeInstanceID != request.RuntimeInstanceID ||
		bootstrap.RuntimeGeneration != request.Generation ||
		bootstrap.ChannelEpoch != request.ChannelEpoch ||
		bootstrap.LoadExistingSession != (bootstrap.AgentSessionID != "") ||
		(bootstrap.AgentSessionID != "" && bootstrap.AgentSessionID != request.AgentSessionID) ||
		bootstrap.AgentProfileID != request.AgentProfile.ID ||
		bootstrap.AgentProfileRevision != request.AgentProfile.Revision ||
		bootstrap.AgentProfileDigest != request.AgentProfile.Digest ||
		!validToken(bootstrap.WorkerControlToken) {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("live Runtime bootstrap identity is stale")
	}
	return resources, bootstrap, nil
}

func (m Materializer) readLiveExisting(
	runtimeInstanceID string,
	generation uint64,
	storage application.SessionStorage,
) (application.RuntimeSecretResources, Bootstrap, error) {
	resources := runtimeSecretResources(runtimeInstanceID, generation, storage)
	runtimeRoot := filepath.Dir(resources.BackendRoot)
	controlRoot := filepath.Join(runtimeRoot, backendControlRootName)
	credentialPath := filepath.Join(controlRoot, backendControlCredentialName)
	for _, path := range []string{runtimeRoot, controlRoot, credentialPath} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return application.RuntimeSecretResources{}, Bootstrap{},
				ErrLiveRuntimeCredentialUnavailable
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime control credential %q is invalid", path)
		}
		if path != credentialPath && !info.IsDir() {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime control credential root is invalid")
		}
		if path == credentialPath &&
			(!info.Mode().IsRegular() || info.Mode().Perm() != 0o400 ||
				info.Size() <= 0 || info.Size() > 16<<10) {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime control credential file is invalid")
		}
	}
	content, err := os.ReadFile(credentialPath)
	if err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("read existing Runtime control credential")
	}
	var credential liveCredential
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("decode existing Runtime control credential")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("existing Runtime control credential has trailing content")
	}
	if credential.SchemaVersion != "switchback.runtime-live-credential.v1" {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("existing Runtime control credential schema is stale")
	}
	return resources, Bootstrap{
		SchemaVersion:        "switchback.runtime-bootstrap.v1",
		RuntimeInstanceID:    credential.RuntimeInstanceID,
		RuntimeGeneration:    credential.RuntimeGeneration,
		WorkerSessionID:      credential.WorkerSessionID,
		WorkerRunID:          credential.WorkerRunID,
		ChannelEpoch:         credential.ChannelEpoch,
		WorkerControlToken:   credential.WorkerControlToken,
		AgentProfileID:       credential.AgentProfileID,
		AgentProfileRevision: credential.AgentProfileRevision,
		AgentProfileDigest:   credential.AgentProfileDigest,
		AgentSessionID:       credential.AgentSessionID,
		LoadExistingSession:  credential.LoadExistingSession,
	}, nil
}

func (m Materializer) readExisting(
	runtimeInstanceID string,
	generation uint64,
	storage application.SessionStorage,
) (application.RuntimeSecretResources, Bootstrap, error) {
	resources := runtimeSecretResources(runtimeInstanceID, generation, storage)
	for _, path := range []string{
		resources.BackendRoot,
		resources.Bootstrap.BackendPath,
		resources.ModelCredential.BackendPath,
	} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime secret resource %q is invalid", path)
		}
		if path == resources.BackendRoot && !info.IsDir() {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime secret root is invalid")
		}
		if path != resources.BackendRoot &&
			(!info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || info.Size() <= 0 || info.Size() > 64<<10) {
			return application.RuntimeSecretResources{}, Bootstrap{},
				fmt.Errorf("existing Runtime secret file %q is invalid", path)
		}
	}
	content, err := os.ReadFile(resources.Bootstrap.BackendPath)
	if err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("read existing Runtime bootstrap")
	}
	var bootstrap Bootstrap
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bootstrap); err != nil {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("decode existing Runtime bootstrap")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return application.RuntimeSecretResources{}, Bootstrap{},
			fmt.Errorf("existing Runtime bootstrap has trailing content")
	}
	return resources, bootstrap, nil
}

func runtimeSecretResources(
	runtimeInstanceID string,
	generation uint64,
	storage application.SessionStorage,
) application.RuntimeSecretResources {
	runtimeLeaf := runtimeInstanceID + "-g" + strconv.FormatUint(generation, 10)
	backendRoot := filepath.Join(storage.BackendRoot, "runtimes", runtimeLeaf, "secret-input")
	engineRoot := filepath.Join(storage.EngineRoot, "runtimes", runtimeLeaf, "secret-input")
	return application.RuntimeSecretResources{
		BackendRoot: backendRoot,
		EngineRoot:  engineRoot,
		Bootstrap: application.StorageMount{
			BackendPath:  filepath.Join(backendRoot, "bootstrap.json"),
			EngineSource: filepath.Join(engineRoot, "bootstrap.json"),
			WorkerPath:   workerBootstrapPath,
		},
		ModelCredential: application.StorageMount{
			BackendPath:  filepath.Join(backendRoot, "model"),
			EngineSource: filepath.Join(engineRoot, "model"),
			WorkerPath:   workerModelSecretPath,
		},
	}
}

func (m Materializer) writeRootOnly(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create root-only Runtime input: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write root-only Runtime input: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync root-only Runtime input: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close root-only Runtime input: %w", err)
	}
	if err := m.Ownership.Chown(path, 0, 0); err != nil {
		return fmt.Errorf("set root ownership on Runtime input: %w", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return fmt.Errorf("protect root-only Runtime input: %w", err)
	}
	owner, err := m.OwnerUID(path)
	if err != nil || owner != 0 {
		return fmt.Errorf("Runtime input is not root-owned")
	}
	cleanup = false
	return nil
}

func validateRequest(request Request) error {
	for name, value := range map[string]string{
		"worker session id":   request.WorkerSessionID,
		"worker run id":       request.WorkerRunID,
		"runtime instance id": request.RuntimeInstanceID,
	} {
		if !canonicalIdentity(value) {
			return fmt.Errorf("%s must be canonical", name)
		}
	}
	if request.Generation == 0 || request.ChannelEpoch == 0 {
		return fmt.Errorf("Runtime generation and channel epoch must be positive")
	}
	if request.LoadExistingSession != (request.AgentSessionID != "") {
		return fmt.Errorf("existing AgentSession identity and load intent must agree")
	}
	if !validToken(request.WorkerControlToken) {
		return fmt.Errorf("WorkerControl token is invalid")
	}
	for name, root := range map[string]string{"Backend": request.SessionStorage.BackendRoot, "Engine": request.SessionStorage.EngineRoot} {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
			filepath.Base(root) != request.WorkerSessionID || filepath.Base(filepath.Dir(root)) != "sessions" {
			return fmt.Errorf("%s Session storage root is invalid", name)
		}
	}
	backendInfo, err := os.Lstat(request.SessionStorage.BackendRoot)
	if err != nil || !backendInfo.IsDir() || backendInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Backend Session storage root must be a real directory")
	}
	if len(request.ProviderCredential) == 0 || len(request.ProviderCredential) > 4<<10 ||
		bytesContainAny(request.ProviderCredential, "\r\n\x00") {
		return fmt.Errorf("provider credential must be one bounded token")
	}
	return nil
}

func canonicalIdentity(value string) bool {
	if value == "" || len(value) > 160 || strings.TrimSpace(value) != value {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (index > 0 && (character == '-' || character == '_' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func bytesContainAny(value []byte, characters string) bool {
	for _, current := range value {
		if strings.ContainsRune(characters, rune(current)) {
			return true
		}
	}
	return false
}

func validToken(value string) bool {
	return value != "" && len(value) <= 4<<10 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func OSOwnerUID(path string) (uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("file ownership metadata is unavailable")
	}
	return stat.Uid, nil
}

func OSOpenFileOwnerUID(file *os.File) (uint32, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("file ownership metadata is unavailable")
	}
	return stat.Uid, nil
}

type OSOwnership struct{}

func (OSOwnership) Chown(path string, uid, gid int) error { return os.Chown(path, uid, gid) }
