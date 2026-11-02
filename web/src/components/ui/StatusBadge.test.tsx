import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it.each([
    ["Queued", "neutral"],
    ["Running", "active"],
    ["Waiting for user", "waiting"],
    ["Complete", "success"],
    ["Failed", "danger"],
  ] as const)("keeps %s status meaning in visible and accessible text", (label, tone) => {
    render(<StatusBadge label={label} tone={tone} />);

    expect(screen.getByText(label)).toBeVisible();
    expect(screen.getByLabelText(label)).toHaveClass(`status-${tone}`);
  });
});
