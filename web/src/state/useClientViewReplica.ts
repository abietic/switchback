import { useSyncExternalStore } from "react";
import type { ClientViewReplica, ClientViewState } from "./clientViewReplica";

export function useClientViewReplica<T>(replica: ClientViewReplica<T>): ClientViewState<T> {
  return useSyncExternalStore(replica.subscribe, replica.getState, replica.getState);
}
