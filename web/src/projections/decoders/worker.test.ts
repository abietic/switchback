import { describe, expect, it } from "vitest";
import { ViewProjectionDecodeError } from "../viewRuntime";
import {
  WORKER_SESSION_ID,
  approvalRequest,
  artifactRefs,
  artifactSummary,
  displayPart,
  sparseLegacyWorkerSnapshot,
  terminalActions,
  timelineTurn,
  workerData,
  workerDelta,
  workerFrame,
  workerSnapshot,
} from "../testdata/workerFixtures";
import { createWorkerViewDecoder } from "./worker";

function decodeSnapshot(
  data: unknown = workerData(),
  actions: unknown[] = terminalActions(),
  refs: unknown[] = artifactRefs(),
) {
  return createWorkerViewDecoder(WORKER_SESSION_ID).decodeSnapshot(
    workerSnapshot({ data, available_actions: actions, related_refs: refs }),
    { requestedClientCommandIds: [] },
  );
}

function expectMalformed(run: () => unknown): void {
  expect(run).toThrowError(expect.objectContaining({
    classification: "invalid_contract",
    safeReason: "authority_violation",
  }));
}

function expectAuthorityViolation(run: () => unknown): void {
  expect(run).toThrowError(expect.objectContaining({
    classification: "invalid_contract",
    safeReason: "authority_violation",
  }));
}

function preRunData(overrides: Record<string, unknown> = {}) {
  return workerData({
    status: "allocating",
    status_tone: "waiting",
    task_status: null,
    task_status_label: null,
    task_revision: null,
    run_status: null,
    turn_status: null,
    bound_task_id: null,
    active_worker_run_id: null,
    active_run_revision: null,
    active_agent_turn_id: null,
    active_turn_revision: null,
    runtime_health: "Not started",
    runtime_generation: 0,
    agent_profile: "",
    network_profile: null,
    runtime_recovery: null,
    started_at: null,
    deadline_at: null,
    artifacts: [],
    approvals: [],
    timeline_groups: [],
    timeline_cursor: null,
    timeline_has_more: false,
    waiting_user: null,
    stage_report: null,
    ...overrides,
  });
}

describe("WorkerDetail production view contract", () => {
  it("decodes the current Go materializer Snapshot with exact bound identity", () => {
    const decoder = createWorkerViewDecoder(WORKER_SESSION_ID);
    const decoded = decoder.decodeSnapshot(workerSnapshot({ additive_envelope: true }), {
      requestedClientCommandIds: [],
    });

    expect(decoder.contractKey).toBe(`worker-detail-v1:${WORKER_SESSION_ID}`);
    expect(decoder.viewType).toBe("worker_detail");
    expect(decoder.viewId).toBe(WORKER_SESSION_ID);
    expect(decoded.effective.data).toMatchObject({
      worker_session_id: WORKER_SESSION_ID,
      status: "interactive",
      task_status: "delivered",
      runtime_generation: 2,
      agent_profile: "codex-deepseek-v3.2",
    });
    expect(decoded.effective.data.timeline_groups?.[0]).toMatchObject({
      display_stream_id: "display-1",
      checkpoint_version: 3,
      backfill_status: "online",
    });
    expect(decoded.effective.data.artifacts[0]).toMatchObject({
      artifact_id: "artifact-1",
      checksum: `sha256:${"b".repeat(64)}`,
    });
  });

  it("decodes an exact-next effective Delta for both Worker event families", () => {
    for (const eventType of ["view.entity_upserted.v1", "view.stream_checkpointed.v1"]) {
      const decoder = createWorkerViewDecoder(WORKER_SESSION_ID);
      const current = decoder.decodeSnapshot(workerSnapshot(), {
        requestedClientCommandIds: [],
      }).effective;
      const decision = decoder.decodeDeltaFrame(workerFrame(workerDelta({ event_type: eventType })), current);

      expect(decision.result.kind).toBe("apply");
      if (decision.result.kind !== "apply") throw new Error("expected apply");
      expect(decision.result.delta.effective).toMatchObject({
        viewCursor: 2,
        projectionVersion: 2,
        data: { runtime_health: "Suspect" },
      });
    }
  });

  it("accepts a data-only Delta only when the retained authority remains valid", () => {
    const decoder = createWorkerViewDecoder(WORKER_SESSION_ID);
    const current = decoder.decodeSnapshot(workerSnapshot(), {
      requestedClientCommandIds: [],
    }).effective;
    const delta = workerDelta({
      payload: { data: workerData({ runtime_health: "Unhealthy" }) },
    });
    const decision = decoder.decodeDeltaFrame(workerFrame(delta), current);
    expect(decision.result.kind).toBe("apply");
    if (decision.result.kind !== "apply") throw new Error("expected apply");
    expect(decision.result.delta.effective.availableActions).toEqual(current.availableActions);
    expect(decision.result.delta.effective.relatedRefs).toEqual(current.relatedRefs);
  });

  it("rejects every broad-writer event outside the closed Worker allowlist", () => {
    for (const eventType of [
      "view.artifact_ready.v1",
      "view.command_receipted.v1",
      "view.projection_rebuilt.v1",
      "future.worker.v2",
    ]) {
      const decoder = createWorkerViewDecoder(WORKER_SESSION_ID);
      const current = decoder.decodeSnapshot(workerSnapshot(), {
        requestedClientCommandIds: [],
      }).effective;
      expectMalformed(() => decoder.decodeDeltaFrame(
        workerFrame(workerDelta({ event_type: eventType })),
        current,
      ));
    }
  });

  it("rejects a foreign bound id before product data is admitted", () => {
    const decoder = createWorkerViewDecoder("session-other");
    expect(() => decoder.decodeSnapshot(workerSnapshot(), { requestedClientCommandIds: [] }))
      .toThrowError(expect.objectContaining({ classification: "foreign_identity" }));
  });

  it("accepts the real empty pre-Run/null materializer shape", () => {
    const decoded = decodeSnapshot(preRunData(), [], []);
    expect(decoded.effective.data).toMatchObject({
      status: "allocating",
      runtime_generation: 0,
      agent_profile: "",
      artifacts: [],
      approvals: [],
      timeline_groups: [],
    });
    expect(decoded.effective.data).not.toHaveProperty("active_worker_run_id");
    expect(decoded.effective.data).not.toHaveProperty("network_profile");
  });

  it.each([
    ["allocating", "waiting"],
    ["interactive", "active"],
    ["running", "active"],
    ["recovering", "neutral"],
    ["waiting_user", "waiting"],
    ["idle", "waiting"],
    ["lost", "danger"],
    ["expired", "danger"],
    ["closed", "danger"],
  ])("admits the closed Session status %s with its exact tone", (status, tone) => {
    const waiting_user = status === "waiting_user"
      ? {
          reason: "worker_waiting_user",
          message: "Worker needs input before it can continue.",
          allowed_actions: ["reply", "cancel"],
        }
      : null;
    expect(() => decodeSnapshot(preRunData({ status, status_tone: tone, waiting_user }), [], []))
      .not.toThrow();
  });

  it("admits every closed Task, WorkerRun and AgentTurn status", () => {
    for (const taskStatus of [
      "queued", "running", "waiting_user", "delivered", "completed", "failed", "cancelled",
    ]) {
      expect(() => decodeSnapshot(workerData({
        task_status: taskStatus,
        task_status_label: taskStatus === "completed"
          ? "Accepted"
          : taskStatus.split("_").map((part) => part[0].toUpperCase() + part.slice(1)).join(" "),
      }), [])).not.toThrow();
    }
    for (const runStatus of [
      "starting", "running", "waiting_user", "completed", "failed", "cancelled", "timed_out",
    ]) {
      expect(() => decodeSnapshot(workerData({ run_status: runStatus }), [])).not.toThrow();
    }
    for (const turnStatus of [
      "accepted", "queued", "delivered", "consumed", "completed", "cancelled", "timed_out", "failed",
    ]) {
      expect(() => decodeSnapshot(workerData({ turn_status: turnStatus }), [])).not.toThrow();
    }
  });

  it.each([
    ["unsafe session revision", { session_revision: Number.MAX_SAFE_INTEGER + 1 }],
    ["fractional runtime generation", { runtime_generation: 1.5 }],
    ["unknown Session status", { status: "suspended" }],
    ["wrong Session tone", { status_tone: "waiting" }],
    ["unknown Task status", { task_status: "succeeded" }],
    ["wrong Task label", { task_status_label: "Accepted" }],
    ["unknown Run status", { run_status: "queued" }],
    ["unknown Turn status", { turn_status: "running" }],
    ["unsafe Task revision", { task_revision: Number.MAX_SAFE_INTEGER + 1 }],
    ["null required workspace", { workspace: null }],
    ["missing current nullable field", { network_profile: undefined }],
    ["invalid started_at", { started_at: "2026-02-31T00:00:00Z" }],
  ])("rejects %s", (_name, mutation) => {
    const data = workerData(mutation);
    if (Object.hasOwn(mutation, "network_profile")) {
      delete (data as Record<string, unknown>).network_profile;
    }
    expectMalformed(() => decodeSnapshot(data));
  });

  it("requires Task, Run and Turn nullable tuples to remain internally complete", () => {
    expectMalformed(() => decodeSnapshot(workerData({ task_revision: null })));
    expectMalformed(() => decodeSnapshot(workerData({ active_run_revision: null })));
    expectMalformed(() => decodeSnapshot(workerData({ active_turn_revision: null })));
    expectMalformed(() => decodeSnapshot(preRunData({ agent_profile: "unexpected-profile" }), [], []));
  });

  it("validates current and legacy network profile evidence", () => {
    expect(() => decodeSnapshot(workerData({
      network_profile: {
        id: "m1_public_web_restricted_v1",
        revision: 1,
        digest: `sha256:${"c".repeat(64)}`,
        enforcement_status: "legacy_unenforced",
      },
    }))).not.toThrow();
    expectMalformed(() => decodeSnapshot(workerData({
      network_profile: {
        id: "trusted_full_network_v1",
        revision: 1,
        digest: "sha256:not-a-digest",
        enforcement_status: "active",
      },
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      network_profile: {
        id: "m1_public_web_restricted_v1",
        revision: 1,
        digest: `sha256:${"c".repeat(64)}`,
        enforcement_status: "active",
      },
    })));
  });

  it("validates recovery phase/status/evidence and nullable generation", () => {
    expectMalformed(() => decodeSnapshot(workerData({
      runtime_recovery: {
        ...workerData().runtime_recovery,
        status: "recovering_runtime",
      },
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      runtime_recovery: {
        ...workerData().runtime_recovery,
        failed_generation: 0,
      },
    })));
    expect(() => decodeSnapshot(workerData({
      runtime_recovery: {
        status: "recovering_runtime",
        phase: "fencing",
        failed_generation: 2,
        replacement_generation: null,
        attempt_count: 0,
        continuity_status: "pending",
        evidence_at: "2026-07-25T00:00:05Z",
        outcome_reason: null,
      },
    }))).not.toThrow();
  });

  it("validates artifacts and exact Artifact navigation authority", () => {
    expectMalformed(() => decodeSnapshot(workerData({
      artifacts: [artifactSummary({ checksum: "bad" })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      artifacts: [artifactSummary({ size_bytes: Number.MAX_SAFE_INTEGER + 1 })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      artifacts: [artifactSummary({ type: "archive" })],
    })));
    expectAuthorityViolation(() => decodeSnapshot(workerData(), terminalActions(), []));
    expectAuthorityViolation(() => decodeSnapshot(workerData(), terminalActions(), [
      ...artifactRefs(),
      ...artifactRefs(),
    ]));
    expectAuthorityViolation(() => decodeSnapshot(workerData(), terminalActions(), [{
      type: "artifact_detail",
      id: "artifact-foreign",
      href: "/api/v1/views/artifact_detail/artifact-foreign",
    }]));
  });

  it("validates approvals, scopes and exact approval action authority", () => {
    expectMalformed(() => decodeSnapshot(workerData({
      approvals: [approvalRequest({ revision: 0 })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      approvals: [approvalRequest({ scope_options: ["workspace"] })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      approvals: [approvalRequest({ scope_options: ["once", "once"] })],
    })));
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [
      ...terminalActions().slice(0, 3),
      { action: "worker.approval", target_id: "approval-foreign", expected_revision: 2 },
    ]));
  });

  it("admits one approval action for every distinct pending materializer request", () => {
    const secondApproval = approvalRequest({
      approval_request_id: "approval-2",
      revision: 3,
      capability: "network.request",
      target: "https://example.com",
      effect: "external_request",
      reason: "Fetch the approved primary source.",
    });
    const actions = [
      ...terminalActions(),
      { action: "worker.approval", target_id: "approval-2", expected_revision: 3 },
    ];

    const decoded = decodeSnapshot(workerData({
      approvals: [approvalRequest(), secondApproval],
    }), actions);
    expect(decoded.effective.data.approvals).toHaveLength(2);
    expect(decoded.effective.availableActions.filter((action) => (
      action.action === "worker.approval"
    ))).toEqual([
      expect.objectContaining({ target_id: "approval-1", expected_revision: 2 }),
      expect.objectContaining({ target_id: "approval-2", expected_revision: 3 }),
    ]);
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [
      ...terminalActions(),
      { action: "worker.approval", target_id: "approval-1", expected_revision: 2 },
    ]));
  });

  it("validates grouped timeline, durable parts and cursor/has-more consistency", () => {
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ status: "running" })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ checkpoint_version: Number.MAX_SAFE_INTEGER + 1 })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ backfill_status: "missing" })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ parts: [displayPart({ schema_version: "frontier.display_part.v2" })] })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ parts: [displayPart({ lifecycle_status: "unknown" })] })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_groups: [timelineTurn({ parts: [displayPart(), displayPart()] })],
    })));
    expectMalformed(() => decodeSnapshot(workerData({
      timeline_cursor: null,
      timeline_has_more: true,
    })));
  });

  it("validates waiting-user authority against Session and recovery state", () => {
    const waiting = {
      reason: "worker_waiting_user",
      message: "Worker needs input before it can continue.",
      allowed_actions: ["reply", "cancel"],
    };
    expect(() => decodeSnapshot(workerData({
      status: "waiting_user",
      status_tone: "waiting",
      waiting_user: waiting,
    }))).not.toThrow();
    expectMalformed(() => decodeSnapshot(workerData({ waiting_user: waiting })));
    expectMalformed(() => decodeSnapshot(workerData({
      status: "waiting_user",
      status_tone: "waiting",
      waiting_user: { ...waiting, allowed_actions: ["cancel", "reply"] },
    })));
  });
});

describe("WorkerDetail exact command authority", () => {
  it("admits standalone follow-up only for an observable terminal Run/Turn", () => {
    const data = workerData({
      task_status: null,
      task_status_label: null,
      task_revision: null,
      bound_task_id: null,
    });
    expect(() => decodeSnapshot(data, [{
      action: "worker.follow_up",
      target_id: WORKER_SESSION_ID,
      expected_revision: 7,
    }])).not.toThrow();
    expectAuthorityViolation(() => decodeSnapshot(
      { ...data, run_status: "running", turn_status: "consumed" },
      [{ action: "worker.follow_up", target_id: WORKER_SESSION_ID, expected_revision: 7 }],
    ));
  });

  it("does not confuse the visible fallback Task id with hidden Session binding", () => {
    expect(() => decodeSnapshot(workerData(), [{
      action: "worker.follow_up",
      target_id: WORKER_SESSION_ID,
      expected_revision: 7,
    }])).not.toThrow();
  });

  it("admits queue and interrupt only for the exact active Session/Run/Turn tuple", () => {
    const data = workerData({ run_status: "running", turn_status: "consumed" });
    const actions = [
      { action: "worker.queue", target_id: WORKER_SESSION_ID, expected_revision: 7 },
      { action: "worker.interrupt", target_id: "turn-1", expected_revision: 5 },
    ];
    expect(() => decodeSnapshot(data, actions)).not.toThrow();
    expectAuthorityViolation(() => decodeSnapshot(data, [
      actions[0],
      { ...actions[1], target_id: "turn-other" },
    ]));
    expectAuthorityViolation(() => decodeSnapshot(
      workerData({ run_status: "completed", turn_status: "completed" }),
      actions,
    ));
  });

  it("admits continue only for delivered Task plus terminal Run/Turn and exact metadata", () => {
    const action = terminalActions()[0];
    expect(() => decodeSnapshot(workerData(), [action])).not.toThrow();
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [{
      ...action,
      metadata: { interaction_kinds: ["chat"] },
    }]));
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [{
      ...action,
      metadata: { interaction_kinds: ["chat", "rework"], extra: true },
    }]));
    expectAuthorityViolation(() => decodeSnapshot(workerData({ task_status: "running", task_status_label: "Running" }), [action]));
  });

  it("admits accept/rework for delivered Task independently of Session terminality", () => {
    const actions = terminalActions().slice(1, 3);
    expect(() => decodeSnapshot(workerData({
      status: "closed",
      status_tone: "danger",
      run_status: "running",
      turn_status: "consumed",
    }), actions)).not.toThrow();
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [{
      ...actions[0],
      expected_revision: 4,
    }]));
    expectAuthorityViolation(() => decodeSnapshot(workerData({ task_status: "completed", task_status_label: "Accepted" }), actions));
  });

  it.each([
    ["unknown action", { action: "worker.send_prompt", target_id: WORKER_SESSION_ID, expected_revision: 7 }],
    ["foreign target", { action: "task.accept", target_id: "task-other", expected_revision: 3 }],
    ["stale revision", { action: "task.rework", target_id: "task-1", expected_revision: 2 }],
    ["forbidden metadata", { action: "worker.follow_up", target_id: WORKER_SESSION_ID, expected_revision: 7, metadata: {} }],
  ])("rejects %s", (_name, action) => {
    const data = action.action === "worker.follow_up"
      ? workerData({
          task_status: null,
          task_status_label: null,
          task_revision: null,
          bound_task_id: null,
        })
      : workerData();
    expectAuthorityViolation(() => decodeSnapshot(data, [action]));
  });

  it("rejects duplicate authority rows", () => {
    const action = terminalActions()[1];
    expectAuthorityViolation(() => decodeSnapshot(workerData(), [action, action]));
  });
});

describe("preserved sparse WorkerDetail legacy rows", () => {
  it("fails closed with the dedicated legacy reason and exposes no decoded authority", () => {
    const decoder = createWorkerViewDecoder("session-legacy-worker-detail");
    let thrown: unknown;
    try {
      decoder.decodeSnapshot(sparseLegacyWorkerSnapshot(), { requestedClientCommandIds: [] });
    } catch (error: unknown) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(ViewProjectionDecodeError);
    expect(thrown).toMatchObject({
      classification: "invalid_contract",
      safeReason: "legacy_worker_contract",
      ingress: "snapshot",
    });
    expect(thrown).not.toHaveProperty("effective");
  });

  it("does not misclassify an arbitrary malformed current row as the exact legacy shape", () => {
    const malformed = sparseLegacyWorkerSnapshot();
    const data = malformed.data as Record<string, unknown>;
    const groups = data.timeline_groups as Array<Record<string, unknown>>;
    delete groups[0].mode;
    const decoder = createWorkerViewDecoder("session-legacy-worker-detail");
    expect(() => decoder.decodeSnapshot(malformed, { requestedClientCommandIds: [] }))
      .toThrowError(expect.objectContaining({
        classification: "invalid_contract",
        safeReason: "authority_violation",
      }));
  });
});
