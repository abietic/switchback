import { describe, expect, it } from "vitest";
import type { HomeDashboardData } from "../../api/homeContracts";
import { adaptHome } from "./homeAdapter";

function dashboard(): HomeDashboardData {
  return {
    attention_tasks: [
      {
        task_id: "task-waiting",
        title: "Review the proposed patch",
        workspace: "workspace-local",
        status: "waiting_user",
        status_tone: "waiting",
        worker_session_id: "session-1",
        updated_at: "2026-07-29T00:00:00Z",
      },
      {
        task_id: "task-delivered",
        title: "Accept the delivery",
        workspace: "workspace-local",
        status: "delivered",
        status_tone: "waiting",
        updated_at: "2026-07-28T00:00:00Z",
      },
    ],
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
  };
}

describe("adaptHome", () => {
  it("creates canonical presentation links while preserving authoritative fields", () => {
    const home = adaptHome(dashboard());

    expect(home.attention).toHaveLength(2);
    expect(home.attention[0]).toMatchObject({
      href: "/workers/session-1",
      status: "waiting_user",
      statusTone: "waiting",
    });
    expect(home.attention[1].href).toBe("/tasks");
    expect(home.capacity.percent).toBe(30);
    expect(home.recentWorkers[0]).toMatchObject({
      href: "/workers/session-1",
      status: "idle",
      statusTone: "waiting",
      runtimeGeneration: 4,
    });
    expect(home.deliveries[0]).toMatchObject({
      acceptanceMode: "manual",
      status: "delivered",
      workerHref: "/workers/session-1",
    });
    expect(home.deliveries[0].artifacts[0].href).toBe("/artifacts/artifact-1");
  });

  it("keeps over-capacity evidence visible instead of hiding it in a capped percentage", () => {
    const value = dashboard();
    value.capacity = { used: 12, limit: 10 };

    expect(adaptHome(value).capacity).toEqual({
      used: 12,
      limit: 10,
      percent: 120,
      fillPercent: 100,
      overBy: 2,
    });
  });

  it("passes auto acceptance through without presentation inference", () => {
    const value = dashboard();
    value.recent_deliveries[0].acceptance_mode = "auto";

    expect(adaptHome(value).deliveries[0].acceptanceMode).toBe("auto");
  });
});
