# ADR 0056: Preserve Failed Workspace Bootstrap For Retry

- Status: Accepted
- Date: 2026-07-15

## Context

The M1 Worker-first form can create a repository-backed Workspace inline, but provider authorization, fetch and materialization are network operations that cannot be part of Task creation or a PostgreSQL transaction. A failed import therefore needs an explicit durable outcome. Automatically deleting the Workspace loses audit and makes response-loss recovery ambiguous; creating a queued Task before the first WorkspaceRevision exists couples bootstrap failure to the Task state machine.

## Decision

- Repository-backed Workspace bootstrap uses `provisioning -> ready | setup_failed`.
- CreateWorkspace persists the logical Workspace, initial owner Membership, RepositoryImport intent and Outbox without calling GitHub or cloning inside the transaction.
- RepositoryImport owns a stable bootstrap identity and append-only attempts. Attempts record generation, timestamps, classified result and a sanitized error reference; provider credentials and Tokens are never stored in the record.
- Failure leaves Workspace in `setup_failed`, with no active WorkspaceRevision, Task, WorkerSession, RepositoryCheckout or partially authoritative revision.
- An authorized user may call RetryRepositoryImport against the original import identity and expected revision. Current repository read authority is revalidated and a new command idempotency key creates at most one additional attempt; response loss returns the existing result.
- An empty failed Workspace may be explicitly archived. Failure does not trigger compensating physical deletion.
- Success commits RepositoryCheckout, the first WorkspaceRevision, ActiveRevisionPointer, Workspace `ready`, RepositoryImport result and Outbox in one PostgreSQL transaction.
- TaskSpec preview and submission remain blocked until the exact ready Workspace/revision is explicitly selected.

## Consequences

- Bootstrap failures remain diagnosable and recoverable without polluting Task state.
- Workspace `active_revision_id` is conditional and absent while `provisioning` or `setup_failed`.
- The UI can preserve one continuous form experience while rendering durable bootstrap progress and retry/archive actions.
- Failed empty Workspace records may accumulate, so archival and later retention policy are required, but physical cleanup is not on the correctness path.

## Alternatives

- Automatically delete Workspace on import failure: rejected because compensation and response loss can make identity and audit ambiguous.
- Create a queued Task before import completes: rejected because Workspace setup is not Task execution and should not add Task states or retry semantics.
- Retry by creating a new Workspace every time: rejected because duplicates consume identity, quota and operator attention while obscuring one bootstrap history.
