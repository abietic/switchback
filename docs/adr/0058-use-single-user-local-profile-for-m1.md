# ADR 0058: Use A Single-User Local Profile For M1

- Status: Accepted
- Date: 2026-07-15

## Context

The M1 objective is to prove one real end-to-end Worker path: repository import, isolated Docker runtime, ACP-controlled Codex through LiteLLM, Trace/SSE and report/patch delivery. Company SSO, browser sessions, multi-user Membership/RBAC, GitHub App installation management and delegated Worker credentials add substantial integration and security work without reducing the main runtime uncertainty.

The tracer bullet still needs stable actor attribution and one real private-repository read path. Removing identity fields entirely would force later contract and migration churn, while injecting a repository token into the Worker would expand the first slice without being necessary because RepositoryImport already materializes the WorkingCopy on the host.

## Decision

- M1 uses `SingleUserLocalProfile`. Server configuration supplies one stable tenant id and user/actor id; clients cannot submit or override them.
- Startup bootstraps the local owner context through the existing ports. TaskSpec, Task, Trace, Artifact and Workspace retain normal tenant/user fields.
- Frontend has no login page. The server listens on loopback by default and refuses non-loopback exposure unless a later authenticated profile is explicitly configured.
- SSO/OIDC, user sessions, multi-user login, dynamic Tenant/Workspace Membership, RBAC administration and revocation propagation are post-tracer-bullet work.
- M1 configures one GitHub repository allowlist entry and one host-side read-only credential from an environment variable or mounted secret.
- The trusted host Repository resolver/Importer uses that credential to resolve RepositorySelectionSnapshot and fetch the pinned commit. The secret is not persisted in Task, Trace, Artifact or Workspace metadata.
- Worker containers receive only their materialized isolated WorkingCopy. They receive no GitHub token, credential helper, refresh secret or remote Git/`gh` capability in M1.
- GitHub App, CredentialBroker, dynamic RepositoryAccessGrant, RepositoryCredentialLease and managed B2 remain accepted later capabilities, not dependencies of the tracer bullet.

## Consequences

- M1 validates the highest-risk Agent runtime path without first implementing enterprise IAM.
- Single-user mode is not a production multi-user security profile; loopback binding is part of its correctness boundary.
- The identity and repository authorization ports remain replaceable, so later SSO and GitHub App adapters do not change domain identities or event schemas.
- Worker cannot fetch new remote refs by itself in M1. Repository refresh must remain a host-side explicit operation.

## Alternatives

- Company SSO plus GitHub App installation tokens in M1: deferred because it adds integration work before the Worker path is proven.
- Per-user GitHub App user tokens: deferred because user authorization, refresh, SSO and revocation are unnecessary for one local actor.
- Inject a PAT into Worker: rejected for M1 because the Worker does not need remote access after host import.
- Remove tenant/user identity fields: rejected because it creates avoidable protocol and migration churn later.
