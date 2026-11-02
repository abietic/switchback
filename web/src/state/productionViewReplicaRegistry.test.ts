import { describe, expect, it } from "vitest";
import type { BoundProductionView } from "../projections/productionViewRegistry";
import { productionViews } from "../projections/productionViewRegistry";
import { workerSnapshot } from "../projections/testdata/workerFixtures";
import { ClientViewReplicaRegistry } from "./clientViewReplica";

function replacement(
  view: BoundProductionView<"worker">,
  contractKey: string,
): BoundProductionView<"worker"> {
  return { ...view, contractKey } as BoundProductionView<"worker">;
}

describe("production View replica leases", () => {
  it("reuses one exact replica for concurrent same-contract leases", () => {
    const registry = new ClientViewReplicaRegistry();
    const view = productionViews.worker("session-1");
    const first = registry.acquire(view);
    const second = registry.acquire(view);

    expect(second.replica).toBe(first.replica);
    expect(first.contractKey).toBe(view.contractKey);
    first.release();
    second.release();
  });

  it("fails closed when a different contract is acquired before every old lease releases", () => {
    const registry = new ClientViewReplicaRegistry();
    const current = productionViews.worker("session-1");
    const next = replacement(current, "worker-detail-v2:session-1");
    const first = registry.acquire(current);
    const second = registry.acquire(current);

    first.release();
    expect(() => registry.acquire(next)).toThrow("concurrent production View contract mismatch");
    second.release();
  });

  it("replaces exactly once after the final old lease releases", () => {
    const registry = new ClientViewReplicaRegistry();
    const current = productionViews.worker("session-1");
    const next = replacement(current, "worker-detail-v2:session-1");
    const old = registry.acquire(current);
    old.replica.commitDecodedSnapshot(current.decoder.decodeSnapshot(workerSnapshot(), {
      requestedClientCommandIds: [],
    }));
    old.release();

    const replacementLease = registry.acquire(next);
    const reusedReplacement = registry.acquire(next);
    expect(replacementLease.replica).not.toBe(old.replica);
    expect(old.replica.getState()).toMatchObject({
      sync: "empty",
      availableActions: [],
      relatedRefs: [],
    });
    expect(old.replica.getState().data).toBeUndefined();
    expect(reusedReplacement.replica).toBe(replacementLease.replica);
    replacementLease.release();
    reusedReplacement.release();
  });

  it("does not clear another exact View identity during contract replacement", () => {
    const registry = new ClientViewReplicaRegistry();
    const firstView = productionViews.worker("session-1");
    const otherView = productionViews.worker("session-2");
    const first = registry.acquire(firstView);
    const other = registry.acquire(otherView);
    const otherReplica = other.replica;
    first.release();

    const replacementLease = registry.acquire(replacement(firstView, "worker-detail-v2:session-1"));
    expect(registry.acquire(otherView).replica).toBe(otherReplica);
    replacementLease.release();
    other.release();
  });

  it("makes release idempotent and never releases a successor lease", () => {
    const registry = new ClientViewReplicaRegistry();
    const view = productionViews.worker("session-1");
    const old = registry.acquire(view);
    old.release();
    old.release();
    const successor = registry.acquire(replacement(view, "worker-detail-v2:session-1"));
    old.release();
    expect(registry.acquire(replacement(view, "worker-detail-v2:session-1")).replica)
      .toBe(successor.replica);
    successor.release();
  });
});
