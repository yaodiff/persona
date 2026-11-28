// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { loadPages } from "../lib/lazy-page";
import { connect, refreshAssistant, refreshTodos, reset } from "../lib/store";
import { loadTodoProjects, useTodoProjects } from "../lib/todo-projects";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";
import { installedMailbox, preloadMailbox } from "../test/installed-mailbox";
import { MailBody } from "./inbox/InboxBodies";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.all.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}

const at = (m: number) => `2026-01-01T00:${String(m).padStart(2, "0")}:00Z`;
const makeRow = (id: string, todoId: string, state: Session["state"] = "idle"): Session => ({
  machine_id: "mac",
  session_id: id,
  title: id,
  cwd: "/code/app",
  status: state === "working" ? "working" : "idle",
  state,
  updated_at: at(10),
  todo: { id: todoId, title: todoId },
});

const TODOS: Todo[] = [
  { id: "todo_1", title: "Alpha", created_at: at(16), updated_at: at(16), agents: [] },
  { id: "todo_2", title: "Beta", created_at: at(15), updated_at: at(15), agents: [] },
  { id: "todo_3", title: "Gamma", created_at: at(14), updated_at: at(14), agents: [] },
  { id: "todo_4", title: "Delta", created_at: at(13), updated_at: at(13), agents: [] },
  { id: "todo_5", title: "Epsilon", created_at: at(12), updated_at: at(12), agents: [] },
  { id: "todo_6", title: "Zeta Urgent", created_at: at(11), updated_at: at(11), agents: [] },
];

const ROWS: Session[] = [
  makeRow("s1", "todo_1", "working"),
  makeRow("s2", "todo_2", "idle"),
  makeRow("s3", "todo_3", "idle"),
  makeRow("s4", "todo_4", "idle"),
  makeRow("s5", "todo_5", "idle"),
  makeRow("s6", "todo_6", "needs_you"),
];

const FEATURES = ["sessions_list", "sessions_lookup", "sessions_agents_only", "sessions_todo", "todo_revision"];

let host: HTMLDivElement;
let root: Root;

const flush = async (times = 12) => {
  for (let i = 0; i < times; i++) await act(async () => { await Promise.resolve(); });
};

beforeAll(() => loadPages(), 60_000);

beforeEach(() => {
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Sidebar lazy loading, section concurrency cap, and route-switch safety", () => {
  it("keeps non-urgent extra to-do sections folded, keeps needs_you open, caps concurrent section fetches at 2, and aborts folded sections", async () => {
    let inFlightSections = 0;
    let peakSectionConcurrency = 0;
    const sectionOrder: string[] = [];
    const pendingSections: { todo: string; signal?: AbortSignal | null; resolve: () => void }[] = [];

    const fetchMock = vi.fn((input: string, init?: RequestInit) => {
      const url = new URL(input, "http://hub.example.com");
      const q = url.searchParams;
      if (url.pathname === "/api/v1/todos") return Promise.resolve(Response.json({ todos: TODOS }));
      if (url.pathname === "/api/v1/machines") return Promise.resolve(Response.json({ machines: [{ id: "mac", name: "Mac", online: true }] }));
      if (url.pathname === "/api/v1/pins") return Promise.resolve(Response.json({ sessions: [], spaces: [] }));
      if (url.pathname === "/api/v1/assistant") return Promise.resolve(Response.json({ machine_id: "hub", main: "main", conversations: [] }));
      if (url.pathname === "/api/v1/sessions") {
        const todo = q.get("todo");
        const state = q.get("state");
        const filtered = ROWS.filter((r) => (!todo || r.todo?.id === todo) && (!state || r.state === state));
        if (q.has("counts")) {
          const count = (key: (r: Session) => string) => filtered.reduce<Record<string, number>>((m, r) => ({ ...m, [key(r)]: (m[key(r)] ?? 0) + 1 }), {});
          return Promise.resolve(Response.json({
            total: filtered.length,
            state: count((r) => r.state!),
            machine: count((r) => r.machine_id),
            group: {},
            project: {},
            todo: count((r) => r.todo?.id ?? ""),
            revision: "r1",
          }));
        }
        if (todo) {
          sectionOrder.push(todo);
          inFlightSections++;
          peakSectionConcurrency = Math.max(peakSectionConcurrency, inFlightSections);
          return new Promise<Response>((resolve, reject) => {
            const done = () => { inFlightSections = Math.max(0, inFlightSections - 1); };
            init?.signal?.addEventListener("abort", () => {
              done();
              reject(new DOMException("aborted", "AbortError"));
            }, { once: true });
            pendingSections.push({
              todo,
              signal: init?.signal,
              resolve: () => {
                done();
                resolve(Response.json({ sessions: filtered, remove: [], has_more: false, revision: "r1", total: filtered.length }));
              },
            });
          });
        }
        return Promise.resolve(Response.json({ sessions: filtered, remove: [], has_more: false, revision: "r1", total: filtered.length }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);

    act(() => {
      connect();
      const s = FakeSocket.all.at(-1)!;
      s.open();
      s.push({ type: "hello", features: FEATURES });
    });
    await flush();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // First 3 to-dos plus the needs_you to-do (todo_6) start open; non-urgent todo_4 and todo_5 stay folded.
    const groups = [...host.querySelectorAll<HTMLElement>("[data-session-group]")];
    const expanded = Object.fromEntries(groups.map((g) => [g.getAttribute("data-session-group")!, g.getAttribute("data-group-open")]));
    expect(expanded.todo_1).toBe("true");
    expect(expanded.todo_2).toBe("true");
    expect(expanded.todo_3).toBe("true");
    expect(expanded.todo_4).toBe("false");
    expect(expanded.todo_5).toBe("false");
    expect(expanded.todo_6).toBe("true");

    // Concurrency cap holds at 2 even with 4 sections open simultaneously.
    expect(peakSectionConcurrency).toBe(2);
    expect(pendingSections).toHaveLength(2);
    expect(sectionOrder).not.toContain("todo_4");
    expect(sectionOrder).not.toContain("todo_5");

    // Folding the first in-flight section aborts its HTTP request immediately and advances the queue.
    const firstInFlight = pendingSections[0]!;
    const firstBtn = host.querySelector<HTMLButtonElement>(`[data-session-group="${firstInFlight.todo}"] button[aria-expanded]`)!;
    act(() => firstBtn.click());
    await flush();
    expect(firstInFlight.signal?.aborted).toBe(true);
    expect(peakSectionConcurrency).toBeLessThanOrEqual(2);
    // The needs_you section (todo_6) is prioritized ahead of non-urgent queued sections.
    expect(sectionOrder).toContain("todo_6");

    // Drain remaining sections and confirm peak concurrency never exceeded 2.
    while (pendingSections.length) {
      const next = pendingSections.shift()!;
      if (!next.signal?.aborted) {
        act(() => next.resolve());
        await flush();
      }
    }
    expect(peakSectionConcurrency).toBe(2);
  });

  it("defers sidebar Agents and feed fetches while sidebar is hidden (active=false), and loads on reveal", async () => {
    const requestedPaths: string[] = [];
    vi.stubGlobal("fetch", vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      requestedPaths.push(`${url.pathname}${url.search}`);
      if (url.pathname === "/api/v1/todos") return Promise.resolve(Response.json({ todos: TODOS.slice(0, 2) }));
      if (url.pathname === "/api/v1/assistant") return Promise.resolve(Response.json({ machine_id: "hub", main: "main", conversations: [] }));
      if (url.pathname === "/api/v1/sessions") {
        if (url.searchParams.has("counts")) return Promise.resolve(Response.json({ total: 2, state: { working: 1, idle: 1 }, machine: { mac: 2 }, group: {}, project: {}, todo: { todo_1: 1, todo_2: 1 }, revision: "r1" }));
        return Promise.resolve(Response.json({ sessions: ROWS.slice(0, 2), remove: [], has_more: false, revision: "r1", total: 2 }));
      }
      return Promise.resolve(Response.json({}));
    }));

    act(() => {
      connect();
      const s = FakeSocket.all.at(-1)!;
      s.open();
      s.push({ type: "hello", features: FEATURES });
    });
    await flush();
    requestedPaths.length = 0;

    act(() => root.render(<Sidebar active={false} route={{ name: "todos" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(requestedPaths.filter((p) => p.includes("todo=") || p.includes("/feed"))).toEqual([]);

    act(() => root.render(<Sidebar active route={{ name: "todos" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(requestedPaths.some((p) => p.includes("todo=todo_1"))).toBe(true);
    expect(requestedPaths.some((p) => p.includes("/assistant/conversations/main/feed"))).toBe(true);
  });

  it("skips complete-list session downloads when the legacy Agents block starts folded", async () => {
    localStorage.setItem("fold:agents", "1");
    const requested: string[] = [];
    vi.stubGlobal("fetch", vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      requested.push(`${url.pathname}${url.search}`);
      if (url.pathname === "/api/v1/sessions") {
        if (url.searchParams.has("counts")) return Promise.resolve(Response.json({ total: 1, state: { working: 1 }, machine: { mac: 1 }, group: {}, project: {}, revision: "r1" }));
        return Promise.resolve(Response.json({ sessions: [ROWS[0]], remove: [], has_more: false, revision: "r1", total: 1 }));
      }
      return Promise.resolve(Response.json({}));
    }));

    act(() => {
      connect();
      const s = FakeSocket.all.at(-1)!;
      s.open();
      // Paging hub without sessions_agents_only falls back to LegacyAgentsBlock.
      s.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
    });
    await flush();
    requested.length = 0;

    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(requested.some((p) => p.includes("summary=0"))).toBe(false);

    // Expanding the Agents block triggers the complete-list read on demand.
    const toggle = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "Agents")!;
    act(() => toggle.click());
    await flush();
    expect(requested.some((p) => p.includes("summary=0"))).toBe(true);
  });

  it("avoids duplicate concurrent /todos and /assistant across cold start and / -> /overview -> /inbox -> /todos route switches and aborts route-scoped reads on unmount", async () => {
    await preloadMailbox("Body");
    const mailRead = installedMailbox ? "/api/v1/mail/messages/acc/42" : "/api/v1/inbox/m1";
    vi.useFakeTimers();
    const inFlightByPath = new Map<string, number>();
    const peakByPath = new Map<string, number>();
    const abortedPaths: string[] = [];
    const held: { path: string; resolve: () => void }[] = [];

    vi.stubGlobal("fetch", vi.fn((input: string, init?: RequestInit) => {
      const url = new URL(input, "http://hub.example.com");
      const path = url.pathname;
      const cur = (inFlightByPath.get(path) ?? 0) + 1;
      inFlightByPath.set(path, cur);
      peakByPath.set(path, Math.max(peakByPath.get(path) ?? 0, cur));
      const releaseCount = () => inFlightByPath.set(path, Math.max(0, (inFlightByPath.get(path) ?? 1) - 1));

      if (path === "/api/v1/todos" || path === "/api/v1/assistant" || path === "/api/v1/kb/projects" || path === mailRead) {
        return new Promise<Response>((resolve, reject) => {
          init?.signal?.addEventListener("abort", () => {
            abortedPaths.push(path);
            releaseCount();
            reject(new DOMException("aborted", "AbortError"));
          }, { once: true });
          held.push({
            path,
            resolve: () => {
              releaseCount();
              const body = path === "/api/v1/todos"
                ? { todos: TODOS.slice(0, 2) }
                : path === "/api/v1/assistant"
                  ? { machine_id: "hub", main: "main", conversations: [] }
                  : path === "/api/v1/kb/projects"
                    ? { items: [] }
                    : { message: { subject: "Mail", text: "Hi" } };
              resolve(Response.json(body));
            },
          });
        });
      }
      releaseCount();
      if (path === "/api/v1/sessions") {
        if (url.searchParams.has("counts")) return Promise.resolve(Response.json({ total: 2, state: { working: 1, idle: 1 }, machine: { mac: 2 }, group: {}, project: {}, todo: { todo_1: 1, todo_2: 1 }, revision: "r1" }));
        return Promise.resolve(Response.json({ sessions: ROWS.slice(0, 2), remove: [], has_more: false, revision: "r1", total: 2 }));
      }
      return Promise.resolve(Response.json({}));
    }));

    function TodosProjectsProbe() {
      useTodoProjects();
      return null;
    }

    // 1. Cold start on `/`: 250ms fallback timer coalesces with overlapping passive reads in the same pre-hello boundary.
    await act(async () => {
      connect();
      root.render(<Sidebar route={{ name: "persona" }} onSearch={() => {}} onNewAgent={() => {}} />);
      await vi.advanceTimersByTimeAsync(250);
      void refreshTodos({ coalesce: true });
      void refreshAssistant({ coalesce: true }).catch(() => {});
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(peakByPath.get("/api/v1/todos")).toBe(1);
    expect(peakByPath.get("/api/v1/assistant")).toBe(1);

    // Drain pre-hello reads, then establish WS hello and verify post-hello passive bursts coalesce while route switches abort stale reads.
    await act(async () => {
      while (held.length) held.shift()!.resolve();
      await vi.advanceTimersByTimeAsync(0);
      const sock = FakeSocket.all.at(-1)!;
      sock.open();
      sock.push({ type: "hello", features: FEATURES });
      await vi.advanceTimersByTimeAsync(0);
      while (held.length) held.shift()!.resolve();
      await vi.advanceTimersByTimeAsync(0);
    });

    // 2. Switch `/` -> `/overview` -> `/inbox` (mounting route-scoped MailBody) -> `/todos` (mounting useTodoProjects) -> `/overview`.
    await act(async () => {
      void refreshTodos({ coalesce: true });
      void refreshTodos({ coalesce: true });
      void refreshAssistant({ coalesce: true }).catch(() => {});
      void refreshAssistant({ coalesce: true }).catch(() => {});
      root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
      root.render(<>
        <Sidebar route={{ name: "inbox" }} onSearch={() => {}} onNewAgent={() => {}} />
        <MailBody item={{ id: "m1", kind: "mail", source: "mail", account: "acc", account_name: "Mail", title: "Hello", preview: "", count: 1, updated_at: at(1), mail: { account_id: "acc", uids: [42] } }} />
      </>);
      await vi.advanceTimersByTimeAsync(0);
    });

    await act(async () => {
      root.render(<>
        <Sidebar route={{ name: "todos" }} onSearch={() => {}} onNewAgent={() => {}} />
        <TodosProjectsProbe />
      </>);
      await vi.advanceTimersByTimeAsync(0);
    });
    // Navigating away from /inbox aborted the pending MailBody fetch immediately.
    expect(abortedPaths).toContain(mailRead);

    // Switching away from /todos aborts its pending /kb/projects fetch.
    await act(async () => {
      root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(abortedPaths).toContain("/api/v1/kb/projects");

    // Verify loadTodoProjects still works cleanly after abort.
    await act(async () => {
      const afterAbort = loadTodoProjects();
      while (held.length) held.shift()!.resolve();
      await afterAbort;
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(peakByPath.get("/api/v1/todos")).toBe(1);
    expect(peakByPath.get("/api/v1/assistant")).toBe(1);
  });
});
