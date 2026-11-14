// @vitest-environment jsdom
// A waiting card taller than the room left for it in the dock keeps its answers in view at the box's bottom edge, while
// what they answer is out of view above them (audit item 10): the answer row then says what it answers (the card's title
// and its command), a click on that brings the whole card into view, and the stack's first card says how many it holds.
// The browser check of the real layout is web/e2e/pending-cards-cut.mjs.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// Each card's observer (its end mark and its title), driven by the test through `say`; `options` is what it was made with.
type Watcher = { els: Element[]; call: (entries: unknown[]) => void; options?: IntersectionObserverInit };
const io = vi.hoisted(() => ({ watchers: [] as Watcher[] }));
vi.hoisted(() => {
  (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = class {
    private me: Watcher;
    constructor(call: (entries: unknown[]) => void, options?: IntersectionObserverInit) {
      this.me = { els: [], call, options };
      io.watchers.push(this.me);
    }
    observe(el: Element) { this.me.els.push(el); }
    unobserve() {}
    disconnect() { io.watchers = io.watchers.filter((w) => w !== this.me); }
    takeRecords() { return []; }
  };
});

import { PendingCards } from "./Interactions";
import { cutBelow } from "./card-cut";
import { getState, reset, upsertSession } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const question = { id: "q_1", machine_id: "m", session_id: "s", kind: "question", questions: [{ question: "Which test framework?", options: [{ label: "Swift Testing" }, { label: "XCTest" }] }] } as Interaction;
const permission = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "swift test" } } as Interaction;

let host: HTMLDivElement;
let root: Root;
const card = (id: string) => host.querySelector(`[data-card="${id}"]`)!;
const watcher = (id: string) => io.watchers.find((w) => w.els.length > 0 && card(id).contains(w.els[0]))!;
/** The card's end out of view below its answer row (`below`) or not; its title wholly in view (`title`, the default) or not. */
const say = (id: string, below: boolean, title = true) => act(() => {
  const [end, head] = watcher(id).els;
  watcher(id).call([
    { target: end, isIntersecting: !below, intersectionRatio: below ? 0 : 1, boundingClientRect: { top: below ? 900 : 300 }, rootBounds: { bottom: 700 } },
    { target: head, isIntersecting: title, intersectionRatio: title ? 1 : 0, boundingClientRect: { top: 200 }, rootBounds: { bottom: 700 } },
  ]);
});
const draw = (cards: Interaction[], narrow = false) => act(() => root.render(<PendingCards interactions={cards} answered={[]} narrow={narrow} keyboard={false} onDismissKeyboard={() => {}} />));

beforeEach(() => {
  io.watchers = [];
  upsertSession({ machine_id: "m", session_id: "s", title: "Demo", state: "needs_you" } as Session);
  Object.assign(getState(), { interactions: [question, permission], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("a waiting card cut off below the dock's box", () => {
  it("is told apart from one in view or above it", () => {
    expect(cutBelow(false, { top: 720 }, 700)).toBe(true);
    expect(cutBelow(true, { top: 680 }, 700)).toBe(false);
    expect(cutBelow(false, { top: 40 }, 700)).toBe(false); // scrolled up past it: its row is not at the bottom edge
  });

  it("says in its answer row what the row answers, its title and command, and nothing while it is in view", () => {
    draw([question, permission]);
    expect(host.querySelector("[data-card-answers]")).toBeNull();
    say("p_1", true);
    const line = card("p_1").querySelector<HTMLButtonElement>("[data-card-answers]")!;
    expect(line.closest("[data-card-actions]")).not.toBeNull();
    expect(line.textContent).toBe(`${t("chat-interactions.permission_title", { tool: "Bash" })} · swift test`);
    // a pointer reads the whole of it, however much the line truncates
    expect(line.title).toBe(line.textContent);
    expect(line.getAttribute("aria-label")).toBe(`${t("chat-interactions.show_card")}: ${line.textContent}`);
    // beside the answers it is about, which stay as they were
    const answers = [...card("p_1").querySelectorAll("[data-card-actions] button")].map((b) => b.textContent);
    expect(answers).toEqual(expect.arrayContaining([t("chat-interactions.deny"), t("chat-interactions.allow")]));
    say("p_1", false);
    expect(host.querySelector("[data-card-answers]")).toBeNull();
  });

  it("brings the whole card into view on a click on that line, with the focus on the card", () => {
    draw([question, permission]);
    say("p_1", true);
    const scrolled = vi.fn();
    const whole = card("p_1").querySelector<HTMLElement>("[data-pending-card]")!;
    whole.scrollIntoView = scrolled;
    act(() => card("p_1").querySelector<HTMLButtonElement>("[data-card-answers]")!.click());
    expect(scrolled).toHaveBeenCalledWith({ block: "nearest" });
    expect(document.activeElement).toBe(whole);
  });

  it("has the stack's first card say how many the stack holds while any is cut off, and only then", () => {
    draw([question, permission]);
    expect(host.querySelector("[data-chat-cards-count]")).toBeNull();
    say("p_1", true);
    const count = host.querySelector("[data-chat-cards-count]")!;
    expect(count.textContent).toBe(t("chat-interactions.pending", { count: 2 }));
    expect(card("q_1").contains(count)).toBe(true);
    say("p_1", false);
    expect(host.querySelector("[data-chat-cards-count]")).toBeNull();
    // a lone card has no stack to count
    draw([permission]);
    say("p_1", true);
    expect(host.querySelector("[data-chat-cards-count]")).toBeNull();
    expect(host.querySelector("[data-card-answers]")).not.toBeNull();
  });

  it("measures the cut above the answer row stuck at the box's bottom edge: what is under that row is out of view too", () => {
    const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get(this: HTMLElement) { return this.hasAttribute("data-card-actions") ? 46 : 0; } });
    try {
      draw([permission]);
      expect(watcher("p_1").options?.rootMargin).toBe("0px 0px -46px 0px");
    } finally {
      if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    }
  });

  it("does not repeat a question whose title is in view: only once the question is out of view as well", () => {
    draw([question, permission]);
    say("q_1", true);
    expect(card("q_1").querySelector("[data-card-answers]")).toBeNull();
    // the stack still says it goes on below
    expect(host.querySelector("[data-chat-cards-count]")?.textContent).toBe(t("chat-interactions.pending", { count: 2 }));
    say("q_1", true, false);
    expect(card("q_1").querySelector("[data-card-answers]")?.textContent).toBe("Which test framework?");
    // a tool approval's line says its command, which its title does not, title in view or not
    say("p_1", true);
    expect(card("p_1").querySelector("[data-card-answers]")).not.toBeNull();
  });

  it("keeps the next card mounted, with what was typed in it, when the card above it goes", () => {
    draw([permission, question]);
    const next = card("q_1");
    const field = next.querySelector<HTMLInputElement>(`input[aria-label="${t("chat-interactions.custom_answer")}"]`)!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "Quick");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => field.focus());
    draw([question]);
    expect(card("q_1")).toBe(next);
    expect(next.querySelector<HTMLInputElement>(`input[aria-label="${t("chat-interactions.custom_answer")}"]`)?.value).toBe("Quick");
    expect(document.activeElement).toBe(field);
  });

  it("says how many a narrow stack holds once it is opened in full, and leaves a folded one to its N more row", () => {
    draw([question, permission], true);
    say("p_1", true);
    // folded: the first card only, the rest in the row under the box
    expect(host.querySelector("[data-chat-cards-count]")).toBeNull();
    expect(host.querySelector("[data-chat-cards-fold]")).not.toBeNull();
    act(() => host.querySelector<HTMLButtonElement>("[data-chat-cards-fold]")!.click());
    expect(host.querySelector("[data-chat-cards-count]")?.textContent).toBe(t("chat-interactions.pending", { count: 2 }));
    expect(card("q_1").contains(host.querySelector("[data-chat-cards-count]"))).toBe(true);
  });
});
