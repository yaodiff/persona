// GET /todos is the whole list, hundreds of KB on a busy hub, and most {"type":"todos"} pushes change nothing it holds.
// The hub tags the list (ETag W/"t1-…", #9590); the page sends the tag back and a 304 applies the list it kept.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "./api";
import { todosV2LabsSource } from "./labs-todos-v2";
import { getState, refreshTodos, reset } from "./store";

const todo = (id: string, title: string) => ({ id, title, created_at: "", updated_at: "", spaces: [] });

type Answer = { status: number; etag?: string; todos?: ReturnType<typeof todo>[] };

let answers: Answer[] = [];
let sent: (string | null)[] = [];

function storage() {
  const items = new Map<string, string>();
  return {
    getItem: (k: string) => items.get(k) ?? null,
    setItem: (k: string, v: string) => void items.set(k, v),
    removeItem: (k: string) => void items.delete(k),
  };
}

beforeEach(() => {
  reset();
  answers = [];
  sent = [];
  vi.stubGlobal("localStorage", storage());
  setToken("tok-a");
  vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    if (new URL(url, "http://hub").pathname.endsWith("/assistant") && init?.method === "PUT") {
      return Response.json({ manager: "assistant", todos_v2: JSON.parse(String(init.body)).todos_v2 });
    }
    if (!new URL(url, "http://hub").pathname.endsWith("/todos")) return new Response("{}", { status: 200 });
    sent.push(new Headers(init?.headers).get("If-None-Match"));
    const a = answers.shift();
    if (!a) throw new Error("unexpected GET /todos");
    const headers: Record<string, string> = a.etag ? { ETag: a.etag } : {};
    if (a.status === 304) return new Response(null, { status: 304, headers });
    return new Response(JSON.stringify({ todos: a.todos ?? [] }), { status: a.status, headers: { "Content-Type": "application/json", ...headers } });
  }));
});

afterEach(() => {
  reset();
  vi.unstubAllGlobals();
});

describe("the to-do list's validator", () => {
  it("refreshes without the previous validator when the Labs switch changes in either direction", async () => {
    answers.push({ status: 200, etag: 'W/"standard"', todos: [todo("t1", "Standard")] });
    await refreshTodos();
    const source = todosV2LabsSource("assistant");
    const update = { schema_version: 1 as const, id: "assistant-todos-v2" };

    answers.push({ status: 200, etag: 'W/"trial"', todos: [todo("t1", "Trial")] });
    await source.update({ ...update, revision: "false", changes: { todos_v2: true } });
    await vi.waitFor(() => expect(getState().todos[0]?.title).toBe("Trial"));

    answers.push({ status: 200, etag: 'W/"rolled-back"', todos: [todo("t1", "Rolled back")] });
    await source.update({ ...update, revision: "true", reset: ["todos_v2"] });
    await vi.waitFor(() => expect(getState().todos[0]?.title).toBe("Rolled back"));
    expect(sent).toEqual([null, null, null]);
  });

  it("sends the last tag back and keeps the list it has on a 304", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] });
    await refreshTodos();
    expect(sent).toEqual([null]); // nothing kept yet: the whole list
    const list = getState().todos;
    expect(list.map((t) => t.title)).toEqual(["first"]);

    answers.push({ status: 304, etag: 'W/"t1-aaa"' });
    await refreshTodos();
    expect(sent[1]).toBe('W/"t1-aaa"');
    const st = getState();
    expect(st.todos).toBe(list); // the same rows, not a re-read copy: memoized rows stay as they are
    expect(st.todosLoaded).toBe(true);
    expect(st.todosError).toBeNull();
    expect(st.todosSupported).toBe(true);
  });

  it("a changed list replaces the kept one and its tag", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] });
    await refreshTodos();
    answers.push({ status: 200, etag: 'W/"t1-bbb"', todos: [todo("t1", "first"), todo("t2", "second")] });
    await refreshTodos();
    expect(getState().todos.map((t) => t.id)).toEqual(["t1", "t2"]);

    answers.push({ status: 304, etag: 'W/"t1-bbb"' });
    await refreshTodos();
    expect(sent).toEqual([null, 'W/"t1-aaa"', 'W/"t1-bbb"']);
    expect(getState().todos.map((t) => t.id)).toEqual(["t1", "t2"]);
  });

  it("a hub that sends no tag is read whole every time, as before", async () => {
    answers.push({ status: 200, todos: [todo("t1", "first")] }, { status: 200, todos: [todo("t1", "again")] });
    await refreshTodos();
    await refreshTodos();
    expect(sent).toEqual([null, null]);
    expect(getState().todos.map((t) => t.title)).toEqual(["again"]);
  });

  it("an answer without a tag drops the one kept", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] }, { status: 200, todos: [todo("t1", "untagged")] }, { status: 200, todos: [] });
    await refreshTodos();
    await refreshTodos();
    await refreshTodos();
    expect(sent).toEqual([null, 'W/"t1-aaa"', null]);
  });

  it("a failed read keeps the tag and the list for the next one", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] }, { status: 503 }, { status: 304 });
    await refreshTodos();
    await refreshTodos();
    expect(getState().todosError).not.toBeNull();
    await refreshTodos();
    expect(sent).toEqual([null, 'W/"t1-aaa"', 'W/"t1-aaa"']);
    expect(getState().todos.map((t) => t.title)).toEqual(["first"]);
    expect(getState().todosError).toBeNull();
  });

  it("another login does not send the last login's tag", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] });
    await refreshTodos();
    setToken("tok-b");
    answers.push({ status: 200, etag: 'W/"t1-ccc"', todos: [todo("t9", "theirs")] });
    await refreshTodos();
    expect(sent).toEqual([null, null]);
    expect(getState().todos.map((t) => t.id)).toEqual(["t9"]);
  });

  it("reset() forgets the tag", async () => {
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] });
    await refreshTodos();
    reset();
    answers.push({ status: 200, etag: 'W/"t1-aaa"', todos: [todo("t1", "first")] });
    await refreshTodos();
    expect(sent).toEqual([null, null]);
    expect(getState().todos.map((t) => t.id)).toEqual(["t1"]);
  });
});
