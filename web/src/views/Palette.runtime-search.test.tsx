// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof matchMedia;
  Element.prototype.scrollIntoView ??= () => {};
});
import { setToken } from "../lib/api";
import { closePalette, openPalette, setPaletteQuery } from "../lib/palette";
import { getState, reset } from "../lib/store";
import { t } from "../i18n";
import type { Session } from "../lib/types";
import { Palette } from "./Palette";
import { mergeRuntimeSession } from "./palette/RuntimeSearchResults";
// The asynchronous intent imports are cached, but still execute their production lifecycle in the mounted Palette.
await import("../lib/agent-search");
await import("../lib/runtime-search");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const agent = (id: string, title = `needle ${id}`, extra: Partial<Session> = {}): Session => ({ machine_id: "one", session_id: id, title, cwd: "/work/alpha", state: "idle", status: "idle", updated_at: "2026-11-14T08:00:00Z", rev: 2, ...extra });
const local = [agent("local-a", "needle local A"), agent("local-b", "needle local B")];
type Match = { session: Session; score: number; evidence?: { role: string; before: string; match: string; after: string } };
const page = (hits: Match[], extra: Record<string, unknown> = {}) => ({ sessions: hits.map((hit) => hit.session), search_hits: Object.fromEntries(hits.map((hit) => [JSON.stringify([hit.session.machine_id, hit.session.session_id]), { score: hit.score, evidence: hit.evidence }])), revision: "frozen-query", total: hits.length, has_more: false, remove: [], ...extra });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement, root: Root, closed: number;
let requests: { url: URL; signal?: AbortSignal | null; headers: Headers }[];
let reply: (url: URL, init: RequestInit) => Promise<Response>;
const flush = async () => { for (let index = 0; index < 10; index++) await act(async () => { await Promise.resolve(); }); };
const settled = async () => { await act(async () => { await vi.dynamicImportSettled(); }); await flush(); await act(async () => { vi.advanceTimersByTime(210); }); await flush(); };
const search = async (query = "needle", open = true) => {
  act(() => { openPalette(); setPaletteQuery(query); root.render(<Palette open={open} onClose={() => closed++} onNewAgent={() => {}} />); });
  await settled();
};
const options = () => [...document.querySelectorAll<HTMLButtonElement>('[role="option"]')];
const field = () => document.querySelector<HTMLInputElement>('[role="combobox"]')!;
const selected = () => document.getElementById(field().getAttribute("aria-activedescendant") ?? "");
const key = (value: string) => act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: value, bubbles: true })));
const button = (key: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === t(`runtime-search.${key}`))!;
const reads = () => requests.filter((request) => request.url.searchParams.has("search"));

beforeEach(() => {
  reset(); localStorage.clear(); closed = 0; requests = [];
  Object.assign(getState(), { loaded: true, features: ["sessions_list", "sessions_search"], sessionSummaries: local, sessions: {}, assistant: null, machines: [{ id: "one", name: "First computer", online: true }, { id: "two", name: "Second computer", online: true }] });
  reply = async () => json(page([]));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  vi.stubGlobal("fetch", vi.fn((input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://example.invalid");
    requests.push({ url, signal: init.signal, headers: new Headers(init.headers) });
    return url.searchParams.has("search") ? reply(url, init) : Promise.resolve(json({}));
  }));
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => { root.unmount(); closePalette(); reset(); setToken(""); }); host.remove();
  vi.useRealTimers(); vi.unstubAllGlobals();
});

describe("server-backed runtime search in the palette", () => {
  it("finds an unloaded Agent from evidence beyond its truncated summary and opens that exact session", async () => {
    reply = async () => json(page([{ session: agent("unloaded", "Other work", { last_text: "Truncated preview" }), score: 45, evidence: { role: "agent", before: "The older preview contains ", match: "needle", after: " after its cutoff" } }]));
    await search();
    expect(reads()).toHaveLength(1);
    expect(reads()[0].url.searchParams.get("search")).toBe("needle");
    expect(reads()[0].url.searchParams.get("limit")).toBe("40");
    expect(reads()[0].url.searchParams.get("roots")).toBe("1");
    expect(options()).toHaveLength(1);
    expect(options()[0].textContent).toContain("Other work");
    expect(options()[0].querySelector("[data-evidence] mark")?.textContent).toBe("needle");
    // The initial local selection was removed by authoritative results: Enter cannot pick another Agent silently.
    expect(selected()).toBeNull(); key("Enter"); expect(closed).toBe(0);
    key("ArrowDown"); expect(selected()).toBe(options()[0]); key("Enter");
    expect(closed).toBe(1); expect(decodeURIComponent(location.pathname)).toContain("unloaded");
  });

  it("retains selected identity when a higher-ranked server row arrives, and removes stale local-only matches on completion", async () => {
    let complete!: (response: Response) => void;
    reply = () => new Promise((resolve) => { complete = resolve; });
    await search(); key("ArrowDown");
    expect(selected()?.textContent).toContain("needle local B");
    await act(async () => complete(json(page([
      { session: agent("first", "needle best", { updated_at: "2026-11-14T09:00:00Z" }), score: 105 },
      { session: local[1], score: 105 },
    ])))); await flush();
    expect(options()).toHaveLength(2);
    expect(options()[0].textContent).toContain("needle best");
    expect(selected()?.textContent).toContain("needle local B");
    expect(options().some((option) => option.textContent?.includes("needle local A"))).toBe(false);
    key("Enter"); expect(decodeURIComponent(location.pathname)).toContain("local-b");
  });

  it("deduplicates tuple identities while retaining distinct machines and richer same-version local titles", async () => {
    const full = agent("same", "needle complete local title");
    Object.assign(getState(), { sessionSummaries: [full] });
    reply = async () => json(page([
      { session: { ...full, title: "needle complete" }, score: 105 },
      { session: agent("same", "needle second computer", { machine_id: "two" }), score: 105 },
    ]));
    await search();
    expect(options()).toHaveLength(2);
    expect(options()[0].textContent).toContain("needle complete local title");
    expect(options()[1].textContent).toContain("Second computer");
  });

  it("makes the second server page visible beyond the old 40-row cap without closing the palette", async () => {
    const hits = Array.from({ length: 41 }, (_, index) => ({ session: agent(`remote-${String(index).padStart(2, "0")}`), score: 85 }));
    reply = async (url) => json(url.searchParams.has("cursor") ? page(hits.slice(40), { total: 41 }) : page(hits.slice(0, 40), { total: 41, has_more: true, next_cursor: "next" }));
    await search();
    expect(options()).toHaveLength(40);
    expect(options().filter((option) => option.textContent?.includes("remote-"))).toHaveLength(40);
    act(() => { button("more").click(); button("more").click(); }); await flush();
    expect(reads()).toHaveLength(2); expect(reads()[1].url.searchParams.get("cursor")).toBe("next");
    expect(options()).toHaveLength(41);
    expect(options().at(-1)?.textContent).toContain("remote-40");
    expect(closed).toBe(0); expect(button("more")).toBeUndefined();
    expect(document.querySelector('[role="listbox"] [data-runtime-search-status]')).toBeNull();
  });

  it("cancels superseded queries and closing, ignoring late responses", async () => {
    let complete!: (response: Response) => void;
    reply = () => new Promise((resolve) => { complete = resolve; });
    await search("first-query"); const first = reads()[0];
    act(() => setPaletteQuery("second-query")); await flush();
    expect(first.signal?.aborted).toBe(true);
    await act(async () => complete(json(page([{ session: agent("stale", "first-query stale"), score: 105 }]))));
    reply = async () => json(page([{ session: agent("new", "second-query result"), score: 105 }]));
    await settled(); expect(options().map((option) => option.textContent).join(" ")).not.toContain("stale");
    expect(options().map((option) => option.textContent).join(" ")).toContain("second-query result");
    reply = () => new Promise(() => {});
    act(() => setPaletteQuery("third-query")); await settled(); const pending = reads().at(-1)!;
    act(() => root.render(<Palette open={false} onClose={() => closed++} onNewAgent={() => {}} />)); await flush();
    expect(pending.signal?.aborted).toBe(true);
    await settled(); expect(reads()).toHaveLength(3);
  });

  it("isolates credential replacement and starts a fresh search for the new owner", async () => {
    setToken("synthetic-first");
    let complete!: (response: Response) => void;
    reply = () => new Promise((resolve) => { complete = resolve; });
    await search(); const old = reads()[0];
    reply = async () => json(page([{ session: agent("new-owner"), score: 105 }]));
    act(() => setToken("synthetic-second")); await settled();
    expect(old.signal?.aborted).toBe(true);
    await act(async () => complete(json(page([{ session: agent("old-owner"), score: 105 }])))); await flush();
    expect(options().map((option) => option.textContent).join(" ")).toContain("new-owner");
    expect(options().map((option) => option.textContent).join(" ")).not.toContain("old-owner");
    expect(reads()).toHaveLength(2);
  });

  it("keeps local matches and offers explicit retry after failure, without claiming no results", async () => {
    reply = async () => json({ error: "Synthetic failure" }, 500);
    await search(); expect(options()).toHaveLength(2);
    expect(document.querySelector('[data-runtime-search-status] [role="alert"]')).not.toBeNull();
    expect(button("retry")).toBeDefined();
    reply = async () => json(page([{ session: local[0], score: 105 }]));
    act(() => button("retry").click()); await flush();
    expect(options()).toHaveLength(1); expect(reads()).toHaveLength(2);
  });

  it("preserves raw operators and leaves an old bounded hub explicitly limited to loaded Agents", async () => {
    await search("machine:one needle");
    expect(reads()[0].url.searchParams.get("search")).toBe("machine:one needle");
    act(() => root.unmount()); root = createRoot(host);
    Object.assign(getState(), { features: ["sessions_list"] }); requests = [];
    await search("needle");
    expect(reads()).toHaveLength(0); expect(options()).toHaveLength(2);
    expect(document.body.textContent).toContain(t("runtime-search.loaded_only"));
  });

  it("preserves loaded pages after a failure and retries the same cursor", async () => {
    const first = { session: agent("first-page"), score: 105 };
    const second = { session: agent("second-page"), score: 85 };
    let failing = true;
    reply = async (url) => url.searchParams.has("cursor")
      ? failing ? json({ error: "Synthetic failure" }, 500) : json(page([second], { total: 2 }))
      : json(page([first], { total: 2, has_more: true, next_cursor: "next" }));
    await search(); act(() => button("more").click()); await flush();
    expect(options()).toHaveLength(1);
    expect(options()[0].textContent).toContain("first-page");
    expect(button("retry")).toBeDefined(); failing = false;
    act(() => button("retry").click()); await flush();
    expect(reads().slice(1).map((request) => request.url.searchParams.get("cursor"))).toEqual(["next", "next"]);
    expect(options()).toHaveLength(2);
    expect(options()[1].textContent).toContain("second-page");
  });

  it("treats ignored search parameters as unsupported, retains local matches, and offers no dead Retry button", async () => {
    reply = async () => json({ sessions: [agent("unfiltered")], has_more: false, revision: "old", total: 1 });
    await search(); expect(options()).toHaveLength(2);
    expect(document.body.textContent).toContain(t("runtime-search.loaded_only"));
    expect(document.body.textContent).toContain(t("runtime-search.unsupported"));
    expect(button("retry")).toBeUndefined();
    expect(document.body.textContent).not.toContain("unfiltered");
  });

  it("never lets omitted fields from an older local revision change the server row's state", () => {
    const remote = agent("same", "needle renamed", { rev: 3 });
    const merged = mergeRuntimeSession(remote, agent("same", "needle old", { rev: 2, start_error: "Old error", state: "needs_you" }));
    expect(merged).toEqual(remote);
  });

  it("explains an oversized query without offering a Retry that cannot run", async () => {
    await search("x".repeat(1025));
    expect(reads()).toHaveLength(0);
    expect(document.body.textContent).toContain(t("runtime-search.query_too_long"));
    expect(button("retry")).toBeUndefined();
  });

  it("loads page aliases with search intent and keeps them separate from Agent results", async () => {
    await search("cpu");
    expect(options().some((option) => option.textContent?.includes("机器"))).toBe(true);
    await search("todo");
    expect(options().some((option) => option.textContent?.includes("待办"))).toBe(true);
  });

  it("keeps the status in view when a load-more request fails", async () => {
    reply = async (url) => url.searchParams.has("cursor") ? json({ error: "Synthetic failure" }, 500)
      : json(page([{ session: agent("first-page"), score: 105 }], { total: 2, has_more: true, next_cursor: "next" }));
    await search(); key("ArrowDown");
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView"); scroll.mockClear();
    act(() => { button("more").focus(); button("more").click(); }); await flush();
    expect(button("retry")).toBeDefined();
    expect(scroll).not.toHaveBeenCalled(); scroll.mockRestore();
  });

  it.each(["identity", "submillisecond"])("keeps server tie ordering by %s even when local rows arrived in reverse order", async (kind) => {
    const a = agent(kind === "identity" ? "a" : "z", "needle first", { updated_at: kind === "identity" ? "2026-11-14T08:00:00Z" : "2026-11-14T08:00:00.000000002Z" });
    const b = agent(kind === "identity" ? "b" : "a", "needle second", { updated_at: kind === "identity" ? "2026-11-14T08:00:00Z" : "2026-11-14T08:00:00.000000001Z" });
    Object.assign(getState(), { sessionSummaries: [b, a] });
    reply = async () => json(page([{ session: a, score: 105 }, { session: b, score: 105 }]));
    await search();
    expect(options()).toHaveLength(2);
    expect(options()[0].textContent).toContain("needle first");
    expect(options()[1].textContent).toContain("needle second");
  });

  it("returns focus to the combobox when keyboard-activated final More disappears", async () => {
    reply = async (url) => json(url.searchParams.has("cursor")
      ? page([{ session: agent("last"), score: 85 }], { total: 2 })
      : page([{ session: agent("first"), score: 105 }], { total: 2, has_more: true, next_cursor: "next" }));
    await search();
    act(() => { const more = button("more"); more.focus(); more.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); more.click(); });
    await flush();
    expect(button("more")).toBeUndefined();
    expect(document.activeElement).toBe(field());
    key("ArrowDown"); key("Enter"); expect(closed).toBe(1);
  });

  it("returns focus after Retry is removed, without stealing focus the user moved elsewhere", async () => {
    reply = async () => json({ error: "Synthetic failure" }, 500);
    await search();
    reply = async () => json(page([{ session: agent("found"), score: 105 }]));
    act(() => { button("retry").focus(); button("retry").click(); }); await flush();
    expect(button("retry")).toBeUndefined(); expect(document.activeElement).toBe(field());
    let complete!: (value: Response) => void;
    reply = async (url) => url.searchParams.has("cursor") ? new Promise((resolve) => { complete = resolve; })
      : json(page([{ session: agent("first"), score: 105 }], { total: 2, has_more: true, next_cursor: "next" }));
    await search("other");
    act(() => { button("more").focus(); button("more").click(); }); await flush();
    const target = options()[0]; act(() => target.focus());
    await act(async () => complete(json(page([{ session: agent("last"), score: 85 }], { total: 2 })))); await flush();
    expect(document.activeElement).toBe(target);
  });

  it("focuses Search again beside the error when an expired cursor replaces More", async () => {
    reply = async (url) => url.searchParams.has("cursor") ? json({ code: "cursor_expired", error: "Expired cursor" }, 409)
      : json(page([{ session: agent("first"), score: 105 }], { total: 2, has_more: true, next_cursor: "next" }));
    await search(); key("ArrowDown");
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView"); scroll.mockClear();
    act(() => { button("more").focus(); button("more").click(); }); await flush();
    expect(button("more")).toBeUndefined();
    expect(document.activeElement).toBe(button("search_again"));
    expect(document.body.textContent).toContain(t("runtime-search.expired"));
    expect(scroll).not.toHaveBeenCalled(); scroll.mockRestore();
    reply = async () => json(page([{ session: agent("renewed"), score: 105 }]));
    act(() => button("search_again").click()); await flush();
    expect(document.activeElement).toBe(field());
    expect(options()[0].textContent).toContain("renewed");
  });

  it("never starts runtime search for an empty query, command mode, or a closed palette", async () => {
    await search(""); await search(">new"); await search("needle", false);
    expect(reads()).toHaveLength(0);
  });
});
