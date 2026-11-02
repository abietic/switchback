package postgres

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestWorkerRunStartedPayloadCarriesNetworkEvidenceWithoutDestinationClaims(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	record := application.StartWorkerRunRecord{
		Command: application.StartWorkerRunCommand{
			TaskID: "task-1", NetworkPolicyID: profile.ID,
			NetworkPolicyRevision: profile.Revision, NetworkPolicyDigest: profile.Digest,
			NetworkEnforcementStatus: config.NetworkEnforcementActive,
		},
		WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1",
		AgentSessionID: "agent-1", AgentTurnID: "turn-1",
	}
	payload, err := workerRunStartedPayload(record, 3)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		NetworkProfile config.RuntimeNetworkEvidence `json:"network_profile"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.NetworkProfile != profile.Evidence() {
		t.Fatalf("network profile = %+v", decoded.NetworkProfile)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"destination"`), []byte(`"host"`), []byte(`"url"`),
		[]byte(`"network_decision"`), []byte(`"payload_body"`),
	} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("event payload contains forbidden network claim %s", forbidden)
		}
	}
}
