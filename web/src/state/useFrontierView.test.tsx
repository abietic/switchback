import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode, useLayoutEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  isViewMutationEnabled,
  viewBoundaryConnection,
} from "../components/ui/ViewBoundary";
import {
  WORKER_SESSION_ID,
  workerData,
  workerSnapshot,
} from "../projections/testdata/workerFixtures";
import { productionViews } from "../projections/productionViewRegistry";
import { clientViewReplicas } from "./clientViewReplica";
import { FrontierViewController } from "./frontierViewController";
import { useFrontierView } from "./useFrontierView";

class RemountStream {
  static instances: RemountStream[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, (event: MessageEvent<string>) => void>();

  constructor(readonly url: string) {
    RemountStream.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    this.listeners.set(type, listener);
  }

  close(): void {}
}

function FrontierProbe({ viewId }: { viewId: string }) {
  const { error, status } = useFrontierView(productionViews.worker(viewId));
  return <>
    <output aria-label="frontier status">{status}</output>
    <output aria-label="frontier error">{error ?? "no error"}</output>
  </>;
}

function RemountAuthorityProbe() {
  const { state, status } = useFrontierView(productionViews.worker(WORKER_SESSION_ID));
  const enabled = isViewMutationEnabled({
    connection: state.connection,
    status,
    sync: state.sync,
  });
  return <>
    <output aria-label="remount lifecycle">{status}</output>
    <output aria-label="remount presentation">{viewBoundaryConnection({
      connection: state.connection,
      hasData: Boolean(state.data),
      status,
    })}</output>
    <button disabled={!enabled} type="button">Projected mutation</button>
  </>;
}

function RetryProbe() {
  const { manualRetry, reconcileCommandStale } = useFrontierView(
    productionViews.worker(WORKER_SESSION_ID),
  );
  return <>
    <button onClick={() => void manualRetry()}>Retry</button>
    <button onClick={reconcileCommandStale}>Stale</button>
  </>;
}

function BoundWorkerProbe({ viewId }: { viewId: string }) {
  const { replica, status } = useFrontierView(productionViews.worker(viewId));
  return <>
    <output aria-label="bound identity">{replica.viewId}</output>
    <output aria-label="bound status">{status}</output>
  </>;
}

interface IdentitySwitchObservation {
  readonly selectedViewId: string;
  readonly replicaViewId: string;
  readonly dataViewId?: string;
  readonly availableActionCount: number;
}

function IdentitySwitchProbe({
  viewId,
  onRender,
}: {
  viewId: string;
  onRender: (observation: IdentitySwitchObservation) => void;
}) {
  const {
    replica,
    state,
    manualRetry,
    reconcileCommandStale,
  } = useFrontierView(productionViews.worker(viewId));
  onRender({
    selectedViewId: viewId,
    replicaViewId: replica.viewId,
    dataViewId: state.data?.worker_session_id,
    availableActionCount: state.availableActions.length,
  });
  useLayoutEffect(() => {
    if (viewId !== "session-current") return;
    void manualRetry();
    void reconcileCommandStale();
  }, [manualRetry, reconcileCommandStale, viewId]);
  return <output aria-label="switch data">{state.data?.worker_session_id ?? "empty"}</output>;
}

afterEach(() => {
  for (const id of [WORKER_SESSION_ID, "session-old", "session-current"]) {
    clientViewReplicas.delete("worker_detail", id);
  }
  RemountStream.instances.splice(0);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("useFrontierView bound production lease", () => {
  it("reuses one lease and one controller across same-contract factory values", async () => {
    const acquire = vi.spyOn(clientViewReplicas, "acquire");
    const start = vi.spyOn(FrontierViewController.prototype, "start");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation(
      async () => jsonResponse(workerSnapshot()),
    ));
    vi.stubGlobal("EventSource", RemountStream);

    const rendered = render(<BoundWorkerProbe viewId={WORKER_SESSION_ID} />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(1));
    rendered.rerender(<BoundWorkerProbe viewId={WORKER_SESSION_ID} />);

    expect(acquire).toHaveBeenCalledTimes(1);
    expect(start).toHaveBeenCalledTimes(1);
    expect(RemountStream.instances).toHaveLength(1);
    expect(screen.getByLabelText("bound identity")).toHaveTextContent(WORKER_SESSION_ID);
  });

  it("stops the exact controller before releasing its final lease", async () => {
    const events: string[] = [];
    const originalAcquire = clientViewReplicas.acquire.bind(clientViewReplicas);
    vi.spyOn(clientViewReplicas, "acquire").mockImplementation((view) => {
      const lease = originalAcquire(view);
      return {
        ...lease,
        release() {
          events.push("release");
          lease.release();
        },
      } as never;
    });
    vi.spyOn(FrontierViewController.prototype, "stop").mockImplementation(function (this: FrontierViewController<"worker">) {
      events.push("stop");
      return undefined;
    });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation(
      async () => jsonResponse(workerSnapshot()),
    ));
    vi.stubGlobal("EventSource", RemountStream);

    const rendered = render(<BoundWorkerProbe viewId={WORKER_SESSION_ID} />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(1));
    rendered.unmount();

    expect(events).toEqual(["stop", "release"]);
  });

  it("replaces the effect safely when the bound View identity changes", async () => {
    const stopped: string[] = [];
    const originalStop = FrontierViewController.prototype.stop;
    vi.spyOn(FrontierViewController.prototype, "stop").mockImplementation(function (
      this: FrontierViewController<"worker">,
    ) {
      stopped.push((this as unknown as { view: { viewId: string } }).view.viewId);
      return originalStop.call(this);
    });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const viewId = decodeURIComponent(String(input).split("/").at(-1)?.split("?")[0] ?? "");
      return jsonResponse(workerSnapshot({
        view_id: viewId,
        data: workerData({ worker_session_id: viewId }),
      }));
    }));
    vi.stubGlobal("EventSource", RemountStream);

    const rendered = render(<BoundWorkerProbe viewId="session-old" />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(1));
    rendered.rerender(<BoundWorkerProbe viewId="session-current" />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(2));

    expect(stopped).toContain("session-old");
    expect(screen.getByLabelText("bound identity")).toHaveTextContent("session-current");
  });

  it("fails closed on the first render of a new bound View identity", async () => {
    const oldView = productionViews.worker("session-old");
    const seed = clientViewReplicas.acquire(oldView);
    seed.replica.commitDecodedSnapshot(oldView.decoder.decodeSnapshot(
      snapshotFor("session-old"),
      { requestedClientCommandIds: [] },
    ));
    seed.replica.setConnection("connected");
    seed.release();
    const requests: Array<ReturnType<typeof deferred<Response>>> = [];
    vi.stubGlobal("fetch", vi.fn(() => {
      const request = deferred<Response>();
      requests.push(request);
      return request.promise;
    }));
    const manualRetryOwners: string[] = [];
    const staleOwners: string[] = [];
    vi.spyOn(FrontierViewController.prototype, "manualRetry").mockImplementation(function (
      this: FrontierViewController<"worker">,
    ) {
      manualRetryOwners.push((this as unknown as { view: { viewId: string } }).view.viewId);
    });
    vi.spyOn(FrontierViewController.prototype, "reconcileCommandStale").mockImplementation(function (
      this: FrontierViewController<"worker">,
    ) {
      staleOwners.push((this as unknown as { view: { viewId: string } }).view.viewId);
    });
    const observations: IdentitySwitchObservation[] = [];
    const rendered = render(<IdentitySwitchProbe
      viewId="session-old"
      onRender={(observation) => observations.push(observation)}
    />);
    await waitFor(() => expect(screen.getByLabelText("switch data"))
      .toHaveTextContent("session-old"));

    observations.splice(0);
    rendered.rerender(<IdentitySwitchProbe
      viewId="session-current"
      onRender={(observation) => observations.push(observation)}
    />);

    expect(observations[0]).toEqual({
      selectedViewId: "session-current",
      replicaViewId: "session-current",
      dataViewId: undefined,
      availableActionCount: 0,
    });
    expect(manualRetryOwners).toEqual([]);
    expect(staleOwners).toEqual([]);
    for (const request of requests) request.resolve(jsonResponse(snapshotFor("session-current")));
  });

  it("keeps an exact cached View read-only across remount until the new stream opens", async () => {
    const view = productionViews.worker(WORKER_SESSION_ID);
    const seed = clientViewReplicas.acquire(view);
    seed.replica.commitDecodedSnapshot(view.decoder.decodeSnapshot(workerSnapshot(), {
      requestedClientCommandIds: [],
    }));
    seed.replica.setConnection("connected");
    seed.release();
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation(
      async () => jsonResponse(workerSnapshot()),
    ));
    vi.stubGlobal("EventSource", RemountStream);

    const first = render(<RemountAuthorityProbe />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(1));
    act(() => RemountStream.instances[0].onopen?.());
    expect(screen.getByRole("button", { name: "Projected mutation" })).toBeEnabled();
    first.unmount();

    render(<RemountAuthorityProbe />);
    expect(screen.getByRole("button", { name: "Projected mutation" })).toBeDisabled();
    expect(screen.getByLabelText("remount lifecycle")).toHaveTextContent("reconnecting");
    expect(screen.getByLabelText("remount presentation")).toHaveTextContent("reconnecting");
    await waitFor(() => expect(RemountStream.instances).toHaveLength(2));
    act(() => RemountStream.instances[1].onopen?.());
    expect(screen.getByRole("button", { name: "Projected mutation" })).toBeEnabled();
  });

  it("ignores stale StrictMode fetch failures after the exact View identity changes", async () => {
    const requests = new Map<string, Array<ReturnType<typeof deferred<Response>>>>();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const match = String(input).match(/worker_detail\/([^?]+)/);
      const viewId = decodeURIComponent(match?.[1] ?? "");
      const request = deferred<Response>();
      requests.set(viewId, [...(requests.get(viewId) ?? []), request]);
      return request.promise;
    }));

    const { rerender } = render(
      <StrictMode><FrontierProbe viewId="session-old" /></StrictMode>,
    );
    await waitFor(() => expect(requests.get("session-old")).toHaveLength(2));
    rerender(<StrictMode><FrontierProbe viewId="session-current" /></StrictMode>);
    await waitFor(() => expect(requests.get("session-current")?.length).toBeGreaterThan(0));

    await act(async () => Promise.all((requests.get("session-old") ?? []).map((request) => {
      request.reject(new Error("stale controller failure"));
      return request.promise.catch(() => undefined);
    })));
    expect(screen.getByLabelText("frontier error")).toHaveTextContent("no error");

    await act(async () => Promise.all((requests.get("session-current") ?? []).map((request) => {
      request.reject(new Error("current controller failure"));
      return request.promise.catch(() => undefined);
    })));
    expect(screen.getByLabelText("frontier error"))
      .toHaveTextContent("Frontier snapshot is temporarily unavailable.");
    expect(screen.getByLabelText("frontier status")).toHaveTextContent("error");
  });

  it("exposes explicit Manual Retry and command-stale reconciliation", async () => {
    const manualRetry = vi.spyOn(FrontierViewController.prototype, "manualRetry")
      .mockImplementation(() => undefined);
    const stale = vi.spyOn(FrontierViewController.prototype, "reconcileCommandStale")
      .mockImplementation(() => undefined);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation(
      async () => jsonResponse(workerSnapshot()),
    ));
    vi.stubGlobal("EventSource", RemountStream);
    render(<RetryProbe />);
    await waitFor(() => expect(RemountStream.instances).toHaveLength(1));

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(manualRetry).toHaveBeenCalledTimes(1);
    expect(stale).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Stale" }));
    expect(stale).toHaveBeenCalledTimes(1);
    expect(manualRetry).toHaveBeenCalledTimes(1);
  });
});

function snapshotFor(viewId: string) {
  return workerSnapshot({
    view_id: viewId,
    data: workerData({ worker_session_id: viewId }),
  });
}

function jsonResponse(value: unknown): Response {
  return new Response(JSON.stringify(value), { status: 200 });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

void snapshotFor;
