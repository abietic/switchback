import type {
  ActionRef,
  CommandReceipt,
  ProjectionControlEvent,
  ProjectionDelta,
  ProjectionDeltaPayload,
  ProjectionSnapshot,
  RelatedRef,
  SourceRef,
} from "../api/contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "../api/contractPrimitives";

const boundDecoderBrand: unique symbol = Symbol("boundViewProjectionRuntimeDecoder");
const decodedProjectionBrand: unique symbol = Symbol("decodedViewProjection");
const intrinsicPromiseThen = Promise.prototype.then;
const intrinsicReflectApply = Reflect.apply;
const observedNativePromises = new WeakSet<object>();
const trustedProjectionDecodeErrors = new WeakSet<object>();

const MAX_RECONCILE_CLIENT_COMMAND_IDS = 64;

export type ViewProjectionDecodeClassification =
  | "foreign_identity"
  | "unsupported_schema"
  | "invalid_contract";

export type IntegrityFailureReason =
  | "malformed_contract"
  | "authority_violation"
  | "sse_binding_mismatch";

export type InternalViewContractRejectionReason = "legacy_worker_contract";

type DeepReadonly<T> = T extends (...args: never[]) => unknown
  ? T
  : T extends readonly (infer Item)[]
    ? readonly DeepReadonly<Item>[]
    : T extends object
      ? { readonly [Key in keyof T]: DeepReadonly<T[Key]> }
      : T;

type ContainsAsyncProjectionValue<T> = T extends (...args: never[]) => unknown
  ? true
  : T extends { readonly then: (...args: never[]) => unknown }
    ? true
    : T extends readonly (infer Item)[]
      ? ContainsAsyncProjectionValue<Item>
      : T extends object
        ? true extends {
          [Key in keyof T]-?: ContainsAsyncProjectionValue<T[Key]>
        }[keyof T]
          ? true
          : false
        : false;

type SynchronousProjectionData<T> = true extends ContainsAsyncProjectionValue<T>
  ? never
  : T;

export interface ProjectionSSEFrame {
  readonly eventName: string;
  readonly lastEventId: string;
  readonly data: string;
}

export interface TrustedViewProjectionState<T> {
  readonly data: DeepReadonly<T>;
  readonly availableActions: readonly DeepReadonly<ActionRef>[];
  readonly relatedRefs: readonly DeepReadonly<RelatedRef>[];
  readonly viewEpoch: number;
  readonly viewCursor: number;
  readonly projectionVersion: number;
}

export interface TrustedProjectionPosition {
  readonly viewEpoch: number;
  readonly viewCursor: number;
  readonly projectionVersion: number;
}

export interface InternalViewContractState<T> {
  readonly data: DeepReadonly<T>;
  readonly availableActions: readonly DeepReadonly<ActionRef>[];
  readonly relatedRefs: readonly DeepReadonly<RelatedRef>[];
}

/** @internal Composed only by projection-owned product factories. */
export interface InternalViewContract<T> {
  readonly allowedDeltaEventTypes: readonly string[];
  readonly canonicalRelatedRefTypes: readonly string[];
  readonly decodeData: (this: void, value: unknown) => SynchronousProjectionData<T>;
  readonly validateState: (this: void, state: InternalViewContractState<T>) => undefined;
}

export interface DecodedDeltaIdentity {
  readonly deltaId: string;
  readonly viewEpoch: number;
  readonly viewCursor: number;
}

export type DeltaResyncReason =
  | "epoch_mismatch"
  | "cursor_gap"
  | "base_version_mismatch";

export type DecodedContinuityDiscontinuity =
  | {
      readonly [decodedProjectionBrand]: true;
      readonly source: "delta";
      readonly reason: DeltaResyncReason;
      readonly key: string;
      readonly trusted: TrustedProjectionPosition;
      readonly incoming: {
        readonly deltaId: string;
        readonly viewEpoch: number;
        readonly viewCursor: number;
        readonly baseProjectionVersion: number;
        readonly resultProjectionVersion: number;
      };
    }
  | {
      readonly [decodedProjectionBrand]: true;
      readonly source: "control";
      readonly reason: "server_resync";
      readonly key: string;
      readonly trusted: TrustedProjectionPosition;
      readonly incoming: {
        readonly viewEpoch: number;
        readonly snapshotCursor: number;
      };
    };

export interface DecodedIntegrityFailure {
  readonly [decodedProjectionBrand]: true;
  readonly ingress: "delta";
  readonly eventFamily: "projection_delta";
  readonly replayable: true;
  readonly key: string;
  readonly safeReason: IntegrityFailureReason;
}

export interface DecodedProjectionSnapshot<T> {
  readonly [decodedProjectionBrand]: true;
  readonly envelope: DeepReadonly<ProjectionSnapshot<T>>;
  readonly effective: TrustedViewProjectionState<T>;
  readonly requestContext: {
    readonly requestedClientCommandIds: readonly string[];
  };
}

export interface DecodedProjectionDelta<T> {
  readonly [decodedProjectionBrand]: true;
  readonly envelope: DeepReadonly<ProjectionDelta<T>>;
  readonly effective: TrustedViewProjectionState<T>;
}

export interface DecodedDeltaDecision<T> {
  readonly [decodedProjectionBrand]: true;
  readonly result:
    | { readonly kind: "duplicate"; readonly identity: DecodedDeltaIdentity }
    | {
        readonly kind: "resync_required";
        readonly discontinuity: DecodedContinuityDiscontinuity & { readonly source: "delta" };
      }
    | { readonly kind: "apply"; readonly delta: DecodedProjectionDelta<T> };
}

export interface DecodedProjectionControl {
  readonly [decodedProjectionBrand]: true;
  readonly envelope: DeepReadonly<ProjectionControlEvent>;
  readonly result:
    | {
        readonly kind: "resync_required";
        readonly discontinuity: DecodedContinuityDiscontinuity & { readonly source: "control" };
      }
    | { readonly kind: "client_upgrade_required" };
}

export interface BoundViewProjectionRuntimeDecoder<T> {
  readonly [boundDecoderBrand]: true;
  readonly contractKey: string;
  readonly viewType: string;
  readonly viewId: string;
  readonly decodeSnapshot: (
    value: unknown,
    context: { readonly requestedClientCommandIds: readonly string[] },
  ) => DecodedProjectionSnapshot<T>;
  readonly decodeDeltaFrame: (
    frame: ProjectionSSEFrame,
    current: TrustedViewProjectionState<T>,
  ) => DecodedDeltaDecision<T>;
  readonly decodeControlFrame: (
    frame: ProjectionSSEFrame,
    current: TrustedViewProjectionState<T>,
  ) => DecodedProjectionControl;
}

export class ViewProjectionDecodeError extends Error {
  readonly classification: ViewProjectionDecodeClassification;
  readonly safeReason: string;
  readonly ingress: "snapshot" | "delta" | "control";
  readonly expectedIdentity: Readonly<{ viewType: string; viewId: string }>;
  readonly integrityFailure?: DecodedIntegrityFailure;

  constructor(options: {
    classification: ViewProjectionDecodeClassification;
    safeReason: string;
    ingress: "snapshot" | "delta" | "control";
    expectedIdentity: Readonly<{ viewType: string; viewId: string }>;
    integrityFailure?: DecodedIntegrityFailure;
  }) {
    super(`View projection ${options.ingress} decode failed: ${options.classification}/${options.safeReason}`);
    this.name = "ViewProjectionDecodeError";
    this.classification = options.classification;
    this.safeReason = options.safeReason;
    this.ingress = options.ingress;
    this.expectedIdentity = options.expectedIdentity;
    this.integrityFailure = options.integrityFailure;
  }
}

class ViewContractRejection extends Error {
  readonly safeReason: InternalViewContractRejectionReason;

  constructor(safeReason: InternalViewContractRejectionReason) {
    super("View projection product contract rejected the value");
    this.name = "ViewContractRejection";
    this.safeReason = safeReason;
  }
}

/** @internal Lets a projection-owned product contract select one closed safe reason. */
export function internalRejectViewContract(
  safeReason: InternalViewContractRejectionReason,
): never {
  throw new ViewContractRejection(safeReason);
}

interface InternalRuntimeDecoderOptions<T> {
  readonly contractKey: string;
  readonly viewType: string;
  readonly viewId: string;
  readonly contract: InternalViewContract<T>;
}

interface CapturedInternalViewContract<T> {
  readonly allowedDeltaEventTypes: readonly string[];
  readonly canonicalRelatedRefTypes: readonly string[];
  readonly decodeData: InternalViewContract<T>["decodeData"];
  readonly validateState: InternalViewContract<T>["validateState"];
}

/** @internal Imported only by closed production factories under src/projections. */
export function internalCreateViewProjectionRuntimeDecoder<T>(
  options: InternalRuntimeDecoderOptions<T>,
): BoundViewProjectionRuntimeDecoder<T> {
  const expectedIdentity = Object.freeze({
    viewType: requireCanonicalTrustedIdentity(options.viewType, "view type"),
    viewId: requireCanonicalTrustedIdentity(options.viewId, "view id"),
  });
  const contractKey = requireCanonicalTrustedIdentity(options.contractKey, "contract key");
  const contract = captureInternalViewContract(options.contract);
  const allowedDeltaEventTypes = new Set(contract.allowedDeltaEventTypes);
  const canonicalRelatedRefTypes = new Set(contract.canonicalRelatedRefTypes);

  const decoder: BoundViewProjectionRuntimeDecoder<T> = {
    [boundDecoderBrand]: true,
    contractKey,
    viewType: expectedIdentity.viewType,
    viewId: expectedIdentity.viewId,
    decodeSnapshot(value, context) {
      try {
        const record = requireIngressRecord(value, "snapshot", expectedIdentity);
        validateBoundIdentity(record, "snapshot", expectedIdentity);
        validateSchema(record.schema_version, "projection_snapshot", "snapshot", expectedIdentity);

        const viewEpoch = requirePositiveSafeInteger(record.view_epoch, "snapshot", expectedIdentity);
        const viewCursor = requireNonNegativeSafeInteger(record.view_cursor, "snapshot", expectedIdentity);
        const projectionVersion = requirePositiveSafeInteger(
          record.projection_version,
          "snapshot",
          expectedIdentity,
        );
        const tenantId = requireCanonicalIdentity(record.tenant_id, "snapshot", expectedIdentity);
        const queryFingerprint = requireCanonicalIdentity(
          record.query_fingerprint,
          "snapshot",
          expectedIdentity,
        );
        const generatedAt = requireTimestamp(record.generated_at, "snapshot", expectedIdentity);
        const requestedClientCommandIds = normalizeRequestedClientCommandIds(context, expectedIdentity);
        const commandReceipts = parseCommandReceipts(
          record,
          requestedClientCommandIds,
          expectedIdentity,
        );
        const data = decodeContractData(contract.decodeData, record.data, "snapshot", expectedIdentity);
        const availableActions = parseActionRefs(record.available_actions, "snapshot", expectedIdentity);
        const relatedRefs = parseRelatedRefs(
          record.related_refs,
          "snapshot",
          expectedIdentity,
          canonicalRelatedRefTypes,
        );
        const effective: TrustedViewProjectionState<T> = canonicalFreeze({
          data: data as DeepReadonly<T>,
          availableActions,
          relatedRefs,
          viewEpoch,
          viewCursor,
          projectionVersion,
        }, "snapshot", expectedIdentity);
        validateContractState(contract.validateState, effective, "snapshot", expectedIdentity);

        const envelope: ProjectionSnapshot<T> = {
          schema_version: "frontier.projection_snapshot.v1",
          view_type: expectedIdentity.viewType,
          view_id: expectedIdentity.viewId,
          tenant_id: tenantId,
          view_epoch: viewEpoch,
          view_cursor: viewCursor,
          projection_version: projectionVersion,
          query_fingerprint: queryFingerprint,
          generated_at: generatedAt,
          data: effective.data as unknown as T,
          available_actions: effective.availableActions as unknown as ActionRef[],
          related_refs: effective.relatedRefs as unknown as RelatedRef[],
          ...(commandReceipts === undefined ? {} : { command_receipts: commandReceipts }),
        };
        return canonicalFreeze<DecodedProjectionSnapshot<T>>({
          [decodedProjectionBrand]: true as const,
          envelope: envelope as DeepReadonly<ProjectionSnapshot<T>>,
          effective,
          requestContext: { requestedClientCommandIds },
        }, "snapshot", expectedIdentity);
      } catch (error: unknown) {
        throw normalizeIngressFailure(error, "snapshot", expectedIdentity);
      }
    },
    decodeDeltaFrame(frame, current) {
      try {
        const record = parseFrameRecord(frame, "delta", expectedIdentity);
        validateBoundIdentity(record, "delta", expectedIdentity);
        validateSchema(record.schema_version, "projection_delta", "delta", expectedIdentity);

        const deltaId = requireCanonicalIdentity(record.delta_id, "delta", expectedIdentity);
        const viewEpoch = requirePositiveSafeInteger(record.view_epoch, "delta", expectedIdentity);
        const viewCursor = requirePositiveSafeInteger(record.view_cursor, "delta", expectedIdentity);
        const baseProjectionVersion = requireNonNegativeSafeInteger(
          record.base_projection_version,
          "delta",
          expectedIdentity,
        );
        const resultProjectionVersion = requirePositiveSafeInteger(
          record.result_projection_version,
          "delta",
          expectedIdentity,
        );
        if (
          baseProjectionVersion === Number.MAX_SAFE_INTEGER
          || resultProjectionVersion !== baseProjectionVersion + 1
        ) {
          throw decodeError("invalid_contract", "malformed_contract", "delta", expectedIdentity);
        }
        const eventType = requireCanonicalIdentity(record.event_type, "delta", expectedIdentity);
        const critical = requireBoolean(record.critical, "delta", expectedIdentity);
        const commandId = requireOptionalCanonicalIdentity(record.command_id, "delta", expectedIdentity);
        const clientCommandId = requireOptionalCanonicalIdentity(
          record.client_command_id,
          "delta",
          expectedIdentity,
        );
        if (clientCommandId !== undefined && commandId === undefined) {
          throw decodeError("invalid_contract", "malformed_contract", "delta", expectedIdentity);
        }
        const sourceRefs = parseSourceRefs(record.source_refs, "delta", expectedIdentity);
        validateDeltaFrameBinding(frame, viewEpoch, viewCursor, expectedIdentity);
        const trusted = requireTrustedPosition(current, "delta", expectedIdentity);
        const identity = { deltaId, viewEpoch, viewCursor };

        if (viewEpoch === trusted.viewEpoch && viewCursor <= trusted.viewCursor) {
          return canonicalFreeze<DecodedDeltaDecision<T>>({
            [decodedProjectionBrand]: true as const,
            result: { kind: "duplicate", identity },
          }, "delta", expectedIdentity);
        }

        const discontinuityReason = deltaDiscontinuityReason(
          trusted,
          viewEpoch,
          viewCursor,
          baseProjectionVersion,
        );
        if (discontinuityReason !== undefined) {
          const incoming = {
            deltaId,
            viewEpoch,
            viewCursor,
            baseProjectionVersion,
            resultProjectionVersion,
          };
          const discontinuity = {
            [decodedProjectionBrand]: true as const,
            source: "delta" as const,
            reason: discontinuityReason,
            key: deltaDiscontinuityKey(discontinuityReason, trusted, incoming),
            trusted,
            incoming,
          };
          return canonicalFreeze<DecodedDeltaDecision<T>>({
            [decodedProjectionBrand]: true as const,
            result: { kind: "resync_required", discontinuity },
          }, "delta", expectedIdentity);
        }

        if (!allowedDeltaEventTypes.has(eventType)) {
          throw decodeError("invalid_contract", "authority_violation", "delta", expectedIdentity);
        }
        const rawPayload = requireIngressRecord(record.payload, "delta", expectedIdentity);
        const hasData = Object.hasOwn(rawPayload, "data");
        const data = hasData
          ? decodeContractData(contract.decodeData, rawPayload.data, "delta", expectedIdentity)
          : current.data as unknown as T;
        const hasAvailableActions = Object.hasOwn(rawPayload, "available_actions");
        const availableActions = hasAvailableActions
          ? parseActionRefs(rawPayload.available_actions, "delta", expectedIdentity)
          : [...current.availableActions];
        const hasRelatedRefs = Object.hasOwn(rawPayload, "related_refs");
        const relatedRefs = hasRelatedRefs
          ? parseRelatedRefs(
              rawPayload.related_refs,
              "delta",
              expectedIdentity,
              canonicalRelatedRefTypes,
            )
          : [...current.relatedRefs];
        const displayCheckpoint = Object.hasOwn(rawPayload, "display_checkpoint")
          ? parseDisplayCheckpoint(
            rawPayload.display_checkpoint,
            expectedIdentity,
          )
          : undefined;

        const effective: TrustedViewProjectionState<T> = canonicalFreeze({
          data: data as DeepReadonly<T>,
          availableActions,
          relatedRefs,
          viewEpoch,
          viewCursor,
          projectionVersion: resultProjectionVersion,
        }, "delta", expectedIdentity);
        validateContractState(contract.validateState, effective, "delta", expectedIdentity);
        const payload: ProjectionDeltaPayload<T> = {
          ...(hasData ? { data: effective.data as unknown as T } : {}),
          ...(hasAvailableActions
            ? { available_actions: effective.availableActions as unknown as ActionRef[] }
            : {}),
          ...(hasRelatedRefs
            ? { related_refs: effective.relatedRefs as unknown as RelatedRef[] }
            : {}),
          ...(displayCheckpoint === undefined ? {} : { display_checkpoint: displayCheckpoint }),
        };
        const envelope: ProjectionDelta<T> = {
          schema_version: "frontier.projection_delta.v1",
          delta_id: deltaId,
          view_type: expectedIdentity.viewType,
          view_id: expectedIdentity.viewId,
          view_epoch: viewEpoch,
          view_cursor: viewCursor,
          base_projection_version: baseProjectionVersion,
          result_projection_version: resultProjectionVersion,
          event_type: eventType,
          critical,
          ...(commandId === undefined ? {} : { command_id: commandId }),
          ...(clientCommandId === undefined ? {} : { client_command_id: clientCommandId }),
          source_refs: sourceRefs,
          payload,
        };
        const decodedDelta: DecodedProjectionDelta<T> = {
          [decodedProjectionBrand]: true as const,
          envelope: envelope as DeepReadonly<ProjectionDelta<T>>,
          effective,
        };
        return canonicalFreeze<DecodedDeltaDecision<T>>({
          [decodedProjectionBrand]: true as const,
          result: { kind: "apply", delta: decodedDelta },
        }, "delta", expectedIdentity);
      } catch (error: unknown) {
        throw attachDeltaIntegrityFailure(error, frame, expectedIdentity);
      }
    },
    decodeControlFrame(frame, current) {
      try {
        const record = parseFrameRecord(frame, "control", expectedIdentity);
        validateBoundIdentity(record, "control", expectedIdentity);
        validateSchema(record.schema_version, "projection_control", "control", expectedIdentity);
        const eventType = record.event_type;
        if (eventType !== "resync_required" && eventType !== "client_upgrade_required") {
          throw decodeError("invalid_contract", "malformed_contract", "control", expectedIdentity);
        }
        const viewEpoch = requirePositiveSafeInteger(record.view_epoch, "control", expectedIdentity);
        const snapshotCursor = requireNonNegativeSafeInteger(
          record.snapshot_cursor,
          "control",
          expectedIdentity,
        );
        const trusted = requireTrustedPosition(current, "control", expectedIdentity);
        validateControlFrameBinding(frame, eventType, trusted, expectedIdentity);
        const envelope: ProjectionControlEvent = {
          schema_version: "frontier.projection_control.v1",
          event_type: eventType,
          view_type: expectedIdentity.viewType,
          view_id: expectedIdentity.viewId,
          view_epoch: viewEpoch,
          snapshot_cursor: snapshotCursor,
        };
        if (eventType === "client_upgrade_required") {
          return canonicalFreeze<DecodedProjectionControl>({
            [decodedProjectionBrand]: true as const,
            envelope,
            result: { kind: "client_upgrade_required" },
          }, "control", expectedIdentity);
        }
        const discontinuity = {
          [decodedProjectionBrand]: true as const,
          source: "control" as const,
          reason: "server_resync" as const,
          key: controlDiscontinuityKey(trusted, viewEpoch, snapshotCursor),
          trusted,
          incoming: { viewEpoch, snapshotCursor },
        };
        return canonicalFreeze<DecodedProjectionControl>({
          [decodedProjectionBrand]: true as const,
          envelope,
          result: { kind: "resync_required", discontinuity },
        }, "control", expectedIdentity);
      } catch (error: unknown) {
        throw normalizeIngressFailure(error, "control", expectedIdentity);
      }
    },
  };
  return Object.freeze(decoder);
}

type Ingress = "snapshot" | "delta" | "control";
type ExpectedIdentity = Readonly<{ viewType: string; viewId: string }>;
type ContractRecord = Record<string, unknown>;

function canonicalFreeze<T>(
  value: T,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): T {
  try {
    if (containsAsyncProjectionValue(value)) {
      throw new Error("async projection values are forbidden");
    }
    return cloneAndFreezeCanonical(value, new WeakSet<object>());
  } catch {
    throw decodeError("invalid_contract", "authority_violation", ingress, expectedIdentity);
  }
}

function cloneAndFreezeCanonical<T>(
  value: T,
  ancestors: WeakSet<object>,
): T {
  if (
    value === undefined
    || value === null
    || typeof value === "string"
    || typeof value === "boolean"
  ) {
    return value;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new Error("non-finite projection value");
    return value;
  }
  if (typeof value !== "object") throw new Error("non-canonical projection value");
  if (ancestors.has(value)) throw new Error("cyclic projection value");
  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      const clone: unknown[] = [];
      for (let index = 0; index < value.length; index += 1) {
        if (!Object.hasOwn(value, index)) throw new Error("sparse projection array");
        clone.push(cloneAndFreezeCanonical(value[index], ancestors));
      }
      const extraKeys = Reflect.ownKeys(value).filter((key) => (
        key !== "length"
        && (typeof key !== "string" || !/^(0|[1-9][0-9]*)$/.test(key))
      ));
      if (extraKeys.length > 0) throw new Error("non-canonical projection array");
      return Object.freeze(clone) as T;
    }

    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) {
      throw new Error("non-plain projection object");
    }
    const clone: Record<PropertyKey, unknown> = {};
    for (const key of Reflect.ownKeys(value)) {
      if (typeof key === "symbol" && key !== decodedProjectionBrand) {
        throw new Error("non-canonical projection symbol");
      }
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (descriptor === undefined || !("value" in descriptor) || !descriptor.enumerable) {
        throw new Error("non-canonical projection property");
      }
      Object.defineProperty(clone, key, {
        configurable: false,
        enumerable: true,
        value: cloneAndFreezeCanonical(descriptor.value, ancestors),
        writable: false,
      });
    }
    return Object.freeze(clone) as T;
  } finally {
    ancestors.delete(value);
  }
}

function decodeError(
  classification: ViewProjectionDecodeClassification,
  safeReason: string,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): ViewProjectionDecodeError {
  return trustProjectionDecodeError(new ViewProjectionDecodeError({
    classification,
    safeReason,
    ingress,
    expectedIdentity,
  }));
}

function normalizeIngressFailure(
  error: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): ViewProjectionDecodeError {
  if (
    error !== null
    && (typeof error === "object" || typeof error === "function")
    && trustedProjectionDecodeErrors.has(error)
  ) {
    const trustedError = error as ViewProjectionDecodeError;
    if (
      trustedError.ingress === ingress
      && trustedError.expectedIdentity.viewType === expectedIdentity.viewType
      && trustedError.expectedIdentity.viewId === expectedIdentity.viewId
    ) {
      return trustedError;
    }
  }
  return decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
}

function trustProjectionDecodeError(
  error: ViewProjectionDecodeError,
): ViewProjectionDecodeError {
  trustedProjectionDecodeErrors.add(error);
  return Object.freeze(error);
}

function requireIngressRecord(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): ContractRecord {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value as ContractRecord;
}

function parseFrameRecord(
  frame: ProjectionSSEFrame,
  ingress: "delta" | "control",
  expectedIdentity: ExpectedIdentity,
): ContractRecord {
  if (frame === null || typeof frame !== "object" || Array.isArray(frame)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  if (typeof frame.data !== "string") {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  let value: unknown;
  try {
    value = JSON.parse(frame.data);
  } catch {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return requireIngressRecord(value, ingress, expectedIdentity);
}

function validateBoundIdentity(
  record: ContractRecord,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): void {
  const viewType = requireCanonicalIdentity(record.view_type, ingress, expectedIdentity);
  const viewId = requireCanonicalIdentity(record.view_id, ingress, expectedIdentity);
  if (viewType !== expectedIdentity.viewType || viewId !== expectedIdentity.viewId) {
    throw decodeError("foreign_identity", "foreign_identity", ingress, expectedIdentity);
  }
}

function validateSchema(
  value: unknown,
  family: "projection_snapshot" | "projection_delta" | "projection_control",
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): void {
  if (typeof value !== "string") {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  const match = new RegExp(`^frontier\\.${family}\\.v([1-9][0-9]*)$`).exec(value);
  if (!match) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  if (match[1] !== "1") {
    throw decodeError("unsupported_schema", "unsupported_schema", ingress, expectedIdentity);
  }
}

function requireCanonicalTrustedIdentity(value: unknown, label: string): string {
  if (!isCanonicalIdentity(value)) throw new Error(`${label} is invalid`);
  return value;
}

function requireCanonicalIdentity(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): string {
  if (!isCanonicalIdentity(value)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value;
}

function requireOptionalCanonicalIdentity(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): string | undefined {
  if (value === undefined) return undefined;
  return requireCanonicalIdentity(value, ingress, expectedIdentity);
}

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

function requireBoolean(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): boolean {
  if (typeof value !== "boolean") {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value;
}

function requireArray(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): unknown[] {
  if (!Array.isArray(value)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return [...value];
}

function normalizeTrustedContractIdentities(values: readonly string[], label: string): string[] {
  if (!Array.isArray(values)) throw new Error(`${label} allowlist is invalid`);
  const normalized = new Set<string>();
  for (const value of values) normalized.add(requireCanonicalTrustedIdentity(value, label));
  return [...normalized];
}

function captureInternalViewContract<T>(
  contract: InternalViewContract<T>,
): CapturedInternalViewContract<T> {
  if (contract === null || typeof contract !== "object" || Array.isArray(contract)) {
    throw new Error("view contract is invalid");
  }
  const decodeData = contract.decodeData;
  const validateState = contract.validateState;
  if (typeof decodeData !== "function" || typeof validateState !== "function") {
    throw new Error("view contract functions are invalid");
  }
  return Object.freeze({
    allowedDeltaEventTypes: Object.freeze(normalizeTrustedContractIdentities(
      contract.allowedDeltaEventTypes,
      "allowed Delta event type",
    )),
    canonicalRelatedRefTypes: Object.freeze(normalizeTrustedContractIdentities(
      contract.canonicalRelatedRefTypes,
      "canonical related ref type",
    )),
    decodeData,
    validateState,
  });
}

function normalizeRequestedClientCommandIds(
  context: { readonly requestedClientCommandIds: readonly string[] },
  expectedIdentity: ExpectedIdentity,
): readonly string[] {
  if (context === null || typeof context !== "object" || Array.isArray(context)) {
    throw decodeError("invalid_contract", "malformed_contract", "snapshot", expectedIdentity);
  }
  const values = context.requestedClientCommandIds;
  if (!Array.isArray(values)) {
    throw decodeError("invalid_contract", "malformed_contract", "snapshot", expectedIdentity);
  }
  const normalized = new Set<string>();
  for (const value of values) {
    normalized.add(requireCanonicalIdentity(value, "snapshot", expectedIdentity));
    if (normalized.size > MAX_RECONCILE_CLIENT_COMMAND_IDS) {
      throw decodeError("invalid_contract", "authority_violation", "snapshot", expectedIdentity);
    }
  }
  return Object.freeze([...normalized].sort());
}

function parseCommandReceipts(
  snapshot: ContractRecord,
  requestedClientCommandIds: readonly string[],
  expectedIdentity: ExpectedIdentity,
): CommandReceipt[] | undefined {
  const hasReceipts = Object.hasOwn(snapshot, "command_receipts");
  if (!hasReceipts) return undefined;
  if (requestedClientCommandIds.length === 0) {
    throw decodeError("invalid_contract", "authority_violation", "snapshot", expectedIdentity);
  }
  const values = requireArray(snapshot.command_receipts, "snapshot", expectedIdentity);
  if (values.length > MAX_RECONCILE_CLIENT_COMMAND_IDS) {
    throw decodeError("invalid_contract", "authority_violation", "snapshot", expectedIdentity);
  }

  const requested = new Set(requestedClientCommandIds);
  const serverByClient = new Map<string, string>();
  const clientByServer = new Map<string, string>();
  for (const value of values) {
    const receipt = requireIngressRecord(value, "snapshot", expectedIdentity);
    const clientCommandId = requireCanonicalIdentity(
      receipt.client_command_id,
      "snapshot",
      expectedIdentity,
    );
    const commandId = requireCanonicalIdentity(receipt.command_id, "snapshot", expectedIdentity);
    if (!requested.has(clientCommandId)) {
      throw decodeError("invalid_contract", "authority_violation", "snapshot", expectedIdentity);
    }
    const existingServer = serverByClient.get(clientCommandId);
    const existingClient = clientByServer.get(commandId);
    if (
      (existingServer !== undefined && existingServer !== commandId)
      || (existingClient !== undefined && existingClient !== clientCommandId)
    ) {
      throw decodeError("invalid_contract", "authority_violation", "snapshot", expectedIdentity);
    }
    serverByClient.set(clientCommandId, commandId);
    clientByServer.set(commandId, clientCommandId);
  }

  return [...serverByClient]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([clientCommandId, commandId]) => ({
      client_command_id: clientCommandId,
      command_id: commandId,
    }));
}

function parseActionRefs(
  value: unknown,
  ingress: "snapshot" | "delta",
  expectedIdentity: ExpectedIdentity,
): ActionRef[] {
  return requireArray(value, ingress, expectedIdentity).map((item) => {
    const action = requireIngressRecord(item, ingress, expectedIdentity);
    const metadata = action.metadata === undefined
      ? undefined
      : { ...requireIngressRecord(action.metadata, ingress, expectedIdentity) };
    return {
      action: requireCanonicalIdentity(action.action, ingress, expectedIdentity),
      target_id: requireCanonicalIdentity(action.target_id, ingress, expectedIdentity),
      expected_revision: requirePositiveSafeInteger(
        action.expected_revision,
        ingress,
        expectedIdentity,
      ),
      ...(metadata === undefined ? {} : { metadata }),
    };
  });
}

function parseRelatedRefs(
  value: unknown,
  ingress: "snapshot" | "delta",
  expectedIdentity: ExpectedIdentity,
  canonicalRelatedRefTypes: ReadonlySet<string>,
): RelatedRef[] {
  return requireArray(value, ingress, expectedIdentity).map((item) => {
    const reference = requireIngressRecord(item, ingress, expectedIdentity);
    const type = requireCanonicalIdentity(reference.type, ingress, expectedIdentity);
    const id = requireCanonicalIdentity(reference.id, ingress, expectedIdentity);
    const href = requireCanonicalIdentity(reference.href, ingress, expectedIdentity);
    if (!canonicalRelatedRefTypes.has(type)) {
      throw decodeError("invalid_contract", "authority_violation", ingress, expectedIdentity);
    }
    let expectedHref: string;
    try {
      expectedHref = `/api/v1/views/${encodeURIComponent(type)}/${encodeURIComponent(id)}`;
    } catch {
      throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
    }
    if (href !== expectedHref) {
      throw decodeError("invalid_contract", "authority_violation", ingress, expectedIdentity);
    }
    return { type, id, href };
  });
}

function parseSourceRefs(
  value: unknown,
  ingress: "delta",
  expectedIdentity: ExpectedIdentity,
): SourceRef[] {
  return requireArray(value, ingress, expectedIdentity).map((item) => {
    const reference = requireIngressRecord(item, ingress, expectedIdentity);
    return {
      type: requireCanonicalIdentity(reference.type, ingress, expectedIdentity),
      id: requireCanonicalIdentity(reference.id, ingress, expectedIdentity),
    };
  });
}

function parseDisplayCheckpoint(
  value: unknown,
  expectedIdentity: ExpectedIdentity,
): NonNullable<ProjectionDeltaPayload<unknown>["display_checkpoint"]> {
  const checkpoint = requireIngressRecord(value, "delta", expectedIdentity);
  return {
    display_stream_id: requireCanonicalIdentity(
      checkpoint.display_stream_id,
      "delta",
      expectedIdentity,
    ),
    checkpoint_version: requirePositiveSafeInteger(
      checkpoint.checkpoint_version,
      "delta",
      expectedIdentity,
    ),
  };
}

function requireTimestamp(
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): string {
  if (typeof value !== "string") {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!match) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (
    month < 1
    || month > 12
    || day < 1
    || day > daysInMonth[month - 1]
    || hour > 23
    || minute > 59
    || second > 59
  ) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return value;
}

function decodeContractData<T>(
  decodeData: InternalViewContract<T>["decodeData"],
  value: unknown,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): T {
  try {
    const decoded = decodeData(value);
    if (containsAsyncProjectionValue(decoded)) {
      throw new Error("async view data decoders are forbidden");
    }
    return decoded as T;
  } catch (error: unknown) {
    if (error instanceof ViewContractRejection) {
      throw decodeError("invalid_contract", error.safeReason, ingress, expectedIdentity);
    }
    throw decodeError("invalid_contract", "authority_violation", ingress, expectedIdentity);
  }
}

function validateContractState<T>(
  validateState: InternalViewContract<T>["validateState"],
  state: InternalViewContractState<T>,
  ingress: Ingress,
  expectedIdentity: ExpectedIdentity,
): void {
  try {
    const result: unknown = validateState(state);
    if (containsAsyncProjectionValue(result)) {
      throw new Error("view state validators must complete synchronously");
    }
    if (result !== undefined) {
      throw new Error("view state validators must complete synchronously");
    }
  } catch (error: unknown) {
    if (error instanceof ViewContractRejection) {
      throw decodeError("invalid_contract", error.safeReason, ingress, expectedIdentity);
    }
    throw decodeError("invalid_contract", "authority_violation", ingress, expectedIdentity);
  }
}

function containsAsyncProjectionValue(value: unknown): boolean {
  return scanAsyncProjectionValues(value, new WeakSet<object>());
}

function scanAsyncProjectionValues(
  value: unknown,
  visited: WeakSet<object>,
): boolean {
  if (
    (typeof value !== "object" || value === null)
    && typeof value !== "function"
  ) {
    return false;
  }

  if (visited.has(value)) return false;
  visited.add(value);
  const isNativePromise = observeNativePromiseSettlement(value);
  let containsAsyncValue = isNativePromise
    || hasThenableShapeWithoutInvoking(value);

  let keys: readonly PropertyKey[];
  try {
    keys = Reflect.ownKeys(value);
  } catch {
    return true;
  }
  for (const key of keys) {
    let descriptor: PropertyDescriptor | undefined;
    try {
      descriptor = Object.getOwnPropertyDescriptor(value, key);
    } catch {
      containsAsyncValue = true;
      continue;
    }
    if (descriptor === undefined || !("value" in descriptor)) continue;
    const childContainsAsyncValue = scanAsyncProjectionValues(descriptor.value, visited);
    containsAsyncValue = childContainsAsyncValue || containsAsyncValue;
  }
  return containsAsyncValue;
}

function observeNativePromiseSettlement(value: object): boolean {
  if (observedNativePromises.has(value)) return true;
  try {
    // Calling the captured intrinsic performs the native Promise internal-slot
    // brand check without reading or invoking an untrusted `.then` property.
    // Native cross-realm Promises and Promise subclasses retain that slot.
    const observeSettlementValue = (settledValue: unknown): undefined => {
      try {
        containsAsyncProjectionValue(settledValue);
      } catch {
        // The decoder has already failed closed; this continuation exists only
        // to observe nested rejected native Promises in a settlement value.
      }
      return undefined;
    };
    void intrinsicReflectApply(intrinsicPromiseThen, value, [
      observeSettlementValue,
      observeSettlementValue,
    ]);
    observedNativePromises.add(value);
    return true;
  } catch {
    // The standard API has no species-free Promise reaction primitive. If a
    // poisoned constructor/@@species throws before the intrinsic can register
    // a reaction, fail closed without mutating the value or assimilating its
    // public `.then`. The synchronous contract type excludes this from honest
    // product implementations; reaching it requires a malicious cast or other
    // repository code, because unknown JSON cannot carry a Promise object.
    return false;
  }
}

function hasThenableShapeWithoutInvoking(value: object): boolean {
  const visited = new WeakSet<object>();
  let candidate: object | null = value;
  while (candidate !== null) {
    if (visited.has(candidate)) return true;
    visited.add(candidate);
    try {
      const descriptor = Object.getOwnPropertyDescriptor(candidate, "then");
      if (descriptor !== undefined) {
        return "value" in descriptor ? typeof descriptor.value === "function" : true;
      }
      candidate = Object.getPrototypeOf(candidate) as object | null;
    } catch {
      return true;
    }
  }
  return false;
}

function requireTrustedPosition<T>(
  state: TrustedViewProjectionState<T>,
  ingress: "delta" | "control",
  expectedIdentity: ExpectedIdentity,
): TrustedProjectionPosition {
  if (state === null || typeof state !== "object" || Array.isArray(state)) {
    throw decodeError("invalid_contract", "malformed_contract", ingress, expectedIdentity);
  }
  return {
    viewEpoch: requirePositiveSafeInteger(state.viewEpoch, ingress, expectedIdentity),
    viewCursor: requireNonNegativeSafeInteger(state.viewCursor, ingress, expectedIdentity),
    projectionVersion: requirePositiveSafeInteger(state.projectionVersion, ingress, expectedIdentity),
  };
}

function validateDeltaFrameBinding(
  frame: ProjectionSSEFrame,
  viewEpoch: number,
  viewCursor: number,
  expectedIdentity: ExpectedIdentity,
): void {
  if (
    frame.eventName !== "projection_delta"
    || frame.lastEventId !== `${viewEpoch}:${viewCursor}`
  ) {
    throw decodeError("invalid_contract", "sse_binding_mismatch", "delta", expectedIdentity);
  }
}

function validateControlFrameBinding(
  frame: ProjectionSSEFrame,
  eventType: ProjectionControlEvent["event_type"],
  trusted: TrustedProjectionPosition,
  expectedIdentity: ExpectedIdentity,
): void {
  if (
    frame.eventName !== eventType
    || (frame.lastEventId !== "" && frame.lastEventId !== `${trusted.viewEpoch}:${trusted.viewCursor}`)
  ) {
    throw decodeError("invalid_contract", "sse_binding_mismatch", "control", expectedIdentity);
  }
}

function deltaDiscontinuityReason(
  trusted: TrustedProjectionPosition,
  viewEpoch: number,
  viewCursor: number,
  baseProjectionVersion: number,
): DeltaResyncReason | undefined {
  if (viewEpoch !== trusted.viewEpoch) return "epoch_mismatch";
  if (
    trusted.viewCursor === Number.MAX_SAFE_INTEGER
    || viewCursor !== trusted.viewCursor + 1
  ) {
    return "cursor_gap";
  }
  if (baseProjectionVersion !== trusted.projectionVersion) return "base_version_mismatch";
  return undefined;
}

function deltaDiscontinuityKey(
  reason: DeltaResyncReason,
  trusted: TrustedProjectionPosition,
  incoming: {
    readonly viewEpoch: number;
    readonly viewCursor: number;
    readonly baseProjectionVersion: number;
    readonly resultProjectionVersion: number;
  },
): string {
  return [
    "delta",
    reason,
    trusted.viewEpoch,
    trusted.viewCursor,
    trusted.projectionVersion,
    incoming.viewEpoch,
    incoming.viewCursor,
    incoming.baseProjectionVersion,
    incoming.resultProjectionVersion,
  ].join(":");
}

function controlDiscontinuityKey(
  trusted: TrustedProjectionPosition,
  viewEpoch: number,
  snapshotCursor: number,
): string {
  return [
    "control",
    "server_resync",
    trusted.viewEpoch,
    trusted.viewCursor,
    trusted.projectionVersion,
    viewEpoch,
    snapshotCursor,
  ].join(":");
}

function attachDeltaIntegrityFailure(
  error: unknown,
  frame: ProjectionSSEFrame,
  expectedIdentity: ExpectedIdentity,
): ViewProjectionDecodeError {
  const decodedError = normalizeIngressFailure(error, "delta", expectedIdentity);
  if (
    decodedError.classification !== "invalid_contract"
    || decodedError.integrityFailure !== undefined
  ) {
    return decodedError;
  }
  const replayId = canonicalReplayId(frame);
  if (replayId === undefined) return decodedError;
  const safeReason = isIntegrityFailureReason(decodedError.safeReason)
    ? decodedError.safeReason
    : "malformed_contract";
  const integrityFailure: DecodedIntegrityFailure = Object.freeze({
    [decodedProjectionBrand]: true as const,
    ingress: "delta",
    eventFamily: "projection_delta",
    replayable: true,
    key: `delta:${replayId}`,
    safeReason,
  });
  return trustProjectionDecodeError(new ViewProjectionDecodeError({
    classification: decodedError.classification,
    safeReason: decodedError.safeReason,
    ingress: decodedError.ingress,
    expectedIdentity,
    integrityFailure,
  }));
}

function canonicalReplayId(frame: ProjectionSSEFrame): string | undefined {
  try {
    if (frame === null || typeof frame !== "object" || Array.isArray(frame)) return undefined;
    const value = frame.lastEventId;
    if (typeof value !== "string") return undefined;
    const match = /^([1-9][0-9]*):(0|[1-9][0-9]*)$/.exec(value);
    if (!match) return undefined;
    const epoch = Number(match[1]);
    const cursor = Number(match[2]);
    if (!Number.isSafeInteger(epoch) || !Number.isSafeInteger(cursor)) return undefined;
    return value;
  } catch {
    return undefined;
  }
}

function isIntegrityFailureReason(value: string): value is IntegrityFailureReason {
  return value === "malformed_contract"
    || value === "authority_violation"
    || value === "sse_binding_mismatch";
}
