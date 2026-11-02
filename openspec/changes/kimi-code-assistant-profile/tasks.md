## 15. Immutable Assistant profile and request policy

- [x] 15.1 Add `kimi-code-assistant-openai-v1` at revision `1` with the typed,
  digest-bound request policy, `single_user_local_v1` scope,
  `switchback-assistant-kimi-code` alias, `temperature=1`, and no fallback;
  preserve historical profile refs and fail closed on model-policy conflicts.

## 16. Local Kimi Code deployment configuration

- [x] 16.1 Select the new profile only for the local product, serialize only
  profile-declared LiteLLM parameters, retain the existing Kimi Code key file,
  `kimi-for-coding`, and `https://api.kimi.com/coding/v1`, and reject Kimi
  Platform endpoint, key, or fallback configuration before traffic can start.

## 17. Automated contract and repository gates

- [x] 17.1 Add registry, digest, request-payload, scope, historical-Run,
  interaction, architecture, and secret/forbidden-surface tests proving exact
  Kimi `temperature=1`, DeepSeek omission, typed-decision failure handling, and
  no Kimi Platform surface; run focused tests and `make verify` with a fresh
  PostgreSQL test database.

## 18. Real-provider focused acceptance

- [x] 18.1 Rebuild the Assistant LiteLLM and Backend, then use the existing
  non-empty mounted Kimi Code key to prove one valid `assistant.decision.v1`
  response through `kimi-code-assistant-openai-v1` without exposing key,
  headers, environment, or unrestricted provider bodies.

## 19. Complete product acceptance and confirmed closeout

- [ ] 19.1 Prove the local Compose clarification, user answer, ready draft,
  exact confirmation, Task creation, Worker delivery, Artifact/Task navigation,
  and final Home projection flow; keep `web-demo/` at zero task diff and keep
  Production Web unit tests, production build, and Playwright gates passing;
  this task is blocked on Task 23 revision-2 evidence. Only after that evidence
  exists and a new explicit user confirmation may it present delta
  synchronization impact, synchronize this change, rerun OpenSpec strict
  validation, complete the corresponding `production-web-core-experience`
  closeout task when all exact evidence exists, or archive.

## 20. Revision-2 OpenSpec contract freeze

- [x] 20.1 Amend only the active Kimi Code OpenSpec artifacts to preserve
  completed Tasks 15–18 and historical revision-1 truth while specifying
  revision 2: same profile ID, typed digest-bound strict
  `assistant.decision.v1` output, dedicated
  `assistant-decision-strict-json-schema-v1` canonical digest, fully closed
  provider objects, `checkpoint=null`, authoritative server validators, and no
  Kimi Platform, fallback, hidden retry, or lenient decode. Strictly validate
  this intentionally RED runtime contract.

## 21. Closed provider schema and immutable Kimi revision 2

- [x] 21.1 Reconcile current decision/readiness/TaskDraft types with the
  approved provider projection; use `go:embed` to own canonical minified schema
  bytes, fixed ID/digest, and defensive-copy access; prove every generated
  object is closed, checkpoint is null, valid representative decisions also
  strictly decode, and invalid nested/semantic-shape cases fail.
- [x] 21.2 Extend profile cloning, digest calculation, validation, and local
  scope fencing so new Kimi Code Runs resolve to revision `2` with
  `temperature=1` plus typed `strict_json_schema` policy bound only to the
  contract owner; preserve DeepSeek's exact no-temperature/no-structured-output
  behavior and historical revision-1 model-policy fencing.

## 22. Exact strict LiteLLM serialization and failure fencing

- [x] 22.1 Serialize the validated Kimi revision-2 policy as the exact
  OpenAI-compatible `response_format.json_schema` request with stable
  `assistant_decision_v1` name, `strict=true`, and the repository-owned schema;
  reject schema mode/identity/digest and historical profile mismatches before
  HTTP, while DeepSeek sends neither `temperature` nor `response_format`.
- [x] 22.2 Prove exact payload keys, one-request sanitized failures for schema
  rejection/non-2xx/malformed/domain-invalid output, and preservation of the
  bounded envelope decoder, `DecodeAssistantDecision`, and existing domain
  validators without retry, fallback, response-format removal, or lenient
  decode; preserve a historical revision-1 failed Run while an explicit Retry
  creates a new Run from the current server-owned revision-2 profile and
  converges on the original retry result for the same idempotency key.

## 23. Revision-2 product evidence and closeout boundary

- [ ] 23.1 Review and complete the existing uncommitted real-provider harness
  inputs without weakening canonical direct summary assertions; run focused
  contract/runtime gates, rebuild the exact local runtime, and prove the
  revision-2 Kimi Code Assistant-to-Worker flow with sanitized real-provider
  evidence. Preserve user-owned `web-demo/` changes and record only evidence
  justified by passing gates.
- [ ] 23.2 Run repository and Web gates, prove privacy/unsupported-surface and
  worktree boundaries, update status/progress evidence only after all matching
  gates pass, and leave Task 19 synchronization/archive unchecked pending a new
  explicit user approval.
