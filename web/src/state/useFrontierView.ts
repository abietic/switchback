import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type BoundProductionView,
  type DataOfKind,
  type ProductionViewKind,
} from "../projections/productionViewRegistry";
import {
  ClientViewReplica,
  clientViewReplicas,
  type ClientViewReplicaLease,
  type ClientViewState,
} from "./clientViewReplica";
import {
  FrontierViewController,
  type FrontierViewLifecycleState,
  type FrontierViewStatus,
} from "./frontierViewController";
import { useClientViewReplica } from "./useClientViewReplica";

export interface FrontierViewResult<T> {
  readonly replica: ClientViewReplica<T>;
  readonly state: ClientViewState<T>;
  readonly status: FrontierViewStatus;
  readonly error?: string;
  readonly manualRetry: () => Promise<void>;
  readonly reconcileCommandStale: () => Promise<void>;
}

export function useFrontierView<K extends ProductionViewKind>(
  view: BoundProductionView<K>,
): FrontierViewResult<DataOfKind<K>> {
  const incomingView = view;
  const semanticKey = productionViewSemanticKey(incomingView);
  // Production factories intentionally return immutable values. A same-key HMR
  // replacement is the same runtime contract and must not churn the live owner.
  const stableView = useMemo(() => incomingView, [semanticKey]);
  const placeholder = useMemo(
    () => new ClientViewReplica<DataOfKind<K>>(stableView.viewType, stableView.viewId),
    [stableView],
  );
  const [binding, setBinding] = useState<{
    readonly token: symbol;
    readonly semanticKey: string;
    readonly lease: ClientViewReplicaLease<K>;
  }>();
  const ownerRef = useRef<{
    readonly token: symbol;
    readonly semanticKey: string;
    readonly controller: FrontierViewController<K>;
  } | undefined>(undefined);
  const activeBinding = binding
    && binding.semanticKey === semanticKey
    && ownerRef.current?.token === binding.token
    && ownerRef.current.semanticKey === semanticKey
    ? binding
    : undefined;
  const replica = activeBinding?.lease.replica ?? placeholder;
  const state = useClientViewReplica(replica);
  const initialLifecycle = (): FrontierViewLifecycleState => ({
    status: replica.getState().data ? "reconnecting" : "connecting",
  });
  const [lifecycle, setLifecycle] = useState<{
    token: symbol;
    state: FrontierViewLifecycleState;
  }>();
  const currentLifecycle = activeBinding && lifecycle?.token === activeBinding.token
    ? lifecycle.state
    : initialLifecycle();

  useEffect(() => {
    const token = Symbol("frontierViewHookOwner");
    const lease = clientViewReplicas.acquire(stableView);
    const controller = new FrontierViewController({
      view: stableView,
      replica: lease.replica,
      onStateChange(next) {
        if (ownerRef.current?.token === token) setLifecycle({ token, state: next });
      },
    });
    ownerRef.current = { token, semanticKey, controller };
    setBinding({ token, semanticKey, lease });
    setLifecycle({ token, state: {
      status: lease.replica.getState().data ? "reconnecting" : "connecting",
    } });
    controller.start();
    return () => {
      if (ownerRef.current?.token === token) ownerRef.current = undefined;
      controller.stop();
      lease.release();
      setBinding((current) => current?.token === token ? undefined : current);
    };
  }, [semanticKey, stableView]);

  const manualRetry = useCallback(async () => {
    const owner = ownerRef.current;
    if (owner?.semanticKey === semanticKey) owner.controller.manualRetry();
  }, [semanticKey]);
  const reconcileCommandStale = useCallback(async () => {
    const owner = ownerRef.current;
    if (owner?.semanticKey === semanticKey) owner.controller.reconcileCommandStale();
  }, [semanticKey]);

  return {
    replica,
    state,
    status: currentLifecycle.status,
    error: currentLifecycle.error,
    manualRetry,
    reconcileCommandStale,
  };
}

function productionViewSemanticKey<K extends ProductionViewKind>(
  view: BoundProductionView<K>,
): string {
  return [view.runtimeVersion, view.viewType, view.viewId, view.contractKey].join("\0");
}
