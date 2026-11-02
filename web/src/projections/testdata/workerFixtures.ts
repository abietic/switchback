import type { ProjectionSSEFrame } from "../viewRuntime";

export const WORKER_SESSION_ID = "session-1";

const digest = (value: string): string => `sha256:${value.repeat(64)}`;

export function workerData(overrides: Record<string, unknown> = {}) {
  return {
    worker_session_id: WORKER_SESSION_ID,
    session_revision: 7,
    workspace: "workspace-1",
    status: "interactive",
    status_tone: "active",
    task_status: "delivered",
    task_status_label: "Delivered",
    task_revision: 3,
    run_status: "completed",
    turn_status: "completed",
    bound_task_id: "task-1",
    active_worker_run_id: "run-1",
    active_run_revision: 4,
    active_agent_turn_id: "turn-1",
    active_turn_revision: 5,
    runtime_health: "Active",
    runtime_generation: 2,
    agent_profile: "codex-deepseek-v3.2",
    network_profile: {
      id: "trusted_full_network_v1",
      revision: 1,
      digest: digest("a"),
      enforcement_status: "active",
    },
    runtime_recovery: {
      status: "runtime_recovered",
      phase: "recovered",
      failed_generation: 1,
      replacement_generation: 2,
      attempt_count: 1,
      continuity_status: "proven",
      evidence_at: "2026-07-25T00:00:05.123456789Z",
      outcome_reason: null,
    },
    started_at: "2026-07-25T00:00:00Z",
    deadline_at: "2026-07-26T00:00:00Z",
    artifacts: [artifactSummary()],
    approvals: [approvalRequest()],
    timeline_groups: [timelineTurn()],
    timeline_cursor: null,
    timeline_has_more: false,
    waiting_user: null,
    stage_report: "# Stage report\n\nThe production fixture is complete.",
    ...overrides,
  };
}

export function artifactSummary(overrides: Record<string, unknown> = {}) {
  return {
    artifact_id: "artifact-1",
    type: "report",
    label: "Stage report",
    size_bytes: 128,
    checksum: digest("b"),
    completeness: "complete",
    worker_run_id: "run-1",
    agent_turn_id: "turn-1",
    runtime_generation: 2,
    generated_at: "2026-07-25T00:01:00Z",
    ...overrides,
  };
}

export function approvalRequest(overrides: Record<string, unknown> = {}) {
  return {
    approval_request_id: "approval-1",
    revision: 2,
    capability: "shell.execute",
    target: "make verify",
    effect: "execute_command",
    reason: "Run the repository verification gate.",
    expires_at: "2026-07-25T01:00:00Z",
    scope_options: ["once", "session"],
    ...overrides,
  };
}

export function timelineTurn(overrides: Record<string, unknown> = {}) {
  return {
    worker_run_id: "run-1",
    agent_turn_id: "turn-1",
    display_stream_id: "display-1",
    status: "completed",
    started_at: "2026-07-25T00:00:00Z",
    terminal_at: "2026-07-25T00:00:30Z",
    prompt: "Implement the production projection decoder.",
    parts: [displayPart()],
    checkpoint_version: 3,
    checkpoint_complete: true,
    backfill_status: "online",
    latest_checkpointed_at: "2026-07-25T00:00:31Z",
    projection_lag_millis: 1000,
    ...overrides,
  };
}

export function displayPart(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.display_part.v1",
    part_id: "part-1",
    part_type: "text",
    part_revision: 2,
    ordinal: 0,
    lifecycle_status: "completed",
    critical: false,
    visibility: { audience: "user" },
    provenance: {
      source: "acp",
      runtime_generation: 2,
      agent_session_id: "agent-session-1",
    },
    available_actions: [],
    payload: { format: "markdown", content: "Done." },
    created_at: "2026-07-25T00:00:01Z",
    updated_at: "2026-07-25T00:00:29Z",
    ...overrides,
  };
}

export function terminalActions() {
  return [
    {
      action: "task.continue",
      target_id: "task-1",
      expected_revision: 3,
      metadata: { interaction_kinds: ["chat", "rework"] },
    },
    { action: "task.accept", target_id: "task-1", expected_revision: 3 },
    { action: "task.rework", target_id: "task-1", expected_revision: 3 },
    { action: "worker.approval", target_id: "approval-1", expected_revision: 2 },
  ];
}

export function artifactRefs() {
  return [{
    type: "artifact_detail",
    id: "artifact-1",
    href: "/api/v1/views/artifact_detail/artifact-1",
  }];
}

export function workerSnapshot(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "worker_detail",
    view_id: WORKER_SESSION_ID,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-25T00:01:00Z",
    data: workerData(),
    available_actions: terminalActions(),
    related_refs: artifactRefs(),
    ...overrides,
  };
}

export function workerDelta(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "worker-delta-2",
    view_type: "worker_detail",
    view_id: WORKER_SESSION_ID,
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: "view.stream_checkpointed.v1",
    critical: true,
    source_refs: [{ type: "display_checkpoint", id: "checkpoint-2" }],
    payload: {
      data: workerData({ runtime_health: "Suspect" }),
      available_actions: terminalActions(),
      related_refs: artifactRefs(),
    },
    ...overrides,
  };
}

export function workerFrame(value: unknown, cursor = 2): ProjectionSSEFrame {
  return {
    eventName: "projection_delta",
    lastEventId: `1:${cursor}`,
    data: JSON.stringify(value),
  };
}

export function sparseLegacyWorkerSnapshot() {
  return workerSnapshot({
    data: {
      worker_session_id: "session-legacy-worker-detail",
      timeline_groups: [{
        worker_run_id: "run-legacy",
        agent_turn_id: "turn-legacy",
        mode: "prompt",
        status: "completed",
        started_at: "2026-07-29T10:00:00Z",
        terminal_at: "2026-07-29T10:01:00Z",
        input: { text: "keep grouped history" },
        updates: [{ kind: "agent_message", body: { text: "grouped response" } }],
      }],
      timeline_cursor: "worker-timeline-v1:legacy",
      timeline_has_more: true,
      unrelated_contract: { preserved: true },
    },
    view_id: "session-legacy-worker-detail",
    available_actions: [{
      action: "worker.send_prompt",
      target_id: "session-legacy-worker-detail",
      expected_revision: 7,
    }],
    related_refs: [{
      type: "task",
      id: "task-legacy-worker-detail",
      href: "/api/v1/tasks/task-legacy-worker-detail",
    }],
  });
}
