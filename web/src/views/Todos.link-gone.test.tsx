// @vitest-environment jsdom
// A link to a to-do the list doesn't have (deleted, or an old link; web usability audit item 42): the page says so once
// and the address loses the id, instead of showing the first to-do under it. Deleting the open to-do here is not such
// a link: it says only that it was deleted. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
// the roster as the hub gives it when a test has several assistants (the page reads it again)
const roster = vi.hoisted(() => ({ list: null as unknown }));
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api: (path: string, init?: unknown) => api(path, init) }));
const toast = vi.fn();
vi.mock("../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");
const { useRoute } = await import("../lib/router");
const { chooseManager, resetManagerChoice } = await import("../lib/manager-choice");
const { setRoster } = await import("../lib/managers");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000) });
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
const gone = () => toast.mock.calls.filter((c) => c[0] === t("not-found.todo_gone"));

// as App draws it: the page follows the address
function Page() {
  const route = useRoute();
  return route.name === "todos" ? <TodosView selectedId={route.id} view={route.view} /> : null;
}

beforeEach(() => {
  api.mockReset();
  api.mockImplementation(async (path: string) => (path.endsWith("/launchers") ? { launchers: [] } : path === "/managers" && roster.list ? roster.list : {}));
  toast.mockReset();
  seed({ todos: [todo("t1", "Release checklist"), todo("t2", "deploy web")], todosLoaded: true, todosError: null, todosSupported: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  roster.list = null;
  setRoster(null);
  resetManagerChoice();
  seed(before);
  history.replaceState(null, "", "/");
});

describe("a link to a to-do that isn't there", () => {
  it("says so once and drops the id from the address, not showing another to-do under it", async () => {
    history.replaceState(null, "", "/todos?id=nope");
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(1);
    expect(location.pathname + location.search).toBe("/todos");
    await flush();
    expect(gone()).toHaveLength(1);
  });

  it("waits for the list: nothing is said while it loads, or when it couldn't be read", async () => {
    seed({ todosLoaded: false });
    history.replaceState(null, "", "/todos?id=nope");
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(0);
    seed({ todosLoaded: true, todosError: "hub down" });
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(0);
    expect(location.search).toBe("?id=nope");
  });

  it("keeps the link on a hub without to-dos, whose list is empty", async () => {
    seed({ todos: [], todosSupported: false });
    history.replaceState(null, "", "/todos?id=t9");
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(0);
    expect(location.search).toBe("?id=t9");
  });

  it("keeps the link while the current assistant is offline, its to-dos left out of the list, and says it once it is back", async () => {
    const list = (homeOnline: boolean) => ({
      default: "assistant",
      managers: [
        { id: "assistant", name: "Assistant", online: true },
        { id: "m-home", name: "Home", online: homeOnline, last_seen: iso(3 * 3_600_000) },
      ],
    });
    roster.list = list(false);
    setRoster(list(false));
    act(() => chooseManager("m-home"));
    history.replaceState(null, "", "/todos?id=t9");
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(0);
    expect(location.search).toBe("?id=t9");
    // back online and still without it: now it is gone
    roster.list = list(true);
    await act(async () => setRoster(list(true)));
    await flush();
    expect(gone()).toHaveLength(1);
    expect(location.search).toBe("");
  });

  it("is not what deleting the open to-do here says", async () => {
    const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
    const click = async (el: Element | undefined | null) => {
      await act(async () => (el as HTMLElement).click());
      await flush();
    };
    history.replaceState(null, "", "/todos?id=t1");
    await act(async () => root.render(<Page />));
    await flush();
    await click(button("更多选项"));
    await click(document.querySelector("[role=menuitem]"));
    await click(button("删除", document.querySelector("[role=dialog]")!));
    expect(api.mock.calls.some(([, init]) => (init as { method?: string } | undefined)?.method === "DELETE")).toBe(true);
    expect(toast.mock.calls.map((c) => c[0])).toContain(t("todos.deleted"));
    expect(gone()).toHaveLength(0);
  });

  it("opens one that is there as before", async () => {
    history.replaceState(null, "", "/todos?id=t2");
    await act(async () => root.render(<Page />));
    await flush();
    expect(gone()).toHaveLength(0);
    expect(location.search).toBe("?id=t2");
  });
});
