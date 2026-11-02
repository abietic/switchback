# Full-Fidelity Trace JSONL For P0

P0 Trace will preserve fidelity close to a normal Coding Agent local session JSONL, and redaction is not a blocking requirement for the first execution loop. Trace defaults to durable retention under Tenant/Workspace Membership and ACL with no automatic physical deletion. ADR 0029 defines hot/warm/cold lifecycle, TraceManifest, incremental analytics, quarantine and lightweight explicit deletion. This favors replay, debugging, Evolution evidence quality and effect tracking while retaining a path for correction and access revocation.

## Status

Accepted as draft baseline.
