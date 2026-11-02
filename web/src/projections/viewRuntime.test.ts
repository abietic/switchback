import { describe, expect, it } from "vitest";
import {
  ViewProjectionDecodeError,
  internalCreateViewProjectionRuntimeDecoder,
  internalRejectViewContract,
  type DecodedDeltaDecision,
  type DecodedProjectionDelta,
  type InternalViewContract,
  type ProjectionSSEFrame,
  type TrustedViewProjectionState,
} from "./viewRuntime";

interface TestViewData {
  status: string;
}

const contract: InternalViewContract<TestViewData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: ["worker_detail"],
  decodeData(value: unknown): TestViewData {
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      throw new Error("test data is invalid");
    }
    const status = (value as Record<string, unknown>).status;
    if (typeof status !== "string" || status.length === 0) {
      throw new Error("test data status is invalid");
    }
    return { status };
  },
  validateState(): undefined {
    return undefined;
  },
};

const decoder = internalCreateViewProjectionRuntimeDecoder({
  contractKey: "test-v1:worker_detail:session-1",
  viewType: "worker_detail",
  viewId: "session-1",
  contract,
});

function snapshot(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "worker_detail",
    view_id: "session-1",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 4,
    projection_version: 7,
    query_fingerprint: "worker-session-1",
    generated_at: "2026-08-24T00:00:00.123456789Z",
    data: { status: "running" },
    available_actions: [],
    related_refs: [],
    ...overrides,
  };
}

function delta(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "delta-5",
    view_type: "worker_detail",
    view_id: "session-1",
    view_epoch: 1,
    view_cursor: 5,
    base_projection_version: 7,
    result_projection_version: 8,
    event_type: "view.entity_upserted.v1",
    critical: true,
    source_refs: [],
    payload: { data: { status: "waiting_user" } },
    ...overrides,
  };
}

function deltaFrame(overrides: Record<string, unknown> = {}, frame: Partial<ProjectionSSEFrame> = {}): ProjectionSSEFrame {
  const value = delta(overrides);
  return {
    eventName: "projection_delta",
    lastEventId: `${String(value.view_epoch)}:${String(value.view_cursor)}`,
    data: JSON.stringify(value),
    ...frame,
  };
}

const current: TrustedViewProjectionState<TestViewData> = {
  data: { status: "running" },
  availableActions: [],
  relatedRefs: [],
  viewEpoch: 1,
  viewCursor: 4,
  projectionVersion: 7,
};

function captureDecodeError(run: () => unknown): ViewProjectionDecodeError {
  try {
    run();
  } catch (error: unknown) {
    expect(error).toBeInstanceOf(ViewProjectionDecodeError);
    return error as ViewProjectionDecodeError;
  }
  throw new Error("expected projection decoding to fail");
}

function applyDelta<T>(decision: DecodedDeltaDecision<T>): DecodedProjectionDelta<T> {
  if (decision.result.kind !== "apply") throw new Error("expected apply");
  return decision.result.delta;
}

describe("ViewProjection runtime decoder common boundary", () => {
  it.each([null, [], "snapshot", 42])("rejects a non-record Snapshot: %j", (value) => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(value, { requestedClientCommandIds: [] }));
    expect(error.classification).toBe("invalid_contract");
    expect(error.safeReason).toBe("malformed_contract");
  });

  it("classifies identity before schema", () => {
    const missing = captureDecodeError(() => decoder.decodeSnapshot({
      schema_version: "frontier.projection_snapshot.v2",
      view_id: "session-1",
    }, { requestedClientCommandIds: [] }));
    expect(missing.classification).toBe("invalid_contract");

    const foreign = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      view_id: "session-other",
      schema_version: "frontier.projection_snapshot.v2",
    }), { requestedClientCommandIds: [] }));
    expect(foreign.classification).toBe("foreign_identity");

    const matching = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      schema_version: "frontier.projection_snapshot.v2",
    }), { requestedClientCommandIds: [] }));
    expect(matching.classification).toBe("unsupported_schema");
  });

  it("compares arbitrarily large canonical schema text without Number conversion", () => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      schema_version: `frontier.projection_snapshot.v${"9".repeat(512)}`,
    }), { requestedClientCommandIds: [] }));
    expect(error.classification).toBe("unsupported_schema");
  });

  it.each([
    "frontier.projection_snapshot.v0",
    "frontier.projection_snapshot.v01",
    "frontier.projection_snapshot.v1.0",
    "frontier.projection_snapshot.vone",
    "frontier.projection_delta.v1",
  ])("rejects non-canonical or cross-family Snapshot schema %s", (schemaVersion) => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      schema_version: schemaVersion,
    }), { requestedClientCommandIds: [] }));
    expect(error.classification).toBe("invalid_contract");
  });

  it.each([
    ["view_epoch", -1],
    ["view_epoch", 1.5],
    ["view_epoch", Number.MAX_SAFE_INTEGER + 1],
    ["view_cursor", -1],
    ["view_cursor", 1.5],
    ["view_cursor", Number.POSITIVE_INFINITY],
    ["projection_version", 0],
    ["projection_version", 1.5],
    ["projection_version", Number.MAX_SAFE_INTEGER + 1],
  ])("rejects unsafe Snapshot position %s=%s", (field, value) => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({ [field]: value }), {
      requestedClientCommandIds: [],
    }));
    expect(error.classification).toBe("invalid_contract");
  });

  it("accepts cursor zero, strict UTC timestamps and additive object fields", () => {
    const decoded = decoder.decodeSnapshot(snapshot({
      view_cursor: 0,
      additive_top_level: { ignored: true },
      data: { status: "running", additive_data: true },
    }), { requestedClientCommandIds: [] });
    expect(decoded.effective.viewCursor).toBe(0);
    expect(decoded.effective.data).toEqual({ status: "running" });
  });

  it.each([
    ["tenant_id", " tenant-local"],
    ["query_fingerprint", "worker\nsession"],
    ["generated_at", "2026-08-24T08:00:00+08:00"],
    ["generated_at", "2026-02-30T00:00:00Z"],
  ])("rejects non-canonical Snapshot field %s", (field, value) => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({ [field]: value }), {
      requestedClientCommandIds: [],
    }));
    expect(error.classification).toBe("invalid_contract");
  });

  it("requires an exact safe Delta version increment", () => {
    for (const result of [7, 9, 8.5, Number.MAX_SAFE_INTEGER + 1]) {
      const error = captureDecodeError(() => decoder.decodeDeltaFrame(
        deltaFrame({ result_projection_version: result }),
        current,
      ));
      expect(error.classification).toBe("invalid_contract");
    }
  });

  it("requires command_id whenever client_command_id is present", () => {
    const error = captureDecodeError(() => decoder.decodeDeltaFrame(
      deltaFrame({ client_command_id: "client-command-1" }),
      current,
    ));
    expect(error.classification).toBe("invalid_contract");
  });

  it("never includes raw protected payload in a decoding error", () => {
    const protectedValue = "do-not-leak-this-value";
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      tenant_id: " tenant-local",
      data: { status: "running", secret: protectedValue },
    }), { requestedClientCommandIds: [] }));
    expect(String(error)).not.toContain(protectedValue);
    expect(JSON.stringify(error)).not.toContain(protectedValue);
  });

  it("normalizes a throwing Snapshot getter without exposing its secret", () => {
    const secret = "snapshot-getter-secret";
    const ingress = snapshot();
    Object.defineProperty(ingress, "schema_version", {
      enumerable: true,
      get() {
        throw new Error(secret);
      },
    });

    const error = captureDecodeError(() => decoder.decodeSnapshot(ingress, {
      requestedClientCommandIds: [],
    }));
    expect(error).toMatchObject({
      classification: "invalid_contract",
      safeReason: "malformed_contract",
      ingress: "snapshot",
    });
    expect(String(error)).not.toContain(secret);
    expect(JSON.stringify(error)).not.toContain(secret);
  });

  it("normalizes a revoked Proxy thrown by a Snapshot getter", () => {
    const revocable = Proxy.revocable({}, {});
    revocable.revoke();
    const ingress = snapshot();
    Object.defineProperty(ingress, "schema_version", {
      enumerable: true,
      get() {
        throw revocable.proxy;
      },
    });

    const error = captureDecodeError(() => decoder.decodeSnapshot(ingress, {
      requestedClientCommandIds: [],
    }));
    expect(error).toMatchObject({
      classification: "invalid_contract",
      safeReason: "malformed_contract",
      ingress: "snapshot",
    });
  });

  it("rejects a forged decode error from Snapshot ingress without leaking it", () => {
    const secret = "forged-view-error-secret";
    const forged = new ViewProjectionDecodeError({
      classification: "invalid_contract",
      safeReason: secret,
      ingress: "delta",
      expectedIdentity: { viewType: "attacker", viewId: "attacker" },
    });
    const ingress = snapshot();
    Object.defineProperty(ingress, "schema_version", {
      enumerable: true,
      get() {
        throw forged;
      },
    });

    const error = captureDecodeError(() => decoder.decodeSnapshot(ingress, {
      requestedClientCommandIds: [],
    }));
    expect(error).not.toBe(forged);
    expect(error).toMatchObject({
      classification: "invalid_contract",
      safeReason: "malformed_contract",
      ingress: "snapshot",
      expectedIdentity: { viewType: "worker_detail", viewId: "session-1" },
    });
    expect(String(error)).not.toContain(secret);
    expect(JSON.stringify(error)).not.toContain(secret);
  });

  it("preserves a closed product rejection without exposing arbitrary contract errors", () => {
    const legacyDecoder = decoderWithContract({
      decodeData() {
        internalRejectViewContract("legacy_worker_contract");
      },
    });
    const legacy = captureDecodeError(() => legacyDecoder.decodeSnapshot(snapshot({
      data: { protected: "legacy-row-payload" },
    }), { requestedClientCommandIds: [] }));
    expect(legacy.classification).toBe("invalid_contract");
    expect(legacy.safeReason).toBe("legacy_worker_contract");
    expect(JSON.stringify(legacy)).not.toContain("legacy-row-payload");

    const arbitraryDecoder = decoderWithContract({
      decodeData() {
        throw new Error("do-not-expose-product-parser-details");
      },
    });
    const arbitrary = captureDecodeError(() => arbitraryDecoder.decodeSnapshot(snapshot(), {
      requestedClientCommandIds: [],
    }));
    expect(arbitrary.safeReason).toBe("authority_violation");
    expect(String(arbitrary)).not.toContain("do-not-expose-product-parser-details");
  });
});

describe("decoded projection commit immutability", () => {
  it("deep-freezes Snapshot output without ingress or envelope/effective aliases", () => {
    const ingressMetadata = {
      policy: { scopes: ["session"] },
    };
    const ingressAction = { ...action, metadata: ingressMetadata };
    const ingressRelatedRef = { ...relatedRef };
    const ingress = snapshot({
      data: { status: "running" },
      available_actions: [ingressAction],
      related_refs: [ingressRelatedRef],
      command_receipts: [
        { client_command_id: "client-command-1", command_id: "server-command-1" },
      ],
    });

    const decoded = decoder.decodeSnapshot(ingress, {
      requestedClientCommandIds: ["client-command-1"],
    });

    expect(Object.isFrozen(decoded)).toBe(true);
    expect(Object.isFrozen(decoded.envelope)).toBe(true);
    expect(Object.isFrozen(decoded.effective)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.data)).toBe(true);
    expect(Object.isFrozen(decoded.effective.data)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.available_actions)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.available_actions[0])).toBe(true);
    expect(Object.isFrozen(decoded.envelope.available_actions[0].metadata)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.available_actions[0].metadata?.policy)).toBe(true);
    expect(Object.isFrozen(
      (decoded.envelope.available_actions[0].metadata?.policy as { scopes: string[] }).scopes,
    )).toBe(true);
    expect(Object.isFrozen(decoded.envelope.related_refs)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.related_refs[0])).toBe(true);
    expect(Object.isFrozen(decoded.envelope.command_receipts)).toBe(true);

    expect(decoded.envelope.data).not.toBe(decoded.effective.data);
    expect(decoded.envelope.available_actions).not.toBe(decoded.effective.availableActions);
    expect(decoded.envelope.available_actions[0]).not.toBe(decoded.effective.availableActions[0]);
    expect(decoded.envelope.available_actions[0].metadata)
      .not.toBe(decoded.effective.availableActions[0].metadata);
    expect(decoded.envelope.related_refs).not.toBe(decoded.effective.relatedRefs);
    expect(decoded.envelope.related_refs[0]).not.toBe(decoded.effective.relatedRefs[0]);
    expect(decoded.envelope.available_actions[0]).not.toBe(ingressAction);
    expect(decoded.envelope.available_actions[0].metadata).not.toBe(ingressMetadata);
    expect(decoded.envelope.related_refs[0]).not.toBe(ingressRelatedRef);

    ingressMetadata.policy.scopes.push("late-ingress-mutation");
    ingressAction.action = "attacker.rewritten";
    ingressRelatedRef.href = "/attacker-rewritten";
    expect(decoded.envelope.available_actions[0]).toMatchObject({
      action: "worker.send_prompt",
      metadata: { policy: { scopes: ["session"] } },
    });
    expect(decoded.envelope.related_refs[0].href)
      .toBe("/api/v1/views/worker_detail/session%20%2F1");
    expect(() => {
      (decoded.envelope.available_actions[0].metadata?.policy as { scopes: string[] })
        .scopes.push("late-decoded-mutation");
    }).toThrow(TypeError);
  });

  it("deep-freezes Delta envelopes, payloads and effective state independently", () => {
    const decision = decoder.decodeDeltaFrame(deltaFrame({
      source_refs: [{ type: "integration_event", id: "event-5" }],
      payload: {
        data: { status: "waiting_user" },
        available_actions: [{ ...action, metadata: { nested: { enabled: true } } }],
        related_refs: [relatedRef],
        display_checkpoint: { display_stream_id: "display-1", checkpoint_version: 2 },
      },
    }), current);
    expect(decision.result.kind).toBe("apply");
    if (decision.result.kind !== "apply") throw new Error("expected apply");
    const decoded = decision.result.delta;

    expect(Object.isFrozen(decision)).toBe(true);
    expect(Object.isFrozen(decoded)).toBe(true);
    expect(Object.isFrozen(decoded.envelope)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.payload)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.source_refs)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.source_refs[0])).toBe(true);
    expect(Object.isFrozen(decoded.envelope.payload.available_actions)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.payload.available_actions?.[0].metadata)).toBe(true);
    expect(Object.isFrozen(
      (decoded.envelope.payload.available_actions?.[0].metadata?.nested as { enabled: boolean }),
    )).toBe(true);
    expect(Object.isFrozen(decoded.envelope.payload.related_refs)).toBe(true);
    expect(Object.isFrozen(decoded.envelope.payload.display_checkpoint)).toBe(true);
    expect(Object.isFrozen(decoded.effective)).toBe(true);
    expect(decoded.envelope.payload.data).not.toBe(decoded.effective.data);
    expect(decoded.envelope.payload.available_actions).not.toBe(decoded.effective.availableActions);
    expect(decoded.envelope.payload.available_actions?.[0])
      .not.toBe(decoded.effective.availableActions[0]);
    expect(decoded.envelope.payload.related_refs).not.toBe(decoded.effective.relatedRefs);
    expect(decoded.envelope.payload.related_refs?.[0]).not.toBe(decoded.effective.relatedRefs[0]);
  });

  it("derives the envelope from the validated canonical state, not a mutable decoder alias", () => {
    const decodedSource = { status: "validated" };
    const aliasingDecoder = internalCreateViewProjectionRuntimeDecoder({
      contractKey: "aliasing-v1:worker_detail:session-1",
      viewType: "worker_detail",
      viewId: "session-1",
      contract: {
        allowedDeltaEventTypes: ["view.entity_upserted.v1"],
        canonicalRelatedRefTypes: ["worker_detail"],
        decodeData: () => decodedSource,
        validateState: () => {
          decodedSource.status = "mutated-after-validation";
          return undefined;
        },
      },
    });

    const decoded = aliasingDecoder.decodeSnapshot(snapshot(), {
      requestedClientCommandIds: [],
    });
    expect(decoded.effective.data.status).toBe("validated");
    expect(decoded.envelope.data.status).toBe("validated");
  });

  it.each(["snapshot", "delta"] as const)(
    "preserves nested explicit optional undefined in canonical %s data",
    (ingress) => {
      interface OptionalViewData {
        readonly status: string;
        readonly optional_note?: string;
        readonly nested: { readonly optional_label?: string };
      }
      const optionalDecoder = internalCreateViewProjectionRuntimeDecoder<OptionalViewData>({
        contractKey: `optional-${ingress}-v1:worker_detail:session-1`,
        viewType: "worker_detail",
        viewId: "session-1",
        contract: {
          allowedDeltaEventTypes: ["view.entity_upserted.v1"],
          canonicalRelatedRefTypes: ["worker_detail"],
          decodeData: () => ({
            status: "running",
            optional_note: undefined,
            nested: { optional_label: undefined },
          }),
          validateState: () => undefined,
        },
      });

      const data = ingress === "snapshot"
        ? optionalDecoder.decodeSnapshot(snapshot(), {
            requestedClientCommandIds: [],
          }).envelope.data
        : applyDelta(optionalDecoder.decodeDeltaFrame(deltaFrame(), {
            data: { status: "running", nested: {} },
            availableActions: [],
            relatedRefs: [],
            viewEpoch: 1,
            viewCursor: 4,
            projectionVersion: 7,
          })).envelope.payload.data;

      expect(data).toEqual({
        status: "running",
        optional_note: undefined,
        nested: { optional_label: undefined },
      });
      expect(Object.hasOwn(data ?? {}, "optional_note")).toBe(true);
      expect(Object.hasOwn(data?.nested ?? {}, "optional_label")).toBe(true);
      expect(Object.isFrozen(data)).toBe(true);
      expect(Object.isFrozen(data?.nested)).toBe(true);
    },
  );
});

describe("projection contract capture", () => {
  it("captures exact contract functions and allowlists at decoder construction", () => {
    const mutableContract = {
      allowedDeltaEventTypes: ["view.entity_upserted.v1"],
      canonicalRelatedRefTypes: ["worker_detail"],
      decodeData: contract.decodeData,
      validateState: contract.validateState,
    };
    const capturedDecoder = internalCreateViewProjectionRuntimeDecoder({
      contractKey: "captured-v1:worker_detail:session-1",
      viewType: "worker_detail",
      viewId: "session-1",
      contract: mutableContract,
    });

    mutableContract.allowedDeltaEventTypes[0] = "attacker.rewritten.v1";
    mutableContract.canonicalRelatedRefTypes[0] = "attacker_detail";
    mutableContract.decodeData = () => {
      throw new Error("late decodeData replacement must not run");
    };
    mutableContract.validateState = () => {
      throw new Error("late validateState replacement must not run");
    };

    expect(capturedDecoder.decodeSnapshot(snapshot({
      available_actions: [action],
      related_refs: [relatedRef],
    }), { requestedClientCommandIds: [] }).effective.data).toEqual({ status: "running" });
    expect(capturedDecoder.decodeDeltaFrame(deltaFrame(), current).result.kind).toBe("apply");
  });

  it("invokes captured contract callbacks without binding the mutable source contract", () => {
    let decodeReceiver: unknown = "not-called";
    let validateReceiver: unknown = "not-called";
    const receiverFreeDecoder = internalCreateViewProjectionRuntimeDecoder({
      contractKey: "receiver-free-v1:worker_detail:session-1",
      viewType: "worker_detail",
      viewId: "session-1",
      contract: {
        allowedDeltaEventTypes: ["view.entity_upserted.v1"],
        canonicalRelatedRefTypes: ["worker_detail"],
        decodeData(this: void, value: unknown) {
          decodeReceiver = this;
          return contract.decodeData(value);
        },
        validateState(this: void) {
          validateReceiver = this;
          return undefined;
        },
      },
    });

    receiverFreeDecoder.decodeSnapshot(snapshot(), { requestedClientCommandIds: [] });
    expect(decodeReceiver).toBeUndefined();
    expect(validateReceiver).toBeUndefined();
  });

  it("rejects thenables returned by data decoders and state validators synchronously", () => {
    const asyncDataDecoder = decoderWithContract({
      decodeData: (() => Promise.resolve({ status: "forged" })) as never,
    });
    const asyncDataError = captureDecodeError(() => asyncDataDecoder.decodeSnapshot(snapshot(), {
      requestedClientCommandIds: [],
    }));
    expect(asyncDataError).toMatchObject({
      classification: "invalid_contract",
      safeReason: "authority_violation",
    });

    const asyncValidatorDecoder = decoderWithContract({
      validateState: (() => Promise.resolve(undefined)) as never,
    });
    const asyncValidatorError = captureDecodeError(() => asyncValidatorDecoder.decodeSnapshot(
      snapshot(),
      { requestedClientCommandIds: [] },
    ));
    expect(asyncValidatorError).toMatchObject({
      classification: "invalid_contract",
      safeReason: "authority_violation",
    });
  });

  it("observes rejected async contract promises without leaking an unhandled rejection", async () => {
    const runtimeProcess = (globalThis as typeof globalThis & {
      process?: {
        off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
        on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
      };
    }).process;
    expect(runtimeProcess).toBeDefined();
    const unhandled: unknown[] = [];
    const captureUnhandled = (reason: unknown) => unhandled.push(reason);
    runtimeProcess?.on("unhandledRejection", captureUnhandled);
    try {
      const rejectedDataDecoder = decoderWithContract({
        decodeData: (async () => {
          throw new Error("raw-data-secret");
        }) as never,
      });
      expect(captureDecodeError(() => rejectedDataDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }))).toMatchObject({
        classification: "invalid_contract",
        safeReason: "authority_violation",
      });

      const rejectedValidatorDecoder = decoderWithContract({
        validateState: (async () => {
          await Promise.resolve();
          throw new Error("raw-validator-secret");
        }) as never,
      });
      expect(captureDecodeError(() => rejectedValidatorDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }))).toMatchObject({
        classification: "invalid_contract",
        safeReason: "authority_violation",
      });

      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(unhandled).toEqual([]);
    } finally {
      runtimeProcess?.off("unhandledRejection", captureUnhandled);
    }
  });

  it("observes nested intrinsic-compatible Promises across arrays, realms and subclasses", async () => {
    const runtimeProcess = nodeRejectionProcess();
    const unhandled: unknown[] = [];
    const captureUnhandled = (reason: unknown) => unhandled.push(reason);
    runtimeProcess.on("unhandledRejection", captureUnhandled);
    const iframe = document.createElement("iframe");
    document.body.appendChild(iframe);
    try {
      const ForeignPromise = (iframe.contentWindow as unknown as {
        readonly Promise?: PromiseConstructor;
      } | null)?.Promise;
      if (ForeignPromise === undefined) throw new Error("iframe Promise realm is unavailable");
      expect(ForeignPromise).not.toBe(Promise);

      class NativePromiseSubclass<T> extends Promise<T> {}
      const subclassPromise = new NativePromiseSubclass<never>((_resolve, reject) => {
        reject(new Error("nested-subclass-secret"));
      });
      let subclassThenCalls = 0;
      Object.defineProperty(subclassPromise, "then", {
        value: () => {
          subclassThenCalls += 1;
          return Promise.reject(new Error("subclass-override-secret"));
        },
      });

      const rejectedValues = [
        Promise.reject(new Error("nested-local-secret")),
        { later: Promise.reject(new Error("nested-later-secret")) },
        { subclassPromise },
        {
          foreignPromise: new ForeignPromise<never>((_resolve, reject) => {
            reject(new Error("nested-foreign-secret"));
          }),
        },
      ];
      const nestedPromiseDecoder = decoderWithContract({
        decodeData: (() => ({ status: "running", nested: rejectedValues })) as never,
      });

      const error = captureDecodeError(() => nestedPromiseDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }));
      expect(error).toMatchObject({
        classification: "invalid_contract",
        safeReason: "authority_violation",
      });

      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(subclassThenCalls).toBe(0);
      expect(unhandled).toEqual([]);
    } finally {
      iframe.remove();
      runtimeProcess.off("unhandledRejection", captureUnhandled);
    }
  });

  it("rejects an adversarial custom thenable without calling or assimilating its then", async () => {
    const runtimeProcess = nodeRejectionProcess();
    const unhandled: unknown[] = [];
    const captureUnhandled = (reason: unknown) => unhandled.push(reason);
    runtimeProcess.on("unhandledRejection", captureUnhandled);
    let thenCalls = 0;
    try {
      const adversarialThenable = {
        then() {
          thenCalls += 1;
          return Promise.reject(new Error("custom-then-secret"));
        },
      };
      const thenableDecoder = decoderWithContract({
        decodeData: (() => adversarialThenable) as never,
      });

      expect(captureDecodeError(() => thenableDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }))).toMatchObject({
        classification: "invalid_contract",
        safeReason: "authority_violation",
      });

      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(thenCalls).toBe(0);
      expect(unhandled).toEqual([]);
    } finally {
      runtimeProcess.off("unhandledRejection", captureUnhandled);
    }
  });

  it("observes rejected Promises in native Promise properties and fulfilled values", async () => {
    const runtimeProcess = nodeRejectionProcess();
    const unhandled: unknown[] = [];
    const captureUnhandled = (reason: unknown) => unhandled.push(reason);
    runtimeProcess.on("unhandledRejection", captureUnhandled);
    try {
      const rejectedOuter = Promise.reject(new Error("outer-native-secret"));
      Object.defineProperty(rejectedOuter, "nested", {
        enumerable: true,
        value: Promise.reject(new Error("own-nested-secret")),
      });
      const ownPropertyDecoder = decoderWithContract({
        decodeData: (() => rejectedOuter) as never,
      });
      captureDecodeError(() => ownPropertyDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }));

      const fulfilledValueDecoder = decoderWithContract({
        decodeData: (() => Promise.resolve({
          status: "running",
          nested: Promise.reject(new Error("fulfilled-nested-secret")),
        })) as never,
      });
      captureDecodeError(() => fulfilledValueDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }));

      const nestedRejectionReason = Promise.reject(new Error("nested-reason-secret"));
      const rejectedReasonDecoder = decoderWithContract({
        decodeData: (() => Promise.reject(nestedRejectionReason)) as never,
      });
      captureDecodeError(() => rejectedReasonDecoder.decodeSnapshot(snapshot(), {
        requestedClientCommandIds: [],
      }));

      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(unhandled).toEqual([]);
    } finally {
      runtimeProcess.off("unhandledRejection", captureUnhandled);
    }
  });
});

function nodeRejectionProcess(): {
  off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
  on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
} {
  const runtimeProcess = (globalThis as typeof globalThis & {
    process?: {
      off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
      on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
    };
  }).process;
  if (runtimeProcess === undefined) throw new Error("Node process is required for rejection capture");
  return runtimeProcess;
}

function control(
  eventType: "resync_required" | "client_upgrade_required",
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    schema_version: "frontier.projection_control.v1",
    event_type: eventType,
    view_type: "worker_detail",
    view_id: "session-1",
    view_epoch: 1,
    snapshot_cursor: 5,
    ...overrides,
  };
}

function controlFrame(
  eventType: "resync_required" | "client_upgrade_required",
  overrides: Record<string, unknown> = {},
  frame: Partial<ProjectionSSEFrame> = {},
): ProjectionSSEFrame {
  return {
    eventName: eventType,
    lastEventId: "1:4",
    data: JSON.stringify(control(eventType, overrides)),
    ...frame,
  };
}

function decoderWithContract(overrides: Partial<InternalViewContract<TestViewData>>) {
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: "custom-v1:worker_detail:session-1",
    viewType: "worker_detail",
    viewId: "session-1",
    contract: { ...contract, ...overrides },
  });
}

const action = {
  action: "worker.send_prompt",
  target_id: "session-1",
  expected_revision: 3,
  metadata: { mode: "queue" },
};

const relatedRef = {
  type: "worker_detail",
  id: "session /1",
  href: "/api/v1/views/worker_detail/session%20%2F1",
};

describe("Snapshot request context and command receipts", () => {
  it("preserves absent receipts when no reconciliation query was sent", () => {
    const decoded = decoder.decodeSnapshot(snapshot(), { requestedClientCommandIds: [] });
    expect(decoded.envelope).not.toHaveProperty("command_receipts");

    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      command_receipts: [],
    }), { requestedClientCommandIds: [] }));
    expect(error.classification).toBe("invalid_contract");
  });

  it("allows absent or present-empty receipts for a non-empty requested set", () => {
    const absent = decoder.decodeSnapshot(snapshot(), {
      requestedClientCommandIds: ["client-command-1"],
    });
    expect(absent.envelope).not.toHaveProperty("command_receipts");

    const present = decoder.decodeSnapshot(snapshot({ command_receipts: [] }), {
      requestedClientCommandIds: ["client-command-1"],
    });
    expect(present.envelope).toHaveProperty("command_receipts", []);
  });

  it("captures one sorted, deduplicated and frozen requested-id set", () => {
    const requested = ["client-command-b", "client-command-a", "client-command-b"];
    const decoded = decoder.decodeSnapshot(snapshot(), { requestedClientCommandIds: requested });
    requested.push("client-command-late");

    expect(decoded.requestContext.requestedClientCommandIds).toEqual([
      "client-command-a",
      "client-command-b",
    ]);
    expect(Object.isFrozen(decoded.requestContext)).toBe(true);
    expect(Object.isFrozen(decoded.requestContext.requestedClientCommandIds)).toBe(true);
  });

  it("normalizes exact duplicate receipts and preserves the requested subset", () => {
    const decoded = decoder.decodeSnapshot(snapshot({
      command_receipts: [
        { client_command_id: "client-command-b", command_id: "server-command-b" },
        { client_command_id: "client-command-a", command_id: "server-command-a" },
        { client_command_id: "client-command-b", command_id: "server-command-b" },
      ],
    }), {
      requestedClientCommandIds: ["client-command-c", "client-command-b", "client-command-a"],
    });
    expect(decoded.envelope.command_receipts).toEqual([
      { client_command_id: "client-command-a", command_id: "server-command-a" },
      { client_command_id: "client-command-b", command_id: "server-command-b" },
    ]);
  });

  it.each([
    {
      name: "one client mapped to two server commands",
      receipts: [
        { client_command_id: "client-command-a", command_id: "server-command-a" },
        { client_command_id: "client-command-a", command_id: "server-command-b" },
      ],
    },
    {
      name: "two clients mapped to one server command",
      receipts: [
        { client_command_id: "client-command-a", command_id: "server-command-a" },
        { client_command_id: "client-command-b", command_id: "server-command-a" },
      ],
    },
    {
      name: "an unrequested receipt",
      receipts: [
        { client_command_id: "client-command-c", command_id: "server-command-c" },
      ],
    },
  ])("rejects $name", ({ receipts }) => {
    const error = captureDecodeError(() => decoder.decodeSnapshot(snapshot({
      command_receipts: receipts,
    }), { requestedClientCommandIds: ["client-command-a", "client-command-b"] }));
    expect(error.classification).toBe("invalid_contract");
  });

  it("enforces the 64-id reconciliation bound before and after normalization", () => {
    const tooManyRequested = Array.from({ length: 65 }, (_, index) => `client-command-${index}`);
    expect(() => decoder.decodeSnapshot(snapshot(), {
      requestedClientCommandIds: tooManyRequested,
    })).toThrow(ViewProjectionDecodeError);

    const repeatedReceipts = Array.from({ length: 65 }, () => ({
      client_command_id: "client-command-a",
      command_id: "server-command-a",
    }));
    expect(() => decoder.decodeSnapshot(snapshot({ command_receipts: repeatedReceipts }), {
      requestedClientCommandIds: ["client-command-a"],
    })).toThrow(ViewProjectionDecodeError);
  });
});

describe("Delta continuity decisions", () => {
  it("classifies the producer's first base=0/result=1 Delta by continuity", () => {
    const mismatched = decoder.decodeDeltaFrame(deltaFrame({
      base_projection_version: 0,
      result_projection_version: 1,
    }), current);
    expect(mismatched.result.kind).toBe("resync_required");
    if (mismatched.result.kind !== "resync_required") throw new Error("expected resync");
    expect(mismatched.result.discontinuity).toMatchObject({
      source: "delta",
      reason: "base_version_mismatch",
      incoming: {
        baseProjectionVersion: 0,
        resultProjectionVersion: 1,
      },
    });

    const duplicate = decoder.decodeDeltaFrame(deltaFrame({
      view_cursor: 4,
      base_projection_version: 0,
      result_projection_version: 1,
    }), current);
    expect(duplicate.result).toEqual({
      kind: "duplicate",
      identity: { deltaId: "delta-5", viewEpoch: 1, viewCursor: 4 },
    });
  });

  it("returns a same-epoch duplicate before decoding payload authority", () => {
    let dataDecodeCount = 0;
    let stateValidationCount = 0;
    const strictDecoder = decoderWithContract({
      decodeData() {
        dataDecodeCount += 1;
        throw new Error("duplicate payload must not be decoded");
      },
      validateState() {
        stateValidationCount += 1;
        throw new Error("duplicate authority must not be validated");
      },
    });
    const decision = strictDecoder.decodeDeltaFrame(deltaFrame({
      view_cursor: 4,
      base_projection_version: 1,
      result_projection_version: 2,
      event_type: "writer.taxonomy.not-admitted.v1",
      payload: { data: { protected: "not-authority" } },
    }), current);

    expect(decision.result).toEqual({
      kind: "duplicate",
      identity: { deltaId: "delta-5", viewEpoch: 1, viewCursor: 4 },
    });
    expect(dataDecodeCount).toBe(0);
    expect(stateValidationCount).toBe(0);
  });

  it.each([
    {
      name: "epoch mismatch",
      overrides: { view_epoch: 2, view_cursor: 1, base_projection_version: 7, result_projection_version: 8 },
      reason: "epoch_mismatch",
    },
    {
      name: "cursor gap",
      overrides: { view_cursor: 6, base_projection_version: 7, result_projection_version: 8 },
      reason: "cursor_gap",
    },
    {
      name: "base version mismatch",
      overrides: { view_cursor: 5, base_projection_version: 6, result_projection_version: 7 },
      reason: "base_version_mismatch",
    },
  ] as const)("returns a closed discontinuity for $name without parsing payload", ({ overrides, reason }) => {
    const strictDecoder = decoderWithContract({
      decodeData() {
        throw new Error("discontinuous payload must not be decoded");
      },
      validateState() {
        throw new Error("discontinuous authority must not be validated");
      },
    });
    const frame = deltaFrame({
      ...overrides,
      payload: { data: { protected: "never-merge-this" } },
    });
    const decision = strictDecoder.decodeDeltaFrame(frame, current);
    expect(decision.result.kind).toBe("resync_required");
    if (decision.result.kind !== "resync_required") throw new Error("expected resync");
    expect(decision.result.discontinuity).toMatchObject({
      source: "delta",
      reason,
      trusted: { viewEpoch: 1, viewCursor: 4, projectionVersion: 7 },
      incoming: {
        deltaId: "delta-5",
        viewEpoch: overrides.view_epoch ?? 1,
        viewCursor: overrides.view_cursor,
        baseProjectionVersion: overrides.base_projection_version,
        resultProjectionVersion: overrides.result_projection_version,
      },
    });
    expect(decision.result.discontinuity.key).toMatch(/^[a-z0-9:_-]+$/);
    expect(decision.result.discontinuity.key).not.toContain("never-merge-this");
  });

  it("builds one effective state only for the exact next Delta", () => {
    const decision = decoder.decodeDeltaFrame(deltaFrame({
      payload: {
        available_actions: [action],
      },
    }), current);
    expect(decision.result.kind).toBe("apply");
    if (decision.result.kind !== "apply") throw new Error("expected apply");
    expect(decision.result.delta.effective).toEqual({
      data: { status: "running" },
      availableActions: [action],
      relatedRefs: [],
      viewEpoch: 1,
      viewCursor: 5,
      projectionVersion: 8,
    });
    expect(current).toEqual({
      data: { status: "running" },
      availableActions: [],
      relatedRefs: [],
      viewEpoch: 1,
      viewCursor: 4,
      projectionVersion: 7,
    });
  });
});

describe("SSE binding and closed integrity evidence", () => {
  it("normalizes hostile Delta frame access even while attaching integrity evidence", () => {
    const secret = "delta-frame-getter-secret";
    const frame = deltaFrame();
    Object.defineProperty(frame, "lastEventId", {
      enumerable: true,
      get() {
        throw new Error(secret);
      },
    });

    const error = captureDecodeError(() => decoder.decodeDeltaFrame(frame, current));
    expect(error).toMatchObject({
      classification: "invalid_contract",
      safeReason: "malformed_contract",
      ingress: "delta",
    });
    expect(error.integrityFailure).toBeUndefined();
    expect(String(error)).not.toContain(secret);
    expect(JSON.stringify(error)).not.toContain(secret);
  });

  it("normalizes a throwing Control frame getter without exposing its secret", () => {
    const secret = "control-frame-getter-secret";
    const frame = controlFrame("resync_required");
    Object.defineProperty(frame, "data", {
      enumerable: true,
      get() {
        throw new Error(secret);
      },
    });

    const error = captureDecodeError(() => decoder.decodeControlFrame(frame, current));
    expect(error).toMatchObject({
      classification: "invalid_contract",
      safeReason: "malformed_contract",
      ingress: "control",
    });
    expect(String(error)).not.toContain(secret);
    expect(JSON.stringify(error)).not.toContain(secret);
  });

  it("requires the projection_delta listener name and exact replay id", () => {
    for (const frame of [
      deltaFrame({}, { eventName: "message" }),
      deltaFrame({}, { lastEventId: "1:6" }),
    ]) {
      const error = captureDecodeError(() => decoder.decodeDeltaFrame(frame, current));
      expect(error.classification).toBe("invalid_contract");
      expect(error.safeReason).toBe("sse_binding_mismatch");
      expect(error.integrityFailure).toMatchObject({
        ingress: "delta",
        eventFamily: "projection_delta",
        replayable: true,
        safeReason: "sse_binding_mismatch",
      });
    }
  });

  it("attaches integrity evidence only to invalid Delta frames with a canonical replay id", () => {
    const malformed = captureDecodeError(() => decoder.decodeDeltaFrame({
      eventName: "projection_delta",
      lastEventId: "1:5",
      data: "{protected-invalid-json",
    }, current));
    expect(malformed.classification).toBe("invalid_contract");
    expect(malformed.integrityFailure?.key).toBe("delta:1:5");

    const nonCanonicalReplay = captureDecodeError(() => decoder.decodeDeltaFrame({
      eventName: "projection_delta",
      lastEventId: "01:5",
      data: "{invalid-json",
    }, current));
    expect(nonCanonicalReplay.integrityFailure).toBeUndefined();

    const foreign = captureDecodeError(() => decoder.decodeDeltaFrame(deltaFrame({
      view_id: "session-other",
      schema_version: "frontier.projection_delta.v999",
    }), current));
    expect(foreign.classification).toBe("foreign_identity");
    expect(foreign.integrityFailure).toBeUndefined();

    const upgrade = captureDecodeError(() => decoder.decodeDeltaFrame(deltaFrame({
      schema_version: "frontier.projection_delta.v999",
    }), current));
    expect(upgrade.classification).toBe("unsupported_schema");
    expect(upgrade.integrityFailure).toBeUndefined();
  });

  it("binds Control listener names and lastEventId to the current durable cursor", () => {
    const resync = decoder.decodeControlFrame(controlFrame("resync_required"), current);
    expect(resync.result.kind).toBe("resync_required");
    if (resync.result.kind !== "resync_required") throw new Error("expected control resync");
    expect(resync.result.discontinuity).toMatchObject({
      source: "control",
      reason: "server_resync",
      trusted: { viewEpoch: 1, viewCursor: 4, projectionVersion: 7 },
      incoming: { viewEpoch: 1, snapshotCursor: 5 },
    });

    expect(decoder.decodeControlFrame(controlFrame(
      "client_upgrade_required",
      {},
      { lastEventId: "" },
    ), current).result.kind).toBe("client_upgrade_required");

    for (const frame of [
      controlFrame("resync_required", {}, { eventName: "client_upgrade_required" }),
      controlFrame("resync_required", {}, { lastEventId: "1:5" }),
      controlFrame("resync_required", {}, { lastEventId: "2:4" }),
    ]) {
      const error = captureDecodeError(() => decoder.decodeControlFrame(frame, current));
      expect(error.classification).toBe("invalid_contract");
      expect(error.safeReason).toBe("sse_binding_mismatch");
      expect(error.integrityFailure).toBeUndefined();
    }
  });
});

describe("common action, source and related reference authority", () => {
  it("normalizes canonical actions and canonical encoded View references", () => {
    const decoded = decoder.decodeSnapshot(snapshot({
      available_actions: [{ ...action, additive: "ignored" }],
      related_refs: [{ ...relatedRef, additive: "ignored" }],
    }), { requestedClientCommandIds: [] });
    expect(decoded.envelope.available_actions).toEqual([action]);
    expect(decoded.envelope.related_refs).toEqual([relatedRef]);
  });

  it("normalizes canonical source refs and a positive display checkpoint", () => {
    const decision = decoder.decodeDeltaFrame(deltaFrame({
      source_refs: [{ type: "integration_event", id: "event /5", additive: true }],
      payload: {
        display_checkpoint: { display_stream_id: "display-1", checkpoint_version: 2, additive: true },
      },
    }), current);
    expect(decision.result.kind).toBe("apply");
    if (decision.result.kind !== "apply") throw new Error("expected apply");
    expect(decision.result.delta.envelope.source_refs).toEqual([
      { type: "integration_event", id: "event /5" },
    ]);
    expect(decision.result.delta.envelope.payload.display_checkpoint).toEqual({
      display_stream_id: "display-1",
      checkpoint_version: 2,
    });
  });

  it.each([
    { field: "action", value: " worker.send_prompt" },
    { field: "target_id", value: "session-1\n" },
    { field: "expected_revision", value: 0 },
    { field: "expected_revision", value: 1.5 },
    { field: "expected_revision", value: Number.MAX_SAFE_INTEGER + 1 },
    { field: "metadata", value: [] },
  ])("rejects a malformed ActionRef $field", ({ field, value }) => {
    expect(() => decoder.decodeSnapshot(snapshot({
      available_actions: [{ ...action, [field]: value }],
    }), { requestedClientCommandIds: [] })).toThrow(ViewProjectionDecodeError);
  });

  it.each([
    { ...relatedRef, id: " session-1" },
    { ...relatedRef, href: "/api/v1/views/worker_detail/session /1" },
    { ...relatedRef, href: "/api/v1/views/worker_detail/foreign" },
    { ...relatedRef, type: "artifact_detail", href: "/api/v1/views/artifact_detail/session%20%2F1" },
  ])("rejects a malformed or foreign canonical View ref %#", (reference) => {
    expect(() => decoder.decodeSnapshot(snapshot({
      related_refs: [reference],
    }), { requestedClientCommandIds: [] })).toThrow(ViewProjectionDecodeError);
  });

  it("rejects non-canonical SourceRef and display checkpoint identities", () => {
    expect(() => decoder.decodeDeltaFrame(deltaFrame({
      source_refs: [{ type: "integration_event", id: " event-5" }],
    }), current)).toThrow(ViewProjectionDecodeError);
    expect(() => decoder.decodeDeltaFrame(deltaFrame({
      payload: { display_checkpoint: { display_stream_id: "display-1", checkpoint_version: 0 } },
    }), current)).toThrow(ViewProjectionDecodeError);
  });

  it("admits event_type only from the bound product contract allowlist", () => {
    const rejected = captureDecodeError(() => decoder.decodeDeltaFrame(deltaFrame({
      event_type: "view.stream_checkpointed.v1",
    }), current));
    expect(rejected.classification).toBe("invalid_contract");
    expect(rejected.safeReason).toBe("authority_violation");

    const workerContractDecoder = decoderWithContract({
      allowedDeltaEventTypes: ["view.entity_upserted.v1", "view.stream_checkpointed.v1"],
    });
    expect(workerContractDecoder.decodeDeltaFrame(deltaFrame({
      event_type: "view.stream_checkpointed.v1",
      payload: { arbitrary_reducer_name: "must-not-select-code" },
    }), current).result.kind).toBe("apply");
  });
});
