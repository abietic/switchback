package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
)

func TestMappedHostPathVerifierChecksEquivalentBackendPath(t *testing.T) {
	backendRoot := t.TempDir()
	backendPath := filepath.Join(backendRoot, "sessions", "session-1", "working-copy")
	if err := os.MkdirAll(backendPath, 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := dockeradapter.MappedHostPathVerifier{
		EngineRoot: "/host/switchback", BackendRoot: backendRoot,
	}
	err := verifier.VerifyHostPaths(context.Background(), "/host/switchback", []dockeradapter.HostPathRequirement{{
		Path: "/host/switchback/sessions/session-1/working-copy", Kind: dockeradapter.HostPathDirectory,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyHostPaths(context.Background(), "/host/switchback", []dockeradapter.HostPathRequirement{{
		Path: "/host/other", Kind: dockeradapter.HostPathDirectory,
	}}); err == nil {
		t.Fatal("mapped verifier accepted a path outside EngineDataRoot")
	}
}
