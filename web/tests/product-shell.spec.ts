import { expect, test } from "@playwright/test";
import { mockFrontier } from "./frontierMock";

test("one product shell owns all six production route patterns", async ({ page }) => {
  await mockFrontier(page);
  const routes = [
    ["/", "overview"],
    ["/tasks", "overview"],
    ["/assistant", "overview"],
    ["/assistant/conversation-browser", "focused"],
    ["/workers/session-local-01", "focused"],
    ["/artifacts/artifact-report-01", "focused"],
  ] as const;

  for (const [path, shellMode] of routes) {
    await page.goto(path);
    await expect(page.getByTestId("product-shell")).toHaveAttribute("data-shell-mode", shellMode);
  }

  await expect(page.getByRole("navigation", { name: "产品导航" })).toHaveAttribute("data-navigation-mode", "rail");
});

test("mobile drawer and explicit Assistant creation preserve command and focus authority", async ({ page }) => {
  await mockFrontier(page);
  await page.setViewportSize({ width: 390, height: 844 });
  const creates: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/assistant/conversations", async (route) => {
    creates.push(route.request().postDataJSON());
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        command_id: "create-browser",
        conversation_id: "conversation-browser",
        conversation_revision: 1,
        projection_ref: "/api/v1/views/assistant_conversation/conversation-browser",
        status: "accepted",
      }),
    });
  });

  await page.goto("/tasks");
  const trigger = page.getByRole("button", { name: "打开导航菜单" });
  await trigger.click();
  const dialog = page.getByRole("dialog", { name: "导航菜单" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("平台规划")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await dialog.getByRole("link", { name: "Assistant 对话" }).click();
  await expect(page).toHaveURL(/\/assistant$/);
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
  expect(creates).toHaveLength(0);

  await page.getByRole("button", { name: "开始 Assistant 对话" }).click();
  await expect(page).toHaveURL(/\/assistant\/conversation-browser$/);
  expect(creates).toHaveLength(1);
  expect(creates[0]).toMatchObject({ command_type: "create_conversation" });
});

test("low-height mobile drawer scrolls independently and traps real keyboard focus", async ({ page }) => {
  await mockFrontier(page);
  await page.setViewportSize({ width: 844, height: 320 });
  await page.goto("/tasks");

  await page.getByRole("button", { name: "打开导航菜单" }).click();
  const dialog = page.getByRole("dialog", { name: "导航菜单" });
  const closeButton = dialog.getByRole("button", { name: "关闭导航菜单" });
  const lastLink = dialog.getByRole("link").last();
  const scrollRegion = dialog.getByTestId("mobile-navigation-scroll");
  const lastPlannedItem = dialog.getByText("Admin", { exact: true });

  await expect(dialog).toBeVisible();
  await expect(closeButton).toBeFocused();
  await expect(scrollRegion).toHaveCSS("overflow-y", "auto");
  expect(await scrollRegion.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
  await lastPlannedItem.scrollIntoViewIfNeeded();
  await expect(lastPlannedItem).toBeVisible();
  await expect(page.locator("body")).toHaveCSS("overflow", "hidden");

  await lastLink.focus();
  await page.keyboard.press("Tab");
  await expect(closeButton).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(lastLink).toBeFocused();
});
