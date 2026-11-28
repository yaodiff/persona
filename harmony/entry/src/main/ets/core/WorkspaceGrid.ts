/**
 * Responsive multi-column grid and horizontal margin helpers for HarmonyOS NEXT
 * secondary workspaces across straight phones (Mate 90 Pro / cover screens),
 * dual-fold unfolded canvases (Mate X7), and tri-fold / tablet canvases (Mate XT / MatePad Pro 13).
 *
 * Pure TypeScript so layout decisions are deterministically verified on host without ArkUI mocks.
 */

export const WORKSPACE_WIDE_BREAKPOINT_VP: number = 560;
export const WORKSPACE_ULTRA_WIDE_BREAKPOINT_VP: number = 960;
export const WORKSPACE_A11Y_SINGLE_COL_FONT_SCALE: number = 1.6;

/**
 * Resolves the adaptive card column count for a workspace canvas:
 * - `1` on straight phones / cover screens (`widthVp < 560`) or accessibility font scales (`fontScale >= 1.6`)
 * - `2` on Mate X7 unfolded / medium canvases (`560 <= widthVp < 960`)
 * - `3` on Mate XT tri-fold / MatePad Pro 13 wide canvases (`widthVp >= 960`)
 * Clamped to `[1, maxCols]` (default `3`).
 */
export function workspaceGridColumns(widthVp: number, fontScale: number, maxCols: number = 3): number {
  const cap = Number.isFinite(maxCols) ? Math.max(1, Math.floor(maxCols)) : 3;
  if (!Number.isFinite(widthVp) || widthVp < WORKSPACE_WIDE_BREAKPOINT_VP) return 1;
  if (Number.isFinite(fontScale) && fontScale >= WORKSPACE_A11Y_SINGLE_COL_FONT_SCALE) return 1;
  const cols = widthVp >= WORKSPACE_ULTRA_WIDE_BREAKPOINT_VP ? 3 : 2;
  return Math.min(cap, cols);
}

/**
 * Resolves comfortable horizontal margins for a workspace canvas:
 * - `16vp` on straight phones / cover screens (`< 560vp`)
 * - `20vp` on Mate X7 unfolded / medium canvases (`560–959vp`)
 * - `24vp` on Mate XT tri-fold / wide tablets (`>= 960vp`)
 */
export function workspaceHorizontalPadding(widthVp: number): number {
  if (!Number.isFinite(widthVp) || widthVp < WORKSPACE_WIDE_BREAKPOINT_VP) return 16;
  if (widthVp >= WORKSPACE_ULTRA_WIDE_BREAKPOINT_VP) return 24;
  return 20;
}

/**
 * Resolves inter-card gutter spacing (vp) aligned with the active column count.
 */
export function workspaceGridGutter(columns: number): number {
  if (!Number.isFinite(columns) || columns <= 1) return 10;
  return columns >= 3 ? 14 : 12;
}
