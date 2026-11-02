import { act, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ThemeControls, ThemeProvider, useTheme } from "./ThemeProvider";

const storageKey = "switchback.theme";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  document.documentElement.classList.remove("dark");
  delete document.documentElement.dataset.theme;
  document.documentElement.style.colorScheme = "";
});

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

function ThemeResolutionProbe() {
  const { resolvedTheme } = useTheme();
  return <output aria-label="resolved theme">{resolvedTheme}</output>;
}

describe("ThemeProvider", () => {
  it("uses dark on a first visit without persisting a preference", () => {
    render(
      <ThemeProvider>
        <div />
      </ThemeProvider>,
    );

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(document.documentElement.style.colorScheme).toBe("dark");
    expect(window.localStorage.getItem(storageKey)).toBeNull();
  });

  it("persists only an explicit accessible light or dark selection", () => {
    render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByRole("radio", { name: "浅色主题" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(window.localStorage.getItem(storageKey)).toBe("light");

    fireEvent.click(screen.getByRole("radio", { name: "深色主题" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(storageKey)).toBe("dark");
  });

  it("follows system changes and removes its media listener on unmount", () => {
    const { listeners, mediaQuery } = installMediaQuery(true);

    const { unmount } = render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByRole("radio", { name: "跟随系统主题" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(storageKey)).toBe("system");

    act(() => {
      mediaQuery.matches = false;
      for (const listener of listeners) {
        listener({ matches: false } as MediaQueryListEvent);
      }
    });
    expect(document.documentElement.dataset.theme).toBe("light");

    unmount();
    expect(mediaQuery.removeEventListener).toHaveBeenCalledWith("change", expect.any(Function));
  });

  it.each([
    ["light", false, "light"],
    ["dark", false, "dark"],
    ["system", true, "dark"],
  ] as const)("restores persisted %s preference on mount", (preference, systemDark, resolved) => {
    installMediaQuery(systemDark);
    window.localStorage.setItem(storageKey, preference);

    render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );

    expect(screen.getByRole("radio", { name: preference === "light" ? "浅色主题" : preference === "dark" ? "深色主题" : "跟随系统主题" })).toBeChecked();
    expect(document.documentElement.dataset.theme).toBe(resolved);
    expect(document.documentElement.style.colorScheme).toBe(resolved);
  });

  it("ignores invalid storage without mutating presentation or domain storage", () => {
    window.localStorage.setItem(storageKey, "sepia");
    window.localStorage.setItem("switchback.task", "authoritative");
    const setItem = vi.spyOn(Storage.prototype, "setItem");

    render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(storageKey)).toBe("sepia");
    expect(window.localStorage.getItem("switchback.task")).toBe("authoritative");
    expect(setItem).not.toHaveBeenCalled();
  });

  it("writes only the presentation key after an explicit selection", () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");

    render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );
    fireEvent.click(screen.getByRole("radio", { name: "浅色主题" }));
    fireEvent.click(screen.getByRole("radio", { name: "跟随系统主题" }));

    expect(setItem.mock.calls).toEqual([
      [storageKey, "light"],
      [storageKey, "system"],
    ]);
  });

  it("restores the root attributes and class state it owned on unmount", () => {
    const root = document.documentElement;
    root.dataset.theme = "embedded-host";
    root.classList.add("host-theme");
    root.classList.remove("dark");
    root.style.colorScheme = "normal";

    const { unmount } = render(
      <ThemeProvider>
        <div />
      </ThemeProvider>,
    );
    expect(root.dataset.theme).toBe("dark");
    expect(root.classList.contains("dark")).toBe(true);

    unmount();
    expect(root.dataset.theme).toBe("embedded-host");
    expect(root.classList.contains("dark")).toBe(false);
    expect(root.classList.contains("host-theme")).toBe(true);
    expect(root.style.colorScheme).toBe("normal");
  });

  it("keeps one system listener under StrictMode and releases all effects", () => {
    const root = document.documentElement;
    root.dataset.theme = "host";
    root.style.colorScheme = "normal";
    window.localStorage.setItem(storageKey, "system");
    const { listeners, mediaQuery } = installMediaQuery(true);

    const { unmount } = render(
      <StrictMode>
        <ThemeProvider>
          <ThemeControls />
        </ThemeProvider>
      </StrictMode>,
    );

    expect(listeners.size).toBe(1);
    expect(mediaQuery.addEventListener).toHaveBeenCalledTimes(2);
    expect(mediaQuery.removeEventListener).toHaveBeenCalledTimes(1);

    unmount();
    expect(listeners.size).toBe(0);
    expect(mediaQuery.removeEventListener).toHaveBeenCalledTimes(2);
    expect(root.dataset.theme).toBe("host");
    expect(root.classList.contains("dark")).toBe(false);
    expect(root.style.colorScheme).toBe("normal");
  });

  it("exposes a named group and named controls for every preference", () => {
    render(
      <ThemeProvider>
        <ThemeControls />
      </ThemeProvider>,
    );

    expect(screen.getByRole("group", { name: "主题" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "浅色主题" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "深色主题" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "跟随系统主题" })).toBeInTheDocument();
  });

  it("exposes the resolved system theme and updates consumers after a media change", () => {
    const { listeners, mediaQuery } = installMediaQuery(true);
    window.localStorage.setItem(storageKey, "system");

    render(
      <ThemeProvider>
        <ThemeResolutionProbe />
      </ThemeProvider>,
    );
    expect(screen.getByRole("status", { name: "resolved theme" })).toHaveTextContent("dark");

    act(() => {
      mediaQuery.matches = false;
      for (const listener of listeners) {
        listener({ matches: false } as MediaQueryListEvent);
      }
    });
    expect(screen.getByRole("status", { name: "resolved theme" })).toHaveTextContent("light");
  });
});
