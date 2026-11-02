import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "../../../api/contracts";
import { GatewayCommandError } from "../../../api/workerCommands";
import { ApprovalControls } from "./ApprovalControls";

const approval: ApprovalRequest = {
  approval_request_id: "approval-1",
  revision: 4,
  capability: "github.branch_write",
  target: "github.com/acme/switchback:refs/heads/fix-counter",
  effect: "external_write",
  reason: "Publish the reviewed patch as a branch.",
  expires_at: "2099-07-23T12:00:00Z",
  scope_options: ["once", "session"],
};
const commandTarget = {
  workerSessionId: "session-1",
  expectedSessionRevision: 14,
  workerRunId: "run-3",
  expectedRunRevision: 7,
  agentTurnId: "turn-5",
  expectedTurnRevision: 3,
};
const approvalActions = [{ action: "worker.approval", target_id: "approval-1", expected_revision: 4 }];

describe("ApprovalControls", () => {
  it("submits the exact projected approval boundary", async () => {
    vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000002");
    const onCommand = vi.fn().mockResolvedValue({ command_id: "command-approval", status: "accepted" });
    render(<ApprovalControls approvals={[approval]} target={commandTarget} availableActions={approvalActions} onCommand={onCommand} />);

    expect(screen.getByText("github.branch_write")).toBeInTheDocument();
    expect(screen.getByText("github.com/acme/switchback:refs/heads/fix-counter")).toBeInTheDocument();
    expect(screen.getByText("Expires 2099-07-23 12:00 UTC")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));

    await waitFor(() => expect(onCommand).toHaveBeenCalledWith(expect.objectContaining({
      action: "approval",
      target: commandTarget,
      approval: {
        approval_request_id: "approval-1",
        expected_approval_revision: 4,
        decision: "approve",
        scope: "once",
        capability: "github.branch_write",
        target: "github.com/acme/switchback:refs/heads/fix-counter",
        effect: "external_write",
      },
    })));
  });

  it("removes stale actions as soon as the projected request disappears", () => {
    const onCommand = vi.fn();
    const { rerender } = render(<ApprovalControls approvals={[approval]} target={commandTarget} availableActions={approvalActions} onCommand={onCommand} />);
    expect(screen.getByRole("button", { name: "Approve for session" })).toBeEnabled();

    rerender(<ApprovalControls approvals={[{ ...approval, revision: 5 }]} target={commandTarget} availableActions={approvalActions} onCommand={onCommand} />);
    expect(screen.getByRole("button", { name: "Approve for session" })).toBeDisabled();

    rerender(<ApprovalControls approvals={[]} target={commandTarget} availableActions={[]} onCommand={onCommand} />);
    expect(screen.queryByRole("button", { name: "Approve for session" })).not.toBeInTheDocument();
    expect(screen.queryByText("github.branch_write")).not.toBeInTheDocument();
  });

  it("disables actions for an expired request", () => {
    render(<ApprovalControls
      approvals={[{ ...approval, expires_at: "2000-01-01T00:00:00Z" }]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={vi.fn()}
    />);

    expect(screen.getByRole("button", { name: "Approve once" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Approve for session" })).toBeDisabled();
    expect(screen.getByText("This request expired. Wait for a refreshed approval request.")).toBeInTheDocument();
  });

  it("retries an unknown approval decision with the same intent and fences other decisions", async () => {
    const randomUUID = vi.spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000051")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000052");
    randomUUID.mockClear();
    const onCommand = vi.fn()
      .mockRejectedValueOnce(new GatewayCommandError("network_error"))
      .mockResolvedValueOnce({ command_id: "command-approval", status: "accepted" });
    render(<ApprovalControls
      approvals={[approval]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));

    const retry = await screen.findByRole("button", { name: "Retry approval decision" });
    expect(screen.queryByRole("button", { name: "Abandon approval retry" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Approve once" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Approve for session" })).toBeDisabled();
    fireEvent.click(retry);

    await waitFor(() => expect(onCommand).toHaveBeenCalledTimes(2));
    expect(onCommand.mock.calls[1][0]).toEqual(onCommand.mock.calls[0][0]);
    expect(randomUUID).toHaveBeenCalledOnce();
  });

  it("keeps an unknown approval under unchanged authority and discards it after revision changes", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = render(<ApprovalControls
      approvals={[approval]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));

    expect(await screen.findByRole("button", { name: "Retry approval decision" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Abandon approval retry" })).not.toBeInTheDocument();
    view.rerender(<ApprovalControls
      approvals={[{ ...approval }]}
      target={{ ...commandTarget }}
      availableActions={[{ ...approvalActions[0] }]}
      onCommand={onCommand}
    />);
    expect(screen.getByRole("button", { name: "Retry approval decision" })).toBeEnabled();

    view.rerender(<ApprovalControls
      approvals={[{ ...approval, revision: 5 }]}
      target={commandTarget}
      availableActions={[{ action: "worker.approval", target_id: "approval-1", expected_revision: 5 }]}
      onCommand={onCommand}
    />);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry approval decision" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Approve once" })).toBeEnabled();
  });

  it("invalidates an unknown session-scope retry when that exact scope is removed", async () => {
    const onCommand = vi.fn().mockRejectedValue(new GatewayCommandError("network_error"));
    const view = render(<ApprovalControls
      approvals={[approval]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);
    fireEvent.click(screen.getByRole("button", { name: "Approve for session" }));
    expect(await screen.findByRole("button", { name: "Retry approval decision" })).toBeEnabled();

    view.rerender(<ApprovalControls
      approvals={[{ ...approval, scope_options: ["once"] }]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);

    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry approval decision" })).not.toBeInTheDocument());
  });

  it("fails closed when projected approval scopes are missing or malformed", () => {
    const onCommand = vi.fn();
    const view = render(<ApprovalControls
      approvals={[{ ...approval, scope_options: undefined as unknown as ApprovalRequest["scope_options"] }]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);

    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Approve once" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Approve for session" })).not.toBeInTheDocument();

    view.rerender(<ApprovalControls
      approvals={[{ ...approval, scope_options: ["unexpected"] as unknown as ApprovalRequest["scope_options"] }]}
      target={commandTarget}
      availableActions={approvalActions}
      onCommand={onCommand}
    />);
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Approve once" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Approve for session" })).not.toBeInTheDocument();
    expect(onCommand).not.toHaveBeenCalled();
  });
});
