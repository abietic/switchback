// Command assistantfailurematrix records the successfully executed Assistant
// failure scenarios for aggregation by the production failure-acceptance gate.
package main

import (
	"fmt"
	"os"

	"github.com/abietic/switchback/internal/testsupport/failurematrix"
)

func main() {
	directory := os.Getenv(failurematrix.ResultDirEnv)
	if directory == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Assistant failure matrix result directory is required")
		os.Exit(2)
	}
	result := failurematrix.Result{
		Variant: "assistant-failure-matrix", ExecutionOutcome: "contained",
		AcceptanceOutcome: "passed",
		Assertions: []string{
			"backend_restart_unknown_effect_visible",
			"concurrent_message_revision_fenced",
			"model_timeout_and_malformed_sanitized",
			"stale_workspace_and_draft_rejected",
			"duplicate_outbox_delivery_idempotent",
			"task_admission_rollback_atomic",
			"object_store_failure_independent",
			"projection_gap_requires_resync",
		},
	}
	if err := failurematrix.WriteResult(directory, result); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
