package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOptionsRequiresEveryServerOwnedInputAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	values := map[string]string{
		"SWITCHBACK_ACCEPTANCE_DATA_ROOT":             root,
		"SWITCHBACK_ACCEPTANCE_ENGINE_DATA_ROOT":      root,
		"SWITCHBACK_ACCEPTANCE_WORKER_IMAGE":          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SWITCHBACK_ACCEPTANCE_NETWORK":               "switchback-worker-control",
		"SWITCHBACK_ACCEPTANCE_BACKEND_GRPC_TARGET":   "acceptance-backend:9090",
		"SWITCHBACK_ACCEPTANCE_BACKEND_HTTP_BASE_URL": "http://acceptance-backend:8080",
		"SWITCHBACK_ACCEPTANCE_KIMI_KEY_FILE":         filepath.Join(root, "kimi"),
		"SWITCHBACK_ACCEPTANCE_DEEPSEEK_KEY_FILE":     filepath.Join(root, "deepseek"),
		"SWITCHBACK_ACCEPTANCE_SUMMARY":               filepath.Join(root, "summary.json"),
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	if _, err := loadOptions(lookup); err != nil {
		t.Fatalf("load valid options: %v", err)
	}
	delete(values, "SWITCHBACK_ACCEPTANCE_KIMI_KEY_FILE")
	if _, err := loadOptions(lookup); err == nil {
		t.Fatal("missing credential path was accepted")
	}
}

func TestLoadOptionsAllowsCredentialFreeInvalidCredentialVariant(t *testing.T) {
	root := t.TempDir()
	values := map[string]string{
		"SWITCHBACK_ACCEPTANCE_MODE":                  "invalid-credential",
		"SWITCHBACK_ACCEPTANCE_DATA_ROOT":             root,
		"SWITCHBACK_ACCEPTANCE_ENGINE_DATA_ROOT":      root,
		"SWITCHBACK_ACCEPTANCE_WORKER_IMAGE":          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SWITCHBACK_ACCEPTANCE_NETWORK":               "switchback-worker-control",
		"SWITCHBACK_ACCEPTANCE_BACKEND_GRPC_TARGET":   "acceptance-backend:9090",
		"SWITCHBACK_ACCEPTANCE_BACKEND_HTTP_BASE_URL": "http://acceptance-backend:8080",
		"SWITCHBACK_ACCEPTANCE_SUMMARY":               filepath.Join(root, "summary.json"),
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	options, err := loadOptions(lookup)
	if err != nil || options.mode != "invalid-credential" || options.kimiKeyPath != "" {
		t.Fatalf("load invalid-credential options=%+v error=%v", options, err)
	}
}

func TestLoadOptionsRequiresOnlyDeepSeekCredentialForRecoveryVariant(t *testing.T) {
	root := t.TempDir()
	values := map[string]string{
		"SWITCHBACK_ACCEPTANCE_MODE":                  "recovery",
		"SWITCHBACK_ACCEPTANCE_DATA_ROOT":             root,
		"SWITCHBACK_ACCEPTANCE_ENGINE_DATA_ROOT":      root,
		"SWITCHBACK_ACCEPTANCE_WORKER_IMAGE":          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SWITCHBACK_ACCEPTANCE_NETWORK":               "switchback-worker-control",
		"SWITCHBACK_ACCEPTANCE_BACKEND_GRPC_TARGET":   "acceptance-backend:9090",
		"SWITCHBACK_ACCEPTANCE_BACKEND_HTTP_BASE_URL": "http://acceptance-backend:8080",
		"SWITCHBACK_ACCEPTANCE_DEEPSEEK_KEY_FILE":     filepath.Join(root, "deepseek"),
		"SWITCHBACK_ACCEPTANCE_SUMMARY":               filepath.Join(root, "summary.json"),
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	options, err := loadOptions(lookup)
	if err != nil || options.mode != "recovery" || options.kimiKeyPath != "" || options.deepSeekKeyPath == "" {
		t.Fatalf("load recovery options=%+v error=%v", options, err)
	}
	delete(values, "SWITCHBACK_ACCEPTANCE_DEEPSEEK_KEY_FILE")
	if _, err := loadOptions(lookup); err == nil {
		t.Fatal("missing recovery credential path was accepted")
	}
}

func TestRecoveryEvidenceSerializesManualIdleReplacementAsNonProduction(t *testing.T) {
	productionRecoveryPath := false
	encoded, err := json.Marshal(profileSummary{
		RecoveryAttempted:      true,
		RecoveryScenario:       "idle_between_turns_manual_replacement",
		ProductionRecoveryPath: &productionRecoveryPath,
	})
	if err != nil {
		t.Fatalf("marshal recovery evidence: %v", err)
	}
	summary := string(encoded)
	for _, expected := range []string{
		`"recovery_attempted":true`,
		`"recovery_scenario":"idle_between_turns_manual_replacement"`,
		`"production_recovery_path":false`,
	} {
		if !strings.Contains(summary, expected) {
			t.Errorf("recovery evidence lacks %s: %s", expected, summary)
		}
	}
}

func TestRootCauseReturnsDeepestWrappedError(t *testing.T) {
	root := errors.New("docker daemon rejected create")
	wrapped := errors.Join(errors.New("acceptance context"), root)
	if got := rootCause(wrapped); got != wrapped {
		t.Fatalf("errors.Join has no single unwrap, got %v", got)
	}
	if got := rootCause(&singleWrappedError{cause: root}); got != root {
		t.Fatalf("root cause = %v", got)
	}
}

type singleWrappedError struct {
	cause error
}

func (err *singleWrappedError) Error() string { return "wrapped" }
func (err *singleWrappedError) Unwrap() error { return err.cause }
