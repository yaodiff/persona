// @vitest-environment jsdom
// An Agent opened from a to-do's page is a drill-down (#12055, the rule of #9971): its Agent rows and Hand to Agent's
// existing Agent open the Agent's page on top of the to-do's, and the new entry names the to-do's address, so the page
// offers "Came from … · click to return" and the sidebar keeps To-dos lit (lib/handoff.ts openFromPage). The hub is
// faked; the data made up.
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

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState } = await import("../lib/store");
const { handoffFrom } = await import("../lib/handoff");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...document.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo: Todo = { id: "t1", title: "Tidy the release checklist", created_at: iso(7_200_000), updated_at: iso(3_600_000) };
const session = (sid: string, title: string) => ({ machine_id: "m1", session_id: sid, title, status: "idle", state: "idle" }) as Session;

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(() => {
  api.mockReset();
  // the hub still lists the to-do after the hand-off (refreshTodos), so the page stays on it
  api.mockImplementation(async (path: string) => (path.endsWith("/launchers") ? { launchers: [] } : path === "/todos" ? { todos: getState().todos } : {}));
  seed({
    todos: [todo],
    todosLoaded: true,
    machines: [{ id: "m1", name: "Example computer", online: true }],
    sessions: { m1: [session("s1", "First Agent"), session("s2", "Second Agent")] },
  });
  history.replaceState(null, "", "/todos?id=t1");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

describe("an Agent opened from a to-do's page (#12055)", () => {
  it("opens from the to-do's Agent rows with the to-do named as where it came from", async () => {
    seed({ todos: [{ ...todo, agents: [{ machine_id: "m1", session_id: "s1", linked_at: iso(1000) }] }] });
    await act(async () => root.render(<TodosView selectedId="t1" />));
    await flush();
    const row = [...document.querySelectorAll<HTMLButtonElement>("[data-testid=todo-agent-open]")].find((el) => el.textContent?.includes("First Agent"));
    expect(row?.tagName).toBe("BUTTON");
    expect(row?.querySelector("button")).toBeNull();
    const depth = history.length;
    await click(row);
    expect(location.pathname).toBe("/s/m1/s1");
    expect(history.length).toBe(depth + 1);
    expect(handoffFrom()).toEqual({ path: "/todos?id=t1" });
  });

  it("opens the existing Agent the to-do was handed to the same way", async () => {
    await act(async () => root.render(<TodosView selectedId="t1" />));
    await flush();
    await click(button("交给 Agent"));
    await click(button("已有 Agent"));
    const send = [...dialog().querySelectorAll<HTMLButtonElement>("button:not([role=radio])")].find((b) => b.textContent?.includes("发送") && !b.disabled);
    await click(send);
    expect(api).toHaveBeenCalledWith("/sessions/m1/s1/messages", expect.objectContaining({ method: "POST" }));
    expect(location.pathname).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/todos?id=t1" });
  });
});
