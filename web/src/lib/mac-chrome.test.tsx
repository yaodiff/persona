// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, useCallback, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MacBayTools, MacMainContour } from "../views/MacChrome";
import { getLayout, setSidebarCollapsed, toggleMaximized } from "./layout";
import { appleG2Contour, appleG2ContourAt, macHeaderInset, sidebarBayCutout, useMacChrome, useMacChromeMode } from "./mac-chrome";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("macOS unified titlebar bridge & Apple G2 contour", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    setSidebarCollapsed(false);
    if (getLayout().maximized) toggleMaximized();
    vi.unstubAllGlobals();
  });

  it("defaults safely outside macOS desktop and subscribes when bridge is present", () => {
    function Probe() {
      const c = useMacChrome();
      return <span data-u={String(c.unifiedTitlebar)} data-fs={String(c.fullScreen)} data-f={String(c.focused)} />;
    }
    act(() => root.render(<Probe />));
    expect({ ...host.querySelector("span")!.dataset }).toEqual({ u: "false", fs: "false", f: "true" });

    let emit: ((next: { unifiedTitlebar: boolean; fullScreen: boolean; focused: boolean }) => void) | undefined;
    const off = vi.fn();
    vi.stubGlobal("window", Object.assign(window, {
      personaDesktop: {
        windowChrome: () => ({ unifiedTitlebar: true, fullScreen: false, focused: true }),
        onWindowChromeChange: (cb: typeof emit) => { emit = cb; return off; },
      },
    }));

    act(() => root.unmount());
    root = createRoot(host);
    act(() => root.render(<Probe />));
    expect({ ...host.querySelector("span")!.dataset }).toEqual({ u: "true", fs: "false", f: "true" });

    act(() => emit?.({ unifiedTitlebar: true, fullScreen: true, focused: false }));
    expect({ ...host.querySelector("span")!.dataset }).toEqual({ u: "true", fs: "true", f: "false" });
  });

  it("useMacChromeMode ignores focus-only updates so the shell does not re-render on window focus/blur", () => {
    let emit: ((next: { unifiedTitlebar: boolean; fullScreen: boolean; focused: boolean }) => void) | undefined;
    let renders = 0;
    vi.stubGlobal("window", Object.assign(window, {
      personaDesktop: {
        windowChrome: () => ({ unifiedTitlebar: true, fullScreen: false, focused: true }),
        onWindowChromeChange: (cb: typeof emit) => { emit = cb; return () => {}; },
      },
    }));
    function ModeProbe() {
      renders += 1;
      return <span data-mode={useMacChromeMode() ?? "none"} />;
    }
    act(() => root.render(<ModeProbe />));
    expect(renders).toBe(1);
    expect(host.querySelector("span")!.dataset.mode).toBe("windowed");

    act(() => emit?.({ unifiedTitlebar: true, fullScreen: false, focused: false }));
    expect(renders).toBe(1);

    act(() => emit?.({ unifiedTitlebar: true, fullScreen: true, focused: false }));
    expect(renders).toBe(2);
    expect(host.querySelector("span")!.dataset.mode).toBe("fullscreen");
  });

  it("computes continuous bay cutouts, header insets, and G2 contours across sidebar transition widths", () => {
    expect(sidebarBayCutout(284)).toBe(0);
    expect(sidebarBayCutout(168)).toBe(0);
    expect(sidebarBayCutout(56)).toBe(112);
    expect(sidebarBayCutout(0)).toBe(166);
    expect(macHeaderInset(0)).toBe(16);
    expect(macHeaderInset(112)).toBe(146);
    expect(macHeaderInset(166)).toBe(200);
    expect(appleG2ContourAt(900, 600, 0)).toEqual(appleG2Contour(900, 600, null));
    expect(appleG2ContourAt(900, 600, 112)).toEqual(appleG2Contour(900, 600, "rail"));
    expect(appleG2ContourAt(900, 600, 166)).toEqual(appleG2Contour(900, 600, "hidden"));
    const mid = appleG2ContourAt(900, 600, 12);
    expect(mid.clip).toContain("30,0 ");
    expect(mid.stroke).toContain("30.25,0.5 ");
  });

  it("builds 44 px co-linear G2 curves for rail and hidden bays and a clean rect otherwise", () => {
    const plain = appleG2Contour(900, 600, null);
    expect(plain.clip.startsWith("M 14,0 ")).toBe(true);

    const rail = appleG2Contour(900, 600, "rail");
    expect(rail.clip.startsWith("M 134,0 ")).toBe(true);
    expect(rail.clip).toContain("14,44 H 102 C 113.5,44");
    expect(rail.stroke).toContain("14,44.5 H 102.5");

    const hidden = appleG2Contour(900, 600, "hidden");
    expect(hidden.clip.startsWith("M 188,0 ")).toBe(true);
    expect(hidden.clip).toContain("14,44 H 156");
  });

  it("clips <main> immediately on initial mount in rail mode (Cmd+R reload regression)", () => {
    function ShellProbe() {
      const [mainEl, setMainEl] = useState<HTMLElement | null>(null);
      const setRef = useCallback((el: HTMLElement | null) => {
        if (el) {
          Object.defineProperty(el, "clientWidth", { value: 900, configurable: true });
          Object.defineProperty(el, "clientHeight", { value: 600, configurable: true });
        }
        setMainEl(el);
      }, []);
      return (
        <main ref={setRef}>
          <MacMainContour mainEl={mainEl} bay="rail" />
        </main>
      );
    }
    act(() => root.render(<ShellProbe />));
    const main = host.querySelector("main")!;
    expect(main.style.clipPath).toContain("M 134,0 ");
    expect(host.querySelector("[data-mac-contour]")).not.toBeNull();
  });

  it("places divider, collapse, and search buttons inside MacBayTools across expanded, rail, and hidden states", () => {
    const onSearch = vi.fn();
    setSidebarCollapsed(false);
    act(() => root.render(<MacBayTools bay="expanded" onSearch={onSearch} />));
    const expandedButtons = host.querySelectorAll("button");
    expect(expandedButtons).toHaveLength(2);
    expect(expandedButtons[0]!.getAttribute("aria-label")).toBe("收起侧边栏");
    expect(expandedButtons[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(expandedButtons[1]!.getAttribute("aria-label")).toBe("搜索会话");
    const tools = host.querySelector<HTMLElement>("[data-mac-bay-tools]")!;
    expect(tools.style.left).toBe("86px");
    expect(tools.style.top).toBe("11.5px");
    expect(tools.querySelector(".w-px")).not.toBeNull();

    act(() => expandedButtons[0]!.click());
    expect(getLayout().sidebarCollapsed).toBe(true);
    act(() => expandedButtons[1]!.click());
    expect(onSearch).toHaveBeenCalledTimes(1);
  });

  it("expands from rail and exits maximized hidden state when MacBayTools expand button is clicked", () => {
    const onSearch = vi.fn();
    setSidebarCollapsed(true);
    act(() => root.render(<MacBayTools bay="rail" onSearch={onSearch} />));
    const buttons = host.querySelectorAll("button");
    expect(buttons).toHaveLength(2);
    act(() => buttons[0]!.click());
    expect(getLayout().sidebarCollapsed).toBe(false);
    expect(getLayout().maximized).toBe(false);

    setSidebarCollapsed(true);
    toggleMaximized();
    expect(getLayout().maximized).toBe(true);
    act(() => root.render(<MacBayTools bay="hidden" onSearch={onSearch} />));
    act(() => host.querySelectorAll("button")[0]!.click());
    expect(getLayout().maximized).toBe(false);
    expect(getLayout().sidebarCollapsed).toBe(false);

    act(() => host.querySelectorAll("button")[1]!.click());
    expect(onSearch).toHaveBeenCalledTimes(1);
  });

  it("marks [data-mac-bay-tools] and descendants as non-draggable in styles.css", () => {
    const css = readFileSync(resolve(import.meta.dirname, "../styles.css"), "utf8");
    expect(css).toMatch(/\[data-app-shell\] \[data-mac-bay-tools\],\s*\[data-app-shell\] \[data-mac-bay-tools\] \* \{\s*-webkit-app-region: no-drag;\s*app-region: no-drag;/);
  });
});
