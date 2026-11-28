import type { DesktopThemeSource } from "../../spec/desktop-bridge.js";

/** Validates an untrusted renderer theme-source payload before applying it to nativeTheme. */
export function parseDesktopThemeSource(value: unknown): DesktopThemeSource | undefined {
  return value === "system" || value === "light" || value === "dark" ? value : undefined;
}

/** Updates Electron nativeTheme.themeSource and refreshes the current window surface backing. */
export function applyDesktopThemeSource(
  target: { themeSource: DesktopThemeSource },
  theme: DesktopThemeSource,
  refreshSurface: () => void,
): boolean {
  const changed = target.themeSource !== theme;
  if (changed) target.themeSource = theme;
  refreshSurface();
  return changed;
}
