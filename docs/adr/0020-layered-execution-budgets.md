# Layered Execution Budgets

> Status: Superseded by [ADR 0055](0055-use-a-fixed-agentturn-hard-deadline.md) on 2026-07-15. Retained as design history; it is not the P0 implementation baseline.

TaskSpec uses one `execution_policy` with three distinct controls: WorkerRunDeadline bounds the total stage execution, AgentTurnDeadline bounds one user/Agent interaction, and ToolTimeout protects one tool invocation from hanging. WorkerRun and AgentTurn deadlines are business decision boundaries that use the accepted interactive `ask_user` or explicitly authorized unattended `auto_cancel` behavior. ToolTimeout is a local execution guard: it cancels only the current tool and returns a structured timeout result to the Agent without directly cancelling the AgentTurn or WorkerRun.

`runtime_config` keeps container network, resource, and isolation settings and no longer carries an overloaded `timeout_seconds`. A TaskSpec draft may inherit policy defaults, but the accepted TaskSpec snapshot records resolved durations, provenance, and expiry actions. AgentTurn budget may inherit WorkerRun remaining budget but cannot extend the WorkerRun deadline.

Platform or organization ExecutionBudgetPolicy owns defaults and hard ceilings. Assistant and Delivery may select initial values only within that policy. The accepted TaskSpec remains immutable; a user authorized by TaskActionPolicy or an exact TaskActionGrant extends WorkerRun or AgentTurn through an appended ExecutionBudgetGrant that records the prior budget, granted budget, reason, actor, and policy snapshot. Worker and Agent may request a grant but cannot activate one. A timed-out Tool is not extended in place; a higher-budget retry is a new invocation allowed by pre-authorized retry policy or a new grant. ADR 0047 defines who may create that grant; this ADR continues to define its budget semantics.

ADR 0054 extends this model across Task-bound WorkerSession absolute expiry. A future continuation Run receives an explicit initiator-approved ContinuationBudgetSpec rather than inheriting source budget/Grants or automatically resetting TaskSpec budget. All Task-bound Runs consume one TaskCumulativeActiveBudget based on actual ActiveExecutionClock time; standalone Runs do not charge an origin Task merely because they retain lineage.

WorkerRun and AgentTurn consume recoverable active execution time. Model inference, tool execution, `active_quiet`, and cancellation drain count; queued/pre-start, `waiting_user`, confirmed platform outage, and runtime recovery pause. A separate WorkerRun wall-clock hard cap starts on first run and never pauses. ToolTimeout uses the runtime shim's monotonic clock from invocation start and never pauses. Backend state transitions persist accumulated active duration, current segment, pause reason, and applied grants so recovery and replay do not reset or double-count budget.

P0 policy revision `p0-default-v1` sets WorkerRun active default to 60 minutes, active ceiling to 6 hours, and non-pausing wall cap to 24 hours. AgentTurn active default is 30 minutes with a 60-minute ceiling. Tool profiles use 10 minutes for `standard`, 30 minutes for `build_test`, and a common 60-minute ceiling. Worker selects profiles from structured Tool kind and TaskSpec authorization; Agent output alone cannot promote a Tool to a longer profile. The platform collects at least two weeks of active-time distributions, grant rates, timeout/retry outcomes, and wall-cap states before superseding the policy revision.

## Status

Superseded.

## Consequences

- Task orchestration can distinguish an overdue stage, an overdue interaction, and a hung tool without guessing from one timer.
- P0 may inherit profile defaults to keep the user interface small while preserving all three contract layers.
- Tool adapters must return a normalized timeout result and must not translate every tool timeout into ACP session cancellation.
- Tool cancellation escalation is defined by ADR 0021, and the accepted declaration/evidence/platform-policy authority chain is defined by ADR 0022.
