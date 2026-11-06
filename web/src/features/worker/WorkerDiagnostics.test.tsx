import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkerDiagnostics } from "./WorkerDiagnostics";

afterEach(() => vi.unstubAllGlobals());

const configured = {
  links: [{ kind: "grafana", url: "http://localhost:3001/d/sb-worker-runtime/worker-runtime" }, { kind: "logs", url: "http://localhost:3001/explore?panes=scope" }],
  grafana_status: "configured", langfuse_status: "identity_unavailable",
  target: { worker_session_id: "session-1" },
};

function responseFor(url: string, body: typeof configured) {
  const parsed = new URL(url, "http://localhost");
  return { ok: true, json: async () => ({ ...body, target: { worker_session_id: decodeURIComponent(parsed.pathname.split("/").at(-2)!), worker_run_id: parsed.searchParams.get("worker_run_id") ?? undefined, agent_turn_id: parsed.searchParams.get("agent_turn_id") ?? undefined } }) };
}

describe("WorkerDiagnostics", () => {
  it("shows historical proof boundaries and exact safe identity summary", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({
      links: [{ kind: "langfuse", url: "http://localhost:3000/project/switchback-m1/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }],
      grafana_status: "disabled", langfuse_status: "historically_verified",
      target: { worker_session_id: "session-1", worker_run_id: "run-1", agent_turn_id: "turn-1" },
      identity: { version: "langfuse.projection.v3", trace_id: "a".repeat(32), selected_observation_id: "b".repeat(32), sources: [
        { kind: "run_basis", source_id: "run-source", frame_digest: `sha256:${"c".repeat(64)}` },
        { kind: "turn_basis", source_id: "turn-source", frame_digest: `sha256:${"d".repeat(64)}` },
      ] },
    }) }));
    render(<WorkerDiagnostics sessionId="session-1" runId="run-1" turnId="turn-1" />);
    await screen.findByRole("link", { name: "Langfuse" });
    expect(screen.getByText("身份曾核验；链接使用当前项目配置，未实时核对")).toBeInTheDocument();
    expect(screen.getByText(/Trace aaaaaaaaaa/)).toBeInTheDocument();
    expect(screen.getByText(/Observation bbbbbbbbbb/)).toBeInTheDocument();
  });

  it("ignores first A response after A to B to A", async () => {
    let firstA: (response: unknown) => void = () => {};
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { firstA = resolve; }))
      .mockImplementation((url: string) => responseFor(url, { ...configured, links: [], grafana_status: "disabled" }));
    vi.stubGlobal("fetch", fetcher);
    const a = { sessionId: "session-1", runId: "run-1", turnId: "turn-1" };
    const view = render(<WorkerDiagnostics {...a} />);
    view.rerender(<WorkerDiagnostics {...a} turnId="turn-2" />);
    await screen.findByText("Diagnostic links disabled.");
    view.rerender(<WorkerDiagnostics {...a} />);
    await screen.findByText("Diagnostic links disabled.");
    await act(async () => { firstA(responseFor("/api/v1/views/worker_detail/session-1/diagnostics?worker_run_id=run-1&agent_turn_id=turn-1", configured)); });
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  });
  it("loads exact targets and opens configured operator links safely", async () => {
    const fetcher = vi.fn().mockImplementation((url: string) => responseFor(url, configured));
    vi.stubGlobal("fetch", fetcher);
    render(<WorkerDiagnostics sessionId="session /1" runId="run&1" turnId="turn?1" />);
    const link = await screen.findByRole("link", { name: "Grafana · Worker overview" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/views/worker_detail/session%20%2F1/diagnostics?worker_run_id=run%261&agent_turn_id=turn%3F1");
    expect(screen.getByText(/Aggregate metrics/)).toBeInTheDocument();
    expect(screen.getByText(/Langfuse link unavailable/)).toBeInTheDocument();
  });

  it.each(["disabled", "invalid_config"])("shows %s availability without links", async (status) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ ...configured, links: [], grafana_status: status }) }));
    render(<WorkerDiagnostics sessionId="session-1" />);
    await screen.findByText(status === "disabled" ? "Diagnostic links disabled." : /invalid local browser configuration/);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it.each(["rejected", "http", "unsafe", "credentials", "nonlocal", "malformed"])("handles %s response independently of Worker controls", async (failure) => {
    const fetcher = vi.fn();
    if (failure === "rejected") fetcher.mockRejectedValue(new Error("private server detail"));
    else fetcher.mockResolvedValue({ ok: failure !== "http", json: async () => failure === "malformed" ? null : ({ ...configured, links: [{ kind: "logs", url: failure === "unsafe" ? "javascript:alert(1)" : failure === "credentials" ? "http://secret@localhost:3001/explore" : "https://evil.test/explore" }] }) });
    vi.stubGlobal("fetch", fetcher);
    render(<WorkerDiagnostics sessionId="session-1" />);
    await screen.findByText("Diagnostics unavailable.");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByText(/private server detail/)).not.toBeInTheDocument();
  });

  it.each(["session", "run", "turn"])("removes links immediately on %s change and ignores late old replies", async (field) => {
    let oldReply: (value: unknown) => void = () => {};
    const fetcher = vi.fn()
      .mockImplementationOnce((url: string) => responseFor(url, configured))
      .mockImplementationOnce(() => new Promise((resolve) => { oldReply = resolve; }))
      .mockImplementationOnce((url: string) => responseFor(url, { ...configured, links: [], grafana_status: "disabled" }));
    vi.stubGlobal("fetch", fetcher);
    const target = { sessionId: "session-1", runId: "run-1", turnId: "turn-1" };
    const { rerender } = render(<WorkerDiagnostics {...target} />);
    await screen.findByRole("link", { name: /Grafana/ });
    const next = { ...target, [`${field}Id`]: `${field}-2` };
    rerender(<WorkerDiagnostics {...next} />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    rerender(<WorkerDiagnostics {...next} turnId="turn-3" />);
    await screen.findByText("Diagnostic links disabled.");
    await act(async () => { oldReply({ ok: true, json: async () => configured }); });
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(fetcher.mock.calls[1][1].signal.aborted).toBe(true);
  });
});
