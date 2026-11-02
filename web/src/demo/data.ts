import type { ArtifactDetailData, ProjectionSnapshot, TaskListItem, WorkerDetailData } from "../api/contracts";

export const demoTasks: TaskListItem[] = [
  {
    task_id: "task-switchback-counter",
    title: "Repair counter boundary behavior",
    workspace: "switchback / main@a184c9d",
    status: "Delivered",
    status_tone: "waiting",
    worker_session_id: "session-local-01",
    updated_at: "2 min ago",
  },
  {
    task_id: "task-runtime-policy",
    title: "Verify Worker network policy",
    workspace: "switchback / runtime-spike",
    status: "Running",
    status_tone: "active",
    worker_session_id: "session-local-02",
    updated_at: "8 min ago",
  },
  {
    task_id: "task-readme",
    title: "Summarize local deployment path",
    workspace: "switchback / docs",
    status: "Completed",
    status_tone: "success",
    worker_session_id: "session-local-03",
    updated_at: "Yesterday",
  },
];

export const demoWorker: WorkerDetailData = {
  worker_session_id: "session-local-01",
  session_revision: 14,
  workspace: "switchback / main@a184c9d",
  status: "Waiting for review",
  status_tone: "waiting",
  task_status: "delivered",
  run_status: "completed",
  turn_status: "completed",
  bound_task_id: "task-switchback-counter",
  task_revision: 8,
  active_worker_run_id: "run-03",
  active_run_revision: 7,
  active_agent_turn_id: "turn-05",
  active_turn_revision: 3,
  runtime_health: "Healthy",
  runtime_generation: 1,
  agent_profile: "deepseek-codex-openai-v1",
  started_at: "2026-07-23T09:31:00Z",
  deadline_at: "2026-07-24T09:31:00Z",
  timeline_groups: [{
    worker_run_id: "run-03",
    agent_turn_id: "turn-05",
    display_stream_id: "display-turn-05",
    status: "completed",
    started_at: "2026-07-23T09:31:00Z",
    terminal_at: "2026-07-23T09:37:00Z",
    prompt: "Repair the counter and add a focused boundary test.",
    checkpoint_version: 4,
    checkpoint_complete: true,
    backfill_status: "online",
    parts: [
      {
        schema_version: "frontier.display_part.v1",
        part_id: "part-agent-05",
        part_type: "text",
        part_revision: 2,
        ordinal: 0,
        lifecycle_status: "completed",
        critical: false,
        payload: {
          format: "markdown",
          content: "## Result\n\nThe counter now applies positive deltas with addition. A focused boundary test passes.",
        },
        created_at: "2026-07-23T09:33:00Z",
        updated_at: "2026-07-23T09:37:00Z",
      },
      {
        schema_version: "frontier.display_part.v1",
        part_id: "part-tool-05",
        part_type: "tool",
        part_revision: 2,
        ordinal: 1,
        lifecycle_status: "completed",
        critical: false,
        payload: {
          title: "Run tests",
          arguments: { cwd: "/workspace/switchback" },
          output: "ok  switchback.local/counter  0.42s",
          duration_ms: 420,
          locations: [{ path: "counter/counter.go", line: 14 }],
        },
        created_at: "2026-07-23T09:35:00Z",
        updated_at: "2026-07-23T09:36:00Z",
      },
    ],
  }],
  artifacts: [
    {
      artifact_id: "artifact-report-01",
      type: "report",
      label: "Stage report",
      size_bytes: 1842,
      checksum: "sha256:6d143c9f2ac4…f920",
      completeness: "complete",
      worker_run_id: "run-03",
      agent_turn_id: "turn-05",
    },
    {
      artifact_id: "artifact-patch-01",
      type: "patch",
      label: "2 files changed",
      size_bytes: 963,
      checksum: "sha256:a4927e018e35…1fb4",
      completeness: "complete",
      worker_run_id: "run-03",
      agent_turn_id: "turn-05",
    },
  ],
  approvals: [],
  stage_report: "The counter now applies positive deltas with addition. A focused test covers the zero and positive boundary, and the repository test suite passes.",
};

export function demoWorkerSnapshot(workerSessionId: string): ProjectionSnapshot<WorkerDetailData> {
  const terminalTask = workerSessionId === "session-local-03";
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "worker_detail",
    view_id: workerSessionId,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 4,
    projection_version: 4,
    query_fingerprint: `worker-${workerSessionId}`,
    generated_at: "2026-07-23T09:37:00Z",
    data: {
      ...demoWorker,
      worker_session_id: workerSessionId,
      task_status: terminalTask ? "completed" : demoWorker.task_status,
      task_status_label: terminalTask ? "Accepted" : "Delivered",
      status: terminalTask ? "idle" : demoWorker.status,
      status_tone: terminalTask ? "neutral" : demoWorker.status_tone,
    },
    available_actions: terminalTask ? [
      { action: "worker.follow_up", target_id: workerSessionId, expected_revision: demoWorker.session_revision },
    ] : [
      {
        action: "task.continue", target_id: "task-switchback-counter", expected_revision: 8,
        metadata: { interaction_kinds: ["chat", "rework"] },
      },
      ...[
        { action: "task.accept", target_id: "task-switchback-counter", expected_revision: 8 },
        { action: "task.rework", target_id: "task-switchback-counter", expected_revision: 8 },
      ],
    ],
    related_refs: demoWorker.artifacts.map((artifact) => ({
      type: "artifact_detail",
      id: artifact.artifact_id,
      href: `/api/v1/views/artifact_detail/${artifact.artifact_id}`,
    })),
  };
}

export const demoArtifacts: Record<string, ArtifactDetailData> = {
  "artifact-report-01": {
    ...demoWorker.artifacts[0],
    availability: "online",
    generated_at: "2026-07-23T09:37:00Z",
    download_url: "/api/v1/artifacts/artifact-report-01/content",
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "online", renderable: true, download_allowed: true,
      media_type: "text/markdown", content_size_bytes: 131, diagnostics: [],
    },
    content: "## Result\n\nThe counter now applies positive deltas with addition.\n\n## Verification\n\n- `go test ./counter`\n- `go test ./...`",
  },
  "artifact-patch-01": {
    ...demoWorker.artifacts[1],
    availability: "online",
    generated_at: "2026-07-23T09:37:00Z",
    download_url: "/api/v1/artifacts/artifact-patch-01/content",
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "online", renderable: true, download_allowed: true,
      media_type: "text/x-diff", content_size_bytes: 278, diagnostics: [],
    },
    content: "diff --git a/counter/counter.go b/counter/counter.go\nindex 17a2..91df 100644\n--- a/counter/counter.go\n+++ b/counter/counter.go\n@@ -14,2 +14,2 @@ func Add(current, delta int) int {\n-    return current - delta\n+    return current + delta\n }\n",
  },
  "artifact-checksum-failed": {
    ...demoWorker.artifacts[0],
    artifact_id: "artifact-checksum-failed",
    availability: "checksum_failed",
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "checksum_failed", renderable: false, download_allowed: false,
      media_type: "text/markdown", content_size_bytes: 0,
      diagnostics: [{ code: "artifact_integrity_failed", message: "Artifact checksum verification failed." }],
    },
    content: "",
  },
};
