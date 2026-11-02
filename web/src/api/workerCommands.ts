export type WorkerCommandAction = "follow-up" | "queue" | "interrupt" | "steer" | "approval";

export interface WorkerCommandTarget {
  workerSessionId: string;
  expectedSessionRevision: number;
  workerRunId: string;
  expectedRunRevision: number;
  agentTurnId: string;
  expectedTurnRevision: number;
}

export interface ApprovalDecisionInput {
  approval_request_id: string;
  expected_approval_revision: number;
  decision: "approve" | "deny";
  scope: "once" | "session";
  capability: string;
  target: string;
  effect: string;
}

export interface WorkerCommandRequest {
  action: WorkerCommandAction;
  target: WorkerCommandTarget;
  idempotencyKey: string;
  content?: string;
  approval?: ApprovalDecisionInput;
}

export interface CommandResult {
  command_id: string;
  status: "accepted" | "completed";
  queue_sequence?: number;
  view_ref?: string;
}

export type GatewayCommandErrorCode =
  | "stale_target"
  | "target_unavailable"
  | "unsupported_command"
  | "command_conflict"
  | "command_service_unavailable"
  | "invalid_request"
  | "network_error";

export class GatewayCommandError extends Error {
  readonly code: GatewayCommandErrorCode;
  readonly viewRef?: string;

  constructor(code: GatewayCommandErrorCode, viewRef?: string) {
    super(commandErrorMessage(code));
    this.name = "GatewayCommandError";
    this.code = code;
    this.viewRef = viewRef;
  }
}

export function isDefinitiveCommandFailure(cause: unknown): boolean {
  return cause instanceof GatewayCommandError &&
    cause.code !== "network_error" &&
    cause.code !== "command_service_unavailable";
}

export async function submitWorkerCommand(
  fetcher: typeof fetch,
  request: WorkerCommandRequest,
): Promise<CommandResult> {
  const body: Record<string, unknown> = {
    schema_version: "gateway.worker_command.v1",
    idempotency_key: request.idempotencyKey,
    expected_session_revision: request.target.expectedSessionRevision,
    worker_run_id: request.target.workerRunId,
    expected_run_revision: request.target.expectedRunRevision,
    agent_turn_id: request.target.agentTurnId,
    expected_turn_revision: request.target.expectedTurnRevision,
  };
  if (request.content !== undefined) body.content = request.content;
  if (request.approval !== undefined) body.approval = request.approval;

  let response: Response;
  try {
    response = await fetcher(`/api/v1/worker-sessions/${encodeURIComponent(request.target.workerSessionId)}/${request.action}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  } catch {
    throw new GatewayCommandError("network_error");
  }
  const payload = await safeJSON(response);
  if (!response.ok) {
    throw new GatewayCommandError(commandErrorCode(payload.code), stringValue(payload.view_ref));
  }
  if (!isCommandResult(payload)) {
    throw new GatewayCommandError("command_service_unavailable");
  }
  return payload;
}

export function newCommandIdempotencyKey(): string {
  return `web-${crypto.randomUUID()}`;
}

function commandErrorCode(value: unknown): GatewayCommandErrorCode {
  switch (value) {
    case "stale_target":
    case "target_unavailable":
    case "unsupported_command":
    case "command_conflict":
    case "command_service_unavailable":
    case "invalid_request":
      return value;
    default:
      return "command_service_unavailable";
  }
}

function commandErrorMessage(code: GatewayCommandErrorCode): string {
  switch (code) {
    case "stale_target": return "Worker state changed. Refresh before retrying.";
    case "target_unavailable": return "This Worker is no longer available.";
    case "unsupported_command": return "This command is not supported by the active Agent.";
    case "command_conflict": return "This command conflicts with the current Worker state.";
    case "invalid_request": return "The command could not be validated.";
    case "network_error": return "Delivery status is unknown. Retry safely with the same command.";
    default: return "The command service is temporarily unavailable.";
  }
}

async function safeJSON(response: Response): Promise<Record<string, unknown>> {
  try {
    const value: unknown = await response.json();
    return typeof value === "object" && value !== null ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function isCommandResult(value: Record<string, unknown>): value is Record<string, unknown> & CommandResult {
  return typeof value.command_id === "string" && (value.status === "accepted" || value.status === "completed") &&
    (value.queue_sequence === undefined || typeof value.queue_sequence === "number");
}
