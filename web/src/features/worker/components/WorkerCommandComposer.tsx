import { CircleStop, CornerDownRight, ListEnd, Route, RotateCcw, Send } from "lucide-react";
import { useEffect, useState } from "react";
import type { ActionRef } from "../../../api/contracts";
import {
  newTaskIdempotencyKey,
  type TaskContinuationRequest,
} from "../../../api/taskCommands";
import {
  GatewayCommandError,
  newCommandIdempotencyKey,
  type CommandResult,
  type WorkerCommandAction,
  type WorkerCommandRequest,
  type WorkerCommandTarget,
} from "../../../api/workerCommands";
import type {
  ComposerAuthorityAttempt,
  TaskContinuationTarget,
} from "../useWorkerCommandAuthority";

type ComposerAction = Extract<WorkerCommandAction, "follow-up" | "queue" | "steer">;
type ComposerAttempt = ComposerAuthorityAttempt & { authorityGeneration: number };

export function WorkerCommandComposer({
  target,
  availableActions,
  pendingCount,
  mutationBlocked = () => false,
  retryBlocked,
  authorityGeneration = () => 0,
  onCommand,
  taskContinuationTarget,
  onTaskContinue,
  onStale,
}: {
  target?: WorkerCommandTarget;
  availableActions: ActionRef[];
  pendingCount: number;
  mutationBlocked?: (action: string) => boolean;
  retryBlocked?: (attempt: ComposerAuthorityAttempt) => boolean;
  authorityGeneration?: (attempt: ComposerAuthorityAttempt) => number;
  onCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
  taskContinuationTarget?: TaskContinuationTarget;
  onTaskContinue?: (request: TaskContinuationRequest) => Promise<CommandResult>;
  onStale?: () => Promise<void>;
}) {
  const canContinueTask = Boolean(taskContinuationTarget && hasTaskContinueActionRef(
    availableActions,
    taskContinuationTarget.taskId,
    taskContinuationTarget.expectedTaskRevision,
    "chat",
  ));
  const modes: ComposerAction[] = canContinueTask ? ["follow-up"] :
    (["follow-up", "queue", "steer"] as const).filter((action) =>
      target && hasWorkerActionRef(availableActions, action, target));
  const [mode, setMode] = useState<ComposerAction>(modes[0] ?? "follow-up");
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState<ComposerAttempt>();
  const [submitting, setSubmitting] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: "success" | "error" | "neutral"; message: string }>();
  const retryMutationBlocked = retryBlocked ?? ((candidate: ComposerAuthorityAttempt) =>
    mutationBlocked(candidate.type === "task" ? "task.continue" : candidate.request.action));

  const submit = async (candidate: ComposerAuthorityAttempt | ComposerAttempt) => {
    const nextAttempt: ComposerAttempt = "authorityGeneration" in candidate
      ? candidate
      : { ...candidate, authorityGeneration: authorityGeneration(candidate) };
    setAttempt(nextAttempt);
    setSubmitting(true);
    setFeedback(undefined);
    try {
      const result = nextAttempt.type === "task" ?
        await onTaskContinue?.(nextAttempt.request) :
        await onCommand(nextAttempt.request);
      if (!result) throw new GatewayCommandError("command_service_unavailable");
      if (nextAttempt.draftSnapshot !== undefined) {
        setMessage((current) => current === nextAttempt.draftSnapshot ? "" : current);
      }
      setAttempt(undefined);
      setFeedback({
        tone: "success",
        message: result.queue_sequence ? `Queued at position ${result.queue_sequence}.` : "Command accepted. Waiting for Worker state.",
      });
    } catch (error) {
      const commandError = error instanceof GatewayCommandError ? error : new GatewayCommandError("network_error");
      if (commandError.code === "stale_target") {
        setAttempt(undefined);
        try {
          await onStale?.();
        } catch {
          // The original stale result remains the useful user-facing error.
        }
        setFeedback({ tone: "error", message: "Worker state changed. The latest state is loaded; review your draft and send it again." });
      } else {
        const action = nextAttempt.type === "worker" ? nextAttempt.request.action : "follow-up";
        setFeedback({ tone: "error", message: feedbackFor(commandError, action) });
      }
    } finally {
      setSubmitting(false);
    }
  };

  const createRequest = (action: WorkerCommandAction, content?: string): WorkerCommandRequest | undefined => {
    if (!target || !hasWorkerActionRef(availableActions, action, target)) return undefined;
    return { action, target, idempotencyKey: newCommandIdempotencyKey(), content };
  };
  const selectedMode = modes.includes(mode) ? mode : modes[0] ?? "follow-up";
  const enabled = Boolean((canContinueTask && taskContinuationTarget) || target);
  const selectedAction = canContinueTask ? "task.continue" : selectedMode;
  const commandBlocked = mutationBlocked(selectedAction);
  const currentAttemptGeneration = attempt ? authorityGeneration(attempt) : 0;
  const retryIsBlocked = attempt ? !attemptStillAuthorized(
    attempt,
    availableActions,
    target,
    taskContinuationTarget,
  ) || retryMutationBlocked(attempt) : true;
  useEffect(() => {
    if (!attempt || attempt.authorityGeneration === currentAttemptGeneration) return;
    setAttempt(undefined);
    setSubmitting(false);
    setFeedback({
      tone: "error",
      message: "Command authority changed. Review your draft and send it again.",
    });
  }, [attempt, currentAttemptGeneration]);

  return <div className="command-composer">
    <form onSubmit={(event) => {
      event.preventDefault();
      const content = message.trim();
      if (commandBlocked) return;
      if (canContinueTask && taskContinuationTarget) {
        void submit({ type: "task", draftSnapshot: message, request: {
          ...taskContinuationTarget,
          interactionKind: "chat",
          idempotencyKey: newTaskIdempotencyKey(),
          message: content,
        } });
        return;
      }
      const request = createRequest(selectedMode, content);
      if (request) void submit({ type: "worker", request, draftSnapshot: message });
    }}>
      <textarea value={message} onChange={(event) => setMessage(event.target.value)} placeholder="Continue with this worker" aria-label="Continue with this worker" disabled={!enabled} />
      <div className="composer-toolbar">
        <div className="composer-modes" aria-label="Message delivery mode">
          {modes.map((action) => <button type="button" className={selectedMode === action ? "selected" : ""} key={action} onClick={() => setMode(action)} title={canContinueTask ? "Continue the delivered Task" : modeTitle(action)} aria-label={canContinueTask ? "Continue the delivered Task" : modeTitle(action)}>{modeIcon(action)}<span>{modeLabel(action)}</span></button>)}
        </div>
        <div className="composer-actions">
          {pendingCount > 0 ? <span className="pending-count">{pendingCount} pending</span> : null}
          {target && hasWorkerActionRef(availableActions, "interrupt", target) ? <button type="button" className="icon-button interrupt-button" title="Interrupt active turn" aria-label="Interrupt active turn" disabled={submitting || mutationBlocked("interrupt")} onClick={() => {
            const request = createRequest("interrupt");
            if (request) void submit({ type: "worker", request });
          }}><CircleStop size={17} /></button> : null}
          <button className="icon-button send-button" title="Send" aria-label="Send" disabled={!enabled || submitting || commandBlocked || !message.trim() || modes.length === 0}><Send size={17} /></button>
        </div>
      </div>
    </form>
    <span className="session-availability">Session remains available for follow-up</span>
    {feedback ? <div className={`command-feedback feedback-${feedback.tone}`} role={feedback.tone === "error" ? "alert" : "status"}>
      <span>{feedback.message}</span>
      {attempt ? <button type="button" onClick={() => void submit(attempt)} disabled={submitting || retryIsBlocked}><RotateCcw size={13} /> Retry</button> : null}
    </div> : null}
  </div>;
}

function actionRef(action: WorkerCommandAction): string {
  return action === "follow-up" ? "worker.follow_up" : `worker.${action}`;
}

function hasWorkerActionRef(
  availableActions: ActionRef[],
  action: WorkerCommandAction,
  target: WorkerCommandTarget,
): boolean {
  if (action === "approval") return false;
  const turnOwned = action === "interrupt" || action === "steer";
  return hasActionRef(
    availableActions,
    actionRef(action),
    turnOwned ? target.agentTurnId : target.workerSessionId,
    turnOwned ? target.expectedTurnRevision : target.expectedSessionRevision,
  );
}

function hasActionRef(
  availableActions: ActionRef[],
  action: string,
  targetID: string,
  expectedRevision: number,
): boolean {
  return availableActions.some((reference) =>
    reference.action === action &&
    reference.target_id === targetID &&
    reference.expected_revision === expectedRevision);
}

function attemptStillAuthorized(
  attempt: ComposerAttempt,
  availableActions: ActionRef[],
  target?: WorkerCommandTarget,
  taskTarget?: TaskContinuationTarget,
): boolean {
  if (attempt.type === "worker") {
    return Boolean(target &&
      sameExecutionTarget(attempt.request.target, target) &&
      hasWorkerActionRef(availableActions, attempt.request.action, target));
  }
  return Boolean(taskTarget &&
    sameTaskContinuationTarget(attempt.request, taskTarget) &&
    hasTaskContinueActionRef(
      availableActions,
      taskTarget.taskId,
      taskTarget.expectedTaskRevision,
      attempt.request.interactionKind,
    ));
}

function hasTaskContinueActionRef(
  availableActions: ActionRef[],
  taskID: string,
  expectedRevision: number,
  interactionKind: TaskContinuationRequest["interactionKind"],
): boolean {
  return availableActions.some((reference) => {
    if (reference.action !== "task.continue" ||
      reference.target_id !== taskID ||
      reference.expected_revision !== expectedRevision) {
      return false;
    }
    const kinds = reference.metadata?.interaction_kinds;
    return Array.isArray(kinds) && kinds.some((kind) => kind === interactionKind);
  });
}

function sameExecutionTarget(left: WorkerCommandTarget, right: WorkerCommandTarget): boolean {
  return left.workerSessionId === right.workerSessionId &&
    left.expectedSessionRevision === right.expectedSessionRevision &&
    left.workerRunId === right.workerRunId &&
    left.expectedRunRevision === right.expectedRunRevision &&
    left.agentTurnId === right.agentTurnId &&
    left.expectedTurnRevision === right.expectedTurnRevision;
}

function sameTaskContinuationTarget(
  attempt: TaskContinuationRequest,
  target: TaskContinuationTarget,
): boolean {
  return attempt.taskId === target.taskId &&
    attempt.expectedTaskRevision === target.expectedTaskRevision &&
    attempt.workerSessionId === target.workerSessionId &&
    attempt.expectedSessionRevision === target.expectedSessionRevision &&
    attempt.workerRunId === target.workerRunId &&
    attempt.expectedRunRevision === target.expectedRunRevision &&
    attempt.agentTurnId === target.agentTurnId &&
    attempt.expectedTurnRevision === target.expectedTurnRevision;
}

function modeIcon(action: ComposerAction) {
  if (action === "queue") return <ListEnd size={14} />;
  if (action === "steer") return <Route size={14} />;
  return <CornerDownRight size={14} />;
}

function modeLabel(action: ComposerAction): string {
  return action === "follow-up" ? "Follow up" : action === "queue" ? "Queue" : "Steer";
}

function modeTitle(action: ComposerAction): string {
  if (action === "queue") return "Queue after current turn";
  if (action === "steer") return "Steer active turn";
  return "Start a follow-up turn";
}

function feedbackFor(error: GatewayCommandError, action: WorkerCommandAction): string {
  if (error.code === "unsupported_command" && action === "steer") return "Steer is unavailable. Queue a message or interrupt the active turn instead.";
  return error.message;
}
