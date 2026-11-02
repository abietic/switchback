import type {
  ProjectionSnapshot,
} from "../api/contracts";
import { internalRejectViewContract } from "./viewRuntime";
import type {
  BoundViewProjectionRuntimeDecoder,
  DecodedContinuityDiscontinuity,
  DecodedIntegrityFailure,
  DecodedProjectionDelta,
  DecodedProjectionSnapshot,
  InternalViewContract,
  ProjectionSSEFrame,
  TrustedViewProjectionState,
} from "./viewRuntime";

interface TestViewData {
  status: string;
}

declare const decoder: BoundViewProjectionRuntimeDecoder<TestViewData>;
declare const frame: ProjectionSSEFrame;
declare const snapshotEnvelope: ProjectionSnapshot<TestViewData>;
declare const decodedSnapshot: DecodedProjectionSnapshot<TestViewData>;
declare const decodedDelta: DecodedProjectionDelta<TestViewData>;

const current: TrustedViewProjectionState<TestViewData> = {
  data: { status: "running" },
  availableActions: [],
  relatedRefs: [],
  viewEpoch: 1,
  viewCursor: 4,
  projectionVersion: 7,
};

// @ts-expect-error decoded Snapshot values require the runtime-private brand
const forgedSnapshot: DecodedProjectionSnapshot<TestViewData> = {
  envelope: snapshotEnvelope,
  effective: current,
  requestContext: { requestedClientCommandIds: [] },
};

// @ts-expect-error integrity evidence requires the runtime-private brand
const forgedIntegrityFailure: DecodedIntegrityFailure = {
  ingress: "delta",
  eventFamily: "projection_delta",
  replayable: true,
  key: "delta:1:5",
  safeReason: "malformed_contract",
};

// @ts-expect-error continuity evidence requires the runtime-private brand
const forgedDiscontinuity: DecodedContinuityDiscontinuity = {
  source: "delta",
  reason: "cursor_gap",
  key: "delta:cursor_gap:1:4:7:1:6:7:8",
  trusted: { viewEpoch: 1, viewCursor: 4, projectionVersion: 7 },
  incoming: {
    deltaId: "delta-6",
    viewEpoch: 1,
    viewCursor: 6,
    baseProjectionVersion: 7,
    resultProjectionVersion: 8,
  },
};

// @ts-expect-error a decoder bound to TestViewData cannot be widened to unrelated data
const mismatchedDecoder: BoundViewProjectionRuntimeDecoder<{ count: number }> = decoder;

declare const narrowStatusDecoder: BoundViewProjectionRuntimeDecoder<{ status: "running" }>;
// @ts-expect-error bound decoders are invariant across related literal data domains
const widenedStatusDecoder: BoundViewProjectionRuntimeDecoder<{
  status: "running" | "waiting_user";
}> = narrowStatusDecoder;

// @ts-expect-error no arbitrary per-call decoder callback crosses the bound runtime seam
decoder.decodeDeltaFrame(frame, current, (value: unknown) => value);

// @ts-expect-error decoded Snapshot arrays are deeply readonly
decodedSnapshot.envelope.available_actions.push({
  action: "worker.send_prompt",
  target_id: "session-1",
  expected_revision: 4,
});

// @ts-expect-error decoded Snapshot action objects are deeply readonly
decodedSnapshot.envelope.available_actions[0].expected_revision = 4;

// @ts-expect-error decoded Snapshot action metadata is deeply readonly
decodedSnapshot.envelope.available_actions[0].metadata!.mode = "attacker-rewritten";

// @ts-expect-error decoded effective data is deeply readonly
decodedSnapshot.effective.data.status = "attacker-rewritten";

// @ts-expect-error decoded Delta payload arrays are deeply readonly
decodedDelta.envelope.payload.related_refs?.push({
  type: "worker_detail",
  id: "session-2",
  href: "/api/v1/views/worker_detail/session-2",
});

// @ts-expect-error decoded effective reference objects are deeply readonly
decodedDelta.effective.relatedRefs[0].href = "/attacker-rewritten";

const asyncValidationContract: InternalViewContract<TestViewData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: ["worker_detail"],
  decodeData: () => ({ status: "running" }),
  // @ts-expect-error a state validator must complete synchronously with undefined
  async validateState() {},
};

const receiverBoundContract: InternalViewContract<TestViewData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: ["worker_detail"],
  // @ts-expect-error product decoders must not require a mutable contract receiver
  decodeData(this: { status: string }): TestViewData {
    return { status: this.status };
  },
  validateState: () => undefined,
};

const receiverBoundValidationContract: InternalViewContract<TestViewData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: ["worker_detail"],
  decodeData: () => ({ status: "running" }),
  // @ts-expect-error state validators must not require a mutable contract receiver
  validateState(this: { expectedStatus: string }): undefined {
    if (this.expectedStatus.length === 0) throw new Error("invalid receiver state");
    return undefined;
  },
};

interface NestedPromiseViewData {
  nested: {
    secret: Promise<string>;
  };
}

const nestedPromiseContract: InternalViewContract<NestedPromiseViewData> = {
  allowedDeltaEventTypes: [],
  canonicalRelatedRefTypes: [],
  // @ts-expect-error product data must be deeply synchronous, including nested fields
  decodeData: () => ({ nested: { secret: Promise.resolve("forbidden") } }),
  validateState: () => undefined,
};

interface NestedThenableViewData {
  nested: {
    deferred: { then(resolve: (value: string) => void): void };
  };
}

const nestedThenableContract: InternalViewContract<NestedThenableViewData> = {
  allowedDeltaEventTypes: [],
  canonicalRelatedRefTypes: [],
  // @ts-expect-error custom thenables are also asynchronous product data
  decodeData: () => ({
    nested: { deferred: { then(resolve: (value: string) => void) { resolve("value"); } } },
  }),
  validateState: () => undefined,
};

interface NestedFunctionViewData {
  nested: {
    mutate: (value: string) => void;
  };
}

const nestedFunctionContract: InternalViewContract<NestedFunctionViewData> = {
  allowedDeltaEventTypes: [],
  canonicalRelatedRefTypes: [],
  // @ts-expect-error executable fields cannot cross the projection data boundary
  decodeData: () => ({ nested: { mutate(_value: string) {} } }),
  validateState: () => undefined,
};

// @ts-expect-error product contracts can select only the closed safe-reason set
internalRejectViewContract("raw-product-parser-message");

void forgedSnapshot;
void forgedIntegrityFailure;
void forgedDiscontinuity;
void mismatchedDecoder;
void widenedStatusDecoder;
void decodedSnapshot;
void decodedDelta;
void asyncValidationContract;
void receiverBoundContract;
void receiverBoundValidationContract;
void nestedPromiseContract;
void nestedThenableContract;
void nestedFunctionContract;
