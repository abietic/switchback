import type { CommandResult } from "./workerCommands";
import { GatewayCommandError } from "./workerCommands";

export interface CreateTaskRequest {
  idempotencyKey: string;
  title: string;
  goal: string;
  agentProfileId: string;
  acceptanceMode: "auto" | "manual";
}

export interface TaskCommandRequest {
  taskId: string;
  action: "accept" | "rework" | "cancel";
  expectedTaskRevision: number;
  idempotencyKey: string;
  message?: string;
}

export interface TaskContinuationRequest {
  taskId: string;
  expectedTaskRevision: number;
  workerSessionId: string;
  expectedSessionRevision: number;
  workerRunId: string;
  expectedRunRevision: number;
  agentTurnId: string;
  expectedTurnRevision: number;
  interactionKind: "chat" | "rework";
  idempotencyKey: string;
  message: string;
}

export async function createTask(fetcher: typeof fetch, request: CreateTaskRequest): Promise<CommandResult> {
  return submit(fetcher, "/api/v1/tasks", {
    schema_version: "gateway.direct_task_input.v1",
    idempotency_key: request.idempotencyKey,
    project_id: "project-local",
    workspace_id: "workspace-local",
    workspace_revision_id: "workspace-revision-local-v1",
    title: request.title,
    goal: request.goal,
    target: { kind: "workspace", ref: "workspace-revision-local-v1" },
    constraints: ["Do not publish remote changes."],
    acceptance_criteria: ["Produce a report and preserve any patch for review."],
    agent_profile_id: request.agentProfileId,
    acceptance_mode: request.acceptanceMode,
    user_visible_types: ["report", "patch"],
  });
}

export async function submitTaskCommand(fetcher: typeof fetch, request: TaskCommandRequest): Promise<CommandResult> {
  return submit(fetcher, `/api/v1/tasks/${encodeURIComponent(request.taskId)}/${request.action}`, {
    schema_version: "gateway.task_command.v1",
    idempotency_key: request.idempotencyKey,
    expected_task_revision: request.expectedTaskRevision,
    ...(request.message ? { message: request.message } : {}),
  });
}

export async function submitTaskContinuation(
  fetcher: typeof fetch,
  request: TaskContinuationRequest,
): Promise<CommandResult> {
  return submit(fetcher, `/api/v1/tasks/${encodeURIComponent(request.taskId)}/continue`, {
    schema_version: "gateway.task_continuation.v1",
    idempotency_key: request.idempotencyKey,
    expected_task_revision: request.expectedTaskRevision,
    worker_session_id: request.workerSessionId,
    expected_session_revision: request.expectedSessionRevision,
    worker_run_id: request.workerRunId,
    expected_run_revision: request.expectedRunRevision,
    agent_turn_id: request.agentTurnId,
    expected_turn_revision: request.expectedTurnRevision,
    interaction_kind: request.interactionKind,
    message: request.message,
  });
}

export function newTaskIdempotencyKey(): string {
  return `web-task-${crypto.randomUUID()}`;
}

async function submit(fetcher: typeof fetch, url: string, body: Record<string, unknown>): Promise<CommandResult> {
  let response: Response;
  try {
    response = await fetcher(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  } catch {
    throw new GatewayCommandError("network_error");
  }
  const payload = await safeJSON(response);
  if (!response.ok) {
    throw new GatewayCommandError(errorCode(payload.code), stringValue(payload.view_ref));
  }
  if (typeof payload.command_id !== "string" || (payload.status !== "accepted" && payload.status !== "completed")) {
    throw new GatewayCommandError("command_service_unavailable");
  }
  return payload as unknown as CommandResult;
}

async function safeJSON(response: Response): Promise<Record<string, unknown>> {
  try {
    const value: unknown = await response.json();
    return typeof value === "object" && value !== null ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function errorCode(value: unknown) {
  switch (value) {
    case "stale_target":
    case "target_unavailable":
    case "unsupported_command":
    case "command_conflict":
    case "command_service_unavailable":
    case "invalid_request":
      return value;
    default:
      return "command_service_unavailable" as const;
  }
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}
