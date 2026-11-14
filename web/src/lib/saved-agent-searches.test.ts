// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, setPairedToken, setToken } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "./manager-choice";
import { SavedAgentSearchesModel, savedSearchProblem, type SavedAgentSearch, type SavedAgentSearches, type SavedSearchMutation } from "./saved-agent-searches";

const id = (n: number) => `search_${String(n).padStart(24, "0")}`;
const at = "2026-01-01T10:00:00Z";
const item = (n: number, query = "is:working"): SavedAgentSearch => ({ id: id(n), name: `Search ${n}`, query, created_at: at, updated_at: at });
const initial = (): SavedAgentSearches => ({ revision: "7", searches: [item(1), item(2, "since:24h"), item(3, "project:example")], can_write: true });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function fixture() {
  let document = initial();
  const request = vi.fn(async (_path: string, init?: { method?: string; json?: unknown }) => {
    if (init?.method !== "POST") return structuredClone(document);
    const operation = init.json as SavedSearchMutation & { revision: string };
    if (operation.revision !== document.revision) throw new ApiError(409, "conflict", {});
    const entries = [...document.searches];
    if (operation.action === "add") entries.push({ ...item(9, operation.query.trim()), name: operation.name.trim() });
    else if (operation.action === "restore") entries.splice(operation.before_id ? entries.findIndex((value) => value.id === operation.before_id) : entries.length, 0, operation.item);
    else {
      const index = entries.findIndex((value) => value.id === operation.id);
      if (operation.action === "delete") entries.splice(index, 1);
      else if (operation.action === "update") entries[index] = { ...entries[index], name: operation.name.trim(), query: operation.query.trim() };
      else { const [saved] = entries.splice(index, 1); entries.splice(operation.before_id ? entries.findIndex((value) => value.id === operation.before_id) : entries.length, 0, saved); }
    }
    document = { ...document, revision: String(BigInt(document.revision) + 1n), searches: entries };
    return structuredClone(document);
  });
  return { model: new SavedAgentSearchesModel(request as never), request, read: () => document, replace: (next: SavedAgentSearches) => { document = next; } };
}
beforeEach(() => { resetManagerChoice(); setRosterDefault("assistant"); setToken("synthetic-saved-search-owner"); });
afterEach(() => vi.unstubAllGlobals());

describe("saved Agent search persistence model", () => {
  it("reads only when activated and posts the presented revision plus metadata", async () => {
    const f = fixture(); expect(f.request).not.toHaveBeenCalled(); await f.model.refresh();
    expect(await f.model.mutate({ action: "add", name: "Working today", query: "is:working since:24h" })).toBe(true);
    const post = f.request.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(post[0]).toBe("/saved-agent-searches");
    expect(post[1]?.json).toEqual({ revision: "7", action: "add", name: "Working today", query: "is:working since:24h" });
    expect(f.model.snapshot().document?.searches.at(-1)?.query).toBe("is:working since:24h");
    expect(f.request.mock.calls.every(([path]) => path === "/saved-agent-searches")).toBe(true);
    f.model.dispose();
  });
  it("projects reorder immediately, locks competing writes and rolls back the full order on failure", async () => {
    const f = fixture(); await f.model.refresh(); const response = deferred<SavedAgentSearches>(); f.request.mockImplementationOnce(() => response.promise);
    const pending = f.model.mutate({ action: "move", id: id(3), before_id: id(1) });
    expect(f.model.snapshot().document?.searches.map((value) => value.id)).toEqual([id(3), id(1), id(2)]);
    expect(await f.model.mutate({ action: "delete", id: id(2) })).toBe(false);
    response.reject(new Error("Save unavailable")); expect(await pending).toBe(false);
    expect(f.model.snapshot()).toMatchObject({ document: initial(), busy: false, error: "Save unavailable" }); f.model.dispose();
  });
  it("undoes only the last successful deletion, with original identity, query and position", async () => {
    const f = fixture(); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(2) });
    expect(f.model.snapshot().canUndo).toBe(true);
    await f.model.refresh(); expect(f.model.snapshot().canUndo).toBe(true);
    expect(await f.model.undo()).toBe(true);
    expect(f.read().searches).toEqual(initial().searches);
    expect(f.request.mock.calls.at(-1)?.[1]?.json).toEqual({ revision: "8", action: "restore", item: item(2, "since:24h"), before_id: id(3) });
    expect(f.model.snapshot().canUndo).toBe(false); f.model.dispose();
  });
  it("restores a removed last item at the end, even when the collection becomes empty", async () => {
    const f = fixture(); f.replace({ ...initial(), searches: [item(1)] }); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(1) });
    expect(f.model.snapshot().document).toMatchObject({ revision: "8", searches: [] }); await f.model.undo();
    expect(f.read()).toMatchObject({ revision: "9", searches: [item(1)] }); f.model.dispose();
  });
  it("retains Undo after failed restore, but retires it after another successful mutation", async () => {
    const f = fixture(); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(2) });
    f.request.mockRejectedValueOnce(new Error("Offline")); expect(await f.model.undo()).toBe(false); expect(f.model.snapshot().canUndo).toBe(true);
    await f.model.mutate({ action: "update", id: id(1), name: "Renamed", query: "state:idle" });
    expect(f.model.snapshot().canUndo).toBe(false); expect(await f.model.undo()).toBe(false); f.model.dispose();
  });
  it("does not overwrite a changed collection when an editor submits its captured revision", async () => {
    const f = fixture(); await f.model.refresh();
    f.replace({ ...initial(), revision: "8", searches: [{ ...item(1), name: "Changed in another browser" }, item(2), item(3)] });
    await f.model.refresh(); const requests = f.request.mock.calls.length;
    expect(await f.model.mutate({ action: "update", id: id(1), name: "My draft", query: "model:example" }, "7")).toBe(false);
    expect(f.request).toHaveBeenCalledTimes(requests); expect(f.model.snapshot().conflict).toBe(true);
    expect(f.model.snapshot().document?.searches[0].name).toBe("Changed in another browser");
    // Only an explicit later submission with the reviewed revision may replace it.
    expect(await f.model.mutate({ action: "update", id: id(1), name: "My draft", query: "model:example" }, "8")).toBe(true);
    expect(f.model.snapshot().document?.searches[0].name).toBe("My draft"); f.model.dispose();
  });
  it("reloads a server CAS conflict and prevents another mutation during recovery", async () => {
    const f = fixture(); await f.model.refresh();
    const recovered = deferred<SavedAgentSearches>(); f.request.mockRejectedValueOnce(new ApiError(409, "conflict", {})).mockImplementationOnce(() => recovered.promise);
    const pending = f.model.mutate({ action: "delete", id: id(1) });
    await vi.waitFor(() => expect(f.request).toHaveBeenCalledTimes(3));
    expect(f.model.snapshot().busy).toBe(true); expect(await f.model.mutate({ action: "delete", id: id(2) })).toBe(false);
    recovered.resolve({ ...initial(), revision: "9" }); expect(await pending).toBe(false);
    expect(f.model.snapshot()).toMatchObject({ conflict: true, busy: false, canUndo: false, document: { revision: "9" } }); f.model.dispose();
  });
  it("does not overwrite a newer deletion or rename when Undo is rejected by CAS", async () => {
    const f = fixture(); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(2) });
    f.replace({ ...f.read(), revision: "9", searches: [{ ...item(1), name: "New owner edit" }] });
    expect(await f.model.undo()).toBe(false);
    expect(f.model.snapshot()).toMatchObject({ conflict: true, canUndo: false, document: { revision: "9", searches: [{ name: "New owner edit" }] } }); f.model.dispose();
  });
  it.each(["revision", "permission"])("retires Undo after a refreshed %s change", async (change) => {
    const f = fixture(); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(2) });
    f.replace({ ...f.read(), ...(change === "revision" ? { revision: "9" } : { can_write: false }) }); await f.model.refresh();
    expect(f.model.snapshot().canUndo).toBe(false); f.model.dispose();
  });
  it("tells unsupported/read-only storage from a valid empty collection", async () => {
    for (const response of [new ApiError(404, "old hub", {}), new ApiError(503, "not supported", { code: "not_supported" }), new ApiError(403, "denied", {})]) {
      const request = vi.fn().mockRejectedValue(response); const model = new SavedAgentSearchesModel(request);
      await model.refresh(); expect(model.snapshot().document).toBeNull(); expect(model.snapshot().readonly).toBe(true);
      expect(await model.mutate({ action: "add", name: "a", query: "b" })).toBe(false); expect(request).toHaveBeenCalledTimes(1); model.dispose();
    }
    const f = fixture(); f.replace({ revision: "0", searches: [], can_write: false }); await f.model.refresh();
    expect(f.model.snapshot()).toMatchObject({ unsupported: false, error: "", readonly: true, document: { searches: [] } }); f.model.dispose();
  });
  it("keeps duplicate-name and missing-item errors distinct from concurrency and missing support", async () => {
    for (const error of [new ApiError(409, "Name already used", { code: "duplicate_name" }), new ApiError(404, "Search missing", { code: "search_not_found" })]) {
      const f = fixture(); await f.model.refresh(); f.request.mockRejectedValueOnce(error);
      expect(await f.model.mutate({ action: "update", id: id(1), name: "Duplicate", query: "query" })).toBe(false);
      expect(f.model.snapshot()).toMatchObject({ conflict: false, unsupported: false, readonly: false, document: initial() });
      expect(f.request).toHaveBeenCalledTimes(2); expect(f.model.snapshot().error).toContain(error.message); f.model.dispose();
    }
  });
  it("ignores a stale GET that completes after a successful write", async () => {
    const f = fixture(); await f.model.refresh(); const old = deferred<SavedAgentSearches>(); f.request.mockImplementationOnce(() => old.promise);
    const read = f.model.refresh(); await f.model.mutate({ action: "delete", id: id(1) }); old.resolve(initial()); await read;
    expect(f.model.snapshot().document).toMatchObject({ revision: "8" }); expect(f.model.snapshot().document?.searches).toHaveLength(2); f.model.dispose();
  });
  it("queues a pushed refresh during writes and keeps admission closed until the read completes", async () => {
    const f = fixture(); await f.model.refresh(); const write = deferred<SavedAgentSearches>(), read = deferred<SavedAgentSearches>();
    f.request.mockImplementationOnce(() => write.promise); const pending = f.model.mutate({ action: "delete", id: id(1) }); await f.model.refresh();
    f.request.mockImplementationOnce(() => read.promise); write.resolve({ ...initial(), revision: "8", searches: initial().searches.slice(1) });
    await vi.waitFor(() => expect(f.request).toHaveBeenCalledTimes(3)); expect(f.model.snapshot().busy).toBe(true);
    read.resolve({ ...initial(), revision: "9", searches: [item(3)] }); expect(await pending).toBe(true);
    expect(f.model.snapshot()).toMatchObject({ busy: false, canUndo: false, document: { revision: "9", searches: [item(3)] } }); f.model.dispose();
  });
  it("keeps write failures visible even after an unrelated queued read succeeds", async () => {
    const f = fixture(); await f.model.refresh(); const response = deferred<SavedAgentSearches>(); f.request.mockImplementationOnce(() => response.promise);
    const pending = f.model.mutate({ action: "delete", id: id(1) }); await f.model.refresh(); response.reject(new Error("Write failed")); await pending;
    expect(f.model.snapshot().error).toBe("Write failed"); expect(f.model.snapshot().document).toEqual(initial()); f.model.dispose();
  });
  it("aborts on close and fences pending writes through a StrictMode reopen", async () => {
    const f = fixture(); await f.model.refresh(); const old = deferred<SavedAgentSearches>(); f.request.mockImplementationOnce(() => old.promise);
    const pending = f.model.mutate({ action: "delete", id: id(1) }); const signal = (f.request.mock.calls[1][1] as RequestInit).signal!;
    f.model.dispose(); expect(signal.aborted).toBe(true); await f.model.refresh(); old.resolve({ ...initial(), revision: "8", searches: [] }); await pending;
    expect(f.model.snapshot().document).toEqual(initial()); expect(f.model.snapshot().canUndo).toBe(false); f.model.dispose();
  });
  it("keeps the last confirmed collection after an aborted optimistic write and a failed reopen", async () => {
    const f = fixture(); await f.model.refresh(); const pendingReply = deferred<SavedAgentSearches>();
    f.request.mockImplementationOnce(() => pendingReply.promise);
    const pending = f.model.mutate({ action: "delete", id: id(1) });
    expect(f.model.snapshot().document?.searches).toHaveLength(2);
    f.model.dispose();
    expect(f.model.snapshot().document).toEqual(initial());
    f.request.mockRejectedValueOnce(new Error("Reopen offline")); await f.model.refresh();
    pendingReply.resolve({ ...initial(), revision: "8", searches: [] }); await pending;
    expect(f.model.snapshot().document).toEqual(initial());
    expect(f.model.snapshot().error).toBe("Reopen offline"); f.model.dispose();
  });
  it.each(["credential", "default"])("retires old %s operations without issuing recovery reads", async (scope) => {
    const f = fixture(); await f.model.refresh(); const response = deferred<SavedAgentSearches>(); f.request.mockImplementationOnce(() => response.promise);
    const pending = f.model.mutate({ action: "delete", id: id(1) });
    if (scope === "credential") setPairedToken("synthetic-saved-search-owner"); else { chooseManager("other-assistant"); setRosterDefault("new-default"); }
    response.reject(new ApiError(409, "conflict", {})); expect(await pending).toBe(false); await f.model.refresh(); expect(await f.model.undo()).toBe(false);
    expect(f.request).toHaveBeenCalledTimes(2); f.model.dispose();
  });
  it("pins real requests to the known default manager rather than the selected Assistant", async () => {
    resetManagerChoice(); const fetcher = vi.fn(async () => new Response(JSON.stringify(initial()), { headers: { "Content-Type": "application/json" } })); vi.stubGlobal("fetch", fetcher);
    const unknown = new SavedAgentSearchesModel(); await unknown.refresh(); expect(fetcher).not.toHaveBeenCalled(); unknown.dispose();
    setRosterDefault("saved-search-owner"); chooseManager("another-assistant");
    const model = new SavedAgentSearchesModel(); await model.refresh();
    const init = (fetcher.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(new Headers(init.headers).get("X-Persona-Manager")).toBe("saved-search-owner");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer synthetic-saved-search-owner"); model.dispose();
  });
  it("retains int64 revision precision across writes", async () => {
    const f = fixture(); f.replace({ ...initial(), revision: "9007199254740993" }); await f.model.refresh(); await f.model.mutate({ action: "delete", id: id(1) });
    expect(f.model.snapshot().document?.revision).toBe("9007199254740994"); f.model.dispose();
  });
  it("rejects malformed persistence without erasing the previously confirmed list", async () => {
    const f = fixture(); await f.model.refresh(); f.request.mockResolvedValueOnce({ ...initial(), revision: 8 } as never); await f.model.refresh();
    expect(f.model.snapshot().document).toEqual(initial()); expect(f.model.snapshot().error).not.toBe(""); f.model.dispose();
  });
  it("validates Unicode names, UTF-8 query bounds and command-mode rejection", () => {
    expect(savedSearchProblem("界".repeat(64), "is:working")).toBe("");
    expect(savedSearchProblem("界".repeat(65), "a")).not.toBe("");
    expect(savedSearchProblem("Today", "界".repeat(341))).toBe("");
    for (const query of ["", " > close", "line\nline", "x\0y", "界".repeat(342)]) expect(savedSearchProblem("Today", query)).not.toBe("");
    expect(savedSearchProblem("", "valid")).not.toBe("");
  });
});
