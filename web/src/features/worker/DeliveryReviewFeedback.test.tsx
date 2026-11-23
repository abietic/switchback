import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { DeliveryReviewFeedback } from "./DeliveryReviewFeedback";

it("displays findings for the current delivery and rejects a receipt for a different revision", async () => {
  const review = {
    reviewer: "argus",
    verdict: "rework_required",
    findings: [{ code: "missing-parameters", summary: "Disclose the scenario parameters." }],
  };
  const request = vi.fn()
    .mockResolvedValueOnce(Response.json({ task_revision: 4, review }))
    .mockResolvedValueOnce(Response.json({ task_revision: 5, review }));
  vi.stubGlobal("fetch", request);

  try {
    render(<DeliveryReviewFeedback taskId="task-example" taskRevision={4} />);
    const check = screen.getByRole("button", { name: "Check Argus review" });
    fireEvent.click(check);
    expect(await screen.findByText("Disclose the scenario parameters.")).toBeInTheDocument();
    expect(request).toHaveBeenCalledWith("/api/v1/tasks/task-example/delivery-review", expect.objectContaining({ cache: "no-store" }));

    fireEvent.click(check);
    expect(await screen.findByText("The delivery changed. Refresh its review actions before accepting.")).toBeInTheDocument();
    expect(screen.queryByText("Disclose the scenario parameters.")).toBeNull();
  } finally {
    vi.unstubAllGlobals();
  }
});
