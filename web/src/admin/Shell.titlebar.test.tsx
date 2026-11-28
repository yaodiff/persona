// @vitest-environment jsdom
// Hub Admin under the desktop's macOS unified titlebar (#16992): the window controls sit over the top of its sidebar, so
// the sidebar takes the main app's expanded-sidebar inset and drag strip; a browser and full screen take none.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DesktopWindowChrome } from "../lib/desktop-types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

// The shell's pages wait for the hub; only the shell itself is looked at here.
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  adminApi: () => new Promise(() => {}),
  getMesh: () => new Promise(() => {}),
}));

const { Shell } = await import("./Shell");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let emit: ((next: DesktopWindowChrome) => void) | undefined;

/** The desktop bridge's window chrome, as the macOS desktop app's preload answers it. */
function desktop(chrome: DesktopWindowChrome) {
  Object.defineProperty(window, "personaDesktop", {
    configurable: true,
    value: {
      windowChrome: () => chrome,
      onWindowChromeChange: (listener: (next: DesktopWindowChrome) => void) => {
        emit = listener;
        return () => { emit = undefined; };
      },
    },
  });
}

const shell = () => host.querySelector<HTMLElement>("[data-admin-shell]");

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Reflect.deleteProperty(window, "personaDesktop");
  emit = undefined;
});

describe("Hub Admin under the desktop's macOS titlebar", () => {
  it("leaves the sidebar alone in a browser", () => {
    act(() => root.render(<Shell hubName="Example hub" onLogout={() => {}} />));
    expect(shell()).not.toBeNull();
    expect(shell()!.dataset.macChrome).toBeUndefined();
    expect(shell()!.querySelector("aside[data-admin-sidebar]")).not.toBeNull();
  });

  it("marks the shell windowed under the unified titlebar, and no longer in full screen", () => {
    desktop({ unifiedTitlebar: true, fullScreen: false, focused: true });
    act(() => root.render(<Shell hubName="Example hub" onLogout={() => {}} />));
    expect(shell()!.dataset.macChrome).toBe("windowed");
    act(() => emit?.({ unifiedTitlebar: true, fullScreen: true, focused: true }));
    expect(shell()!.dataset.macChrome).toBe("fullscreen");
    act(() => emit?.({ unifiedTitlebar: true, fullScreen: false, focused: false }));
    expect(shell()!.dataset.macChrome).toBe("windowed");
  });

  it("takes the expanded sidebar's own inset and drag strip from styles.css", () => {
    const css = readFileSync(resolve(import.meta.dirname, "../styles.css"), "utf8");
    // One rule for both sidebars, so the two stay the same: the 38 px inset and the 44 px strip the window drags by.
    expect(css).toMatch(/\[data-app-shell\]\[data-mac-bay="expanded"\] \[data-sidebar-root\],\s*\[data-admin-shell\]\[data-mac-chrome="windowed"\] \[data-admin-sidebar\] \{\s*position: relative;\s*padding-top: 38px;\s*\}/);
    expect(css).toMatch(/\[data-app-shell\]\[data-mac-bay="expanded"\] \[data-sidebar-root\]::before,\s*\[data-admin-shell\]\[data-mac-chrome="windowed"\] \[data-admin-sidebar\]::before \{[^}]*height: 44px;\s*-webkit-app-region: drag;/);
  });
});
