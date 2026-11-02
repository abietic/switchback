import type {
  ActionRef,
  RelatedRef,
  TransientDisplayDelta,
} from "../api/contracts";
import type {
  BoundProductionView,
  DataOfKind,
  ProductionViewKind,
} from "../projections/productionViewRegistry";
import type {
  DecodedProjectionControl,
  DecodedProjectionDelta,
  DecodedProjectionSnapshot,
  TrustedProjectionPosition,
  TrustedViewProjectionState,
} from "../projections/viewRuntime";

export type ReplicaSyncState = "empty" | "ready" | "resync_required" | "client_upgrade_required";
export type ReplicaConnectionState = "connecting" | "connected" | "reconnecting";
export type TransientApplyResult = "applied" | "discarded";

const commandReconciliationBrand: unique symbol = Symbol("commandReconciliationCause");
const replicaCommitBrand: unique symbol = Symbol("replicaCommitDecision");

export type CommandReconciliationReason =
  | "delta_command_identity_conflict"
  | "snapshot_receipt_identity_conflict"
  | "submission_response_identity_conflict";

export interface DecodedCommandReconciliationCause {
  readonly [commandReconciliationBrand]: true;
  readonly reason: CommandReconciliationReason;
  readonly key: string;
  readonly trustedPosition: TrustedProjectionPosition | undefined;
  readonly conflictingPosition?: {
    readonly viewEpoch: number;
    readonly viewCursor: number;
    readonly projectionVersion: number;
  };
  readonly pendingBindingFingerprint: string;
  readonly requestedClientCommandIds: readonly string[];
}

export type ReplicaCommitDecision =
  | { readonly [replicaCommitBrand]: true; readonly kind: "committed" }
  | {
      readonly [replicaCommitBrand]: true;
      readonly kind: "command_reconciliation_required";
      readonly cause: DecodedCommandReconciliationCause;
    }
  | { readonly [replicaCommitBrand]: true; readonly kind: "command_reconciliation_blocked" }
  | { readonly [replicaCommitBrand]: true; readonly kind: "stale_decode_context" };

export interface PendingCommand {
  clientCommandId?: string;
  commandId?: string;
  action: string;
  scopeKey?: string;
  submittedAt: string;
}

export interface CommandSubmission<Result extends { command_id: string }> {
  clientCommandId: string;
  action: string;
  scopeKey?: string;
  submit: () => Promise<Result>;
  isDefinitiveFailure: (cause: unknown) => boolean;
}

export interface ClientViewState<T> {
  connection: ReplicaConnectionState;
  sync: ReplicaSyncState;
  data?: T;
  cursor?: { epoch: number; sequence: number };
  projectionVersion?: number;
  availableActions: ActionRef[];
  relatedRefs: RelatedRef[];
  transient: TransientDisplayDelta[];
  pendingCommands: PendingCommand[];
}

interface TransientCursor {
  baseCheckpointVersion: number;
  lastSequence: number;
}

interface ActiveCommand {
  clientCommandId: string;
  action: string;
  scopeKey?: string;
  submittedAt: string;
  commandId?: string;
  observedCommandId?: string;
  transportActive: boolean;
  settled: boolean;
}

interface CommandReconciliationBasis {
  readonly cause: DecodedCommandReconciliationCause;
  readonly implicatedClientCommandIds: ReadonlySet<string>;
  readonly deltaPosition?: { readonly epoch: number; readonly cursor: number };
}

type Listener = () => void;

const activeCommandLimit = 64;

const committedDecision: ReplicaCommitDecision = Object.freeze({
  [replicaCommitBrand]: true as const,
  kind: "committed",
});
const reconciliationBlockedDecision: ReplicaCommitDecision = Object.freeze({
  [replicaCommitBrand]: true as const,
  kind: "command_reconciliation_blocked",
});
const staleDecodeContextDecision: ReplicaCommitDecision = Object.freeze({
  [replicaCommitBrand]: true as const,
  kind: "stale_decode_context",
});

export class ClientViewReplica<T> {
  readonly viewType: string;
  readonly viewId: string;

  private state: ClientViewState<T> = {
    connection: "connecting",
    sync: "empty",
    availableActions: [],
    relatedRefs: [],
    transient: [],
    pendingCommands: [],
  };
  private listeners = new Set<Listener>();
  private transientCursors = new Map<string, TransientCursor>();
  private activeCommands = new Map<string, ActiveCommand>();
  private observedServerCommands = new Map<string, { epoch: number; sequence: number }>();
  private commandReconciliation?: CommandReconciliationBasis;
  private commandReconciliationListeners = new Set<(decision: ReplicaCommitDecision) => void>();

  constructor(viewType: string, viewId: string) {
    this.viewType = viewType;
    this.viewId = viewId;
  }

  getState = (): ClientViewState<T> => this.state;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  subscribeCommandReconciliation = (
    listener: (decision: ReplicaCommitDecision) => void,
  ): (() => void) => {
    this.commandReconciliationListeners.add(listener);
    return () => this.commandReconciliationListeners.delete(listener);
  };

  commitDecodedSnapshot(decoded: DecodedProjectionSnapshot<T>): ReplicaCommitDecision {
    if (
      decoded.envelope.view_type !== this.viewType
      || decoded.envelope.view_id !== this.viewId
      || !sameIdentities(
        decoded.requestContext.requestedClientCommandIds,
        this.requestedClientCommandIds(),
      )
    ) {
      return staleDecodeContextDecision;
    }

    const receiptPlan = this.planSnapshotReceipts(decoded);
    if (receiptPlan.conflict) {
      return this.chargeCommandReconciliation({
        reason: "snapshot_receipt_identity_conflict",
        implicated: receiptPlan.implicated,
        requestedClientCommandIds: decoded.requestContext.requestedClientCommandIds,
      });
    }
    for (const [active, commandId] of receiptPlan.settlements) {
      active.observedCommandId = commandId;
      active.settled = true;
      if (!active.transportActive) this.activeCommands.delete(active.clientCommandId);
    }

    const priorEpoch = this.state.cursor?.epoch;
    if (priorEpoch !== undefined && priorEpoch !== decoded.effective.viewEpoch) {
      this.observedServerCommands.clear();
    }
    this.transientCursors.clear();
    this.publishEffectiveState(decoded.effective, []);
    this.maybeClearCommandReconciliation({
      epoch: decoded.effective.viewEpoch,
      cursor: decoded.effective.viewCursor,
    });
    return committedDecision;
  }

  commitDecodedDelta(decoded: DecodedProjectionDelta<T>): ReplicaCommitDecision {
    const cursor = this.state.cursor;
    if (
      decoded.envelope.view_type !== this.viewType
      || decoded.envelope.view_id !== this.viewId
      || this.state.sync !== "ready"
      || cursor === undefined
      || decoded.envelope.view_epoch !== cursor.epoch
      || decoded.envelope.view_cursor !== cursor.sequence + 1
      || decoded.envelope.base_projection_version !== this.state.projectionVersion
      || decoded.envelope.result_projection_version !== decoded.envelope.base_projection_version + 1
    ) {
      return staleDecodeContextDecision;
    }

    const commandPlan = this.planDeltaCommand(decoded);
    if (commandPlan.conflict) {
      return this.chargeCommandReconciliation({
        reason: "delta_command_identity_conflict",
        implicated: commandPlan.implicated,
        requestedClientCommandIds: this.requestedClientCommandIds(),
        conflictingPosition: {
          viewEpoch: decoded.envelope.view_epoch,
          viewCursor: decoded.envelope.view_cursor,
          projectionVersion: decoded.envelope.result_projection_version,
        },
        deltaPosition: {
          epoch: decoded.envelope.view_epoch,
          cursor: decoded.envelope.view_cursor,
        },
      });
    }
    this.applyDeltaCommandPlan(commandPlan, decoded);

    let transient = this.state.transient;
    const checkpoint = decoded.envelope.payload.display_checkpoint;
    if (checkpoint) {
      transient = transient.filter((item) => item.display_stream_id !== checkpoint.display_stream_id);
      this.transientCursors.set(checkpoint.display_stream_id, {
        baseCheckpointVersion: checkpoint.checkpoint_version,
        lastSequence: 0,
      });
    }
    this.publishEffectiveState(decoded.effective, transient);
    this.maybeClearCommandReconciliation({
      epoch: decoded.effective.viewEpoch,
      cursor: decoded.effective.viewCursor,
    });
    return committedDecision;
  }

  commitDecodedControl(decoded: DecodedProjectionControl): ReplicaCommitDecision {
    if (
      decoded.envelope.view_type !== this.viewType
      || decoded.envelope.view_id !== this.viewId
    ) {
      return staleDecodeContextDecision;
    }
    this.setSync(decoded.envelope.event_type);
    return committedDecision;
  }

  beginManualRetry(): void {
    this.commandReconciliation = undefined;
  }

  clearAccess(): void {
    this.transientCursors.clear();
    this.activeCommands.clear();
    this.observedServerCommands.clear();
    this.commandReconciliation = undefined;
    this.publish({
      connection: this.state.connection,
      sync: "empty",
      availableActions: [],
      relatedRefs: [],
      transient: [],
      pendingCommands: [],
    });
  }

  async submitCommand<Result extends { command_id: string }>(
    submission: CommandSubmission<Result>,
  ): Promise<Result> {
    let active = this.activeCommands.get(submission.clientCommandId);
    if (active) {
      if (active.action !== submission.action || active.scopeKey !== submission.scopeKey) {
        throw new Error("active client command identity has a different binding");
      }
      if (active.transportActive) {
        throw new Error("client command transport is already active");
      }
      active.transportActive = true;
    } else {
      if (this.activeCommands.size >= activeCommandLimit) {
        throw new Error("active client command admission limit reached");
      }
      active = {
        clientCommandId: submission.clientCommandId,
        action: submission.action,
        scopeKey: submission.scopeKey,
        submittedAt: new Date().toISOString(),
        transportActive: true,
        settled: false,
      };
      this.activeCommands.set(submission.clientCommandId, active);
      this.publishCommandState();
    }

    try {
      const result = await submission.submit();
      if (this.activeCommands.get(submission.clientCommandId) === active) {
        active.transportActive = false;
        const serverIdentity = this.serverIdentityOwner(result.command_id);
        if (
          serverIdentity.conflict
          || (serverIdentity.owner && serverIdentity.owner !== active)
          || (active.commandId && active.commandId !== result.command_id)
        ) {
          active.settled = false;
          this.signalCommandReconciliation(this.chargeCommandReconciliation({
            reason: "submission_response_identity_conflict",
            implicated: new Set([
              active.clientCommandId,
              ...this.serverIdentityOwners(result.command_id).map((owner) => owner.clientCommandId),
            ]),
            requestedClientCommandIds: this.requestedClientCommandIds(),
          }));
          return result;
        }
        active.commandId = result.command_id;
        if (active.observedCommandId && active.observedCommandId !== result.command_id) {
          active.settled = false;
          this.signalCommandReconciliation(this.chargeCommandReconciliation({
            reason: "submission_response_identity_conflict",
            implicated: new Set([active.clientCommandId]),
            requestedClientCommandIds: this.requestedClientCommandIds(),
          }));
          return result;
        }
        if (
          active.observedCommandId === result.command_id
          || this.observedServerCommands.has(result.command_id)
        ) {
          active.settled = true;
        }
        if (active.settled) {
          this.activeCommands.delete(submission.clientCommandId);
          this.maybeClearCommandReconciliation();
        }
        this.publishCommandState();
      }
      return result;
    } catch (cause) {
      if (this.activeCommands.get(submission.clientCommandId) === active) {
        active.transportActive = false;
        if (active.settled || submission.isDefinitiveFailure(cause)) {
          this.activeCommands.delete(submission.clientCommandId);
          this.maybeClearCommandReconciliation();
          this.publishCommandState();
        }
      }
      throw cause;
    }
  }

  applyTransient(delta: TransientDisplayDelta): TransientApplyResult {
    if (this.state.sync !== "ready") {
      return "discarded";
    }
    const cursor = this.transientCursors.get(delta.display_stream_id);
    if (
      cursor &&
      (cursor.baseCheckpointVersion !== delta.base_checkpoint_version ||
        delta.transient_sequence !== cursor.lastSequence + 1)
    ) {
      this.discardTransientStream(delta.display_stream_id);
      return "discarded";
    }
    if (!cursor && delta.transient_sequence !== 1) {
      this.discardTransientStream(delta.display_stream_id);
      return "discarded";
    }
    this.transientCursors.set(delta.display_stream_id, {
      baseCheckpointVersion: delta.base_checkpoint_version,
      lastSequence: delta.transient_sequence,
    });
    this.publish({ ...this.state, transient: [...this.state.transient, delta] });
    return "applied";
  }

  private planSnapshotReceipts(decoded: DecodedProjectionSnapshot<T>): {
    readonly conflict: boolean;
    readonly settlements: ReadonlyMap<ActiveCommand, string>;
    readonly implicated: ReadonlySet<string>;
  } {
    const settlements = new Map<ActiveCommand, string>();
    const receiptServerOwners = new Map<string, ActiveCommand>();
    const implicated = new Set<string>();
    for (const receipt of decoded.envelope.command_receipts ?? []) {
      const active = this.activeCommands.get(receipt.client_command_id);
      const serverOwners = this.serverIdentityOwners(receipt.command_id);
      if (active) implicated.add(active.clientCommandId);
      for (const owner of serverOwners) implicated.add(owner.clientCommandId);
      if (!active) {
        if (serverOwners.length !== 0) return { conflict: true, settlements, implicated };
        continue;
      }
      const receiptServerOwner = receiptServerOwners.get(receipt.command_id);
      const priorReceiptCommand = settlements.get(active);
      if (receiptServerOwner) implicated.add(receiptServerOwner.clientCommandId);
      if (
        (active.commandId && active.commandId !== receipt.command_id)
        || (active.observedCommandId && active.observedCommandId !== receipt.command_id)
        || serverOwners.some((owner) => owner !== active)
        || (receiptServerOwner && receiptServerOwner !== active)
        || (priorReceiptCommand && priorReceiptCommand !== receipt.command_id)
      ) {
        return { conflict: true, settlements, implicated };
      }
      receiptServerOwners.set(receipt.command_id, active);
      settlements.set(active, receipt.command_id);
    }
    return { conflict: false, settlements, implicated };
  }

  private planDeltaCommand(decoded: DecodedProjectionDelta<T>): {
    readonly conflict: boolean;
    readonly matched?: ActiveCommand;
    readonly serverCommandId?: string;
    readonly implicated: ReadonlySet<string>;
  } {
    const clientCommandId = decoded.envelope.client_command_id;
    const serverCommandId = decoded.envelope.command_id;
    const rawMatch = clientCommandId ? this.activeCommands.get(clientCommandId) : undefined;
    const serverOwners = serverCommandId ? this.serverIdentityOwners(serverCommandId) : [];
    const implicated = new Set<string>();
    if (rawMatch) implicated.add(rawMatch.clientCommandId);
    for (const owner of serverOwners) implicated.add(owner.clientCommandId);
    const serverOwner = serverOwners[0];
    const matched = rawMatch ?? serverOwner;
    const conflict = serverOwners.length > 1
      || Boolean(
        clientCommandId && serverOwner && serverOwner.clientCommandId !== clientCommandId,
      )
      || Boolean(rawMatch && serverOwner && rawMatch !== serverOwner)
      || Boolean(
        matched && serverCommandId
        && ((matched.commandId && matched.commandId !== serverCommandId)
          || (matched.observedCommandId && matched.observedCommandId !== serverCommandId)),
      );
    return { conflict, matched, serverCommandId, implicated };
  }

  private applyDeltaCommandPlan(
    plan: ReturnType<ClientViewReplica<T>["planDeltaCommand"]>,
    decoded: DecodedProjectionDelta<T>,
  ): void {
    const matched = plan.matched;
    const serverCommandId = plan.serverCommandId;
    if (matched) {
      if (serverCommandId) matched.observedCommandId = serverCommandId;
      if (serverCommandId && matched.commandId === serverCommandId) matched.settled = true;
      if (matched.settled && !matched.transportActive) {
        this.activeCommands.delete(matched.clientCommandId);
      }
    } else if (serverCommandId) {
      this.rememberServerCommand(
        serverCommandId,
        decoded.envelope.view_epoch,
        decoded.envelope.view_cursor,
      );
    }
  }

  private publishEffectiveState(
    effective: TrustedViewProjectionState<T>,
    transient: TransientDisplayDelta[],
  ): void {
    this.publish({
      connection: this.state.connection,
      sync: "ready",
      data: effective.data as T,
      cursor: { epoch: effective.viewEpoch, sequence: effective.viewCursor },
      projectionVersion: effective.projectionVersion,
      availableActions: effective.availableActions as unknown as ActionRef[],
      relatedRefs: effective.relatedRefs as unknown as RelatedRef[],
      transient,
      pendingCommands: this.pendingCommands(),
    });
  }

  private requestedClientCommandIds(): readonly string[] {
    return Object.freeze(
      [...this.activeCommands.values()]
        .filter((command) => !command.settled)
        .map((command) => command.clientCommandId)
        .sort(),
    );
  }

  private chargeCommandReconciliation(options: {
    readonly reason: CommandReconciliationReason;
    readonly implicated: ReadonlySet<string>;
    readonly requestedClientCommandIds: readonly string[];
    readonly conflictingPosition?: {
      readonly viewEpoch: number;
      readonly viewCursor: number;
      readonly projectionVersion: number;
    };
    readonly deltaPosition?: { readonly epoch: number; readonly cursor: number };
  }): ReplicaCommitDecision {
    if (this.commandReconciliation) return reconciliationBlockedDecision;
    const implicated = new Set(options.implicated);
    const fingerprint = bindingFingerprint(
      [...implicated]
        .map((id) => this.activeCommands.get(id))
        .filter((command): command is ActiveCommand => command !== undefined),
    );
    const trustedPosition = this.state.cursor && this.state.projectionVersion !== undefined
      ? Object.freeze({
          viewEpoch: this.state.cursor.epoch,
          viewCursor: this.state.cursor.sequence,
          projectionVersion: this.state.projectionVersion,
        })
      : undefined;
    const requestedClientCommandIds = Object.freeze([...options.requestedClientCommandIds].sort());
    const key = `command-reconciliation-v1:${safeHash([
      options.reason,
      fingerprint,
      trustedPosition?.viewEpoch ?? "none",
      trustedPosition?.viewCursor ?? "none",
      options.conflictingPosition?.viewEpoch ?? "none",
      options.conflictingPosition?.viewCursor ?? "none",
    ].join("\0"))}`;
    const cause: DecodedCommandReconciliationCause = Object.freeze({
      [commandReconciliationBrand]: true as const,
      reason: options.reason,
      key,
      trustedPosition,
      ...(options.conflictingPosition === undefined
        ? {}
        : { conflictingPosition: Object.freeze({ ...options.conflictingPosition }) }),
      pendingBindingFingerprint: fingerprint,
      requestedClientCommandIds,
    });
    this.commandReconciliation = {
      cause,
      implicatedClientCommandIds: implicated,
      ...(options.deltaPosition === undefined ? {} : { deltaPosition: options.deltaPosition }),
    };
    return Object.freeze({
      [replicaCommitBrand]: true as const,
      kind: "command_reconciliation_required",
      cause,
    });
  }

  private signalCommandReconciliation(decision: ReplicaCommitDecision): void {
    this.commandReconciliationListeners.forEach((listener) => listener(decision));
  }

  private maybeClearCommandReconciliation(position?: { epoch: number; cursor: number }): void {
    const basis = this.commandReconciliation;
    if (!basis) return;
    const implicatedSettled = [...basis.implicatedClientCommandIds]
      .every((id) => !this.activeCommands.has(id));
    const deltaCovered = basis.deltaPosition !== undefined && position !== undefined
      && (position.epoch !== basis.deltaPosition.epoch || position.cursor >= basis.deltaPosition.cursor);
    if (implicatedSettled || deltaCovered) this.commandReconciliation = undefined;
  }

  private serverIdentityOwners(commandId: string): ActiveCommand[] {
    return [...this.activeCommands.values()].filter((command) => (
      command.commandId === commandId || command.observedCommandId === commandId
    ));
  }

  private pendingCommands(): PendingCommand[] {
    return [...this.activeCommands.values()]
      .filter((command) => !command.settled)
      .map((command) => ({
        clientCommandId: command.clientCommandId,
        commandId: command.commandId,
        action: command.action,
        scopeKey: command.scopeKey,
        submittedAt: command.submittedAt,
      }));
  }

  private publishCommandState(): void {
    this.publish({ ...this.state, pendingCommands: this.pendingCommands() });
  }

  private rememberServerCommand(commandId: string, epoch: number, sequence: number): void {
    this.observedServerCommands.delete(commandId);
    this.observedServerCommands.set(commandId, { epoch, sequence });
    while (this.observedServerCommands.size > activeCommandLimit) {
      const oldest = this.observedServerCommands.keys().next().value as string | undefined;
      if (!oldest) break;
      this.observedServerCommands.delete(oldest);
    }
  }

  private serverIdentityOwner(commandId: string): {
    owner?: ActiveCommand;
    conflict: boolean;
  } {
    let owner: ActiveCommand | undefined;
    for (const command of this.activeCommands.values()) {
      if (command.commandId !== commandId && command.observedCommandId !== commandId) continue;
      if (owner && owner !== command) return { conflict: true };
      owner = command;
    }
    return { owner, conflict: false };
  }

  setConnection(connection: ReplicaConnectionState): void {
    if (this.state.connection === connection) return;
    this.publish({ ...this.state, connection });
  }

  private setSync(sync: ReplicaSyncState): void {
    this.transientCursors.clear();
    this.publish({ ...this.state, sync, transient: [] });
  }

  private discardTransientStream(displayStreamId: string): void {
    this.transientCursors.delete(displayStreamId);
    this.publish({
      ...this.state,
      transient: this.state.transient.filter((item) => item.display_stream_id !== displayStreamId),
    });
  }

  private publish(state: ClientViewState<T>): void {
    this.state = state;
    this.listeners.forEach((listener) => listener());
  }
}

function sameIdentities(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false;
  const normalizedLeft = [...left].sort();
  const normalizedRight = [...right].sort();
  return normalizedLeft.every((value, index) => value === normalizedRight[index]);
}

function bindingFingerprint(commands: readonly ActiveCommand[]): string {
  const basis = commands
    .map((command) => [
      command.clientCommandId,
      command.commandId ?? "",
      command.observedCommandId ?? "",
      command.action,
      command.scopeKey ?? "",
    ].join("\0"))
    .sort()
    .join("\u0001");
  return `binding-v1:${safeHash(basis)}`;
}

function safeHash(value: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, "0");
}

export class ClientViewReplicaRegistry {
  private replicas = new Map<string, ReplicaRegistryEntry>();

  acquire<K extends ProductionViewKind>(
    view: BoundProductionView<K>,
  ): ClientViewReplicaLease<K> {
    const identityKey = `${view.viewType}\u0000${view.viewId}`;
    let entry = this.replicas.get(identityKey);
    if (entry?.contractKey !== view.contractKey) {
      if (entry && entry.activeLeases.size !== 0) {
        throw new Error("concurrent production View contract mismatch");
      }
      entry?.replica.clearAccess();
      entry = {
        contractKey: view.contractKey,
        replica: new ClientViewReplica<unknown>(view.viewType, view.viewId),
        activeLeases: new Set(),
      };
      this.replicas.set(identityKey, entry);
    }

    const leaseToken = Symbol("productionViewReplicaLease");
    entry.activeLeases.add(leaseToken);
    const leasedEntry = entry;
    let released = false;
    return Object.freeze({
      contractKey: view.contractKey,
      replica: entry.replica as ClientViewReplica<DataOfKind<K>>,
      release: () => {
        if (released) return;
        released = true;
        const current = this.replicas.get(identityKey);
        if (current === leasedEntry) current.activeLeases.delete(leaseToken);
      },
    });
  }

  delete(viewType: string, viewId: string): void {
    this.replicas.delete(`${viewType}\u0000${viewId}`);
  }
}

interface ReplicaRegistryEntry {
  readonly replica: ClientViewReplica<unknown>;
  readonly activeLeases: Set<symbol>;
  readonly contractKey?: string;
}

export interface ClientViewReplicaLease<K extends ProductionViewKind> {
  readonly contractKey: string;
  readonly replica: ClientViewReplica<DataOfKind<K>>;
  readonly release: () => void;
}

export const clientViewReplicas = new ClientViewReplicaRegistry();
