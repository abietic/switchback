import {
  requireArray,
  requireBoolean,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireTimestamp,
  type ActionRef,
  type ArtifactDetailData,
  type ArtifactReviewManifest,
  type ArtifactReviewOutcome,
  type RelatedRef,
} from "../api/contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "../api/contractPrimitives";

export const artifactDownloadAuthorityRevision = 1;

export type ReadonlyArtifactDetailData = Omit<Readonly<ArtifactDetailData>, "review_manifest"> & {
  readonly review_manifest?: Readonly<Omit<ArtifactReviewManifest, "diagnostics">> & {
    readonly diagnostics: readonly Readonly<{ code: string; message: string }>[];
  };
};

export interface ArtifactProjectionAuthorityState {
  readonly data: ReadonlyArtifactDetailData;
  readonly availableActions: readonly Readonly<ActionRef>[];
  readonly relatedRefs: readonly Readonly<RelatedRef>[];
}

const downloadableOutcomes = new Set<ArtifactReviewOutcome>([
  "online",
  "empty",
  "binary",
  "oversized",
  "parse_failed",
]);

export function parseArtifactDetailData(
  value: unknown,
  expectedArtifactId: string,
): ArtifactDetailData {
  const artifact = requireRecord(value, "Artifact detail");
  const artifactId = canonicalIdentity(artifact.artifact_id, "Artifact identity");
  if (artifactId !== expectedArtifactId) throw new Error("Artifact detail identity is invalid");

  const type = closedString(artifact.type, ["report", "patch"] as const, "Artifact type");
  const availability = artifactOutcome(artifact.availability);
  const completeness = closedString(
    artifact.completeness,
    ["complete", "partial", "unverified"] as const,
    "Artifact completeness",
  );
  const manifest = parseManifest(artifact.review_manifest);
  const expectedMedia = type === "report" ? "text/markdown" : "text/x-diff";
  if (
    manifest.outcome !== availability
    || manifest.media_type !== expectedMedia
    || manifest.renderable !== (availability === "online")
    || manifest.download_allowed !== downloadableOutcomes.has(availability)
  ) {
    throw new Error("Artifact review authority is inconsistent");
  }

  const expectedDownloadUrl = `/api/v1/artifacts/${encodeURIComponent(artifactId)}/content`;
  const downloadUrl = optionalString(artifact.download_url, "Artifact download URL");
  if (manifest.download_allowed !== (downloadUrl === expectedDownloadUrl)) {
    throw new Error("Artifact download authority is inconsistent");
  }

  return {
    artifact_id: artifactId,
    type,
    label: requireString(artifact.label, "Artifact label"),
    size_bytes: nonNegativeSafeInteger(artifact.size_bytes, "Artifact size"),
    checksum: sha256Digest(artifact.checksum, "Artifact checksum"),
    completeness,
    worker_run_id: canonicalIdentity(artifact.worker_run_id, "Artifact WorkerRun identity"),
    agent_turn_id: canonicalIdentity(artifact.agent_turn_id, "Artifact AgentTurn identity"),
    runtime_generation: positiveSafeInteger(
      artifact.runtime_generation,
      "Artifact runtime generation",
    ),
    generated_at: requireTimestamp(artifact.generated_at, "Artifact generated_at"),
    availability,
    content: text(artifact.content, "Artifact content"),
    ...(downloadUrl === undefined ? {} : { download_url: downloadUrl }),
    review_manifest: manifest,
  };
}

export function validateArtifactProjectionAuthority(
  state: ArtifactProjectionAuthorityState,
): undefined {
  if (state.relatedRefs.length !== 0) {
    throw new Error("Artifact related-ref authority must be empty");
  }
  if (state.availableActions.length > 1) {
    throw new Error("Artifact action authority is duplicated");
  }
  for (const action of state.availableActions) {
    if (
      action.action !== "artifact.download"
      || action.target_id !== state.data.artifact_id
      || action.expected_revision !== artifactDownloadAuthorityRevision
      || Object.hasOwn(action, "metadata")
      || !artifactHasDownloadAuthority(state.data)
    ) {
      throw new Error("Artifact download action authority is invalid");
    }
  }
  return undefined;
}

export function isArtifactDownloadAuthorized(
  artifact: ReadonlyArtifactDetailData,
  actions: readonly Readonly<ActionRef>[],
): boolean {
  return artifactHasDownloadAuthority(artifact) && actions.some((action) => (
    action.action === "artifact.download"
    && action.target_id === artifact.artifact_id
    && action.expected_revision === artifactDownloadAuthorityRevision
    && !Object.hasOwn(action, "metadata")
  ));
}

function artifactHasDownloadAuthority(artifact: ReadonlyArtifactDetailData): boolean {
  const manifest = artifact.review_manifest;
  const expectedMedia = artifact.type === "report" ? "text/markdown" : "text/x-diff";
  return Boolean(
    artifact.completeness === "complete"
    && manifest?.schema_version === "artifact.review_manifest.v1"
    && manifest.outcome === artifact.availability
    && downloadableOutcomes.has(artifact.availability)
    && manifest.renderable === (artifact.availability === "online")
    && manifest.download_allowed
    && manifest.media_type === expectedMedia
    && Number.isSafeInteger(manifest.content_size_bytes)
    && manifest.content_size_bytes >= 0
    && Array.isArray(manifest.diagnostics)
    && artifact.download_url
      === `/api/v1/artifacts/${encodeURIComponent(artifact.artifact_id)}/content`
  );
}

function parseManifest(value: unknown): ArtifactReviewManifest {
  const manifest = requireRecord(value, "Artifact review manifest");
  if (manifest.schema_version !== "artifact.review_manifest.v1") {
    throw new Error("Artifact review manifest schema is invalid");
  }
  const diagnostics = requireArray(manifest.diagnostics, "Artifact diagnostics").map((value) => {
    const diagnostic = requireRecord(value, "Artifact diagnostic");
    return {
      code: canonicalIdentity(diagnostic.code, "Artifact diagnostic code"),
      message: requireString(diagnostic.message, "Artifact diagnostic message"),
    };
  });
  if (new Set(diagnostics.map((item) => item.code)).size !== diagnostics.length) {
    throw new Error("Artifact diagnostic code is duplicated");
  }
  return {
    schema_version: "artifact.review_manifest.v1",
    outcome: artifactOutcome(manifest.outcome),
    renderable: requireBoolean(manifest.renderable, "Artifact manifest renderable"),
    download_allowed: requireBoolean(
      manifest.download_allowed,
      "Artifact manifest download_allowed",
    ),
    media_type: requireString(manifest.media_type, "Artifact manifest media type"),
    content_size_bytes: nonNegativeSafeInteger(
      manifest.content_size_bytes,
      "Artifact manifest content size",
    ),
    diagnostics,
  };
}

function artifactOutcome(value: unknown): ArtifactReviewOutcome {
  return closedString(value, [
    "online",
    "empty",
    "binary",
    "oversized",
    "parse_failed",
    "missing",
    "checksum_failed",
  ] as const, "Artifact review outcome");
}

function canonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function sha256Digest(value: unknown, label: string): string {
  const digest = requireString(value, label);
  if (!/^sha256:[0-9a-f]{64}$/.test(digest)) throw new Error(`${label} is invalid`);
  return digest;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegativeSafeInteger(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function optionalString(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined;
  return requireString(value, label);
}

function text(value: unknown, label: string): string {
  if (typeof value !== "string") throw new Error(`${label} is invalid`);
  return value;
}

function closedString<const T extends string>(
  value: unknown,
  allowed: readonly T[],
  label: string,
): T {
  if (typeof value !== "string" || !allowed.includes(value as T)) {
    throw new Error(`${label} is invalid`);
  }
  return value as T;
}
