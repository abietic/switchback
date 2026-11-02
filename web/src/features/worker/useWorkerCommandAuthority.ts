import { useCallback, useLayoutEffect, useReducer, useState } from "react";
import type { ActionRef, WorkerDetailData } from "../../api/contracts";
import {
  GatewayCommandError,
  type CommandResult,
  type WorkerCommandRequest,
  type WorkerCommandTarget,
} from "../../api/workerCommands";
import type { TaskCommandRequest, TaskContinuationRequest } from "../../api/taskCommands";
import type { PendingCommand } from "../../state/clientViewReplica";

const workerTurnMutationActions = new Set(["follow-up", "queue", "steer", "interrupt", "task.continue"]);
const taskDeliveryMutationActions = new Set(["accept", "rework", "cancel", "task.continue"]);

export interface UnknownTaskDeliveryAttempt {
  request: TaskCommandRequest;
  workerSessionId: string;
}

export type ComposerAuthorityAttempt =
  | { type: "worker"; request: WorkerCommandRequest; draftSnapshot?: string }
  | { type: "task"; request: TaskContinuationRequest; draftSnapshot: string };

export type TaskContinuationTarget = Omit<
  TaskContinuationRequest,
  "interactionKind" | "idempotencyKey" | "message"
>;

interface CommittedDeliveryAuthority {
  worker: WorkerDetailData;
  availableActions: ActionRef[];
  commandTarget?: WorkerCommandTarget;
  mutationsEnabled: boolean;
  onCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
  onTaskCommand: (request: TaskCommandRequest) => Promise<CommandResult>;
  onTaskContinue: (request: TaskContinuationRequest) => Promise<CommandResult>;
}

interface MutationAttemptToken {
  id: number;
  generation: number;
  action: string;
  scopeKey: string;
}

interface ActiveMutationAttempt extends MutationAttemptToken {
  requestFingerprint: string;
  authorityFingerprint?: string;
  authorize?: (authority: CommittedDeliveryAuthority) => boolean;
  fingerprintAuthority?: (authority: CommittedDeliveryAuthority) => string | undefined;
}

export class CommandAuthoritySupersededError extends Error {
  constructor() {
    super("Command authority changed before delivery completed");
    this.name = "CommandAuthoritySupersededError";
  }
}

interface AuthorityInput {
  worker: WorkerDetailData;
  availableActions: ActionRef[];
  pendingCommands: readonly PendingCommand[];
  mutationsEnabled: boolean;
  onCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
  onTaskCommand: (request: TaskCommandRequest) => Promise<CommandResult>;
  onTaskContinue: (request: TaskContinuationRequest) => Promise<CommandResult>;
}

class WorkerCommandAuthority {
  private committed?: CommittedDeliveryAuthority;
  private pendingCommands: readonly PendingCommand[] = [];
  private attempts = new Map<string, ActiveMutationAttempt>();
  private scopeGenerations = new Map<string, number>();
  private nextAttemptID = 0;

  constructor(private readonly publish: () => void) {}

  commit(input: AuthorityInput): void {
    const authority: CommittedDeliveryAuthority = {
      worker: input.worker,
      availableActions: input.availableActions,
      commandTarget: commandTargetFor(input.worker),
      mutationsEnabled: input.mutationsEnabled,
      onCommand: input.onCommand,
      onTaskCommand: input.onTaskCommand,
      onTaskContinue: input.onTaskContinue,
    };
    this.committed = authority;
    this.pendingCommands = input.pendingCommands;
    let changed = false;
    for (const [scopeKey, attempt] of this.attempts) {
      const nextFingerprint = attempt.fingerprintAuthority?.(authority);
      const remainsAuthorized = authority.mutationsEnabled &&
        (!attempt.authorize || attempt.authorize(authority)) &&
        (!attempt.fingerprintAuthority ||
          (nextFingerprint !== undefined && nextFingerprint === attempt.authorityFingerprint));
      if (remainsAuthorized) continue;
      this.attempts.delete(scopeKey);
      this.advanceScopeGeneration(scopeKey);
      changed = true;
    }
    if (changed) this.publish();
  }

  dispose(): void {
    this.committed = undefined;
    this.pendingCommands = [];
    this.attempts.clear();
    this.scopeGenerations.clear();
  }

  generation(scopeKey: string): number {
    return this.scopeGenerations.get(scopeKey) ?? 0;
  }

  hasActiveConflict(action: string, scopeKey?: string): boolean {
    return [...this.attempts.values()].some((attempt) =>
      mutationOwnersConflict(action, scopeKey, attempt.action, attempt.scopeKey));
  }

  executeWorker(request: WorkerCommandRequest): Promise<CommandResult> {
    return this.run(
      request.action,
      workerCommandScopeKey(request),
      request.idempotencyKey,
      stableJSON(request),
      (authority) => authority.onCommand(request),
      (authority) => workerAttemptAuthorized(request, authority),
      (authority) => workerAttemptAuthorityFingerprint(request, authority),
    );
  }

  executeApproval(request: WorkerCommandRequest): Promise<CommandResult> {
    return this.run(
      request.action,
      approvalCommandScopeKey(request),
      request.idempotencyKey,
      stableJSON(request),
      (authority) => authority.onCommand(request),
      (authority) => approvalAttemptAuthorized(
        request,
        authority.worker,
        authority.availableActions,
        authority.commandTarget,
      ),
      (authority) => approvalAttemptAuthorityFingerprint(request, authority),
    );
  }

  executeTaskDecision(attempt: UnknownTaskDeliveryAttempt): Promise<CommandResult> {
    return this.run(
      attempt.request.action,
      taskDecisionScopeKey(attempt),
      attempt.request.idempotencyKey,
      stableJSON(attempt),
      (authority) => authority.onTaskCommand(attempt.request),
      (authority) => taskDecisionAttemptAuthorized(
        attempt,
        authority.worker,
        authority.availableActions,
      ),
      (authority) => taskDecisionAuthorityFingerprint(attempt, authority),
    );
  }

  executeTaskContinuation(request: TaskContinuationRequest): Promise<CommandResult> {
    return this.run(
      "task.continue",
      taskContinuationScopeKey(request),
      request.idempotencyKey,
      stableJSON(request),
      (authority) => authority.onTaskContinue(request),
      (authority) => taskContinuationAttemptAuthorized(request, authority),
      (authority) => taskContinuationAuthorityFingerprint(request, authority),
    );
  }

  private async run(
    action: string,
    scopeKey: string,
    clientCommandId: string,
    requestFingerprint: string,
    command: (authority: CommittedDeliveryAuthority) => Promise<CommandResult>,
    authorize: (authority: CommittedDeliveryAuthority) => boolean,
    fingerprintAuthority: (authority: CommittedDeliveryAuthority) => string | undefined,
  ): Promise<CommandResult> {
    const token = this.begin(
      action,
      scopeKey,
      clientCommandId,
      requestFingerprint,
      authorize,
      fingerprintAuthority,
    );
    const authority = this.committed;
    if (!authority) throw new GatewayCommandError("stale_target");
    try {
      const result = await command(authority);
      this.settleAccepted(token, authorize, fingerprintAuthority);
      return result;
    } catch (cause) {
      if (cause instanceof CommandAuthoritySupersededError) throw cause;
      const remainsAuthorized = this.attemptRemainsAuthorized(token, authorize, fingerprintAuthority);
      if (isUnknownDelivery(cause) && remainsAuthorized) throw cause;
      this.release(token);
      if (!remainsAuthorized) throw new CommandAuthoritySupersededError();
      throw cause;
    }
  }

  private begin(
    action: string,
    scopeKey: string,
    clientCommandId: string,
    requestFingerprint: string,
    authorize: (authority: CommittedDeliveryAuthority) => boolean,
    fingerprintAuthority: (authority: CommittedDeliveryAuthority) => string | undefined,
  ): MutationAttemptToken {
    const authority = this.committed;
    const authorityFingerprint = authority && fingerprintAuthority(authority);
    if (!authority || !authority.mutationsEnabled || !authorize(authority) || authorityFingerprint === undefined) {
      throw new GatewayCommandError("stale_target");
    }
    const conflicts = this.pendingCommands.some((candidate) =>
      mutationOwnersConflict(action, scopeKey, candidate.action, candidate.scopeKey) &&
      (candidate.clientCommandId !== clientCommandId ||
        candidate.action !== action ||
        candidate.scopeKey !== scopeKey))
      || [...this.attempts.values()].some((candidate) => {
        if (!mutationOwnersConflict(action, scopeKey, candidate.action, candidate.scopeKey)) return false;
        return candidate.scopeKey !== scopeKey || candidate.requestFingerprint !== requestFingerprint;
      });
    if (conflicts) throw new Error("Worker mutations require a live authoritative projection");

    const token: ActiveMutationAttempt = {
      id: this.nextAttemptID + 1,
      generation: this.generation(scopeKey),
      action,
      scopeKey,
      requestFingerprint,
      authorityFingerprint,
      authorize,
      fingerprintAuthority,
    };
    this.nextAttemptID = token.id;
    this.attempts.set(scopeKey, token);
    this.publish();
    return token;
  }

  private settleAccepted(
    token: MutationAttemptToken,
    authorize: (authority: CommittedDeliveryAuthority) => boolean,
    fingerprintAuthority: (authority: CommittedDeliveryAuthority) => string | undefined,
  ): void {
    if (!this.attemptRemainsAuthorized(token, authorize, fingerprintAuthority)) {
      throw new CommandAuthoritySupersededError();
    }
    this.release(token);
  }

  private attemptRemainsAuthorized(
    token: MutationAttemptToken,
    authorize: (authority: CommittedDeliveryAuthority) => boolean,
    fingerprintAuthority: (authority: CommittedDeliveryAuthority) => string | undefined,
  ): boolean {
    const authority = this.committed;
    const active = this.attempts.get(token.scopeKey);
    const currentFingerprint = authority && fingerprintAuthority(authority);
    return this.tokenIsCurrent(token) && Boolean(authority) && Boolean(active) &&
      authorize(authority as CommittedDeliveryAuthority) &&
      currentFingerprint !== undefined && currentFingerprint === active?.authorityFingerprint;
  }

  private release(token: MutationAttemptToken): void {
    if (!this.tokenIsCurrent(token)) return;
    this.attempts.delete(token.scopeKey);
    this.publish();
  }

  private tokenIsCurrent(token: MutationAttemptToken): boolean {
    const active = this.attempts.get(token.scopeKey);
    return Boolean(active &&
      active.id === token.id &&
      active.generation === token.generation &&
      active.scopeKey === token.scopeKey &&
      this.generation(token.scopeKey) === token.generation);
  }

  private advanceScopeGeneration(scopeKey: string): void {
    this.scopeGenerations.set(scopeKey, this.generation(scopeKey) + 1);
  }
}

export function useWorkerCommandAuthority(input: AuthorityInput) {
  const [, publish] = useReducer((revision: number) => revision + 1, 0);
  const [authority] = useState(() => new WorkerCommandAuthority(publish));
  const commandTarget = commandTargetFor(input.worker);
  const taskContinuationTarget = taskContinuationTargetFor(input.worker, commandTarget);

  useLayoutEffect(() => authority.commit(input));
  useLayoutEffect(() => () => authority.dispose(), [authority]);

  const isMutationBlocked = useCallback((action: string, scopeKey?: string) => {
    if (!input.mutationsEnabled) return true;
    return input.pendingCommands.some((command) =>
      mutationOwnersConflict(action, scopeKey, command.action, command.scopeKey))
      || authority.hasActiveConflict(action, scopeKey);
  }, [authority, input.mutationsEnabled, input.pendingCommands]);
  const isRetryMutationBlocked = useCallback((
    action: string,
    scopeKey: string | undefined,
    clientCommandId: string,
  ) => {
    if (!input.mutationsEnabled) return true;
    return input.pendingCommands.some((command) =>
      mutationOwnersConflict(action, scopeKey, command.action, command.scopeKey) &&
      (command.clientCommandId !== clientCommandId ||
        command.action !== action ||
        command.scopeKey !== scopeKey));
  }, [input.mutationsEnabled, input.pendingCommands]);

  return {
    commandTarget,
    taskContinuationTarget,
    executeWorkerCommand: useCallback(
      (request: WorkerCommandRequest) => authority.executeWorker(request),
      [authority],
    ),
    executeApprovalCommand: useCallback(
      (request: WorkerCommandRequest) => authority.executeApproval(request),
      [authority],
    ),
    executeTaskCommand: useCallback(
      (attempt: UnknownTaskDeliveryAttempt) => authority.executeTaskDecision(attempt),
      [authority],
    ),
    executeTaskContinuation: useCallback(
      (request: TaskContinuationRequest) => authority.executeTaskContinuation(request),
      [authority],
    ),
    composerMutationBlocked: useCallback((action: string) => {
      const scopeKey = action === "task.continue" && taskContinuationTarget
        ? taskContinuationScopeKey(taskContinuationTarget)
        : commandTarget ? workerCommandScopeKeyFor(action, commandTarget) : undefined;
      return isMutationBlocked(action, scopeKey);
    }, [commandTarget, isMutationBlocked, taskContinuationTarget]),
    composerRetryBlocked: useCallback((attempt: ComposerAuthorityAttempt) => {
      const action = attempt.type === "task" ? "task.continue" : attempt.request.action;
      const scopeKey = attempt.type === "task"
        ? taskContinuationScopeKey(attempt.request)
        : workerCommandScopeKey(attempt.request);
      return isRetryMutationBlocked(action, scopeKey, attempt.request.idempotencyKey);
    }, [isRetryMutationBlocked]),
    composerAuthorityGeneration: useCallback((attempt: ComposerAuthorityAttempt) => {
      const scopeKey = attempt.type === "task"
        ? taskContinuationScopeKey(attempt.request)
        : workerCommandScopeKey(attempt.request);
      return authority.generation(scopeKey);
    }, [authority]),
    approvalMutationBlocked: useCallback(
      (action: string, approvalRequestID: string) =>
        isMutationBlocked(action, approvalScopeKey(approvalRequestID)),
      [isMutationBlocked],
    ),
    approvalRetryBlocked: useCallback(
      (request: WorkerCommandRequest) => isRetryMutationBlocked(
        request.action,
        approvalCommandScopeKey(request),
        request.idempotencyKey,
      ),
      [isRetryMutationBlocked],
    ),
    approvalAuthorityGeneration: useCallback(
      (approvalRequestID: string) => authority.generation(approvalScopeKey(approvalRequestID)),
      [authority],
    ),
    taskDecisionMutationBlocked: useCallback(
      (action: string) => isMutationBlocked(
        action,
        taskDecisionScopeKeyFor(action, input.worker.bound_task_id, input.worker.worker_session_id),
      ),
      [input.worker.bound_task_id, input.worker.worker_session_id, isMutationBlocked],
    ),
    taskDecisionRetryBlocked: useCallback(
      (attempt: UnknownTaskDeliveryAttempt) => isRetryMutationBlocked(
        attempt.request.action,
        taskDecisionScopeKey(attempt),
        attempt.request.idempotencyKey,
      ),
      [isRetryMutationBlocked],
    ),
    taskDecisionAuthorityKey: `${authority.generation(taskDecisionScopeKeyFor(
      "accept",
      input.worker.bound_task_id,
      input.worker.worker_session_id,
    ))}-${authority.generation(taskDecisionScopeKeyFor(
      "rework",
      input.worker.bound_task_id,
      input.worker.worker_session_id,
    ))}`,
  };
}

function commandTargetFor(worker: WorkerDetailData): WorkerCommandTarget | undefined {
  return worker.active_worker_run_id && worker.active_run_revision &&
    worker.active_agent_turn_id && worker.active_turn_revision ? {
      workerSessionId: worker.worker_session_id,
      expectedSessionRevision: worker.session_revision,
      workerRunId: worker.active_worker_run_id,
      expectedRunRevision: worker.active_run_revision,
      agentTurnId: worker.active_agent_turn_id,
      expectedTurnRevision: worker.active_turn_revision,
    } : undefined;
}

function taskContinuationTargetFor(
  worker: WorkerDetailData,
  target?: WorkerCommandTarget,
): TaskContinuationTarget | undefined {
  return target && worker.bound_task_id && worker.task_revision ? {
    taskId: worker.bound_task_id,
    expectedTaskRevision: worker.task_revision,
    workerSessionId: target.workerSessionId,
    expectedSessionRevision: target.expectedSessionRevision,
    workerRunId: target.workerRunId,
    expectedRunRevision: target.expectedRunRevision,
    agentTurnId: target.agentTurnId,
    expectedTurnRevision: target.expectedTurnRevision,
  } : undefined;
}

function workerMutationsConflict(next: string, active: string): boolean {
  if (next === active) return true;
  return (workerTurnMutationActions.has(next) && workerTurnMutationActions.has(active))
    || (taskDeliveryMutationActions.has(next) && taskDeliveryMutationActions.has(active));
}

function mutationOwnersConflict(
  nextAction: string,
  nextScopeKey: string | undefined,
  activeAction: string,
  activeScopeKey: string | undefined,
): boolean {
  if (nextAction === "approval" && activeAction === "approval") {
    return !nextScopeKey || !activeScopeKey || nextScopeKey === activeScopeKey;
  }
  return workerMutationsConflict(nextAction, activeAction);
}

function approvalAttemptAuthorized(
  request: WorkerCommandRequest,
  worker: WorkerDetailData,
  availableActions: ActionRef[],
  target?: WorkerCommandTarget,
): boolean {
  const attempted = request.approval;
  if (request.action !== "approval" || !attempted || !target ||
    !sameWorkerCommandTarget(request.target, target)) return false;
  const current = worker.approvals.find((approval) =>
    approval.approval_request_id === attempted.approval_request_id);
  return Boolean(current &&
    current.revision === attempted.expected_approval_revision &&
    current.capability === attempted.capability &&
    current.target === attempted.target &&
    current.effect === attempted.effect &&
    hasApprovalScope(current.scope_options, attempted.scope) &&
    availableActions.some((action) =>
      action.action === "worker.approval" &&
      action.target_id === attempted.approval_request_id &&
      action.expected_revision === attempted.expected_approval_revision));
}

function taskDecisionAttemptAuthorized(
  attempt: UnknownTaskDeliveryAttempt,
  worker: WorkerDetailData,
  availableActions: ActionRef[],
): boolean {
  const request = attempt.request;
  if ((request.action !== "accept" && request.action !== "rework") ||
    attempt.workerSessionId !== worker.worker_session_id ||
    request.taskId !== worker.bound_task_id ||
    request.expectedTaskRevision !== worker.task_revision) return false;
  return availableActions.some((action) =>
    action.action === `task.${request.action}` &&
    action.target_id === request.taskId &&
    action.expected_revision === request.expectedTaskRevision);
}

function sameWorkerCommandTarget(left: WorkerCommandTarget, right: WorkerCommandTarget): boolean {
  return left.workerSessionId === right.workerSessionId &&
    left.expectedSessionRevision === right.expectedSessionRevision &&
    left.workerRunId === right.workerRunId &&
    left.expectedRunRevision === right.expectedRunRevision &&
    left.agentTurnId === right.agentTurnId &&
    left.expectedTurnRevision === right.expectedTurnRevision;
}

function hasApprovalScope(value: unknown, scope: "once" | "session"): boolean {
  return Array.isArray(value) &&
    value.every((candidate) => candidate === "once" || candidate === "session") &&
    value.includes(scope);
}

function approvalScopeKey(approvalRequestID: string): string {
  return `approval:${approvalRequestID}`;
}

function approvalCommandScopeKey(request: WorkerCommandRequest): string {
  return approvalScopeKey(request.approval?.approval_request_id ?? "invalid");
}

function taskDecisionScopeKey(attempt: UnknownTaskDeliveryAttempt): string {
  return taskDecisionScopeKeyFor(
    attempt.request.action,
    attempt.request.taskId,
    attempt.workerSessionId,
  );
}

export function taskDecisionScopeKeyFor(
  action: string,
  taskID: string | undefined,
  workerSessionID: string,
): string {
  return `task-decision:${workerSessionID}:${taskID ?? "none"}:${action}`;
}

export function taskContinuationScopeKey(
  request: Pick<TaskContinuationRequest, "workerSessionId" | "taskId">,
): string {
  return `task-continue:${request.workerSessionId}:${request.taskId}`;
}

export function workerCommandScopeKey(request: WorkerCommandRequest): string {
  if (request.action === "approval") return approvalCommandScopeKey(request);
  return workerCommandScopeKeyFor(request.action, request.target);
}

function workerCommandScopeKeyFor(action: string, target: WorkerCommandTarget): string {
  const owner = action === "interrupt" || action === "steer"
    ? target.agentTurnId
    : target.workerSessionId;
  return `worker-command:${action}:${owner}`;
}

function workerAttemptAuthorized(
  request: WorkerCommandRequest,
  authority: CommittedDeliveryAuthority,
): boolean {
  if (request.action === "approval") {
    return approvalAttemptAuthorized(
      request,
      authority.worker,
      authority.availableActions,
      authority.commandTarget,
    );
  }
  const target = authority.commandTarget;
  if (!target || !sameWorkerCommandTarget(request.target, target)) return false;
  const turnOwned = request.action === "interrupt" || request.action === "steer";
  const actionName = request.action === "follow-up" ? "worker.follow_up" : `worker.${request.action}`;
  return matchingActionRefs(
    authority.availableActions,
    actionName,
    turnOwned ? target.agentTurnId : target.workerSessionId,
    turnOwned ? target.expectedTurnRevision : target.expectedSessionRevision,
  ).length > 0;
}

function workerAttemptAuthorityFingerprint(
  request: WorkerCommandRequest,
  authority: CommittedDeliveryAuthority,
): string | undefined {
  if (!workerAttemptAuthorized(request, authority)) return undefined;
  const turnOwned = request.action === "interrupt" || request.action === "steer";
  const actionName = request.action === "follow-up" ? "worker.follow_up" : `worker.${request.action}`;
  return stableJSON({
    execution: authority.commandTarget,
    action_refs: matchingActionRefs(
      authority.availableActions,
      actionName,
      turnOwned ? request.target.agentTurnId : request.target.workerSessionId,
      turnOwned ? request.target.expectedTurnRevision : request.target.expectedSessionRevision,
    ),
  });
}

function approvalAttemptAuthorityFingerprint(
  request: WorkerCommandRequest,
  authority: CommittedDeliveryAuthority,
): string | undefined {
  if (!approvalAttemptAuthorized(
    request,
    authority.worker,
    authority.availableActions,
    authority.commandTarget,
  )) return undefined;
  const attempted = request.approval;
  if (!attempted) return undefined;
  const approval = authority.worker.approvals.find((candidate) =>
    candidate.approval_request_id === attempted.approval_request_id);
  if (!approval) return undefined;
  return stableJSON({
    execution: authority.commandTarget,
    approval,
    action_refs: matchingActionRefs(
      authority.availableActions,
      "worker.approval",
      attempted.approval_request_id,
      attempted.expected_approval_revision,
    ),
  });
}

function taskDecisionAuthorityFingerprint(
  attempt: UnknownTaskDeliveryAttempt,
  authority: CommittedDeliveryAuthority,
): string | undefined {
  if (!taskDecisionAttemptAuthorized(
    attempt,
    authority.worker,
    authority.availableActions,
  )) return undefined;
  const request = attempt.request;
  return stableJSON({
    worker_session_id: authority.worker.worker_session_id,
    task: {
      id: authority.worker.bound_task_id,
      revision: authority.worker.task_revision,
    },
    action_refs: matchingActionRefs(
      authority.availableActions,
      `task.${request.action}`,
      request.taskId,
      request.expectedTaskRevision,
    ),
  });
}

function taskContinuationAttemptAuthorized(
  request: TaskContinuationRequest,
  authority: CommittedDeliveryAuthority,
): boolean {
  const target = authority.commandTarget;
  if (!target ||
    request.taskId !== authority.worker.bound_task_id ||
    request.expectedTaskRevision !== authority.worker.task_revision ||
    request.workerSessionId !== target.workerSessionId ||
    request.expectedSessionRevision !== target.expectedSessionRevision ||
    request.workerRunId !== target.workerRunId ||
    request.expectedRunRevision !== target.expectedRunRevision ||
    request.agentTurnId !== target.agentTurnId ||
    request.expectedTurnRevision !== target.expectedTurnRevision) return false;
  return matchingActionRefs(
    authority.availableActions,
    "task.continue",
    request.taskId,
    request.expectedTaskRevision,
  ).some((reference) => {
    const kinds = reference.metadata?.interaction_kinds;
    return Array.isArray(kinds) && kinds.some((kind) => kind === request.interactionKind);
  });
}

function taskContinuationAuthorityFingerprint(
  request: TaskContinuationRequest,
  authority: CommittedDeliveryAuthority,
): string | undefined {
  if (!taskContinuationAttemptAuthorized(request, authority)) return undefined;
  return stableJSON({
    execution: authority.commandTarget,
    task: {
      id: authority.worker.bound_task_id,
      revision: authority.worker.task_revision,
    },
    action_refs: matchingActionRefs(
      authority.availableActions,
      "task.continue",
      request.taskId,
      request.expectedTaskRevision,
    ),
  });
}

function matchingActionRefs(
  availableActions: ActionRef[],
  action: string,
  targetID: string,
  expectedRevision: number,
): ActionRef[] {
  return availableActions
    .filter((reference) =>
      reference.action === action &&
      reference.target_id === targetID &&
      reference.expected_revision === expectedRevision)
    .map((reference) => ({ ...reference }))
    .sort((left, right) => stableJSON(left).localeCompare(stableJSON(right)));
}

function stableJSON(value: unknown): string {
  return JSON.stringify(canonicalJSONValue(value));
}

function canonicalJSONValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalJSONValue);
  if (!value || typeof value !== "object") return value;
  const object = value as Record<string, unknown>;
  return Object.keys(object).sort().reduce<Record<string, unknown>>((canonical, key) => {
    const item = object[key];
    if (item !== undefined) canonical[key] = canonicalJSONValue(item);
    return canonical;
  }, {});
}

export function isUnknownDelivery(cause: unknown): boolean {
  return cause instanceof GatewayCommandError &&
    (cause.code === "network_error" || cause.code === "command_service_unavailable");
}
