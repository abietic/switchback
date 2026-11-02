# M1 Worker delivery requirement audit

_Scenario-by-scenario closeout for OpenSpec change `m1-worker-delivery-vertical-slice`._

## Audit result

- Audit date: 2026-07-23
- Scope: 75 scenarios in the three delta specs
- Result: every P0/P1 scenario has executable evidence; two optional capabilities remain explicitly disabled behind P2 gates
- Authority: PostgreSQL and checksum-verified ObjectStore evidence, never Frontend state or Langfuse
- Release gates: `make verify`, `make failure-acceptance`, `make real-provider-acceptance`, Web unit/build/Playwright, callback tests, strict OpenSpec validation, Git secret/ignored-evidence checks

The audit uses these statuses:

| Status | Meaning |
|---|---|
| Covered | Executable automated evidence verifies the scenario or its allowed fail-closed branch |
| Gated P2 | M1 exposes no capability that can enter the scenario; its contract and negative behavior are tested, and enabling it requires a later OpenSpec change |

## Task and Session persistence

| # | Scenario | Status | Primary evidence |
|---:|---|---|---|
| 1 | Valid direct TaskSpec is submitted | Covered | `application/direct_admission_test.go::TestDirectAdmissionCreatesReadyAssessmentAndBoundIntent`; `create_task_integration_test.go::TestCreateTaskCommitsIntentTaskAllocationOutboxAndIdempotency` |
| 2 | Workspace or TaskSpec evidence is missing | Covered | `direct_admission_test.go::TestDirectAdmissionDoesNotCreateIntentForBlockingInput` and `TestDirectAdmissionRejectsUnreadableWorkspace` |
| 3 | CreateTask response is lost and retried | Covered | `create_task_integration_test.go::TestCreateTaskResponseLossRetryReturnsOriginalResultWithoutNewWrites` |
| 4 | Idempotency key is reused with different content | Covered | `create_task_integration_test.go::TestCreateTaskIdempotencyContentConflictPreservesOriginal` |
| 5 | Runtime starts work | Covered | `dispatch_integration_test.go::TestSessionFIFOAndSingleUsePermitPreventConcurrentTopLevelRuns`; local product real Runtime composition |
| 6 | Invalid duplicate lifecycle is proposed | Covered | exhaustive Task/Run/Turn state tests in `internal/*/domain/state_test.go` |
| 7 | Patch result is ready | Covered | `domain/acceptance_test.go::TestStageResultAcceptanceMatrix`; fake-Agent and real-provider acceptance |
| 8 | User accepts delivered result | Covered | `domain/acceptance_test.go::TestAcceptAndReworkRequireInitiatorExactRevision`; `actions_integration_test.go` |
| 9 | User requests rework | Covered | `actions_integration_test.go::TestReworkAndCancelUseExactResidentSession`; Web worker-flow test |
| 10 | Capacity is available | Covered | `allocation_integration_test.go::TestWorkerSessionAllocatorEnforcesQuotaAndStableFIFOWithConcurrentRetries` |
| 11 | Capacity is full | Covered | same allocation integration test plus failure-acceptance `quota_full` case |
| 12 | Queue head receives execution slot | Covered | `dispatch_integration_test.go::TestSessionFIFOAndSingleUsePermitPreventConcurrentTopLevelRuns` |
| 13 | Another top-level command arrives while busy | Covered | same dispatch test and queue command tests |
| 14 | New Task omits Session target | Covered | `session_reuse_integration_test.go::TestExactSessionReuseRequiresIntentAndPreservesBusySessionState` |
| 15 | Exact live Session is targeted | Covered | same reuse test; `runtime_residency_integration_test.go` |
| 16 | Closed or expired Session is targeted | Covered | reuse integration negative branch and exhaustive Session transitions |
| 17 | Backend restarts after Task commit | Covered | `backend_restart_integration_test.go::TestBackendRestartRecoversTaskAllocationOutboxIdempotencyAndDueCommands` |
| 18 | Backend restarts during a live Run | Covered | allowed M1 fail-closed branch in `application/runtime_loss_test.go::TestBackendRestartReconcilesAnUnownedRuntimeAfterExactCleanup` and cleanup-unconfirmed negative test |
| 19 | AgentTurn reaches 24-hour deadline | Covered | `lifecycle_commands_integration_test.go::TestLifecycleScheduledCommandsUseServerDeadlinesAndFencing`; local Runtime-owner deadline test; failure acceptance |
| 20 | Session is idle for 48 hours | Covered | lifecycle scheduled-command integration test |
| 21 | Session reaches 14-day absolute lifetime | Covered | lifecycle scheduled-command integration test |
| 22 | Follow-up requests rework before Task terminal | Covered | `actions_integration_test.go`; exact Worker command Gateway tests |
| 23 | Follow-up arrives after Task terminal | Covered | `domain/acceptance_test.go::TestPostTerminalFollowUpCreatesStandaloneRunWithoutTaskMutation`; real-provider two-Turn acceptance |
| 24 | Task status changes | Covered | `repository_integration_test.go::TestOwnerStreamSequenceIsTransactionalAndContiguous`; materializer integration |
| 25 | Duplicate event is delivered | Covered | projection idempotency and replay integration tests |

## Runtime execution core

| # | Scenario | Status | Primary evidence |
|---:|---|---|---|
| 1 | Runtime is created for an allocated Session | Covered | `application/runtime_coordinator_test.go`; `docker/provisioner_test.go`; vertical-slice acceptance |
| 2 | Client supplies Runtime configuration | Covered | Gateway Task contract rejects infrastructure/identity overrides; Docker provisioner rejects profile/mount overrides |
| 3 | Worker container is inspected | Covered | `docker/provisioner_test.go::TestProvisionCreatesOneFixedIsolatedWorkerContainer`; image provenance gate |
| 4 | Agent requests broader container access | Covered | Docker provisioner negative tests reject arbitrary mounts, host network and floating images |
| 5 | First AgentTurn starts | Covered | Codex ACP adapter lifecycle tests; real Kimi/DeepSeek acceptance |
| 6 | Follow-up AgentTurn uses the live Session | Covered | ACP existing-session test; real-provider two-Turn cardinality evidence |
| 7 | ACP security semantics are unknown | Covered | raw-frame tee/unknown-update tests and `TestProtocolClientFailsClosedForPermissionsAndTerminals` |
| 8 | Current Runtime reports an event | Covered | `controlgrpc/control_test.go::TestAuthenticatedStreamExchangesSequencedEventsCommandsAndACKs` |
| 9 | Stale or duplicate Runtime event arrives | Covered | control gRPC duplicate, generation, epoch and conflicting-event fencing tests |
| 10 | WorkerRun starts with Kimi or DeepSeek | Covered | fixed profile tests plus successful real-provider acceptance for both routes |
| 11 | Upstream request fails | Covered | failure acceptance invalid credential case; execution and acceptance outcomes remain separate |
| 12 | Runtime secret boundary is inspected | Covered | runtime-secret loader/materializer tests and image boundary tests |
| 13 | Evidence is finalized | Covered | `application/finalizer_test.go::TestTurnEvidenceFinalizerPersistsVerifiedObjectsAndExactProvenance` |
| 14 | AgentTurn completes | Covered | finalizer, publisher and vertical-slice acceptance verify Trace/report/patch visibility |
| 15 | Evidence checksum is invalid | Covered | finalizer/publisher negative tests and failure acceptance checksum case |
| 16 | Langfuse projection succeeds | Covered | projector exact export/query tests and live LiteLLM model-observation path |
| 17 | Langfuse is unavailable | Covered | observability degradation integration test, missing-model-observation test and failure acceptance |
| 18 | Stage result is delivered | Covered | `runtime_residency_integration_test.go::TestStageCompletionKeepsGenerationOneRuntimeResident` |
| 19 | Session becomes terminal | Covered | exact Docker container/secret cleanup tests |
| 20 | Generation 1 container exits unexpectedly | Covered | runtime-loss unit/integration tests and failure acceptance |
| 21 | Normal Session shutdown completes | Covered | Docker cleanup absence proof and Runtime cleanup service tests |
| 22 | Docker deletion fails | Covered | `runtime_cleanup_integration_test.go::TestCleanupFailureIsReportedAndScheduledBeforeReconciliationConfirmsZeroResidue`; failure acceptance |

## WorkerDetail interaction

| # | Scenario | Status | Primary evidence |
|---:|---|---|---|
| 1 | User opens a live WorkerSession | Covered | WorkerDetail unit test, Frontier materializer integration and browser flow |
| 2 | Task is terminal but Session remains live | Covered | WorkerDetail follow-up unit/browser tests; Runtime residency integration |
| 3 | Client connects after snapshot | Covered | Frontier SSE handler and client view controller tests |
| 4 | Client reconnects with retained cursor | Covered | Frontier replay integration and Web controller reconnect test |
| 5 | Cursor cannot be replayed | Covered | explicit `resync_required` backend and client-replica tests |
| 6 | Worker emits progress and Artifact events | Covered | projection materializer, display checkpoint and WorkerDetail rendering tests |
| 7 | Projection is behind an accepted command | Covered | pending-command client replica tests; UI never invents authoritative state |
| 8 | Agent streams text or activity | Covered | bounded transient broker and SSE transient-event tests |
| 9 | Tool, approval, Artifact or terminal state changes | Covered | durable display checkpoint and materializer tests |
| 10 | Browser misses transient chunks | Covered | client replica drops transient overlay on gap and reloads durable checkpoint |
| 11 | Report Artifact is opened | Covered | ArtifactDetail report test and canonical content handler test |
| 12 | Patch Artifact is opened | Covered | ArtifactDetail diff test and browser review flow |
| 13 | Artifact content is missing or checksum fails | Covered | Artifact HTTP safe integrity/visibility tests and browser integrity flow |
| 14 | Valid follow-up chat is submitted | Covered | exact Worker Gateway command test and browser retry flow |
| 15 | Command target is stale | Covered | Gateway stale mapping and stale client command behavior |
| 16 | Command is retried | Covered | Gateway and Web tests retain the original idempotency key |
| 17 | User queues a message | Covered | queued-Turn application/PostgreSQL behavior and composer tests |
| 18 | User interrupts active work | Covered | ACP exact cancel test, Runtime command handling and composer/browser tests |
| 19 | User steers active work | Gated P2 | command is capability-gated; no M1 Codex adapter capability advertises steer |
| 20 | Adapter does not support steer | Covered | Gateway returns `unsupported_command`; composer offers queue/interrupt without emulation |
| 21 | Ordinary sandbox command runs | Covered | real Codex acceptance executes WorkingCopy commands without per-operation approval and records Trace |
| 22 | Protected effect is requested | Gated P2 | M1 injects no external credentials/production-write/capability-install Tool; unsolicited ACP permission requests fail closed |
| 23 | Session-scoped approval is granted | Covered | `approval_integration_test.go::TestApprovalDecisionPersistsExactSessionGrantAndIdempotency`; only exact future enabled adapters may consume it |
| 24 | Worker needs user input | Covered | pending ApprovalRequest materialization plus WorkerDetail actionable state tests |
| 25 | Waiting action becomes obsolete | Covered | projected action removal test and exact request revision check |
| 26 | Local UI submits a command | Covered | loopback-only local product composition and server-injected actor Gateway tests |
| 27 | Client sends identity override | Covered | Gateway Task/Worker contract negative tests |
| 28 | Model or Runtime fails | Covered | safe Gateway/Artifact diagnostics, invalid-provider/runtime-loss acceptance and known-token scan |

## P0/P1 findings resolved during closeout

1. The default Backend now composes Task admission, allocation, RuntimeSupervisor, Artifact publication, Frontier snapshot/SSE and live UI commands instead of serving demo-only data.
2. LiteLLM emits authenticated exact model observations to the Shim; Backend projects verified Run/Turn/model identity to Langfuse without making it authoritative.
3. A missing model observation is persisted as `degraded:model_observation_missing` and never produces a false successful Langfuse projection.
4. The scheduled-command poller cannot be blocked by a live 24-hour Turn; deadline handling is owned by the exact Runtime and fences Docker when interrupt grace expires.
5. Every failure after Runtime ownership commit converges through exact cleanup and fail-closed Task/Session/Run/Turn state.
6. Approval decisions persist exact request revision, scope, expiry, idempotency and idle refresh; disabled protected effects remain unauthorized.
7. Artifact content is checksum-verified on read and returns non-disclosing integrity/visibility errors.
8. Live completion no longer fails while settling an empty interrupt set: the PostgreSQL JSONB terminal-reason parameter is explicitly typed and covered against a real database.

## Explicit P2 gates

- Real steer requires an AgentAdapter capability and must preserve queue/interrupt semantics.
- Protected external effects require a same-operation request/pause/grant/resume bridge before any credentialed or production-write Tool is exposed.
- Runtime generation recovery/ACP resume, long-poll fallback, Linux-enforced egress, managed processes/preview, remote repository credentials and multi-user authorization remain separate changes.

These gates are absent capabilities, not tolerated P0/P1 defects. No enabled M1 path can silently enter them.

## Evidence retention and secret boundary

Acceptance output remains under ignored `runtime-data/`; local product state remains under ignored `.switchback/`; generated credentials remain in ignored `deploy/local/.env` or external mode-`0600` key files. Final closeout must prove that none of these paths or known provider tokens are tracked by Git.
