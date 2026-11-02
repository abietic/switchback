import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ActionRef, ArtifactDetailData, ArtifactReviewOutcome } from "../../api/contracts";
import { demoArtifacts } from "../../demo/data";
import { ArtifactDetailPage } from "./ArtifactDetailPage";

const artifactID = "artifact-report-01";

function artifact(overrides: Partial<ArtifactDetailData> = {}): ArtifactDetailData {
  return {
    ...demoArtifacts[artifactID],
    runtime_generation: 7,
    ...overrides,
  };
}

function downloadAction(
  targetID = artifactID,
  expectedRevision = 1,
  metadata?: Record<string, unknown>,
): ActionRef {
  return {
    action: "artifact.download",
    target_id: targetID,
    expected_revision: expectedRevision,
    ...(metadata === undefined ? {} : { metadata }),
  };
}

function manifest(
  outcome: ArtifactReviewOutcome,
  renderable: boolean,
  downloadAllowed: boolean,
  mediaType = "text/markdown",
) {
  return {
    schema_version: "artifact.review_manifest.v1" as const,
    outcome,
    renderable,
    download_allowed: downloadAllowed,
    media_type: mediaType,
    content_size_bytes: 131,
    diagnostics: [],
  };
}

describe("ArtifactDetailPage", () => {
  it("renders an online report with complete authoritative provenance and exact download permission", () => {
    render(
      <ArtifactDetailPage
        artifact={artifact()}
        availableActions={[downloadAction()]}
      />,
    );

    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Checksum verified")).toBeInTheDocument();
    expect(screen.queryByText("Integrity unavailable")).not.toBeInTheDocument();
    const provenance = screen.getByLabelText("Artifact provenance");
    expect(provenance).toHaveTextContent("Run");
    expect(provenance).toHaveTextContent("run-03");
    expect(provenance).toHaveTextContent("Turn");
    expect(provenance).toHaveTextContent("turn-05");
    expect(provenance).toHaveTextContent("Generation");
    expect(provenance).toHaveTextContent("7");
    expect(provenance).toHaveTextContent("Digest");
    expect(provenance).toHaveTextContent("sha256:6d143c9f2ac4…f920");
    expect(provenance).toHaveTextContent("Size");
    expect(provenance).toHaveTextContent("1.8 KB");
    expect(provenance).toHaveTextContent("Completeness");
    expect(provenance).toHaveTextContent("Complete");
    expect(screen.getByRole("link", { name: "Download artifact" })).toHaveAttribute(
      "href",
      "/api/v1/artifacts/artifact-report-01/content",
    );
  });

  it("renders a patch inside the page's single bounded horizontal content viewport", () => {
    render(
      <ArtifactDetailPage
        artifact={{ ...demoArtifacts["artifact-patch-01"], runtime_generation: 4 }}
        availableActions={[downloadAction("artifact-patch-01")]}
      />,
    );

    const viewport = screen.getByTestId("artifact-content-viewport");
    expect(viewport).toHaveClass("artifact-content-viewport");
    expect(within(viewport).getByLabelText("Patch diff")).toBeInTheDocument();
    expect(screen.getAllByTestId("artifact-content-viewport")).toHaveLength(1);
    expect(screen.getByText("Review only · not applied")).toBeInTheDocument();
  });

  it("requires both manifest authority and the exact projected download action", () => {
    const { rerender } = render(
      <ArtifactDetailPage
        artifact={artifact()}
        availableActions={[downloadAction("artifact-other")]}
      />,
    );
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();

    rerender(
      <ArtifactDetailPage
        artifact={artifact()}
        availableActions={[downloadAction(artifactID, 2)]}
      />,
    );
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();

    rerender(
      <ArtifactDetailPage
        artifact={artifact()}
        availableActions={[downloadAction(artifactID, 1, { download_url: "forged" })]}
      />,
    );
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();

    rerender(
      <ArtifactDetailPage
        artifact={artifact({
          review_manifest: manifest("online", true, false),
        })}
        availableActions={[downloadAction()]}
      />,
    );
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();

    rerender(
      <ArtifactDetailPage
        artifact={artifact({
          content: "# Must not render",
          review_manifest: manifest("online", false, true),
        })}
        availableActions={[downloadAction()]}
      />,
    );
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it("shows checksum failure diagnostics and blocks rendering and download", () => {
    render(
      <ArtifactDetailPage
        artifact={artifact({
          availability: "checksum_failed",
          content: "# Must not render",
          review_manifest: manifest("checksum_failed", false, false),
        })}
        availableActions={[downloadAction()]}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Checksum verification failed");
    expect(screen.queryByText("Checksum verified")).not.toBeInTheDocument();
    expect(screen.getByText("Integrity unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
  });

  it.each([
    ["missing", "Artifact content missing"],
    ["oversized", "Artifact is too large to render"],
    ["binary", "Binary artifact"],
  ] as const)("blocks %s content and actions while retaining provenance", (outcome, warning) => {
    render(
      <ArtifactDetailPage
        artifact={artifact({
          availability: outcome,
          content: "# Must not render",
          review_manifest: manifest(outcome, false, true),
        })}
        availableActions={[downloadAction()]}
      />,
    );

    expect(screen.getByText(warning)).toBeInTheDocument();
    expect(screen.queryByText("Checksum verified")).not.toBeInTheDocument();
    expect(screen.getByText("Integrity unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    if (outcome === "oversized" || outcome === "binary") {
      expect(screen.getByRole("link", { name: "Download artifact" })).toBeInTheDocument();
    } else {
      expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
    }
    expect(screen.getByLabelText("Artifact provenance")).toHaveTextContent("run-03");
  });

  it.each([
    ["missing manifest", { review_manifest: undefined }],
    ["manifest render and download denied", { review_manifest: manifest("online", false, false) }],
    ["incomplete integrity", { completeness: "unverified" as const }],
    ["unknown outcome", { availability: "future_outcome" as ArtifactDetailData["availability"] }],
  ])("fails closed for %s", (_label, overrides) => {
    render(
      <ArtifactDetailPage
        artifact={artifact({
          content: "# Must not render",
          ...overrides,
        })}
        availableActions={[downloadAction()]}
      />,
    );

    expect(screen.queryByRole("heading", { name: "Must not render" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download artifact" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Artifact content"))
      .toHaveTextContent(/Artifact (?:unavailable|cannot be rendered)|review metadata/i);
  });

  it("does not invent reverse Worker navigation from Artifact authority", () => {
    render(
      <ArtifactDetailPage
        artifact={artifact()}
        availableActions={[]}
      />,
    );
    expect(screen.queryByRole("button", { name: /Return to Worker/ })).not.toBeInTheDocument();
  });
});
