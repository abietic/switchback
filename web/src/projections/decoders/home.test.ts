import { describe, expect, it } from "vitest";
import type { HomeDashboardData } from "../../api/homeContracts";
import type { ProjectionSSEFrame, TrustedViewProjectionState } from "../viewRuntime";
import { createHomeViewDecoder } from "./home";

const taskRef = (id: string) => ({
  type: "task_summary",
  id,
  href: `/api/v1/views/task_summary/${id}`,
});

const workerRef = (id: string) => ({
  type: "worker_detail",
  id,
  href: `/api/v1/views/worker_detail/${id}`,
});

const artifactRef = (id: string) => ({
  type: "artifact_detail",
  id,
  href: `/api/v1/views/artifact_detail/${id}`,
});

function homeData() {
  return {
    attention_tasks: [{
      task_id: "task-waiting",
      title: "Confirm the production contract",
      workspace: "workspace-local",
      status: "waiting_user",
      status_tone: "waiting",
      worker_session_id: "worker-local",
      updated_at: "2026-07-29T00:00:00.123456789Z",
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
    capacity: { used: 11, limit: 10 },
    updated_at: "2026-07-29T00:00:00Z",
  };
}

function relatedRefs() {
  return [
    taskRef("task-waiting"),
    workerRef("worker-local"),
    taskRef("task-delivered"),
    artifactRef("artifact-report"),
  ];
}

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "home_dashboard",
    view_id: "index",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-29T00:00:00Z",
    data: homeData(),
    available_actions: [],
    related_refs: relatedRefs(),
    ...overrides,
  };
}

function delta(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "home-delta-2",
    view_type: "home_dashboard",
    view_id: "index",
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [{ type: "integration_event", id: "event-home-2" }],
    payload: {
      data: homeData(),
      available_actions: [],
      related_refs: relatedRefs(),
    },
    ...overrides,
  };
}

function frame(value: unknown): ProjectionSSEFrame {
  return {
    eventName: "projection_delta",
    lastEventId: "1:2",
    data: JSON.stringify(value),
  };
}

function initialState(): TrustedViewProjectionState<HomeDashboardData> {
  return createHomeViewDecoder().decodeSnapshot(
    snapshot(),
    { requestedClientCommandIds: [] },
  ).effective;
}

function decodeDelta(value: unknown, current = initialState()) {
  return createHomeViewDecoder().decodeDeltaFrame(frame(value), current);
}

describe("Home production view contract", () => {
  it("decodes the real materializer Snapshot under the exact Home identity", () => {
    const decoder = createHomeViewDecoder();
    const decoded = decoder.decodeSnapshot(snapshot({ additive_envelope: true }), {
      requestedClientCommandIds: [],
    });

    expect(decoder.viewType).toBe("home_dashboard");
    expect(decoder.viewId).toBe("index");
    expect(decoded.effective.data.capacity).toEqual({ used: 11, limit: 10 });
    expect(decoded.effective.data.attention_tasks[0]?.worker_session_id).toBe("worker-local");
    expect(decoded.effective.relatedRefs).toHaveLength(4);
    expect(decoded.envelope).not.toHaveProperty("command_receipts");
  });

  it("accepts exact-next data-only, action-only and ref-only authority updates", () => {
    const current = initialState();
    const dataOnly = decodeDelta(delta({
      payload: { data: { ...homeData(), capacity: { used: 12, limit: 10 } } },
    }), current);
    expect(dataOnly.result.kind).toBe("apply");
    if (dataOnly.result.kind !== "apply") throw new Error("expected data-only apply");
    expect(dataOnly.result.delta.effective.data.capacity.used).toBe(12);
    expect(dataOnly.result.delta.effective.relatedRefs).toEqual(current.relatedRefs);

    const actionOnly = decodeDelta(delta({ payload: { available_actions: [] } }), current);
    expect(actionOnly.result.kind).toBe("apply");

    const refOnly = decodeDelta(delta({ payload: { related_refs: [...relatedRefs()].reverse() } }), current);
    expect(refOnly.result.kind).toBe("apply");
  });

  it("requires empty actions and the exact deduplicated refs derived from top-level Home lists", () => {
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({
      available_actions: [{
        action: "task.accept",
        target_id: "task-delivered",
        expected_revision: 1,
      }],
    }), { requestedClientCommandIds: [] })).toThrow();

    for (const invalidRefs of [
      relatedRefs().slice(1),
      [...relatedRefs(), taskRef("task-extra")],
      [...relatedRefs(), taskRef("task-waiting")],
    ]) {
      expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ related_refs: invalidRefs }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }

    // Nested display fields do not prove additional navigation authority.
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({
      related_refs: [...relatedRefs(), taskRef("worker-bound-task")],
    }), { requestedClientCommandIds: [] })).toThrow();

    const nestedDisplayOnly = structuredClone(homeData());
    nestedDisplayOnly.attention_tasks[0]!.worker_session_id = "worker-nested-only";
    nestedDisplayOnly.recent_workers[0]!.bound_task_id = "task-worker-nested-only";
    nestedDisplayOnly.recent_deliveries[0]!.worker_session_id = "worker-delivery-only";
    expect(createHomeViewDecoder().decodeSnapshot(snapshot({ data: nestedDisplayOnly }), {
      requestedClientCommandIds: [],
    }).effective.relatedRefs).toHaveLength(4);
  });

  it("rejects unsupported events and authority-only Delta pollution", () => {
    expect(() => decodeDelta(delta({ event_type: "view.stream_checkpointed.v1" }))).toThrow();
    expect(() => decodeDelta(delta({
      payload: {
        available_actions: [{
          action: "task.accept",
          target_id: "task-delivered",
          expected_revision: 1,
        }],
      },
    }))).toThrow();
    expect(() => decodeDelta(delta({ payload: { related_refs: [] } }))).toThrow();
  });

  it("rejects unsafe numbers, malformed timestamps and non-canonical identities", () => {
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ view_id: "other" }), {
      requestedClientCommandIds: [],
    })).toThrow();
    const unsafeCapacity = { ...homeData(), capacity: { used: Number.MAX_SAFE_INTEGER + 1, limit: 10 } };
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: unsafeCapacity }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const unsafeGeneration = structuredClone(homeData());
    unsafeGeneration.recent_workers[0]!.runtime_generation = 1.5;
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: unsafeGeneration }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const invalidTimestamp = structuredClone(homeData());
    invalidTimestamp.attention_tasks[0]!.updated_at = "2026-07-29T08:00:00+08:00";
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: invalidTimestamp }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const invalidIdentity = structuredClone(homeData());
    invalidIdentity.attention_tasks[0]!.task_id = " task-waiting";
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: invalidIdentity }), {
      requestedClientCommandIds: [],
    })).toThrow();
  });

  it("requires optional fields to be omitted rather than null or explicit undefined", () => {
    for (const value of [null, undefined]) {
      const invalid = structuredClone(homeData());
      (invalid.attention_tasks[0] as Record<string, unknown>).worker_session_id = value;
      expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: invalid }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
  });

  it("enforces list bounds, closed statuses and Artifact href/type bindings", () => {
    const tooMany = structuredClone(homeData());
    tooMany.attention_tasks = Array.from({ length: 7 }, (_, index) => ({
      ...homeData().attention_tasks[0]!,
      task_id: `task-${index}`,
    }));
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: tooMany }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const status = structuredClone(homeData());
    (status.recent_workers[0] as Record<string, unknown>).status = "unknown";
    expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: status }), {
      requestedClientCommandIds: [],
    })).toThrow();

    for (const artifactOverrides of [
      { artifact_type: "archive" },
      { href: "/api/v1/views/artifact_detail/other" },
    ]) {
      const artifact = structuredClone(homeData());
      Object.assign(artifact.recent_deliveries[0]!.artifacts[0]!, artifactOverrides);
      expect(() => createHomeViewDecoder().decodeSnapshot(snapshot({ data: artifact }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
  });
});
