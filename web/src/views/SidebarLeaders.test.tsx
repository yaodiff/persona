// @vitest-environment jsdom
// The leader conversations in the sidebar (#3623, #4331): Persona chats are the main one (its row above) and the hub's
// `assistant.leaders`, "常驻", in the hub's order under each one's name with what it has going: the working arc while
// anything under it runs and its tasks Waiting on you (the direction tree, GET /api/v1/assistant/directions/tree, #4352,
// its `running`, #6626), then "最近", the newest of the user's other chats
// (#4873; 5 by default, a setting, with Show more, #5146; the scheduled tasks' chats stay out of it, #6228); every chat is under 全部对话. The hub's `directions` message makes an open sidebar read the tree
// again (a leader made or ended anywhere).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { setToken } from "../lib/api";
import { resetDirectionNames } from "../lib/direction-names";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { setRecentCount } from "../lib/sidebar-recent";
import { connect, refresh, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const conversation = (session_id: string, title: string, updated_at: string): Session => ({
  machine_id: "hub", session_id, title, cwd: "/persona", status: "idle", state: "idle", assistant: true, origin: "user", updated_at,
});
const base = [
  conversation("main", "Persona", "2026-11-10T09:00:00Z"),
  conversation("newest", "Trip plan", "2026-11-10T08:00:00Z"),
  conversation("mine", "Release notes", "2026-11-01T00:00:00Z"),
  conversation("lead-b", "Payments", "2026-11-02T00:00:00Z"),
  conversation("lead-a", "Growth", "2026-11-03T00:00:00Z"),
];
let conversations: Session[];
let leaders: string[] | undefined;
let assistantManager: string | undefined;
type LeaderFixture = {
  id: string;
  title?: string;
  name?: string;
  ended?: boolean;
  counts?: { working?: number; needs_you?: number };
  running?: boolean;
  external?: boolean;
  machine_id?: string;
  session_id?: string;
  tasks?: { id: string; title: string; state: string; agents?: [] }[];
};
let named: { leaders: LeaderFixture[] } | undefined;
let treeMissing = false;
let assistantReads = 0;
let posts: string[] = [];

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const rowIds = () => [...host.querySelectorAll<HTMLAnchorElement>('a[href^="/persona/"]')].map((a) => a.getAttribute("href")!.slice("/persona/".length)).filter((id) => id !== "new");
const headings = () => [...host.querySelectorAll('[role="heading"]')].map((h) => h.textContent);
const newChat = () => host.querySelector<HTMLAnchorElement>(`a[href="/persona/new"][aria-label="${t("sidebar.new_chat")}"]`);
const row = (sid: string) => host.querySelector<HTMLAnchorElement>(`a[href="/persona/${sid}"]`)!;
const directionRow = (id: string) => host.querySelector<HTMLElement>(`[data-direction="${id}"]`)!;

beforeEach(() => {
  localStorage.clear();
  conversations = base;
  leaders = ["lead-a", "lead-b"];
  treeMissing = false;
  assistantReads = 0;
  assistantManager = undefined;
  posts = [];
  named = { leaders: [
    // lead-b has a task in the working state whose Agents all stopped: nothing runs under it (#6626)
    { id: "lead-a", title: "1110 | explore | Growth", name: "Product", counts: { working: 2, needs_you: 1 }, running: true },
    { id: "lead-b", title: "Payments", name: "Infrastructure", counts: { working: 1, needs_you: 0 }, running: false },
    { id: "old", title: "Old", name: "Ended one", ended: true },
  ] };
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    const path = new URL(url, "http://x").pathname;
    if (init?.method === "POST") {
      posts.push(path);
      return Promise.resolve(new Response(String(init.body), { headers: { "Content-Type": "application/json" } }));
    }
    if (path === "/api/v1/assistant") assistantReads++;
    if (path === "/api/v1/assistant/directions/tree" && (named === undefined || treeMissing)) return Promise.resolve(new Response("{}", { status: 404 }));
    if (path === "/api/v1/assistant/leaders" && named === undefined) return Promise.resolve(new Response("{}", { status: 404 }));
    if (path === "/api/v1/assistant/directions/tree") {
      const list = (named?.leaders ?? []).filter((l) => !l.ended);
      // `running` only where a case gives it: without it the tree is an older hub's
      return Promise.resolve(new Response(JSON.stringify({ directions: [...list.map((l) => ({ id: l.id, name: l.name, external: l.external, machine_id: l.machine_id, session_id: l.session_id, counts: { stuck: 0, review: 0, ...(l.counts ?? {}) }, ...(l.running === undefined ? {} : { running: l.running }), tasks: l.tasks ?? [] })), { id: "main", name: "", secretary: true, counts: { working: 9, needs_you: 9, stuck: 0, review: 0 }, tasks: [] }] }), { headers: { "Content-Type": "application/json" } }));
    }
    const body = path === "/api/v1/assistant" ? { manager: assistantManager, machine_id: "hub", main: "main", leaders, conversations } : path === "/api/v1/assistant/leaders" ? named : path === "/api/v1/machines" ? { machines: [{ id: "hub", name: "Hub", online: true }] } : {};
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetDirectionNames();
  resetManagerChoice();
  vi.unstubAllGlobals();
});

async function renderSidebar() {
  await act(() => refresh());
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
}

class FakeSocket {
  static last: FakeSocket | null = null;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.last = this; }
  send() {}
  close() { this.readyState = 3; }
}
const push = (msg: unknown) => act(() => FakeSocket.last!.onmessage?.({ data: JSON.stringify(msg) }));

describe("the leader conversations in the sidebar", () => {
  it("are 常驻, in the hub's order, under their names with what they have open; then 最近, then 全部对话", async () => {
    await renderSidebar();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "newest", "mine"]);
    expect(headings()).toEqual([t("sidebar.directions"), t("sidebar.recent_chats")]);
    expect(t("sidebar.directions")).toBe("常驻");
    expect(t("sidebar.recent_chats")).toBe("最近");
    expect(row("lead-a").textContent).toContain("Product");
    // what it has waiting on you in words; what runs under it is the arc alone, never "在做 N" as well (#6626)
    expect(row("lead-a").textContent).toContain("待处理 1");
    expect(row("lead-a").textContent).not.toContain("在做");
    expect(row("lead-b").textContent).toContain("Infrastructure");
    expect(row("lead-b").textContent).not.toContain("在做");
    // 常驻 is marked only by its heading and counts (#6214): a lead with nothing running draws the ordinary chat icon,
    // the same one a 最近 row draws — no pin glyph that could read as a tappable button. (lead-a has Agents working, so
    // it carries the working arc instead, asserted in the spin test below.)
    const leadIcon = row("lead-b").querySelector("svg")!.outerHTML;
    const recentIcon = row("newest").querySelector("svg")!.outerHTML;
    expect(leadIcon).toBe(recentIcon);
    // 常驻 is marked only by its heading and counts (#6214): no pin glyph on the row that could read as a button
    const allChats = host.querySelector<HTMLAnchorElement>('a[href="/history"]')!;
    expect(allChats.textContent).toContain(t("sidebar.all_chats"));
    expect(headings().join()).not.toMatch(/置顶|其他对话|方向/);
  });

  it("spin the one working mark while anything under them runs, even when their own conversation is idle (#6230, #6626)", async () => {
    await renderSidebar();
    // lead-a's own session is idle but Agents under it work (the tree's running): the row carries the working arc
    const leadA = row("lead-a");
    expect(leadA.querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
    // lead-b counts a task in the working state, but nothing under it runs: no arc, the quiet chat icon stays
    const leadB = row("lead-b");
    expect(leadB.querySelector('[data-shape="arc"]')).toBeNull();
  });

  it("spin on what runs, not on the working count: a task that waits on you with an Agent still at work spins", async () => {
    named = { leaders: [
      { id: "lead-a", name: "Product", counts: { working: 0, needs_you: 1 }, running: true },
      { id: "lead-b", name: "Infrastructure", counts: { working: 2, needs_you: 0 }, running: false },
    ] };
    await renderSidebar();
    expect(row("lead-a").querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
    expect(row("lead-a").textContent).toContain("待处理 1");
    expect(row("lead-b").querySelector('[data-shape="arc"]')).toBeNull();
    expect(row("lead-b").textContent).not.toMatch(/在做|待处理/);
  });

  it("spin on a working task from an older hub whose tree has no running", async () => {
    named = { leaders: [
      { id: "lead-a", name: "Product", counts: { working: 1, needs_you: 0 } },
      { id: "lead-b", name: "Infrastructure", counts: { working: 0, needs_you: 0 } },
    ] };
    await renderSidebar();
    expect(row("lead-a").querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
    expect(row("lead-a").textContent).not.toContain("在做");
    expect(row("lead-b").querySelector('[data-shape="arc"]')).toBeNull();
  });

  it("keep, folded, the arc while anything under a lead runs and the tasks waiting on you, never 在做 N", async () => {
    localStorage.setItem("fold:persona", "1");
    await renderSidebar();
    const tally = host.querySelector<HTMLElement>("[data-sidebar-rows] [data-direction-counts]");
    expect(tally).not.toBeNull();
    expect(tally!.textContent).toBe("待处理 1");
    expect(tally!.querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
  });

  it("keep, folded, only the tasks waiting on you while nothing runs under any lead", async () => {
    localStorage.setItem("fold:persona", "1");
    named = { leaders: [
      { id: "lead-a", name: "Product", counts: { working: 1, needs_you: 2 }, running: false },
      { id: "lead-b", name: "Infrastructure", counts: { working: 0, needs_you: 1 }, running: false },
    ] };
    await renderSidebar();
    const tally = host.querySelector<HTMLElement>("[data-sidebar-rows] [data-direction-counts]");
    expect(tally!.textContent).toBe("待处理 3");
    expect(tally!.querySelector('[data-shape="arc"]')).toBeNull();
  });

  it("keeps mixed conversation and external rows, opens only a complete outside pair, and folds by direction id", async () => {
    leaders = ["lead-a", "direction-outside", "direction-targetless", "lead-b"];
    named = { leaders: [
      { id: "lead-a", name: "Product", counts: { working: 0, needs_you: 1 }, running: false },
      { id: "direction-outside", name: "Outside release", external: true, machine_id: "mac", session_id: "agent-7", counts: { working: 1, needs_you: 2 }, running: true,
        tasks: [{ id: "todo-outside", title: "Check the outside work", state: "needs_you", agents: [] }] },
      { id: "direction-targetless", name: "Outside without a target", external: true, counts: { working: 0, needs_you: 0 }, running: false },
      { id: "lead-b", name: "Infrastructure", counts: { working: 0, needs_you: 0 }, running: false },
    ] };
    await renderSidebar();
    // The existing conversation row, route and menu are unchanged.
    expect(row("lead-a").getAttribute("href")).toBe("/persona/lead-a");
    expect(row("lead-a").textContent).toContain("Product");
    expect(directionRow("lead-a").querySelector('[aria-haspopup="menu"]')).not.toBeNull();
    const outside = directionRow("direction-outside");
    const outsideLink = outside.querySelector<HTMLAnchorElement>('a[href="/s/mac/agent-7"]')!;
    expect(outsideLink.textContent).toContain("Outside release");
    expect(outsideLink.textContent).toContain("待处理 2");
    expect(outside.textContent).not.toContain("agent-7");
    expect(outside.querySelector('a[href^="/persona/"]')).toBeNull();
    expect(outside.querySelector('[aria-haspopup="menu"]')).toBeNull();
    const targetless = directionRow("direction-targetless");
    expect(targetless.textContent).toContain("Outside without a target");
    expect(targetless.querySelector("a")).toBeNull();
    expect(targetless.querySelector('[aria-haspopup="menu"]')).toBeNull();
    expect(host.querySelector('a[href="/persona/direction-targetless"]')).toBeNull();
    // The disclosure and its persisted state use the opaque direction id, not the outside Agent's session id.
    const disclosure = outside.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    act(() => disclosure.click());
    await flush();
    expect(localStorage.getItem("fold:direction:direction-outside")).toBe("0");
    expect(localStorage.getItem("fold:direction:agent-7")).toBeNull();
    expect(outside.querySelector('a[href="/todos?id=todo-outside"]')).not.toBeNull();
    act(() => outsideLink.click());
    await flush();
    expect(location.pathname).toBe("/s/mac/agent-7");
  });

  it("includes conversationless directions in the folded section's counts", async () => {
    localStorage.setItem("fold:persona", "1");
    conversations = [base[0]];
    leaders = ["direction-outside", "direction-targetless"];
    named = { leaders: [
      { id: "direction-outside", name: "Outside release", external: true, machine_id: "mac", session_id: "agent-7", counts: { working: 1, needs_you: 2 }, running: true },
      { id: "direction-targetless", name: "Outside without a target", external: true, counts: { working: 0, needs_you: 1 }, running: false },
    ] };
    await renderSidebar();
    const tally = host.querySelector<HTMLElement>("[data-sidebar-rows] [data-direction-counts]")!;
    expect(tally.textContent).toBe("待处理 3");
    expect(tally.querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
  });

  it("shows only conversationless directions through the leaders fallback when the tree is 404", async () => {
    treeMissing = true;
    conversations = [base[0]];
    leaders = ["direction-outside", "direction-targetless"];
    named = { leaders: [
      { id: "direction-outside", name: "Outside release", external: true, machine_id: "mac", session_id: "agent-7" },
      { id: "direction-targetless", name: "Outside without a target", external: true },
    ] };
    await renderSidebar();
    const outsideLink = host.querySelector<HTMLAnchorElement>('a[href="/s/mac/agent-7"]')!;
    expect(outsideLink.textContent).toContain("Outside release");
    const targetless = [...host.querySelectorAll<HTMLElement>(".sidebar-row")].find((el) => el.textContent?.includes("Outside without a target"))!;
    expect(targetless.tagName).toBe("DIV");
    expect(targetless.querySelector("a")).toBeNull();
    expect(host.querySelector('a[href^="/persona/direction-"]')).toBeNull();
  });

  it("show their titles on a hub without the leaders list", async () => {
    named = undefined;
    await renderSidebar();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "newest", "mine"]);
    expect(row("lead-a").textContent).toContain("Growth");
  });

  it("leave 最近 and 全部对话 when the hub has none", async () => {
    leaders = undefined;
    await renderSidebar();
    expect(rowIds()).toEqual(["newest", "lead-a", "lead-b", "mine"]);
    expect(headings()).toEqual([t("sidebar.recent_chats")]);
    expect(host.querySelector('a[href="/history"]')).not.toBeNull();
  });

  it("leave 最近 out when every other chat is 常驻", async () => {
    conversations = base.filter((c) => c.session_id === "main" || c.session_id.startsWith("lead-"));
    await renderSidebar();
    expect(rowIds()).toEqual(["lead-a", "lead-b"]);
    expect(headings()).toEqual([t("sidebar.directions")]);
  });

  describe("最近 (#5146, #6228)", () => {
    const pad = (d: number) => String(d).padStart(2, "0");
    // 20 older chats of the user's own, c20 the newest of them
    const older = Array.from({ length: 20 }, (_, i) => conversation(`c${pad(i + 1)}`, `Chat ${i + 1}`, `2026-10-${pad(i + 1)}T00:00:00Z`));
    // the scheduled task's chat no longer takes a Recent row (#6228): the rest, whoever opened it, newest first
    const own = ["twin", "newest", "wx", "mine", ...older.map((c) => c.session_id).reverse()];
    const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === text);
    const showMore = () => button(t("sidebar.show_more", { count: 5 }));
    const recentHeading = () => [...host.querySelectorAll<HTMLElement>('[role="heading"] button')].find((b) => b.textContent?.includes(t("sidebar.recent_chats")));
    beforeEach(() => {
      conversations = [
        ...base,
        { ...conversation("twin", "Inbox reply", "2026-11-10T08:30:00Z"), origin: "inbox" },
        { ...conversation("task", "Morning brief", "2026-11-10T08:40:00Z"), origin: "task" },
        { ...conversation("wx", "From a channel", "2026-11-09T00:00:00Z"), origin: "channel" },
        ...older,
      ];
    });

    it("lists the newest 5 other chats by default, never the scheduled task's chat, with the Inbox chat's origin mark", async () => {
      await renderSidebar();
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 5)]);
      expect(rowIds()).not.toContain("task");
      expect(host.querySelector('a[href="/history"]')!.textContent).toContain(String(conversations.length));
      const mark = (id: string) => row(id).querySelector("[data-origin-mark]");
      expect(mark("twin")?.getAttribute("data-origin-mark")).toBe("inbox");
      for (const id of ["newest", "wx", "mine"]) expect(mark(id), id).toBeNull();
    });

    it("shows 5 more for each Show more until none are left, Show less goes back, and a reload keeps what was open", async () => {
      await renderSidebar();
      expect(button(t("sidebar.show_less"))).toBeUndefined();
      act(() => showMore()!.click());
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 10)]);
      act(() => button(t("sidebar.show_more", { count: 5 }))!.click());
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 15)]);
      // a reload keeps Show more's state
      act(() => root.unmount());
      root = createRoot(host);
      await renderSidebar();
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 15)]);
      act(() => button(t("sidebar.show_less"))!.click());
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 5)]);
      expect(button(t("sidebar.show_less"))).toBeUndefined();
    });

    it("lists as many as the setting says, and Show more adds as many again", async () => {
      setRecentCount(10);
      await renderSidebar();
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 10)]);
      act(() => button(t("sidebar.show_more", { count: 10 }))!.click());
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 20)]);
    });

    it("has no Show more when the count covers every Recent chat", async () => {
      conversations = [...base, ...older.slice(0, 13)];
      setRecentCount(25);
      await renderSidebar();
      expect(rowIds()).toHaveLength(2 + 15);
      expect(showMore()).toBeUndefined();
      expect(button(t("sidebar.show_less"))).toBeUndefined();
    });

    it("folds 最近 and keeps it folded after a reload; the open chat's row opens it by itself", async () => {
      await renderSidebar();
      const heading = recentHeading()!;
      // the box that clips the recent rows: its grid row goes to 0fr at once when folded (the Collapse's inert follows the animation)
      const recentBox = () => row("twin").closest(".grid") as HTMLElement;
      expect(heading.getAttribute("aria-expanded")).toBe("true");
      expect(recentBox().style.gridTemplateRows).toBe("1fr");
      act(() => heading.click());
      expect(heading.getAttribute("aria-expanded")).toBe("false");
      expect(localStorage.getItem("fold:recent-chats")).toBe("1");
      expect(recentBox().style.gridTemplateRows).toBe("0fr");
      act(() => root.unmount());
      root = createRoot(host);
      await renderSidebar();
      expect(recentHeading()!.getAttribute("aria-expanded")).toBe("false");
      // folded from the start, its rows are not drawn at all (a folded group mounts no rows)
      expect(rowIds()).toEqual(["lead-a", "lead-b"]);
      // unfold again
      act(() => recentHeading()!.click());
      expect(rowIds()).toEqual(["lead-a", "lead-b", ...own.slice(0, 5)]);
      expect(localStorage.getItem("fold:recent-chats")).toBe("0");
    });
  });

  it("have New chat as the compose icon on the section's title, not a row of its own", async () => {
    await renderSidebar();
    const link = newChat();
    expect(link).not.toBeNull();
    expect(link!.textContent).toBe("");
    expect(link!.closest("[data-sidebar-rows] .group\\/sec")).not.toBeNull();
    expect(link!.parentElement!.textContent).toContain(t("sidebar.persona_chats"));
  });

  it("are made and ended from the menu through the hub: 取消常驻 asks again, never 置顶 (#6214)", async () => {
    await renderSidebar();
    act(() => row("lead-a").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 })));
    await flush();
    const unpin = () => [...document.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')]
      .find((el) => el.textContent?.includes("取消常驻"));
    // The row menu is lazy; wait for its module before exercising the same two-step action.
    await vi.waitFor(() => expect(unpin()).toBeDefined());
    expect([...document.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')]
      .some((el) => el.textContent?.includes("置顶"))).toBe(false);
    // the first tap only arms the row and says what moves; nothing has reached the hub
    act(() => unpin()!.click());
    await flush();
    const asking = [...document.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')]
      .find((el) => el.textContent?.includes("确认取消常驻"));
    expect(asking).toBeTruthy();
    expect(asking!.textContent).toContain("最近");
    expect(posts).toEqual([]);
    // the second tap runs the end call
    act(() => asking!.click());
    await flush();
    expect(posts).toEqual(["/api/v1/assistant/directions/lead-a/end"]);
  });

  it("does not reread the assistant solely for a permanent external direction id", async () => {
    conversations = [base[0]];
    leaders = ["direction-outside"];
    named = { leaders: [{ id: "direction-outside", name: "Outside release", external: true, machine_id: "mac", session_id: "agent-7", counts: { working: 0, needs_you: 0 }, running: false }] };
    await renderSidebar();
    vi.stubGlobal("WebSocket", FakeSocket);
    act(() => {
      connect();
      FakeSocket.last!.readyState = 1;
      FakeSocket.last!.onopen?.();
    });
    push({ type: "hello", features: [] });
    await flush();
    const reads = assistantReads;
    push({ type: "directions" });
    await flush();
    expect(assistantReads).toBe(reads);
    push({ type: "directions" });
    await flush();
    expect(assistantReads).toBe(reads);
  });

  it("follow the hub's directions message at once: a new one shows, an ended one goes, counts change", async () => {
    vi.stubGlobal("WebSocket", FakeSocket);
    act(() => {
      connect();
      FakeSocket.last!.readyState = 1;
      FakeSocket.last!.onopen?.();
    });
    push({ type: "hello", features: [] });
    await renderSidebar();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "newest", "mine"]);
    // the secretary made "mine" a lead: the tree has it; no reload, no reconnect
    named = { leaders: [
      { id: "lead-a", name: "Product", counts: { working: 0, needs_you: 3 }, running: false },
      { id: "lead-b", name: "Infrastructure", counts: { working: 0, needs_you: 0 }, running: false },
      { id: "mine", name: "Docs sync", counts: { working: 1, needs_you: 0 }, running: true },
    ] };
    push({ type: "directions" });
    await flush();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "mine", "newest"]);
    expect(row("mine").textContent).toContain("Docs sync");
    expect(row("mine").querySelector('[data-shape="arc"][data-state="working"]')).not.toBeNull();
    expect(row("lead-a").textContent).toContain("待处理 3");
    // the secretary group's counts are never a lead's
    expect(host.querySelector("[data-sidebar-rows]")!.textContent).not.toContain("待处理 9");
    named = { leaders: [{ id: "mine", name: "Docs sync", counts: { working: 1, needs_you: 0 }, running: true }] };
    push({ type: "directions" });
    await flush();
    // the ended ones leave 常驻 and are back among 最近 by their time
    expect(rowIds()).toEqual(["mine", "newest", "lead-a", "lead-b"]);
  });
});

describe("cold full-page reloads (#12231)", () => {
  /** A warm answer may have persisted a snapshot; a query-shaped lightweight read is also allowed to answer early. */
  async function coldReload(early: unknown, authoritative: unknown, beforeRead?: () => void) {
    const ordinary = fetch;
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    act(() => root.unmount());
    reset();
    resetDirectionNames();
    root = createRoot(host);
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://x");
      if (url.pathname === "/api/v1/assistant") {
        if (url.search) return Promise.resolve(Response.json(early));
        return held.then(() => Response.json(authoritative));
      }
      return ordinary(input, init);
    }));
    act(() => beforeRead?.());
    let loading!: Promise<void>;
    act(() => {
      loading = refresh();
      root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    });
    await flush();
    return async () => {
      release();
      await act(async () => { await loading; });
      await flush();
    };
  }

  it("draws directions, recent chats and All chats before the full assistant answer, then reconciles them", async () => {
    setToken("first-credential");
    await renderSidebar();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "newest", "mine"]);
    const early = { machine_id: "hub", main: "main", leaders, conversations: base };
    const fresh = conversation("fresh", "Fresh authoritative chat", "2026-11-11T09:00:00Z");
    const full = { ...early, conversations: [...base, fresh] };
    const finish = await coldReload(early, full);

    expect(rowIds()).toEqual(["lead-a", "lead-b", "newest", "mine"]);
    expect(headings()).toEqual([t("sidebar.directions"), t("sidebar.recent_chats")]);
    const allChats = host.querySelector<HTMLAnchorElement>('a[href="/history"]')!;
    expect(allChats).not.toBeNull();
    expect(allChats.textContent).toContain(String(base.length));

    await finish();
    expect(rowIds()).toEqual(["lead-a", "lead-b", "fresh", "newest", "mine"]);
    expect(row("fresh").getAttribute("href")).toBe("/persona/fresh");
    expect(row("lead-a").getAttribute("href")).toBe("/persona/lead-a");
    expect(host.querySelector<HTMLAnchorElement>('a[href="/history"]')!.textContent).toContain(String(base.length + 1));
  });

  it.each(["credential", "manager", "default manager"])("never draws the warm sidebar for a different %s", async (change) => {
    setToken("first-credential");
    setRosterDefault("first-manager");
    await renderSidebar();
    expect(row("newest").textContent).toContain("Trip plan");
    const other = {
      machine_id: "hub", main: "main", leaders: [], conversations: [
        conversation("main", "Other main", "2026-11-10T09:00:00Z"),
        conversation("other-chat", "Other owner's chat", "2026-11-10T08:00:00Z"),
      ],
    };
    const finish = await coldReload(other, other, () => {
      if (change === "credential") setToken("second-credential");
      else if (change === "manager") chooseManager("other-manager");
      else setRosterDefault("other-manager");
    });

    expect(host.textContent).not.toContain("Trip plan");
    expect(host.textContent).not.toContain("Release notes");
    await finish();
    expect(row("other-chat").getAttribute("href")).toBe("/persona/other-chat");
    expect(host.textContent).not.toContain("Trip plan");
  });

  it("retires a pre-roster answer and its read when the roster names another default", async () => {
    setToken("first-credential");
    const ordinary = fetch;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (new URL(String(input), "http://x").pathname === "/api/v1/managers") return new Promise<Response>(() => {});
      return ordinary(input, init);
    }));
    assistantManager = "first-manager";
    await renderSidebar();
    expect(row("newest").textContent).toContain("Trip plan");

    assistantManager = "other-manager";
    conversations = [
      conversation("main", "Other main", "2026-11-10T09:00:00Z"),
      conversation("other-chat", "Other default's chat", "2026-11-10T08:00:00Z"),
    ];
    act(() => setRosterDefault("other-manager"));
    await flush();

    expect(host.textContent).not.toContain("Trip plan");
    expect(row("other-chat").getAttribute("href")).toBe("/persona/other-chat");
  });

  it("does not let an older default answer overtake its replacement", async () => {
    setToken("first-credential");
    assistantManager = "first-manager";
    const ordinary = fetch;
    const pending: (() => void)[] = [];
    let holdAnswers = false;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://x");
      if (url.pathname === "/api/v1/managers") return new Promise<Response>(() => {});
      if (url.pathname === "/api/v1/assistant") {
        const body = { manager: assistantManager, machine_id: "hub", main: "main", leaders, conversations };
        if (holdAnswers) return new Promise<Response>((resolve) => pending.push(() => resolve(Response.json(body))));
        return Promise.resolve(Response.json(body));
      }
      return ordinary(input, init);
    }));
    await renderSidebar();

    holdAnswers = true;
    act(() => root.unmount());
    reset();
    root = createRoot(host);
    let loading!: Promise<void>;
    act(() => {
      loading = refresh();
      root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    });
    await flush();
    expect(row("newest").textContent).toContain("Trip plan");
    expect(pending).toHaveLength(1);

    assistantManager = "other-manager";
    conversations = [
      conversation("main", "Other main", "2026-11-10T09:00:00Z"),
      conversation("other-chat", "Other default's chat", "2026-11-10T08:00:00Z"),
    ];
    act(() => setRosterDefault("other-manager"));
    await flush();
    expect(pending).toHaveLength(2);
    expect(host.textContent).not.toContain("Trip plan");

    pending[0]();
    await act(async () => { await loading; });
    await flush();
    expect(host.textContent).not.toContain("Trip plan");
    pending[1]();
    await flush();
    expect(row("other-chat").getAttribute("href")).toBe("/persona/other-chat");
  });
});
