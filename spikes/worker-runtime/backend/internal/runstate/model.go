package runstate

type State string

const (
	StateProvisioning   State = "provisioning"
	StateConnecting     State = "connecting"
	StateReady          State = "ready"
	StateTurnRunning    State = "turn_running"
	StateIdle           State = "idle"
	StateCompleted      State = "completed"
	StateFailed         State = "failed"
	StateDiagnosticHold State = "diagnostic_hold"
	StateShuttingDown   State = "shutting_down"
)

type ExecutionOutcome string

const (
	ExecutionPending   ExecutionOutcome = "pending"
	ExecutionCompleted ExecutionOutcome = "completed"
	ExecutionFailed    ExecutionOutcome = "failed"
)

type SpikeAcceptance string

const (
	AcceptancePending SpikeAcceptance = "pending"
	AcceptancePassed  SpikeAcceptance = "passed"
	AcceptanceFailed  SpikeAcceptance = "failed"
)

type Turn struct {
	ID string
}

type Run struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
	AgentSessionID    string
	TraceID           string
	State             State
	ActiveTurnID      string
	Turns             []Turn
	ExecutionOutcome  ExecutionOutcome
	SpikeAcceptance   SpikeAcceptance
	Revision          uint64
}

func cloneRun(run Run) Run {
	run.Turns = append([]Turn(nil), run.Turns...)
	return run
}
