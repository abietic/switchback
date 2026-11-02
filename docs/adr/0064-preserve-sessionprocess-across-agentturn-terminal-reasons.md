# Preserve SessionProcess across AgentTurn terminal reasons

Once Worker accepts a managed start before the source AgentTurn becomes terminal, the resulting SessionProcess survives `completed`, `cancelled`, `timed_out` and `failed` Turn outcomes. `interrupt` is a control action that converges to `cancelled` with `terminal_reason=user_interrupt`; the raw Codex `interrupted` status remains Trace provenance rather than a Switchback AgentTurn state. A Turn terminal event still cancels the ACP prompt, active Tool and every ordinary descendant, but it does not stop or roll back an already accepted SessionProcess. This preserves useful preview servers, watchers and intermediate services even when the Agent's surrounding step ends abnormally.

Managed start and Turn terminality serialize on exact AgentTurn revision, Runtime generation and Worker owner commit order. If start acceptance commits first, SessionProcess survives; if terminality commits first, the start is rejected and cannot be retroactively adopted. This rule does not let ordinary detached commands escape their Turn and does not authorize a SessionProcess to invoke ACP or autonomous Agent work.

WorkerSession close, idle or absolute expiry, authorization/security termination, Runtime taint/fencing/replacement and resource hard limits remain higher-priority boundaries and may stop the process. Runtime replacement never auto-restarts it. Process logs and activity do not renew the Session idle clock.

## Considered Options

- Preserve only after normal completion: safer deadline semantics, but discards intermediate services that may still be useful after cancellation or failure.
- Preserve after every AgentTurn terminal reason: selected because explicit managed registration already separates the process from Turn-owned execution and keeps it visible and controllable.
- Stop every SessionProcess after any abnormal Turn: simple, but couples unrelated long-lived services to one failed interaction.
