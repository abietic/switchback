import { describe, expect, it, vi } from "vitest";
import { submitTaskContinuation } from "./taskCommands";

describe("submitTaskContinuation", () => {
  it("posts the exact Task, Session, Run, and Turn revision tuple", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      command_id: "command-1", status: "accepted",
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    await submitTaskContinuation(fetcher, {
      taskId: "task-1", expectedTaskRevision: 4,
      workerSessionId: "session-1", expectedSessionRevision: 5,
      workerRunId: "run-1", expectedRunRevision: 6,
      agentTurnId: "turn-1", expectedTurnRevision: 7,
      interactionKind: "chat", idempotencyKey: "continue-1", message: "Continue",
    });
    expect(fetcher).toHaveBeenCalledWith("/api/v1/tasks/task-1/continue", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        schema_version: "gateway.task_continuation.v1",
        idempotency_key: "continue-1",
        expected_task_revision: 4,
        worker_session_id: "session-1",
        expected_session_revision: 5,
        worker_run_id: "run-1",
        expected_run_revision: 6,
        agent_turn_id: "turn-1",
        expected_turn_revision: 7,
        interaction_kind: "chat",
        message: "Continue",
      }),
    }));
  });
});
