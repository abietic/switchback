import { AlertCircle, Bot, Check, ChevronRight, CircleDashed, FileCheck2, ListTodo, LoaderCircle, MessageSquare, RotateCcw, Send, SlidersHorizontal, Sparkles } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { ActionRef, AssistantConversationData, RelatedRef } from "../../api/contracts";
import type { AssistantCommandResult, ConfirmationTarget, WorkspaceSelection } from "../../api/assistantCommands";
import { actOnAssistantCheckpoint, confirmAssistantTask, newAssistantIdempotencyKey, retryAssistantRun, selectAssistantWorkspace, sendAssistantMessage } from "../../api/assistantCommands";
import type { PendingCommand } from "../../state/clientViewReplica";

type Command = (
  kind: string,
  clientCommandId: string,
  run: () => Promise<AssistantCommandResult>,
) => Promise<void>;
type MutationCommand = (
  action: string,
  run: (clientCommandId: string) => Promise<AssistantCommandResult>,
) => Promise<boolean>;

export function AssistantConversationPage({ data, actions, relatedRefs, pendingCommands, mutationsEnabled = true, onCommand, onNavigate }: {
  data: AssistantConversationData;
  actions: ActionRef[];
  relatedRefs: RelatedRef[];
  pendingCommands: readonly PendingCommand[];
  mutationsEnabled?: boolean;
  onCommand: Command;
  onNavigate: (href: string) => void;
}) {
  const submittingRef = useRef(new Set<string>());
  const [submittingActions, setSubmittingActions] = useState<string[]>([]);
  const releaseSubmitting = (action: string) => {
    if (!submittingRef.current.delete(action)) return;
    setSubmittingActions([...submittingRef.current]);
  };
  useEffect(() => {
    for (const command of pendingCommands) releaseSubmitting(command.action);
  }, [pendingCommands]);
  const isBlocked = (action: string) => {
    if (!mutationsEnabled) return true;
    return [
      ...pendingCommands.map((command) => command.action),
      ...submittingRef.current,
    ].some((active) => mutationsConflict(action, active));
  };
  const submitMutation: MutationCommand = async (action, run) => {
    if (isBlocked(action)) return false;
    submittingRef.current.add(action);
    setSubmittingActions([...submittingRef.current]);
    const clientCommandId = newAssistantIdempotencyKey();
    try {
      await onCommand(action, clientCommandId, () => run(clientCommandId));
      return true;
    } catch {
      releaseSubmitting(action);
      return false;
    }
  };
  const canSend = permits(actions, "assistant.send_message", data.conversation_id, data.revision) && data.status === "active";
  const checkpoint = data.checkpoint?.status === "active" && permits(actions, "assistant.checkpoint", data.checkpoint.checkpoint_id, data.checkpoint.revision) ? data.checkpoint : undefined;
  const canRetry = data.assistant_run?.status === "failed" && data.assistant_run.retryable && permits(actions, "assistant.retry_run", data.assistant_run.assistant_run_id, data.assistant_run.revision);
  const confirmation = confirmationTarget(data);
  const canConfirm = Boolean(confirmation && data.task_draft?.status === "current" && permits(actions, "assistant.confirm_task", data.task_draft.task_draft_id, data.task_draft.revision));
  return <main className="assistant-layout" data-layout="assistant-focused">
    <section className="assistant-thread" data-region="primary" aria-label="Assistant conversation">
      <ConversationStatus run={data.assistant_run} pendingCount={pendingCommands.length} submittingCount={submittingActions.length} />
      <div className="assistant-turns">
        {[...data.turns].sort((left, right) => left.sequence - right.sequence).map((turn) => <article className={`assistant-turn ${turn.role}`} key={turn.turn_id}>
          <span className="assistant-turn-icon">{turn.role === "assistant" ? <Sparkles size={16} /> : <MessageSquare size={16} />}</span>
          <div><header><strong>{turn.role === "assistant" ? "Assistant" : "You"}</strong><time>{formatTime(turn.created_at)}</time></header><p>{turn.content}</p></div>
        </article>)}
      </div>
      {canRetry && data.assistant_run ? <button className="retry-run" disabled={isBlocked("retry")} onClick={() => void submitMutation("retry", (clientCommandId) => retryAssistantRun(fetch, { conversationId: data.conversation_id, expectedRevision: data.revision, runId: data.assistant_run!.assistant_run_id, runRevision: data.assistant_run!.revision, idempotencyKey: clientCommandId }))}><RotateCcw size={15} /> Retry this response</button> : null}
      <MessageComposer editorDisabled={!canSend || Boolean(checkpoint)} submitDisabled={isBlocked("send_message")} submitting={submittingActions.includes("send_message")} pending={pendingCommands.some((command) => command.action === "send_message")} onSend={(content) => submitMutation("send_message", (clientCommandId) => sendAssistantMessage(fetch, { conversationId: data.conversation_id, expectedRevision: data.revision, content, idempotencyKey: clientCommandId }))} />
    </section>
    <aside className="assistant-context" data-region="secondary" aria-label="Assistant readiness and context">
      <WorkspacePanel data={data} canSend={canSend} blocked={isBlocked("select_workspace")} onCommand={submitMutation} />
      <ClarificationPanel data={data} checkpoint={checkpoint} isBlocked={isBlocked} onCommand={submitMutation} />
      <DraftPanel data={data} confirmation={confirmation} canConfirm={canConfirm} blocked={isBlocked("confirm_task")} onCommand={submitMutation} />
      <HandoffPanel handoffs={data.handoffs} refs={relatedRefs} onNavigate={onNavigate} />
    </aside>
  </main>;
}

function ConversationStatus({ run, pendingCount, submittingCount }: { run?: AssistantConversationData["assistant_run"]; pendingCount: number; submittingCount: number }) {
  const label = run?.status === "queued" ? "Queued" : run?.status === "claimed" ? "Working" : run?.status === "failed" ? "Needs attention" : "Ready";
  const Icon = run?.status === "failed" ? AlertCircle : run?.status === "claimed" || run?.status === "queued" ? LoaderCircle : Check;
  const detail = run?.status === "failed" ? diagnostic(run.error_code) : run?.status === "claimed" ? "Assistant run is active." : run?.status === "queued" ? "Assistant run is queued." : "Conversation projection is current";
  return <div className={`assistant-status ${run?.status ?? "ready"}`} role={run?.status === "failed" ? "alert" : "status"}><Icon aria-hidden="true" size={16} /><strong>{label}</strong><span>{detail}</span>{submittingCount ? <span>Submitting command</span> : null}{pendingCount ? <span>Command pending projection</span> : null}</div>;
}

function MessageComposer({ editorDisabled, submitDisabled, submitting, pending, onSend }: { editorDisabled: boolean; submitDisabled: boolean; submitting: boolean; pending: boolean; onSend: (content: string) => Promise<boolean> }) {
  const [content, setContent] = useState("");
  return <form className="assistant-composer" onSubmit={(event) => { event.preventDefault(); const submittedContent = content; const message = submittedContent.trim(); if (!message || editorDisabled || submitDisabled) return; void onSend(message).then((accepted) => { if (accepted) setContent((current) => current === submittedContent ? "" : current); }); }}>
    <textarea aria-label="Message the Assistant" value={content} onChange={(event) => setContent(event.target.value)} disabled={editorDisabled} placeholder={editorDisabled ? "Wait for the current question or state update." : "Describe what you want to deliver"} />
    <footer><span>{pending ? "Waiting for the durable projection" : submitting ? "Submitting command" : "Assistant drafts work; you confirm before delivery starts."}</span><button className="primary-button send-button" aria-label="Send message" disabled={editorDisabled || submitDisabled || !content.trim()}><Send size={16} /> Send</button></footer>
  </form>;
}

function WorkspacePanel({ data, canSend, blocked, onCommand }: { data: AssistantConversationData; canSend: boolean; blocked: boolean; onCommand: MutationCommand }) {
  const [open, setOpen] = useState(false);
  const [workspace, setWorkspace] = useState({
    workspaceId: "",
    workspaceRevisionId: "",
    workspaceResolutionId: "",
    workspaceResolutionRevision: "",
    membershipRevision: "",
  });
  const valid =
    Boolean(workspace.workspaceId.trim())
    && Boolean(workspace.workspaceRevisionId.trim())
    && Boolean(workspace.workspaceResolutionId.trim())
    && positiveInteger(workspace.workspaceResolutionRevision)
    && positiveInteger(workspace.membershipRevision);
  if (data.workspace_id) return <section className="assistant-panel"><div className="panel-title"><SlidersHorizontal size={17} /><h2>Workspace</h2></div><strong className="workspace-name">{data.workspace_id}</strong><p>Exact workspace evidence is bound to this conversation.</p></section>;
  const submit = () => {
    if (!valid) return;
    const selection: WorkspaceSelection = {
      workspaceId: workspace.workspaceId.trim(),
      workspaceRevisionId: workspace.workspaceRevisionId.trim(),
      workspaceResolutionId: workspace.workspaceResolutionId.trim(),
      workspaceResolutionRevision: Number(workspace.workspaceResolutionRevision),
      membershipRevision: Number(workspace.membershipRevision),
    };
    void onCommand("select_workspace", (clientCommandId) => selectAssistantWorkspace(fetch, {
      ...selection,
      conversationId: data.conversation_id,
      expectedRevision: data.revision,
      idempotencyKey: clientCommandId,
    }));
  };
  return <section className="assistant-panel"><div className="panel-title"><SlidersHorizontal size={17} /><h2>Workspace</h2></div><p>Select the exact server-authorized workspace before asking for scoped work.</p>{open ? <form className="workspace-form" onSubmit={(event) => { event.preventDefault(); submit(); }}><label>Workspace<input value={workspace.workspaceId} onChange={(event) => setWorkspace({ ...workspace, workspaceId: event.target.value })} /></label><label>Workspace revision<input value={workspace.workspaceRevisionId} onChange={(event) => setWorkspace({ ...workspace, workspaceRevisionId: event.target.value })} /></label><label>Resolution<input value={workspace.workspaceResolutionId} onChange={(event) => setWorkspace({ ...workspace, workspaceResolutionId: event.target.value })} /></label><div className="workspace-numbers"><label>Resolution revision<input type="number" min="1" value={workspace.workspaceResolutionRevision} onChange={(event) => setWorkspace({ ...workspace, workspaceResolutionRevision: event.target.value })} /></label><label>Membership revision<input type="number" min="1" value={workspace.membershipRevision} onChange={(event) => setWorkspace({ ...workspace, membershipRevision: event.target.value })} /></label></div><button className="secondary-button" disabled={!canSend || blocked || !valid}>Use exact workspace</button></form> : <button className="secondary-button" disabled={!canSend || blocked} onClick={() => setOpen(true)}>Select workspace</button>}</section>;
}

function ClarificationPanel({ data, checkpoint, isBlocked, onCommand }: { data: AssistantConversationData; checkpoint?: NonNullable<AssistantConversationData["checkpoint"]>; isBlocked: (action: string) => boolean; onCommand: MutationCommand }) {
  const blocker = data.readiness?.blocking_unknowns[0];
  const question = [...data.turns].reverse().find((turn) => turn.role === "assistant")?.content;
  if (!blocker && !checkpoint) return null;
  return <section className="assistant-panel clarification-panel"><div className="panel-title"><CircleDashed size={17} /><h2>Clarification</h2></div>{blocker ? <p className="clarification-question">{question ?? blocker}</p> : null}{checkpoint ? <div className="checkpoint-actions">{checkpoint.allowed_actions.map((action) => <button key={action} className={action === "cancel" ? "danger-button" : "secondary-button"} disabled={isBlocked(`checkpoint_${action}`)} onClick={() => void onCommand(`checkpoint_${action}`, (clientCommandId) => actOnAssistantCheckpoint(fetch, { conversationId: data.conversation_id, expectedRevision: data.revision, checkpointId: checkpoint.checkpoint_id, checkpointRevision: checkpoint.revision, action, idempotencyKey: clientCommandId }))}>{checkpointLabel(action)}</button>)}</div> : null}</section>;
}

function DraftPanel({ data, confirmation, canConfirm, blocked, onCommand }: { data: AssistantConversationData; confirmation?: ConfirmationTarget; canConfirm: boolean; blocked: boolean; onCommand: MutationCommand }) {
  const draft = data.task_draft;
  if (!draft) return null;
  const spec = draft.task_spec;
  const assumptions = unique([...draft.assumptions, ...(data.readiness?.assumptions ?? []), ...(spec.assumptions ?? [])]);
  return <section className="assistant-panel task-draft-panel"><div className="panel-title"><FileCheck2 size={17} /><h2>Task draft</h2></div><strong className="draft-title">{spec.title}</strong><p className="draft-goal">{spec.goal}</p><DraftList label="Acceptance" values={spec.acceptance_criteria} /><DraftList label="Constraints" values={spec.constraints} />{assumptions.length ? <DraftList label="Assumptions" values={assumptions} /> : null}<dl className="draft-summary"><div><dt>Target</dt><dd>{targetLabel(spec.target)}</dd></div><div><dt>Execution</dt><dd>{spec.agent_config.profile_id} via {spec.agent_config.adapter}</dd></div><div><dt>Network</dt><dd>{spec.runtime_config.network_policy}</dd></div></dl>{canConfirm && confirmation ? <button className="primary-button" disabled={blocked} onClick={() => void onCommand("confirm_task", (clientCommandId) => confirmAssistantTask(fetch, { ...confirmation, idempotencyKey: clientCommandId }))}><Check size={16} /> Confirm exact draft</button> : <p className="draft-blocked">Confirmation is unavailable until the current exact draft is ready.</p>}</section>;
}

function HandoffPanel({ handoffs, refs, onNavigate }: { handoffs: AssistantConversationData["handoffs"]; refs: RelatedRef[]; onNavigate: (href: string) => void }) {
  if (!handoffs.length) return null;
  return <section className="assistant-panel handoff-panel"><div className="panel-title"><Bot size={17} /><h2>Delivery handoff</h2></div>{handoffs.map((handoff) => { const task = refs.find((ref) => ref.type === "task_summary" && ref.id === handoff.task_id); const worker = handoff.worker_session_id && refs.find((ref) => ref.type === "worker_detail" && ref.id === handoff.worker_session_id); return <div className="handoff-row" key={handoff.task_id}><div><strong>{handoff.status}</strong><span>{handoff.task_id}</span></div><span className="handoff-actions">{task ? <button className="icon-button" aria-label={`Open task ${task.id}`} title="Open Task" onClick={() => onNavigate("/tasks")}><ListTodo size={16} /></button> : null}{worker ? <button className="icon-button" aria-label={`Open worker ${worker.id}`} title="Open Worker" onClick={() => onNavigate(`/workers/${worker.id}`)}><ChevronRight size={16} /></button> : <span>Allocating worker</span>}</span></div>; })}</section>;
}

function DraftList({ label, values }: { label: string; values?: string[] }) { if (!values?.length) return null; return <div className="draft-list"><strong>{label}</strong><ul>{values.map((value) => <li key={value}>{value}</li>)}</ul></div>; }
function permits(actions: ActionRef[], action: string, id: string, revision: number) { return actions.some((item) => item.action === action && item.target_id === id && item.expected_revision === revision); }
function confirmationTarget(data: AssistantConversationData): ConfirmationTarget | undefined { const draft = data.task_draft; const readiness = data.readiness; if (!draft || !readiness || !data.workspace_id) return undefined; const reference = draft.task_spec.workspace_resolution_ref; const intent = draft.task_spec.creation_intent_ref; return { conversationId: data.conversation_id, conversationRevision: data.revision, taskDraftId: draft.task_draft_id, taskDraftRevision: draft.revision, readinessAssessmentId: readiness.assessment_id, readinessAssessmentRevision: readiness.revision, workspaceId: data.workspace_id, workspaceRevisionId: reference.workspace_revision_id, workspaceResolutionId: reference.resolution_id, workspaceResolutionRevision: reference.revision, membershipRevision: reference.membership_revision, taskSpecDigest: intent.task_spec_digest, triggerPolicyRevision: intent.trigger_policy_revision }; }
function targetLabel(target: { kind: string; ref: string }) { return target.ref || target.kind; }
function checkpointLabel(action: "continue" | "narrow_scope" | "cancel") { return action === "narrow_scope" ? "Narrow scope" : action[0].toUpperCase() + action.slice(1); }
function diagnostic(code?: string) { if (code === "model_timeout") return "The response timed out. You can retry safely."; if (code === "invalid_model_response") return "The response could not be validated. No work was created."; if (code === "policy_conflict") return "The proposal conflicts with the active policy."; return "The Assistant could not finish this response."; }
function formatTime(value: string) { const time = new Date(value); return Number.isNaN(time.getTime()) ? "" : time.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
function positiveInteger(value: string) { const parsed = Number(value); return Number.isInteger(parsed) && parsed > 0; }
function mutationsConflict(candidate: string, active: string) {
  if (candidate === active) return true;
  if (candidate === "select_workspace" || active === "select_workspace") return true;
  return candidate.startsWith("checkpoint_") && active.startsWith("checkpoint_");
}
function unique(values: string[]) { return [...new Set(values)]; }
