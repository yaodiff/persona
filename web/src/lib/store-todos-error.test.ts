import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorText, HubTimeoutError, READ_TIMEOUT_MS } from "./api";
import { getState, refresh, refreshTodos, reset } from "./store";

const todo = { id: "todo-1", title: "Release checklist", created_at: "", updated_at: "", spaces: [] };
const failure = (status = 503) => Response.json({ error: "Temporarily unavailable" }, { status });
const failureText = (status = 503) => errorText(new ApiError(status, "Temporarily unavailable", {}));

function answerTodos(answer: () => Response | Promise<Response>) {
  vi.stubGlobal("fetch", vi.fn((url: string) => new URL(url, "http://hub").pathname.endsWith("/todos") ? answer() : Promise.resolve(Response.json({}))));
}

function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}

beforeEach(() => {
  reset();
  vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
});

afterEach(() => {
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe.each([
  ["refresh", refresh],
  ["refreshTodos", refreshTodos],
] as const)("%s to-do read failures", (_name, load) => {
  it.each([500, 503])("records the explained HTTP %i failure and finishes loading", async (status) => {
    answerTodos(() => failure(status));
    await expect(load()).resolves.toBeUndefined();
    expect(getState()).toMatchObject({
      todos: [], todosLoaded: true, todosSupported: true, spacesSupported: true, todosError: failureText(status),
    });
  });

  it("records the existing read-timeout explanation", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new URL(url, "http://hub").pathname.endsWith("/todos")
      ? new Promise<Response>((_resolve, reject) => {
        init.signal!.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      })
      : Promise.resolve(Response.json({}))));
    const pending = load();
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS);
    await pending;
    expect(getState()).toMatchObject({ todos: [], todosLoaded: true, todosError: errorText(new HubTimeoutError()) });
  });

  it.each([{ todos: [] }, { todos: [todo] }])("clears the error when the next read succeeds with $todos", async ({ todos }) => {
    answerTodos(() => failure());
    await load();
    expect(getState().todosError).toBe(failureText());

    answerTodos(() => Response.json({ todos }));
    await load();
    expect(getState()).toMatchObject({ todos, todosLoaded: true, todosSupported: true, todosError: null });
  });

  it("preserves the last successful list when the next read fails", async () => {
    answerTodos(() => Response.json({ todos: [todo] }));
    await load();
    const previous = getState().todos;

    answerTodos(() => failure());
    await load();
    expect(getState().todos).toBe(previous);
    expect(getState().todosError).toBe(failureText());
  });

  it("keeps 404 compatibility handling and clears a previous read error", async () => {
    answerTodos(() => failure());
    await load();

    answerTodos(() => failure(404));
    await load();
    expect(getState()).toMatchObject({ todos: [], todosLoaded: true, todosSupported: false, spacesSupported: false, todosError: null });
  });

  it.each([404, 503])("ignores an older HTTP %i failure after a newer successful read", async (status) => {
    const old = deferredResponse();
    answerTodos(() => old.promise);
    const pending = load();

    answerTodos(() => Response.json({ todos: [] }));
    await refreshTodos();
    old.resolve(failure(status));
    await pending;
    expect(getState()).toMatchObject({ todos: [], todosLoaded: true, todosSupported: true, spacesSupported: true, todosError: null });
  });

  it("does not let an older success erase a newer read error", async () => {
    const old = deferredResponse();
    answerTodos(() => old.promise);
    const pending = load();

    answerTodos(() => failure());
    await refreshTodos();
    old.resolve(Response.json({ todos: [todo] }));
    await pending;
    expect(getState()).toMatchObject({ todos: [], todosLoaded: true, todosError: failureText() });
  });

  it.each([404, 503])("drops an HTTP %i failure belonging to the previous login", async (status) => {
    const old = deferredResponse();
    answerTodos(() => old.promise);
    const pending = load();
    reset();

    old.resolve(failure(status));
    await pending;
    expect(getState()).toMatchObject({ todos: [], todosLoaded: false, todosSupported: true, spacesSupported: true, todosError: null });
  });
});

it("clears an existing to-do read error on reset", async () => {
  answerTodos(() => failure());
  await refreshTodos();
  expect(getState().todosError).toBe(failureText());

  reset();
  expect(getState()).toMatchObject({ todos: [], todosLoaded: false, todosError: null });
});

it("shows the to-do failure without waiting for the initial sessions request", async () => {
  const sessions = deferredResponse();
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    if (url.endsWith("/sessions")) return sessions.promise;
    return Promise.resolve(new URL(url, "http://hub").pathname.endsWith("/todos") ? failure() : Response.json({}));
  }));
  const pending = refresh();
  try {
    await vi.waitFor(() => expect(getState().todosError).toBe(failureText()));
    expect(getState().todosLoaded).toBe(true);
    expect(getState().loaded).toBe(false);
  } finally {
    sessions.resolve(Response.json({ sessions: [] }));
    await pending;
  }
});
