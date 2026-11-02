import * as Dialog from "@radix-ui/react-dialog";
import { Menu, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useLocation } from "react-router";
import { ProductNavigation } from "./ProductNavigation";

export function MobileNavigation() {
  const [open, setOpen] = useState(false);
  const location = useLocation();

  useEffect(() => {
    setOpen(false);
  }, [location.pathname]);

  return (
    <Dialog.Root onOpenChange={setOpen} open={open}>
      <Dialog.Trigger asChild>
        <button aria-label="打开导航菜单" className="mobile-navigation-trigger" type="button">
          <Menu aria-hidden="true" size={19} />
        </button>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay
          className="mobile-navigation-overlay"
          data-testid="mobile-navigation-overlay"
          onClick={() => setOpen(false)}
        />
        <Dialog.Content className="mobile-navigation-drawer">
          <header>
            <div>
              <Dialog.Title className="mobile-navigation-title">导航菜单</Dialog.Title>
              <Dialog.Description className="mobile-navigation-description">在 Switchback 核心页面之间切换</Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <button aria-label="关闭导航菜单" className="icon-button" type="button">
                <X aria-hidden="true" size={18} />
              </button>
            </Dialog.Close>
          </header>
          <div className="mobile-navigation-scroll" data-testid="mobile-navigation-scroll">
            <ProductNavigation mode="drawer" />
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
