import { PanelLeft } from "lucide-react";
import { Outlet, useMatches } from "react-router";
import { isSwitchbackRouteHandle, type SwitchbackRouteHandle } from "../../app/routeHandles";
import { ThemeControls } from "../../providers/ThemeProvider";
import { MobileNavigation } from "./MobileNavigation";
import { ProductNavigation } from "./ProductNavigation";

function currentHandle(matches: ReturnType<typeof useMatches>): SwitchbackRouteHandle {
  const handle = [...matches].reverse().find((match) => isSwitchbackRouteHandle(match.handle))?.handle;
  if (!isSwitchbackRouteHandle(handle)) {
    throw new Error("ProductShell requires a Switchback route handle");
  }
  return handle;
}

export function ProductShell() {
  const handle = currentHandle(useMatches());
  const navigationMode = handle.shellMode === "focused" ? "rail" : "expanded";

  return (
    <div className="product-shell" data-shell-mode={handle.shellMode} data-testid="product-shell">
      <aside className="product-sidebar">
        <div className="product-brand">
          <span aria-hidden="true" className="product-brand-mark"><PanelLeft size={17} /></span>
          {handle.shellMode === "overview" ? <strong>SWITCHBACK</strong> : null}
        </div>
        <ProductNavigation mode={navigationMode} />
      </aside>

      <div className="product-frame">
        <header className="product-header">
          <MobileNavigation />
          <div>
            <h1>{handle.title}</h1>
            <span>Switchback</span>
          </div>
          <ThemeControls />
        </header>
        <div className="product-canvas">
          <Outlet />
        </div>
      </div>
    </div>
  );
}
