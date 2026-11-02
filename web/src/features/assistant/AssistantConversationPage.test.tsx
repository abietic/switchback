import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AssistantConversationData } from "../../api/contracts";
import { AssistantConversationPage } from "./AssistantConversationPage";

function conversation(overrides: Partial<AssistantConversationData> = {}): AssistantConversationData {
  return {
    conversation_id: "conversation-1", revision: 8, generation: 1, status: "active", workspace_id: "workspace-local", updated_at: "2026-07-25T00:00:00Z",
    turns: [{ turn_id: "turn-2", sequence: 2, role: "assistant", content: "Which repository area should this change cover?", created_at: "2026-07-25T00:01:00Z" }, { turn_id: "turn-1", sequence: 1, role: "user", content: "Improve the assistant route.", created_at: "2026-07-25T00:00:00Z" }],
    assistant_run: { assistant_run_id: "run-1", status: "failed", revision: 3, error_code: "model_timeout", retryable: true },
    readiness: { assessment_id: "assessment-1", revision: 5, status: "ready_with_assumptions", dimensions: [], blocking_unknowns: [], assumptions: ["Use the existing visual system."], clarification_questions: 1 },
    task_draft: { task_draft_id: "draft-1", revision: 3, status: "current", assumptions: [], task_spec: {
      title: "Assistant route", goal: "Deliver the focused route.", workspace_id: "workspace-local",
      workspace_resolution_ref: { resolution_id: "resolution-1", revision: 2, workspace_revision_id: "workspace-revision-1", membership_revision: 3 },
      readiness_assessment_ref: { assessment_id: "assessment-1", policy_revision: "assistant-readiness-v1", content_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
      creation_intent_ref: { intent_id: "pending-confirmation", task_spec_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", trigger_policy_revision: "assistant-trigger-v1" },
      target: { kind: "workspace", ref: "workspace-local" }, acceptance_criteria: ["Run browser coverage"], constraints: ["Do not edit backend files."], assumptions: [],
      agent_config: { profile_id: "codex-deepseek-v3.2", profile_revision: "1", adapter: "codex-acp" },
      runtime_config: { network_policy: "m1_public_web_restricted_v1" },
    } },
    handoffs: [],
    ...overrides,
  };
}

const actions = [
  { action: "assistant.send_message", target_id: "conversation-1", expected_revision: 8 },
  { action: "assistant.confirm_task", target_id: "draft-1", expected_revision: 3 },
  { action: "assistant.retry_run", target_id: "run-1", expected_revision: 3 },
];

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("AssistantConversationPage", () => {
  it("orders visible turns and renders only human-readable TaskDraft fields", () => {
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    const turns = screen.getAllByText(/Improve the assistant route|Which repository area/).map((node) => node.textContent);
    expect(turns).toEqual(["Improve the assistant route.", "Which repository area should this change cover?"]);
    expect(screen.getByText("Assistant route")).toBeInTheDocument();
    expect(screen.queryByText(/task_spec/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Confirm exact draft/ })).toBeEnabled();
  });

  it("marks the focused thread as primary and readiness context as secondary", () => {
    const { container } = render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    expect(container.querySelector('[data-layout="assistant-focused"]')).toBeInTheDocument();
    expect(container.querySelector('[data-region="primary"]')).toHaveAttribute("aria-label", "Assistant conversation");
    expect(container.querySelector('[data-region="secondary"]')).toHaveAttribute("aria-label", "Assistant readiness and context");
  });

  it("disables confirmation when exact binding becomes stale and keeps the direct Task path external", () => {
    const staleActions = actions.map((action) => action.action === "assistant.confirm_task" ? { ...action, expected_revision: 2 } : action);
    render(<AssistantConversationPage data={conversation()} actions={staleActions} relatedRefs={[]} pendingCommands={[]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    expect(screen.getByText("Confirmation is unavailable until the current exact draft is ready.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Confirm exact draft/ })).toBeNull();
  });

  it("does not expose confirmation without the exact projected action", () => {
    render(<AssistantConversationPage data={conversation()} actions={actions.filter((action) => action.action !== "assistant.confirm_task")} relatedRefs={[]} pendingCommands={[]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /Confirm exact draft/ })).toBeNull();
    expect(screen.getByText("Confirmation is unavailable until the current exact draft is ready.")).toBeInTheDocument();
  });

  it("keeps the trusted draft and non-conflicting composer available while confirmation is pending", () => {
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[{ commandId: "confirm-1", action: "confirm_task", submittedAt: "2026-07-25T00:02:00Z" }]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    expect(screen.getByText("Assistant route")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Confirm exact draft/ })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Message the Assistant" })).toBeEnabled();
  });

  it("freezes old-revision mutations while Workspace selection is pending", () => {
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[{ commandId: "workspace-1", action: "select_workspace", submittedAt: "2026-07-25T00:02:00Z" }]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    const composer = screen.getByRole("textbox", { name: "Message the Assistant" });
    fireEvent.change(composer, { target: { value: "Keep this editable" } });
    expect(composer).toBeEnabled();
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Retry this response" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Confirm exact draft/ })).toBeDisabled();
  });

  it("requires the user to enter explicit Workspace evidence", () => {
    render(<AssistantConversationPage data={conversation({ workspace_id: undefined, task_draft: undefined })} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Select workspace" }));
    expect(screen.getByLabelText("Workspace")).toHaveValue("");
    expect(screen.getByLabelText("Workspace revision")).toHaveValue("");
    expect(screen.getByLabelText("Resolution")).toHaveValue("");
    expect(screen.getByLabelText("Resolution revision")).toHaveValue(null);
    expect(screen.getByLabelText("Membership revision")).toHaveValue(null);
    expect(screen.getByRole("button", { name: "Use exact workspace" })).toBeDisabled();

    fireEvent.change(screen.getByLabelText("Workspace"), { target: { value: "workspace-explicit" } });
    fireEvent.change(screen.getByLabelText("Workspace revision"), { target: { value: "workspace-revision-explicit" } });
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "resolution-explicit" } });
    fireEvent.change(screen.getByLabelText("Resolution revision"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Membership revision"), { target: { value: "7" } });
    expect(screen.getByRole("button", { name: "Use exact workspace" })).toBeEnabled();
  });

  it("binds checkpoint actions to the projection-authorized choice", () => {
    const onCommand = vi.fn().mockResolvedValue(undefined);
    render(<AssistantConversationPage data={conversation({ checkpoint: { checkpoint_id: "checkpoint-1", revision: 4, status: "active", working_draft: {}, allowed_actions: ["continue", "narrow_scope"] } })} actions={[...actions, { action: "assistant.checkpoint", target_id: "checkpoint-1", expected_revision: 4 }]} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    const panel = screen.getByRole("heading", { name: "Clarification" }).closest("section");
    expect(panel).not.toBeNull();
    expect(within(panel!).getAllByRole("button").map((button) => button.textContent)).toEqual(["Continue", "Narrow scope"]);
    fireEvent.click(screen.getByRole("button", { name: "Narrow scope" }));
    expect(onCommand).toHaveBeenCalledWith(
      "checkpoint_narrow_scope",
      expect.stringMatching(/^web-assistant-/),
      expect.any(Function),
    );
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  it("freezes every checkpoint choice while one checkpoint action is pending", () => {
    render(<AssistantConversationPage
      data={conversation({ checkpoint: { checkpoint_id: "checkpoint-1", revision: 4, status: "active", working_draft: {}, allowed_actions: ["continue", "narrow_scope", "cancel"] } })}
      actions={[...actions, { action: "assistant.checkpoint", target_id: "checkpoint-1", expected_revision: 4 }]}
      relatedRefs={[]}
      pendingCommands={[{ commandId: "checkpoint-command-1", action: "checkpoint_continue", submittedAt: "2026-07-25T00:02:00Z" }]}
      onCommand={vi.fn()}
      onNavigate={vi.fn()}
    />);
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Narrow scope" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  });

  it("fences a confirmation before its HTTP response returns", async () => {
    const request = deferred<void>();
    const onCommand = vi.fn(() => request.promise);
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    const confirm = screen.getByRole("button", { name: /Confirm exact draft/ });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(onCommand).toHaveBeenCalledTimes(1);
    expect(confirm).toBeDisabled();
    await act(async () => {
      request.resolve();
      await request.promise;
    });
  });

  it("keeps the local fence until an accepted command becomes projection-pending", async () => {
    const request = deferred<void>();
    const onCommand = vi.fn(() => request.promise);
    const view = render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    const confirm = screen.getByRole("button", { name: /Confirm exact draft/ });
    fireEvent.click(confirm);
    await act(async () => {
      request.resolve();
      await request.promise;
    });
    expect(confirm).toBeDisabled();

    view.rerender(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[{ commandId: "confirm-accepted", action: "confirm_task", submittedAt: "2026-07-25T00:02:00Z" }]} onCommand={onCommand} onNavigate={vi.fn()} />);
    expect(confirm).toBeDisabled();
    view.rerender(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    await waitFor(() => expect(confirm).toBeEnabled());
  });

  it("clears only the submitted message snapshot after HTTP acceptance", async () => {
    const request = deferred<void>();
    const onCommand = vi.fn(() => request.promise);
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    const composer = screen.getByRole("textbox", { name: "Message the Assistant" });
    fireEvent.change(composer, { target: { value: "Submitted snapshot" } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    fireEvent.change(composer, { target: { value: "Typed after submit" } });
    await act(async () => {
      request.resolve();
      await request.promise;
    });
    await waitFor(() => expect(composer).toHaveValue("Typed after submit"));
    expect(onCommand).toHaveBeenCalledTimes(1);
  });

  it("clears an unchanged submitted message after HTTP acceptance", async () => {
    const request = deferred<void>();
    const onCommand = vi.fn(() => request.promise);
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    const composer = screen.getByRole("textbox", { name: "Message the Assistant" });
    fireEvent.change(composer, { target: { value: "Submitted snapshot" } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await act(async () => {
      request.resolve();
      await request.promise;
    });
    await waitFor(() => expect(composer).toHaveValue(""));
  });

  it("shows failed-run truth and pending projection truth together", () => {
    render(<AssistantConversationPage data={conversation()} actions={actions} relatedRefs={[]} pendingCommands={[{ commandId: "retry-accepted", action: "retry", submittedAt: "2026-07-25T00:02:00Z" }]} onCommand={vi.fn()} onNavigate={vi.fn()} />);
    expect(screen.getByText("The response timed out. You can retry safely.")).toBeInTheDocument();
    expect(screen.getByText("Command pending projection")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry this response" })).toBeDisabled();
  });

  it("sanitizes failed-run diagnostics before offering an exact retry", () => {
    const onCommand = vi.fn().mockResolvedValue(undefined);
    render(<AssistantConversationPage data={conversation({ assistant_run: { assistant_run_id: "run-1", status: "failed", revision: 3, error_code: "provider_secret=do-not-render", retryable: true } })} actions={actions} relatedRefs={[]} pendingCommands={[]} onCommand={onCommand} onNavigate={vi.fn()} />);
    expect(screen.getByText("The Assistant could not finish this response.")).toBeInTheDocument();
    expect(screen.queryByText(/provider_secret|do-not-render/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry this response" }));
    expect(onCommand).toHaveBeenCalledWith(
      "retry",
      expect.stringMatching(/^web-assistant-/),
      expect.any(Function),
    );
  });

  it("uses canonical Task and exact Worker routes for projected handoffs", () => {
    const onNavigate = vi.fn();
    render(<AssistantConversationPage
      data={conversation({ handoffs: [{ task_id: "task-9", status: "running", worker_session_id: "worker-9" }] })}
      actions={actions}
      relatedRefs={[
        { type: "task_summary", id: "task-9", href: "/api/v1/views/task_summary/task-9" },
        { type: "worker_detail", id: "worker-9", href: "/api/v1/views/worker_detail/worker-9" },
      ]}
      pendingCommands={[]}
      onCommand={vi.fn()}
      onNavigate={onNavigate}
    />);
    fireEvent.click(screen.getByRole("button", { name: "Open task task-9" }));
    expect(onNavigate).toHaveBeenLastCalledWith("/tasks");
    fireEvent.click(screen.getByRole("button", { name: "Open worker worker-9" }));
    expect(onNavigate).toHaveBeenLastCalledWith("/workers/worker-9");
  });
});
