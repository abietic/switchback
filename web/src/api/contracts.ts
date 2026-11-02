import { isCanonicalIdentity } from "./contractPrimitives";

export type StatusTone = "neutral" | "active" | "waiting" | "success" | "danger";

export type ContractRecord = Record<string, unknown>;

export function requireRecord(value: unknown, label: string): ContractRecord {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} is invalid`);
  return value as ContractRecord;
}

export function requireArray(value: unknown, label: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} is invalid`);
  return value;
}

export function requireString(value: unknown, label: string): string {
  if (typeof value !== "string" || !value) throw new Error(`${label} is invalid`);
  return value;
}

export function requireBoolean(value: unknown, label: string): boolean {
  if (typeof value !== "boolean") throw new Error(`${label} is invalid`);
  return value;
}

export function requireFiniteNumber(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`${label} is invalid`);
  return value;
}

export function requirePositiveNumber(value: unknown, label: string): number {
  const parsed = requireFiniteNumber(value, label);
  if (parsed <= 0) throw new Error(`${label} is invalid`);
  return parsed;
}

export function requireNonNegativeInteger(value: unknown, label: string): number {
  const parsed = requireFiniteNumber(value, label);
  if (!Number.isInteger(parsed) || parsed < 0) throw new Error(`${label} is invalid`);
  return parsed;
}

export function requireTimestamp(value: unknown, label: string): string {
  const timestamp = requireString(value, label);
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(timestamp);
  if (!match) throw new Error(`${label} is invalid`);
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
  const daysInMonth = [31, isLeapYear(year) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth[month - 1] ||
    hour > 23 || minute > 59 || second > 59) {
    throw new Error(`${label} is invalid`);
  }
  return timestamp;
}

function isLeapYear(year: number): boolean {
  return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
}

export interface ActionRef {
  action: string;
  target_id: string;
  expected_revision: number;
  metadata?: Record<string, unknown>;
}

export interface RelatedRef {
  type: string;
  id: string;
  href: string;
}

export interface CommandReceipt {
  client_command_id: string;
  command_id: string;
}

export function parseOptionalCommandReceipts(value: unknown): CommandReceipt[] | undefined {
  if (value === undefined) return undefined;
  const byClient = new Map<string, string>();
  const byServer = new Map<string, string>();
  const result: CommandReceipt[] = [];
  for (const item of requireArray(value, "command receipts")) {
    const receipt = requireRecord(item, "command receipt");
    const clientCommandID = requireCommandIdentity(receipt.client_command_id, "client command id");
    const commandID = requireCommandIdentity(receipt.command_id, "server command id");
    const existingServer = byClient.get(clientCommandID);
    const existingClient = byServer.get(commandID);
    if (
      (existingServer && existingServer !== commandID)
      || (existingClient && existingClient !== clientCommandID)
    ) {
      throw new Error("command receipts are conflicting");
    }
    if (existingServer === commandID) continue;
    byClient.set(clientCommandID, commandID);
    byServer.set(commandID, clientCommandID);
    result.push({ client_command_id: clientCommandID, command_id: commandID });
  }
  return result;
}

function requireCommandIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

export interface ProjectionSnapshot<T> {
  schema_version: "frontier.projection_snapshot.v1";
  view_type: string;
  view_id: string;
  tenant_id: string;
  view_epoch: number;
  view_cursor: number;
  projection_version: number;
  query_fingerprint: string;
  generated_at: string;
  data: T;
  available_actions: ActionRef[];
  related_refs: RelatedRef[];
  command_receipts?: CommandReceipt[];
}

export interface SourceRef {
  type: string;
  id: string;
}

export interface ProjectionDeltaPayload<T> {
  data?: T;
  available_actions?: ActionRef[];
  related_refs?: RelatedRef[];
  display_checkpoint?: {
    display_stream_id: string;
    checkpoint_version: number;
  };
}

export interface ProjectionDelta<T> {
  schema_version: "frontier.projection_delta.v1";
  delta_id: string;
  view_type: string;
  view_id: string;
  view_epoch: number;
  view_cursor: number;
  base_projection_version: number;
  result_projection_version: number;
  event_type: string;
  critical: boolean;
  command_id?: string;
  client_command_id?: string;
  source_refs: SourceRef[];
  payload: ProjectionDeltaPayload<T>;
}

export interface ProjectionControlEvent {
  schema_version: "frontier.projection_control.v1";
  event_type: "resync_required" | "client_upgrade_required";
  view_type: string;
  view_id: string;
  view_epoch: number;
  snapshot_cursor: number;
}

export interface TransientDisplayDelta {
  schema_version: "frontier.transient_display_delta.v1";
  display_stream_id: string;
  message_id?: string;
  part_id: string;
  base_checkpoint_version: number;
  transient_sequence: number;
  delta_type: "text_delta" | "reasoning_summary_delta" | "tool_args_delta" | "progress_pulse";
  payload: Record<string, unknown>;
  emitted_at: string;
}

export interface TaskListItem {
  task_id: string;
  title: string;
  workspace: string;
  status: string;
  status_tone: StatusTone;
  worker_session_id?: string;
  updated_at: string;
}

export interface TaskIndexData {
  tasks: TaskListItem[];
  capacity: {
    used: number;
    limit: number;
  };
}

export interface ArtifactSummary {
  artifact_id: string;
  type: "report" | "patch";
  label: string;
  size_bytes: number;
  checksum: string;
  completeness: "complete" | "partial" | "unverified";
  worker_run_id: string;
  agent_turn_id: string;
  runtime_generation?: number;
  generated_at?: string;
}

export type DisplayPartType =
  | "text"
  | "reasoning_summary"
  | "tool"
  | "activity"
  | "approval"
  | "artifact"
  | "source"
  | "error";

export interface DisplayPart {
  schema_version: "frontier.display_part.v1";
  part_id: string;
  part_type: DisplayPartType;
  part_revision: number;
  ordinal: number;
  lifecycle_status: string;
  critical: boolean;
  payload: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface WorkerTimelineTurn {
  worker_run_id: string;
  agent_turn_id: string;
  display_stream_id: string;
  status: string;
  started_at: string;
  terminal_at?: string;
  prompt: string;
  parts: DisplayPart[];
  checkpoint_version: number;
  checkpoint_complete: boolean;
  backfill_status: "online" | "pending" | "non_backfillable";
  projection_lag_millis?: number;
}

export interface WorkerTimelinePage {
  turns: WorkerTimelineTurn[];
  next_cursor?: string;
  has_more: boolean;
}

export interface ApprovalRequest {
  approval_request_id: string;
  revision: number;
  capability: string;
  target: string;
  effect: string;
  reason: string;
  expires_at: string;
  scope_options: Array<"once" | "session">;
}

export interface WorkerDiagnosticLinks {
  links: Array<{ kind: "grafana" | "logs" | "langfuse"; url: string }>;
  grafana_status: "configured" | "disabled" | "invalid_config";
  langfuse_status: "identity_unavailable";
}

export interface WorkerDetailData {
  worker_session_id: string;
  session_revision: number;
  workspace: string;
  status: string;
  status_tone: StatusTone;
  task_status?: string;
  task_status_label?: string;
  run_status?: string;
  turn_status?: string;
  bound_task_id?: string;
  task_revision?: number;
  active_worker_run_id?: string;
  active_run_revision?: number;
  active_agent_turn_id?: string;
  active_turn_revision?: number;
  runtime_health: string;
  runtime_generation: number;
  agent_profile: string;
  network_profile?: {
    id: "trusted_full_network_v1" | "m1_public_web_restricted_v1";
    revision: number;
    digest: string;
    enforcement_status: "active" | "legacy_unenforced";
  };
  runtime_recovery?: {
    status: "recovering_runtime" | "runtime_recovered" | "runtime_recovery_failed";
    phase: "open" | "fencing" | "provisioning" | "reattaching" | "verifying" | "recovered" | "exhausted";
    failed_generation: number;
    replacement_generation?: number;
    attempt_count: number;
    continuity_status: "pending" | "proven" | "unproven";
    evidence_at: string;
    outcome_reason?: string;
  };
  started_at?: string;
  deadline_at?: string;
  timeline_groups?: WorkerTimelineTurn[];
  timeline_cursor?: string;
  timeline_has_more?: boolean;
  artifacts: ArtifactSummary[];
  approvals: ApprovalRequest[];
  waiting_user?: {
    reason: string;
    message: string;
    allowed_actions: string[];
  };
  stage_report?: string;
}

export interface ArtifactDetailData extends ArtifactSummary {
  availability: ArtifactReviewOutcome;
  content: string;
  download_url?: string;
  review_manifest?: ArtifactReviewManifest;
}

export type ArtifactReviewOutcome =
  | "online"
  | "empty"
  | "binary"
  | "oversized"
  | "parse_failed"
  | "missing"
  | "checksum_failed";

export interface ArtifactReviewManifest {
  schema_version: "artifact.review_manifest.v1";
  outcome: ArtifactReviewOutcome;
  renderable: boolean;
  download_allowed: boolean;
  media_type: string;
  content_size_bytes: number;
  diagnostics: Array<{ code: string; message: string }>;
}

export interface AssistantTurn {
  turn_id: string;
  sequence: number;
  role: "user" | "assistant";
  content: string;
  created_at: string;
}

export interface AssistantRun {
  assistant_run_id: string;
  status: "queued" | "claimed" | "failed" | "completed" | "cancelled";
  revision: number;
  error_code?: string;
  retryable: boolean;
}

export type AssistantReadinessDimension =
  | "goal"
  | "deliverable_acceptance"
  | "scope_target"
  | "constraints"
  | "context_access"
  | "risk_approval"
  | "execution_policy";

export interface AssistantReadiness {
  assessment_id: string;
  revision: number;
  status: "clarifying" | "ready_confirmed" | "ready_with_assumptions";
  dimensions: Array<{
    dimension: AssistantReadinessDimension;
    status: "known" | "assumed" | "blocking_unknown" | "not_applicable";
    summary: string;
    evidence_refs: string[];
  }>;
  blocking_unknowns: string[];
  assumptions: string[];
  clarification_questions: number;
}

export interface AssistantTaskSpec {
  title: string;
  goal: string;
  workspace_id: string;
  workspace_resolution_ref: {
    resolution_id: string;
    revision: number;
    workspace_revision_id: string;
    membership_revision: number;
  };
  readiness_assessment_ref: {
    assessment_id: string;
    policy_revision: string;
    content_digest: string;
  };
  creation_intent_ref: {
    intent_id: string;
    task_spec_digest: string;
    trigger_policy_revision: string;
  };
  target: { kind: string; ref: string };
  constraints: string[];
  acceptance_criteria: string[];
  assumptions: string[];
  agent_config: {
    profile_id: string;
    profile_revision: string;
    adapter: string;
  };
  runtime_config: {
    network_policy: "trusted_full_network_v1" | "m1_public_web_restricted_v1";
    network_policy_revision?: number;
    network_policy_digest?: string;
  };
}

export interface AssistantTaskDraft {
  task_draft_id: string;
  revision: number;
  status: "current" | "invalidated" | "confirmed";
  task_spec: AssistantTaskSpec;
  assumptions: string[];
}

export interface AssistantCheckpoint {
  checkpoint_id: string;
  revision: number;
  status: "active" | "resolved" | "cancelled";
  working_draft: Record<string, unknown>;
  allowed_actions: Array<"continue" | "narrow_scope" | "cancel">;
}

export interface AssistantHandoff {
  task_id: string;
  status: string;
  worker_session_id?: string;
}

export interface AssistantConversationData {
  conversation_id: string;
  revision: number;
  generation: number;
  status: "active" | "closed";
  workspace_id?: string;
  updated_at: string;
  turns: AssistantTurn[];
  assistant_run?: AssistantRun;
  readiness?: AssistantReadiness;
  task_draft?: AssistantTaskDraft;
  checkpoint?: AssistantCheckpoint;
  handoffs: AssistantHandoff[];
}
