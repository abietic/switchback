package architecture_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type goPackage struct {
	ImportPath string
	Imports    []string
}

func TestProductionPackageBoundaries(t *testing.T) {
	command := exec.Command("go", "list", "-json", "./cmd/...", "./internal/...")
	command.Dir = filepath.Join("..", "..")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list production packages: %v", err)
	}

	decoder := json.NewDecoder(strings.NewReader(string(output)))
	packages := make(map[string]goPackage)
	for decoder.More() {
		var current goPackage
		if err := decoder.Decode(&current); err != nil {
			t.Fatalf("decode package: %v", err)
		}
		packages[current.ImportPath] = current
	}

	expected := []string{
		"github.com/abietic/switchback/cmd/switchback-backend",
		"github.com/abietic/switchback/internal/artifacttrace/adapter",
		"github.com/abietic/switchback/internal/artifacttrace/application",
		"github.com/abietic/switchback/internal/artifacttrace/domain",
		"github.com/abietic/switchback/internal/eventingprojection/adapter",
		"github.com/abietic/switchback/internal/eventingprojection/application",
		"github.com/abietic/switchback/internal/frontier/http",
		"github.com/abietic/switchback/internal/frontier/sse",
		"github.com/abietic/switchback/internal/gateway/http",
		"github.com/abietic/switchback/internal/taskorchestration/adapter",
		"github.com/abietic/switchback/internal/taskorchestration/application",
		"github.com/abietic/switchback/internal/taskorchestration/domain",
		"github.com/abietic/switchback/internal/workerruntime/adapter",
		"github.com/abietic/switchback/internal/workerruntime/application",
		"github.com/abietic/switchback/internal/workerruntime/domain",
	}
	for _, path := range expected {
		if _, ok := packages[path]; !ok {
			t.Errorf("expected production package %q", path)
		}
	}

	for path, current := range packages {
		for _, imported := range current.Imports {
			if strings.HasPrefix(imported, "switchback.local/spikes/") {
				t.Errorf("production package %q imports Spike package %q", path, imported)
			}
			if strings.Contains(path, "/domain") && strings.HasPrefix(imported, "github.com/abietic/switchback/internal/") &&
				(slices.Contains(strings.Split(imported, "/"), "application") ||
					slices.Contains(strings.Split(imported, "/"), "adapter")) {
				t.Errorf("domain package %q depends on outer package %q", path, imported)
			}
		}
	}
}
