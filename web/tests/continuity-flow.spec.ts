import { expect, test } from "@playwright/test";
import type {
  ProjectionControlEvent,
  ProjectionDelta,
  WorkerDetailData,
} from "../src/api/contracts";
import {
  demoAssistantProjection,
  installFrontierEventHarness,
  mockFrontier,
  productionWorkerSnapshot,
} from "./frontierMock";

test("reconnect keeps trusted Assistant state visible and suspends mutations until live recovery", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  await mockFrontier(page);
  await page.goto("/assistant/conversation-browser");
  const composer = page.getByRole("textbox", { name: "Message the Assistant" });
  await composer.fill("Preserve this draft through reconnect.");
  await expect(page.getByRole("button", { name: "Send message" })).toBeEnabled();

  await events.setAutoOpen(false);
  await events.disconnectLatest();
  await expect(page.getByText("Reconnecting to live Assistant updates")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send message" })).toBeDisabled();
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();

  await events.setAutoOpen(true);
  await expect(page.getByText("Reconnecting to live Assistant updates")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send message" })).toBeEnabled();
  await expect(composer).toHaveValue("Preserve this draft through reconnect.");
});

test("SSE error followed by Snapshot 404 clears the bound View and does not reconnect", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  await mockFrontier(page);
  let snapshotRequests = 0;
  let denyRecovery = false;
  await page.route("**/api/v1/views/worker_detail/session-local-01", async (route) => {
    snapshotRequests += 1;
    if (!denyRecovery) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(productionWorkerSnapshot("session-local-01")),
      });
      return;
    }
    await route.fulfill({ status: 404, contentType: "application/json", body: "{}" });
  });

  await page.goto("/workers/session-local-01");
  await expect(page.getByRole("button", { name: "Accept" })).toBeEnabled();
  await expect.poll(async () => (await events.inspectSources()).length).toBe(1);
  const initialSnapshots = snapshotRequests;
  denyRecovery = true;

  await events.disconnectLatest();
  await expect(page.getByRole("alert")).toContainText("This View is no longer available.");
  expect(snapshotRequests).toBe(initialSnapshots + 1);
  expect(await events.inspectSources()).toHaveLength(1);

  await page.waitForTimeout(1_200);
  expect(snapshotRequests).toBe(initialSnapshots + 1);
  expect(await events.inspectSources()).toHaveLength(1);
});

test("SSE error followed by 503 uses one retry chain and one SSE reconnect", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  await mockFrontier(page);
  let snapshotRequests = 0;
  let retryableFailures = 0;
  await page.route("**/api/v1/views/worker_detail/session-local-01", async (route) => {
    snapshotRequests += 1;
    if (retryableFailures > 0) {
      retryableFailures -= 1;
      await route.fulfill({ status: 503, contentType: "application/json", body: "{}" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(productionWorkerSnapshot("session-local-01")),
    });
  });

  await page.goto("/workers/session-local-01");
  await expect(page.getByRole("button", { name: "Accept" })).toBeEnabled();
  await expect.poll(async () => (await events.inspectSources()).length).toBe(1);
  const initialSnapshots = snapshotRequests;
  retryableFailures = 1;

  await events.disconnectLatest();
  await expect.poll(() => snapshotRequests).toBe(initialSnapshots + 1);
  expect(await events.inspectSources()).toHaveLength(1);
  await expect.poll(() => snapshotRequests).toBe(initialSnapshots + 2);
  await expect.poll(async () => (await events.inspectSources()).length).toBe(2);
  await expect(page.getByRole("button", { name: "Accept" })).toBeEnabled();

  await page.waitForTimeout(1_200);
  expect(snapshotRequests).toBe(initialSnapshots + 2);
  expect(await events.inspectSources()).toHaveLength(2);
});

test("resync retains trusted state, blocks commands, and reloads one authoritative snapshot", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  let releaseRefresh: (() => void) | undefined;
  const refreshBlocked = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  let snapshots = 0;
  let blockRefresh = false;
  await mockFrontier(page);
  await page.route("**/api/v1/views/assistant_conversation/conversation-browser", async (route) => {
    snapshots += 1;
    if (blockRefresh) await refreshBlocked;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        schema_version: "frontier.projection_snapshot.v1",
        view_type: "assistant_conversation",
        view_id: "conversation-browser",
        tenant_id: "tenant-local",
        view_epoch: blockRefresh ? 2 : 1,
        view_cursor: 1,
        projection_version: blockRefresh ? 2 : 1,
        query_fingerprint: `assistant-resync-${snapshots}`,
        generated_at: "2026-07-29T00:00:00Z",
        data: demoAssistantProjection(),
        available_actions: [
          { action: "assistant.send_message", target_id: "conversation-browser", expected_revision: 8 },
          { action: "assistant.confirm_task", target_id: "draft-browser", expected_revision: 3 },
        ],
        related_refs: [],
      }),
    });
  });
  await page.goto("/assistant/conversation-browser");
  const composer = page.getByRole("textbox", { name: "Message the Assistant" });
  await composer.fill("Preserve this draft through resync.");
  await expect(page.getByRole("button", { name: "Send message" })).toBeEnabled();
  const initialSnapshots = snapshots;
  blockRefresh = true;

  await events.emitLatest("resync_required", control("resync_required"));
  await expect(page.getByText("Refreshing authoritative Assistant state")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send message" })).toBeDisabled();
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();
  expect(snapshots).toBe(initialSnapshots + 1);

  releaseRefresh?.();
  await expect(page.getByText("Refreshing authoritative Assistant state")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send message" })).toBeEnabled();
  await expect(composer).toHaveValue("Preserve this draft through resync.");
  expect(snapshots).toBe(initialSnapshots + 1);
});

test("client upgrade keeps trusted Assistant projection read-only without attempting resync", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  const snapshots: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/api/v1/views/assistant_conversation/conversation-browser")
      && !request.url().includes("/events")) {
      snapshots.push(request.url());
    }
  });
  await mockFrontier(page);
  await page.goto("/assistant/conversation-browser");
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();
  const initialSnapshots = snapshots.length;

  await events.emitLatest("client_upgrade_required", control("client_upgrade_required"));
  await expect(page.getByRole("alert")).toContainText("Client update required");
  await expect(page.getByRole("button", { name: "Send message" })).toBeDisabled();
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();
  await page.waitForTimeout(1_200);
  expect(snapshots).toHaveLength(initialSnapshots);
});

test("causal projection clears only its pending Assistant command", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  await mockFrontier(page);
  await page.route("**/api/v1/assistant/conversations/conversation-browser/messages", async (route) => {
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        command_id: "message-browser",
        conversation_id: "conversation-browser",
        conversation_revision: 8,
        assistant_run_id: "run-browser-next",
        assistant_run_revision: 1,
        projection_ref: "/api/v1/views/assistant_conversation/conversation-browser",
        status: "accepted",
      }),
    });
  });
  await page.goto("/assistant/conversation-browser");
  const composer = page.getByRole("textbox", { name: "Message the Assistant" });
  await composer.fill("Keep this pending until its causal delta.");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect(page.getByText("Command pending projection")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send message" })).toBeDisabled();

  await events.emitLatest("projection_delta", assistantDelta("message-browser"));
  await expect(page.getByText("Command pending projection")).toHaveCount(0);
  await composer.fill("The causal command is now settled.");
  await expect(page.getByRole("button", { name: "Send message" })).toBeEnabled();
});

test("permission removal and Runtime recovery are applied from one authoritative Worker delta", async ({ page }) => {
  const events = await installFrontierEventHarness(page);
  await mockFrontier(page);
  await page.goto("/workers/session-local-01");
  await expect(page.getByRole("button", { name: "Accept" })).toBeEnabled();
  await expect(page.getByRole("textbox", { name: "Continue with this worker" })).toBeEnabled();

  const snapshot = productionWorkerSnapshot("session-local-01");
  const recovered: WorkerDetailData = {
    ...snapshot.data,
    session_revision: 15,
    runtime_generation: 2,
    runtime_health: "Recovering",
    runtime_recovery: {
      status: "recovering_runtime",
      phase: "verifying",
      failed_generation: 1,
      replacement_generation: 2,
      attempt_count: 1,
      continuity_status: "pending",
      evidence_at: "2026-07-29T00:00:00Z",
      outcome_reason: null as never,
    },
  };
  const delta: ProjectionDelta<WorkerDetailData> = {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "worker-recovery-browser",
    view_type: "worker_detail",
    view_id: "session-local-01",
    view_epoch: 1,
    view_cursor: snapshot.view_cursor + 1,
    base_projection_version: snapshot.projection_version,
    result_projection_version: snapshot.projection_version + 1,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [{ type: "worker_session", id: "session-local-01" }],
    payload: {
      data: recovered,
      available_actions: [],
      related_refs: snapshot.related_refs,
    },
  };
  await events.emitLatest("projection_delta", delta);

  await expect(page.getByLabel("Runtime recovery")).toContainText("Generation 1 → 2");
  await expect(page.getByRole("button", { name: "Accept" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send" })).toBeDisabled();
});

function control(eventType: ProjectionControlEvent["event_type"]): ProjectionControlEvent {
  return {
    schema_version: "frontier.projection_control.v1",
    event_type: eventType,
    view_type: "assistant_conversation",
    view_id: "conversation-browser",
    view_epoch: 1,
    snapshot_cursor: 1,
  };
}

function assistantDelta(commandId: string): ProjectionDelta<ReturnType<typeof demoAssistantProjection>> {
  const data = {
    ...demoAssistantProjection(),
    revision: 9,
    updated_at: "2026-07-29T00:01:00Z",
  };
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "assistant-command-browser",
    view_type: "assistant_conversation",
    view_id: "conversation-browser",
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: "view.entity_upserted.v1",
    critical: false,
    command_id: commandId,
    source_refs: [{ type: "conversation", id: "conversation-browser" }],
    payload: {
      data,
      available_actions: [
        { action: "assistant.send_message", target_id: "conversation-browser", expected_revision: 9 },
        { action: "assistant.confirm_task", target_id: "draft-browser", expected_revision: 3 },
      ],
      related_refs: [],
    },
  };
}
