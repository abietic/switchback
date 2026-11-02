import { Ban, Check, Clock3, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import type { ActionRef, ApprovalRequest } from "../../../api/contracts";
import {
  GatewayCommandError,
  newCommandIdempotencyKey,
  type CommandResult,
  type WorkerCommandRequest,
  type WorkerCommandTarget,
} from "../../../api/workerCommands";

export function ApprovalControls({
  approvals,
  target,
  availableActions,
  authorityGeneration = () => 0,
  mutationBlocked = () => false,
  retryBlocked = () => false,
  onCommand,
  onRetryCommand,
}: {
  approvals: ApprovalRequest[];
  target?: WorkerCommandTarget;
  availableActions: ActionRef[];
  authorityGeneration?: (approvalRequestID: string) => number;
  mutationBlocked?: (action: string, approvalRequestID: string) => boolean;
  retryBlocked?: (request: WorkerCommandRequest) => boolean;
  onCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
  onRetryCommand?: (request: WorkerCommandRequest) => Promise<CommandResult>;
}) {
  if (approvals.length === 0) return null;
  return <section className="side-section approval-section">
    <div className="panel-title"><ShieldCheck size={17} /><h2>Approval required</h2></div>
    {approvals.map((approval) => <ApprovalItem
      approval={approval}
      target={target}
      enabled={availableActions.some((action) => action.action === "worker.approval" && action.target_id === approval.approval_request_id && action.expected_revision === approval.revision)}
      mutationBlocked={mutationBlocked("approval", approval.approval_request_id)}
      retryBlocked={retryBlocked}
      onCommand={onCommand}
      onRetryCommand={onRetryCommand ?? onCommand}
      key={`${approval.approval_request_id}:${authorityGeneration(approval.approval_request_id)}`}
    />)}
  </section>;
}

function ApprovalItem({
  approval,
  target,
  enabled,
  mutationBlocked,
  retryBlocked,
  onCommand,
  onRetryCommand,
}: {
  approval: ApprovalRequest;
  target?: WorkerCommandTarget;
  enabled: boolean;
  mutationBlocked: boolean;
  retryBlocked: (request: WorkerCommandRequest) => boolean;
  onCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
  onRetryCommand: (request: WorkerCommandRequest) => Promise<CommandResult>;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [feedback, setFeedback] = useState<string>();
  const [attempt, setAttempt] = useState<WorkerCommandRequest>();
  const [deliveryUnknown, setDeliveryUnknown] = useState(false);
  const expired = Date.parse(approval.expires_at) <= Date.now();
  const canAct = Boolean(target && enabled && !mutationBlocked && !expired && !submitting && !attempt);
  const canActOnce = canAct && hasApprovalScope(approval.scope_options, "once");
  const canActForSession = canAct && hasApprovalScope(approval.scope_options, "session");

  const submit = async (request: WorkerCommandRequest, retry = false) => {
    setSubmitting(true);
    setFeedback(undefined);
    try {
      await (retry ? onRetryCommand : onCommand)(request);
      setDeliveryUnknown(false);
      setAttempt(undefined);
      setFeedback("Decision accepted. Waiting for Worker state.");
    } catch (error) {
      setFeedback(error instanceof GatewayCommandError ? error.message : "Approval status is unknown. Refresh before acting again.");
      if (isUnknownDelivery(error)) {
        setDeliveryUnknown(true);
      } else {
        setDeliveryUnknown(false);
        setAttempt(undefined);
      }
    } finally {
      setSubmitting(false);
    }
  };
  const decide = (decision: "approve" | "deny", scope: "once" | "session") => {
    if (!target || !canAct || !hasApprovalScope(approval.scope_options, scope)) return;
    const request: WorkerCommandRequest = {
      action: "approval",
      target,
      idempotencyKey: newCommandIdempotencyKey(),
      approval: {
        approval_request_id: approval.approval_request_id,
        expected_approval_revision: approval.revision,
        decision,
        scope,
        capability: approval.capability,
        target: approval.target,
        effect: approval.effect,
      },
    };
    setAttempt(request);
    void submit(request);
  };
  const retryAuthorized = Boolean(attempt && target && attempt.approval &&
    sameExecutionTarget(attempt.target, target) &&
    attempt.approval.approval_request_id === approval.approval_request_id &&
    attempt.approval.expected_approval_revision === approval.revision &&
    attempt.approval.capability === approval.capability &&
    attempt.approval.target === approval.target &&
    attempt.approval.effect === approval.effect &&
    hasApprovalScope(approval.scope_options, attempt.approval.scope) &&
    enabled && !expired);
  useEffect(() => {
    if (!deliveryUnknown || !attempt || retryAuthorized) return;
    setDeliveryUnknown(false);
    setAttempt(undefined);
    setFeedback("Approval authority changed. The previous retry is no longer valid.");
  }, [attempt, deliveryUnknown, retryAuthorized]);

  return <div className="approval-item">
    <strong>{approval.capability}</strong>
    <span>{approval.target}</span>
    <small>{approval.reason}</small>
    <dl><div><dt>Effect</dt><dd>{humanStatus(approval.effect)}</dd></div><div><dt><Clock3 size={12} /> Expiry</dt><dd>Expires {formatExpiry(approval.expires_at)}</dd></div></dl>
    <div className="approval-actions">
      <button className="secondary-button deny-button" disabled={!canActOnce} onClick={() => decide("deny", "once")}><Ban size={14} /> Deny</button>
      <button className="secondary-button" disabled={!canActOnce} onClick={() => decide("approve", "once")}><Check size={14} /> Approve once</button>
      {hasApprovalScope(approval.scope_options, "session") ? <button className="primary-button" disabled={!canActForSession} onClick={() => decide("approve", "session")}><ShieldCheck size={14} /> Approve for session</button> : null}
    </div>
    {expired ? <p className="approval-feedback">This request expired. Wait for a refreshed approval request.</p> : feedback ? <p className="approval-feedback" role="status">{feedback}</p> : null}
    {attempt ? <div className="approval-actions">
      <button
        type="button"
        className="secondary-button"
        disabled={submitting || retryBlocked(attempt) || !retryAuthorized}
        onClick={() => void submit(attempt, true)}
      >Retry approval decision</button>
    </div> : null}
  </div>;
}

function sameExecutionTarget(left: WorkerCommandTarget, right: WorkerCommandTarget): boolean {
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

function isUnknownDelivery(error: unknown): boolean {
  return error instanceof GatewayCommandError &&
    (error.code === "network_error" || error.code === "command_service_unavailable");
}

function formatExpiry(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return "invalid";
  return date.toISOString().slice(0, 16).replace("T", " ") + " UTC";
}

function humanStatus(value: string): string {
  return value.split("_").map((part) => part ? part[0].toUpperCase() + part.slice(1) : part).join(" ");
}
