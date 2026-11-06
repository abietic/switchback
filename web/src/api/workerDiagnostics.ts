import type { WorkerDiagnosticLinks } from "./contracts";
import { isCanonicalIdentity } from "./contractPrimitives";

export type WorkerDiagnosticSelection = { kind: "none" } | { kind: "invalid" } |
  { kind: "artifact"; artifactId: string } | { kind: "exact"; runId: string; turnId?: string };

export function parseWorkerDiagnosticSelection(search: string): WorkerDiagnosticSelection {
  try { decodeURIComponent(search.replace(/\+/g, " ")); } catch { return { kind: "invalid" }; }
  const params = new URLSearchParams(search);
  for (const key of ["artifact_id", "worker_run_id", "agent_turn_id"]) {
    const values = params.getAll(key);
    if (values.length > 1 || values.some((value) => !isCanonicalIdentity(value) || value.length > 512)) return { kind: "invalid" };
  }
  const artifactId = params.get("artifact_id"), runId = params.get("worker_run_id"), turnId = params.get("agent_turn_id");
  if ((artifactId && (runId || turnId)) || (turnId && !runId)) return { kind: "invalid" };
  return artifactId ? { kind: "artifact", artifactId } : runId ? { kind: "exact", runId, ...(turnId ? { turnId } : {}) } : { kind: "none" };
}

export interface WorkerDiagnosticTarget {
  sessionId: string;
  runId?: string;
  turnId?: string;
}

export async function fetchWorkerDiagnostics(target: WorkerDiagnosticTarget, signal: AbortSignal): Promise<WorkerDiagnosticLinks> {
  const query = new URLSearchParams();
  if (target.runId) query.set("worker_run_id", target.runId);
  if (target.turnId) query.set("agent_turn_id", target.turnId);
  const suffix = query.size ? `?${query}` : "";
  const response = await fetch(`/api/v1/views/worker_detail/${encodeURIComponent(target.sessionId)}/diagnostics${suffix}`, {
    signal, cache: "no-store", credentials: "same-origin",
  });
  if (!response.ok) throw new Error("Diagnostics unavailable");
  const result: unknown = await response.json();
  if (!validDiagnostics(result, target)) {
    throw new Error("Invalid diagnostic links");
  }
  return result;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validDiagnostics(value: unknown, target: WorkerDiagnosticTarget): value is WorkerDiagnosticLinks {
  if (!record(value) || !record(value.target) || !Array.isArray(value.links) || value.links.length > 3 ||
    typeof value.grafana_status !== "string" || !["configured", "disabled", "invalid_config"].includes(value.grafana_status) ||
    typeof value.langfuse_status !== "string" || !["disabled", "invalid_config", "identity_unavailable", "pending", "blocked", "unavailable", "historically_verified"].includes(value.langfuse_status) ||
    value.target.worker_session_id !== target.sessionId || value.target.worker_run_id !== target.runId || value.target.agent_turn_id !== target.turnId) return false;
  const kinds = new Set<string>();
  let langfuse: URL | undefined;
  for (const link of value.links) {
    if (!record(link) || typeof link.kind !== "string" || kinds.has(link.kind) || typeof link.url !== "string") return false;
    const url = safeLocalLink(link.url);
    if (!url || url.hash) return false;
    if (link.kind === "grafana" ? url.pathname !== "/d/sb-worker-runtime/worker-runtime"
      : link.kind === "logs" ? url.pathname !== "/explore"
        : link.kind === "langfuse" ? false : true) return false;
    kinds.add(link.kind);
    if (link.kind === "langfuse") langfuse = url;
  }
  if ((value.grafana_status === "configured") !== (kinds.has("grafana") && kinds.has("logs")) ||
    (value.grafana_status !== "configured" && (kinds.has("grafana") || kinds.has("logs")))) return false;
  if (value.langfuse_status !== "historically_verified") return !langfuse && !("identity" in value);
  const identity = value.identity;
  if (!target.runId || !langfuse || !record(identity) || identity.version !== "langfuse.projection.v3" ||
    typeof identity.trace_id !== "string" || !/^[0-9a-f]{32}$/.test(identity.trace_id) || langfuse.search ||
    !new RegExp(`^/project/[A-Za-z0-9_-]+/traces/${identity.trace_id}$`).test(langfuse.pathname) ||
    !Array.isArray(identity.sources) || identity.sources.length !== (target.turnId ? 2 : 1)) return false;
  if (target.turnId ? typeof identity.selected_observation_id !== "string" || !/^[0-9a-f]{32}$/.test(identity.selected_observation_id) || identity.selected_observation_id === identity.trace_id
    : "selected_observation_id" in identity) return false;
  const sourceKinds = new Set<string>();
  for (const source of identity.sources) {
    if (!record(source) || (source.kind !== "run_basis" && source.kind !== "turn_basis") || sourceKinds.has(source.kind) ||
      typeof source.source_id !== "string" || !source.source_id || source.source_id.length > 512 || /[\s\x00]/.test(source.source_id) ||
      typeof source.frame_digest !== "string" || !/^sha256:[0-9a-f]{64}$/.test(source.frame_digest)) return false;
    sourceKinds.add(source.kind);
  }
  return sourceKinds.has("run_basis") && (Boolean(target.turnId) === sourceKinds.has("turn_basis"));
}

function safeLocalLink(value: string): URL | undefined {
  try {
    const url = new URL(value);
    return value === value.trim() && ["http:", "https:"].includes(url.protocol) && !url.username && !url.password &&
      (url.hostname === "localhost" || url.hostname === "[::1]" || /^127\.\d+\.\d+\.\d+$/.test(url.hostname)) ? url : undefined;
  } catch { return undefined; }
}
