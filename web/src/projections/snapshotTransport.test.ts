import { describe, expect, it, vi } from "vitest";
import { productionViews } from "./productionViewRegistry";
import { fetchBoundProjectionSnapshot } from "./snapshotTransport";
import { WORKER_SESSION_ID, workerSnapshot } from "./testdata/workerFixtures";

describe("bound Snapshot transport", () => {
  it.each([401, 403, 404])("classifies %s before reading the body as access_clear", async (status) => {
    const json = vi.fn(() => Promise.reject(new Error("must not decode")));
    const outcome = await fetchBoundProjectionSnapshot(productionViews.worker("session-1"), [], {
      fetcher: vi.fn().mockResolvedValue(response(status, json)),
    });

    expect(outcome).toEqual({ kind: "access_clear", status });
    expect(json).not.toHaveBeenCalled();
  });

  it.each([408, 425, 429, 500, 503, 599])(
    "classifies %s before reading the body as retryable_transport",
    async (status) => {
      const json = vi.fn(() => Promise.reject(new Error("must not decode")));
      const outcome = await fetchBoundProjectionSnapshot(productionViews.worker("session-1"), [], {
        fetcher: vi.fn().mockResolvedValue(response(status, json)),
      });

      expect(outcome).toEqual({ kind: "retryable_transport", status });
      expect(json).not.toHaveBeenCalled();
    },
  );

  it.each([300, 400, 409, 422, 600])(
    "classifies final HTTP %s before reading the body as terminal_http",
    async (status) => {
      const json = vi.fn(() => Promise.reject(new Error("must not decode")));
      const outcome = await fetchBoundProjectionSnapshot(productionViews.worker("session-1"), [], {
        fetcher: vi.fn().mockResolvedValue(response(status, json)),
      });

      expect(outcome).toEqual({ kind: "terminal_http", status });
      expect(json).not.toHaveBeenCalled();
    },
  );

  it("classifies only fetch rejection as retryable transport", async () => {
    const outcome = await fetchBoundProjectionSnapshot(productionViews.worker("session-1"), [], {
      fetcher: vi.fn().mockRejectedValue(new TypeError("network down")),
    });
    expect(outcome).toEqual({ kind: "retryable_transport" });
  });

  it("decodes a 2xx body with one frozen request-id context used by the URL", async () => {
    const requested = ["client-command-b", "client-command-a", "client-command-a"];
    let release!: (value: Response) => void;
    const pending = new Promise<Response>((resolve) => { release = resolve; });
    const fetcher = vi.fn(() => pending);
    const promise = fetchBoundProjectionSnapshot(
      productionViews.worker(WORKER_SESSION_ID),
      requested,
      { fetcher },
    );
    requested.splice(0, requested.length, "client-command-mutated");
    release(new Response(JSON.stringify(workerSnapshot({
      command_receipts: [
        { client_command_id: "client-command-a", command_id: "server-command-a" },
      ],
    })), { status: 200, headers: { "Content-Type": "application/json" } }));

    const outcome = await promise;
    expect(fetcher).toHaveBeenCalledWith(
      `/api/v1/views/worker_detail/${WORKER_SESSION_ID}?reconcile_client_command_id=client-command-a&reconcile_client_command_id=client-command-b`,
      { headers: { Accept: "application/json" }, signal: undefined },
    );
    expect(outcome.kind).toBe("decoded");
    if (outcome.kind !== "decoded") throw new Error("expected decoded Snapshot");
    expect(outcome.snapshot.requestContext.requestedClientCommandIds)
      .toEqual(["client-command-a", "client-command-b"]);
    expect(Object.isFrozen(outcome.snapshot.requestContext.requestedClientCommandIds)).toBe(true);
  });

  it("does not inspect Retry-After or perform lifecycle, retry, timer, or SSE effects", async () => {
    const headers = { get: vi.fn(() => "120") };
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    const eventSource = vi.fn();
    vi.stubGlobal("EventSource", eventSource);

    const outcome = await fetchBoundProjectionSnapshot(productionViews.worker("session-1"), [], {
      fetcher: vi.fn().mockResolvedValue({
        ok: false,
        status: 503,
        headers,
        json: vi.fn(),
      } as unknown as Response),
    });

    expect(outcome).toEqual({ kind: "retryable_transport", status: 503 });
    expect(headers.get).not.toHaveBeenCalled();
    expect(setTimeoutSpy).not.toHaveBeenCalled();
    expect(eventSource).not.toHaveBeenCalled();
    setTimeoutSpy.mockRestore();
    vi.unstubAllGlobals();
  });
});

function response(status: number, json: () => Promise<unknown>): Response {
  return { ok: status >= 200 && status < 300, status, json } as unknown as Response;
}
