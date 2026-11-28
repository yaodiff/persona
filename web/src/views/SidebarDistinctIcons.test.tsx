// @vitest-environment jsdom
// Each built-in entry of the sidebar draws a glyph of its own (#16282), so a monochrome sidebar and the folded rail
// tell them apart: the main chat's row, every page of the sidebar's table (useNavPages, the optional ones turned on),
// History (where its row is, by the hub's features), All Persona chats and New chat. Scheduled tasks and History both
// drew `clock`, the main chat and Overview both `home`. The glyphs are compared as drawn (the svg's markup), so two
// icon names with the same paths count as one glyph. The entries this issue changed, Overview and History, also draw no
// glyph an extension's page can name: an extension may name any icon of the vocabulary for its nav_page. Every
// History row, the sidebar's (in either place) and the phone's Agents page's, draws HISTORY_ICON.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// the pages that are off unless the hub or a setting turns them on: the workflow board and Starred
vi.mock("../lib/workspace-board-setting", async (original) => ({
  ...(await original<typeof import("../lib/workspace-board-setting")>()),
  useWorkspaceBoardSetting: () => ({ enabled: true, canWrite: false, retry: async () => {} }),
}));
vi.mock("../lib/starred-summary", async (original) => ({
  ...(await original<typeof import("../lib/starred-summary")>()),
  useStarredCount: () => 0,
}));
import { t } from "../i18n";
import { MODULES } from "../modules";
import { ROW_ICONS, UI_ICONS } from "../lib/extension-ui";
import { NAV_ICONS } from "../lib/extension-wire";
import { href } from "../lib/router";
import { connect, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { Icon, type IconName } from "../ui/icons";
import { navIcon } from "./extensions/ExtensionSlots";
import { NEW_ICON_GLYPH } from "./extensions/icon-map";
import { ROW_ICON } from "./extensions/ViewBody";
import { AgentsList, type NavPage, Sidebar, SidebarRail, useNavPages } from "./Sidebar";
import { HISTORY_ICON } from "./SidebarParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// the modules' pages (Health, Messages) when this run has them: the core-only lane (#13932) runs with none
const MODULE_PAGES = MODULES.flatMap((m) => m.navItems?.map((n) => n.key) ?? []);
/** Every glyph an extension's icon name can draw on the web: a row's (ROW_ICON, with the UI version 6 names) and a
 *  nav_page's (navIcon over every name the schema and each UI version know, and `map`). #16296 gives a nav_page the
 *  rows' whole set, so all of it counts. */
const EXTENSION_GLYPHS = new Set<IconName>([
  ...Object.values(ROW_ICON),
  ...Object.values(NEW_ICON_GLYPH),
  ...[...NAV_ICONS, ...UI_ICONS, ...ROW_ICONS, "map"].map((name) => navIcon(name)),
]);
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

const chat = (session_id: string, title: string): Session => ({ machine_id: "hub", session_id, title, cwd: "/persona", status: "idle", state: "idle", assistant: true, origin: "user", updated_at: "2026-01-01T00:00:00Z" });
const agent: Session = { machine_id: "computer", session_id: "a1", title: "Read the logs", cwd: "/code/demo", status: "idle", state: "idle", updated_at: "2026-01-01T00:00:00Z" };
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));

let host: HTMLDivElement, root: Root;
let pages: NavPage[] = [];
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };

/** The sidebar's own table of pages, as the sidebar reads it. */
function Table() {
  pages = useNavPages().all;
  return null;
}

async function render(features: string[], view: React.ReactNode) {
  vi.stubGlobal("fetch", vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    if (url.pathname === "/api/v1/assistant") return json({ machine_id: "hub", main: "main", conversations: [chat("main", "Persona"), chat("trip", "Trip plan")] });
    if (url.pathname === "/api/v1/sessions") return json(url.search ? { sessions: [agent], remove: [], has_more: false, revision: "r1", total: 1 } : { sessions: [agent] });
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
  act(() => root.render(<>{view}<Table /></>));
  await flush();
}

/** The glyph a link draws: its svg's markup. */
const glyph = (a: Element | null | undefined) => a?.querySelector("svg")?.innerHTML;
const linkTo = (path: string, text?: string) => [...host.querySelectorAll<HTMLAnchorElement>("a")]
  .find((a) => a.getAttribute("href") === path && (text === undefined || a.textContent?.trim() === text));

/** Each icon's markup as drawn, to the icon's name. */
function drawnGlyphs(names: Iterable<IconName>): Map<string, IconName> {
  const list = [...names];
  const box = document.createElement("div");
  const drawn = createRoot(box);
  act(() => drawn.render(<>{list.map((name) => <Icon key={name} name={name} />)}</>));
  const out = new Map([...box.querySelectorAll("svg")].map((svg, i) => [svg.innerHTML, list[i]!]));
  act(() => drawn.unmount());
  return out;
}

/** The entries that draw the same glyph, as groups of their names: none when every one has its own. */
function clashes(entries: Record<string, string | undefined>) {
  const by = new Map<string, string[]>();
  for (const [name, g] of Object.entries(entries)) by.set(g ?? "", [...(by.get(g ?? "") ?? []), name]);
  return [...by.values()].filter((names) => names.length > 1);
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("lark.homeMessages", "1"); // Messages shows in the sidebar once this browser turned it on
  reset();
  pages = [];
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
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

describe("the sidebar's built-in entries (#16282)", () => {
  it.each([
    { where: "History after the Agents list", features: [] },
    { where: "History under Pending, in the Agents list's place", features: ["sessions_dimensions", "lark"] },
  ])("draw pairwise distinct icons: $where", async ({ features }) => {
    await render(features, <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    // the optional pages are on, so the whole table is checked
    expect(pages.map((p) => p.k)).toEqual(expect.arrayContaining(["home", "inbox", "starred", "workspace-board", "todos", "routines", "kb", "machines", "settings", ...MODULE_PAGES]));
    const entries: Record<string, string | undefined> = {
      [t("sidebar.main_chat")]: glyph(host.querySelector(`a[aria-description="${t("sidebar.persona_main")}"]`)),
      [t("work-entry.title")]: glyph(linkTo(href({ name: "terminal" }), t(features.includes("sessions_dimensions") ? "work-entry.all" : "work-entry.title"))),
      [t("sidebar.all_chats")]: glyph(linkTo(href({ name: "history" }))),
      [t("sidebar.new_chat")]: glyph(linkTo(href({ name: "persona-new" }))),
    };
    for (const p of pages) entries[`${p.label} (${p.k})`] = glyph(linkTo(href(p.to), undefined));
    // every entry is drawn with an icon, so a missing row cannot pass as distinct
    expect(Object.entries(entries).filter(([, g]) => !g).map(([name]) => name)).toEqual([]);
    expect(clashes(entries)).toEqual([]);
  });

  it("Overview and History draw no glyph an extension's page can name", async () => {
    await render([], <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    expect(EXTENSION_GLYPHS.size).toBeGreaterThan(60);
    const overview = pages.find((p) => p.k === "home")!;
    expect({ overview: EXTENSION_GLYPHS.has(overview.icon), history: EXTENSION_GLYPHS.has(HISTORY_ICON) }).toEqual({ overview: false, history: false });
    // as drawn too: no extension icon's markup is theirs under another name
    const extension = drawnGlyphs(EXTENSION_GLYPHS);
    const drawn = {
      overview: glyph(linkTo(href(overview.to))),
      history: glyph(linkTo(href({ name: "terminal" }), t("work-entry.title"))),
    };
    expect(drawn.overview && drawn.history).toBeTruthy();
    expect({ overview: extension.get(drawn.overview!), history: extension.get(drawn.history!) }).toEqual({ overview: undefined, history: undefined });
  });

  it.each([
    { where: "the sidebar, after the Agents list", features: [], text: "work-entry.title", view: () => <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} /> },
    { where: "the sidebar, under Pending", features: ["sessions_dimensions", "lark"], text: "work-entry.all", view: () => <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} /> },
    { where: "the phone's Agents page", features: [], text: "work-entry.title", view: () => <AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} /> },
  ])("History draws HISTORY_ICON in $where", async ({ features, text, view }) => {
    const history = [...drawnGlyphs([HISTORY_ICON]).keys()][0];
    await render(features, view());
    expect(glyph(linkTo(href({ name: "terminal" }), t(text)))).toBe(history);
  });

  it("draw pairwise distinct icons on the folded rail", async () => {
    await render([], <SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    const entries = Object.fromEntries([...host.querySelectorAll<HTMLAnchorElement>("nav a")]
      .filter((a) => a.querySelector("svg"))
      .map((a) => [a.getAttribute("aria-label") ?? a.getAttribute("href"), glyph(a)]));
    expect(Object.keys(entries)).toEqual(expect.arrayContaining(pages.map((p) => expect.stringContaining(p.label))));
    expect(clashes(entries)).toEqual([]);
  });
});
