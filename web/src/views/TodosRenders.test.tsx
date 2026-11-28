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

const { getState, putTodo, upsertSession, refreshTodos } = await import("../lib/store");
const { applyLeadersEvent, resetDirectionNames } = await import("../lib/direction-names");
const { ReviewChecklist, TodosView } = await import("./Todos");
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
  resetDirectionNames();
  seed(before);
});

describe("todo rows on a sessions push", () => {
  it("renders a fresh waiting PR in its own section with collapsed summary and expandable blocker details", async () => {
    const url = "https://example.com/repo/pull/91";
    await act(async () => putTodo({ ...getState().todos[0], id: "ta", title: "Waiting PR", agents: [], pr_url: url, delivery: { state: "pr" }, pr_status: { url, state: "open", draft: false, review: "required", checks: "failure", conflicts: false, checked_at: new Date(Date.now() - 30_000).toISOString() } }));
    const section = host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="parked"]');
    expect(section?.textContent).toContain("Waiting PR");
    expect(section?.querySelector('[data-testid="todo-status-line"]')).toBeNull();
    expect(section?.querySelector('[data-wait-collapsed="true"]')).not.toBeNull();
    expect(section?.querySelector('[data-testid="todo-pr-line"] a')).toBeNull();
    await act(async () => section?.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    const link = section?.querySelector<HTMLAnchorElement>('[data-testid="todo-pr-line"] a');
    expect(link?.href).toBe(url);
    expect(section?.textContent).toContain(t("assistant-tasks.pr_blocker_review_required"));
    expect(section?.textContent).toContain(t("assistant-tasks.pr_blocker_checks_failed"));
  });

  it("refreshes N/M, evidence and reporter from a to-do update without waiting for a review", async () => {
    const list = getState().todos.map((td) => ({ ...td, supervision: {
      supervisor: "main", done_when: ["Tests pass", "PR opened"],
      criteria: [{ criterion: "Tests pass", state: "pending" as const }, { criterion: "PR opened", state: "pending" as const }],
    } }));
    api.mockImplementation(async (path) => path === "/todos" ? { todos: list } : {});
    await act(async () => { await refreshTodos(); });
    const row = () => [...host.querySelectorAll<HTMLElement>("[data-todo]")].find((el) => el.textContent?.includes("修登录页"))!;
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
    expect(detail.querySelector('[data-testid="review-evidence-author"]')?.textContent).toBe("s2");
    expect(detail.textContent).not.toContain("agent:m1/s2");
    expect(detail.querySelector("time")?.dateTime).toBe(now);
  });

  it("renders readable evidence authors in ReviewChecklist and scopes lookups to the todo manager", async () => {
    const { makeSessionLookup } = await import("../lib/todos");
    await act(async () => {
      applyLeadersEvent([{ id: "dir-1", name: "Release Train" }]);
    });
    seed({
      assistant: {
        main: "main-conv",
        conversations: [
          session("dir-1", { assistant: true, title: "Old Direction Title" }),
          session("conv-1", { assistant: true, title: "Weekly Triage" }),
        ],
      },
    });
    const lookup = makeSessionLookup([
      session("m1", { machine_id: "m1", session_id: "s2", title: "Fix login tests" }),
    ]);
    const baseSup = {
      supervisor: "dir-1",
      done_when: ["Direction check", "Conversation check", "Unknown supervisor", "Agent check", "Missing agent", "Judge check", "Unknown format"],
      criteria: [
        { criterion: "Direction check", state: "met" as const, evidence: "ok", by: "supervisor:dir-1" },
        { criterion: "Conversation check", state: "met" as const, evidence: "ok", by: "supervisor:conv-1" },
        { criterion: "Unknown supervisor", state: "met" as const, evidence: "ok", by: "supervisor:agy-231f5e13-secret" },
        { criterion: "Agent check", state: "met" as const, evidence: "ok", by: "agent:m1/s2" },
        { criterion: "Missing agent", state: "met" as const, evidence: "ok", by: "agent:m1/gone-session" },
        { criterion: "Judge check", state: "met" as const, evidence: "ok", by: "judge" },
        { criterion: "Unknown format", state: "met" as const, evidence: "ok", by: "raw:internal-999" },
      ],
    };
    const defaultTodo: Todo = { ...todo("tc", "Checklist", "s2"), supervision: baseSup };
    await act(async () => root.render(<ReviewChecklist todo={defaultTodo} lookup={lookup} />));
    const authors = () => [...host.querySelectorAll('[data-testid="review-evidence-author"]')].map((el) => el.textContent);
    expect(authors()).toEqual([
      "Release Train",
      "Weekly Triage",
      t("assistant-tasks.evidence_author_supervisor"),
      "Fix login tests",
      t("assistant-tasks.evidence_author_agent"),
      t("assistant-tasks.evidence_author_reviewer"),
      t("assistant-tasks.evidence_author_reviewer"),
    ]);
    expect(host.textContent).not.toContain("supervisor:");
    expect(host.textContent).not.toContain("agy-231f5e13");
    expect(host.textContent).not.toContain("agent:m1/");
    expect(host.textContent).not.toContain("gone-session");
    expect(host.textContent).not.toContain("internal-999");

    // Cross-manager todo must not leak the default manager's direction name, conversation title, or session title.
    const otherManagerTodo: Todo = { ...defaultTodo, manager: "other-mgr" };
    await act(async () => root.render(<ReviewChecklist todo={otherManagerTodo} lookup={lookup} />));
    expect(authors()).toEqual([
      t("assistant-tasks.evidence_author_supervisor"),
      t("assistant-tasks.evidence_author_supervisor"),
      t("assistant-tasks.evidence_author_supervisor"),
      t("assistant-tasks.evidence_author_agent"),
      t("assistant-tasks.evidence_author_agent"),
      t("assistant-tasks.evidence_author_reviewer"),
      t("assistant-tasks.evidence_author_reviewer"),
    ]);
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
    const titles = [...host.querySelectorAll("[data-todo] .truncate.font-semibold")].map((el) => el.textContent);
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
    const openConcernLine = host.querySelector('[data-todo="td"] [data-testid="todo-status-line"]')?.textContent ?? "";
    expect(openConcernLine).toContain(t("todos.status_tracking"));
    expect(openConcernLine).not.toContain(t("todos.not_handed"));
  });

  it("keeps a fresh reported concern in Just finished and reserves awaiting review for live delivery", async () => {
    const reported = { ...todo("tc", "Reported concern", "s3"), kind: "concern", done_at: now, done_by: "reported", agents: [] };
    const live = { ...todo("td", "Live delivery", "s4"), agents: [], delivery: { state: "live", at: now },
      supervision: { supervisor: "main", state: "pending_review" as const } };
    const load = async (list: Todo[]) => {
      api.mockImplementation(async (path) => path === "/todos" ? { todos: list } : {});
      await act(async () => { await refreshTodos(); });
    };
    const title = () => host.querySelector('[data-testid="todo-section"][data-section="just_done"] button')?.textContent;

    await load([reported]);
    expect(title()).toContain("刚做完");
    await load([live]);
    expect(title()).toContain("已交付 · 待看");
    await load([reported, live]);
    expect(title()).toContain("刚做完");
  });
});
