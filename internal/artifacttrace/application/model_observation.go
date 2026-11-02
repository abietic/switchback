package application

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// ModelObservationFragment builds exclusively from an original event and the
// server-owned Run scope. It neither accepts the event nor trusts receipt fields.
func ModelObservationFragment(scope domain.ObservationScope, e contractsv1.RuntimeControlEnvelope) (domain.ObservationFragment, error) {
	var m workerapp.ModelObservation
	if json.Unmarshal(e.Payload, &m) != nil || e.MessageType != "model.observation" || m.WorkerSessionID != scope.WorkerSessionID || m.WorkerRunID != scope.WorkerRunID || m.WorkerSessionID != string(e.WorkerSessionID) || m.WorkerRunID != string(e.WorkerRunID) || m.AgentTurnID != string(e.AgentTurnID) || m.RuntimeID != string(e.RuntimeInstanceID) || m.Generation != uint64(e.RuntimeGeneration) {
		return domain.ObservationFragment{}, errors.New("scope_invalid")
	}
	turn := &domain.TurnIdentity{TenantID: scope.TenantID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID, WorkerSessionID: e.WorkerSessionID, WorkerRunID: e.WorkerRunID, AgentSessionID: e.AgentSessionID, AgentTurnID: e.AgentTurnID, RuntimeInstanceID: e.RuntimeInstanceID, RuntimeGeneration: e.RuntimeGeneration}
	f := domain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: domain.FragmentModel, Scope: scope, Turn: turn, ObservationID: m.ObservationID, StartedAt: m.StartedAt, EndedAt: &m.EndedAt, InputJSON: string(m.Input), OutputJSON: string(m.Output), Model: m.Model, Route: m.Route, Status: m.Status, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens}
	return f, nil
}

// A no-content candidate still needs the same logical ID. Placeholder values
// never leave this function and are not receipts or published model content.
func ModelObservationSourceID(f domain.ObservationFragment) (string, error) {
	now := time.Unix(1, 0).UTC()
	f.StartedAt = now
	f.EndedAt = &now
	f.InputJSON = "{}"
	f.OutputJSON = "{}"
	f.Model = "unavailable"
	f.Route = "unavailable"
	f.Status = "unavailable"
	f.InputTokens = 0
	f.OutputTokens = 0
	return domain.ObservationFragmentID(f)
}
