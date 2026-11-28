import type { DesktopSurfaceStyle } from "../../spec/desktop-bridge.js";

export type MacLiquidGlassVibrancy = "hud" | "popover";

export interface MacSurfaceState {
  vibrancy: MacLiquidGlassVibrancy | null;
  background: string;
}

const appliedStates = new WeakMap<object, MacSurfaceState>();

/** Records the initial BrowserWindow vibrancy/background so later idempotent refreshes avoid recreating NSVisualEffectView. */
export function seedMacSurfaceState(window: object, state: MacSurfaceState): void {
  appliedStates.set(window, { ...state });
}

/** Picks a crisp, scheme-appropriate macOS vibrancy material for Liquid Glass (dark hud vs light popover). */
export function liquidGlassVibrancy(shouldUseDarkColors: boolean): MacLiquidGlassVibrancy {
  return shouldUseDarkColors ? "hud" : "popover";
}

/** Applies or clears macOS Liquid Glass vibrancy on a BrowserWindow without touching destroyed windows or repeating unchanged state. */
export function applyMacSurfaceStyle(
  window: {
    isDestroyed(): boolean;
    setVibrancy(type: MacLiquidGlassVibrancy | null): void;
    setBackgroundColor(color: string): void;
  },
  style: DesktopSurfaceStyle,
  shouldUseDarkColors: boolean,
  opaqueBackground: string,
): void {
  if (window.isDestroyed()) return;
  const next: MacSurfaceState = style === "liquid-glass"
    ? { vibrancy: liquidGlassVibrancy(shouldUseDarkColors), background: "#00000000" }
    : { vibrancy: null, background: opaqueBackground };
  const prev = appliedStates.get(window);
  if (prev?.vibrancy === next.vibrancy && prev?.background === next.background) return;
  if (!prev || prev.vibrancy !== next.vibrancy) window.setVibrancy(next.vibrancy);
  if (!prev || prev.background !== next.background) window.setBackgroundColor(next.background);
  appliedStates.set(window, next);
}
