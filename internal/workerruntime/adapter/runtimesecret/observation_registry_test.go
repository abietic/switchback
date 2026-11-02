package runtimesecret_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	observationProviderToken = "provider-observation-token"
	observationControlToken  = "control-observation-token"
	observationProxyToken    = "proxy-observation-token"
	observationSinkToken     = "sink-observation-token"
)

func TestObservationScannerSurvivesRestartAndTransientInputCleanup(t *testing.T) {
	storage, materializer, resources := materializeObservationRegistry(t, nil)
	if err := os.RemoveAll(resources.BackendRoot); err != nil {
		t.Fatal(err)
	}

	scanner, err := (runtimesecret.Materializer{
		OwnerUID:         func(string) (uint32, error) { return 0, nil },
		OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
	}).ObservationScanner(observationRequest(storage))
	if err != nil {
		t.Fatalf("open observation scanner after restart: %v", err)
	}
	for name, token := range map[string]string{
		"provider":   observationProviderToken,
		"control":    observationControlToken,
		"proxy":      observationProxyToken,
		"model-sink": observationSinkToken,
	} {
		t.Run(name, func(t *testing.T) {
			err := scanner.ScanJSON(context.Background(), "nested", map[string]any{
				"outer": []any{map[string]string{"text": "prefix " + token + " suffix"}},
			})
			if !secretcheck.IsExposure(err) {
				t.Fatalf("generated credential was not detected: %v", err)
			}
		})
	}
	if err := scanner.ScanJSON(context.Background(), "clean", map[string]string{"text": "clean"}); err != nil {
		t.Fatalf("clean content failed scan: %v", err)
	}
	_ = materializer

	registryPath := observationRegistryPath(storage)
	info, err := os.Lstat(registryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
		t.Fatalf("registry metadata=%v error=%v", info, err)
	}
	content, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic := fmt.Sprintf("%v %#v", scanner, *scanner); strings.Contains(diagnostic, observationProviderToken) {
		t.Fatalf("scanner formatting leaked registry token: %s", diagnostic)
	}
	if !bytes.Contains(content, []byte(observationProviderToken)) {
		t.Fatal("registry did not retain provider token")
	}
}

func TestObservationScannerDoesNotCacheRegistryAcrossBoundedCalls(t *testing.T) {
	storage, materializer, _ := materializeObservationRegistry(t, nil)
	scanner, err := materializer.ObservationScanner(observationRequest(storage))
	if err != nil {
		t.Fatal(err)
	}
	// Delete only this test fixture's original registry; the already acquired
	// scanner remains useful only to the call holding it, never to a new retry.
	if err := os.Remove(observationRegistryPath(storage)); err != nil {
		t.Fatal(err)
	}
	if err := scanner.ScanJSON(context.Background(), "original-call", map[string]string{"value": observationProviderToken}); !secretcheck.IsExposure(err) {
		t.Fatalf("original scanner lost known token: %v", err)
	}
	if _, err := materializer.ObservationScanner(observationRequest(storage)); err == nil {
		t.Fatal("new bounded call borrowed cached original tokens")
	}
}

func TestObservationScannerRejectsUnsafeRegistryAndIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, application.SessionStorage, *runtimesecret.ObservationRegistryRequest)
	}{
		{name: "identity mismatch", mutate: func(_ *testing.T, _ application.SessionStorage, request *runtimesecret.ObservationRegistryRequest) {
			request.WorkerSessionID = "session-other"
		}},
		{name: "stored identity mismatch", mutate: mutateObservationJSON(func(value map[string]any) { value["runtime_instance_id"] = "runtime-other" })},
		{name: "generation mismatch", mutate: func(_ *testing.T, _ application.SessionStorage, request *runtimesecret.ObservationRegistryRequest) {
			request.Generation = 2
		}},
		{name: "missing", mutate: func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
			if err := os.Remove(observationRegistryPath(storage)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "legacy schema", mutate: mutateObservationJSON(func(value map[string]any) { value["schema_version"] = "switchback.runtime-observation-tokens.v0" })},
		{name: "unknown field", mutate: mutateObservationJSON(func(value map[string]any) { value["unknown"] = true })},
		{name: "trailing content", mutate: func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
			path := observationRegistryPath(storage)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rewriteObservationFile(t, path, append(content, []byte("{}")...))
		}},
		{name: "symlink file", mutate: func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
			path := observationRegistryPath(storage)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "registry.json")
			if err := os.WriteFile(target, content, 0o400); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "writable file", mutate: func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
			if err := os.Chmod(observationRegistryPath(storage), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized file", mutate: func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
			rewriteObservationFile(t, observationRegistryPath(storage), bytes.Repeat([]byte{'x'}, (64<<10)+1))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage, _, _ := materializeObservationRegistry(t, nil)
			request := observationRequest(storage)
			test.mutate(t, storage, &request)
			_, err := observationScannerMaterializer(func(string) (uint32, error) { return 0, nil }).ObservationScanner(request)
			assertSanitizedObservationError(t, err)
		})
	}
}

func TestObservationScannerRejectsSymlinkAndNonPrivateDirectoryChain(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, application.SessionStorage)
	}{
		{name: "symlink control directory", mutate: func(t *testing.T, storage application.SessionStorage) {
			control := filepath.Dir(observationRegistryPath(storage))
			moved := control + "-moved"
			if err := os.Rename(control, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, control); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "group readable runtime directory", mutate: func(t *testing.T, storage application.SessionStorage) {
			runtimeRoot := filepath.Dir(filepath.Dir(observationRegistryPath(storage)))
			if err := os.Chmod(runtimeRoot, 0o750); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, _, _ := materializeObservationRegistry(t, nil)
			test.mutate(t, storage)
			_, err := observationScannerMaterializer(func(string) (uint32, error) { return 0, nil }).ObservationScanner(observationRequest(storage))
			assertSanitizedObservationError(t, err)
		})
	}
}

func TestObservationScannerRejectsNonRootOwnedFileAndDirectory(t *testing.T) {
	for _, target := range []string{"file", "directory"} {
		t.Run(target, func(t *testing.T) {
			storage, _, _ := materializeObservationRegistry(t, nil)
			badPath := observationRegistryPath(storage)
			if target == "directory" {
				badPath = filepath.Dir(badPath)
			}
			ownerUID := func(path string) (uint32, error) {
				if path == badPath {
					return 501, nil
				}
				return 0, nil
			}
			_, err := observationScannerMaterializer(ownerUID).ObservationScanner(observationRequest(storage))
			assertSanitizedObservationError(t, err)
		})
	}
}

func TestObservationScannerRejectsRegistryChangedDuringValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "grown beyond limit", mutate: func(t *testing.T, path string) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rewriteObservationFile(t, path, append(content, bytes.Repeat([]byte{' '}, 64<<10)...))
		}},
		{name: "replaced by symlink", mutate: func(t *testing.T, path string) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "replacement.json")
			if err := os.WriteFile(target, content, 0o400); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, _, _ := materializeObservationRegistry(t, nil)
			path := observationRegistryPath(storage)
			mutated := false
			ownerUID := func(candidate string) (uint32, error) {
				if candidate == path && !mutated {
					mutated = true
					test.mutate(t, path)
				}
				return 0, nil
			}
			_, err := observationScannerMaterializer(ownerUID).ObservationScanner(observationRequest(storage))
			assertSanitizedObservationError(t, err)
		})
	}
}

func TestObservationScannerRejectsReplacementWithWrongOpenedInodeOwner(t *testing.T) {
	storage, _, _ := materializeObservationRegistry(t, nil)
	path := observationRegistryPath(storage)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	ownerUID := func(candidate string) (uint32, error) {
		if candidate == path && !mutated {
			mutated = true
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o400); err != nil {
				t.Fatal(err)
			}
		}
		return 0, nil
	}
	materializer := observationScannerMaterializer(ownerUID)
	materializer.OpenFileOwnerUID = func(*os.File) (uint32, error) { return 501, nil }
	_, err = materializer.ObservationScanner(observationRequest(storage))
	assertSanitizedObservationError(t, err)
}

func materializeObservationRegistry(t *testing.T, chown func(string, int, int) error) (application.SessionStorage, runtimesecret.Materializer, application.RuntimeSecretResources) {
	t.Helper()
	backendRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	if err := os.MkdirAll(backendRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	storage := application.SessionStorage{BackendRoot: backendRoot, EngineRoot: "/srv/switchback/sessions/session-1"}
	if chown == nil {
		chown = func(string, int, int) error { return nil }
	}
	materializer := runtimesecret.Materializer{
		Ownership:        runtimesecret.OwnershipFunc(chown),
		OwnerUID:         func(string) (uint32, error) { return 0, nil },
		OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
		NewToken:         sequentialTokens(observationProxyToken, observationSinkToken),
	}
	resources, err := materializer.Materialize(runtimesecret.Request{
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1",
		Generation: 1, ChannelEpoch: 1, WorkerControlToken: observationControlToken,
		SessionStorage:     storage,
		AgentProfile:       application.ProfileRef{ID: "kimi-codex-anthropic-v1", Revision: 1, Digest: "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2"},
		ProviderCredential: runtimesecret.ProviderCredential([]byte(observationProviderToken)),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return storage, materializer, resources
}

func observationScannerMaterializer(ownerUID func(string) (uint32, error)) runtimesecret.Materializer {
	return runtimesecret.Materializer{
		OwnerUID:         ownerUID,
		OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil },
	}
}

func observationRequest(storage application.SessionStorage) runtimesecret.ObservationRegistryRequest {
	return runtimesecret.ObservationRegistryRequest{WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1", Generation: 1, SessionStorage: storage}
}

func observationRegistryPath(storage application.SessionStorage) string {
	return filepath.Join(storage.BackendRoot, "runtimes", "runtime-1-g1", "control-plane", "observation-tokens.json")
}

func mutateObservationJSON(mutate func(map[string]any)) func(*testing.T, application.SessionStorage, *runtimesecret.ObservationRegistryRequest) {
	return func(t *testing.T, storage application.SessionStorage, _ *runtimesecret.ObservationRegistryRequest) {
		path := observationRegistryPath(storage)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(content, &value); err != nil {
			t.Fatal(err)
		}
		mutate(value)
		content, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		rewriteObservationFile(t, path, append(content, '\n'))
	}
}

func rewriteObservationFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

func assertSanitizedObservationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unsafe observation registry was accepted")
	}
	if err.Error() != "Runtime observation registry is unavailable" {
		t.Fatalf("unsanitized observation registry error: %q", err)
	}
}
