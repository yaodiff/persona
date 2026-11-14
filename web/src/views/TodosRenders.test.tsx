// @vitest-environment jsdom
// A sessions push re-renders only the todo rows whose agents it changed: the rows are memoized on a view of plain
// values, and the lists, groups and callbacks they get stay the same objects while nothing they show moved.
// ReviewBadge renders once per row render, so counting it counts the rows. The data is made up.
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

const api = vi.hoisted(() => vi.fn(async (_path: string): Promise<unknown> => ({})));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api,
}));

const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("./assistant/Supervision", async (orig) => {
  const real = await orig<typeof import("./assistant/Supervision")>();
  return {
    ...real,
    ReviewBadge: (props: Parameters<typeof real.ReviewBadge>[0]) => {
      renders.set(props.todo.id, (renders.get(props.todo.id) ?? 0) + 1);
      return real.ReviewBadge(props);
    },
  };
});

const { getState, upsertSession, refreshTodos } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const now = new Date().toISOString();
const todo = (id: string, title: string, sid: string): Todo =>
  ({ id, title, created_at: now, updated_at: now, agents: [{ machine_id: "m1", session_id: sid }] }) as Todo;
const session = (sid: string, o: Partial<Session> = {}) =>
  ({ machine_id: "m1", session_id: sid, title: sid, cwd: "/work/app", status: "running", state: "working", updated_at: now, ...o }) as Session;

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  api.mockReset();
  api.mockResolvedValue({});
  seed({
    todos: [todo("ta", "整理发布清单", "s1"), todo("tb", "修登录页", "s2")],
    todosLoaded: true,
    sessions: { m1: [session("s1", { last_text: "在读代码" }), session("s2", { last_text: "在跑测试" })] },
    interactions: [],
    answered: {},
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  // ta stays selected: without a selection the first row is, and a reorder would move it (a real change)
  await act(async () => root.render(<TodosView selectedId="ta" />));
  renders.clear();
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
});

describe("todo rows on a sessions push", () => {
  it("refreshes N/M, evidence and reporter from a to-do update without waiting for a review", async () => {
    const list = getState().todos.map((td) => ({ ...td, supervision: {
      supervisor: "main", done_when: ["Tests pass", "PR opened"],
      criteria: [{ criterion: "Tests pass", state: "pending" as const }, { criterion: "PR opened", state: "pending" as const }],
    } }));
    api.mockImplementation(async (path) => path === "/todos" ? { todos: list } : {});
    await act(async () => { await refreshTodos(); });
    const row = () => [...host.querySelectorAll<HTMLElement>("[role=button]")].find((el) => el.textContent?.includes("修登录页"))!;
    expect(row().querySelector('[data-testid="review-badge"]')?.textContent).toContain("0/2");
    const updated = list.map((td) => td.id !== "tb" ? td : { ...td, supervision: { ...td.supervision, criteria: [
      { criterion: "Tests pass", state: "met" as const, evidence: "go test: PASS", at: now, by: "agent:m1/s2" },
      { criterion: "PR opened", state: "pending" as const },
    ] } });
    api.mockImplementation(async (path) => path === "/todos" ? { todos: updated } : {});
    await act(async () => { await refreshTodos(); });
    expect(row().querySelector('[data-testid="review-badge"]')?.textContent).toContain("1/2");
    expect(row().querySelector('[data-testid="criteria-summary"], details, summary, time')).toBeNull();
    expect(row().textContent).not.toContain("go test: PASS");
    expect(row().textContent).not.toContain("Agent · m1/s2");
    expect(host.querySelector('[aria-current="true"]')?.textContent).toContain("整理发布清单");
    // The existing task navigation opens the existing review checklist; the row adds no criterion UI.
    await act(async () => { row().click(); });
    const detail = host.querySelector('[data-testid="review-checklist"]')!;
    expect(detail.textContent).toContain("go test: PASS");
    expect(detail.textContent).toContain("agent:m1/s2");
    expect(detail.querySelector("time")?.dateTime).toBe(now);
  });
  it("keeps the selected Needs you detail neutral while working retains its orange tint", async () => {
    const { TODO_STATUS_INFO } = await import("../lib/todos");
    expect(host.querySelector(".bg-orange-tint.text-orange")?.textContent).toContain(TODO_STATUS_INFO.working.label);
    await act(async () => upsertSession(session("s1", { state: "needs_you", status: "blocked" })));
    expect([...host.querySelectorAll(".bg-field.text-attention")].some((el) => el.textContent?.includes(TODO_STATUS_INFO.needs_you.label))).toBe(true);
    expect([...host.querySelectorAll(".bg-orange-tint")].some((el) => el.textContent?.includes(TODO_STATUS_INFO.needs_you.label))).toBe(false);
  });

  it("re-render only the row whose agent changed", async () => {
    await act(async () => upsertSession(session("s1", { last_text: "改完了，在提交" })));
    expect(renders.get("ta")).toBe(1);
    expect(renders.get("tb")).toBeUndefined();
    expect(host.textContent).toContain("改完了，在提交");
  });

  it("re-render none when the push leaves what they show alone", async () => {
    await act(async () => upsertSession(session("s2", { last_text: "在跑测试", updated_at: new Date(Date.now() + 1000).toISOString() })));
    expect(renders.size).toBe(0);
  });

  it("re-render the row whose agent starts waiting on you, and it moves first", async () => {
    await act(async () => upsertSession(session("s2", { state: "needs_you", status: "blocked" })));
    expect(renders.get("tb")).toBe(1);
    expect(renders.get("ta")).toBeUndefined();
    const titles = [...host.querySelectorAll("[role=button] .truncate.font-semibold")].map((el) => el.textContent);
    expect(titles).toEqual(["修登录页", "整理发布清单"]);
  });

  it("labels open and closed concerns, not ordinary to-dos", async () => {
    const list = [
      { ...todo("ta", "Working concern", "s1"), kind: "concern" },
      todo("tb", "Ordinary task", "s2"),
      { ...todo("tc", "Reported concern", "s3"), kind: "concern", done_at: now, done_by: "reported", agents: [] },
      { ...todo("td", "Open concern", "s4"), kind: "concern", agents: [] },
    ];
    api.mockImplementation(async (path) => path === "/todos" ? { todos: list } : {});
    await act(async () => { await refreshTodos(); });
    const tag = (id: string) => host.querySelector(`[data-todo="${id}"] [data-testid="todo-concern"]`);
    expect(tag("ta")?.textContent).toBe(t("todos.concern"));
    expect(tag("tc")?.textContent).toBe(t("todos.concern"));
    expect(tag("td")?.textContent).toBe(t("todos.concern"));
    expect(tag("tb")).toBeNull();
  });
});
