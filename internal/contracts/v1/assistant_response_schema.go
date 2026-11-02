package contractsv1

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

const (
	AssistantStructuredOutputModeStrictJSONSchema = "strict_json_schema"
	AssistantDecisionProviderSchemaID             = "assistant-decision-strict-json-schema-v1"
	AssistantDecisionProviderSchemaDigest         = "sha256:a19f1aad64846edcf40d5c03117934bdb273ed7106b294161795ba0f8d891e47"
)

//go:embed assistant_decision_provider_schema.json
var embeddedAssistantDecisionProviderSchema []byte

var canonicalAssistantDecisionProviderSchema = bytes.TrimSpace(
	embeddedAssistantDecisionProviderSchema,
)

func init() {
	sum := sha256.Sum256(canonicalAssistantDecisionProviderSchema)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != AssistantDecisionProviderSchemaDigest {
		panic("embedded Assistant decision provider schema digest mismatch")
	}
}

func AssistantDecisionProviderSchema() []byte {
	return bytes.Clone(canonicalAssistantDecisionProviderSchema)
}
