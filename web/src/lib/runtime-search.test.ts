// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, setPairedToken, setToken } from "./api";
import { chooseManager, resetManagerChoice } from "./manager-choice";
import { runtimeIdentity } from "./runtime-list";
import { RuntimeSearchModel } from "./runtime-search";
import type { Session } from "./types";
const session = (id: string, machine = "computer"): Session => ({ machine_id: machine, session_id: id, title: `Agent ${id}`, cwd: "/projects/example", status: "idle", updated_at: "2026-01-01T12:00:00Z" });
const page = (sessions: Session[], options: { next?: string; total?: number; revision?: string; scores?: number[] } = {}) => ({
  sessions, search_hits: Object.fromEntries(sessions.map((row, i) => [runtimeIdentity(row), { score: options.scores?.[i] ?? 85 }])),
  has_more: Boolean(options.next), next_cursor: options.next, total: options.total ?? sessions.length, revision: options.revision ?? "revision-1",
});
function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
const debounce = () => vi.advanceTimersByTimeAsync(200);
beforeEach(() => { vi.useFakeTimers(); setToken("synthetic-search-owner"); resetManagerChoice(); });
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("all-runtime Agent search transport", () => {
  it("is lazy, debounces typing, uses the explicit search contract and never requests the complete collection", async () => {
    const request = vi.fn().mockResolvedValue(page([session("unloaded-501")]));
    const model = new RuntimeSearchModel(request);
    expect(request).not.toHaveBeenCalled(); model.search(""); await debounce(); expect(request).not.toHaveBeenCalled();
    model.search("par"); await vi.advanceTimersByTimeAsync(100); model.search("parser machine:computer");
    expect(model.snapshot()).toMatchObject({ loading: true, query: "parser machine:computer", loaded: false });
    await debounce(); expect(request).toHaveBeenCalledTimes(1);
    const query = new URL(request.mock.calls[0][0], "https://example.invalid").searchParams;
    expect(Object.fromEntries(query)).toEqual({ search: "parser machine:computer", summary: "1", roots: "1", limit: "40" });
    expect(model.snapshot().hits[0].session.session_id).toBe("unloaded-501"); model.dispose();
  });
  it("keeps authoritative matches beyond the compact summary and preserves separate machine identities", async () => {
    const rows = [session("same", "a"), session("same", "b")];
    const response = page(rows);
    Object.assign(response.search_hits[runtimeIdentity(rows[0])], { evidence: { role: "agent", before: "Later in the answer ", match: "needle", after: " was found." } });
    const model = new RuntimeSearchModel(vi.fn().mockResolvedValue(response)); model.search("needle"); await debounce();
    expect(model.snapshot().hits).toHaveLength(2);
    expect(model.snapshot().hits[0].evidence).toMatchObject({ role: "agent", match: "needle" });
    expect(model.snapshot().hits[0].session.last_text).toBeUndefined(); model.dispose();
  });
  it("shows a matched parent in its folded state without re-filtering the summary", async () => {
    const parent = { ...session("parent"), state: "idle" as const, children: { total: 2, working: 1, needs_you: 1 } };
    const response = page([parent], { scores: [35] });
    const model = new RuntimeSearchModel(vi.fn().mockResolvedValue(response));
    model.search("state:needs_you"); await debounce();
    expect(model.snapshot().hits[0]).toMatchObject({ score: 35, session: { state: "needs_you", session_id: "parent" } });
    expect(parent.state).toBe("idle"); model.dispose();
  });
  it("does not cap appended pages at 40 and only admits one More request", async () => {
    const first = Array.from({ length: 40 }, (_, i) => session(String(i).padStart(3, "0")));
    const next = deferred<ReturnType<typeof page>>();
    const request = vi.fn().mockResolvedValueOnce(page(first, { next: "cursor-a", total: 41 })).mockImplementationOnce(() => next.promise);
    const model = new RuntimeSearchModel(request); model.search("Agent"); await debounce();
    const loading = model.more(); await model.more(); expect(request).toHaveBeenCalledTimes(2);
    next.resolve(page([session("040")], { total: 41 })); await loading;
    expect(model.snapshot()).toMatchObject({ loaded: true, hasMore: false, total: 41, loadingMore: false });
    expect(model.snapshot().hits).toHaveLength(41); model.dispose();
  });
  it("preserves loaded hits and retries the exact cursor on a continuation failure", async () => {
    const request = vi.fn().mockResolvedValueOnce(page([session("a")], { next: "frozen", total: 2 }))
      .mockRejectedValueOnce(new Error("Connection lost")).mockResolvedValueOnce(page([session("b")], { total: 2 }));
    const model = new RuntimeSearchModel(request); model.search("Agent"); await debounce(); await model.more();
    expect(model.snapshot()).toMatchObject({ moreError: "Connection lost", hasMore: true, total: 2 });
    expect(model.snapshot().hits).toHaveLength(1); await model.retry();
    expect(request.mock.calls[1][0]).toBe(request.mock.calls[2][0]); expect(model.snapshot().hits).toHaveLength(2); model.dispose();
  });
  it("makes expired cursors an explicit fresh search, without auto-replaying More", async () => {
    const request = vi.fn().mockResolvedValueOnce(page([session("a")], { next: "expired", total: 2 }))
      .mockRejectedValueOnce(new ApiError(409, "expired", { code: "cursor_expired" })).mockResolvedValueOnce(page([session("new")], { revision: "revision-2" }));
    const model = new RuntimeSearchModel(request); model.search("Agent"); await debounce(); await model.more();
    expect(model.snapshot().expired).toBe(true); await model.more(); expect(request).toHaveBeenCalledTimes(2);
    await model.retry(); expect(request.mock.calls[2][0]).not.toContain("cursor=");
    expect(model.snapshot().hits.map((h) => h.session.session_id)).toEqual(["new"]); expect(model.snapshot().expired).toBe(false); model.dispose();
  });
  it("offers a fresh query when server authorization invalidates a continuation binding", async () => {
    const request = vi.fn().mockResolvedValueOnce(page([session("a")], { next: "old-scope", total: 2 }))
      .mockRejectedValueOnce(new ApiError(400, "cursor does not match the query", { code: "invalid_query" }))
      .mockResolvedValueOnce(page([session("new-scope")]));
    const model = new RuntimeSearchModel(request); model.search("Agent"); await debounce(); await model.more();
    expect(model.snapshot().expired).toBe(true); await model.retry();
    expect(request.mock.calls[2][0]).not.toContain("cursor=");
    expect(model.snapshot().hits[0].session.session_id).toBe("new-scope"); model.dispose();
  });
  it("aborts earlier queries and ignores their late successful responses", async () => {
    const old = deferred<ReturnType<typeof page>>(); const request = vi.fn().mockImplementationOnce(() => old.promise).mockResolvedValueOnce(page([session("new")]));
    const model = new RuntimeSearchModel(request); model.search("old"); await debounce();
    const signal = request.mock.calls[0][1].signal as AbortSignal;
    model.search("new"); expect(signal.aborted).toBe(true); expect(model.snapshot().hits).toEqual([]); await debounce();
    old.resolve(page([session("old")])); await Promise.resolve(); expect(model.snapshot().hits[0].session.session_id).toBe("new"); model.dispose();
  });
  it("fences late errors and More responses after a query changes", async () => {
    const late = deferred<ReturnType<typeof page>>(); const request = vi.fn().mockResolvedValueOnce(page([session("a")], { total: 2, next: "next" })).mockImplementationOnce(() => late.promise).mockResolvedValueOnce(page([session("z")]));
    const model = new RuntimeSearchModel(request); model.search("first"); await debounce(); const pending = model.more();
    model.search("second"); await debounce(); late.reject(new Error("Old failure")); await pending;
    expect(model.snapshot()).toMatchObject({ query: "second", error: "", moreError: "", loadingMore: false });
    expect(model.snapshot().hits[0].session.session_id).toBe("z"); model.dispose();
  });
  it("cancels scheduled and active reads on close, with a fenced StrictMode reopen", async () => {
    const old = deferred<ReturnType<typeof page>>(); const request = vi.fn().mockImplementationOnce(() => old.promise).mockResolvedValueOnce(page([session("fresh")]));
    const model = new RuntimeSearchModel(request); model.search("query"); model.dispose(); await debounce(); expect(request).not.toHaveBeenCalled();
    model.search("query"); await debounce(); const signal = request.mock.calls[0][1].signal as AbortSignal;
    model.dispose(); expect(signal.aborted).toBe(true); model.search("query"); await debounce();
    old.resolve(page([session("retired")])); await Promise.resolve(); expect(model.snapshot().hits[0].session.session_id).toBe("fresh"); model.dispose();
  });
  it("retires work after sign-in even when the credential text is unchanged", async () => {
    const response = deferred<ReturnType<typeof page>>(); const request = vi.fn(() => response.promise);
    const model = new RuntimeSearchModel(request as never); model.search("query"); await debounce();
    setPairedToken("synthetic-search-owner"); response.resolve(page([session("old-owner")])); await Promise.resolve();
    await model.retry(); model.search("different"); await debounce();
    expect(model.snapshot().hits).toEqual([]); expect(request).toHaveBeenCalledTimes(1); model.dispose();
  });
  it("keeps runtime search hub-wide, independent of the sidebar's selected Assistant", async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify(page([session("a")])), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher); chooseManager("another-assistant");
    const model = new RuntimeSearchModel(); model.search("query"); await debounce();
    const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).not.toContain("manager="); expect(new Headers(init.headers).get("Authorization")).toBe("Bearer synthetic-search-owner");
    chooseManager("third-assistant"); expect(model.snapshot().hits).toHaveLength(1); model.dispose();
  });
  it("replaces the result window when an explicit manager scope changes", async () => {
    const request = vi.fn().mockResolvedValue(page([session("a")])); const model = new RuntimeSearchModel(request);
    model.search("query", { manager: "one" }); await debounce(); model.search("query", { manager: "two" }); expect(model.snapshot().hits).toEqual([]); await debounce();
    expect(request.mock.calls[0][0]).toContain("manager=one"); expect(request.mock.calls[1][0]).toContain("manager=two"); model.dispose();
  });
  it("does not disguise an old server's unfiltered page as complete search results", async () => {
    const response = { ...page([session("a")]), search_hits: undefined };
    const request = vi.fn().mockResolvedValue(response); const model = new RuntimeSearchModel(request);
    model.search("unmatched"); await debounce(); expect(model.snapshot()).toMatchObject({ supported: false, loaded: false, hits: [], hasMore: false });
    expect(model.snapshot().error).not.toBe(""); await model.retry(); expect(request).toHaveBeenCalledTimes(1); model.dispose();
  });
  it("keeps an empty authoritative result distinct from unloaded or failed search", async () => {
    const model = new RuntimeSearchModel(vi.fn().mockResolvedValue(page([])));
    model.search("unmatched"); await debounce();
    expect(model.snapshot()).toMatchObject({ query: "unmatched", loaded: true, hits: [], total: 0, hasMore: false, error: "" });
    model.dispose();
  });
  it("refreshes the first page on initial failure and never changes the entered query", async () => {
    const request = vi.fn().mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce(page([session("a")]));
    const model = new RuntimeSearchModel(request); model.search("machine:computer Agent"); await debounce();
    expect(model.snapshot()).toMatchObject({ loaded: false, query: "machine:computer Agent", error: "Offline", loading: false });
    await model.retry(); expect(request.mock.calls[0][0]).toBe(request.mock.calls[1][0]);
    expect(model.snapshot()).toMatchObject({ loaded: true, error: "", total: 1 }); model.dispose();
  });
  it("bounds a query by UTF-8 bytes before any network read", async () => {
    const request = vi.fn(); const model = new RuntimeSearchModel(request); model.search("界".repeat(342)); await debounce();
    expect(request).not.toHaveBeenCalled(); expect(model.snapshot().error).not.toBe(""); model.dispose();
  });
  it.each(["repeat", "revision", "total", "order"])("retains the last valid page on malformed %s continuation", async (kind) => {
    const response = kind === "repeat" ? page([session("a")], { total: 2 }) : kind === "revision" ? page([session("b")], { total: 2, revision: "changed" })
      : kind === "total" ? page([session("b")], { total: 3 }) : page([session("b")], { total: 2, scores: [105] });
    const request = vi.fn().mockResolvedValueOnce(page([session("a")], { total: 2, next: "next" })).mockResolvedValueOnce(response);
    const model = new RuntimeSearchModel(request); model.search("query"); await debounce(); await model.more();
    expect(model.snapshot().hits.map((h) => h.session.session_id)).toEqual(["a"]); expect(model.snapshot().moreError).not.toBe(""); model.dispose();
  });
  it("rejects evidence maps containing foreign identities and unknown evidence roles", async () => {
    for (const mutate of [
      (value: ReturnType<typeof page>) => { value.search_hits['["foreign","private"]'] = { score: 100 }; },
      (value: ReturnType<typeof page>) => { Object.assign(value.search_hits[runtimeIdentity(value.sessions[0])], { evidence: { role: "unsafe", before: "", match: "x", after: "" } }); },
    ]) {
      const response = page([session("a")]); mutate(response); const model = new RuntimeSearchModel(vi.fn().mockResolvedValue(response));
      model.search("query"); await debounce(); expect(model.snapshot().hits).toEqual([]); expect(model.snapshot().error).not.toBe(""); model.dispose();
    }
  });
});
