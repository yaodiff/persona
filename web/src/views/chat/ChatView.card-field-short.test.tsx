// @vitest-environment jsdom
// A waiting card's own field on a phone whose keyboard resizes the page (Chrome on Android, #11442): the page turns
// short (under 500 px), and the short fold that puts the cards in one line took away the card being typed into, its
// field and the keyboard with it. While a card's field has the focus the card stays; once it goes the cards fold again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Interaction, Item, Session } from "../../lib/types";

// a touch phone: narrow, no fine pointer; short when the test says so
const media = vi.hoisted(() => {
  const state = { short: false, listeners: new Set<() => void>() };
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (query: string) => ({
    media: query,
    get matches() { return query === "(max-width: 1023px)" || (query === "(max-height: 500px)" && state.short); },
    addEventListener(_: string, l: () => void) { state.listeners.add(l); },
    removeEventListener(_: string, l: () => void) { state.listeners.delete(l); },
  });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  return state;
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

// a worker's question (folded under the chat, #5120): such cards fold on a short page. The chat's own question holds its
// turn and is never folded (lib/turn-asks.ts, #11794; the last test).
const question = {
  id: "ix_question", machine_id: "hub", session_id: "w1", session_title: "Worker", folded_under: "c1", folded_as: "worker", kind: "question", tool_name: "AskUserQuestion",
  questions: [{ question: "Move tomorrow's meeting to the afternoon?", header: "Decide", options: [{ label: "Yes" }, { label: "Not now" }] }],
  created_at: "2020-01-01T00:01:00Z",
} as Interaction;

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const field = () => host.querySelector<HTMLInputElement>(`[data-pending-card] input[aria-label="${t("chat-interactions.custom_answer")}"]`);
/** The cards in one line: the line shows, their box is hidden (the field with it, and the focus it had). */
const folded = () => {
  const line = Boolean(host.querySelector("[data-chat-cards-fold]"));
  const hidden = Boolean(host.querySelector("[data-chat-cards]")?.classList.contains("hidden"));
  expect(line).toBe(hidden);
  return line;
};
const type = (el: HTMLInputElement, text: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, text);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const sendArrow = () => host.querySelector<HTMLButtonElement>(`[data-pending-card] button[aria-label="${t("chat-interactions.send_answer")}"]`);
const resolves = () => fetcher.mock.calls.filter(([url]) => String(url).endsWith(`/interactions/${question.id}/resolve`));
const setShort = (short: boolean) => act(() => {
  media.short = short;
  media.listeners.forEach((l) => l());
});

beforeEach(() => {
  localStorage.clear();
  media.short = false;
  transcript.items = [];
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => window.setTimeout(() => callback(0), 0));
  upsertSession({ machine_id: "hub", session_id: "c1", title: "Persona", status: "working", state: "needs_you", assistant: true, agent: "claude" } as Session);
  upsertSession({ machine_id: "hub", session_id: "w1", title: "Worker", status: "working", state: "needs_you", agent: "claude", folded_under: "c1", folded_as: "worker" } as Session);
  Object.assign(getState(), { interactions: [question], answered: {} });
  fetcher = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("a waiting card's own field on a page the keyboard made short", () => {
  it("keeps the card while its field has the focus, and folds the cards into one line again after", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(folded()).toBe(false);
    const input = field()!;
    expect(input).not.toBeNull();

    act(() => input.focus());
    setShort(true); // the keyboard came up and the page got shorter
    await flush();
    expect(folded()).toBe(false);
    expect(field()).toBe(input);
    expect(document.activeElement).toBe(input);

    act(() => input.blur());
    await flush();
    expect(folded()).toBe(true);
  });

  it("keeps the card while the focus moves to its own send arrow, so the tap answers it", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const input = field()!;
    act(() => input.focus());
    await type(input, "Next Monday");
    setShort(true);
    await flush();
    // the tap focuses the arrow before its click: folded in between, the click would land on nothing
    act(() => sendArrow()!.focus());
    await flush();
    expect(document.activeElement).toBe(sendArrow());
    expect(folded()).toBe(false);
    act(() => sendArrow()!.click());
    await flush();
    expect(resolves()).toHaveLength(1);
  });

  it("folds again once the focus leaves the card", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    const input = field()!;
    act(() => input.focus());
    await type(input, "Next Monday");
    setShort(true);
    act(() => sendArrow()!.focus());
    await flush();
    expect(folded()).toBe(false);
    act(() => host.querySelector<HTMLTextAreaElement>("textarea[data-composer-input]")!.focus());
    await flush();
    expect(folded()).toBe(true);
  });

  it("does not count a field in a card outside this chat's own stack (the Assistant tray's)", async () => {
    media.short = true;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(folded()).toBe(true);
    const tray = document.createElement("div");
    tray.setAttribute("data-pending-card", "");
    const other = document.createElement("input");
    tray.append(other);
    host.querySelector("[data-chat-dock]")!.append(tray);
    act(() => other.focus());
    await flush();
    expect(folded()).toBe(true);
    act(() => other.blur());
    tray.remove();
  });

  it("folds as before when the page is short and nothing in a card has the focus", async () => {
    media.short = true;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    expect(folded()).toBe(true);
  });

  it("opens the chat's own question by default on a short page", async () => {
    const own = { ...question, id: "ix_own", session_id: "c1", session_title: "Persona", folded_under: undefined, folded_as: undefined, tool_name: "ask_user" } as Interaction;
    Object.assign(getState(), { interactions: [own, question], answered: {} });
    media.short = true;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant onToggleSidebar={() => {}} />));
    await flush();
    // the worker's folds into the one line; the chat's own stands open above the capsules with its answers
    expect(folded()).toBe(true);
    const open = host.querySelector(`[data-decision-pager] [data-card="${own.id}"]`);
    expect(open).not.toBeNull();
    expect(open!.querySelector("[data-pending-card] button")).not.toBeNull();
  });
});
