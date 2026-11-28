// @vitest-environment jsdom
// The mis-tap guard on the chat's waiting cards (#8161): a pointer tap that lands in the guard window of a card that
// appeared after the chat was already open answers nothing; once armed, Deny and Allow take one deliberate tap; the
// keyboard (Enter, digits) is never blocked; a changed option set (the next question in one card) re-arms, and an
// unrelated re-render of the same card does not. What the chat shows when it opens stays exempt, as the engine tests
// (lib/tap-guard.test.tsx) pin directly.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { getState } from "../../lib/store";
import { resetChatTapGuardForTest } from "./card-tap-guard";
import { TAP_GUARD_ATTR } from "../../lib/tap-guard";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const permission = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "git status" }, summary: "git status" } as Interaction;
const opener = { ...permission, id: "p_open" };
const twoQuestions = {
  id: "q_2", machine_id: "m", session_id: "s", kind: "question", tool_name: "AskUserQuestion",
  questions: [
    { question: "first", options: [{ label: "A" }, { label: "B" }] },
    { question: "second", options: [{ label: "C" }, { label: "D" }] },
  ],
} as Interaction;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
/** A real pointer or touch click: a press carries detail 1, unlike a keyboard or programmatic click. */
const tap = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 })));
const cardRoot = () => host.querySelector<HTMLElement>("[data-card-keys]")!;
const press = (k: string) => act(() => cardRoot().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const findButton = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;

/** The chat has been open longer than the guard's open-grace: anything appearing from now on is a new card. */
async function settleChat() {
  await act(async () => root.render(<InteractionCard ix={opener} />));
  act(() => vi.advanceTimersByTime(1100));
}

beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  resetChatTapGuardForTest();
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("a card appearing after the chat opened", () => {
  it("swallows an early pointer tap on Allow and Deny, then takes one deliberate tap once armed", async () => {
    await settleChat();
    await act(async () => root.render(<InteractionCard ix={permission} />));
    const allow = findButton("允许");
    const deny = findButton("拒绝");
    expect(allow.hasAttribute(TAP_GUARD_ATTR)).toBe(true);
    await tap(allow);
    await tap(deny);
    expect(calls).toEqual([]);
    act(() => vi.advanceTimersByTime(450));
    expect(allow.hasAttribute(TAP_GUARD_ATTR)).toBe(false);
    await tap(deny);
    await flush();
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toMatchObject({ allow: false });
  });

  it("takes the first deliberate confirmation tap after opening a code push for review", async () => {
    await settleChat();
    const push = { ...permission, tool_input: { command: "git push" } };
    await act(async () => root.render(<InteractionCard ix={push} />));
    act(() => vi.advanceTimersByTime(450));
    await tap(findButton("允许"));
    expect(calls).toEqual([]);
    const confirm = findButton("确认允许");
    await tap(confirm);
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p_1/resolve", body: { allow: true } }]);
  });

  it("still answers from the keyboard (Enter) while the pointer guard is running", async () => {
    await settleChat();
    await act(async () => root.render(<InteractionCard ix={permission} />));
    act(() => cardRoot().focus());
    await press("Enter");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p_1/resolve", body: { allow: true } }]);
  });

  it("re-arms when the visible option set changes, not when the same card merely re-renders", async () => {
    await settleChat();
    await act(async () => root.render(<InteractionCard ix={permission} />));
    act(() => vi.advanceTimersByTime(300));
    // an unrelated re-render (output streaming above) keeps the window that was running
    await act(async () => root.render(<InteractionCard ix={permission} />));
    act(() => vi.advanceTimersByTime(200)); // 500 ms after the card appeared: the original window is over
    await tap(findButton("允许"));
    await flush();
    expect(calls).toHaveLength(1);

    calls.length = 0;
    await act(async () => root.render(<InteractionCard ix={twoQuestions} />));
    const option = () => [...host.querySelectorAll("button[aria-pressed]")].find((b) => b.textContent?.includes("A"))!;
    expect(option().hasAttribute(TAP_GUARD_ATTR)).toBe(true);
    await tap(option()); // 300 ms into the new card's window would already be swallowed; here it is fresh
    expect(calls).toEqual([]);
    // keyboard answers never wait: a digit picks, Enter advances to the second question
    act(() => cardRoot().focus());
    await press("1");
    await press("Enter");
    await flush();
    const second = () => [...host.querySelectorAll("button[aria-pressed]")].find((b) => b.textContent?.includes("C"))!;
    expect(second).toBeTruthy();
    // the next question is a new option set: its answers restart the guard
    expect(second().hasAttribute(TAP_GUARD_ATTR)).toBe(true);
    await tap(second());
    expect(calls).toEqual([]);
    act(() => vi.advanceTimersByTime(450));
    await tap(second()); // armed: the pointer pick now lands
    await press("Enter"); // Enter submits the two answers, and is never guarded
    await flush();
    expect(calls).toEqual([{ path: "/interactions/q_2/resolve", body: { answers: { first: "A", second: "C" } } }]);
  });
});
