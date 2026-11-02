## Why

WorkerDetail currently advertises follow-up chat in a `delivered` bound Task state that the command owner rejects, and its durable history omits Agent output and Tool activity even though the ACP Trace contains that evidence. Report and patch Artifacts are technically reachable but are not presented as a usable coding-agent review surface, so the M2 Assistant-to-Worker path cannot yet provide a trustworthy multi-turn product experience.

## What Changes

- Make projected Worker actions agree with authoritative Task, WorkerSession, WorkerRun and AgentTurn state.
- Route free-form feedback on a `delivered` Task through the existing same-Task continuation semantics and reserve standalone follow-up for terminal Tasks.
- Preserve `completed` as the terminal accepted-delivery state while rendering it as `Accepted`; keep the WorkerSession independently interactive.
- Materialize durable Agent messages, Tool activity, approvals, errors and Artifact references from normalized execution evidence into DisplayCheckpoint-backed Worker history.
- Replace raw report text and the handwritten line diff with a resizable Worker Workbench that renders safe Markdown, a changed-file tree and structured patch review.
- Integrate pinned `@pierre/diffs` and `@pierre/trees` versions behind Switchback-owned adapters.
- Preserve user drafts across stale-target refresh without automatically replaying a command against changed authority.
- Add deterministic contract, integration and browser acceptance coverage for delivered continuation, accepted standalone chat, replayable history and Artifact integrity states.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `worker-detail-interaction`: Strengthen WorkerDetail action truthfulness, durable Agent/Tool history, Artifact Workbench rendering, stale-command recovery and multi-turn acceptance behavior.

## Impact

- Affects Task action routing, Worker follow-up command selection, Eventing & Projection materialization, Frontier WorkerDetail snapshots/SSE, Artifact reads and the React WorkerDetail surface.
- Adds frontend dependencies on `@pierre/diffs` and beta `@pierre/trees`; both are version-pinned and isolated behind local adapters so provider API churn does not become a Switchback contract.
- Reuses the existing `task-session-persistence` requirements rather than changing Task terminality or introducing another workflow aggregate.
- M2 remains responsible for Assistant dispatch. Its archive is held until the corrected Worker path passes a real delivered-feedback-accept-standalone sequence.
- Web IDE and managed SessionProcess delivery are explicitly excluded and tracked by `worker-web-ide-session-process`.
