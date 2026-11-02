import { describe, expect, it, vi } from "vitest";
import { GatewayCommandError, submitWorkerCommand } from "./workerCommands";

const target = {
  workerSessionId: "session-1",
  expectedSessionRevision: 14,
  workerRunId: "run-3",
  expectedRunRevision: 7,
  agentTurnId: "turn-5",
  expectedTurnRevision: 3,
};

describe("submitWorkerCommand", () => {
  it("sends an exact revisioned Session command with caller-owned idempotency", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      command_id: "command-1",
      status: "accepted",
      queue_sequence: 4,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));

    await expect(submitWorkerCommand(fetcher, {
      action: "queue",
      target,
      idempotencyKey: "worker-command-retry-1",
      content: "Run the browser tests next.",
    })).resolves.toMatchObject({ command_id: "command-1", queue_sequence: 4 });

    expect(fetcher).toHaveBeenCalledWith("/api/v1/worker-sessions/session-1/queue", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        schema_version: "gateway.worker_command.v1",
        idempotency_key: "worker-command-retry-1",
        expected_session_revision: 14,
        worker_run_id: "run-3",
        expected_run_revision: 7,
        agent_turn_id: "turn-5",
        expected_turn_revision: 3,
        content: "Run the browser tests next.",
      }),
    }));
  });

  it.each([
    [409, "stale_target"],
    [422, "unsupported_command"],
  ])("maps %s command errors without exposing raw response text", async (status, code) => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      code,
      view_ref: "/api/v1/views/worker_detail/session-1",
      internal: "must not escape",
    }), { status, headers: { "Content-Type": "application/json" } }));

    const promise = submitWorkerCommand(fetcher, {
      action: "steer",
      target,
      idempotencyKey: "worker-command-retry-2",
      content: "Focus on the failing test.",
    });
    await expect(promise).rejects.toBeInstanceOf(GatewayCommandError);
    await expect(promise).rejects.toMatchObject({ code, viewRef: "/api/v1/views/worker_detail/session-1" });
  });
});
