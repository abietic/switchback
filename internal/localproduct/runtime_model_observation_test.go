package localproduct

import (
	"context"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"testing"
)

func TestRuntimeModelObservationMissingOriginalDependenciesIsNoContent(t *testing.T) {
	for _, runner := range []*runtimeRunner{nil, {}, {environment: &runtimeEnvironment{}}} {
		got := runner.PrepareModelObservation(context.Background(), workerapp.WorkerControlEvent{})
		if got.SafeCode != workerapp.ObservationCaptureUnavailable || len(got.Sealed) != 0 {
			t.Fatal("missing dependencies produced a receipt")
		}
	}
}

func TestRuntimeHistoricalObservationMissingOriginalDependenciesIsNoContent(t *testing.T) {
	for _, runner := range []*runtimeRunner{nil, {}, {environment: &runtimeEnvironment{}}} {
		got := runner.PrepareHistoricalObservation(context.Background(), artifactapp.HistoricalObservationCapture{})
		if got.ErrorCode != "scanner_unavailable" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
			t.Fatal("history invented original proof")
		}
	}
}
