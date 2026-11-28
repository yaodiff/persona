// @vitest-environment jsdom
// Parked and fresh PR waits share one status section; lifecycle states with higher priority keep their places.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-16T12:00:00.000Z");
const MINUTE = 60_000;
const DAY = 86_400_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);

beforeEach(() => {
  reset();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  api.mockReset();
  api.mockImplementation(async () => ({}));
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  localStorage.clear();
  reset();
  vi.useRealTimers();
});

const render = async (todos: Todo[], sessions: Record<string, Session[]> = {}) => {
  localStorage.setItem("persona.todos.grouping", "status");
  seed({ todos, todosLoaded: true, sessions, interactions: [], answered: {} });
  await act(async () => root.render(<TodosView />));
  await flush();
};
const section = (name: string) => host.querySelector<HTMLElement>(`[data-testid="todo-section"][data-section="${name}"]`);
const summary = () => host.querySelector("[data-testid=todos-summary]")?.textContent;
const summaryFor = (open: number, parked: number, done: number) =>
  t("todos.summary_triage", { open, parked, shelved: 0, done, cancelled: 0 });
const task = (id: string, fields: Partial<Todo> = {}): Todo => ({
  id,
  title: `Independent task ${id}`,
  created_at: iso(4 * DAY),
  updated_at: iso(3 * DAY),
  ...fields,
});
const prFacts = (number: number) => ({
  url: `https://github.com/example/project/pull/${number}`,
  state: "open" as const,
  draft: false,
  review: "required" as const,
  checks: "pending" as const,
  conflicts: false,
  checked_at: iso(5 * MINUTE),
});
const mergeWait = (id: string, number: number, fields: Partial<Todo> = {}) => task(id, {
  pr_url: `https://github.com/example/project/pull/${number}`,
  pr_status: prFacts(number),
  ...fields,
});

const parkedTask = (id: string, until: string) => task(id, {
  supervision: {
    supervisor: "conversation",
    state: "parked",
    parked: { reason: `Waiting for event ${id}`, until, waiting_on: "external_event", count: 1 },
  },
});

describe("To-dos parked status section", () => {
  it("renders the hub's active blocker on the list and detail without supervision or local PR facts", async () => {
    const todo = task("blocked", { wait: { kind: "external_event", waiting_on: "CDN rollout (+1 more)", reason: "Propagation" } });
    await render([todo]);
    const row = section("parked")?.querySelector('[data-todo="blocked"]')!;
    expect(row.querySelector("[data-wait-target]")?.textContent).toContain("CDN rollout (+1 more)");
    await act(async () => root.render(<TodosView selectedId={todo.id} />));
    await flush();
    const detail = host.querySelector('[data-wait-collapsed="false"]');
    expect(detail?.querySelector("[data-wait-reason]")?.textContent).toBe("Propagation");
  });

  it("keeps explicitly parked tasks together, including a park past its until time", async () => {
    const todos = [
      parkedTask("parked-future", new Date(NOW + DAY).toISOString()),
      parkedTask("parked-overdue", new Date(NOW - MINUTE).toISOString()),
    ];
    await render(todos);

    const parked = section("parked");
    expect(parked).toBeTruthy();
    expect(parked?.getAttribute("data-todos")).toBe("2");
    expect(summary()).toBe(summaryFor(2, 2, 0));
    expect(parked?.querySelector('[data-todo="parked-future"]')).toBeTruthy();
    expect(parked?.querySelector('[data-todo="parked-overdue"]')).toBeTruthy();
    expect(section("pr")).toBeNull();
  });

  it("puts fresh matching PR waits in that section and counts a multi-PR task once", async () => {
    const todos = [
      mergeWait("merge-many", 101, {
        other_prs: [prFacts(102), prFacts(103)],
      }),
      mergeWait("merge-one", 201),
    ];
    await render(todos);

    const parked = section("parked");
    expect(parked).toBeTruthy();
    expect(parked?.getAttribute("data-todos")).toBe("2");
    expect(parked?.getAttribute("data-count")).toBe("2");
    expect(summary()).toBe(summaryFor(2, 2, 0));
    expect(parked?.querySelector('[data-todo="merge-many"]')).toBeTruthy();
    expect(parked?.querySelector('[data-todo="merge-one"]')).toBeTruthy();
    expect(parked?.querySelectorAll("[data-todo]")).toHaveLength(2);
    expect(parked?.querySelectorAll('[data-testid="todo-status-line"]')).toHaveLength(0);
    expect(parked?.querySelectorAll('[data-wait-collapsed="true"]')).toHaveLength(2);
    const mergeMany = parked?.querySelector<HTMLElement>('[data-todo="merge-many"]')!;
    expect(mergeMany.querySelectorAll("[data-pr-waiting-url]")).toHaveLength(0);
    await act(async () => mergeMany.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(mergeMany.querySelectorAll("[data-pr-waiting-url]")).toHaveLength(3);
    expect(section("pr")).toBeNull();
  });

  it("keeps active, owner-decision, closed and accepted tasks ahead of the parked grouping", async () => {
    const active = mergeWait("active-pr", 301, {
      agents: [{ machine_id: "machine", session_id: "session", linked_at: iso(MINUTE) }],
    });
    const ownerDecision = mergeWait("owner-decision-pr", 302, {
      supervision: {
        supervisor: "conversation",
        state: "needs_user",
        reviews: [{ at: iso(2 * DAY), verdict: "needs_user" }],
      },
    });
    const closed = mergeWait("closed-pr", 303, { done_at: iso(3 * DAY) });
    const accepted = mergeWait("accepted-pr", 304, {
      supervision: {
        supervisor: "conversation",
        state: "accepted",
        reviews: [{ at: iso(3 * DAY), verdict: "accepted" }],
      },
    });
    const session = {
      machine_id: "machine",
      session_id: "session",
      title: "Active task agent",
      status: "working",
      updated_at: iso(MINUTE),
    } as unknown as Session;
    await render([active, ownerDecision, closed, accepted], { machine: [session] });

    const working = section("working");
    const waiting = section("waiting");
    const done = section("done");
    expect(working).toBeTruthy();
    expect(waiting).toBeTruthy();
    expect(done).toBeTruthy();
    expect(working?.getAttribute("data-todos")).toBe("1");
    expect(waiting?.getAttribute("data-todos")).toBe("1");
    expect(done?.getAttribute("data-todos")).toBe("2");
    expect(summary()).toBe(summaryFor(2, 0, 2));
    const doneToggle = done?.querySelector<HTMLButtonElement>("button[aria-expanded]");
    expect(doneToggle).toBeTruthy();
    await act(async () => doneToggle?.click());
    await flush();
    expect(working?.querySelector('[data-todo="active-pr"]')).toBeTruthy();
    expect(waiting?.querySelector('[data-todo="owner-decision-pr"]')).toBeTruthy();
    expect(done?.querySelector('[data-todo="closed-pr"]')).toBeTruthy();
    expect(done?.querySelector('[data-todo="accepted-pr"]')).toBeTruthy();
    expect(section("parked")).toBeNull();
    expect(section("pr")).toBeNull();
  });
});
