import {
  AlertCircle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Code2,
  Download,
  FileText,
  FolderTree,
  LoaderCircle,
  WrapText,
} from "lucide-react";
import { FileDiff } from "@pierre/diffs/react";
import { FileTree as PierreFileTree } from "@pierre/trees/react";
import { useEffect, useMemo, useState } from "react";
import type { ArtifactSummary } from "../../../api/contracts";
import { SafeMarkdown } from "../../../components/SafeMarkdown";
import {
  isArtifactDownloadAuthorized,
  type ReadonlyArtifactDetailData,
} from "../../../projections/artifactAuthority";
import { loadArtifactPreview, type ArtifactPreview } from "../../../projections/artifactPreview";
import { productionViews } from "../../../projections/productionViewRegistry";
import { useTheme, type ResolvedTheme } from "../../../providers/ThemeProvider";
import { createArtifactTree } from "./ArtifactTreeAdapter";
import { parsePatchReview, type PatchReviewModel } from "./PatchReviewAdapter";

type WorkbenchTab = "report" | "diff" | "files";

export function ArtifactWorkbench({
  artifacts,
  requestedArtifact,
  onRequestedArtifactHandled,
  selectedArtifactId,
  onArtifactSelectionChange,
}: {
  artifacts: ArtifactSummary[];
  requestedArtifact?: string;
  onRequestedArtifactHandled: () => void;
  selectedArtifactId?: string;
  onArtifactSelectionChange?: (artifactId: string) => void;
}) {
  const { resolvedTheme } = useTheme();
  const [selectedArtifactIDs, setSelectedArtifactIDs] = useState<Partial<Record<ArtifactSummary["type"], string>>>({});
  const report = artifactForType(artifacts, "report", selectedArtifactIDs.report);
  const patch = artifactForType(artifacts, "patch", selectedArtifactIDs.patch);
  const [tab, setTab] = useState<WorkbenchTab>(report ? "report" : "diff");
  const [snapshots, setSnapshots] = useState<Record<string, ArtifactPreview>>({});
  const [error, setError] = useState<{ artifactID: string; message: string }>();
  const explicit = selectedArtifactId ? artifacts.find((item) => item.artifact_id === selectedArtifactId) : undefined;
  const selectedTab = selectedArtifactId ? explicit?.type === "report" ? "report" : tab === "files" ? "files" : "diff" : tab === "report" && !report && patch ? "diff"
    : tab !== "report" && !patch && report ? "report" : tab;
  const artifact = selectedArtifactId ? explicit : selectedTab === "report" ? report : patch;
  const artifactID = artifact?.artifact_id;
  const selectTab = (next: WorkbenchTab) => {
    const selected = next === "report" ? (explicit?.type === "report" ? explicit : report) : (explicit?.type === "patch" ? explicit : patch);
    setTab(next);
    if (selected) {
      setSelectedArtifactIDs((current) => ({ ...current, [selected.type]: selected.artifact_id }));
      onArtifactSelectionChange?.(selected.artifact_id);
    }
  };

  useEffect(() => {
    if (!requestedArtifact) return;
    const selected = artifacts.find((candidate) => candidate.artifact_id === requestedArtifact);
    if (selected) {
      setSelectedArtifactIDs((current) => ({ ...current, [selected.type]: selected.artifact_id }));
      setTab(selected.type === "report" ? "report" : "diff");
    }
    onRequestedArtifactHandled();
  }, [artifacts, onRequestedArtifactHandled, requestedArtifact]);

  useEffect(() => {
    if (!artifactID || snapshots[artifactID]) return;
    const controller = new AbortController();
    setError(undefined);
    void loadArtifactPreview(productionViews.artifact(artifactID), {
      signal: controller.signal,
    }).then((outcome) => {
      if (controller.signal.aborted) return;
      if (outcome.kind !== "available") throw new Error("Artifact review is unavailable.");
      setSnapshots((current) => ({ ...current, [artifactID]: outcome.preview }));
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return;
      setError({ artifactID, message: cause instanceof Error ? cause.message : "Artifact review is unavailable." });
    });
    return () => controller.abort();
  }, [artifactID, snapshots]);

  const selectedSnapshot = artifact ? snapshots[artifact.artifact_id] : undefined;
  const selected = selectedSnapshot?.data;
  const downloadURL = selectedSnapshot
    && isArtifactDownloadAuthorized(selectedSnapshot.data, selectedSnapshot.availableActions)
    ? selectedSnapshot.data.download_url
    : undefined;
  return <section className="artifact-workbench" aria-label="Artifact workbench">
    <header className="workbench-header">
      <div><strong>Workbench</strong><span>{artifacts.length} artifacts</span></div>
      {downloadURL ?
        <a className="icon-button" href={downloadURL} download title="Download artifact" aria-label="Download artifact">
          <Download size={16} />
        </a> : null}
    </header>
    <nav className="workbench-tabs" aria-label="Artifact views">
      <Tab selected={selectedTab === "report"} disabled={!report} onClick={() => selectTab("report")} icon={<FileText size={15} />} label="Report" />
      <Tab selected={selectedTab === "diff"} disabled={!patch} onClick={() => selectTab("diff")} icon={<Code2 size={15} />} label="Diff" />
      <Tab selected={selectedTab === "files"} disabled={!patch} onClick={() => selectTab("files")} icon={<FolderTree size={15} />} label="Files" />
    </nav>
    {artifact ? <select aria-label="Selected artifact" value={artifact.artifact_id} onChange={(event) => {
      const selected = artifacts.find((item) => item.artifact_id === event.target.value);
      if (!selected) return;
      setSelectedArtifactIDs((current) => ({ ...current, [selected.type]: selected.artifact_id }));
      onArtifactSelectionChange?.(selected.artifact_id);
    }}>{artifacts.filter((item) => item.type === artifact.type).map((item) => <option key={item.artifact_id} value={item.artifact_id}>{item.label} · {item.agent_turn_id}</option>)}</select> : null}
    <div className="workbench-body">
      {!artifact ? selectedArtifactId ? <WorkbenchMessage tone="danger" title="Artifact unavailable" message="The selected Artifact is no longer in the canonical Worker summary." /> : <WorkbenchMessage title="No artifact" message="This stage has not published this artifact type." /> :
        error?.artifactID === artifact.artifact_id ? <WorkbenchMessage tone="danger" title="Artifact unavailable" message={error.message} /> :
          !selected ? <WorkbenchLoading /> :
            <ArtifactSurface tab={selectedTab} artifact={selected} resolvedTheme={resolvedTheme} />}
    </div>
  </section>;
}

function artifactForType(
  artifacts: ArtifactSummary[],
  type: ArtifactSummary["type"],
  selectedID?: string,
): ArtifactSummary | undefined {
  const selected = artifacts.find((artifact) => artifact.type === type && artifact.artifact_id === selectedID);
  if (selected) return selected;
  return artifacts.reduce<ArtifactSummary | undefined>((latest, artifact) => {
    if (artifact.type !== type) return latest;
    if (!latest) return artifact;
    const latestAt = latest.generated_at ? Date.parse(latest.generated_at) : 0;
    const artifactAt = artifact.generated_at ? Date.parse(artifact.generated_at) : 0;
    return artifactAt >= latestAt ? artifact : latest;
  }, undefined);
}

function ArtifactSurface({
  tab,
  artifact,
  resolvedTheme,
}: {
  tab: WorkbenchTab;
  artifact: ReadonlyArtifactDetailData;
  resolvedTheme: ResolvedTheme;
}) {
  const manifest = artifact.review_manifest;
  if (!manifest) {
    return <WorkbenchMessage tone="danger" title="Artifact unavailable" message="Authoritative review metadata is missing." />;
  }
  if (manifest.outcome === "empty") {
    return artifact.type === "patch"
      ? <div className="empty-patch"><CheckCircle2 size={24} /><strong>No code changes.</strong><p>The Worker published a verified zero-byte patch.</p></div>
      : <WorkbenchFallback artifact={artifact} />;
  }
  if (manifest.outcome !== "online" || !manifest.renderable) {
    return <WorkbenchFallback artifact={artifact} />;
  }
  if (artifact.type === "report") {
    return <div className="report-surface">
      <ArtifactProvenance artifact={artifact} />
      {manifest.diagnostics.map((diagnostic) =>
        <WorkbenchMessage key={diagnostic.code} tone="warning" title="Runtime diagnostic" message={diagnostic.message} />)}
      <SafeMarkdown className="report-markdown">{artifact.content}</SafeMarkdown>
    </div>;
  }
  return <PatchSurface content={artifact.content} tab={tab} artifact={artifact} resolvedTheme={resolvedTheme} />;
}

function PatchSurface({
  content,
  tab,
  artifact,
  resolvedTheme,
}: {
  content: string;
  tab: WorkbenchTab;
  artifact: ReadonlyArtifactDetailData;
  resolvedTheme: ResolvedTheme;
}) {
  const review = useMemo(() => {
    try {
      return parsePatchReview(content);
    } catch {
      return undefined;
    }
  }, [content]);
  if (!review) {
    return <WorkbenchMessage tone="warning" title="Patch cannot be rendered" message="The verified Artifact is available to download, but its diff format could not be parsed." />;
  }
  return <ParsedPatchSurface review={review} tab={tab} artifact={artifact} resolvedTheme={resolvedTheme} />;
}

function ParsedPatchSurface({ review, tab, artifact, resolvedTheme }: {
  review: PatchReviewModel;
  tab: WorkbenchTab;
  artifact: ReadonlyArtifactDetailData;
  resolvedTheme: ResolvedTheme;
}) {
  const [selectedPath, setSelectedPath] = useState(review.files[0]?.path);
  const [layout, setLayout] = useState<"unified" | "split">("unified");
  const [wrap, setWrap] = useState(true);
  const selected = review.files.find((file) => file.path === selectedPath) ?? review.files[0];
  const [hunkIndex, setHunkIndex] = useState(0);
  useEffect(() => setHunkIndex(0), [selectedPath]);
  const tree = useMemo(
    () => createArtifactTree(review.files, selectedPath, setSelectedPath),
    [artifact.artifact_id, review.files, selectedPath],
  );
  useEffect(() => () => tree.cleanUp(), [tree]);
  const hunk = selected?.diff.hunks[hunkIndex];
  const selectedLines = hunk ? {
    start: Math.max(1, hunk.additionStart),
    end: Math.max(1, hunk.additionStart + Math.max(0, hunk.additionCount - 1)),
    side: "additions" as const,
  } : null;

  return <div className={`patch-surface patch-tab-${tab}`}>
    <ArtifactProvenance artifact={artifact} />
    <div className="patch-summary">
      <span>{review.files.length} files</span><span className="stat-add">+{review.additions}</span><span className="stat-del">-{review.deletions}</span>
      <span>{review.hunkCount} hunks</span>
    </div>
    <div className="patch-review-grid">
      <aside className="file-tree-pane">
        <PierreFileTree model={tree} aria-label="Changed files" />
      </aside>
      <section aria-label="Patch diff" className="diff-pane">
        {tab === "files" ? <FileInventory review={review} selectedPath={selected.path} onSelect={setSelectedPath} /> : <>
          <div className="diff-toolbar">
            <div className="segmented-control" aria-label="Diff layout">
              <button className={layout === "unified" ? "selected" : ""} onClick={() => setLayout("unified")}>Unified</button>
              <button className={layout === "split" ? "selected" : ""} onClick={() => setLayout("split")}>Split</button>
            </div>
            <button className={`icon-button ${wrap ? "selected" : ""}`} onClick={() => setWrap((value) => !value)} title="Toggle line wrapping" aria-label="Toggle line wrapping"><WrapText size={16} /></button>
            <div className="hunk-nav">
              <button className="icon-button" disabled={hunkIndex === 0} onClick={() => setHunkIndex((value) => Math.max(0, value - 1))} title="Previous hunk" aria-label="Previous hunk"><ChevronLeft size={15} /></button>
              <span>Hunk {selected.diff.hunks.length ? hunkIndex + 1 : 0}/{selected.diff.hunks.length}</span>
              <button className="icon-button" disabled={hunkIndex >= selected.diff.hunks.length - 1} onClick={() => setHunkIndex((value) => Math.min(selected.diff.hunks.length - 1, value + 1))} title="Next hunk" aria-label="Next hunk"><ChevronRight size={15} /></button>
            </div>
          </div>
          <FileDiff
            key={`${selected.id}-${layout}-${wrap}-${resolvedTheme}`}
            fileDiff={selected.diff}
            disableWorkerPool
            selectedLines={selectedLines}
            options={{
              diffStyle: layout,
              overflow: wrap ? "wrap" : "scroll",
              hunkSeparators: "line-info",
              lineDiffType: "word",
              themeType: resolvedTheme,
              stickyHeader: true,
            }}
          />
        </>}
      </section>
    </div>
  </div>;
}

function FileInventory({ review, selectedPath, onSelect }: { review: PatchReviewModel; selectedPath: string; onSelect: (path: string) => void }) {
  return <div className="file-inventory">{review.files.map((file) =>
    <button key={file.id} className={file.path === selectedPath ? "selected" : ""} onClick={() => onSelect(file.path)}>
      <span><strong>{file.path}</strong><small>{file.status}{file.previousPath ? ` from ${file.previousPath}` : ""}</small></span>
      <span><i>+{file.additions}</i><b>-{file.deletions}</b></span>
    </button>)}</div>;
}

function ArtifactProvenance({ artifact }: { artifact: ReadonlyArtifactDetailData }) {
  return <div className="workbench-provenance">
    <span>{artifact.worker_run_id}</span><span>{artifact.agent_turn_id}</span>
    {artifact.generated_at ? <time>{new Date(artifact.generated_at).toLocaleString()}</time> : null}
    <span title={artifact.checksum}>{artifact.checksum}</span>
  </div>;
}

function WorkbenchFallback({ artifact }: { artifact: ReadonlyArtifactDetailData }) {
  const outcome = artifact.review_manifest?.outcome ?? artifact.availability;
  const messages: Record<string, [string, string]> = {
    binary: ["Binary artifact", "Verified binary content cannot be rendered in the browser."],
    oversized: ["Artifact is too large to render", "The checksum is verified; download the Artifact for local review."],
    parse_failed: ["Patch cannot be parsed", "The checksum is verified, but this content is not a supported unified diff."],
    missing: ["Artifact content missing", "Metadata and provenance remain available, but the stored object cannot be read."],
    checksum_failed: ["Checksum verification failed", "Rendering and download are disabled because the stored bytes do not match the ArtifactRef."],
  };
  const [title, message] = messages[outcome] ?? ["Artifact cannot be rendered", "Review metadata and diagnostics for details."];
  return <><ArtifactProvenance artifact={artifact} /><WorkbenchMessage tone={outcome === "checksum_failed" ? "danger" : "warning"} title={title} message={message} /></>;
}

function WorkbenchMessage({ tone = "neutral", title, message }: { tone?: "neutral" | "warning" | "danger"; title: string; message: string }) {
  return <div className={`workbench-message workbench-${tone}`} role={tone === "danger" ? "alert" : "status"}>
    <AlertCircle size={18} /><div><strong>{title}</strong><p>{message}</p></div>
  </div>;
}

function WorkbenchLoading() {
  return <div className="workbench-loading" role="status"><LoaderCircle className="spin" size={18} /> Loading artifact…</div>;
}

function Tab({ selected, disabled, onClick, icon, label }: { selected: boolean; disabled: boolean; onClick: () => void; icon: React.ReactNode; label: string }) {
  return <button className={selected ? "selected" : ""} disabled={disabled} onClick={onClick}>{icon}<span>{label}</span></button>;
}
