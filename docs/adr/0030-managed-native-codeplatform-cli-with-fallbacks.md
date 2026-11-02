# Managed Native CodePlatform CLI With Fallbacks

P0 accepts managed B2 for CodePlatform access. Worker images include native Git, GitHub CLI and approved Skills. After a repository-scoped once or WorkerSession RepositoryAccessGrant is active, Agent may directly perform any non-administrative GitHub operation allowed by its short-lived Token without per-command approval. Switchback owns identity binding, capability resolution, CredentialBroker mint/refresh/revoke, network policy, known-secret redaction and CodePlatformOperation tracing; it does not proxy every Git or GitHub command.

The trust boundary is explicit: short-lived access tokens are available to the Agent and are not treated as secret from the sandbox. GitHub App private keys, OAuth refresh tokens and long-lived PAT/root credentials remain only in the control plane. P0 profiles permit repository read, repository contents write, pull-request write and CI read as separately granted capabilities, while administration, secrets, variables, workflow/Actions write, ruleset management and repository deletion remain unavailable. Provider rulesets protect default/protected branches and force/delete behavior.

Git uses a path-aware `git-credential-switchback`; GitHub CLI runs through a managed wrapper that obtains a current RepositoryCredentialLease and execs native `gh` without persistent `gh auth login`. Token lifetime is independent from the up-to-14-day WorkerSession and renews only while Membership, Grant, Session and policy remain valid. Platform-issued secrets are deterministically removed before Trace/Langfuse/Artifact persistence even though general Trace redaction is not a P0 blocker.

## Status

Accepted as P0 design baseline.

## Considered Alternatives

- **A, full control-plane execution:** strongest secret isolation, branch constraints and operation idempotency, but weakest native Agent flexibility. Retain as fallback when policy forbids Token use in the sandbox or direct operations repeatedly fail governance/recovery requirements.
- **C, direct read plus controlled write:** preserves fetch/submodule/LFS flexibility while keeping write tokens outside Worker. Retain as fallback when Provider permissions cannot safely contain direct writes but full A is unnecessarily restrictive.

## Consequences

- Worker behaves like a normal Coding Agent for GitHub instead of learning a parallel Switchback command surface.
- RepositoryAccessGrant becomes a period of delegated authority rather than a single remote operation approval.
- Provider token scope and branch rules are mandatory controls; prompts and Skills are not authorization boundaries.
- GitHub-specific native CLI is the P0 path; GitLab adds managed `glab`/credential support under the same contracts rather than forcing one lowest-common-denominator command API.
- P0 still guarantees report/patch only; branch, PR and CI-read outputs are traced external references available when the task and Grant use them.
