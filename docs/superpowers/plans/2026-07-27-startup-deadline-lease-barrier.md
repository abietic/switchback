# Startup Deadline Lease Barrier Implementation Plan

> Status: approved by the ongoing reliability iteration. Preserve unrelated dirty worktree changes; do not stage or commit.

**Goal:** Ensure Backend startup never enables Runtime supervision, dispatch, or ordinary execution while an already-due `agent_turn_deadline` command is still pending or leased, and ensure cancellation cannot strand a command lease after its handler finishes.

**Architecture:** The generic PostgreSQL `ScheduledCommandPoller` remains the sole lease owner. It gains a filtered, read-only due-outstanding query and finalizes every claimed command with a short context detached from worker cancellation. The local startup drain reaches a fixed point only when the exact tenant/type filter has no due `pending` or `leased` command; a foreign live lease therefore blocks readiness and supervision until it completes or expires and can be reclaimed. Runtime hydration may finish before that final fixed point, but it cannot receive supervision or new execution authority.

**Safety boundary:** This change does not steal an unexpired lease, alter fencing tokens, or make the startup barrier execute domain work directly. It only prevents a false-empty result and makes lease finalization cancellation-safe.

## Task 1: Make claimed-command finalization cancellation-safe

**Files:**

- Modify: `internal/platform/postgres/scheduled_commands.go`
- Modify: `internal/platform/postgres/scheduled_commands_integration_test.go`

**RED tests:**

- A handler that succeeds while the worker context is cancelled still commits the exact leased command as `applied`.
- A handler that fails while the worker context is cancelled still releases the exact lease to `pending` with its error code.
- A stale fencing token still cannot complete or release another owner's lease.

**GREEN implementation:**

- Derive a short finalization context from `context.WithoutCancel(ctx)`.
- Use it only for `complete` and `release` after a command was claimed.
- Keep handler execution and claim cancellation-sensitive.

## Task 2: Drain to a durable due-command fixed point

**Files:**

- Modify: `internal/platform/postgres/scheduled_commands.go`
- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/localproduct/backend_deadline_claim_handler_integration_test.go`

**RED tests:**

- A due `agent_turn_deadline` held by a foreign unexpired lease prevents the startup drain from returning success.
- After that lease expires, the startup poller reclaims it with a higher fencing token, persists the deadline settlement, and only then returns.
- Unrelated tenants and command types do not block the filtered startup drain.
- Cancellation while waiting for a foreign lease returns promptly.

**GREEN implementation:**

- Add a filtered `HasDueOutstanding` query covering both `pending` and `leased`.
- After each `RunOnce`, return success only when that query is false.
- If work remains but none was applied, wait for the normal bounded poll interval before retrying.
- Retain the existing maximum transition guard and fail closed on database or handler errors.

## Verification

```bash
go test ./internal/platform/postgres ./internal/localproduct -count=1
go test -race ./internal/platform/postgres ./internal/localproduct -count=1
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" go test -tags=integration ./internal/platform/postgres ./internal/localproduct -run 'ScheduledCommand|StartupDeadline' -count=1
make verify
```
