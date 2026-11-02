## 1. Command Authority And Task Continuation

- [x] 1.1 Add a failing contract/integration test that reproduces `worker.follow_up` being projected for a `delivered` bound Task while the owner rejects it
- [x] 1.2 Define the revisioned `ContinueTaskWithWorker` command and `task.continue` ActionRef with `chat|rework` interaction metadata
- [x] 1.3 Reuse the existing Task follow-up domain plan so delivered chat returns the same Task to `running` without adding Task states or aggregates
- [x] 1.4 Persist Task transition, new WorkerRun/AgentTurn, Session references, idempotency result and owner-stream Outbox atomically
- [x] 1.5 Add Gateway routing and stale-target responses for Task continuation while preserving existing Request Changes compatibility
- [x] 1.6 Derive WorkerDetail actions from the full Task/Session/Run/Turn state tuple and render canonical `completed` as `Accepted`
- [x] 1.7 Add state-machine, initiator authorization, exact-revision, duplicate-idempotency and concurrent-command tests

## 2. Durable Display Projection

- [x] 2.1 Add PostgreSQL persistence and migrations for versioned DisplayCheckpoints and historical Worker timeline paging metadata
- [x] 2.2 Implement an ACP Display Adapter that maps normalized Agent messages, Tool lifecycle, activities, approvals, Artifacts and safe errors to stable DisplayParts
- [x] 2.3 Emit bounded TransientDisplayDelta text without advancing durable View cursor and checkpoint every semantic Tool/approval/Artifact/terminal boundary
- [x] 2.4 Enforce safe-field allowlists, hidden-reasoning exclusion, Tool output truncation and authorized evidence references
- [x] 2.5 Materialize DisplayCheckpoint data into WorkerDetail snapshots and durable deltas without requiring browser Trace reconstruction
- [x] 2.6 Add stable Run/Turn grouping and cursor pagination for older Worker history
- [x] 2.7 Add reconciliation for terminal Turns missing a complete checkpoint and mark non-backfillable historical content truthfully
- [x] 2.8 Add adapter, checkpoint versioning, event deduplication, late-evidence, replay-gap and sanitization tests

## 3. Canonical Artifact Review

- [x] 3.1 Extend authorized Artifact reads with a checksum-verified review manifest for report and patch content
- [x] 3.2 Separate Runtime/provider diagnostics from canonical report content and preserve source Run/Turn provenance
- [x] 3.3 Define empty, binary, oversized, parse-failed, missing and checksum-failed review outcomes with action gating
- [x] 3.4 Add Artifact adapter and PostgreSQL/ObjectStore integration tests for integrity, authorization and fallback states

## 4. Worker Workbench Frontend

- [x] 4.1 Pin `@pierre/diffs` and `@pierre/trees` with lockfile integrity and add Switchback-owned PatchReviewAdapter and ArtifactTreeAdapter characterization tests
- [x] 4.2 Render durable Agent, Tool, activity, approval, Artifact and error parts grouped by WorkerRun/AgentTurn
- [x] 4.3 Make Agent Markdown visible by default and Tool Calls collapsed with expandable safe arguments, output, locations, status and duration
- [x] 4.4 Preserve the composer draft across stale refresh, replace the affected projection replica and require explicit resend against the refreshed action
- [x] 4.5 Replace the narrow Stage result panel with a resizable Report/Diff/Files Workbench that retains chat and composer
- [x] 4.6 Add safe Markdown report rendering with raw HTML, script and automatic remote-image loading disabled
- [x] 4.7 Add changed-file tree, file statistics, unified/split diff, wrapping, hunk navigation and no-code-changes state
- [x] 4.8 Correct preview/download icon semantics and render integrity, parse and mixed-origin warnings without card nesting or layout overlap
- [x] 4.9 Add component tests for long output, long paths, Markdown sanitization, empty patch and every Artifact failure state

## 5. Verification And Rollout

- [x] 5.1 Run Go unit and PostgreSQL integration suites for Task, Worker, projection, Gateway, Frontier and Artifact boundaries
- [x] 5.2 Run frontend unit, build and Playwright suites across desktop and mobile viewports, including reconnect and stale-command cases
- [x] 5.3 Execute a real M2 provider acceptance flow covering first delivery, direct delivered chat, same-Task rework, Accept and standalone follow-up
- [x] 5.4 Verify raw ACP/provider payloads and credentials do not appear in WorkerDetail, Artifact report HTML or browser network responses
- [x] 5.5 Update Worker delivery operations, manual acceptance and M2 acceptance documents with the corrected product flow and rollback flags
- [x] 5.6 Validate the OpenSpec change and record exact command, test, browser and real-provider evidence before archive
