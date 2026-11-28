// @vitest-environment jsdom
// A Persona conversation's own approval card is in the chat, above the composer (#4883): its first Bash waited for
// approval while the card sat only in the closed Needs you tray, and the owner thought the assistant had hung. The
// card says it waits for the user's approval, Allow ends it, and Needs you still counts it with a row that leads to it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Interaction, Item, Session } from "../../lib/types";
import zh from "../../i18n/locales/zh-Hans/chat-interactions.json";
import en from "../../i18n/locales/en/chat-interactions.json";
import ja from "../../i18n/locales/ja/chat-interactions.json";
import zhTasks from "../../i18n/locales/zh-Hans/assistant-tasks.json";
import enTasks from "../../i18n/locales/en/assistant-tasks.json";
import jaTasks from "../../i18n/locales/ja/assistant-tasks.json";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../lib/router", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/router")>(), useRoute: () => ({ name: "persona", sid: "c1" }) }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

const { ChatView } = await import("./ChatView");
const { getState, reset, upsertSession } = await import("../../lib/store");
const { t } = await import("../../i18n");
const { loadPages } = await import("../../lib/lazy-page");
await loadPages();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const bash = {
  id: "ix_bash", machine_id: "hub", session_id: "c1", session_title: "Persona", kind: "permission", tool_name: "Bash",
  tool_input: { command: "ssh vps 'cat /etc/example/config.json'", description: "Read the server config" },
  created_at: "2020-01-01T00:00:00Z",
} as Interaction;

const question = {
  id: "ix_question", machine_id: "hub", session_id: "c1", session_title: "Persona", kind: "question", tool_name: "AskUserQuestion",
  questions: [{ question: "Which mock change should ship?", header: "Mock choice", options: [{ label: "Use mock", description: "Local fixture only" }] }],
  created_at: "2020-01-01T00:01:00Z",
} as Interaction;

// A worker folded under the chat (#5120) asks too: its cards do not hold the chat's turn (lib/turn-asks.ts).
const workerBash = { ...bash, id: "ix_worker_bash", session_id: "w1", session_title: "Worker", folded_under: "c1", folded_as: "worker" } as Interaction;
const workerQuestion = { ...question, id: "ix_worker_question", session_id: "w1", session_title: "Worker", folded_under: "c1", folded_as: "worker" } as Interaction;

const withWorker = () => upsertSession({ machine_id: "hub", session_id: "w1", title: "Worker", status: "working", state: "needs_you", agent: "claude", folded_under: "c1", folded_as: "worker" } as Session);

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const card = (id = bash.id) => host.querySelector(`[data-card="${id}"]`);
const tray = () => host.querySelector("[data-assistant-actionbar]");
const nextOwn = () => host.querySelector<HTMLButtonElement>("[data-next-own-ask]");
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const resolves = () => fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${bash.id}/resolve`));
const revealed: string[] = [];
const originalScrollIntoView = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");

beforeEach(() => {
  localStorage.clear();
  transcript.items = [];
  revealed.length = 0;
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, writable: true, value: function (this: HTMLElement) {
    const id = this.parentElement?.closest("[data-card]")?.getAttribute("data-card");
    if (id) revealed.push(id);
  } });
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => window.setTimeout(() => callback(0), 0));
  upsertSession({ machine_id: "hub", session_id: "c1", title: "Persona", status: "working", state: "needs_you", assistant: true, agent: "claude" } as Session);
  Object.assign(getState(), { interactions: [bash], answered: {} });
  fetcher = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", originalScrollIntoView);
  else Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  reset();
  vi.unstubAllGlobals();
});

describe("an assistant-owned approval question", () => {
  const prepared = {
    ...bash,
    id: "ix_prepared",
    tool_name: "persona_settings",
    can_clarify: true,
    fields: [{ key: "password", label: "Password", secret: true }],
  } as Interaction;
  const area = () => host.querySelector<HTMLTextAreaElement>("[data-approval-question] textarea");
  const type = (text: string) => act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area(), text);
    area()!.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const show = async () => {
    Object.assign(getState(), { interactions: [prepared], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    await act(async () => {
      button(t("chat-interactions.have_question"))!.click();
      await import("./ApprovalQuestion");
    });
    await flush();
  };

  it("sends only verbatim text and a stable message ID, then allows the same card once", async () => {
    await show();
    const question = "  Why does this change need my password?\nPlease be specific.  ";
    await type(question);
    act(() => button(t("approval-question.send"))!.click());
    await flush();
    const calls = fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${prepared.id}/clarify`));
    expect(calls.length).toBe(1);
    const body = JSON.parse(String((calls[0][1] as RequestInit).body));
    expect(body.text).toBe(question);
    expect(body.client_msg_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(Object.keys(body).sort()).toEqual(["client_msg_id", "text"]);
    expect(fetcher.mock.calls.some(([url]) => String(url).endsWith(`/interactions/${prepared.id}/resolve`))).toBe(false);
    expect(area()).toBeNull();
    expect(getState().interactions.some((i) => i.id === prepared.id)).toBe(true);
    act(() => button(t("chat-interactions.allow"))!.click());
    act(() => button(t("approval-impact.confirm"))!.click());
    await flush();
    const allows = fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${prepared.id}/resolve`));
    expect(allows.length).toBe(1);
    expect(JSON.parse(String((allows[0][1] as RequestInit).body))).toMatchObject({ allow: true });
  });

  it("preserves a failed draft and retries it under the same delivery ID", async () => {
    await show();
    await type("  keep every surrounding character  ");
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify({ error: "offline" }), { status: 400, headers: { "Content-Type": "application/json" } }));
    act(() => button(t("approval-question.send"))!.click());
    await flush();
    expect(area()?.value).toBe("  keep every surrounding character  ");
    expect(button(t("chat-interactions.allow"))!.disabled).toBe(false);
    act(() => button(t("approval-question.send"))!.click());
    await flush();
    const calls = fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${prepared.id}/clarify`));
    expect(calls.length).toBe(2);
    const first = JSON.parse(String((calls[0][1] as RequestInit).body));
    const second = JSON.parse(String((calls[1][1] as RequestInit).body));
    expect(second).toEqual(first);
  });

  it("does not turn Enter in the question editor into Allow", async () => {
    await show();
    await type("Why this change?");
    act(() => area()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
    await flush();
    expect(fetcher.mock.calls.some(([url]) => String(url).endsWith(`/interactions/${prepared.id}/resolve`))).toBe(false);
    expect(fetcher.mock.calls.some(([url]) => String(url).endsWith(`/interactions/${prepared.id}/clarify`))).toBe(true);
  });

  it("ships the action in all three languages and leaves old cards unchanged", async () => {
    expect(en.have_question).toBe("Have a question");
    expect(zh.have_question).toBe("有疑问");
    expect(ja.have_question).toBe("質問する");
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(button(t("chat-interactions.have_question"))).toBeUndefined();
    expect(button(t("chat-interactions.allow"))).toBeDefined();
    expect(button(t("chat-interactions.deny"))).toBeDefined();
  });
});

describe("a Persona conversation's own approval card (#4883)", () => {
  it("keeps a promoted relay input collapsed while the answer and question card stay visible", async () => {
    const original = "Synthetic relay transport details\n".repeat(8).trim();
    const relay: Item = { id: "relay#0", seq: 1, role: "user", kind: "text", text: original, source: { kind: "conversation", conversation_id: "source-1", title: "Synthetic source" } };
    const ask: Item = { id: "ask#0", seq: 2, role: "assistant", kind: "tool_use", tool_use_id: "ask-call", tool_name: "mcp__persona__ask_user", tool_input: { question: "Which mock change should ship?" } };
    const receipt: Item = { id: "receipt#0", seq: 3, role: "user", kind: "tool_result", tool_use_id: "ask-call", text: "ok" };
    const reply: Item = { id: "reply#0", seq: 4, role: "assistant", kind: "text", text: "The mock result needs your confirmation." };
    transcript.items = [relay, ask, receipt, reply];
    Object.assign(getState(), {
      assistant: { machine_id: "hub", main: "c1", conversations: [] },
      interactions: [question],
      answered: {},
    });
    upsertSession({ machine_id: "hub", session_id: "c1", title: "Persona", status: "done", state: "needs_you", assistant: true, agent: "claude" } as Session);
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();

    const source = host.querySelector('[data-message-source="conversation"]');
    const disclosure = source?.querySelector<HTMLButtonElement>("button[aria-expanded]");
    expect(disclosure?.getAttribute("aria-expanded")).toBe("false");
    expect(host.textContent).not.toContain(original);
    expect(host.querySelector("[data-answer]")?.textContent).toContain(reply.text);
    expect(card(question.id)?.textContent).toContain("Which mock change should ship?");

    act(() => disclosure!.click());
    expect(disclosure?.getAttribute("aria-expanded")).toBe("true");
    expect(host.textContent).toContain(original);
  });

  it("is in the chat, not only in the closed Needs you tray", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    // the tray starts closed, and the card is there anyway
    expect(host.querySelector("[data-tray-list]")).toBeNull();
    expect(card()).not.toBeNull();
    expect(host.querySelector("[data-decision-pager]")?.contains(card())).toBe(true);
    expect(card()?.textContent).toContain(t("chat-interactions.permission_kicker"));
    expect(card()?.textContent).toContain("ssh vps");
    expect(button(t("chat-interactions.allow"))).toBeDefined();
    expect(button(t("chat-interactions.deny"))).toBeDefined();
  });

  it("a worker's card is still counted in Needs you, whose row leads to the card instead of drawing it twice", async () => {
    withWorker();
    Object.assign(getState(), { interactions: [workerBash], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const needs = host.querySelector<HTMLButtonElement>('[data-tray-entry="needs"]')!;
    expect(needs.textContent).toContain("1");
    act(() => needs.click());
    const row = host.querySelector<HTMLButtonElement>(`[data-own-ask="${workerBash.id}"]`);
    expect(row).not.toBeNull();
    expect(host.querySelectorAll(`[data-card="${workerBash.id}"]`).length).toBe(1);
    act(() => row!.click());
    expect(host.querySelector("[data-tray-list]")).toBeNull();
    expect(card(workerBash.id)).not.toBeNull();
  });

  it("cycles to the next unanswered card of a session folded under it, expanding the existing stack and wrapping (#10347)", async () => {
    withWorker();
    Object.assign(getState(), { interactions: [workerBash, workerQuestion], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(nextOwn()?.textContent).toBe(`${t("assistant-tasks.next_unanswered")}2`);
    expect(card(workerQuestion.id)).toBeNull();
    revealed.length = 0; // the card slot's initial landing is not a shortcut navigation

    act(() => nextOwn()!.click());
    await flush();
    expect(revealed).toEqual([workerBash.id]);

    act(() => nextOwn()!.click());
    await flush();
    expect(card(workerQuestion.id)).not.toBeNull();
    expect(revealed).toEqual([workerBash.id, workerQuestion.id]);
    // The expanded cards must not shrink the closed action bar to zero and cover the next click (real-browser evidence).
    expect(tray()?.classList.contains("shrink-0")).toBe(true);

    act(() => nextOwn()!.click());
    await flush();
    expect(revealed).toEqual([workerBash.id, workerQuestion.id, workerBash.id]);
    expect(host.querySelectorAll("[data-card]")).toHaveLength(2);
  });

  it("reduces the next-unanswered count as either kind of card is answered, then hides it", async () => {
    withWorker();
    Object.assign(getState(), { interactions: [workerBash, workerQuestion], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    act(() => button(t("chat-interactions.allow"))!.click());
    act(() => button(t("approval-impact.confirm"))!.click());
    await flush();
    expect(nextOwn()?.textContent).toBe(`${t("assistant-tasks.next_unanswered")}1`);

    const choice = [...card(workerQuestion.id)!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("Use mock"));
    act(() => choice!.click());
    await flush();
    expect(getState().interactions).toEqual([]);
    expect(nextOwn()).toBeNull();
  });

  it("has no next-unanswered entry when no card waits", async () => {
    Object.assign(getState(), { interactions: [], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(nextOwn()).toBeNull();
  });

  it("labels the next-unanswered entry in all three languages", () => {
    expect(enTasks.next_unanswered).toBe("Next waiting for your answer");
    expect(zhTasks.next_unanswered).toBe("下一条待你回复");
    expect(jaTasks.next_unanswered).toBe("次の回答待ち");
  });

  it("ends once allowed, and the chat goes on", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    act(() => button(t("chat-interactions.allow"))!.click());
    act(() => button(t("approval-impact.confirm"))!.click());
    await flush();
    expect(resolves().length).toBe(1);
    expect(JSON.parse(String((resolves()[0][1] as RequestInit).body))).toMatchObject({ allow: true });
    expect(getState().interactions.some((i) => i.id === bash.id)).toBe(false);
    // no card waits any more: no answer buttons, no "waiting for your approval", the composer is there to go on
    expect(button(t("chat-interactions.allow"))).toBeUndefined();
    expect(host.textContent).not.toContain(t("chat-interactions.permission_kicker"));
    expect(host.querySelector("textarea")).not.toBeNull();
  });

  it("says it waits for the user's approval, in every language", () => {
    expect(zh.permission_kicker).toBe("等待你的批准");
    expect(en.permission_kicker).toBe("Waiting for your approval");
    expect(ja.permission_kicker).toBe("承認を待っています");
  });
});

// An approval the chat's own turn waits on starts in the visible decision pager. Needs you does not need a second copy.
// A worker's or side session's card does not hold the turn and stays folded in the stack and behind Needs you.
describe("cards the chat's own turn waits on", () => {
  it("keeps an unavailable skip outside the answers and hand-back available without Skip", async () => {
    Object.assign(getState(), { interactions: [bash, question], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const disabledSkip = card(bash.id)?.querySelector<HTMLButtonElement>("[data-decision-actions] button[disabled]");
    expect(disabledSkip).not.toBeNull();
    expect(disabledSkip?.hasAttribute("data-card-answer")).toBe(false);
    expect(card(bash.id)?.querySelector(`[aria-label="${t("chat-interactions.hand_back")}"]`)).not.toBeNull();
    act(() => above()!.querySelector<HTMLButtonElement>(`[aria-label="${t("assistant-tasks.next_decision")}"]`)!.click());
    expect(card(question.id)?.querySelector(`[aria-label="${t("chat-interactions.hand_back")}"]`)).not.toBeNull();
  });

  it("hands an approval back from the pager when Skip is unavailable", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const handBack = card(bash.id)?.querySelector<HTMLButtonElement>(`[aria-label="${t("chat-interactions.hand_back")}"]`);
    expect(handBack).not.toBeNull();
    act(() => handBack!.click());
    await flush();
    expect(resolves()).toHaveLength(1);
    expect(JSON.parse(String((resolves()[0][1] as RequestInit).body))).toEqual({ dismiss: true });
  });

  const above = () => host.querySelector("[data-decision-pager]");
  const before = (a: Element, b: Element) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

  it("opens above the capsules and pages between requests without opening Needs you", async () => {
    Object.assign(getState(), { interactions: [bash, question], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(above()?.contains(card(bash.id))).toBe(true);
    expect(card(question.id)).toBeNull();
    const next = above()!.querySelector<HTMLButtonElement>(`[aria-label="${t("assistant-tasks.next_decision")}"]`)!;
    act(() => next.click());
    expect(above()?.contains(card(question.id))).toBe(true);
    expect(card(bash.id)).toBeNull();
    expect(host.querySelector("[data-chat-cards-position]")).toBeNull();
    expect(host.querySelector("[data-chat-cards-fold]")).toBeNull();
    // above the capsules, and the tray is closed
    const needs = host.querySelector<HTMLButtonElement>('[data-tray-entry="needs"]')!;
    expect(before(above()!, needs)).toBe(true);
    expect(tray()?.contains(above())).toBe(true);
    expect(host.querySelector("[data-tray-list]")).toBeNull();
    // the cards give up their height and scroll in their own box; the capsules under them never shrink out of view
    expect(above()?.querySelector("[data-scroll-box]")?.className).toContain("overflow-y-auto");
    // answerable in place, and not counted or listed behind the capsule as well
    expect(card(question.id)!.querySelector("button")).not.toBeNull();
    expect(needs.textContent).toBe(t("assistant-tasks.tray_needs_you"));
    expect(nextOwn()).toBeNull();
    act(() => needs.click());
    expect(host.querySelector(`[data-own-ask="${bash.id}"]`)).toBeNull();
    expect(above()).toBeNull();
    expect(host.querySelectorAll(`[data-card="${bash.id}"]`)).toHaveLength(0);
  });

  it("leave the other cards folded as before", async () => {
    withWorker();
    Object.assign(getState(), { interactions: [workerBash, workerQuestion, bash], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(above()?.contains(card(bash.id))).toBe(true);
    expect(above()?.querySelectorAll("[data-card]")).toHaveLength(1);
    // the worker's two: the first in the stack with its position chip, the second folded behind it
    expect(card(workerBash.id)).not.toBeNull();
    expect(above()?.contains(card(workerBash.id))).toBe(false);
    expect(card(workerQuestion.id)).toBeNull();
    expect(host.querySelector("[data-chat-cards-position]")?.textContent).toContain("2");
    // and behind Needs you: counted there, with a row each
    const needs = host.querySelector<HTMLButtonElement>('[data-tray-entry="needs"]')!;
    expect(needs.textContent).toContain("2");
    expect(nextOwn()?.textContent).toBe(`${t("assistant-tasks.next_unanswered")}2`);
    act(() => needs.click());
    expect(host.querySelector(`[data-own-ask="${workerBash.id}"]`)).not.toBeNull();
    expect(host.querySelector(`[data-own-ask="${workerQuestion.id}"]`)).not.toBeNull();
    expect(host.querySelector(`[data-own-ask="${bash.id}"]`)).toBeNull();
  });
});

describe("background supervisor interactions in main chat", () => {
  const bgBash = {
    id: "ix_bg_bash",
    machine_id: "m1",
    session_id: "bg1",
    session_title: "assistant",
    kind: "permission",
    tool_name: "ATS",
    tool_input: { command: "ats GET /tasks/task_75", description: "Read ATS task" },
    created_at: "2020-01-01T00:00:00Z",
  } as Interaction;

  it("surfaces a background supervisor permission card above the main chat composer and resolves Allow", async () => {
    upsertSession({ machine_id: "hub", session_id: "c1", title: "Persona", status: "idle", state: "idle", assistant: true, agent: "claude" } as Session);
    upsertSession({ machine_id: "m1", session_id: "bg1", title: "assistant", status: "working", state: "needs_you", assistant: true, agent: "claude" } as Session);
    Object.assign(getState(), {
      assistant: { machine_id: "hub", main: "c1", background_supervisor: "bg1", conversations: [] },
      interactions: [bgBash],
      answered: {},
    });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const bgCard = card(bgBash.id);
    expect(bgCard).not.toBeNull();
    expect(bgCard!.textContent).toContain(t("assistant-conversations.background_supervisor"));
    expect(button(t("chat-interactions.allow"))).toBeDefined();
    act(() => button(t("chat-interactions.allow"))!.click());
    await flush();
    const resolveCalls = fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${bgBash.id}/resolve`));
    expect(resolveCalls).toHaveLength(1);
    expect(JSON.parse(String((resolveCalls[0][1] as RequestInit).body))).toMatchObject({ allow: true });
  });

  it("does not surface supervisor-held delegating permission cards or leak cards into non-main chats", async () => {
    const heldBgBash = { ...bgBash, id: "ix_bg_held", delegating: true } as Interaction;
    upsertSession({ machine_id: "hub", session_id: "other", title: "Other", status: "idle", state: "idle", assistant: true, agent: "claude" } as Session);
    Object.assign(getState(), {
      assistant: { machine_id: "hub", main: "c1", background_supervisor: "bg1", conversations: [] },
      interactions: [heldBgBash],
      answered: {},
    });
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(card(heldBgBash.id)).toBeNull();

    Object.assign(getState(), { interactions: [bgBash], answered: {} });
    act(() => root.render(<ChatView mid="hub" sid="other" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(card(bgBash.id)).toBeNull();
  });
});
