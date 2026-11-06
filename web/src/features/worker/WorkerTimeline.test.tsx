import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TransientDisplayDelta, WorkerTimelineTurn } from "../../api/contracts";
import { WorkerTimeline } from "./WorkerTimeline";

afterEach(() => vi.unstubAllGlobals());

describe("WorkerTimeline", () => {
  it.each([false, true])("selects canonical older Turn refs and rejects malformed history (%s)", async (malformed) => {
    const old: WorkerTimelineTurn = { worker_run_id: "old-run", agent_turn_id: malformed ? "" : "old-turn", display_stream_id: "display-old", status: "completed", started_at: "2026-07-25T00:00:00Z", terminal_at: "2026-07-25T00:01:00Z", prompt: "Older prompt", checkpoint_version: 1, checkpoint_complete: true, backfill_status: "online", parts: [] };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ turns: [old], has_more: false }) }));
    const select = vi.fn();
    render(<WorkerTimeline workerSessionId="session-1" initialGroups={[]} initialCursor="cursor" initialHasMore transient={[]} onArtifact={vi.fn()} onDiagnosticTarget={select} />);
    fireEvent.click(screen.getByRole("button", { name: "Load older activity" }));
    if (malformed) { await screen.findByRole("alert"); expect(screen.queryByText("Older prompt")).not.toBeInTheDocument(); }
    else { fireEvent.click(await screen.findByRole("button", { name: "Diagnostics for Turn old-turn" })); expect(select).toHaveBeenCalledWith("old-run", "old-turn"); }
  });
  it("shows Agent Markdown and keeps long safe Tool output collapsed", () => {
    const turn: WorkerTimelineTurn = {
      worker_run_id: "run-1", agent_turn_id: "turn-1", display_stream_id: "display-1",
      status: "completed", started_at: "2026-07-25T00:00:00Z", prompt: "Inspect the workspace",
      checkpoint_version: 3, checkpoint_complete: true, backfill_status: "online",
      parts: [
        {
          schema_version: "frontier.display_part.v1", part_id: "agent-1", part_type: "text",
          part_revision: 1, ordinal: 0, lifecycle_status: "completed", critical: false,
          payload: { content: "## Result\nAll tests passed.", format: "markdown" },
          created_at: "2026-07-25T00:00:01Z", updated_at: "2026-07-25T00:00:02Z",
        },
        {
          schema_version: "frontier.display_part.v1", part_id: "tool-1", part_type: "tool",
          part_revision: 2, ordinal: 1, lifecycle_status: "completed", critical: false,
          payload: { title: "Run tests", duration_ms: 1200, output: "x".repeat(2048), output_truncated: true },
          created_at: "2026-07-25T00:00:01Z", updated_at: "2026-07-25T00:00:02Z",
        },
      ],
    };
    const { container } = render(<WorkerTimeline
      workerSessionId="session-1"
      initialGroups={[turn]}
      initialHasMore={false}
      transient={[]}
      onArtifact={vi.fn()}
    />);
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    const details = container.querySelector("details");
    expect(details).not.toHaveAttribute("open");
    fireEvent.click(screen.getByText("Run tests"));
    expect(details).toHaveAttribute("open");
    expect(screen.getByText("x".repeat(2048))).toBeInTheDocument();
  });

  it("renders durable checkpoint history separately from a transient overlay", () => {
    const turn: WorkerTimelineTurn = {
      worker_run_id: "run-durable", agent_turn_id: "turn-durable", display_stream_id: "display-durable",
      status: "running", started_at: "2026-07-25T00:00:00Z", prompt: "Use durable history",
      checkpoint_version: 4, checkpoint_complete: true, backfill_status: "online",
      parts: [{
        schema_version: "frontier.display_part.v1", part_id: "part-durable", part_type: "text",
        part_revision: 2, ordinal: 0, lifecycle_status: "completed", critical: false,
        payload: { content: "Durable checkpoint text" },
        created_at: "2026-07-25T00:00:01Z", updated_at: "2026-07-25T00:00:02Z",
      }],
    };
    const transient: TransientDisplayDelta = {
      schema_version: "frontier.transient_display_delta.v1",
      display_stream_id: "display-live",
      part_id: "part-live",
      base_checkpoint_version: 1,
      transient_sequence: 1,
      delta_type: "text_delta",
      payload: { text: "Transient overlay text" },
      emitted_at: "2026-07-25T00:00:03Z",
    };
    render(<WorkerTimeline
      workerSessionId="session-1"
      initialGroups={[turn]}
      initialHasMore={false}
      transient={[transient]}
      onArtifact={vi.fn()}
    />);

    expect(screen.getByLabelText("Durable Worker history")).toHaveTextContent("Durable checkpoint text");
    expect(screen.getByLabelText("Live Worker activity")).toHaveTextContent("Transient overlay text");
    expect(screen.getByLabelText("Live Worker activity")).not.toHaveTextContent("Durable checkpoint text");
  });
});
