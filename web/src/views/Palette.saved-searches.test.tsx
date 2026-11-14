// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof matchMedia;
  Element.prototype.scrollIntoView ??= () => {};
});
const events = vi.hoisted(() => new Map<string, Set<(event: Record<string, unknown>) => void>>());
vi.mock("../lib/store", async (load) => ({ ...(await load<typeof import("../lib/store")>()), onHubEvent: (type: string, listener: (event: Record<string, unknown>) => void) => {
  if (!events.has(type)) events.set(type, new Set()); events.get(type)!.add(listener);
  return () => events.get(type)?.delete(listener);
} }));
import { t } from "../i18n";
import { setToken } from "../lib/api";
import { setRosterDefault, resetManagerChoice, chooseManager } from "../lib/manager-choice";
import { closePalette, getPalette, openPaletteWith, setPaletteQuery, usePalette } from "../lib/palette";
import { getState, reset } from "../lib/store";
import type { SavedAgentSearch, SavedAgentSearches, SavedSearchMutation } from "../lib/saved-agent-searches";
import { Palette } from "./Palette";
await import("./palette/SavedSearchesDialog");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const word = (key: string, values?: Parameters<typeof t>[1]) => t(`saved-agent-searches.${key}`, values);
const id = (n: number) => `search_${n.toString(16).padStart(24, "0")}`;
const item = (n: number, name = `Search ${n}`, query = `machine:computer project:repo${n}`): SavedAgentSearch => ({ id: id(n), name, query, created_at: "2026-11-14T12:00:00Z", updated_at: "2026-11-14T12:00:00Z" });
let doc: SavedAgentSearches;
let host: HTMLDivElement, root: Root;
let requests: { method: string; path: string; url: URL; headers: Headers; signal?: AbortSignal | null; body?: SavedSearchMutation & { revision: string } }[];
let custom: ((request: typeof requests[number]) => Promise<Response> | undefined) | undefined;
let serial: number;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const writes = () => requests.filter((request) => request.method === "POST");
const reads = () => requests.filter((request) => request.method === "GET" && request.path === "/api/v1/saved-agent-searches");
const apply = (operation: SavedSearchMutation & { revision: string }): Response => {
  if (operation.revision !== doc.revision) return json({ code: "conflict", error: "Changed" }, 409);
  const searches = structuredClone(doc.searches);
  if (operation.action === "add") searches.push(item(++serial, operation.name.trim(), operation.query.trim()));
  else if (operation.action === "restore") {
    const before = operation.before_id ? searches.findIndex((search) => search.id === operation.before_id) : searches.length;
    searches.splice(before < 0 ? searches.length : before, 0, operation.item);
  } else {
    const at = searches.findIndex((search) => search.id === operation.id);
    if (at < 0) return json({ error: "Missing" }, 404);
    if (operation.action === "update") searches[at] = { ...searches[at], name: operation.name.trim(), query: operation.query.trim() };
    else {
      const [search] = searches.splice(at, 1);
      if (operation.action === "move") searches.splice(operation.before_id ? searches.findIndex((row) => row.id === operation.before_id) : searches.length, 0, search);
    }
  }
  doc = { ...doc, revision: String(BigInt(doc.revision) + 1n), searches };
  return json(doc);
};
function Shell() { const state = usePalette(); return <Palette open={state.open} onClose={closePalette} onNewAgent={() => { throw new Error("Saved searches never start Agents"); }} />; }
const flush = async () => { for (let n = 0; n < 8; n++) await act(async () => { await Promise.resolve(); }); };
const settle = async () => { await act(async () => { await vi.dynamicImportSettled(); }); await flush(); };
const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const button = (key: string, values?: Parameters<typeof t>[1]) => buttons().find((element) => element.textContent === word(key, values))!;
const named = (key: string, values?: Parameters<typeof t>[1]) => buttons().find((element) => element.getAttribute("aria-label") === word(key, values))!;
const press = async (key: string, values?: Parameters<typeof t>[1]) => { await act(async () => button(key, values).click()); await settle(); };
const manage = async () => { await act(async () => document.querySelector<HTMLButtonElement>(`button[aria-label="${t("palette.saved_searches")}"]`)!.click()); await settle(); };
const show = async (query = "") => { act(() => { openPaletteWith(query); root.render(<Shell />); }); await settle(); };
const editorField = (key: "name" | "query") => {
  const label = [...document.querySelectorAll<HTMLLabelElement>("label")].find((node) => node.textContent === word(key))!;
  return document.getElementById(label.htmlFor) as HTMLInputElement | HTMLTextAreaElement;
};
const fill = (key: "name" | "query", value: string) => act(() => {
  const field = editorField(key), proto = field instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(field, value); field.dispatchEvent(new Event("input", { bubbles: true }));
});
const push = async (revision = doc.revision, manager = "assistant") => { act(() => events.get("saved_agent_searches")?.forEach((listener) => listener({ type: "saved_agent_searches", revision, manager }))); await settle(); };
const dialogs = () => document.querySelectorAll('[role="dialog"]');
beforeEach(() => {
  doc = { revision: "1", can_write: true, searches: [item(1), item(2), item(3)] }; serial = 3; requests = []; custom = undefined; events.clear();
  reset(); resetManagerChoice(); setRosterDefault("assistant");
  Object.assign(getState(), { loaded: true, features: [], machines: [], sessions: {}, sessionSummaries: [], assistant: null });
  vi.stubGlobal("fetch", vi.fn((input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://example.invalid"), path = url.pathname;
    const request = { url, method: init.method ?? "GET", path, headers: new Headers(init.headers), signal: init.signal, body: init.body ? JSON.parse(String(init.body)) : undefined };
    requests.push(request);
    const response = custom?.(request); if (response) return response;
    if (path !== "/api/v1/saved-agent-searches") return Promise.resolve(json({}));
    return Promise.resolve(request.method === "GET" ? json(doc) : apply(request.body));
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => { root.unmount(); closePalette(); reset(); resetManagerChoice(); setToken(""); }); host.remove(); vi.unstubAllGlobals(); });

describe("saved searches in the palette", () => {
  it("saves the current query through one dialog, returns to it, and applies a saved shortcut with the keyboard", async () => {
    await show("state:working needle");
    expect(reads()).toHaveLength(1);
    await act(async () => buttons().find((element) => element.textContent === t("palette.save_search"))!.click()); await settle();
    expect(dialogs()).toHaveLength(1); expect(document.querySelector('[role="combobox"]')).toBeNull();
    expect(editorField("query").value).toBe("state:working needle");
    fill("name", "My active work"); await press("save");
    expect(writes()[0].body).toMatchObject({ revision: "1", action: "add", name: "My active work", query: "state:working needle" });
    expect(getPalette().query).toBe("state:working needle"); expect(dialogs()).toHaveLength(1);
    expect(document.activeElement).toBe(document.querySelector('[role="combobox"]'));
    act(() => setPaletteQuery("")); await settle();
    expect(document.querySelectorAll("[data-saved-search-quick]")).toHaveLength(4);
    const target = document.querySelector<HTMLElement>(`[data-saved-search-quick="${id(4)}"]`)!;
    act(() => target.dispatchEvent(new MouseEvent("mousemove", { bubbles: true })));
    act(() => document.querySelector('[role="combobox"]')!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await settle(); expect(getPalette().query).toBe("state:working needle"); expect(getPalette().open).toBe(true);
    expect(writes()).toHaveLength(1);
  });

  it("applies a saved shortcut to the complete runtime search and preserves that query through Save", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      Object.assign(getState(), { features: ["sessions_list", "sessions_search"], sessionSummaries: [] });
      const found = { machine_id: "computer", session_id: "outside-browser-window", title: "Older indexed task", cwd: "/work/repo1", status: "idle", state: "idle", updated_at: "2026-11-14T12:00:00Z" };
      custom = (request) => request.url.searchParams.has("search") ? Promise.resolve(json({ sessions: [found], search_hits: { '["computer","outside-browser-window"]': { score: 35 } }, total: 1, has_more: false, revision: "frozen-result", remove: [] })) : undefined;
      await show();
      expect(requests.filter((request) => request.url.searchParams.has("search"))).toHaveLength(0);
      const shortcut = document.querySelector<HTMLElement>(`[data-saved-search-quick="${id(1)}"]`)!;
      act(() => shortcut.click()); await settle();
      await act(async () => { vi.advanceTimersByTime(210); }); await settle();
      expect(getPalette().query).toBe(item(1).query);
      expect(document.querySelector('[role="listbox"]')?.textContent).toContain("Older indexed task");
      expect(requests.filter((request) => request.url.searchParams.has("search")).map((request) => request.url.searchParams.get("search"))).toEqual([item(1).query]);
      await act(async () => buttons().find((element) => element.textContent === t("palette.save_search"))!.click()); await settle();
      expect(dialogs()).toHaveLength(1); expect(editorField("query").value).toBe(item(1).query);
      fill("name", "Shared quick filter"); await press("save");
      await act(async () => { vi.advanceTimersByTime(210); }); await settle();
      expect(getPalette().query).toBe(item(1).query);
      expect(document.querySelector('[role="listbox"]')?.textContent).toContain("Older indexed task");
      expect(writes()).toHaveLength(1);
      expect(writes()[0].body).toMatchObject({ action: "add", name: "Shared quick filter", query: item(1).query });
    } finally { vi.useRealTimers(); }
  });

  it("manages edit, reorder, delete and revision-guarded Undo in a single dialog", async () => {
    await show("return query"); await manage();
    await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle();
    fill("name", "Renamed"); fill("query", "is:pinned"); await press("save");
    expect(doc.searches[0]).toMatchObject({ name: "Renamed", query: "is:pinned" }); expect(dialogs()).toHaveLength(1);
    await act(async () => named("move_down", { name: "Renamed" }).click()); await settle();
    expect(doc.searches.map((search) => search.id)).toEqual([id(2), id(1), id(3)]);
    await act(async () => named("delete_named", { name: "Renamed" }).click()); await settle();
    expect(doc.searches.map((search) => search.id)).toEqual([id(2), id(3)]);
    await press("undo"); expect(doc.searches.map((search) => search.id)).toEqual([id(2), id(1), id(3)]);
    expect(writes().map((request) => request.body?.action)).toEqual(["update", "move", "delete", "restore"]);
    await press("back_to_search"); expect(getPalette().query).toBe("return query"); expect(dialogs()).toHaveLength(1);
  });

  it.each(["cancel", "save", "add"])("keeps focus in the management dialog after %s returns its editor to the list", async (action) => {
    const visible = vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
    try {
      await show(); await manage();
      if (action === "add") { await press("add"); fill("name", "Added from list"); fill("query", "state:working"); }
      else { await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle(); if (action === "save") fill("name", "Edited from list"); }
      const control = action === "cancel" ? buttons().find((element) => element.textContent === t("common.cancel"))! : button("save");
      await act(async () => { control.focus(); control.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); control.click(); }); await settle();
      const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
      expect(dialog.contains(document.activeElement)).toBe(true);
      if (action === "save") expect(document.activeElement).toBe(named("apply_named", { name: "Edited from list" }));
      const last = [...dialog.querySelectorAll<HTMLButtonElement>("button")].at(-1)!;
      act(() => { last.focus(); last.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })); });
      expect(dialog.contains(document.activeElement)).toBe(true); expect(document.activeElement).not.toBe(last);
    } finally { visible.mockRestore(); }
  });

  it("keeps a dirty draft through a push and requires explicit review of the new version", async () => {
    await show(); await manage(); await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle();
    fill("name", "My draft"); fill("query", "model:opus");
    doc = { ...doc, revision: "2", searches: doc.searches.map((search) => search.id === id(1) ? { ...search, name: "Other browser", query: "state:idle" } : search) };
    await push();
    expect(editorField("name").value).toBe("My draft"); expect(editorField("query").value).toBe("model:opus");
    expect(document.body.textContent).toContain("Other browser"); expect(button("save").disabled).toBe(true);
    expect(writes()).toHaveLength(0);
    await press("review_save"); expect(writes()[0].body).toMatchObject({ revision: "2", action: "update", name: "My draft", query: "model:opus" });
    expect(doc.searches[0].name).toBe("My draft");
  });

  it("keeps failed saves and HTTP conflicts as drafts instead of retrying silently", async () => {
    await show(); await manage(); await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle(); fill("name", "Keep my draft");
    let failure = true;
    custom = (request) => request.method === "POST" && failure ? Promise.resolve(json({ error: "Temporary failure" }, 500)) : undefined;
    await press("save"); expect(editorField("name").value).toBe("Keep my draft"); expect(writes()).toHaveLength(1);
    failure = false; doc = { ...doc, revision: "2" };
    await press("save"); expect(writes()).toHaveLength(2); expect(editorField("name").value).toBe("Keep my draft");
    expect(button("review_save")).toBeDefined(); await press("review_save");
    expect(writes()).toHaveLength(3); expect(doc.searches[0].name).toBe("Keep my draft");
  });

  it.each(["readonly", "unsupported"])("provides a truthful %s experience", async (mode) => {
    if (mode === "readonly") doc.can_write = false;
    else custom = (request) => request.path === "/api/v1/saved-agent-searches" ? Promise.resolve(json({ code: "not_supported", error: "Unsupported" }, 503)) : undefined;
    await show("needle"); expect(buttons().some((element) => element.textContent === t("palette.save_search"))).toBe(false);
    await manage(); expect(document.body.textContent).toContain(word(mode === "readonly" ? "read_only" : "unsupported"));
    expect(button("add")).toBeUndefined(); expect(writes()).toHaveLength(0);
    if (mode === "readonly") { await act(async () => named("apply_named", { name: "Search 1" }).click()); await settle(); expect(getPalette().query).toBe(item(1).query); }
  });

  it("follows only the default manager and discards drafts across a default or credential switch", async () => {
    await show(); await manage(); await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle(); fill("name", "Private draft");
    act(() => chooseManager("sidebar-other")); await settle(); expect(editorField("name").value).toBe("Private draft");
    const before = reads().length; await push("99", "sidebar-other"); expect(reads()).toHaveLength(before);
    act(() => setRosterDefault("next-default")); await settle(); expect(dialogs()).toHaveLength(0); expect(document.body.textContent).not.toContain("Private draft");
    await show(); expect(reads().at(-1)?.headers.get("X-Persona-Manager")).toBe("next-default");
    await manage(); await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle(); fill("name", "Second private draft");
    act(() => setToken("synthetic-new-owner")); await settle(); expect(dialogs()).toHaveLength(0); expect(document.body.textContent).not.toContain("Second private draft");
  });

  it.each([200, 500])("allows closing a pending save and ignores its late %s UI callback", async (status) => {
    await show("return here"); await act(async () => buttons().find((element) => element.textContent === t("palette.save_search"))!.click()); await settle(); fill("name", "Deferred");
    let finish!: (value: Response) => void;
    custom = (request) => request.method === "POST" ? new Promise((resolve) => { finish = resolve; }) : undefined;
    await act(async () => button("save").click()); await settle();
    await act(async () => buttons().find((element) => element.textContent === t("common.cancel"))!.click()); await settle();
    expect(getPalette().query).toBe("return here");
    act(() => closePalette()); await settle(); expect(dialogs()).toHaveLength(0);
    await act(async () => finish(status === 200 ? apply(writes()[0].body!) : json({ error: "Failed" }, status))); await settle();
    expect(getPalette().open).toBe(false); expect(dialogs()).toHaveLength(0);
  });

  it("does not restore the old query when a closed editor's successful save finishes while the palette stays open", async () => {
    await show("old query"); await act(async () => buttons().find((element) => element.textContent === t("palette.save_search"))!.click()); await settle(); fill("name", "Deferred success");
    let finish!: (value: Response) => void;
    custom = (request) => request.method === "POST" ? new Promise((resolve) => { finish = resolve; }) : undefined;
    await act(async () => button("save").click()); await settle();
    await act(async () => buttons().find((element) => element.textContent === t("common.cancel"))!.click()); await settle();
    act(() => setPaletteQuery("new user query")); await settle();
    await act(async () => finish(apply(writes()[0].body!))); await settle();
    expect(doc.searches.at(-1)?.name).toBe("Deferred success");
    expect(getPalette().query).toBe("new user query"); expect(getPalette().open).toBe(true);
    expect(document.querySelector('[data-saved-searches]')).toBeNull();
  });

  it("offers Save as new for an edited search removed elsewhere, without resurrecting its old identity", async () => {
    await show(); await manage(); await act(async () => named("edit_named", { name: "Search 1" }).click()); await settle();
    fill("name", "Recovered draft");
    doc = { ...doc, revision: "2", searches: doc.searches.filter((search) => search.id !== id(1)) }; await push();
    expect(editorField("name").value).toBe("Recovered draft"); expect(document.body.textContent).toContain(word("removed_elsewhere"));
    await press("save_as_new");
    expect(writes()[0].body).toMatchObject({ revision: "2", action: "add", name: "Recovered draft" });
    expect(doc.searches.some((search) => search.id === id(1))).toBe(false);
    expect(doc.searches.at(-1)?.id).toBe(id(4));
  });

  it("bounds quick shortcuts, keeps their identity through live reorder, and stops reads when closed", async () => {
    doc.searches = Array.from({ length: 8 }, (_, index) => item(index + 1)); await show();
    expect(document.querySelectorAll("[data-saved-search-quick]")).toHaveLength(5);
    const target = document.querySelector<HTMLElement>(`[data-saved-search-quick="${id(2)}"]`)!;
    act(() => target.dispatchEvent(new MouseEvent("mousemove", { bubbles: true })));
    doc = { ...doc, revision: "2", searches: [doc.searches[1], doc.searches[0], ...doc.searches.slice(2)] }; await push();
    const combobox = document.querySelector<HTMLInputElement>('[role="combobox"]')!;
    expect(document.getElementById(combobox.getAttribute("aria-activedescendant")!)?.getAttribute("data-saved-search-quick")).toBe(id(2));
    const before = reads().length; await push("1"); expect(reads()).toHaveLength(before);
    act(() => closePalette()); await settle(); await push("99"); expect(reads()).toHaveLength(before);
    expect(events.get("saved_agent_searches")?.size).toBe(0);
  });

  it("keeps page aliases available after their intent-only code loads", async () => {
    await show("cpu");
    expect([...document.querySelectorAll('[role="option"]')].some((row) => row.textContent?.includes("机器"))).toBe(true);
    act(() => setPaletteQuery("todo")); await settle();
    expect([...document.querySelectorAll('[role="option"]')].some((row) => row.textContent?.includes("待办"))).toBe(true);
  });

  it("allows 64 emoji in a name and rejects commands as saved queries", async () => {
    await show("needle"); await act(async () => buttons().find((element) => element.textContent === t("palette.save_search"))!.click()); await settle();
    fill("name", "😀".repeat(64)); expect(button("save").disabled).toBe(false);
    fill("query", "> deploy"); expect(button("save").disabled).toBe(true); expect(document.body.textContent).toContain(word("query_invalid"));
    fill("query", "is:pinned"); await press("save"); expect(doc.searches.at(-1)?.name).toHaveLength(128);
  });
});
