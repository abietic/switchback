package contractsv1_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestJSONContractsRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		decode func([]byte) (any, error)
	}{
		{name: "TaskSpec", file: "task-spec.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeTaskSpec(data) }},
		{name: "RuntimeControlEnvelope", file: "runtime-control-envelope.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeRuntimeControlEnvelope(data) }},
		{name: "RuntimeControlFrame", file: "runtime-control-frame.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeRuntimeControlFrame(data) }},
		{name: "IntegrationEvent v1", file: "integration-event.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeIntegrationEvent(data) }},
		{name: "IntegrationEvent v2", file: "integration-event-v2.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeIntegrationEvent(data) }},
		{name: "TraceManifest", file: "trace-manifest.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeTraceManifest(data) }},
		{name: "ArtifactRef", file: "artifact-ref.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeArtifactRef(data) }},
		{name: "ConversationCommand", file: "assistant-conversation-command.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeConversationCommand(data) }},
		{name: "AssistantDecision", file: "assistant-decision.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeAssistantDecision(data) }},
		{name: "AssistantProjectionEvent", file: "assistant-projection-event.json", decode: func(data []byte) (any, error) { return contractsv1.DecodeAssistantProjectionEvent(data) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := readTestdata(t, test.file)
			first, err := test.decode(data)
			if err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatalf("marshal contract: %v", err)
			}
			second, err := test.decode(encoded)
			if err != nil {
				t.Fatalf("decode round trip: %v", err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("round trip changed contract\nfirst:  %#v\nsecond: %#v", first, second)
			}
		})
	}
}

func TestJSONContractsRejectUnknownFields(t *testing.T) {
	tests := []struct {
		file   string
		decode func([]byte) error
	}{
		{file: "task-spec.json", decode: func(data []byte) error { _, err := contractsv1.DecodeTaskSpec(data); return err }},
		{file: "runtime-control-envelope.json", decode: func(data []byte) error { _, err := contractsv1.DecodeRuntimeControlEnvelope(data); return err }},
		{file: "runtime-control-frame.json", decode: func(data []byte) error { _, err := contractsv1.DecodeRuntimeControlFrame(data); return err }},
		{file: "integration-event.json", decode: func(data []byte) error { _, err := contractsv1.DecodeIntegrationEvent(data); return err }},
		{file: "integration-event-v2.json", decode: func(data []byte) error { _, err := contractsv1.DecodeIntegrationEvent(data); return err }},
		{file: "trace-manifest.json", decode: func(data []byte) error { _, err := contractsv1.DecodeTraceManifest(data); return err }},
		{file: "artifact-ref.json", decode: func(data []byte) error { _, err := contractsv1.DecodeArtifactRef(data); return err }},
		{file: "assistant-conversation-command.json", decode: func(data []byte) error { _, err := contractsv1.DecodeConversationCommand(data); return err }},
		{file: "assistant-decision.json", decode: func(data []byte) error { _, err := contractsv1.DecodeAssistantDecision(data); return err }},
		{file: "assistant-projection-event.json", decode: func(data []byte) error { _, err := contractsv1.DecodeAssistantProjectionEvent(data); return err }},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			data := bytes.TrimSpace(readTestdata(t, test.file))
			data = append(data[:len(data)-1], []byte(`,"client_override":"forbidden"}`)...)
			if err := test.decode(data); err == nil {
				t.Fatal("unknown client field was accepted")
			}
		})
	}
}

func TestIntegrationEventV1StrictlyRejectsClientCommandIdentity(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(readTestdata(t, "integration-event.json"), &document); err != nil {
		t.Fatal(err)
	}
	document["client_command_id"] = "web-raw-command-1"
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contractsv1.DecodeIntegrationEvent(data); err == nil {
		t.Fatal("event.v1 accepted event.v2-only client_command_id")
	}

	event := decodeIntegrationEventFixture(t, "integration-event.json")
	event.ClientCommandID = "web-raw-command-1"
	if err := event.Validate(); err == nil {
		t.Fatal("programmatic event.v1 accepted event.v2-only client command identity")
	}
}

func TestIntegrationEventV2CarriesExplicitClientCommandIdentity(t *testing.T) {
	event := decodeIntegrationEventFixture(t, "integration-event-v2.json")
	if event.SchemaVersion != contractsv1.IntegrationEventSchemaVersion {
		t.Fatalf("current schema version=%q, want %q", event.SchemaVersion, contractsv1.IntegrationEventSchemaVersion)
	}
	if event.ClientCommandID != "web-raw-command-1" {
		t.Fatalf("client command identity=%q", event.ClientCommandID)
	}
	if contractsv1.IntegrationEventLegacySchemaVersion == contractsv1.IntegrationEventSchemaVersion {
		t.Fatal("legacy and current IntegrationEvent schema versions must be distinct")
	}
}

func TestConversationCommandRejectsClientAuthorityOverrides(t *testing.T) {
	for _, field := range []string{"tenant_id", "user_id", "model_profile", "policy_revision", "tool_config", "secret"} {
		data := bytes.TrimSpace(readTestdata(t, "assistant-conversation-command.json"))
		data = append(data[:len(data)-1], []byte(`,"`+field+`":"caller-controlled"}`)...)
		if _, err := contractsv1.DecodeConversationCommand(data); err == nil {
			t.Fatalf("client authority override %q was accepted", field)
		}
	}
}

func TestJSONSchemaTopLevelMatchesGoContract(t *testing.T) {
	tests := []struct {
		file     string
		contract reflect.Type
	}{
		{file: "task-spec.schema.json", contract: reflect.TypeFor[contractsv1.TaskSpec]()},
		{file: "runtime-control-envelope.schema.json", contract: reflect.TypeFor[contractsv1.RuntimeControlEnvelope]()},
		{file: "runtime-control-frame.schema.json", contract: reflect.TypeFor[contractsv1.RuntimeControlFrame]()},
		{file: "integration-event.schema.json", contract: reflect.TypeFor[contractsv1.IntegrationEvent]()},
		{file: "trace-manifest.schema.json", contract: reflect.TypeFor[contractsv1.TraceManifest]()},
		{file: "artifact-ref.schema.json", contract: reflect.TypeFor[contractsv1.ArtifactRef]()},
		{file: "assistant-conversation-command.schema.json", contract: reflect.TypeFor[contractsv1.ConversationCommand]()},
		{file: "assistant-decision.schema.json", contract: reflect.TypeFor[contractsv1.AssistantDecision]()},
		{file: "assistant-projection-event.schema.json", contract: reflect.TypeFor[contractsv1.AssistantProjectionEvent]()},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			var schema struct {
				ID                   string                     `json:"$id"`
				AdditionalProperties bool                       `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
			}
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schema", "v1", test.file))
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if schema.ID == "" || schema.AdditionalProperties {
				t.Fatalf("schema must have id and reject additional properties: %+v", schema)
			}
			fields := jsonFields(test.contract)
			if !reflect.DeepEqual(fields, sortedKeys(schema.Properties)) {
				t.Fatalf("schema fields = %v, Go fields = %v", sortedKeys(schema.Properties), fields)
			}
			sortStrings(schema.Required)
			requiredFields := requiredJSONFields(test.contract)
			if !reflect.DeepEqual(schema.Required, requiredFields) {
				t.Fatalf("required fields = %v, required Go fields = %v", schema.Required, requiredFields)
			}
		})
	}
}

func TestIntegrationEventV1SchemaExcludesClientCommandIdentity(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schema", "v1", "integration-event-v1.schema.json"))
	if err != nil {
		t.Fatalf("read legacy schema: %v", err)
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode legacy schema: %v", err)
	}
	if _, found := schema.Properties["client_command_id"]; found {
		t.Fatal("event.v1 schema exposes event.v2-only client_command_id")
	}
}

func TestIntegrationEventVersionedJSONSchemasAreClosedAndDisjoint(t *testing.T) {
	legacySchema := compileContractSchema(t, "integration-event-v1.schema.json")
	currentSchema := compileContractSchema(t, "integration-event.schema.json")
	legacyFixture := schemaInstance(t, readTestdata(t, "integration-event.json"))
	currentFixture := schemaInstance(t, readTestdata(t, "integration-event-v2.json"))
	if err := legacySchema.Validate(legacyFixture); err != nil {
		t.Fatalf("event.v1 schema rejected legacy fixture: %v", err)
	}
	if err := currentSchema.Validate(currentFixture); err != nil {
		t.Fatalf("event.v2 schema rejected current fixture: %v", err)
	}
	if err := legacySchema.Validate(currentFixture); err == nil {
		t.Fatal("event.v1 schema accepted event.v2 fixture")
	}
	if err := currentSchema.Validate(legacyFixture); err == nil {
		t.Fatal("event.v2 schema accepted event.v1 fixture")
	}

	var legacyWithClient map[string]any
	if err := json.Unmarshal(readTestdata(t, "integration-event.json"), &legacyWithClient); err != nil {
		t.Fatal(err)
	}
	legacyWithClient["client_command_id"] = "v2-only-client-command"
	encoded, err := json.Marshal(legacyWithClient)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySchema.Validate(schemaInstance(t, encoded)); err == nil {
		t.Fatal("closed event.v1 schema accepted client_command_id")
	}
}

func TestArtifactRefAllowsStandaloneWorkerRun(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(readTestdata(t, "artifact-ref.json"), &document); err != nil {
		t.Fatal(err)
	}
	document["task_id"] = ""
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contractsv1.DecodeArtifactRef(data); err != nil {
		t.Fatalf("standalone WorkerRun ArtifactRef was rejected: %v", err)
	}
}

func TestAssistantDecisionProviderSchemaContract(t *testing.T) {
	const (
		wantID     = "assistant-decision-strict-json-schema-v1"
		wantDigest = "sha256:a19f1aad64846edcf40d5c03117934bdb273ed7106b294161795ba0f8d891e47"
	)

	schemaBytes := contractsv1.AssistantDecisionProviderSchema()
	if contractsv1.AssistantDecisionProviderSchemaID != wantID {
		t.Fatalf("provider schema id = %q, want %q", contractsv1.AssistantDecisionProviderSchemaID, wantID)
	}
	if contractsv1.AssistantDecisionProviderSchemaDigest != wantDigest {
		t.Fatalf("provider schema digest = %q, want %q", contractsv1.AssistantDecisionProviderSchemaDigest, wantDigest)
	}
	sum := sha256.Sum256(schemaBytes)
	if actual := "sha256:" + hex.EncodeToString(sum[:]); actual != wantDigest {
		t.Fatalf("canonical provider schema digest = %q, want %q", actual, wantDigest)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, schemaBytes); err != nil {
		t.Fatalf("compact provider schema: %v", err)
	}
	if !bytes.Equal(compact.Bytes(), schemaBytes) {
		t.Fatal("provider schema bytes are not canonical minified JSON")
	}

	var document map[string]any
	if err := json.Unmarshal(schemaBytes, &document); err != nil {
		t.Fatalf("decode provider schema: %v", err)
	}
	for _, forbidden := range []string{"$schema", "$id", "title"} {
		if _, ok := document[forbidden]; ok {
			t.Fatalf("provider schema contains forbidden metadata %q", forbidden)
		}
	}
	assertClosedProviderSchemaObjects(t, document, "$")

	schemaBytes[0] = '!'
	fresh := contractsv1.AssistantDecisionProviderSchema()
	if len(fresh) == 0 || fresh[0] != '{' {
		t.Fatal("provider schema accessor returned shared mutable bytes")
	}
}

func TestAssistantDecisionProviderSchemaAcceptsStrictlyDecodableFixtures(t *testing.T) {
	schema := compileAssistantDecisionProviderSchema(t)
	for _, decisionType := range []string{"clarification", "informational", "ready_draft"} {
		t.Run(decisionType, func(t *testing.T) {
			fixture := providerDecisionFixture(decisionType)
			encoded, err := json.Marshal(fixture)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode fixture for schema validation: %v", err)
			}
			if err := schema.Validate(instance); err != nil {
				t.Fatalf("provider schema rejected fixture: %v", err)
			}
			if _, err := contractsv1.DecodeAssistantDecision(encoded); err != nil {
				t.Fatalf("strict decision decoder rejected fixture: %v", err)
			}
		})
	}
}

func TestAssistantDecisionProviderSchemaRejectsClosedAndInvalidShapes(t *testing.T) {
	schema := compileAssistantDecisionProviderSchema(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "extra top-level property", mutate: func(value map[string]any) { value["provider_override"] = true }},
		{name: "extra readiness property", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["provider_override"] = true
		}},
		{name: "extra dimension property", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["dimensions"].([]any)[0].(map[string]any)["provider_override"] = true
		}},
		{name: "extra TaskDraft property", mutate: func(value map[string]any) {
			value["task_draft"].(map[string]any)["provider_override"] = true
		}},
		{name: "extra target property", mutate: func(value map[string]any) {
			value["task_draft"].(map[string]any)["target"].(map[string]any)["provider_override"] = true
		}},
		{name: "extra task-spec property", mutate: func(value map[string]any) {
			value["task_draft"].(map[string]any)["task_spec"].(map[string]any)["provider_override"] = true
		}},
		{name: "non-null checkpoint", mutate: func(value map[string]any) {
			value["checkpoint"] = map[string]any{}
		}},
		{name: "wrong decision schema version", mutate: func(value map[string]any) {
			value["schema_version"] = "assistant.decision.v2"
		}},
		{name: "wrong readiness schema version", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["schema_version"] = "assistant.readiness_assessment.v2"
		}},
		{name: "wrong TaskDraft schema version", mutate: func(value map[string]any) {
			value["task_draft"].(map[string]any)["schema_version"] = "assistant.task_draft.v2"
		}},
		{name: "unknown decision enum", mutate: func(value map[string]any) {
			value["decision_type"] = "execute"
		}},
		{name: "unknown readiness enum", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["status"] = "ready"
		}},
		{name: "unknown dimension enum", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["dimensions"].([]any)[0].(map[string]any)["dimension"] = "budget"
		}},
		{name: "unknown dimension status enum", mutate: func(value map[string]any) {
			value["readiness"].(map[string]any)["dimensions"].([]any)[0].(map[string]any)["status"] = "unknown"
		}},
		{name: "wrong dimension count", mutate: func(value map[string]any) {
			dimensions := value["readiness"].(map[string]any)["dimensions"].([]any)
			value["readiness"].(map[string]any)["dimensions"] = dimensions[:len(dimensions)-1]
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := providerDecisionFixture("ready_draft")
			test.mutate(fixture)
			encoded, err := json.Marshal(fixture)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode fixture for schema validation: %v", err)
			}
			if err := schema.Validate(instance); err == nil {
				t.Fatal("invalid provider decision was accepted")
			}
		})
	}
}

func TestAssistantDecisionDecoderRejectsSemanticShapesBeyondProviderSchema(t *testing.T) {
	schema := compileAssistantDecisionProviderSchema(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "duplicate readiness dimension", mutate: func(fixture map[string]any) {
			dimensions := fixture["readiness"].(map[string]any)["dimensions"].([]any)
			dimensions[1].(map[string]any)["dimension"] = "goal"
		}},
		{name: "ready decision without TaskDraft", mutate: func(fixture map[string]any) {
			fixture["task_draft"] = nil
		}},
		{name: "clarification decision with TaskDraft", mutate: func(fixture map[string]any) {
			fixture["decision_type"] = "clarification"
			fixture["clarification_question"] = "Which target should be used?"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := providerDecisionFixture("ready_draft")
			test.mutate(fixture)
			encoded, err := json.Marshal(fixture)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode fixture for schema validation: %v", err)
			}
			if err := schema.Validate(instance); err != nil {
				t.Fatalf("provider shape unexpectedly rejected semantic fixture: %v", err)
			}
			if _, err := contractsv1.DecodeAssistantDecision(encoded); err == nil {
				t.Fatal("strict decision decoder accepted an invalid semantic shape")
			}
		})
	}
}

func compileAssistantDecisionProviderSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	document, err := jsonschema.UnmarshalJSON(
		bytes.NewReader(contractsv1.AssistantDecisionProviderSchema()),
	)
	if err != nil {
		t.Fatalf("decode provider schema: %v", err)
	}
	const location = "https://switchback.local/internal/assistant-decision-provider-schema.json"
	if err := compiler.AddResource(location, document); err != nil {
		t.Fatalf("add provider schema: %v", err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatalf("compile provider schema: %v", err)
	}
	return schema
}

func compileContractSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schema", "v1", name))
	if err != nil {
		t.Fatalf("read contract schema %s: %v", name, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode contract schema %s: %v", name, err)
	}
	location := "https://switchback.local/test/" + name
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(location, document); err != nil {
		t.Fatalf("add contract schema %s: %v", name, err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatalf("compile contract schema %s: %v", name, err)
	}
	return schema
}

func schemaInstance(t *testing.T, data []byte) any {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode schema instance: %v", err)
	}
	return value
}

func assertClosedProviderSchemaObjects(t *testing.T, value any, path string) {
	t.Helper()
	switch current := value.(type) {
	case map[string]any:
		if current["type"] == "object" {
			closed, ok := current["additionalProperties"].(bool)
			if !ok || closed {
				t.Fatalf("provider schema object %s is not closed", path)
			}
		}
		for key, child := range current {
			assertClosedProviderSchemaObjects(t, child, path+"."+key)
		}
	case []any:
		for index, child := range current {
			assertClosedProviderSchemaObjects(t, child, path+"["+string(rune('0'+index))+"]")
		}
	}
}

func providerDecisionFixture(decisionType string) map[string]any {
	dimensions := make([]any, 0, 7)
	for _, dimension := range []string{
		"goal",
		"deliverable_acceptance",
		"scope_target",
		"constraints",
		"context_access",
		"risk_approval",
		"execution_policy",
	} {
		dimensions = append(dimensions, map[string]any{
			"dimension": dimension, "status": "known",
			"summary": "The requirement is explicit.", "evidence_refs": []string{},
		})
	}
	fixture := map[string]any{
		"schema_version":         "assistant.decision.v1",
		"decision_type":          decisionType,
		"user_visible_content":   "The request has been assessed.",
		"clarification_question": "",
		"task_draft":             nil,
		"checkpoint":             nil,
		"readiness": map[string]any{
			"schema_version":          "assistant.readiness_assessment.v1",
			"assessment_id":           "model-proposal",
			"revision":                1,
			"policy_revision":         "assistant-readiness-v1",
			"dimensions":              dimensions,
			"blocking_unknowns":       []string{},
			"assumptions":             []string{},
			"clarification_questions": 0,
			"status":                  "ready_confirmed",
			"evaluated_at":            "2026-01-01T00:00:00Z",
		},
	}
	switch decisionType {
	case "clarification":
		fixture["clarification_question"] = "Which Workspace should receive the change?"
		fixture["readiness"].(map[string]any)["status"] = "clarifying"
		fixture["readiness"].(map[string]any)["blocking_unknowns"] = []string{"Workspace is not selected."}
		dimensions[2].(map[string]any)["status"] = "blocking_unknown"
	case "ready_draft":
		fixture["task_draft"] = map[string]any{
			"schema_version":          "assistant.task_draft.v1",
			"task_draft_id":           "model-draft",
			"revision":                1,
			"initiator_user_id":       "model-user",
			"conversation_id":         "model-conversation",
			"workspace_id":            "workspace-local",
			"workspace_revision_id":   "workspace-revision-local-v1",
			"readiness_assessment_id": "model-proposal",
			"task_spec_digest":        "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"title":                   "Implement the requested change",
			"goal":                    "Deliver the requested repository change.",
			"target": map[string]any{
				"kind": "workspace",
				"ref":  "workspace-revision-local-v1",
			},
			"constraints":         []string{},
			"acceptance_criteria": []string{"Focused tests pass."},
			"assumptions":         []string{},
			"task_spec":           map[string]any{},
			"created_at":          "2026-01-01T00:00:00Z",
		}
	}
	return fixture
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func decodeIntegrationEventFixture(t *testing.T, name string) contractsv1.IntegrationEvent {
	t.Helper()
	event, err := contractsv1.DecodeIntegrationEvent(readTestdata(t, name))
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return event
}

func jsonFields(contract reflect.Type) []string {
	fields := make([]string, 0, contract.NumField())
	for index := range contract.NumField() {
		name := contract.Field(index).Tag.Get("json")
		if comma := bytes.IndexByte([]byte(name), ','); comma >= 0 {
			name = name[:comma]
		}
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	sortStrings(fields)
	return fields
}

func requiredJSONFields(contract reflect.Type) []string {
	fields := make([]string, 0, contract.NumField())
	for index := range contract.NumField() {
		tag := contract.Field(index).Tag.Get("json")
		name := tag
		if comma := bytes.IndexByte([]byte(name), ','); comma >= 0 {
			if bytes.Contains([]byte(tag[comma+1:]), []byte("omitempty")) {
				continue
			}
			name = name[:comma]
		}
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	sortStrings(fields)
	return fields
}

func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
