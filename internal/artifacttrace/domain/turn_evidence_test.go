package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestStripKnownModelMetadataWarningKeepsCanonicalAgentReport(t *testing.T) {
	input := "Warning: Model metadata for `switchback-codex` not found. " +
		"Defaulting to fallback metadata; this can degrade performance and cause issues. " +
		"## Result\nDone."
	if result := stripKnownModelMetadataWarning(input); result != "## Result\nDone." {
		t.Fatalf("canonical report = %q", result)
	}
	ordinary := "Warning: the deployment was intentionally skipped."
	if result := stripKnownModelMetadataWarning(ordinary); result != ordinary {
		t.Fatalf("ordinary Agent warning was removed: %q", result)
	}
}

func TestTraceIdentityCarriesNetworkEvidenceWithoutDestinationTelemetry(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := BuildTurnEvidenceInput{
		Identity: TurnIdentity{
			TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local",
			TaskID: "task-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
			AgentSessionID: "agent-1", AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1, TraceStreamID: "trace-1", LangfuseTraceID: "langfuse-1",
			RuntimeNetwork: RuntimeNetworkEvidence{
				ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
				EnforcementStatus: config.NetworkEnforcementActive,
			},
		},
		ServerRecordedAt: time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC),
		WorkerRecordedAt: time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC),
		RawACPJSONL: []byte(
			`{"type":"acp.raw_frame","direction":"agent_to_client","frame":"{\"jsonrpc\":\"2.0\"}"}` + "\n",
		),
		NoFilesChanged: true,
	}
	bundle, err := BuildTurnEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(bundle.Objects[0].Payload)))
	var record TraceRecord
	if err := decoder.Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.Identity.RuntimeNetwork != input.Identity.RuntimeNetwork {
		t.Fatalf("runtime network identity = %+v", record.Identity.RuntimeNetwork)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"destination"`),
		[]byte(`"host"`),
		[]byte(`"url"`),
		[]byte(`"payload_body"`),
		[]byte(`"network_decision"`),
	} {
		if bytes.Contains(bundle.Objects[0].Payload, forbidden) {
			t.Fatalf("Trace contains forbidden network telemetry field %s", forbidden)
		}
	}
}

func TestBuildTurnEvidencePreservesOpaqueObservationSource(t *testing.T) {
	input := validDomainEvidenceInput()
	input.ObservationSourceJSONL = []byte("{\"opaque\":true}\n")
	bundle, err := BuildTurnEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Objects) != 5 || bundle.Objects[2].Kind != KindObservationSource ||
		!bytes.Equal(bundle.Objects[2].Payload, input.ObservationSourceJSONL) || bundle.Objects[2].RecordCount != 1 ||
		bundle.Objects[2].FirstSequence != 1 || bundle.Objects[2].LastSequence != 1 {
		t.Fatalf("observation source object = %#v", bundle.Objects)
	}
}

func validDomainEvidenceInput() BuildTurnEvidenceInput {
	return BuildTurnEvidenceInput{
		Identity:         TurnIdentity{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local", TaskID: "task-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentSessionID: "agent-1", AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, TraceStreamID: "trace-1", LangfuseTraceID: "langfuse-1", RuntimeNetwork: RuntimeNetworkEvidence{ID: "m1_public_web_restricted_v1", EnforcementStatus: "legacy_unenforced"}},
		ServerRecordedAt: time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC), WorkerRecordedAt: time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC),
		RawACPJSONL: []byte(`{"type":"acp.raw_frame","direction":"agent_to_client","frame":"{\"jsonrpc\":\"2.0\"}"}` + "\n"), NoFilesChanged: true,
	}
}
