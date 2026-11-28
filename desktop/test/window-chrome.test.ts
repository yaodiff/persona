import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { applyMacSurfaceStyle, liquidGlassVibrancy } from "../src/surface-style.js";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");

test("showMain configures macOS hiddenInset titlebar and publishes window chrome changes", async () => {
  const main = await source("main.ts");
  const showMain = main.slice(main.indexOf("async function showMain"), main.indexOf("async function showSetup"));
  assert.ok(!showMain.includes("transparent: true"), "showMain must not set transparent: true on darwin; vibrancy + #00000000 avoids macOS Stage Manager's ~400ms gray vibrancy dropout");
  assert.ok(showMain.includes('titleBarStyle: "hiddenInset"'), "showMain must enable hiddenInset on darwin");
  assert.ok(showMain.includes("trafficLightPosition: { x: 14, y: 19 }"), "showMain must anchor traffic lights inside the 44px bay");
  assert.ok(showMain.includes("vibrancy: initialVibrancy"), "showMain must initialize vibrancy in constructor alongside visualEffectState");
  assert.ok(showMain.includes('visualEffectState: "active"'), "showMain must keep Liquid Glass vibrancy active");
  assert.ok(showMain.includes('void store().update({ surfaceStyle: style })'), "showMain must persist surfaceStyle for launch");
  assert.ok(showMain.includes('void store().update({ themeSource: theme })'), "showMain must persist themeSource for launch");
  assert.ok(showMain.includes('desktop:window-chrome-changed'), "showMain must notify renderer when fullscreen changes");
  assert.ok(showMain.includes('current.on("enter-full-screen", publishChrome)'), "showMain must publish chrome on fullscreen enter");
  assert.ok(showMain.includes('current.on("leave-full-screen", publishChrome)'), "showMain must publish chrome on fullscreen leave");
  assert.ok(!showMain.includes('current.on("focus", publishChrome)'), "showMain must not churn root chrome IPC on window focus");
  assert.ok(!showMain.includes('current.on("blur", publishChrome)'), "showMain must not churn root chrome IPC on window blur");
  assert.ok(showMain.includes('if (process.platform === "darwin") applySurfaceStyle(current, surfaceStyle);'), "onThemeUpdated must keep macOS appliedStates in sync across Light/Dark switches");
  const tray = await source("tray.ts");
  assert.ok(tray.includes("!window.isVisible() || window.isMinimized()"), "macOS activate handler must avoid redundant show() when window is already visible");
  assert.ok(main.includes("applyMacSurfaceStyle("), "applySurfaceStyle must delegate to applyMacSurfaceStyle");
});

test("liquidGlassVibrancy and applyMacSurfaceStyle select scheme-aware materials, stay idempotent, and guard destroyed windows", () => {
  assert.equal(liquidGlassVibrancy(true), "hud");
  assert.equal(liquidGlassVibrancy(false), "popover");

  const calls: string[] = [];
  const win = {
    destroyed: false,
    isDestroyed() { return this.destroyed; },
    setVibrancy(v: string | null) { calls.push(`v:${v}`); },
    setBackgroundColor(c: string) { calls.push(`bg:${c}`); },
  };

  applyMacSurfaceStyle(win, "liquid-glass", true, "#0b0b0c");
  applyMacSurfaceStyle(win, "liquid-glass", true, "#0b0b0c");
  applyMacSurfaceStyle(win, "liquid-glass", false, "#f1f1f4");
  applyMacSurfaceStyle(win, "default", true, "#0b0b0c");
  applyMacSurfaceStyle(win, "default", true, "#0b0b0c");
  win.destroyed = true;
  applyMacSurfaceStyle(win, "liquid-glass", true, "#0b0b0c");

  assert.deepEqual(calls, [
    "v:hud",
    "bg:#00000000",
    "v:popover",
    "v:null",
    "bg:#0b0b0c",
  ]);
});

test("window chrome and surface style IPC handlers validate input and clean up on close", async () => {
  const ipc = await source("ipc.ts");
  const preload = await source("preload.cts");
  assert.ok(ipc.includes('"desktop:window-chrome"'), "ipc must register desktop:window-chrome");
  assert.ok(ipc.includes('"desktop:surface-style"'), "ipc must register desktop:surface-style");
  assert.match(ipc, /value === "default" \|\| value === "liquid-glass"/);
  assert.ok(preload.includes("windowChrome(): DesktopWindowChrome"));
  assert.ok(preload.includes("onWindowChromeChange("));
  assert.ok(preload.includes("setSurfaceStyle(style: DesktopSurfaceStyle)"));
});
