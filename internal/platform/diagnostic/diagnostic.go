// Package diagnostic maps internal failures to stable user-visible records.
package diagnostic

import (
	"errors"
	"fmt"
)

const SchemaVersion = "frontier.diagnostic.v1"

type FailureKind string

const (
	FailureProviderAuth        FailureKind = "provider_auth"
	FailureProviderUnavailable FailureKind = "provider_unavailable"
	FailureACPProtocol         FailureKind = "acp_protocol"
	FailureACPUnavailable      FailureKind = "acp_unavailable"
	FailureArtifactIntegrity   FailureKind = "artifact_integrity"
	FailureCleanupUnconfirmed  FailureKind = "cleanup_unconfirmed"
)

type ClassifiedError struct {
	kind  FailureKind
	cause error
}

func Classify(kind FailureKind, cause error) error {
	if !validKind(kind) || cause == nil {
		return errors.New("invalid classified failure")
	}
	return &ClassifiedError{kind: kind, cause: cause}
}

func (failure *ClassifiedError) Error() string {
	if failure == nil {
		return "classified failure"
	}
	return "classified " + string(failure.kind) + " failure"
}

func (failure *ClassifiedError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure ClassifiedError) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, failure.Error())
}

type UserDiagnostic struct {
	SchemaVersion string   `json:"schema_version"`
	Code          string   `json:"code"`
	Stage         string   `json:"stage"`
	Message       string   `json:"message"`
	Recoverable   bool     `json:"recoverable"`
	NextActions   []string `json:"next_actions"`
}

func FromError(err error) UserDiagnostic {
	var classified *ClassifiedError
	if !errors.As(err, &classified) || !validKind(classified.kind) {
		return UserDiagnostic{
			SchemaVersion: SchemaVersion, Code: "internal_error", Stage: "internal",
			Message: "The operation could not be completed.", Recoverable: true,
			NextActions: []string{"retry", "inspect_trace"},
		}
	}
	switch classified.kind {
	case FailureProviderAuth:
		return diagnostic("model_auth_failed", "model", "Model authentication failed.", false, "update_model_credential")
	case FailureProviderUnavailable:
		return diagnostic("model_unavailable", "model", "The selected model route is unavailable.", true, "retry")
	case FailureACPProtocol:
		return diagnostic("agent_protocol_failed", "agent", "The Coding Agent protocol is incompatible.", false, "inspect_trace")
	case FailureACPUnavailable:
		return diagnostic("agent_unavailable", "agent", "The Coding Agent stopped responding.", true, "retry_turn", "inspect_trace")
	case FailureArtifactIntegrity:
		return diagnostic("artifact_integrity_failed", "artifact", "Artifact content could not be verified.", false, "inspect_trace")
	case FailureCleanupUnconfirmed:
		return diagnostic("cleanup_unconfirmed", "cleanup", "Runtime cleanup is not yet confirmed.", true, "retry_cleanup", "inspect_trace")
	default:
		panic("validated diagnostic kind is unmapped")
	}
}

func diagnostic(code, stage, message string, recoverable bool, actions ...string) UserDiagnostic {
	return UserDiagnostic{
		SchemaVersion: SchemaVersion, Code: code, Stage: stage, Message: message,
		Recoverable: recoverable, NextActions: actions,
	}
}

func validKind(kind FailureKind) bool {
	switch kind {
	case FailureProviderAuth, FailureProviderUnavailable, FailureACPProtocol,
		FailureACPUnavailable, FailureArtifactIntegrity, FailureCleanupUnconfirmed:
		return true
	default:
		return false
	}
}
