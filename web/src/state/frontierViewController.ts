import type { TransientDisplayDelta } from "../api/contracts";
import type {
  BoundProductionView,
  DataOfKind,
  ProductionViewKind,
} from "../projections/productionViewRegistry";
import {
  fetchBoundProjectionSnapshot,
  type SnapshotTransportOutcome,
} from "../projections/snapshotTransport";
import {
  ViewProjectionDecodeError,
  type DecodedContinuityDiscontinuity,
  type DecodedIntegrityFailure,
  type DecodedProjectionSnapshot,
  type ProjectionSSEFrame,
  type TrustedProjectionPosition,
  type TrustedViewProjectionState,
} from "../projections/viewRuntime";
import type { ClientViewReplica, ReplicaCommitDecision } from "./clientViewReplica";
import {
  FrontierSnapshotCoordinator,
  type SnapshotAttemptContext,
  type SnapshotCommitOutcome,
  type SnapshotFetchOutcome,
  type SnapshotStopDisposition,
} from "./frontierSnapshotCoordinator";

export interface EventStream {
  onopen: (() => void) | null;
  onerror: (() => void) | null;
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void;
  close(): void;
}

export type EventStreamFactory = (url: string) => EventStream;

export type FrontierViewStatus =
  | "connecting"
  | "live"
  | "reconnecting"
  | "error"
  | "client_upgrade_required";

export interface FrontierViewLifecycleState {
  status: FrontierViewStatus;
  error?: string;
}

export interface FrontierViewControllerOptions<K extends ProductionViewKind> {
  readonly view: BoundProductionView<K>;
  readonly replica: ClientViewReplica<DataOfKind<K>>;
  readonly fetcher?: typeof fetch;
  readonly streamFactory?: EventStreamFactory;
  readonly retryDelayMs?: number;
  readonly maxRetryDelayMs?: number;
  readonly onStateChange?: (state: FrontierViewLifecycleState) => void;
}

type SnapshotStage<T> =
  | { readonly kind: "decoded_snapshot"; readonly snapshot: DecodedProjectionSnapshot<T> }
  | { readonly kind: "access_clear" }
  | { readonly kind: "terminal_http" }
  | { readonly kind: "client_upgrade_required" }
  | { readonly kind: "invalid_snapshot" };

interface ContinuityGuard {
  readonly key: string;
  readonly trusted: TrustedProjectionPosition;
}

export class FrontierViewController<K extends ProductionViewKind> {
  private readonly view: BoundProductionView<K>;
  private readonly replica: ClientViewReplica<DataOfKind<K>>;
  private readonly streamFactory: EventStreamFactory;
  private readonly onStateChange?: (state: FrontierViewLifecycleState) => void;
  private readonly coordinator: FrontierSnapshotCoordinator<SnapshotStage<DataOfKind<K>>>;

  private stream?: EventStream;
  private streamToken?: symbol;
  private lifecycle?: FrontierViewLifecycleState;
  private continuityGuard?: ContinuityGuard;
  private integrityGuard?: { readonly key: string };
  private started = false;
  private terminal?: "client_upgrade_required" | "disposed";
  private unsubscribeCommandReconciliation: () => void;

  constructor(options: FrontierViewControllerOptions<K>) {
    this.view = options.view;
    this.replica = options.replica;
    this.streamFactory = options.streamFactory
      ?? ((url) => new EventSource(url) as unknown as EventStream);
    this.onStateChange = options.onStateChange;
    const fetcher = options.fetcher ?? ((input, init) => fetch(input, init));
    this.coordinator = new FrontierSnapshotCoordinator({
      fenceAndCloseStream: () => this.fenceAndCloseStream(),
      captureRequestedClientCommandIds: () => this.requestedClientCommandIds(),
      fetchSnapshot: (context) => this.fetchSnapshotStage(fetcher, context),
      commitSnapshot: (stage) => this.commitSnapshotStage(stage),
      beginManualRetry: () => {
        this.continuityGuard = undefined;
        this.integrityGuard = undefined;
        this.replica.beginManualRetry();
      },
      connectWhenIdle: () => this.connectWhenIdle(),
      onStopped: (disposition) => this.publishStopped(disposition),
      onClientUpgradeRequired: () => this.publishClientUpgrade(),
    }, {
      retryDelayMs: options.retryDelayMs,
      maxRetryDelayMs: options.maxRetryDelayMs,
    });
    this.unsubscribeCommandReconciliation = this.replica.subscribeCommandReconciliation(
      (decision) => this.handleAsynchronousCommandDecision(decision),
    );
  }

  start(): void {
    if (this.started || this.terminal) return;
    this.started = true;
    this.publishLoadingState();
    this.coordinator.start();
  }

  manualRetry(): void {
    if (!this.started || this.terminal) return;
    this.publishLoadingState();
    this.coordinator.manualRetry();
  }

  reconcileCommandStale(): void {
    if (!this.started || this.terminal) return;
    this.requestAutomatic({ kind: "command_stale" });
  }

  stop(): void {
    if (this.terminal === "disposed") return;
    this.terminal = "disposed";
    this.unsubscribeCommandReconciliation();
    this.coordinator.stop();
  }

  private async fetchSnapshotStage(
    fetcher: typeof fetch,
    context: SnapshotAttemptContext,
  ): Promise<SnapshotFetchOutcome<SnapshotStage<DataOfKind<K>>>> {
    try {
      const outcome = await fetchBoundProjectionSnapshot(
        this.view,
        context.requestedClientCommandIds,
        { fetcher },
      );
      return transportStage(outcome);
    } catch (cause) {
      if (cause instanceof ViewProjectionDecodeError) {
        return {
          kind: "decoded",
          snapshot: cause.classification === "unsupported_schema"
            ? { kind: "client_upgrade_required" }
            : { kind: "invalid_snapshot" },
        };
      }
      return { kind: "decoded", snapshot: { kind: "invalid_snapshot" } };
    }
  }

  private commitSnapshotStage(
    stage: SnapshotStage<DataOfKind<K>>,
  ): SnapshotCommitOutcome {
    switch (stage.kind) {
      case "decoded_snapshot": {
        const decision = this.replica.commitDecodedSnapshot(stage.snapshot);
        if (decision.kind === "committed") {
          if (
            this.continuityGuard
            && !samePosition(this.continuityGuard.trusted, stage.snapshot.effective)
          ) this.continuityGuard = undefined;
          return { kind: "committed" };
        }
        return this.handleSnapshotCommitDecision(decision);
      }
      case "access_clear":
        this.continuityGuard = undefined;
        this.integrityGuard = undefined;
        this.replica.clearAccess();
        return { kind: "stopped_manual_revivable", disposition: "access_clear" };
      case "terminal_http":
        this.continuityGuard = undefined;
        this.integrityGuard = undefined;
        return { kind: "stopped_manual_revivable", disposition: "terminal_http" };
      case "client_upgrade_required":
        return { kind: "client_upgrade_required" };
      case "invalid_snapshot":
        return { kind: "stopped_manual_revivable", disposition: "invalid_snapshot" };
    }
  }

  private handleSnapshotCommitDecision(decision: ReplicaCommitDecision): SnapshotCommitOutcome {
    switch (decision.kind) {
      case "committed":
        return { kind: "committed" };
      case "command_reconciliation_required":
        this.coordinator.requestAutomatic({
          kind: "command_reconciliation",
          cause: decision.cause,
        });
        return { kind: "recovery_queued" };
      case "command_reconciliation_blocked":
        return {
          kind: "stopped_manual_revivable",
          disposition: "command_reconciliation_blocked",
        };
      case "stale_decode_context":
        return { kind: "stopped_manual_revivable", disposition: "stale_decode_context" };
    }
  }

  private connectWhenIdle(): void {
    const current = this.trustedCurrentState();
    if (!current || this.terminal) return;
    const token = Symbol("frontierViewStream");
    const url = `/api/v1/views/${encodeURIComponent(this.view.viewType)}/${encodeURIComponent(this.view.viewId)}/events?cursor=${encodeURIComponent(`${current.viewEpoch}:${current.viewCursor}`)}`;
    const stream = this.streamFactory(url);
    this.streamToken = token;
    this.stream = stream;

    stream.onopen = () => {
      if (!this.isCurrentStream(stream, token) || this.replica.getState().sync !== "ready") return;
      this.replica.setConnection("connected");
      this.publishLifecycle({ status: "live" });
    };
    stream.addEventListener("projection_delta", (event) => {
      if (!this.isCurrentStream(stream, token)) return;
      this.handleDeltaFrame(event);
    });
    stream.addEventListener("transient_display_delta", (event) => {
      if (!this.isCurrentStream(stream, token)) return;
      const transient = decodeTransient(event.data);
      if (transient) this.replica.applyTransient(transient);
    });
    for (const eventName of ["resync_required", "client_upgrade_required"] as const) {
      stream.addEventListener(eventName, (event) => {
        if (!this.isCurrentStream(stream, token)) return;
        this.handleControlFrame(eventName, event);
      });
    }
    stream.onerror = () => {
      if (!this.isCurrentStream(stream, token)) return;
      this.fenceAndCloseStream();
      this.replica.setConnection("reconnecting");
      this.publishLifecycle({ status: "reconnecting" });
      this.coordinator.requestAutomaticAfterDelay({ kind: "sse_transport" });
    };
  }

  private handleDeltaFrame(event: MessageEvent<string>): void {
    const current = this.trustedCurrentState();
    if (!current) {
      this.stopPollutedStream("invalid_snapshot");
      return;
    }
    try {
      const decision = this.view.decoder.decodeDeltaFrame(sseFrame("projection_delta", event), current);
      switch (decision.result.kind) {
        case "duplicate":
          return;
        case "resync_required":
          this.requestContinuityRecovery(decision.result.discontinuity);
          return;
        case "apply": {
          const commit = this.replica.commitDecodedDelta(decision.result.delta);
          if (commit.kind === "committed") {
            this.continuityGuard = undefined;
            this.integrityGuard = undefined;
            return;
          }
          if (commit.kind === "command_reconciliation_required") {
            this.requestAutomatic({ kind: "command_reconciliation", cause: commit.cause });
            return;
          }
          this.stopPollutedStream(
            commit.kind === "command_reconciliation_blocked"
              ? "command_reconciliation_blocked"
              : "stale_decode_context",
          );
        }
      }
    } catch (cause) {
      this.handleDurableDecodeFailure(cause);
    }
  }

  private handleControlFrame(
    eventName: "resync_required" | "client_upgrade_required",
    event: MessageEvent<string>,
  ): void {
    const current = this.trustedCurrentState();
    if (!current) {
      this.stopPollutedStream("invalid_snapshot");
      return;
    }
    try {
      const decoded = this.view.decoder.decodeControlFrame(sseFrame(eventName, event), current);
      this.replica.commitDecodedControl(decoded);
      if (decoded.result.kind === "resync_required") {
        this.requestContinuityRecovery(decoded.result.discontinuity);
      } else {
        this.fenceAndCloseStream();
        this.coordinator.requireClientUpgrade();
      }
    } catch (cause) {
      this.handleDurableDecodeFailure(cause);
    }
  }

  private handleDurableDecodeFailure(cause: unknown): void {
    if (cause instanceof ViewProjectionDecodeError) {
      if (cause.classification === "unsupported_schema") {
        this.fenceAndCloseStream();
        this.coordinator.requireClientUpgrade();
        return;
      }
      if (cause.integrityFailure) {
        this.requestIntegrityRecovery(cause.integrityFailure);
        return;
      }
    }
    this.stopPollutedStream("invalid_snapshot");
  }

  private requestContinuityRecovery(discontinuity: DecodedContinuityDiscontinuity): void {
    if (this.continuityGuard) {
      this.stopPollutedStream("continuity_blocked");
      return;
    }
    this.continuityGuard = {
      key: discontinuity.key,
      trusted: discontinuity.trusted,
    };
    this.requestAutomatic({ kind: "continuity_discontinuity", discontinuity });
  }

  private requestIntegrityRecovery(failure: DecodedIntegrityFailure): void {
    if (this.integrityGuard) {
      this.stopPollutedStream("integrity_blocked");
      return;
    }
    this.integrityGuard = { key: failure.key };
    this.requestAutomatic({ kind: "integrity_failure", failure });
  }

  private requestAutomatic(
    cause: Parameters<FrontierSnapshotCoordinator["requestAutomatic"]>[0],
  ): void {
    const result = this.coordinator.requestAutomatic(cause);
    if (result === "ignored") return;
    this.replica.setConnection("reconnecting");
    this.publishLifecycle({ status: "reconnecting" });
  }

  private handleAsynchronousCommandDecision(decision: ReplicaCommitDecision): void {
    if (decision.kind === "command_reconciliation_required") {
      this.requestAutomatic({ kind: "command_reconciliation", cause: decision.cause });
    } else if (decision.kind === "command_reconciliation_blocked") {
      this.stopPollutedStream("command_reconciliation_blocked");
    }
  }

  private stopPollutedStream(
    disposition: Parameters<FrontierSnapshotCoordinator["stopRecovery"]>[0],
  ): void {
    this.fenceAndCloseStream();
    this.coordinator.stopRecovery(disposition);
  }

  private fenceAndCloseStream(): void {
    const stream = this.stream;
    this.streamToken = undefined;
    this.stream = undefined;
    stream?.close();
  }

  private isCurrentStream(stream: EventStream, token: symbol): boolean {
    return this.terminal === undefined
      && this.stream === stream
      && this.streamToken === token;
  }

  private trustedCurrentState(): TrustedViewProjectionState<DataOfKind<K>> | undefined {
    const state = this.replica.getState();
    if (state.data === undefined || !state.cursor || state.projectionVersion === undefined) return;
    return Object.freeze({
      data: state.data,
      availableActions: state.availableActions,
      relatedRefs: state.relatedRefs,
      viewEpoch: state.cursor.epoch,
      viewCursor: state.cursor.sequence,
      projectionVersion: state.projectionVersion,
    }) as unknown as TrustedViewProjectionState<DataOfKind<K>>;
  }

  private requestedClientCommandIds(): readonly string[] {
    return Object.freeze([...new Set(this.replica.getState().pendingCommands
      .map((command) => command.clientCommandId)
      .filter((value): value is string => Boolean(value)))].sort().slice(0, 64));
  }

  private publishLoadingState(): void {
    const status = this.replica.getState().data ? "reconnecting" : "connecting";
    this.replica.setConnection(status);
    this.publishLifecycle({ status });
  }

  private publishStopped(disposition: SnapshotStopDisposition): void {
    this.publishLifecycle({ status: "error", error: stoppedMessage(disposition) });
  }

  private publishClientUpgrade(): void {
    this.terminal = "client_upgrade_required";
    this.fenceAndCloseStream();
    this.publishLifecycle({ status: "client_upgrade_required" });
  }

  private publishLifecycle(state: FrontierViewLifecycleState): void {
    if (this.lifecycle?.status === state.status && this.lifecycle.error === state.error) return;
    this.lifecycle = state;
    this.onStateChange?.(state);
  }
}

function transportStage<K extends ProductionViewKind>(
  outcome: SnapshotTransportOutcome<K>,
): SnapshotFetchOutcome<SnapshotStage<DataOfKind<K>>> {
  switch (outcome.kind) {
    case "decoded":
      return { kind: "decoded", snapshot: { kind: "decoded_snapshot", snapshot: outcome.snapshot } };
    case "retryable_transport":
      return { kind: "retryable_transport" };
    case "access_clear":
      return { kind: "decoded", snapshot: { kind: "access_clear" } };
    case "terminal_http":
      return { kind: "decoded", snapshot: { kind: "terminal_http" } };
  }
}

function sseFrame(eventName: string, event: MessageEvent<string>): ProjectionSSEFrame {
  return { eventName, lastEventId: event.lastEventId, data: event.data };
}

function samePosition(
  trusted: TrustedProjectionPosition,
  effective: TrustedViewProjectionState<unknown>,
): boolean {
  return trusted.viewEpoch === effective.viewEpoch
    && trusted.viewCursor === effective.viewCursor
    && trusted.projectionVersion === effective.projectionVersion;
}

function decodeTransient(data: string): TransientDisplayDelta | undefined {
  try {
    const value: unknown = JSON.parse(data);
    if (!value || typeof value !== "object" || Array.isArray(value)) return;
    const record = value as Record<string, unknown>;
    if (
      record.schema_version !== "frontier.transient_display_delta.v1"
      || typeof record.display_stream_id !== "string"
      || typeof record.part_id !== "string"
      || !Number.isSafeInteger(record.base_checkpoint_version)
      || !Number.isSafeInteger(record.transient_sequence)
      || typeof record.delta_type !== "string"
      || !record.payload
      || typeof record.payload !== "object"
      || Array.isArray(record.payload)
      || typeof record.emitted_at !== "string"
    ) return;
    return value as TransientDisplayDelta;
  } catch {
    return;
  }
}

function stoppedMessage(disposition: SnapshotStopDisposition): string {
  switch (disposition) {
    case "access_clear": return "This View is no longer available.";
    case "retryable_transport": return "Frontier snapshot is temporarily unavailable.";
    case "terminal_http": return "Frontier snapshot request was rejected.";
    case "continuity_blocked": return "Frontier continuity could not be reconciled automatically.";
    case "integrity_blocked": return "Frontier projection integrity could not be reconciled automatically.";
    case "command_reconciliation_blocked": return "Command state could not be reconciled automatically.";
    case "stale_decode_context": return "Frontier snapshot context changed before commit.";
    case "automatic_conflict": return "Conflicting Frontier recovery requests require a manual retry.";
    case "fence_failed": return "Frontier stream could not be fenced safely.";
    case "attempt_failed":
    case "invalid_snapshot":
      return "Frontier view data could not be validated.";
  }
}
