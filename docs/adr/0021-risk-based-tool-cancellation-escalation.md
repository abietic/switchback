# Risk-Based Tool Cancellation Escalation

An AgentTurn hard deadline or user cancellation does not imply that the underlying Tool has stopped. P0 classifies cancellation evidence and side effects instead of applying one response to every Tool. Confirmed cancellation lets the AgentTurn reach its requested terminal state. An unconfirmed local process-group or cgroup cancellation taints RuntimeInstance, stops new command dispatch, and opens one `tool_cancel_unconfirmed_local` RuntimeRecoveryIncident. An unconfirmed remote read-only or verifiably idempotent invocation may be fenced before the WorkerSession continues. An unconfirmed non-idempotent or unknown external effect enters `waiting_user` and cannot retry automatically.

Each Tool invocation has a stable `tool_invocation_id`, runtime generation, effect class, cancellation mode, and conditional idempotency/reconciliation references. After terminal state, late progress, success, or error is retained only as `late_tool_result` Trace and cannot mutate Tool, AgentTurn, or WorkerRun state. User actions for uncertain external effects are limited to capabilities the Tool actually exposes, such as reconcile, confirm success, compensate, or retry.

## Status

Accepted as design baseline.

## Consequences

- Local runaway processes cannot keep receiving work or writing beside a replacement runtime.
- Remote safe operations avoid unnecessary container rebuilds.
- Unknown and non-idempotent side effects fail closed to user reconciliation rather than duplicate execution.
- Tool metadata follows ADR 0022: declarations and Adapter evidence are inputs, Capability/Skilling platform policy is authoritative, and missing or conflicting metadata fails closed.
