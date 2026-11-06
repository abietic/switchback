import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  ArtifactDetailData,
  ArtifactReviewOutcome,
  ArtifactSummary,
  ProjectionSnapshot,
} from "../../../api/contracts";
import { ThemeControls, ThemeProvider } from "../../../providers/ThemeProvider";
import { ArtifactWorkbench } from "./ArtifactWorkbench";

interface MockFileDiffProps {
  fileDiff?: {
    name?: string;
  };
  options?: {
    themeType?: "dark" | "light";
  };
}

const fileDiffRender = vi.hoisted(() => vi.fn<(props: MockFileDiffProps) => void>());

vi.mock("@pierre/diffs/react", () => ({
  FileDiff: (props: MockFileDiffProps) => {
    fileDiffRender(props);
    return null;
  },
}));

const artifact: ArtifactSummary = {
  artifact_id: "artifact-1", type: "report", label: "Stage report",
  size_bytes: 12, checksum: `sha256:${"a".repeat(64)}`,
  completeness: "complete", worker_run_id: "run-1", agent_turn_id: "turn-1",
  runtime_generation: 1, generated_at: "2026-07-25T00:00:00Z",
};

afterEach(() => {
  fileDiffRender.mockReset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  document.documentElement.classList.remove("dark");
  delete document.documentElement.dataset.theme;
  document.documentElement.style.colorScheme = "";
});

describe("ArtifactWorkbench", () => {
  it("shows an empty state instead of loading when no artifacts exist", () => {
    renderWorkbench({ artifacts: [], onRequestedArtifactHandled: vi.fn() });
    expect(screen.getByText("No artifact")).toBeInTheDocument();
    expect(screen.queryByText("Loading artifact…")).not.toBeInTheDocument();
  });

  it("shows the report when it arrives after an empty projection", async () => {
    mockArtifact({
      ...artifact, availability: "online", content: "# Arrived report",
      review_manifest: manifest("online", true, true),
    });
    const handled = vi.fn();
    const view = renderWorkbench({ artifacts: [], onRequestedArtifactHandled: handled });
    view.rerender(<ThemeProvider>
      <ArtifactWorkbench artifacts={[artifact]} onRequestedArtifactHandled={handled} />
    </ThemeProvider>);
    expect(await screen.findByRole("heading", { name: "Arrived report" })).toBeInTheDocument();
  });

  it("keeps a cached report visible after another artifact fails", async () => {
    const patch = { ...artifact, artifact_id: "patch-failed", type: "patch" as const };
    const detail: ArtifactDetailData = {
      ...artifact, availability: "online", content: "# Cached report",
      review_manifest: manifest("online", true, true),
    };
    vi.stubGlobal("fetch", vi.fn().mockImplementation((input: RequestInfo | URL) =>
      Promise.resolve(String(input).endsWith(artifact.artifact_id)
        ? new Response(JSON.stringify(artifactSnapshot(detail)), { status: 200 })
        : new Response("Unavailable", { status: 503 }))));
    renderWorkbench({ artifacts: [artifact, patch], onRequestedArtifactHandled: vi.fn() });
    await screen.findByRole("heading", { name: "Cached report" });

    fireEvent.click(screen.getByRole("button", { name: "Diff" }));
    await screen.findByText("Artifact unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Report" }));

    expect(await screen.findByRole("heading", { name: "Cached report" })).toBeInTheDocument();
    expect(screen.queryByText("Artifact unavailable")).not.toBeInTheDocument();
  });

  it("discards a late response from an aborted artifact request", async () => {
    const patch = { ...artifact, artifact_id: "patch-late", type: "patch" as const };
    const reportDetail: ArtifactDetailData = {
      ...artifact, availability: "online", content: "# Current report",
      review_manifest: manifest("online", true, true),
    };
    const lateDetail: ArtifactDetailData = {
      ...patch, availability: "parse_failed", content: "",
      review_manifest: manifest("parse_failed", false, true, "text/x-diff"),
    };
    const currentDetail: ArtifactDetailData = {
      ...patch, size_bytes: 0, availability: "empty", content: "",
      review_manifest: manifest("empty", false, true, "text/x-diff"),
    };
    const response = (detail: ArtifactDetailData) =>
      new Response(JSON.stringify(artifactSnapshot(detail)), { status: 200 });
    let completeAbortedRequest!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(response(reportDetail))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { completeAbortedRequest = resolve; }))
      .mockResolvedValueOnce(response(currentDetail)));
    renderWorkbench({ artifacts: [artifact, patch], onRequestedArtifactHandled: vi.fn() });
    await screen.findByRole("heading", { name: "Current report" });
    fireEvent.click(screen.getByRole("button", { name: "Diff" }));
    await screen.findByText("Loading artifact…");
    fireEvent.click(screen.getByRole("button", { name: "Report" }));
    await act(async () => { completeAbortedRequest(response(lateDetail)); });
    fireEvent.click(screen.getByRole("button", { name: "Diff" }));

    expect(await screen.findByText("No code changes.")).toBeInTheDocument();
    expect(screen.queryByText("Patch cannot be parsed")).not.toBeInTheDocument();
    expect(screen.queryByText("Loading artifact…")).not.toBeInTheDocument();
  });

  it("keeps the same artifact request alive across summary projection refreshes", async () => {
    const detail: ArtifactDetailData = {
      ...artifact, availability: "online", content: "# Refreshed report",
      review_manifest: manifest("online", true, true),
    };
    let completeRequest!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn()
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { completeRequest = resolve; }))
      .mockImplementation(() => new Promise<Response>(() => {})));
    const handled = vi.fn();
    const view = renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: handled });
    await screen.findByText("Loading artifact…");
    view.rerender(<ThemeProvider>
      <ArtifactWorkbench artifacts={[{ ...artifact }]} onRequestedArtifactHandled={handled} />
    </ThemeProvider>);
    await act(async () => {
      completeRequest(new Response(JSON.stringify(artifactSnapshot(detail)), { status: 200 }));
    });

    expect(await screen.findByRole("heading", { name: "Refreshed report" })).toBeInTheDocument();
  });

  it("renders a checksum-reviewed Markdown report with provenance", async () => {
    mockArtifact({
      ...artifact,
      availability: "online",
      content: "## Result\n**Done**",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", true, true),
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });
    expect(await screen.findByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Done")).toBeInTheDocument();
    expect(screen.getByText("run-1")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Download artifact" })).toHaveAttribute(
      "href", "/api/v1/artifacts/artifact-1/content",
    );
  });

  it("rejects a snapshot whose View identity does not match the requested Artifact", async () => {
    const detail: ArtifactDetailData = {
      ...artifact,
      availability: "online",
      content: "# Must not render",
      review_manifest: manifest("online", true, false),
    };
    mockArtifactSnapshot({
      ...artifactSnapshot(detail),
      view_id: "artifact-other",
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("rejects an Artifact detail without authoritative review metadata", async () => {
    mockArtifact({
      ...artifact,
      availability: "online",
      content: "# Must not render",
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
  });

  it("rejects an impossible online manifest that marks content non-renderable", async () => {
    mockArtifact({
      ...artifact,
      availability: "online",
      content: "# Must not render",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", false, true),
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("rejects non-contract ActionRef metadata before rendering", async () => {
    const detail: ArtifactDetailData = {
      ...artifact,
      availability: "online",
      content: "# Reviewed",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", true, true),
    };
    mockArtifactSnapshot({
      ...artifactSnapshot(detail),
      available_actions: [{
        action: "artifact.download",
        target_id: "artifact-1",
        expected_revision: 1,
        metadata: { download_url: "/api/v1/artifacts/artifact-1/content" },
      }],
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Reviewed" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("rejects a stale Artifact download revision before rendering actions", async () => {
    const detail: ArtifactDetailData = {
      ...artifact,
      availability: "online",
      content: "# Must not render",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", true, true),
    };
    mockArtifactSnapshot({
      ...artifactSnapshot(detail),
      available_actions: [{
        action: "artifact.download",
        target_id: "artifact-1",
        expected_revision: 2,
      }],
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("rejects inconsistent availability and review outcomes", async () => {
    mockArtifact({
      ...artifact,
      availability: "online",
      content: "# Must not render",
      review_manifest: manifest("parse_failed", false, false),
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
  });

  it("rejects a projected Artifact download action for the wrong target", async () => {
    const detail: ArtifactDetailData = {
      ...artifact,
      availability: "online",
      content: "# Reviewed",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", true, true),
    };
    mockArtifactSnapshot({
      ...artifactSnapshot(detail),
      available_actions: [{
        action: "artifact.download",
        target_id: "artifact-other",
        expected_revision: 1,
      }],
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Reviewed" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("rejects malformed or integrity-inconsistent Artifact authority", async () => {
    mockArtifactSnapshot({
      ...artifactSnapshot({
        ...artifact,
        availability: "checksum_failed",
        content: "# Forged content",
        download_url: "/api/v1/artifacts/artifact-1/content",
        review_manifest: manifest("checksum_failed", true, true),
      }),
      data: {
        ...artifact,
        size_bytes: "12",
        availability: "checksum_failed",
        content: "# Forged content",
        download_url: "/api/v1/artifacts/artifact-1/content",
        review_manifest: manifest("checksum_failed", true, true),
      },
    });
    renderWorkbench({ artifacts: [artifact], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByText("Artifact unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Forged content" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("defaults to the newest artifact and selects the exact historical artifact requested by Timeline", async () => {
    const oldArtifact = {
      ...artifact,
      artifact_id: "artifact-old",
      generated_at: "2026-07-25T10:00:00Z",
    };
    const newArtifact = {
      ...artifact,
      artifact_id: "artifact-new",
      worker_run_id: "run-new",
      agent_turn_id: "turn-new",
      generated_at: "2026-07-25T11:00:00Z",
    };
    mockArtifacts({
      "artifact-old": {
        ...oldArtifact, availability: "online", content: "# Old delivery",
        review_manifest: manifest("online", true, true),
      },
      "artifact-new": {
        ...newArtifact, availability: "online", content: "# New delivery",
        review_manifest: manifest("online", true, true),
      },
    });
    const handled = vi.fn();
    const view = renderWorkbench({
      artifacts: [oldArtifact, newArtifact],
      onRequestedArtifactHandled: handled,
    });
    expect(await screen.findByRole("heading", { name: "New delivery" })).toBeInTheDocument();

    view.rerender(
      <ThemeProvider>
        <ArtifactWorkbench
          artifacts={[oldArtifact, newArtifact]}
          requestedArtifact="artifact-old"
          onRequestedArtifactHandled={handled}
        />
      </ThemeProvider>,
    );
    expect(await screen.findByRole("heading", { name: "Old delivery" })).toBeInTheDocument();
    expect(handled).toHaveBeenCalled();
  });

  it("uses authoritative Artifact details for Report, Diff, and Files instead of summary labels", async () => {
    const report = { ...artifact, artifact_id: "report-authoritative", label: "Summary report label" };
    const patch = {
      ...artifact,
      artifact_id: "patch-authoritative",
      type: "patch" as const,
      label: "Summary patch label",
    };
    mockArtifacts({
      [report.artifact_id]: {
        ...report,
        availability: "online",
        content: "# Authoritative report body",
        review_manifest: manifest("online", true, true),
      },
      [patch.artifact_id]: {
        ...patch,
        availability: "online",
        content: `diff --git a/server-owned.ts b/server-owned.ts
index 1111111..2222222 100644
--- a/server-owned.ts
+++ b/server-owned.ts
@@ -1 +1 @@
-export const owner = "summary";
+export const owner = "artifact";
`,
        review_manifest: {
          ...manifest("online", true, true),
          media_type: "text/x-diff",
        },
      },
    });
    renderWorkbench({ artifacts: [report, patch], onRequestedArtifactHandled: vi.fn() });

    expect(await screen.findByRole("heading", { name: "Authoritative report body" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Diff" }));
    await screen.findByText("1 files");
    expect(fileDiffRender.mock.lastCall?.[0].fileDiff?.name).toBe("server-owned.ts");
    expect(screen.getByLabelText("Changed files")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Files" }));
    expect(screen.getByText("server-owned.ts")).toBeInTheDocument();
  });

  it("renders a verified zero-byte patch without an empty tree", async () => {
    const patch = { ...artifact, type: "patch" as const, label: "Patch review", size_bytes: 0 };
    mockArtifact({
      ...patch, availability: "empty", content: "",
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("empty", false, true, "text/x-diff"),
    });
    renderWorkbench({ artifacts: [patch], onRequestedArtifactHandled: vi.fn() });
    expect(await screen.findByText("No code changes.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Changed files")).not.toBeInTheDocument();
  });

  it("keeps long changed paths inspectable in the Files view", async () => {
    const longPath = "internal/workerruntime/application/continuation/reconciliation/worker_session_state.go";
    const patch = { ...artifact, type: "patch" as const, label: "Patch review" };
    mockArtifact({
      ...patch,
      availability: "online",
      content: `diff --git a/${longPath} b/${longPath}
index 1111111..2222222 100644
--- a/${longPath}
+++ b/${longPath}
@@ -1 +1 @@
-const oldValue = 1;
+const newValue = 2;
`,
      download_url: "/api/v1/artifacts/artifact-1/content",
      review_manifest: manifest("online", true, true, "text/x-diff"),
    });
    renderWorkbench({ artifacts: [patch], onRequestedArtifactHandled: vi.fn() });
    await screen.findByText("1 files");
    fireEvent.click(screen.getByRole("button", { name: "Files" }));
    expect(screen.getByText(longPath)).toBeInTheDocument();
  });

  it("renders FileDiff with the resolved explicit dark and light themes", async () => {
    const patch = mockRenderablePatch();
    renderThemeWorkbench(patch);

    await waitFor(() => expect(lastFileDiffTheme()).toBe("dark"));

    fireEvent.click(screen.getByRole("radio", { name: "浅色主题" }));
    await waitFor(() => expect(lastFileDiffTheme()).toBe("light"));

    fireEvent.click(screen.getByRole("radio", { name: "深色主题" }));
    await waitFor(() => expect(lastFileDiffTheme()).toBe("dark"));
  });

  it("rerenders FileDiff when a system-dark preference changes", async () => {
    const { listeners, mediaQuery } = installMediaQuery(true);
    const patch = mockRenderablePatch();
    renderThemeWorkbench(patch);
    fireEvent.click(screen.getByRole("radio", { name: "跟随系统主题" }));

    await waitFor(() => expect(lastFileDiffTheme()).toBe("dark"));
    const rendersBeforeChange = fileDiffRender.mock.calls.length;

    act(() => {
      mediaQuery.matches = false;
      for (const listener of listeners) {
        listener({ matches: false } as MediaQueryListEvent);
      }
    });
    await waitFor(() => {
      expect(fileDiffRender.mock.calls.length).toBeGreaterThan(rendersBeforeChange);
      expect(lastFileDiffTheme()).toBe("light");
    });
  });

  for (const [outcome, title, download] of [
    ["binary", "Binary artifact", true],
    ["oversized", "Artifact is too large to render", true],
    ["parse_failed", "Patch cannot be parsed", true],
    ["missing", "Artifact content missing", false],
    ["checksum_failed", "Checksum verification failed", false],
  ] as Array<[ArtifactReviewOutcome, string, boolean]>) {
    it(`gates ${outcome} rendering and download truthfully`, async () => {
      const patch = { ...artifact, type: "patch" as const, label: "Patch review" };
      mockArtifact({
        ...patch, availability: outcome, content: "",
        download_url: download ? "/api/v1/artifacts/artifact-1/content" : undefined,
        review_manifest: manifest(outcome, false, download, "text/x-diff"),
      });
      renderWorkbench({ artifacts: [patch], onRequestedArtifactHandled: vi.fn() });
      expect(await screen.findByText(title)).toBeInTheDocument();
      expect(screen.queryByLabelText("Changed files")).not.toBeInTheDocument();
      if (download) {
        expect(screen.getByRole("link", { name: "Download artifact" })).toBeInTheDocument();
      } else {
        expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
      }
    });
  }
});

function manifest(
  outcome: ArtifactReviewOutcome,
  renderable: boolean,
  downloadAllowed: boolean,
  mediaType = "text/markdown",
) {
  return {
    schema_version: "artifact.review_manifest.v1" as const,
    outcome, renderable, download_allowed: downloadAllowed,
    media_type: mediaType, content_size_bytes: 12, diagnostics: [],
  };
}

function mockArtifact(detail: ArtifactDetailData) {
  mockArtifacts({ [detail.artifact_id]: detail });
}

function mockArtifacts(details: Record<string, ArtifactDetailData>) {
  vi.stubGlobal("fetch", vi.fn().mockImplementation((input: RequestInfo | URL) => {
    const artifactID = decodeURIComponent(String(input).split("/").pop() ?? "");
    const detail = details[artifactID];
    const snapshot = artifactSnapshot(detail, artifactID);
    return Promise.resolve(new Response(JSON.stringify(snapshot), {
      status: detail ? 200 : 404, headers: { "Content-Type": "application/json" },
    }));
  }));
}

function mockArtifactSnapshot(snapshot: unknown) {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(snapshot), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })));
}

function artifactSnapshot(
  detail: ArtifactDetailData,
  artifactID = detail.artifact_id,
): ProjectionSnapshot<ArtifactDetailData> {
  const projectedDetail = detail.review_manifest?.download_allowed && !detail.download_url
    ? { ...detail, download_url: `/api/v1/artifacts/${encodeURIComponent(artifactID)}/content` }
    : detail;
  return {
    schema_version: "frontier.projection_snapshot.v1",
    tenant_id: "tenant-local", view_type: "artifact_detail", view_id: artifactID,
    view_epoch: 1, view_cursor: 1, projection_version: 1,
    query_fingerprint: "test", generated_at: "2026-07-25T00:00:00Z",
    data: projectedDetail,
    available_actions: projectedDetail.download_url && projectedDetail.review_manifest?.download_allowed ? [{
      action: "artifact.download",
      target_id: artifactID,
      expected_revision: 1,
    }] : [],
    related_refs: [],
  };
}

function installMediaQuery(matches: boolean) {
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const mediaQuery = {
    matches,
    addEventListener: vi.fn((type: string, listener: EventListenerOrEventListenerObject) => {
      if (type === "change" && typeof listener === "function") {
        listeners.add(listener as (event: MediaQueryListEvent) => void);
      }
    }),
    removeEventListener: vi.fn((type: string, listener: EventListenerOrEventListenerObject) => {
      if (type === "change" && typeof listener === "function") {
        listeners.delete(listener as (event: MediaQueryListEvent) => void);
      }
    }),
  };
  vi.stubGlobal("matchMedia", vi.fn(() => mediaQuery));
  return { listeners, mediaQuery };
}

function mockRenderablePatch() {
  const patch = { ...artifact, type: "patch" as const, label: "Patch review" };
  mockArtifact({
    ...patch,
    availability: "online",
    content: `diff --git a/example.ts b/example.ts
index 1111111..2222222 100644
--- a/example.ts
+++ b/example.ts
@@ -1 +1 @@
-const oldValue = 1;
+const newValue = 2;
`,
    review_manifest: manifest("online", true, true, "text/x-diff"),
  });
  return patch;
}

function renderWorkbench(props: ComponentProps<typeof ArtifactWorkbench>) {
  return render(
    <ThemeProvider>
      <ArtifactWorkbench {...props} />
    </ThemeProvider>,
  );
}

function renderThemeWorkbench(patch: ArtifactSummary) {
  return render(
    <ThemeProvider>
      <ThemeControls />
      <ArtifactWorkbench artifacts={[patch]} onRequestedArtifactHandled={vi.fn()} />
    </ThemeProvider>,
  );
}

function lastFileDiffTheme() {
  return fileDiffRender.mock.lastCall?.[0].options?.themeType;
}
