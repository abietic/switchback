# M2 Assistant Task Dispatch Requirement Audit

## Audit Method

This audit maps every scenario in the three M2 delta specifications to an
executable test or acceptance path. The source set contains 49 scenarios:

| Delta specification | Scenarios |
| --- | ---: |
| `assistant-conversation-dispatch` | 28 |
| `assistant-web-interaction` | 17 |
| `task-session-persistence` | 4 |
| **Total** | **49** |

Evidence classes are unit, PostgreSQL/Gateway/Frontier integration, Web
Vitest/Playwright, `make failure-acceptance`, and
`make m2-real-provider-acceptance`.

## Assistant Conversation Dispatch

| Scenario | Evidence |
| --- | --- |
| User sends a new natural-language message | `TestSendConversationMessageIsModelFreeAndIdempotent`; deterministic fake-Worker E2E |
| Message acceptance response is lost | message idempotency integration; Gateway pending retry |
| Client attempts to override identity | `TestGatewayRejectsAssistantIdentityOverrideFields` |
| User sends another message while a run is active | Conversation serialization and queue integration |
| Backend restarts with queued work | queue recovery integration; Failure gate |
| ContextPack is built for a resolved Workspace | deterministic ContextCompiler selection test |
| Conversation has no resolved Workspace | protected-context exclusion test |
| Context exceeds the server budget | budget drop and stable digest tests |
| Model proposes a valid clarification | typed LiteLLM adapter test; Real gate |
| Model output is malformed or conflicts with policy | adapter/processor fail-closed tests; Failure gate |
| Selected provider fails | deadline/profile tests; Failure gate |
| Request has an untestable outcome | hard readiness-dimension test |
| A reversible low-risk detail is absent | allowed-default test |
| User asks to bypass a hard blocker | hard-dimension and explicit Workspace tests |
| One blocker remains | highest-priority clarification test |
| Three questions have not reached readiness | checkpoint-on-third-question test |
| User continues from a checkpoint | reset/fencing and PostgreSQL command tests |
| Assistant reaches ready state | processor ready-draft test; deterministic and Real gates |
| User edits intent after a draft is ready | domain draft invalidation test |
| User agrees through unbound free text | exact confirmation tests; no authority before command |
| User confirms the current ready draft | confirmation unit/integration; deterministic and Real gates |
| Confirmation target is stale | stale-binding test; Failure gate |
| Workspace is inferred rather than selected | explicit Workspace test; admission rejection |
| Task is admitted | atomic confirmation integration; deterministic and Real gates |
| Task admission rejects current evidence | rollback and stale-intent tests |
| Ready draft is confirmed | checksum/manifest test; Real artifact provenance |
| Evidence contains hidden or secret material | forbidden-field/known-token tests; secret scan |
| ObjectStore or Langfuse is unavailable | derived-effect degradation tests; Failure gate |

## Assistant Web Interaction

| Scenario | Evidence |
| --- | --- |
| Valid message command is submitted | Gateway revisioned command and Web client tests |
| Command target is stale | Gateway stale mapping; Web stale confirmation |
| Command is retried | Gateway original-result retry; stable Web idempotency key |
| Browser opens an Assistant Conversation | Web snapshot contract; composed Backend integration |
| Browser connects after snapshot | Frontier reconnect and Assistant SSE delta tests |
| Cursor cannot be replayed | Frontier resync test; Failure gate |
| Assistant needs one clarification | Assistant view test; Playwright Assistant flow |
| Clarification checkpoint is active | projection-owned checkpoint action test |
| TaskDraft is ready | human-readable draft and stale-binding tests |
| Confirmation is accepted before Task projection arrives | Playwright causal pending-state flow |
| Command response is lost | Web retry and projection convergence |
| Assistant-created Task starts | Playwright handoff; deterministic and Real gates |
| Worker later delivers report or patch | existing Worker flow; Real report/patch assertions |
| Model or evidence processing fails | sanitized Gateway failure; runtime validation; Failure gate |
| Model emits reasoning fields | evidence forbidden-field tests; visible projection allowlist |
| Assistant route is disabled or degraded | config tests and direct Task route fallback |
| Direct and Assistant paths submit equivalent TaskSpec | direct admission and Assistant confirmation tests |

## Task Session Persistence

| Scenario | Evidence |
| --- | --- |
| Valid direct TaskSpec is submitted | direct admission and existing Task create integration |
| Valid Assistant TaskDraft is confirmed | atomic and idempotent Assistant confirmation integration |
| Assistant confirmation or delivery is duplicated | confirmation/task/projection idempotency; Failure gate |
| Workspace or TaskSpec evidence is missing | direct blocker and stale Assistant evidence tests |

## Cross-Cutting Acceptance Paths

The deterministic E2E
`TestAssistantClarificationConfirmsTaskAndDispatchesFakeWorker` proves:

```text
ambiguous message
  -> clarification
  -> exact follow-up
  -> ready TaskDraft
  -> exact confirmation
  -> Task admission
  -> fake Worker dispatch
```

`scripts/run-m2-real-provider-acceptance` proves the same authority path through
one explicit real provider and a real Codex Worker. It asserts report/patch
checksums, provenance, fixture immutability, and known-token absence without
printing user/model content.

`scripts/run-failure-acceptance` combines the Assistant-specific matrix with
the existing Worker failure matrix in one ignored status-only summary.

## Closeout Gate

| Gate | Required result | Last closeout evidence |
| --- | --- | --- |
| Go model/Interaction tests | Pass | 2026-07-25: all requested packages passed |
| LiteLLM image version | `1.93.0` | 2026-07-25: container metadata returned `1.93.0` |
| `make verify` | Pass | 2026-07-25: format/migration/protocol/image/unit/race/vet/integration/secret/OpenSpec passed |
| Web Vitest | Pass | 2026-07-25: 10 files, 33 tests passed |
| Web build | Pass | 2026-07-25: TypeScript checks and Vite production build passed |
| Web Playwright | Pass | 2026-07-25: 6 Chromium desktop/mobile tests passed |
| OpenSpec strict validation | Pass | 2026-07-25: 5 items passed, 0 failed |
| Compose health and migration | Healthy / migration exit 0 | 2026-07-25: Backend/Web ok, sidecar healthy with no host port, schema `000010` |
| Failure acceptance | `outcome: passed` | `runtime-data/failure-acceptance/run-20260724T211516Z/failure-acceptance-summary.json`, 8 results |
| Real provider and Worker | `outcome: passed` | `runtime-data/m2-real-provider/run-20260724T211656Z/m2-real-provider-summary.json`, DeepSeek, Task delivered, Run/Turn completed, patch+report |
| Secret scan and fixture immutability | Pass | 2026-07-25: repository scan passed; real summary flags both checks true |

## Deferred Boundaries

The audit does not claim:

- multi-user login, SSO, dynamic membership, or production authorization;
- cross-provider automatic fallback;
- Evolution Truth or Inspiration retrieval in ContextPack;
- multi-Workspace TaskDrafts;
- Assistant tool execution or direct WorkingCopy access;
- production Kubernetes, remote Git writes, branch publication, or PR creation;
- streaming provider reasoning or raw provider payload retention.
