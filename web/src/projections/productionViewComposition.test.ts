import { describe, expect, it } from "vitest";
import runtimeSource from "./viewRuntime.ts?raw";
import workerSource from "./decoders/worker.ts?raw";
import { findProductionViewCompositionViolations } from "../test/productionViewCompositionGuard";

describe("projection runtime composition boundary", () => {
  it("allows only the exact pure Worker timeline page parser beside the closed factory", () => {
    expect(findProductionViewCompositionViolations({ "./decoders/worker.ts": workerSource })).toEqual([]);
  });

  it.each([
    ["wrong name", "export function parseWorkerTimelinePage(value: unknown): WorkerTimelinePage", "export function arbitraryParser(value: unknown): WorkerTimelinePage"],
    ["wrong input", "parseWorkerTimelinePage(value: unknown)", "parseWorkerTimelinePage(value: any)"],
    ["wrong return", "parseWorkerTimelinePage(value: unknown): WorkerTimelinePage", "parseWorkerTimelinePage(value: unknown): unknown"],
    ["async", "export function parseWorkerTimelinePage", "export async function parseWorkerTimelinePage"],
    ["composition", "const page = requireRecord(value", "internalCreateViewProjectionRuntimeDecoder({}); const page = requireRecord(value"],
    ["composition alias", "const page = requireRecord(value", "const alias = internalCreateViewProjectionRuntimeDecoder; alias({}); const page = requireRecord(value"],
    ["factory alias", "const page = requireRecord(value", "const alias = createWorkerViewDecoder; alias(\"other\"); const page = requireRecord(value"],
  ])("rejects timeline parser %s escape", (_label, before, after) => {
    expect(workerSource).toContain(before);
    expect(findProductionViewCompositionViolations({ "./decoders/worker.ts": workerSource.replace(before, after) })).toEqual([
      expect.objectContaining({ path: "src/projections/decoders/worker.ts" }),
    ]);
  });
  it("keeps the guard seam closed", () => {
    expect(findProductionViewCompositionViolations({
      "../app/SafeConsumer.tsx": "export const safe = true;",
    })).toEqual([]);
    expect(findProductionViewCompositionViolations({
      "../app/UnsafeConsumer.tsx":
        `import { internalCreateViewProjectionRuntimeDecoder } from "../projections/viewRuntime";`,
    })).toEqual([
      expect.objectContaining({ path: "src/app/UnsafeConsumer.tsx" }),
    ]);
    expect(runtimeSource).not.toMatch(/^export interface ViewContract\b/m);
    expect(runtimeSource).not.toMatch(/^export function createViewProjectionRuntimeDecoder\b/m);
    expect(runtimeSource).not.toMatch(/^export function rejectViewContract\b/m);

    const consumerSources = import.meta.glob<string>("../**/*.{ts,tsx,mts,cts}", {
      eager: true,
      import: "default",
      query: "?raw",
    });
    expect(findProductionViewCompositionViolations(consumerSources)).toEqual([]);
  });

  it.each([
    {
      name: "direct internal import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `import { internalCreateViewProjectionRuntimeDecoder } from "../projections/viewRuntime";`,
    },
    {
      name: "aliased internal import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `import { internalCreateViewProjectionRuntimeDecoder as makeDecoder } from "../projections/viewRuntime";`,
    },
    {
      name: "string-literal aliased internal import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `import { "internalCreateViewProjectionRuntimeDecoder" as makeDecoder } from "../projections/viewRuntime";`,
    },
    {
      name: "production registry generic import",
      path: "./productionViewRegistry.ts",
      expectedPath: "src/projections/productionViewRegistry.ts",
      source: `import { internalCreateViewProjectionRuntimeDecoder } from "./viewRuntime";`,
    },
    {
      name: "literal dynamic import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `void import("../projections/viewRuntime");`,
    },
    {
      name: "constant-folded dynamic import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `void import("../projections/" + "viewRuntime");`,
    },
    {
      name: "module-const dynamic import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `const runtimePath = "../projections/viewRuntime"; void import(runtimePath);`,
    },
    {
      name: "projection glob import",
      path: "../app/UnsafeConsumer.tsx",
      expectedPath: "src/app/UnsafeConsumer.tsx",
      source: `const modules = import.meta.glob("../projections/*.ts");`,
    },
  ])("rejects $name outside the exact importer allowlist", ({ expectedPath, path, source }) => {
    expect(findProductionViewCompositionViolations({ [path]: source })).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ path: expectedPath }),
      ]),
    );
  });

  it("allows an unrelated non-literal dynamic import", () => {
    expect(findProductionViewCompositionViolations({
      "../app/LazyConsumer.tsx": `const modulePath = chooseFeatureModule(); void import(modulePath);`,
    })).toEqual([]);
  });

  it.each([
    {
      name: "aliased named re-export",
      source: `export { internalCreateViewProjectionRuntimeDecoder as makeDecoder } from "../viewRuntime";`,
    },
    {
      name: "star re-export",
      source: `export * from "../viewRuntime";`,
    },
    {
      name: "namespace re-export",
      source: `export * as runtime from "../viewRuntime";`,
    },
    {
      name: "namespace import barrel",
      source: `import * as runtime from "../viewRuntime"; export { runtime };`,
    },
    {
      name: "local aliased re-export",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime"
        export { compose as makeDecoder }
      `,
    },
    {
      name: "default alias re-export",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        export default compose;
      `,
    },
    {
      name: "exported variable alias",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        export const makeDecoder = compose;
      `,
    },
    {
      name: "transitive local alias re-export",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        const alias = compose;
        export { alias as makeDecoder };
      `,
    },
    {
      name: "multi-declarator default alias",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        const harmless = 0, makeDecoder = compose;
        export default makeDecoder;
      `,
    },
    {
      name: "ASI type-asserted default alias",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime"
        const alias = compose as typeof compose
        export default alias
      `,
    },
    {
      name: "non-null exported variable alias",
      source: `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        export const makeDecoder = compose!;
      `,
    },
    {
      name: "generic contract type alias",
      source: `
        import type { InternalViewContract as Contract } from "../viewRuntime";
        export type UnsafeContract<T> = Contract<T>;
      `,
    },
  ])("rejects $name even from an allowlisted decoder", ({ source }) => {
    expect(findProductionViewCompositionViolations({ "./decoders/home.ts": source })).toEqual([
      expect.objectContaining({ path: "src/projections/decoders/home.ts" }),
    ]);
  });

  it("rejects a source-local alias export of an internal composition symbol", () => {
    expect(findProductionViewCompositionViolations({
      "./viewRuntime.ts": `
        export function internalCreateViewProjectionRuntimeDecoder() {}
        export { internalCreateViewProjectionRuntimeDecoder as makeDecoder };
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/viewRuntime.ts" }),
    ]);
  });

  it("rejects new runtime values and aliases outside the exact viewRuntime export allowlist", () => {
    expect(findProductionViewCompositionViolations({
      "./viewRuntime.ts": `
        export class ViewProjectionDecodeError extends Error {}
        export function internalRejectViewContract(): never { throw new Error(); }
        export function internalCreateViewProjectionRuntimeDecoder() {}
        export const unsafeRuntimeValue = internalCreateViewProjectionRuntimeDecoder;
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/viewRuntime.ts" }),
    ]);
  });

  it("requires the exact runtime export declaration kinds", () => {
    expect(findProductionViewCompositionViolations({
      "./viewRuntime.ts": `
        export function ViewProjectionDecodeError() {}
        export class internalRejectViewContract {}
        export class internalCreateViewProjectionRuntimeDecoder {}
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/viewRuntime.ts" }),
    ]);
  });

  it("blocks the reviewer alias-re-export escape before an app consumer can call it", () => {
    const violations = findProductionViewCompositionViolations({
      "../app/UnsafeConsumer.tsx": `
        import { makeDecoder } from "../projections/unsafe";
        export const decoder = makeDecoder({ contract: arbitraryContract });
      `,
      "./unsafe.ts": `
        export {
          internalCreateViewProjectionRuntimeDecoder as makeDecoder,
        } from "./viewRuntime";
      `,
    });
    expect([...new Set(violations.map(({ path }) => path))]).toEqual([
      "src/projections/unsafe.ts",
    ]);
  });

  it("allows exact closed Home and Assistant factories", () => {
    expect(findProductionViewCompositionViolations({
      "./decoders/home.ts": `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        import type {
          BoundViewProjectionRuntimeDecoder,
          InternalViewContract,
        } from "../viewRuntime";
        import type { HomeDashboardData } from "../../api/homeContracts";
        const homeContract: InternalViewContract<HomeDashboardData> = {
          allowedDeltaEventTypes: [],
          canonicalRelatedRefTypes: [],
          decodeData: parseHomeData,
          validateState: validateHomeState,
        };
        export function createHomeViewDecoder(): BoundViewProjectionRuntimeDecoder<HomeDashboardData> {
          return compose({
            contractKey: "home-dashboard-v1:index",
            viewType: "home_dashboard",
            viewId: "index",
            contract: homeContract,
          });
        }
      `,
      "./decoders/assistant.ts": `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        import type {
          BoundViewProjectionRuntimeDecoder,
          InternalViewContract,
        } from "../viewRuntime";
        import type { AssistantConversationData } from "../../api/contracts";
        export function createAssistantViewDecoder(
          conversationId: string,
        ): BoundViewProjectionRuntimeDecoder<AssistantConversationData> {
          const contract: InternalViewContract<AssistantConversationData> = {
            allowedDeltaEventTypes: [],
            canonicalRelatedRefTypes: [],
            decodeData: parseAssistantData,
            validateState: validateAssistantState,
          };
          return compose({
            contractKey: \`assistant-conversation-v1:\${conversationId}\`,
            viewType: "assistant_conversation",
            viewId: conversationId,
            contract,
          });
        }
      `,
    })).toEqual([]);
  });

  it("rejects a generic forwarding wrapper at an exact decoder path", () => {
    expect(findProductionViewCompositionViolations({
      "./decoders/assistant.ts": `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        export function createAssistantViewDecoder<T>(
          options: { viewType: string; viewId: string; contract: unknown },
        ) {
          return compose(options);
        }
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/decoders/assistant.ts" }),
    ]);
  });

  it.each([
    {
      name: "an imported product contract",
      setup: `import { assistantContract } from "./sharedContract";`,
      contractKey: `\`assistant-conversation-v1:\${conversationId}\``,
    },
    {
      name: "a local alias of an imported product contract",
      setup: `
        import { arbitraryContract } from "./sharedContract";
        const assistantContract = arbitraryContract;
      `,
      contractKey: `\`assistant-conversation-v1:\${conversationId}\``,
    },
    {
      name: "an arbitrary contract-key prefix",
      setup: `
        const assistantContract: InternalViewContract<AssistantConversationData> = {
          allowedDeltaEventTypes: [],
          canonicalRelatedRefTypes: [],
          decodeData: parseAssistantData,
          validateState: validateAssistantState,
        };
      `,
      contractKey: `\`arbitrary-v9:\${conversationId}\``,
    },
  ])("rejects $name in an otherwise-shaped factory", ({ setup, contractKey }) => {
    expect(findProductionViewCompositionViolations({
      "./decoders/assistant.ts": `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        import type {
          BoundViewProjectionRuntimeDecoder,
          InternalViewContract,
        } from "../viewRuntime";
        import type { AssistantConversationData } from "../../api/contracts";
        ${setup}
        export function createAssistantViewDecoder(
          conversationId: string,
        ): BoundViewProjectionRuntimeDecoder<AssistantConversationData> {
          return compose({
            contractKey: ${contractKey},
            viewType: "assistant_conversation",
            viewId: conversationId,
            contract: assistantContract,
          });
        }
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/decoders/assistant.ts" }),
    ]);
  });

  it("rejects additional runtime exports from an exact decoder", () => {
    expect(findProductionViewCompositionViolations({
      "./decoders/home.ts": `
        import { internalCreateViewProjectionRuntimeDecoder as compose } from "../viewRuntime";
        import type { BoundViewProjectionRuntimeDecoder } from "../viewRuntime";
        import type { HomeDashboardData } from "../../api/homeContracts";
        const homeContract = fixedHomeContract;
        export function createHomeViewDecoder(): BoundViewProjectionRuntimeDecoder<HomeDashboardData> {
          return compose({
            contractKey: "home-dashboard-v1:index",
            viewType: "home_dashboard",
            viewId: "index",
            contract: homeContract,
          });
        }
        export const makeArbitraryDecoder = compose;
      `,
    })).toEqual([
      expect.objectContaining({ path: "src/projections/decoders/home.ts" }),
    ]);
  });
});
