## Why

M1 proves that a user can submit an explicit TaskSpec and complete real coding work through a persistent Docker-hosted Codex Worker, but the user still has to structure the request manually. The next smallest product step is a single-user Assistant loop that turns natural-language requests into an explicitly confirmed TaskSpec and dispatches the existing Worker path without moving execution ownership into Assistant.

## What Changes

- Add a durable single-user Web Conversation and AssistantTurn lifecycle with asynchronous message acceptance, server-owned model routing, bounded context, PostgreSQL state, Outbox events, and phase-completion JSONL evidence.
- Add versioned seven-dimension TaskReadiness assessment, one-question clarification, safe recorded assumptions, and a three-question ClarificationCheckpoint.
- Add revisioned TaskDraft projection and an explicit exact-draft confirmation action that creates an immutable task-creation intent.
- Reuse the existing Task Orchestration admission and M1 Worker allocation/runtime path after revalidating actor, Workspace, readiness, TaskSpec digest, policy revision, and idempotency.
- Add Gateway commands, Frontier snapshot/SSE projections, and a focused Assistant Web view for conversation, blockers, assumptions, draft preview, confirmation status, and the resulting Task/Worker link.
- Preserve the existing direct TaskSpec form/API as a supported zero-question path.
- Defer Delivery/Feishu, shared-channel participation, Conversation rollover, Evolution/OpenViking, Inspiration, Skill Market, multi-user authentication, and Runtime recovery to later changes.

## Capabilities

### New Capabilities

- `assistant-conversation-dispatch`: Durable Conversation and AssistantTurn processing, bounded context, readiness/clarification, TaskDraft revisions, explicit creation intent, TaskSpec emission, and Assistant evidence.
- `assistant-web-interaction`: Asynchronous Gateway message/confirmation commands plus Frontier projections and the single-user Assistant Web interaction surface.

### Modified Capabilities

- `task-session-persistence`: Admit an Assistant-produced TaskSpec only from an exact confirmed TaskDraft and active creation intent while preserving the existing transactional idempotency and direct TaskSpec path.

## Impact

- Adds an `internal/interaction` bounded context with PostgreSQL adapters and migrations, Assistant model/client ports, LocalFile ObjectStore evidence writing, Outbox publication, and projection handlers.
- Extends Gateway, Frontier, `internal/localproduct`, Compose configuration, and the React/Vite Web application.
- Calls the existing LiteLLM path through a server-owned Assistant profile; Assistant never receives Docker, WorkingCopy, Tool, or Worker runtime authority.
- Reuses existing Task, WorkerSession, WorkerRun, Artifact, SSE cursor, and command-id convergence contracts.
- Requires deterministic and model-backed tests for readiness, stale confirmation, duplicate delivery, persistence/restart, projection replay, and complete natural-language-to-Worker acceptance.
