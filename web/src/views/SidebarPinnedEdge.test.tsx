// @vitest-environment jsdom
// A sidebar heading pinned over its group's rows draws an edge under it (the web usability audit of 2026-11-14, audit
// item 51): a row half under "Persona 对话" read as text run into the heading. Only the innermost pinned heading draws
// it, and whether a heading is pinned comes from an IntersectionObserver over two marks per group, never a layout read.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Observed = { callback: (entries: unknown[]) => void; targets: Set<Element>; options?: IntersectionObserverInit };
const { intersections, sizes } = vi.hoisted(() => {
  const intersections: Observed[] = [];
  const sizes: Observed[] = [];
  globalThis.IntersectionObserver = class {
    o: Observed;
    constructor(callback: (entries: unknown[]) => void, options?: IntersectionObserverInit) { this.o = { callback, targets: new Set(), options }; intersections.push(this.o); }
    observe(el: Element) { this.o.targets.add(el); }
    unobserve(el: Element) { this.o.targets.delete(el); }
    disconnect() { this.o.targets.clear(); }
  } as unknown as typeof IntersectionObserver;
  globalThis.ResizeObserver = class {
    o: Observed;
    constructor(callback: (entries: unknown[]) => void) { this.o = { callback, targets: new Set() }; sizes.push(this.o); }
    observe(el: Element) { this.o.targets.add(el); }
    unobserve(el: Element) { this.o.targets.delete(el); }
    disconnect() { this.o.targets.clear(); }
  } as unknown as typeof ResizeObserver;
  return { intersections, sizes };
});
import { Section, SidebarGroup } from "./SidebarSection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  host.setAttribute("data-row-scroller", "");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const group = (name: string) => [...host.querySelectorAll<HTMLElement>("[data-sidebar-group]")].find((g) => g.querySelector(":scope > [data-sidebar-group-heading]")?.textContent?.startsWith(name))!;
const heading = (name: string) => group(name).querySelector<HTMLElement>(":scope > [data-sidebar-group-heading]")!;
/** The group's two marks: the one before its heading and the one after its rows. */
const marks = (name: string) => [...group(name).querySelectorAll<HTMLElement>(":scope > span > span")];
const watcher = () => intersections.find((o) => o.targets.size > 0)!;
/** The list's observer reports the mark above the list's top edge, in its view, or clipped out of it while within it. */
const report = (mark: Element, where: "above" | "in" | "clipped") => act(() => watcher().callback([{
  target: mark,
  isIntersecting: where === "in",
  boundingClientRect: where === "above" ? { top: -120, bottom: -119 } : { top: 300, bottom: 301 },
  rootBounds: { top: 0, bottom: 800 },
}]));
const edged = () => [...host.querySelectorAll("[data-sidebar-group-heading][data-edge]")].map((h) => h.textContent);

describe("a pinned sidebar heading (audit item 51)", () => {
  it("draws its edge while its group's top has gone under it and its end has not, and none at rest", () => {
    act(() => root.render(<Section title="Persona 对话" folded={false} onToggle={() => {}}><p>再显示 3 个</p></Section>));
    const [start, end] = marks("Persona 对话");
    report(start!, "in");
    report(end!, "in");
    expect(edged()).toEqual([]);
    // scrolled: the rows go under the heading, a row half hidden under it
    report(start!, "above");
    expect(heading("Persona 对话").hasAttribute("data-edge")).toBe(true);
    // scrolled on: the group's end pushes the heading out, nothing of it is pinned any more
    report(end!, "above");
    expect(edged()).toEqual([]);
    // and back
    report(end!, "in");
    report(start!, "in");
    expect(edged()).toEqual([]);
  });

  it("puts the edge under the innermost pinned heading only, and gives it back to the outer one when that is pushed out", () => {
    act(() => root.render(
      <SidebarGroup heading={<span>Agents</span>}>
        <Section title="天气 App 发 1.3 版" folded={false} onToggle={() => {}}><p>row</p></Section>
      </SidebarGroup>,
    ));
    const [outer] = marks("Agents");
    const [start, end] = marks("天气 App 发 1.3 版");
    report(outer!, "above");
    expect(edged()).toEqual(["Agents"]);
    // the to-do's heading sticks under the Agents toolbar: one edge, under it
    report(start!, "above");
    expect(edged()).toEqual(["天气 App 发 1.3 版"]);
    // its end pushes it out under the toolbar: the toolbar has the edge again
    report(end!, "above");
    expect(edged()).toEqual(["Agents"]);
  });

  it("gives the outer heading its edge back when a pinned inner group goes away", () => {
    const tree = (inner: boolean) => (
      <SidebarGroup heading={<span>Agents</span>}>
        {inner && <Section title="检查 PR #42" folded={false} onToggle={() => {}}><p>row</p></Section>}
      </SidebarGroup>
    );
    act(() => root.render(tree(true)));
    report(marks("Agents")[0]!, "above");
    report(marks("检查 PR #42")[0]!, "above");
    expect(edged()).toEqual(["检查 PR #42"]);
    act(() => root.render(tree(false)));
    expect(edged()).toEqual(["Agents"]);
  });

  it("does not take a mark clipped out of view inside the list (a fold opening) as pinned", () => {
    act(() => root.render(<Section title="Pages" folded={false} onToggle={() => {}}><p>row</p></Section>));
    report(marks("Pages")[0]!, "clipped");
    expect(edged()).toEqual([]);
  });

  it("raises its marks by where the heading sticks, the end one by the heading's height too, in a view open below the list", () => {
    act(() => root.render(<Section title="Pages" folded={false} onToggle={() => {}}><p>row</p></Section>));
    const [start, end] = marks("Pages");
    // the browser's layout reports the heading 24 px tall
    const h = heading("Pages");
    act(() => sizes.find((o) => o.targets.has(h))!.callback([{ target: h, borderBoxSize: [{ blockSize: 24, inlineSize: 260 }] }]));
    expect(start!.style.top).toBe("calc(-1 * (var(--sidebar-sticky-top, 0px) + 0px))");
    expect(end!.style.top).toBe("calc(-1 * (var(--sidebar-sticky-top, 0px) + 24px))");
    expect(start!.closest("[aria-hidden]")).not.toBeNull();
    // one observer for the list, rooted at it, whose view reaches far below it: a jump past the list is still reported
    const o = watcher();
    expect(o.options?.root).toBe(host);
    expect(o.options?.rootMargin).toMatch(/^0px 0px \d{6,}px 0px$/);
    expect([...o.targets]).toEqual([start, end]);
  });

  it("is drawn as the Persona row's divider: a 1 px line in the line color, under the heading", () => {
    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    const rule = /\.sidebar-group-heading\[data-edge\]::after\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(rule).toMatch(/height:\s*1px/);
    expect(rule).toMatch(/background:\s*var\(--line\)/);
    expect(rule).toMatch(/bottom:\s*0/);
  });
});
