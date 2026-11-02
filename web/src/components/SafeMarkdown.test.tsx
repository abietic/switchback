import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SafeMarkdown } from "./SafeMarkdown";

describe("SafeMarkdown", () => {
  it("supports GFM while disabling raw HTML and automatic remote images", () => {
    const { container } = render(<SafeMarkdown>{`
| Result | Status |
| --- | --- |
| test | pass |

<script>window.stolen = true</script>

![remote diagram](https://example.com/tracker.png)
`}</SafeMarkdown>);
    expect(screen.getByRole("table")).toHaveTextContent("testpass");
    expect(screen.getByText("Image blocked: remote diagram")).toBeInTheDocument();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container).not.toHaveTextContent("window.stolen");
  });
});
