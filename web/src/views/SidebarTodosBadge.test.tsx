// @vitest-environment jsdom
// The sidebar's To-dos badge (#11085): its number is the to-dos that need the owner (lib/todo-triage ownerBadge), and
// what it counts is said with it, in the row's tooltip and in what a screen reader hears for the row, the rail's icon,
// the phone's grid and a folded Pages title, in en, zh-Hans and ja. A goal whose pieces Agents are doing shows no
// badge at all, nor a task whose Agent the To-dos page knows from the list's task rows (#11838). The data is made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { getState, reset } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";
import { initI18n, t } from "../i18n";
import { NavGrid, Sidebar, SidebarRail, useNavPages } from "./Sidebar";
import { PagesTally } from "./SidebarRail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const at = new Date(Date.now() - 3_600_000).toISOString();
const link = (sid: string) => ({ machine_id: "mac", session_id: sid, linked_at: at });
const agent = (sid: string, state: Session["state"]): Session => ({ machine_id: "mac", session_id: sid, title: `agent ${sid}`, cwd: "/code/a", status: state === "working" ? "working" : "idle", state, updated_at: at });
/** A goal of the owner's whose two pieces Agents are doing, and the owner's own errand: one to-do needs them. */
const todos: Todo[] = [
  { id: "goal", title: "Plan the launch", created_at: at, updated_at: at },
  { id: "p1", title: "Draft the notes", part_of: "goal", origin: "conv", created_at: at, updated_at: at, agents: [link("a")] },
  { id: "p2", title: "Check the build", part_of: "goal", origin: "conv", created_at: at, updated_at: at, agents: [link("b")] },
  { id: "own", title: "Buy a cable", created_at: at, updated_at: at },
];
const seed = (list: Todo[]) => Object.assign(getState() as unknown as Record<string, unknown>, {
  todos: list,
  todosLoaded: true,
  sessions: { mac: [agent("a", "working"), agent("b", "working")] },
});
const todosLink = () => host.querySelector<HTMLAnchorElement>('a[href="/todos"]')!;

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { headers: { "Content-Type": "application/json" } }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

function DrawerPages() {
  const { pages } = useNavPages();
  return <NavGrid pages={pages} activeKey="home" go={() => () => {}} />;
}
function FoldedTally() {
  const { pages } = useNavPages();
  return <PagesTally pages={pages} />;
}

describe("the sidebar's To-dos badge (#11085)", () => {
  it.each([
    { lang: "zh-Hans", page: "待办", said: "有 1 个待办需要你处理" },
    { lang: "en", page: "To-dos", said: "1 to-do needs you" },
    { lang: "ja", page: "ToDo", said: "あなたの対応が必要なToDo 1件" },
  ] as const)("counts only the to-do that needs the owner and says what it counts in $lang", async ({ lang, page, said }) => {
    await initI18n(lang);
    expect(t("sidebar.todos_badge_title", { count: 1 })).toBe(said);
    seed(todos);
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    // the row: the digit drawn, the sentence spoken, the sentence as its tooltip
    const row = todosLink();
    expect(row.title).toBe(said);
    const spoken = row.querySelector(".sr-only")!;
    expect(spoken.textContent).toBe(said);
    expect(spoken.previousElementSibling?.getAttribute("aria-hidden")).toBe("true");
    expect(spoken.previousElementSibling?.textContent).toBe("1");
    expect(row.textContent).toBe(`${page}1${said}`);

    act(() => root.render(<SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(todosLink().getAttribute("aria-label")).toBe(`${page} ${said}`);

    act(() => root.render(<DrawerPages />));
    await flush();
    expect(todosLink().getAttribute("aria-label")).toBe(`${page} ${said}`);

    act(() => root.render(<FoldedTally />));
    await flush();
    expect(host.querySelector(".sr-only")?.textContent).toBe(`${page} ${said}`);
  });

  it("reads a task's Agent as the To-dos page does, from the list's task rows too (#11838)", async () => {
    // its Agent works outside the Agents list's window: the page files it under Working, so the badge leaves it out
    seed([{ id: "far", title: "Check the backups", origin: "conv", created_at: at, updated_at: at, agents: [link("far")] }]);
    Object.assign(getState() as unknown as Record<string, unknown>, { sessions: {}, taskRows: { [JSON.stringify(["mac", "far"])]: agent("far", "working") } });
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(todosLink().textContent).toBe("待办");
  });

  it("shows no badge for a goal whose pieces Agents are doing", async () => {
    seed(todos.filter((td) => td.id !== "own"));
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const row = todosLink();
    expect(row.textContent).toBe("待办");
    expect(row.title).toBe("");
  });
});
