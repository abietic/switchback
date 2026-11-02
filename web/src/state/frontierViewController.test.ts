import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkerDetailData } from "../api/contracts";
import { productionViews } from "../projections/productionViewRegistry";
import {
  WORKER_SESSION_ID,
  workerData,
  workerDelta,
  workerSnapshot,
} from "../projections/testdata/workerFixtures";
import { ClientViewReplica } from "./clientViewReplica";
import {
  FrontierViewController,
  type EventStream,
  type EventStreamFactory,
  type FrontierViewLifecycleState,
} from "./frontierViewController";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

class FakeStream implements EventStream {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, (event: MessageEvent<string>) => void>();

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    this.listeners.set(type, listener);
  }

  close(): void {
    this.closed = true;
  }

  emit(type: string, value: unknown, lastEventId = ""): void {
    this.listeners.get(type)?.(new MessageEvent(type, {
      data: JSON.stringify(value),
      lastEventId,
    }));
  }

  open(): void {
    this.onopen?.();
  }
}

describe("bound FrontierViewController", () => {
  it("decodes before commit, reconciles frozen client ids, and connects after the committed cursor", async () => {
    const replica = workerReplica();
    await replica.submitCommand({
      clientCommandId: "client-command-a",
      action: "steer",
      submit: async () => ({ command_id: "server-command-a" }),
      isDefinitiveFailure: () => false,
    });
    const { controller, fetcher, urls } = controllerHarness(replica, [workerSnapshot()]);

    controller.start();
    await vi.waitFor(() => expect(urls).toHaveLength(1));

    expect(fetcher.mock.calls[0]?.[0]).toBe(
      `/api/v1/views/worker_detail/${WORKER_SESSION_ID}?reconcile_client_command_id=client-command-a`,
    );
    expect(urls[0]).toBe(
      `/api/v1/views/worker_detail/${WORKER_SESSION_ID}/events?cursor=1%3A1`,
    );
    expect(replica.getState().data?.worker_session_id).toBe(WORKER_SESSION_ID);
    controller.stop();
  });

  it("rejects malformed authority with zero replica mutation", async () => {
    const replica = workerReplica();
    const before = replica.getState();
    const lifecycle: FrontierViewLifecycleState[] = [];
    const { controller, streams } = controllerHarness(replica, [workerSnapshot({
      data: workerData({ session_revision: "7" }),
    })], lifecycle);

    controller.start();
    await vi.waitFor(() => expect(lifecycle.at(-1)?.status).toBe("error"));

    expect(replica.getState()).toBe(before);
    expect(streams).toHaveLength(0);
    controller.stop();
  });

  it("keeps a stopped lifecycle unchanged when command_stale arrives late", async () => {
    const replica = workerReplica();
    const lifecycle: FrontierViewLifecycleState[] = [];
    const { controller, fetcher } = controllerHarness(replica, [workerSnapshot({
      data: workerData({ session_revision: "7" }),
    })], lifecycle);
    controller.start();
    await vi.waitFor(() => expect(lifecycle.at(-1)?.status).toBe("error"));
    const stopped = lifecycle.at(-1);
    const connection = replica.getState().connection;

    controller.reconcileCommandStale();

    expect(lifecycle.at(-1)).toBe(stopped);
    expect(replica.getState().connection).toBe(connection);
    expect(fetcher).toHaveBeenCalledTimes(1);
    controller.stop();
  });

  it("classifies future schema identity-first", async () => {
    const matchingLifecycle: FrontierViewLifecycleState[] = [];
    const matching = controllerHarness(workerReplica(), [{
      ...workerSnapshot(),
      schema_version: "frontier.projection_snapshot.v2",
    }], matchingLifecycle);
    matching.controller.start();
    await vi.waitFor(() => expect(matchingLifecycle.at(-1)?.status)
      .toBe("client_upgrade_required"));
    expect(matching.streams).toHaveLength(0);

    const foreignLifecycle: FrontierViewLifecycleState[] = [];
    const foreign = controllerHarness(workerReplica(), [{
      ...workerSnapshot(),
      schema_version: "frontier.projection_snapshot.v2",
      view_id: "session-foreign",
    }], foreignLifecycle);
    foreign.controller.start();
    await vi.waitFor(() => expect(foreignLifecycle.at(-1)?.status).toBe("error"));
    expect(foreignLifecycle.at(-1)?.status).not.toBe("client_upgrade_required");
    matching.controller.stop();
    foreign.controller.stop();
  });

  it("commits exact-next Delta and ignores same-epoch duplicate without recovery", async () => {
    const replica = workerReplica();
    const { controller, fetcher, streams } = controllerHarness(replica, [workerSnapshot()]);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));

    streams[0].emit("projection_delta", workerDelta(), "1:2");
    expect(replica.getState().data?.runtime_health).toBe("Suspect");
    const committed = replica.getState();
    streams[0].emit("projection_delta", workerDelta({
      delta_id: "worker-duplicate-1",
      view_cursor: 1,
    }), "1:1");

    expect(replica.getState()).toBe(committed);
    expect(fetcher).toHaveBeenCalledTimes(1);
    controller.stop();
  });

  it("fences old Delta/open/error callbacks before an automatic Snapshot attempt", async () => {
    const second = deferred<Response>();
    const replica = workerReplica();
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(workerSnapshot()))
      .mockImplementationOnce(async () => second.promise);
    const { controller, streams } = controllerHarness(replica, [], [], fetcher);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));
    streams[0].open();

    controller.reconcileCommandStale();
    expect(streams[0].closed).toBe(true);
    const before = replica.getState();
    streams[0].emit("projection_delta", workerDelta(), "1:2");
    streams[0].open();
    streams[0].onerror?.();
    expect(replica.getState()).toBe(before);
    expect(fetcher).toHaveBeenCalledTimes(2);

    second.resolve(jsonResponse(workerSnapshot({
      view_cursor: 2,
      projection_version: 2,
    })));
    await vi.waitFor(() => expect(streams).toHaveLength(2));
    expect(streams[1].closed).toBe(false);
    controller.stop();
  });

  it("SSE error followed by Snapshot 404 clears only the bound replica and never reconnects", async () => {
    vi.useFakeTimers();
    const bound = workerReplica();
    const unrelatedView = productionViews.worker(WORKER_SESSION_ID);
    const unrelated = new ClientViewReplica<WorkerDetailData>(
      unrelatedView.viewType,
      unrelatedView.viewId,
    );
    unrelated.commitDecodedSnapshot(unrelatedView.decoder.decodeSnapshot(workerSnapshot(), {
      requestedClientCommandIds: [],
    }));
    const unrelatedBefore = unrelated.getState();
    const command = deferred<{ command_id: string }>();
    const { controller, streams, fetcher } = controllerHarness(bound, [
      workerSnapshot(),
      new Response(null, { status: 404 }),
    ], [], undefined, 50);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));
    void bound.submitCommand({
      clientCommandId: "client-command-pending",
      action: "steer",
      submit: () => command.promise,
      isDefinitiveFailure: () => false,
    });

    streams[0].onerror?.();
    expect(streams[0].closed).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(50);
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));

    expect(bound.getState()).toMatchObject({
      sync: "empty",
      availableActions: [],
      relatedRefs: [],
      pendingCommands: [],
    });
    expect(bound.getState().data).toBeUndefined();
    expect(unrelated.getState()).toBe(unrelatedBefore);
    expect(streams).toHaveLength(1);
    command.resolve({ command_id: "server-command-late" });
    controller.stop();
  });

  it("SSE 503 probe uses one retry timer and reconnects exactly once after a later 200", async () => {
    vi.useFakeTimers();
    const replica = workerReplica();
    const { controller, streams, fetcher, urls } = controllerHarness(replica, [
      workerSnapshot(),
      new Response(null, { status: 503 }),
      workerSnapshot({ view_cursor: 3, projection_version: 3 }),
    ], [], undefined, 40);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));

    streams[0].onerror?.();
    await vi.advanceTimersByTimeAsync(40);
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(streams).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(1);

    await vi.advanceTimersByTimeAsync(80);
    await vi.waitFor(() => expect(streams).toHaveLength(2));
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(urls.at(-1)).toContain("cursor=1%3A3");
    await vi.advanceTimersByTimeAsync(160);
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(streams).toHaveLength(2);
    controller.stop();
  });

  it("bounds repeated continuity recovery until trusted progress or Manual Retry", async () => {
    const lifecycle: FrontierViewLifecycleState[] = [];
    const replica = workerReplica();
    const { controller, streams, fetcher } = controllerHarness(replica, [
      workerSnapshot(),
      workerSnapshot(),
      workerSnapshot({ view_cursor: 2, projection_version: 2 }),
    ], lifecycle);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));
    const gap = workerDelta({ view_cursor: 3, delta_id: "gap-3" });

    streams[0].emit("projection_delta", gap, "1:3");
    await vi.waitFor(() => expect(streams).toHaveLength(2));
    streams[1].emit("projection_delta", gap, "1:3");
    expect(streams[1].closed).toBe(true);
    expect(lifecycle.at(-1)?.status).toBe("error");
    expect(fetcher).toHaveBeenCalledTimes(2);

    controller.manualRetry();
    await vi.waitFor(() => expect(streams).toHaveLength(3));
    expect(fetcher).toHaveBeenCalledTimes(3);
    controller.stop();
  });

  it("bounds replayable malformed Delta integrity independently from continuity", async () => {
    const lifecycle: FrontierViewLifecycleState[] = [];
    const { controller, streams, fetcher } = controllerHarness(workerReplica(), [
      workerSnapshot(),
      workerSnapshot(),
    ], lifecycle);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));
    const malformed = workerDelta({ payload: { data: { worker_session_id: WORKER_SESSION_ID } } });

    streams[0].emit("projection_delta", malformed, "1:2");
    await vi.waitFor(() => expect(streams).toHaveLength(2));
    streams[1].emit("projection_delta", malformed, "1:2");

    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(streams[1].closed).toBe(true);
    expect(lifecycle.at(-1)?.status).toBe("error");
    controller.stop();
  });

  it("keeps command_stale automatic and reserves guard reset for explicit Manual Retry", async () => {
    const replica = workerReplica();
    const beginManualRetry = vi.spyOn(replica, "beginManualRetry");
    const { controller, streams, fetcher } = controllerHarness(replica, [
      workerSnapshot(),
      workerSnapshot(),
      workerSnapshot(),
    ]);
    controller.start();
    await vi.waitFor(() => expect(streams).toHaveLength(1));

    controller.reconcileCommandStale();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    expect(beginManualRetry).not.toHaveBeenCalled();
    controller.manualRetry();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
    expect(beginManualRetry).toHaveBeenCalledTimes(1);
    controller.stop();
  });
});

function workerReplica(): ClientViewReplica<WorkerDetailData> {
  return new ClientViewReplica<WorkerDetailData>("worker_detail", WORKER_SESSION_ID);
}

function controllerHarness(
  replica: ClientViewReplica<WorkerDetailData>,
  responses: unknown[],
  lifecycle: FrontierViewLifecycleState[] = [],
  suppliedFetcher?: typeof fetch,
  retryDelayMs = 10,
) {
  const fetcher = suppliedFetcher
    ? vi.mocked(suppliedFetcher)
    : vi.fn<typeof fetch>().mockImplementation(async () => responseFor(responses.shift()));
  const streams: FakeStream[] = [];
  const urls: string[] = [];
  const streamFactory: EventStreamFactory = (url) => {
    urls.push(url);
    const stream = new FakeStream();
    streams.push(stream);
    return stream;
  };
  const controller = new FrontierViewController({
    view: productionViews.worker(WORKER_SESSION_ID),
    replica,
    fetcher,
    streamFactory,
    retryDelayMs,
    maxRetryDelayMs: 1_000,
    onStateChange: (state) => lifecycle.push(state),
  });
  return { controller, fetcher, streams, urls, lifecycle };
}

function responseFor(value: unknown): Response {
  return value instanceof Response ? value : jsonResponse(value);
}

function jsonResponse(value: unknown): Response {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}
