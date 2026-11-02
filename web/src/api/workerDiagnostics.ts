import type { WorkerDiagnosticLinks } from "./contracts";

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
  const result: WorkerDiagnosticLinks = await response.json();
  if (!Array.isArray(result.links) || result.links.length > 3 ||
    !["configured", "disabled", "invalid_config"].includes(result.grafana_status) ||
    result.langfuse_status !== "identity_unavailable" ||
    result.links.some((link) => !["grafana", "logs", "langfuse"].includes(link.kind) || !safeLocalLink(link.url))) {
    throw new Error("Invalid diagnostic links");
  }
  return result;
}

function safeLocalLink(value: string): boolean {
  try {
    const url = new URL(value);
    return ["http:", "https:"].includes(url.protocol) && !url.username && !url.password &&
      (url.hostname === "localhost" || url.hostname === "[::1]" || /^127\.\d+\.\d+\.\d+$/.test(url.hostname));
  } catch { return false; }
}
