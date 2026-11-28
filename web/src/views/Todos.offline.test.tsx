// @vitest-environment jsdom
// An offline current assistant's to-dos (docs/assistant/managers.md "Clients"): the hub's merged list leaves out a manager
// that doesn't answer, so the page says it is offline instead of "No to-dos" with New To-Do, in the list and the
// calendar; with one assistant, or an online one, the empty page is as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
// the roster as the hub gives it (the page reads it again); anything else answers nothing
const roster = vi.hoisted(() => ({ list: null as unknown }));
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api: vi.fn(async (path: string) => (path === "/managers" ? roster.list : {})) }));

const { getState } = await import("../lib/store");
const { chooseManager, resetManagerChoice } = await import("../lib/manager-choice");
const { setRoster } = await import("../lib/managers");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const before = { ...getState() };
const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true },
    { id: "m-work", name: "工作", online: true },
    { id: "m-home", name: "家里", online: false, last_seen: new Date(Date.now() - 3 * 3_600_000).toISOString() },
  ],
};
const mine: Todo = { id: "t-default", title: "Release checklist", manager: "assistant", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" } as Todo;
let host: HTMLDivElement, root: Root;
const render = async (view?: "calendar") => { await act(async () => root.render(<TodosView view={view} />)); };
const offlineState = () => host.querySelector<HTMLElement>("[data-assistant-offline]");

beforeEach(async () => {
  await initI18n("zh-Hans");
  resetManagerChoice();
  roster.list = ROSTER;
  setRoster(ROSTER);
  Object.assign(getState(), { todos: [mine], todosLoaded: true, todosError: null, todosSupported: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  setRoster(null);
  resetManagerChoice();
  Object.assign(getState(), before);
});

it("says the current assistant is offline in place of the empty list", async () => {
  act(() => chooseManager("m-home"));
  await render();
  expect(offlineState()?.dataset.assistantOffline).toBe("m-home");
  expect(offlineState()?.textContent).toContain("家里 已离线");
  expect(offlineState()?.textContent).toContain("助手恢复在线后页面将自动更新。");
  expect(host.textContent).not.toContain(t("todos.empty_title"));
});

it("says so in place of the calendar too", async () => {
  act(() => chooseManager("m-home"));
  await render("calendar");
  expect(offlineState()?.dataset.assistantOffline).toBe("m-home");
});

it("shows an online assistant's empty list as before", async () => {
  act(() => chooseManager("m-work"));
  await render();
  expect(offlineState()).toBeNull();
  expect(host.textContent).toContain(t("todos.empty_title"));
});

it("shows nothing of it with one assistant", async () => {
  roster.list = { default: "assistant", managers: [{ id: "assistant", name: "助手", online: false }] };
  setRoster(roster.list as typeof ROSTER);
  Object.assign(getState(), { todos: [] });
  await render();
  expect(offlineState()).toBeNull();
  expect(host.textContent).toContain(t("todos.empty_title"));
});
