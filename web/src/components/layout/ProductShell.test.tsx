import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeProvider } from "../../providers/ThemeProvider";
import type { SwitchbackRouteHandle } from "../../app/routeHandles";
import { ProductShell } from "./ProductShell";

const overview = { shellMode: "overview", title: "任务交付" } satisfies SwitchbackRouteHandle;
const focused = { shellMode: "focused", title: "Assistant" } satisfies SwitchbackRouteHandle;

function renderShell(initialEntry = "/tasks") {
  const router = createMemoryRouter([{
    path: "/",
    element: <ProductShell />,
    children: [
      { path: "tasks", element: <main>任务页面</main>, handle: overview },
      { path: "observability", element: <main>观测入口页面</main>, handle: { shellMode: "overview", title: "可观测性" } satisfies SwitchbackRouteHandle },
      { path: "assistant", element: <main>开始对话页面</main>, handle: { shellMode: "overview", title: "Assistant" } satisfies SwitchbackRouteHandle },
      { path: "assistant/:conversationId", element: <main>对话页面</main>, handle: focused },
    ],
  }], { initialEntries: [initialEntry] });

  render(<ThemeProvider><RouterProvider router={router} /></ThemeProvider>);
  return router;
}

afterEach(() => {
  window.localStorage.clear();
});

describe("ProductShell", () => {
  it("renders the active expanded overview navigation without interactive planned routes", () => {
    const router = renderShell();

    expect(screen.getByTestId("product-shell")).toHaveAttribute("data-shell-mode", "overview");
    expect(screen.getByRole("link", { name: "任务交付" })).toHaveAttribute("aria-current", "page");
    expect(screen.getAllByRole("group", { name: "主题" })).toHaveLength(1);
    expect(screen.getByText("平台规划")).toBeInTheDocument();
    expect(screen.getAllByText("规划中").length).toBeGreaterThan(0);
    expect(screen.queryByRole("link", { name: /Workspace|Memory|Skills|Admin/ })).toBeNull();
    expect(screen.getByRole("link", { name: "可观测性" })).toHaveAttribute("href", "/observability");
    expect(screen.getByText("Admin").closest("li")).toHaveTextContent("规划中");

    router.dispose();
  });

  it("renders the same shell as a focused rail for execution routes", () => {
    const router = renderShell("/assistant/conversation-1");

    expect(screen.getByTestId("product-shell")).toHaveAttribute("data-shell-mode", "focused");
    expect(screen.getByRole("link", { name: "Assistant 对话" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("navigation", { name: "产品导航" })).toHaveAttribute("data-navigation-mode", "rail");
    expect(screen.getByRole("link", { name: "可观测性" })).toHaveAttribute("href", "/observability");

    router.dispose();
  });

  it("opens observability from the drawer and restores focus", async () => {
    const router = renderShell();
    const trigger = screen.getByRole("button", { name: "打开导航菜单" });
    fireEvent.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "导航菜单" });
    expect(within(dialog).queryByRole("link", { name: "Admin" })).toBeNull();
    fireEvent.click(within(dialog).getByRole("link", { name: "可观测性" }));
    await screen.findByText("观测入口页面");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "导航菜单" })).toBeNull());
    expect(screen.getByRole("link", { name: "可观测性" })).toHaveAttribute("aria-current", "page");
    expect(trigger).toHaveFocus();
    router.dispose();
  });

  it("moves initial focus into the modal drawer and restores it after Escape", async () => {
    const router = renderShell();
    const trigger = screen.getByRole("button", { name: "打开导航菜单" });

    fireEvent.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "导航菜单" });
    await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));

    fireEvent.keyDown(document.activeElement ?? dialog, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "导航菜单" })).toBeNull());
    expect(trigger).toHaveFocus();

    router.dispose();
  });

  it("closes from the overlay and after navigation, restoring focus to the trigger", async () => {
    const router = renderShell();
    const trigger = screen.getByRole("button", { name: "打开导航菜单" });

    fireEvent.click(trigger);
    fireEvent.click(await screen.findByTestId("mobile-navigation-overlay"));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "导航菜单" })).toBeNull());
    expect(trigger).toHaveFocus();

    fireEvent.click(trigger);
    const dialog = await screen.findByRole("dialog", { name: "导航菜单" });
    fireEvent.click(within(dialog).getByRole("link", { name: "Assistant 对话" }));
    await screen.findByText("开始对话页面");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "导航菜单" })).toBeNull());
    expect(trigger).toHaveFocus();

    router.dispose();
  });
});
