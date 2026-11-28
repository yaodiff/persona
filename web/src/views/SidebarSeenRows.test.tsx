// @vitest-environment jsdom
// Web Agents seen-rows UI behavior (#12585): held rows under By status (complete & paged lists), By project, Unread
// quick filter (including rows that become unread later), Clear seen (N), one-time notice, Settings deep-link, and Leave mode.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { seedAgentsDisplay } from "../lib/agents-display";
import { resetAgentListFilters, setAgentListFilters } from "../lib/agent-list-filters";
import { DEFAULT_DIMENSION_FILTERS } from "../lib/agent-chatlist-filter";
import { loadPages } from "../lib/lazy-page";
import { resetSeenVisit, setSeenRowsMode, setSeenRowsNoticed } from "../lib/seen-rows";
import { stateName } from "../lib/state-labels";
import { connect, markSeenLocally, refresh, reset, upsertSession, viewSession } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
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

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

const mac: Machine = { id: "mac", name: "Work Mac", online: true };
const box: Machine = { id: "box", name: "Home Box", online: true };
const row = (id: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: id,
  title: `Agent ${id}`,
  cwd: `/code/${id}`,
  status: state === "unread" ? "done" : state === "working" ? "working" : "idle",
  state,
  updated_at: "2026-01-01T00:01:00Z",
  ...patch,
});

let host: HTMLDivElement;
let root: Root;
const opened: Array<() => void> = [];
const openChat = (mid: string, sid: string) => {
  let release!: () => void;
  act(() => { release = viewSession(mid, sid); });
  let done = false;
  const close = () => { if (!done) { done = true; act(() => release()); } };
  opened.push(close);
  return close;
};

const agentsRoot = () => host.querySelector<HTMLElement>("[data-sidebar-agents]")!;
const sectionByTitle = (title: string) =>
  [...agentsRoot().querySelectorAll<HTMLElement>("[data-sidebar-group]")].find((g) => {
    const btn = g.querySelector<HTMLElement>(":scope > [data-sidebar-group-heading] button[aria-expanded]");
    return btn?.textContent?.trim() === title;
  });
const clearSeenBtn = (container: ParentNode = host) => container.querySelector<HTMLButtonElement>('[data-testid="agents-clear-seen"]');
const noticeEl = () => host.querySelector<HTMLElement>('[data-testid="agents-seen-notice"]');
const filterChip = (labelPrefix: string) =>
  [...agentsRoot().querySelectorAll<HTMLButtonElement>(`[role="radiogroup"][aria-label="${t("sidebar.quick_filters")}"] [role="radio"]`)]
    .find((b) => b.getAttribute("aria-label")?.startsWith(labelPrefix));

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  reset();
  resetSeenVisit();
  resetAgentListFilters();
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  opened.splice(0).forEach((close) => close());
  act(() => root.unmount());
  host.remove();
  reset();
  resetSeenVisit();
  setSeenRowsMode("stay");
  setSeenRowsNoticed(false);
  resetAgentListFilters();
  localStorage.clear();
  vi.unstubAllGlobals();
});

const mountLegacy = async (sessions: Session[], machines: Machine[] = [mac]) => {
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://hub.example.com").pathname;
    if (path === "/api/v1/sessions") return json({ sessions });
    if (path === "/api/v1/machines") return json({ machines });
    if (path === "/api/v1/todos") return json({ todos: [] });
    return json({});
  }));
  await act(async () => { await refresh(); });
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};

describe("Web Agents seen rows (complete list)", () => {
  it("keeps read rows in Unread drawn as Seen, drops counts, offers Clear seen (N), and shows the one-time notice", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("a", "unread", { updated_at: "2026-01-01T00:05:00Z" }),
      row("b", "unread", { updated_at: "2026-01-01T00:04:00Z" }),
      row("old", "idle", { updated_at: "2026-01-01T00:03:00Z" }),
    ]);

    const unreadTitle = stateName("unread");
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent a");
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent b");
    expect(clearSeenBtn()).toBeNull();
    expect(noticeEl()).toBeNull();

    // Open Agent a and then close its chat: it stays in Unread, drawn as Seen.
    const closeA = openChat("mac", "a");
    await flush();
    closeA();
    await flush();

    const unreadSec = sectionByTitle(stateName("done"))!;
    expect(unreadSec.textContent).toContain("Agent a");
    expect(unreadSec.textContent).toContain("Agent b");
    // Drawn as Seen: idle status mark, not unread.
    const rowA = unreadSec.querySelector('a[href="/s/mac/a"]')!;
    expect(rowA.querySelector(".status-mark")?.getAttribute("data-state")).toBe("idle");
    // Section count leaves out the held-seen row (only Agent b is still unread = 1).
    const countLabel = unreadSec.querySelector("[data-sidebar-group-heading] .sr-only")?.textContent;
    expect(countLabel).toBe(t("sidebar.agent_count", { count: 1 }));
    // Quick filter Unread count is also 1.
    expect(filterChip(unreadTitle)?.getAttribute("aria-label")).toBe(`${unreadTitle} 1`);
    // Clear seen (1) button is shown in the Unread section header.
    expect(clearSeenBtn(unreadSec)?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));
    // One-time notice floats at the list's foot.
    expect(noticeEl()?.textContent).toContain(t("seen-rows.notice"));

    // Dismissing the notice hides it for good.
    await act(async () => { host.querySelector<HTMLButtonElement>('[data-testid="agents-seen-notice-dismiss"]')!.click(); });
    await flush();
    expect(noticeEl()).toBeNull();

    // Clicking Clear seen (1) moves Agent a out of Unread into Seen.
    await act(async () => { clearSeenBtn(unreadSec)!.click(); });
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).not.toContain("Agent a");
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent b");
    expect(clearSeenBtn()).toBeNull();
    expect(noticeEl()).toBeNull();
  });

  it("keeps a row read from Just finished in that section with Clear seen (1)", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("done1", "idle", { work: { phase: "done", just_finished: true } }),
      row("done2", "idle", { work: { phase: "done", just_finished: true } }),
    ]);
    const jfTitle = stateName("done");
    expect(sectionByTitle(jfTitle)?.textContent).toContain("Agent done1");

    act(() => markSeenLocally("mac", "done1"));
    await flush();

    const jfSec = sectionByTitle(jfTitle)!;
    expect(jfSec.textContent).toContain("Agent done1");
    expect(jfSec.querySelector('a[href="/s/mac/done1"] .status-mark')?.getAttribute("data-state")).toBe("idle");
    expect(clearSeenBtn(jfSec)?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));
    expect(jfSec.querySelector("[data-sidebar-group-heading] .sr-only")?.textContent).toBe(t("sidebar.agent_count", { count: 1 }));
  });

  it("Unread filter takes in rows that become unread later and Clear seen drops read ones from the filter", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("a", "unread", { updated_at: "2026-01-01T00:05:00Z" }),
      row("later", "working", { updated_at: "2026-01-01T00:04:00Z" }),
    ]);

    const unreadTitle = stateName("unread");
    await act(async () => { filterChip(unreadTitle)!.click(); });
    await flush();
    expect(agentsRoot().textContent).toContain("Agent a");
    expect(agentsRoot().textContent).not.toContain("Agent later");

    // `later` finishes unread while the Unread filter is already chosen: kept takes it in!
    act(() => upsertSession(row("later", "unread", { updated_at: "2026-01-01T00:06:00Z" })));
    await flush();
    expect(agentsRoot().textContent).toContain("Agent later");

    // Reading `later` keeps it in the Unread filter and in the Unread section, drawn as Seen.
    const closeLater = openChat("mac", "later");
    await flush();
    closeLater();
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent later");
    expect(clearSeenBtn()?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));

    // Clear seen drops `later` out of the Unread filter too.
    await act(async () => { clearSeenBtn()!.click(); });
    await flush();
    expect(agentsRoot().textContent).not.toContain("Agent later");
    expect(agentsRoot().textContent).toContain("Agent a");
  });

  it("releases held rows when switching computer scope or re-choosing a quick filter", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("a", "unread", { machine_id: "mac" }),
      row("b", "unread", { machine_id: "box" }),
    ], [mac, box]);

    act(() => markSeenLocally("mac", "a"));
    await flush();
    expect(clearSeenBtn()?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));

    // Narrowing to another computer releases held rows.
    act(() => setAgentListFilters({ ...DEFAULT_DIMENSION_FILTERS, machines: ["box"] }));
    await flush();
    expect(clearSeenBtn()).toBeNull();
    act(() => setAgentListFilters(DEFAULT_DIMENSION_FILTERS));
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).not.toContain("Agent a");
  });

  it("in Leave mode holds only the open row and drops it when its chat closes, with no notice or Clear seen", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    setSeenRowsMode("leave");
    setSeenRowsNoticed(false);
    await mountLegacy([row("a", "unread"), row("b", "unread")]);

    const closeA = openChat("mac", "a");
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent a");
    expect(clearSeenBtn()).toBeNull();
    expect(noticeEl()).toBeNull();

    closeA();
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).not.toContain("Agent a");
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent b");
  });

  it("keeps an otherwise idle space active under By project while it holds a read row", async () => {
    localStorage.setItem("persona.agentGrouping", "space");
    await mountLegacy([
      row("a", "unread", { space_id: "sp1", space_name: "Alpha" }),
      row("b", "idle", { space_id: "sp2", space_name: "Beta" }),
    ]);
    // Alpha starts active, Beta in Idle spaces.
    expect(agentsRoot().textContent).toContain("Alpha");
    expect(agentsRoot().textContent).toContain(t("sidebar.idle_spaces", { count: 1 }));

    // Clear sessionStorage retainedActive to verify held-seen itself keeps Alpha active.
    sessionStorage.removeItem("persona.agentActiveSpaces");
    const closeA = openChat("mac", "a");
    await flush();
    closeA();
    await flush();
    expect(sectionByTitle("Alpha")?.textContent).toContain("Agent a");
    expect(sectionByTitle("Alpha")?.querySelector('a[href="/s/mac/a"] .status-mark')?.getAttribute("data-state")).toBe("idle");
  });

  it("keeps a row seen elsewhere (hub push turns it idle) in Unread drawn as Seen until Clear seen", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("away", "unread", { updated_at: "2026-01-01T00:05:00Z" }),
      row("stay-unread", "unread", { updated_at: "2026-01-01T00:04:00Z" }),
    ]);

    // Another device marks `away` seen: hub pushes idle without this browser ever opening it.
    act(() => upsertSession(row("away", "idle", { seen_at: "2026-01-01T00:06:00Z" })));
    await flush();

    const unreadSec = sectionByTitle(stateName("done"))!;
    expect(unreadSec.textContent).toContain("Agent away");
    expect(unreadSec.querySelector('a[href="/s/mac/away"] .status-mark')?.getAttribute("data-state")).toBe("idle");
    expect(clearSeenBtn(unreadSec)?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));
    expect(unreadSec.querySelector("[data-sidebar-group-heading] .sr-only")?.textContent).toBe(t("sidebar.agent_count", { count: 1 }));
  });

  it("Mark all as seen under the Unread filter lets already-read held rows go with the unread ones", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([
      row("a", "unread", { updated_at: "2026-01-01T00:05:00Z" }),
      row("b", "unread", { updated_at: "2026-01-01T00:04:00Z" }),
    ]);

    const unreadTitle = stateName("unread");
    await act(async () => { filterChip(unreadTitle)!.click(); });
    await flush();

    // Read Agent a first so it is held in place as Seen.
    const closeA = openChat("mac", "a");
    await flush();
    closeA();
    await flush();
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent a");
    expect(sectionByTitle(stateName("done"))?.textContent).toContain("Agent b");

    // Click Mark all as seen and confirm in the dialog: both Agent b (unread) and Agent a (already read & held) leave at once.
    const markAllBtn = [...agentsRoot().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("lib-state-labels.mark_all_seen")))!;
    await act(async () => { markAllBtn.click(); });
    await flush();
    const confirmBtn = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent?.includes("1"))!;
    await act(async () => { confirmBtn.click(); });
    await flush();
    expect(sectionByTitle(stateName("done"))).toBeUndefined();
    expect(clearSeenBtn()).toBeNull();
  });

  it("navigates to Settings › Seen Agents when clicking Change in Settings on the notice", async () => {
    localStorage.setItem("persona.agentGrouping", "state");
    await mountLegacy([row("a", "unread")]);
    act(() => markSeenLocally("mac", "a"));
    await flush();

    const btn = host.querySelector<HTMLButtonElement>('[data-testid="agents-seen-notice-settings"]')!;
    expect(btn.textContent).toBe(t("seen-rows.change_in_settings"));
    await act(async () => { btn.click(); });
    expect(location.pathname + location.hash).toBe("/settings#settings-seen-rows");
  });
});

describe("Web Agents seen rows (paged list / BoundedAgentsBlock)", () => {
  it("keeps a read row in By status Done with count 0 and Clear seen (1) even after hub counts drop to 0", async () => {
    await loadPages();
    localStorage.setItem("persona.agentGrouping", "state");
    let serverRows: Session[] = [row("p1", "unread")];
    vi.stubGlobal("fetch", vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      if (url.pathname.endsWith("/sessions")) {
        const st = url.searchParams.get("state");
        const matched = st ? serverRows.filter((r) => r.state === st) : serverRows;
        const counts = {
          total: serverRows.length,
          state: {
            unread: serverRows.filter((r) => r.state === "unread").length,
            idle: serverRows.filter((r) => r.state === "idle").length,
          },
          machine: { mac: serverRows.length },
          project: {},
          group: {},
          revision: "r1",
        };
        if (url.searchParams.has("counts") && !url.searchParams.has("since")) return json(counts);
        return json({ sessions: matched, remove: [], added: [], has_more: false, revision: "r1", total: matched.length, counts });
      }
      if (url.pathname.endsWith("/machines")) return json({ machines: [mac] });
      if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
      if (url.pathname.endsWith("/todos")) return json({ todos: [] });
      return json({});
    }));

    act(() => {
      connect();
      const sock = FakeSocket.all.at(-1)!;
      sock.open();
      sock.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_todo", "sessions_agents_only", "sessions_page_counts", "sessions_just_finished", "sessions_quick_filters"] });
    });
    await flush();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(host.querySelector("[data-paged-sessions]")).toBeNull();
    const unreadGroup = () => sectionByTitle(stateName("done")) ?? null;
    expect(unreadGroup()?.textContent).toContain("Agent p1");

    // User opens p1 and closes it; hub marks it idle.
    const closeP1 = openChat("mac", "p1");
    await flush();
    closeP1();
    serverRows = [row("p1", "idle", { seen_at: "2026-01-01T00:02:00Z" })];
    act(() => FakeSocket.all.at(-1)!.push({ type: "sessions_list" }));
    await flush();

    // Unread group stays visible with count 0, Agent p1 drawn as Seen, and Clear seen (1).
    expect(unreadGroup()?.textContent).toContain("Agent p1");
    expect(unreadGroup()?.querySelector('a[href="/s/mac/p1"] .status-mark')?.getAttribute("data-state")).toBe("idle");
    expect(clearSeenBtn(unreadGroup()!)?.textContent).toBe(t("seen-rows.clear_seen", { count: 1 }));
    expect(noticeEl()).not.toBeNull();

    // Clear seen drops it cleanly.
    await act(async () => { clearSeenBtn(unreadGroup()!)!.click(); });
    await flush();
    expect(unreadGroup()).toBeNull();
  });
});
