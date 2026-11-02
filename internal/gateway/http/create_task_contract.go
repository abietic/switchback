package gatewayhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

const directTaskInputSchemaVersion = "gateway.direct_task_input.v1"

// DirectTaskInput contains user execution intent only. Identity, RuntimeSpec,
// credentials, readiness evidence, and creation intent are server-owned.
type DirectTaskInput struct {
	SchemaVersion       string                 `json:"schema_version"`
	IdempotencyKey      string                 `json:"idempotency_key"`
	ProjectID           string                 `json:"project_id"`
	WorkspaceID         string                 `json:"workspace_id"`
	WorkspaceRevisionID string                 `json:"workspace_revision_id"`
	Title               string                 `json:"title"`
	Goal                string                 `json:"goal"`
	Target              contractsv1.TaskTarget `json:"target"`
	Constraints         []string               `json:"constraints"`
	AcceptanceCriteria  []string               `json:"acceptance_criteria"`
	AgentProfileID      string                 `json:"agent_profile_id"`
	AcceptanceMode      string                 `json:"acceptance_mode"`
	UserVisibleTypes    []string               `json:"user_visible_types"`
}

func DecodeDirectTaskInput(data []byte) (DirectTaskInput, error) {
	var input DirectTaskInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return DirectTaskInput{}, fmt.Errorf("decode direct Task input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return DirectTaskInput{}, fmt.Errorf("decode direct Task input: trailing JSON value")
	}
	if err := input.Validate(); err != nil {
		return DirectTaskInput{}, err
	}
	return input, nil
}

func (input DirectTaskInput) Validate() error {
	if input.SchemaVersion != directTaskInputSchemaVersion {
		return fmt.Errorf("unsupported direct Task input schema %q", input.SchemaVersion)
	}
	for name, value := range map[string]string{
		"idempotency_key": input.IdempotencyKey, "project_id": input.ProjectID,
		"workspace_id": input.WorkspaceID, "workspace_revision_id": input.WorkspaceRevisionID,
		"title": input.Title, "goal": input.Goal, "target.kind": input.Target.Kind,
		"target.ref": input.Target.Ref, "agent_profile_id": input.AgentProfileID,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s must be a nonempty canonical string", name)
		}
	}
	if input.Constraints == nil || len(input.AcceptanceCriteria) == 0 {
		return fmt.Errorf("constraints and acceptance_criteria must be explicit arrays")
	}
	if input.AcceptanceMode != "auto" && input.AcceptanceMode != "manual" {
		return fmt.Errorf("unsupported acceptance mode %q", input.AcceptanceMode)
	}
	if len(input.UserVisibleTypes) == 0 {
		return fmt.Errorf("user_visible_types must not be empty")
	}
	for _, artifactType := range input.UserVisibleTypes {
		if artifactType != "report" && artifactType != "patch" {
			return fmt.Errorf("unsupported M1 Artifact type %q", artifactType)
		}
	}
	return nil
}
