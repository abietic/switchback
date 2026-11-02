import { describe, expect, it } from "vitest";
import { productionViews, type AnyBoundProductionView } from "./productionViewRegistry";
import type { BoundViewProjectionRuntimeDecoder } from "./viewRuntime";

const checksum = `sha256:${"a".repeat(64)}`;

interface ViewCase {
  readonly kind: keyof typeof productionViews;
  readonly view: AnyBoundProductionView;
  readonly snapshot: Record<string, unknown>;
}

function envelope(viewType: string, viewId: string, data: unknown) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: viewType,
    view_id: viewId,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-29T00:00:00Z",
    data,
    available_actions: [],
    related_refs: [],
  };
}

function cases(): ViewCase[] {
  const home = productionViews.home();
  const taskIndex = productionViews.taskIndex();
  const assistant = productionViews.assistant("conversation-1");
  const worker = productionViews.worker("session-1");
  const artifact = productionViews.artifact("artifact-1");
  return [
    {
      kind: "home",
      view: home,
      snapshot: envelope(home.viewType, home.viewId, {
        attention_tasks: [],
        recent_workers: [],
        recent_deliveries: [],
        capacity: { used: 0, limit: 10 },
        updated_at: "2026-07-29T00:00:00Z",
      }),
    },
    {
      kind: "taskIndex",
      view: taskIndex,
      snapshot: envelope(taskIndex.viewType, taskIndex.viewId, {
        tasks: [],
        capacity: { used: 0, limit: 10 },
      }),
    },
    {
      kind: "assistant",
      view: assistant,
      snapshot: envelope(assistant.viewType, assistant.viewId, {
        conversation_id: "conversation-1",
        revision: 1,
        generation: 1,
        status: "active",
        workspace_id: null,
        updated_at: "2026-07-29T00:00:00Z",
        turns: [],
        assistant_run: null,
        readiness: null,
        task_draft: null,
        checkpoint: null,
        handoffs: [],
      }),
    },
    {
      kind: "worker",
      view: worker,
      snapshot: envelope(worker.viewType, worker.viewId, {
        worker_session_id: "session-1",
        session_revision: 1,
        workspace: "workspace-1",
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
        timeline_groups: [],
        timeline_cursor: null,
        timeline_has_more: false,
        artifacts: [],
        approvals: [],
        waiting_user: null,
        stage_report: null,
      }),
    },
    {
      kind: "artifact",
      view: artifact,
      snapshot: envelope(artifact.viewType, artifact.viewId, {
        artifact_id: "artifact-1",
        type: "report",
        label: "Stage report",
        size_bytes: 6,
        checksum,
        completeness: "complete",
        worker_run_id: "run-1",
        agent_turn_id: "turn-1",
        runtime_generation: 1,
        generated_at: "2026-07-29T00:00:00Z",
        availability: "online",
        content: "# Done",
        download_url: "/api/v1/artifacts/artifact-1/content",
        review_manifest: {
          schema_version: "artifact.review_manifest.v1",
          outcome: "online",
          renderable: true,
          download_allowed: true,
          media_type: "text/markdown",
          content_size_bytes: 6,
          diagnostics: [],
        },
      }),
    },
  ];
}

function delta(item: ViewCase, eventType: string) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: `${item.kind}-delta-2`,
    view_type: item.view.viewType,
    view_id: item.view.viewId,
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: eventType,
    critical: true,
    source_refs: [{ type: "integration_event", id: `event-${item.kind}-2` }],
    payload: {
      data: item.snapshot.data,
      available_actions: [],
      related_refs: [],
    },
  };
}

describe("closed production View registry", () => {
  it("exports exactly five semantic factories with explicit runtime versions", () => {
    expect(Object.keys(productionViews)).toEqual([
      "home",
      "taskIndex",
      "assistant",
      "worker",
      "artifact",
    ]);
    expect(cases().map(({ kind, view }) => ({
      kind: view.kind,
      runtimeVersion: view.runtimeVersion,
      contractKey: view.contractKey,
      viewType: view.viewType,
      viewId: view.viewId,
      factoryKind: kind,
    }))).toEqual([
      { kind: "home", runtimeVersion: "v1", contractKey: "home-dashboard-v1:index", viewType: "home_dashboard", viewId: "index", factoryKind: "home" },
      { kind: "taskIndex", runtimeVersion: "v1", contractKey: "task-index-v1:index", viewType: "task_summary", viewId: "index", factoryKind: "taskIndex" },
      { kind: "assistant", runtimeVersion: "v1", contractKey: "assistant-conversation-v1:conversation-1", viewType: "assistant_conversation", viewId: "conversation-1", factoryKind: "assistant" },
      { kind: "worker", runtimeVersion: "v1", contractKey: "worker-detail-v1:session-1", viewType: "worker_detail", viewId: "session-1", factoryKind: "worker" },
      { kind: "artifact", runtimeVersion: "v1", contractKey: "artifact-detail-v1:artifact-1", viewType: "artifact_detail", viewId: "artifact-1", factoryKind: "artifact" },
    ]);
  });

  it("decodes a current materializer Snapshot and exact-next Delta for every kind", () => {
    for (const item of cases()) {
      const decoder = item.view.decoder as unknown as BoundViewProjectionRuntimeDecoder<unknown>;
      const current = decoder.decodeSnapshot(item.snapshot, {
        requestedClientCommandIds: [],
      }).effective;
      const decision = decoder.decodeDeltaFrame({
        eventName: "projection_delta",
        lastEventId: "1:2",
        data: JSON.stringify(delta(item, "view.entity_upserted.v1")),
      }, current);
      expect(decision.result.kind, item.kind).toBe("apply");
    }
  });

  it("enforces the closed event cross-product", () => {
    const events = [
      "view.entity_upserted.v1",
      "view.stream_checkpointed.v1",
      "view.artifact_ready.v1",
      "view.command_receipted.v1",
    ];
    for (const item of cases()) {
      for (const eventType of events) {
        const decoder = item.view.decoder as unknown as BoundViewProjectionRuntimeDecoder<unknown>;
        const current = decoder.decodeSnapshot(item.snapshot, {
          requestedClientCommandIds: [],
        }).effective;
        const run = () => decoder.decodeDeltaFrame({
          eventName: "projection_delta",
          lastEventId: "1:2",
          data: JSON.stringify(delta(item, eventType)),
        }, current);
        const allowed = eventType === "view.entity_upserted.v1"
          || (item.kind === "worker" && eventType === "view.stream_checkpointed.v1")
          || (item.kind === "artifact" && eventType === "view.artifact_ready.v1");
        if (allowed) expect(run, `${item.kind}/${eventType}`).not.toThrow();
        else expect(run, `${item.kind}/${eventType}`).toThrow();
      }
    }
  });
});
