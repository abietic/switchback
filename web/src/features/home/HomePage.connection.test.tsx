import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProjectionSnapshot } from "../../api/contracts";
import type { HomeDashboardData } from "../../api/homeContracts";
import { productionViews } from "../../projections/productionViewRegistry";
import { clientViewReplicas } from "../../state/clientViewReplica";
import { HomePage } from "./HomePage";

class FakeHomeStream {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, (event: MessageEvent<string>) => void>();

  constructor(readonly url: string) {
    streams.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    this.listeners.set(type, listener);
  }

  close(): void {
    this.closed = true;
  }

  emit(type: string, value: unknown): void {
    this.listeners.get(type)?.(new MessageEvent(type, { data: JSON.stringify(value) }));
  }

  open(): void {
    this.onopen?.();
  }
}

const streams: FakeHomeStream[] = [];

function dashboard(acceptanceMode: "auto" | "manual" = "manual"): HomeDashboardData {
  return {
    attention_tasks: [],
    recent_workers: [],
    recent_deliveries: [{
      task_id: "task-delivered",
      title: "Authoritative delivery",
      workspace: "workspace-local",
      status: "delivered",
      status_tone: "waiting",
      acceptance_mode: acceptanceMode,
      worker_session_id: "session-1",
      updated_at: "2026-07-29T00:00:00Z",
      artifacts: [],
    }],
    capacity: { used: 1, limit: 10 },
    updated_at: "2026-07-29T00:00:00Z",
  };
}

function snapshot(
  data: HomeDashboardData,
  cursor = 1,
): ProjectionSnapshot<HomeDashboardData> {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "home_dashboard",
    view_id: "index",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: cursor,
    projection_version: cursor,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-29T00:00:00Z",
    data,
    available_actions: [],
    related_refs: [{
      type: "task_summary",
      id: "task-delivered",
      href: "/api/v1/views/task_summary/task-delivered",
    }],
  };
}

function renderHome() {
  return render(<MemoryRouter><HomePage /></MemoryRouter>);
}

afterEach(() => {
  clientViewReplicas.delete("home_dashboard", "index");
  streams.splice(0);
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("HomePage Frontier lifecycle", () => {
  it("keeps cached data visible when snapshot parsing fails and retries through the controller", async () => {
    seedHome(snapshot(dashboard()));
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ malformed: true }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(snapshot(dashboard("auto"), 2)), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeHomeStream);

    renderHome();

    const stale = await screen.findByRole("alert");
    expect(stale).toHaveTextContent("Live Home dashboard updates are unavailable");
    expect(stale).toHaveTextContent("Frontier view data could not be validated.");
    expect(screen.getByText("Authoritative delivery")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Retry Home dashboard" }));
    await waitFor(() => expect(streams).toHaveLength(1));
    act(() => streams[0].open());
    await waitFor(() => expect(screen.queryByText("Live Home dashboard updates are unavailable")).not.toBeInTheDocument());
    expect(screen.getByText(/auto acceptance/)).toBeVisible();
  });

  it("shows real reconnect, refresh failure, recovery, and terminal upgrade states", async () => {
    seedHome(snapshot(dashboard()));
    const fetcher = vi.fn<typeof fetch>()
      .mockImplementation(async () => new Response(
        JSON.stringify(snapshot(dashboard(), 2)),
        { status: 200 },
      ));
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeHomeStream);

    renderHome();
    await waitFor(() => expect(streams).toHaveLength(1));
    expect(screen.getByRole("status")).toHaveTextContent("Reconnecting to live Home dashboard updates");
    act(() => streams[0].open());
    expect(screen.queryByText("Reconnecting to live Home dashboard updates")).not.toBeInTheDocument();

    act(() => streams[0].onerror?.());
    expect(screen.getByRole("status")).toHaveTextContent("Reconnecting to live Home dashboard updates");
    await waitFor(() => expect(streams).toHaveLength(2), { timeout: 4000 });
    act(() => streams[1].open());
    expect(screen.queryByText("Reconnecting to live Home dashboard updates")).not.toBeInTheDocument();

    fetcher
      .mockRejectedValueOnce(new Error("refresh failed"))
      .mockResolvedValueOnce(new Response(
        JSON.stringify(snapshot(dashboard(), 3)),
        { status: 200 },
      ));
    act(() => streams[1].emit("resync_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "resync_required",
      view_type: "home_dashboard",
      view_id: "index",
      view_epoch: 1,
      snapshot_cursor: 3,
    }));
    await waitFor(() => expect(screen.getByRole("status"))
      .toHaveTextContent("Refreshing authoritative Home dashboard state"));
    expect(screen.getByText("Authoritative delivery")).toBeVisible();
    await waitFor(() => expect(streams).toHaveLength(3), { timeout: 4000 });
    act(() => streams[2].open());
    await waitFor(() => expect(screen.queryByText("Frontier snapshot is temporarily unavailable."))
      .not.toBeInTheDocument());

    act(() => streams[2].emit("client_upgrade_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "client_upgrade_required",
      view_type: "home_dashboard",
      view_id: "index",
      view_epoch: 1,
      snapshot_cursor: 3,
    }));
    expect(screen.getByRole("alert")).toHaveTextContent("Client update required");
    expect(screen.queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
    expect(screen.getByText("Authoritative delivery")).toBeVisible();
  });

  it("renders a distinct initial error before any trusted Home snapshot exists", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(new Error("initial snapshot failed")));
    vi.stubGlobal("EventSource", FakeHomeStream);

    renderHome();

    expect(await screen.findByRole("alert")).toHaveTextContent("Unable to load Home dashboard");
    expect(screen.getByRole("button", { name: "Retry Home dashboard" })).toBeVisible();
  });
});

function seedHome(value: ProjectionSnapshot<HomeDashboardData>): void {
  const view = productionViews.home();
  const lease = clientViewReplicas.acquire(view);
  lease.replica.commitDecodedSnapshot(view.decoder.decodeSnapshot(value, {
    requestedClientCommandIds: [],
  }));
  lease.release();
}
