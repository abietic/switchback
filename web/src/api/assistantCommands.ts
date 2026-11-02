import { GatewayCommandError, type GatewayCommandErrorCode } from "./workerCommands";

export interface AssistantCommandResult {
  command_id: string;
  conversation_id: string;
  conversation_revision: number;
  assistant_run_id?: string;
  assistant_run_revision?: number;
  task_id?: string;
  task_revision?: number;
  projection_ref: string;
  status: "accepted" | "completed";
}

export interface WorkspaceSelection {
  workspaceId: string;
  workspaceRevisionId: string;
  workspaceResolutionId: string;
  workspaceResolutionRevision: number;
  membershipRevision: number;
}

export interface ConfirmationTarget extends WorkspaceSelection {
  conversationId: string;
  conversationRevision: number;
  taskDraftId: string;
  taskDraftRevision: number;
  readinessAssessmentId: string;
  readinessAssessmentRevision: number;
  taskSpecDigest: string;
  triggerPolicyRevision: string;
}

export async function createAssistantConversation(fetcher: typeof fetch, idempotencyKey: string): Promise<AssistantCommandResult> {
  return submit(fetcher, "/api/v1/assistant/conversations", { schema_version: "assistant.conversation_command.v1", command_type: "create_conversation", idempotency_key: idempotencyKey });
}

export async function sendAssistantMessage(fetcher: typeof fetch, input: { conversationId: string; expectedRevision: number; content: string; idempotencyKey: string }): Promise<AssistantCommandResult> {
  return submit(fetcher, `/api/v1/assistant/conversations/${encodeURIComponent(input.conversationId)}/messages`, {
    schema_version: "assistant.conversation_command.v1", command_type: "send_message", conversation_id: input.conversationId,
    expected_revision: input.expectedRevision, idempotency_key: input.idempotencyKey, content: input.content,
  });
}

export async function selectAssistantWorkspace(fetcher: typeof fetch, input: WorkspaceSelection & { conversationId: string; expectedRevision: number; idempotencyKey: string }): Promise<AssistantCommandResult> {
  return submit(fetcher, `/api/v1/assistant/conversations/${encodeURIComponent(input.conversationId)}/workspace`, {
    schema_version: "assistant.conversation_command.v1", command_type: "select_workspace", conversation_id: input.conversationId,
    expected_revision: input.expectedRevision, idempotency_key: input.idempotencyKey, workspace_id: input.workspaceId,
    workspace_revision_id: input.workspaceRevisionId, workspace_resolution_id: input.workspaceResolutionId,
    workspace_resolution_revision: input.workspaceResolutionRevision, membership_revision: input.membershipRevision,
  });
}

export async function actOnAssistantCheckpoint(fetcher: typeof fetch, input: { conversationId: string; expectedRevision: number; checkpointId: string; checkpointRevision: number; action: "continue" | "narrow_scope" | "cancel"; idempotencyKey: string }): Promise<AssistantCommandResult> {
  return submit(fetcher, `/api/v1/assistant/conversations/${encodeURIComponent(input.conversationId)}/checkpoints/${encodeURIComponent(input.checkpointId)}/actions`, {
    schema_version: "assistant.conversation_command.v1", command_type: "checkpoint_action", conversation_id: input.conversationId,
    expected_revision: input.expectedRevision, idempotency_key: input.idempotencyKey, checkpoint_id: input.checkpointId,
    checkpoint_revision: input.checkpointRevision, checkpoint_action: input.action,
  });
}

export async function confirmAssistantTask(fetcher: typeof fetch, input: ConfirmationTarget & { idempotencyKey: string }): Promise<AssistantCommandResult> {
  return submit(fetcher, `/api/v1/assistant/conversations/${encodeURIComponent(input.conversationId)}/task-drafts/${encodeURIComponent(input.taskDraftId)}/confirm`, {
    schema_version: "assistant.conversation_command.v1", command_type: "confirm_task", conversation_id: input.conversationId,
    expected_revision: input.conversationRevision, idempotency_key: input.idempotencyKey, task_draft_id: input.taskDraftId,
    task_draft_revision: input.taskDraftRevision, readiness_assessment_id: input.readinessAssessmentId,
    readiness_assessment_revision: input.readinessAssessmentRevision, workspace_id: input.workspaceId,
    workspace_revision_id: input.workspaceRevisionId, workspace_resolution_id: input.workspaceResolutionId,
    workspace_resolution_revision: input.workspaceResolutionRevision, membership_revision: input.membershipRevision,
    task_spec_digest: input.taskSpecDigest, trigger_policy_revision: input.triggerPolicyRevision,
  });
}

export async function retryAssistantRun(fetcher: typeof fetch, input: { conversationId: string; expectedRevision: number; runId: string; runRevision: number; idempotencyKey: string }): Promise<AssistantCommandResult> {
  return submit(fetcher, `/api/v1/assistant/conversations/${encodeURIComponent(input.conversationId)}/runs/${encodeURIComponent(input.runId)}/retry`, {
    schema_version: "assistant.conversation_command.v1", command_type: "retry_run", conversation_id: input.conversationId,
    expected_revision: input.expectedRevision, idempotency_key: input.idempotencyKey, assistant_run_id: input.runId, assistant_run_revision: input.runRevision,
  });
}

export function newAssistantIdempotencyKey(): string { return `web-assistant-${crypto.randomUUID()}`; }

export function assistantConversationPath(result: Pick<AssistantCommandResult, "conversation_id" | "projection_ref">): string {
  const expected = `/api/v1/views/assistant_conversation/${encodeURIComponent(result.conversation_id)}`;
  if (result.projection_ref !== expected) throw new GatewayCommandError("command_service_unavailable");
  return `/assistant/${encodeURIComponent(result.conversation_id)}`;
}

async function submit(fetcher: typeof fetch, url: string, body: Record<string, unknown>): Promise<AssistantCommandResult> {
  let response: Response;
  try { response = await fetcher(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); }
  catch { throw new GatewayCommandError("network_error"); }
  const payload = await safeJSON(response);
  if (!response.ok) throw new GatewayCommandError(errorCode(payload.code), stringValue(payload.view_ref));
  if (!isResult(payload)) throw new GatewayCommandError("command_service_unavailable");
  return payload;
}

function isResult(value: Record<string, unknown>): value is Record<string, unknown> & AssistantCommandResult {
  return typeof value.command_id === "string" && typeof value.conversation_id === "string" && value.conversation_id.length > 0 &&
    typeof value.conversation_revision === "number" && Number.isInteger(value.conversation_revision) && value.conversation_revision > 0 &&
    typeof value.projection_ref === "string" && (value.status === "accepted" || value.status === "completed");
}
async function safeJSON(response: Response): Promise<Record<string, unknown>> { try { const value: unknown = await response.json(); return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}; } catch { return {}; } }
function stringValue(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }
function errorCode(value: unknown): GatewayCommandErrorCode { return ["stale_target", "target_unavailable", "unsupported_command", "command_conflict", "command_service_unavailable", "invalid_request"].includes(value as string) ? value as GatewayCommandErrorCode : "command_service_unavailable"; }
