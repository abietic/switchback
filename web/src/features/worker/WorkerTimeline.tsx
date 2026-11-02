import {
  AlertCircle,
  Bot,
  CheckCircle2,
  ChevronDown,
  Clock3,
  FileText,
  GitBranch,
  LoaderCircle,
  Radio,
  Terminal,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type {
  DisplayPart,
  TransientDisplayDelta,
  WorkerTimelineTurn,
} from "../../api/contracts";
import { SafeMarkdown } from "../../components/SafeMarkdown";
import { parseWorkerTimelinePage } from "../../projections/decoders/worker";

export function WorkerTimeline({
  workerSessionId,
  initialGroups,
  initialCursor,
  initialHasMore,
  transient,
  onArtifact,
  onDiagnosticTarget,
}: {
  workerSessionId: string;
  initialGroups: WorkerTimelineTurn[];
  initialCursor?: string;
  initialHasMore: boolean;
  transient: TransientDisplayDelta[];
  onArtifact: (artifactId: string) => void;
  onDiagnosticTarget?: (runId: string, turnId: string) => void;
}) {
  const [groups, setGroups] = useState(initialGroups);
  const [cursor, setCursor] = useState(initialCursor);
  const [hasMore, setHasMore] = useState(initialHasMore);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string>();

  useEffect(() => {
    setGroups(initialGroups);
    setCursor(initialCursor);
    setHasMore(initialHasMore);
  }, [initialCursor, initialGroups, initialHasMore]);

  const ordered = useMemo(
    () => [...groups].sort((left, right) =>
      left.started_at.localeCompare(right.started_at) ||
      left.agent_turn_id.localeCompare(right.agent_turn_id)),
    [groups],
  );

  const loadOlder = async () => {
    if (!cursor || loading) return;
    setLoading(true);
    setLoadError(undefined);
    try {
      const response = await fetch(
        `/api/v1/views/worker_detail/${encodeURIComponent(workerSessionId)}/timeline?before=${encodeURIComponent(cursor)}&limit=20`,
        { headers: { Accept: "application/json" } },
      );
      if (!response.ok) throw new Error("Older Worker history is unavailable.");
      const page = parseWorkerTimelinePage(await response.json());
      setGroups((current) => {
        const byID = new Map(current.map((turn) => [turn.agent_turn_id, turn]));
        for (const turn of page.turns) byID.set(turn.agent_turn_id, turn);
        return [...byID.values()];
      });
      setCursor(page.next_cursor);
      setHasMore(page.has_more);
    } catch (cause) {
      setLoadError(cause instanceof Error ? cause.message : "Older Worker history is unavailable.");
    } finally {
      setLoading(false);
    }
  };

  return <div className="timeline" aria-label="Worker timeline">
    {hasMore ? <button className="load-history" onClick={() => void loadOlder()} disabled={loading}>
      {loading ? <LoaderCircle size={14} className="spin" /> : <Clock3 size={14} />}
      Load older activity
    </button> : null}
    {loadError ? <p className="timeline-warning" role="alert">{loadError}</p> : null}
    <div aria-label="Durable Worker history">
      {ordered.length > 0 ? ordered.map((turn) => (
        <TurnGroup key={turn.agent_turn_id} turn={turn} onArtifact={onArtifact} onDiagnosticTarget={onDiagnosticTarget} />
      )) : <p className="timeline-warning">No durable Worker activity yet.</p>}
    </div>
    <LiveActivity transient={transient} />
  </div>;
}

function TurnGroup({ turn, onArtifact, onDiagnosticTarget }: { turn: WorkerTimelineTurn; onArtifact: (artifactId: string) => void; onDiagnosticTarget?: (runId: string, turnId: string) => void }) {
  return <section className="turn-group" aria-label={`Agent turn ${turn.agent_turn_id}`}>
    <header className="turn-group-header">
      <div><span>Run {shortID(turn.worker_run_id)}</span><span>Turn {shortID(turn.agent_turn_id)}</span></div>
      <span className={`turn-state turn-${turn.status}`}>{humanStatus(turn.status)}</span>
      {onDiagnosticTarget ? <button type="button" aria-label={`Diagnostics for Turn ${turn.agent_turn_id}`} onClick={() => onDiagnosticTarget(turn.worker_run_id, turn.agent_turn_id)}>Diagnostics</button> : null}
    </header>
    <article className="timeline-entry">
      <span className="timeline-icon timeline-user"><GitBranch size={15} /></span>
      <div><header><strong>You</strong><time>{formatTime(turn.started_at)}</time></header><p>{turn.prompt}</p></div>
    </article>
    {turn.parts.map((part) => <DisplayPartView key={part.part_id} part={part} onArtifact={onArtifact} />)}
    {turn.backfill_status === "non_backfillable" ? <p className="timeline-warning">
      Earlier rich output was not captured for this turn. Trace and Artifact evidence remain available.
    </p> : null}
  </section>;
}

function DisplayPartView({ part, onArtifact }: { part: DisplayPart; onArtifact: (artifactId: string) => void }) {
  const payload = part.payload;
  if (part.part_type === "text" || part.part_type === "reasoning_summary") {
    return <article className="timeline-entry">
      <span className="timeline-icon timeline-agent"><Bot size={15} /></span>
      <div className="timeline-rich"><header><strong>Agent</strong><StatusText status={part.lifecycle_status} /></header>
        <SafeMarkdown>{stringValue(payload.content)}</SafeMarkdown>
      </div>
    </article>;
  }
  if (part.part_type === "tool") return <ToolPart part={part} />;
  if (part.part_type === "artifact") {
    const artifactID = stringValue(payload.artifact_id);
    return <article className="timeline-entry">
      <span className="timeline-icon timeline-artifact"><FileText size={15} /></span>
      <div><header><strong>Artifact ready</strong><StatusText status={part.lifecycle_status} /></header>
        <button className="timeline-artifact-link" onClick={() => onArtifact(artifactID)}>
          {stringValue(payload.label) || humanStatus(stringValue(payload.type))}
        </button>
      </div>
    </article>;
  }
  if (part.part_type === "error") {
    return <article className="timeline-entry timeline-critical">
      <span className="timeline-icon timeline-error"><AlertCircle size={15} /></span>
      <div><header><strong>Execution issue</strong><StatusText status={part.lifecycle_status} /></header>
        <p>{stringValue(payload.message)}</p>
        {stringValue(payload.recovery) ? <small>{stringValue(payload.recovery)}</small> : null}
      </div>
    </article>;
  }
  const approval = part.part_type === "approval";
  return <article className="timeline-entry">
    <span className={`timeline-icon timeline-${approval ? "approval" : "activity"}`}>
      {approval ? <Clock3 size={15} /> : <Radio size={15} />}
    </span>
    <div><header><strong>{stringValue(payload.title) || (approval ? "Approval" : "Activity")}</strong>
      <StatusText status={part.lifecycle_status} />
    </header>
      {stringValue(payload.detail) || stringValue(payload.target) ? <p>{stringValue(payload.detail) || stringValue(payload.target)}</p> : null}
    </div>
  </article>;
}

function ToolPart({ part }: { part: DisplayPart }) {
  const payload = part.payload;
  const args = objectValue(payload.arguments);
  const locations = arrayValue(payload.locations);
  return <article className="timeline-entry tool-entry">
    <span className="timeline-icon timeline-tool"><Terminal size={15} /></span>
    <details>
      <summary>
        <span><strong>{stringValue(payload.title) || "Tool call"}</strong><StatusText status={part.lifecycle_status} /></span>
        <span>{numberValue(payload.duration_ms) ? `${numberValue(payload.duration_ms)} ms` : ""}<ChevronDown size={14} /></span>
      </summary>
      <div className="tool-details">
        {Object.keys(args).length ? <dl>{Object.entries(args).map(([key, value]) =>
          <div key={key}><dt>{key}</dt><dd>{String(value)}</dd></div>)}</dl> : null}
        {stringValue(payload.output) ? <pre>{stringValue(payload.output)}</pre> : null}
        {locations.length ? <ul>{locations.map((location, index) =>
          <li key={`${stringValue(location.path)}-${index}`}>{stringValue(location.path)}{location.line ? `:${location.line}` : ""}</li>)}</ul> : null}
      </div>
    </details>
  </article>;
}

function LiveActivity({ transient }: { transient: TransientDisplayDelta[] }) {
  const text = transient.map((item) => typeof item.payload.text === "string" ? item.payload.text : "").join("");
  if (!text) return null;
  return <article className="timeline-entry live-activity" aria-label="Live Worker activity" aria-live="polite">
    <span className="timeline-icon timeline-agent"><Radio size={15} /></span>
    <div><header><strong>Agent</strong><span className="live-dot">Streaming</span></header>
      <SafeMarkdown>{text}</SafeMarkdown>
    </div>
  </article>;
}

function StatusText({ status }: { status: string }) {
  return <span className="part-status">{humanStatus(status)}</span>;
}

function shortID(value: string) {
  return value.length > 18 ? `${value.slice(0, 9)}…${value.slice(-6)}` : value;
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function humanStatus(value: string) {
  return value.split("_").map((part) => part ? part[0].toUpperCase() + part.slice(1) : part).join(" ");
}

function stringValue(value: unknown) {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function objectValue(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function arrayValue(value: unknown): Array<Record<string, unknown>> {
  return Array.isArray(value) ? value.filter((item) => typeof item === "object" && item !== null) as Array<Record<string, unknown>> : [];
}
