import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { GatewayCommandError, type CommandResult } from "../../../api/workerCommands";
import { WorkerCommandComposer } from "./WorkerCommandComposer";

const target = {
  workerSessionId: "session-1",
  expectedSessionRevision: 14,
  workerRunId: "run-3",
  expectedRunRevision: 7,
  agentTurnId: "turn-5",
  expectedTurnRevision: 3,
};
const actions = [
  { action: "worker.follow_up", target_id: "session-1", expected_revision: 14 },
  { action: "worker.queue", target_id: "session-1", expected_revision: 14 },
  { action: "worker.steer", target_id: "turn-5", expected_revision: 3 },
  { action: "worker.interrupt", target_id: "turn-5", expected_revision: 3 },
];

describe("WorkerCommandComposer", () => {
  it("submits the selected queue action against the exact projected target", async () => {
    vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000001");
    const onCommand = vi.fn().mockResolvedValue({ command_id: "command-1", status: "accepted", queue_sequence: 2 });
    render(<WorkerCommandComposer target={target} availableActions={actions} pendingCount={0} onCommand={onCommand} />);

    fireEvent.click(screen.getByRole("button", { name: "Queue after current turn" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), { target: { value: "Run browser tests" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onCommand).toHaveBeenCalledWith(expect.objectContaining({
      action: "queue",
      target,
      content: "Run browser tests",
      idempotencyKey: "web-00000000-0000-4000-8000-000000000001",
    })));
    expect(await screen.findByText("Queued at position 2.")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Continue with this worker" })).toHaveValue("");
  });

  it("offers queue or interrupt when steer is rejected as unsupported", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("unsupported_command"));
    render(<WorkerCommandComposer target={target} availableActions={actions} pendingCount={0} onCommand={onCommand} />);
    fireEvent.click(screen.getByRole("button", { name: "Steer active turn" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), { target: { value: "Focus on tests" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Steer is unavailable. Queue a message or interrupt the active turn instead.");
    expect(screen.getByRole("button", { name: "Queue after current turn" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Interrupt active turn" })).toBeEnabled();
  });

  it("retries an unknown delivery with the original idempotency key", async () => {
    vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000003");
    const onCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockResolvedValueOnce({ command_id: "command-replayed", status: "accepted" });
    render(<WorkerCommandComposer target={target} availableActions={actions} pendingCount={0} onCommand={onCommand} />);
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), { target: { value: "Continue safely" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));

    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    expect(onCommand.mock.calls[0][0].idempotencyKey).toBe("web-00000000-0000-4000-8000-000000000003");
    expect(onCommand.mock.calls[1][0].idempotencyKey).toBe(onCommand.mock.calls[0][0].idempotencyKey);
  });

  it("keeps an ambiguous-delivery retry visible but disabled when authority is no longer live", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = render(<WorkerCommandComposer
      target={target}
      availableActions={actions}
      pendingCount={0}
      mutationBlocked={() => false}
      onCommand={onCommand}
    />);
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Retry only while authoritative" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled();

    view.rerender(<WorkerCommandComposer
      target={target}
      availableActions={actions}
      pendingCount={0}
      mutationBlocked={() => true}
      onCommand={onCommand}
    />);
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
  });

  it("keeps an ambiguous-delivery retry disabled when the exact ActionRef becomes stale", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = render(<WorkerCommandComposer
      target={target}
      availableActions={actions}
      pendingCount={0}
      onCommand={onCommand}
    />);
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Retry only against the same authority" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled();

    view.rerender(<WorkerCommandComposer
      target={target}
      availableActions={[{ ...actions[0], expected_revision: 13 }]}
      pendingCount={0}
      onCommand={onCommand}
    />);
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
  });

  it("preserves the draft, refreshes stale authority, and requires an explicit resend", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("stale_target"));
    const onStale = vi.fn().mockResolvedValue(undefined);
    render(<WorkerCommandComposer
      target={target}
      availableActions={actions}
      pendingCount={0}
      onCommand={onCommand}
      onStale={onStale}
    />);
    const composer = screen.getByRole("textbox", { name: "Continue with this worker" });
    fireEvent.change(composer, { target: { value: "Keep this exact draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("review your draft and send it again");
    expect(composer).toHaveValue("Keep this exact draft");
    expect(onStale).toHaveBeenCalledOnce();
    expect(onCommand).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("does not clear a newer draft when an earlier command is accepted", async () => {
    let resolveAccepted!: (result: CommandResult) => void;
    const accepted = new Promise<CommandResult>((resolve) => {
      resolveAccepted = resolve;
    });
    const onCommand = vi.fn().mockReturnValue(accepted);
    render(<WorkerCommandComposer target={target} availableActions={actions} pendingCount={0} onCommand={onCommand} />);
    const composer = screen.getByRole("textbox", { name: "Continue with this worker" });
    fireEvent.change(composer, { target: { value: "Submitted draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    fireEvent.change(composer, { target: { value: "New draft while request is pending" } });

    resolveAccepted({ command_id: "command-accepted", status: "accepted" });
    await waitFor(() => expect(onCommand).toHaveBeenCalledOnce());
    await waitFor(() => expect(composer).toHaveValue("New draft while request is pending"));
  });

  it("removes command controls when their exact projected actions disappear", () => {
    const onCommand = vi.fn();
    const view = render(<WorkerCommandComposer target={target} availableActions={actions} pendingCount={0} onCommand={onCommand} />);
    expect(screen.getByRole("button", { name: "Queue after current turn" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Steer active turn" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Interrupt active turn" })).toBeInTheDocument();

    view.rerender(<WorkerCommandComposer
      target={target}
      availableActions={[actions[0]]}
      pendingCount={0}
      onCommand={onCommand}
    />);
    expect(screen.queryByRole("button", { name: "Queue after current turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Steer active turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Interrupt active turn" })).not.toBeInTheDocument();
  });

  it("fails closed when same-named Worker actions target another owner revision", () => {
    const onCommand = vi.fn();
    render(<WorkerCommandComposer
      target={target}
      availableActions={[
        { action: "worker.follow_up", target_id: "session-other", expected_revision: 14 },
        { action: "worker.queue", target_id: "session-1", expected_revision: 13 },
        { action: "worker.steer", target_id: "turn-other", expected_revision: 3 },
        { action: "worker.interrupt", target_id: "turn-5", expected_revision: 2 },
      ]}
      pendingCount={0}
      onCommand={onCommand}
    />);

    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Do not send against stale authority" },
    });
    expect(screen.queryByRole("button", { name: "Start a follow-up turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Queue after current turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Steer active turn" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Interrupt active turn" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(onCommand).not.toHaveBeenCalled();
  });

  it("requires exact Task authority with an explicit chat interaction kind", () => {
    const taskContinuationTarget = {
      taskId: "task-1",
      expectedTaskRevision: 9,
      ...target,
    };
    const onTaskContinue = vi.fn();
    const view = render(<WorkerCommandComposer
      target={target}
      taskContinuationTarget={taskContinuationTarget}
      availableActions={[{ action: "task.continue", target_id: "task-other", expected_revision: 9 }]}
      pendingCount={0}
      onCommand={vi.fn()}
      onTaskContinue={onTaskContinue}
    />);
    fireEvent.change(screen.getByRole("textbox", { name: "Continue with this worker" }), {
      target: { value: "Continue the exact Task only" },
    });
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Continue the delivered Task" })).not.toBeInTheDocument();

    view.rerender(<WorkerCommandComposer
      target={target}
      taskContinuationTarget={taskContinuationTarget}
      availableActions={[{ action: "task.continue", target_id: "task-1", expected_revision: 9 }]}
      pendingCount={0}
      onCommand={vi.fn()}
      onTaskContinue={onTaskContinue}
    />);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Continue the delivered Task" })).not.toBeInTheDocument();

    view.rerender(<WorkerCommandComposer
      target={target}
      taskContinuationTarget={taskContinuationTarget}
      availableActions={[{
        action: "task.continue",
        target_id: "task-1",
        expected_revision: 9,
        metadata: { interaction_kinds: ["rework"] },
      }]}
      pendingCount={0}
      onCommand={vi.fn()}
      onTaskContinue={onTaskContinue}
    />);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Continue the delivered Task" })).not.toBeInTheDocument();

    view.rerender(<WorkerCommandComposer
      target={target}
      taskContinuationTarget={taskContinuationTarget}
      availableActions={[{
        action: "task.continue",
        target_id: "task-1",
        expected_revision: 9,
        metadata: { interaction_kinds: ["chat"] },
      }]}
      pendingCount={0}
      onCommand={vi.fn()}
      onTaskContinue={onTaskContinue}
    />);
    expect(screen.getByRole("button", { name: "Continue the delivered Task" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });
});
