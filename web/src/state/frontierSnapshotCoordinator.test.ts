import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FrontierSnapshotCoordinator,
  type SnapshotAttemptContext,
  type SnapshotCoordinatorEffects,
  type SnapshotFetchOutcome,
  type SnapshotRecoveryCause,
} from "./frontierSnapshotCoordinator";

type TestSnapshot = Readonly<{ value: "decoded" }>;

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("FrontierSnapshotCoordinator", () => {
  it("acquires one slot, fences, then freezes generation/cause/request ids before fetch", async () => {
    const order: string[] = [];
    const requested = ["client-command-1"];
    const harness = createHarness({
      fenceAndCloseStream: () => { order.push("fence"); },
      captureRequestedClientCommandIds: () => {
        order.push("capture");
        return requested;
      },
      fetchSnapshot: async (context) => {
        order.push("fetch");
        expect(context.generation).toBe(1);
        expect(context.cause).toEqual({ kind: "initial" });
        expect(Object.isFrozen(context.cause)).toBe(true);
        expect(context.requestedClientCommandIds).toEqual(["client-command-1"]);
        expect(Object.isFrozen(context.requestedClientCommandIds)).toBe(true);
        return decodedSnapshot();
      },
      commitSnapshot: () => {
        order.push("commit");
        return { kind: "committed" };
      },
    });

    harness.coordinator.start();
    requested[0] = "mutated-after-capture";
    await flush();

    expect(order).toEqual(["fence", "capture", "fetch", "commit"]);
    expect(harness.connectWhenIdle).toHaveBeenCalledTimes(1);
  });

  it("fails closed without fetching when the synchronous fence throws", async () => {
    const harness = createHarness({
      fenceAndCloseStream: () => { throw new Error("broken fence"); },
    });

    harness.coordinator.start();
    await flush();

    expect(harness.fetchSnapshot).not.toHaveBeenCalled();
    expect(harness.onStopped).toHaveBeenCalledWith("fence_failed");
    expect(harness.connectWhenIdle).not.toHaveBeenCalled();
  });

  it("serializes a queued automatic successor and opens no intermediate stream", async () => {
    const first = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const second = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const harness = createHarness({
      fetchSnapshot: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockImplementationOnce(() => second.promise),
    });

    harness.coordinator.start();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(1);
    first.resolve(decodedSnapshot());
    await flush();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.connectWhenIdle).not.toHaveBeenCalled();

    second.resolve(decodedSnapshot());
    await flush();
    expect(harness.connectWhenIdle).toHaveBeenCalledTimes(1);
  });

  it("queues a commit-stage recovery and runs it only after releasing the current slot", async () => {
    let coordinator!: FrontierSnapshotCoordinator<TestSnapshot>;
    let commitCount = 0;
    const harness = createHarness({
      commitSnapshot: () => {
        commitCount += 1;
        if (commitCount === 1) {
          coordinator.requestAutomatic({ kind: "command_stale" });
          return { kind: "recovery_queued" };
        }
        return { kind: "committed" };
      },
    });
    coordinator = harness.coordinator;

    coordinator.start();
    await flush();

    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.commitSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.connectWhenIdle).toHaveBeenCalledTimes(1);
  });

  it("coalesces one identical queued automatic cause", async () => {
    const first = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const harness = createHarness({
      fetchSnapshot: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockResolvedValueOnce(decodedSnapshot()),
    });

    harness.coordinator.start();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    first.resolve(decodedSnapshot());
    await flush();

    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.onStopped).not.toHaveBeenCalled();
  });

  it("fails closed on a second different automatic cause instead of growing a queue", async () => {
    const first = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const harness = createHarness({ fetchSnapshot: vi.fn(() => first.promise) });

    harness.coordinator.start();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    harness.coordinator.requestAutomatic({ kind: "command_stale" });
    first.resolve(decodedSnapshot());
    await flush();

    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(1);
    expect(harness.onStopped).toHaveBeenCalledWith("automatic_conflict");
    expect(harness.connectWhenIdle).not.toHaveBeenCalled();
  });

  it("queues Manual Retry once without early reset and gives it successor priority", async () => {
    const first = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const second = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const order: string[] = [];
    const harness = createHarness({
      fenceAndCloseStream: () => { order.push("fence"); },
      beginManualRetry: () => { order.push("manual-reset"); },
      fetchSnapshot: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockImplementationOnce((context: SnapshotAttemptContext) => {
          order.push(`fetch-${context.cause.kind}`);
          return second.promise;
        }),
    });

    harness.coordinator.start();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    harness.coordinator.manualRetry();
    harness.coordinator.manualRetry();
    expect(harness.beginManualRetry).not.toHaveBeenCalled();
    first.resolve(decodedSnapshot());
    await flush();

    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(order.slice(-3)).toEqual(["fence", "manual-reset", "fetch-manual_retry"]);
    expect(harness.connectWhenIdle).not.toHaveBeenCalled();
    second.resolve(decodedSnapshot());
    await flush();
    expect(harness.connectWhenIdle).toHaveBeenCalledTimes(1);
  });

  it("stops initial and Manual retryable transport after one attempt", async () => {
    vi.useFakeTimers();
    const harness = createHarness({
      fetchSnapshot: vi.fn().mockResolvedValue({ kind: "retryable_transport" }),
    });

    harness.coordinator.start();
    await flush();
    expect(harness.onStopped).toHaveBeenLastCalledWith("retryable_transport");
    expect(vi.getTimerCount()).toBe(0);

    harness.coordinator.manualRetry();
    await flush();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.beginManualRetry).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("retries an automatic cause with its exact frozen context through one timer", async () => {
    vi.useFakeTimers();
    let requested = ["client-command-old"];
    const contexts: SnapshotAttemptContext[] = [];
    const harness = createHarness({
      retryDelayMs: 25,
      captureRequestedClientCommandIds: () => requested,
      fetchSnapshot: vi.fn(async (
        context: SnapshotAttemptContext,
      ): Promise<SnapshotFetchOutcome<TestSnapshot>> => {
        contexts.push(context);
        if (context.cause.kind === "initial") return decodedSnapshot();
        return contexts.filter((item) => item.cause.kind === "sse_transport").length === 1
          ? { kind: "retryable_transport" }
          : decodedSnapshot();
      }),
    });
    harness.coordinator.start();
    await flush();
    harness.connectWhenIdle.mockClear();

    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    await flush();
    expect(vi.getTimerCount()).toBe(1);
    requested = ["client-command-new"];
    await vi.advanceTimersByTimeAsync(25);
    await flush();

    const automatic = contexts.filter((item) => item.cause.kind === "sse_transport");
    expect(automatic).toHaveLength(2);
    expect(automatic[1].cause).toBe(automatic[0].cause);
    expect(automatic[1].requestedClientCommandIds).toEqual(["client-command-old"]);
    expect(harness.connectWhenIdle).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("coalesces the same armed cause and replaces it with one different immediate cause", async () => {
    vi.useFakeTimers();
    const causes: string[] = [];
    const harness = createHarness({
      retryDelayMs: 50,
      fetchSnapshot: vi.fn(async (
        context: SnapshotAttemptContext,
      ): Promise<SnapshotFetchOutcome<TestSnapshot>> => {
        causes.push(context.cause.kind);
        if (context.cause.kind === "initial") return decodedSnapshot();
        if (context.cause.kind === "sse_transport") return { kind: "retryable_transport" };
        return decodedSnapshot();
      }),
    });
    harness.coordinator.start();
    await flush();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    await flush();
    expect(vi.getTimerCount()).toBe(1);

    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    expect(vi.getTimerCount()).toBe(1);
    harness.coordinator.requestAutomatic({ kind: "command_stale" });
    await flush();

    expect(vi.getTimerCount()).toBe(0);
    expect(causes).toEqual(["initial", "sse_transport", "command_stale"]);
  });

  it("caps repeated automatic transport backoff at the configured maximum", async () => {
    vi.useFakeTimers();
    const harness = createHarness({
      retryDelayMs: 10,
      maxRetryDelayMs: 25,
      fetchSnapshot: vi.fn(async (
        context: SnapshotAttemptContext,
      ): Promise<SnapshotFetchOutcome<TestSnapshot>> => (
        context.cause.kind === "initial"
          ? decodedSnapshot()
          : { kind: "retryable_transport" }
      )),
    });
    harness.coordinator.start();
    await flush();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    await flush();

    await vi.advanceTimersByTimeAsync(10);
    await flush();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(19);
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(1);
    await flush();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(24);
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(1);
    await flush();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(5);
  });

  it("preserves a queued Manual Retry when an in-flight attempt stops automatically", async () => {
    const first = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const harness = createHarness({
      fetchSnapshot: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockResolvedValueOnce(decodedSnapshot()),
    });
    harness.coordinator.start();
    harness.coordinator.manualRetry();
    harness.coordinator.stopRecovery("command_reconciliation_blocked");
    first.resolve(decodedSnapshot());
    await flush();

    expect(harness.beginManualRetry).toHaveBeenCalledTimes(1);
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.onStopped).not.toHaveBeenCalledWith("command_reconciliation_blocked");
  });

  it("cancels a pending retry immediately when automatic recovery is stopped", async () => {
    vi.useFakeTimers();
    const harness = createHarness({
      retryDelayMs: 10,
      fetchSnapshot: vi.fn(async (
        context: SnapshotAttemptContext,
      ): Promise<SnapshotFetchOutcome<TestSnapshot>> => (
        context.cause.kind === "initial"
          ? decodedSnapshot()
          : { kind: "retryable_transport" }
      )),
    });
    harness.coordinator.start();
    await flush();
    harness.coordinator.requestAutomatic({ kind: "sse_transport" });
    await flush();
    expect(vi.getTimerCount()).toBe(1);

    harness.coordinator.stopRecovery("command_reconciliation_blocked");
    expect(vi.getTimerCount()).toBe(0);
    await vi.runAllTimersAsync();
    expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
    expect(harness.onStopped).toHaveBeenLastCalledWith("command_reconciliation_blocked");
  });

  it("allows Manual Retry to revive terminal/access/blocked stopped outcomes", async () => {
    const dispositions = ["access_clear", "terminal_http", "stale_decode_context"] as const;
    for (const disposition of dispositions) {
      const harness = createHarness({
        fetchSnapshot: vi.fn()
          .mockResolvedValueOnce({ kind: "stopped_manual_revivable", disposition })
          .mockResolvedValueOnce(decodedSnapshot()),
      });
      harness.coordinator.start();
      await flush();
      harness.coordinator.requestAutomatic({ kind: "sse_transport" });
      expect(harness.fetchSnapshot).toHaveBeenCalledTimes(1);
      harness.coordinator.manualRetry();
      await flush();
      expect(harness.fetchSnapshot).toHaveBeenCalledTimes(2);
      expect(harness.beginManualRetry).toHaveBeenCalledTimes(1);
    }
  });

  it("does not revive client-upgrade or disposed state", async () => {
    const upgraded = createHarness({
      fetchSnapshot: vi.fn().mockResolvedValue({ kind: "client_upgrade_required" }),
    });
    upgraded.coordinator.start();
    await flush();
    upgraded.coordinator.manualRetry();
    upgraded.coordinator.requestAutomatic({ kind: "sse_transport" });
    expect(upgraded.fetchSnapshot).toHaveBeenCalledTimes(1);
    expect(upgraded.onClientUpgradeRequired).toHaveBeenCalledTimes(1);

    const pending = deferred<SnapshotFetchOutcome<TestSnapshot>>();
    const disposed = createHarness({ fetchSnapshot: vi.fn(() => pending.promise) });
    disposed.coordinator.start();
    disposed.coordinator.stop();
    disposed.coordinator.manualRetry();
    disposed.coordinator.requestAutomatic({ kind: "sse_transport" });
    pending.resolve(decodedSnapshot());
    await flush();
    expect(disposed.fetchSnapshot).toHaveBeenCalledTimes(1);
    expect(disposed.commitSnapshot).not.toHaveBeenCalled();
    expect(disposed.connectWhenIdle).not.toHaveBeenCalled();
  });

  it("treats command_stale as automatic and never gives it Manual reset semantics", async () => {
    const harness = createHarness();
    harness.coordinator.start();
    await flush();
    harness.beginManualRetry.mockClear();

    harness.coordinator.requestAutomatic({ kind: "command_stale" });
    await flush();

    expect(harness.beginManualRetry).not.toHaveBeenCalled();
    expect(harness.fetchSnapshot.mock.calls[1]?.[0].cause).toEqual({ kind: "command_stale" });
  });
});

function createHarness(overrides: Partial<SnapshotCoordinatorEffects<TestSnapshot>> & {
  readonly retryDelayMs?: number;
  readonly maxRetryDelayMs?: number;
} = {}) {
  const fenceAndCloseStream = vi.fn(overrides.fenceAndCloseStream ?? (() => undefined));
  const captureRequestedClientCommandIds = vi.fn(
    overrides.captureRequestedClientCommandIds ?? (() => []),
  );
  const fetchSnapshot = vi.fn(
    overrides.fetchSnapshot ?? (async () => decodedSnapshot()),
  );
  const commitSnapshot = vi.fn(
    overrides.commitSnapshot ?? (() => ({ kind: "committed" } as const)),
  );
  const beginManualRetry = vi.fn(overrides.beginManualRetry ?? (() => undefined));
  const connectWhenIdle = vi.fn(overrides.connectWhenIdle ?? (() => undefined));
  const onStopped = vi.fn(overrides.onStopped ?? (() => undefined));
  const onClientUpgradeRequired = vi.fn(
    overrides.onClientUpgradeRequired ?? (() => undefined),
  );
  const coordinator = new FrontierSnapshotCoordinator({
    fenceAndCloseStream,
    captureRequestedClientCommandIds,
    fetchSnapshot,
    commitSnapshot,
    beginManualRetry,
    connectWhenIdle,
    onStopped,
    onClientUpgradeRequired,
  }, {
    retryDelayMs: overrides.retryDelayMs ?? 10,
    maxRetryDelayMs: overrides.maxRetryDelayMs,
  });
  return {
    coordinator,
    fenceAndCloseStream,
    captureRequestedClientCommandIds,
    fetchSnapshot,
    commitSnapshot,
    beginManualRetry,
    connectWhenIdle,
    onStopped,
    onClientUpgradeRequired,
  };
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

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function decodedSnapshot(): SnapshotFetchOutcome<TestSnapshot> {
  return { kind: "decoded", snapshot: Object.freeze({ value: "decoded" }) };
}

function proveClosedCoordinatorSurface(coordinator: FrontierSnapshotCoordinator): void {
  // @ts-expect-error Initial attempts can only be started by the coordinator.
  coordinator.requestAutomatic({ kind: "initial" });
  // @ts-expect-error Fence failures can only be produced inside the coordinator.
  coordinator.stopRecovery("fence_failed");
}

void proveClosedCoordinatorSurface;
void ({} as SnapshotRecoveryCause);
