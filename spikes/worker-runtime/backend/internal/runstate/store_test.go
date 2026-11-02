package runstate

import "testing"

func TestStoreLifecycleTransitions(t *testing.T) {
	tests := []struct {
		name  string
		steps []lifecycleStep
	}{
		{
			name: "successful two turn run",
			steps: []lifecycleStep{
				{name: "connect", apply: advance(StateConnecting), wantState: StateConnecting, wantAgentSessionID: ""},
				{name: "bind ACP agent session", apply: bindAgentSession("agent-session-1"), wantState: StateConnecting, wantAgentSessionID: "agent-session-1"},
				{name: "ready", apply: advance(StateReady), wantState: StateReady, wantAgentSessionID: "agent-session-1"},
				{name: "start turn one", apply: startTurn("turn-1"), wantState: StateTurnRunning, wantActiveTurnID: "turn-1", wantAgentSessionID: "agent-session-1"},
				{name: "complete turn one", apply: completeTurn("turn-1"), wantState: StateIdle, wantAgentSessionID: "agent-session-1"},
				{name: "start turn two", apply: startTurn("turn-2"), wantState: StateTurnRunning, wantActiveTurnID: "turn-2", wantAgentSessionID: "agent-session-1"},
				{name: "complete turn two", apply: completeTurn("turn-2"), wantState: StateCompleted, wantAgentSessionID: "agent-session-1"},
				{name: "shut down", apply: advance(StateShuttingDown), wantState: StateShuttingDown, wantAgentSessionID: "agent-session-1"},
			},
		},
		{
			name: "failure diagnostics and shutdown",
			steps: []lifecycleStep{
				{name: "fail", apply: advance(StateFailed), wantState: StateFailed},
				{name: "hold diagnostics", apply: advance(StateDiagnosticHold), wantState: StateDiagnosticHold},
				{name: "shut down", apply: advance(StateShuttingDown), wantState: StateShuttingDown},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newProvisioningTestStore(t)
			run := store.Snapshot()

			for _, step := range tt.steps {
				t.Run(step.name, func(t *testing.T) {
					var err error
					run, err = step.apply(store, run.Revision)
					if err != nil {
						t.Fatalf("mutation error = %v", err)
					}
					if run.State != step.wantState {
						t.Fatalf("state = %q, want %q", run.State, step.wantState)
					}
					if run.ActiveTurnID != step.wantActiveTurnID {
						t.Fatalf("active turn ID = %q, want %q", run.ActiveTurnID, step.wantActiveTurnID)
					}
					if run.RuntimeGeneration != 1 {
						t.Fatalf("runtime generation = %d, want 1", run.RuntimeGeneration)
					}
					if run.AgentSessionID != step.wantAgentSessionID {
						t.Fatalf("agent session ID = %q, want %q", run.AgentSessionID, step.wantAgentSessionID)
					}
				})
			}
		})
	}
}

func TestStoreRejectsForbiddenTransitionsAndSecondActiveTurn(t *testing.T) {
	store := newReadyTestStore(t)
	run := store.Snapshot()

	if _, err := store.Advance(run.Revision, StateConnecting); err == nil {
		t.Fatal("Advance() error = nil, want forbidden transition error")
	}
	if got := store.Snapshot(); got.Revision != run.Revision {
		t.Fatalf("revision after forbidden transition = %d, want %d", got.Revision, run.Revision)
	}

	run = mustStartTurn(t, store, run, "turn-1")
	if _, err := store.StartTurn(run.Revision, "turn-2"); err == nil {
		t.Fatal("StartTurn() error = nil, want active turn error")
	}
	if got := store.Snapshot(); got.ActiveTurnID != "turn-1" || got.Revision != run.Revision {
		t.Fatalf("run changed after rejected second turn: %+v", got)
	}

	run = mustCompleteTurn(t, store, run, "turn-1")
	if _, err := store.Advance(run.Revision, StateCompleted); err == nil {
		t.Fatal("Advance() error = nil, want idle to completed rejection")
	}
	if got := store.Snapshot(); got.State != StateIdle || got.Revision != run.Revision {
		t.Fatalf("run changed after rejected generic completion: %+v", got)
	}
	if _, err := store.CompleteTurn(run.Revision, "turn-1"); err == nil {
		t.Fatal("CompleteTurn() error = nil, want forbidden transition error")
	}
	if _, err := store.Advance(run.Revision, StateDiagnosticHold); err == nil {
		t.Fatal("Advance() error = nil, want forbidden transition error")
	}
}

func TestStoreBindsAgentSessionIDOnceWhileConnecting(t *testing.T) {
	store := newProvisioningTestStore(t)
	run := store.Snapshot()

	if _, err := store.BindAgentSession(run.Revision, "agent-session-1"); err == nil {
		t.Fatal("BindAgentSession() error = nil, want provisioning bind rejection")
	}
	if got := store.Snapshot(); got.Revision != run.Revision || got.State != StateProvisioning || got.AgentSessionID != "" {
		t.Fatalf("run changed after rejected provisioning bind: %+v", got)
	}

	run = mustAdvance(t, store, run, StateConnecting)
	if _, err := store.Advance(run.Revision, StateReady); err == nil {
		t.Fatal("Advance() error = nil, want ready transition to require agent session ID")
	}
	if got := store.Snapshot(); got.Revision != run.Revision || got.State != StateConnecting || got.AgentSessionID != "" {
		t.Fatalf("run changed after rejected ready transition: %+v", got)
	}

	run = mustBindAgentSession(t, store, run, "agent-session-1")
	if run.State != StateConnecting || run.AgentSessionID != "agent-session-1" {
		t.Fatalf("BindAgentSession() = %+v, want connecting run with bound agent session", run)
	}

	beforeRebind := run
	if _, err := store.BindAgentSession(run.Revision, "agent-session-2"); err == nil {
		t.Fatal("BindAgentSession() error = nil, want rejected rebind")
	}
	if got := store.Snapshot(); got.Revision != beforeRebind.Revision || got.State != beforeRebind.State || got.AgentSessionID != beforeRebind.AgentSessionID {
		t.Fatalf("run changed after rejected rebind: %+v, want revision/state/agent session ID from %+v", got, beforeRebind)
	}

	run = mustAdvance(t, store, run, StateReady)
	run = mustStartTurn(t, store, run, "turn-1")
	run = mustCompleteTurn(t, store, run, "turn-1")
	run = mustStartTurn(t, store, run, "turn-2")
	run = mustCompleteTurn(t, store, run, "turn-2")
	if _, err := store.StartTurn(run.Revision, "turn-3"); err == nil {
		t.Fatal("StartTurn() error = nil, want completed run to reject a third top-level turn")
	}
	if got := store.Snapshot(); got.State != StateCompleted || got.Revision != run.Revision || len(got.Turns) != 2 {
		t.Fatalf("run changed after rejected third turn: %+v", got)
	}
}

func TestStoreAllowsFailureFromEveryRunningState(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Store, Run) Run
	}{
		{
			name: "provisioning",
			prepare: func(_ *testing.T, _ *Store, run Run) Run {
				return run
			},
		},
		{
			name: "connecting",
			prepare: func(t *testing.T, store *Store, run Run) Run {
				return mustAdvance(t, store, run, StateConnecting)
			},
		},
		{
			name: "ready",
			prepare: func(t *testing.T, store *Store, run Run) Run {
				return mustReadyRun(t, store, run)
			},
		},
		{
			name: "turn running",
			prepare: func(t *testing.T, store *Store, run Run) Run {
				run = mustReadyRun(t, store, run)
				return mustStartTurn(t, store, run, "turn-1")
			},
		},
		{
			name: "idle",
			prepare: func(t *testing.T, store *Store, run Run) Run {
				run = mustReadyRun(t, store, run)
				run = mustStartTurn(t, store, run, "turn-1")
				return mustCompleteTurn(t, store, run, "turn-1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newProvisioningTestStore(t)
			run := tt.prepare(t, store, store.Snapshot())

			run = mustAdvance(t, store, run, StateFailed)
			if run.State != StateFailed {
				t.Fatalf("state = %q, want %q", run.State, StateFailed)
			}
			if run.ActiveTurnID != "" {
				t.Fatalf("active turn ID = %q after failure, want empty", run.ActiveTurnID)
			}
		})
	}
}

func TestStoreWritesExecutionAndAcceptanceIndependently(t *testing.T) {
	store := newProvisioningTestStore(t)
	run := store.Snapshot()

	var err error
	run, err = store.SetExecutionOutcome(run.Revision, ExecutionCompleted)
	if err != nil {
		t.Fatalf("SetExecutionOutcome() error = %v", err)
	}
	if run.ExecutionOutcome != ExecutionCompleted || run.SpikeAcceptance != AcceptancePending {
		t.Fatalf("execution mutation changed outcomes to %+v", run)
	}

	run, err = store.SetSpikeAcceptance(run.Revision, AcceptanceFailed)
	if err != nil {
		t.Fatalf("SetSpikeAcceptance() error = %v", err)
	}
	if run.ExecutionOutcome != ExecutionCompleted || run.SpikeAcceptance != AcceptanceFailed {
		t.Fatalf("acceptance mutation changed outcomes to %+v", run)
	}
}

func TestStoreReturnsIndependentSnapshots(t *testing.T) {
	store := newReadyTestStore(t)
	run := store.Snapshot()
	run = mustStartTurn(t, store, run, "turn-1")

	run.AgentSessionID = "mutated-agent-session"
	run.Turns[0].ID = "mutated-turn"
	run.Turns = append(run.Turns, Turn{ID: "extra-turn"})

	got := store.Snapshot()
	if got.AgentSessionID != "agent-session-1" {
		t.Fatalf("agent session ID = %q, want original value", got.AgentSessionID)
	}
	if len(got.Turns) != 1 || got.Turns[0].ID != "turn-1" {
		t.Fatalf("turns = %+v, want only original turn", got.Turns)
	}
}

func TestStoreAllowsOnlyOneCompetingMutationForARevision(t *testing.T) {
	store := newReadyTestStore(t)
	run := store.Snapshot()

	type result struct {
		run Run
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, turnID := range []string{"turn-1", "turn-2"} {
		go func(turnID string) {
			<-start
			updated, err := store.StartTurn(run.Revision, turnID)
			results <- result{run: updated, err: err}
		}(turnID)
	}
	close(start)

	successes := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
			if result.run.State != StateTurnRunning || result.run.ActiveTurnID == "" {
				t.Fatalf("successful concurrent mutation returned %+v", result.run)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("successful competing mutations = %d, want 1", successes)
	}

	got := store.Snapshot()
	if got.State != StateTurnRunning || got.ActiveTurnID == "" || len(got.Turns) != 1 {
		t.Fatalf("store accepted more than one active turn: %+v", got)
	}
}

type lifecycleStep struct {
	name               string
	apply              func(*Store, uint64) (Run, error)
	wantState          State
	wantActiveTurnID   string
	wantAgentSessionID string
}

func advance(state State) func(*Store, uint64) (Run, error) {
	return func(store *Store, expectedRevision uint64) (Run, error) {
		return store.Advance(expectedRevision, state)
	}
}

func startTurn(turnID string) func(*Store, uint64) (Run, error) {
	return func(store *Store, expectedRevision uint64) (Run, error) {
		return store.StartTurn(expectedRevision, turnID)
	}
}

func completeTurn(turnID string) func(*Store, uint64) (Run, error) {
	return func(store *Store, expectedRevision uint64) (Run, error) {
		return store.CompleteTurn(expectedRevision, turnID)
	}
}

func bindAgentSession(agentSessionID string) func(*Store, uint64) (Run, error) {
	return func(store *Store, expectedRevision uint64) (Run, error) {
		return store.BindAgentSession(expectedRevision, agentSessionID)
	}
}

func newProvisioningTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := NewStore(testRun())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func newReadyTestStore(t *testing.T) *Store {
	t.Helper()

	store := newProvisioningTestStore(t)
	mustReadyRun(t, store, store.Snapshot())
	return store
}

func testRun() Run {
	return Run{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		TraceID:           "trace-1",
		State:             StateProvisioning,
		ExecutionOutcome:  ExecutionPending,
		SpikeAcceptance:   AcceptancePending,
	}
}

func mustReadyRun(t *testing.T, store *Store, run Run) Run {
	t.Helper()

	run = mustAdvance(t, store, run, StateConnecting)
	run = mustBindAgentSession(t, store, run, "agent-session-1")
	return mustAdvance(t, store, run, StateReady)
}

func mustAdvance(t *testing.T, store *Store, run Run, state State) Run {
	t.Helper()

	updated, err := store.Advance(run.Revision, state)
	if err != nil {
		t.Fatalf("Advance(%q) error = %v", state, err)
	}
	return updated
}

func mustStartTurn(t *testing.T, store *Store, run Run, turnID string) Run {
	t.Helper()

	updated, err := store.StartTurn(run.Revision, turnID)
	if err != nil {
		t.Fatalf("StartTurn(%q) error = %v", turnID, err)
	}
	return updated
}

func mustCompleteTurn(t *testing.T, store *Store, run Run, turnID string) Run {
	t.Helper()

	updated, err := store.CompleteTurn(run.Revision, turnID)
	if err != nil {
		t.Fatalf("CompleteTurn(%q) error = %v", turnID, err)
	}
	return updated
}

func mustBindAgentSession(t *testing.T, store *Store, run Run, agentSessionID string) Run {
	t.Helper()

	updated, err := store.BindAgentSession(run.Revision, agentSessionID)
	if err != nil {
		t.Fatalf("BindAgentSession(%q) error = %v", agentSessionID, err)
	}
	return updated
}
