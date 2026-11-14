// @vitest-environment jsdom
// Each item pushed to the open conversation renders ChatView again (its transcript changed). The header and the composer
// around it did not change, so they stay put: counted through a leaf each one draws (the header's "Session info" dialog,
// the composer's context chips, the composer's permission picker).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const renders = vi.hoisted(() => ({ info: 0, chips: 0, modes: 0 }));
const live = vi.hoisted(() => ({ listeners: new Set<{ items?: (e: ItemsEvent) => void }>() }));
vi.mock("./SessionDialogs", async (load) => {
  const real = await load<typeof import("./SessionDialogs")>();
  const { createElement } = await import("react");
  return {
    ...real,
    SessionInfoDialog: (p: Parameters<typeof real.SessionInfoDialog>[0]) => (renders.info++, createElement(real.SessionInfoDialog, p)),
  };
});
vi.mock("./PermissionPicker", async (load) => {
  const real = await load<typeof import("./PermissionPicker")>();
  const { createElement } = await import("react");
  return { ...real, PermissionPicker: (p: Parameters<typeof real.PermissionPicker>[0]) => (renders.modes++, createElement(real.PermissionPicker, p)) };
});
vi.mock("../link/ContextChips", async (load) => {
  const real = await load<typeof import("../link/ContextChips")>();
  const { createElement } = await import("react");
  return { ...real, ContextChips: (p: Parameters<typeof real.ContextChips>[0]) => (renders.chips++, createElement(real.ContextChips, p)) };
});
vi.mock("../../lib/store", async (load) => ({
  ...(await load<typeof import("../../lib/store")>()),
  // the socket is not there: the test pushes to the chat's listeners itself
  listenSession: (_mid: string, _sid: string, l: { items?: (e: ItemsEvent) => void }) => {
    live.listeners.add(l);
    return () => live.listeners.delete(l);
  },
}));
// every render of the agent's persisted words (Answer draws them through Markdown with `fade` given, set or not): counted
const words = vi.hoisted(() => [] as { text: string; fade?: boolean }[]);
vi.mock("../../ui/Markdown", async (load) => {
  const real = await load<typeof import("../../ui/Markdown")>();
  const { createElement } = await import("react");
  return { ...real, Markdown: (p: ComponentProps<typeof real.Markdown>) => ("fade" in p && words.push({ text: p.text, fade: p.fade }), createElement(real.Markdown, p)) };
});
// every message's actions ask for the chat's actions each time they render: counted
const asks = vi.hoisted(() => ({ n: 0 }));
vi.mock("./chat-actions", async (load) => {
  const real = await load<typeof import("./chat-actions")>();
  return { ...real, useChatActions: () => (asks.n++, real.useChatActions()) };
});
vi.mock("../../lib/panel", async (load) => ({ ...(await load<typeof import("../../lib/panel")>()), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

import { applyCompleteSessions, getState, reset, type ItemsEvent } from "../../lib/store";
import { whenComposer } from "../../lib/links";
import type { Item, Session } from "../../lib/types";
import { t } from "../../i18n";
import { ChatView } from "./ChatView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (x: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "Checks", cwd: "/code", status: "working", state: "working", ...x }) as Session;
const item = (seq: number, x: Partial<Item> = {}): Item => ({ id: `i${seq}`, seq, role: "assistant", kind: "text", text: `row ${seq}`, ts: "2026-11-06T00:00:00Z", ...x }) as Item;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
const push = async (seq: number, x: Partial<Item> = {}) => {
  act(() => live.listeners.forEach((l) => l.items?.({ machine_id: "mac", session_id: "s1", items: [item(seq, x)], from_seq: seq - 1, to_seq: seq })));
  await settle();
};
const zero = () => Object.assign(renders, { info: 0, chips: 0, modes: 0 });

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
beforeEach(() => {
  localStorage.clear();
  // the first page has one reply already, so the assistant's face starts from what the pushes keep showing
  fetcher = vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items: [item(0)], to_seq: 0 } : {}), { status: 200, headers: { "Content-Type": "application/json" } })));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  live.listeners.clear();
  reset();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function open(view: React.ReactElement, session = row()) {
  act(() => applyCompleteSessions([session]));
  act(() => root.render(view));
  for (let i = 0; i < 6; i++) await settle();
  expect(live.listeners.size).toBeGreaterThan(0);
  expect(renders.info).toBeGreaterThan(0);
  expect(renders.chips).toBeGreaterThan(0);
  expect(renders.modes).toBeGreaterThan(0);
  zero();
}
const posted = (path: string) => fetcher.mock.calls.filter(([url, init]) => String(url).endsWith(path) && (init as RequestInit | undefined)?.method === "POST");

it("a completed task remains a labelled, clickable owner, not a prompt to link it again", async () => {
  const router = await import("../../lib/router");
  const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
  try {
    await open(<ChatView mid="mac" sid="s1" inPage />, row({
      status: "idle", state: "idle", completed_todo: { id: "done-task", title: "Completed example task" },
    }));
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
    await settle();
    const menu = document.querySelector('[role="menu"]')!;
    const label = t("chat-chatview.todo_completed_label", { title: "Completed example task" });
    expect(menu.textContent).toContain(label);
    expect(menu.textContent).not.toContain(t("chat-chatview.link_todo"));
    expect(menu.textContent).not.toContain(t("chat-chatview.relink_todo"));
    const item = [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((item) => item.textContent?.includes(label))!;
    act(() => item.click());
    expect(navigate).toHaveBeenCalledWith("/todos?id=done-task");
  } finally {
    navigate.mockRestore();
  }
});

it.each([false, true])("a closed task has a persistent ownership banner and keeps its composer (cancelled: %s)", async (cancelled) => {
  await open(<ChatView mid="mac" sid="s1" inPage />, row({
    status: "idle", state: "idle", completed_todo: { id: "done-task", title: "Disposable task", cancelled },
  }));
  await act(async () => { await vi.dynamicImportSettled(); });
  const notice = host.querySelector("[data-completed-todo]")!;
  expect(notice.textContent).toContain(t(cancelled ? "chat-closed-task.cancelled" : "chat-closed-task.completed"));
  expect(notice.textContent).toContain("Disposable task");
  expect(notice.textContent).toContain(t("chat-closed-task.continue"));
  expect(notice.closest("[data-chat-scroll]")).toBeNull();
  expect(notice.querySelector("a")?.getAttribute("href")).toBe("/todos?id=done-task");
  expect(host.querySelector("textarea")).not.toBeNull();
  expect(host.querySelector("[data-stopped-agent]")).toBeNull();
});

it("active task ownership suppresses a stale completed association", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />, row({
    todo: { id: "active", title: "Current task" },
    completed_todo: { id: "done-task", title: "Old task", cancelled: true },
  }));
  expect(host.querySelector("[data-completed-todo]")).toBeNull();
});

it("a stopped closed-task session says why and does not promise it can receive messages", async () => {
  const reason = "Stopped because its to-do was closed. Resume from the terminal to continue.";
  act(() => applyCompleteSessions([row({
    status: "offline", state: "offline", read_only_reason: reason,
    capabilities: { read: true, terminal: true },
    completed_todo: { id: "done-task", title: "Disposable task", cancelled: true },
  })]));
  act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 6; i++) await settle();
  expect(host.querySelector("[data-stopped-agent]")?.textContent).toContain(reason);
  expect(host.querySelector("[data-completed-todo]")?.textContent).not.toContain(t("chat-closed-task.continue"));
  expect(host.querySelector("textarea")).toBeNull();
});

it("an agent's page: pushed items leave its header and composer alone, a session change still reaches them", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  for (let seq = 1; seq <= 10; seq++) await push(seq);
  expect(host.textContent).toContain("row 10");
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });

  act(() => applyCompleteSessions([row({ title: "Renamed" })]));
  await settle();
  expect(renders.info).toBeGreaterThan(0);
  expect(renders.modes).toBeGreaterThan(0);
});

it("a pushed reply renders its own words once, mounting them fading, and no other words", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  for (let seq = 1; seq <= 3; seq++) await push(seq);
  words.length = 0;
  await push(4);
  expect(words).toEqual([{ text: "row 4", fade: true }]);
});

// the first page: this many exchanges, a question and its reply each
const serve = (exchanges: number) => {
  const items = Array.from({ length: exchanges }, (_, i) => [item(2 * i, { role: "user", text: `ask ${i}` }), item(2 * i + 1)]).flat();
  fetcher.mockImplementation((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items, to_seq: items.length - 1 } : {}), { status: 200, headers: { "Content-Type": "application/json" } })));
};

it("a turn starting or ending renders the message actions of its own exchange only", async () => {
  const flip = async (sid: string, exchanges: number) => {
    serve(exchanges);
    await open(<ChatView mid="mac" sid={sid} inPage />, row({ session_id: sid }));
    asks.n = 0;
    for (const state of ["idle", "working"] as const) {
      act(() => applyCompleteSessions([row({ session_id: sid, status: state, state })]));
      await settle();
    }
    return asks.n;
  };
  const one = await flip("s1", 1);
  expect(one).toBeGreaterThan(0); // the exchange that ended, and started again
  expect(await flip("s2", 6)).toBe(one);
});

it("finished exchanges before the last two skip drawing while off screen, from a guessed height", async () => {
  serve(5);
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ status: "idle", state: "idle" }));
  const sections = [...host.querySelectorAll<HTMLElement>("section[data-segment]")];
  expect(sections.map((s) => s.classList.contains("seg-cv"))).toEqual([true, true, true, false, false]);
  expect(sections[0].style.getPropertyValue("--seg-est")).toMatch(/^[1-9]\d*px$/);
});

it("the assistant: the header draws again only when what its face reads changes", async () => {
  await open(<ChatView mid="mac" sid="s1" assistant />);
  for (let seq = 1; seq <= 10; seq++) await push(seq);
  expect(host.textContent).toContain("row 10");
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });

  // a message of yours last: the face thinks again (lib/persona.ts personaState)
  await push(11, { role: "user" });
  expect(renders.info).toBe(1);
  expect(renders.chips).toBe(0);
});

it("the main conversation's composer has the model pill; another Persona conversation's has none (#5032, #5033, #6911)", async () => {
  getState().assistant = { machine_id: "mac", launcher: "claude", main: "s1", conversations: [] };
  await open(<ChatView mid="mac" sid="s1" assistant />);
  expect(host.querySelector("button[data-model-pill]")).not.toBeNull();
  expect(host.querySelector(`button[title="${t("chat-sessiondialogs.effort")}"]`)).toBeNull(); // no effort chip
  // A leader, the supervisor or another assistant conversation is not the main one: no per-conversation picker.
  act(() => root.render(<></>));
  getState().assistant = { machine_id: "mac", launcher: "claude", main: "other", conversations: [] };
  await open(<ChatView mid="mac" sid="s1" assistant />);
  expect(host.querySelector("[data-model-pill]")).toBeNull();
  // An Agent's session still gets its own pill.
  act(() => root.render(<></>));
  await open(<ChatView mid="mac" sid="s1" inPage />);
  expect(host.querySelector("[data-model-pill]")).not.toBeNull();
});

it("the top bar is one line (#5421): a Persona conversation's has no subtitle", async () => {
  await open(<ChatView mid="mac" sid="s1" assistant />, row({ updated_at: new Date(Date.now() - 10_000).toISOString() }));
  const header = host.querySelector("header")!;
  expect(header.textContent).not.toContain(t("lib-format.just_now"));
  expect(header.querySelector("h1")?.textContent).toBe("Checks");
});

it("double-clicking the title renames it in place: Enter saves the title alone, Esc leaves it and the Agent running (#5421)", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  const title = () => host.querySelector<HTMLElement>("[data-chat-title]");
  const field = () => host.querySelector<HTMLInputElement>("[data-chat-title-input]");
  const type = (value: string) => {
    const input = field()!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  };
  const key = (k: string) => field()!.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));

  act(() => title()!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
  expect(field()).not.toBeNull();
  expect(field()!.value).toBe("Checks");
  act(() => type("Network tests"));
  act(() => key("Escape"));
  await settle();
  expect(field()).toBeNull();
  expect(posted("/sessions/mac/s1/rename")).toHaveLength(0);
  // Esc in the field did not also stop the working Agent
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);

  act(() => title()!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
  act(() => type("Network tests"));
  await act(async () => {
    key("Enter");
    await new Promise((r) => setTimeout(r, 0));
  });
  const renamed = posted("/sessions/mac/s1/rename");
  expect(renamed).toHaveLength(1);
  expect(JSON.parse(String((renamed[0][1] as RequestInit).body))).toEqual({ title: "Network tests" });
  expect(title()?.textContent).toBe("Network tests");
});

it("an in-place rename shows the new title at once and puts the old one back when the save fails (#5421)", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  let fail!: () => void;
  fetcher.mockImplementation((url: string, init?: RequestInit) =>
    String(url).endsWith("/rename") && init?.method === "POST"
      ? new Promise((_, reject) => (fail = () => reject(new TypeError("offline"))))
      : Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items: [item(0)], to_seq: 0 } : {}), { status: 200, headers: { "Content-Type": "application/json" } })),
  );
  const title = () => host.querySelector<HTMLElement>("[data-chat-title]");
  act(() => title()!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
  const input = host.querySelector<HTMLInputElement>("[data-chat-title-input]")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Network tests");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  act(() => void input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
  await settle();
  expect(title()?.textContent).toBe("Network tests");
  await act(async () => {
    fail();
    for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0));
  });
  expect(title()?.textContent).toBe("Checks");
});

it("/workflows opens the Background tasks panel at the latest run instead of going to the Agent (#5421)", async () => {
  const { getPanel, openPanel } = await import("../../lib/panel");
  const runs = [
    { task_id: "old", status: "completed", started_at: "2026-11-12T01:00:00Z" },
    { task_id: "new", status: "running", running: true, started_at: "2026-11-12T02:00:00Z" },
  ] as Session["workflows"];
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ workflows: runs }));
  await act(async () => {
    whenComposer("mac/s1", (c) => void c.send("/workflows"));
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(posted("/sessions/mac/s1/messages")).toHaveLength(0);
  expect(getPanel()).toEqual({ kind: "background", mid: "mac", sid: "s1", run: "new" });
  act(() => openPanel(null));
});

it("after the pushes, sending and stopping still go out", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  for (let seq = 1; seq <= 3; seq++) await push(seq);
  await act(async () => {
    whenComposer("mac/s1", (c) => void c.send("next step"));
    await new Promise((r) => setTimeout(r, 0));
  });
  const sent = posted("/sessions/mac/s1/messages");
  expect(sent).toHaveLength(1);
  expect(JSON.parse(String((sent[0][1] as RequestInit).body))).toMatchObject({ text: "next step" });

  const stop = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.interrupt")}"]`);
  expect(stop).toBeTruthy();
  await act(async () => {
    stop!.click();
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(1);
  expect(new Headers((posted("/sessions/mac/s1/interrupt")[0][1] as RequestInit).headers).get("X-Persona-Op-Id")).toBeTruthy();

  // still working: the button says it is stopping and takes no second press, until the turn is over
  const pressed = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.stopping")}"]`);
  expect(pressed()?.disabled).toBe(true);
  act(() => dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
  await settle();
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(1);
  act(() => applyCompleteSessions([row({ status: "idle", state: "idle" })]));
  await settle();
  expect(pressed()).toBeNull();
});

it("a stop the hub lost track of is not reported as done, and can be pressed again", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  fetcher.mockImplementationOnce(() => Promise.resolve(new Response(JSON.stringify({ state: "unknown" }), { status: 202 })));
  await act(async () => {
    host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.interrupt")}"]`)!.click();
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(1);
  expect(host.querySelector(`button[aria-label="${t("chat-composer.interrupt")}"]`)).toBeTruthy();
});
