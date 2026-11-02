## Context

The loopback Assistant previously sent `temperature=0` from the LiteLLM client
to the Kimi Code OpenAI endpoint. Historical revision 1 corrected that request
to profile-owned `temperature=1`, but it did not provide provider-enforced
structure. Kimi Code responses can therefore still fail strict
`assistant.decision.v1` decoding before semantic validation, or fail the
existing semantic rules afterwards.

Revision 2 evolves the same immutable `kimi-code-assistant-openai-v1` identity
to include strict provider structured output. It is a contract amendment only:
runtime implementation, real-provider evidence, synchronization, and archive
remain pending Tasks 21–23 and a later explicit user approval.

This change applies only to the loopback `single_user_local_v1` product. It
uses the existing mounted Kimi Code key, the existing `kimi-for-coding` model,
and `https://api.kimi.com/coding/v1`; it adds neither a Kimi Platform surface
nor another credential path.

## Goals / Non-Goals

**Goals:**

- Make the existing Kimi Code key usable by the local Assistant.
- Bind `temperature=1`, deployment scope, and strict structured output to the
  exact profile revision and digest recorded by every new AssistantRun.
- Send exact OpenAI-compatible `response_format.type=json_schema`,
  `json_schema.strict=true`, a stable schema name, and one repository-owned
  `assistant.decision.v1` provider schema for Kimi revision 2.
- Keep provider schema enforcement as defense in depth; strict decoding and
  existing readiness, TaskDraft, and domain validators remain authoritative.
- Preserve typed `assistant.decision.v1` validation, historical Run identity,
  and fail-closed provider failure semantics.
- Provide automated and real-provider evidence for the complete
  Assistant-to-Worker acceptance flow.

**Non-Goals:**

- Kimi Platform credentials, endpoints (including `api.moonshot.cn`), billing
  semantics, or fallback.
- Multi-user, remote, team, or externally hosted profiles.
- Arbitrary provider JSON, header injection, request-policy coercion, or Worker
  AgentProfile changes.
- Rewriting historical Run identity, revision, or digest.

## Decisions

1. Keep `kimi-code-assistant-openai-v1`; do not redefine or rewrite historical
   revision-1 Run refs. New local Runs will select revision 2 only after the
   implementation is complete.
2. Put optional typed request policy and deployment scope inside the profile
   digest. Revision 2 adds a typed `strict_json_schema` structured-output
   policy to the existing Kimi temperature policy.
3. Bind the policy to response schema `assistant.decision.v1`, provider schema
   ID `assistant-decision-strict-json-schema-v1`, and a `sha256:<lowercase hex>`
   digest over canonical minified UTF-8 schema bytes.
4. The dedicated provider schema is not the public API schema. Every reachable
   generated object is closed with `additionalProperties: false`, all declared
   properties are required, and `checkpoint` is exactly `null`. The server-side
   ClarificationPlanner remains the only owner that creates a checkpoint.
5. Map only this typed policy to the fixed OpenAI-compatible strict
   `response_format`; arbitrary schema JSON, provider parameters, headers, or
   passthrough maps are not representable. DeepSeek declares neither
   temperature nor structured output and omits both request fields.
6. Enforce `single_user_local_v1` before serving traffic. Keep the existing
   Kimi Code key file and endpoint; add no Kimi Platform surface.
7. Preserve old Run refs. A current revision-2 client claiming an exact
   revision-1 Run fails with `ErrModelPolicyConflict`; explicit Retry creates a
   new Run under the current revision instead of rebinding history.
8. Provider schema rejection, non-2xx, malformed output, and semantic failure
   record the existing sanitized failure for the same exact profile. No hidden
   retry, fallback, response-format removal, permissive parse, or lenient
   decode is allowed.
9. Require implementation, real-provider evidence, and complete product
   acceptance before closeout. This artifact does not claim those results.

The historical fixed profile is revision `1`, with LiteLLM alias
`switchback-assistant-kimi-code`, response schema `assistant.decision.v1`,
deployment scope `single_user_local_v1`, temperature `1`, and no automatic
fallback. Revision `2` retains that identity and adds the typed strict
structured-output policy above. The old `kimi-assistant-openai-v1` remains
readable only through historical Run references and is not selectable by new
local configuration.

`AssistantModelProfile` receives optional typed `RequestPolicy` and
`DeploymentScope` fields with `omitempty`, so the existing DeepSeek digest is
stable and its request omits both `temperature` and `response_format`. The
policy is closed: optional `temperature` is finite and within `0` to `2`, and
optional structured output supports only `strict_json_schema` bound to the
profile response schema and fixed provider schema ID/digest. Unknown provider
parameters, caller-owned raw schema JSON, fallback, and stale policy identity
are invalid. The profile's request policy and deployment scope participate in
the digest.

`localproduct.Config.Validate` compares a selected scoped profile with
`configuration.Profile.Name` before an Assistant runner, listener, or provider
client is created. The registry validates its fixed profile, configuration
validates scope, and the LiteLLM client validates digest and serializes only the
selected typed request policy. These three independent checks make the request
boundary fail closed.

Existing Runs retain their original profile refs. A queued exact revision-1 Run
is never rebound; when a revision-2 client claims one, it returns the existing
`ErrModelPolicyConflict`, commits the established visible failure path, and an
explicit user Retry creates a new Run under the current revision. Provider
schema rejection, timeout, non-2xx, malformed decision, or semantic failure
remains recorded for the selected profile and never retries, removes strict
output, switches providers, or uses a lenient decoder.

### Rejected alternatives

- **In-place profile mutation:** rejected because changing the old identity
  would make old Run refs ambiguous and would silently alter historical
  authority.
- **LiteLLM-only parameter override:** rejected because LiteLLM would become a
  hidden policy owner and the exact parameter would not be profile-digest-bound
  or auditable in the AssistantRun.
- **Reuse the public decision schema:** rejected because its nested objects are
  intentionally open for public-documentation compatibility and are not an
  acceptable strict provider projection.
- **Trust provider schema enforcement as validation:** rejected because provider
  enforcement cannot replace strict decoding or domain semantic authority.

## Risks / Trade-offs

- **A scoped profile is selected outside the local product** → validate before
  network-serving components initialize and fail startup.
- **A request policy is malformed, stale, or unsafe** → validate finite range,
  structured-output schema ID/digest, profile digest, and empty fallback at
  registry/client construction before HTTP.
- **Kimi Code provider or output contract fails** → retain the selected-profile
  sanitized failure and require an explicit retry; never fallback, hidden
  replay, response-format removal, or lenient decode.
- **Credential disclosure through new configuration** → keep the existing key
  file mounted only into Assistant LiteLLM; exclude it from Backend config,
  profile JSON, Trace, Frontier state, logs, and status evidence.
- **Automated tests hide live-provider incompatibility** → require the focused
  real-provider probe and the complete local Compose product flow before
  closeout.

## Migration Plan

1. Freeze this revision-2 contract without changing completed historical tasks.
2. Implement the closed provider-schema owner and immutable revision-2 profile.
3. Serialize the exact strict request and prove pre-HTTP policy fencing.
4. Rebuild and prove the focused real-provider and complete product flows.
5. Present synchronization impact only after Task 23 evidence. Synchronize or
   archive only after a new explicit user confirmation; rollback is a Git
   revert plus image rebuild and never rewrites existing Runs, credentials, or
   provider observations.

## Open Questions

None. Tasks 21–22 intentionally leave runtime behavior absent at this revision
of the contract. Task 19 synchronization/archive remains an explicit,
user-confirmed action after Task 23 evidence exists.
