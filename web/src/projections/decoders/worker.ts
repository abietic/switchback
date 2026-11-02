import {
  type ActionRef,
  type ApprovalRequest,
  type ArtifactSummary,
  type DisplayPart,
  type StatusTone,
  type WorkerDetailData,
  type WorkerTimelineTurn,
  requireArray,
  requireBoolean,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireTimestamp,
} from "../../api/contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "../../api/contractPrimitives";
import {
  internalCreateViewProjectionRuntimeDecoder,
  internalRejectViewContract,
  type BoundViewProjectionRuntimeDecoder,
  type InternalViewContract,
  type InternalViewContractState,
} from "../viewRuntime";

const sessionStatuses = [
  "allocating",
  "interactive",
  "running",
  "recovering",
  "waiting_user",
  "idle",
  "lost",
  "expired",
  "closed",
] as const;

const taskStatuses = [
  "queued",
  "running",
  "waiting_user",
  "delivered",
  "completed",
  "failed",
  "cancelled",
] as const;

const runStatuses = [
  "starting",
  "running",
  "waiting_user",
  "completed",
  "failed",
  "cancelled",
  "timed_out",
] as const;

const turnStatuses = [
  "accepted",
  "queued",
  "delivered",
  "consumed",
  "completed",
  "cancelled",
  "timed_out",
  "failed",
] as const;

const terminalStatuses = new Set(["completed", "failed", "cancelled", "timed_out"]);
const activeRunStatuses = new Set(["starting", "running", "waiting_user"]);
const activeTurnStatuses = new Set(["accepted", "delivered", "consumed"]);

export function createWorkerViewDecoder(
  workerSessionId: string,
): BoundViewProjectionRuntimeDecoder<WorkerDetailData> {
  const contract: InternalViewContract<WorkerDetailData> = {
    allowedDeltaEventTypes: ["view.entity_upserted.v1", "view.stream_checkpointed.v1"],
    canonicalRelatedRefTypes: ["artifact_detail"],
    decodeData(value) {
      return parseWorkerDetailData(value, workerSessionId);
    },
    validateState,
  };
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: `worker-detail-v1:${workerSessionId}`,
    viewType: "worker_detail",
    viewId: workerSessionId,
    contract,
  });
}

function parseWorkerDetailData(value: unknown, expectedWorkerSessionId: string): WorkerDetailData {
  const data = requireRecord(value, "WorkerDetail data");
  if (isPreservedSparseLegacyContract(data)) {
    internalRejectViewContract("legacy_worker_contract");
  }
  const workerSessionId = canonicalIdentity(data.worker_session_id, "WorkerSession id");
  if (workerSessionId !== expectedWorkerSessionId) {
    throw new Error("WorkerDetail data identity is invalid");
  }

  const status = closedString(data.status, sessionStatuses, "WorkerSession status");
  const statusTone = closedString(
    data.status_tone,
    ["neutral", "active", "waiting", "success", "danger"] as const,
    "WorkerSession status tone",
  );
  if (statusTone !== sessionTone(status)) {
    throw new Error("WorkerSession status authority is invalid");
  }

  const taskStatus = requiredNullable(data, "task_status", (item) => (
    closedString(item, taskStatuses, "Task status")
  ));
  const taskStatusLabel = requiredNullable(data, "task_status_label", (item) => (
    requireString(item, "Task status label")
  ));
  const taskRevision = requiredNullable(data, "task_revision", (item) => (
    positiveSafeInteger(item, "Task revision")
  ));
  const boundTaskId = requiredNullable(data, "bound_task_id", (item) => (
    canonicalIdentity(item, "bound Task id")
  ));
  validateTaskTuple(taskStatus, taskStatusLabel, taskRevision, boundTaskId);

  const runStatus = requiredNullable(data, "run_status", (item) => (
    closedString(item, runStatuses, "WorkerRun status")
  ));
  const activeWorkerRunId = requiredNullable(data, "active_worker_run_id", (item) => (
    canonicalIdentity(item, "WorkerRun id")
  ));
  const activeRunRevision = requiredNullable(data, "active_run_revision", (item) => (
    positiveSafeInteger(item, "WorkerRun revision")
  ));
  const startedAt = requiredNullable(data, "started_at", (item) => (
    requireTimestamp(item, "WorkerRun started_at")
  ));

  const turnStatus = requiredNullable(data, "turn_status", (item) => (
    closedString(item, turnStatuses, "AgentTurn status")
  ));
  const activeAgentTurnId = requiredNullable(data, "active_agent_turn_id", (item) => (
    canonicalIdentity(item, "AgentTurn id")
  ));
  const activeTurnRevision = requiredNullable(data, "active_turn_revision", (item) => (
    positiveSafeInteger(item, "AgentTurn revision")
  ));
  const deadlineAt = requiredNullable(data, "deadline_at", (item) => (
    requireTimestamp(item, "AgentTurn deadline_at")
  ));
  const agentProfile = optionalCanonicalIdentityOrEmpty(data.agent_profile, "Agent profile");
  validateRunTurnTuple({
    runStatus,
    activeWorkerRunId,
    activeRunRevision,
    startedAt,
    agentProfile,
    turnStatus,
    activeAgentTurnId,
    activeTurnRevision,
    deadlineAt,
  });

  const networkProfile = requiredNullable(data, "network_profile", parseNetworkProfile);
  const runtimeRecovery = requiredNullable(data, "runtime_recovery", parseRuntimeRecovery);
  const artifacts = requireArray(data.artifacts, "Worker artifacts").map(parseArtifact);
  requireUnique(artifacts.map((artifact) => artifact.artifact_id), "Worker Artifact identity");
  const approvals = requireArray(data.approvals, "Worker approvals").map(parseApproval);
  requireUnique(approvals.map((approval) => approval.approval_request_id), "Worker approval identity");
  const timelineGroups = requireArray(data.timeline_groups, "Worker timeline groups")
    .map(parseTimelineTurn);
  if (timelineGroups.length > 20) throw new Error("Worker timeline page is invalid");
  requireUnique(timelineGroups.map((turn) => turn.agent_turn_id), "Worker timeline AgentTurn identity");
  const timelineCursor = requiredNullable(data, "timeline_cursor", parseTimelineCursor);
  const timelineHasMore = requireBoolean(data.timeline_has_more, "Worker timeline has_more");
  if (timelineHasMore !== (timelineCursor !== undefined)) {
    throw new Error("Worker timeline cursor authority is invalid");
  }
  const waitingUser = requiredNullable(data, "waiting_user", (item) => (
    parseWaitingUser(item, status, runtimeRecovery)
  ));
  if ((status === "waiting_user") !== (waitingUser !== undefined)) {
    throw new Error("Worker waiting-user authority is invalid");
  }
  const stageReport = requiredNullable(data, "stage_report", (item) => (
    requireString(item, "Worker stage report")
  ));

  return {
    worker_session_id: workerSessionId,
    session_revision: positiveSafeInteger(data.session_revision, "WorkerSession revision"),
    workspace: canonicalIdentity(data.workspace, "Worker workspace"),
    status,
    status_tone: statusTone,
    ...(taskStatus === undefined ? {} : { task_status: taskStatus }),
    ...(taskStatusLabel === undefined ? {} : { task_status_label: taskStatusLabel }),
    ...(runStatus === undefined ? {} : { run_status: runStatus }),
    ...(turnStatus === undefined ? {} : { turn_status: turnStatus }),
    ...(boundTaskId === undefined ? {} : { bound_task_id: boundTaskId }),
    ...(taskRevision === undefined ? {} : { task_revision: taskRevision }),
    ...(activeWorkerRunId === undefined ? {} : { active_worker_run_id: activeWorkerRunId }),
    ...(activeRunRevision === undefined ? {} : { active_run_revision: activeRunRevision }),
    ...(activeAgentTurnId === undefined ? {} : { active_agent_turn_id: activeAgentTurnId }),
    ...(activeTurnRevision === undefined ? {} : { active_turn_revision: activeTurnRevision }),
    runtime_health: requireString(data.runtime_health, "Worker Runtime health"),
    runtime_generation: nonNegativeSafeInteger(data.runtime_generation, "Runtime generation"),
    agent_profile: agentProfile,
    ...(networkProfile === undefined ? {} : { network_profile: networkProfile }),
    ...(runtimeRecovery === undefined ? {} : { runtime_recovery: runtimeRecovery }),
    ...(startedAt === undefined ? {} : { started_at: startedAt }),
    ...(deadlineAt === undefined ? {} : { deadline_at: deadlineAt }),
    timeline_groups: timelineGroups,
    ...(timelineCursor === undefined ? {} : { timeline_cursor: timelineCursor }),
    timeline_has_more: timelineHasMore,
    artifacts,
    approvals,
    ...(waitingUser === undefined ? {} : { waiting_user: waitingUser }),
    ...(stageReport === undefined ? {} : { stage_report: stageReport }),
  };
}

function validateState(state: InternalViewContractState<WorkerDetailData>): undefined {
  const seenActions = new Set<string>();
  for (const action of state.availableActions) {
    const actionIdentity = action.action === "worker.approval"
      ? `${action.action}\0${action.target_id}`
      : action.action;
    if (seenActions.has(actionIdentity)) throw new Error("Worker action is duplicated");
    seenActions.add(actionIdentity);
    validateAction(action, state.data);
  }

  const expectedRefs = new Set(
    state.data.artifacts.map((artifact) => `artifact_detail\0${artifact.artifact_id}`),
  );
  const actualRefs = new Set(
    state.relatedRefs.map((ref) => `${ref.type}\0${ref.id}`),
  );
  if (
    actualRefs.size !== state.relatedRefs.length
    || actualRefs.size !== expectedRefs.size
    || [...expectedRefs].some((key) => !actualRefs.has(key))
  ) {
    throw new Error("Worker Artifact navigation authority is invalid");
  }
  return undefined;
}

function validateAction(
  action: InternalViewContractState<WorkerDetailData>["availableActions"][number],
  data: InternalViewContractState<WorkerDetailData>["data"],
): void {
  const sessionAllowsInteraction = data.status !== "closed"
    && data.status !== "expired"
    && data.status !== "recovering";
  const terminalRunTurn = data.run_status !== undefined
    && data.turn_status !== undefined
    && terminalStatuses.has(data.run_status)
    && terminalStatuses.has(data.turn_status);
  const activeRunTurn = data.run_status !== undefined
    && data.turn_status !== undefined
    && activeRunStatuses.has(data.run_status)
    && activeTurnStatuses.has(data.turn_status);

  switch (action.action) {
    case "worker.follow_up":
      requireNoMetadata(action);
      if (
        !sessionAllowsInteraction
        || !terminalRunTurn
        || action.target_id !== data.worker_session_id
        || action.expected_revision !== data.session_revision
      ) {
        throw new Error("Worker follow-up authority is invalid");
      }
      return;
    case "worker.queue":
      requireNoMetadata(action);
      if (
        !sessionAllowsInteraction
        || !activeRunTurn
        || action.target_id !== data.worker_session_id
        || action.expected_revision !== data.session_revision
      ) {
        throw new Error("Worker queue authority is invalid");
      }
      return;
    case "worker.interrupt":
      requireNoMetadata(action);
      if (
        !sessionAllowsInteraction
        || !activeRunTurn
        || data.active_agent_turn_id === undefined
        || data.active_turn_revision === undefined
        || action.target_id !== data.active_agent_turn_id
        || action.expected_revision !== data.active_turn_revision
      ) {
        throw new Error("Worker interrupt authority is invalid");
      }
      return;
    case "task.continue":
      if (
        !sessionAllowsInteraction
        || !terminalRunTurn
        || data.task_status !== "delivered"
        || data.bound_task_id === undefined
        || data.task_revision === undefined
        || action.target_id !== data.bound_task_id
        || action.expected_revision !== data.task_revision
        || !isContinueMetadata(action.metadata)
      ) {
        throw new Error("Task continuation authority is invalid");
      }
      return;
    case "task.accept":
    case "task.rework":
      requireNoMetadata(action);
      if (
        data.task_status !== "delivered"
        || data.bound_task_id === undefined
        || data.task_revision === undefined
        || action.target_id !== data.bound_task_id
        || action.expected_revision !== data.task_revision
      ) {
        throw new Error("Task delivery authority is invalid");
      }
      return;
    case "worker.approval": {
      requireNoMetadata(action);
      const request = data.approvals.find((candidate) => (
        candidate.approval_request_id === action.target_id
      ));
      if (request === undefined || request.revision !== action.expected_revision) {
        throw new Error("Worker approval authority is invalid");
      }
      return;
    }
    default:
      throw new Error("Worker action is unknown");
  }
}

function parseArtifact(value: unknown): ArtifactSummary {
  const artifact = requireRecord(value, "Worker Artifact");
  const type = closedString(artifact.type, ["report", "patch"] as const, "Artifact type");
  const completeness = closedString(
    artifact.completeness,
    ["complete", "partial", "unverified"] as const,
    "Artifact completeness",
  );
  return {
    artifact_id: canonicalIdentity(artifact.artifact_id, "Artifact id"),
    type,
    label: requireString(artifact.label, "Artifact label"),
    size_bytes: nonNegativeSafeInteger(artifact.size_bytes, "Artifact size"),
    checksum: sha256Digest(artifact.checksum, "Artifact checksum"),
    completeness,
    worker_run_id: canonicalIdentity(artifact.worker_run_id, "Artifact WorkerRun id"),
    agent_turn_id: canonicalIdentity(artifact.agent_turn_id, "Artifact AgentTurn id"),
    runtime_generation: positiveSafeInteger(artifact.runtime_generation, "Artifact Runtime generation"),
    generated_at: requireTimestamp(artifact.generated_at, "Artifact generated_at"),
  };
}

function parseApproval(value: unknown): ApprovalRequest {
  const approval = requireRecord(value, "Worker approval");
  const scopes = requireArray(approval.scope_options, "Worker approval scopes").map((scope) => (
    closedString(scope, ["once", "session"] as const, "Worker approval scope")
  ));
  if (scopes.length === 0 || new Set(scopes).size !== scopes.length) {
    throw new Error("Worker approval scopes are invalid");
  }
  return {
    approval_request_id: canonicalIdentity(approval.approval_request_id, "approval request id"),
    revision: positiveSafeInteger(approval.revision, "approval revision"),
    capability: canonicalIdentity(approval.capability, "approval capability"),
    target: requireString(approval.target, "approval target"),
    effect: canonicalIdentity(approval.effect, "approval effect"),
    reason: requireString(approval.reason, "approval reason"),
    expires_at: requireTimestamp(approval.expires_at, "approval expires_at"),
    scope_options: scopes,
  };
}

function parseTimelineTurn(value: unknown): WorkerTimelineTurn {
  const turn = requireRecord(value, "Worker timeline turn");
  const status = closedString(turn.status, turnStatuses, "timeline AgentTurn status");
  const terminalAt = optionalOmitted(turn, "terminal_at", (item) => (
    requireTimestamp(item, "timeline terminal_at")
  ));
  if (terminalStatuses.has(status) !== (terminalAt !== undefined)) {
    throw new Error("Worker timeline terminal authority is invalid");
  }
  const parts = requireArray(turn.parts, "Worker timeline parts").map(parseDisplayPart);
  requireUnique(parts.map((part) => part.part_id), "DisplayPart identity");
  requireUnique(parts.map((part) => String(part.ordinal)), "DisplayPart ordinal");
  const latestCheckpointedAt = optionalOmitted(
    turn,
    "latest_checkpointed_at",
    (item) => requireTimestamp(item, "timeline latest_checkpointed_at"),
  );
  const projectionLagMillis = optionalOmitted(
    turn,
    "projection_lag_millis",
    (item) => nonNegativeSafeInteger(item, "timeline projection lag"),
  );
  const parsed = {
    worker_run_id: canonicalIdentity(turn.worker_run_id, "timeline WorkerRun id"),
    agent_turn_id: canonicalIdentity(turn.agent_turn_id, "timeline AgentTurn id"),
    display_stream_id: canonicalIdentity(turn.display_stream_id, "timeline Display stream id"),
    status,
    started_at: requireTimestamp(turn.started_at, "timeline started_at"),
    ...(terminalAt === undefined ? {} : { terminal_at: terminalAt }),
    prompt: requireText(turn.prompt, "timeline prompt"),
    parts,
    checkpoint_version: nonNegativeSafeInteger(turn.checkpoint_version, "timeline checkpoint version"),
    checkpoint_complete: requireBoolean(turn.checkpoint_complete, "timeline checkpoint complete"),
    backfill_status: closedString(
      turn.backfill_status,
      ["online", "pending", "non_backfillable"] as const,
      "timeline backfill status",
    ),
    ...(latestCheckpointedAt === undefined ? {} : { latest_checkpointed_at: latestCheckpointedAt }),
    ...(projectionLagMillis === undefined ? {} : { projection_lag_millis: projectionLagMillis }),
  };
  return parsed;
}

function parseDisplayPart(value: unknown): DisplayPart {
  const part = requireRecord(value, "DisplayPart");
  if (part.schema_version !== "frontier.display_part.v1") {
    throw new Error("DisplayPart schema is invalid");
  }
  const partType = closedString(
    part.part_type,
    ["text", "reasoning_summary", "tool", "activity", "approval", "artifact", "source", "error"] as const,
    "DisplayPart type",
  );
  const lifecycleStatus = closedString(
    part.lifecycle_status,
    ["pending", "streaming", "waiting_user", "completed", "failed", "cancelled", "timed_out", "expired"] as const,
    "DisplayPart lifecycle",
  );
  const critical = requireBoolean(part.critical, "DisplayPart critical");
  if (critical !== (partType === "approval" || partType === "error")) {
    throw new Error("DisplayPart critical authority is invalid");
  }
  const visibility = requireRecord(part.visibility, "DisplayPart visibility");
  if (visibility.audience !== "user") throw new Error("DisplayPart visibility is invalid");
  const provenance = requireRecord(part.provenance, "DisplayPart provenance");
  if (
    provenance.source !== "acp"
    || positiveSafeInteger(provenance.runtime_generation, "DisplayPart Runtime generation") < 1
  ) {
    throw new Error("DisplayPart provenance is invalid");
  }
  canonicalIdentity(provenance.agent_session_id, "DisplayPart AgentSession id");
  const availableActions = requireArray(part.available_actions, "DisplayPart actions");
  if (availableActions.length !== 0) throw new Error("DisplayPart actions are not current authority");
  const payload = requireRecord(part.payload, "DisplayPart payload");
  const parsed = {
    schema_version: "frontier.display_part.v1" as const,
    part_id: canonicalIdentity(part.part_id, "DisplayPart id"),
    part_type: partType,
    part_revision: positiveSafeInteger(part.part_revision, "DisplayPart revision"),
    ordinal: nonNegativeSafeInteger(part.ordinal, "DisplayPart ordinal"),
    lifecycle_status: lifecycleStatus,
    critical,
    visibility: { ...visibility },
    provenance: { ...provenance },
    available_actions: [] as ActionRef[],
    payload: { ...payload },
    created_at: requireTimestamp(part.created_at, "DisplayPart created_at"),
    updated_at: requireTimestamp(part.updated_at, "DisplayPart updated_at"),
  };
  return parsed;
}

function parseNetworkProfile(value: unknown): NonNullable<WorkerDetailData["network_profile"]> {
  const profile = requireRecord(value, "Worker network profile");
  const id = closedString(
    profile.id,
    ["trusted_full_network_v1", "m1_public_web_restricted_v1"] as const,
    "Worker network profile id",
  );
  const enforcementStatus = closedString(
    profile.enforcement_status,
    ["active", "legacy_unenforced"] as const,
    "Worker network enforcement status",
  );
  if (
    (id === "trusted_full_network_v1" && enforcementStatus !== "active")
    || (id === "m1_public_web_restricted_v1" && enforcementStatus !== "legacy_unenforced")
  ) {
    throw new Error("Worker network profile authority is invalid");
  }
  return {
    id,
    revision: positiveSafeInteger(profile.revision, "Worker network profile revision"),
    digest: sha256Digest(profile.digest, "Worker network profile digest"),
    enforcement_status: enforcementStatus,
  };
}

function parseRuntimeRecovery(value: unknown): NonNullable<WorkerDetailData["runtime_recovery"]> {
  const recovery = requireRecord(value, "Worker Runtime recovery");
  const phase = closedString(
    recovery.phase,
    ["open", "fencing", "provisioning", "reattaching", "verifying", "recovered", "exhausted"] as const,
    "Runtime recovery phase",
  );
  const status = closedString(
    recovery.status,
    ["recovering_runtime", "runtime_recovered", "runtime_recovery_failed"] as const,
    "Runtime recovery status",
  );
  const expectedStatus = phase === "recovered"
    ? "runtime_recovered"
    : phase === "exhausted"
      ? "runtime_recovery_failed"
      : "recovering_runtime";
  if (status !== expectedStatus) throw new Error("Runtime recovery status authority is invalid");
  const replacementGeneration = requiredNullable(
    recovery,
    "replacement_generation",
    (item) => positiveSafeInteger(item, "Runtime recovery replacement generation"),
  );
  const outcomeReason = requiredNullable(recovery, "outcome_reason", (item) => (
    requireString(item, "Runtime recovery outcome reason")
  ));
  return {
    status,
    phase,
    failed_generation: positiveSafeInteger(recovery.failed_generation, "Runtime recovery failed generation"),
    ...(replacementGeneration === undefined ? {} : { replacement_generation: replacementGeneration }),
    attempt_count: nonNegativeSafeInteger(recovery.attempt_count, "Runtime recovery attempt count"),
    continuity_status: closedString(
      recovery.continuity_status,
      ["pending", "proven", "unproven"] as const,
      "Runtime recovery continuity status",
    ),
    evidence_at: requireTimestamp(recovery.evidence_at, "Runtime recovery evidence_at"),
    ...(outcomeReason === undefined ? {} : { outcome_reason: outcomeReason }),
  };
}

function parseWaitingUser(
  value: unknown,
  sessionStatus: string,
  recovery: WorkerDetailData["runtime_recovery"],
): NonNullable<WorkerDetailData["waiting_user"]> {
  if (sessionStatus !== "waiting_user") throw new Error("Worker waiting-user state is invalid");
  const waiting = requireRecord(value, "Worker waiting-user state");
  const recoveryFailed = recovery?.phase === "exhausted";
  const expectedReason = recoveryFailed ? "runtime_recovery_failed" : "worker_waiting_user";
  const expectedMessage = recoveryFailed
    ? "Runtime recovery stopped after its single safe replacement attempt."
    : "Worker needs input before it can continue.";
  const actions = requireArray(waiting.allowed_actions, "Worker waiting-user actions").map((item) => (
    requireString(item, "Worker waiting-user action")
  ));
  if (
    waiting.reason !== expectedReason
    || waiting.message !== expectedMessage
    || actions.length !== 2
    || actions[0] !== "reply"
    || actions[1] !== "cancel"
  ) {
    throw new Error("Worker waiting-user state is invalid");
  }
  return { reason: expectedReason, message: expectedMessage, allowed_actions: actions };
}

function validateTaskTuple(
  status: string | undefined,
  label: string | undefined,
  revision: number | undefined,
  taskId: string | undefined,
): void {
  if (status === undefined) {
    if (label !== undefined || revision !== undefined || taskId !== undefined) {
      throw new Error("Worker Task tuple is incomplete");
    }
    return;
  }
  if (
    label !== taskStatusLabel(status)
    || revision === undefined
    || taskId === undefined
  ) {
    throw new Error("Worker Task tuple is incomplete");
  }
}

function validateRunTurnTuple(tuple: {
  runStatus: string | undefined;
  activeWorkerRunId: string | undefined;
  activeRunRevision: number | undefined;
  startedAt: string | undefined;
  agentProfile: string;
  turnStatus: string | undefined;
  activeAgentTurnId: string | undefined;
  activeTurnRevision: number | undefined;
  deadlineAt: string | undefined;
}): void {
  if (tuple.runStatus === undefined) {
    if (
      tuple.activeWorkerRunId !== undefined
      || tuple.activeRunRevision !== undefined
      || tuple.startedAt !== undefined
      || tuple.agentProfile !== ""
      || tuple.turnStatus !== undefined
      || tuple.activeAgentTurnId !== undefined
      || tuple.activeTurnRevision !== undefined
      || tuple.deadlineAt !== undefined
    ) {
      throw new Error("WorkerRun tuple is incomplete");
    }
    return;
  }
  if (
    tuple.activeWorkerRunId === undefined
    || tuple.activeRunRevision === undefined
    || tuple.startedAt === undefined
    || tuple.agentProfile === ""
  ) {
    throw new Error("WorkerRun tuple is incomplete");
  }
  if (tuple.turnStatus === undefined) {
    if (
      tuple.activeAgentTurnId !== undefined
      || tuple.activeTurnRevision !== undefined
      || tuple.deadlineAt !== undefined
    ) {
      throw new Error("AgentTurn tuple is incomplete");
    }
    return;
  }
  if (
    tuple.activeAgentTurnId === undefined
    || tuple.activeTurnRevision === undefined
    || tuple.deadlineAt === undefined
  ) {
    throw new Error("AgentTurn tuple is incomplete");
  }
}

function sessionTone(status: typeof sessionStatuses[number]): StatusTone {
  switch (status) {
    case "interactive":
    case "running":
      return "active";
    case "waiting_user":
    case "idle":
    case "allocating":
      return "waiting";
    case "lost":
    case "expired":
    case "closed":
      return "danger";
    case "recovering":
      return "neutral";
  }
}

function taskStatusLabel(status: string): string {
  if (status === "completed") return "Accepted";
  return status.split("_").map((part) => (
    part === "" ? "" : part[0].toUpperCase() + part.slice(1)
  )).join(" ");
}

function isContinueMetadata(value: Readonly<Record<string, unknown>> | undefined): boolean {
  if (value === undefined || Reflect.ownKeys(value).length !== 1) return false;
  const kinds = value.interaction_kinds;
  return Array.isArray(kinds)
    && kinds.length === 2
    && new Set(kinds).size === 2
    && kinds.every((kind) => kind === "chat" || kind === "rework");
}

function requireNoMetadata(action: Readonly<ActionRef>): void {
  if (Object.hasOwn(action, "metadata")) throw new Error("Worker action metadata is invalid");
}

function isPreservedSparseLegacyContract(value: Record<string, unknown>): boolean {
  const groups = value.timeline_groups;
  if (!Array.isArray(groups) || groups.length === 0) return false;
  const first = groups[0];
  const legacyTurn = first !== null && typeof first === "object" && !Array.isArray(first)
    ? first as Record<string, unknown>
    : undefined;
  return typeof value.worker_session_id === "string"
    && Object.hasOwn(value, "timeline_cursor")
    && typeof value.timeline_has_more === "boolean"
    && !Object.hasOwn(value, "session_revision")
    && !Object.hasOwn(value, "workspace")
    && !Object.hasOwn(value, "status")
    && legacyTurn?.mode === "prompt"
    && legacyTurn.input !== null
    && typeof legacyTurn.input === "object"
    && Array.isArray(legacyTurn.updates)
    && !Object.hasOwn(legacyTurn, "display_stream_id");
}

function parseTimelineCursor(value: unknown): string {
  const cursor = canonicalIdentity(value, "Worker timeline cursor");
  if (!/^[A-Za-z0-9_-]+$/.test(cursor)) throw new Error("Worker timeline cursor is invalid");
  try {
    const padded = cursor.replace(/-/g, "+").replace(/_/g, "/")
      .padEnd(Math.ceil(cursor.length / 4) * 4, "=");
    const decoded = atob(padded);
    const separator = decoded.indexOf("\0");
    if (
      separator <= 0
      || !/^[1-9][0-9]*$/.test(decoded.slice(0, separator))
      || !isLegacyTimelineCursorPayloadIdentity(decoded.slice(separator + 1))
    ) {
      throw new Error("invalid cursor payload");
    }
  } catch {
    throw new Error("Worker timeline cursor is invalid");
  }
  return cursor;
}

function requiredNullable<T>(
  record: Record<string, unknown>,
  key: string,
  parse: (value: unknown) => T,
): T | undefined {
  if (!Object.hasOwn(record, key)) throw new Error(`${key} is missing`);
  return record[key] === null ? undefined : parse(record[key]);
}

function optionalOmitted<T>(
  record: Record<string, unknown>,
  key: string,
  parse: (value: unknown) => T,
): T | undefined {
  return Object.hasOwn(record, key) ? parse(record[key]) : undefined;
}

function closedString<const T extends readonly string[]>(
  value: unknown,
  allowed: T,
  label: string,
): T[number] {
  const parsed = requireString(value, label);
  if (!(allowed as readonly string[]).includes(parsed)) throw new Error(`${label} is invalid`);
  return parsed as T[number];
}

function canonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function optionalCanonicalIdentityOrEmpty(value: unknown, label: string): string {
  if (value === "") return "";
  return canonicalIdentity(value, label);
}

function isLegacyTimelineCursorPayloadIdentity(value: string): boolean {
  return value.trim() === value && !/[\r\n\0]/.test(value);
}

function requireText(value: unknown, label: string): string {
  if (typeof value !== "string") throw new Error(`${label} is invalid`);
  return value;
}

function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegativeSafeInteger(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function sha256Digest(value: unknown, label: string): string {
  const parsed = requireString(value, label);
  if (!/^sha256:[a-f0-9]{64}$/.test(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function requireUnique(values: readonly string[], label: string): void {
  if (new Set(values).size !== values.length) throw new Error(`${label} is duplicated`);
}
