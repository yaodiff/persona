// @vitest-environment jsdom
// Conversation tasks on a phone (#11511): under 640 px the header's button gives way, so the title keeps its room, and
// ⋯ offers the panel instead; from 640 px the button is there and ⋯ is as it was. The "Loading earlier messages" row
// is drawn over the chat's top padding, out of the flow: an older page starting or landing moves nothing on screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openAgentMore } from "../../test/agent-menu";
import { REVIEW_FEATURE, type Item, type Session } from "../../lib/types";

// a phone (under 640 px, touch) or a desktop (a mouse), as the test says
const media = vi.hoisted(() => {
  const state = { phone: false };
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (query: string) => ({
    media: query,
    get matches() { return state.phone ? /max-width: (639|767|1023)px|hover: none|pointer: coarse/.test(query) : /pointer: *fine|hover: *hover/.test(query); },
    addEventListener() {},
    removeEventListener() {},
  });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  return state;
});

const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "", loadingEarlier: false }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

const { ChatView, Header } = await import("./ChatView");
const { applyCompleteSessions, reset, upsertSession } = await import("../../lib/store");
const { closePanel, getPanel, openPanel } = await import("../../lib/panel");
const { initI18n, t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const TASKS = t("sidepanel.conversation_tasks");
const REVIEW = t("chat-chatview.review_changes");
const agent = { machine_id: "mac", session_id: "s1", title: "Checks", cwd: "/code", status: "idle", state: "idle" } as Session;
const main = { machine_id: "hub", session_id: "c1", title: "Persona", status: "idle", state: "idle", assistant: true, agent: "claude" } as Session;
const reply = { id: "i1", seq: 1, role: "assistant", kind: "text", text: "Done.", ts: "2026-11-06T00:00:00Z" } as Item;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const header = () => host.querySelector("header")!;
const tasksButton = () => header().querySelector<HTMLButtonElement>(`button[aria-label="${TASKS}"]`);
const menuRows = () => [...document.querySelectorAll<HTMLElement>('[role="menu"] [role^="menuitem"]')];
const tasksRow = () => menuRows().find((r) => r.textContent?.includes(TASKS));
const openHeaderMenu = async () => {
  act(() => header().querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  await flush();
  expect(menuRows().length).toBeGreaterThan(0);
};

beforeEach(async () => {
  await initI18n("zh-Hans");
  localStorage.clear();
  Object.assign(transcript, { items: [reply], hasMore: false, loadingEarlier: false });
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  act(() => closePanel());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("the single-Agent header menu", () => {
  const labels = () => menuRows().map((row) => row.querySelector("span")?.textContent);
  it.each(["en", "zh-Hans", "ja"])("offers each action once, with Close last and Delete under More (%s)", async (lang) => {
    await initI18n(lang);
    media.phone = true;
    const onView = vi.fn();
    const session: Session = { ...agent, status: "done", state: "unread" };
    act(() => root.render(<Header session={session} title={agent.title} machineName="Test" siblings={[]} onInterrupt={() => {}} view="term" onView={onView} />));
    await openHeaderMenu();
    const top = [t("agentmenus.back_to_chat"), t("agentmenus.mark_seen"), t("chat-chatview.rename"), t("chat-chatview.pin"), t("sidebar.more"), t("agentmenus.close")];
    expect(labels()).toEqual(top);
    openAgentMore();
    const children = labels().slice(top.length);
    expect(children).toEqual([
      t("chat-chatview.files"), "Git", t("chat-chatview.mute"), t("chat-chatview.link_todo"), t("chat-chatview.share"),
      t("chat-chatview.export"), t("chat-chatview.session_info"), t("chat-chatview.copy_session_id"),
      t("chat-chatview.copy_folder"), t("chat-chatview.copy_resume_command"), t("chat-chatview.delete_chat"),
    ]);
    expect(new Set(labels()).size).toBe(labels().length);
    act(() => menuRows()[0].click());
    expect(onView).toHaveBeenCalledExactlyOnceWith("chat");
  });

  it("places Interrupt first for a working Agent and returns from a side panel", async () => {
    media.phone = false;
    const session: Session = { ...agent, state: "working", status: "working" };
    const onInterrupt = vi.fn();
    act(() => {
      openPanel({ kind: "files", mid: agent.machine_id, sid: agent.session_id, cwd: agent.cwd });
      root.render(<Header session={session} title={agent.title} machineName="Test" siblings={[]} onInterrupt={onInterrupt} />);
    });
    await openHeaderMenu();
    expect(labels()).toEqual([t("agentmenus.back_to_chat"), t("chat-chatview.interrupt"), t("chat-chatview.rename"), t("chat-chatview.pin"), t("sidebar.more"), t("agentmenus.close")]);
    act(() => menuRows()[0].click());
    expect(getPanel()).toBeNull();
    await openHeaderMenu();
    expect(labels()[0]).toBe(t("chat-chatview.interrupt"));
    act(() => menuRows()[0].click());
    expect(onInterrupt).toHaveBeenCalledOnce();
  });

  it.each([
    { kind: "agent", mid: "mac", sid: "another-agent" },
    { kind: "task", todo: "separate-task" },
  ] as const)("returns from the Agent's terminal without closing a separate $kind panel", async (panel) => {
    const onView = vi.fn();
    const render = (view: "chat" | "term") => root.render(
      <Header session={agent} title={agent.title} machineName="Test" siblings={[]} onInterrupt={() => {}} view={view} onView={onView} />,
    );
    act(() => { openPanel(panel); render("chat"); });
    await openHeaderMenu();
    expect(labels()).not.toContain(t("agentmenus.back_to_chat"));
    act(() => render("term"));
    expect(labels()[0]).toBe(t("agentmenus.back_to_chat"));
    act(() => menuRows()[0].click());
    expect(onView).toHaveBeenCalledExactlyOnceWith("chat");
    expect(getPanel()).toEqual(panel);
  });

  it("keeps Delete last for an offline Agent without a name change or a directory", async () => {
    media.phone = true;
    const session: Session = { ...agent, cwd: "", state: "offline", status: "offline", capabilities: { read: true, rename: false } };
    act(() => root.render(<Header session={session} title={agent.title} machineName="Test" siblings={[]} onInterrupt={() => {}} />));
    await openHeaderMenu();
    expect(labels()).toEqual([t("chat-chatview.pin"), t("sidebar.more"), t("chat-chatview.delete_chat")]);
    openAgentMore();
    expect(labels()).not.toContain(t("chat-chatview.copy_folder"));
    expect(labels()).not.toContain(t("chat-chatview.files"));
    expect(labels()).not.toContain(t("chat-chatview.rename"));
    expect(labels().filter((label) => label === t("chat-chatview.delete_chat"))).toHaveLength(1);
  });
});

describe("conversation tasks in the header", () => {
  it("on a phone, the button gives way and ⋯ opens the panel", async () => {
    media.phone = true;
    upsertSession(agent);
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await flush();
    expect(tasksButton()!.className).toMatch(/(^| )hidden( |$)/);
    expect(tasksButton()!.className).toContain("sm:flex");
    await openHeaderMenu();
    openAgentMore();
    const row = tasksRow()!;
    expect(row).toBeDefined();
    act(() => row.click());
    await flush();
    expect(getPanel()).toMatchObject({ kind: "conversation-tasks", mid: "mac", sid: "s1" });
  });

  it("on a phone, the main conversation's menu (its title) has it too", async () => {
    media.phone = true;
    upsertSession(main);
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant mainCache={{ scope: "test" }} onToggleSidebar={() => {}} />));
    await flush();
    expect(tasksButton()!.className).toContain("sm:flex");
    act(() => header().querySelector<HTMLButtonElement>("[data-assistant-name-menu]")!.click());
    await flush();
    act(() => tasksRow()!.click());
    await flush();
    expect(getPanel()).toMatchObject({ kind: "conversation-tasks", mid: "hub", sid: "c1" });
  });

  it("on a phone, ⋯ has it before the session is in (a chat opened by link, a new one starting)", async () => {
    media.phone = true;
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await flush();
    expect(tasksButton()!.className).toContain("sm:flex");
    await openHeaderMenu();
    expect(menuRows().map((r) => r.textContent)).toEqual([TASKS]);
    act(() => tasksRow()!.click());
    await flush();
    expect(getPanel()).toMatchObject({ kind: "conversation-tasks", mid: "mac", sid: "s1" });
  });

  it("from 640 px, the button opens it and ⋯ omits duplicate desktop panel items and aligns right", async () => {
    media.phone = false;
    applyCompleteSessions([{ ...agent, branch: "feat/header-cleanup" }], [], { features: [REVIEW_FEATURE] });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await flush();
    expect(header().querySelector("[data-chat-branch]")).toBeNull();
    expect(header().querySelector("[data-header-divider]")).not.toBeNull();
    await openHeaderMenu();
    openAgentMore();
    const labels = menuRows().map((r) => r.textContent ?? "");
    expect(tasksRow()).toBeUndefined();
    expect(labels.some((l) => l.includes(t("chat-chatview.files")))).toBe(false);
    expect(labels.some((l) => l === "Git")).toBe(false);
    expect(labels.some((l) => l.includes(t("chat-chatview.terminal")))).toBe(false);
    expect(labels).toContain(REVIEW);
    const menuEl = document.querySelector<HTMLElement>('[role="menu"]')!;
    expect(menuEl.style.transformOrigin).toBe("top right");
    act(() => tasksButton()!.click());
    await flush();
    expect(getPanel()).toMatchObject({ kind: "conversation-tasks", mid: "mac", sid: "s1" });
  });

  it("on a phone, ⋯ retains Files, Git, and Review changes alongside Conversation tasks", async () => {
    media.phone = true;
    applyCompleteSessions([agent], [], { features: [REVIEW_FEATURE] });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await flush();
    await openHeaderMenu();
    openAgentMore();
    const labels = menuRows().map((r) => r.textContent ?? "");
    expect(labels.some((l) => l.includes(t("chat-chatview.files")))).toBe(true);
    expect(labels.some((l) => l === "Git")).toBe(true);
    expect(labels).toContain(REVIEW);
    expect(labels.some((l) => l.includes(TASKS))).toBe(true);
  });
});

describe("Review changes in the header", () => {
  it("opens the review panel from ⋯ on desktop", async () => {
    media.phone = false;
    applyCompleteSessions([agent], [], { features: [REVIEW_FEATURE] });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await openHeaderMenu();
    openAgentMore();
    const reviewRow = menuRows().find((row) => row.textContent === REVIEW);
    expect(reviewRow).toBeDefined();
    act(() => reviewRow!.click());
    expect(getPanel()).toMatchObject({ kind: "review", mid: "mac", sid: "s1", cwd: agent.cwd });
  });

  it.each([
    { reason: "the hub lacks review support", session: agent, features: [] },
    { reason: "the session has no working directory", session: { ...agent, cwd: "" }, features: [REVIEW_FEATURE] },
  ])("omits the entry when $reason", async ({ session, features }) => {
    media.phone = true;
    applyCompleteSessions([session], [], { features });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await openHeaderMenu();
    openAgentMore();
    expect(menuRows().map((row) => row.textContent)).not.toContain(REVIEW);
  });
});

it("draws Loading earlier messages out of the flow, over the chat's top padding", async () => {
  media.phone = true;
  upsertSession(agent);
  Object.assign(transcript, { hasMore: true, loadingEarlier: true });
  act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
  await flush();
  const status = [...host.querySelectorAll('[data-chat-scroll] [role="status"]')].find((s) => s.textContent?.includes(t("chat-chatview.loading_earlier")))!;
  expect(status).toBeDefined();
  const row = status.parentElement!;
  expect(row.hasAttribute("data-loading-earlier")).toBe(true);
  expect(row.className.split(" ")).toEqual(expect.arrayContaining(["absolute", "pointer-events-none", "top-1"]));
  // nothing positioned between it and the scroller, its containing block, which scrolls it with the chat
  expect(row.parentElement!.hasAttribute("data-chat-content")).toBe(true);
  expect(row.parentElement!.className.split(" ")).not.toContain("relative");
  expect(row.parentElement!.parentElement!.matches("[data-chat-scroll].relative")).toBe(true);
});
