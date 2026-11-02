## MODIFIED Requirements

### Requirement: Assistant model output is an untrusted typed proposal
The system SHALL route one exact server-owned model profile through LiteLLM per
AssistantRun, SHALL derive provider request parameters only from that profile's
typed and digest-bound request policy, and SHALL validate a versioned typed
AssistantDecision before it can change Conversation, readiness, TaskDraft,
intent, Task, or Worker state.

#### Scenario: Profile owns the Kimi Code request
- **WHEN** a new local AssistantRun uses `kimi-code-assistant-openai-v1`
- **THEN** revision `2` sends exactly `switchback-assistant-kimi-code`, numeric
  `temperature=1`, `response_format.type=json_schema`, and
  `response_format.json_schema.strict=true` for `assistant.decision.v1` with
  the fixed provider schema ID and digest, and the Run records the exact profile
  revision and digest

#### Scenario: Structured-output policy is stale or mismatched
- **WHEN** a Kimi revision-2 structured-output schema ID or digest does not
  match the repository-owned `assistant-decision-strict-json-schema-v1`
  contract
- **THEN** profile/client validation fails before any provider HTTP request

#### Scenario: DeepSeek has no provider request policy
- **WHEN** a selected supported profile has no temperature policy
- **THEN** the Backend omits both `temperature` and `response_format` instead
  of applying a client default

#### Scenario: Provider rejects the schema or output is malformed
- **WHEN** the selected provider rejects the strict schema, times out, returns
  non-2xx, or returns malformed or contract-invalid decision content
- **THEN** the Run records a sanitized failure for the same profile and does
  not retry, remove `response_format`, use a lenient decoder, or fallback

#### Scenario: Historical Kimi revision one Run is claimed
- **WHEN** the current exact Kimi client claims an AssistantRun recorded with
  revision `1`
- **THEN** it fails closed with the model-policy conflict and an explicit Retry
  creates a new Run using current revision `2` without rewriting the old Run

#### Scenario: Provider cannot create a checkpoint
- **WHEN** Kimi revision 2 returns an `assistant.decision.v1` proposal
- **THEN** the dedicated fully closed provider schema requires `checkpoint` to
  be `null`, and only the server planner may create the governed checkpoint

## ADDED Requirements

### Requirement: Kimi Code Assistant is local-profile scoped
The system SHALL allow `kimi-code-assistant-openai-v1` only under
`single_user_local_v1`, using the mounted Kimi Code key and
`https://api.kimi.com/coding/v1`.

#### Scenario: Loopback local product selects Kimi Code
- **WHEN** the fixed local product loads the Kimi Code Assistant profile
- **THEN** startup accepts the profile without exposing the key in Backend
  configuration, profile JSON, Trace, or Frontier state

#### Scenario: Another deployment profile selects Kimi Code
- **WHEN** the Kimi Code Assistant profile scope differs from the active
  deployment profile
- **THEN** startup fails before the listener or provider client becomes active

#### Scenario: Configuration attempts Kimi Platform
- **WHEN** configuration introduces a Kimi Platform endpoint, key, or fallback
- **THEN** repository and configuration gates reject the unsupported surface
