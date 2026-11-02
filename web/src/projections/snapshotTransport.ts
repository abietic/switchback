import type {
  BoundProductionView,
  DataOfKind,
  ProductionViewKind,
} from "./productionViewRegistry";
import type { DecodedProjectionSnapshot } from "./viewRuntime";

export type SnapshotTransportFailure =
  | { readonly kind: "access_clear"; readonly status: 401 | 403 | 404 }
  | { readonly kind: "retryable_transport"; readonly status?: number }
  | { readonly kind: "terminal_http"; readonly status: number };

export type SnapshotTransportOutcome<K extends ProductionViewKind> =
  | {
      readonly kind: "decoded";
      readonly snapshot: DecodedProjectionSnapshot<DataOfKind<K>>;
    }
  | SnapshotTransportFailure;

export interface SnapshotTransportOptions {
  readonly fetcher?: typeof fetch;
  readonly signal?: AbortSignal;
}

export async function fetchBoundProjectionSnapshot<K extends ProductionViewKind>(
  view: BoundProductionView<K>,
  requestedClientCommandIds: readonly string[],
  options: SnapshotTransportOptions = {},
): Promise<SnapshotTransportOutcome<K>> {
  const requestIds = Object.freeze([...new Set(requestedClientCommandIds)].sort());
  const fetcher = options.fetcher ?? ((input, init) => fetch(input, init));
  let response: Response;
  try {
    response = await fetcher(snapshotURL(view, requestIds), {
      headers: { Accept: "application/json" },
      signal: options.signal,
    });
  } catch {
    return Object.freeze({ kind: "retryable_transport" });
  }

  if (response.status >= 200 && response.status <= 299) {
    const value: unknown = await response.json();
    return Object.freeze({
      kind: "decoded",
      snapshot: view.decoder.decodeSnapshot(value, {
        requestedClientCommandIds: requestIds,
      }),
    });
  }
  return classifyHTTPStatus(response.status);
}

function snapshotURL<K extends ProductionViewKind>(
  view: BoundProductionView<K>,
  requestedClientCommandIds: readonly string[],
): string {
  const base = `/api/v1/views/${encodeURIComponent(view.viewType)}/${encodeURIComponent(view.viewId)}`;
  if (requestedClientCommandIds.length === 0) return base;
  const query = new URLSearchParams();
  for (const id of requestedClientCommandIds) {
    query.append("reconcile_client_command_id", id);
  }
  return `${base}?${query.toString()}`;
}

function classifyHTTPStatus(status: number): SnapshotTransportFailure {
  if (status === 401 || status === 403 || status === 404) {
    return Object.freeze({ kind: "access_clear", status });
  }
  if (status === 408 || status === 425 || status === 429 || (status >= 500 && status <= 599)) {
    return Object.freeze({ kind: "retryable_transport", status });
  }
  return Object.freeze({ kind: "terminal_http", status });
}
