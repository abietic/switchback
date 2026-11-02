import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { installFrontierEventHarness, mockFrontier } from "./frontierMock";

const variants = [
  { name: "desktop-dark", width: 1440, height: 900, theme: "dark" },
  { name: "desktop-light", width: 1440, height: 900, theme: "light" },
  { name: "mobile-dark", width: 390, height: 844, theme: "dark" },
  { name: "mobile-light", width: 390, height: 844, theme: "light" },
] as const;

const surfaces = [
  { name: "home", path: "/" },
  { name: "assistant", path: "/assistant/conversation-browser" },
  { name: "worker", path: "/workers/session-local-01" },
  {
    name: "diff",
    path: "/workers/session-local-01",
    prepare: async (page: Page) => page.getByRole("button", { name: "Diff" }).click(),
  },
] as const;

for (const variant of variants) {
  for (const surface of surfaces) {
    test(`${surface.name} has a bounded ${variant.name} presentation`, async ({ page }, testInfo) => {
      await installFrontierEventHarness(page);
      await mockFrontier(page);
      await page.setViewportSize({ width: variant.width, height: variant.height });
      await page.goto("/");
      await page.evaluate((theme) => localStorage.setItem("switchback.theme", theme), variant.theme);

      await page.goto(surface.path);
      await surface.prepare?.(page);
      await expect(page.locator("body")).toBeVisible();
      await expect(page.locator("html")).toHaveAttribute("data-theme", variant.theme);
      await expectNoDocumentOverflow(page);
      await expectNoClippedButtons(page);

      if (surface.name === "diff") {
        const scroller = page.getByLabel("Patch diff");
        await expect(scroller).toBeVisible();
        const bounds = await scroller.evaluate((element) => ({
          label: element.getAttribute("aria-label"),
          overflowX: getComputedStyle(element).overflowX,
          bounded: element.clientWidth <= document.documentElement.clientWidth,
        }));
        expect(bounds.label).toBe("Patch diff");
        expect(bounds.overflowX).toBe("auto");
        expect(bounds.bounded).toBe(true);
      }

      await attachCapture(page, testInfo, `${surface.name}-${variant.name}`);
    });
  }
}

async function expectNoDocumentOverflow(page: Page) {
  const layout = await page.evaluate(() => ({
    bodyWidth: document.body.scrollWidth,
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: document.documentElement.clientWidth,
  }));
  expect(layout.bodyWidth).toBeLessThanOrEqual(layout.viewportWidth + 1);
  expect(layout.documentWidth).toBeLessThanOrEqual(layout.viewportWidth + 1);
}

async function expectNoClippedButtons(page: Page) {
  const clippedButtons = await page.locator("button:visible").evaluateAll((buttons) =>
    buttons
      .filter((button) => button.scrollWidth > button.clientWidth + 1)
      .map((button) => button.getAttribute("aria-label") ?? button.textContent?.trim() ?? "button"),
  );
  expect(clippedButtons).toEqual([]);
}

async function attachCapture(page: Page, testInfo: TestInfo, name: string) {
  const screenshot = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path: screenshot, fullPage: true });
  await testInfo.attach(name, { path: screenshot, contentType: "image/png" });
}
