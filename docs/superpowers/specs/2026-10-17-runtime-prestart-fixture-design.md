# Runtime preStart fixture fidelity

This continues the already approved Create → exact-CID quota check → Start boundary. The production Docker adapter already calls the per-call check after Create and before Start. The shared fake and successful integration fixtures currently bypass it, so they cannot prove this boundary or faithfully reproduce activation races.

Use the existing RuntimePreStartCheck interface. A successful fake invocation must present the synthetic created CID and exact runtime/session/generation, invoke the supplied check exactly once before reporting StartedAt or opening a control stream, propagate rejection/cancellation, and preserve its created partial identity. A configured pre-Create failure must retain its existing error and never call the check. Callbacks run outside the fake mutex. No opt-out flag or default allow checker is added. Existing deliberately failing/interrupting fixtures retain their distinct semantics.

Success fixtures publish their already explicit synthetic created-CID observation before the check, then start the owned simulated control client only if the check succeeds. Synthetic proof remains visibly synthetic. Real initial authorizer, PostgreSQL, journal and Hub are not replaced to force tests green. Any new rejection must be diagnosed rather than weakening production authority. A deterministic real PG/Hub activation reproduction is a separate task after this boundary is faithful.

Rejected alternatives: keeping bypasses and adding another test-only mock would preserve false positives; changing production Activate CAS without a failing real seam could admit ownership drift. This design adds neither capacity permission nor Engine startup authority.

Candidate remains the existing dirty worktree/HEAD. No Git writes, VM/Engine/actual DB/key/paid-provider actions or edits to older sealed evidence. A fresh disposable local PostgreSQL may be used only for isolated test schemas, on a private Unix socket with no TCP listener; Root owns startup/stop. Product modifications are limited to the fake adapter and relevant tests/fixtures, with baseline snapshots and precise diffs. OpenSpec 6.5/8.4/9.3 stay open.
