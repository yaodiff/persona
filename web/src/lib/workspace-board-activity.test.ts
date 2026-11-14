// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "./manager-choice";
import { BoardActivityModel, type ActivityDetail, type ActivityPage, type ActivitySummary } from "./workspace-board-activity";
const at = "2026-01-01T10:00:00Z";
const entry = (revision: string): ActivitySummary => ({ revision, at, change_count: 1, card_count: 1, kinds: ["card_updated"] });
const page = (ids: string[], next_cursor?: string, latest_revision = "9"): ActivityPage => ({ entries: ids.map(entry), latest_revision, oldest_revision: "1", retention_limit: 500, ...(next_cursor ? { next_cursor } : {}) });
const detail = (id = "9", offset = 0): ActivityDetail => ({ revision: id, at, changes: [{ kind: "card_updated", id: "a", title: "Original title", fields: ["archived"], before: { title: "Original title", archived: false }, after: { title: "Original title", archived: true } }], total: 2, ...(offset ? {} : { next_offset: 1 }) });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (value: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
beforeEach(() => { resetManagerChoice(); setRosterDefault("assistant"); setToken("synthetic-activity-token"); });
afterEach(() => vi.unstubAllGlobals());

describe("workflow board history reader", () => {
  it("loads only summaries on demand, encodes the card filter, and fetches details only on expansion", async () => {
    const request = vi.fn(async (path: string) => path.includes("/9?") ? detail() : page(["9", "8"], "8"));
    const model = new BoardActivityModel("card / ?", request as never);
    expect(request).not.toHaveBeenCalled();
    await model.refresh();
    expect(request.mock.calls.map(([path]) => path)).toEqual(["/workspace-board/activity?limit=20&card_id=card+%2F+%3F"]);
    expect(model.snapshot().details.size).toBe(0);
    await model.detail("9"); await model.detail("9");
    expect(request).toHaveBeenCalledTimes(2);
    expect(model.snapshot().details.get("9")).toMatchObject({ total: 2, nextOffset: 1, changes: [{ title: "Original title" }] });
    model.dispose();
  });
  it("admits one request per page and retains pages across a load-more failure", async () => {
    const next = deferred<ActivityPage>();
    const request = vi.fn().mockResolvedValueOnce(page(["9", "8"], "8")).mockImplementationOnce(() => next.promise).mockResolvedValueOnce(page(["7"]));
    const model = new BoardActivityModel(undefined, request); await model.refresh();
    const loading = model.more(); await model.more();
    expect(request).toHaveBeenCalledTimes(2);
    next.reject(new Error("Offline")); await loading;
    expect(model.snapshot()).toMatchObject({ entries: [entry("9"), entry("8")], nextCursor: "8", moreError: "Offline", loadingMore: false });
    await model.more();
    expect(model.snapshot().entries.map((item) => item.revision)).toEqual(["9", "8", "7"]);
    expect(model.snapshot().nextCursor).toBeUndefined();
    model.dispose();
  });
  it("cannot loop on repeated cursors or reorder old pages", async () => {
    const request = vi.fn().mockResolvedValueOnce(page(["9", "8"], "8")).mockResolvedValueOnce(page(["8"], "8"));
    const model = new BoardActivityModel(undefined, request); await model.refresh(); await model.more();
    expect(model.snapshot().entries).toHaveLength(2);
    expect(model.snapshot().moreError).not.toBe("");
    expect(model.snapshot().loadingMore).toBe(false);
    model.dispose();
  });
  it("preserves detail pages and retries the same offset after failure", async () => {
    const next = deferred<ActivityDetail>();
    const request = vi.fn().mockResolvedValueOnce(page(["9"])).mockResolvedValueOnce(detail()).mockImplementationOnce(() => next.promise).mockResolvedValueOnce(detail("9", 1));
    const model = new BoardActivityModel(undefined, request); await model.refresh(); await model.detail("9");
    const loading = model.moreDetails("9"); await model.moreDetails("9");
    expect(request).toHaveBeenCalledTimes(3);
    next.reject(new Error("Try again")); await loading;
    expect(model.snapshot().details.get("9")).toMatchObject({ error: "Try again", nextOffset: 1, changes: [detail().changes[0]] });
    await model.moreDetails("9");
    expect(request.mock.calls[3][0]).toContain("offset=1");
    expect(model.snapshot().details.get("9")).toMatchObject({ error: "", nextOffset: undefined, changes: [detail().changes[0], detail().changes[0]] });
    model.dispose();
  });
  it("keeps loaded detail pages when a failed continuation is collapsed and reopened", async () => {
    const request = vi.fn().mockResolvedValueOnce(page(["9"]))
      .mockResolvedValueOnce({ ...detail(), total: 3 })
      .mockResolvedValueOnce({ ...detail("9", 1), total: 3, next_offset: 2 })
      .mockRejectedValueOnce(new Error("Continuation failed"))
      .mockResolvedValueOnce({ ...detail("9", 1), total: 3 });
    const model = new BoardActivityModel(undefined, request);
    await model.refresh(); await model.detail("9"); await model.moreDetails("9"); await model.moreDetails("9");
    expect(model.snapshot().details.get("9")).toMatchObject({ total: 3, nextOffset: 2, error: "Continuation failed" });
    expect(model.snapshot().details.get("9")?.changes).toHaveLength(2);
    await model.detail("9"); // Reopening the group must not restart offset zero.
    expect(request).toHaveBeenCalledTimes(4);
    expect(model.snapshot().details.get("9")?.changes).toHaveLength(2);
    await model.moreDetails("9");
    expect(request.mock.calls[4][0]).toContain("offset=2");
    expect(model.snapshot().details.get("9")?.changes).toHaveLength(3);
    model.dispose();
  });
  it("fences a stale page and unfinished detail on refresh", async () => {
    const old = deferred<ActivityDetail>();
    const request = vi.fn().mockResolvedValueOnce(page(["9"])).mockImplementationOnce(() => old.promise).mockResolvedValueOnce(page(["10", "9"], undefined, "10")).mockResolvedValueOnce(detail());
    const model = new BoardActivityModel(undefined, request); await model.refresh();
    const pending = model.detail("9"); await model.refresh();
    old.resolve({ ...detail(), changes: [{ kind: "card_added", title: "Stale" }] }); await pending;
    await model.detail("9");
    expect(model.snapshot().details.get("9")?.changes[0].title).toBe("Original title");
    expect(request).toHaveBeenCalledTimes(4);
    model.dispose();
  });
  it("announces new activity without replacing the reader's rows until explicit refresh", async () => {
    const request = vi.fn().mockResolvedValueOnce(page(["9", "8"], "8")).mockResolvedValueOnce(page(["10", "9"], "9", "10"));
    const model = new BoardActivityModel(undefined, request); await model.refresh();
    model.noteRevision(10); model.noteRevision(8);
    expect(model.snapshot().newer).toBe(true); expect(model.snapshot().entries[0].revision).toBe("9");
    expect(request).toHaveBeenCalledTimes(1);
    await model.refresh(); expect(model.snapshot().newer).toBe(false); expect(model.snapshot().entries[0].revision).toBe("10");
    model.dispose();
  });
  it("preserves an event received while a refresh was in flight", async () => {
    const response = deferred<ActivityPage>(); const model = new BoardActivityModel(undefined, (() => response.promise) as never);
    const pending = model.refresh(); model.noteRevision("10"); response.resolve(page(["9"])); await pending;
    expect(model.snapshot().newer).toBe(true); model.dispose();
  });
  it("aborts on close, ignores late results, and permits a fenced StrictMode reopen", async () => {
    const old = deferred<ActivityPage>();
    const request = vi.fn().mockImplementationOnce(() => old.promise).mockResolvedValueOnce(page(["9"]));
    const model = new BoardActivityModel(undefined, request);
    const pending = model.refresh(); const signal = request.mock.calls[0][1].signal as AbortSignal;
    model.dispose(); expect(signal.aborted).toBe(true);
    await model.more(); await model.detail("9"); expect(request).toHaveBeenCalledTimes(1);
    await model.refresh(); old.resolve(page(["8"])); await pending;
    expect(model.snapshot().entries.map((item) => item.revision)).toEqual(["9"]); model.dispose();
  });
  it.each(["credential", "default"])("retires old %s responses and cannot issue another read", async (scope) => {
    const response = deferred<ActivityPage>(); const request = vi.fn(() => response.promise);
    const model = new BoardActivityModel(undefined, request as never); const pending = model.refresh();
    if (scope === "credential") setToken("synthetic-next-token"); else { chooseManager("selected-assistant"); setRosterDefault("new-default"); }
    response.resolve(page(["9"])); await pending; await model.refresh();
    expect(model.snapshot().entries).toEqual([]); expect(request).toHaveBeenCalledTimes(1); model.dispose();
  });
  it("pins real HTTP requests to the known default manager and does not fetch before it is known", async () => {
    resetManagerChoice(); const fetcher = vi.fn(async () => new Response(JSON.stringify(page(["9"])), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);
    const provisional = new BoardActivityModel(); await provisional.refresh(); expect(fetcher).not.toHaveBeenCalled(); provisional.dispose();
    setRosterDefault("board-owner"); chooseManager("other-assistant");
    const model = new BoardActivityModel(); await model.refresh();
    const headers = new Headers((fetcher.mock.calls[0] as unknown as [string, RequestInit])[1].headers);
    expect(headers.get("X-Persona-Manager")).toBe("board-owner");
    expect(headers.get("Authorization")).toBe("Bearer synthetic-activity-token"); model.dispose();
  });
  it("handles pruning between pages without inventing more results", async () => {
    const request = vi.fn().mockResolvedValueOnce(page(["9", "8"], "8")).mockResolvedValueOnce({ ...page([], undefined, "20"), oldest_revision: "10" });
    const model = new BoardActivityModel(undefined, request); await model.refresh(); await model.more();
    expect(model.snapshot()).toMatchObject({ oldestRevision: "10", nextCursor: undefined, newer: true, moreError: "" }); model.dispose();
  });
  it.each([
    { ...detail(), changes: [{ kind: "card_updated", title: { bad: true } }] },
    { ...detail(), changes: [{ kind: "card_updated", fields: "archived" }] },
    { ...detail(), changes: [{ kind: "card_updated", before: { title: "x", pinned: "false" } }] },
    { ...detail(), next_offset: 0 },
    { ...detail(), revision: "8" },
  ])("rejects malformed details as a retryable error", async (bad) => {
    const request = vi.fn().mockResolvedValueOnce(page(["9"])).mockResolvedValueOnce(bad);
    const model = new BoardActivityModel(undefined, request); await model.refresh(); await model.detail("9");
    expect(model.snapshot().details.get("9")).toMatchObject({ changes: [], loading: false });
    expect(model.snapshot().details.get("9")?.error).not.toBe(""); model.dispose();
  });
});
