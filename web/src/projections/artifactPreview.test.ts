import { describe, expect, it, vi } from "vitest";
import type { ArtifactDetailData, ProjectionSnapshot } from "../api/contracts";
import { ClientViewReplica } from "../state/clientViewReplica";
import { loadArtifactPreview } from "./artifactPreview";
import { productionViews } from "./productionViewRegistry";

describe("Artifact preview", () => {
  it("returns only decoder-proved effective Artifact data without controller effects", async () => {
    const view = productionViews.artifact("artifact-1");
    const replica = new ClientViewReplica<ArtifactDetailData>(view.viewType, view.viewId);
    replica.commitDecodedSnapshot(view.decoder.decodeSnapshot(artifactSnapshot(), {
      requestedClientCommandIds: [],
    }));
    const before = replica.getState();
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    const eventSource = vi.fn();
    vi.stubGlobal("EventSource", eventSource);

    const outcome = await loadArtifactPreview(view, {
      fetcher: vi.fn().mockResolvedValue(new Response(JSON.stringify(artifactSnapshot()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })),
    });

    expect(outcome.kind).toBe("available");
    if (outcome.kind !== "available") throw new Error("expected available preview");
    expect(outcome.preview.data.artifact_id).toBe("artifact-1");
    expect(outcome.preview).not.toHaveProperty("envelope");
    expect(replica.getState()).toBe(before);
    expect(setTimeoutSpy).not.toHaveBeenCalled();
    expect(eventSource).not.toHaveBeenCalled();
    setTimeoutSpy.mockRestore();
    vi.unstubAllGlobals();
  });

  it("preserves closed transport outcomes without decoding or side effects", async () => {
    const json = vi.fn();
    const outcome = await loadArtifactPreview(productionViews.artifact("artifact-1"), {
      fetcher: vi.fn().mockResolvedValue({ ok: false, status: 404, json } as unknown as Response),
    });

    expect(outcome).toEqual({ kind: "access_clear", status: 404 });
    expect(json).not.toHaveBeenCalled();
  });
});

function artifactSnapshot(): ProjectionSnapshot<ArtifactDetailData> {
  const detail: ArtifactDetailData = {
    artifact_id: "artifact-1",
    type: "report",
    label: "Report",
    size_bytes: 12,
    checksum: `sha256:${"a".repeat(64)}`,
    completeness: "complete",
    worker_run_id: "run-1",
    agent_turn_id: "turn-1",
    runtime_generation: 1,
    generated_at: "2026-08-24T00:00:00Z",
    availability: "online",
    content: "# Result",
    download_url: "/api/v1/artifacts/artifact-1/content",
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "online",
      renderable: true,
      download_allowed: true,
      media_type: "text/markdown",
      content_size_bytes: 12,
      diagnostics: [],
    },
  };
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "artifact_detail",
    view_id: "artifact-1",
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "artifact-test",
    generated_at: "2026-08-24T00:00:00Z",
    data: detail,
    available_actions: [{
      action: "artifact.download",
      target_id: "artifact-1",
      expected_revision: 1,
    }],
    related_refs: [],
  };
}
