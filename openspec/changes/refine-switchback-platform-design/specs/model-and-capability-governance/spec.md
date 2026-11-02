## ADDED Requirements

### Requirement: LLMProxy attaches governance metadata
The system SHALL attach tenant, user, task, conversation, worker run, trace, Langfuse observation, purpose, route policy, budget, and safety metadata to model requests.

#### Scenario: Worker agent model call
- **WHEN** Worker proxies a Coding Agent model call
- **THEN** LLMProxy records ModelInvocation metadata with task id, worker run id, trace id, Langfuse trace/span/generation id, route policy, usage, and error classification

### Requirement: LLMProxy supports routing and degradation
The system SHALL use LiteLLM in P0 to route model requests by Switchback policy and support controlled fallback when a provider fails or budget is exceeded, without exposing LiteLLM-specific fields in domain contracts.

#### Scenario: Provider failure
- **WHEN** the selected provider fails with a retryable error
- **THEN** LLMProxy applies route policy retry or fallback and records the decision

### Requirement: Skilling governs capability manifests
The system SHALL manage Skill, MCP server, and AgentProfile manifests with version, scope, permissions, dependencies, owner, and review status.

#### Scenario: Codex agent profile
- **WHEN** the P0 CodexAgentProfile is enabled
- **THEN** Skilling records its adapter type, runtime image requirement, permissions, version, owner, review status, and allowed scope

#### Scenario: Unauthorized skill
- **WHEN** TaskSpec references a Skill outside the user or project authorization scope
- **THEN** the system rejects the TaskSpec or removes the Skill before Worker runtime injection

### Requirement: Skilling resolves authoritative Tool policy
The system SHALL treat ToolCapabilityDeclaration and Adapter evidence as inputs, SHALL use versioned ToolGovernancePolicy to create an immutable ResolvedToolPolicy, and SHALL permit TaskSpec, tenant policy, ApprovalGrant, and runtime constraints only to narrow that result.

#### Scenario: Tool metadata is valid
- **WHEN** a versioned Tool declaration and trusted Adapter evidence agree and satisfy the active ToolGovernancePolicy
- **THEN** Skilling emits a ResolvedToolPolicy with declaration, evidence, policy revision, scope, content hash, resolved effect/cancellation capabilities, and allowed retry/reconciliation actions

#### Scenario: Tool metadata is missing or conflicting
- **WHEN** declaration or evidence is missing, untrusted, stale, conflicting, or cannot be verified
- **THEN** Skilling resolves effect class to `unknown`, unverifiable cancellation to `unsupported`, disables automatic retry, and records the reasons without allowing Agent/model text to lower risk

#### Scenario: Runtime evidence raises risk
- **WHEN** Worker observes trusted evidence that one invocation is riskier than its ResolvedToolPolicy snapshot
- **THEN** Worker escalates that invocation and records evidence without lowering risk, expanding permission, or mutating the policy snapshot

### Requirement: Worker receives least-privilege capabilities
The system SHALL inject only approved capabilities into Worker runtime based on TaskSpec, CapabilityBundle, and policy.

#### Scenario: MCP server requires secret access
- **WHEN** a selected MCP server requires a secret
- **THEN** Worker receives only a short-lived secret lease if approval policy and user permissions allow it

### Requirement: Capability changes are auditable
The system SHALL record enable, disable, upgrade, rollback, and permission changes for Skills, MCP servers, and AgentProfiles.

#### Scenario: Skill version upgrade
- **WHEN** an administrator upgrades a project Skill version
- **THEN** Skilling records old version, new version, actor, timestamp, scope, and rollback reference

### Requirement: Skill installation requires source-aware authorization
The system SHALL allow preinstalled Skills without a separate user installation prompt, require human content/security publication review for Evolution-generated or modified Skills, allow reviewed Evolution Skills within their approved scope without a duplicate installation prompt, and require a CapabilityInstallGrant for other Skill sources.

#### Scenario: Evolution generates a Skill revision
- **WHEN** Evolution proposes a new SkillPackage or modifies an existing Skill revision
- **THEN** Skilling records an immutable revision and blocks publication and runtime loading until a human reviewer approves content, permissions, dependencies, and allowed scope

#### Scenario: User asks Assistant to install a Skill
- **WHEN** a user explicitly instructs Assistant to install a marketplace Skill and confirms its permissions
- **THEN** Skilling records the Assistant Turn as CapabilityInstallGrant evidence and performs the audited installation

#### Scenario: Worker discovers an unapproved Skill
- **WHEN** Worker wants to install a marketplace or user-repository Skill without a valid grant
- **THEN** Skilling rejects installation and returns a structured approval request

### Requirement: CodePlatform capability is delegated by repository-scoped Grant
The system SHALL use RepositoryAccessGrant to delegate repository read, repository write, pull-request write and CI read independently, SHALL support once or WorkerSession scope, and SHALL NOT infer external provider authority from WorkspaceRole, TaskSpec, Skill or Agent prompt.

#### Scenario: WorkerSession write Grant is active
- **WHEN** a user or organization policy grants `repository.write` to one WorkerSession for one Repository with compatible constraints and expiry
- **THEN** managed B2 permits subsequent non-administrative native GitHub operations in that scope without per-command approval

#### Scenario: Agent requests a management capability
- **WHEN** Agent requests administration, secret/variable access, workflow or Actions write, ruleset management, repository delete or organization management
- **THEN** P0 capability resolution fails closed and does not mint a broader provider Token

### Requirement: CredentialBroker separates short Agent access from root credentials
The system SHALL keep provider private keys, OAuth refresh tokens and long-lived PAT/root credentials in CredentialBroker, SHALL issue short RepositoryCredentialLease only while Membership, Grant, Session, runtime generation and policy remain valid, and SHALL accept that the short access token is available to Agent inside managed B2.

#### Scenario: Repository Token expires during a long WorkerSession
- **WHEN** native Git or GitHub CLI requests a credential after the prior lease expires
- **THEN** a path-aware helper or managed wrapper revalidates the live Grant and obtains a new short lease without returning root or refresh credentials to Worker

#### Scenario: Grant or Membership is revoked
- **WHEN** a newer revoked Grant, Membership or policy revision becomes authoritative
- **THEN** CredentialBroker stops minting or refreshing access, revokes the active provider Token when supported, and Worker fences new CodePlatform operations

### Requirement: Platform-known credentials are removed before persistence
The system SHALL deterministically remove platform-issued or known Token values before Trace, Langfuse, Artifact, user progress or persistent CLI output is stored, independently of later generic content-DLP work.

#### Scenario: Agent prints the current repository Token
- **WHEN** Agent or a child process emits a known Token value
- **THEN** ingest replaces the value, marks the record `secret_redacted`, emits a security fact and does not preserve an ordinary raw unredacted copy
