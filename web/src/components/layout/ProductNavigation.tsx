import {
  BotMessageSquare,
  Boxes,
  BrainCircuit,
  Gauge,
  LayoutDashboard,
  ListTodo,
  Sparkles,
} from "lucide-react";
import { NavLink } from "react-router";
import { cn } from "../../lib/cn";

export const productNavigationItems = [
  { to: "/", label: "工作台", icon: LayoutDashboard, end: true },
  { to: "/assistant", label: "Assistant 对话", icon: BotMessageSquare, end: false },
  { to: "/tasks", label: "任务交付", icon: ListTodo, end: true },
  { to: "/observability", label: "可观测性", icon: Gauge, end: true },
] as const;

export const plannedNavigationItems = [
  { label: "Workspaces", icon: Boxes },
  { label: "Memory", icon: BrainCircuit },
  { label: "Skills / MCP", icon: Sparkles },
  { label: "Admin", icon: Gauge },
] as const;

interface ProductNavigationProps {
  mode: "expanded" | "rail" | "drawer";
}

export function ProductNavigation({ mode }: ProductNavigationProps) {
  const showLabels = mode !== "rail";

  return (
    <nav aria-label="产品导航" className="product-navigation" data-navigation-mode={mode}>
      <div className="product-navigation-primary">
        {productNavigationItems.map(({ to, label, icon: Icon, end }) => (
          <NavLink
            className={({ isActive }) => cn("product-navigation-link", isActive && "active")}
            end={end}
            key={to}
            to={to}
            title={mode === "rail" ? label : undefined}
          >
            <Icon aria-hidden="true" size={18} />
            <span className={showLabels ? undefined : "visually-hidden"}>{label}</span>
          </NavLink>
        ))}
      </div>

      {showLabels ? (
        <section aria-labelledby={`planned-navigation-${mode}`} className="planned-navigation">
          <h2 id={`planned-navigation-${mode}`}>平台规划</h2>
          <ul>
            {plannedNavigationItems.map(({ label, icon: Icon }) => (
              <li key={label}>
                <Icon aria-hidden="true" size={16} />
                <span>{label}</span>
                <small>规划中</small>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </nav>
  );
}
