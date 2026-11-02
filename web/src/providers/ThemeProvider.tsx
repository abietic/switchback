import { createContext, useContext, useEffect, useLayoutEffect, useState, type ReactNode } from "react";

export type ThemePreference = "dark" | "light" | "system";
export type ResolvedTheme = "dark" | "light";

const storageKey = "switchback.theme";

interface ThemeContextValue {
  readonly preference: ThemePreference;
  readonly resolvedTheme: ResolvedTheme;
  readonly setPreference: (preference: ThemePreference) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

function systemPrefersDark() {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function resolveTheme(preference: ThemePreference, systemDark: boolean): ResolvedTheme {
  return preference === "system" ? (systemDark ? "dark" : "light") : preference;
}

interface ThemeProviderProps {
  children: ReactNode;
}

export function ThemeProvider({ children }: ThemeProviderProps) {
  const [preference, setPreferenceState] = useState<ThemePreference>(() => {
    const saved = window.localStorage.getItem(storageKey);
    return saved === "dark" || saved === "light" || saved === "system" ? saved : "dark";
  });
  const [systemDark, setSystemDark] = useState(systemPrefersDark);
  const resolvedTheme = resolveTheme(preference, systemDark);

  useLayoutEffect(() => {
    const root = document.documentElement;
    const previousTheme = root.getAttribute("data-theme");
    const previousDark = root.classList.contains("dark");
    const previousColorScheme = root.style.colorScheme;

    return () => {
      if (previousTheme === null) {
        root.removeAttribute("data-theme");
      } else {
        root.setAttribute("data-theme", previousTheme);
      }
      root.classList.toggle("dark", previousDark);
      root.style.colorScheme = previousColorScheme;
    };
  }, []);

  useLayoutEffect(() => {
    const root = document.documentElement;
    root.dataset.theme = resolvedTheme;
    root.classList.toggle("dark", resolvedTheme === "dark");
    root.style.colorScheme = resolvedTheme;
  }, [resolvedTheme]);

  useEffect(() => {
    if (preference !== "system" || typeof window.matchMedia !== "function") {
      return;
    }

    const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    setSystemDark(mediaQuery.matches);
    mediaQuery.addEventListener("change", onChange);
    return () => mediaQuery.removeEventListener("change", onChange);
  }, [preference]);

  const setPreference = (nextPreference: ThemePreference) => {
    setPreferenceState(nextPreference);
    window.localStorage.setItem(storageKey, nextPreference);
  };

  return <ThemeContext value={{ preference, resolvedTheme, setPreference }}>{children}</ThemeContext>;
}

export function useTheme() {
  const theme = useContext(ThemeContext);
  if (theme === null) {
    throw new Error("useTheme must be rendered within ThemeProvider");
  }

  return theme;
}

export function ThemeControls() {
  const { preference, setPreference } = useTheme();

  return (
    <fieldset>
      <legend>主题</legend>
      <label>
        <input
          checked={preference === "light"}
          name="theme"
          onChange={() => setPreference("light")}
          type="radio"
        />
        浅色主题
      </label>
      <label>
        <input
          checked={preference === "dark"}
          name="theme"
          onChange={() => setPreference("dark")}
          type="radio"
        />
        深色主题
      </label>
      <label>
        <input
          checked={preference === "system"}
          name="theme"
          onChange={() => setPreference("system")}
          type="radio"
        />
        跟随系统主题
      </label>
    </fieldset>
  );
}
