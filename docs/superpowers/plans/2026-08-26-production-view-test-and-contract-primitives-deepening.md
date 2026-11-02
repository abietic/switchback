# Production View Test and Contract Primitives Deepening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Isolate the Oxc Production View composition gate behind one test-only
interface and centralize three exact wire predicates while preserving every
runtime result, validation order, error classification and error string.

**Architecture:** `src/test/productionViewCompositionGuard.ts` owns all Oxc AST
implementation and one exhaustive Production View decoder manifest; one dedicated
test suite supplies sources and asserts violations. `src/api/contractPrimitives.ts`
owns only three pure boolean predicates. Existing ingress-local wrappers continue
to own labels, throwing policy and trusted `ViewProjectionDecodeError` mapping.

**Tech Stack:** TypeScript 7, Vitest 4, Vite 8/Oxc `parseAst`, Playwright 1.61,
Go 1.26 repository gates and PostgreSQL integration tests.

**Design authority:**
`docs/superpowers/specs/2026-08-26-production-view-test-and-contract-primitives-deepening-design.md`
at commit `d9c23bfbde0b7ca35109ab7701a4f757ab10b880`.

## Global Constraints

- This is an observationally equivalent refactor. Do not change accepted wire
  values, validation order, exact errors, error classification, safe reasons,
  provenance, View identities, decoder factories or allowed importer rules.
- Do not modify Go Projection wire structs, `api/schema/v1`, migrations,
  materializers, persisted fixtures, routes, controllers or `web-demo`.
- Preserve every pre-existing dirty path. Before each task require
  `git diff --cached --quiet`; stage only paths named by that task, inspect
  `git diff --cached --name-status`, and run `git diff --cached --check`.
- Do not add a schema framework, throwing shared validator, normalization,
  coercion, label parameter, maximum identity length or unrelated helper cleanup.
- Keep the composition module under `web/src/test`; production modules must not
  import it. Its only exports are the violation interface and finding function.
- The typed decoder manifest is the only decoder-module-path inventory inside the
  guard. Derive decoder importer permissions and path lookup from that manifest.
- Keep optional/null behavior and local wrappers local. Shared predicates only
  answer a value-level question and never throw.
- Record the implementation base before Task 1:

```bash
architecture_deepening_base=$(git rev-parse HEAD)
test -n "$architecture_deepening_base"
git diff --cached --quiet
```

- Every task ends with `git diff --check`, focused tests and an exact-path commit.
  Do not claim a gate passed without current command output.

---

### Task 1: Extract the Production View composition guard

**Files:**

- Create: `web/src/test/productionViewCompositionGuard.ts`
- Create: `web/src/projections/productionViewComposition.test.ts`
- Modify: `web/src/projections/viewRuntime.test.ts`

**Interfaces:**

- Consumes: `ProductionViewKind`, `ProductionViewType` and
  `ProductionViewDefinitionByKind` as type-only registry inputs; current source
  map fixtures as `Readonly<Record<string, string>>`.
- Produces: `findProductionViewCompositionViolations(sources): readonly
  ProductionViewCompositionViolation[]`, one exhaustive private decoder manifest
  and a behavior-only `viewRuntime.test.ts`.

- [ ] **Step 1: Write a failing seam test**

Create `productionViewComposition.test.ts` with its own `describe`, `expect` and
`it` imports. Import the not-yet-created function, load `viewRuntime.ts?raw`, and
add one legal unrelated source plus one direct forbidden internal import:

```typescript
import { describe, expect, it } from "vitest";
import runtimeSource from "./viewRuntime.ts?raw";
import { findProductionViewCompositionViolations } from "../test/productionViewCompositionGuard";

describe("projection runtime composition boundary", () => {
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
    expect(runtimeSource).not.toMatch(/^export interface ViewContract\\b/m);
  });
});
```

- [ ] **Step 2: Prove red because the guard module is absent**

```bash
npm --prefix web test -- src/projections/productionViewComposition.test.ts
```

Expected: fail with an unresolved `../test/productionViewCompositionGuard`
import. A syntax or fixture failure is not the expected red.

- [ ] **Step 3: Move the scanner behind the exported interface**

Move the current `compositionBoundaryViolations` implementation and every Oxc
helper it depends on from `viewRuntime.test.ts` into the new guard module. Import
`parseAst` there and expose only:

```typescript
export interface ProductionViewCompositionViolation {
  readonly path: string;
  readonly reason: string;
}

export function findProductionViewCompositionViolations(
  sources: Readonly<Record<string, string>>,
): readonly ProductionViewCompositionViolation[];
```

Keep `OxcNode`, walking/string helpers, alias propagation, import/export checks,
factory inspection, path normalization and violation accumulation private. Rename
the old top-level function but do not rewrite its algorithm or reason strings.

- [ ] **Step 4: Replace decoder path tables with one exhaustive manifest**

Import only registry types from `../projections/productionViewRegistry` and define:

```typescript
import type {
  ProductionViewDefinitionByKind,
  ProductionViewKind,
  ProductionViewType,
} from "../projections/productionViewRegistry";

interface DecoderFactorySpec {
  readonly modulePath: string;
  readonly factoryName: string;
  readonly dataTypeName: string;
  readonly viewType: ProductionViewType;
  readonly contractKeyPrefix: string;
  readonly dynamicId: boolean;
}

type DecoderFactoryManifest = {
  readonly [K in ProductionViewKind]: DecoderFactorySpec & {
    readonly viewType: ProductionViewDefinitionByKind[K]["viewType"];
  };
};

const DECODER_FACTORY_MANIFEST = {
  home: {
    modulePath: "src/projections/decoders/home.ts",
    factoryName: "createHomeViewDecoder",
    dataTypeName: "HomeDashboardData",
    viewType: "home_dashboard",
    contractKeyPrefix: "home-dashboard-v1:",
    dynamicId: false,
  },
  taskIndex: {
    modulePath: "src/projections/decoders/taskIndex.ts",
    factoryName: "createTaskIndexViewDecoder",
    dataTypeName: "TaskIndexData",
    viewType: "task_summary",
    contractKeyPrefix: "task-index-v1:",
    dynamicId: false,
  },
  assistant: {
    modulePath: "src/projections/decoders/assistant.ts",
    factoryName: "createAssistantViewDecoder",
    dataTypeName: "AssistantConversationData",
    viewType: "assistant_conversation",
    contractKeyPrefix: "assistant-conversation-v1:",
    dynamicId: true,
  },
  worker: {
    modulePath: "src/projections/decoders/worker.ts",
    factoryName: "createWorkerViewDecoder",
    dataTypeName: "WorkerDetailData",
    viewType: "worker_detail",
    contractKeyPrefix: "worker-detail-v1:",
    dynamicId: true,
  },
  artifact: {
    modulePath: "src/projections/decoders/artifact.ts",
    factoryName: "createArtifactViewDecoder",
    dataTypeName: "ArtifactDetailData",
    viewType: "artifact_detail",
    contractKeyPrefix: "artifact-detail-v1:",
    dynamicId: true,
  },
} satisfies DecoderFactoryManifest;
```

Derive a path-keyed lookup with `Object.fromEntries(Object.values(...).map(...))`
and derive decoder entries in `INTERNAL_COMPOSITION_IMPORTERS` from the same
manifest values. Retain only the three static non-decoder paths:
`viewRuntime.ts`, `viewRuntime.test.ts` and `viewRuntime.typecheck.ts`.

Add `COMPOSITION_TEST_PATH =
"src/projections/productionViewComposition.test.ts"`. Use it for the two test
exceptions previously tied to `viewRuntime.test.ts`: the permitted raw runtime
import and permitted `import.meta.glob`. Do not make it a general internal-symbol
importer.

- [ ] **Step 5: Move the full composition suite without weakening it**

Move the complete existing `describe("projection runtime composition boundary",
...)` block to `productionViewComposition.test.ts`. Replace calls with
`findProductionViewCompositionViolations`. Replace the one assertion that called
private `normalizeSourcePath(path)` with an `expectedPath` field in its test table;
keep path expectations, fixtures, reason behavior and all legal/illegal cases.

Load all repository sources from the new test:

```typescript
const consumerSources = import.meta.glob<string>("../**/*.{ts,tsx,mts,cts}", {
  eager: true,
  import: "default",
  query: "?raw",
});
```

Remove `parseAst`, `runtimeSource`, the moved suite and every Oxc helper from
`viewRuntime.test.ts`. Retain its internal composition imports because its
behavior tests still exercise that seam.

- [ ] **Step 6: Prove focused green and manifest exhaustiveness**

```bash
npm --prefix web test -- \
  src/projections/productionViewComposition.test.ts \
  src/projections/viewRuntime.test.ts
npm --prefix web run build
```

The build is the compile-time proof that every `ProductionViewKind` has a typed
manifest entry with the exact `viewType`.

- [ ] **Step 7: Prove the old file is behavior-only and paths are single-source**

```bash
test -z "$(rg -n 'parseAst|compositionBoundaryViolations|DECODER_FACTORY_SPECS|interface OxcNode' \
  web/src/projections/viewRuntime.test.ts || true)"
test "$(rg -o 'src/projections/decoders/(home|taskIndex|assistant|worker|artifact)\\.ts' \
  web/src/test/productionViewCompositionGuard.ts | wc -l | tr -d ' ')" = 5
git diff --check
```

Expected: no scanner symbol remains in `viewRuntime.test.ts`; each decoder path
appears once in the guard, in the manifest.

- [ ] **Step 8: Commit exactly the composition extraction**

```bash
git diff --cached --quiet
git add -- \
  web/src/test/productionViewCompositionGuard.ts \
  web/src/projections/productionViewComposition.test.ts \
  web/src/projections/viewRuntime.test.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: isolate Production View composition guard"
```

---

### Task 2: Centralize exact wire-contract predicates

**Files:**

- Create: `web/src/api/contractPrimitives.ts`
- Create: `web/src/api/contractPrimitives.test.ts`
- Create: `web/src/api/contracts.test.ts`
- Modify: `web/src/api/contracts.ts`
- Modify: `web/src/api/homeContracts.ts`
- Modify: `web/src/api/assistantContracts.ts`
- Modify: `web/src/projections/viewRuntime.ts`
- Modify: `web/src/projections/decoders/worker.ts`
- Modify: `web/src/projections/artifactAuthority.ts`
- Modify: `web/src/projections/decoders/taskIndex.ts`

**Interfaces:**

- Consumes: unknown wire values at seven existing ingress-local wrappers.
- Produces: `isCanonicalIdentity(value: unknown): value is string`,
  `isPositiveSafeInteger(value: unknown): value is number`, and
  `isNonNegativeSafeInteger(value: unknown): value is number`; all remain pure and
  non-throwing. Existing local wrappers remain the only error-policy interfaces.

- [ ] **Step 1: Characterize command-receipt identity behavior before refactoring**

Create `contracts.test.ts` around the exported `parseOptionalCommandReceipts`.
Use this complete characterization, including exact local error labels:

```typescript
import { describe, expect, it } from "vitest";
import { parseOptionalCommandReceipts } from "./contracts";

describe("optional command receipts", () => {
  it("preserves absence, valid pairs and identical duplicate collapse", () => {
    expect(parseOptionalCommandReceipts(undefined)).toBeUndefined();
    expect(parseOptionalCommandReceipts([
      { client_command_id: "client-1", command_id: "server-1" },
      { client_command_id: "client-1", command_id: "server-1" },
    ])).toEqual([
      { client_command_id: "client-1", command_id: "server-1" },
    ]);
  });

  it("preserves conflicting-pair rejection", () => {
    expect(() => parseOptionalCommandReceipts([
      { client_command_id: "client-1", command_id: "server-1" },
      { client_command_id: "client-1", command_id: "server-2" },
    ])).toThrow("command receipts are conflicting");
  });

  it.each(["", " client-1", "client-1 ", "client\r1", "client\n1", "client\0x", 42])(
    "preserves invalid client identity error for %j",
    (value) => {
      expect(() => parseOptionalCommandReceipts([
        { client_command_id: value, command_id: "server-1" },
      ])).toThrow("client command id is invalid");
    },
  );

  it.each(["", " server-1", "server-1 ", "server\r1", "server\n1", "server\0x", 42])(
    "preserves invalid server identity error for %j",
    (value) => {
      expect(() => parseOptionalCommandReceipts([
        { client_command_id: "client-1", command_id: value },
      ])).toThrow("server command id is invalid");
    },
  );
});
```

Run the characterization suite before any production edit:

```bash
npm --prefix web test -- src/api/contracts.test.ts
```

Expected: green against the current local wrapper. If it is not green, correct
the characterization rather than production behavior.

- [ ] **Step 2: Write independent predicate tables and prove red**

Create `contractPrimitives.test.ts` importing the three absent exports. Use literal
expectations, not the duplicated production helpers:

```typescript
import { describe, expect, it } from "vitest";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "./contractPrimitives";

describe("wire contract primitives", () => {
  it.each([
    ["id-1", true], ["", false], [" id", false], ["id ", false],
    ["id\r", false], ["id\n", false], ["id\0", false], [42, false],
  ])("classifies canonical identity %j", (value, expected) => {
    expect(isCanonicalIdentity(value)).toBe(expected);
  });

  it.each([
    [0, false, true], [1, true, true], [-1, false, false],
    [1.5, false, false], [Number.NaN, false, false],
    [Number.POSITIVE_INFINITY, false, false],
    [Number.MAX_SAFE_INTEGER + 1, false, false],
  ])("classifies safe integer %j", (value, positive, nonNegative) => {
    expect(isPositiveSafeInteger(value)).toBe(positive);
    expect(isNonNegativeSafeInteger(value)).toBe(nonNegative);
  });
});
```

```bash
npm --prefix web test -- src/api/contractPrimitives.test.ts
```

Expected: fail because `contractPrimitives.ts` does not exist.

- [ ] **Step 3: Implement exactly three pure exports**

Create `contractPrimitives.ts` with no imports and exactly this behavior:

```typescript
export function isCanonicalIdentity(value: unknown): value is string {
  return typeof value === "string"
    && value.length > 0
    && value.trim() === value
    && !/[\r\n\0]/.test(value);
}

export function isPositiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function isNonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}
```

Do not add throwing variants, labels, optional helpers or re-exports from a barrel.

- [ ] **Step 4: Delegate command-receipt parsing**

In `api/contracts.ts`, import `isCanonicalIdentity` from
`./contractPrimitives` and keep the wrapper/error boundary exactly:

```typescript
function requireCommandIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}
```

```bash
npm --prefix web test -- src/api/contracts.test.ts
```

Expected: the characterization suite stays green with identical errors.

- [ ] **Step 5: Delegate Home parsing**

In `api/homeContracts.ts`, import the canonical and non-negative predicates from
`./contractPrimitives`. Preserve the existing base parser calls:

```typescript
function requireCanonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function requireNonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}
```

```bash
npm --prefix web test -- src/api/homeContracts.test.ts
```

- [ ] **Step 6: Delegate Assistant parsing**

In `api/assistantContracts.ts`, import all three predicates from
`./contractPrimitives` and retain the current finite/positive base checks:

```typescript
function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = positive(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegative(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function canonicalIdentity(value: unknown, label: string): string {
  const identity = string(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}
```

```bash
npm --prefix web test -- src/api/assistantContracts.test.ts
```

- [ ] **Step 7: Delegate Task-index parsing**

In `projections/decoders/taskIndex.ts`, import the canonical and non-negative
predicates from `../../api/contractPrimitives`, retaining the existing base parser
calls and error labels:

```typescript
function requireCanonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function requireNonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}
```

```bash
npm --prefix web test -- src/projections/decoders/taskIndex.test.ts
```

- [ ] **Step 8: Delegate Worker parsing without changing cursor compatibility**

In `projections/decoders/worker.ts`, import all three predicates from
`../../api/contractPrimitives`. Use the shared canonical predicate only in the
ordinary labeled wrapper:

```typescript
function canonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegativeSafeInteger(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}
```

The existing private `isCanonicalIdentity(value: string)` is also used by the
decoded timeline-cursor payload, where it currently accepts an empty suffix.
Preserve that compatibility exactly by renaming it and its cursor call only:

```typescript
function isLegacyTimelineCursorPayloadIdentity(value: string): boolean {
  return value.trim() === value && !/[\r\n\0]/.test(value);
}
```

Do not replace this legacy-only check with the stricter shared predicate.

```bash
npm --prefix web test -- src/projections/decoders/worker.test.ts
```

- [ ] **Step 9: Delegate Artifact authority parsing**

In `projections/artifactAuthority.ts`, import all three predicates from
`../api/contractPrimitives` and keep authority parsing order:

```typescript
function canonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegativeSafeInteger(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}
```

Do not consolidate the optional manifest-size compound check at its current call
site because it has different optionality and surrounding error semantics.

```bash
npm --prefix web test -- src/projections/decoders/artifact.test.ts
```

- [ ] **Step 10: Delegate runtime trust-boundary checks**

In `projections/viewRuntime.ts`, import all three predicates from
`../api/contractPrimitives`, remove only its private `isCanonicalIdentity`, and
retain the trusted error adapter:

```typescript
function requirePositiveSafeInteger(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): number {
  if (!isPositiveSafeInteger(value)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value;
}

function requireNonNegativeSafeInteger(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): number {
  if (!isNonNegativeSafeInteger(value)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value;
}
```

`requireCanonicalTrustedIdentity` and runtime `requireCanonicalIdentity` keep their
current bodies and now resolve the imported predicate. Do not touch replay-id
parsing or the transient-display check in `frontierViewController.ts`.

```bash
npm --prefix web test -- src/projections/viewRuntime.test.ts
```

- [ ] **Step 11: Prove the predicate module and equivalence oracles green**

```bash
npm --prefix web test -- \
  src/api/contractPrimitives.test.ts \
  src/api/contracts.test.ts \
  src/api/homeContracts.test.ts \
  src/api/assistantContracts.test.ts \
  src/projections/decoders/taskIndex.test.ts \
  src/projections/decoders/worker.test.ts \
  src/projections/decoders/artifact.test.ts \
  src/projections/viewRuntime.test.ts
npm --prefix web run build
```

No test fixture, snapshot or expected error may be rewritten to accommodate the
refactor.

- [ ] **Step 12: Prove duplicated rule bodies are gone only from in-scope callers**

```bash
test -z "$(rg -n \
  'identity\.trim\(\) !== identity|!Number\.isSafeInteger\(parsed\)' \
  web/src/api/contracts.ts \
  web/src/api/homeContracts.ts \
  web/src/api/assistantContracts.ts \
  web/src/projections/viewRuntime.ts \
  web/src/projections/decoders/worker.ts \
  web/src/projections/artifactAuthority.ts \
  web/src/projections/decoders/taskIndex.ts || true)"
test "$(rg -l 'from ".*contractPrimitives"' \
  web/src/api/contracts.ts \
  web/src/api/homeContracts.ts \
  web/src/api/assistantContracts.ts \
  web/src/projections/viewRuntime.ts \
  web/src/projections/decoders/worker.ts \
  web/src/projections/artifactAuthority.ts \
  web/src/projections/decoders/taskIndex.ts | wc -l | tr -d ' ')" = 7
test "$(rg -n '^export function is(CanonicalIdentity|PositiveSafeInteger|NonNegativeSafeInteger)' \
  web/src/api/contractPrimitives.ts | wc -l | tr -d ' ')" = 3
git diff --check
```

The preserved `isLegacyTimelineCursorPayloadIdentity` body is intentionally not a
duplicate canonical-identity contract: it retains the cursor path's existing empty
suffix behavior. Inspect unexpected matches rather than weakening the assertion.

- [ ] **Step 13: Commit exactly the predicate extraction**

```bash
git diff --cached --quiet
git add -- \
  web/src/api/contractPrimitives.ts \
  web/src/api/contractPrimitives.test.ts \
  web/src/api/contracts.ts \
  web/src/api/contracts.test.ts \
  web/src/api/homeContracts.ts \
  web/src/api/assistantContracts.ts \
  web/src/projections/viewRuntime.ts \
  web/src/projections/decoders/worker.ts \
  web/src/projections/artifactAuthority.ts \
  web/src/projections/decoders/taskIndex.ts
git diff --cached --name-status
git diff --cached --check
git commit -m "refactor: centralize projection contract primitives"
```

---

### Task 3: Review the fixed range and run completion gates

**Files:**

- Modify only files from Tasks 1–2 if review finds a proven Critical or Important
  defect.
- Do not create cleanup commits for unrelated warnings or pre-existing dirty paths.

**Interfaces:**

- Consumes: the two independently committed deliverables from Tasks 1–2 and the
  fixed `$architecture_deepening_base..HEAD` review range.
- Produces: verified gate evidence and a finding disposition; no new runtime API.

- [ ] **Step 1: Inspect the exact implementation range and protected paths**

```bash
git status --short
git diff --name-status "$architecture_deepening_base"..HEAD
git diff --check "$architecture_deepening_base"..HEAD
test -z "$(git diff --name-only "$architecture_deepening_base"..HEAD -- \
  '*.go' 'api/schema/**' 'internal/**/migrations/**' 'web-demo/**')"
```

Confirm the range contains only the approved test extraction, predicate module,
seven delegates and their tests. Confirm the user's pre-existing dirty paths are
still unstaged.

- [ ] **Step 2: Run independent Standards and Spec reviews**

Use the `code-review` skill against the fixed range
`$architecture_deepening_base..HEAD`, dispatching its Standards and Spec reviewers
in parallel as required by that skill. Give the Spec reviewer the design authority
commit above. Treat findings as hypotheses: reproduce each against current source
and tests before changing code.

- [ ] **Step 3: Resolve every proven Critical or Important finding with TDD**

For each proven issue, add or tighten the smallest focused test, observe red, apply
the smallest in-scope fix, rerun the focused test and commit only the affected task
paths. Record rejected findings with source evidence. Do not expand into optional
cleanup.

- [ ] **Step 4: Run full Web, browser and build gates**

```bash
npm --prefix web test
npm --prefix web run test:browser
npm --prefix web run build
```

Record Vitest file/test counts, Playwright passed/skipped/failed counts and Vite
warnings separately from failures.

- [ ] **Step 5: Run the uncached repository verification gate**

First prove the configured PostgreSQL endpoint responds, then run uncached verbose
tests so skipped integration coverage is visible:

```bash
SWITCHBACK_TEST_DATABASE_URL='postgresql:///postgres?host=/tmp' \
  psql 'postgresql:///postgres?host=/tmp' -Atqc 'select 1'
GOFLAGS='-count=1' SWITCHBACK_TEST_DATABASE_URL='postgresql:///postgres?host=/tmp' \
  make verify V=1
```

Inspect output for `FAIL`, `SKIP` and unavailable-database fallbacks. If PostgreSQL
is unavailable, report the repository gate as blocked rather than green.

- [ ] **Step 6: Perform the final scope and worktree audit**

```bash
git diff --check "$architecture_deepening_base"..HEAD
git diff --cached --quiet
git status --short
git log --oneline "$architecture_deepening_base"..HEAD
```

The final report must state:

- the two architecture outcomes and exact commits;
- focused/full Vitest, Playwright, build and repository-gate results;
- build warnings separately from failures;
- Standards and Spec review results, including rejected-noise evidence;
- that no Go/schema/migration/materializer/browser behavior changed;
- that unrelated dirty paths remained untouched and unstaged.
