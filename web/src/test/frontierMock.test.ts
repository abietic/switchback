import { describe, expect, it } from "vitest";
import type { HomeDashboardData } from "../api/homeContracts";
import { productionViews } from "../projections/productionViewRegistry";
import {
  demoHomeDashboard,
  homeSnapshot,
  productionWorkerSnapshot,
  snapshotFor,
} from "../../tests/frontierMock";

describe("Home Frontier browser fixture", () => {
  it("derives canonical related refs from the supplied Home data", () => {
    expect(homeSnapshot(demoHomeDashboard).related_refs).toEqual([
      { type: "task_summary", id: "task-home-waiting", href: "/api/v1/views/task_summary/task-home-waiting" },
      { type: "worker_detail", id: "worker-home-idle", href: "/api/v1/views/worker_detail/worker-home-idle" },
      { type: "task_summary", id: "task-home-delivered", href: "/api/v1/views/task_summary/task-home-delivered" },
      { type: "artifact_detail", id: "artifact-home-report", href: "/api/v1/views/artifact_detail/artifact-home-report" },
    ]);
  });

  it("does not retain sample related refs for empty Home data", () => {
    const empty: HomeDashboardData = {
      ...demoHomeDashboard,
      attention_tasks: [],
      recent_workers: [],
      recent_deliveries: [],
    };

    expect(homeSnapshot(empty).related_refs).toEqual([]);
  });

  it("preserves outward Worker refs but forbids every reverse Artifact ref", () => {
    expect(productionWorkerSnapshot("session-local-01").related_refs).not.toEqual([]);
    expect(snapshotFor("artifact_detail", "artifact-home-report")?.related_refs).toEqual([]);
    expect(snapshotFor("artifact_detail", "artifact-report-01")?.related_refs).toEqual([]);
  });

  it("admits every browser fixture only through its exact production decoder", () => {
    const fixtures = [
      [productionViews.home(), homeSnapshot()],
      [productionViews.taskIndex(), snapshotFor("task_summary", "index")],
      [productionViews.worker("worker-home-idle"), snapshotFor("worker_detail", "worker-home-idle")],
      [productionViews.worker("session-local-01"), productionWorkerSnapshot("session-local-01")],
      [productionViews.worker("session-local-03"), productionWorkerSnapshot("session-local-03")],
      [productionViews.artifact("artifact-home-report"), snapshotFor("artifact_detail", "artifact-home-report")],
      [productionViews.artifact("artifact-report-01"), snapshotFor("artifact_detail", "artifact-report-01")],
    ] as const;

    for (const [view, fixture] of fixtures) {
      expect(fixture).toBeDefined();
      expect(() => view.decoder.decodeSnapshot(fixture, {
        requestedClientCommandIds: [],
      })).not.toThrow();
    }
  });
});
