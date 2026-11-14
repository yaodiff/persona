// @vitest-environment jsdom
// #3713: a to-do whose agent never started (the hub closes it, with its start error) waits on the owner in Needs you. Its
// row reads open, not done, and says why in red; ticking it closes it for good (`done: true`, the hub clears the start
// error). One whose work finished in a related to-do (the same plan) is history: Just finished, ticked, nothing asked.
// Its page agrees with its row (audit item 13): why it did not start, Retry (from its plan) or Hand to agent, which
// reopens it only once handed over, and Complete closes it for good as the checkbox does; not Reopen. Its Retry is the
// main chat's task card's (lib/feed retryTask): one on its way at a time from either, the reopened to-do shown at once.
// The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

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
const launched: Record<string, unknown>[] = [];
vi.mock("../lib/new-agent", async (orig) => ({
  ...(await orig<typeof import("../lib/new-agent")>()),
  launchAgent: (input: Record<string, unknown>) => {
    launched.push(input);
    return "local-1";
  },
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const failed = (id: string, title: string, plan?: string, closedAgo = 3_600_000): Todo =>
  ({ id, title, plan_id: plan, origin: "conv", created_at: iso(closedAgo + 3_600_000), updated_at: iso(closedAgo), done_at: iso(closedAgo), start_error: "machine offline", agents: [] }) as Todo;
const finished: Todo = { id: "done", title: "Verify the panel", plan_id: "p", origin: "conv", created_at: iso(7_200_000), updated_at: iso(1_800_000), done_at: iso(1_800_000), done_by: "review", agents: [] } as Todo;

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(async () => {
  api.mockReset();
  api.mockImplementation(async () => ({}));
  launched.length = 0;
  seed({
    todos: [failed("waits", "Rotate the keys"), failed("gone", "Deploy the panel", "p"), finished, failed("again", "Renew the certificate", "p2"), failed("old", "Clean the build cache", undefined, 3 * 86_400_000)],
    todosLoaded: true, sessions: {}, interactions: [], answered: {}, machines: [{ id: "m1", name: "Example box", online: true }],
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<TodosView />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
});

const section = (name: string) => host.querySelector(`[data-testid=todo-section][data-section=${name}]`);
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const calls = (method: string, path: string) => api.mock.calls.filter(([p, init]) => p === path && (init as { method?: string })?.method === method).map(([, init]) => (init as { json?: unknown }).json);
const show = async (id: string) => {
  await act(async () => root.render(<TodosView selectedId={id} />));
  await flush();
};
const row = (id: string) => host.querySelector<HTMLElement>(`[data-testid=todo-triage-row][data-id=${id}]`);

describe("a to-do whose agent never started", () => {
  it("waits in Needs you reading open, says why in red, and ticking it closes it", async () => {
    const waits = row("waits")!;
    expect(section("waiting")?.contains(waits)).toBe(true);
    expect(waits.querySelector(".line-through")).toBeNull();
    expect(waits.querySelector("[data-testid=todo-start-failed]")?.textContent).toContain("machine offline");
    const box = waits.querySelector<HTMLButtonElement>("button[aria-label]")!;
    await act(async () => box.click());
    await flush();
    const patch = api.mock.calls.find(([path, init]) => path.includes("/todos/waits") && (init as { method?: string })?.method === "PATCH");
    expect((patch?.[1] as { json?: unknown })?.json).toEqual({ done: true });
  });

  it("goes with the work that finished in its row: Just finished, behind it, done", () => {
    expect(section("waiting")?.querySelector("[data-id=gone]")).toBeNull();
    const r = row("done")!;
    expect(section("just_done")?.contains(r)).toBe(true);
    expect(r.getAttribute("data-members")).toBe("2");
  });
});

describe("the page of a to-do whose agent never started (audit item 13)", () => {
  it("says why, offers Hand to agent, and Complete closes it for good as its checkbox does", async () => {
    await show("waits");
    const detail = host.querySelector("[data-testid=todos-detail-column]")!;
    expect(detail.querySelector("[data-testid=todo-detail-start-failed]")?.textContent).toContain("machine offline");
    expect(button(t("todos.reopen"))).toBeUndefined();
    expect(button(t("todos.hand_over"))).toBeTruthy();
    // not from a plan: the hub could not start it again by itself
    expect(button(t("todos.retry"))).toBeUndefined();
    expect(detail.textContent).toContain(t("todos.no_agents"));
    await act(async () => button(t("todos.complete"))!.click());
    await flush();
    expect(calls("PATCH", "/todos/waits")).toEqual([{ done: true }]);
  });

  it("starts it again from its plan with Retry", async () => {
    await show("again");
    await act(async () => button(t("todos.retry"))!.click());
    await flush();
    expect(api.mock.calls.some(([p, init]) => p === "/todos/again/retry" && (init as { method?: string })?.method === "POST")).toBe(true);
    expect(calls("PATCH", "/todos/again")).toEqual([]);
  });

  it("shares Retry with the main chat's task card: one on its way at a time, and the to-do the hub reopens shows at once", async () => {
    const reopened = { ...failed("again", "Renew the certificate", "p2"), done_at: null, start_error: null } as unknown as Todo;
    const waiting: (() => void)[] = [];
    const answer = () => waiting.splice(0).forEach((r) => r());
    api.mockImplementation(async (path, init) => {
      if (path === "/todos/again/retry") {
        await new Promise<void>((r) => waiting.push(r));
        return { todo: reopened };
      }
      // the list's read stays out, so only the retry's answer can show the to-do reopened
      if (path === "/todos" && !(init as { method?: string })?.method) return new Promise(() => {});
      return {};
    });
    await show("again");
    await act(async () => button(t("todos.retry"))!.click());
    // the chat's card asks too while the page's is on its way: nothing more is sent
    const { retryTask } = await import("../lib/feed");
    await act(async () => void retryTask(getState().todos.find((x) => x.id === "again")!));
    expect(api.mock.calls.filter(([p]) => p === "/todos/again/retry")).toHaveLength(1);
    await act(async () => answer());
    await flush();
    expect(getState().todos.find((x) => x.id === "again")?.done_at).toBeFalsy();
    expect(host.querySelector("[data-testid=todo-detail-start-failed]")).toBeNull();
    expect(button(t("todos.retry"))).toBeUndefined();
  });

  it("reopens it when it is handed to an agent, not when the dialog only opens", async () => {
    await show("waits");
    await act(async () => button(t("todos.hand_over"))!.click());
    await flush();
    expect(calls("PATCH", "/todos/waits")).toEqual([]);
    await act(async () => button(t("todos.launch_and_hand"))!.click());
    await flush();
    expect(calls("PATCH", "/todos/waits")).toEqual([{ done: false }]);
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "m1", todoId: "waits" });
  });

  it("reads done once it left Needs you (48 hours on): Reopen, as its row", async () => {
    await show("old");
    expect(host.querySelector("[data-testid=todo-detail-start-failed]")).toBeNull();
    expect(button(t("todos.reopen"))).toBeTruthy();
    expect(button(t("todos.hand_over"))).toBeUndefined();
  });
});
