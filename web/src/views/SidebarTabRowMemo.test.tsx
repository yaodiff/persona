// @vitest-environment jsdom
// A herdr tab with several panes has a menu of its own on its lead row. That menu is one function per tab, so a push for
// another agent does not re-render the lead row (SessionRow's memo), and the menu still opens on the tab as it is now.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every render of a SessionRow reads its left draft once: counted per session
const draftReads = vi.hoisted(() => new Map<string, number>());
vi.mock("../lib/drafts", async (load) => {
  const real = await load<typeof import("../lib/drafts")>();
  return {
    ...real,
    useLeftDraft: (key: string | null) => {
      if (key) draftReads.set(key, (draftReads.get(key) ?? 0) + 1);
      return real.useLeftDraft(key);
    },
  };
});
import { getState, refresh, reset, upsertSession } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const pane = (id: string, pane_id: string, slot_id: string, slot_index: number, title: string): Session => ({
  machine_id: "mac", session_id: id, title, cwd: "/code", status: "idle", state: "idle",
  space_kind: "herdr", space_id: "w1", space_name: "app", slot_id, slot_index, slot_name: `#${slot_index}`, pane_id,
});
// tab #1 has two panes (a1 leads: the first in pane order, both idle); tab #2 has one, the agent the pushes change
const sessions: Session[] = [pane("a1", "p1", "t1", 1, "主窗格"), pane("a2", "p2", "t1", 1, "拆出来的"), pane("b1", "p3", "t2", 2, "别的标签页")];
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); }); // NodeMenu comes in a chunk of its own
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const leadRow = () => [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("#1") && a.textContent.includes("主窗格"));
const menu = () => document.querySelector('[role="menu"]');

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("persona.agentGrouping", "machine");
  draftReads.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

async function renderTree() {
  await act(() => refresh());
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
  expect(leadRow()).toBeTruthy();
}

describe("a herdr tab with several panes", () => {
  it("keeps its lead row across pushes for another agent", async () => {
    await renderTree();
    expect(draftReads.get("mac/a1")).toBeGreaterThan(0);
    draftReads.clear();

    for (let i = 1; i <= 10; i++) {
      act(() => upsertSession({ ...getState().sessions.mac.find((s) => s.session_id === "b1")!, title: `别的标签页 ${i}` }));
      await flush();
    }
    expect(host.textContent).toContain("别的标签页 10");
    expect(draftReads.get("mac/b1")).toBeGreaterThanOrEqual(10);
    // the lead's left draft is read once by its row in the tab (a1 is also the first pane row under it): none of them again
    expect(draftReads.get("mac/a1") ?? 0).toBe(0);
    expect(draftReads.get("mac/a2") ?? 0).toBe(0);
  });

  it("opens the tab's menu from its ⋯ and on a right click, after pushes too", async () => {
    await renderTree();
    act(() => upsertSession({ ...getState().sessions.mac.find((s) => s.session_id === "b1")!, title: "别的标签页 改" }));
    await flush();

    const more = host.querySelector<HTMLButtonElement>('button[aria-label="标签页操作"]');
    expect(more).toBeTruthy();
    act(() => more!.click());
    await flush();
    expect(menu()?.textContent).toContain("标签页 #1");
    const items = menu()?.textContent ?? "";
    // the tab's own items (its node), not one agent's: rename, split, close the tab
    for (const item of ["重命名", "终端分屏", "关闭标签页"]) expect(items).toContain(item);
    // a closed menu again on the second click (the toggle a ⋯ has)
    act(() => more!.click());
    await flush();
    expect(menu()).toBeNull();

    act(() => leadRow()!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    await flush();
    expect(menu()?.textContent).toContain("标签页 #1");
    expect(menu()?.textContent).toBe(items);
  });
});
