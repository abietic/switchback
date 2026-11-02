# Explicit Workspace Membership And Separate External Authorization

P0 treats Tenant as a hard security boundary: every Workspace belongs to exactly one Tenant and cross-Tenant WorkspaceMembership is forbidden. Access requires both an active TenantMembership and explicit WorkspaceMembership. Workspace roles are composable `viewer`, `editor`, `reviewer`, and `owner`; Workspace must retain at least one owner. Project or team association does not silently grant access.

Workspace roles authorize only Switchback actions. CodePlatform effects such as private clone, branch push or PR creation additionally require resolved Capability policy, RepositoryAccessGrant, RepositoryCredentialLease and provider acceptance; ADR 0030 allows native commands without repeated prompts while a matching session Grant remains active. Feishu mutation, production access and other external effects use their corresponding ApprovalGrant and SecretLease. Worker always acts on behalf of the initiating user through a short-lived ExecutionAuthorizationLease and never becomes a platform superuser.

Authorization is rechecked for commands, protected reads, Apply, Artifact actions, lease renewal, secret issuance, and external operations. Membership or policy revocation fences new work, revokes execution/secret leases, and preserves the WorkerSession and evidence in an access-revoked waiting state. Tenant administrators do not implicitly read all Workspace content; audited break-glass access is a separate future design.

## Status

Accepted as P0 design baseline.

## Consequences

- Collaboration is explicit and auditable instead of depending on hidden Project/Team inheritance.
- Role checks and external credentials cannot accidentally substitute for each other.
- Identity & Access must provide one shared authorization API and revisioned decisions to Gateway, Frontier, Delivery, Worker, Evolution, and Skilling.
- Long-running Worker sessions need short authorization leases and revocation handling rather than a creation-time permission snapshot.
- Cross-Tenant collaboration requires a future audited copy/export/import flow, not shared object identities.
