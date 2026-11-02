import { describe, expect, it } from "vitest";
import type { ProjectionSSEFrame } from "../viewRuntime";
import { createTaskIndexViewDecoder } from "./taskIndex";

function taskIndexData() {
  return {
    tasks: [
      {
        task_id: "task-queued",
        title: "Materialize a real Task",
        workspace: "workspace-local",
        status: "queued",
        status_tone: "neutral",
        updated_at: "2026-07-29T00:00:00Z",
      },
      {
        task_id: "task-running",
        title: "Run the coding agent",
        workspace: "workspace-local",
        status: "running",
        status_tone: "active",
        worker_session_id: "worker-local",
        updated_at: "2026-07-29T00:00:00.123456789Z",
      },
    ],
    capacity: { used: 11, limit: 10 },
  };
}

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "task_summary",
    view_id: "index",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-29T00:00:00Z",
    data: taskIndexData(),
    available_actions: [],
    related_refs: [],
    ...overrides,
  };
}

function delta(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "task-index-delta-2",
    view_type: "task_summary",
    view_id: "index",
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [{ type: "integration_event", id: "event-task-index-2" }],
    payload: {
      data: taskIndexData(),
      available_actions: [],
      related_refs: [],
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

function decodeDelta(value: unknown) {
  const decoder = createTaskIndexViewDecoder();
  const current = decoder.decodeSnapshot(snapshot(), { requestedClientCommandIds: [] }).effective;
  return decoder.decodeDeltaFrame(frame(value), current);
}

describe("Task index production view contract", () => {
  it("decodes the real unbounded Task index Snapshot and exact-next Delta", () => {
    const decoder = createTaskIndexViewDecoder();
    const manyTasks = Array.from({ length: 7 }, (_, index) => ({
      ...taskIndexData().tasks[0]!,
      task_id: `task-${index}`,
    }));
    const decoded = decoder.decodeSnapshot(snapshot({
      data: { ...taskIndexData(), tasks: manyTasks, additive: true },
    }), { requestedClientCommandIds: [] });
    expect(decoder.viewType).toBe("task_summary");
    expect(decoder.viewId).toBe("index");
    expect(decoded.effective.data.tasks).toHaveLength(7);
    expect(decoded.effective.data.capacity).toEqual({ used: 11, limit: 10 });

    const next = decodeDelta(delta({ payload: { data: taskIndexData() } }));
    expect(next.result.kind).toBe("apply");
  });

  it("accepts data-only, action-only and ref-only exact-next updates", () => {
    expect(decodeDelta(delta({ payload: { data: taskIndexData() } })).result.kind).toBe("apply");
    expect(decodeDelta(delta({ payload: { available_actions: [] } })).result.kind).toBe("apply");
    expect(decodeDelta(delta({ payload: { related_refs: [] } })).result.kind).toBe("apply");
  });

  it("rejects individual Task shape, non-empty actions/refs and unsupported events", () => {
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({ view_id: "task-1" }), {
      requestedClientCommandIds: [],
    })).toThrow();
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({
      data: {
        task_id: "task-1",
        title: "Individual Task",
        workspace: "workspace-local",
        status: "queued",
        status_tone: "neutral",
        revision: 1,
        updated_at: "2026-07-29T00:00:00Z",
      },
    }), { requestedClientCommandIds: [] })).toThrow();
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({
      available_actions: [{ action: "task.cancel", target_id: "task-queued", expected_revision: 1 }],
    }), { requestedClientCommandIds: [] })).toThrow();
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({
      related_refs: [{
        type: "worker_detail",
        id: "worker-local",
        href: "/api/v1/views/worker_detail/worker-local",
      }],
    }), { requestedClientCommandIds: [] })).toThrow();
    expect(() => decodeDelta(delta({ event_type: "view.stream_checkpointed.v1" }))).toThrow();
  });

  it.each([
    ["queued", "neutral"],
    ["running", "active"],
    ["waiting_user", "waiting"],
    ["delivered", "waiting"],
    ["completed", "success"],
    ["failed", "danger"],
    ["cancelled", "danger"],
  ])("accepts status %s only with tone %s", (status, tone) => {
    const data = taskIndexData();
    Object.assign(data.tasks[0]!, { status, status_tone: tone });
    expect(createTaskIndexViewDecoder().decodeSnapshot(snapshot({ data }), {
      requestedClientCommandIds: [],
    }).effective.data.tasks[0]?.status).toBe(status);

    const invalid = structuredClone(data);
    invalid.tasks[0]!.status_tone = tone === "neutral" ? "active" : "neutral";
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({ data: invalid }), {
      requestedClientCommandIds: [],
    })).toThrow();
  });

  it("rejects unsafe numbers, invalid timestamps, identities and nullable optional Worker", () => {
    const unsafe = { ...taskIndexData(), capacity: { used: Number.MAX_SAFE_INTEGER + 1, limit: 10 } };
    expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({ data: unsafe }), {
      requestedClientCommandIds: [],
    })).toThrow();

    for (const mutation of [
      { task_id: " task-queued" },
      { updated_at: "2026-07-29T08:00:00+08:00" },
      { worker_session_id: null },
      { worker_session_id: undefined },
    ]) {
      const data = taskIndexData();
      Object.assign(data.tasks[0]!, mutation);
      expect(() => createTaskIndexViewDecoder().decodeSnapshot(snapshot({ data }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
  });
});

describe("queued runtime dispatch waits", () => {
  const reasons = ["capacity_uninitialized", "capacity_evidence_invalid", "capacity_evidence_stale", "capacity_identity_drift", "capacity_inventory_unknown", "capacity_exhausted", "capacity_path_not_integrated"];
  const observed_at = "2026-10-13T06:00:00.123456Z";
  function waitingData(wait: unknown, status = "queued") {
    const data = taskIndexData();
    Object.assign(data.tasks[0]!, {status, status_tone: status === "queued" ? "neutral" : "active", runtime_dispatch_wait: wait});
    return data;
  }
  it.each(reasons)("retains %s in snapshot and exact-next delta", (reason) => {
    const wait = {reason, observed_at};
    const data = waitingData(wait);
    const decoder = createTaskIndexViewDecoder();
    const decoded = decoder.decodeSnapshot(snapshot({data}), {requestedClientCommandIds: []});
    expect(decoded.effective.data.tasks[0]).toHaveProperty("runtime_dispatch_wait", wait);
    const next = decoder.decodeDeltaFrame(frame(delta({payload: {data}})), decoded.effective);
    expect(next.result.kind).toBe("apply");
  });
  it.each([null, undefined, {}, {reason:"unknown",observed_at}, {reason:"capacity_exhausted", observed_at:"2026-10-13T14:00:00+08:00"}, {reason:"capacity_exhausted",observed_at:"2026-02-30T00:00:00Z"}])("rejects malformed snapshot and delta diagnostic %j", (wait) => {
    const data=waitingData(wait);
    expect(()=>createTaskIndexViewDecoder().decodeSnapshot(snapshot({data}),{requestedClientCommandIds:[]})).toThrow();
    if (wait !== undefined) expect(()=>decodeDelta(delta({payload:{data}}))).toThrow();
  });
  it("rejects wait on a running task in both transports", () => {
    const data=waitingData({reason:"capacity_exhausted",observed_at},"running");
    expect(()=>createTaskIndexViewDecoder().decodeSnapshot(snapshot({data}),{requestedClientCommandIds:[]})).toThrow();
    expect(()=>decodeDelta(delta({payload:{data}}))).toThrow();
  });
});
