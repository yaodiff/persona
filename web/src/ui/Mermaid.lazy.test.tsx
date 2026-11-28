// @vitest-environment jsdom
/// <reference types="node" />
// A reply's ```mermaid block is drawn by Mermaid.tsx, which with lib/mermaid.ts and the words only they read is not on
// the first screen: Markdown loads it with the first block it draws, showing the block's source dimmed meanwhile.
import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/mermaid", () => ({
  cachedMermaid: () => '<svg data-drawn=""></svg>',
  renderMermaid: () => Promise.resolve('<svg data-drawn=""></svg>'),
  mermaidProblem: () => ({ summary: "", detail: "" }),
}));
import startup from "../i18n/startup-namespaces.json";
import { startupMatcher } from "../i18n/startup-keys";
import { t } from "../i18n";
import { Markdown, MermaidDiagram } from "./Markdown";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const keys = (source: string) => [...new Set(source.match(/(?:ui|lib)-mermaid\.[a-z_]+/g) ?? [])];
const text = "```mermaid\ngraph TD; A-->B\n```";

describe("the Mermaid block's chunk", () => {
  it("is loaded by Markdown, not imported by it", () => {
    const markdown = read("./Markdown.tsx");
    expect(markdown).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/Mermaid["']/m);
    expect(markdown).toMatch(/lazyPage\(\(\) => import\("\.\/Mermaid"\)\.then\(\(m\) => m\.MermaidBlock\)\)/);
  });

  it("takes the words only the diagram reads off the startup catalog", () => {
    const inStartup = startupMatcher(startup);
    const own = [...keys(read("./Mermaid.tsx")), ...keys(read("../lib/mermaid.ts"))];
    expect(own.length).toBeGreaterThan(5);
    for (const k of own) expect(inStartup(k), k).toBe(false);
  });

  it("shows the source dimmed until the chunk is in, then the diagram, and later blocks at once", async () => {
    expect(MermaidDiagram.ready()).toBe(false);
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<Markdown text={text} />));
    expect(host.querySelector("[data-mermaid] pre.opacity-60")?.textContent).toBe("graph TD; A-->B");
    expect(host.querySelector(`button[aria-label="${t("ui-mermaid.zoom")}"]`)).toBeNull();
    await vi.waitFor(async () => {
      await act(async () => {});
      expect(host.querySelector("[data-mermaid-diagram] svg[data-drawn]")).not.toBeNull();
    });
    expect(host.querySelector(`button[aria-label="${t("ui-mermaid.zoom")}"]`)).not.toBeNull();
    act(() => root.unmount());

    // the chunk is in: a block drawn now (a reply scrolled back into view) has its diagram on the first render
    const again = createRoot(host);
    act(() => again.render(<Markdown text={text} />));
    expect(host.querySelector("[data-mermaid-diagram] svg[data-drawn]")).not.toBeNull();
    expect(host.querySelector("pre.opacity-60")).toBeNull();
    act(() => again.unmount());
    host.remove();
  });
});
