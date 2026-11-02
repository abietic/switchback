export type ShellMode = "overview" | "focused";

export interface SwitchbackRouteHandle {
  shellMode: ShellMode;
  title: string;
}

export function isSwitchbackRouteHandle(value: unknown): value is SwitchbackRouteHandle {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<SwitchbackRouteHandle>;
  return (candidate.shellMode === "overview" || candidate.shellMode === "focused")
    && typeof candidate.title === "string";
}
