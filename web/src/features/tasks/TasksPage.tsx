import { ChevronRight, Clock3, GitBranch, HardDrive, MessageSquare, Plus, X } from "lucide-react";
import { useMemo, useState } from "react";
import type { TaskIndexData, TaskListItem } from "../../api/contracts";
import { createTask, newTaskIdempotencyKey } from "../../api/taskCommands";
import { StatusBadge } from "../../components/ui/StatusBadge";

type TaskFilter = "active" | "completed" | "all";

export function TasksPage({
  data,
  mutationsEnabled = true,
  isMutationAuthorized = () => mutationsEnabled,
  onNavigate,
}: {
  data: TaskIndexData;
  isMutationAuthorized?: () => boolean;
  mutationsEnabled?: boolean;
  onNavigate: (href: string) => void;
}) {
  const [filter, setFilter] = useState<TaskFilter>("active");
  const [creating, setCreating] = useState(false);
  const tasks = useMemo(() => data.tasks.filter((task) => matchesFilter(task, filter)), [data.tasks, filter]);
  const percent = data.capacity.limit > 0
    ? Math.round(data.capacity.used / data.capacity.limit * 100)
    : 0;
  const fillPercent = Math.min(100, Math.max(0, percent));
  const overBy = Math.max(0, data.capacity.used - data.capacity.limit);
  return (
    <main className="task-layout">
      <section className="task-table-section">
        <div className="section-toolbar">
          <div className="segmented" aria-label="Task filter">
            {(["active", "completed", "all"] as const).map((item) => <button key={item} className={filter === item ? "selected" : ""} onClick={() => setFilter(item)}>{titleCase(item)}</button>)}
          </div>
          <div className="toolbar-actions"><button className="icon-button" title="Start Assistant conversation" aria-label="Start Assistant conversation" onClick={() => onNavigate("/assistant")}><MessageSquare size={18} /></button><button className="icon-button" title="Create task" aria-label="Create task" disabled={!mutationsEnabled} onClick={() => setCreating(true)}><Plus size={18} /></button></div>
        </div>
        {creating ? <CreateTaskForm enabled={mutationsEnabled} isMutationAuthorized={isMutationAuthorized} onClose={() => setCreating(false)} /> : null}
        <div className="task-table-scroll">
          <table className="task-table" aria-label="Tasks">
            <thead>
              <tr className="task-row task-header">
                <th scope="col">Task</th>
                <th scope="col">Status</th>
                <th scope="col">Workspace</th>
                <th scope="col">Updated</th>
                <th scope="col">Worker</th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((task) => (
                <tr className="task-row task-data-row" key={task.task_id}>
                  <td><span className="task-title"><strong>{task.title}</strong><small>{task.task_id}</small></span></td>
                  <td><StatusBadge label={task.status} tone={task.status_tone} /></td>
                  <td><span className="workspace-cell"><GitBranch aria-hidden="true" size={14} /> {task.workspace}</span></td>
                  <td><span className="muted"><Clock3 aria-hidden="true" size={14} /> {task.updated_at}</span></td>
                  <td className="task-worker-cell">
                    {task.worker_session_id ? (
                      <button
                        aria-label={`Open Worker ${task.worker_session_id} for ${task.title}`}
                        className="task-worker-action"
                        onClick={() => onNavigate(`/workers/${task.worker_session_id}`)}
                      >
                        <ChevronRight aria-hidden="true" size={16} />
                      </button>
                    ) : <span className="visually-hidden">No Worker assigned</span>}
                  </td>
                </tr>
              ))}
              {tasks.length === 0 ? <tr><td className="task-empty" colSpan={5}>No tasks match this view.</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>
      <aside className="capacity-panel">
        <div className="panel-title"><HardDrive size={17} /><h2>Worker capacity</h2></div>
        <div className="capacity-value"><strong>{data.capacity.used}</strong><span>of {data.capacity.limit} sessions</span></div>
        <div
          aria-label={`${percent}% capacity used`}
          aria-valuemax={Math.max(data.capacity.limit, data.capacity.used)}
          aria-valuemin={0}
          aria-valuenow={data.capacity.used}
          className="capacity-track"
          role="progressbar"
        >
          <span style={{ width: `${fillPercent}%` }} />
        </div>
        <p className={overBy > 0 ? "capacity-over" : undefined}>
          {percent}%{overBy > 0 ? ` · ${overBy} over capacity` : ""}
        </p>
      </aside>
    </main>
  );
}

function CreateTaskForm({
  enabled,
  isMutationAuthorized,
  onClose,
}: {
  enabled: boolean;
  isMutationAuthorized: () => boolean;
  onClose: () => void;
}) {
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [profile, setProfile] = useState("deepseek-codex-openai-v1");
  const [feedback, setFeedback] = useState<string>();
  const [submitting, setSubmitting] = useState(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!enabled || !isMutationAuthorized()) {
      setFeedback("Task creation is suspended until the projection is live.");
      return;
    }
    setSubmitting(true);
    setFeedback(undefined);
    try {
      await createTask(fetch, { idempotencyKey: newTaskIdempotencyKey(), title: title.trim(), goal: goal.trim(), agentProfileId: profile, acceptanceMode: "manual" });
      setFeedback("Task accepted. The live index will update when projection catches up.");
      setTitle("");
      setGoal("");
    } catch (cause) {
      setFeedback(cause instanceof Error ? cause.message : "Task creation failed.");
    } finally {
      setSubmitting(false);
    }
  };
  return <form className="create-task-form" onSubmit={submit}>
    <header><strong>Create task</strong><button type="button" className="icon-button" aria-label="Close task form" onClick={onClose}><X size={16} /></button></header>
    <label>Title<input disabled={!enabled} required value={title} onChange={(event) => setTitle(event.target.value)} /></label>
    <label>Goal<textarea disabled={!enabled} required value={goal} onChange={(event) => setGoal(event.target.value)} /></label>
    <label>Agent<select disabled={!enabled} value={profile} onChange={(event) => setProfile(event.target.value)}><option value="deepseek-codex-openai-v1">DeepSeek</option><option value="kimi-codex-anthropic-v1">Kimi</option></select></label>
    <footer>{feedback ? <span role="status">{feedback}</span> : <span>{enabled ? "Manual review · report and patch" : "Task creation is suspended until the projection is live."}</span>}<button className="primary-button" disabled={!enabled || submitting}>{submitting ? "Creating" : "Create"}</button></footer>
  </form>;
}

function matchesFilter(task: TaskListItem, filter: TaskFilter): boolean {
  if (filter === "all") return true;
  const terminal = ["completed", "failed", "cancelled"].includes(task.status.toLowerCase());
  return filter === "completed" ? terminal : !terminal;
}

function titleCase(value: string): string {
  return value[0].toUpperCase() + value.slice(1);
}
