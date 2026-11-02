# Host WorkingCopy Storage And Atomic Revision Pointer

P0 stores every WorkerSession WorkingCopy and durable Agent state in host-managed persistent WorkspaceStorage, outside the disposable container layer. A RuntimeInstance bind mounts only its own WorkingCopy. Git content uses per-session worktrees and immutable commit references; non-repository content uses immutable manifests and checksums. PostgreSQL remains authoritative for WorkspaceRevision metadata, WorkspaceApply state, leases, and the active revision pointer.

WorkspaceApply prepares a complete immutable StagingRevision outside the write lease. It then acquires a short PostgreSQL WorkspaceWriteLease with a monotonic fencing token and, in one transaction, revalidates the expected active revision and token, inserts the new WorkspaceRevision, advances the active pointer, records the Apply result, and writes Outbox. Atomicity means one database pointer selects one complete multi-component manifest; it does not require an impossible atomic rename across repositories and directories.

## Status

Accepted as P0 design baseline.

## Consequences

- Large repository materialization and tests do not hold the Workspace writer lock.
- Orphan staging is non-authoritative and can be cleaned after failures or crashes.
- Expired Apply processes cannot commit after a newer fencing token is issued.
- The control plane must operate authoritative/staging paths; Worker containers never receive a shared authoritative write mount.
- Storage optimizations such as reflink are replaceable and cannot be correctness requirements.
