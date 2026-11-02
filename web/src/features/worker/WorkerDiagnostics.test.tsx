import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkerDiagnostics } from "./WorkerDiagnostics";

afterEach(() => vi.unstubAllGlobals());

const configured = {
  links: [{ kind: "grafana", url: "http://localhost:3001/d/sb-worker-runtime/worker-runtime" }, { kind: "logs", url: "http://localhost:3001/explore?panes=scope" }],
  grafana_status: "configured", langfuse_status: "identity_unavailable",
};

describe("WorkerDiagnostics", () => {
  it("loads exact targets and opens configured operator links safely", async () => {
    const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => configured });
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
      .mockResolvedValueOnce({ ok: true, json: async () => configured })
      .mockImplementationOnce(() => new Promise((resolve) => { oldReply = resolve; }))
      .mockResolvedValueOnce({ ok: true, json: async () => ({ ...configured, links: [], grafana_status: "disabled" }) });
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
