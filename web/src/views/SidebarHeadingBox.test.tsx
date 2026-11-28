// @vitest-environment jsdom
// The sidebar's top-level headings stand evenly apart (#10637): Pages, Persona chats and Agents (the complete list's
// title row and the paged one's), or Pending in Agents' place, each sit in one box, `sidebar-heading`, whatever they
// carry beside their title. Each row was as tall as its tallest control (Pages' count 16.5 px, Persona chats' New chat
// 20, Agents' fold all 24), so the gaps between the titles differed. A group's heading inside a list keeps its own row.
// jsdom lays nothing out: this checks the box every heading's row takes and the token behind it;
// web/e2e/sidebar-heading-spacing.mjs measures the gaps in Chrome. iOS did the same in #10561 (SidebarHeadingBox).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { connect, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
class FakeSocket {
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.all.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}

const agent = (id: string, state: "working" | "idle"): Session => ({ machine_id: "computer", session_id: id, title: id, cwd: "/code/demo", status: state, state, updated_at: "2026-01-01T00:00:00Z", space_id: "project", space_name: "Demo project" });
const agents = [agent("Release script", "working"), agent("Read the logs", "idle")];
const chat = (session_id: string, title: string): Session => ({ machine_id: "hub", session_id, title, cwd: "/persona", status: "idle", state: "idle", assistant: true, origin: "user", updated_at: "2026-01-01T00:00:00Z" });
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));

let host: HTMLDivElement, root: Root;
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };

async function render(features: string[]) {
  vi.stubGlobal("fetch", vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    const q = url.searchParams;
    if (url.pathname === "/api/v1/assistant") return json({ machine_id: "hub", main: "main", conversations: [chat("main", "Persona"), chat("trip", "Trip plan")] });
    if (url.pathname === "/api/v1/sessions") {
      if (q.has("counts")) return json({ total: 2, state: { working: 1, idle: 1 }, machine: { computer: 2 }, group: {}, project: {}, revision: "r1" });
      if (q.has("since")) return json({ sessions: [], remove: [], has_more: false, revision: "r1", total: 2 });
      return json(url.search ? { sessions: agents, remove: [], has_more: false, revision: "r1", total: 2 } : { sessions: agents });
    }
    if (url.pathname === "/api/v1/pins") return json({ sessions: [], spaces: [] });
    if (url.pathname === "/api/v1/machines") return json({ machines: [{ id: "computer", name: "Demo computer", online: true }] });
    if (url.pathname === "/api/v1/todos") return json({ todos: [] });
    return json({});
  }));
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features });
  });
  await flush();
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
}

/** A heading's row, found by its title's own words as e2e/sidebar-heading-spacing.mjs finds it: the row holding the
 *  title's fold button, or the title itself where the heading does not fold (the paged Agents list). */
const rowOf = (title: string) => {
  const words = [...host.querySelectorAll("[data-sidebar-rows] span")].find((s) => s.childElementCount === 0 && s.textContent?.trim() === title);
  return words ? (words.closest("button")?.parentElement ?? words.parentElement) : null;
};
/** The titles of the rows that take the heading box, top to bottom. */
const boxed = () => [...host.querySelectorAll<HTMLElement>("[data-sidebar-rows] .sidebar-heading")].map((row) => {
  const words = [...row.querySelectorAll("span")].find((s) => s.childElementCount === 0 && s.textContent?.trim());
  return words?.textContent?.trim();
});
/** Folds (or opens) every top-level heading that folds, by its title's button. */
const foldAll = async (titles: string[], folded: boolean) => {
  for (const title of titles) {
    const button = rowOf(title)?.querySelector<HTMLButtonElement>(":scope > button[aria-expanded]");
    if (button && button.getAttribute("aria-expanded") === String(folded)) act(() => button.click());
  }
  await flush();
};

beforeEach(() => {
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  // By status: the Agents list's own groups (工作中, 空闲…) are drawn under its heading, with headings of their own
  localStorage.setItem("persona.agentGrouping", "state");
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

describe("the sidebar's top-level headings (#10637)", () => {
  const cases = [
    { list: "the complete Agents list", features: [], titles: ["页面", "Persona 对话", "Agents"] },
    { list: "the paged Agents list", features: ["sessions_list", "sessions_lookup", "sessions_todo", "sessions_agents_only"], titles: ["页面", "Persona 对话", "Agents"] },
    { list: "Pending in Agents' place", features: ["sessions_dimensions"], titles: ["页面", "Persona 对话", "待处理"] },
  ];
  for (const { list, features, titles } of cases) {
    it(`share one heading box, folded and unfolded, with ${list}`, async () => {
      if (features.includes("sessions_list")) localStorage.setItem("persona.agentGrouping", "machine");
      await render(features);
      expect(Boolean(host.querySelector("[data-paged-sessions]"))).toBe(features.includes("sessions_list"));
      for (const folded of [false, true]) {
        await foldAll(titles, folded);
        for (const title of titles) expect(rowOf(title)?.classList.contains("sidebar-heading"), `${title}, ${folded ? "folded" : "unfolded"}`).toBe(true);
        // and only they: a group's heading inside a list keeps its own row
        expect(boxed()).toEqual(titles);
      }
    });
  }

  it("draws the Agents list's own group headings without the box", async () => {
    await render([]);
    const groups = [...host.querySelectorAll("[data-sidebar-agents] [data-sidebar-group-heading] button[aria-expanded]")].filter((b) => !b.closest("[data-sidebar-agents-controls]"));
    expect(groups.length).toBeGreaterThan(0);
    for (const button of groups) expect(button.parentElement?.classList.contains("sidebar-heading")).toBe(false);
  });
});

// #11511: in the Agents title rows the icons sit 4 px apart, so each takes a real 44 px box on a touch screen (centred
// touch-hit squares overlapped, the later button taking the earlier one's taps), the row's end padding dropped so the last
// box ends where the rows' own ⋯ boxes do; an add button alone at a heading's end keeps its touch-hit square
describe("the headings' icon buttons on a touch screen (#11511)", () => {
  const TOUCH_BOX = "[@media(pointer:coarse)]:size-11";
  const ROW_END = "[@media(pointer:coarse)]:pr-0";
  const classes = (el: Element | null | undefined) => el?.className.split(/\s+/) ?? [];
  const add = (scope: string) => host.querySelector(`${scope} [aria-label="新建 Agent"]`);

  it("give the Agents title row's + a 44 px box, the complete list's and the paged one's, and no other heading's", async () => {
    await render([]);
    expect(classes(add("[data-sidebar-agents-controls]"))).toContain(TOUCH_BOX);
    expect(classes(add("[data-sidebar-agents-controls]")?.parentElement)).toEqual(expect.arrayContaining(["px-2", ROW_END]));
    expect(classes(host.querySelector('[aria-label="新对话"]'))).not.toContain(TOUCH_BOX);
    expect(classes(host.querySelector('[aria-label="新对话"]'))).toContain("touch-hit");
    expect(classes(host.querySelector('[aria-label="新对话"]')?.parentElement)).not.toContain(ROW_END);
    act(() => root.unmount());
    root = createRoot(host);
    await render(["sessions_list", "sessions_lookup", "sessions_todo", "sessions_agents_only"]);
    expect(classes(add("[data-sidebar-agents]"))).toContain(TOUCH_BOX);
    expect(classes(add("[data-sidebar-agents]")?.parentElement)).toEqual(expect.arrayContaining(["px-2", ROW_END]));
  });

  it("leave Pending's + alone at its heading's end", async () => {
    await render(["sessions_dimensions"]);
    const pending = add("[data-sidebar-rows]");
    expect(pending).not.toBeNull();
    expect(classes(pending)).not.toContain(TOUCH_BOX);
    expect(classes(pending)).toContain("touch-hit");
  });

  it("hide the search box's ⌘K on a screen without hover (a landscape phone is wide enough for it)", async () => {
    await render([]);
    const hint = host.querySelector("aside button kbd")?.parentElement;
    expect(classes(hint)).toEqual(expect.arrayContaining(["hidden", "sm:inline-flex", "[@media(hover:none)]:hidden"]));
  });
});

describe("the heading box's token", () => {
  const css = readFileSync(join(__dirname, "../styles.css"), "utf8");

  it("is as tall as the tallest control a heading carries: the fold-all button, 24 px and 44 px on a touch screen", () => {
    expect(css).toMatch(/:root \{ --sidebar-heading: 24px; \}/);
    expect(css).toMatch(/@media \(pointer: coarse\) \{ :root \{ --sidebar-heading: 44px; \} \}/);
    const fold = SESSION_LIST_STYLE.foldButton.split(/\s+/);
    expect(fold).toContain("size-6");
    expect(fold).toContain("[@media(pointer:coarse)]:size-11");
  });

  it("sets the content box's height, so a heading's room under it comes on top and its controls never set it", () => {
    const rule = css.match(/@utility sidebar-heading \{([^}]*)\}/)?.[1] ?? "";
    expect(rule).toMatch(/box-sizing: content-box;/);
    expect(rule).toMatch(/(^|[\s;])height: var\(--sidebar-heading\);/);
    expect(rule).not.toMatch(/min-height/);
  });
});
