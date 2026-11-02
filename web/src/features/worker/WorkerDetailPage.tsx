import {
  AlertCircle,
  Check,
  Clock3,
  HeartPulse,
} from "lucide-react";
import type { ActionRef, TransientDisplayDelta, WorkerDetailData } from "../../api/contracts";
import type { WorkerDiagnosticSelection } from "../../api/workerDiagnostics";
import {
  type CommandResult,
  type WorkerCommandRequest,
} from "../../api/workerCommands";
import { newTaskIdempotencyKey, type TaskCommandRequest, type TaskContinuationRequest } from "../../api/taskCommands";
import {
  useEffect,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { StatusBadge } from "../../components/ui/StatusBadge";
import type { PendingCommand } from "../../state/clientViewReplica";
import { ApprovalControls } from "./components/ApprovalControls";
import { WorkerCommandComposer } from "./components/WorkerCommandComposer";
import {
  CommandAuthoritySupersededError,
  isUnknownDelivery,
  type UnknownTaskDeliveryAttempt,
  useWorkerCommandAuthority,
} from "./useWorkerCommandAuthority";
import { WorkerTimeline } from "./WorkerTimeline";
import { WorkerDiagnostics } from "./WorkerDiagnostics";
import { ArtifactWorkbench } from "./workbench/ArtifactWorkbench";

const minimumWorkbenchWidth = 360;
const defaultWorkbenchWidth = 520;
const workbenchResizeStep = 24;

export function WorkerDetailPage({
  worker,
  transient = [],
  availableActions = [],
  pendingCommands = [],
  mutationsEnabled = true,
  onCommand = async () => { throw new Error("command client unavailable"); },
  onTaskCommand = async () => { throw new Error("Task command client unavailable"); },
  onTaskContinue = async () => { throw new Error("Task continuation client unavailable"); },
  onStale = async () => {},
  diagnosticSelection,
  onDiagnosticSelectionChange,
}: {
  worker: WorkerDetailData;
  transient?: TransientDisplayDelta[];
  availableActions?: ActionRef[];
  pendingCommands?: readonly PendingCommand[];
  mutationsEnabled?: boolean;
  onCommand?: (request: WorkerCommandRequest) => Promise<CommandResult>;
  onTaskCommand?: (request: TaskCommandRequest) => Promise<CommandResult>;
  onTaskContinue?: (request: TaskContinuationRequest) => Promise<CommandResult>;
  onStale?: () => Promise<void>;
  diagnosticSelection?: WorkerDiagnosticSelection;
  onDiagnosticSelectionChange?: (selection: WorkerDiagnosticSelection) => void;
}) {
  const commandAuthority = useWorkerCommandAuthority({
    worker,
    availableActions,
    pendingCommands,
    mutationsEnabled,
    onCommand,
    onTaskCommand,
    onTaskContinue,
  });
  const { commandTarget, taskContinuationTarget } = commandAuthority;
  const [workbenchMaximum, setWorkbenchMaximum] = useState(() => maximumWorkbenchWidth());
  const [workbenchWidth, setWorkbenchWidth] = useState(() =>
    clampWorkbenchWidth(defaultWorkbenchWidth, maximumWorkbenchWidth()));
  useEffect(() => {
    const resize = () => {
      const maximum = maximumWorkbenchWidth();
      setWorkbenchMaximum(maximum);
      setWorkbenchWidth((current) => clampWorkbenchWidth(current, maximum));
    };
    window.addEventListener("resize", resize);
    return () => window.removeEventListener("resize", resize);
  }, []);
  const [localSelection, setLocalSelection] = useState<WorkerDiagnosticSelection>({ kind: "none" });
  const selection = diagnosticSelection ?? localSelection;
  const selectDiagnostics = (next: WorkerDiagnosticSelection) => {
    if (onDiagnosticSelectionChange) onDiagnosticSelectionChange(next);
    else setLocalSelection(next);
  };
  const selectedArtifact = selection.kind === "artifact" ? worker.artifacts.find((artifact) => artifact.artifact_id === selection.artifactId) : undefined;
  const diagnosticTarget = selection.kind === "none" ? { sessionId: worker.worker_session_id }
    : selection.kind === "exact" ? { sessionId: worker.worker_session_id, runId: selection.runId, turnId: selection.turnId }
      : selectedArtifact ? { sessionId: worker.worker_session_id, runId: selectedArtifact.worker_run_id, turnId: selectedArtifact.agent_turn_id } : undefined;
  const hasReviewAction = availableActions.some((action) =>
    (action.action === "task.accept" || action.action === "task.rework") &&
    action.target_id === worker.bound_task_id &&
    action.expected_revision === worker.task_revision,
  );
  const startResize = (event: ReactPointerEvent<HTMLButtonElement>) => {
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = workbenchWidth;
    const move = (next: PointerEvent) => {
      setWorkbenchWidth(clampWorkbenchWidth(
        startWidth + startX - next.clientX,
        maximumWorkbenchWidth(),
      ));
    };
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };
  const resizeWithKeyboard = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    const direction = event.key === "ArrowLeft" ? 1 : event.key === "ArrowRight" ? -1 : 0;
    if (direction === 0) return;
    event.preventDefault();
    setWorkbenchWidth((current) => clampWorkbenchWidth(
      current + direction * workbenchResizeStep,
      maximumWorkbenchWidth(),
    ));
  };
  return (
    <main className="worker-grid" data-layout="worker-focused" style={{ "--workbench-width": `${workbenchWidth}px` } as CSSProperties}>
      <section className="worker-main">
        <div className="worker-summary-band">
          <div><StatusBadge label={humanStatus(worker.status)} tone={worker.status_tone} /><strong>{worker.workspace}</strong></div>
          <div className="summary-metrics"><span><HeartPulse size={14} /> {worker.runtime_health}</span><span>{worker.runtime_recovery?.replacement_generation ? `Generation ${worker.runtime_recovery.failed_generation} → ${worker.runtime_recovery.replacement_generation}` : `Generation ${worker.runtime_generation}`}</span></div>
        </div>
        <ExecutionStatus worker={worker} />
        <RuntimeRecoveryStatus worker={worker} />
        <div className="worker-execution-scroll" data-region="worker-execution">
          <WorkerTimeline
            workerSessionId={worker.worker_session_id}
            initialGroups={worker.timeline_groups ?? []}
            initialCursor={worker.timeline_cursor}
            initialHasMore={worker.timeline_has_more ?? false}
            transient={transient}
            onArtifact={(artifactId) => selectDiagnostics({ kind: "artifact", artifactId })}
            onDiagnosticTarget={(runId, turnId) => selectDiagnostics({ kind: "exact", runId, turnId })}
          />
        </div>
        <WorkerCommandComposer
          target={commandTarget}
          taskContinuationTarget={taskContinuationTarget}
          availableActions={availableActions}
          pendingCount={pendingCommands.length}
          mutationBlocked={commandAuthority.composerMutationBlocked}
          retryBlocked={commandAuthority.composerRetryBlocked}
          authorityGeneration={commandAuthority.composerAuthorityGeneration}
          onCommand={commandAuthority.executeWorkerCommand}
          onTaskContinue={commandAuthority.executeTaskContinuation}
          onStale={onStale}
        />
      </section>
      <button
        type="button"
        role="separator"
        className="workbench-divider"
        aria-label="Resize artifact workbench"
        aria-orientation="vertical"
        aria-valuemin={minimumWorkbenchWidth}
        aria-valuemax={workbenchMaximum}
        aria-valuenow={workbenchWidth}
        title="Drag or use arrow keys to resize artifact workbench"
        onPointerDown={startResize}
        onKeyDown={resizeWithKeyboard}
      />
      <aside className="worker-side">
        <ArtifactWorkbench
          artifacts={worker.artifacts}
          selectedArtifactId={selection.kind === "artifact" ? selection.artifactId : undefined}
          onArtifactSelectionChange={(artifactId) => selectDiagnostics({ kind: "artifact", artifactId })}
          onRequestedArtifactHandled={() => {}}
        />
        {worker.waiting_user ? <WaitingUser worker={worker} /> : null}
        <ApprovalControls
          approvals={worker.approvals}
          target={commandTarget}
          availableActions={availableActions}
          authorityGeneration={commandAuthority.approvalAuthorityGeneration}
          mutationBlocked={commandAuthority.approvalMutationBlocked}
          retryBlocked={commandAuthority.approvalRetryBlocked}
          onCommand={commandAuthority.executeApprovalCommand}
          onRetryCommand={commandAuthority.executeApprovalCommand}
        />
        {hasReviewAction ? <section className="side-section delivery-review">
          <h2>Delivery review</h2>
          <ReviewActions
            key={`review-actions-${worker.worker_session_id}-${worker.bound_task_id ?? "none"}-${
              commandAuthority.taskDecisionAuthorityKey
            }`}
            worker={worker}
            availableActions={availableActions}
            mutationBlocked={commandAuthority.taskDecisionMutationBlocked}
            retryBlocked={commandAuthority.taskDecisionRetryBlocked}
            onCommand={commandAuthority.executeTaskCommand}
            onRetryCommand={commandAuthority.executeTaskCommand}
          />
        </section> : null}
        <section className="side-section metadata-section">
          <h2>Runtime</h2>
          <dl className="metadata-list"><div><dt>Session</dt><dd>{worker.worker_session_id}</dd></div><div><dt>Run</dt><dd>{worker.active_worker_run_id ?? "None"}</dd></div><div><dt>Profile</dt><dd>{worker.agent_profile}</dd></div></dl>
        </section>
        <section className="side-section metadata-section" aria-label="Diagnostic target">
          <p>{selection.kind === "none" ? "Session diagnostics" : selection.kind === "artifact" ? `Artifact ${selection.artifactId}` : selection.kind === "exact" ? `Run ${selection.runId}${selection.turnId ? ` · Turn ${selection.turnId}` : ""}` : "Invalid diagnostic selection"}</p>
          {selection.kind !== "none" ? <button type="button" onClick={() => selectDiagnostics({ kind: "none" })}>Session diagnostics</button> : null}
        </section>
        {diagnosticTarget ? <WorkerDiagnostics {...diagnosticTarget} /> : <section className="side-section" aria-label="Diagnostics"><h2>Diagnostics</h2><p role="status">Selected diagnostic target unavailable.</p></section>}
      </aside>
    </main>
  );
}

function maximumWorkbenchWidth(): number {
  return Math.max(minimumWorkbenchWidth, window.innerWidth - 480);
}

function clampWorkbenchWidth(width: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimumWorkbenchWidth, width));
}

function ReviewActions({
  worker,
  availableActions,
  mutationBlocked,
  retryBlocked,
  onCommand,
  onRetryCommand,
}: {
  worker: WorkerDetailData;
  availableActions: ActionRef[];
  mutationBlocked: (action: string) => boolean;
  retryBlocked: (attempt: UnknownTaskDeliveryAttempt) => boolean;
  onCommand: (attempt: UnknownTaskDeliveryAttempt) => Promise<CommandResult>;
  onRetryCommand: (attempt: UnknownTaskDeliveryAttempt) => Promise<CommandResult>;
}) {
  const [message, setMessage] = useState("");
  const [feedback, setFeedback] = useState<string>();
  const [submitting, setSubmitting] = useState(false);
  const [attempt, setAttempt] = useState<UnknownTaskDeliveryAttempt>();
  const [deliveryUnknown, setDeliveryUnknown] = useState(false);
  const target = worker.bound_task_id && worker.task_revision ? { taskId: worker.bound_task_id, revision: worker.task_revision } : undefined;
  const canAccept = Boolean(target && availableActions.some((action) => action.action === "task.accept" && action.target_id === target.taskId && action.expected_revision === target.revision));
  const canRework = Boolean(target && availableActions.some((action) => action.action === "task.rework" && action.target_id === target.taskId && action.expected_revision === target.revision));
  const submit = async (deliveryAttempt: UnknownTaskDeliveryAttempt, retry = false) => {
    if (!target) return;
    const request = deliveryAttempt.request;
    setSubmitting(true);
    setFeedback(undefined);
    try {
      await (retry ? onRetryCommand : onCommand)(deliveryAttempt);
      setDeliveryUnknown(false);
      setAttempt(undefined);
      setFeedback(request.action === "accept" ? "Acceptance submitted." : "Rework submitted.");
      if (request.action === "rework") setMessage("");
    } catch (cause) {
      setFeedback(cause instanceof Error ? cause.message : "Task command failed.");
      if (isUnknownDelivery(cause)) {
        setDeliveryUnknown(true);
      } else if (cause instanceof CommandAuthoritySupersededError) {
        return;
      } else {
        setDeliveryUnknown(false);
        setAttempt(undefined);
      }
    } finally {
      setSubmitting(false);
    }
  };
  const begin = (action: "accept" | "rework") => {
    if (!target || attempt) return;
    const request: TaskCommandRequest = {
      taskId: target.taskId,
      action,
      expectedTaskRevision: target.revision,
      idempotencyKey: newTaskIdempotencyKey(),
      message: action === "rework" ? message.trim() : undefined,
    };
    const deliveryAttempt = { request, workerSessionId: worker.worker_session_id };
    setAttempt(deliveryAttempt);
    void submit(deliveryAttempt);
  };
  const attemptAuthorized = Boolean(attempt && target &&
    attempt.workerSessionId === worker.worker_session_id &&
    attempt.request.taskId === target.taskId &&
    attempt.request.expectedTaskRevision === target.revision &&
    ((attempt.request.action === "accept" && canAccept) || (attempt.request.action === "rework" && canRework)));
  useEffect(() => {
    if (!deliveryUnknown || !attempt || attemptAuthorized) return;
    setDeliveryUnknown(false);
    setAttempt(undefined);
    setFeedback("Task authority changed. The previous retry is no longer valid.");
  }, [attempt, attemptAuthorized, deliveryUnknown]);
  if (!canAccept && !canRework) return null;
  return <div className="review-command">
    {canRework ? <textarea aria-label="Request changes message" placeholder="Describe the requested change" value={message} disabled={Boolean(attempt)} onChange={(event) => setMessage(event.target.value)} /> : null}
    <div className="review-actions">
      {canRework ? <button className="secondary-button" disabled={submitting || Boolean(attempt) || mutationBlocked("rework") || !message.trim()} onClick={() => begin("rework")}>Request changes</button> : null}
      {canAccept ? <button className="primary-button" disabled={submitting || Boolean(attempt) || mutationBlocked("accept")} onClick={() => begin("accept")}><Check size={16} /> Accept</button> : null}
    </div>
    {feedback ? <p className="review-feedback" role="status">{feedback}</p> : null}
    {attempt ? <div className="review-actions">
      <button
        type="button"
        className="secondary-button"
        disabled={submitting || !attemptAuthorized || retryBlocked(attempt)}
        onClick={() => void submit(attempt, true)}
      >Retry Task decision</button>
    </div> : null}
  </div>;
}

function ExecutionStatus({ worker }: { worker: WorkerDetailData }) {
  const network = worker.network_profile
    ? `${humanStatus(worker.network_profile.id)} · ${humanStatus(worker.network_profile.enforcement_status)}`
    : undefined;
  const items = [
    ["Task", worker.task_status_label ?? canonicalTaskStatus(worker.task_status)],
    ["Run", worker.run_status],
    ["Turn", worker.turn_status],
    ["Recovery", worker.runtime_recovery?.status],
    ["Network", network],
  ].filter((item): item is [string, string] => Boolean(item[1]));
  if (items.length === 0) return null;
  return <div className="execution-status" aria-label="Execution status" role="status">{items.map(([label, status]) => (
    <span key={label}><small>{label}</small><strong>{humanStatus(status)}</strong></span>
  ))}</div>;
}

function RuntimeRecoveryStatus({ worker }: { worker: WorkerDetailData }) {
  const recovery = worker.runtime_recovery;
  if (!recovery || recovery.status === "runtime_recovered") return null;
  return <section
    aria-label="Runtime recovery"
    className={`recovery-status ${recovery.status}`}
    role={recovery.status === "runtime_recovery_failed" ? "alert" : "status"}
  >
    <div><strong>{humanStatus(recovery.status)}</strong><span>{humanStatus(recovery.phase)}</span></div>
    <p>Generation {recovery.failed_generation}{recovery.replacement_generation ? ` → ${recovery.replacement_generation}` : ""} · Attempt {recovery.attempt_count}/1 · {humanStatus(recovery.continuity_status)}</p>
    <small>Evidence {new Date(recovery.evidence_at).toLocaleString()}{recovery.outcome_reason ? ` · ${recoveryOutcome(recovery.outcome_reason)}` : ""}</small>
  </section>;
}

function canonicalTaskStatus(status?: string): string | undefined {
  return status === "completed" ? "Accepted" : status;
}

function WaitingUser({ worker }: { worker: WorkerDetailData }) {
  const waiting = worker.waiting_user;
  if (!waiting) return null;
  return <section className="side-section attention-section">
    <div className="panel-title"><AlertCircle size={17} /><h2>Needs your input</h2></div>
    <p className="stage-copy">{waiting.message}</p>
    <div className="reason-row"><span>{humanStatus(waiting.reason)}</span><small>{waiting.allowed_actions.map(humanStatus).join(" · ")}</small></div>
  </section>;
}

function humanStatus(value: string): string {
  return value.split("_").map((part) => part ? part[0].toUpperCase() + part.slice(1) : part).join(" ");
}

function recoveryOutcome(value: string): string {
  const outcomes: Record<string, string> = {
    agent_turn_deadline: "Agent turn deadline reached",
    preempted_by_backend_deadline: "Backend deadline took authority",
    recovery_continuity_unproven: "Runtime continuity could not be proven",
    replacement_environment_failed: "Replacement environment unavailable",
    replacement_first_heartbeat_timeout: "Replacement heartbeat timed out",
    replacement_health_failed: "Replacement health verification failed",
    replacement_identity_mismatch: "Replacement identity did not match",
    replacement_provision_failed: "Replacement provisioning failed",
    replacement_runtime_stable: "Replacement Runtime is stable",
    runtime_fencing_failed: "Failed Runtime fencing could not be proven",
    worker_session_absolute_expired: "Worker Session reached its absolute expiry",
  };
  return outcomes[value] ?? "Recovery details are unavailable";
}
