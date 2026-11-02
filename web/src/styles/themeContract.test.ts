import { describe, expect, it } from "vitest";
// @ts-expect-error Vitest executes tests in Node; production code remains browser-only.
import { readFileSync } from "node:fs";

const applicationStyles = readFileSync("src/styles/application.css", "utf8");
const baseStyles = readFileSync("src/styles/base.css", "utf8");
const contentStyles = readFileSync("src/styles/content.css", "utf8");
const tokens = readFileSync("src/styles/tokens.css", "utf8");
const workerDetailSource = readFileSync("src/features/worker/WorkerDetailPage.tsx", "utf8");
const workerTimelineSource = readFileSync("src/features/worker/WorkerTimeline.tsx", "utf8");

const requiredTokens = [
  "background",
  "foreground",
  "surface",
  "card",
  "muted",
  "muted-foreground",
  "border",
  "input",
  "ring",
  "primary",
  "primary-foreground",
  "destructive",
  "destructive-foreground",
  "sidebar",
  "sidebar-foreground",
  "sidebar-border",
  "status-neutral",
  "status-active",
  "status-success",
  "status-waiting",
  "status-danger",
  "surface-subtle",
  "surface-hover",
  "foreground-soft",
  "status-neutral-surface",
  "status-neutral-border",
  "status-active-surface",
  "status-active-border",
  "status-success-surface",
  "status-success-border",
  "status-waiting-surface",
  "status-waiting-border",
  "status-danger-surface",
  "status-danger-border",
  "shadow-elevated",
  "diff-add-background",
  "diff-add-foreground",
  "diff-remove-background",
  "diff-remove-foreground",
  "diff-hunk-background",
  "diff-hunk-foreground",
];

const themeAliasTokens = [
  "background",
  "foreground",
  "surface",
  "card",
  "muted",
  "muted-foreground",
  "border",
  "input",
  "green",
  "green-soft",
  "amber",
  "amber-soft",
  "red",
  "red-soft",
  "blue",
];

function declarations(block: string) {
  return new Map(
    [...block.matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)].map((match) => [match[1], match[2].trim()]),
  );
}

describe("production theme CSS contract", () => {
  it("defines a complete non-empty semantic palette for dark and light", () => {
    const darkBlock = tokens.match(/:root,\s*:root\[data-theme="dark"\]\s*\{([^}]+)\}/)?.[1] ?? "";
    const lightBlock = tokens.match(/:root\[data-theme="light"\]\s*\{([^}]+)\}/)?.[1] ?? "";
    const dark = declarations(darkBlock);
    const light = declarations(lightBlock);
    let distinct = 0;

    for (const token of requiredTokens) {
      expect(dark.get(token), `dark --${token}`).toBeTruthy();
      expect(light.get(token), `light --${token}`).toBeTruthy();
      if (light.get(token) !== dark.get(token)) {
        distinct += 1;
      }
    }
    expect(distinct).toBeGreaterThanOrEqual(requiredTokens.length - 2);
  });

  it("keeps production styles on semantic colors", () => {
    const productionStyles = `${applicationStyles}\n${baseStyles}\n${contentStyles}`;
    expect(productionStyles).not.toMatch(/\bcolor\s*:\s*var\(--muted\)/);
    expect(productionStyles).not.toMatch(/#[\da-f]{3,8}\b/iu);
    expect(productionStyles).not.toMatch(/\b(?:background|color|border(?:-color)?)\s*:\s*(?:white|black)\b/iu);
    expect(productionStyles).not.toMatch(/\brgba?\(/iu);
    for (const token of themeAliasTokens) {
      expect(productionStyles, `production styles must not redefine --${token}`).not.toMatch(
        new RegExp(`--${token}\\s*:`),
      );
    }
  });

  it("contains the reduced-motion override required by the presentation contract", () => {
    expect(baseStyles).toContain("@media (prefers-reduced-motion: reduce)");
    expect(baseStyles).toMatch(/animation-duration:\s*0\.01ms\s*!important/);
    expect(baseStyles).toMatch(/animation-iteration-count:\s*1\s*!important/);
    expect(baseStyles).toMatch(/transition-duration:\s*0\.01ms\s*!important/);
  });

  it("has no permanent Worker migration flag or disabled legacy UI path", () => {
    expect(workerDetailSource).not.toContain(["VITE", "WORKER", "WORKBENCH", "ENABLED"].join("_"));
    expect(workerDetailSource).not.toContain(["Legacy", "Artifact", "List"].join(""));
    expect(workerTimelineSource).not.toContain(["legacy", "Icons"].join(""));
    expect(workerTimelineSource).not.toMatch(/\blegacy\s*=/);
  });
});
