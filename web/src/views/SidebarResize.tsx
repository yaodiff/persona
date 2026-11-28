// The full sidebar's right edge on a wide screen (docs/web/web.md "The sidebar's width"): dragged (ui/ResizeHandle.tsx,
// like the side panel's left edge) it sets the column's width, kept in this browser (lib/layout.ts).
// The width reaches the column as a CSS variable this sets on it, so a drag moves only the column and the page beside
// it: the shell, the sidebar's rows and the page are not rendered again on each pointer move.
import { useEffect, useLayoutEffect, useState } from "react";
import { CHAT_MIN, PANEL_GAP, PANEL_MIN, resetSidebarWidth, setSidebarWidth, SIDEBAR_MIN, sidebarWidth, useSidebarWidth } from "../lib/layout";
import { usePanel } from "../lib/panel";
import { t } from "../i18n";
import { ResizeHandle } from "../ui/ResizeHandle";

/** The column's width, read by its `lg:w-[var(--sidebar-w,284px)]` (App.tsx). */
export const SIDEBAR_VAR = "--sidebar-w";

/**
 * `column` is the id of the sidebar's column (its handle's aria-controls too). Looked up by id, not by the column's ref:
 * on the first commit a child's layout effect runs before React attaches its parent's ref.
 */
export function SidebarResize({ column }: { column: string }) {
  const want = useSidebarWidth();
  // the page beside it keeps what a conversation needs, and an open side panel its least (SidePanel.tsx, #5148)
  const panel = Boolean(usePanel());
  const [windowWidth, setWindowWidth] = useState(() => innerWidth);
  useEffect(() => {
    const resized = () => setWindowWidth(innerWidth);
    addEventListener("resize", resized);
    return () => removeEventListener("resize", resized);
  }, []);
  // Reserve the outer right inset (PANEL_GAP), plus the inter-card gap and minimum width when a side panel is open.
  const room = windowWidth - CHAT_MIN - PANEL_GAP - (panel ? PANEL_MIN + PANEL_GAP : 0);
  const shown = sidebarWidth(want, room);
  const draw = (w: number) => document.getElementById(column)?.style.setProperty(SIDEBAR_VAR, `${w}px`);
  // before paint, so the column never shows another width first; only when it changes, so another render in the middle
  // of a drag (a panel opening) leaves the dragged width alone
  useLayoutEffect(() => {
    document.getElementById(column)?.style.setProperty(SIDEBAR_VAR, `${shown}px`);
  }, [column, shown]);
  return (
    <ResizeHandle
      edge="right"
      value={shown}
      min={SIDEBAR_MIN}
      max={sidebarWidth(Infinity, room)}
      onResize={draw}
      onCommit={setSidebarWidth}
      onReset={resetSidebarWidth}
      label={t("sidebar.resize_label")}
      title={t("sidebar.resize_title")}
      controls={column}
      className="hidden lg:flex"
    />
  );
}
