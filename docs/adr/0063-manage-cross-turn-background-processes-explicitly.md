# Manage cross-Turn background processes explicitly

WorkerSession Runtime remains resident between AgentTurns, but container residency must not let arbitrary shell descendants escape a Turn deadline. Switchback therefore treats every ordinary shell/Tool process as AgentTurn-scoped and stops its process group/cgroup when the Turn becomes terminal. Shell syntax such as `&`, `nohup`, daemonization, stdio detachment or parent exit does not promote a process.

Preview servers, development servers and file watchers may outlive the originating Turn only when they are launched through the Worker managed-process interface and recorded as a `SessionProcess`. The record binds a stable process identity to WorkerSession, source AgentTurn/Tool, Runtime generation/process handle, kind, command summary, cwd, declared ports, lifecycle state, bounded log reference and revision. WorkerDetail exposes running/history entries and authorized stop/restart actions. An unknown orphan cannot be adopted after Turn terminality.

A SessionProcess is not an AgentTurn, WorkerRun or new authority. It cannot invoke an ACP prompt or perform autonomous Agent work, and it inherits the Runtime's resource profile, network policy, WorkingCopy mount, ResolvedToolPolicy, ApprovalGrant and Secret boundaries. Its live use remains visible to aggregate capacity accounting, and logs are bounded/rotated into Trace instead of streaming without limit.

Explicit WorkerSession close, idle expiry, absolute expiry, authorization/security termination stop every SessionProcess. Process output, logs, port traffic, health checks and status updates are not user interaction and do not renew the 48-hour idle clock. Runtime generation fencing or replacement marks affected processes `stopped` when termination is proven or `lost` when it is not; Switchback does not automatically restart them, because replay could duplicate ports, watchers or external effects. A user or later AgentTurn may explicitly create a new process identity. Survival across individual AgentTurn terminal reasons is governed by [ADR 0064](0064-preserve-sessionprocess-across-agentturn-terminal-reasons.md).

## Considered Options

- Stop every process at AgentTurn terminal: simplest, but breaks preview and iterative development continuity.
- Allow any detached descendant to survive: maximally flexible, but bypasses cancellation/deadline semantics and hides resource or external activity.
- Explicit managed SessionProcess: selected because it preserves intentional interactive services while keeping ownership, visibility and cleanup enforceable.
