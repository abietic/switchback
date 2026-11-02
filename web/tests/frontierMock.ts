import type { Page } from "@playwright/test";
import type {
  ArtifactDetailData,
  ArtifactSummary,
  ProjectionSnapshot,
  RelatedRef,
  TaskIndexData,
  WorkerDetailData,
} from "../src/api/contracts";
import type { HomeDashboardData } from "../src/api/homeContracts";
import { demoArtifacts, demoTasks, demoWorker } from "../src/demo/data";
import {
  workerData,
  workerSnapshot,
} from "../src/projections/testdata/workerFixtures";

export const demoHomeDashboard: HomeDashboardData = {
  attention_tasks: [{
    task_id: "task-home-waiting",
    title: "Confirm the Home dashboard contract",
    workspace: "workspace-local",
    status: "waiting_user",
    status_tone: "waiting",
    worker_session_id: "worker-home-idle",
    updated_at: "2026-07-29T00:00:00Z",
  }],
  recent_workers: [{
    worker_session_id: "worker-home-idle",
    workspace: "workspace-local",
    status: "idle",
    status_tone: "waiting",
    runtime_generation: 2,
    bound_task_id: "task-home-waiting",
    task_title: "Confirm the Home dashboard contract",
    updated_at: "2026-07-29T00:00:00Z",
  }],
  recent_deliveries: [{
    task_id: "task-home-delivered",
    title: "Publish Home dashboard evidence",
    workspace: "workspace-local",
    status: "delivered",
    status_tone: "waiting",
    acceptance_mode: "manual",
    worker_session_id: "worker-home-idle",
    updated_at: "2026-07-29T00:00:00Z",
    artifacts: [{
      artifact_id: "artifact-home-report",
      artifact_type: "report",
      href: "/api/v1/views/artifact_detail/artifact-home-report",
    }],
  }],
  capacity: { used: 3, limit: 10 },
  updated_at: "2026-07-29T00:00:00Z",
};

const homeArtifactContent = "## Home delivery report\n\nThe Home projection links to this verified delivery report.";

const demoHomeArtifact: ArtifactDetailData = {
  artifact_id: "artifact-home-report",
  type: "report",
  label: "Home delivery report",
  size_bytes: homeArtifactContent.length,
  checksum: `sha256:${"b".repeat(64)}`,
  completeness: "complete",
  worker_run_id: "run-home-delivery",
  agent_turn_id: "turn-home-delivery",
  runtime_generation: 2,
  generated_at: "2026-07-29T00:00:00Z",
  availability: "online",
  download_url: "/api/v1/artifacts/artifact-home-report/content",
  review_manifest: {
    schema_version: "artifact.review_manifest.v1",
    outcome: "online",
    renderable: true,
    download_allowed: true,
    media_type: "text/markdown",
    content_size_bytes: homeArtifactContent.length,
    diagnostics: [],
  },
  content: homeArtifactContent,
};

const demoHomeWorker: WorkerDetailData = {
  worker_session_id: "worker-home-idle",
  session_revision: 4,
  workspace: "workspace-local",
  status: "idle",
  status_tone: "waiting",
  task_status: "waiting_user",
  task_status_label: "Waiting User",
  bound_task_id: "task-home-waiting",
  task_revision: 3,
  runtime_health: "Healthy",
  runtime_generation: 2,
  agent_profile: "codex-deepseek-v3.2",
  artifacts: [artifactSummary(demoHomeArtifact)],
  approvals: [],
};

export async function mockFrontier(page: Page) {
  await page.route("**/api/v1/views/**", async (route) => {
    const path = new URL(route.request().url()).pathname.split("/").filter(Boolean);
    const viewType = decodeURIComponent(path[3] ?? "");
    const viewId = decodeURIComponent(path[4] ?? "");
    if (path[5] === "events") {
      return route.fulfill({
        status: 200,
        headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" },
        body: ": connected\n\n",
      });
    }
    const snapshot = snapshotFor(viewType, viewId);
    if (!snapshot) return route.fulfill({ status: 404, contentType: "application/json", body: "{}" });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(snapshot) });
  });
}

export interface FrontierEventHarness {
  disconnectLatest(): Promise<void>;
  emitLatest(type: string, data: unknown): Promise<void>;
  inspectSources(): Promise<readonly FrontierEventSourceInspection[]>;
  openLatest(): Promise<void>;
  setAutoOpen(value: boolean): Promise<void>;
}

export interface FrontierEventSourceInspection {
  readonly readyState: number;
  readonly url: string;
}

type HarnessOperation =
  | { action: "disconnect" }
  | { action: "emit"; type: string; data: unknown }
  | { action: "open" }
  | { action: "set-auto-open"; value: boolean };

export async function installFrontierEventHarness(
  page: Page,
  options: { autoOpen?: boolean } = {},
): Promise<FrontierEventHarness> {
  await page.addInitScript(({ autoOpen }) => {
    type HarnessSource = EventTarget & {
      close(): void;
      emit(type: string, data: unknown): void;
      fail(): void;
      open(): void;
      onerror: ((event: Event) => void) | null;
      onmessage: ((event: MessageEvent) => void) | null;
      onopen: ((event: Event) => void) | null;
      readyState: number;
      url: string;
      withCredentials: boolean;
    };
    type BrowserHarness = {
      autoOpen: boolean;
      sources: HarnessSource[];
    };

    const browserWindow = window as unknown as Window & {
      __switchbackFrontierHarness?: BrowserHarness;
    };
    const harness: BrowserHarness = {
      autoOpen,
      sources: [],
    };

    class ControlledEventSource extends EventTarget {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSED = 2;

      readonly url: string;
      readonly withCredentials = false;
      readyState = ControlledEventSource.CONNECTING;
      onopen: ((event: Event) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;

      constructor(url: string | URL) {
        super();
        this.url = String(url);
        harness.sources.push(this);
        if (harness.autoOpen) setTimeout(() => this.open(), 0);
      }

      close() {
        this.readyState = ControlledEventSource.CLOSED;
      }

      emit(type: string, data: unknown) {
        if (this.readyState !== ControlledEventSource.OPEN) return;
        const record = data && typeof data === "object" && !Array.isArray(data)
          ? data as Record<string, unknown>
          : undefined;
        const cursor = record?.view_cursor ?? record?.snapshot_cursor;
        const lastEventId = Number.isSafeInteger(record?.view_epoch)
          && Number.isSafeInteger(cursor)
          ? `${record?.view_epoch}:${cursor}`
          : "";
        const event = new MessageEvent(type, {
          data: JSON.stringify(data),
          lastEventId,
        });
        this.dispatchEvent(event);
      }

      fail() {
        if (this.readyState === ControlledEventSource.CLOSED) return;
        this.readyState = ControlledEventSource.CONNECTING;
        this.onerror?.(new Event("error"));
      }

      open() {
        if (this.readyState === ControlledEventSource.CLOSED) return;
        this.readyState = ControlledEventSource.OPEN;
        this.onopen?.(new Event("open"));
      }
    }

    browserWindow.__switchbackFrontierHarness = harness;
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: ControlledEventSource,
    });
  }, { autoOpen: options.autoOpen ?? true });

  return {
    disconnectLatest: () => operateHarness(page, { action: "disconnect" }),
    emitLatest: (type, data) => operateHarness(page, { action: "emit", type, data }),
    inspectSources: () => inspectHarnessSources(page),
    openLatest: () => operateHarness(page, { action: "open" }),
    setAutoOpen: (value) => operateHarness(page, { action: "set-auto-open", value }),
  };
}

async function inspectHarnessSources(page: Page): Promise<readonly FrontierEventSourceInspection[]> {
  return page.evaluate(() => {
    const browserWindow = window as unknown as Window & {
      __switchbackFrontierHarness?: {
        sources: Array<{ readyState: number; url: string }>;
      };
    };
    const harness = browserWindow.__switchbackFrontierHarness;
    if (!harness) throw new Error("Frontier event harness is not installed");
    return harness.sources.map(({ readyState, url }) => ({ readyState, url }));
  });
}

async function operateHarness(page: Page, operation: HarnessOperation): Promise<void> {
  await page.evaluate((input) => {
    type HarnessSource = {
      emit(type: string, data: unknown): void;
      fail(): void;
      open(): void;
    };
    const browserWindow = window as unknown as Window & {
      __switchbackFrontierHarness?: {
        autoOpen: boolean;
        sources: HarnessSource[];
      };
    };
    const harness = browserWindow.__switchbackFrontierHarness;
    if (!harness) throw new Error("Frontier event harness is not installed");
    if (input.action === "set-auto-open") {
      harness.autoOpen = input.value;
      return;
    }
    const source = harness.sources.at(-1);
    if (!source) throw new Error("Frontier event stream has not connected");
    if (input.action === "disconnect") source.fail();
    else if (input.action === "open") source.open();
    else source.emit(input.type, input.data);
  }, operation);
}

export function snapshotFor(viewType: string, viewId: string): ProjectionSnapshot<unknown> | undefined {
  if (viewType === "home_dashboard" && viewId === "index") {
    return homeSnapshot();
  }
  if (viewType === "task_summary" && viewId === "index") {
    return baseSnapshot<TaskIndexData>(viewType, viewId, {
      tasks: demoTasks.map((task, index) => {
        const status = index === 0 ? "delivered" : index === 1 ? "running" : "completed";
        return {
          ...task,
          status,
          status_tone: status === "running" ? "active" : status === "completed" ? "success" : "waiting",
          updated_at: `2026-07-${String(25 - index).padStart(2, "0")}T00:00:00Z`,
        };
      }),
      capacity: { used: 3, limit: 10 },
    });
  }
  if (viewType === "worker_detail") {
    if (viewId === demoHomeWorker.worker_session_id) return homeWorkerSnapshot();
    if (["session-local-01", "session-local-02", "session-local-03"].includes(viewId)) {
      return productionWorkerSnapshot(viewId) as ProjectionSnapshot<unknown>;
    }
    return undefined;
  }
  if (viewType === "assistant_conversation" && viewId === "conversation-browser") {
    return {
      ...baseSnapshot<unknown>(viewType, viewId, demoAssistantProjection()),
      available_actions: [
        { action: "assistant.send_message", target_id: viewId, expected_revision: 8 },
        { action: "assistant.confirm_task", target_id: "draft-browser", expected_revision: 3 },
      ],
      related_refs: [
        { type: "task_summary", id: "task-switchback-counter", href: "/api/v1/views/task_summary/task-switchback-counter" },
        { type: "worker_detail", id: "session-local-01", href: "/api/v1/views/worker_detail/session-local-01" },
      ],
    };
  }
  if (viewType === "artifact_detail") {
    const artifact = projectedArtifact(viewId);
    if (!artifact) return undefined;
    return {
      ...baseSnapshot<ArtifactDetailData>(viewType, viewId, artifact),
      available_actions: artifact.review_manifest?.download_allowed ? [{
        action: "artifact.download",
        target_id: viewId,
        expected_revision: 1,
      }] : [],
      related_refs: [],
    };
  }
  return undefined;
}

export function productionWorkerSnapshot(viewId: string): ProjectionSnapshot<WorkerDetailData> {
  const terminalTask = viewId === "session-local-03";
  const artifacts = demoWorker.artifacts
    .map((artifact) => projectedArtifact(artifact.artifact_id))
    .filter((artifact): artifact is ArtifactDetailData => artifact !== undefined)
    .map(artifactSummary);
  const data = workerData({
    worker_session_id: viewId,
    session_revision: demoWorker.session_revision,
    workspace: demoWorker.workspace,
    status: "interactive",
    status_tone: "active",
    task_status: terminalTask ? "completed" : "delivered",
    task_status_label: terminalTask ? "Accepted" : "Delivered",
    task_revision: demoWorker.task_revision,
    bound_task_id: demoWorker.bound_task_id,
    active_worker_run_id: demoWorker.active_worker_run_id,
    active_run_revision: demoWorker.active_run_revision,
    active_agent_turn_id: demoWorker.active_agent_turn_id,
    active_turn_revision: demoWorker.active_turn_revision,
    runtime_health: demoWorker.runtime_health,
    runtime_generation: demoWorker.runtime_generation,
    agent_profile: demoWorker.agent_profile,
    started_at: demoWorker.started_at,
    deadline_at: demoWorker.deadline_at,
    artifacts,
    approvals: [],
    timeline_groups: [],
    timeline_cursor: null,
    timeline_has_more: false,
    waiting_user: null,
    stage_report: demoWorker.stage_report,
  });
  const availableActions = terminalTask
    ? [{
        action: "worker.follow_up",
        target_id: viewId,
        expected_revision: demoWorker.session_revision,
      }]
    : [
        {
          action: "task.continue",
          target_id: demoWorker.bound_task_id,
          expected_revision: demoWorker.task_revision,
          metadata: { interaction_kinds: ["chat", "rework"] },
        },
        {
          action: "task.accept",
          target_id: demoWorker.bound_task_id,
          expected_revision: demoWorker.task_revision,
        },
        {
          action: "task.rework",
          target_id: demoWorker.bound_task_id,
          expected_revision: demoWorker.task_revision,
        },
      ];
  return workerSnapshot({
    view_id: viewId,
    view_cursor: 4,
    projection_version: 4,
    data,
    available_actions: availableActions,
    related_refs: artifacts.map((artifact) => ({
      type: "artifact_detail",
      id: artifact.artifact_id,
      href: `/api/v1/views/artifact_detail/${artifact.artifact_id}`,
    })),
  }) as unknown as ProjectionSnapshot<WorkerDetailData>;
}

function projectedArtifact(viewId: string): ArtifactDetailData | undefined {
  if (viewId === demoHomeArtifact.artifact_id) return demoHomeArtifact;
  const artifact = demoArtifacts[viewId];
  if (!artifact) return undefined;
  return {
    ...artifact,
    checksum: `sha256:${"a".repeat(64)}`,
    runtime_generation: artifact.runtime_generation ?? 1,
    generated_at: artifact.generated_at ?? "2026-07-23T09:37:00Z",
  };
}

function artifactSummary(artifact: ArtifactDetailData): ArtifactSummary {
  const {
    artifact_id,
    type,
    label,
    size_bytes,
    checksum,
    completeness,
    worker_run_id,
    agent_turn_id,
    runtime_generation,
    generated_at,
  } = artifact;
  return {
    artifact_id,
    type,
    label,
    size_bytes,
    checksum,
    completeness,
    worker_run_id,
    agent_turn_id,
    runtime_generation,
    generated_at,
  };
}

function homeWorkerSnapshot(): ProjectionSnapshot<WorkerDetailData> {
  const data = workerData({
    ...demoHomeWorker,
    run_status: "completed",
    turn_status: "completed",
    active_worker_run_id: "run-home-delivery",
    active_run_revision: 1,
    active_agent_turn_id: "turn-home-delivery",
    active_turn_revision: 1,
    started_at: "2026-07-29T00:00:00Z",
    deadline_at: "2026-07-30T00:00:00Z",
    runtime_recovery: null,
    timeline_groups: [],
    timeline_cursor: null,
    timeline_has_more: false,
    waiting_user: null,
    stage_report: null,
  }) as unknown as WorkerDetailData;
  return {
    ...baseSnapshot<WorkerDetailData>(
      "worker_detail",
      demoHomeWorker.worker_session_id,
      data,
    ),
    view_cursor: 2,
    projection_version: 2,
    available_actions: [],
    related_refs: [{
      type: "artifact_detail",
      id: demoHomeArtifact.artifact_id,
      href: `/api/v1/views/artifact_detail/${demoHomeArtifact.artifact_id}`,
    }],
  };
}

export function homeSnapshot(data: HomeDashboardData = demoHomeDashboard): ProjectionSnapshot<HomeDashboardData> {
  return {
    ...baseSnapshot<HomeDashboardData>("home_dashboard", "index", data),
    related_refs: homeRelatedRefs(data),
  };
}

function homeRelatedRefs(data: HomeDashboardData): RelatedRef[] {
  const refs: RelatedRef[] = [];
  const seen = new Set<string>();
  const append = (type: string, id: string) => {
    const key = `${type}\0${id}`;
    if (seen.has(key)) return;
    seen.add(key);
    refs.push({ type, id, href: `/api/v1/views/${type}/${id}` });
  };

  data.attention_tasks.forEach((task) => append("task_summary", task.task_id));
  data.recent_workers.forEach((worker) => append("worker_detail", worker.worker_session_id));
  data.recent_deliveries.forEach((delivery) => {
    append("task_summary", delivery.task_id);
    delivery.artifacts.forEach((artifact) => append("artifact_detail", artifact.artifact_id));
  });
  return refs;
}

export function demoAssistantProjection() {
  const dimensions = Object.fromEntries([
    "goal",
    "deliverable_acceptance",
    "scope_target",
    "constraints",
    "context_access",
    "risk_approval",
    "execution_policy",
  ].map((dimension) => [dimension, {
    status: dimension === "constraints" ? "assumed" : "known",
    summary: `${dimension} is ready`,
    evidence_refs: [],
  }]));
  return {
    conversation_id: "conversation-browser", revision: 8, generation: 1, status: "active", workspace_id: "workspace-local", updated_at: "2026-07-25T00:00:00Z",
    turns: [
      { turn_id: "turn-1", sequence: 1, role: "user", content: "Add the Assistant dispatch route.", created_at: "2026-07-25T00:00:00Z" },
      { turn_id: "turn-2", sequence: 2, role: "assistant", content: "I prepared a ready draft for the local workspace.", created_at: "2026-07-25T00:01:00Z" },
    ],
    assistant_run: { assistant_run_id: "run-browser", status: "completed", revision: 3, retryable: false },
    readiness: { assessment_id: "assessment-browser", revision: 5, status: "ready_with_assumptions", dimensions, blocking_unknowns: [], assumptions: ["Keep direct TaskSpec creation available."], clarification_questions: 1 },
    task_draft: { task_draft_id: "draft-browser", revision: 3, status: "current", assumptions: [], task_spec: {
      title: "Assistant dispatch route", goal: "Deliver the focused Assistant experience.", workspace_id: "workspace-local",
      workspace_resolution_ref: { resolution_id: "resolution-local", revision: 2, workspace_revision_id: "workspace-revision-local-v1", membership_revision: 3 },
      readiness_assessment_ref: { assessment_id: "assessment-browser", policy_revision: "assistant-readiness-v1", content_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
      creation_intent_ref: { intent_id: "pending-confirmation", task_spec_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", trigger_policy_revision: "assistant-trigger-v1" },
      target: { kind: "workspace", ref: "workspace-local" }, assumptions: [{ rule_id: "assistant-default/1", description: "Keep direct TaskSpec creation available." }],
      acceptance_criteria: ["Verify desktop and mobile layouts."], constraints: ["Do not modify server contracts."],
      agent_config: { profile_id: "codex-deepseek-v3.2", profile_revision: "1", adapter: "codex-acp" },
      runtime_config: { network_policy: "m1_public_web_restricted_v1" },
    } },
    handoffs: [{ task_id: "task-switchback-counter", status: "running", worker_session_id: "session-local-01" }],
  };
}

function baseSnapshot<T>(viewType: string, viewId: string, data: T): ProjectionSnapshot<T> {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: viewType,
    view_id: viewId,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: `${viewType}-${viewId}`,
    generated_at: "2026-07-23T09:37:00Z",
    data,
    available_actions: [],
    related_refs: [],
  };
}
