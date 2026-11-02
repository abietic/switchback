import { describe, expect, it } from "vitest";
import type { ProjectionSSEFrame } from "../viewRuntime";
import { createAssistantViewDecoder } from "./assistant";

const CONVERSATION_ID = "conversation-1";

function readinessDimensions() {
  return Object.fromEntries([
    "goal",
    "deliverable_acceptance",
    "scope_target",
    "constraints",
    "context_access",
    "risk_approval",
    "execution_policy",
  ].map((dimension) => [dimension, {
    status: "known",
    summary: `${dimension} is known`,
    evidence_refs: [],
  }]));
}

function taskSpec() {
  return {
    title: "Draft",
    goal: "Deliver the draft.",
    workspace_id: "workspace-1",
    workspace_resolution_ref: {
      resolution_id: "resolution-1",
      revision: 1,
      workspace_revision_id: "workspace-revision-1",
      membership_revision: 1,
    },
    readiness_assessment_ref: {
      assessment_id: "assessment-1",
      policy_revision: "assistant-readiness-v1",
      content_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    },
    creation_intent_ref: {
      intent_id: "pending-confirmation",
      task_spec_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      trigger_policy_revision: "assistant-trigger-v1",
    },
    target: { kind: "workspace", ref: "workspace-1" },
    constraints: [],
    acceptance_criteria: ["The draft is delivered."],
    assumptions: [{
      rule_id: "assistant-default/1",
      description: "Use the existing repository conventions.",
    }],
    agent_config: {
      profile_id: "codex-deepseek-v3.2",
      profile_revision: "1",
      adapter: "codex-acp",
    },
    runtime_config: {
      network_policy: "trusted_full_network_v1",
      network_policy_revision: 1,
      network_policy_digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    },
    additive_task_spec_field: { accepted: true },
  };
}

function conversationData() {
  return {
    conversation_id: CONVERSATION_ID,
    revision: 7,
    generation: 2,
    status: "active",
    workspace_id: "workspace-1",
    updated_at: "2026-07-25T00:00:00Z",
    turns: [
      {
        turn_id: "turn-2",
        sequence: 2,
        role: "assistant",
        content: "Two",
        created_at: "2026-07-25T00:00:00.123456789Z",
      },
      {
        turn_id: "turn-1",
        sequence: 1,
        role: "user",
        content: "One",
        created_at: "2026-07-25T00:00:00Z",
      },
    ],
    assistant_run: {
      assistant_run_id: "run-1",
      status: "failed",
      revision: 3,
      error_code: null,
      retryable: true,
    },
    readiness: {
      assessment_id: "assessment-1",
      revision: 5,
      status: "ready_confirmed",
      clarification_questions: 0,
      blocking_unknowns: [],
      assumptions: [],
      dimensions: readinessDimensions(),
    },
    task_draft: {
      task_draft_id: "draft-1",
      revision: 4,
      status: "current",
      assumptions: [],
      task_spec: taskSpec(),
    },
    checkpoint: {
      checkpoint_id: "checkpoint-1",
      revision: 2,
      status: "active",
      working_draft: {},
      allowed_actions: ["continue", "narrow_scope", "cancel"],
    },
    handoffs: [
      { task_id: "task-1", status: "delivered", worker_session_id: "worker-shared" },
      { task_id: "task-2", status: "future_additive_status", worker_session_id: "worker-shared" },
    ],
    additive_conversation_field: true,
  };
}

function actions() {
  return [
    { action: "assistant.send_message", target_id: CONVERSATION_ID, expected_revision: 7 },
    { action: "assistant.confirm_task", target_id: "draft-1", expected_revision: 4 },
    { action: "assistant.checkpoint", target_id: "checkpoint-1", expected_revision: 2 },
    { action: "assistant.retry_run", target_id: "run-1", expected_revision: 3 },
  ];
}

function refs() {
  return [
    { type: "task_summary", id: "task-1", href: "/api/v1/views/task_summary/task-1" },
    { type: "worker_detail", id: "worker-shared", href: "/api/v1/views/worker_detail/worker-shared" },
    { type: "task_summary", id: "task-2", href: "/api/v1/views/task_summary/task-2" },
    { type: "worker_detail", id: "worker-shared", href: "/api/v1/views/worker_detail/worker-shared" },
  ];
}

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "assistant_conversation",
    view_id: CONVERSATION_ID,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-25T00:00:00Z",
    data: conversationData(),
    available_actions: actions(),
    related_refs: refs(),
    ...overrides,
  };
}

function delta(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "assistant-delta-2",
    view_type: "assistant_conversation",
    view_id: CONVERSATION_ID,
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [{ type: "integration_event", id: "event-assistant-2" }],
    payload: {
      data: conversationData(),
      available_actions: actions(),
      related_refs: refs(),
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
  const decoder = createAssistantViewDecoder(CONVERSATION_ID);
  const current = decoder.decodeSnapshot(snapshot(), { requestedClientCommandIds: [] }).effective;
  return decoder.decodeDeltaFrame(frame(value), current);
}

describe("Assistant production view contract", () => {
  it("decodes the real materializer shape, sorts turns and preserves current profile evidence", () => {
    const decoder = createAssistantViewDecoder(CONVERSATION_ID);
    const decoded = decoder.decodeSnapshot(snapshot({ additive_envelope: true }), {
      requestedClientCommandIds: [],
    });
    expect(decoder.viewType).toBe("assistant_conversation");
    expect(decoder.viewId).toBe(CONVERSATION_ID);
    expect(decoded.effective.data.turns.map((turn) => turn.turn_id)).toEqual(["turn-1", "turn-2"]);
    expect(decoded.effective.data.readiness?.dimensions).toHaveLength(7);
    expect(decoded.effective.data.task_draft?.task_spec.runtime_config).toEqual(
      taskSpec().runtime_config,
    );
    expect(decoded.effective.data.handoffs[1]?.status).toBe("future_additive_status");
    expect(decoded.envelope).not.toHaveProperty("command_receipts");
  });

  it("accepts immutable legacy network profile evidence", () => {
    const data = structuredClone(conversationData());
    const runtime = data.task_draft.task_spec.runtime_config as Record<string, unknown>;
    runtime.network_policy = "m1_public_web_restricted_v1";
    delete runtime.network_policy_revision;
    delete runtime.network_policy_digest;
    const decoded = createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data }), {
      requestedClientCommandIds: [],
    });
    expect(decoded.effective.data.task_draft?.task_spec.runtime_config).toEqual({
      network_policy: "m1_public_web_restricted_v1",
    });

    const explicitUndefined = structuredClone(data);
    (explicitUndefined.task_draft.task_spec.runtime_config as Record<string, unknown>)
      .network_policy_revision = undefined;
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: explicitUndefined,
    }), { requestedClientCommandIds: [] })).toThrow();
  });

  it("accepts data-only, action-only and permission-filtered ref-only exact-next updates", () => {
    expect(decodeDelta(delta({ payload: { data: conversationData() } })).result.kind).toBe("apply");
    expect(decodeDelta(delta({ payload: { available_actions: actions() } })).result.kind).toBe("apply");
    expect(decodeDelta(delta({ payload: { related_refs: refs().slice(0, 2) } })).result.kind).toBe("apply");
  });

  it("admits only present actions with exact decoded authority and no metadata", () => {
    const decoder = createAssistantViewDecoder(CONVERSATION_ID);
    for (let omitted = 0; omitted < actions().length; omitted += 1) {
      const filtered = actions().filter((_, index) => index !== omitted);
      expect(decoder.decodeSnapshot(snapshot({ available_actions: filtered }), {
        requestedClientCommandIds: [],
      }).effective.availableActions).toHaveLength(3);
    }

    for (const invalidAction of [
      { action: "assistant.unknown", target_id: CONVERSATION_ID, expected_revision: 7 },
      { action: "assistant.send_message", target_id: "conversation-other", expected_revision: 7 },
      { action: "assistant.confirm_task", target_id: "draft-1", expected_revision: 5 },
      { action: "assistant.checkpoint", target_id: "checkpoint-1", expected_revision: 2, metadata: {} },
    ]) {
      expect(() => decoder.decodeSnapshot(snapshot({ available_actions: [invalidAction] }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
    expect(() => decoder.decodeSnapshot(snapshot({
      available_actions: [actions()[0], actions()[0]],
    }), { requestedClientCommandIds: [] })).toThrow();
  });

  it("rejects actions whose eligibility is not proved by the effective data", () => {
    const decoder = createAssistantViewDecoder(CONVERSATION_ID);
    const cases: Array<[Record<string, unknown>, unknown]> = [
      [{ status: "closed" }, actions()[0]],
      [{ readiness: { ...conversationData().readiness, status: "clarifying" } }, actions()[1]],
      [{ checkpoint: { ...conversationData().checkpoint, status: "resolved" } }, actions()[2]],
      [{ assistant_run: { ...conversationData().assistant_run, retryable: false } }, actions()[3]],
    ];
    for (const [dataOverride, action] of cases) {
      expect(() => decoder.decodeSnapshot(snapshot({
        data: { ...conversationData(), ...dataOverride },
        available_actions: [action],
      }), { requestedClientCommandIds: [] })).toThrow();
    }

    // A data-only Delta cannot retain authority made stale by its new data.
    expect(() => decodeDelta(delta({
      payload: { data: { ...conversationData(), status: "closed" } },
    }))).toThrow();
  });

  it("allows filtered Task/Worker refs but never more occurrences than handoffs prove", () => {
    const decoder = createAssistantViewDecoder(CONVERSATION_ID);
    for (const related_refs of [
      [],
      [refs()[0]],
      [refs()[1]],
      [refs()[1], refs()[3]],
    ]) {
      expect(decoder.decodeSnapshot(snapshot({ related_refs }), {
        requestedClientCommandIds: [],
      }).effective.relatedRefs).toHaveLength(related_refs.length);
    }

    for (const related_refs of [
      [refs()[1], refs()[3], refs()[1]],
      [{ type: "task_summary", id: "task-other", href: "/api/v1/views/task_summary/task-other" }],
      [{ type: "worker_detail", id: "worker-other", href: "/api/v1/views/worker_detail/worker-other" }],
    ]) {
      expect(() => decoder.decodeSnapshot(snapshot({ related_refs }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
  });

  it("normalizes only the materializer absent/null whitelist", () => {
    const data = conversationData() as unknown as Record<string, unknown>;
    data.workspace_id = null;
    data.assistant_run = null;
    data.readiness = null;
    data.task_draft = null;
    data.checkpoint = null;
    data.handoffs = [{ task_id: "task-1", status: "queued", worker_session_id: null }];
    const decoded = createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data,
      available_actions: [{
        action: "assistant.send_message",
        target_id: CONVERSATION_ID,
        expected_revision: 7,
      }],
      related_refs: [],
    }), { requestedClientCommandIds: [] });
    expect(decoded.effective.data.workspace_id).toBeUndefined();
    expect(decoded.effective.data.assistant_run).toBeUndefined();
    expect(decoded.effective.data.handoffs[0]?.worker_session_id).toBeUndefined();

    const run = conversationData();
    run.assistant_run.error_code = null;
    expect(createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data: run }), {
      requestedClientCommandIds: [],
    }).effective.data.assistant_run?.error_code).toBeUndefined();

    const invalidErrorCode = structuredClone(conversationData());
    (invalidErrorCode.assistant_run as Record<string, unknown>).error_code = false;
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: invalidErrorCode,
    }), { requestedClientCommandIds: [] })).toThrow();

    const invalidHandoff = structuredClone(conversationData());
    (invalidHandoff.handoffs[0] as Record<string, unknown>).worker_session_id = 0;
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: invalidHandoff,
    }), { requestedClientCommandIds: [] })).toThrow();
  });

  it.each([
    ["workspace_id", false],
    ["assistant_run", false],
    ["readiness", 0],
    ["task_draft", {}],
    ["checkpoint", false],
  ])("does not treat %s=%j as absent", (field, value) => {
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: { ...conversationData(), [field]: value },
    }), { requestedClientCommandIds: [] })).toThrow();
  });

  it("rejects unsafe revisions/sequences, invalid timestamps and non-canonical identities", () => {
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      view_id: "conversation-other",
    }), { requestedClientCommandIds: [] })).toThrow();
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: { ...conversationData(), conversation_id: "conversation-other" },
    }), { requestedClientCommandIds: [] })).toThrow();
    for (const [field, value] of [
      ["revision", Number.MAX_SAFE_INTEGER + 1],
      ["generation", 1.5],
    ] as const) {
      expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
        data: { ...conversationData(), [field]: value },
      }), { requestedClientCommandIds: [] })).toThrow();
    }

    const sequence = structuredClone(conversationData());
    sequence.turns[0]!.sequence = Number.MAX_SAFE_INTEGER + 1;
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data: sequence }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const time = structuredClone(conversationData());
    time.turns[0]!.created_at = "2026-07-25T08:00:00+08:00";
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data: time }), {
      requestedClientCommandIds: [],
    })).toThrow();

    const identity = structuredClone(conversationData());
    identity.handoffs[0]!.task_id = " task-1";
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data: identity }), {
      requestedClientCommandIds: [],
    })).toThrow();
  });

  it("requires exactly seven readiness dimensions and closed checkpoint actions", () => {
    const missingDimension = structuredClone(conversationData());
    delete (missingDimension.readiness.dimensions as Record<string, unknown>).risk_approval;
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({
      data: missingDimension,
    }), { requestedClientCommandIds: [] })).toThrow();

    const checkpoint = structuredClone(conversationData());
    checkpoint.checkpoint.allowed_actions = ["continue", "invented"];
    expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data: checkpoint }), {
      requestedClientCommandIds: [],
    })).toThrow();
  });

  it("requires canonical TaskSpec evidence digests", () => {
    for (const mutate of [
      (spec: ReturnType<typeof taskSpec>) => {
        spec.readiness_assessment_ref.content_digest = "sha256:ABC";
      },
      (spec: ReturnType<typeof taskSpec>) => {
        spec.creation_intent_ref.task_spec_digest = "not-a-digest";
      },
    ]) {
      const data = structuredClone(conversationData());
      mutate(data.task_draft.task_spec);
      expect(() => createAssistantViewDecoder(CONVERSATION_ID).decodeSnapshot(snapshot({ data }), {
        requestedClientCommandIds: [],
      })).toThrow();
    }
  });

  it("rejects unsupported Delta events", () => {
    expect(() => decodeDelta(delta({ event_type: "view.stream_checkpointed.v1" }))).toThrow();
  });
});
