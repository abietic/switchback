package architecture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRecoveryAcceptanceReportsOnlyExecutedEvidence(t *testing.T) {
	root := filepath.Join("..", "..")
	scriptBytes, err := os.ReadFile(filepath.Join(root, "scripts", "run-runtime-recovery-acceptance"))
	if err != nil {
		t.Fatalf("read runtime recovery acceptance script: %v", err)
	}
	script := string(scriptBytes)

	for _, overclaim := range []string{
		`"idle_and_active_loss"`,
		`"backend_restart_persistence"`,
		`"failed_fencing_and_replacement_convergence"`,
	} {
		if strings.Contains(script, overclaim) {
			t.Errorf("runtime recovery acceptance still hard-codes unexecuted evidence %s", overclaim)
		}
	}
	for _, required := range []string{
		"integration_test_results",
		"required_enablement_scenarios",
		"missing_enablement_scenarios",
		"enablement_ready",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("runtime recovery acceptance lacks truthful evidence field %q", required)
		}
	}

	harnessBytes, err := os.ReadFile(filepath.Join(root, "test", "acceptance", "realprovider", "main.go"))
	if err != nil {
		t.Fatalf("read fixed-image acceptance harness: %v", err)
	}
	harness := string(harnessBytes)
	for _, required := range []string{
		`json:"recovery_scenario,omitempty"`,
		`json:"production_recovery_path,omitempty"`,
		`idle_between_turns_manual_replacement`,
	} {
		if !strings.Contains(harness, required) {
			t.Errorf("fixed-image recovery evidence lacks %q", required)
		}
	}
}
