package diagnostic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/diagnostic"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func TestClassifiedFailuresMapToStableSanitizedDiagnostics(t *testing.T) {
	t.Parallel()
	const rawSecret = "known-value-never-expose-67890"
	cause := errors.New("upstream Authorization Bearer " + rawSecret)
	tests := []struct {
		kind  diagnostic.FailureKind
		code  string
		stage string
	}{
		{diagnostic.FailureProviderAuth, "model_auth_failed", "model"},
		{diagnostic.FailureProviderUnavailable, "model_unavailable", "model"},
		{diagnostic.FailureACPProtocol, "agent_protocol_failed", "agent"},
		{diagnostic.FailureACPUnavailable, "agent_unavailable", "agent"},
		{diagnostic.FailureArtifactIntegrity, "artifact_integrity_failed", "artifact"},
		{diagnostic.FailureCleanupUnconfirmed, "cleanup_unconfirmed", "cleanup"},
	}
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "provider-key", Value: rawSecret}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			classified := diagnostic.Classify(test.kind, cause)
			if !errors.Is(classified, cause) {
				t.Fatal("classified failure no longer unwraps to internal cause")
			}
			formatted := fmt.Sprintf("%v %+v %#v", classified, classified, classified)
			if strings.Contains(formatted, rawSecret) {
				t.Fatalf("formatted failure leaked secret: %q", formatted)
			}
			user := diagnostic.FromError(classified)
			if user.SchemaVersion != diagnostic.SchemaVersion || user.Code != test.code || user.Stage != test.stage ||
				len(user.NextActions) == 0 {
				t.Fatalf("diagnostic = %#v", user)
			}
			encoded, err := json.Marshal(user)
			if err != nil {
				t.Fatal(err)
			}
			if err := scanner.ScanBytes(t.Context(), "sanitized.diagnostics", encoded); err != nil {
				t.Fatalf("diagnostic contains known token: %v", err)
			}
		})
	}
}

func TestUnknownFailureMapsToNonDisclosingInternalError(t *testing.T) {
	t.Parallel()
	diagnosticRecord := diagnostic.FromError(errors.New("database password known-value-never-expose"))
	if diagnosticRecord.Code != "internal_error" || diagnosticRecord.Stage != "internal" ||
		strings.Contains(diagnosticRecord.Message, "database") {
		t.Fatalf("diagnostic = %#v", diagnosticRecord)
	}
}
