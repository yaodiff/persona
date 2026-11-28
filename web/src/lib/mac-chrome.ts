import { useSyncExternalStore } from "react";
import type { DesktopWindowChrome } from "./desktop-types";

export const DEFAULT_MAC_CHROME: DesktopWindowChrome = { unifiedTitlebar: false, fullScreen: false, focused: true };

const readChrome = (): DesktopWindowChrome =>
  (typeof window !== "undefined" ? window.personaDesktop?.windowChrome?.() : undefined) ?? DEFAULT_MAC_CHROME;

const sameChrome = (a: DesktopWindowChrome, b: DesktopWindowChrome): boolean =>
  a.unifiedTitlebar === b.unifiedTitlebar && a.fullScreen === b.fullScreen && a.focused === b.focused;

let bridgeRef: unknown = typeof window !== "undefined" ? window.personaDesktop : undefined;
let currentChrome: DesktopWindowChrome = readChrome();
const listeners = new Set<() => void>();
let detachBridge: (() => void) | undefined;

function syncBridge(): void {
  const bridge = typeof window !== "undefined" ? window.personaDesktop : undefined;
  if (bridge === bridgeRef) return;
  bridgeRef = bridge;
  const next = readChrome();
  if (!sameChrome(currentChrome, next)) currentChrome = next;
}

function getSnapshot(): DesktopWindowChrome {
  syncBridge();
  return currentChrome;
}

function subscribe(listener: () => void): () => void {
  syncBridge();
  listeners.add(listener);
  if (!detachBridge && typeof window !== "undefined") {
    detachBridge = window.personaDesktop?.onWindowChromeChange?.((next: DesktopWindowChrome) => {
      if (sameChrome(currentChrome, next)) return;
      currentChrome = next;
      listeners.forEach((f) => f());
    });
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && detachBridge) {
      detachBridge();
      detachBridge = undefined;
    }
  };
}

/** Subscribes to macOS unified-titlebar chrome state; returns browser-safe defaults outside macOS desktop. */
export const useMacChrome = (): DesktopWindowChrome => useSyncExternalStore(subscribe, getSnapshot, () => DEFAULT_MAC_CHROME);

/** A shell's `data-mac-chrome` (styles.css): the window under the unified titlebar, or in full screen; absent outside it. */
export const macChromeMode = (chrome: DesktopWindowChrome): "windowed" | "fullscreen" | undefined =>
  chrome.unifiedTitlebar ? (chrome.fullScreen ? "fullscreen" : "windowed") : undefined;

const getModeSnapshot = () => macChromeMode(getSnapshot());

/** Subscribes only to `"windowed" | "fullscreen" | undefined` so window focus/blur churn never re-renders the shell. */
export const useMacChromeMode = (): "windowed" | "fullscreen" | undefined =>
  useSyncExternalStore(subscribe, getModeSnapshot, () => undefined);

const BAY_ANCHOR_X = 168;
const BAY_CUTOUT_MAX = 166;
const BAY_RAMP_BX = 24;

/** Cutout depth `bx` for the macOS 44 px control bay as a function of the live sidebar width in px. */
export function sidebarBayCutout(sidebarWidth: number): number {
  return Math.min(BAY_CUTOUT_MAX, Math.max(0, Math.round(BAY_ANCHOR_X - sidebarWidth)));
}

/** Matching `<main>` top header left padding (px) ahead of a bay cutout `bx`. */
export function macHeaderInset(bx: number): number {
  if (bx <= 0) return 16;
  return Math.round(16 + Math.min(1, bx / BAY_RAMP_BX) * 18 + bx);
}

const fmtCoord = (n: number): string => String(Math.round(n * 100) / 100);

/** Computes the Apple G2 continuous-curvature SVG cutout and contour paths at any bay depth `bx` (0 = rounded rect, 112 = rail, 166 = hidden). */
export function appleG2ContourAt(W: number, H: number, bx: number): { clip: string; stroke: string; rim: string } {
  const clipTail = `H ${W - 14} A 14,14 0 0 1 ${W},14 V ${H - 14} A 14,14 0 0 1 ${W - 14},${H} H 14 A 14,14 0 0 1 0,${H - 14}`;
  const strokeTail = `H ${W - 14} A 13.5,13.5 0 0 1 ${W - 0.5},14 V ${H - 14} A 13.5,13.5 0 0 1 ${W - 14},${H - 0.5} H 14 A 13.5,13.5 0 0 1 0.5,${H - 14}`;
  if (bx <= 0.5) {
    return {
      clip: `M 14,0 ${clipTail} V 14 A 14,14 0 0 1 14,0 Z`,
      stroke: `M 14,0.5 ${strokeTail} V 14 A 13.5,13.5 0 0 1 14,0.5 Z`,
      rim: `M 14,1.25 H ${W - 14}`,
    };
  }
  const t = Math.min(1, bx / BAY_RAMP_BX);
  const by = 44 * t;
  const sy = 0.5 + by;
  const x0 = bx < BAY_RAMP_BX ? 14 : bx - 10;
  const sx0 = bx < BAY_RAMP_BX ? 14 + 0.5 * t : bx - 9.5;
  const c = fmtCoord;
  return {
    clip: `M ${c(x0 + 32 * t)},0 ${clipTail} V ${c(by + 14)} C 0,${c(by + 5)} 5,${c(by)} 14,${c(by)} H ${c(x0)} C ${c(x0 + 11.5 * t)},${c(by)} ${c(x0 + 13.5 * t)},${c(by - 6 * t)} ${c(x0 + 16 * t)},${c(by / 2)} C ${c(x0 + 18.5 * t)},${c(6 * t)} ${c(x0 + 20.5 * t)},0 ${c(x0 + 32 * t)},0 Z`,
    stroke: `M ${c(sx0 + 32 * t)},0.5 ${strokeTail} V ${c(sy + 13.5)} C 0.5,${c(sy + 5)} 5,${c(sy)} 14,${c(sy)} H ${c(sx0)} C ${c(sx0 + 11.5 * t)},${c(sy)} ${c(sx0 + 13.5 * t)},${c(sy - 6 * t)} ${c(sx0 + 16 * t)},${c(0.5 + 21.75 * t)} C ${c(sx0 + 18.5 * t)},${c(0.5 + 5.75 * t)} ${c(sx0 + 20.5 * t)},0.5 ${c(sx0 + 32 * t)},0.5 Z`,
    rim: `M ${c(x0 + 32 * t)},1.25 H ${W - 14}`,
  };
}

/** Computes the 44 px co-linear Apple G2 continuous-curvature SVG cutout and contour paths for `<main>`. */
export function appleG2Contour(W: number, H: number, bay: "rail" | "hidden" | null): { clip: string; stroke: string; rim: string } {
  return appleG2ContourAt(W, H, bay === "rail" ? 112 : bay === "hidden" ? 166 : 0);
}
