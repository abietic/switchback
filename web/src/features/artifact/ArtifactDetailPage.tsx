import { CircleAlert, Download, FileDiff, FileText, ShieldCheck } from "lucide-react";
import type { ActionRef, ArtifactDetailData, StatusTone } from "../../api/contracts";
import { SafeMarkdown } from "../../components/SafeMarkdown";
import { StatusBadge } from "../../components/ui/StatusBadge";
import {
  isArtifactDownloadAuthorized,
} from "../../projections/artifactAuthority";

interface ArtifactDetailPageProps {
  actionStatus?: string;
  artifact: ArtifactDetailData;
  availableActions?: ActionRef[];
}

const knownOutcomes = new Set<ArtifactDetailData["availability"]>([
  "online",
  "empty",
  "binary",
  "oversized",
  "parse_failed",
  "missing",
  "checksum_failed",
]);

export function ArtifactDetailPage({
  actionStatus,
  artifact,
  availableActions = [],
}: ArtifactDetailPageProps) {
  const manifest = authoritativeManifest(artifact);
  const integrityComplete = artifact.completeness === "complete";
  const onlineAuthority = Boolean(
    manifest
    && integrityComplete
    && artifact.availability === "online",
  );
  const renderable = Boolean(
    onlineAuthority
    && manifest?.renderable
    && manifest.media_type === expectedMediaType(artifact),
  );
  const downloadAllowed = isArtifactDownloadAuthorized(artifact, availableActions);
  const failure = failureReason(artifact, manifest, renderable);

  return (
    <main className="artifact-detail">
      <div className="artifact-toolbar">
        <span aria-hidden="true" />
        <div>
          <IntegrityLabel verified={onlineAuthority} />
          {downloadAllowed ? (
            <a
              aria-label="Download artifact"
              className="icon-button"
              download
              href={artifact.download_url}
              title="Download artifact"
            >
              <Download size={17} />
            </a>
          ) : null}
        </div>
      </div>
      {actionStatus ? <p role="status">{actionStatus}</p> : null}
      <header className="artifact-heading">
        <span className="artifact-large-icon">
          {artifact.type === "patch" ? <FileDiff size={22} /> : <FileText size={22} />}
        </span>
        <div>
          <h2>{artifact.label}</h2>
          <ArtifactProvenance artifact={artifact} />
        </div>
        <StatusBadge
          label={`Completeness: ${humanStatus(artifact.completeness)}`}
          tone={completenessTone(artifact.completeness)}
        />
      </header>
      <section
        aria-label="Artifact content"
        className="artifact-content-viewport"
        data-testid="artifact-content-viewport"
      >
        {failure ? <IntegrityFailure reason={failure} /> : artifact.type === "patch" ? (
          <>
            <div className="artifact-notice">
              <FileDiff size={15} /> Review only · not applied
            </div>
            <PatchViewer content={artifact.content} />
          </>
        ) : (
          <article className="report-view">
            <SafeMarkdown>{artifact.content}</SafeMarkdown>
          </article>
        )}
      </section>
    </main>
  );
}

function authoritativeManifest(artifact: ArtifactDetailData) {
  const manifest = artifact.review_manifest;
  if (
    !manifest
    || manifest.schema_version !== "artifact.review_manifest.v1"
    || !knownOutcomes.has(artifact.availability)
    || !knownOutcomes.has(manifest.outcome)
    || manifest.outcome !== artifact.availability
    || typeof manifest.renderable !== "boolean"
    || typeof manifest.download_allowed !== "boolean"
    || typeof manifest.media_type !== "string"
    || !Number.isInteger(manifest.content_size_bytes)
    || manifest.content_size_bytes < 0
    || !Array.isArray(manifest.diagnostics)
  ) {
    return undefined;
  }
  return manifest;
}

type FailureReason =
  | ArtifactDetailData["availability"]
  | "authority_missing"
  | "integrity_incomplete"
  | "render_disabled";

function failureReason(
  artifact: ArtifactDetailData,
  manifest: ReturnType<typeof authoritativeManifest>,
  renderable: boolean,
): FailureReason | undefined {
  if (!manifest) return "authority_missing";
  if (artifact.completeness !== "complete") return "integrity_incomplete";
  if (artifact.availability !== "online") return artifact.availability;
  if (!renderable) return "render_disabled";
  return undefined;
}

function IntegrityLabel({ verified }: { verified: boolean }) {
  return verified ? (
    <span role="status"><ShieldCheck aria-hidden="true" size={15} /> Checksum verified</span>
  ) : (
    <span className="integrity-failed" role="status"><CircleAlert aria-hidden="true" size={15} /> Integrity unavailable</span>
  );
}

function ArtifactProvenance({ artifact }: { artifact: ArtifactDetailData }) {
  return (
    <dl aria-label="Artifact provenance" className="artifact-provenance">
      <div><dt>Run</dt><dd>{artifact.worker_run_id}</dd></div>
      <div><dt>Turn</dt><dd>{artifact.agent_turn_id}</dd></div>
      <div><dt>Generation</dt><dd>{artifact.runtime_generation ?? "Not projected"}</dd></div>
      <div><dt>Digest</dt><dd>{artifact.checksum}</dd></div>
      <div><dt>Size</dt><dd>{formatBytes(artifact.size_bytes)}</dd></div>
      <div><dt>Completeness</dt><dd>{humanStatus(artifact.completeness)}</dd></div>
    </dl>
  );
}

function IntegrityFailure({ reason }: { reason: FailureReason }) {
  const messages: Record<FailureReason, [string, string]> = {
    online: ["Artifact cannot be rendered", "The authoritative review manifest does not permit browser rendering."],
    empty: ["No content", "The checksum-verified Artifact is empty."],
    binary: ["Binary artifact", "Verified binary content cannot be rendered or downloaded from this review surface."],
    oversized: ["Artifact is too large to render", "Content actions are disabled for the oversized Artifact."],
    parse_failed: ["Patch cannot be parsed", "The checksum is verified, but this content is not a supported unified diff."],
    missing: ["Artifact content missing", "Metadata and provenance remain available, but the stored object cannot be read."],
    checksum_failed: ["Checksum verification failed", "Content actions are disabled because the stored bytes do not match the ArtifactRef."],
    authority_missing: ["Artifact unavailable", "Authoritative review metadata is missing or invalid."],
    integrity_incomplete: ["Artifact cannot be rendered", "Content actions require complete authoritative integrity evidence."],
    render_disabled: ["Artifact cannot be rendered", "The authoritative review manifest does not permit browser rendering."],
  };
  const [title, message] = messages[reason];
  return (
    <section
      className="artifact-integrity-error"
      role={reason === "checksum_failed" || reason === "authority_missing" ? "alert" : "status"}
    >
      <CircleAlert size={20} />
      <div><strong>{title}</strong><p>{message}</p></div>
    </section>
  );
}

function PatchViewer({ content }: { content: string }) {
  return (
    <div aria-label="Patch diff" className="diff-view">
      {content.split("\n").map((line, index) => {
        const type = line.startsWith("+") && !line.startsWith("+++")
          ? "add"
          : line.startsWith("-") && !line.startsWith("---")
            ? "remove"
            : line.startsWith("@@")
              ? "hunk"
              : "context";
        return (
          <div className={`diff-line diff-${type}`} key={`${index}-${line}`}>
            <span>{index + 1}</span><code>{line || " "}</code>
          </div>
        );
      })}
    </div>
  );
}

function expectedMediaType(artifact: ArtifactDetailData): string {
  return artifact.type === "patch" ? "text/x-diff" : "text/markdown";
}

function completenessTone(completeness: ArtifactDetailData["completeness"]): StatusTone {
  if (completeness === "complete") return "success";
  if (completeness === "partial") return "waiting";
  return "danger";
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function humanStatus(value: string): string {
  return value
    .split("_")
    .map((part) => part ? part[0].toUpperCase() + part.slice(1) : part)
    .join(" ");
}
