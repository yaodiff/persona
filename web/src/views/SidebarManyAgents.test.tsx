// @vitest-environment jsdom
// The complete Agents list with hundreds of Agents (the sidebar and the phone's Agents page): a folded group mounts no
// rows, an open long group draws a bounded number of them (its first rows, its last, the parts near the view and the
// part holding the open row) under every grouping, By machine's workspaces too, a row keeps its element as the list
// changes around it, and a group heading's height comes from a ResizeObserver, never from a layout read on mount.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Observed = { callback: (entries: unknown[]) => void; targets: Set<Element> };
const { intersections, sizes } = vi.hoisted(() => {
  const intersections: Observed[] = [];
  const sizes: Observed[] = [];
  const observer = (list: Observed[]) => class {
    o: Observed;
    constructor(callback: (entries: unknown[]) => void) { this.o = { callback, targets: new Set() }; list.push(this.o); }
    observe(el: Element) { this.o.targets.add(el); }
    unobserve(el: Element) { this.o.targets.delete(el); }
    disconnect() { this.o.targets.clear(); }
  };
  globalThis.ResizeObserver = observer(sizes) as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = observer(intersections) as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  return { intersections, sizes };
});
import { buildMachineTree } from "../lib/herdr-tree";
import { buildTodoTree } from "../lib/todo-tree";
import type { Machine, Session } from "../lib/types";
import { SESSION_LIST_METRICS } from "../ui/list-metrics";
import { Section, SidebarGroup } from "./SidebarSection";
import { MachineSection, TodoTreeView } from "./SidebarTree";
import { partCount, WindowedRows } from "./WindowedRows";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { windowHead: HEAD, windowPart: PART } = SESSION_LIST_METRICS;
const at = new Date(Date.now() - 3 * 86_400_000).toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
// 400 Agents on no to-do, all done and seen, as the audit's hub had them
const many: Session[] = Array.from({ length: 400 }, (_, i) => ({ machine_id: "m1", session_id: `s${i}`, title: `Session ${i}`, cwd: "/code/app", status: "idle", state: "idle", updated_at: at }));
const stable = { reg: () => () => {}, hover: () => {}, go: () => () => {}, onRowMenu: () => {} };

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const agentLinks = () => [...host.querySelectorAll<HTMLAnchorElement>('a[href^="/s/"]')];
const parts = () => [...host.querySelectorAll<HTMLElement>("[data-rows-part]")];
const gaps = () => [...host.querySelectorAll<HTMLElement>("[data-rows-gap]")];
const gap = (i: number) => host.querySelector<HTMLElement>(`[data-rows-gap][data-rows-part="${i}"]`)!;
/** A drawn part's rows: the elements after its mark, up to the next part's mark or box or the list's end. */
const partRowEls = (i: number) => {
  const out: Element[] = [];
  for (let el = host.querySelector(`[data-rows-part="${i}"]:not([data-rows-gap])`)?.nextElementSibling; el && !el.matches("[data-rows-part], [data-rows-end]"); el = el.nextElementSibling) out.push(el);
  return out;
};
/** The list observer reports `el` within a screen of the view (0), above it (-1) or below it (1), as on a scroll. */
const seen = (el: Element, side: -1 | 0 | 1) => {
  const o = intersections.find((x) => x.targets.has(el))!;
  const box = side < 0 ? { top: -400, bottom: -300 } : { top: 3000, bottom: 3100 };
  act(() => o.callback([{ target: el, isIntersecting: side === 0, boundingClientRect: box, rootBounds: { top: 0, bottom: 2000 } }]));
};
/** Lays `el` out at `top`…`bottom` (jsdom has no layout) and has the size observer watching it report a change. */
const laidOut = (el: Element, top: number, bottom: number) => {
  Object.defineProperty(el, "getBoundingClientRect", { configurable: true, value: () => ({ top, bottom, left: 0, right: 300, width: 300, height: bottom - top, x: 0, y: top }) });
};
const resized = (el: Element) => {
  const o = sizes.find((x) => x.targets.has(el))!;
  act(() => o.callback([{ target: el }]));
};

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  host.setAttribute("data-row-scroller", "");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("a long list of Agents", () => {
  it("mounts no rows under a folded group", async () => {
    localStorage.setItem("fold:todo:none", "1");
    const tree = buildTodoTree(many, machines, [], { spaces: [], sessions: [] }, Date.now());
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="" {...stable} />));
    await settle();
    expect(host.querySelector('[data-tree-branch="todo:none"] button[aria-expanded]')!.getAttribute("aria-expanded")).toBe("false");
    // before, all 400 were there under the fold, inert
    expect(agentLinks()).toHaveLength(0);
    // opened, its rows come
    act(() => host.querySelector<HTMLButtonElement>('[data-tree-branch="todo:none"] button[aria-expanded]')!.click());
    await settle();
    expect(agentLinks().length).toBeGreaterThan(0);
  });

  it("draws a bounded number of an open group's rows, and keeps the rest's place", async () => {
    localStorage.setItem("fold:todo:none", "0");
    const tree = buildTodoTree(many, machines, [], { spaces: [], sessions: [] }, Date.now());
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="" {...stable} />));
    await settle();
    // the first rows and the last, a box for each part between: before, all 400 rows were drawn at once
    expect(agentLinks()).toHaveLength(HEAD + 1);
    expect(agentLinks()[0]!.textContent).toContain("Session 0");
    expect(parts()).toHaveLength(partCount(400));
    expect(gaps()).toHaveLength(partCount(400) - 2);
    // the last row comes after the boxes
    expect(gaps().at(-1)!.compareDocumentPosition(agentLinks()[HEAD]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(gaps().every((p) => p.childElementCount === 0 && Number.parseFloat(p.style.height) > 0)).toBe(true);
  });

  it("draws the open Agent's row and the parts beside it wherever it is", async () => {
    localStorage.setItem("fold:todo:none", "0");
    const tree = buildTodoTree(many, machines, [], { spaces: [], sessions: [] }, Date.now());
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="s:m1/s300" {...stable} />));
    await settle();
    const shown = agentLinks().map((a) => a.getAttribute("href"));
    expect(shown).toContain("/s/m1/s300");
    expect(shown.length).toBeLessThanOrEqual(HEAD + 3 * PART + 1);
    // a herdr Agent's page moves on to its tab's address, which no row here holds: its row stays for the second
    // scroll to it
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="t:m1/w1/t1" {...stable} />));
    await settle();
    expect(agentLinks().map((a) => a.getAttribute("href"))).toContain("/s/m1/s300");
  });

  it("keeps the part holding the keyboard's focus and the next one drawn", async () => {
    localStorage.setItem("fold:todo:none", "0");
    const tree = buildTodoTree(many, machines, [], { spaces: [], sessions: [] }, Date.now());
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="" {...stable} />));
    await settle();
    // Tab reaches the last of the first rows: the next part is there for the next Tab
    const last = agentLinks()[HEAD - 1]!;
    act(() => last.focus());
    expect(agentLinks()).toHaveLength(HEAD + PART + 1);
    expect(document.activeElement).toBe(last);
  });

  it("draws a By machine workspace with hundreds of tabs a part at a time", async () => {
    // the audit's hub: every Agent a tab of one herdr workspace
    const tabs: Session[] = many.map((s, i) => ({ ...s, space_kind: "herdr", space_id: "w1", space_name: "fixture", space_index: 0, slot_id: `w1:t${i}`, slot_index: i, slot_name: String(i), tab_id: `w1:t${i}`, pane_id: `w1:p${i}` }));
    const [m] = buildMachineTree(machines, tabs);
    expect(m!.spaces[0]!.tabs).toHaveLength(400);
    // a workspace of quiet Agents starts folded: this one is open
    localStorage.setItem(`fold:${m!.spaces[0]!.foldKey}`, "0");
    const tabLinks = () => [...host.querySelectorAll<HTMLAnchorElement>('a[href^="/t/"]')];
    const draw = (activeKey: string) => act(() => root.render(<MachineSection m={m!} machines={machines} activeKey={activeKey} mutes={{ spaces: [], sessions: [], allowed: [] }} onSpaceMenu={() => {}} onNodeMenu={() => {}} {...stable} />));
    draw("");
    await settle();
    // before, its 400 tabs were drawn at once whatever the grouping's windowing did elsewhere
    expect(tabLinks()).toHaveLength(HEAD + 1);
    expect(gaps()).toHaveLength(partCount(400) - 2);
    // the open Agent deep in it is drawn, with the parts beside it
    draw("s:m1/s300");
    await settle();
    expect(tabLinks().some((a) => a.textContent?.includes("Session 300"))).toBe(true);
    expect(tabLinks().length).toBeLessThanOrEqual(HEAD + 3 * PART + 1);
  });
});

describe("WindowedRows", () => {
  const rows = (n: number) => Array.from({ length: n }, (_, i) => i);
  const render = (i: number) => <a key={i} href={`#${i}`} data-row={i}>row {i}</a>;
  const drawn = () => [...host.querySelectorAll<HTMLElement>("[data-row]")].map((el) => Number(el.dataset.row));
  const row = (i: number) => host.querySelector<HTMLAnchorElement>(`[data-row="${i}"]`);

  it("draws a short list as it always was", () => {
    act(() => root.render(<WindowedRows items={rows(HEAD + PART)} render={render} />));
    expect(drawn()).toHaveLength(HEAD + PART);
    expect(parts()).toHaveLength(0);
  });

  it("draws a long list's parts in its first render, with no code to load first", () => {
    // its own chunk once: a failed download (a redeploy under the page, offline) left the whole app blank
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    expect(drawn()).toEqual([...rows(HEAD), 399]);
    expect(parts()).toHaveLength(partCount(400));
  });

  it("draws a part while it is near the view, and keeps its height once it is not", async () => {
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    await settle();
    seen(gap(3), 0);
    const third = rows(PART).map((i) => HEAD + 2 * PART + i);
    expect(drawn()).toEqual([...rows(HEAD), ...third, 399]);
    const els = partRowEls(3);
    expect(els).toHaveLength(PART);
    laidOut(els[0]!, 1000, 1020);
    laidOut(els[PART - 1]!, 1497, 1517);
    resized(els[4]!);
    seen(els[0]!, 1);
    seen(els[PART - 1]!, 1);
    expect(drawn()).toEqual([...rows(HEAD), 399]);
    expect(gap(3).style.height).toBe("517px");
  });

  it("keeps a part drawn while the view runs through it", async () => {
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    await settle();
    seen(gap(3), 0);
    const els = partRowEls(3);
    // its first row above the view, its last below: the rows in view are this part's
    seen(els[0]!, -1);
    seen(els[PART - 1]!, 1);
    expect(drawn()).toContain(HEAD + 2 * PART);
  });

  it("gives a part never drawn the first rows' average height", async () => {
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    await settle();
    const head = partRowEls(0);
    laidOut(head[0]!, 0, 30);
    laidOut(head[HEAD - 1]!, 900, 930);
    resized(head[0]!);
    expect(gap(5).style.height).toBe(`${PART * 31}px`);
  });

  it("always draws the list's last row, so the previous-row shortcut wraps round to it", () => {
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    expect(row(399)).not.toBeNull();
    expect(row(398)).toBeNull();
  });

  it("keeps a focused row's element and focus when an Agent arrives above it", async () => {
    const list = (extra: number[] = []) => [...extra, ...rows(80)];
    act(() => root.render(<WindowedRows items={list()} render={render} />));
    await settle();
    // the last of the first rows: one Agent more above moves it into the next part
    const focused = row(HEAD - 1)!;
    act(() => focused.focus());
    act(() => root.render(<WindowedRows items={list([-1])} render={render} />));
    await settle();
    expect(row(HEAD - 1)).toBe(focused);
    expect(document.activeElement).toBe(focused);
    // and back out of it when that Agent leaves
    act(() => root.render(<WindowedRows items={list()} render={render} />));
    await settle();
    expect(row(HEAD - 1)).toBe(focused);
    expect(document.activeElement).toBe(focused);
  });

  it("keeps a focused row's element and focus when the list grows past the bound", async () => {
    act(() => root.render(<WindowedRows items={rows(HEAD + PART)} render={render} />));
    await settle();
    const focused = row(10)!;
    act(() => focused.focus());
    act(() => root.render(<WindowedRows items={rows(HEAD + PART + 1)} render={render} />));
    await settle();
    expect(parts().length).toBeGreaterThan(0);
    expect(row(10)).toBe(focused);
    expect(document.activeElement).toBe(focused);
  });

  it("draws only the parts holding the rows it had when it grows far past the bound in one update", async () => {
    act(() => root.render(<WindowedRows items={rows(10)} render={render} />));
    await settle();
    act(() => root.render(<WindowedRows items={rows(400)} render={render} />));
    // its first rows (holding the ten it had) and its last, not all 400 for a commit; the observer adds the rest
    expect(drawn()).toEqual([...rows(HEAD), 399]);
    await settle();
    expect(drawn()).toEqual([...rows(HEAD), 399]);
  });

  it("keeps the rows it had, and the focus among them, when hundreds arrive above them", async () => {
    act(() => root.render(<WindowedRows items={rows(10)} render={render} />));
    await settle();
    const focused = row(5)!;
    act(() => focused.focus());
    // 390 Agents come in above the ten: those are now the list's last rows
    const grown = [...Array.from({ length: 390 }, (_, i) => 1000 + i), ...rows(10)];
    act(() => root.render(<WindowedRows items={grown} render={render} />));
    await settle();
    expect(row(5)).toBe(focused);
    expect(document.activeElement).toBe(focused);
    for (const i of rows(10)) expect(row(i)).not.toBeNull();
    expect(drawn().length).toBeLessThanOrEqual(HEAD + 3 * PART + 1);
  });
});

describe("a group heading", () => {
  it("takes its height from the observer, with no layout read when it mounts", () => {
    const read = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect");
    act(() => root.render(
      <SidebarGroup heading={<span>Agents</span>}>
        <Section title="Not on a to-do" folded={false} onToggle={() => {}}><p>row</p></Section>
      </SidebarGroup>,
    ));
    expect(read).not.toHaveBeenCalled();
    const [outer, inner] = [...host.querySelectorAll<HTMLElement>("[data-sidebar-group-heading]")];
    expect(inner!.style.top).toBe("calc(var(--sidebar-sticky-top, 0px) + 0px)");
    // the browser's layout reports the outer heading's height: the nested heading sticks under it
    const o = sizes.find((x) => x.targets.has(outer!))!;
    act(() => o.callback([{ target: outer, borderBoxSize: [{ blockSize: 36, inlineSize: 300 }] }]));
    expect(inner!.style.top).toBe("calc(var(--sidebar-sticky-top, 0px) + 36px)");
    read.mockRestore();
  });
});
