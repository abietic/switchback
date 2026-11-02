// Command failurematrix validates and aggregates the executable production
// failure acceptance records emitted by integration and real-provider tests.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abietic/switchback/internal/testsupport/failurematrix"
)

func main() {
	resultDirectory := os.Getenv(failurematrix.ResultDirEnv)
	summaryPath := os.Getenv("SWITCHBACK_FAILURE_MATRIX_SUMMARY")
	if resultDirectory == "" || summaryPath == "" || !filepath.IsAbs(resultDirectory) || !filepath.IsAbs(summaryPath) {
		_, _ = fmt.Fprintln(os.Stderr, "failure matrix output paths must be absolute")
		os.Exit(2)
	}
	summary, err := failurematrix.Aggregate(resultDirectory, summaryPath, expectedResults(), time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, _ := json.Marshal(map[string]any{
		"outcome": summary.Outcome, "results": len(summary.Results), "summary": summaryPath,
	})
	_, _ = fmt.Fprintln(os.Stdout, string(encoded))
}

func expectedResults() map[string]failurematrix.Result {
	return map[string]failurematrix.Result{
		"assistant-failure-matrix": {
			Variant: "assistant-failure-matrix", ExecutionOutcome: "contained", AcceptanceOutcome: "passed",
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
		},
		"invalid-credential": {
			Variant: "invalid-credential", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
			Assertions: []string{"model_authentication_rejected", "false_completion_absent", "runtime_cleanup_confirmed"},
		},
		"checksum-mismatch": {
			Variant: "checksum-mismatch", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
			Assertions: []string{"checksum_rejected", "artifact_not_published", "outbox_not_emitted"},
		},
		"langfuse-unavailable": {
			Variant: "langfuse-unavailable", ExecutionOutcome: "completed", AcceptanceOutcome: "failed",
			Assertions: []string{"execution_preserved", "observability_degraded", "reprojection_required"},
		},
		"quota-full": {
			Variant: "quota-full", ExecutionOutcome: "not_started", AcceptanceOutcome: "pending",
			Assertions: []string{"quota_not_exceeded", "task_remains_queued", "fifo_head_preserved"},
		},
		"deadline": {
			Variant: "deadline", ExecutionOutcome: "timed_out", AcceptanceOutcome: "pending",
			Assertions: []string{"server_deadline_enforced", "turn_timed_out", "scheduled_command_fenced"},
		},
		"runtime-loss": {
			Variant: "runtime-loss", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
			Assertions: []string{"generation_fenced", "implicit_replacement_absent", "task_waiting_user"},
		},
		"cleanup-failure": {
			Variant: "cleanup-failure", ExecutionOutcome: "completed", AcceptanceOutcome: "failed",
			Assertions: []string{"residue_reported", "reconciliation_scheduled", "eventual_cleanup_confirmed"},
		},
	}
}
