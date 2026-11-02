import { expect, test, type Page } from "@playwright/test";
import { demoArtifacts } from "../src/demo/data";
import { mockFrontier } from "./frontierMock";

async function keepEventStreamLive(page: Page) {
  await page.addInitScript(() => {
    class StableEventSource extends EventTarget {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSED = 2;

      readonly url: string;
      readonly withCredentials = false;
      readyState = StableEventSource.OPEN;
      onopen: ((event: Event) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;

      constructor(url: string | URL) {
        super();
        this.url = String(url);
        setTimeout(() => this.onopen?.(new Event("open")), 0);
      }

      close() {
        this.readyState = StableEventSource.CLOSED;
      }
    }

    window.EventSource = StableEventSource as unknown as typeof EventSource;
  });
}

test("reviews a Worker patch from the Task index", async ({ page }) => {
  await mockFrontier(page);
  await page.goto("/tasks");
  const openWorker = page.getByRole("button", {
    name: "Open Worker session-local-01 for Repair counter boundary behavior",
  });
  await openWorker.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/workers\/session-local-01$/);
  await expect(page.getByLabel("Execution status")).toContainText("TaskDelivered");
  await page.getByRole("button", { name: "Diff" }).click();
  await expect(page.getByLabel("Changed files")).toBeVisible();
  await expect(page.getByRole("treeitem", { name: /counter\.go/ })).toBeVisible();
  await expect(page.getByText("1 files")).toBeVisible();
});

test("fails closed when Artifact detail authority does not match the requested Artifact", async ({ page }) => {
  await mockFrontier(page);
  await page.route("**/api/v1/views/artifact_detail/artifact-patch-01", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        schema_version: "frontier.projection_snapshot.v1",
        view_type: "artifact_detail",
        view_id: "artifact-other",
        tenant_id: "tenant-local",
        view_epoch: 1,
        view_cursor: 1,
        projection_version: 1,
        query_fingerprint: "artifact-invalid",
        generated_at: "2026-07-23T09:37:00Z",
        data: demoArtifacts["artifact-patch-01"],
        available_actions: [{
          action: "artifact.download",
          target_id: "artifact-patch-01",
          expected_revision: 1,
        }],
        related_refs: [],
      }),
    });
  });
  await page.goto("/workers/session-local-01");
  await page.getByRole("button", { name: "Diff" }).click();

  await expect(page.getByRole("alert")).toContainText("Artifact unavailable");
  await expect(page.getByLabel("Changed files")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Download artifact" })).toHaveCount(0);
});

test("keeps Worker execution bounded beside the Workbench in focused shell mode", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await mockFrontier(page);
  await page.goto("/workers/session-local-01");

  await expect(page.getByTestId("product-shell")).toHaveAttribute("data-shell-mode", "focused");
  await expect(page.getByText("Workbench", { exact: true })).toBeVisible();
  await expect(page.getByText("Web IDE", { exact: true })).toHaveCount(0);
  const execution = page.locator("[data-region='worker-execution']");
  await expect(execution).toBeVisible();
  const bounds = await execution.evaluate((element) => {
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return {
      overflowY: style.overflowY,
      top: rect.top,
      bottom: rect.bottom,
      viewport: window.innerHeight,
      documentOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    };
  });
  expect(bounds.overflowY).toBe("auto");
  expect(bounds.top).toBeGreaterThanOrEqual(52);
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.viewport);
  expect(bounds.documentOverflow).toBe(false);
});

test("reviews a long Artifact patch in one bounded mobile viewport without reverse Worker authority", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 720 });
  await mockFrontier(page);
  const artifact = demoArtifacts["artifact-patch-01"];
  await page.route("**/api/v1/views/artifact_detail/artifact-patch-01", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        schema_version: "frontier.projection_snapshot.v1",
        view_type: "artifact_detail",
        view_id: artifact.artifact_id,
        tenant_id: "tenant-local",
        view_epoch: 1,
        view_cursor: 1,
        projection_version: 1,
        query_fingerprint: "artifact-mobile-patch",
        generated_at: "2026-07-29T00:00:00Z",
        data: {
          ...artifact,
          checksum: `sha256:${"a".repeat(64)}`,
          runtime_generation: 4,
          content: `${artifact.content}+const mobileOverflowEvidence = "${"x".repeat(320)}";\n`,
        },
        available_actions: [{
          action: "artifact.download",
          target_id: artifact.artifact_id,
          expected_revision: 1,
        }],
        related_refs: [],
      }),
    });
  });
  await page.goto("/artifacts/artifact-patch-01");

  await expect(page.getByTestId("product-shell")).toHaveAttribute("data-shell-mode", "focused");
  await expect(page.getByText("Review only · not applied")).toBeVisible();
  await expect(page.getByLabel("Artifact provenance")).toContainText("Generation4");
  const viewport = page.getByTestId("artifact-content-viewport");
  const bounds = await viewport.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      overflowX: style.overflowX,
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
      documentOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    };
  });
  expect(bounds.overflowX).toBe("auto");
  expect(bounds.scrollWidth).toBeGreaterThan(bounds.clientWidth);
  expect(bounds.documentOverflow).toBe(false);

  await expect(page.getByRole("button", { name: /Return to Worker/ })).toHaveCount(0);
});

test("blocks checksum-failed Artifact content actions in the real focused route", async ({ page }) => {
  await mockFrontier(page);
  await page.goto("/artifacts/artifact-checksum-failed");

  await expect(page.getByRole("alert")).toContainText("Checksum verification failed");
  await expect(page.getByRole("link", { name: "Download artifact" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Result" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Return to Worker/ })).toHaveCount(0);
});

test("continues a delivered Task with the exact execution tuple", async ({ page }) => {
  await mockFrontier(page);
  await keepEventStreamLive(page);
  const requests: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/tasks/task-switchback-counter/continue", async (route) => {
    requests.push(route.request().postDataJSON());
    return route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ command_id: "command-task-chat", status: "accepted" }),
    });
  });
  await page.goto("/workers/session-local-01");
  await page.getByRole("textbox", { name: "Continue with this worker" }).fill("Explain the verification result");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Command accepted" })).toBeVisible();
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({
    schema_version: "gateway.task_continuation.v1",
    expected_task_revision: 8,
    worker_session_id: "session-local-01",
    expected_session_revision: 14,
    worker_run_id: "run-03",
    expected_run_revision: 7,
    agent_turn_id: "turn-05",
    expected_turn_revision: 3,
    interaction_kind: "chat",
    message: "Explain the verification result",
  });
});

test("accepts a delivered Task with exact projected revision authority", async ({ page }) => {
  await mockFrontier(page);
  await keepEventStreamLive(page);
  const requests: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/tasks/task-switchback-counter/accept", async (route) => {
    requests.push(route.request().postDataJSON());
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ command_id: "command-task-accept", status: "accepted" }),
    });
  });
  await page.goto("/workers/session-local-01");

  await page.getByRole("button", { name: "Accept" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Acceptance submitted." })).toBeVisible();
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({
    schema_version: "gateway.task_command.v1",
    expected_task_revision: 8,
  });
  expect(requests[0].idempotency_key).toMatch(/^web-task-/);
  expect(requests[0]).not.toHaveProperty("message");
});

test("requests Task rework with exact revision and explicit feedback", async ({ page }) => {
  await mockFrontier(page);
  await keepEventStreamLive(page);
  const requests: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/tasks/task-switchback-counter/rework", async (route) => {
    requests.push(route.request().postDataJSON());
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ command_id: "command-task-rework", status: "accepted" }),
    });
  });
  await page.goto("/workers/session-local-01");

  await page.getByRole("textbox", { name: "Request changes message" }).fill("Add the missing negative-boundary proof.");
  await page.getByRole("button", { name: "Request changes" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Rework submitted." })).toBeVisible();
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({
    schema_version: "gateway.task_command.v1",
    expected_task_revision: 8,
    message: "Add the missing negative-boundary proof.",
  });
  expect(requests[0].idempotency_key).toMatch(/^web-task-/);
});

test("preserves a stale Task draft and requires an explicit resend", async ({ page }) => {
  await mockFrontier(page);
  await keepEventStreamLive(page);
  const requests: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/tasks/task-switchback-counter/continue", async (route) => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1) {
      return route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ code: "stale_target", view_ref: "/api/v1/views/worker_detail/session-local-01" }),
      });
    }
    return route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ command_id: "command-task-resend", status: "accepted" }),
    });
  });
  await page.goto("/workers/session-local-01");
  const composer = page.getByRole("textbox", { name: "Continue with this worker" });
  await composer.fill("Keep this draft through refresh");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("alert")).toContainText("review your draft and send it again");
  await expect(composer).toHaveValue("Keep this draft through refresh");
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Command accepted" })).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[1].idempotency_key).not.toBe(requests[0].idempotency_key);
});

test("keeps a terminal Task Session interactive and retries safely", async ({ page }) => {
  await mockFrontier(page);
  await keepEventStreamLive(page);
  const requests: Array<Record<string, unknown>> = [];
  let attempt = 0;
  await page.route("**/api/v1/worker-sessions/session-local-03/follow-up", async (route) => {
    attempt += 1;
    requests.push(route.request().postDataJSON());
    if (attempt === 1) return route.abort("connectionreset");
    return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ command_id: "command-browser", status: "accepted" }) });
  });
  await page.goto("/workers/session-local-03");
  await expect(page.getByLabel("Execution status")).toContainText("TaskAccepted");
  await page.getByRole("textbox", { name: "Continue with this worker" }).fill("Explain the final patch");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByRole("alert")).toContainText("Delivery status is unknown");
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Command accepted" })).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[1].idempotency_key).toBe(requests[0].idempotency_key);
  expect(requests[1].expected_session_revision).toBe(14);
});

test("reconnects the Worker stream from its durable cursor", async ({ page }) => {
  await mockFrontier(page);
  const eventRequests: string[] = [];
  await page.route("**/api/v1/views/worker_detail/session-local-01/events?cursor=*", async (route) => {
    eventRequests.push(route.request().url());
    return route.fulfill({
      status: 200,
      headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" },
      body: ": stream closed for reconnect test\n\n",
    });
  });
  await page.goto("/workers/session-local-01");
  await expect.poll(() => eventRequests.length, { timeout: 5_000 }).toBeGreaterThan(1);
  expect(eventRequests.every((url) => url.endsWith("cursor=1%3A4"))).toBe(true);
});

test("blocks content actions for an integrity failure", async ({ page }) => {
  await mockFrontier(page);
  await page.goto("/artifacts/artifact-checksum-failed");
  await expect(page.getByRole("alert")).toContainText("Checksum verification failed");
  await expect(page.getByRole("link", { name: "Download artifact" })).toHaveCount(0);
  await expect(page.getByLabel("Artifact provenance")).toContainText("run-03");
});
