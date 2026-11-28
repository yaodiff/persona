// @vitest-environment node
/// <reference types="node" />
import { resolve } from "node:path";
import type { Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import tailwindcss from "@tailwindcss/vite";
import { build } from "vite";
import { textScale } from "../lib/text-scale";

interface JsdomInstance {
  window: Window & typeof globalThis & { close: () => void };
}

interface CssRuleRecord {
  selector: string;
  inLayer: boolean;
  media: string[];
  order: number;
  props: Record<string, { value: string; important: boolean }>;
}

type Specificity = [number, number, number];

function splitTopLevel(input: string, delimiter = ","): string[] {
  const parts: string[] = [];
  let start = 0;
  let paren = 0;
  let bracket = 0;
  let quote: string | null = null;
  for (let i = 0; i < input.length; i++) {
    const ch = input[i]!;
    if (ch === "\\" && i + 1 < input.length) {
      i++;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (ch === "(") paren++;
    else if (ch === ")" && paren > 0) paren--;
    else if (ch === "[") bracket++;
    else if (ch === "]" && bracket > 0) bracket--;
    else if (ch === delimiter && paren === 0 && bracket === 0) {
      const part = input.slice(start, i).trim();
      if (part) parts.push(part);
      start = i + 1;
    }
  }
  const tail = input.slice(start).trim();
  if (tail) parts.push(tail);
  return parts;
}

function stripCssComments(css: string): string {
  let out = "";
  let quote: string | null = null;
  for (let i = 0; i < css.length; i++) {
    const ch = css[i]!;
    if (ch === "\\" && i + 1 < css.length) {
      out += ch + css[++i]!;
      continue;
    }
    if (quote) {
      out += ch;
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      out += ch;
      continue;
    }
    if (ch === "/" && css[i + 1] === "*") {
      const close = css.indexOf("*/", i + 2);
      i = close < 0 ? css.length : close + 1;
      continue;
    }
    out += ch;
  }
  return out;
}

function parseDeclarations(body: string): CssRuleRecord["props"] {
  const props: CssRuleRecord["props"] = {};
  for (const rawDecl of splitTopLevel(body, ";")) {
    const colon = rawDecl.indexOf(":");
    if (colon <= 0) continue;
    const prop = rawDecl.slice(0, colon).trim().toLowerCase();
    let val = rawDecl.slice(colon + 1).trim();
    const impMatch = /\s*!important\s*$/i.exec(val);
    const important = Boolean(impMatch);
    if (impMatch) val = val.slice(0, impMatch.index).trim();
    props[prop] = { value: val, important };
  }
  return props;
}

function parseCssRules(cssText: string): CssRuleRecord[] {
  const clean = stripCssComments(cssText);
  const rules: CssRuleRecord[] = [];
  let order = 0;

  function findBlockEnd(input: string, openBrace: number): number {
    let depth = 0;
    let quote: string | null = null;
    for (let i = openBrace; i < input.length; i++) {
      const ch = input[i]!;
      if (ch === "\\" && i + 1 < input.length) {
        i++;
        continue;
      }
      if (quote) {
        if (ch === quote) quote = null;
        continue;
      }
      if (ch === '"' || ch === "'") quote = ch;
      else if (ch === "{") depth++;
      else if (ch === "}" && --depth === 0) return i;
    }
    return input.length - 1;
  }

  function walk(block: string, inLayer: boolean, media: string[]) {
    let i = 0;
    while (i < block.length) {
      while (i < block.length && /[\s;]/.test(block[i]!)) i++;
      if (i >= block.length) break;

      let quote: string | null = null;
      let headerEnd = i;
      let term = "";
      while (headerEnd < block.length) {
        const ch = block[headerEnd]!;
        if (ch === "\\" && headerEnd + 1 < block.length) {
          headerEnd += 2;
          continue;
        }
        if (quote) {
          if (ch === quote) quote = null;
          headerEnd++;
          continue;
        }
        if (ch === '"' || ch === "'") {
          quote = ch;
          headerEnd++;
          continue;
        }
        if (ch === "{" || ch === ";") {
          term = ch;
          break;
        }
        headerEnd++;
      }
      if (!term) break;
      const prelude = block.slice(i, headerEnd).trim();
      if (term === ";") {
        i = headerEnd + 1;
        continue;
      }
      const closeBrace = findBlockEnd(block, headerEnd);
      const inner = block.slice(headerEnd + 1, closeBrace);
      if (prelude.startsWith("@layer")) {
        walk(inner, true, media);
      } else if (prelude.startsWith("@media")) {
        const cond = prelude.slice("@media".length).trim().replace(/\s+/g, " ");
        walk(inner, inLayer, [...media, cond]);
      } else if (!prelude.startsWith("@")) {
        const props = parseDeclarations(inner);
        if (Object.keys(props).length > 0) {
          rules.push({ selector: prelude, inLayer, media, order: order++, props });
        }
      }
      i = closeBrace + 1;
    }
  }

  walk(clean, false, []);
  return rules;
}

function compareSpecificity(a: Specificity, b: Specificity): number {
  return a[0] - b[0] || a[1] - b[1] || a[2] - b[2];
}

function maxSpecificity(selectorList: string): Specificity {
  let best: Specificity = [0, 0, 0];
  for (const part of splitTopLevel(selectorList)) {
    const spec = selectorSpecificity(part);
    if (compareSpecificity(spec, best) > 0) best = spec;
  }
  return best;
}

function readBalanced(input: string, openAt: number, openChar: string, closeChar: string): number {
  let depth = 0;
  let quote: string | null = null;
  for (let i = openAt; i < input.length; i++) {
    const ch = input[i]!;
    if (ch === "\\" && i + 1 < input.length) {
      i++;
      continue;
    }
    if (quote) {
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (ch === openChar) depth++;
    else if (ch === closeChar && --depth === 0) return i;
  }
  return input.length - 1;
}

function readIdent(input: string, from: number): number {
  let i = from;
  while (i < input.length) {
    const ch = input[i]!;
    if (ch === "\\" && i + 1 < input.length) {
      i += 2;
      continue;
    }
    if (/[\s>+~.#[:()*,]/.test(ch)) break;
    i++;
  }
  return i;
}

function selectorSpecificity(selector: string): Specificity {
  let a = 0;
  let b = 0;
  let c = 0;
  let i = 0;
  while (i < selector.length) {
    const ch = selector[i]!;
    if (/[\s>+~*]/.test(ch)) {
      i++;
      continue;
    }
    if (ch === "[") {
      b++;
      i = readBalanced(selector, i, "[", "]") + 1;
      continue;
    }
    if (ch === "#") {
      a++;
      i = readIdent(selector, i + 1);
      continue;
    }
    if (ch === ".") {
      b++;
      i = readIdent(selector, i + 1);
      continue;
    }
    if (ch === ":") {
      if (selector[i + 1] === ":") {
        c++;
        i = readIdent(selector, i + 2);
        if (selector[i] === "(") i = readBalanced(selector, i, "(", ")") + 1;
        continue;
      }
      const end = readIdent(selector, i + 1);
      const name = selector.slice(i + 1, end).toLowerCase();
      if (selector[end] === "(") {
        const close = readBalanced(selector, end, "(", ")");
        const inner = selector.slice(end + 1, close);
        if (name === "is" || name === "not" || name === "has") {
          const [ia, ib, ic] = maxSpecificity(inner);
          a += ia;
          b += ib;
          c += ic;
        } else if (name !== "where") {
          b++;
        }
        i = close + 1;
      } else {
        b++;
        i = end;
      }
      continue;
    }
    c++;
    i = readIdent(selector, i);
  }
  return [a, b, c];
}

const normalizeMedia = (cond: string) => cond.replace(/\s+/g, "").toLowerCase();

/** Resolves CSS cascade winner for a CSS property on a DOM node using compiled Vite CSS + jsdom `matches()`. */
function resolvePropCascade(
  el: Element,
  propName: string,
  rules: readonly CssRuleRecord[],
  activeMedia: readonly string[] = [],
): string | null {
  const prop = propName.toLowerCase();
  const activeSet = new Set(activeMedia.map(normalizeMedia));
  const candidates: { importance: number; layer: number; spec: Specificity; order: number; value: string }[] = [];

  for (const rule of rules) {
    const decl = rule.props[prop];
    if (!decl) continue;
    if (rule.media.some((m) => !activeSet.has(normalizeMedia(m)))) continue;
    for (const part of splitTopLevel(rule.selector)) {
      if (part.includes("::")) continue;
      let matched = false;
      try {
        matched = el.matches(part);
      } catch {
        continue;
      }
      if (!matched) continue;
      candidates.push({
        importance: decl.important ? 1 : 0,
        layer: decl.important ? (rule.inLayer ? 2 : 1) : (rule.inLayer ? 0 : 1),
        spec: selectorSpecificity(part),
        order: rule.order,
        value: decl.value,
      });
    }
  }

  candidates.sort((x, y) =>
    (x.importance - y.importance) ||
    (x.layer - y.layer) ||
    compareSpecificity(x.spec, y.spec) ||
    (x.order - y.order));
  return candidates.at(-1)?.value ?? null;
}

const resolveBackdropCascade = (
  el: Element,
  rules: readonly CssRuleRecord[],
  activeMedia: readonly string[] = [],
) => ({
  backdropFilter: resolvePropCascade(el, "backdrop-filter", rules, activeMedia),
  webkitBackdropFilter: resolvePropCascade(el, "-webkit-backdrop-filter", rules, activeMedia),
});

const DOM_GLOBALS = [
  "window",
  "document",
  "Element",
  "HTMLElement",
  "HTMLDivElement",
  "HTMLButtonElement",
  "HTMLInputElement",
  "HTMLTextAreaElement",
  "Node",
  "NodeFilter",
  "Text",
  "DocumentFragment",
  "DOMRect",
  "Event",
  "CustomEvent",
  "MouseEvent",
  "KeyboardEvent",
  "MutationObserver",
  "getComputedStyle",
  "localStorage",
  "addEventListener",
  "removeEventListener",
  "requestAnimationFrame",
  "cancelAnimationFrame",
] as const;

let cssRules: CssRuleRecord[] = [];
let dom: JsdomInstance;
let savedGlobals: Map<string, PropertyDescriptor | undefined>;
let act: typeof import("react").act;
let createRoot: typeof import("react-dom/client").createRoot;
let closePanel: typeof import("../lib/panel").closePanel;
let openPanel: typeof import("../lib/panel").openPanel;
let SidePanel: typeof import("../views/SidePanel").SidePanel;
let Dialog: typeof import("./atoms").Dialog;
let applyLook: typeof import("./look").applyLook;
let setSurfaceStyle: typeof import("./look").setSurfaceStyle;
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  // Compile src/styles.css in pure Node before installing DOM globals so esbuild's Uint8Array realm invariant holds.
  const webRoot = resolve(import.meta.dirname, "../..");
  const stylesPath = resolve(webRoot, "src/styles.css");
  const built = await build({
    root: webRoot,
    configFile: false,
    logLevel: "silent",
    plugins: [tailwindcss()],
    css: { postcss: { plugins: [textScale()] } },
    build: {
      write: false,
      emptyOutDir: false,
      cssMinify: true,
      // Match Vite's cssTarget preserving -webkit-backdrop-filter alongside backdrop-filter.
      cssTarget: ["chrome87", "safari14"],
      rollupOptions: { input: stylesPath },
    },
  });
  const outputs = Array.isArray(built) ? built : [built];
  let compiledCss = "";
  for (const out of outputs) {
    if (!("output" in out)) continue;
    for (const item of out.output) {
      if (item.type === "asset" && item.fileName.endsWith(".css")) {
        compiledCss = typeof item.source === "string" ? item.source : Buffer.from(item.source).toString("utf8");
      }
    }
  }
  expect(compiledCss.length).toBeGreaterThan(50_000);
  cssRules = parseCssRules(compiledCss);
  expect(cssRules.length).toBeGreaterThan(200);

  const jsdomModule = "jsdom";
  const { JSDOM } = (await import(jsdomModule)) as {
    JSDOM: new (html: string, opts?: { url?: string }) => JsdomInstance;
  };
  dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://127.0.0.1/" });
  savedGlobals = new Map();
  const win = dom.window as unknown as Record<string, unknown>;
  for (const key of DOM_GLOBALS) {
    savedGlobals.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, {
      configurable: true,
      writable: true,
      value: win[key] ?? (key === "window" ? dom.window : undefined),
    });
  }
  Object.defineProperty(globalThis, "IS_REACT_ACT_ENVIRONMENT", { configurable: true, writable: true, value: true });

  ({ act } = await import("react"));
  ({ createRoot } = await import("react-dom/client"));
  ({ closePanel, openPanel } = await import("../lib/panel"));
  ({ SidePanel } = await import("../views/SidePanel"));
  ({ Dialog } = await import("./atoms"));
  ({ applyLook, setSurfaceStyle } = await import("./look"));
}, 30_000);

beforeEach(() => {
  localStorage.clear();
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  applyLook();
});

afterEach(() => {
  act(() => closePanel());
  act(() => root.unmount());
  host.remove();
  delete (window as { personaDesktop?: unknown }).personaDesktop;
  delete document.documentElement.dataset.composerStage;
  document.documentElement.classList.remove("dark");
  localStorage.clear();
  applyLook();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

afterAll(() => {
  if (dom) {
    delete document.documentElement.dataset.surfaceStyle;
    delete document.documentElement.dataset.desktopGlass;
    delete document.documentElement.dataset.composerStage;
    document.documentElement.classList.remove("dark");
    document.documentElement.style.removeProperty("--glass-t");
    dom.window.close();
  }
  if (savedGlobals) {
    for (const [key, desc] of savedGlobals) {
      if (desc) Object.defineProperty(globalThis, key, desc);
      else delete (globalThis as Record<string, unknown>)[key];
    }
  }
});

function renderShellWithOverlays() {
  act(() => openPanel({ kind: "agent", mid: "m1", sid: "s1" }));
  act(() => {
    root.render(
      <>
        <div data-app-shell="">
          <div data-sidebar-column="">
            <div role="separator" data-probe="sidebar-separator" />
          </div>
          <main>
            <div className="min-h-0">
              <div>
                <div>
                  <header data-probe="main-header" className="h-14 backdrop-blur-md">
                    <h1 data-probe="main-title" data-chat-title="">Main Chat</h1>
                  </header>
                  <header data-probe="terminal-header" data-terminal-header="">
                    <span>Terminal</span>
                  </header>
                </div>
                <div>
                  <header data-probe="nested-terminal-header" data-terminal-header="" />
                </div>
              </div>
            </div>
            <div data-probe="main-composer" className="backdrop-blur-md" />
            <div data-probe="main-drop-hint" className="backdrop-blur-[2px]" />
            <div data-probe="main-sticky" className="backdrop-blur-sm" />
            <div data-probe="main-diff" className="backdrop-blur" />
            <div data-probe="admin-devices" className="backdrop-blur-[2px]" />
          </main>
          <SidePanel
            deps={{
              narrow: false,
              maximized: false,
              canGoBack: false,
              width: 480,
              room: 1280,
              closeOnBack: false,
              renderPanel: () => (
                <>
                  <header data-probe="aside-header" className="h-14 backdrop-blur-md">
                    <h1 data-probe="aside-title" data-chat-title="">Panel Chat</h1>
                  </header>
                  <div data-subagent-panel="m1/sub1">
                    <header data-probe="subagent-header">
                      <div data-probe="subagent-top-row">
                        <button type="button" data-probe="subagent-close">Close</button>
                      </div>
                      <div data-probe="subagent-second-row" />
                    </header>
                  </div>
                  <div data-probe="aside-composer" className="backdrop-blur-md" />
                  <div data-probe="aside-drop-hint" className="backdrop-blur-[2px]" />
                  <div data-probe="aside-diff" className="backdrop-blur" />
                </>
              ),
            }}
          />
        </div>
        <Dialog open onClose={() => {}} title="Dialog">
          <p>Modal content</p>
        </Dialog>
        <div data-probe="palette-backdrop" className="fixed inset-0 z-40 backdrop-blur-[2px]" />
        <div data-probe="image-viewer" className="fixed inset-0 z-50 backdrop-blur-sm" />
      </>,
    );
  });
}

describe("compiled CSS backdrop-filter suppression & macOS Frosted Glass rules (#17761, #17793)", () => {
  it("disables backdrop-filter and -webkit-backdrop-filter across <main>, <aside>, portals, and arbitrary utilities when data-desktop-glass=1", () => {
    Object.assign(window, {
      personaDesktop: { platform: "darwin", setSurfaceStyle: vi.fn(), setThemeSource: vi.fn() },
    });
    renderShellWithOverlays();
    act(() => setSurfaceStyle("liquid-glass"));

    expect(document.documentElement.dataset.surfaceStyle).toBe("liquid-glass");
    expect(document.documentElement.dataset.desktopGlass).toBe("1");

    const shell = host.querySelector("[data-app-shell]")!;
    const main = host.querySelector("[data-app-shell] > main")!;
    const aside = host.querySelector("[data-app-shell] > aside[data-side-panel]")!;
    expect(aside).not.toBeNull();

    for (const container of [shell, main, aside]) {
      const resolved = resolveBackdropCascade(container, cssRules);
      expect(resolved.backdropFilter).toBeNull();
      expect(resolved.webkitBackdropFilter).toBeNull();
    }

    const dialogBackdrop = document.body.querySelector<HTMLElement>('[role="dialog"]')?.parentElement;
    expect(dialogBackdrop).not.toBeNull();
    expect(dialogBackdrop!.closest("[data-app-shell]")).toBeNull();
    expect(dialogBackdrop!.className).toContain("backdrop-blur-[2px]");

    const backdropNodes = [...document.querySelectorAll('[class*="backdrop-"]')];
    expect(backdropNodes.length).toBeGreaterThanOrEqual(13);
    for (const node of backdropNodes) {
      const resolved = resolveBackdropCascade(node, cssRules);
      expect(resolved.backdropFilter, node.getAttribute("data-probe") ?? node.className).toBe("none");
      expect(resolved.webkitBackdropFilter, node.getAttribute("data-probe") ?? node.className).toBe("none");
    }
  });

  it("preserves Web-only Frosted Glass blur on [data-app-shell], > main, > aside, and overlays when data-desktop-glass is unset", () => {
    renderShellWithOverlays();
    act(() => setSurfaceStyle("liquid-glass"));

    expect(document.documentElement.dataset.surfaceStyle).toBe("liquid-glass");
    expect(document.documentElement.dataset.desktopGlass).toBeUndefined();

    const shell = host.querySelector("[data-app-shell]")!;
    const main = host.querySelector("[data-app-shell] > main")!;
    const aside = host.querySelector("[data-app-shell] > aside[data-side-panel]")!;

    for (const container of [shell, main, aside]) {
      const resolved = resolveBackdropCascade(container, cssRules);
      expect(resolved.backdropFilter).toContain("blur(var(--glass-blur))");
      expect(resolved.backdropFilter).toContain("saturate(var(--glass-sat))");
      expect(resolved.webkitBackdropFilter).toContain("blur(var(--glass-blur))");
    }

    for (const node of document.querySelectorAll('[class*="backdrop-"]')) {
      const resolved = resolveBackdropCascade(node, cssRules);
      expect(resolved.backdropFilter).toContain("var(--tw-backdrop-blur");
      expect(resolved.backdropFilter).not.toBe("none");
    }
  });

  it("keeps Default appearance without glass backdrop-filter", () => {
    renderShellWithOverlays();
    act(() => setSurfaceStyle("default"));

    expect(document.documentElement.dataset.surfaceStyle).toBeUndefined();
    expect(document.documentElement.dataset.desktopGlass).toBeUndefined();

    const shell = host.querySelector("[data-app-shell]")!;
    const main = host.querySelector("[data-app-shell] > main")!;
    const aside = host.querySelector("[data-app-shell] > aside[data-side-panel]")!;

    for (const container of [shell, main, aside]) {
      const resolved = resolveBackdropCascade(container, cssRules);
      expect(resolved.backdropFilter).toBeNull();
      expect(resolved.webkitBackdropFilter).toBeNull();
    }

    for (const node of document.querySelectorAll('[class*="backdrop-"]')) {
      const resolved = resolveBackdropCascade(node, cssRules);
      expect(resolved.backdropFilter).toContain("var(--tw-backdrop-blur");
      expect(resolved.webkitBackdropFilter).toContain("var(--tw-backdrop-blur");
    }
  });

  it("clears shell filter, backdrop-filter, and clip-path across Web and Desktop Frosted Glass when data-composer-stage=1", () => {
    renderShellWithOverlays();
    act(() => setSurfaceStyle("liquid-glass"));
    document.documentElement.classList.add("dark");

    const shell = host.querySelector<HTMLElement>("[data-app-shell]")!;
    const main = host.querySelector<HTMLElement>("[data-app-shell] > main")!;
    const aside = host.querySelector<HTMLElement>("[data-app-shell] > aside[data-side-panel]")!;
    shell.dataset.macChrome = "windowed";
    shell.dataset.macBay = "rail";

    expect(resolvePropCascade(main, "filter", cssRules)).toContain("drop-shadow");
    expect(resolvePropCascade(aside, "backdrop-filter", cssRules)).toContain("blur(var(--glass-blur))");

    document.documentElement.dataset.composerStage = "1";
    for (const target of [shell, main, aside]) {
      expect(resolvePropCascade(target, "filter", cssRules)).toBe("none");
      expect(resolvePropCascade(target, "backdrop-filter", cssRules)).toBe("none");
      expect(resolvePropCascade(target, "-webkit-backdrop-filter", cssRules)).toBe("none");
      expect(resolvePropCascade(target, "clip-path", cssRules)).toBe("none");
    }

    document.documentElement.dataset.desktopGlass = "1";
    for (const target of [shell, main, aside]) {
      expect(resolvePropCascade(target, "filter", cssRules)).toBe("none");
      expect(resolvePropCascade(target, "clip-path", cssRules)).toBe("none");
    }
  });

  it("keeps reduced-transparency backgrounds opaque and clears drop-shadow on both Desktop and Web Frosted Glass", () => {
    Object.assign(window, {
      personaDesktop: { platform: "darwin", setSurfaceStyle: vi.fn(), setThemeSource: vi.fn() },
    });
    renderShellWithOverlays();
    act(() => setSurfaceStyle("liquid-glass"));

    const shell = host.querySelector<HTMLElement>("[data-app-shell]")!;
    const main = host.querySelector<HTMLElement>("[data-app-shell] > main")!;
    const aside = host.querySelector<HTMLElement>("[data-app-shell] > aside[data-side-panel]")!;
    shell.dataset.macChrome = "windowed";
    shell.dataset.macBay = "rail";
    const reduced = ["(prefers-reduced-transparency: reduce)"];

    expect(resolvePropCascade(document.documentElement, "background", cssRules, reduced)).toBe("#f1f1f4");
    expect(resolvePropCascade(document.body, "background", cssRules, reduced)).toBe("var(--page)");
    expect(resolvePropCascade(main, "filter", cssRules, reduced)).toBe("none");

    document.documentElement.classList.add("dark");
    expect(resolvePropCascade(document.documentElement, "background", cssRules, reduced)).toBe("#0b0b0c");
    expect(resolvePropCascade(document.body, "background", cssRules, reduced)).toBe("var(--page)");
    expect(resolvePropCascade(main, "filter", cssRules, reduced)).toBe("none");

    // Web-only Frosted Glass (data-desktop-glass unset) must also clear blur, gradient bg, and filter under reduced transparency.
    delete (window as { personaDesktop?: unknown }).personaDesktop;
    document.documentElement.classList.remove("dark");
    act(() => applyLook());
    expect(document.documentElement.dataset.desktopGlass).toBeUndefined();

    expect(resolvePropCascade(document.documentElement, "background", cssRules, reduced)).toBe("#f1f1f4");
    expect(resolvePropCascade(document.body, "background", cssRules, reduced)).toBe("var(--page)");
    expect(resolvePropCascade(main, "filter", cssRules, reduced)).toBe("none");
    for (const container of [shell, main, aside]) {
      const resolved = resolveBackdropCascade(container, cssRules, reduced);
      expect(resolved.backdropFilter).toBe("none");
      expect(resolved.webkitBackdropFilter).toBe("none");
    }

    document.documentElement.classList.add("dark");
    expect(resolvePropCascade(document.documentElement, "background", cssRules, reduced)).toBe("#0b0b0c");
    expect(resolvePropCascade(document.body, "background", cssRules, reduced)).toBe("var(--page)");
    expect(resolvePropCascade(main, "filter", cssRules, reduced)).toBe("none");
    for (const container of [shell, main, aside]) {
      const resolved = resolveBackdropCascade(container, cssRules, reduced);
      expect(resolved.backdropFilter).toBe("none");
      expect(resolved.webkitBackdropFilter).toBe("none");
    }
  });

  it("configures macOS windowed drag/no-drag regions for chat titles, resize handles, TerminalPanel, and SubagentPanel headers", () => {
    renderShellWithOverlays();
    const shell = host.querySelector<HTMLElement>("[data-app-shell]")!;
    shell.dataset.macChrome = "windowed";
    shell.dataset.macBay = "rail";

    const wide = ["(min-width:64rem)"];
    const terminalHeader = host.querySelector('[data-probe="terminal-header"]')!;
    const nestedTerminalHeader = host.querySelector('[data-probe="nested-terminal-header"]')!;
    const mainTitle = host.querySelector('[data-probe="main-title"]')!;
    const asideTitle = host.querySelector('[data-probe="aside-title"]')!;
    const separator = host.querySelector('[data-probe="sidebar-separator"]')!;
    const subagentHeader = host.querySelector('[data-probe="subagent-header"]')!;
    const subagentTopRow = host.querySelector('[data-probe="subagent-top-row"]')!;
    const subagentClose = host.querySelector('[data-probe="subagent-close"]')!;

    expect(resolvePropCascade(terminalHeader, "height", cssRules, wide)).toBe("45px");
    expect(resolvePropCascade(terminalHeader, "-webkit-app-region", cssRules, wide)).toBe("drag");
    expect(resolvePropCascade(terminalHeader, "padding-left", cssRules, wide)).toBe("146px");
    expect(resolvePropCascade(nestedTerminalHeader, "padding-left", cssRules, wide)).toBe("1rem");

    shell.dataset.macBay = "hidden";
    expect(resolvePropCascade(terminalHeader, "padding-left", cssRules, wide)).toBe("200px");

    expect(resolvePropCascade(mainTitle, "-webkit-app-region", cssRules, wide)).toBe("no-drag");
    expect(resolvePropCascade(asideTitle, "-webkit-app-region", cssRules, wide)).toBe("no-drag");
    expect(resolvePropCascade(separator, "-webkit-app-region", cssRules, wide)).toBe("no-drag");
    expect(resolvePropCascade(separator, "app-region", cssRules, wide)).toBe("no-drag");

    expect(resolvePropCascade(subagentTopRow, "-webkit-app-region", cssRules, wide)).toBe("drag");
    expect(resolvePropCascade(subagentClose, "-webkit-app-region", cssRules, wide)).toBe("no-drag");
    expect(resolvePropCascade(subagentHeader, "height", cssRules, wide)).toBeNull();
  });
});
