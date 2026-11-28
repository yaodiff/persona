// @vitest-environment jsdom
// A read-only, stored-data board of open to-dos by project (#12713): responsible party, latest stored progress,
// owner decisions and a browser-local stale threshold. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import enTodos from "../i18n/locales/en/todos.json";
import zhTodos from "../i18n/locales/zh-Hans/todos.json";
import jaTodos from "../i18n/locales/ja/todos.json";
import { waitsForOwner, type Todo, type TodoBoardProjection } from "../lib/todos";
import { waitName } from "../lib/state-labels";
import type { Session } from "../lib/types";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
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

vi.mock("../lib/direction-tree", async (orig) => ({
  ...(await orig<typeof import("../lib/direction-tree")>()),
  useDirectionTree: () => null,
}));

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState, patchAssistant, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-23T12:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title,
  created_at: iso(2 * HOUR),
  updated_at: iso(HOUR),
  ...patch,
});
const progress = (at: string, line = "Stored one-line summary"): Pick<Todo, "supervision"> => ({
  supervision: {
    supervisor: "main",
    progress: { kind: "progress", at, line, text: "The much longer raw report, not the board summary" },
  },
});
const link = (session_id: string) => ({ machine_id: "mac", session_id, linked_at: iso(3 * HOUR) });
const worker: Session = {
  machine_id: "mac",
  session_id: "worker-1",
  title: "Linked Worker",
  cwd: "/code/apollo",
  status: "working",
  state: "working",
  updated_at: iso(0),
};
const ownerDecision = (td: Todo): Todo => ({
  ...td,
  supervision: {
    ...td.supervision,
    supervisor: "main",
    state: "needs_user",
    reviews: [{ at: iso(HOUR), verdict: "needs_user" }],
  },
});

let host: HTMLDivElement;
let root: Root;
let hub: Todo[];
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const option = (label: string) => [...host.querySelectorAll<HTMLElement>("[role=radio]")].find((el) => el.textContent?.trim() === label);
const board = () => host.querySelector<HTMLElement>("[data-testid=todos-board]");
const row = (id: string) => board()?.querySelector<HTMLElement>(`[data-todo="${id}"]`);
const group = (project: string) => board()?.querySelector<HTMLElement>(`[data-testid=todo-board-group][data-project="${project}"]`);
const threshold = () => board()?.querySelector<HTMLInputElement>("[data-testid=todo-board-stale-hours]");
const writes = () => api.mock.calls.filter(([, init]) => {
  const method = (init as { method?: string } | undefined)?.method;
  return method !== undefined && method.toUpperCase() !== "GET";
});

const render = async (todos: Todo[] = [todo("plain", "Plain open to-do", { project: "apollo" })], sessions: Session[] = [worker]) => {
  hub = todos;
  seed({
    todos,
    todosLoaded: true,
    todosError: null,
    todosSupported: true,
    machines: [{ id: "mac", name: "Mac", online: true }],
    sessions: { mac: sessions },
    taskRows: {},
    interactions: [],
    answered: {},
    features: [],
  });
  await act(async () => root.render(<TodosView />));
  await flush();
};

const openBoard = async () => {
  const choice = option(t("todos.view_board"));
  expect(choice, "the existing To-dos page offers Board mode").toBeTruthy();
  await act(async () => choice!.click());
  await flush();
  expect(board(), "Board mode renders its read-only board").toBeTruthy();
};

const setThreshold = async (value: string) => {
  const input = threshold();
  expect(input, "stale threshold input").toBeTruthy();
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, value);
    input!.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await flush();
};

const storageSnapshot = () => Object.fromEntries(Array.from({ length: localStorage.length }, (_, i) => {
  const key = localStorage.key(i)!;
  return [key, localStorage.getItem(key)];
}));

beforeEach(async () => {
  reset();
  await initI18n("en");
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  localStorage.clear();
  api.mockReset();
  api.mockImplementation(async (path: string, init?: unknown) => {
    const method = (init as { method?: string } | undefined)?.method;
    if (path === "/todos" && !method) return { todos: hub };
    if (path === "/kb/projects") return { items: [
      { id: "apollo", name: "Apollo", extra: "long · active" },
      { id: "beta", name: "Beta", extra: "long · active" },
    ] };
    if (path.endsWith("/launchers")) return { launchers: [] };
    if (path.endsWith("/decisions?all=1")) return { decisions: [] };
    if (method === "PATCH" && path.startsWith("/todos/")) {
      const current = hub.find((td) => td.id === path.slice("/todos/".length));
      return { todo: current ? { ...current, done_at: iso(0) } : { id: path } };
    }
    return {};
  });
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
  vi.restoreAllMocks();
  await initI18n("zh-Hans");
});

describe("the read-only To-dos board (#12713)", () => {
  it("groups only open to-dos by stored project and shows responsible and latest stored progress fields", async () => {
    const progressAt = iso(3 * HOUR);
    const linked = todo("linked", "Linked worker row", {
      project: "apollo",
      agents: [link("worker-1")],
      ...progress(progressAt),
    });
    const responsible = todo("responsible", "Stored responsible wins", {
      project: "apollo",
      responsible: { kind: "person", name: "Alex Outside Persona" },
      agents: [link("worker-1")],
      ...progress(iso(4 * HOUR)),
    });
    const unassigned = todo("unassigned", "No project row");
    const closed = todo("closed", "Closed row must not appear", { project: "apollo", done_at: iso(HOUR) });
    await render([linked, responsible, unassigned, closed]);
    await openBoard();

    expect(group("apollo")?.textContent).toContain("Apollo");
    expect(group("")?.textContent).toContain(t("todos.board_no_project"));
    expect(board()?.querySelector("[data-todo=closed]")).toBeNull();
    expect(row("responsible")?.textContent).toContain("Alex Outside Persona");
    expect(row("responsible")?.textContent).not.toContain("Linked Worker");
    expect(row("linked")?.textContent).toContain("Linked Worker");
    expect(row("linked")?.textContent).toContain("Stored one-line summary");
    expect(row("linked")?.textContent).not.toContain("much longer raw report");
    expect(row("linked")?.querySelector("time")?.getAttribute("datetime")).toBe(progressAt);
    expect(row("unassigned")?.textContent).toContain(t("todos.board_unassigned"));
    expect(row("unassigned")?.textContent).toContain(t("todos.board_no_progress"));
  });

  it("uses an external responsible party's stored update and note when no other progress exists", async () => {
    const updatedAt = iso(10 * HOUR);
    const external = todo("external-note", "Work tracked outside Persona", {
      project: "apollo",
      created_at: iso(100 * HOUR),
      updated_at: updatedAt,
      responsible: { kind: "session", name: "Outside supervisor" },
      notes: "Recorded an external checkpoint\nImplementation detail, not the one-line summary",
    });
    await render([external]);
    await openBoard();

    expect(row("external-note")?.textContent).toContain("Outside supervisor");
    expect(row("external-note")?.textContent).toContain("Recorded an external checkpoint");
    expect(row("external-note")?.textContent).not.toContain("Implementation detail");
    expect(row("external-note")?.querySelector("time")?.getAttribute("datetime")).toBe(updatedAt);
    expect(row("external-note")?.textContent).not.toContain(t("todos.board_stale"));
  });

  it("marks Waiting on you exactly when the existing owner-decision projection applies", async () => {
    const waiting = ownerDecision(todo("waiting", "Open owner decision", { project: "apollo" }));
    const dismissed = {
      ...ownerDecision(todo("dismissed", "Dismissed owner decision", { project: "apollo" })),
      decision_review: { review_at: iso(HOUR), dismissal_id: "dismiss-current-round" },
    };
    const unattended = todo("unattended", "Unattended but no owner decision", { project: "apollo" });
    expect(waitsForOwner(waiting)).toBe(true);
    expect(waitsForOwner(dismissed)).toBe(false);
    expect(waitsForOwner(unattended)).toBe(false);
    await render([waiting, dismissed, unattended]);
    await openBoard();

    expect(row("waiting")?.textContent).toContain(waitName("owner"));
    expect(row("dismissed")?.textContent).not.toContain(waitName("owner"));
    expect(row("unattended")?.textContent).not.toContain(waitName("owner"));
  });

  it("puts open, waiting and stale counts from displayed rows on every project heading", async () => {
    const waitingStale = ownerDecision(todo("waiting-stale", "Waiting and stale", {
      project: "apollo",
      ...progress(iso(80 * HOUR)),
    }));
    const fresh = todo("fresh", "Fresh", { project: "apollo", ...progress(iso(HOUR)) });
    const createdStale = todo("created-stale", "No progress and stale", {
      project: "apollo",
      created_at: iso(100 * HOUR),
    });
    const beta = todo("beta", "Beta fresh", { project: "beta", ...progress(iso(2 * HOUR)) });
    const noProject = todo("none", "No project stale", { created_at: iso(90 * HOUR) });
    await render([waitingStale, fresh, createdStale, beta, noProject]);
    await openBoard();

    for (const [project, open, waiting, stale] of [["apollo", 3, 1, 2], ["beta", 1, 0, 0], ["", 1, 0, 1]] as const) {
      const text = group(project)?.textContent ?? "";
      expect(text).toContain(t("todos.board_open_count", { count: open }));
      expect(text).toContain(t("todos.board_waiting_count", { count: waiting, state: waitName("owner") }));
      expect(text).toContain(t("todos.board_stale_count", { count: stale }));
    }
  });

  it("defaults to 72 hours and marks stale only strictly past progress, or creation when progress is absent", async () => {
    const exactProgress = todo("exact-progress", "Exactly 72 hours since progress", {
      project: "apollo",
      updated_at: iso(0),
      ...progress(iso(72 * HOUR)),
    });
    const overProgress = todo("over-progress", "Just over 72 hours since progress", {
      project: "apollo",
      updated_at: iso(0),
      ...progress(iso(72 * HOUR + 1)),
    });
    const exactCreated = todo("exact-created", "Exactly 72 hours since creation", {
      project: "apollo",
      created_at: iso(72 * HOUR),
      updated_at: iso(0),
    });
    const overCreated = todo("over-created", "Just over 72 hours since creation", {
      project: "apollo",
      created_at: iso(72 * HOUR + 1),
      updated_at: iso(0),
    });
    await render([exactProgress, overProgress, exactCreated, overCreated]);
    await openBoard();

    expect(threshold()?.value).toBe("72");
    expect(threshold()?.getAttribute("aria-label")).toBe(t("todos.board_stale_hours"));
    expect(row("exact-progress")?.textContent).not.toContain(t("todos.board_stale"));
    expect(row("over-progress")?.textContent).toContain(t("todos.board_stale"));
    expect(row("exact-created")?.textContent).not.toContain(t("todos.board_stale"));
    expect(row("over-created")?.textContent).toContain(t("todos.board_stale"));
  });

  it("persists a positive numeric threshold locally and rejects invalid values without replacing the last valid one", async () => {
    const old = todo("old", "Forty-nine hours old", { project: "apollo", ...progress(iso(49 * HOUR)) });
    const edge = todo("edge", "Exactly forty-eight hours old", { project: "apollo", ...progress(iso(48 * HOUR)) });
    await render([old, edge]);
    await openBoard();
    await setThreshold("48.5");

    expect(row("old")?.textContent).toContain(t("todos.board_stale"));
    expect(row("edge")?.textContent).not.toContain(t("todos.board_stale"));
    const stored = storageSnapshot();
    const thresholdEntries = Object.entries(stored).filter(([, value]) => value === "48.5");
    expect(thresholdEntries).toHaveLength(1);
    expect(writes()).toEqual([]);

    for (const invalid of ["", "abc", "0", "-1", "Infinity", "NaN", "1e309"]) {
      await setThreshold(invalid);
      expect(storageSnapshot()).toEqual(stored);
      expect(row("old")?.textContent).toContain(t("todos.board_stale"));
      expect(row("edge")?.textContent).not.toContain(t("todos.board_stale"));
    }

    await act(async () => root.render(<div />));
    await act(async () => root.render(<TodosView />));
    await flush();
    await openBoard();
    expect(threshold()?.value).toBe("48.5");
    expect(row("old")?.textContent).toContain(t("todos.board_stale"));
    expect(row("edge")?.textContent).not.toContain(t("todos.board_stale"));
    expect(writes()).toEqual([]);
  });

  it("opens and uses the board from stored data without writes, model or Agent actions, and rows only open detail", async () => {
    await render([todo("detail", "Open existing detail", { project: "apollo" })]);
    api.mockClear();
    await openBoard();
    expect(api).not.toHaveBeenCalled();
    await setThreshold("24");
    expect(api).not.toHaveBeenCalled();

    const boardRow = row("detail");
    expect(boardRow).toBeTruthy();
    expect(boardRow!.querySelector("button, input, select, textarea")).toBeNull();
    await act(async () => {
      boardRow!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
    });
    await flush();
    expect(document.querySelector("[role=menu]")).toBeNull();
    expect(writes()).toEqual([]);

    await act(async () => boardRow!.click());
    await flush();
    expect(host.querySelector<HTMLInputElement>("[data-testid=todo-detail] input[type=text]")?.value).toBe("Open existing detail");
    expect(writes()).toEqual([]);
    const actions = api.mock.calls.filter(([path]) => /(?:model|agent|session).*(?:call|start|launch)|\/todos\/[^/]+\/(?:retry|follow|check)/i.test(path));
    expect(actions).toEqual([]);
  });

  it("does not mark a finished to-do seen when Board detail navigation closes", async () => {
    const finishedWorker: Session = { ...worker, state: "unread", status: "idle", last_text: "Finished the turn" };
    const finished = todo("finished", "Unsupervised finished turn", { project: "apollo", agents: [link("worker-1")] });
    await render([finished], [finishedWorker]);
    await openBoard();
    seed({ features: ["todo_seen"] });
    api.mockClear();

    await act(async () => root.render(<TodosView view="board" selectedId="finished" />));
    await flush();
    expect(host.querySelector<HTMLInputElement>("[data-testid=todo-detail] input[type=text]")?.value).toBe("Unsupervised finished turn");
    await act(async () => root.render(<TodosView view="board" />));
    await flush();
    expect(writes()).toEqual([]);
  });

  it("uses a stopped worker step's own activity time for progress and staleness", async () => {
    const activityAt = iso(100 * HOUR);
    const stoppedWorker: Session = {
      ...worker,
      state: "idle",
      status: "idle",
      activity: "running tests",
      activity_at: activityAt,
      last_text: undefined,
      updated_at: iso(HOUR),
    };
    const stopped = todo("stopped", "Stopped test worker", { project: "apollo", created_at: iso(120 * HOUR), agents: [link("worker-1")] });
    await render([stopped], [stoppedWorker]);
    await openBoard();

    expect(row("stopped")?.textContent).toContain("running tests");
    expect(row("stopped")?.querySelector("time")?.getAttribute("datetime")).toBe(activityAt);
    expect(row("stopped")?.textContent).toContain(t("todos.board_stale"));
  });

  it("keeps the existing To-dos list completion write unchanged", async () => {
    await render();
    expect(option(t("todos.view_board")), "Board is additive to the existing list").toBeTruthy();
    const complete = row("plain") ?? host.querySelector<HTMLElement>("[data-todo=plain]");
    expect(complete).toBeTruthy();
    const checkbox = complete!.querySelector<HTMLButtonElement>("button[aria-label]");
    expect(checkbox?.getAttribute("aria-label")).toBe(t("todos.complete_todo"));
    await act(async () => checkbox!.click());
    await flush();
    expect(writes().map(([path, init]) => [path, (init as { json?: unknown }).json])).toEqual([
      ["/todos/plain", { done: true }],
    ]);
  });

  it.each([
    ["en", enTodos],
    ["zh-Hans", zhTodos],
    ["ja", jaTodos],
  ] as const)("provides every new board string in %s", async (lang, catalog) => {
    await initI18n(lang);
    await render();
    await openBoard();

    const strings = catalog as Record<string, string>;
    for (const key of [
      "view_board",
      "board_no_project",
      "board_unassigned",
      "board_no_progress",
      "board_stale",
      "board_stale_hours",
      "direction_uncategorized",
      "done_direction_filter_label",
      "direction_done_count",
      "detail_subtasks_done_toggle",
    ]) expect(strings[key], `${lang} todos.${key}`).toBeTruthy();
    expect(strings.direction_review_done_label_other ?? strings.direction_review_done_label, `${lang} todos.direction_review_done_label`).toBeTruthy();
    for (const key of ["board_open_count", "board_waiting_count", "board_stale_count"]) {
      expect(strings[`${key}_other`] ?? strings[key], `${lang} todos.${key}`).toBeTruthy();
      expect(t(`todos.${key}`, { count: 2, ...(key === "board_waiting_count" ? { state: waitName("owner") } : {}) })).not.toBe(`todos.${key}`);
    }
  });

  it("renders zero v2 board controls or hierarchy/direction badges when todos_v2 is off (#16939)", async () => {
    const parent = todo("parent", "Parent goal", {
      project: "apollo",
      direction_id: "dir-platform",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Platform Stream",
      child_summary: { total: 3, open: 2, needs_you: 1, working: 1, done: 1 },
    });
    const child = todo("child", "Child task", {
      project: "apollo",
      part_of: "parent",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Platform Stream",
    });
    const todoBoard: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [
        {
          direction_id: "dir-platform",
          label: "Platform Stream",
          todo_ids: ["parent", "child"],
          counts: { needs_you: 0, in_progress: 2, pending_review: 0, waiting: 0, done: 0, total: 2 },
        },
      ],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 2, pending_review: 0, waiting: 0, someday: 0, done: 0, open_total: 2 },
    };
    hub = [parent, child];
    seed({
      todos: hub,
      todoBoard,
      todosLoaded: true,
      todosError: null,
      todosSupported: true,
      machines: [{ id: "mac", name: "Mac", online: true }],
      sessions: { mac: [worker] },
      taskRows: {},
      interactions: [],
      answered: {},
      features: [],
      assistant: {
        machine_id: "mac",
        main: "main",
        leaders: ["dir-platform"],
        conversations: [{ machine_id: "mac", session_id: "dir-platform", title: "Platform Direction" }],
        todos_v2: false,
      },
    });
    await act(async () => root.render(<TodosView />));
    await flush();
    await openBoard();

    expect(board()?.querySelector("[data-testid=todo-board-grouping]")).toBeNull();
    expect(board()?.querySelector("[data-testid=board-direction-tag]")).toBeNull();
    expect(board()?.querySelector("[data-testid=board-parent-goal]")).toBeNull();
    expect(board()?.querySelector("[data-testid=board-child-summary]")).toBeNull();
    expect(row("child")?.querySelector("button")).toBeNull();
  });
});

describe("To-dos v2 board direction, hierarchy, and grouping (#16939)", () => {
  const renderV2Board = async ({
    todos,
    todoBoard = null,
    sessions = [worker],
    todosV2 = true,
  }: {
    todos: Todo[];
    todoBoard?: TodoBoardProjection | null;
    sessions?: Session[];
    todosV2?: boolean;
  }) => {
    hub = todos;
    seed({
      todos,
      todoBoard,
      todosLoaded: true,
      todosError: null,
      todosSupported: true,
      machines: [{ id: "mac", name: "Mac", online: true }],
      sessions: { mac: sessions },
      taskRows: {},
      interactions: [],
      answered: {},
      features: [],
      assistant: {
        machine_id: "mac",
        main: "main",
        leaders: ["dir-platform", "dir-growth"],
        conversations: [
          { machine_id: "mac", session_id: "dir-platform", title: "Platform Direction" },
          { machine_id: "mac", session_id: "dir-growth", title: "Growth Direction" },
        ],
        todos_v2: todosV2,
      },
    });
    await act(async () => root.render(<TodosView />));
    await flush();
    await openBoard();
  };

  const boardGroupingButton = (value: "project" | "direction") =>
    board()?.querySelector<HTMLButtonElement>(`[data-testid=todo-board-grouping] [data-board-grouping="${value}"]`);

  const directionGroup = (directionId: string) =>
    board()?.querySelector<HTMLElement>(`[data-testid=todo-board-group][data-direction="${directionId}"]`);

  it("renders effective direction tags for explicit direction_id, server effective_direction_name, and board.direction_groups", async () => {
    const explicit = todo("explicit-dir", "Explicit direction item", {
      project: "apollo",
      direction_id: "dir-growth",
    });
    const serverEffective = todo("server-eff", "Server effective direction item", {
      project: "apollo",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Platform Stream",
    });
    const boardGroupResolved = todo("board-dir", "Board direction_groups item", {
      project: "beta",
    });
    const ungrouped = todo("ungrouped", "No direction item", {
      project: "beta",
    });
    const todoBoard: TodoBoardProjection = {
      status_groups: [{
        key: "in_progress",
        total_count: 4,
        visible_count: 4,
        entries: [explicit, serverEffective, boardGroupResolved, ungrouped].map(({ id }) => ({ todo_id: id })),
      }],
      direction_groups: [
        {
          direction_id: "dir-platform",
          label: "Platform Stream",
          todo_ids: ["board-dir"],
          counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
        },
      ],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 4, pending_review: 0, waiting: 0, someday: 0, done: 0, open_total: 4 },
    };

    await renderV2Board({ todos: [explicit, serverEffective, boardGroupResolved, ungrouped], todoBoard });

    const explicitTag = row("explicit-dir")?.querySelector("[data-testid=board-direction-tag]");
    expect(explicitTag?.textContent).toBe("Growth Direction");
    expect(explicitTag?.getAttribute("data-direction-inherited")).toBe("false");

    const serverTag = row("server-eff")?.querySelector("[data-testid=board-direction-tag]");
    expect(serverTag?.textContent).toBe("Platform Stream");
    expect(serverTag?.getAttribute("data-direction-inherited")).toBe("true");

    const boardTag = row("board-dir")?.querySelector("[data-testid=board-direction-tag]");
    expect(boardTag?.textContent).toBe("Platform Stream");
    expect(boardTag?.getAttribute("data-direction-inherited")).toBe("true");

    expect(row("ungrouped")?.querySelector("[data-testid=board-direction-tag]")).toBeNull();
  });

  it("renders clickable parent goal breadcrumb and jumps to parent detail without triggering child row click", async () => {
    const parent = todo("goal-parent", "Ship Q4 platform release", {
      project: "apollo",
      direction_id: "dir-platform",
    });
    const child = todo("goal-child", "Update board hierarchy UI", {
      project: "apollo",
      part_of: "goal-parent",
    });
    const orphanChild = todo("orphan-child", "Task under unloaded parent", {
      project: "apollo",
      part_of: "missing-parent",
    });

    await renderV2Board({ todos: [parent, child, orphanChild] });

    const parentBtn = row("goal-child")?.querySelector<HTMLButtonElement>("button[data-testid=board-parent-goal]");
    expect(parentBtn).toBeTruthy();
    expect(parentBtn?.textContent).toContain(t("todos.goal_parent_hint", { title: "Ship Q4 platform release" }));
    // Board row with clickable parent goal uses sibling controls rather than nesting a button inside role="button" (#17696)
    expect(row("goal-child")?.getAttribute("role")).toBeNull();
    expect(parentBtn?.closest('[role="button"]')).toBeNull();
    expect(row("goal-child")?.querySelector<HTMLButtonElement>("button[data-testid=board-row-open]")).toBeTruthy();

    // Inherited direction from parent also resolves cleanly via effectiveDirectionOf.
    expect(row("goal-child")?.querySelector("[data-testid=board-direction-tag]")?.textContent).toBe("Platform Direction");

    // Non-loaded parent renders as a non-interactive span fallback.
    expect(row("orphan-child")?.querySelector("button[data-testid=board-parent-goal]")).toBeNull();
    expect(row("orphan-child")?.querySelector("span[data-testid=board-parent-goal]")?.textContent).toContain("missing-parent");

    // Clicking parent breadcrumb opens parent detail, NOT child detail.
    await act(async () => parentBtn!.click());
    await flush();
    expect(host.querySelector<HTMLInputElement>("[data-testid=todo-detail] input[type=text]")?.value).toBe("Ship Q4 platform release");
  });

  it("renders server child_summary progress badges and falls back to direct children when child_summary is absent", async () => {
    const serverParent = todo("server-parent", "Parent with server summary", {
      project: "apollo",
      child_summary: { total: 5, open: 3, needs_you: 1, working: 2, done: 2 },
    });
    const fallbackParent = todo("fallback-parent", "Parent computed from children", {
      project: "apollo",
    });
    const doneChild = todo("fb-done", "Done subtask", {
      project: "apollo",
      part_of: "fallback-parent",
      done_at: iso(HOUR),
    });
    const workingChild = todo("fb-working", "Working subtask", {
      project: "apollo",
      part_of: "fallback-parent",
      agents: [link("worker-1")],
    });
    const blockedChild = ownerDecision(todo("fb-blocked", "Blocked subtask", {
      project: "apollo",
      part_of: "fallback-parent",
    }));
    const allDoneParent = todo("all-done-parent", "Parent with all children done stays open", {
      project: "beta",
      child_summary: { total: 2, open: 0, needs_you: 0, working: 0, done: 2 },
    });
    const leaf = todo("leaf-item", "Standalone leaf", { project: "beta" });

    await renderV2Board({
      todos: [serverParent, fallbackParent, doneChild, workingChild, blockedChild, allDoneParent, leaf],
    });

    const serverBadge = row("server-parent")?.querySelector("[data-testid=board-child-summary]");
    expect(serverBadge?.textContent).toContain(t("todos.goal_progress", { done: 2, total: 5 }));
    expect(serverBadge?.querySelector("[data-testid=board-child-summary-working]")?.textContent).toBe(
      t("todos.child_summary_working", { count: 2 }),
    );
    expect(serverBadge?.querySelector("[data-testid=board-child-summary-blocked]")?.textContent).toBe(
      t("todos.goal_blocked", { count: 1 }),
    );

    const fallbackBadge = row("fallback-parent")?.querySelector("[data-testid=board-child-summary]");
    expect(fallbackBadge?.textContent).toContain(t("todos.goal_progress", { done: 1, total: 3 }));
    expect(fallbackBadge?.querySelector("[data-testid=board-child-summary-working]")?.textContent).toBe(
      t("todos.child_summary_working", { count: 1 }),
    );
    expect(fallbackBadge?.querySelector("[data-testid=board-child-summary-blocked]")?.textContent).toBe(
      t("todos.goal_blocked", { count: 1 }),
    );

    // Parent with all children done remains visible on open board (never auto-closed).
    const allDoneBadge = row("all-done-parent")?.querySelector("[data-testid=board-child-summary]");
    expect(allDoneBadge?.textContent).toContain(t("todos.goal_progress", { done: 2, total: 2 }));
    expect(allDoneBadge?.querySelector("[data-testid=board-child-summary-working]")).toBeNull();
    expect(allDoneBadge?.querySelector("[data-testid=board-child-summary-blocked]")).toBeNull();

    // Leaf has no child summary badge.
    expect(row("leaf-item")?.querySelector("[data-testid=board-child-summary]")).toBeNull();
  });

  it("switches board grouping, sorts ungrouped last, hides redundant tags, and keeps the v2 choice across rollback", async () => {
    const platA = ownerDecision(todo("plat-a", "Platform waiting task", {
      project: "apollo",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Platform Stream",
      ...progress(iso(80 * HOUR)),
    }));
    const growthA = todo("growth-a", "Growth fresh task", {
      project: "apollo",
      direction_id: "dir-growth",
      ...progress(iso(HOUR)),
    });
    const ungroupedA = todo("ungrouped-a", "Secretary ungrouped task", {
      project: "beta",
      ...progress(iso(2 * HOUR)),
    });

    await renderV2Board({ todos: [platA, growthA, ungroupedA] });

    // Defaults to project grouping; direction tags are shown on rows with directions.
    expect(boardGroupingButton("project")?.getAttribute("aria-checked")).toBe("true");
    expect(boardGroupingButton("direction")?.getAttribute("aria-checked")).toBe("false");
    expect(group("apollo")).toBeTruthy();
    expect(group("beta")).toBeTruthy();
    expect(row("plat-a")?.querySelector("[data-testid=board-direction-tag]")?.textContent).toBe("Platform Stream");
    expect(row("growth-a")?.querySelector("[data-testid=board-direction-tag]")?.textContent).toBe("Growth Direction");

    // Toggle to By direction.
    await act(async () => boardGroupingButton("direction")!.click());
    await flush();

    expect(boardGroupingButton("direction")?.getAttribute("aria-checked")).toBe("true");
    expect(boardGroupingButton("project")?.getAttribute("aria-checked")).toBe("false");

    const sections = [...(board()?.querySelectorAll<HTMLElement>("[data-testid=todo-board-group]") ?? [])];
    expect(sections.map((el) => el.getAttribute("data-direction"))).toEqual(["dir-growth", "dir-platform", ""]);

    expect(directionGroup("dir-growth")?.textContent).toContain("Growth Direction");
    expect(directionGroup("dir-platform")?.textContent).toContain("Platform Stream");
    expect(directionGroup("dir-platform")?.getAttribute("data-waiting-count")).toBe("1");
    expect(directionGroup("dir-platform")?.getAttribute("data-stale-count")).toBe("1");
    expect(directionGroup("")?.querySelector("header")?.textContent).toContain(t("todos.direction_uncategorized"));
    expect(directionGroup("")?.querySelector("header")?.textContent).not.toContain(t("lib-todo-tree.secretary"));

    // Redundant direction chips matching the current direction group are omitted.
    expect(row("plat-a")?.querySelector("[data-testid=board-direction-tag]")).toBeNull();
    expect(row("growth-a")?.querySelector("[data-testid=board-direction-tag]")).toBeNull();
    expect(row("ungrouped-a")?.querySelector("[data-testid=board-direction-tag]")).toBeNull();

    // Turning todos_v2 off immediately hides the switcher and restores project grouping.
    await act(async () => {
      patchAssistant({ todos_v2: false });
    });
    await flush();
    expect(board()?.querySelector("[data-testid=todo-board-grouping]")).toBeNull();
    expect(group("apollo")).toBeTruthy();
    expect(group("beta")).toBeTruthy();
    expect(board()?.querySelector("[data-testid=board-direction-tag]")).toBeNull();

    // Re-enabling the Lab restores the previous v2 choice.
    await act(async () => {
      patchAssistant({ todos_v2: true });
    });
    await flush();
    expect(boardGroupingButton("direction")?.getAttribute("aria-checked")).toBe("true");
    expect(directionGroup("dir-platform")).toBeTruthy();
  });
});
