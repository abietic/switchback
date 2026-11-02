import { describe, expect, it } from "vitest";
// @ts-expect-error Vitest executes tests in Node; production code remains browser-only.
import { readFileSync } from "node:fs";
// @ts-expect-error Vitest executes tests in Node; production code remains browser-only.
import { execFileSync } from "node:child_process";

const workerBuildFlag = ["VITE", "WORKER", "WORKBENCH", "ENABLED"].join("_");
const disabledArtifactComponent = ["Legacy", "Artifact", "List"].join("");
const activeOwnerSources = [
  "../deploy/local/compose.yaml",
  "../images/web/Dockerfile",
  "../docs/architecture/m1-worker-delivery-operations.md",
  "../docs/architecture/m2-assistant-task-dispatch-manual-acceptance.md",
  "../docs/architecture/m2-assistant-task-dispatch.md",
].map((path) => readFileSync(path, "utf8")).join("\n");
const webContracts = readFileSync("src/api/contracts.ts", "utf8");
const workerMaterializer = readFileSync("../internal/eventingprojection/adapter/postgres/materializer.go", "utf8");

function trackedMatches(needle: string): string {
  try {
    return execFileSync("git", ["grep", "-n", "-F", "--", needle], {
      cwd: "..",
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (cause) {
    if ((cause as { status?: number }).status === 1) return "";
    throw cause;
  }
}

describe("production migration closeout contract", () => {
  it("has no permanent Workbench build flag or disabled Artifact UI owner", () => {
    expect(activeOwnerSources).not.toContain(workerBuildFlag);
    expect(activeOwnerSources).not.toContain(disabledArtifactComponent);
    expect(trackedMatches(workerBuildFlag)).toBe("");
    expect(trackedMatches(disabledArtifactComponent)).toBe("");
  });

  it("projects only the bounded grouped Worker timeline contract", () => {
    expect(webContracts).not.toMatch(/export interface TimelineEntry/);
    expect(webContracts).not.toMatch(/^\s+timeline:\s+TimelineEntry\[\];$/m);
    expect(workerMaterializer).not.toMatch(/"timeline":\s*timeline/);
    expect(workerMaterializer).not.toMatch(/func \(m Materializer\) timeline\(/);
    expect(workerMaterializer).toContain('"timeline_groups": timelinePage.Turns');
  });
});
