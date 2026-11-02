package runtimesecret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/abietic/switchback/internal/platform/config"
	"golang.org/x/sys/unix"
)

const (
	maxBootstrapBytes  = 64 << 10
	maxCredentialBytes = 4 << 10
)

type RuntimeInputs struct {
	Bootstrap  Bootstrap
	Credential ProviderCredential
}

type inputPaths struct {
	bootstrap  string
	credential string
}

// LoadRuntimeInputs reads the two fixed root-only resources mounted by the
// Backend-owned provisioner and binds them to the immutable container identity.
func LoadRuntimeInputs() (RuntimeInputs, error) {
	return loadRuntimeInputs(inputPaths{
		bootstrap:  workerBootstrapPath,
		credential: workerModelSecretPath,
	}, 0, os.LookupEnv)
}

func loadRuntimeInputs(paths inputPaths, expectedOwner uint32, lookupEnv func(string) (string, bool)) (RuntimeInputs, error) {
	bootstrapBytes, err := readProtectedRegularFile(paths.bootstrap, expectedOwner, maxBootstrapBytes)
	if err != nil {
		return RuntimeInputs{}, fmt.Errorf("read Runtime bootstrap: %w", err)
	}
	credential, err := readProtectedRegularFile(paths.credential, expectedOwner, maxCredentialBytes)
	if err != nil {
		return RuntimeInputs{}, fmt.Errorf("read Runtime model credential: %w", err)
	}
	var bootstrap Bootstrap
	decoder := json.NewDecoder(bytes.NewReader(bootstrapBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bootstrap); err != nil {
		return RuntimeInputs{}, fmt.Errorf("decode Runtime bootstrap")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RuntimeInputs{}, fmt.Errorf("decode Runtime bootstrap")
	}
	if err := validateLoadedInputs(bootstrap, ProviderCredential(credential), lookupEnv); err != nil {
		return RuntimeInputs{}, err
	}
	return RuntimeInputs{Bootstrap: bootstrap, Credential: ProviderCredential(credential)}, nil
}

func readProtectedRegularFile(path string, expectedOwner uint32, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open protected Runtime input")
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect protected Runtime input")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != expectedOwner || stat.Mode&0o777 != 0o400 ||
		stat.Size <= 0 || stat.Size > limit {
		return nil, fmt.Errorf("protected Runtime input ownership, mode, or size is invalid")
	}
	content := make([]byte, stat.Size)
	read := 0
	for read < len(content) {
		count, readErr := unix.Read(fd, content[read:])
		if readErr != nil {
			return nil, fmt.Errorf("read protected Runtime input")
		}
		if count == 0 {
			return nil, fmt.Errorf("protected Runtime input was truncated")
		}
		read += count
	}
	return content, nil
}

func validateLoadedInputs(bootstrap Bootstrap, credential ProviderCredential, lookupEnv func(string) (string, bool)) error {
	if bootstrap.SchemaVersion != "switchback.runtime-bootstrap.v1" || bootstrap.RuntimeGeneration == 0 ||
		bootstrap.ChannelEpoch == 0 || bootstrap.LiteLLMUID != LiteLLMUID || bootstrap.AgentUID != AgentUID ||
		bootstrap.ModelSecretPath != workerModelSecretPath || !canonicalIdentity(bootstrap.RuntimeInstanceID) ||
		!canonicalIdentity(bootstrap.WorkerSessionID) || !canonicalIdentity(bootstrap.WorkerRunID) ||
		!validToken(bootstrap.WorkerControlToken) || !validToken(bootstrap.ProxyToken) ||
		!validToken(bootstrap.ModelSinkToken) || len(credential) == 0 || len(credential) > maxCredentialBytes ||
		bytesContainAny(credential, "\r\n\x00") {
		return fmt.Errorf("Runtime bootstrap policy is invalid")
	}
	profile, err := config.ProductionProfileRegistry().ResolveAgent(config.AgentProfileRef{
		ID: bootstrap.AgentProfileID, Revision: bootstrap.AgentProfileRevision, Digest: bootstrap.AgentProfileDigest,
	})
	if err != nil || profile.LiteLLMModel != bootstrap.LiteLLMModel || profile.APIBase != bootstrap.UpstreamAPIBase ||
		profile.UpstreamProtocol != bootstrap.UpstreamProtocol || profile.AutomaticFallbackProfileID != "" {
		return fmt.Errorf("Runtime AgentProfile snapshot is invalid")
	}
	for name, expected := range map[string]string{
		"SWITCHBACK_WORKER_SESSION_ID":   bootstrap.WorkerSessionID,
		"SWITCHBACK_WORKER_RUN_ID":       bootstrap.WorkerRunID,
		"SWITCHBACK_RUNTIME_INSTANCE_ID": bootstrap.RuntimeInstanceID,
		"SWITCHBACK_RUNTIME_GENERATION":  strconv.FormatUint(bootstrap.RuntimeGeneration, 10),
	} {
		actual, exists := lookupEnv(name)
		if !exists || actual != expected {
			return fmt.Errorf("Runtime container identity does not match bootstrap")
		}
	}
	return nil
}
