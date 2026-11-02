import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  ViewBoundary,
  isViewInteractionBlocked,
  isViewMutationEnabled,
  viewBoundaryConnection,
} from "./ViewBoundary";

describe("ViewBoundary", () => {
  it("renders a structural status skeleton before the initial snapshot", () => {
    render(
      <ViewBoundary
        connection="connecting"
        hasData={false}
        label="Worker detail"
        sync="empty"
      />,
    );

    const loading = screen.getByRole("status", { name: "Loading Worker detail" });
    expect(loading).toHaveAttribute("aria-busy", "true");
    expect(loading.querySelectorAll("[aria-hidden='true']")).toHaveLength(3);
  });

  it("renders an actionable error and calls refresh exactly once", () => {
    const refresh = vi.fn().mockResolvedValue(undefined);
    render(
      <ViewBoundary
        connection="connecting"
        error="Frontier snapshot is unavailable"
        hasData={false}
        label="Tasks"
        onRetry={refresh}
        sync="empty"
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Frontier snapshot is unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Retry Tasks" }));
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("keeps trusted content visible with a textual reconnecting status", () => {
    render(
      <ViewBoundary
        connection="reconnecting"
        hasData
        label="Assistant"
        sync="ready"
      >
        <button type="button">Trusted projected action</button>
      </ViewBoundary>,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Reconnecting to live Assistant updates");
    expect(screen.getByRole("button", { name: "Trusted projected action" })).toBeVisible();
  });

  it("does not trust a cached connected replica before its new controller is live", () => {
    expect(isViewMutationEnabled({
      connection: "connected",
      status: "reconnecting",
      sync: "ready",
    })).toBe(false);
    expect(viewBoundaryConnection({
      connection: "connected",
      hasData: true,
      status: "connecting",
    })).toBe("reconnecting");
    expect(isViewMutationEnabled({
      connection: "connected",
      status: "live",
      sync: "ready",
    })).toBe(true);
  });

  it("presents resync as blocking until ready and never dismisses client upgrade", () => {
    const { rerender } = render(
      <ViewBoundary
        connection="reconnecting"
        hasData
        label="Artifact detail"
        sync="resync_required"
      >
        <p>Trusted Artifact metadata</p>
      </ViewBoundary>,
    );

    expect(isViewInteractionBlocked("resync_required")).toBe(true);
    expect(screen.getByRole("status")).toHaveTextContent("Refreshing authoritative Artifact detail state");
    expect(screen.getByText("Trusted Artifact metadata")).toBeVisible();

    rerender(
      <ViewBoundary
        connection="connected"
        hasData
        label="Artifact detail"
        sync="client_upgrade_required"
      >
        <p>Trusted Artifact metadata</p>
      </ViewBoundary>,
    );
    expect(isViewInteractionBlocked("client_upgrade_required")).toBe(true);
    expect(screen.getByRole("alert")).toHaveTextContent("Client update required");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();

    rerender(
      <ViewBoundary
        connection="connected"
        hasData
        label="Artifact detail"
        sync="ready"
      >
        <p>Trusted Artifact metadata</p>
      </ViewBoundary>,
    );
    expect(isViewInteractionBlocked("ready")).toBe(false);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps retry and page controls in keyboard focus order with semantic text", () => {
    render(
      <ViewBoundary
        connection="reconnecting"
        error="Live updates are unavailable"
        hasData
        label="Worker detail"
        onRetry={vi.fn()}
        sync="ready"
      >
        <button type="button">First page action</button>
      </ViewBoundary>,
    );

    const retry = screen.getByRole("button", { name: "Retry Worker detail" });
    const pageAction = screen.getByRole("button", { name: "First page action" });
    expect(retry.compareDocumentPosition(pageAction) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByRole("alert")).toHaveTextContent("Live updates are unavailable");
  });
});
