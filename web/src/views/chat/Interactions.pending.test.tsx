// @vitest-environment jsdom
// An answer on its way holds its card (#2793): a Permission card answered while the hub is away, then unmounted (the
// chat left) and mounted again, keeps every answer disabled and says which answer is waiting to go, until it ends.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const sent = vi.hoisted(() => ({ calls: [] as unknown[], finish: (_: unknown) => {} }));
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn((_path: string, init: { json: unknown }) => {
    sent.calls.push(init.json);
    return new Promise((done) => { sent.finish = done; });
  }),
}));
import { InteractionCard } from "./Interactions";
import { PENDING_LINE_MS } from "../../lib/card-answer";
import { getState, reset, upsertSession } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const permission = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "git status" } } as Interaction;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("[data-card] button")];
const button = (label: string) => buttons().find((b) => b.textContent === label)!;
const locked = (b: HTMLButtonElement) => b.disabled || !!b.closest("fieldset[disabled]");

beforeEach(() => {
  sent.calls.length = 0;
  upsertSession({ machine_id: "m", session_id: "s", title: "Demo", state: "needs_you" } as Session);
  Object.assign(getState(), { interactions: [permission], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("an answer on its way", () => {
  it("keeps the card's answers disabled across a remount and says which answer waits to go", async () => {
    act(() => root.render(<InteractionCard ix={permission} />));
    expect(buttons().some(locked)).toBe(false);
    expect(host.querySelector("[data-card-pending]")).toBeNull();
    act(() => button(t("chat-interactions.allow")).click());
    // the chat is left and opened again: the card mounts afresh, its own busy state gone
    act(() => root.render(null));
    act(() => root.render(<InteractionCard ix={permission} />));
    const answers = [t("chat-interactions.deny"), t("chat-interactions.always_allow"), t("chat-interactions.allow"), t("chat-interactions.hand_back")];
    expect(answers.map((l) => locked(button(l)))).toEqual([true, true, true, true]);
    // the hub is not connected in this test: it goes once it is back
    expect(host.querySelector("[data-card-pending]")?.textContent).toBe(t("chat-interactions.pending_offline", { answer: t("lib-answered.allowed") }));
    act(() => button(t("chat-interactions.deny")).click());
    expect(sent.calls).toEqual([{ allow: true }]);
    // refused for good (not a gap): the card can be answered again
    await act(async () => sent.finish(Promise.reject(new Error("Synthetic refusal"))));
    await flush();
    expect(host.querySelector("[data-card-pending]")).toBeNull();
    expect(locked(button(t("chat-interactions.deny")))).toBe(false);
  });

  it("with the hub connected, says it only once the answer takes a moment: one delivered at once shows no line", async () => {
    vi.useFakeTimers();
    Object.assign(getState(), { conn: "online" });
    act(() => root.render(<InteractionCard ix={permission} />));
    act(() => button(t("chat-interactions.allow")).click());
    expect(locked(button(t("chat-interactions.deny")))).toBe(true);
    expect(host.querySelector("[data-card-pending]")).toBeNull();
    act(() => vi.advanceTimersByTime(PENDING_LINE_MS));
    expect(host.querySelector("[data-card-pending]")?.textContent).toBe(t("chat-interactions.pending_sending", { answer: t("lib-answered.allowed") }));
    vi.useRealTimers();
    await act(async () => sent.finish(Promise.reject(new Error("Synthetic refusal"))));
    await flush();
  });

  it("blocks QuestionCard global digit shortcuts across a remount while an offline answer is pending", async () => {
    const question = {
      id: "q_pending", machine_id: "m", session_id: "s", kind: "question", tool_name: "AskUserQuestion",
      questions: [{ question: "Which?", multi_select: false, options: [{ label: "One" }, { label: "Two" }] }],
    } as Interaction;
    Object.assign(getState(), { interactions: [question], answered: {} });
    act(() => root.render(<InteractionCard ix={question} />));
    act(() => host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")[0]!.click());
    expect(sent.calls).toEqual([{ answers: { "Which?": "One" } }]);
    // Leave and reopen the chat: QuestionCard remounts with busy=false while fieldset stays disabled.
    act(() => root.render(null));
    act(() => root.render(<InteractionCard ix={question} />));
    expect(host.querySelector("fieldset[disabled]")).toBeTruthy();
    act(() => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "2", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")[1]!.getAttribute("aria-pressed")).toBe("false");
    expect(sent.calls).toEqual([{ answers: { "Which?": "One" } }]);
    await act(async () => sent.finish(Promise.reject(new Error("Synthetic refusal"))));
    await flush();
  });
});
