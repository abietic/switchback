package failurematrix

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAggregateRequiresEveryExactDualOutcome(t *testing.T) {
	directory := t.TempDir()
	result := Result{
		Variant: "runtime-loss", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
		Assertions: []string{"generation_fenced"},
	}
	if err := WriteResult(directory, result); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(directory, "summary.json")
	summary, err := Aggregate(directory, summaryPath, map[string]Result{"runtime-loss": result}, time.Unix(1, 0))
	if err != nil || summary.Outcome != "passed" || len(summary.Results) != 1 {
		t.Fatalf("Aggregate() summary=%+v error=%v", summary, err)
	}

	wrong := result
	wrong.AcceptanceOutcome = "passed"
	if _, err := Aggregate(directory, summaryPath, map[string]Result{"runtime-loss": wrong}, time.Unix(2, 0)); err == nil {
		t.Fatal("Aggregate() accepted a mismatched acceptance outcome")
	}
}
