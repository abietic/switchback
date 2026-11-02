import type {
  DecodedContinuityDiscontinuity,
  DecodedIntegrityFailure,
} from "../projections/viewRuntime";
import type { DecodedCommandReconciliationCause } from "./clientViewReplica";

export type SnapshotRecoveryCause =
  | { readonly kind: "initial" }
  | { readonly kind: "manual_retry" }
  | { readonly kind: "sse_transport" }
  | { readonly kind: "command_stale" }
  | {
      readonly kind: "continuity_discontinuity";
      readonly discontinuity: DecodedContinuityDiscontinuity;
    }
  | { readonly kind: "integrity_failure"; readonly failure: DecodedIntegrityFailure }
  | {
      readonly kind: "command_reconciliation";
      readonly cause: DecodedCommandReconciliationCause;
    };

export type AutomaticSnapshotRecoveryCause = Exclude<
  SnapshotRecoveryCause,
  { readonly kind: "initial" } | { readonly kind: "manual_retry" }
>;

export type SnapshotAttemptStopDisposition =
  | "access_clear"
  | "terminal_http"
  | "invalid_snapshot"
  | "stale_decode_context"
  | "continuity_blocked"
  | "integrity_blocked"
  | "command_reconciliation_blocked";

export type SnapshotStopDisposition =
  | SnapshotAttemptStopDisposition
  | "retryable_transport"
  | "automatic_conflict"
  | "fence_failed"
  | "attempt_failed";

export type SnapshotFetchOutcome<Snapshot> =
  | { readonly kind: "decoded"; readonly snapshot: Snapshot }
  | { readonly kind: "retryable_transport" }
  | {
      readonly kind: "stopped_manual_revivable";
      readonly disposition: SnapshotAttemptStopDisposition;
    }
  | { readonly kind: "client_upgrade_required" };

export type SnapshotCommitOutcome =
  | { readonly kind: "committed" }
  | { readonly kind: "recovery_queued" }
  | {
      readonly kind: "stopped_manual_revivable";
      readonly disposition: SnapshotAttemptStopDisposition;
    }
  | { readonly kind: "client_upgrade_required" };

export interface SnapshotAttemptContext {
  readonly generation: number;
  readonly cause: SnapshotRecoveryCause;
  readonly requestedClientCommandIds: readonly string[];
}

export interface SnapshotCoordinatorEffects<Snapshot = unknown> {
  readonly fenceAndCloseStream: () => void;
  readonly captureRequestedClientCommandIds: () => readonly string[];
  readonly fetchSnapshot: (
    context: SnapshotAttemptContext,
  ) => Promise<SnapshotFetchOutcome<Snapshot>>;
  readonly commitSnapshot: (
    snapshot: Snapshot,
    context: SnapshotAttemptContext,
  ) => SnapshotCommitOutcome;
  readonly beginManualRetry: () => void;
  readonly connectWhenIdle: () => void;
  readonly onStopped: (disposition: SnapshotStopDisposition) => void;
  readonly onClientUpgradeRequired: () => void;
}

export interface FrontierSnapshotCoordinatorOptions {
  readonly retryDelayMs?: number;
  readonly maxRetryDelayMs?: number;
}

export type AutomaticSnapshotRequestResult = "accepted" | "ignored";

type Lifecycle =
  | "idle"
  | "active"
  | "stopped_manual_revivable"
  | "client_upgrade_required"
  | "disposed";

type AttemptCompletion =
  | { readonly kind: "continue" }
  | { readonly kind: "stopped_manual_revivable"; readonly disposition: SnapshotStopDisposition }
  | { readonly kind: "client_upgrade_required" }
  | { readonly kind: "disposed" };

interface PendingAttempt {
  readonly cause: SnapshotRecoveryCause;
  readonly requestedClientCommandIds?: readonly string[];
}

const initialCause: SnapshotRecoveryCause = Object.freeze({ kind: "initial" });
const manualCause: SnapshotRecoveryCause = Object.freeze({ kind: "manual_retry" });

export class FrontierSnapshotCoordinator<Snapshot = unknown> {
  private readonly effects: SnapshotCoordinatorEffects<Snapshot>;
  private readonly retryDelayMs: number;
  private readonly maxRetryDelayMs: number;

  private lifecycle: Lifecycle = "idle";
  private generation = 0;
  private inFlight = false;
  private completion: AttemptCompletion = { kind: "continue" };
  private queuedManual = false;
  private queuedAutomatic?: PendingAttempt;
  private automaticConflict = false;
  private retryIntent?: PendingAttempt;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private timerAttempt?: PendingAttempt;
  private retryCauseKey?: string;
  private retryOrdinal = 0;
  private shouldConnectWhenIdle = false;

  constructor(
    effects: SnapshotCoordinatorEffects<Snapshot>,
    options: FrontierSnapshotCoordinatorOptions = {},
  ) {
    this.effects = effects;
    this.retryDelayMs = Math.max(0, options.retryDelayMs ?? 1000);
    this.maxRetryDelayMs = Math.max(
      this.retryDelayMs,
      options.maxRetryDelayMs ?? 30_000,
    );
  }

  start(): void {
    if (this.lifecycle !== "idle") return;
    this.lifecycle = "active";
    this.launch({ cause: initialCause });
  }

  manualRetry(): void {
    if (
      this.lifecycle === "disposed"
      || this.lifecycle === "client_upgrade_required"
      || this.lifecycle === "idle"
    ) return;
    if (this.inFlight) {
      this.queuedManual = true;
      return;
    }

    this.cancelRetryTimer();
    this.queuedAutomatic = undefined;
    this.automaticConflict = false;
    this.retryIntent = undefined;
    this.shouldConnectWhenIdle = false;
    this.lifecycle = "active";
    this.resetRetryBackoff();
    this.launch({ cause: manualCause });
  }

  requestAutomatic(cause: AutomaticSnapshotRecoveryCause): AutomaticSnapshotRequestResult {
    if (this.lifecycle !== "active") return "ignored";
    const pending = pendingAttempt(cause);
    if (this.inFlight) {
      if (!this.queuedAutomatic) {
        this.queuedAutomatic = pending;
      } else if (causeKey(this.queuedAutomatic.cause) !== causeKey(pending.cause)) {
        this.automaticConflict = true;
      }
      return "accepted";
    }

    if (this.retryTimer !== undefined && this.timerAttempt) {
      if (causeKey(this.timerAttempt.cause) === causeKey(pending.cause)) return "accepted";
      this.cancelRetryTimer();
      this.resetRetryBackoff();
    }
    this.launch(pending);
    return "accepted";
  }

  requestAutomaticAfterDelay(cause: AutomaticSnapshotRecoveryCause): void {
    if (this.lifecycle !== "active") return;
    if (this.inFlight) {
      this.requestAutomatic(cause);
      return;
    }
    const pending = pendingAttempt(cause);
    if (this.retryTimer !== undefined && this.timerAttempt) {
      if (causeKey(this.timerAttempt.cause) === causeKey(pending.cause)) return;
      this.cancelRetryTimer();
      this.resetRetryBackoff();
    }
    this.armRetryTimer(pending);
  }

  stopRecovery(disposition: SnapshotAttemptStopDisposition): void {
    if (this.lifecycle !== "active") return;
    this.stopAutomaticRecovery(disposition);
  }

  requireClientUpgrade(): void {
    if (this.lifecycle !== "active") return;
    this.dropAutomaticSuccessors();
    if (this.inFlight) {
      this.completion = { kind: "client_upgrade_required" };
      return;
    }
    this.lifecycle = "client_upgrade_required";
    this.queuedManual = false;
    this.effects.onClientUpgradeRequired();
  }

  stop(): void {
    if (this.lifecycle === "disposed") return;
    this.lifecycle = "disposed";
    this.generation += 1;
    this.completion = { kind: "disposed" };
    this.queuedManual = false;
    this.dropAutomaticSuccessors();
    try {
      this.effects.fenceAndCloseStream();
    } catch {
      // Disposal is terminal even when an injected fence violates its non-throwing contract.
    }
  }

  private launch(pending: PendingAttempt): void {
    if (this.lifecycle !== "active" || this.inFlight) return;
    this.inFlight = true;
    this.completion = { kind: "continue" };
    this.retryIntent = undefined;
    this.shouldConnectWhenIdle = false;
    void this.execute(pending);
  }

  private async execute(pending: PendingAttempt): Promise<void> {
    let context: SnapshotAttemptContext | undefined;
    try {
      try {
        this.effects.fenceAndCloseStream();
      } catch {
        this.completion = {
          kind: "stopped_manual_revivable",
          disposition: "fence_failed",
        };
        return;
      }
      if (pending.cause.kind === "manual_retry") this.effects.beginManualRetry();

      const cause = freezeCause(pending.cause);
      const requestedClientCommandIds = pending.requestedClientCommandIds
        ?? Object.freeze([...this.effects.captureRequestedClientCommandIds()]);
      const generation = this.generation + 1;
      this.generation = generation;
      context = Object.freeze({ generation, cause, requestedClientCommandIds });
      const outcome = await this.effects.fetchSnapshot(context);
      if (!this.isAuthoritative(generation)) return;
      if (outcome.kind === "decoded") {
        const commitOutcome = this.effects.commitSnapshot(outcome.snapshot, context);
        if (!this.isAuthoritative(generation)) return;
        this.applyCommitOutcome(commitOutcome);
      } else {
        this.applyFetchOutcome(outcome, context);
      }
    } catch {
      if (this.completion.kind === "continue") {
        this.completion = {
          kind: "stopped_manual_revivable",
          disposition: "attempt_failed",
        };
      }
    } finally {
      this.inFlight = false;
      this.arbitrate();
    }
  }

  private applyFetchOutcome(
    outcome: Exclude<SnapshotFetchOutcome<Snapshot>, { readonly kind: "decoded" }>,
    context: SnapshotAttemptContext,
  ): void {
    switch (outcome.kind) {
      case "retryable_transport":
        if (isAutomaticCause(context.cause)) {
          this.retryIntent = {
            cause: context.cause,
            requestedClientCommandIds: context.requestedClientCommandIds,
          };
        } else {
          this.completion = {
            kind: "stopped_manual_revivable",
            disposition: "retryable_transport",
          };
        }
        return;
      case "stopped_manual_revivable":
        this.completion = outcome;
        return;
      case "client_upgrade_required":
        this.completion = { kind: "client_upgrade_required" };
    }
  }

  private applyCommitOutcome(outcome: SnapshotCommitOutcome): void {
    switch (outcome.kind) {
      case "committed":
        this.shouldConnectWhenIdle = true;
        this.resetRetryBackoff();
        return;
      case "recovery_queued":
        return;
      case "stopped_manual_revivable":
        this.completion = outcome;
        return;
      case "client_upgrade_required":
        this.completion = { kind: "client_upgrade_required" };
    }
  }

  private arbitrate(): void {
    if (this.lifecycle === "disposed" || this.completion.kind === "disposed") {
      this.dropAllSuccessors();
      return;
    }
    if (this.completion.kind === "client_upgrade_required") {
      this.lifecycle = "client_upgrade_required";
      this.dropAllSuccessors();
      this.effects.onClientUpgradeRequired();
      return;
    }
    if (this.queuedManual) {
      this.queuedManual = false;
      this.dropAutomaticSuccessors();
      this.lifecycle = "active";
      this.resetRetryBackoff();
      this.launch({ cause: manualCause });
      return;
    }
    if (this.completion.kind === "stopped_manual_revivable") {
      const disposition = this.completion.disposition;
      this.lifecycle = "stopped_manual_revivable";
      this.dropAutomaticSuccessors();
      this.effects.onStopped(disposition);
      return;
    }
    if (this.automaticConflict) {
      this.lifecycle = "stopped_manual_revivable";
      this.dropAutomaticSuccessors();
      this.effects.onStopped("automatic_conflict");
      return;
    }
    if (this.queuedAutomatic) {
      const successor = this.queuedAutomatic;
      this.queuedAutomatic = undefined;
      this.retryIntent = undefined;
      this.shouldConnectWhenIdle = false;
      this.launch(successor);
      return;
    }
    if (this.retryIntent) {
      const retry = this.retryIntent;
      this.retryIntent = undefined;
      this.shouldConnectWhenIdle = false;
      this.armRetryTimer(retry);
      return;
    }
    if (this.shouldConnectWhenIdle) {
      this.shouldConnectWhenIdle = false;
      try {
        this.effects.connectWhenIdle();
      } catch {
        this.stopAutomaticRecovery("attempt_failed");
      }
    }
  }

  private armRetryTimer(pending: PendingAttempt): void {
    this.cancelRetryTimer();
    const key = causeKey(pending.cause);
    if (this.retryCauseKey === key) this.retryOrdinal += 1;
    else {
      this.retryCauseKey = key;
      this.retryOrdinal = 0;
    }
    const delay = Math.min(
      this.maxRetryDelayMs,
      this.retryDelayMs * (2 ** Math.min(this.retryOrdinal, 20)),
    );
    this.timerAttempt = pending;
    this.retryTimer = setTimeout(() => {
      const attempt = this.timerAttempt;
      this.retryTimer = undefined;
      this.timerAttempt = undefined;
      if (!attempt || this.lifecycle !== "active" || this.inFlight) return;
      this.launch(attempt);
    }, delay);
  }

  private isAuthoritative(generation: number): boolean {
    return this.lifecycle !== "disposed"
      && this.generation === generation
      && this.completion.kind === "continue";
  }

  private stopAutomaticRecovery(disposition: SnapshotStopDisposition): void {
    this.shouldConnectWhenIdle = false;
    this.dropAutomaticSuccessors();
    if (this.inFlight) {
      this.completion = { kind: "stopped_manual_revivable", disposition };
      return;
    }
    this.lifecycle = "stopped_manual_revivable";
    this.effects.onStopped(disposition);
  }

  private dropAutomaticSuccessors(): void {
    this.cancelRetryTimer();
    this.queuedAutomatic = undefined;
    this.automaticConflict = false;
    this.retryIntent = undefined;
    this.shouldConnectWhenIdle = false;
  }

  private dropAllSuccessors(): void {
    this.queuedManual = false;
    this.dropAutomaticSuccessors();
  }

  private cancelRetryTimer(): void {
    if (this.retryTimer !== undefined) clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    this.timerAttempt = undefined;
  }

  private resetRetryBackoff(): void {
    this.retryCauseKey = undefined;
    this.retryOrdinal = 0;
  }
}

function pendingAttempt(cause: AutomaticSnapshotRecoveryCause): PendingAttempt {
  const frozenCause = freezeCause(cause);
  if (frozenCause.kind === "command_reconciliation") {
    return {
      cause: frozenCause,
      requestedClientCommandIds: frozenCause.cause.requestedClientCommandIds,
    };
  }
  return { cause: frozenCause };
}

function freezeCause(cause: SnapshotRecoveryCause): SnapshotRecoveryCause {
  if (Object.isFrozen(cause)) return cause;
  return Object.freeze({ ...cause }) as SnapshotRecoveryCause;
}

function isAutomaticCause(cause: SnapshotRecoveryCause): cause is AutomaticSnapshotRecoveryCause {
  return cause.kind !== "initial" && cause.kind !== "manual_retry";
}

function causeKey(cause: SnapshotRecoveryCause): string {
  switch (cause.kind) {
    case "continuity_discontinuity":
      return `${cause.kind}:${cause.discontinuity.key}`;
    case "integrity_failure":
      return `${cause.kind}:${cause.failure.key}`;
    case "command_reconciliation":
      return `${cause.kind}:${cause.cause.key}`;
    default:
      return cause.kind;
  }
}
