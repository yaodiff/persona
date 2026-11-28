// @vitest-environment jsdom
// The To-dos page's third grouping, 常驻 (#4352): offered only with a leader in the hub's direction tree, one section per
// direction in the tree's order (empty ones skipped, the secretary's titled 书记官), the page's own open rows, the to-dos
// the tree does not list as open (closed in the last day, inactive) under their status sections after the directions, as
// on Android, and the Completed fold kept for the Done section's; the rows keep their menu; a saved choice waits for the
// first read (loading, not the status grouping) and falls back to status with no leaders or on a hub without the tree.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { countsLabel } from "../lib/direction-names";
import type { DirectionTree } from "../lib/direction-tree";
import type { Todo, TodoBoardProjection } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const tree = vi.hoisted(() => ({ current: undefined as DirectionTree | null | undefined }));
vi.mock("../lib/direction-tree", async (orig) => ({
  ...(await orig<typeof import("../lib/direction-tree")>()),
  useDirectionTree: () => tree.current,
}));
const apiMock = vi.hoisted(() => vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({})));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => apiMock(path, init),
}));

const { getState, refreshTodos, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = "2026-11-10T00:00:00Z";
const todo = (id: string, title: string, extra: Partial<Todo> = {}): Todo => ({ id, title, created_at: at, updated_at: at, ...extra });
const counts = (working = 0, needs_you = 0) => ({ working, needs_you, stuck: 0, review: 0 });
const TREE: DirectionTree = {
  directions: [
    { id: "lead-b", name: "Infrastructure", counts: counts(1, 0), running: true, tasks: [{ id: "t-infra", title: "Infra", state: "working" }, { id: "t-done", title: "Shipped", state: "working" }] },
    { id: "lead-empty", name: "Nothing yet", counts: counts(), running: false, tasks: [] },
    { id: "lead-a", name: "Product", counts: counts(1, 1), running: false, tasks: [{ id: "t-ask", title: "Ask", state: "needs_you" }, { id: "t-card", title: "Card", state: "working" }] },
    { id: "main", name: "", secretary: true, counts: counts(1, 0), running: false, tasks: [{ id: "t-own", title: "Own", state: "working" }] },
  ],
};
const TODOS = [todo("t-card", "Card"), todo("t-ask", "Ask"), todo("t-infra", "Infra"), todo("t-own", "Own"), todo("t-done", "Shipped", { done_at: at })];

const seed = (patch: Partial<ReturnType<typeof getState>>) => Object.assign(getState(), patch);
const before = { ...getState() };
let host: HTMLDivElement, root: Root;
const render = async () => { await act(async () => root.render(<TodosView />)); };
const option = (label: string) => [...host.querySelectorAll<HTMLElement>("[role=radio]")].find((b) => b.textContent?.trim() === label);
const menuItems = () => [...document.querySelectorAll<HTMLElement>("[role=menuitem], [role=menuitemradio]")];
const sections = () => [...host.querySelectorAll<HTMLElement>('[data-testid="todo-group"][data-group^="direction:"]')];

beforeEach(async () => {
  await initI18n("zh-Hans");
  localStorage.clear();
  apiMock.mockReset();
  apiMock.mockResolvedValue({});
  tree.current = TREE;
  seed({ todos: TODOS, todoBoard: null, closedCursors: {}, closedLoading: false, closedError: null, todosLoaded: true, todosError: null, todosSupported: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("To-dos grouped by 常驻", () => {
  it("draws one section per direction in the tree's order, skips empty ones, names the secretary's 书记官, keeps Completed", async () => {
    await render();
    const choice = option(t("todos.grouping_direction"));
    expect(choice).toBeTruthy();
    await act(async () => choice!.click());
    expect(localStorage.getItem("persona.todos.grouping")).toBe("direction");
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);
    const [infra, product, secretary] = sections();
    expect(infra.querySelector("button")!.textContent).toContain("Infrastructure");
    expect(secretary.querySelector("button")!.textContent).toContain(t("lib-todo-tree.secretary"));
    // what it has going on the title as the sidebar's 常驻 rows show it (#6626), and its rows in the tree's order (needs
    // you first), not the page's own: Product waits on you and nothing under it runs (its working task's Agents stopped)
    expect(product.querySelector("button")!.textContent).toContain(countsLabel({ needs_you: 1 }, t));
    expect(product.querySelector("[data-direction-counts] [data-shape=\"arc\"]")).toBeNull();
    // something runs under Infrastructure: the same working arc as a conversation running its own turn (#6230), and
    // no "在做 N" beside it (one meaning, one expression)
    expect(infra.querySelector("[data-direction-counts] [data-shape=\"arc\"][data-state=\"working\"]")).not.toBeNull();
    expect(infra.querySelector("[data-direction-counts]")!.textContent).toBe("");
    expect(infra.querySelector("button")!.textContent).not.toContain("在做");
    const text = product.textContent ?? "";
    expect(text.indexOf("Ask")).toBeGreaterThan(-1);
    expect(text.indexOf("Ask")).toBeLessThan(text.indexOf("Card"));
    expect(host.textContent).toContain(t("todos.done_section", { count: 1 }));
    expect(host.querySelector('[data-testid="todos-direction-unlisted"]')).toBeNull();
    // a closed to-do the tree still lists is in no section, only in the Completed fold
    expect(sections().some((s) => s.textContent?.includes("Shipped"))).toBe(false);
    expect(infra.textContent).toContain("Infra");
    // the first row shown opens beside the list, not the list's first open to-do ("Card", second in Product)
    expect(host.querySelector<HTMLInputElement>("[data-testid=todos-detail-column] input[type=text]")?.value).toBe("Infra");
  });

  it("keeps cancelled out of Completed and shows its own fold and reason", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    seed({ todos: [...TODOS, todo("t-cancelled", "Cancelled work", { done_at: at, done_by: "review", cancelled_at: at, cancelled_reason: "Not needed" })] });
    await render();
    expect(host.textContent).toContain(t("todos.done_section", { count: 1 }));
    expect(host.textContent).toContain(t("todos.cancelled_section", { count: 1 }));
    expect(host.textContent).not.toContain(t("todos.done_section", { count: 2 }));
    expect(sections().some((s) => s.textContent?.includes("Cancelled work"))).toBe(false);
    const fold = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("todos.cancelled_section", { count: 1 }));
    await act(async () => fold!.click());
    expect(host.textContent).toContain("Cancelled work");
    expect(host.textContent).toContain("Not needed");
  });

  it("gives cancelled its own status section instead of mixing it into Just finished", async () => {
    seed({ todos: [...TODOS, todo("t-cancelled", "Cancelled work", { done_at: at, done_by: "review", cancelled_at: at })] });
    await render();
    const cancelled = host.querySelector('[data-section="cancelled"]');
    expect(cancelled).toBeTruthy();
    await act(async () => cancelled!.querySelector("button")!.click());
    expect(cancelled?.textContent).toContain("Cancelled work");
    expect(host.querySelector('[data-section="done"]')?.textContent).not.toContain("Cancelled work");
  });

  it("marks the rows the sidebar's badge counts, and counts them on a folded direction's title (#11838)", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    await render();
    const product = sections()[1];
    // the owner's own to-dos with no Agent on them: each counted by the badge, each marked
    expect(product.querySelectorAll("[data-testid=todo-owner-mark]")).toHaveLength(2);
    const title = product.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(title.querySelector("[data-testid=todo-owner-count]")).toBeNull();
    await act(async () => title.click());
    expect(product.querySelector("[data-testid=todo-owner-mark]")).toBeNull();
    expect(title.querySelector("[data-testid=todo-owner-count]")?.textContent).toBe(t("todos.owner_mark_count", { count: 2 }));
  });

  it("opens a row's menu on a right click, as the other groupings do", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    await render();
    const row = [...sections()[1].querySelectorAll<HTMLElement>("[data-todo]")].find((r) => r.textContent?.includes("Card"));
    expect(row).toBeTruthy();
    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 120 });
    await act(async () => void row!.dispatchEvent(ev));
    expect(ev.defaultPrevented).toBe(true);
    expect(menuItems().length).toBeGreaterThan(0);
  });

  it("keeps a saved choice while the first read is out, the list loading, then draws the sections", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    tree.current = undefined;
    await render();
    expect(option(t("todos.grouping_direction"))?.getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector('[data-testid="todos-loading"]')).toBeTruthy();
    expect(host.querySelector('[data-testid="todos-sections"]')).toBeNull();
    tree.current = TREE;
    await render();
    expect(host.querySelector('[data-testid="todos-loading"]')).toBeNull();
    expect(sections()).toHaveLength(3);
  });

  it("says how many open to-dos no section lists (a worker's, nested under its leader's Agent) instead of hiding them", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    seed({ todos: [...TODOS, todo("t-worker", "Worker")] });
    await render();
    expect(sections()).toHaveLength(3);
    expect(host.textContent).not.toContain("Worker");
    expect(host.querySelector('[data-testid="todos-direction-unlisted"]')?.textContent).toBe(t("todos.direction_unlisted", { count: 1 }));
  });

  it("says so rather than the empty state when no open to-do is in the tree, and shows no note when every one is", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    seed({ todos: [todo("t-loose", "Loose")] });
    await render();
    expect(sections()).toHaveLength(0);
    expect(host.textContent).not.toContain(t("todos.empty_title"));
    expect(host.querySelector('[data-testid="todos-direction-unlisted"]')?.textContent).toBe(t("todos.direction_unlisted", { count: 1 }));
    await act(async () => root.unmount());
    seed({ todos: [] });
    root = createRoot(host);
    await render();
    expect(host.textContent).toContain(t("todos.empty_title"));
    expect(host.querySelector('[data-testid="todos-direction-unlisted"]')).toBeNull();
  });

  it("shows a status-only Shelved section instead of the empty state", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const daysAgo = new Date(Date.now() - 48 * 3_600_000).toISOString();
    seed({ todos: [todo("t-quiet", "Quiet", { origin: "conv", created_at: daysAgo, updated_at: daysAgo })] });
    await render();
    expect(host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=shelved]")?.getAttribute("data-todos")).toBe("1");
    expect(host.textContent).not.toContain(t("todos.empty_title"));
    expect(host.querySelector('[data-testid="todos-direction-unlisted"]')).toBeNull();
  });

  it("labels a fresh closed concern Just finished, including a mixed Just finished section", async () => {
    await initI18n("en");
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
    const closed = todo("t-closed-concern", "Reported concern", { kind: "concern", done_at: hourAgo, done_by: "reported", updated_at: hourAgo });
    const delivered = todo("t-live-delivery", "Live delivery", { delivery: { state: "live", at: hourAgo }, updated_at: hourAgo });
    const header = () => host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=just_done] > button");
    seed({ todos: [closed] });
    await render();
    expect(header()?.textContent).toContain("Just finished");
    expect(header()?.textContent).not.toContain("Delivered · awaiting review");

    // A mixed section keeps the ordinary name too; each related row still carries its own triage rule.
    await act(async () => root.unmount());
    seed({ todos: [closed, delivered] });
    root = createRoot(host);
    await render();
    expect(header()?.textContent).toContain("Just finished");
    expect(header()?.textContent).not.toContain("Delivered · awaiting review");
  });

  it("labels a wholly unaccepted live-delivery section Delivered awaiting review", async () => {
    await initI18n("en");
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
    const delivered = todo("t-live-delivery", "Live delivery", { delivery: { state: "live", at: hourAgo }, updated_at: hourAgo });
    seed({ todos: [delivered] });
    await render();
    const header = host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=just_done] > button");
    expect(header?.textContent).toContain("Delivered · awaiting review");
    expect(header?.textContent).not.toContain("Just finished");
  });

  it("puts closed work immediately in Done and an inactive one in its status section, counted as the header says", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
    const daysAgo = new Date(Date.now() - 3 * 86_400_000).toISOString();
    // closed an hour ago (Done immediately, not open), and a handed-out task with nothing for three days (Shelved): the
    // hub's tree lists neither, as neither counts as open
    const fresh = todo("t-fresh", "Fresh", { done_at: hourAgo, updated_at: hourAgo });
    const quiet = todo("t-quiet", "Quiet", { origin: "conv", created_at: daysAgo, updated_at: daysAgo });
    const header = t("todos.summary_triage", { open: 4, parked: 0, shelved: 1, done: 2, cancelled: 0 });
    for (const listed of [false, true]) {
      // and a tree whose push has not caught up yet still lists the one just ticked
      tree.current = listed ? { directions: TREE.directions.map((d) => (d.id === "lead-b" ? { ...d, tasks: [...(d.tasks ?? []), { id: "t-fresh", title: "Fresh", state: "working" }] } : d)) } : TREE;
      seed({ todos: [...TODOS, fresh, quiet] });
      await act(async () => root.unmount());
      root = createRoot(host);
      await render();
      expect(sections().some((s) => s.textContent?.includes("Fresh"))).toBe(false);
      expect(host.querySelector("[data-testid=todo-section][data-section=just_done]")).toBeNull();
      const shelved = host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=shelved]");
      expect(shelved?.getAttribute("data-todos")).toBe("1");
      // neither is "an open to-do no direction lists"
      expect(host.querySelector('[data-testid="todos-direction-unlisted"]')).toBeNull();
      expect(host.querySelector("[data-testid=todos-summary]")?.textContent).toBe(header);
      expect(host.textContent).toContain(t("todos.done_section", { count: 2 }));
    }
  });

  it("narrows each section by the due-date filter (#3740) and keeps the tree's order under any sort", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    localStorage.setItem("persona.todos.sort", "priority");
    // by priority Card (high) would lead Ask (low); the section keeps the tree's order, which the hub sets by state
    seed({ todos: [todo("t-card", "Card", { due_date: "2026-11-20", priority: "high" }), todo("t-ask", "Ask", { priority: "low" }), todo("t-infra", "Infra"), todo("t-own", "Own")] });
    await render();
    const product = sections()[1].textContent ?? "";
    expect(product.indexOf("Ask")).toBeGreaterThan(-1);
    expect(product.indexOf("Ask")).toBeLessThan(product.indexOf("Card"));
    // No due date leaves Card out of its section
    await act(async () => root.unmount());
    localStorage.setItem("persona.todos.filter", "no_due");
    root = createRoot(host);
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);
    expect(sections()[1].textContent).toContain("Ask");
    expect(sections()[1].textContent).not.toContain("Card");
    // a filter that empties a direction drops its section, as an empty direction is skipped (Card, due or overdue, is in This month)
    await act(async () => root.unmount());
    localStorage.setItem("persona.todos.filter", "month");
    root = createRoot(host);
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-a"]);
  });

  it("is not offered with no leader, and a saved choice shows by status", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    tree.current = { directions: [{ id: "main", name: "", secretary: true, counts: counts(1), running: false, tasks: [{ id: "t-own", title: "Own", state: "working" }] }] };
    await render();
    expect(option(t("todos.grouping_direction"))).toBeUndefined();
    expect(option(t("todos.grouping_status"))?.getAttribute("aria-checked")).toBe("true");
    expect(sections()).toHaveLength(0);
  });

  it("falls back to status on a hub without the tree (404), the choice kept", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    tree.current = null;
    await render();
    expect(option(t("todos.grouping_direction"))).toBeUndefined();
    expect(option(t("todos.grouping_status"))?.getAttribute("aria-checked")).toBe("true");
    expect(sections()).toHaveLength(0);
    expect(localStorage.getItem("persona.todos.grouping")).toBe("direction");
  });

  it("prioritizes explicit direction_id only when todos_v2=true and reverts immediately when off (#15653)", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const movedTodos = [
      // Runtime tree lists t-infra under lead-b, but explicit direction_id points to lead-a (Product)
      todo("t-infra", "Infra", { direction_id: "lead-a" }),
      // Unset direction_id falls back to runtime tree (lead-a)
      todo("t-ask", "Ask"),
      // Explicit direction_id activates a previously empty direction (lead-empty)
      todo("t-card", "Card", { direction_id: "lead-empty" }),
      // Explicit direction_id not in directionTree gets a fallback section before secretary
      todo("t-own", "Own", { direction_id: "lead-custom" }),
    ];

    // 1. todos_v2=false (default): 100% legacy runtime directionTree grouping
    seed({
      todos: movedTodos,
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: false },
    });
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);
    expect(sections()[0].textContent).toContain("Infra");
    expect(sections()[1].textContent).not.toContain("Infra");

    // 2. todos_v2=true: explicit direction_id takes precedence immediately
    await act(async () => root.unmount());
    root = createRoot(host);
    seed({
      todos: movedTodos,
      assistant: {
        machine_id: "m1",
        main: "main",
        conversations: [{ machine_id: "m1", session_id: "lead-custom", title: "Custom Stream", cwd: "", status: "idle", updated_at: at }],
        todos_v2: true,
      },
    });
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-empty", "direction:lead-a", "direction:lead-custom"]);
    expect(sections()[0].textContent).toContain("Card");
    expect(sections()[1].textContent).toContain("Ask");
    expect(sections()[1].textContent).toContain("Infra");
    expect(sections()[2].querySelector("button")?.textContent).toContain("Custom Stream");
    expect(sections()[2].textContent).toContain("Own");
  });

  it("keeps legacy 书记官 label and flat Completed fold when todos_v2 is false, and renames to 未分类 with effective_direction_id fallback when todos_v2 is true (#17181)", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const fallbackTree: DirectionTree = {
      directions: [
        { id: "lead-b", name: "Infrastructure", counts: counts(1, 0), running: true, tasks: [{ id: "t-infra", title: "Infra", state: "working" }] },
        { id: "lead-a", name: "Product", counts: counts(1, 0), running: false, tasks: [{ id: "t-prod", title: "Prod", state: "working" }] },
        {
          id: "main",
          name: "",
          secretary: true,
          counts: counts(2, 0),
          running: false,
          tasks: [
            { id: "t-umbrella", title: "Umbrella Goal", state: "working" },
            { id: "t-own", title: "Loose Note", state: "working" },
          ],
        },
      ],
    };
    const items = [
      todo("t-infra", "Infra"),
      todo("t-prod", "Prod"),
      todo("t-umbrella", "Umbrella Goal", { effective_direction_id: "lead-a", effective_direction_name: "Product" }),
      todo("t-own", "Loose Note"),
      todo("t-done-prod", "Shipped Prod", { done_at: at, effective_direction_id: "lead-a", effective_direction_name: "Product" }),
      todo("t-done-loose", "Shipped Loose", { done_at: at }),
    ];
    tree.current = fallbackTree;

    // 1. Legacy todos_v2=false: keeps 书记官, flat Completed fold, zero direction chips or header review buttons.
    seed({
      todos: items,
      todoBoard: null,
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: false },
    });
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);
    expect(sections()[2].querySelector("button")?.textContent).toContain(t("lib-todo-tree.secretary"));
    expect(sections()[2].textContent).toContain("Umbrella Goal");
    expect(host.querySelector("[data-testid=todo-direction-review-done]")).toBeNull();
    const legacyDoneBtn = host.querySelector<HTMLButtonElement>("[data-testid=todos-done-fold]")!;
    await act(async () => legacyDoneBtn.click());
    expect(host.querySelector("[data-testid=todos-done-direction-filters]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-done-direction-group]")).toBeNull();

    // 2. todos_v2=true with serverBoard=null (fallback path): uncategorized is 未分类, umbrella moves to Product.
    await act(async () => root.unmount());
    root = createRoot(host);
    seed({
      todos: items,
      todoBoard: null,
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
    });
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);
    expect(sections()[1].textContent).toContain("Umbrella Goal");
    expect(sections()[2].querySelector("button")?.textContent).toContain(t("todos.direction_uncategorized"));
    expect(sections()[2].querySelector("button")?.textContent).not.toContain(t("lib-todo-tree.secretary"));
    expect(sections()[2].textContent).not.toContain("Umbrella Goal");
    expect(sections()[2].textContent).toContain("Loose Note");

    // 3. todos_v2=true with serverBoard: board ungrouped bucket also uses 未分类.
    await act(async () => root.unmount());
    root = createRoot(host);
    const board: TodoBoardProjection = {
      status_groups: [
        { key: "in_progress", total_count: 3, visible_count: 3, entries: [{ todo_id: "t-infra" }, { todo_id: "t-umbrella" }, { todo_id: "t-own" }] },
        { key: "done", total_count: 2, visible_count: 2, entries: [{ todo_id: "t-done-prod" }, { todo_id: "t-done-loose" }] },
      ],
      direction_groups: [
        { direction_id: "lead-a", label: "Product", counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 1, total: 2 }, todo_ids: ["t-umbrella", "t-done-prod"] },
        { direction_id: "", counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 1, total: 2 }, todo_ids: ["t-own", "t-done-loose"] },
      ],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 3, pending_review: 0, waiting: 0, someday: 0, done: 2, open_total: 3 },
    };
    seed({
      todos: items,
      todoBoard: board,
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
    });
    await render();
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-a", "direction:secretary"]);
    expect(sections()[1].querySelector("button")?.textContent).toContain(t("todos.direction_uncategorized"));
    expect(sections()[1].querySelector("button")?.textContent).not.toContain(t("lib-todo-tree.secretary"));
  });

  it("groups Completed fold by effective direction with filter chips, header jump without folding, and merges paginated closed history under todos_v2=true (#17181)", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const scrollIntoView = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollIntoView;
    const flush = async () => {
      await act(async () => { await vi.dynamicImportSettled(); });
      for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
    };

    const items = [
      todo("t-infra-open", "Infra Open", { direction_id: "lead-b" }),
      todo("t-prod-open", "Prod Open", { direction_id: "lead-a" }),
      todo("t-loose-open", "Loose Open"),
      todo("t-prod-done-1", "Prod Done 1", { done_at: "2026-11-12T00:00:00Z", effective_direction_id: "lead-a", effective_direction_name: "Product" }),
      todo("t-prod-done-2", "Prod Done 2", { done_at: "2026-11-11T00:00:00Z", direction_id: "lead-a" }),
      todo("t-loose-done", "Loose Done", { done_at: "2026-11-10T00:00:00Z" }),
    ];
    const olderDone = todo("t-infra-done-old", "Infra Done Older", {
      done_at: "2026-10-01T00:00:00Z",
      effective_direction_id: "lead-b",
      effective_direction_name: "Infrastructure",
    });

    tree.current = null;

    const board: TodoBoardProjection = {
      status_groups: [
        { key: "in_progress", total_count: 3, visible_count: 3, entries: [{ todo_id: "t-infra-open" }, { todo_id: "t-prod-open" }, { todo_id: "t-loose-open" }] },
        { key: "done", total_count: 4, visible_count: 4, entries: [{ todo_id: "t-prod-done-1" }, { todo_id: "t-prod-done-2" }, { todo_id: "t-loose-done" }, { todo_id: olderDone.id }] },
      ],
      direction_groups: [
        { direction_id: "lead-b", label: "Infrastructure", counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 1, total: 2 }, todo_ids: ["t-infra-open", olderDone.id] },
        { direction_id: "lead-a", label: "Product", counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 2, total: 3 }, todo_ids: ["t-prod-open", "t-prod-done-1", "t-prod-done-2"] },
        { direction_id: "", counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 1, total: 2 }, todo_ids: ["t-loose-open", "t-loose-done"] },
      ],
      sidebar_tree: [],
      counts: { needs_you: 0, in_progress: 3, pending_review: 0, waiting: 0, someday: 0, done: 4, open_total: 3 },
    };

    apiMock.mockImplementation(async (path: string) => {
      if (path === "/todos") {
        return {
          todos: items,
          board,
          closed_cursors: { assistant: { next_cursor: "closed-1", has_more: true } },
        };
      }
      if (path === "/todos?limit=50&cursor=closed-1") {
        return { todos: [olderDone], has_more: false };
      }
      return {};
    });

    reset();
    await refreshTodos();
    seed({
      assistant: {
        machine_id: "m1",
        main: "main",
        leaders: ["lead-b", "lead-a"],
        conversations: [
          { machine_id: "m1", session_id: "lead-b", title: "Infrastructure", cwd: "", status: "idle", updated_at: at },
          { machine_id: "m1", session_id: "lead-a", title: "Product", cwd: "", status: "idle", updated_at: at },
        ],
        todos_v2: true,
      },
    });
    await render();
    await flush();

    // Active direction sections stay focused on open work only.
    expect(sections().some((s) => s.textContent?.includes("Prod Done 1"))).toBe(false);
    expect(sections().some((s) => s.textContent?.includes("Loose Done"))).toBe(false);

    // The hub count makes a direction reviewable before its first completed row has been loaded.
    const [infraSec, prodSec, uncatSec] = sections();
    const infraJump = infraSec.querySelector<HTMLButtonElement>("[data-testid=todo-direction-review-done]")!;
    expect(infraJump.textContent).toContain(t("todos.direction_done_count", { count: 1 }));
    const prodJump = prodSec.querySelector<HTMLButtonElement>("[data-testid=todo-direction-review-done]")!;
    const uncatJump = uncatSec.querySelector<HTMLButtonElement>("[data-testid=todo-direction-review-done]")!;
    expect(prodJump).toBeTruthy();
    expect(prodJump.textContent).toContain(t("todos.direction_done_count", { count: 2 }));
    expect(uncatJump).toBeTruthy();
    expect(uncatJump.textContent).toContain(t("todos.direction_done_count", { count: 1 }));

    // The review stays selected after the cursor page even without a legacy direction tree.
    const prodFoldToggle = prodSec.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(prodFoldToggle.getAttribute("aria-expanded")).toBe("true");
    await act(async () => infraJump.click());
    await flush();

    expect(prodFoldToggle.getAttribute("aria-expanded")).toBe("true");
    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "nearest" });
    expect(apiMock).toHaveBeenCalledWith("/todos?limit=50&cursor=closed-1", expect.anything());

    // Filter chips include All + Infrastructure (merged from paginated older closed history!) + Product + trailing Uncategorized.
    const chips = () => [...host.querySelectorAll<HTMLButtonElement>("[data-testid=todos-done-direction-chip]")];
    expect(chips().map((c) => c.dataset.directionId)).toEqual(["all", "lead-b", "lead-a", "__uncategorized__"]);
    expect(chips().find((c) => c.dataset.directionId === "lead-b")?.getAttribute("aria-pressed")).toBe("true");
    expect(host.querySelector('[data-testid="todos-done-direction-group"][data-direction-id="lead-b"]')?.textContent).toContain("Infra Done Older");
    expect(getState().todoBoard).toEqual(board);
    expect(sections().map((s) => s.dataset.group)).toEqual(["direction:lead-b", "direction:lead-a", "direction:secretary"]);

    await act(async () => prodJump.click());

    // Only Product completed items are visible while filtered to Product.
    const filteredGroups = [...host.querySelectorAll<HTMLElement>("[data-testid=todos-done-direction-group]")];
    expect(filteredGroups).toHaveLength(1);
    expect(filteredGroups[0].dataset.directionId).toBe("lead-a");
    expect(filteredGroups[0].textContent).toContain("Prod Done 1");
    expect(filteredGroups[0].textContent).toContain("Prod Done 2");
    expect(filteredGroups[0].textContent).not.toContain("Loose Done");
    expect(filteredGroups[0].textContent).not.toContain("Infra Done Older");

    // Clicking the active chip toggles back to All and renders direction sub-groups with Uncategorized last.
    await act(async () => chips().find((c) => c.dataset.directionId === "lead-a")!.click());
    await flush();
    expect(chips().find((c) => c.dataset.directionId === "all")?.getAttribute("aria-pressed")).toBe("true");
    const allGroups = [...host.querySelectorAll<HTMLElement>("[data-testid=todos-done-direction-group]")];
    expect(allGroups.map((g) => g.dataset.directionId)).toEqual(["lead-b", "lead-a", "__uncategorized__"]);
    expect(allGroups[0].textContent).toContain("Infra Done Older");
    expect(allGroups[1].textContent).toContain("Prod Done 1");
    expect(allGroups[2].textContent).toContain(t("todos.direction_uncategorized"));
    expect(allGroups[2].textContent).toContain("Loose Done");

    // Clicking Uncategorized header jump filters cleanly to "__uncategorized__".
    const uncatJumpAfter = sections().find((s) => s.dataset.group === "direction:secretary")!.querySelector<HTMLButtonElement>("[data-testid=todo-direction-review-done]")!;
    await act(async () => uncatJumpAfter.click());
    await flush();
    expect(chips().find((c) => c.dataset.directionId === "__uncategorized__")?.getAttribute("aria-pressed")).toBe("true");
    const uncatGroups = [...host.querySelectorAll<HTMLElement>("[data-testid=todos-done-direction-group]")];
    expect(uncatGroups).toHaveLength(1);
    expect(uncatGroups[0].dataset.directionId).toBe("__uncategorized__");
    expect(uncatGroups[0].textContent).toContain("Loose Done");
    expect(uncatGroups[0].textContent).not.toContain("Prod Done 1");
    reset();
  });

  it("orders open subtasks first and collapses completed subtasks cleanly in TodoDetail under todos_v2=true (#17181)", async () => {
    const flush = async () => {
      await act(async () => { await vi.dynamicImportSettled(); });
      for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
    };

    const items = [
      // Goal A: 1 open + 2 done -> completed collapsed by default
      todo("goal-mixed", "Mixed Goal"),
      todo("mixed-done-1", "Mixed Done 1", { part_of: "goal-mixed", done_at: at }),
      todo("mixed-open-1", "Mixed Open 1", { part_of: "goal-mixed" }),
      todo("mixed-done-2", "Mixed Done 2", { part_of: "goal-mixed", done_at: at }),
      // Goal B: 0 open + 2 done (<= 3) -> completed expanded by default
      todo("goal-small-done", "Small All-Done Goal"),
      todo("small-done-1", "Small Done 1", { part_of: "goal-small-done", done_at: at }),
      todo("small-done-2", "Small Done 2", { part_of: "goal-small-done", done_at: at }),
      // Goal C: 0 open + 4 done (> 3) -> completed collapsed by default
      todo("goal-large-done", "Large All-Done Goal"),
      todo("large-done-1", "Large Done 1", { part_of: "goal-large-done", done_at: at }),
      todo("large-done-2", "Large Done 2", { part_of: "goal-large-done", done_at: at }),
      todo("large-done-3", "Large Done 3", { part_of: "goal-large-done", done_at: at }),
      todo("large-done-4", "Large Done 4", { part_of: "goal-large-done", done_at: at }),
    ];

    seed({
      todos: items,
      todoBoard: null,
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
    });

    // 1. Mixed goal: open subtask shown at top; 2 done subtasks collapsed behind toggle.
    await act(async () => root.render(<TodosView selectedId="goal-mixed" />));
    await flush();
    const visibleIds = () =>
      [...host.querySelectorAll<HTMLElement>("[data-testid=todo-detail-subtask-item]")].map((el) => el.dataset.subtaskId);
    const doneToggle = () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-subtasks-done-toggle]");

    expect(visibleIds()).toEqual(["mixed-open-1"]);
    expect(doneToggle()?.getAttribute("aria-expanded")).toBe("false");
    expect(doneToggle()?.textContent).toContain(t("todos.detail_subtasks_done_toggle", { count: 2 }));

    // Expanding shows completed subtasks after open subtasks.
    await act(async () => doneToggle()!.click());
    await flush();
    expect(doneToggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(visibleIds()).toEqual(["mixed-open-1", "mixed-done-1", "mixed-done-2"]);

    // 2. Switching to large all-done goal (>3 done) resets disclosure state to collapsed.
    await act(async () => root.render(<TodosView selectedId="goal-large-done" />));
    await flush();
    expect(doneToggle()?.getAttribute("aria-expanded")).toBe("false");
    expect(visibleIds()).toEqual([]);
    await act(async () => doneToggle()!.click());
    await flush();
    expect(doneToggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(visibleIds()).toEqual(["large-done-1", "large-done-2", "large-done-3", "large-done-4"]);

    // 3. Small all-done goal (<= 3 done, 0 open) starts expanded by default.
    await act(async () => root.render(<TodosView selectedId="goal-small-done" />));
    await flush();
    expect(doneToggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(visibleIds()).toEqual(["small-done-1", "small-done-2"]);
  });
});
