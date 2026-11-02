import { describe, expect, it } from "vitest";
import { parseHomeDashboardData } from "./homeContracts";

function homeData() {
  return {
    attention_tasks: [{
      task_id: "task-waiting",
      title: "Confirm the production contract",
      workspace: "workspace-local",
      status: "waiting_user",
      status_tone: "waiting",
      worker_session_id: "worker-local",
      updated_at: "2026-07-29T00:00:00Z",
    }],
    recent_workers: [{
      worker_session_id: "worker-local",
      workspace: "workspace-local",
      status: "idle",
      status_tone: "waiting",
      runtime_generation: 2,
      bound_task_id: "task-waiting",
      task_title: "Confirm the production contract",
      updated_at: "2026-07-29T00:00:00Z",
    }],
    recent_deliveries: [{
      task_id: "task-delivered",
      title: "Publish the contract evidence",
      workspace: "workspace-local",
      status: "delivered",
      status_tone: "waiting",
      acceptance_mode: "manual",
      worker_session_id: "worker-local",
      updated_at: "2026-07-29T00:00:00Z",
      artifacts: [{
        artifact_id: "artifact-report",
        artifact_type: "report",
        href: "/api/v1/views/artifact_detail/artifact-report",
      }],
    }],
    capacity: { used: 3, limit: 10 },
    updated_at: "2026-07-29T00:00:00Z",
  };
}

describe("Home dashboard data contract", () => {
  it("accepts additive fields while retaining canonical data", () => {
    expect(parseHomeDashboardData({ ...homeData(), additive_field: true })).toEqual(homeData());
  });

  it("rejects unsafe capacity and nullable identities", () => {
    expect(() => parseHomeDashboardData({
      ...homeData(),
      capacity: { used: Number.MAX_SAFE_INTEGER + 1, limit: 10 },
    })).toThrow("capacity used is invalid");
    expect(() => parseHomeDashboardData({
      ...homeData(),
      attention_tasks: [{ ...homeData().attention_tasks[0], worker_session_id: null }],
    })).toThrow("attention task worker_session_id is invalid");
  });

  it("keeps display limits, status authority and Artifact hrefs closed", () => {
    expect(() => parseHomeDashboardData({
      ...homeData(),
      attention_tasks: Array.from({ length: 7 }, (_, index) => ({
        ...homeData().attention_tasks[0],
        task_id: `task-${index}`,
      })),
    })).toThrow("attention_tasks is invalid");
    expect(() => parseHomeDashboardData({
      ...homeData(),
      recent_workers: [{ ...homeData().recent_workers[0], status_tone: "active" }],
    })).toThrow("recent worker status_tone is invalid");
    expect(() => parseHomeDashboardData({
      ...homeData(),
      recent_deliveries: [{
        ...homeData().recent_deliveries[0],
        artifacts: [{ ...homeData().recent_deliveries[0].artifacts[0], href: "/artifacts/report" }],
      }],
    })).toThrow("delivery artifact href is invalid");
  });
});
