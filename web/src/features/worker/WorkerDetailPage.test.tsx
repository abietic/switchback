import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ComponentProps } from "react";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalRequest, TransientDisplayDelta, WorkerDetailData } from "../../api/contracts";
import { GatewayCommandError } from "../../api/workerCommands";
import { demoWorker } from "../../demo/data";
import { ThemeProvider } from "../../providers/ThemeProvider";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { taskDecisionScopeKeyFor } from "./useWorkerCommandAuthority";

function worker(overrides: Partial<WorkerDetailData> = {}): WorkerDetailData {
  return { ...demoWorker, ...overrides };
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function renderWorker(props: ComponentProps<typeof WorkerDetailPage>) {
  return render(
    <ThemeProvider>
      <WorkerDetailPage {...props} />
    </ThemeProvider>,
  );
}

describe("WorkerDetail", () => {
  it("renders layered execution state and transient activity", () => {
    const live: TransientDisplayDelta = {
      schema_version: "frontier.transient_display_delta.v1",
      display_stream_id: "display-1",
      part_id: "part-live",
      base_checkpoint_version: 1,
      transient_sequence: 1,
      delta_type: "text_delta",
      payload: { text: "Running focused integration tests" },
      emitted_at: "2026-07-23T00:00:00Z",
    };
    renderWorker({ worker: worker({ task_status: "delivered", run_status: "completed", turn_status: "completed" }), transient: [live] });

    expect(screen.getByLabelText("Execution status")).toHaveTextContent("TaskDelivered");
    expect(screen.getByLabelText("Execution status")).toHaveTextContent("RunCompleted");
    expect(screen.getByText("Running focused integration tests")).toBeInTheDocument();
  });

  it("renders the persisted runtime network profile without destination claims", () => {
    renderWorker({ worker: worker({
      network_profile: {
        id: "trusted_full_network_v1",
        revision: 1,
        digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
        enforcement_status: "active",
      },
    }), transient: [] });
    expect(screen.getByLabelText("Execution status")).toHaveTextContent("Trusted Full Network V1 · Active");
    expect(screen.queryByText(/destination|host|url/i)).not.toBeInTheDocument();
  });

  it("renders recovery generation, attempt, continuity and evidence", () => {
    renderWorker({ worker: worker({
      status: "recovering",
      runtime_generation: 2,
      runtime_recovery: {
        status: "recovering_runtime",
        phase: "reattaching",
        failed_generation: 1,
        replacement_generation: 2,
        attempt_count: 1,
        continuity_status: "pending",
        evidence_at: "2026-07-26T09:00:00Z",
      },
    }), transient: [] });
    expect(screen.getByLabelText("Runtime recovery")).toHaveTextContent("Recovering Runtime");
    expect(screen.getByLabelText("Runtime recovery")).toHaveTextContent("Reattaching");
    expect(screen.getByLabelText("Runtime recovery")).toHaveTextContent("Generation 1 → 2");
    expect(screen.getByLabelText("Runtime recovery")).toHaveTextContent("Attempt 1/1");
    expect(screen.getByLabelText("Runtime recovery")).toHaveTextContent("Pending");
    expect(screen.getByLabelText("Execution status")).toHaveTextContent("RecoveryRecovering Runtime");
  });

  it("renders failed recovery continuity without exposing an unsanitized outcome", () => {
    renderWorker({ worker: worker({
      status: "waiting_user",
      runtime_generation: 2,
      runtime_recovery: {
        status: "runtime_recovery_failed",
        phase: "exhausted",
        failed_generation: 1,
        replacement_generation: 2,
        attempt_count: 1,
        continuity_status: "unproven",
        evidence_at: "2026-07-26T09:00:00Z",
        outcome_reason: "provider_secret=do-not-render",
      },
    }), transient: [] });

    const recovery = screen.getByLabelText("Runtime recovery");
    expect(recovery).toHaveTextContent("Runtime Recovery Failed");
    expect(recovery).toHaveTextContent("Exhausted");
    expect(recovery).toHaveTextContent("Generation 1 → 2");
    expect(recovery).toHaveTextContent("Unproven");
    expect(recovery).toHaveTextContent("Recovery details are unavailable");
    expect(recovery).not.toHaveTextContent("provider_secret");
    expect(recovery).not.toHaveTextContent("do-not-render");
  });

  it("exposes a focused layout with a bounded execution scroller", () => {
    renderWorker({ worker: worker({ artifacts: [] }), transient: [] });

    const page = screen.getByRole("main");
    expect(page).toHaveAttribute("data-layout", "worker-focused");
    const execution = page.querySelector("[data-region='worker-execution']");
    expect(execution).toHaveClass("worker-execution-scroll");
    expect(execution).toContainElement(screen.getByLabelText("Worker timeline"));
  });

  it("exposes a keyboard-operable bounded workbench separator", () => {
    const viewport = vi.spyOn(window, "innerWidth", "get").mockReturnValue(1280);
    try {
      renderWorker({ worker: worker({ artifacts: [] }), transient: [] });

      const separator = screen.getByRole("separator", { name: "Resize artifact workbench" });
      expect(separator).toHaveAttribute("aria-orientation", "vertical");
      expect(separator).toHaveAttribute("aria-valuemin", "360");
      expect(separator).toHaveAttribute("aria-valuemax", "800");
      expect(separator).toHaveAttribute("aria-valuenow", "520");

      for (let index = 0; index < 30; index += 1) fireEvent.keyDown(separator, { key: "ArrowLeft" });
      expect(separator).toHaveAttribute("aria-valuenow", "800");

      fireEvent.keyDown(separator, { key: "ArrowRight" });
      expect(separator).toHaveAttribute("aria-valuenow", "776");

      for (let index = 0; index < 30; index += 1) fireEvent.keyDown(separator, { key: "ArrowRight" });
      expect(separator).toHaveAttribute("aria-valuenow", "360");
    } finally {
      viewport.mockRestore();
    }
  });

  it("clamps the workbench rail when the viewport narrows without growing it on expansion", () => {
    let viewportWidth = 1280;
    const viewport = vi.spyOn(window, "innerWidth", "get").mockImplementation(() => viewportWidth);
    const removeListener = vi.spyOn(window, "removeEventListener");
    try {
      const view = renderWorker({ worker: worker({ artifacts: [] }), transient: [] });
      const separator = screen.getByRole("separator", { name: "Resize artifact workbench" });
      const page = screen.getByRole("main");

      for (let index = 0; index < 30; index += 1) fireEvent.keyDown(separator, { key: "ArrowLeft" });
      expect(separator).toHaveAttribute("aria-valuenow", "800");

      viewportWidth = 900;
      fireEvent(window, new Event("resize"));
      expect(separator).toHaveAttribute("aria-valuemax", "420");
      expect(separator).toHaveAttribute("aria-valuenow", "420");
      expect(page).toHaveStyle({ "--workbench-width": "420px" });

      viewportWidth = 1400;
      fireEvent(window, new Event("resize"));
      expect(separator).toHaveAttribute("aria-valuemax", "920");
      expect(separator).toHaveAttribute("aria-valuenow", "420");

      view.unmount();
      expect(removeListener).toHaveBeenCalledWith("resize", expect.any(Function));
    } finally {
      removeListener.mockRestore();
      viewport.mockRestore();
    }
  });

  it("shows actionable waiting-user state without marking the Session terminal", () => {
    renderWorker({ worker: worker({
      status: "waiting_user",
      status_tone: "waiting",
      waiting_user: {
        reason: "approval_required",
        message: "Publishing this branch needs your approval.",
        allowed_actions: ["approve", "cancel"],
      },
    }), transient: [] });

    expect(screen.getByRole("heading", { name: "Needs your input" })).toBeInTheDocument();
    expect(screen.getByText("Publishing this branch needs your approval.")).toBeInTheDocument();
    expect(screen.getByText("Session remains available for follow-up")).toBeInTheDocument();
  });

  it("keeps exact-Session follow-up available after the Task is terminal", async () => {
    const onCommand = vi.fn().mockResolvedValue({ command_id: "command-follow-up", status: "accepted" });
    renderWorker({
      worker: worker({ task_status: "completed", task_status_label: undefined, status: "idle", status_tone: "neutral" }),
      availableActions: [{ action: "worker.follow_up", target_id: "session-local-01", expected_revision: 14 }],
      onCommand,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), { target: { value: "Explain the patch" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onCommand).toHaveBeenCalledWith(expect.objectContaining({ action: "follow-up", content: "Explain the patch" })));
    expect(screen.getByLabelText("Execution status")).toHaveTextContent("TaskAccepted");
  });

  it("keeps an unknown Worker message across unrelated Task and Approval changes", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const followUpAction = { action: "worker.follow_up", target_id: "session-local-01", expected_revision: 14 };
    const unrelatedApproval: ApprovalRequest = {
      approval_request_id: "approval-worker-unrelated",
      revision: 1,
      capability: "artifact.publish",
      target: "artifact-unrelated",
      effect: "external_write",
      reason: "Unrelated to the Worker message.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const view = renderWorker({
      worker: worker({ artifacts: [], task_status: "completed" }),
      availableActions: [followUpAction],
      onCommand,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Keep this exact Worker message" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    const initialRetry = await screen.findByRole("button", { name: "Retry" });
    await waitFor(() => expect(initialRetry).toBeEnabled());
    const original = onCommand.mock.calls[0][0];

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({
            artifacts: [],
            task_status: "delivered",
            task_revision: 9,
            approvals: [unrelatedApproval],
          })}
          availableActions={[
            { ...followUpAction },
            { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 9 },
            { action: "worker.approval", target_id: "approval-worker-unrelated", expected_revision: 1 },
          ]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );

    const retry = screen.getByRole("button", { name: "Retry" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    expect(onCommand.mock.calls[1][0]).toEqual(original);
  });

  it("does not revive an unknown Worker message after its A-B-A authority changes", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = renderWorker({
      worker: worker({ artifacts: [], task_status: "completed" }),
      availableActions: [{ action: "worker.follow_up", target_id: "session-local-01", expected_revision: 14 }],
      onCommand,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Preserve this draft but retire its old intent" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    const retry = await screen.findByRole("button", { name: "Retry" });
    await waitFor(() => expect(retry).toBeEnabled());
    const oldIdempotencyKey = onCommand.mock.calls[0][0].idempotencyKey;

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_status: "completed", session_revision: 15 })}
          availableActions={[{ action: "worker.follow_up", target_id: "session-local-01", expected_revision: 15 }]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_status: "completed", session_revision: 14 })}
          availableActions={[{ action: "worker.follow_up", target_id: "session-local-01", expected_revision: 14 }]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );

    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Continue with this worker" }))
      .toHaveValue("Preserve this draft but retire its old intent");
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    expect(onCommand.mock.calls[1][0].idempotencyKey).not.toBe(oldIdempotencyKey);
  });

  it("continues a delivered bound Task through the exact Task authority", async () => {
    const onTaskContinue = vi.fn().mockResolvedValue({ command_id: "command-continue", status: "accepted" });
    const onCommand = vi.fn();
    renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: [{
        action: "task.continue",
        target_id: "task-switchback-counter",
        expected_revision: 8,
        metadata: { interaction_kinds: ["chat", "rework"] },
      }],
      onCommand,
      onTaskContinue,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Explain the verification result" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onTaskContinue).toHaveBeenCalledWith(expect.objectContaining({
      taskId: "task-switchback-counter",
      expectedTaskRevision: 8,
      workerSessionId: "session-local-01",
      expectedSessionRevision: 14,
      workerRunId: "run-03",
      expectedRunRevision: 7,
      agentTurnId: "turn-05",
      expectedTurnRevision: 3,
      interactionKind: "chat",
      message: "Explain the verification result",
    })));
    expect(onCommand).not.toHaveBeenCalled();
  });

  it("fences pending Task-delivery mutations across continuation, accept, and rework", () => {
    renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: [
        {
          action: "task.continue",
          target_id: "task-switchback-counter",
          expected_revision: 8,
          metadata: { interaction_kinds: ["chat"] },
        },
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
        { action: "task.rework", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
      pendingCommands: [{
        commandId: "command-accept",
        action: "accept",
        submittedAt: "2026-07-26T09:00:00Z",
      }],
      transient: [],
    });

    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Do not submit against the old Task revision" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Request changes message" }), {
      target: { value: "Needs another pass" },
    });
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Accept" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
  });

  it("keeps an independent Task review action available while approval projection is pending", () => {
    renderWorker({
      worker: worker({
        artifacts: [],
        approvals: [{
          approval_request_id: "approval-1",
          revision: 4,
          capability: "github.branch_write",
          target: "github.com/acme/switchback:refs/heads/review",
          effect: "external_write",
          reason: "Publish the reviewed patch.",
          expires_at: "2099-07-26T09:00:00Z",
          scope_options: ["once"],
        }],
      }),
      availableActions: [
        { action: "worker.approval", target_id: "approval-1", expected_revision: 4 },
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
      pendingCommands: [{
        commandId: "command-approval",
        action: "approval",
        submittedAt: "2026-07-26T09:00:00Z",
      }],
      transient: [],
    });

    expect(screen.getByRole("button", { name: "Approve once" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled();
  });

  it("retries an unknown Task decision with the same intent and fences conflicting actions", async () => {
    const randomUUID = vi.spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000041")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000042");
    const onTaskCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockResolvedValueOnce({ command_id: "command-accept", status: "accepted" });
    renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: [
        {
          action: "task.continue",
          target_id: "task-switchback-counter",
          expected_revision: 8,
          metadata: { interaction_kinds: ["chat"] },
        },
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
        { action: "task.rework", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
      onTaskCommand,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Request changes message" }), {
      target: { value: "Do not create a conflicting rework intent" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Do not create a conflicting continuation intent" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));

    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    expect(screen.getByRole("button", { name: "Accept" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    fireEvent.click(retry);

    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));
    expect(onTaskCommand.mock.calls[1][0]).toEqual(onTaskCommand.mock.calls[0][0]);
    expect(randomUUID).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled());
    expect(screen.getByRole("button", { name: "Request changes" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("releases an exact same-key retry attempt when its delta wins the retry HTTP race", async () => {
    vi.spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValue("00000000-0000-4000-8000-000000000043");
    const accepted = deferred<{ command_id: string; status: string }>();
    const onTaskCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockImplementationOnce(() => accepted.promise);
    const pageProps: ComponentProps<typeof WorkerDetailPage> = {
      worker: worker({ artifacts: [] }),
      availableActions: [
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
      onTaskCommand,
      transient: [],
    };
    const view = renderWorker(pageProps);
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));

    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    const firstRequest = onTaskCommand.mock.calls[0][0];
    const exactPending = {
      clientCommandId: firstRequest.idempotencyKey,
      action: firstRequest.action,
      scopeKey: taskDecisionScopeKeyFor(
        firstRequest.action,
        firstRequest.taskId,
        pageProps.worker.worker_session_id,
      ),
      submittedAt: "2026-07-30T05:00:00Z",
    };
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage {...pageProps} pendingCommands={[exactPending]} />
      </ThemeProvider>,
    );
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage {...pageProps} pendingCommands={[]} />
      </ThemeProvider>,
    );
    await act(async () => {
      accepted.resolve({ command_id: "command-accept-retry", status: "accepted" });
      await accepted.promise;
    });

    await waitFor(() => expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled());
  });

  it("keeps the unknown Task fence under unchanged authority and releases it only after revision changes", async () => {
    const onTaskCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: [
        {
          action: "task.continue",
          target_id: "task-switchback-counter",
          expected_revision: 8,
          metadata: { interaction_kinds: ["chat"] },
        },
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
      onTaskCommand,
      transient: [],
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Continue only after the prior decision is abandoned" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));

    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    expect(screen.queryByRole("button", { name: "Abandon Task decision retry" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [] })}
          availableActions={[
            {
              action: "task.continue",
              target_id: "task-switchback-counter",
              expected_revision: 8,
              metadata: { interaction_kinds: ["chat"] },
            },
            { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
          ]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const retryAfterRerender = screen.getByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retryAfterRerender).toBeEnabled());
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 9 })}
          availableActions={[
            {
              action: "task.continue",
              target_id: "task-switchback-counter",
              expected_revision: 9,
              metadata: { interaction_kinds: ["chat"] },
            },
            { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 9 },
          ]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry Task decision" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("keeps an unknown Task decision across unrelated Approval authority changes", async () => {
    const onTaskCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const unrelatedApproval: ApprovalRequest = {
      approval_request_id: "approval-unrelated",
      revision: 1,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/unrelated",
      effect: "external_write",
      reason: "Unrelated approval authority.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const taskAction = { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 };
    const view = renderWorker({
      worker: worker({ artifacts: [], approvals: [] }),
      availableActions: [taskAction],
      onTaskCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    const initialRetry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(initialRetry).toBeEnabled());
    const original = onTaskCommand.mock.calls[0][0];

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({
            artifacts: [],
            approvals: [unrelatedApproval],
            status: "idle",
            runtime_generation: 2,
          })}
          availableActions={[
            { ...taskAction },
            { action: "worker.approval", target_id: "approval-unrelated", expected_revision: 1 },
          ]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const retryAfterFirstRerender = screen.getByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retryAfterFirstRerender).toBeEnabled());
    expect(screen.getByRole("button", { name: "Accept" })).toBeDisabled();

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({
            artifacts: [],
            approvals: [{ ...unrelatedApproval, revision: 2, target: "github.com/acme/switchback:refs/heads/changed" }],
          })}
          availableActions={[
            { ...taskAction },
            { action: "worker.approval", target_id: "approval-unrelated", expected_revision: 2 },
          ]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const retry = screen.getByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));
    expect(onTaskCommand.mock.calls[1][0]).toEqual(original);
  });

  it("keeps Approval A retry authority across unrelated Approval B changes", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const approvalA: ApprovalRequest = {
      approval_request_id: "approval-a",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/a",
      effect: "external_write",
      reason: "Approval A.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const approvalB: ApprovalRequest = {
      ...approvalA,
      approval_request_id: "approval-b",
      revision: 1,
      target: "github.com/acme/switchback:refs/heads/b",
      reason: "Approval B.",
    };
    const actionA = { action: "worker.approval", target_id: "approval-a", expected_revision: 4 };
    const view = renderWorker({
      worker: worker({ artifacts: [], approvals: [approvalA] }),
      availableActions: [actionA],
      onCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));
    const initialRetry = await screen.findByRole("button", { name: "Retry approval decision" });
    await waitFor(() => expect(initialRetry).toBeEnabled());
    const original = onCommand.mock.calls[0][0];

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], approvals: [approvalA, approvalB] })}
          availableActions={[
            { ...actionA },
            { action: "worker.approval", target_id: "approval-b", expected_revision: 1 },
          ]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const retryAfterFirstRerender = screen.getByRole("button", { name: "Retry approval decision" });
    await waitFor(() => expect(retryAfterFirstRerender).toBeEnabled());

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({
            artifacts: [],
            approvals: [approvalA, { ...approvalB, revision: 2, capability: "artifact.publish" }],
          })}
          availableActions={[
            { ...actionA },
            { action: "worker.approval", target_id: "approval-b", expected_revision: 2 },
          ]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const retry = screen.getByRole("button", { name: "Retry approval decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    expect(onCommand.mock.calls[1][0]).toEqual(original);
  });

  it("scopes unknown Approval retries and accepted-pending fences to the exact Approval owner", async () => {
    const retryA = deferred<{ command_id: string; status: "accepted" }>();
    const approvalA: ApprovalRequest = {
      approval_request_id: "approval-owner-a",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/a",
      effect: "external_write",
      reason: "Approval owner A.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const approvalB: ApprovalRequest = {
      ...approvalA,
      approval_request_id: "approval-owner-b",
      revision: 2,
      target: "github.com/acme/switchback:refs/heads/b",
      reason: "Approval owner B.",
    };
    const onCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockResolvedValueOnce({ command_id: "command-approval-b", status: "accepted" })
      .mockReturnValueOnce(retryA.promise);
    renderWorker({
      worker: worker({ artifacts: [], approvals: [approvalA, approvalB] }),
      availableActions: [
        { action: "worker.approval", target_id: approvalA.approval_request_id, expected_revision: approvalA.revision },
        { action: "worker.approval", target_id: approvalB.approval_request_id, expected_revision: approvalB.revision },
      ],
      onCommand,
      transient: [],
    });
    const itemA = screen.getByText("Approval owner A.").closest(".approval-item");
    const itemB = screen.getByText("Approval owner B.").closest(".approval-item");
    expect(itemA).not.toBeNull();
    expect(itemB).not.toBeNull();

    fireEvent.click(within(itemA as HTMLElement).getByRole("button", { name: "Approve once" }));
    const retry = await within(itemA as HTMLElement).findByRole("button", { name: "Retry approval decision" });
    const approveB = within(itemB as HTMLElement).getByRole("button", { name: "Approve once" });
    await waitFor(() => expect(retry).toBeEnabled());
    expect(approveB).toBeEnabled();

    fireEvent.click(approveB);
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(approveB).toBeEnabled());
    await waitFor(() => expect(retry).toBeEnabled());

    fireEvent.click(retry);
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(3));
    expect(onCommand.mock.calls[2][0]).toEqual(onCommand.mock.calls[0][0]);
    await act(async () => {
      retryA.reject(new GatewayCommandError("network_error"));
      await retryA.promise.catch(() => undefined);
    });

    expect(approveB).toBeEnabled();
  });

  it("does not let Approval B pending projection block Approval A same-key retry", async () => {
    const approvalA: ApprovalRequest = {
      approval_request_id: "approval-pending-a",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/a",
      effect: "external_write",
      reason: "Approval pending A.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const approvalB: ApprovalRequest = {
      ...approvalA,
      approval_request_id: "approval-pending-b",
      revision: 2,
      target: "github.com/acme/switchback:refs/heads/b",
      reason: "Approval pending B.",
    };
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = renderWorker({
      worker: worker({ artifacts: [], approvals: [approvalA, approvalB] }),
      availableActions: [
        { action: "worker.approval", target_id: approvalA.approval_request_id, expected_revision: approvalA.revision },
        { action: "worker.approval", target_id: approvalB.approval_request_id, expected_revision: approvalB.revision },
      ],
      onCommand,
      transient: [],
    });
    const itemA = screen.getByText("Approval pending A.").closest(".approval-item");
    expect(itemA).not.toBeNull();
    fireEvent.click(within(itemA as HTMLElement).getByRole("button", { name: "Approve once" }));
    const retry = await within(itemA as HTMLElement).findByRole("button", { name: "Retry approval decision" });
    await waitFor(() => expect(retry).toBeEnabled());

    const pendingB = [{
      commandId: "command-pending-b",
      action: "approval",
      scopeKey: "approval:approval-pending-b",
      submittedAt: "2026-07-26T09:00:00Z",
    }];
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], approvals: [approvalA, approvalB] })}
          availableActions={[
            { action: "worker.approval", target_id: approvalA.approval_request_id, expected_revision: approvalA.revision },
            { action: "worker.approval", target_id: approvalB.approval_request_id, expected_revision: approvalB.revision },
          ]}
          pendingCommands={pendingB}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );

    const retryAfterRerender = within(itemA as HTMLElement)
      .getByRole("button", { name: "Retry approval decision" });
    await waitFor(() => expect(retryAfterRerender).toBeEnabled());
  });

  it("releases an accepted Approval attempt before replica pending and keeps pending scoped to its owner", async () => {
    const approvalA: ApprovalRequest = {
      approval_request_id: "approval-direct-a",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/a",
      effect: "external_write",
      reason: "Direct Approval A.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const approvalB: ApprovalRequest = {
      ...approvalA,
      approval_request_id: "approval-direct-b",
      revision: 2,
      target: "github.com/acme/switchback:refs/heads/b",
      reason: "Direct Approval B.",
    };
    const onCommand = vi.fn()
      .mockResolvedValue({ command_id: "command-direct-a", status: "accepted" });
    const pageProps: ComponentProps<typeof WorkerDetailPage> = {
      worker: worker({ artifacts: [], approvals: [approvalA, approvalB] }),
      availableActions: [
        { action: "worker.approval", target_id: approvalA.approval_request_id, expected_revision: approvalA.revision },
        { action: "worker.approval", target_id: approvalB.approval_request_id, expected_revision: approvalB.revision },
      ],
      onCommand,
      transient: [],
    };
    const view = renderWorker(pageProps);

    const approvalItem = (reason: string) => {
      const item = screen.getByText(reason).closest(".approval-item");
      expect(item).not.toBeNull();
      return item as HTMLElement;
    };
    fireEvent.click(within(approvalItem(approvalA.reason)).getByRole("button", { name: "Approve once" }));
    await waitFor(() => expect(onCommand).toHaveBeenCalledOnce());
    await waitFor(() => expect(
      within(approvalItem(approvalA.reason)).getByRole("button", { name: "Approve once" }),
    ).toBeEnabled());
    expect(
      within(approvalItem(approvalB.reason)).getByRole("button", { name: "Approve once" }),
    ).toBeEnabled();

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          {...pageProps}
          pendingCommands={[{
            commandId: "command-direct-a",
            clientCommandId: onCommand.mock.calls[0][0].idempotencyKey,
            action: "approval",
            scopeKey: `approval:${approvalA.approval_request_id}`,
            submittedAt: "2026-07-30T05:00:00Z",
          }]}
        />
      </ThemeProvider>,
    );

    await waitFor(() => expect(
      within(approvalItem(approvalA.reason)).getByRole("button", { name: "Approve once" }),
    ).toBeDisabled());
    expect(
      within(approvalItem(approvalB.reason)).getByRole("button", { name: "Approve once" }),
    ).toBeEnabled();
  });

  it("rejects a stale unknown approval outcome after the in-flight request authority is replaced", async () => {
    const request = deferred<{ command_id: string; status: "accepted" }>();
    const onCommand = vi.fn().mockReturnValue(request.promise);
    const firstApproval: ApprovalRequest = {
      approval_request_id: "approval-1",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/review",
      effect: "external_write",
      reason: "Publish the reviewed patch.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const nextApproval: ApprovalRequest = {
      ...firstApproval,
      approval_request_id: "approval-2",
      revision: 1,
      target: "github.com/acme/switchback:refs/heads/next",
    };
    const view = renderWorker({
      worker: worker({ artifacts: [], approvals: [firstApproval] }),
      availableActions: [{ action: "worker.approval", target_id: "approval-1", expected_revision: 4 }],
      onCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));
    await waitFor(() => expect(onCommand).toHaveBeenCalledOnce());

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], approvals: [nextApproval] })}
          availableActions={[{ action: "worker.approval", target_id: "approval-2", expected_revision: 1 }]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    expect(screen.getByText("github.com/acme/switchback:refs/heads/next")).toBeInTheDocument();

    request.reject(new GatewayCommandError("network_error"));

    await waitFor(() => expect(screen.getByRole("button", { name: "Approve once" })).toBeEnabled());
  });

  it("treats an exact approval A after A-B-A as a new authority incarnation", async () => {
    const firstRequest = deferred<{ command_id: string; status: "accepted" }>();
    const currentRequest = deferred<{ command_id: string; status: "accepted" }>();
    const onCommand = vi.fn()
      .mockReturnValueOnce(firstRequest.promise)
      .mockReturnValueOnce(currentRequest.promise);
    const approvalA: ApprovalRequest = {
      approval_request_id: "approval-aba",
      revision: 4,
      capability: "github.branch_write",
      target: "github.com/acme/switchback:refs/heads/aba",
      effect: "external_write",
      reason: "Publish the reviewed patch.",
      expires_at: "2099-07-26T09:00:00Z",
      scope_options: ["once"],
    };
    const actionA = [{ action: "worker.approval", target_id: "approval-aba", expected_revision: 4 }];
    const view = renderWorker({
      worker: worker({ artifacts: [], approvals: [approvalA] }),
      availableActions: actionA,
      onCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));
    await waitFor(() => expect(onCommand).toHaveBeenCalledOnce());

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], approvals: [] })}
          availableActions={[]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], approvals: [{ ...approvalA }] })}
          availableActions={[{ ...actionA[0] }]}
          onCommand={onCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );

    const currentApprove = await screen.findByRole("button", { name: "Approve once" });
    expect(currentApprove).toBeEnabled();
    fireEvent.click(currentApprove);
    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    await act(async () => {
      currentRequest.resolve({ command_id: "command-current", status: "accepted" });
      await currentRequest.promise;
    });
    expect(screen.getByRole("button", { name: "Approve once" })).toBeEnabled();

    await act(async () => {
      firstRequest.reject(new GatewayCommandError("network_error"));
      await firstRequest.promise.catch(() => undefined);
    });

    expect(screen.queryByRole("button", { name: "Retry approval decision" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve once" })).toBeEnabled();
  });

  it("rejects a stale unknown Task retry outcome after the exact Task authority disappears", async () => {
    const retryRequest = deferred<{ command_id: string; status: "accepted" }>();
    const onTaskCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockReturnValueOnce(retryRequest.promise);
    const view = renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: [
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
        {
          action: "task.continue",
          target_id: "task-switchback-counter",
          expected_revision: 8,
          metadata: { interaction_kinds: ["chat"] },
        },
      ],
      onTaskCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 9 })}
          availableActions={[{
            action: "task.continue",
            target_id: "task-switchback-counter",
            expected_revision: 9,
            metadata: { interaction_kinds: ["chat"] },
          }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Continue under the new Task authority" },
    });

    await act(async () => {
      retryRequest.reject(new GatewayCommandError("network_error"));
      await retryRequest.promise.catch(() => undefined);
    });

    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("evicts a deferred Task retry across A-B-A before its late unknown outcome", async () => {
    const retryRequest = deferred<{ command_id: string; status: "accepted" }>();
    const onTaskCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockReturnValueOnce(retryRequest.promise)
      .mockResolvedValueOnce({ command_id: "command-current", status: "accepted" });
    const actionA = [{ action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 }];
    const view = renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: actionA,
      onTaskCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 9 })}
          availableActions={[{ action: "task.accept", target_id: "task-switchback-counter", expected_revision: 9 }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 8 })}
          availableActions={[{ ...actionA[0] }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    expect(await screen.findByRole("button", { name: "Accept" })).toBeEnabled();

    await act(async () => {
      retryRequest.reject(new GatewayCommandError("network_error"));
      await retryRequest.promise.catch(() => undefined);
    });

    expect(screen.queryByRole("button", { name: "Retry Task decision" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(3));
  });

  it("does not let a stale successful Task retry clear the current A-B-A fence", async () => {
    const retryRequest = deferred<{ command_id: string; status: "accepted" }>();
    const currentRequest = deferred<{ command_id: string; status: "accepted" }>();
    const onTaskCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockReturnValueOnce(retryRequest.promise)
      .mockReturnValueOnce(currentRequest.promise);
    const actionA = [{ action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 }];
    const view = renderWorker({
      worker: worker({ artifacts: [] }),
      availableActions: actionA,
      onTaskCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    const retry = await screen.findByRole("button", { name: "Retry Task decision" });
    await waitFor(() => expect(retry).toBeEnabled());
    fireEvent.click(retry);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(2));

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 9 })}
          availableActions={[{ action: "task.accept", target_id: "task-switchback-counter", expected_revision: 9 }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], task_revision: 8 })}
          availableActions={[{ ...actionA[0] }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const currentAccept = await screen.findByRole("button", { name: "Accept" });
    expect(currentAccept).toBeEnabled();
    fireEvent.click(currentAccept);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(3));
    await act(async () => {
      currentRequest.resolve({ command_id: "command-current", status: "accepted" });
      await currentRequest.promise;
    });
    expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled();

    await act(async () => {
      retryRequest.resolve({ command_id: "command-stale-retry", status: "accepted" });
      await retryRequest.promise;
    });

    expect(screen.queryByRole("button", { name: "Retry Task decision" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled();
  });

  it.each(["initial accept", "same-key retry"] as const)(
    "remounts Task review authority on Session A to B and isolates a late deferred %s settlement",
    async (attemptKind) => {
    const sessionA = deferred<{ command_id: string; status: "accepted" }>();
    const sessionB = deferred<{ command_id: string; status: "accepted" }>();
    const onTaskCommand = attemptKind === "same-key retry"
      ? vi.fn()
        .mockRejectedValueOnce(new GatewayCommandError("network_error"))
        .mockReturnValueOnce(sessionA.promise)
        .mockReturnValueOnce(sessionB.promise)
      : vi.fn()
        .mockReturnValueOnce(sessionA.promise)
        .mockReturnValueOnce(sessionB.promise);
    const taskAction = {
      action: "task.accept",
      target_id: "task-switchback-counter",
      expected_revision: 8,
    };
    const view = renderWorker({
      worker: worker({ artifacts: [], worker_session_id: "session-a" }),
      availableActions: [taskAction],
      onTaskCommand,
      transient: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    if (attemptKind === "same-key retry") {
      const retry = await screen.findByRole("button", { name: "Retry Task decision" });
      await waitFor(() => expect(retry).toBeEnabled());
      fireEvent.click(retry);
    }
    const callsBeforeSwitch = attemptKind === "same-key retry" ? 2 : 1;
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(callsBeforeSwitch));

    view.rerender(
      <ThemeProvider>
        <WorkerDetailPage
          worker={worker({ artifacts: [], worker_session_id: "session-b" })}
          availableActions={[{ ...taskAction }]}
          onTaskCommand={onTaskCommand}
          transient={[]}
        />
      </ThemeProvider>,
    );
    const acceptB = screen.getByRole("button", { name: "Accept" });
    expect(acceptB).toBeEnabled();
    fireEvent.click(acceptB);
    await waitFor(() => expect(onTaskCommand).toHaveBeenCalledTimes(callsBeforeSwitch + 1));

    await act(async () => {
      sessionB.resolve({ command_id: "command-session-b", status: "accepted" });
      await sessionB.promise;
    });
    expect(acceptB).toBeEnabled();

    await act(async () => {
      sessionA.reject(new GatewayCommandError("network_error"));
      await sessionA.promise.catch(() => undefined);
    });

    expect(screen.queryByRole("button", { name: "Retry Task decision" })).not.toBeInTheDocument();
    expect(acceptB).toBeEnabled();
    },
  );
});
