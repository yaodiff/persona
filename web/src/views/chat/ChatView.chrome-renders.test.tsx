// @vitest-environment jsdom
// Each item pushed to the open conversation renders ChatView again (its transcript changed). The header and the composer
// around it did not change, so they stay put: counted through a leaf each one draws (the header's "Session info" dialog,
// the composer's context chips, the composer's permission picker).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
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
    // a lazy dialog now (its chunk loads on its first open): still drawn by the header on each of its renders
    SessionInfoDialog: (p: ComponentProps<typeof real.SessionInfoDialog>) => (renders.info++, createElement(real.SessionInfoDialog, p)),
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

import { applyCompleteSessions, applyMachine, getState, refreshTodos, reset, type ItemsEvent } from "../../lib/store";
import { whenComposer } from "../../lib/links";
import { closePanel, getPanel, openPanel } from "../../lib/panel";
import { currentPlatform } from "../../lib/shortcuts";
import { useShortcuts } from "../../lib/use-shortcuts";
import { ITEM_COALESCE_MS } from "../../lib/item-coalescer";
import { withContexts } from "../../lib/context-xml";
import { inlineBody } from "../../lib/inline-message";
import { skillContext } from "../../lib/skill-mentions";
import { currentRoster, resetRoster, setRoster } from "../../lib/managers";
import { resetManagerChoice } from "../../lib/manager-choice";
import type { Item, Machine, Session } from "../../lib/types";
import type { Todo } from "../../lib/todos";
import { t } from "../../i18n";
import { ChatView } from "./ChatView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (x: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "Checks", cwd: "/code", status: "working", state: "working", ...x }) as Session;
const item = (seq: number, x: Partial<Item> = {}): Item => ({ id: `i${seq}`, seq, role: "assistant", kind: "text", text: `row ${seq}`, ts: "2026-11-06T00:00:00Z", ...x }) as Item;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
const push = (seq: number, x: Partial<Item> = {}) => act(async () => {
  live.listeners.forEach((l) => l.items?.({ machine_id: "mac", session_id: "s1", items: [item(seq, x)], from_seq: seq - 1, to_seq: seq }));
  // Measure the render after the normal transcript burst window has committed.
  await new Promise((resolve) => setTimeout(resolve, ITEM_COALESCE_MS));
});
const zero = () => Object.assign(renders, { info: 0, chips: 0, modes: 0 });
const updateNotice = () => host.querySelector<HTMLElement>("[data-assistant-update]");
const waitForUpdateNotice = (label: string) => vi.waitFor(async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  expect(updateNotice()?.textContent).toContain(label);
});

let host: HTMLDivElement;
let root: Root;
let opened = "";
let fetcher: ReturnType<typeof vi.fn>;
// An agent's header loads its compare menu lazily (ChatView: import("../../lib/compare-runs")) and finished replies
// load their bar lazily (MessageActions: import("./ReplyBar")), each drawing again when it arrives. Loaded here first,
// neither arrives mid-test under a full parallel run and counts as a render caused by pushes or turn transitions.
beforeAll(async () => {
  await import("../../lib/compare-runs");
  await import("./ReplyBar");
});

beforeEach(() => {
  opened = "";
  localStorage.clear();
  // the first page has one reply already, so the assistant's face starts from what the pushes keep showing
  fetcher = vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items: [item(0)], to_seq: 0 } : String(url).endsWith("/managers") ? currentRoster() ?? {} : {}), { status: 200, headers: { "Content-Type": "application/json" } })));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  act(() => closePanel());
  host.remove();
  live.listeners.clear();
  reset();
  resetRoster();
  resetManagerChoice();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function open(view: React.ReactElement<{ assistant?: boolean }>, session = row()) {
  act(() => applyCompleteSessions([session]));
  if (opened && opened !== session.session_id) {
    // A different conversation is a fresh mount: its predecessor's transcript cleanup must not count in this measurement.
    act(() => root.unmount());
    root = createRoot(host);
  }
  opened = session.session_id;
  await act(async () => root.render(view));
  for (let i = 0; i < 6; i++) await settle();
  const title = view.props.assistant && getState().assistant?.main === session.session_id ? "Persona" : "Checks";
  await vi.waitFor(() => expect(host.querySelector("h1")?.textContent).toBe(title)); // session-derived props are stable
  // Flush the header's lazy menu state before counting renders caused by the transcript.
  await act(async () => { await vi.dynamicImportSettled(); });
  expect(live.listeners.size).toBeGreaterThan(0);
  expect(renders.info).toBeGreaterThan(0);
  expect(renders.chips).toBeGreaterThan(0);
  expect(renders.modes).toBeGreaterThan(0);
  zero();
}
const posted = (path: string) => fetcher.mock.calls.filter(([url, init]) => String(url).endsWith(path) && (init as RequestInit | undefined)?.method === "POST");

it("shows and answers the linked task's current decision above the Agent composer", async () => {
  const round = "2026-11-24T09:00:00Z";
  const task: Todo = {
    id: "task1", title: "Release checks", created_at: round, updated_at: round,
    agents: [{ machine_id: "mac", session_id: "s1", linked_at: round }],
    ask: { outcome: "decide", asked_at: round, verdict: "Needs your decision", question: "How should the release proceed?",
      recommend: "Try a staged rollout", options: [{ label: "Try a staged rollout" }, { label: "Wait for verification" }] },
  };
  let todos: Todo[] = [{ ...task, id: "task2", ask: { ...task.ask!, question: "A different task's decision" } }];
  const fallback = fetcher.getMockImplementation() as typeof fetch;
  fetcher.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(String(url), location.origin).pathname; // the list read carries closed_limit (#16680)
    if (path.endsWith("/todos/task1/accept") && init?.method === "POST") {
      todos = [{ ...task, ask: undefined }];
      return Response.json({});
    }
    return path.endsWith("/todos") ? Response.json({ todos }) : fallback(url, init);
  });
  const card = () => host.querySelector("[data-chat-dock-above] [data-decision-row]");
  const reload = () => act(async () => { await refreshTodos(); });

  await open(<ChatView mid="mac" sid="s1" inPage />, row({ manager: "remote-manager", todo: { id: task.id, title: task.title } }));
  await reload();
  expect(card()).toBeNull(); // a stale association on the to-do list cannot show another task's decision
  todos = [{ ...task, agents: task.agents?.map((a) => ({ ...a, released_at: round })) }];
  await reload();
  expect(card()).toBeNull();

  todos = [task];
  await reload();
  await act(async () => { await import("../assistant/TaskDecision"); });
  await vi.waitFor(() => expect(card()?.querySelector("[data-decision-question]")?.textContent).toBe(task.ask?.question));
  expect(card()?.textContent).toContain("Try a staged rollout");
  expect(card()?.textContent).toContain(t("chat-interactions.recommended"));
  const alternate = [...card()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Wait for verification");
  expect(alternate).toBeDefined();
  await act(async () => { alternate!.click(); });
  const sends = posted("/todos/task1/accept");
  expect(sends).toHaveLength(1);
  expect(JSON.parse((sends[0][1] as RequestInit).body as string)).toEqual({ asked_at: round, option: "Wait for verification" });
  await vi.waitFor(() => expect(card()).toBeNull());
});

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
  expect(notice.textContent).toContain(t(cancelled ? "chat-closed.cancelled" : "chat-closed.completed"));
  expect(notice.textContent).toContain("Disposable task");
  expect(notice.textContent).toContain(t("chat-closed.continue"));
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
  expect(host.querySelector("[data-completed-todo]")?.textContent).not.toContain(t("chat-closed.continue"));
  expect(host.querySelector("textarea")).toBeNull();
});

// Rename…, Rename space… and Delete… open dialogs from a chunk of their own (#10382): the ⋯ menu starts it as it opens, so
// the click draws the dialog in its own render, with no wait for the chunk and no empty frame.
it("the ⋯ menu starts the Rename and Delete dialogs' chunk, so Rename… draws its dialog on the click", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  await import("./SessionMenuDialogs"); // the chunk the menu asked for, in
  await settle();
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((i) => i.textContent?.includes(t("chat-chatview.rename")))!;
  act(() => item.click());
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("chat-sessiondialogs.session_name"));
});

it("an agent's page: pushed items leave its header and composer alone, a session change still reaches them", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  for (let seq = 1; seq <= 10; seq++) await push(seq);
  // Non-critical bursts share the transcript's coalescing window; the commits it makes still leave the chrome alone.
  await vi.waitFor(() => expect(host.textContent).toContain("row 10"));
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
  await vi.waitFor(() => expect(words).toEqual([{ text: "row 4", fade: true }]));
});

it("a pushed inline reference and its disclosure leave the header and composer alone", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  const ref = skillContext({ id: "review", name: "Review", source: "Project", description: "Look for edge cases." });
  await push(1, { role: "user", text: withContexts([ref], inlineBody(["Use ", ref, "."])) });
  const token = () => host.querySelector<HTMLButtonElement>("[data-bubble-text] button[aria-expanded]");
  await vi.waitFor(async () => {
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(token()?.textContent).toBe("Review");
  });
  act(() => token()!.click());
  expect(host.textContent).toContain("Look for edge cases.");
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });
});

// the first page: this many exchanges, a question and its reply each
const serve = (exchanges: number) => {
  const items = Array.from({ length: exchanges }, (_, i) => [item(2 * i, { role: "user", text: `ask ${i}` }), item(2 * i + 1)]).flat();
  fetcher.mockImplementation((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items, to_seq: items.length - 1 } : {}), { status: 200, headers: { "Content-Type": "application/json" } })));
};

it("shows the latest finished reply's actions at rest when replies share a timestamp", async () => {
  serve(2);
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ status: "idle", state: "idle" }));
  const bars = () => [...host.querySelectorAll<HTMLElement>('[data-reply-bar]:not([data-reply-bar="loading"])')];
  await vi.waitFor(() => expect(bars()).toHaveLength(2));
  const [earlier, latest] = bars();
  expect(earlier.querySelector("time")?.closest(".opacity-0")).toBe(earlier);
  expect(latest.querySelector("time")?.closest(".opacity-0")).toBeNull();
  expect(latest.querySelector("button")?.closest(".opacity-0")).toBeNull();
});

it("a turn starting or ending renders the message actions of its own exchange only", async () => {
  const flip = async (sid: string, exchanges: number) => {
    serve(exchanges);
    await open(<ChatView mid="mac" sid={sid} inPage />, row({ session_id: sid }));
    const counts: number[] = [];
    for (const state of ["idle", "working"] as const) {
      asks.n = 0;
      act(() => applyCompleteSessions([row({ session_id: sid, status: state, state })]));
      await settle();
      counts.push(asks.n);
    }
    return counts;
  };
  const one = await flip("s1", 1);
  // Both ending and starting still update the current exchange; neither scales with finished history.
  for (const count of one) expect(count).toBeGreaterThan(0);
  expect(await flip("s2", 6)).toEqual(one);
});

it("a turn starting or ending leaves earlier engine stretches untouched while keeping offscreen virtualization", async () => {
  const measure = async (sid: string, session: Session, earlierItems?: Item[]) => {
    const liveItems = [item(0, { role: "user", text: "live ask" }), item(1, { text: "live reply" })];
    fetcher.mockImplementation((url: string) => {
      const target = String(url);
      if (target.includes("/sessions/mac/prev/items")) {
        return Promise.resolve(new Response(JSON.stringify({ items: earlierItems ?? [], has_more: false }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      if (target.includes("/items")) {
        return Promise.resolve(new Response(JSON.stringify({ items: liveItems, to_seq: 1, has_more: false }), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } }));
    });
    await open(<ChatView mid="mac" sid={sid} inPage />, session);
    if (earlierItems) {
      await vi.waitFor(() => expect(host.querySelector('[data-engine-stretch="prev"]')).not.toBeNull());
      for (let i = 0; i < 4; i++) await settle();
    }
    const counts: number[] = [];
    for (const state of ["idle", "working"] as const) {
      asks.n = 0;
      act(() => applyCompleteSessions([{ ...session, status: state, state }]));
      await settle();
      counts.push(asks.n);
    }
    return counts;
  };
  const baseline = await measure("s1", row({ session_id: "s1" }));
  for (const count of baseline) expect(count).toBeGreaterThan(0);
  const earlierItems = [
    item(0, { id: "p0", role: "user", text: "earlier ask 0", ts: "2026-11-05T00:00:00Z" }),
    item(1, { id: "p1", text: "earlier reply 0", ts: "2026-11-05T00:01:00Z" }),
    item(2, { id: "p2", role: "user", text: "earlier ask 1", ts: "2026-11-05T00:02:00Z" }),
    item(3, { id: "p3", text: "earlier reply 1", ts: "2026-11-05T00:03:00Z" }),
  ];
  const switched = row({
    session_id: "s2",
    agent: "codex",
    engine_history: [{ session_id: "prev", agent: "claude", until: "2026-11-05T01:00:00Z" }],
  });
  expect(await measure("s2", switched, earlierItems)).toEqual(baseline);
  const earlierSections = [...host.querySelectorAll<HTMLElement>('[data-engine-stretch="prev"] section[data-segment]')];
  expect(earlierSections).toHaveLength(2);
  expect(earlierSections.every((s) => s.classList.contains("seg-cv"))).toBe(true);
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
  // Non-critical bursts share the transcript's coalescing window; the commits it makes still leave the chrome alone.
  await vi.waitFor(() => expect(host.textContent).toContain("row 10"));
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });

  // a message of yours last: the face thinks again (lib/persona.ts personaState)
  await push(11, { role: "user" });
  expect(renders.info).toBe(1);
  expect(renders.chips).toBe(0);
});

it("follows the conversation owner's computer update and pending restart without drawing the chat chrome again", async () => {
  const owner = { id: "work", name: "Work assistant", online: true, machine_uid: "work-host", host: "work-box" };
  const other = { id: "assistant", name: "Personal assistant", online: true, pending_restart: true };
  const node: Machine = { id: "linked", name: "Work computer", online: true, machine_uid: "work-host" };
  const lookalike: Machine = { id: "different", name: "Other computer", online: true, hostname: "work-box", deploy_phase: "transferring" };
  const roster = (pending_restart: boolean) => ({ managers: [other, { ...owner, pending_restart }], default: other.id });
  act(() => {
    setRoster(roster(false));
    getState().machines = [lookalike, node];
  });
  await open(<ChatView mid="mac" sid="s1" assistant />, row({ manager: owner.id }));
  const update = (deploy_phase?: Machine["deploy_phase"]) => act(async () => { applyMachine({ ...node, deploy_phase }); });
  expect(updateNotice()).toBeNull();
  await act(async () => {
    applyMachine({ ...lookalike, deploy_phase: "restarting" });
    setRoster(roster(false));
  });
  expect(updateNotice()).toBeNull();
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });

  for (const phase of ["waiting_idle", "transferring"] as const) {
    await update(phase);
    await waitForUpdateNotice(t(`chat-assistantupdate.${phase}`));
    expect(updateNotice()?.getAttribute("role")).toBe("status");
    expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.reconnect"));
  }
  await update("draining");
  await waitForUpdateNotice(t("chat-assistantupdate.draining"));
  expect(updateNotice()?.getAttribute("role")).toBe("status");
  expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.restart_when_idle"));
  expect(updateNotice()?.textContent).not.toContain(t("chat-assistantupdate.reconnect"));
  for (const phase of ["restarting", "confirming"] as const) {
    await update(phase);
    await waitForUpdateNotice(t(`chat-assistantupdate.${phase}`));
    expect(updateNotice()?.getAttribute("role")).toBe("status");
    expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.reconnect"));
  }
  act(() => setRoster(roster(true)));
  expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.confirming"));
  await update();
  expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.pending_restart"));
  expect(updateNotice()?.textContent).toContain(t("chat-assistantupdate.restart_when_idle"));
  act(() => setRoster(roster(false)));
  expect(updateNotice()).toBeNull();
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });
});

it("matches a legacy host by hostname and keeps the notice out of Agent sessions", async () => {
  const manager = { id: "work", name: "Work assistant", online: true, host: "work-box" };
  const node: Machine = { id: "linked", name: "Work computer", online: true, hostname: "WORK-BOX", deploy_phase: "transferring" };
  act(() => {
    setRoster({ managers: [manager], default: manager.id });
    getState().machines = [node];
  });
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ manager: manager.id }));
  expect(updateNotice()).toBeNull();
  await open(<ChatView mid="mac" sid="s2" assistant />, row({ session_id: "s2", manager: manager.id }));
  await waitForUpdateNotice(t("chat-assistantupdate.transferring"));
});

it("follows a conversation's own computer on a hub that predates the roster and deploy phases", async () => {
  const node: Machine = { id: "mac", name: "Example computer", online: true };
  getState().machines = [node];
  await open(<ChatView mid="mac" sid="s1" assistant />);
  expect(updateNotice()).toBeNull();
  await act(async () => { applyMachine({ ...node, deploying: true }); });
  await waitForUpdateNotice(t("chat-assistantupdate.updating"));
  act(() => applyMachine({ ...node, deploying: false }));
  expect(updateNotice()).toBeNull();
  expect(renders).toEqual({ info: 0, chips: 0, modes: 0 });
});

it("the main conversation's composer has the model pill, without an effort chip (#5032, #5033)", async () => {
  getState().assistant = { machine_id: "mac", launcher: "claude", main: "s1", conversations: [] };
  await open(<ChatView mid="mac" sid="s1" assistant />);
  expect(host.querySelector("button[data-model-pill]")).not.toBeNull();
  expect(host.querySelector(`button[title="${t("chat-sessioninfo.effort")}"]`)).toBeNull(); // no effort chip
});

it("a Persona conversation that is not the main one has the model pill too, as the hub keeps its pick (#9248)", async () => {
  getState().assistant = { machine_id: "mac", launcher: "claude", main: "other", conversations: [] };
  await open(<ChatView mid="mac" sid="s1" assistant />);
  expect(host.querySelector("button[data-model-pill]")).not.toBeNull();
});

it("an Agent's session has its own model pill", async () => {
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

it("toggles the session overview and leaves it open while using the chat", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  const trigger = host.querySelector<HTMLButtonElement>("[data-session-overview-trigger]")!;
  const composer = host.querySelector<HTMLTextAreaElement>("textarea")!;
  expect(trigger.getAttribute("aria-pressed")).toBe("false");
  await act(async () => {
    trigger.click();
    await import("./SessionOverview");
  });
  const overview = document.getElementById(trigger.getAttribute("aria-controls")!);
  expect(overview?.tagName).toBe("ASIDE");
  expect(overview?.textContent).toContain(t("chat-sessionoverview.environment"));
  expect(trigger.getAttribute("aria-pressed")).toBe("true");
  act(() => composer.click());
  expect(overview?.isConnected).toBe(true);
  act(() => overview!.querySelector("button")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);
  expect(overview?.isConnected).toBe(true);
  act(() => trigger.click());
  expect(trigger.getAttribute("aria-pressed")).toBe("false");
  expect(overview?.isConnected).toBe(false);
  expect(host.querySelector("textarea")).toBe(composer);
});

it("shows the native Antigravity Swarm status in the session overview", async () => {
  const { SessionOverview } = await import("./SessionOverview");
  const { emptyTaskIndex } = await import("../../lib/tasks");
  let info = {};
  fetcher.mockImplementation(() => Promise.resolve(new Response(JSON.stringify(info), { status: 200, headers: { "Content-Type": "application/json" } })));
  vi.useFakeTimers();
  await act(async () => root.render(<SessionOverview id="test-overview" session={row({ agent: "agy" })}
    machineName="Example computer" items={[]} tasks={emptyTaskIndex()} children={[]} />));
  const overview = host.querySelector("[data-session-overview]")!;
  expect(overview.querySelector("[data-swarm-roster]")).toBeNull();
  info = { swarm_workers: [
    { name: "Build", state: "working" },
    { name: "Review", state: "waiting" },
  ] };
  await act(() => vi.advanceTimersByTimeAsync(3000));
  const roster = overview.querySelector<HTMLDetailsElement>("[data-swarm-roster]")!;
  expect(roster.open).toBe(false);
  expect(roster.querySelector("summary")?.textContent).toContain(t("chat-sessionoverview.swarm_workers", { count: 2 }));
  act(() => roster.querySelector("summary")!.click());
  expect(roster.open).toBe(true);
  expect([...roster.querySelectorAll("li")].map((li) => li.textContent)).toEqual([
    "Build" + t("lib-state-labels.working"),
    "Review" + t("chat-sessionoverview.swarm_waiting"),
  ]);
  info = { swarm_workers: [{ name: "Review", state: "stopped" }] };
  await act(() => vi.advanceTimersByTimeAsync(3000));
  expect(roster.open).toBe(true);
  expect(roster.querySelector("li")?.textContent).toContain(t("chat-sessionoverview.swarm_stopped"));
});

function ShortcutChat({ chat = true }: { chat?: boolean }) {
  const pane = chat ? { mid: "mac", sid: "s1" } : undefined;
  useShortcuts({ route: chat ? { name: "session", mid: "mac", sid: "s1" } : { name: "home" }, pane, narrow: false, toggleDrawer: () => {} });
  return chat ? <ChatView mid="mac" sid="s1" inPage /> : null;
}

const pressB = () => act(() => void dispatchEvent(new KeyboardEvent("keydown", {
  key: "b", code: "KeyB", bubbles: true, cancelable: true,
  ...(currentPlatform() === "mac" ? { metaKey: true } : { ctrlKey: true }),
})));
const overviewDetail = { kind: "background", mid: "mac", sid: "s1", run: "w1", from: "overview" } as const;
const filesPanel = { kind: "files", mid: "mac", sid: "s1", cwd: "/code" } as const;

async function openShortcutOverview() {
  const workflows = [{ task_id: "w1", name: "Overview run", status: "running", running: true, started_at: "2026-11-12T02:00:00Z" }] as Session["workflows"];
  await open(<ShortcutChat />, row({ workflows }));
  const trigger = host.querySelector<HTMLButtonElement>("[data-session-overview-trigger]")!;
  await act(async () => {
    trigger.click();
    await import("./SessionOverview");
  });
  await settle();
  return trigger;
}

function openOverviewRun() {
  act(() => host.querySelector<HTMLButtonElement>('[data-session-overview] [data-bg-run="w1"] [data-bg-row-button]')!.click());
  expect(getPanel()).toEqual(overviewDetail);
}

it("does not reopen a remembered overview detail after its origin closes", async () => {
  const trigger = await openShortcutOverview();
  openOverviewRun();
  pressB();
  expect(getPanel()).toBeNull();
  act(() => trigger.click());
  expect(trigger.getAttribute("aria-pressed")).toBe("false");
  pressB();
  expect(getPanel()).toEqual(filesPanel);
});

it("reopens an overview detail with Cmd+B only while its origin stays open", async () => {
  const trigger = await openShortcutOverview();
  openOverviewRun();
  pressB();
  expect(getPanel()).toBeNull();
  pressB();
  expect(getPanel()).toEqual(overviewDetail);
  act(() => trigger.click());
  expect(getPanel()).toBeNull();
  pressB();
  expect(getPanel()).toEqual(filesPanel);
});

it("forgets a remembered overview detail when its conversation unmounts", async () => {
  await openShortcutOverview();
  openOverviewRun();
  pressB();
  expect(getPanel()).toBeNull();
  act(() => root.render(<ShortcutChat chat={false} />));
  pressB();
  expect(getPanel()).toBeNull();
});

it("keeps a list-origin detail in Cmd+B memory when the overview closes", async () => {
  const trigger = await openShortcutOverview();
  openOverviewRun();
  const listed = { kind: "background", mid: "mac", sid: "s1", run: "w1" } as const;
  act(() => openPanel(listed)); // a chat card replaces the overview detail with its list-origin equivalent
  pressB();
  expect(getPanel()).toBeNull();
  act(() => trigger.click());
  expect(trigger.getAttribute("aria-pressed")).toBe("false");
  pressB();
  expect(getPanel()).toEqual(listed);
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

it("does not interrupt a working turn or close TitleEditor when Escape cancels an IME composition", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  // Open TitleEditor via double-click on the chat title.
  act(() => {
    host.querySelector<HTMLElement>("h1[data-chat-title]")!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
  });
  const titleInput = host.querySelector<HTMLInputElement>("input[data-chat-title-input]");
  expect(titleInput).toBeTruthy();

  // 1. IME Escape (isComposing: true) inside TitleEditor bubbles to window: must neither close TitleEditor nor interrupt.
  await act(async () => {
    titleInput!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true }));
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(host.querySelector("input[data-chat-title-input]")).toBeTruthy();
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);

  // 2. Safari IME Escape (keyCode: 229) inside TitleEditor must neither close TitleEditor nor interrupt.
  const safariTitle = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  Object.defineProperty(safariTitle, "keyCode", { value: 229 });
  await act(async () => {
    titleInput!.dispatchEvent(safariTitle);
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(host.querySelector("input[data-chat-title-input]")).toBeTruthy();
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);

  // 3. Normal Escape closes TitleEditor without interrupting.
  await act(async () => {
    titleInput!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(host.querySelector("input[data-chat-title-input]")).toBeNull();
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);

  // 4. IME Escape (isComposing and Safari keyCode 229) on window while working must not interrupt.
  const safariWindow = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  Object.defineProperty(safariWindow, "keyCode", { value: 229 });
  await act(async () => {
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true }));
    document.body.dispatchEvent(safariWindow);
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(0);

  // 5. Real Escape while working still interrupts.
  await act(async () => {
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await new Promise((r) => setTimeout(r, 0));
  });
  expect(posted("/sessions/mac/s1/interrupt")).toHaveLength(1);
});
