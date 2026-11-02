import { expect, test } from "@playwright/test";
import { mockFrontier } from "./frontierMock";

test("Assistant keeps confirmation pending until its causal projection and hands off to Worker", async ({ page }) => {
  await page.addInitScript(() => {
    class LiveEventSource {
      onopen: (() => void) | null = null;
      onerror: (() => void) | null = null;

      constructor() {
        window.setTimeout(() => this.onopen?.(), 0);
      }

      addEventListener() {}
      close() {}
    }
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: LiveEventSource,
    });
  });
  await mockFrontier(page);
  const commands: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/assistant/conversations/conversation-browser/task-drafts/draft-browser/confirm", async (route) => {
    commands.push(route.request().postDataJSON());
    await route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ command_id: "confirm-browser", conversation_id: "conversation-browser", conversation_revision: 8, projection_ref: "/api/v1/views/assistant_conversation/conversation-browser", status: "accepted" }) });
  });
  await page.goto("/assistant/conversation-browser");
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: /Confirm exact draft/ }).click();
  await expect(page.getByText("Command pending projection")).toBeVisible();
  await expect(page.getByRole("button", { name: /Confirm exact draft/ })).toBeDisabled();
  await expect(page.getByRole("textbox", { name: "Message the Assistant" })).toBeEnabled();
  await expect(page.getByText("Assistant dispatch route", { exact: true })).toBeVisible();
  expect(commands).toHaveLength(1);
  expect(commands[0]).toMatchObject({ command_type: "confirm_task", task_draft_id: "draft-browser", readiness_assessment_id: "assessment-browser", workspace_resolution_id: "resolution-local" });
  await page.getByRole("button", { name: /Open task task-switchback-counter/ }).click();
  await expect(page).toHaveURL(/\/tasks$/);
  await page.goto("/assistant/conversation-browser");
  await page.getByRole("button", { name: /Open worker session-local-01/ }).click();
  await expect(page).toHaveURL(/\/workers\/session-local-01$/);
});

test("Assistant layout remains usable on mobile while direct TaskSpec remains available", async ({ page }) => {
  await mockFrontier(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/assistant/conversation-browser");
  await expect(page.getByRole("textbox", { name: "Message the Assistant" })).toBeVisible();
  const layout = await page.locator('[data-layout="assistant-focused"]').evaluate((element) => ({
    columns: getComputedStyle(element).gridTemplateColumns.split(" ").filter(Boolean).length,
    documentWidth: document.documentElement.scrollWidth,
    viewport: document.documentElement.clientWidth,
    clipped: Array.from(document.querySelectorAll("button")).some((button) => button.offsetParent !== null && button.scrollWidth > button.clientWidth + 1),
    primaryBeforeSecondary: Boolean(element.querySelector('[data-region="primary"]')?.compareDocumentPosition(element.querySelector('[data-region="secondary"]')!) & Node.DOCUMENT_POSITION_FOLLOWING),
  }));
  expect(layout.columns).toBe(1);
  expect(layout.documentWidth).toBeLessThanOrEqual(layout.viewport + 1);
  expect(layout.clipped).toBe(false);
  expect(layout.primaryBeforeSecondary).toBe(true);
  await page.goto("/tasks");
  await expect(page.getByRole("button", { name: "Create task" })).toBeVisible();
});
