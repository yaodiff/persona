// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/workspace-board-setting", () => ({ useWorkspaceBoardSetting: () => ({ enabled: true }) }));
import { setToken } from "../lib/api";
import { reset } from "../lib/store";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { t } from "../i18n";
import type { BoardCard, BoardMutation, BoardState } from "../lib/workspace-board";
import { WorkspaceBoard } from "./WorkspaceBoard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const makeCard = (id: string, column_id = "todo", extra: Partial<BoardCard> = {}): BoardCard => ({ id, machine_id: "computer", workspace_id: id, title: `Workspace ${id}`, cwd: `/projects/${id}`, source: "directory", column_id, pinned: false, archived: false, ...extra });
const initial = (): BoardState => ({ revision: 4, can_write: true, batch_cards: true, columns: [{ id: "todo", title: "To do" }, { id: "done", title: "Done" }], cards: [makeCard("a"), makeCard("b", "todo", { pinned: true }), makeCard("c", "done"), makeCard("old", "done", { archived: true })] });
let board: BoardState;
let host: HTMLDivElement;
let root: Root;
let narrow: boolean;
let posts: (BoardMutation & { revision: number })[];
let postReply: ((operation: BoardMutation & { revision: number }) => Promise<Response>) | undefined;
const observers = new Set<() => void>();
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("button")];
const namedButton = (label: string) => buttons().find((button) => button.textContent === label)!;
const button = (key: string) => namedButton(t(`workspaceboard.${key}`));
const press = (key: string) => act(() => button(key).click());
const checkbox = (id: string) => host.querySelector<HTMLInputElement>(`article[data-card-id="${id}"] input[type=checkbox]`)!;
const selectedCount = (count: number) => expect(host.textContent).toContain(t("workspaceboard.selected_count", { count }));
const fill = (key: string, value: string) => act(() => {
  const element = host.querySelector<HTMLInputElement | HTMLSelectElement>(`[aria-label="${t(`workspaceboard.${key}`)}"]`)!;
  const proto = element instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLSelectElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(element, value);
  element.dispatchEvent(new Event(element instanceof HTMLInputElement ? "input" : "change", { bubbles: true }));
});
const resize = (isNarrow: boolean) => act(() => { narrow = isNarrow; observers.forEach((run) => run()); });
const mount = async () => { act(() => root.render(<WorkspaceBoard />)); await flush(); };
const save = (operation: BoardMutation & { revision: number }) => {
  board = { ...board, revision: board.revision + 1, cards: board.cards.map((card) => {
    const change = operation.changes?.find((item) => item.id === card.id);
    if (!change) return card;
    return { ...card, ...(change.column_id !== undefined ? { column_id: change.column_id } : {}), ...(change.pinned !== undefined ? { pinned: change.pinned } : {}), ...(change.archived !== undefined ? { archived: change.archived } : {}) };
  }) };
  return response(board);
};

beforeEach(() => {
  setRosterDefault("assistant");
  board = initial(); narrow = false; posts = []; postReply = undefined;
  localStorage.removeItem("persona.workspace-board.view");
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  vi.stubGlobal("ResizeObserver", class {
    constructor(private callback: () => void) {}
    observe() { observers.add(this.callback); }
    unobserve() {}
    disconnect() { observers.delete(this.callback); }
  });
  const originalStyle = window.getComputedStyle.bind(window);
  vi.stubGlobal("getComputedStyle", (element: Element) => element.hasAttribute("data-board-wide") ? { display: narrow ? "none" : "block" } : originalStyle(element));
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://test.invalid").pathname;
    if (path === "/api/v1/workspace-board" && init?.method === "POST") {
      const operation = JSON.parse(String(init.body)); posts.push(operation);
      return postReply ? postReply(operation) : save(operation);
    }
    if (path === "/api/v1/workspace-board") return response(board);
    if (path === "/api/v1/workspaces") return response({ workspaces: [] });
    return response({});
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); observers.clear();
  resetManagerChoice(); setToken(""); localStorage.removeItem("persona.workspace-board.view");
  vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});

describe("manual board bulk organization", () => {
  it.each(["old", "readonly", "auto"])("keeps %s boards in their existing single-card experience", async (mode) => {
    if (mode === "old") delete board.batch_cards;
    if (mode === "readonly") board.can_write = false;
    if (mode === "auto") localStorage.setItem("persona.workspace-board.view", "auto");
    await mount();
    expect(button("select_cards")).toBeUndefined();
    expect(host.querySelector('input[type="checkbox"]')).toBeNull();
    expect(posts).toHaveLength(0);
  });

  it("selects without opening or dragging cards, and submits one ordered batch", async () => {
    await mount(); press("select_cards");
    expect(document.activeElement).toBe(checkbox("b"));
    act(() => checkbox("a").click());
    act(() => host.querySelector<HTMLButtonElement>('article[data-card-id="b"] button')!.click());
    selectedCount(2);
    expect(checkbox("b").checked).toBe(true);
    expect(host.querySelector('[role="dialog"]')).toBeNull();
    expect(host.querySelector('article[data-card-id="a"]')?.getAttribute("draggable")).toBe("false");
    fill("bulk_move_to", "done"); press("bulk_move"); await flush();
    expect(posts).toEqual([{ action: "batch_cards", revision: 4, changes: [{ id: "a", column_id: "done" }, { id: "b", column_id: "done" }] }]);
    selectedCount(0); expect(button("bulk_undo")).toBeDefined();
    press("selection_done"); expect(document.activeElement).toBe(button("select_cards"));
    expect(host.querySelectorAll('article[data-card-id] input:checked')).toHaveLength(0);
  });

  it("prunes hidden selections permanently when search, machine or archive filters change", async () => {
    board.cards[1].machine_id = "other";
    await mount(); press("select_cards"); press("select_all_visible"); selectedCount(3);
    fill("search_label", "Workspace a"); selectedCount(1);
    fill("search_label", ""); selectedCount(1);
    press("select_all_visible"); fill("filter_machine", "other"); selectedCount(1);
    fill("filter_machine", ""); selectedCount(1);
    press("archived"); selectedCount(0);
    expect(checkbox("old").checked).toBe(false);
    press("select_all_visible"); press("restore_card"); await flush();
    expect(posts[0].changes).toEqual([{ id: "old", archived: false }]);
  });

  it("selects only the narrow column and forgets cards hidden by a column change or resize", async () => {
    narrow = true; await mount(); press("select_cards"); press("select_all_visible"); selectedCount(2);
    fill("pick_column", "done"); selectedCount(0);
    press("select_all_visible"); selectedCount(1);
    fill("pick_column", "todo"); selectedCount(0);
    resize(false); press("select_all_visible"); selectedCount(3);
    resize(true); selectedCount(2);
    resize(false); selectedCount(2);
    press("pin"); await flush();
    expect(posts[0].changes).toEqual([{ id: "a", pinned: true }, { id: "b", pinned: true }]);
  });

  it("keeps selection after a failed optimistic archive, and admits only one pending batch", async () => {
    let finish!: (response: Response) => void;
    postReply = () => new Promise((resolve) => { finish = resolve; });
    await mount(); press("select_cards"); act(() => checkbox("a").click());
    act(() => { button("bulk_archive").click(); button("bulk_archive").click(); });
    await flush();
    expect(posts).toHaveLength(1);
    expect(host.querySelector('article[data-card-id="a"]')).toBeNull();
    expect(button("selection_done").disabled).toBe(true);
    await act(async () => finish(response({ error: "Temporary failure" }, 500))); await flush();
    expect(checkbox("a").checked).toBe(true); selectedCount(1);
    postReply = undefined; press("bulk_archive"); await flush();
    expect(posts).toHaveLength(2); selectedCount(0);
    press("bulk_undo"); await flush();
    expect(board.cards.find((card) => card.id === "a")?.archived).toBe(false);
    expect(button("bulk_undo")).toBeUndefined();
  });

  it("follows the default manager, not the sidebar pick, and resets after a default or credential change", async () => {
    setRosterDefault("manager-a");
    await mount(); press("select_cards"); act(() => checkbox("a").click());
    act(() => chooseManager("other-manager")); await flush();
    selectedCount(1); expect(checkbox("a").checked).toBe(true);
    act(() => setRosterDefault("manager-c")); await flush();
    expect(button("select_cards")).toBeDefined();
    press("select_cards"); act(() => checkbox("a").click()); press("pin"); await flush();
    expect(button("bulk_undo")).toBeDefined();
    act(() => setToken("synthetic-other-owner")); await flush();
    expect(button("select_cards")).toBeDefined();
    expect(button("bulk_undo")).toBeUndefined();
  });

  it("drops deleted cards and exits selection after write permission or capability is revoked", async () => {
    await mount(); press("select_cards"); press("select_all_visible"); selectedCount(3);
    board = { ...board, revision: board.revision + 1, cards: board.cards.filter((card) => card.id !== "b") };
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspaceboard.refresh")}"]`)!.click()); await flush();
    selectedCount(2);
    board = { ...board, can_write: false };
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspaceboard.refresh")}"]`)!.click()); await flush();
    expect(button("select_cards")).toBeUndefined();
    expect(host.querySelector('input[type="checkbox"]')).toBeNull();
    expect(posts).toHaveLength(0);
  });
});
