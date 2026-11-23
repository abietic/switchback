//go:build firstreportengine

package firstreport_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
)

func TestFirstReportCapacityReadsQualifiedEngine(t *testing.T) {
	expected := os.Getenv("SWITCHBACK_FIRST_REPORT_ENGINE_ID")
	if expected == "" {
		t.Fatal("the live sample must be pinned to an independently qualified Engine ID")
	}
	engine, err := dockeradapter.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := dockeradapter.SampleFirstReportCapacity(context.Background(), engine, "unexpected-engine"); err == nil {
		t.Fatal("the sampler accepted a different Engine identity")
	}
	facts, err := dockeradapter.SampleFirstReportCapacity(context.Background(), engine, expected)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SWITCHBACK_QUALIFIED_ENGINE_SAMPLE %s", encoded)
}
