package runstate

import "testing"

func TestNewStoreRejectsInvalidInitialRun(t *testing.T) {
	valid := Run{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		TraceID:           "trace-1",
		State:             StateProvisioning,
		ExecutionOutcome:  ExecutionPending,
		SpikeAcceptance:   AcceptancePending,
	}

	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{
			name: "runtime generation is not one",
			mutate: func(run *Run) {
				run.RuntimeGeneration = 2
			},
		},
		{
			name: "run does not begin provisioning",
			mutate: func(run *Run) {
				run.State = StateReady
			},
		},
		{
			name: "agent session is prebound",
			mutate: func(run *Run) {
				run.AgentSessionID = "agent-session-1"
			},
		},
		{
			name: "active turn is preseeded",
			mutate: func(run *Run) {
				run.ActiveTurnID = "turn-1"
			},
		},
		{
			name: "turn history is preseeded",
			mutate: func(run *Run) {
				run.Turns = []Turn{{ID: "turn-1"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := valid
			tt.mutate(&run)

			if _, err := NewStore(run); err == nil {
				t.Fatal("NewStore() error = nil, want invalid initial run error")
			}
		})
	}
}

func TestNewStoreAllowsProvisioningWithoutAgentSessionID(t *testing.T) {
	store, err := NewStore(Run{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		TraceID:           "trace-1",
		State:             StateProvisioning,
		ExecutionOutcome:  ExecutionPending,
		SpikeAcceptance:   AcceptancePending,
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	got := store.Snapshot()
	if got.AgentSessionID != "" || got.ActiveTurnID != "" || len(got.Turns) != 0 {
		t.Fatalf("NewStore() snapshot = %+v, want an unbound empty lifecycle", got)
	}
}
