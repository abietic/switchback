import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HomeDashboardData } from "../../api/homeContracts";
import { clientViewReplicas } from "../../state/clientViewReplica";
import { ObservabilityPage } from "./ObservabilityPage";

class ObservationEventSource {
  static current: ObservationEventSource;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (event: MessageEvent<string>) => void>();
  constructor() { ObservationEventSource.current = this; }
  addEventListener(name: string, listener: (event: MessageEvent<string>) => void) {
    this.listeners.set(name, listener);
  }
  close() {}
  emit(name: string, data: unknown) {
    this.listeners.get(name)?.({ data: JSON.stringify(data), lastEventId: "" } as MessageEvent<string>);
  }
}

function snapshot(workers: HomeDashboardData["recent_workers"] = []) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "home_dashboard", view_id: "index", tenant_id: "tenant-local",
    view_epoch: 1, view_cursor: 1, projection_version: 1,
    query_fingerprint: "home-local", generated_at: "2026-10-11T00:00:00Z",
    available_actions: [], related_refs: workers.map((worker) => ({
      type: "worker_detail", id: worker.worker_session_id,
      href: `/api/v1/views/worker_detail/${worker.worker_session_id}`,
    })),
    data: {
      attention_tasks: [], recent_workers: workers, recent_deliveries: [],
      capacity: { used: workers.length, limit: 10 }, updated_at: "2026-10-11T00:00:00Z",
    } satisfies HomeDashboardData,
  };
}

const authorizedWorker: HomeDashboardData["recent_workers"][number] = {
  worker_session_id: "session-authorized", workspace: "workspace-local", status: "idle",
  status_tone: "waiting", runtime_generation: 4, task_title: "正式报告任务",
  bound_task_id: "task-report", updated_at: "2026-10-11T00:00:00Z",
};

function respond(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
}

function renderPage(fetcher: typeof fetch) {
  vi.stubGlobal("fetch", fetcher);
  vi.stubGlobal("EventSource", ObservationEventSource);
  return render(<MemoryRouter><ObservabilityPage /></MemoryRouter>);
}

afterEach(() => {
  clientViewReplicas.delete("home_dashboard", "index");
  vi.unstubAllGlobals();
});

describe("ObservabilityPage production Frontier seam", () => {
  it("offers only exact authorized Workers and reads only Home without issuing a command", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(respond(snapshot([authorizedWorker])));
    renderPage(fetcher);
    expect(await screen.findByRole("link", { name: /正式报告任务/ })).toHaveAttribute("href", "/workers/session-authorized");
    expect(screen.getByText(/session-authorized · workspace-local/)).toBeInTheDocument();
    expect(screen.getByText(/聚合指标/)).toBeInTheDocument();
    expect(screen.getByText(/Langfuse/)).toHaveTextContent("尚无已核验身份时不提供链接");
    expect(screen.queryByRole("link", { name: /Grafana|Logs|Langfuse/ })).toBeNull();
    expect(fetcher.mock.calls.every(([url, init]) => String(url) === "/api/v1/views/home_dashboard/index" && !init?.method)).toBe(true);
    expect(screen.queryByText(/session-unreadable/)).not.toBeInTheDocument();
  });

  it("shows an honest empty state and explicit creation entry points", async () => {
    renderPage(vi.fn<typeof fetch>().mockResolvedValue(respond(snapshot())));
    expect(await screen.findByText("暂无可查看的 Worker。" )).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "进入 Assistant" })).toHaveAttribute("href", "/assistant");
    expect(screen.getByRole("link", { name: "进入任务交付" })).toHaveAttribute("href", "/tasks");
    expect(screen.queryByRole("link", { name: /Worker 诊断/ })).toBeNull();
  });

  it("renders loading then retries a failed Home read", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response("unavailable", { status: 503 }))
      .mockResolvedValueOnce(respond(snapshot([authorizedWorker])));
    renderPage(fetcher);
    expect(screen.getByRole("status", { name: "Loading 可观测性" })).toHaveAttribute("aria-busy", "true");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry 可观测性" }));
    expect(await screen.findByRole("link", { name: /正式报告任务/ })).toHaveAttribute("href", "/workers/session-authorized");
  });

  it("keeps trusted Worker navigation while resync is pending and clears it on access removal", async () => {
    let complete!: (response: Response) => void;
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(respond(snapshot([authorizedWorker])))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { complete = resolve; }));
    renderPage(fetcher);
    await screen.findByRole("link", { name: /正式报告任务/ });
    act(() => ObservationEventSource.current.emit("resync_required", {
      schema_version: "frontier.projection_control.v1", view_type: "home_dashboard", view_id: "index",
      view_epoch: 1, snapshot_cursor: 1, reason: "cursor_gap", event_type: "resync_required",
    }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("status")).toHaveTextContent("Refreshing authoritative 可观测性 state");
    expect(screen.getByRole("link", { name: /正式报告任务/ })).toHaveAttribute("href", "/workers/session-authorized");
    await act(async () => complete(new Response("forbidden", { status: 403 })));
    await waitFor(() => expect(screen.queryByRole("link", { name: /正式报告任务/ })).toBeNull());
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });
});
