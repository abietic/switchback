import { describe, expect, it, vi } from "vitest";
import {
  assistantConversationPath,
  confirmAssistantTask,
  createAssistantConversation,
  sendAssistantMessage,
} from "./assistantCommands";
import { GatewayCommandError } from "./workerCommands";

const confirmation = {
  conversationId: "conversation-1", conversationRevision: 8, taskDraftId: "draft-1", taskDraftRevision: 3,
  readinessAssessmentId: "assessment-1", readinessAssessmentRevision: 5, workspaceId: "workspace-local",
  workspaceRevisionId: "workspace-revision-local-v1", workspaceResolutionId: "resolution-local",
  workspaceResolutionRevision: 2, membershipRevision: 4,
  taskSpecDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", triggerPolicyRevision: "assistant-trigger-v1",
};

describe("Assistant command client", () => {
  it("submits exact confirmation evidence to the canonical route", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      command_id: "command-confirm-1", conversation_id: "conversation-1", conversation_revision: 8,
      projection_ref: "/api/v1/views/assistant_conversation/conversation-1", status: "accepted",
    }), { status: 202 }));
    await expect(confirmAssistantTask(fetcher, { ...confirmation, idempotencyKey: "confirm-retry-1" })).resolves.toMatchObject({ command_id: "command-confirm-1" });
    expect(fetcher).toHaveBeenCalledWith("/api/v1/assistant/conversations/conversation-1/task-drafts/draft-1/confirm", expect.objectContaining({ body: JSON.stringify({
      schema_version: "assistant.conversation_command.v1", command_type: "confirm_task", conversation_id: "conversation-1", expected_revision: 8,
      idempotency_key: "confirm-retry-1", task_draft_id: "draft-1", task_draft_revision: 3, readiness_assessment_id: "assessment-1",
      readiness_assessment_revision: 5, workspace_id: "workspace-local", workspace_revision_id: "workspace-revision-local-v1",
      workspace_resolution_id: "resolution-local", workspace_resolution_revision: 2, membership_revision: 4,
      task_spec_digest: confirmation.taskSpecDigest, trigger_policy_revision: "assistant-trigger-v1",
    }) }));
  });

  it("keeps a retry idempotency key stable and maps stale responses without raw diagnostics", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ code: "stale_target", detail: "provider payload must not escape" }), { status: 409 }));
    await expect(sendAssistantMessage(fetcher, { conversationId: "conversation-1", expectedRevision: 8, content: "Continue", idempotencyKey: "message-retry-1" })).rejects.toMatchObject({ code: "stale_target" });
    await expect(sendAssistantMessage(fetcher, { conversationId: "conversation-1", expectedRevision: 8, content: "Continue", idempotencyKey: "message-retry-1" })).rejects.toBeInstanceOf(GatewayCommandError);
    expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body)).idempotency_key).toBe("message-retry-1");
  });

  it("accepts only a canonical Assistant projection identity", () => {
    expect(assistantConversationPath({ conversation_id: "conversation-1", projection_ref: "/api/v1/views/assistant_conversation/conversation-1" })).toBe("/assistant/conversation-1");
    expect(() => assistantConversationPath({ conversation_id: "conversation-1", projection_ref: "/api/v1/views/task_summary/task-1" })).toThrow(GatewayCommandError);
  });

  it.each([
    ["empty conversation id", { conversation_id: "", conversation_revision: 1 }],
    ["non-positive conversation revision", { conversation_id: "conversation-1", conversation_revision: 0 }],
  ])("fails closed for an accepted result with %s", async (_label, invalidIdentity) => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      command_id: "command-invalid",
      projection_ref: `/api/v1/views/assistant_conversation/${invalidIdentity.conversation_id}`,
      status: "accepted",
      ...invalidIdentity,
    }), { status: 202 }));

    await expect(createAssistantConversation(fetcher, "create-invalid-1")).rejects.toBeInstanceOf(GatewayCommandError);
  });
});
