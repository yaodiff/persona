// @vitest-environment jsdom
// Exercise the real side panel, chat header and menu: a task is an entry point, not the destination of its Agent chat.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

import { t } from "../../i18n";
import { closePanel, openPanel, type Panel } from "../../lib/panel";
import { href } from "../../lib/router";
import { applyCompleteSessions, refreshTodos, reset } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import type { Session } from "../../lib/types";
import { SidePanel } from "../SidePanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
const first: Session = { machine_id: "example-computer", session_id: "example-agent", title: "Example Agent", cwd: "/example", state: "working", status: "working", todo: { id: "example-task", title: "Example task" } } as Session;
const second: Session = { ...first, machine_id: "example-other", session_id: "other-agent", title: "Other Agent" };
const task = (sessions = [first]): Todo => ({
  id: "example-task", title: "Example task", origin: "example-conversation", created_at: at, updated_at: at,
  agents: sessions.map((s) => ({ machine_id: s.machine_id, session_id: s.session_id, title: s.title, linked_at: at })),
});
let servedTodo: Todo;
let host: HTMLDivElement;
let root: Root;
let newWindow: MockInstance<typeof window.open>;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

// SidePanel loads the task and Agent panels on first use: loaded here once, so each test's lazy panel resolves at once
beforeAll(async () => {
  await import("./TaskPanel");
}, 60_000);

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(String(url).includes("/todos") ? { todos: [servedTodo] } : String(url).includes("/items") ? { items: [], to_seq: 0 } : {}), { headers: { "Content-Type": "application/json" } })));
  newWindow = vi.spyOn(window, "open").mockReturnValue(null);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  closePanel();
  reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function open(panel: Panel, todo = task(), sessions = [first]) {
  servedTodo = todo;
  await act(async () => {
    applyCompleteSessions(sessions);
    await refreshTodos();
  });
  act(() => {
    openPanel(panel);
    root.render(<StrictMode><SidePanel /></StrictMode>);
  });
  // the task and Agent panels load with the first one opened (SidePanel's lazy chunk): React.lazy suspends once on its first
  // render even when the module is in, as long as the machine is slow. Each poll runs in act so React's retry can flush.
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
    expect(host.querySelector("aside header")).not.toBeNull();
  }, { timeout: 5000, interval: 20 });
  for (let i = 0; i < 6; i++) await settle();
  expect(host.querySelector("aside header")).not.toBeNull();
}
async function clickMore() {
  const button = host.querySelector<HTMLButtonElement>(`aside header button[aria-label="${t("chat-chatview.more_actions")}"]`)
    ?? host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("assistant-taskpanel.more")}"]`);
  expect(button).not.toBeNull();
  act(() => button!.click());
  await settle();
}
async function clickMenu(label: string) {
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemradio"]')].find((el) => [...el.querySelectorAll("span")].some((span) => span.textContent?.trim() === label));
  expect(item, label).toBeDefined();
  act(() => item!.click());
  await settle();
}
async function expectSessionWindow(s = first) {
  await clickMore();
  await clickMenu(t("assistant-taskpanel.open_in_new_window"));
  expect(newWindow).toHaveBeenLastCalledWith(href({ name: "session", mid: s.machine_id, sid: s.session_id }), "_blank", "noopener");
  expect(newWindow.mock.calls.at(-1)?.[0]).not.toContain("/todos");
}

describe("Agent panel new-window destination", () => {
  it("opens the displayed Agent session from a task card or task-backed In progress tray entry", async () => {
    await open({ kind: "task", todo: "example-task" });
    expect(host.querySelector("aside header h1")?.textContent).toBe("Example task");
    await expectSessionWindow();
  });

  it("opens a directly opened Agent session, even when that Agent is linked to a task", async () => {
    await open({ kind: "agent", mid: first.machine_id, sid: first.session_id });
    expect(host.querySelector("aside header h1")?.textContent).toBe(first.title);
    await expectSessionWindow();
  });

  it("follows the Agent selected in a multi-Agent task, including its machine", async () => {
    await open({ kind: "task", todo: "example-task" }, task([first, second]), [first, second]);
    await expectSessionWindow();
    act(() => host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("assistant-taskpanel.switch_agent")}"]`)!.click());
    await settle();
    await clickMenu(second.title);
    await expectSessionWindow(second);
  });

  it("uses linked Agent identity before the session list arrives", async () => {
    await open({ kind: "task", todo: "example-task" }, task(), []);
    await expectSessionWindow();
  });

  it("opens the task only while the panel has no unreleased Agent to show", async () => {
    await open({ kind: "task", todo: "example-task" }, task([]), []);
    await clickMore();
    await clickMenu(t("assistant-taskpanel.open_in_new_window"));
    expect(newWindow).toHaveBeenCalledExactlyOnceWith(href({ name: "todos", id: "example-task" }), "_blank", "noopener");
  });

  it("keeps the separate linked-task menu item pointing to the task", async () => {
    const before = location.href;
    await open({ kind: "task", todo: "example-task" });
    try {
      await clickMore();
      await clickMenu(t("chat-chatview.todo_label", { title: "Example task" }));
      expect(location.pathname + location.search).toBe("/todos?id=example-task");
      expect(newWindow).not.toHaveBeenCalled();
    } finally {
      history.replaceState({}, "", before);
    }
  });
});
