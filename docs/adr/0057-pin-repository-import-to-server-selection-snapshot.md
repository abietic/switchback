# ADR 0057: Pin Repository Import To A Server Selection Snapshot

- Status: Accepted
- Date: 2026-07-15

## Context

Git branch and tag names are movable references. Resolving them again at RepositoryImport retry, Task submission or Worker dispatch would let one confirmed Workspace bootstrap execute against different source code. Provider lookup is also a network operation and cannot run inside the CreateWorkspace PostgreSQL transaction. Trusting a client-supplied commit SHA would preserve reproducibility but weaken repository identity and authorization evidence.

## Decision

- Code Platform Access exposes ResolveRepositorySelection. Outside the database transaction it uses the managed provider adapter to resolve the user's branch, tag or commit selection.
- The result is a short-lived immutable RepositorySelectionSnapshot bound to tenant, Switchback actor, stable repository identity, requested ref, exact resolved commit SHA, provider observation time, expiry, observed read-grant evidence and a versioned canonical digest protected by server integrity controls.
- Snapshot is selection evidence, not authorization. M1 confirmation and RepositoryImport fetch independently revalidate the configured local repository allowlist/read policy; later authenticated profiles revalidate RepositoryAccessGrant, Switchback policy and provider access.
- CreateWorkspace accepts only an unexpired Snapshot matching actor, tenant and repository, and persists its exact SHA in RepositoryImport without provider network work in the transaction.
- RepositoryImport, RetryRepositoryImport, Task submission and Worker dispatch use the persisted SHA and never re-resolve the requested branch or tag.
- Snapshot expiry after RepositoryImport acceptance does not invalidate or change the accepted SHA.
- If the pinned object cannot be fetched after force-push, deletion or provider retention, import enters `setup_failed`; it never falls forward to the ref's new target.
- Following the latest ref requires an explicit new RepositorySelectionSnapshot and a separately authorized refresh/import action.

## Consequences

- A confirmed bootstrap has a reproducible source baseline and a stable audit trail.
- One read-only provider round trip is added before confirmation, but no distributed transaction is introduced.
- The snapshot signer/key and canonicalization revision become security-sensitive platform configuration.
- The UI should display requested ref, short SHA and observed time so users understand what they are confirming.

## Alternatives

- Resolve at first importer attempt: rejected because queue delay can change the code selected by the user.
- Resolve at Task submission or Worker dispatch: rejected because it breaks WorkspaceRevision and TaskSpec reproducibility.
- Trust a client-supplied SHA: rejected because it lacks server-observed repository/ref evidence and can cross identity boundaries.
- Always follow remote latest: rejected because retries and replays would not be deterministic.
