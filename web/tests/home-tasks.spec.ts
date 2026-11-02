import { expect, test } from "@playwright/test";
import type { HomeDashboardData } from "../src/api/homeContracts";
import { demoHomeDashboard, homeSnapshot, mockFrontier } from "./frontierMock";

test("Home renders authoritative states and canonical deep links", async ({ page }) => {
  await mockFrontier(page);
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Needs your attention" })).toBeVisible();
  const attention = page.getByRole("region", { name: "Needs your attention" });
  const workers = page.getByRole("region", { name: "Recent Worker sessions" });
  await expect(attention.getByRole("link", { name: /Confirm the Home dashboard contract/ }))
    .toHaveAttribute("href", "/workers/worker-home-idle");
  await expect(workers.getByRole("link", { name: /worker-home-idle/ }))
    .toHaveAttribute("href", "/workers/worker-home-idle");
  await expect(page.getByRole("link", { name: "report artifact-home-report" }))
    .toHaveAttribute("href", "/artifacts/artifact-home-report");
  await expect(page.getByText(/manual acceptance/)).toBeVisible();
  await expect(page.getByRole("link", { name: "Start Assistant conversation" }))
    .toHaveAttribute("href", "/assistant");
  await expect(page.getByRole("link", { name: "View all tasks" }))
    .toHaveAttribute("href", "/tasks");

  await page.getByRole("link", { name: "report artifact-home-report" }).click();
  await expect(page).toHaveURL(/\/artifacts\/artifact-home-report$/);
});

test("Home exposes loading, empty, and retryable error DOM without sample records", async ({ page }) => {
  await mockFrontier(page);
  let release!: () => void;
  const delayed = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/views/home_dashboard/index", async (route) => {
    await delayed;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(homeSnapshot(emptyHome())),
    });
  });

  await page.goto("/");
  await expect(page.getByRole("status", { name: "Loading Home dashboard" })).toHaveAttribute("aria-busy", "true");
  release();
  await expect(page.getByText("No tasks need your attention.")).toBeVisible();
  await expect(page.getByText("No recent Worker sessions.")).toBeVisible();
  await expect(page.getByText("No recent deliveries.")).toBeVisible();
  await expect(page.getByText("Confirm the Home dashboard contract")).toHaveCount(0);

  let attempts = 0;
  await page.unroute("**/api/v1/views/home_dashboard/index");
  await page.route("**/api/v1/views/home_dashboard/index", async (route) => {
    attempts += 1;
    // React StrictMode starts two controllers during the initial mount. Both
    // must fail so only the live controller owns the visible error.
    if (attempts <= 2) {
      await route.fulfill({ status: 503, contentType: "text/plain", body: "unavailable" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(homeSnapshot()),
    });
  });
  await page.reload();
  await expect(page.getByRole("alert"))
    .toContainText("Frontier snapshot is temporarily unavailable.");
  await page.getByRole("button", { name: "Retry Home dashboard" }).click();
  await expect(page.getByRole("heading", { name: "Needs your attention" })).toBeVisible();
  expect(attempts).toBe(3);
});

test("Home and Tasks remain responsive and keep their single authoritative View identities", async ({ page }) => {
  const views: string[] = [];
  page.on("request", (request) => {
    const match = new URL(request.url()).pathname.match(/^\/api\/v1\/views\/([^/]+)\/([^/]+)$/);
    if (match) views.push(`${match[1]}/${match[2]}`);
  });
  await mockFrontier(page);

  for (const [path, expectedView] of [["/", "home_dashboard/index"], ["/tasks", "task_summary/index"]] as const) {
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 });
      await page.goto(path);
      await expect(page.locator("body")).toBeVisible();
      expect(await page.evaluate(() => document.body.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true);
    }
    expect(views.filter((view) => view === expectedView).length).toBeGreaterThan(0);
  }

  expect(views).toContain("home_dashboard/index");
  expect(views).toContain("task_summary/index");
});

function emptyHome(): HomeDashboardData {
  return {
    ...demoHomeDashboard,
    attention_tasks: [],
    recent_workers: [],
    recent_deliveries: [],
    capacity: { used: 0, limit: 10 },
  };
}
