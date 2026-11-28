// @vitest-environment jsdom
// The sidebar's To-dos badge on the To-dos page (#11838): every row the badge counts carries its mark, and no other row
// does, so with nothing filtered and every fold open the marked rows are the badge's number. One hook gives both
// (lib/todo-owner-needs.ts): a to-do with no Agent on it filed under Other open is marked; a quiet task with a working
// Agent, a goal its pieces carry and a task whose Agent the page knows only from the list's task rows are neither marked
// nor counted. A folded "N related", section, folder or pinned direction says how many under it are counted. The hub is
// faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
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

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { useNavPages } = await import("./SidebarParts");
const { initI18n, t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const HOUR = 3_600_000;
const link = (sid: string) => ({ machine_id: "mac", session_id: sid, linked_at: iso(2 * HOUR) });
const agent = (sid: string, state: Session["state"]): Session =>
  ({ machine_id: "mac", session_id: sid, title: `agent ${sid}`, cwd: "/code/a", status: state === "working" ? "working" : "idle", state, updated_at: iso(HOUR) }) as Session;
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({ id, title, created_at: iso(5 * HOUR), updated_at: iso(HOUR), ...patch }) as Todo;

const todos: Todo[] = [
  // the owner's own, no Agent ever on it: Other open, and the badge counts it
  todo("errand", "Replace the disk in the home server"),
  // a task whose Agent never started: Needs you, counted
  todo("failed", "Rotate the keys", { origin: "conv", done_at: iso(HOUR), start_error: "machine offline", agents: [] }),
  // a quiet task with its Agent at work: Working, not counted
  todo("quiet", "Tidy the logs", { origin: "conv", quiet: true, agents: [link("q")] }),
  // a goal of the owner's whose piece an Agent is doing: Other open and Working, neither counted
  todo("goal", "Ship the settings page"),
  todo("piece", "Settings page: search", { origin: "conv", part_of: "goal", agents: [link("p")] }),
  // its Agent is outside the Agents list's window, known from the list's task rows: Working, not counted
  todo("far", "Check the backups", { origin: "conv", agents: [link("far")] }),
  // one row of two related to-dos (the same issue): the lead's Agent works, the other has none and is counted
  todo("fix-a", "Fix the login page #42", { origin: "conv", agents: [link("f")] }),
  todo("fix-b", "Follow up on #42 with the vendor"),
];
const sessions = { mac: [agent("q", "working"), agent("p", "working"), agent("f", "working")] };
const taskRows = { [JSON.stringify(["mac", "far"])]: agent("far", "working") };
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

/** The sidebar's To-dos badge as useNavPages gives it to every place that draws it. */
function Badge() {
  const { pages } = useNavPages();
  const page = pages.find((p) => p.k === "todos");
  return <output data-testid="badge">{String(page?.badge ?? 0)}</output>;
}

beforeEach(() => {
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
  seed(before);
  await initI18n("zh-Hans");
});

const render = async (grouping: "status" | "folder" = "status") => {
  localStorage.setItem("persona.todos.grouping", grouping);
  seed({ todos, todosLoaded: true, todosSupported: true, sessions, taskRows, interactions: [], answered: {} });
  await act(async () => root.render(<><Badge /><TodosView /></>));
  await flush();
};
const badge = () => Number(host.querySelector("[data-testid=badge]")?.textContent);
const marked = () => [...host.querySelectorAll("[data-testid=todo-owner-mark]")].map((m) => m.closest<HTMLElement>("[data-todo]")!.dataset.todo).sort();
const row = (id: string) => host.querySelector<HTMLElement>(`[data-todo="${id}"]`)!;
const sectionOf = (id: string) => row(id).closest<HTMLElement>("[data-testid=todo-section]")?.dataset.section;

describe("the badge's mark on the To-dos page (#11838)", () => {
  it("marks every row the badge counts and no other, as many as the badge says", async () => {
    await render();
    // the lead's related to-do is folded under "1 related", which says one under it is counted
    expect(badge()).toBe(3);
    expect(marked()).toEqual(["errand", "failed"]);
    const related = row("fix-a").closest("[data-testid=todo-triage-row]")!.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(related.querySelector("[data-testid=todo-owner-count]")?.getAttribute("data-count")).toBe("1");
    await act(async () => related.click());
    await flush();
    expect(marked()).toEqual(["errand", "failed", "fix-b"]);
    expect(marked()).toHaveLength(badge());
    expect(related.querySelector("[data-testid=todo-owner-count]")).toBeNull();
  });

  it("marks a to-do with no Agent on it under Other open, and says why", async () => {
    await render();
    expect(sectionOf("errand")).toBe("other");
    const mark = row("errand").querySelector<HTMLElement>("[data-testid=todo-owner-mark]")!;
    expect(mark.dataset.need).toBe("unattended");
    const why = t("todos.owner_why_unattended");
    expect(mark.textContent).toBe(t("todos.owner_mark") + t("todos.owner_mark_said", { why }));
    expect(mark.title).toBe(t("todos.owner_mark_title", { why }));
    expect(row("failed").querySelector<HTMLElement>("[data-testid=todo-owner-mark]")?.dataset.need).toBe("failed_start");
  });

  it("leaves out a quiet task at work, a goal its piece carries and an Agent known from the list's task rows", async () => {
    await render();
    expect(sectionOf("quiet")).toBe("working");
    expect(sectionOf("goal")).toBe("other");
    // the page reads the task rows, and so does the badge: Working, neither counted nor marked
    expect(sectionOf("far")).toBe("working");
    for (const id of ["quiet", "goal", "piece", "far", "fix-a"]) expect(row(id).querySelector("[data-testid=todo-owner-mark]")).toBeNull();
    expect(badge()).toBe(3);
  });

  it("counts them on a folded section's heading", async () => {
    await render();
    const other = host.querySelector<HTMLElement>("[data-testid=todo-section][data-section=other]")!;
    const heading = other.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(heading.querySelector("[data-testid=todo-owner-count]")).toBeNull();
    await act(async () => heading.click());
    await flush();
    // folded: the errand is out of sight, its heading says one under it is counted
    expect(other.querySelector("[data-todo=errand]")).toBeNull();
    expect(heading.querySelector("[data-testid=todo-owner-count]")?.textContent).toBe(t("todos.owner_mark_count", { count: 1 }));
  });

  it("marks the same rows by folder, and counts them on a folded folder's title", async () => {
    await render("folder");
    expect(marked()).toEqual(["errand", "failed", "fix-b"]);
    expect(marked()).toHaveLength(badge());
    const folder = row("errand").closest<HTMLElement>("[data-testid=todo-group]")!;
    const title = folder.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    const under = folder.querySelectorAll("[data-testid=todo-owner-mark]").length;
    expect(title.querySelector("[data-testid=todo-owner-count]")).toBeNull();
    await act(async () => title.click());
    await flush();
    expect(folder.querySelector("[data-todo=errand]")).toBeNull();
    expect(title.querySelector("[data-testid=todo-owner-count]")?.getAttribute("data-count")).toBe(String(under));
  });

  it.each([
    { lang: "en", mark: "Up to you", title: "No Agent is working on it. Counted in the sidebar's To-dos badge.", count: "2 up to you" },
    { lang: "zh-Hans", mark: "你来处理", title: "当前没有 Agent 在处理此事项。会计入侧栏「待办」的角标计数。", count: "你来处理 2 项" },
    { lang: "ja", mark: "あなたの対応", title: "どのエージェントも作業していません。サイドバーの「ToDo」のバッジに数えられています。", count: "あなたの対応 2件" },
  ] as const)("says it in $lang", async ({ lang, mark, title, count }) => {
    await initI18n(lang);
    await render();
    const el = row("errand").querySelector<HTMLElement>("[data-testid=todo-owner-mark]")!;
    expect(el.textContent?.startsWith(mark)).toBe(true);
    expect(el.title).toBe(title);
    expect(t("todos.owner_mark_count", { count: 2 })).toBe(count);
  });
});
