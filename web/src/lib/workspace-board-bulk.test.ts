// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, setToken } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "./manager-choice";
import { BoardModel, columnCards, inverseBoardChanges, optimisticBoard, type BoardCard, type BoardCardChange, type BoardMutation, type BoardState } from "./workspace-board";

const card = (id: string, column = "todo", flags: Partial<BoardCard> = {}): BoardCard => ({
  id, machine_id: "computer", workspace_id: id, title: id, cwd: `/projects/${id}`, source: "directory",
  column_id: column, pinned: false, archived: false, ...flags,
});
const initial = (): BoardState => ({ revision: 7, batch_cards: true, can_write: true,
  columns: [{ id: "todo", title: "To do" }, { id: "done", title: "Done" }],
  cards: [card("a"), card("b", "todo", { pinned: true }), card("c"), card("d", "done"), card("e", "done", { archived: true })],
});
const batch = (changes: BoardCardChange[]): BoardMutation => ({ action: "batch_cards", changes });
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function fixture() {
  let board = initial();
  const request = vi.fn(async (path: string, init?: { json?: unknown }) => {
    if (path === "/workspaces") return { workspaces: [] };
    if (!init) return structuredClone(board);
    const op = init.json as BoardMutation & { revision: number };
    if (op.revision !== board.revision) throw new ApiError(409, "conflict", {});
    board = { ...optimisticBoard(board, op), revision: board.revision + 1 };
    return structuredClone(board);
  });
  return { model: new BoardModel(request as never), request, read: () => board, change: (next: BoardState) => { board = next; } };
}

afterEach(() => { vi.unstubAllGlobals(); });

beforeEach(() => { setRosterDefault("assistant"); chooseManager(null); setToken("synthetic-board-owner"); });

describe("bulk board organization and undo", () => {
  it("moves selected workspaces once, keeps flags, and restores their original per-column order", async () => {
    const f = fixture(); await f.model.refresh();
    expect(await f.model.mutate(batch([{ id: "a", column_id: "done" }, { id: "c", column_id: "done" }]))).toBe(true);
    expect(columnCards(f.read(), "done").map((c) => c.id)).toEqual(["d", "a", "c"]);
    expect(f.model.snapshot().canUndo).toBe(true);
    expect(await f.model.undo()).toBe(true);
    expect(f.read().revision).toBe(9);
    expect(columnCards(f.read(), "todo").map((c) => c.id)).toEqual(["b", "a", "c"]);
    expect(f.read().cards.find((c) => c.id === "e")?.archived).toBe(true);
    expect(f.model.snapshot().canUndo).toBe(false);
    expect(f.request.mock.calls.filter(([, init]) => init).map(([, init]) => init?.json)).toEqual([
      { revision: 7, action: "batch_cards", changes: [{ id: "a", column_id: "done" }, { id: "c", column_id: "done" }] },
      { revision: 8, action: "batch_cards", changes: [{ id: "c", column_id: "todo", before_id: "" }, { id: "a", column_id: "todo", before_id: "b" }] },
    ]);
  });

  it("restores heterogeneous pin/archive values without overwriting titles or untouched fields", async () => {
    const f = fixture(); await f.model.refresh();
    await f.model.mutate(batch([{ id: "a", pinned: true, archived: true }, { id: "b", pinned: true, archived: true }]));
    expect(columnCards(f.read(), "todo", { archived: true }).map((c) => c.id)).toEqual(["a", "b"]);
    await f.model.undo();
    expect(f.read().cards).toEqual(initial().cards);
    const inverse = (f.request.mock.calls.at(-1)?.[1]?.json as BoardMutation).changes!;
    expect(inverse.every((c) => !("column_id" in c) && !("title" in c))).toBe(true);
  });

  it("can undo every selected subset across columns, including all cards and hidden archived anchors", () => {
    const board = initial();
    for (let mask = 1; mask < 1 << board.cards.length; mask++) {
      const changes = board.cards.filter((_, i) => mask & 1 << i).map((c) => ({ id: c.id, column_id: "done", pinned: true, archived: true }));
      const moved = optimisticBoard(board, batch(changes));
      const undone = optimisticBoard(moved, batch(inverseBoardChanges(board, changes)));
      for (const column of board.columns) {
        expect(undone.cards.filter((c) => c.column_id === column.id)).toEqual(board.cards.filter((c) => c.column_id === column.id));
      }
      expect(board).toEqual(initial());
    }
  });

  it("projects all cards immediately, blocks a second submit, and rolls back the whole batch on failure", async () => {
    const write = deferred<BoardState>();
    const request = vi.fn(async (path: string, init?: unknown) => path === "/workspaces" ? { workspaces: [] } : init ? write.promise : initial());
    const model = new BoardModel(request as never); await model.refresh();
    const changes = [{ id: "a", archived: true }, { id: "c", archived: true }];
    const pending = model.mutate(batch(changes));
    changes[0].archived = false;
    expect(model.snapshot().board?.cards.filter((c) => c.archived).map((c) => c.id)).toEqual(["a", "c", "e"]);
    expect(await model.mutate(batch([{ id: "b", pinned: false }]))).toBe(false);
    expect(await model.undo()).toBe(false);
    write.reject(new ApiError(503, "storage unavailable", {}));
    expect(await pending).toBe(false);
    expect(model.snapshot()).toMatchObject({ board: initial(), busy: false, canUndo: false });
    expect(request.mock.calls.filter(([, init]) => init)).toHaveLength(1);
  });

  it("retains Undo after a failed request but retires it after a successful single-card edit", async () => {
    const f = fixture(); await f.model.refresh();
    await f.model.mutate(batch([{ id: "a", pinned: true }]));
    f.request.mockImplementationOnce(async () => { throw new ApiError(500, "failed", {}); });
    expect(await f.model.undo()).toBe(false);
    expect(f.model.snapshot().canUndo).toBe(true);
    expect(await f.model.mutate({ action: "update_card", id: "b", pinned: false })).toBe(true);
    expect(f.model.snapshot().canUndo).toBe(false);
    expect(await f.model.undo()).toBe(false);
  });

  it("reloads an unseen concurrent edit on Undo conflict instead of overwriting it", async () => {
    const f = fixture(); await f.model.refresh();
    await f.model.mutate(batch([{ id: "a", archived: true }]));
    f.change({ ...f.read(), revision: 9, cards: f.read().cards.map((c) => c.id === "a" ? { ...c, title: "Renamed elsewhere" } : c) });
    expect(await f.model.undo()).toBe(false);
    expect(f.model.snapshot()).toMatchObject({ canUndo: false, busy: false, board: { revision: 9 } });
    expect(f.model.snapshot().board?.cards.find((c) => c.id === "a")).toMatchObject({ archived: true, title: "Renamed elsewhere" });
  });

  it("keeps admission closed while conflict recovery is waiting", async () => {
    const f = fixture(); await f.model.refresh();
    const reload = deferred<BoardState>();
    f.request.mockImplementationOnce(async () => { throw new ApiError(409, "conflict", {}); });
    f.request.mockImplementationOnce(async () => reload.promise);
    const pending = f.model.mutate(batch([{ id: "a", pinned: true }]));
    await vi.waitFor(() => expect(f.request).toHaveBeenCalledTimes(5));
    expect(f.model.snapshot().busy).toBe(true);
    expect(await f.model.mutate(batch([{ id: "b", pinned: false }]))).toBe(false);
    reload.resolve({ ...initial(), revision: 10 });
    expect(await pending).toBe(false);
    expect(f.model.snapshot()).toMatchObject({ busy: false, canUndo: false, board: { revision: 10 } });
  });

  it.each(["revision", "role", "capability"])("retires Undo when a refresh changes %s", async (change) => {
    const f = fixture(); await f.model.refresh();
    await f.model.mutate(batch([{ id: "a", pinned: true }]));
    await f.model.refresh(); expect(f.model.snapshot().canUndo).toBe(true);
    f.change({ ...f.read(), ...(change === "revision" ? { revision: 9 } : change === "role" ? { can_write: false } : { batch_cards: false }) });
    await f.model.refresh();
    expect(f.model.snapshot().canUndo).toBe(false);
    const requests = f.request.mock.calls.length;
    expect(await f.model.undo()).toBe(false);
    expect(f.request).toHaveBeenCalledTimes(requests);
  });

  it("never submits a batch to an old manager or read-only connection", async () => {
    for (const flags of [{ batch_cards: undefined }, { can_write: false }]) {
      const f = fixture(); f.change({ ...initial(), ...flags }); await f.model.refresh();
      expect(await f.model.mutate(batch([{ id: "a", archived: true }]))).toBe(false);
      expect(f.request.mock.calls.filter(([, init]) => init)).toHaveLength(0);
    }
  });

  it("does not drop a pushed refresh while a write is pending", async () => {
    const f = fixture(); await f.model.refresh();
    const write = deferred<BoardState>();
    f.request.mockImplementationOnce(async () => write.promise);
    const pending = f.model.mutate(batch([{ id: "a", pinned: true }]));
    await f.model.refresh();
    f.change({ ...initial(), revision: 9, cards: [card("new")] });
    write.resolve({ ...initial(), revision: 8 });
    await pending;
    expect(f.model.snapshot().board?.cards.map((c) => c.id)).toEqual(["new"]);
    expect(f.model.snapshot().canUndo).toBe(false);
  });

  it("keeps controls busy through a queued read so a later selection cannot be wiped", async () => {
    const f = fixture(); await f.model.refresh();
    const write = deferred<BoardState>(), read = deferred<BoardState>();
    f.request.mockImplementationOnce(async () => write.promise);
    const pending = f.model.mutate(batch([{ id: "a", pinned: true }]));
    await f.model.refresh();
    f.request.mockImplementationOnce(async () => read.promise);
    write.resolve({ ...initial(), revision: 8 });
    await vi.waitFor(() => expect(f.request).toHaveBeenCalledTimes(5));
    expect(f.model.snapshot().busy).toBe(true);
    expect(await f.model.mutate(batch([{ id: "c", archived: true }]))).toBe(false);
    read.resolve({ ...initial(), revision: 8 });
    expect(await pending).toBe(true);
    expect(f.model.snapshot().busy).toBe(false);
  });

  it("keeps a failed write visible after a queued successful refresh", async () => {
    const f = fixture(); await f.model.refresh();
    const write = deferred<BoardState>(); f.request.mockImplementationOnce(async () => write.promise);
    const pending = f.model.mutate(batch([{ id: "a", pinned: true }]));
    await f.model.refresh();
    write.reject(new ApiError(500, "Write failed", {}));
    expect(await pending).toBe(false);
    expect(f.model.snapshot().error).toContain("Write failed");
    expect(f.model.snapshot().board).toEqual(initial());
  });

  it("does not claim a conflict was reloaded when its recovery read failed", async () => {
    const f = fixture(); await f.model.refresh();
    f.request.mockImplementationOnce(async () => { throw new ApiError(409, "conflict", {}); });
    f.request.mockImplementationOnce(async () => { throw new Error("Recovery unavailable"); });
    expect(await f.model.mutate(batch([{ id: "a", pinned: true }]))).toBe(false);
    expect(f.model.snapshot().error).toBe("Recovery unavailable");
    expect(f.model.snapshot()).toMatchObject({ busy: false, canUndo: false });
  });

  it("pins real HTTP reads and writes to the board owner, independent of the selected Assistant", async () => {
    setRosterDefault("board-owner"); chooseManager("other-assistant");
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => new Response(JSON.stringify(
      url.endsWith("/workspaces") ? { workspaces: [] } : { ...initial(), revision: init?.method === "POST" ? 8 : 7 },
    ), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);
    const model = new BoardModel(); await model.refresh();
    expect(await model.mutate(batch([{ id: "a", pinned: true }]))).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(3);
    for (const [, init] of fetcher.mock.calls) expect(new Headers(init?.headers).get("X-Persona-Manager")).toBe("board-owner");
    setRosterDefault("new-board-owner");
    expect(await model.undo()).toBe(false);
    await model.refresh();
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("does not acquire a real board before the default manager is known", async () => {
    resetManagerChoice();
    const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
    const model = new BoardModel();
    await model.refresh();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it.each(["credential", "manager"])("retires late writes and conflict recovery after a %s switch", async (change) => {
    const write = deferred<BoardState>();
    const request = vi.fn(async (path: string, init?: unknown) => path === "/workspaces" ? { workspaces: [] } : init ? write.promise : initial());
    const model = new BoardModel(request as never); await model.refresh();
    const pending = model.mutate(batch([{ id: "a", pinned: true }]));
    if (change === "credential") setToken("synthetic-other-owner"); else { chooseManager("selected-assistant"); setRosterDefault("other-default"); }
    write.reject(new ApiError(409, "conflict", {}));
    expect(await pending).toBe(false);
    expect(await model.undo()).toBe(false);
    expect(await model.mutate(batch([{ id: "b", archived: true }]))).toBe(false);
    await model.refresh();
    expect(request).toHaveBeenCalledTimes(3);
  });
});
