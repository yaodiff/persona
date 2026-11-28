// @vitest-environment jsdom
// Conversation tasks on a phone (#11511): under 640 px the header's button gives way, so the title keeps its room, and
// ⋯ offers the panel instead; from 640 px the button is there and ⋯ is as it was. The "Loading earlier messages" row
// is drawn over the chat's top padding, out of the flow: an older page starting or landing moves nothing on screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

const { ChatView } = await import("./ChatView");
const { applyCompleteSessions, reset, upsertSession } = await import("../../lib/store");
const { closePanel, getPanel } = await import("../../lib/panel");
const { t } = await import("../../i18n");
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
const openMore = async () => {
  act(() => header().querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  await flush();
  expect(menuRows().length).toBeGreaterThan(0);
};

beforeEach(() => {
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

describe("conversation tasks in the header", () => {
  it("on a phone, the button gives way and ⋯ opens the panel", async () => {
    media.phone = true;
    upsertSession(agent);
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await flush();
    expect(tasksButton()!.className).toMatch(/(^| )hidden( |$)/);
    expect(tasksButton()!.className).toContain("sm:flex");
    await openMore();
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
    await openMore();
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
    await openMore();
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
    await openMore();
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
    await openMore();
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
    await openMore();
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
