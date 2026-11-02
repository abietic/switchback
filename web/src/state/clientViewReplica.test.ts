import { describe, expect, it, vi } from "vitest";
import type {
  ProjectionDelta,
  ProjectionSnapshot,
  TransientDisplayDelta,
  WorkerDetailData,
} from "../api/contracts";
import { createWorkerViewDecoder } from "../projections/decoders/worker";
import { workerData } from "../projections/testdata/workerFixtures";
import {
  type DecodedProjectionDelta,
  type DecodedProjectionSnapshot,
} from "../projections/viewRuntime";
import { ClientViewReplica } from "./clientViewReplica";

type TestView = WorkerDetailData;

function testData(overrides: Record<string, unknown> = {}): WorkerDetailData {
  return workerData({
    artifacts: [],
    approvals: [],
    timeline_groups: [],
    stage_report: null,
    ...overrides,
  }) as unknown as WorkerDetailData;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function snapshot(overrides: Partial<ProjectionSnapshot<TestView>> = {}): ProjectionSnapshot<TestView> {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "worker_detail",
    view_id: "session-1",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 4,
    projection_version: 7,
    query_fingerprint: "worker-session-1",
    generated_at: "2026-07-23T00:00:00Z",
    data: testData(),
    available_actions: [],
    related_refs: [],
    ...overrides,
  };
}

function delta(overrides: Partial<ProjectionDelta<TestView>> = {}): ProjectionDelta<TestView> {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "delta-5",
    view_type: "worker_detail",
    view_id: "session-1",
    view_epoch: 1,
    view_cursor: 5,
    base_projection_version: 7,
    result_projection_version: 8,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [],
    payload: { data: testData({ runtime_health: "Waiting" }) },
    ...overrides,
  };
}

function transient(overrides: Partial<TransientDisplayDelta> = {}): TransientDisplayDelta {
  return {
    schema_version: "frontier.transient_display_delta.v1",
    display_stream_id: "display-1",
    part_id: "part-1",
    base_checkpoint_version: 2,
    transient_sequence: 1,
    delta_type: "text_delta",
    payload: { text: "Working" },
    emitted_at: "2026-07-23T00:00:01Z",
    ...overrides,
  };
}

function testDecoder() {
  return createWorkerViewDecoder("session-1");
}

function decodedSnapshot(
  requestedClientCommandIds: readonly string[] = [],
  overrides: Partial<ProjectionSnapshot<TestView>> = {},
): DecodedProjectionSnapshot<TestView> {
  return testDecoder().decodeSnapshot(snapshot(overrides), { requestedClientCommandIds });
}

function decodedDelta(
  current: DecodedProjectionSnapshot<TestView>["effective"],
  overrides: Partial<ProjectionDelta<TestView>> = {},
): DecodedProjectionDelta<TestView> {
  const value = delta(overrides);
  const decision = testDecoder().decodeDeltaFrame({
    eventName: "projection_delta",
    lastEventId: `${value.view_epoch}:${value.view_cursor}`,
    data: JSON.stringify(value),
  }, current);
  if (decision.result.kind !== "apply") throw new Error("expected decoded Delta apply");
  return decision.result.delta;
}

describe("decoded ClientViewReplica authority", () => {
  it("commits one decoded effective state and rejects a stale decode context without partial merge", () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const publishes = vi.fn();
    replica.subscribe(publishes);
    const initial = decodedSnapshot([], {
      available_actions: [{
        action: "worker.follow_up",
        target_id: "session-1",
        expected_revision: 7,
      }],
    });
    expect(replica.commitDecodedSnapshot(initial).kind).toBe("committed");

    const next = decodedDelta(initial.effective, {
      payload: { data: testData({ runtime_health: "Waiting" }) },
    });
    expect(replica.commitDecodedDelta(next).kind).toBe("committed");
    expect(publishes).toHaveBeenCalledTimes(2);
    expect(replica.getState()).toMatchObject({
      cursor: { epoch: 1, sequence: 5 },
      projectionVersion: 8,
      data: { runtime_health: "Waiting" },
      availableActions: [{ action: "worker.follow_up" }],
    });

    const trusted = replica.getState();
    expect(replica.commitDecodedDelta(next).kind).toBe("stale_decode_context");
    expect(replica.getState()).toBe(trusted);
    expect(publishes).toHaveBeenCalledTimes(2);
  });

  it("freezes Snapshot request context and rejects a response decoded before local command admission", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const stale = decodedSnapshot();
    expect(Object.isFrozen(stale.requestContext)).toBe(true);
    expect(Object.isFrozen(stale.requestContext.requestedClientCommandIds)).toBe(true);
    const accepted = deferred<{ command_id: string }>();
    const result = replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: () => accepted.promise,
      isDefinitiveFailure: () => false,
    });

    expect(replica.commitDecodedSnapshot(stale).kind).toBe("stale_decode_context");
    expect(replica.getState().data).toBeUndefined();
    replica.clearAccess();
    accepted.resolve({ command_id: "server-command-1" });
    await result;
  });

  it("clears all protected View and local command authority on access loss", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    expect(replica.commitDecodedSnapshot(decodedSnapshot()).kind).toBe("committed");
    replica.applyTransient(transient());
    const accepted = deferred<{ command_id: string }>();
    const result = replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: () => accepted.promise,
      isDefinitiveFailure: () => false,
    });

    replica.clearAccess();
    expect(replica.getState()).toEqual({
      connection: "connecting",
      sync: "empty",
      availableActions: [],
      relatedRefs: [],
      transient: [],
      pendingCommands: [],
    });
    accepted.resolve({ command_id: "server-command-1" });
    await result;
  });

  it("accepts only a decoded bound Control entry point", () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    const control = testDecoder().decodeControlFrame({
      eventName: "client_upgrade_required",
      lastEventId: "1:4",
      data: JSON.stringify({
        schema_version: "frontier.projection_control.v1",
        event_type: "client_upgrade_required",
        view_type: "worker_detail",
        view_id: "session-1",
        view_epoch: 1,
        snapshot_cursor: 4,
      }),
    }, initial.effective);

    expect(replica.commitDecodedControl(control).kind).toBe("committed");
    expect(replica.getState().sync).toBe("client_upgrade_required");
  });

  it("charges one branded Delta-conflict guard with zero mutation and Manual Retry resets only it", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    await replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: async () => ({ command_id: "server-command-a" }),
      isDefinitiveFailure: () => false,
    });
    const conflict = decodedDelta(initial.effective, {
      client_command_id: "client-command-1",
      command_id: "server-command-b",
    });
    const before = replica.getState();

    const first = replica.commitDecodedDelta(conflict);
    expect(first.kind).toBe("command_reconciliation_required");
    if (first.kind !== "command_reconciliation_required") throw new Error("expected cause");
    expect(first.cause).toMatchObject({
      reason: "delta_command_identity_conflict",
      trustedPosition: { viewEpoch: 1, viewCursor: 4, projectionVersion: 7 },
      conflictingPosition: { viewEpoch: 1, viewCursor: 5, projectionVersion: 8 },
      requestedClientCommandIds: ["client-command-1"],
    });
    expect(Object.isFrozen(first.cause)).toBe(true);
    expect(replica.getState()).toBe(before);
    expect(replica.commitDecodedDelta(conflict).kind).toBe("command_reconciliation_blocked");

    replica.beginManualRetry();
    expect(replica.getState().pendingCommands).toHaveLength(1);
    expect(replica.commitDecodedDelta(conflict).kind).toBe("command_reconciliation_required");
    replica.clearAccess();
  });

  it("rejects a Delta whose declared client conflicts with the server identity owner", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    await replica.submitCommand({
      clientCommandId: "client-command-a",
      action: "steer",
      submit: async () => ({ command_id: "server-command-shared" }),
      isDefinitiveFailure: () => false,
    });
    const conflict = decodedDelta(initial.effective, {
      client_command_id: "client-command-b",
      command_id: "server-command-shared",
    });
    const before = replica.getState();

    expect(replica.commitDecodedDelta(conflict).kind)
      .toBe("command_reconciliation_required");
    expect(replica.getState()).toBe(before);
    expect(replica.getState().pendingCommands).toEqual([
      expect.objectContaining({
        clientCommandId: "client-command-a",
        commandId: "server-command-shared",
      }),
    ]);
    expect(replica.commitDecodedDelta(conflict).kind)
      .toBe("command_reconciliation_blocked");
    replica.clearAccess();
  });

  it("retains a Snapshot-receipt guard across receipt-absent and unrelated durable commits", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    await replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: async () => ({ command_id: "server-command-a" }),
      isDefinitiveFailure: () => false,
    });
    const conflictingReceipt = decodedSnapshot(["client-command-1"], {
      command_receipts: [{
        client_command_id: "client-command-1",
        command_id: "server-command-b",
      }],
    });
    expect(replica.commitDecodedSnapshot(conflictingReceipt).kind)
      .toBe("command_reconciliation_required");

    const unrelated = decodedDelta(initial.effective);
    expect(replica.commitDecodedDelta(unrelated).kind).toBe("committed");
    const receiptAbsent = decodedSnapshot(["client-command-1"], {
      view_cursor: 5,
      projection_version: 8,
    });
    expect(replica.commitDecodedSnapshot(receiptAbsent).kind).toBe("committed");
    expect(replica.commitDecodedSnapshot(decodedSnapshot(["client-command-1"], {
      view_cursor: 5,
      projection_version: 8,
      command_receipts: [{
        client_command_id: "client-command-1",
        command_id: "server-command-b",
      }],
    })).kind).toBe("command_reconciliation_blocked");
    replica.clearAccess();
  });

  it("clears a receipt guard only after the implicated binding settles", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    replica.commitDecodedSnapshot(decodedSnapshot());
    await replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: async () => ({ command_id: "server-command-a" }),
      isDefinitiveFailure: () => false,
    });
    expect(replica.commitDecodedSnapshot(decodedSnapshot(["client-command-1"], {
      command_receipts: [{
        client_command_id: "client-command-1",
        command_id: "server-command-b",
      }],
    })).kind).toBe("command_reconciliation_required");

    expect(replica.commitDecodedSnapshot(decodedSnapshot(["client-command-1"], {
      command_receipts: [{
        client_command_id: "client-command-1",
        command_id: "server-command-a",
      }],
    })).kind).toBe("committed");
    expect(replica.getState().pendingCommands).toEqual([]);

    await replica.submitCommand({
      clientCommandId: "client-command-2",
      action: "steer",
      submit: async () => ({ command_id: "server-command-c" }),
      isDefinitiveFailure: () => false,
    });
    expect(replica.commitDecodedSnapshot(decodedSnapshot(["client-command-2"], {
      command_receipts: [{
        client_command_id: "client-command-2",
        command_id: "server-command-d",
      }],
    })).kind).toBe("command_reconciliation_required");
    replica.clearAccess();
  });

  it("clears a receipt-first guard when the active transport later removes its binding", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    const responseA = deferred<{ command_id: string }>();
    const resultA = replica.submitCommand({
      clientCommandId: "client-command-a",
      action: "steer",
      submit: () => responseA.promise,
      isDefinitiveFailure: () => false,
    });
    const observedA = decodedDelta(initial.effective, {
      client_command_id: "client-command-a",
      command_id: "server-command-a",
    });
    expect(replica.commitDecodedDelta(observedA).kind).toBe("committed");
    const conflictingA = decodedDelta(observedA.effective, {
      view_cursor: 6,
      base_projection_version: 8,
      result_projection_version: 9,
      client_command_id: "client-command-a",
      command_id: "server-command-conflict-a",
    });
    expect(replica.commitDecodedDelta(conflictingA).kind)
      .toBe("command_reconciliation_required");

    const receiptA = decodedSnapshot(["client-command-a"], {
      view_cursor: 5,
      projection_version: 8,
      command_receipts: [{
        client_command_id: "client-command-a",
        command_id: "server-command-a",
      }],
    });
    expect(replica.commitDecodedSnapshot(receiptA).kind).toBe("committed");
    expect(replica.getState().pendingCommands).toEqual([]);
    await replica.submitCommand({
      clientCommandId: "client-command-b",
      action: "steer",
      submit: async () => ({ command_id: "server-command-b" }),
      isDefinitiveFailure: () => false,
    });
    const conflictingB = decodedDelta(receiptA.effective, {
      view_cursor: 6,
      base_projection_version: 8,
      result_projection_version: 9,
      client_command_id: "client-command-b",
      command_id: "server-command-conflict-b",
    });
    expect(replica.commitDecodedDelta(conflictingB).kind)
      .toBe("command_reconciliation_blocked");

    responseA.resolve({ command_id: "server-command-a" });
    await resultA;

    expect(replica.commitDecodedDelta(conflictingB).kind)
      .toBe("command_reconciliation_required");
    replica.clearAccess();
  });

  it("clears a Delta-origin guard when a committed Snapshot covers its cursor", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    await replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: async () => ({ command_id: "server-command-a" }),
      isDefinitiveFailure: () => false,
    });
    const firstConflict = decodedDelta(initial.effective, {
      client_command_id: "client-command-1",
      command_id: "server-command-b",
    });
    expect(replica.commitDecodedDelta(firstConflict).kind).toBe("command_reconciliation_required");
    const covering = decodedSnapshot(["client-command-1"], {
      view_cursor: 5,
      projection_version: 8,
    });
    expect(replica.commitDecodedSnapshot(covering).kind).toBe("committed");

    const nextConflict = decodedDelta(covering.effective, {
      view_cursor: 6,
      base_projection_version: 8,
      result_projection_version: 9,
      client_command_id: "client-command-1",
      command_id: "server-command-c",
    });
    expect(replica.commitDecodedDelta(nextConflict).kind).toBe("command_reconciliation_required");
    replica.clearAccess();
  });

  it("emits first/blocked branded submission-response signals without dropping bindings", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    const firstResponse = deferred<{ command_id: string }>();
    const result = replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: () => firstResponse.promise,
      isDefinitiveFailure: () => false,
    });
    expect(replica.commitDecodedDelta(decodedDelta(initial.effective, {
      client_command_id: "client-command-1",
      command_id: "server-command-observed",
    })).kind).toBe("committed");
    const signals: string[] = [];
    replica.subscribeCommandReconciliation((decision) => signals.push(decision.kind));
    firstResponse.resolve({ command_id: "server-command-response" });
    await result;

    await replica.submitCommand({
      clientCommandId: "client-command-1",
      action: "steer",
      submit: async () => ({ command_id: "server-command-third" }),
      isDefinitiveFailure: () => false,
    });
    expect(signals).toEqual([
      "command_reconciliation_required",
      "command_reconciliation_blocked",
    ]);
    expect(replica.getState().pendingCommands).toHaveLength(1);
    replica.clearAccess();
  });

  it("removes only definitive failures and settles ambiguous delivery through decoded evidence", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    const initial = decodedSnapshot();
    replica.commitDecodedSnapshot(initial);
    const definitive = new Error("request rejected before submission");
    const ambiguous = new Error("response lost");

    await expect(replica.submitCommand({
      clientCommandId: "client-definitive",
      action: "steer",
      submit: async () => { throw definitive; },
      isDefinitiveFailure: (cause) => cause === definitive,
    })).rejects.toBe(definitive);
    expect(replica.getState().pendingCommands).toEqual([]);

    await expect(replica.submitCommand({
      clientCommandId: "client-ambiguous",
      action: "steer",
      submit: async () => { throw ambiguous; },
      isDefinitiveFailure: () => false,
    })).rejects.toBe(ambiguous);
    expect(replica.getState().pendingCommands).toHaveLength(1);

    const observed = decodedDelta(initial.effective, {
      client_command_id: "client-ambiguous",
      command_id: "server-command-ambiguous",
    });
    expect(replica.commitDecodedDelta(observed).kind).toBe("committed");
    expect(replica.commitDecodedSnapshot(decodedSnapshot(["client-ambiguous"], {
      view_cursor: 5,
      projection_version: 8,
      command_receipts: [{
        client_command_id: "client-ambiguous",
        command_id: "server-command-ambiguous",
      }],
    })).kind).toBe("committed");
    expect(replica.getState().pendingCommands).toEqual([]);
  });

  it("bounds unresolved command admission without evicting existing obligations", async () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    replica.commitDecodedSnapshot(decodedSnapshot());
    for (let index = 0; index < 64; index += 1) {
      await replica.submitCommand({
        clientCommandId: `client-command-${index}`,
        action: "steer",
        scopeKey: `scope-${index}`,
        submit: async () => { throw new Error(`response lost ${index}`); },
        isDefinitiveFailure: () => false,
      }).catch(() => undefined);
    }
    const overflowTransport = vi.fn(async () => ({ command_id: "server-overflow" }));

    await expect(replica.submitCommand({
      clientCommandId: "client-command-overflow",
      action: "steer",
      scopeKey: "scope-overflow",
      submit: overflowTransport,
      isDefinitiveFailure: () => false,
    })).rejects.toThrow("admission limit");
    expect(overflowTransport).not.toHaveBeenCalled();
    expect(replica.getState().pendingCommands).toHaveLength(64);
    expect(replica.getState().pendingCommands[0]).toMatchObject({
      clientCommandId: "client-command-0",
    });
    replica.clearAccess();
  });

  it("keeps transient output outside durable state and drops one stream on a gap", () => {
    const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
    replica.commitDecodedSnapshot(decodedSnapshot());
    expect(replica.applyTransient(transient())).toBe("applied");
    expect(replica.getState().transient).toHaveLength(1);
    expect(replica.applyTransient(transient({ transient_sequence: 3 }))).toBe("discarded");
    expect(replica.getState().transient).toEqual([]);
    expect(replica.getState().data?.runtime_health).toBe("Active");
  });
});
