// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const events = vi.hoisted(() => new Map<string, Set<(event: Record<string, unknown>) => void>>());
vi.mock("../../lib/store", async (load) => ({
  ...(await load<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, listener: (event: Record<string, unknown>) => void) => {
    if (!events.has(type)) events.set(type, new Set());
    events.get(type)!.add(listener);
    return () => events.get(type)?.delete(listener);
  },
}));
import { t } from "../../i18n";
import { setToken } from "../../lib/api";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import type { BoardCard } from "../../lib/workspace-board";
import { ActivityDialog, CardActivity } from "./Activity";
import { WorkspaceBoard } from "../WorkspaceBoard";
vi.mock("../../lib/workspace-board-setting", () => ({ useWorkspaceBoardSetting: () => ({ enabled: true }) }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const word = (key: string, values?: Parameters<typeof t>[1]) => t(`workspaceboard-activity.${key}`, values);
const at = "2026-11-14T08:00:00Z";
const first = { revision: "12", at, change_count: 2, card_count: 1, kinds: ["card_updated"] };
const removed = { revision: "11", at, change_count: 1, card_count: 1, kinds: ["card_removed"] };
const currentCard: BoardCard = { id: "a", title: "Current name", machine_id: "computer", workspace_id: "a", cwd: "/projects/a", source: "directory", column_id: "done", pinned: false, archived: false };
const changed = { kind: "card_updated", id: "a", title: "Historical name", fields: ["title", "column"], before: { title: "Original name", column_id: "todo", column_title: "Original column" }, after: { title: "Historical name", column_id: "done", column_title: "Finished then" } };
let host: HTMLDivElement, root: Root;
let requests: { url: URL; signal?: AbortSignal | null; headers: Headers }[];
let capability: boolean, writable: boolean;
let override: ((url: URL, init: RequestInit) => Promise<Response> | undefined) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const page = () => ({ entries: [first, removed], latest_revision: "12", oldest_revision: "1", next_cursor: "11", retention_limit: 500 });
const flush = async () => { for (let i = 0; i < 7; i++) await act(async () => { await Promise.resolve(); }); };
const listReads = () => requests.filter((request) => request.url.pathname.endsWith("/activity"));
const detailReads = () => requests.filter((request) => /\/activity\/\d+$/.test(request.url.pathname));
const button = (key: string, values?: Parameters<typeof t>[1]) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === word(key, values))!;
const click = (key: string, values?: Parameters<typeof t>[1]) => act(() => button(key, values).click());
const expand = (revision = "12") => act(() => document.querySelector<HTMLButtonElement>(`[data-activity-revision="${revision}"] > button`)!.click());
const show = async (onOpenCard = vi.fn()) => { act(() => root.render(<ActivityDialog currentCards={[currentCard]} onClose={() => root.render(null)} onOpenCard={onOpenCard} />)); await flush(); return onOpenCard; };
const push = (revision: number, manager = "assistant") => act(() => events.get("workspace_board")?.forEach((listener) => listener({ type: "workspace_board", revision, manager })));

beforeEach(() => {
  requests = []; override = undefined; capability = true; writable = true; localStorage.removeItem("persona.workspace-board.view"); events.clear(); setRosterDefault("assistant");
  vi.stubGlobal("fetch", vi.fn((input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://example.invalid");
    requests.push({ url, signal: init.signal, headers: new Headers(init.headers) });
    const custom = override?.(url, init); if (custom) return custom;
    if (url.pathname === "/api/v1/workspace-board") return Promise.resolve(json({ revision: 12, activity: capability, can_write: writable, columns: [{ id: "done", title: "Done" }], cards: [currentCard] }));
    if (url.pathname === "/api/v1/workspaces") return Promise.resolve(json({ workspaces: [] }));
    if (url.pathname.endsWith("/activity")) return Promise.resolve(json(url.searchParams.has("before") ? { entries: [{ ...first, revision: "10", card_count: 0, kinds: ["column_renamed"] }], latest_revision: "12", oldest_revision: "1", retention_limit: 500 } : page()));
    if (url.pathname.endsWith("/12")) return Promise.resolve(json(url.searchParams.get("offset") === "1"
      ? { revision: "12", at, changes: [{ kind: "card_updated", id: "a", title: "Historical name", fields: ["pinned", "archived"], before: { title: "Historical name", pinned: false, archived: false }, after: { title: "Historical name", pinned: true, archived: true } }], total: 2 }
      : { revision: "12", at, changes: [changed], total: 2, next_offset: 1 }));
    if (url.pathname.endsWith("/11")) return Promise.resolve(json({ revision: "11", at, changes: [{ kind: "card_removed", id: "gone", title: "Removed workspace", before: { title: "Removed workspace" } }], total: 1 }));
    return Promise.resolve(json({}));
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); resetManagerChoice(); setToken("");
  vi.unstubAllGlobals(); vi.restoreAllMocks();
});

describe("board activity readers", () => {
  it.each([true, false])("only reads activity on request, and can be opened with can_write=%s", async (canWrite) => {
    writable = canWrite;
    act(() => root.render(<WorkspaceBoard />)); await flush();
    expect(listReads()).toHaveLength(0);
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspaceboard.activity")}"]`)!.click()); await flush();
    expect(listReads()).toHaveLength(1);
    expand(); await flush(); click("open_workspace"); await flush();
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
    expect(document.querySelector('[role="dialog"] h2')?.textContent).toBe(currentCard.title);
    const entry = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((item) => item.textContent === t("workspaceboard.activity"))!;
    expect(entry.getAttribute("aria-expanded")).toBe("false");
    await act(async () => entry.click()); await flush();
    expect(listReads()).toHaveLength(2);
    expect(listReads().at(-1)?.url.searchParams.get("card_id")).toBe("a");
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
    await act(async () => entry.click()); await flush();
    expect(events.get("workspace_board")?.size).toBe(1); // Only the board itself remains subscribed.
  });

  it("hides both entry points when the manager does not advertise activity", async () => {
    capability = false;
    act(() => root.render(<WorkspaceBoard />)); await flush();
    expect(host.querySelector(`button[aria-label="${t("workspaceboard.activity")}"]`)).toBeNull();
    act(() => host.querySelector<HTMLButtonElement>('article[data-card-id="a"] button')!.click()); await flush();
    expect([...document.querySelectorAll('[role="dialog"] button')].some((item) => item.textContent === t("workspaceboard.activity"))).toBe(false);
    expect(listReads()).toHaveLength(0);
  });

  it("shows loading rather than an empty history until the default owner is known", async () => {
    resetManagerChoice();
    await show();
    expect(listReads()).toHaveLength(0);
    expect(document.body.textContent).toContain(word("loading"));
    expect(document.body.textContent).not.toContain(word("empty"));
    act(() => setRosterDefault("assistant")); await flush();
    expect(listReads()).toHaveLength(1);
  });

  it("reads summaries once, expands details lazily, and retains historical values rather than current titles", async () => {
    const open = await show();
    expect(listReads()).toHaveLength(1); expect(detailReads()).toHaveLength(0);
    expect(document.querySelectorAll("[data-activity-revision]")).toHaveLength(2);
    expand(); await flush(); expect(detailReads()).toHaveLength(1);
    expect(document.body.textContent).toContain("Original name");
    expect(document.body.textContent).toContain("Historical name");
    expect(document.body.textContent).toContain("Original column");
    expect(document.body.textContent).toContain("Finished then");
    expect(document.body.textContent).not.toContain("Current name");
    expect(document.querySelector("time")?.getAttribute("datetime")).toBe(at);
    expect(document.querySelector("time")?.getAttribute("title")).toMatch(/2026-11-14/);
    click("open_workspace"); expect(open).toHaveBeenCalledExactlyOnceWith("a");
    expand(); expand(); await flush(); expect(detailReads()).toHaveLength(1);
    click("more_details", { count: 1 }); await flush(); expect(detailReads()).toHaveLength(2);
    expect(document.body.textContent).toContain(word("archived"));
    expect(button("more_details", { count: 1 })).toBeUndefined();
    expand("11"); await flush();
    expect(document.querySelector('[data-activity-revision="11"]')?.textContent).toContain("Removed workspace");
    expect(document.querySelector('[data-activity-revision="11"] [data-activity-change] button')).toBeNull();
  });

  it("loads older groups without losing expanded details and retries a failed page in place", async () => {
    await show(); expand(); await flush();
    let failing = true;
    override = (url) => url.searchParams.has("before") && failing ? Promise.resolve(json({ error: "Temporary failure" }, 500)) : undefined;
    click("load_more"); await flush();
    expect(document.querySelectorAll("[data-activity-revision]")).toHaveLength(2);
    expect(document.body.textContent).toContain("Historical name");
    expect(document.querySelector('[role="alert"]')).not.toBeNull();
    failing = false; click("load_more"); await flush();
    expect(document.querySelectorAll("[data-activity-revision]")).toHaveLength(3);
    expect(document.querySelector('[data-activity-revision="12"] > button')?.getAttribute("aria-expanded")).toBe("true");
  });

  it("shows a new-activity hint without another request or changing the open row", async () => {
    await show(); expand(); await flush();
    push(13, "someone-else"); expect(document.body.textContent).not.toContain(word("newer"));
    push(13); await flush();
    expect(document.body.textContent).toContain(word("newer"));
    expect(listReads()).toHaveLength(1); expect(detailReads()).toHaveLength(1);
    expect(document.body.textContent).toContain("Historical name");
    override = (url) => url.pathname.endsWith("/activity") ? Promise.resolve(json({ ...page(), entries: [{ ...first, revision: "13" }, first], latest_revision: "13", next_cursor: "12" })) : undefined;
    click("show_latest"); await flush();
    expect(listReads()).toHaveLength(2); expect(document.body.textContent).not.toContain(word("newer"));
  });

  it("aborts pending requests and removes event listeners when the reader closes", async () => {
    override = (url) => url.pathname.endsWith("/activity") ? new Promise(() => {}) : undefined;
    await show(); expect(listReads()).toHaveLength(1);
    expect(document.body.textContent).toContain(word("loading"));
    const pending = listReads()[0];
    act(() => root.render(null));
    expect(pending.signal?.aborted).toBe(true);
    expect(events.get("workspace_board")?.size).toBe(0);
    push(13); expect(listReads()).toHaveLength(1);
  });

  it("resumes an expanded detail read cancelled by refreshing the summaries", async () => {
    await show();
    override = (url) => url.pathname.endsWith("/12") ? new Promise(() => {}) : undefined;
    expand(); await flush();
    const pending = detailReads()[0];
    override = undefined;
    click("refresh"); await flush();
    expect(pending.signal?.aborted).toBe(true);
    expect(detailReads()).toHaveLength(2);
    expect(document.body.textContent).toContain("Historical name");
  });

  it("keeps per-card activity inline, with matching updates and explicitly labeled full update details", async () => {
    act(() => root.render(<CardActivity cardID="a / b" />)); await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(listReads()[0].url.searchParams.get("card_id")).toBe("a / b");
    expand(); await flush();
    expect(detailReads()[0].url.searchParams.has("card_id")).toBe(false);
    expect(document.body.textContent).toContain(word("all_changes"));
    act(() => root.render(<CardActivity cardID="second" />)); await flush();
    expect(listReads().at(-1)?.url.searchParams.get("card_id")).toBe("second");
    expect(detailReads()).toHaveLength(1);
  });

  it("replaces the view after a default manager or credential change, fencing late responses", async () => {
    let complete!: (value: Response) => void;
    override = (url) => url.pathname.endsWith("/activity") ? new Promise((resolve) => { complete = resolve; }) : undefined;
    await show(); const old = listReads()[0];
    override = undefined;
    act(() => setRosterDefault("next-default")); await flush();
    expect(old.signal?.aborted).toBe(true);
    expect(listReads().at(-1)?.headers.get("X-Persona-Manager")).toBe("next-default");
    await act(async () => complete(json({ ...page(), entries: [{ ...first, revision: "1" }] }))); await flush();
    expect(document.querySelector('[data-activity-revision="1"]')).toBeNull();
    act(() => setToken("synthetic-second-owner")); await flush();
    expect(listReads()).toHaveLength(3);
  });

  it("survives StrictMode effect replay and gives an unknown change kind a safe label", async () => {
    override = (url) => url.pathname.endsWith("/activity") ? Promise.resolve(json({ ...page(), entries: [{ ...first, kinds: ["future_kind"] }], next_cursor: undefined })) : undefined;
    act(() => root.render(<StrictMode><CardActivity cardID="a" /></StrictMode>)); await flush();
    expect(document.querySelector('[data-activity-revision="12"]')).not.toBeNull();
    expect(document.body.textContent).toContain(word("other_change"));
    expect(document.body.textContent).not.toContain("future_kind");
    expect(listReads()[0].signal?.aborted).toBe(true);
  });
});
