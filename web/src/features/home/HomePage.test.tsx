import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HomeDashboardData } from "../../api/homeContracts";
import { productionViews } from "../../projections/productionViewRegistry";
import { HomePage } from "./HomePage";

const useFrontierViewMock = vi.hoisted(() => vi.fn());

vi.mock("../../state/useFrontierView", () => ({
  useFrontierView: useFrontierViewMock,
}));

function dashboard(overrides: Partial<HomeDashboardData> = {}): HomeDashboardData {
  return {
    attention_tasks: [{
      task_id: "task-waiting",
      title: "Review the proposed patch",
      workspace: "workspace-local",
      status: "waiting_user",
      status_tone: "waiting",
      worker_session_id: "session-1",
      updated_at: "2026-07-29T00:00:00Z",
    }],
    recent_workers: [{
      worker_session_id: "session-1",
      workspace: "workspace-local",
      status: "idle",
      status_tone: "waiting",
      runtime_generation: 4,
      bound_task_id: "task-waiting",
      task_title: "Review the proposed patch",
      updated_at: "2026-07-29T00:00:00Z",
    }],
    recent_deliveries: [{
      task_id: "task-delivered",
      title: "Accept the delivery",
      workspace: "workspace-local",
      status: "delivered",
      status_tone: "waiting",
      acceptance_mode: "manual",
      worker_session_id: "session-1",
      updated_at: "2026-07-28T00:00:00Z",
      artifacts: [{
        artifact_id: "artifact-1",
        artifact_type: "report",
        href: "/api/v1/views/artifact_detail/artifact-1",
      }],
    }],
    capacity: { used: 3, limit: 10 },
    updated_at: "2026-07-29T00:00:00Z",
    ...overrides,
  };
}

function viewState(data?: HomeDashboardData, sync = data ? "ready" : "empty") {
  return {
    sync,
    data,
    availableActions: [],
    relatedRefs: [],
    transient: [],
    pendingCommands: [],
  };
}

function renderPage() {
  return render(<MemoryRouter><HomePage /></MemoryRouter>);
}

afterEach(() => {
  useFrontierViewMock.mockReset();
});

describe("HomePage", () => {
  it("loads only the validated Home View and renders canonical actions", () => {
    useFrontierViewMock.mockReturnValue({
      state: viewState(dashboard()),
      status: "live",
      error: undefined,
      manualRetry: vi.fn(),
    });

    renderPage();

    expect(useFrontierViewMock).toHaveBeenCalledWith(expect.objectContaining({
      kind: "home",
      viewType: "home_dashboard",
      viewId: "index",
      contractKey: productionViews.home().contractKey,
    }));
    const attention = screen.getByRole("heading", { name: "Needs your attention" }).closest("section");
    const workers = screen.getByRole("heading", { name: "Recent Worker sessions" }).closest("section");
    expect(within(attention as HTMLElement).getByRole("link", { name: /Review the proposed patch/ }))
      .toHaveAttribute("href", "/workers/session-1");
    expect(within(workers as HTMLElement).getByRole("link", { name: /session-1/ }))
      .toHaveAttribute("href", "/workers/session-1");
    expect(screen.getByRole("link", { name: /report artifact-1/ })).toHaveAttribute("href", "/artifacts/artifact-1");
    expect(screen.getByText(/manual acceptance/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start Assistant conversation" })).toHaveAttribute("href", "/assistant");
    expect(screen.getByRole("link", { name: "View all tasks" })).toHaveAttribute("href", "/tasks");
  });

  it("renders actionable empty sections without sample records", () => {
    useFrontierViewMock.mockReturnValue({
      state: viewState(dashboard({
        attention_tasks: [],
        recent_workers: [],
        recent_deliveries: [],
      })),
      status: "live",
      error: undefined,
      manualRetry: vi.fn(),
    });

    renderPage();

    expect(screen.getByText("No tasks need your attention.")).toBeInTheDocument();
    expect(screen.getByText("No recent Worker sessions.")).toBeInTheDocument();
    expect(screen.getByText("No recent deliveries.")).toBeInTheDocument();
    expect(screen.queryByText("Review the proposed patch")).not.toBeInTheDocument();
  });

  it("displays the authoritative auto acceptance mode", () => {
    const value = dashboard();
    value.recent_deliveries[0].acceptance_mode = "auto";
    useFrontierViewMock.mockReturnValue({
      state: viewState(value),
      status: "live",
      error: undefined,
      manualRetry: vi.fn(),
    });

    renderPage();

    expect(screen.getByText(/auto acceptance/)).toBeInTheDocument();
  });

  it("renders an accessible loading skeleton", () => {
    useFrontierViewMock.mockReturnValue({
      state: viewState(),
      status: "connecting",
      error: undefined,
      manualRetry: vi.fn(),
    });

    renderPage();

    expect(screen.getByRole("status", { name: "Loading Home dashboard" })).toHaveAttribute("aria-busy", "true");
  });

  it("renders a retry action after a loading error", async () => {
    const manualRetry = vi.fn().mockResolvedValue(undefined);
    useFrontierViewMock.mockReturnValue({
      state: viewState(),
      status: "error",
      error: "Frontier snapshot is unavailable",
      manualRetry,
    });

    renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent("Frontier snapshot is unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Retry Home dashboard" }));
    expect(manualRetry).toHaveBeenCalledTimes(1);
  });
});
