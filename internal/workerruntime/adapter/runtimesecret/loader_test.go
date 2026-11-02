package runtimesecret

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestLoadRuntimeInputsBindsProtectedFilesProfileAndContainerIdentity(t *testing.T) {
	directory := t.TempDir()
	profile, ok := config.ProductionProfileRegistry().Agent(config.KimiCodexAnthropicV1)
	if !ok {
		t.Fatal("Kimi profile is missing")
	}
	bootstrap := validLoadedBootstrap(profile)
	bootstrapPath := filepath.Join(directory, "bootstrap.json")
	credentialPath := filepath.Join(directory, "model")
	writeProtectedInput(t, bootstrapPath, mustJSON(t, bootstrap))
	writeProtectedInput(t, credentialPath, []byte("provider-credential"))

	inputs, err := loadRuntimeInputs(
		inputPaths{bootstrap: bootstrapPath, credential: credentialPath},
		uint32(os.Geteuid()),
		loadedEnvironment(bootstrap),
	)
	if err != nil {
		t.Fatalf("load Runtime inputs: %v", err)
	}
	if inputs.Bootstrap.AgentProfileID != profile.ID || string(inputs.Credential) != "provider-credential" {
		t.Fatalf("loaded Runtime inputs = %+v", inputs.Bootstrap)
	}
}

func TestLoadRuntimeInputsRejectsSymlinkModeIdentityAndProfileDrift(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	for _, test := range []struct {
		name   string
		mutate func(*Bootstrap, string, string)
	}{
		{name: "world readable", mutate: func(_ *Bootstrap, _ string, credentialPath string) {
			if err := os.Chmod(credentialPath, 0o444); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "container identity drift", mutate: func(bootstrap *Bootstrap, _, _ string) {
			bootstrap.WorkerRunID = "run-drift"
		}},
		{name: "profile drift", mutate: func(bootstrap *Bootstrap, _, _ string) {
			bootstrap.LiteLLMModel = "anthropic/other"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			bootstrap := validLoadedBootstrap(profile)
			bootstrapPath := filepath.Join(directory, "bootstrap.json")
			credentialPath := filepath.Join(directory, "model")
			writeProtectedInput(t, credentialPath, []byte("provider-credential"))
			test.mutate(&bootstrap, bootstrapPath, credentialPath)
			writeProtectedInput(t, bootstrapPath, mustJSON(t, bootstrap))
			if _, err := loadRuntimeInputs(
				inputPaths{bootstrap: bootstrapPath, credential: credentialPath}, uint32(os.Geteuid()),
				loadedEnvironment(validLoadedBootstrap(profile)),
			); err == nil {
				t.Fatal("unsafe Runtime inputs were accepted")
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		bootstrap := validLoadedBootstrap(profile)
		realPath := filepath.Join(directory, "real-bootstrap.json")
		writeProtectedInput(t, realPath, mustJSON(t, bootstrap))
		bootstrapPath := filepath.Join(directory, "bootstrap.json")
		if err := os.Symlink(realPath, bootstrapPath); err != nil {
			t.Fatal(err)
		}
		credentialPath := filepath.Join(directory, "model")
		writeProtectedInput(t, credentialPath, []byte("provider-credential"))
		if _, err := loadRuntimeInputs(
			inputPaths{bootstrap: bootstrapPath, credential: credentialPath}, uint32(os.Geteuid()), loadedEnvironment(bootstrap),
		); err == nil {
			t.Fatal("symlink Runtime bootstrap was accepted")
		}
	})
}

func validLoadedBootstrap(profile config.AgentProfile) Bootstrap {
	return Bootstrap{
		SchemaVersion: "switchback.runtime-bootstrap.v1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
		WorkerSessionID: "session-1", WorkerRunID: "run-1", ChannelEpoch: 7,
		WorkerControlToken: "worker-control-token", AgentProfileID: profile.ID,
		AgentProfileRevision: profile.Revision, AgentProfileDigest: profile.Digest,
		LiteLLMModel: profile.LiteLLMModel, UpstreamAPIBase: profile.APIBase,
		UpstreamProtocol: profile.UpstreamProtocol, ProxyToken: "proxy-token",
		ModelSinkToken: "model-sink-token", LiteLLMUID: LiteLLMUID, AgentUID: AgentUID,
		ModelSecretPath: workerModelSecretPath,
	}
}

func loadedEnvironment(bootstrap Bootstrap) func(string) (string, bool) {
	values := map[string]string{
		"SWITCHBACK_WORKER_SESSION_ID":   bootstrap.WorkerSessionID,
		"SWITCHBACK_WORKER_RUN_ID":       bootstrap.WorkerRunID,
		"SWITCHBACK_RUNTIME_INSTANCE_ID": bootstrap.RuntimeInstanceID,
		"SWITCHBACK_RUNTIME_GENERATION":  strconv.FormatUint(bootstrap.RuntimeGeneration, 10),
	}
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func writeProtectedInput(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
