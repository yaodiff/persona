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
import type { Todo } from "../lib/todos";

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
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: async () => ({}),
}));

const { getState } = await import("../lib/store");
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
  tree.current = TREE;
  seed({ todos: TODOS, todosLoaded: true, todosError: null, todosSupported: true, machines: [], sessions: {} });
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

  it("opens a row's menu on a right click, as the other groupings do", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    await render();
    const row = [...sections()[1].querySelectorAll<HTMLElement>("[role=button]")].find((r) => r.textContent?.includes("Card"));
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

  it("puts a to-do closed in the last day and an inactive one under their status sections below the directions, counted as the header says", async () => {
    localStorage.setItem("persona.todos.grouping", "direction");
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
    const daysAgo = new Date(Date.now() - 3 * 86_400_000).toISOString();
    // closed an hour ago (Just finished, not open), and a handed-out task with nothing for three days (Shelved): the
    // hub's tree lists neither, as neither counts as open
    const fresh = todo("t-fresh", "Fresh", { done_at: hourAgo, updated_at: hourAgo });
    const quiet = todo("t-quiet", "Quiet", { origin: "conv", created_at: daysAgo, updated_at: daysAgo });
    const header = t("todos.summary_triage", { open: 4, parked: 0, shelved: 1, done: 1 });
    for (const listed of [false, true]) {
      // and a tree whose push has not caught up yet still lists the one just ticked
      tree.current = listed ? { directions: TREE.directions.map((d) => (d.id === "lead-b" ? { ...d, tasks: [...(d.tasks ?? []), { id: "t-fresh", title: "Fresh", state: "working" }] } : d)) } : TREE;
      seed({ todos: [...TODOS, fresh, quiet] });
      await act(async () => root.unmount());
      root = createRoot(host);
      await render();
      expect(sections().some((s) => s.textContent?.includes("Fresh"))).toBe(false);
      const justDone = host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=just_done]");
      expect(justDone?.textContent).toContain("Fresh");
      expect(justDone?.getAttribute("data-todos")).toBe("1");
      const shelved = host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=shelved]");
      expect(shelved?.getAttribute("data-todos")).toBe("1");
      // neither is "an open to-do no direction lists"
      expect(host.querySelector('[data-testid="todos-direction-unlisted"]')).toBeNull();
      expect(host.querySelector("[data-testid=todos-summary]")?.textContent).toBe(header);
      expect(host.textContent).toContain(t("todos.done_section", { count: 1 }));
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
});
