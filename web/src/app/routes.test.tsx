import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { demoArtifacts } from "../demo/data";
import {
  type BoundProductionView,
  type DataOfKind,
  productionViews,
  type ProductionViewKind,
} from "../projections/productionViewRegistry";
import { artifactSummary, timelineTurn, workerData } from "../projections/testdata/workerFixtures";
import { ThemeProvider } from "../providers/ThemeProvider";
import { clientViewReplicas } from "../state/clientViewReplica";
import { createAppRouter, routeHandle, settleAssistantCreationOperation } from "./routes";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function productionReplicaState<K extends ProductionViewKind>(
  view: BoundProductionView<K>,
) {
  const lease = clientViewReplicas.acquire(view);
  const state = lease.replica.getState() as ReturnType<
    typeof lease.replica.getState
  > & { readonly data?: DataOfKind<K> };
  lease.release();
  return state;
}

class RouteEventSource {
  static instances: RouteEventSource[] = [];

  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private readonly listeners = new Map<string, Array<(event: MessageEvent<string>) => void>>();

  constructor(readonly url: string) {
    RouteEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close() {}

  open() {
    this.onopen?.();
  }

  emit(type: string, value: unknown) {
    const cursor = typeof value === "object" && value !== null && "view_cursor" in value
      ? String((value as { view_cursor: unknown }).view_cursor)
      : "";
    const epoch = typeof value === "object" && value !== null && "view_epoch" in value
      ? String((value as { view_epoch: unknown }).view_epoch)
      : "";
    const event = {
      data: JSON.stringify(value),
      lastEventId: epoch && cursor ? `${epoch}:${cursor}` : "",
    } as MessageEvent<string>;
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  static reset() {
    RouteEventSource.instances = [];
  }
}

function assistantSnapshot({
  cursor,
  revision,
  title,
}: {
  cursor: number;
  revision: number;
  title: string;
}) {
  const conversationId = "conversation-resync";
  const dimensions = Object.fromEntries([
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
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "assistant_conversation",
    view_id: conversationId,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: cursor,
    projection_version: cursor,
    query_fingerprint: conversationId,
    generated_at: "2026-07-25T00:00:00Z",
    available_actions: [
      { action: "assistant.send_message", target_id: conversationId, expected_revision: revision },
      { action: "assistant.confirm_task", target_id: "draft-resync", expected_revision: revision },
    ],
    related_refs: [],
    data: {
      conversation_id: conversationId,
      revision,
      generation: 1,
      status: "active",
      workspace_id: "workspace-resync",
      updated_at: "2026-07-25T00:00:00Z",
      turns: [],
      handoffs: [],
      assistant_run: {
        assistant_run_id: "run-resync",
        status: "completed",
        revision,
        retryable: false,
      },
      readiness: {
        assessment_id: "assessment-resync",
        revision,
        status: "ready_confirmed",
        clarification_questions: 0,
        blocking_unknowns: [],
        assumptions: [],
        dimensions,
      },
      task_draft: {
        task_draft_id: "draft-resync",
        revision,
        status: "current",
        assumptions: [],
        task_spec: {
          title,
          goal: "Deliver the resync-safe draft.",
          workspace_id: "workspace-resync",
          workspace_resolution_ref: {
            resolution_id: "resolution-resync",
            revision,
            workspace_revision_id: "workspace-revision-resync",
            membership_revision: revision,
          },
          readiness_assessment_ref: {
            assessment_id: "assessment-resync",
            policy_revision: "assistant-readiness-v1",
            content_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
          },
          creation_intent_ref: {
            intent_id: "intent-resync",
            task_spec_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            trigger_policy_revision: "assistant-trigger-v1",
          },
          target: { kind: "workspace", ref: "workspace-resync" },
          constraints: [],
          acceptance_criteria: ["The draft is delivered."],
          assumptions: [],
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
        },
      },
    },
  };
}

function workerSnapshot(cursor: number, sessionRevision: number) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "worker_detail",
    view_id: "session-route",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: cursor,
    projection_version: cursor,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-25T00:01:00Z",
    data: workerData({
      worker_session_id: "session-route",
      session_revision: sessionRevision,
      artifacts: [],
      approvals: [],
      timeline_groups: [],
      stage_report: null,
    }),
    available_actions: [{
      action: "worker.follow_up",
      target_id: "session-route",
      expected_revision: sessionRevision,
    }],
    related_refs: [],
  };
}

function artifactSnapshot(artifactId: string) {
  const source = demoArtifacts["artifact-report-01"];
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "artifact_detail",
    view_id: artifactId,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: `artifact-${artifactId}`,
    generated_at: "2026-07-29T00:00:00Z",
    data: {
      ...source,
      artifact_id: artifactId,
      checksum: `sha256:${"a".repeat(64)}`,
      runtime_generation: 7,
      download_url: `/api/v1/artifacts/${artifactId}/content`,
    },
    available_actions: [{
      action: "artifact.download",
      target_id: artifactId,
      expected_revision: 1,
    }],
    related_refs: [],
  };
}

afterEach(() => {
  clientViewReplicas.delete("assistant_conversation", "conversation-resync");
  clientViewReplicas.delete("worker_detail", "session-route");
  clientViewReplicas.delete("artifact_detail", "artifact-route");
  clientViewReplicas.delete("artifact_detail", "artifact-no-worker");
  clientViewReplicas.delete("artifact_detail", "artifact-wrong-identity");
  RouteEventSource.reset();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

describe("production route tree", () => {
  it("clears old links on route target change and fences a late reply when the selected Artifact vanishes", async () => {
    const late = deferred<Response>();
    const diagnosticResponse = (run: string, turn: string) => new Response(JSON.stringify({
      links: [{ kind: "grafana", url: "http://localhost:3001/d/sb-worker-runtime/worker-runtime" }, { kind: "logs", url: "http://localhost:3001/explore" }],
      grafana_status: "configured", langfuse_status: "identity_unavailable",
      target: { worker_session_id: "session-route", worker_run_id: run, agent_turn_id: turn },
    }), { status: 200 });
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("worker_run_id=run-b")) return late.promise;
      if (url.includes("/diagnostics")) return diagnosticResponse("run-a", "turn-a");
      return new Response(JSON.stringify(workerSnapshot(4,14)), { status: 200 });
    });
    vi.stubGlobal("fetch", fetcher); vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/workers/session-route?worker_run_id=run-a&agent_turn_id=turn-a"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);
    await screen.findByRole("link", { name: /Grafana/ });
    await act(async () => { await router.navigate("/workers/session-route?worker_run_id=run-b&agent_turn_id=turn-b"); });
    expect(screen.queryByRole("link", { name: /Grafana/ })).not.toBeInTheDocument();
    await act(async () => { await router.navigate("/workers/session-route?artifact_id=gone"); });
    await screen.findByText("Selected diagnostic target unavailable.");
    await act(async () => { late.resolve(diagnosticResponse("run-b","turn-b")); });
    expect(screen.queryByRole("link", { name: /Grafana/ })).not.toBeInTheDocument();
    router.dispose();
  });
  it.each(["artifact_id=old-report", "worker_run_id=old-run&agent_turn_id=old-turn"])("restores historical diagnostics from %s without finding the target in the first timeline page", async (search) => {
    const old = artifactSummary({ artifact_id: "old-report", worker_run_id: "old-run", agent_turn_id: "old-turn", generated_at: "2026-07-24T00:00:00Z" });
    const latest = artifactSummary({ artifact_id: "new-report", worker_run_id: "new-run", agent_turn_id: "new-turn" });
    const snapshot = { ...workerSnapshot(4, 14), related_refs: [old, latest].map((artifact) => ({ type: "artifact_detail", id: artifact.artifact_id, href: `/api/v1/views/artifact_detail/${artifact.artifact_id}` })) };
    snapshot.data = workerData({ worker_session_id: "session-route", session_revision: 14, artifacts: [old, latest], approvals: [], timeline_groups: Array.from({ length: 20 }, (_, index) => timelineTurn({ agent_turn_id: `new-turn-${index}`, display_stream_id: `display-${index}` })), timeline_has_more: true, timeline_cursor: btoa("20\0new-turn").replace(/=+$/, ""), stage_report: null });
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/diagnostics")) return new Response(JSON.stringify({ links: [], grafana_status: "disabled", langfuse_status: "disabled", target: { worker_session_id: "session-route", worker_run_id: "old-run", agent_turn_id: "old-turn" } }), { status: 200 });
      if (url.includes("/artifact_detail/")) {
        const id = url.split("/").at(-1)!;
        const detail = artifactSnapshot(id);
        detail.data = { ...detail.data, ...(id === "old-report" ? old : latest), type: "report", completeness: "complete", content: id === "old-report" ? "# Restored old report" : "# New report" };
        return new Response(JSON.stringify(detail), { status: 200 });
      }
      return new Response(JSON.stringify(snapshot), { status: 200 });
    });
    vi.stubGlobal("fetch", fetcher); vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter([`/workers/session-route?${search}`]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);
    await screen.findByText("Diagnostic links disabled.");
    expect(fetcher.mock.calls.filter(([url]) => String(url).includes("/diagnostics")).map(([url]) => String(url))).toEqual(["/api/v1/views/worker_detail/session-route/diagnostics?worker_run_id=old-run&agent_turn_id=old-turn"]);
    expect(fetcher.mock.calls.some(([url]) => String(url).includes("/timeline?"))).toBe(false);
    if (search.startsWith("artifact")) { await screen.findByRole("heading", { name: "Restored old report" }); expect(screen.queryByRole("heading", { name: "New report" })).not.toBeInTheDocument(); }
    router.dispose();
  });

  it.each(["artifact_id=gone", "artifact_id=old&worker_run_id=old-run", "agent_turn_id=old-turn", "worker_run_id=", "worker_run_id=one&worker_run_id=two", "worker_run_id=%xx"])("fails malformed or vanished selection closed: %s", async (search) => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL) => new Response(JSON.stringify(workerSnapshot(4, 14)), { status: 200 }));
    vi.stubGlobal("fetch", fetcher); vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter([`/workers/session-route?${search}`]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);
    await screen.findByText("Selected diagnostic target unavailable.");
    expect(fetcher.mock.calls.some(([url]) => String(url).includes("/diagnostics"))).toBe(false);
    expect(router.state.location.search).toBe(`?${search}`);
    router.dispose();
  });
  it.each([
    ["/", "overview", "工作台"],
    ["/tasks", "overview", "任务交付"],
    ["/observability", "overview", "可观测性"],
    ["/assistant", "overview", "Assistant"],
    ["/assistant/conversation-1", "focused", "Assistant"],
    ["/workers/session-1", "focused", "Worker"],
    ["/artifacts/artifact-1", "focused", "Artifact"],
  ] as const)("classifies %s with its product shell handle", (pathname, shellMode, title) => {
    expect(routeHandle(pathname)).toEqual({ shellMode, title });

    const router = createAppRouter([pathname]);
    expect(router.state.matches.at(-1)?.route.handle).toEqual({ shellMode, title });
    router.dispose();
  });

  it.each([
    ["/", "工作台"],
    ["/tasks", "任务交付"],
    ["/observability", "可观测性"],
    ["/assistant", "Assistant"],
    ["/assistant/conversation-heading", "Assistant"],
    ["/workers/session-heading", "Worker"],
    ["/artifacts/artifact-heading", "Artifact"],
  ] as const)("renders exactly one accessible page-level heading for %s", async (pathname, title) => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("view unavailable", { status: 503 })));
    const router = createAppRouter([pathname]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const headings = await screen.findAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0]).toHaveAccessibleName(title);
    router.dispose();
  });

  it("does not expose reverse Worker navigation from Artifact detail", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/artifact_detail/artifact-route")) {
        return new Response(JSON.stringify(artifactSnapshot("artifact-route")), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("view unavailable", { status: 503 });
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("heading", { name: "Stage report" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Return to Worker/ })).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/artifacts/artifact-route");

    router.dispose();
  });

  it("does not manufacture a Worker return when the projected reference is absent", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(
      artifactSnapshot("artifact-no-worker"),
    ), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-no-worker"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("heading", { name: "Stage report" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Return to Worker/ })).not.toBeInTheDocument();

    router.dispose();
  });

  it("fails the Artifact route closed for any historical related reference", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      ...artifactSnapshot("artifact-no-worker"),
      related_refs: [{
        type: "worker_detail",
        id: "session-untrusted",
        href: "/api/v1/views/worker_detail/session-untrusted",
      }],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-no-worker"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("alert"))
      .toHaveTextContent("Frontier view data could not be validated.");
    expect(screen.queryByRole("button", { name: /Return to Worker/ })).not.toBeInTheDocument();

    router.dispose();
  });

  it("fails the Artifact route closed when the snapshot identity is wrong", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      ...artifactSnapshot("artifact-wrong-identity"),
      view_id: "artifact-other",
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-wrong-identity"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("alert"))
      .toHaveTextContent("Frontier view data could not be validated.");
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();

    router.dispose();
  });

  it("suspends cached Artifact actions through a real resync until the replacement stream is live", async () => {
    const refreshedSnapshot = deferred<Response>();
    let snapshotRequest = 0;
    const fetcher = vi.fn(async () => {
      snapshotRequest += 1;
      if (snapshotRequest === 1) {
        return new Response(JSON.stringify(artifactSnapshot("artifact-route")), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return refreshedSnapshot.promise;
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("heading", { name: "Stage report" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
    expect(screen.getByText("Download unavailable while Artifact authority reconnects.")).toBeInTheDocument();
    act(() => RouteEventSource.instances[0].open());
    await waitFor(() => expect(
      screen.getByRole("link", { name: "Download artifact" }),
    ).toBeInTheDocument());

    act(() => RouteEventSource.instances[0].emit("resync_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "resync_required",
      view_type: "artifact_detail",
      view_id: "artifact-route",
      view_epoch: 1,
      snapshot_cursor: 2,
    }));
    await waitFor(() => expect(
      screen.queryByRole("link", { name: "Download artifact" }),
    ).not.toBeInTheDocument());
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Download unavailable while Artifact authority resynchronizes.")).toBeInTheDocument();

    await act(async () => {
      refreshedSnapshot.resolve(new Response(JSON.stringify({
        ...artifactSnapshot("artifact-route"),
        view_cursor: 2,
        projection_version: 2,
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
      await refreshedSnapshot.promise;
    });
    expect(RouteEventSource.instances).toHaveLength(2);
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
    expect(screen.getByText("Download unavailable while Artifact authority reconnects.")).toBeInTheDocument();
    act(() => RouteEventSource.instances[1].open());
    await waitFor(() => expect(
      screen.getByRole("link", { name: "Download artifact" }),
    ).toBeInTheDocument());

    router.dispose();
  });

  it("keeps cached Artifact content read-only after a real client upgrade control", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(
      artifactSnapshot("artifact-route"),
    ), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/artifacts/artifact-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("heading", { name: "Result" })).toBeInTheDocument();
    act(() => RouteEventSource.instances[0].open());
    await waitFor(() => expect(
      screen.getByRole("link", { name: "Download artifact" }),
    ).toBeInTheDocument());

    act(() => RouteEventSource.instances[0].emit("client_upgrade_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "client_upgrade_required",
      view_type: "artifact_detail",
      view_id: "artifact-route",
      view_epoch: 1,
      snapshot_cursor: 1,
    }));
    await waitFor(() => expect(
      screen.queryByRole("link", { name: "Download artifact" }),
    ).not.toBeInTheDocument());
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Download unavailable until the client is updated.")).toBeInTheDocument();

    router.dispose();
  });

  it("reuses the same creation key after response loss and navigates once after accepted retry", async () => {
    let createAttempts = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        createAttempts += 1;
        if (createAttempts === 1) {
          throw new TypeError("response lost after server commit");
        }
        return new Response(JSON.stringify({
          command_id: "command-1",
          conversation_id: "conversation-accepted",
          conversation_revision: 1,
          projection_ref: "/api/v1/views/assistant_conversation/conversation-accepted",
          status: "accepted",
        }), { status: 202, headers: { "Content-Type": "application/json" } });
      }
      return new Response("view unavailable", { status: 503 });
    });
    vi.stubGlobal("fetch", fetcher);
    const router = createAppRouter(["/assistant"]);
    let acceptedNavigations = 0;
    let previousPathname = router.state.location.pathname;
    const unsubscribe = router.subscribe((state) => {
      if (
        previousPathname !== state.location.pathname
        && state.location.pathname === "/assistant/conversation-accepted"
      ) {
        acceptedNavigations += 1;
      }
      previousPathname = state.location.pathname;
    });
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByRole("button", { name: "开始 Assistant 对话" })).toBeInTheDocument();
    expect(fetcher).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "开始 Assistant 对话" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Delivery status is unknown");
    fireEvent.click(screen.getByRole("button", { name: "开始 Assistant 对话" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/assistant/conversation-accepted"));
    const createBodies = fetcher.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([, init]) => JSON.parse(String(init?.body)));
    expect(createBodies).toHaveLength(2);
    expect(createBodies[1].idempotency_key).toBe(createBodies[0].idempotency_key);
    expect(acceptedNavigations).toBe(1);

    unsubscribe();
    router.dispose();
  });

  it("reuses the same creation key when an accepted response has a mismatched projection reference", async () => {
    let createAttempts = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== "POST") {
        return new Response("view unavailable", { status: 503 });
      }
      createAttempts += 1;
      return new Response(JSON.stringify({
        command_id: `command-projection-${createAttempts}`,
        conversation_id: "conversation-projection",
        conversation_revision: 1,
        projection_ref: createAttempts === 1
          ? "/api/v1/views/task_summary/not-the-conversation"
          : "/api/v1/views/assistant_conversation/conversation-projection",
        status: "accepted",
      }), { status: 202, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetcher);
    const router = createAppRouter(["/assistant"]);
    let acceptedNavigations = 0;
    const unsubscribe = router.subscribe((state) => {
      if (state.location.pathname === "/assistant/conversation-projection") {
        acceptedNavigations += 1;
      }
    });
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    fireEvent.click(await screen.findByRole("button", { name: "开始 Assistant 对话" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("temporarily unavailable");
    fireEvent.click(screen.getByRole("button", { name: "开始 Assistant 对话" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/assistant/conversation-projection"));

    const createBodies = fetcher.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([, init]) => JSON.parse(String(init?.body)));
    expect(createBodies).toHaveLength(2);
    expect(createBodies[1].idempotency_key).toBe(createBodies[0].idempotency_key);
    expect(acceptedNavigations).toBe(1);

    unsubscribe();
    router.dispose();
  });

  it("does not let a stale creation operation clear a newer operation key", () => {
    expect(settleAssistantCreationOperation("new-operation-key", "old-operation-key", "accepted")).toEqual({
      apply: false,
      key: "new-operation-key",
    });
    expect(settleAssistantCreationOperation("new-operation-key", "old-operation-key", "definitive_failure")).toEqual({
      apply: false,
      key: "new-operation-key",
    });
    expect(settleAssistantCreationOperation("operation-key", "operation-key", "ambiguous_failure")).toEqual({
      apply: true,
      key: "operation-key",
    });
  });

  it("fails closed through a real replica resync until the refreshed stream is live", async () => {
    const refreshedSnapshot = deferred<Response>();
    let snapshotRequest = 0;
    let commandRequests = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        commandRequests += 1;
        return new Response(JSON.stringify({ command_id: "unexpected-command" }), {
          status: 202,
          headers: { "Content-Type": "application/json" },
        });
      }
      snapshotRequest += 1;
      if (snapshotRequest === 1) {
        return new Response(JSON.stringify(assistantSnapshot({
          cursor: 1,
          revision: 1,
          title: "Draft before resync",
        })), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return refreshedSnapshot.promise;
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/assistant/conversation-resync"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    expect(await screen.findByText("Draft before resync")).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: /Confirm exact draft/ });
    expect(confirm).toBeDisabled();
    expect(RouteEventSource.instances).toHaveLength(1);
    act(() => RouteEventSource.instances[0].open());
    await waitFor(() => expect(confirm).toBeEnabled());

    const composer = screen.getByRole("textbox", { name: "Message the Assistant" });
    fireEvent.change(composer, { target: { value: "Keep this draft visible" } });
    act(() => RouteEventSource.instances[0].emit("resync_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "resync_required",
      view_type: "assistant_conversation",
      view_id: "conversation-resync",
      view_epoch: 1,
      snapshot_cursor: 2,
    }));

    await waitFor(() => expect(confirm).toBeDisabled());
    expect(screen.getByText("Draft before resync")).toBeInTheDocument();
    expect(composer).toBeEnabled();
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    fireEvent.click(confirm);
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(commandRequests).toBe(0);

    await act(async () => {
      refreshedSnapshot.resolve(new Response(JSON.stringify(assistantSnapshot({
        cursor: 2,
        revision: 2,
        title: "Draft after resync",
      })), { status: 200, headers: { "Content-Type": "application/json" } }));
      await refreshedSnapshot.promise;
    });
    expect(await screen.findByText("Draft after resync")).toBeInTheDocument();
    expect(RouteEventSource.instances).toHaveLength(2);
    expect(confirm).toBeDisabled();
    act(() => RouteEventSource.instances[1].open());
    await waitFor(() => expect(confirm).toBeEnabled());
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();

    router.dispose();
  });

  it("fails Worker mutations closed through resync and client upgrade while preserving the draft", async () => {
    const refreshedSnapshot = deferred<Response>();
    let snapshotRequest = 0;
    let commandRequests = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (String(_input).includes("/diagnostics")) {
        return new Response(JSON.stringify({
          links: [], grafana_status: "disabled", langfuse_status: "identity_unavailable",
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (init?.method === "POST") {
        commandRequests += 1;
        return new Response(JSON.stringify({ command_id: "unexpected-command", status: "accepted" }), {
          status: 202,
          headers: { "Content-Type": "application/json" },
        });
      }
      snapshotRequest += 1;
      if (snapshotRequest === 1) {
        return new Response(JSON.stringify(workerSnapshot(4, 14)), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return refreshedSnapshot.promise;
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/workers/session-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const composer = await screen.findByRole("textbox", { name: "Continue with this worker" });
    const send = screen.getByRole("button", { name: "Send" });
    fireEvent.change(composer, { target: { value: "Keep the exact Worker draft" } });
    expect(send).toBeDisabled();
    act(() => RouteEventSource.instances[0].open());
    await waitFor(() => expect(send).toBeEnabled());

    act(() => RouteEventSource.instances[0].emit("resync_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "resync_required",
      view_type: "worker_detail",
      view_id: "session-route",
      view_epoch: 1,
      snapshot_cursor: 5,
    }));
    await waitFor(() => expect(send).toBeDisabled());
    expect(composer).toHaveValue("Keep the exact Worker draft");
    fireEvent.click(send);
    expect(commandRequests).toBe(0);

    await act(async () => {
      refreshedSnapshot.resolve(new Response(JSON.stringify(workerSnapshot(5, 15)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
      await refreshedSnapshot.promise;
    });
    expect(RouteEventSource.instances).toHaveLength(2);
    act(() => RouteEventSource.instances[1].open());
    await waitFor(() => expect(send).toBeEnabled());
    expect(composer).toHaveValue("Keep the exact Worker draft");

    act(() => RouteEventSource.instances[1].emit("client_upgrade_required", {
      schema_version: "frontier.projection_control.v1",
      event_type: "client_upgrade_required",
      view_type: "worker_detail",
      view_id: "session-route",
      view_epoch: 1,
      snapshot_cursor: 5,
    }));
    await waitFor(() => expect(send).toBeDisabled());
    expect(composer).toHaveValue("Keep the exact Worker draft");
    fireEvent.click(send);
    expect(commandRequests).toBe(0);

    router.dispose();
  });

  it("keeps a Worker mutation fenced from local submit through projection pending", async () => {
    const accepted = deferred<Response>();
    let commandRequests = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        commandRequests += 1;
        return accepted.promise;
      }
      return new Response(JSON.stringify(workerSnapshot(4, 14)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/workers/session-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const composer = await screen.findByRole("textbox", { name: "Continue with this worker" });
    const send = screen.getByRole("button", { name: "Send" });
    act(() => RouteEventSource.instances[0].open());
    fireEvent.change(composer, { target: { value: "Submit once" } });
    await waitFor(() => expect(send).toBeEnabled());
    fireEvent.click(send);
    fireEvent.click(send);
    expect(commandRequests).toBe(1);
    expect(send).toBeDisabled();

    await act(async () => {
      accepted.resolve(new Response(JSON.stringify({
        command_id: "command-worker-pending",
        status: "accepted",
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
      await accepted.promise;
    });
    expect(await screen.findByText("1 pending")).toBeInTheDocument();
    expect(productionReplicaState(productionViews.worker("session-route")).pendingCommands)
      .toEqual([expect.objectContaining({
        commandId: "command-worker-pending",
        action: "follow-up",
        scopeKey: "worker-command:follow-up:session-route",
      })]);
    fireEvent.change(composer, { target: { value: "Do not resubmit old revision" } });
    expect(send).toBeDisabled();
    fireEvent.click(send);
    expect(commandRequests).toBe(1);

    router.dispose();
  });

  it("keeps Worker delta-first state pending until the matching HTTP acceptance", async () => {
    const accepted = deferred<Response>();
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") return accepted.promise;
      return new Response(JSON.stringify(workerSnapshot(4, 14)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/workers/session-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const composer = await screen.findByRole("textbox", { name: "Continue with this worker" });
    const send = screen.getByRole("button", { name: "Send" });
    act(() => RouteEventSource.instances[0].open());
    fireEvent.change(composer, { target: { value: "Project before HTTP" } });
    await waitFor(() => expect(send).toBeEnabled());
    fireEvent.click(send);
    const commandRequest = fetcher.mock.calls.find(([, init]) => init?.method === "POST");
    const commandBody = JSON.parse(String(commandRequest?.[1]?.body));
    expect(productionReplicaState(productionViews.worker("session-route")).pendingCommands)
      .toEqual([expect.objectContaining({
        clientCommandId: commandBody.idempotency_key,
        action: "follow-up",
      })]);

    const projected = workerSnapshot(5, 15);
    act(() => RouteEventSource.instances[0].emit("projection_delta", {
      schema_version: "frontier.projection_delta.v1",
      delta_id: "delta-worker-before-http",
      view_type: "worker_detail",
      view_id: "session-route",
      view_epoch: 1,
      view_cursor: 5,
      base_projection_version: 4,
      result_projection_version: 5,
      event_type: "view.entity_upserted.v1",
      critical: true,
      command_id: "command-worker-before-http",
      client_command_id: commandBody.idempotency_key,
      source_refs: [{ type: "integration_event", id: "event-worker-before-http" }],
      payload: {
        data: projected.data,
        available_actions: projected.available_actions,
        related_refs: [],
      },
    }));
    expect(productionReplicaState(productionViews.worker("session-route")).pendingCommands)
      .toEqual([expect.objectContaining({
        clientCommandId: commandBody.idempotency_key,
        commandId: undefined,
      })]);

    await act(async () => {
      accepted.resolve(new Response(JSON.stringify({
        command_id: "command-worker-before-http",
        status: "accepted",
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
      await accepted.promise;
    });
    await waitFor(() => expect(
      productionReplicaState(productionViews.worker("session-route")).pendingCommands,
    ).toEqual([]));

    router.dispose();
  });

  it("keeps Assistant delta-first state pending until the matching HTTP acceptance", async () => {
    const accepted = deferred<Response>();
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") return accepted.promise;
      return new Response(JSON.stringify(assistantSnapshot({
        cursor: 1,
        revision: 8,
        title: "Assistant before HTTP",
      })), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/assistant/conversation-resync"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const composer = await screen.findByRole("textbox", { name: "Message the Assistant" });
    const send = screen.getByRole("button", { name: "Send message" });
    act(() => RouteEventSource.instances[0].open());
    fireEvent.change(composer, { target: { value: "Project Assistant before HTTP" } });
    await waitFor(() => expect(send).toBeEnabled());
    fireEvent.click(send);
    const commandRequest = fetcher.mock.calls.find(([, init]) => init?.method === "POST");
    const commandBody = JSON.parse(String(commandRequest?.[1]?.body));
    expect(productionReplicaState(productionViews.assistant("conversation-resync")).pendingCommands)
      .toEqual([expect.objectContaining({
        clientCommandId: commandBody.idempotency_key,
        action: "send_message",
      })]);

    const projected = assistantSnapshot({
      cursor: 2,
      revision: 9,
      title: "Assistant projected before HTTP",
    });
    act(() => RouteEventSource.instances[0].emit("projection_delta", {
      schema_version: "frontier.projection_delta.v1",
      delta_id: "delta-assistant-before-http",
      view_type: "assistant_conversation",
      view_id: "conversation-resync",
      view_epoch: 1,
      view_cursor: 2,
      base_projection_version: 1,
      result_projection_version: 2,
      event_type: "view.entity_upserted.v1",
      critical: true,
      command_id: "command-assistant-before-http",
      client_command_id: commandBody.idempotency_key,
      source_refs: [{ type: "integration_event", id: "event-assistant-before-http" }],
      payload: {
        data: projected.data,
        available_actions: projected.available_actions,
        related_refs: [],
      },
    }));
    expect(productionReplicaState(productionViews.assistant("conversation-resync")).pendingCommands)
      .toEqual([expect.objectContaining({
        clientCommandId: commandBody.idempotency_key,
        commandId: undefined,
      })]);

    await act(async () => {
      accepted.resolve(new Response(JSON.stringify({
        command_id: "command-assistant-before-http",
        conversation_id: "conversation-resync",
        conversation_revision: 9,
        projection_ref: "/api/v1/views/assistant_conversation/conversation-resync",
        status: "accepted",
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
      await accepted.promise;
    });
    await waitFor(() => expect(
      productionReplicaState(productionViews.assistant("conversation-resync")).pendingCommands,
    ).toEqual([]));

    router.dispose();
  });

  it("records an accepted Approval pending command with its exact Approval owner scope", async () => {
    const snapshot = workerSnapshot(4, 14);
    const approvalID = "approval-route-owner";
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        return new Response(JSON.stringify({
          command_id: "command-approval-route",
          status: "accepted",
        }), { status: 202, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({
        ...snapshot,
        data: {
          ...snapshot.data,
          approvals: [{
            approval_request_id: approvalID,
            revision: 3,
            capability: "github.branch_write",
            target: "github.com/acme/switchback:refs/heads/route",
            effect: "external_write",
            reason: "Route-owned approval.",
            expires_at: "2099-07-26T09:00:00Z",
            scope_options: ["once"],
          }],
        },
        available_actions: [{
          action: "worker.approval",
          target_id: approvalID,
          expected_revision: 3,
        }],
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", RouteEventSource);
    const router = createAppRouter(["/workers/session-route"]);
    render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

    const approve = await screen.findByRole("button", { name: "Approve once" });
    act(() => RouteEventSource.instances[0].open());
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);

    await waitFor(() => expect(
      productionReplicaState(productionViews.worker("session-route")).pendingCommands,
    ).toEqual([expect.objectContaining({
      commandId: "command-approval-route",
      action: "approval",
      scopeKey: `approval:${approvalID}`,
    })]));

    router.dispose();
  });

  it.each(["accepted", "error"] as const)(
    "ignores a late %s creation result after shell navigation unmounts the start route",
    async (outcome) => {
      const pending = deferred<Response>();
      const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === "POST") {
          return pending.promise;
        }
        return new Response("view unavailable", { status: 503 });
      });
      vi.stubGlobal("fetch", fetcher);
      const router = createAppRouter(["/assistant"]);
      render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);

      fireEvent.click(await screen.findByRole("button", { name: "开始 Assistant 对话" }));
      fireEvent.click(screen.getByRole("link", { name: "任务交付" }));
      await waitFor(() => expect(router.state.location.pathname).toBe("/tasks"));

      await act(async () => {
        if (outcome === "accepted") {
          pending.resolve(new Response(JSON.stringify({
            command_id: "command-late",
            conversation_id: "conversation-late",
            conversation_revision: 1,
            projection_ref: "/api/v1/views/assistant_conversation/conversation-late",
            status: "accepted",
          }), { status: 202, headers: { "Content-Type": "application/json" } }));
        } else {
          pending.reject(new TypeError("late response loss"));
        }
        await pending.promise.catch(() => undefined);
      });

      expect(router.state.location.pathname).toBe("/tasks");
      expect(screen.queryByText("Delivery status is unknown. Retry safely with the same command.")).not.toBeInTheDocument();
      router.dispose();
    },
  );
});
