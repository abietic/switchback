import { describe, expect, it } from "vitest";
import { createArtifactViewDecoder } from "./artifact";

const artifactID = "artifact-1";
const checksum = `sha256:${"a".repeat(64)}`;

function data(overrides: Record<string, unknown> = {}) {
  return {
    artifact_id: artifactID,
    type: "report",
    label: "Stage report",
    size_bytes: 128,
    checksum,
    completeness: "complete",
    worker_run_id: "run-1",
    agent_turn_id: "turn-1",
    runtime_generation: 2,
    generated_at: "2026-07-29T00:00:00Z",
    availability: "online",
    content: "# Done",
    download_url: `/api/v1/artifacts/${artifactID}/content`,
    review_manifest: {
      schema_version: "artifact.review_manifest.v1",
      outcome: "online",
      renderable: true,
      download_allowed: true,
      media_type: "text/markdown",
      content_size_bytes: 6,
      diagnostics: [],
    },
    ...overrides,
  };
}

function action(overrides: Record<string, unknown> = {}) {
  return {
    action: "artifact.download",
    target_id: artifactID,
    expected_revision: 1,
    ...overrides,
  };
}

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_snapshot.v1",
    view_type: "artifact_detail",
    view_id: artifactID,
    tenant_id: "tenant-local",
    view_epoch: 1,
    view_cursor: 1,
    projection_version: 1,
    query_fingerprint: "single-user-local-v1",
    generated_at: "2026-07-29T00:00:00Z",
    data: data(),
    available_actions: [action()],
    related_refs: [],
    ...overrides,
  };
}

function delta(eventType = "view.artifact_ready.v1", overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "frontier.projection_delta.v1",
    delta_id: "artifact-delta-2",
    view_type: "artifact_detail",
    view_id: artifactID,
    view_epoch: 1,
    view_cursor: 2,
    base_projection_version: 1,
    result_projection_version: 2,
    event_type: eventType,
    critical: true,
    source_refs: [{ type: "artifact", id: artifactID }],
    payload: { data: data(), available_actions: [action()], related_refs: [] },
    ...overrides,
  };
}

function frame(value: unknown) {
  return { eventName: "projection_delta", lastEventId: "1:2", data: JSON.stringify(value) };
}

function decodeSnapshot(overrides: Record<string, unknown> = {}) {
  return createArtifactViewDecoder(artifactID).decodeSnapshot(snapshot(overrides), {
    requestedClientCommandIds: [],
  });
}

function expectAuthorityViolation(run: () => unknown): void {
  expect(run).toThrowError(expect.objectContaining({
    classification: "invalid_contract",
    safeReason: "authority_violation",
  }));
}

describe("ArtifactDetail production view contract", () => {
  it("decodes the current Go materializer shape with exact identity and authority", () => {
    const decoder = createArtifactViewDecoder(artifactID);
    const decoded = decodeSnapshot();
    expect(decoder).toMatchObject({
      contractKey: `artifact-detail-v1:${artifactID}`,
      viewType: "artifact_detail",
      viewId: artifactID,
    });
    expect(decoded.effective).toMatchObject({
      data: { artifact_id: artifactID, checksum, availability: "online" },
      availableActions: [action()],
      relatedRefs: [],
    });
  });

  it("accepts only the two emitted Artifact event families", () => {
    for (const eventType of ["view.entity_upserted.v1", "view.artifact_ready.v1"]) {
      const decoder = createArtifactViewDecoder(artifactID);
      const current = decodeSnapshot().effective;
      expect(decoder.decodeDeltaFrame(frame(delta(eventType)), current).result.kind).toBe("apply");
    }
    for (const eventType of [
      "view.stream_checkpointed.v1",
      "view.command_receipted.v1",
      "view.projection_rebuilt.v1",
      "future.artifact.v2",
    ]) {
      const decoder = createArtifactViewDecoder(artifactID);
      const current = decodeSnapshot().effective;
      expectAuthorityViolation(() => decoder.decodeDeltaFrame(frame(delta(eventType)), current));
    }
  });

  it("rejects a foreign bound Artifact before product data is admitted", () => {
    expect(() => createArtifactViewDecoder("artifact-other").decodeSnapshot(snapshot(), {
      requestedClientCommandIds: [],
    })).toThrowError(expect.objectContaining({ classification: "foreign_identity" }));
  });

  it("rejects malformed Artifact identity, checksum, numbers and timestamps", () => {
    for (const malformed of [
      { artifact_id: "artifact-other" },
      { artifact_id: " artifact-1" },
      { checksum: "sha256:abc" },
      { size_bytes: -1 },
      { size_bytes: 1.5 },
      { size_bytes: Number.MAX_SAFE_INTEGER + 1 },
      { runtime_generation: 0 },
      { generated_at: "2026-07-29" },
    ]) {
      expectAuthorityViolation(() => decodeSnapshot({ data: data(malformed) }));
    }
  });

  it("keeps outcome, manifest, media, render and download fields mutually consistent", () => {
    const cases = [
      data({ availability: "future" }),
      data({ review_manifest: { ...data().review_manifest as object, outcome: "missing" } }),
      data({ review_manifest: { ...data().review_manifest as object, media_type: "text/x-diff" } }),
      data({ download_url: "/forged" }),
      data({ download_url: undefined }),
      data({ review_manifest: { ...data().review_manifest as object, download_allowed: false } }),
      data({ review_manifest: { ...data().review_manifest as object, renderable: false } }),
    ];
    for (const malformed of cases) {
      expectAuthorityViolation(() => decodeSnapshot({ data: malformed }));
    }
  });

  it("admits every exact non-online materializer outcome without download authority drift", () => {
    for (const outcome of ["empty", "binary", "oversized", "parse_failed"] as const) {
      expect(() => decodeSnapshot({
        data: data({
          availability: outcome,
          content: "",
          review_manifest: {
            ...data().review_manifest as object,
            outcome,
            renderable: false,
            download_allowed: true,
          },
        }),
        available_actions: [],
      })).not.toThrow();
    }
    for (const outcome of ["missing", "checksum_failed"] as const) {
      expect(() => decodeSnapshot({
        data: data({
          availability: outcome,
          content: "",
          download_url: undefined,
          review_manifest: {
            ...data().review_manifest as object,
            outcome,
            renderable: false,
            download_allowed: false,
          },
        }),
        available_actions: [],
      })).not.toThrow();
    }
  });

  it("rejects unknown, duplicated, metadata-bearing or mismatched download authority", () => {
    for (const actions of [
      [action({ action: "artifact.preview" })],
      [action(), action()],
      [action({ target_id: "artifact-other" })],
      [action({ expected_revision: 2 })],
      [action({ metadata: {} })],
    ]) {
      expectAuthorityViolation(() => decodeSnapshot({ available_actions: actions }));
    }
  });

  it("allows permission-filter omission but never authority without online complete integrity", () => {
    expect(() => decodeSnapshot({ available_actions: [] })).not.toThrow();
    expectAuthorityViolation(() => decodeSnapshot({
      data: data({ completeness: "partial" }),
    }));
  });

  it("explicitly rejects the historical Worker navigation hint", () => {
    expectAuthorityViolation(() => decodeSnapshot({
      related_refs: [{
        type: "worker_detail",
        id: "session-1",
        href: "/api/v1/views/worker_detail/session-1",
      }],
    }));
  });
});
