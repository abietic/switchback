# Change: Support Kimi Code Assistant profile

## Why

Revision 1 fixed the loopback Assistant's client-owned `temperature=0` request:
Kimi Code accepts only `temperature=1`. That revision did not, however, give
the provider an enforceable output structure. Kimi Code responses can still
fail the strict `assistant.decision.v1` decoder or later semantic checks.

Revision 2 must bind provider-enforced strict structured output to the same
immutable Kimi Code profile ID. Request policy must remain server-owned,
digest-bound, auditable through the Assistant profile, and fail closed. This
change describes the required contract; deployment and acceptance remain
pending Tasks 21–23.

## What changes

- Evolve `kimi-code-assistant-openai-v1` from historical revision 1 to
  revision 2 without rewriting historical Run references
- Bind typed strict `assistant.decision.v1` JSON Schema output to the exact
  profile digest alongside `temperature=1`
- Own one dedicated provider-schema identity,
  `assistant-decision-strict-json-schema-v1`, and SHA-256 canonical-schema
  digest; provider configuration cannot supply arbitrary JSON Schema
- Require a fully closed provider object graph and `checkpoint=null`; the
  server planner remains the only checkpoint owner
- Send only exact profile-owned LiteLLM parameters, including strict
  `response_format.json_schema` for Kimi revision 2
- Retire the old Kimi Assistant profile from new local configuration
- Preserve historical Run identity and fail closed without retry, fallback, or
  lenient decoding
- Retain strict decoder and domain/readiness/TaskDraft validators as the
  authoritative post-provider boundary
- Prove the implementation and full real Assistant-to-Worker acceptance flow
  only in Tasks 21–23

## Capabilities

### Modified

- `assistant-conversation-dispatch`

## Impact

Configuration, contracts, Interaction LiteLLM adapter, private Assistant
LiteLLM image, local Compose, acceptance tooling, operator documentation, and
the active production Web closeout evidence change. Kimi Platform credentials,
endpoints (including `api.moonshot.cn`), fallback, arbitrary provider JSON, and
multi-user enablement remain unsupported.
