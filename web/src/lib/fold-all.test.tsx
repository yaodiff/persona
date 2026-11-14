// @vitest-environment jsdom
// "Collapse all" / "Expand all" reaches the groups that are not drawn when it happens (#7440): a folded sidebar group
// drops its rows, and a long group draws only the parts near the view, so the groups in them hear no fold-all; they
// take the last one they missed when they are drawn again, unless they were set since.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Observed = { callback: (entries: unknown[]) => void; targets: Set<Element> };
const { intersections } = vi.hoisted(() => {
  const intersections: Observed[] = [];
  const observer = (list: Observed[]) => class {
    o: Observed;
    constructor(callback: (entries: unknown[]) => void) { this.o = { callback, targets: new Set() }; list.push(this.o); }
    observe(el: Element) { this.o.targets.add(el); }
    unobserve(el: Element) { this.o.targets.delete(el); }
    disconnect() { this.o.targets.clear(); }
  };
  globalThis.ResizeObserver = observer([]) as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = observer(intersections) as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  return { intersections };
});
import { Collapse } from "../ui/atoms";
import { SESSION_LIST_METRICS } from "../ui/list-metrics";
import { WindowedRows } from "../views/WindowedRows";
import { resetFoldAll } from "./fold-all";
import { setAllFolded } from "./sidebar-api";
import { FoldScope, useFolded } from "./use-folded";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const settle = async (ms = 0) => {
  await act(async () => { await new Promise((r) => setTimeout(r, ms)); });
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
// past a sidebar fold's 450 ms close, when it drops what is in it
const closed = () => settle(500);

/** A foldable group (a worktree, a to-do's spawned Agents, a tab's panes) and what it holds. */
function Group({ k, children }: { k: string; children?: React.ReactNode }) {
  const [folded, toggle] = useFolded(k, false);
  return (
    <div>
      <button type="button" data-group={k} aria-expanded={!folded} onClick={toggle}>{k}</button>
      <Collapse open={!folded} sidebarFold>{children}</Collapse>
    </div>
  );
}
const head = (k: string) => host.querySelector<HTMLButtonElement>(`[data-group="${k}"]`);
const open = (k: string) => head(k)?.getAttribute("aria-expanded");

beforeEach(() => {
  localStorage.clear();
  resetFoldAll();
  host = document.createElement("div");
  host.setAttribute("data-row-scroller", "");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("a fold-all and the groups under a folded one", () => {
  it("opens a nested group with Expand all after Collapse all", async () => {
    act(() => root.render(<Group k="repo"><Group k="worktree"><p>row</p></Group></Group>));
    expect(open("worktree")).toBe("true");
    act(() => setAllFolded(true));
    await closed();
    // the repository holds nothing now: its worktree is gone with its listener
    expect(open("repo")).toBe("false");
    expect(head("worktree")).toBeNull();
    act(() => setAllFolded(false));
    await settle();
    expect(open("repo")).toBe("true");
    // before, it came back folded: its stored fold was the Collapse all's
    expect(open("worktree")).toBe("true");
    expect(localStorage.getItem("fold:worktree")).toBe("0");
  });

  it("opens a group folded by hand under a folded one, as it opened mounted", async () => {
    act(() => root.render(<Group k="repo"><Group k="worktree"><p>row</p></Group></Group>));
    act(() => head("worktree")!.click());
    act(() => head("repo")!.click());
    await closed();
    act(() => setAllFolded(false));
    await settle();
    expect(open("worktree")).toBe("true");
  });

  it("folds groups two levels down", async () => {
    act(() => root.render(<Group k="machine"><Group k="space"><Group k="tab"><p>pane</p></Group></Group></Group>));
    act(() => head("machine")!.click());
    await closed();
    act(() => setAllFolded(true));
    // the machine opens by hand: what is under it takes the Collapse all it missed
    act(() => head("machine")!.click());
    await settle();
    expect(open("space")).toBe("false");
    act(() => head("space")!.click());
    await settle();
    expect(open("tab")).toBe("false");
  });

  it("keeps a choice made after the fold-all it missed", async () => {
    act(() => root.render(<Group k="repo"><Group k="worktree"><p>row</p></Group></Group>));
    act(() => head("repo")!.click());
    await closed();
    act(() => setAllFolded(true));
    act(() => head("repo")!.click());
    await settle();
    expect(open("worktree")).toBe("false");
    // opened by hand, then its parent folded and opened again: the old Collapse all is not taken a second time
    act(() => head("worktree")!.click());
    act(() => head("repo")!.click());
    await closed();
    act(() => head("repo")!.click());
    await settle();
    expect(open("worktree")).toBe("true");
  });

  it("leaves a group's own fold when it missed nothing", async () => {
    localStorage.setItem("fold:worktree", "1");
    act(() => root.render(<Group k="repo"><Group k="worktree"><p>row</p></Group></Group>));
    act(() => head("repo")!.click());
    await closed();
    act(() => head("repo")!.click());
    await settle();
    expect(open("worktree")).toBe("false");
  });

  it("does not take another block's fold-all", async () => {
    act(() => root.render(<FoldScope.Provider value="agents"><Group k="repo"><Group k="worktree"><p>row</p></Group></Group></FoldScope.Provider>));
    act(() => head("repo")!.click());
    await closed();
    act(() => setAllFolded(true, "pages"));
    act(() => head("repo")!.click());
    await settle();
    expect(open("worktree")).toBe("true");
    // the block's own does reach it
    act(() => head("repo")!.click());
    await closed();
    act(() => setAllFolded(true, "agents"));
    expect(open("repo")).toBe("false");
    act(() => head("repo")!.click());
    await settle();
    expect(open("worktree")).toBe("false");
  });
});

describe("a fold-all and the groups in a long list's parts out of view", () => {
  const { windowHead: HEAD, windowPart: PART } = SESSION_LIST_METRICS;
  const items = Array.from({ length: 400 }, (_, i) => `g${i}`);
  it("folds the groups of a part that comes into view after a Collapse all", async () => {
    act(() => root.render(<WindowedRows items={items} render={(k) => <Group key={k} k={k}><p>pane</p></Group>} />));
    await settle();
    const deep = `g${HEAD + 2 * PART}`;
    expect(head(deep)).toBeNull();
    act(() => setAllFolded(true));
    expect(open("g0")).toBe("false");
    // the browser reports part 3's box in view
    const box = host.querySelector('[data-rows-gap][data-rows-part="3"]')!;
    const o = intersections.find((x) => x.targets.has(box))!;
    act(() => o.callback([{ target: box, isIntersecting: true, boundingClientRect: { top: 0, bottom: 600 }, rootBounds: { top: 0, bottom: 2000 } }]));
    // before, it came in open: its part was not drawn when the Collapse all went out
    expect(open(deep)).toBe("false");
  });

  it("keeps a group's own choice when its part comes back into view", async () => {
    act(() => root.render(<WindowedRows items={items} render={(k) => <Group key={k} k={k}><p>pane</p></Group>} />));
    await settle();
    const deep = `g${HEAD + 2 * PART}`;
    const report = (el: Element, side: -1 | 0 | 1) => {
      const o = intersections.find((x) => x.targets.has(el))!;
      act(() => o.callback([{ target: el, isIntersecting: side === 0, boundingClientRect: side < 0 ? { top: -900, bottom: -800 } : { top: 0, bottom: 600 }, rootBounds: { top: 0, bottom: 2000 } }]));
    };
    report(host.querySelector('[data-rows-gap][data-rows-part="3"]')!, 0);
    // the Collapse all reaches it drawn, then it is opened by hand
    act(() => setAllFolded(true));
    expect(open(deep)).toBe("false");
    act(() => head(deep)!.click());
    // scrolled away (its part's first and last rows above the view) and back
    const rows = [...host.querySelectorAll("[data-group]")].filter((b) => /^g(7\d|8\d)$/.test(b.getAttribute("data-group")!)).map((b) => b.parentElement!);
    report(rows[0]!, -1);
    report(rows[rows.length - 1]!, -1);
    expect(head(deep)).toBeNull();
    report(host.querySelector('[data-rows-gap][data-rows-part="3"]')!, 0);
    expect(open(deep)).toBe("true");
  });
});
