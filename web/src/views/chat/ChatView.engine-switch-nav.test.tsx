// @vitest-environment jsdom
// A conversation that went back to its first engine (#5054, A → B → A): the live session's early stretch is drawn above
// B's by EarlierStretches, so the chat's own rail, jumps and search cover only the live stretch (docs/web/web.md). Its
// inputs before the live stretch are no ticks (they could never be reached), and the search neither finds them nor
// offers to load past the live stretch's start.
import { openAgentMore } from "../../test/agent-menu";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a mouse: the rail is on
  g.matchMedia = (q: string) => ({ matches: q.includes("pointer: fine"), addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  (g.CSS as { escape?: (s: string) => string } | undefined) ?? (g.CSS = {});
  (g.CSS as { escape?: (s: string) => string }).escape ??= (s: string) => s;
});

const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "", loadingEarlier: false, loadEarlier: async () => {}, expandFold: async () => {}, reload: () => {} }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
// what the chat hands its rail and its search
const seen = vi.hoisted(() => ({ rail: null as null | { ticks: { id: string }[]; total?: number }, search: null as null | { items: Item[]; hasMore: boolean } }));
vi.mock("./NavRail", async (importOriginal) => ({ ...await importOriginal<typeof import("./NavRail")>(), NavRail: (p: { ticks: { id: string }[]; total?: number }) => { seen.rail = p; return null; } }));
vi.mock("./ChatSearch", async (importOriginal) => ({ ...await importOriginal<typeof import("./ChatSearch")>(), ChatSearch: (p: { items: Item[]; hasMore: boolean }) => { seen.search = p; return null; } }));

const { ChatView } = await import("./ChatView");
const { getState, reset, upsertSession } = await import("../../lib/store");
const { t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-12T09:00:00Z");
const at = (m: number) => new Date(T0 + m * 60_000).toISOString();
const user = (seq: number, m: number, text: string): Item => ({ id: `au${seq}#0`, seq, role: "user", kind: "text", text, ts: at(m) });
const said = (seq: number, m: number, text: string): Item => ({ id: `aa${seq}#0`, seq, role: "assistant", kind: "text", text, ts: at(m) });
const resumed = '<persona-context kind="engine-switch" title="Handoff" from="codex" to="claude" resumed="1">\nWhat happened meanwhile.\n</persona-context>\n\n';

let host: HTMLDivElement;
let root: Root;
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 200 && !ok(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 5)); });
};
// ChatSearch is deferred until its first open (#5526): use the real menu entry before inspecting its props.
const openSearch = async () => {
  const more = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!;
  act(() => more.click());
  openAgentMore();
  await until(() => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].some((item) => item.textContent === t("chat-chatview.search_menu")));
  const search = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent === t("chat-chatview.search_menu"))!;
  await act(async () => { search.click(); await import("./ChatSearch"); });
};

beforeEach(() => {
  localStorage.clear();
  seen.rail = seen.search = null;
  Object.assign(getState(), { loaded: true });
  // A ran the conversation, then B, then A again: A's early stretch (seq 1-2) is the first, its live one starts at seq 3
  upsertSession({ machine_id: "m", session_id: "a", title: "Login page", cwd: "/w", status: "idle", state: "idle", agent: "claude", engine_history: [{ session_id: "a", agent: "claude", until: at(10) }, { session_id: "b", agent: "codex", until: at(20) }] });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    // the whole live session's inputs, as the hub indexes them: its early stretch's too
    if (url.includes("/sessions/m/a/items?user_inputs=1")) return json({ total: 2, inputs: [{ id: "au1#0", seq: 1 }, { id: "au3#0", seq: 3 }], history: "" });
    return json({ items: [] });
  }));
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

describe("the rail and the search in A → B → A", () => {
  it("cover the live stretch only, and nothing past its start is left to load for them", async () => {
    Object.assign(transcript, { items: [], loading: true, hasMore: true });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    // A's newest page reaches back into its early stretch; A has older pages still
    Object.assign(transcript, {
      loading: false,
      items: [user(1, 1, "Build a login page."), said(2, 2, "The login page is in."), user(3, 21, `${resumed}Review the tests.`), said(4, 22, "They look right.")],
    });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    await openSearch();
    await until(() => (seen.rail?.ticks.length ?? 0) > 0 && seen.rail?.total !== undefined && seen.search !== null);
    expect(seen.rail!.ticks.map((tick) => tick.id)).toEqual(["seg:au3#0"]);
    expect(seen.rail!.total).toBe(1);
    expect(seen.search!.items.map((i) => i.id)).toEqual(["au3#0", "aa4#0"]);
    expect(seen.search!.hasMore).toBe(false);
  });

  it("page back through the live stretch while its start is not loaded", async () => {
    Object.assign(transcript, { items: [], loading: true, hasMore: true });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    Object.assign(transcript, { loading: false, items: [user(3, 21, `${resumed}Review the tests.`), said(4, 22, "They look right.")] });
    act(() => root.render(<ChatView mid="m" sid="a" onToggleSidebar={() => {}} />));
    await openSearch();
    await until(() => seen.rail?.total !== undefined && seen.search !== null);
    expect(seen.rail!.ticks.map((tick) => tick.id)).toEqual(["seg:au3#0"]);
    expect(seen.search!.items.map((i) => i.id)).toEqual(["au3#0", "aa4#0"]);
    expect(seen.search!.hasMore).toBe(true);
  });
});
