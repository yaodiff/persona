// @vitest-environment jsdom
// Answering a waiting card from the keyboard (#1720): the shortcut's focus lands on the card, digits pick its options,
// Enter runs its main action (never a deny — a focused Deny button's Enter stays the button's own, which jsdom does not
// fire, so the card staying silent is exactly the yield), Esc hands focus back to the composer without answering, and
// the key hints (option digits, the Enter badge) show only while the card holds keyboard focus. Typing in the card's
// own fields and an IME composition keep their keys. The hub is faked (apiOp), as the free-text test's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
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
import { t } from "../../i18n";
import { Collapse } from "../../ui/atoms";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(() => {
  // jsdom gives elements no boxes; without this the composer would count as hidden and Esc would not find it
  Element.prototype.getClientRects = function () {
    return [{}] as unknown as DOMRectList;
  };
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const cardRoot = () => host.querySelector<HTMLElement>("[data-card-keys]")!;
/** A keydown on the card's root, as a press with the card focused is; `at` picks another element inside it. */
const press = (k: string, at?: string, mods: Partial<{ ctrlKey: boolean; altKey: boolean; shiftKey: boolean; isComposing: boolean }> = {}) =>
  act(() => {
    (at ? host.querySelector(at)! : cardRoot()).dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...mods }));
  });

const question = JSON.parse(
  '{"id":"q_1","machine_id":"m","session_id":"s","kind":"question","tool_name":"AskUserQuestion",' +
    '"questions":[{"question":"哪个框架？","header":"测试","multi_select":false,"options":[' +
    '{"label":"Swift Testing","description":"新代码统一用"},{"label":"XCTest","description":"和现有的一致"}]}]}',
) as Interaction;
const threeQuestions = JSON.parse(
  '{"id":"q_3","machine_id":"m","session_id":"s","kind":"question","tool_name":"AskUserQuestion",' +
    '"questions":[{"question":"第一题","multi_select":false,"options":[{"label":"A"},{"label":"B"}]},' +
    '{"question":"第二题","multi_select":false,"options":[{"label":"C"}]},' +
    '{"question":"第三题","multi_select":false,"options":[{"label":"D"}]}]}',
) as Interaction;
const permission = JSON.parse(
  '{"id":"p_1","machine_id":"m","session_id":"s","kind":"permission","tool_name":"Bash","tool_input":{"command":"npm test"},"summary":"跑测试"}',
) as Interaction;
const plan = JSON.parse(
  '{"id":"pl_1","machine_id":"m","session_id":"s","kind":"plan","tool_name":"ExitPlanMode","plan":"## 计划\\n1. 第一步"}',
) as Interaction;

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("a card answering from the keyboard", () => {
  it("Enter allows a permission: the main action, never a deny", async () => {
    act(() => root.render(<InteractionCard ix={permission} />));
    act(() => cardRoot().focus());
    await press("Enter");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/p_1/resolve", body: { allow: true } }]);
  });

  it("Enter on a focused Deny button is the button's, not the card's: the card does not answer for it", async () => {
    act(() => root.render(<InteractionCard ix={permission} />));
    const deny = [...host.querySelectorAll("button")].find((b) => b.textContent === "拒绝")!;
    act(() => deny.focus());
    await press("Enter", "button");
    await flush();
    expect(calls).toEqual([]);
  });

  it("a digit submits a single-question, single-choice answer right away", async () => {
    act(() => root.render(<InteractionCard ix={question} />));
    act(() => cardRoot().focus());
    await press("1");
    const picked = [...host.querySelectorAll("button[aria-pressed]")].find((b) => b.textContent?.includes("Swift Testing"))!;
    expect(picked.getAttribute("aria-pressed")).toBe("true");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/q_1/resolve", body: { answers: { "哪个框架？": "Swift Testing" } } }]);
  });

  it("Enter approves a plan with edits", async () => {
    act(() => root.render(<InteractionCard ix={plan} />));
    act(() => cardRoot().focus());
    await press("Enter");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/pl_1/resolve", body: { approve: true, mode: "acceptEdits" } }]);
  });

  it("Esc goes back to the composer and answers nothing", async () => {
    const composer = document.createElement("main");
    composer.innerHTML = '<div data-chat-dock><textarea data-composer-input></textarea></div>';
    document.body.append(composer);
    act(() => root.render(<InteractionCard ix={permission} />));
    act(() => cardRoot().focus());
    await press("Escape");
    await flush();
    expect(document.activeElement).toBe(composer.querySelector("textarea"));
    expect(calls).toEqual([]);
  });

  it("a digit then Enter right away advances one question, not two (the pick's own slide is cancelled)", async () => {
    vi.useFakeTimers();
    try {
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => cardRoot().focus());
      await press("1");
      await press("Enter");
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第二题");
      expect(host.textContent).not.toContain("第三题");
      // unanswered as it is, Enter sends nothing
      await press("Enter");
      await flush();
      expect(calls).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a card this page cannot answer (a disabled fieldset) takes no keys: nothing is picked, nothing sent", async () => {
    Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", source_kind: "codex_history" } as Session] } });
    act(() => root.render(<InteractionCard ix={question} />));
    expect(host.querySelector("fieldset[disabled]")).toBeTruthy();
    act(() => cardRoot().focus());
    await press("1");
    await press("Enter");
    await flush();
    for (const b of host.querySelectorAll("button[aria-pressed]")) expect(b.getAttribute("aria-pressed")).toBe("false");
    expect(calls).toEqual([]);
  });

  it("keeps typing and IME composition to the fields: a digit in the card's input picks nothing", async () => {
    act(() => root.render(<InteractionCard ix={question} />));
    const field = host.querySelector("input")!;
    act(() => field.focus());
    await press("1", "input");
    await press("1", "input", { isComposing: true });
    await flush();
    for (const b of host.querySelectorAll("button[aria-pressed]")) expect(b.getAttribute("aria-pressed")).toBe("false");
    expect(calls).toEqual([]);
  });

  it("shows the key hints only while the card holds keyboard focus", async () => {
    act(() => root.render(<InteractionCard ix={question} />));
    const hints = () => [...host.querySelectorAll("button[aria-pressed]")].map((b) => b.querySelectorAll("kbd, [data-key-hint]").length);
    expect(hints().every((n) => n === 0)).toBe(true);
    act(() => cardRoot().focus());
    await press("2"); // a key first, so the hints' state has gone through a render
    expect(host.querySelectorAll("button[aria-pressed]")[0]!.querySelectorAll("[data-key-hint]").length).toBe(1); // the digit badge
    expect(host.querySelector("[data-card-actions] kbd")).toBeTruthy(); // the Enter badge by the send arrow
    act(() => cardRoot().blur());
    await act(async () => { await Promise.resolve(); });
    expect(hints().every((n) => n === 0)).toBe(true);
    expect(host.querySelector("[data-card-actions] kbd")).toBeFalsy();
  });

  it("window digit shortcuts stand down under disabled fieldset, hidden/inert containers, overlays, fixed side panel, other focused card, IME, or nested contentEditable", async () => {
    const multi = {
      id: "q_multi_win", machine_id: "m", session_id: "s", kind: "question", tool_name: "AskUserQuestion",
      questions: [{ question: "多选？", multi_select: true, options: [{ label: "Alpha" }, { label: "Beta" }] }],
    } as Interaction;
    const multi2 = {
      id: "q_multi_win_2", machine_id: "m", session_id: "s", kind: "question", tool_name: "AskUserQuestion",
      questions: [{ question: "多选二？", multi_select: true, options: [{ label: "Gamma" }, { label: "Delta" }] }],
    } as Interaction;
    const pressWindow = (key: string, target: EventTarget = document.body, init: KeyboardEventInit & { keyCode?: number } = {}) => {
      const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
      if (init.keyCode !== undefined) Object.defineProperty(ev, "keyCode", { value: init.keyCode });
      act(() => { target.dispatchEvent(ev); });
    };

    // 1. Disabled fieldset while canApprove is true (e.g. remounted pending lock): global window digit must not pick or submit.
    act(() => root.render(<fieldset disabled><InteractionCard ix={question} /></fieldset>));
    pressWindow("1");
    await flush();
    expect(calls).toEqual([]);
    expect(host.querySelectorAll("button[aria-pressed='true']")).toHaveLength(0);

    // 2. Sibling open dialog / alertdialog / menu / native <dialog open> blocks the card behind it, while closing/hidden overlays do not.
    act(() => root.render(<InteractionCard ix={question} />));
    for (const make of [
      () => Object.assign(document.createElement("div"), { role: "dialog" }),
      () => Object.assign(document.createElement("div"), { role: "alertdialog" }),
      () => Object.assign(document.createElement("div"), { role: "menu" }),
      () => { const d = document.createElement("dialog"); d.setAttribute("open", ""); return d; },
    ]) {
      const ov = make();
      document.body.append(ov);
      try {
        pressWindow("1");
        await flush();
        expect(calls).toEqual([]);
      } finally {
        ov.remove();
      }
    }

    // Closing overlay (data-closing) does not block.
    const closingDialog = document.createElement("div");
    closingDialog.setAttribute("role", "dialog");
    closingDialog.setAttribute("data-closing", "");
    document.body.append(closingDialog);
    try {
      pressWindow("1");
      await flush();
      expect(calls).toHaveLength(1);
    } finally {
      closingDialog.remove();
    }
    calls.length = 0;

    // 3. IME composition (isComposing and Safari keyCode 229), non-single-digit strings ("10", "!"), and nested contentEditable descendant.
    act(() => root.render(null));
    act(() => root.render(<InteractionCard ix={question} />));
    pressWindow("1", document.body, { isComposing: true });
    pressWindow("1", document.body, { keyCode: 229 });
    pressWindow("10");
    pressWindow("!", document.body, { shiftKey: true });
    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    const inner = document.createElement("span");
    editable.append(inner);
    document.body.append(editable);
    try {
      pressWindow("1", inner);
      await flush();
      expect(calls).toEqual([]);
    } finally {
      editable.remove();
    }

    // 4. Collapsed (inert) Collapse container, mid-transition Collapse (open=true -> open=false, gridTemplateRows: 0fr before inert settles), and display:none ancestor (lg:hidden SidePanel).
    act(() => root.render(<Collapse open={false}><InteractionCard ix={question} /></Collapse>));
    pressWindow("1");
    await flush();
    expect(calls).toEqual([]);

    act(() => root.render(<Collapse open={true}><InteractionCard ix={question} /></Collapse>));
    act(() => root.render(<Collapse open={false}><InteractionCard ix={question} /></Collapse>));
    expect(host.querySelector("[inert]")).toBeNull();
    pressWindow("1");
    await flush();
    expect(calls).toEqual([]);

    act(() => root.render(<aside data-side-panel="" style={{ display: "none" }}><InteractionCard ix={question} /></aside>));
    pressWindow("1");
    await flush();
    expect(calls).toEqual([]);

    // Non-portal dialog nested inside a hidden (display:none) SidePanel must not lock a visible <main> QuestionCard.
    act(() => root.render(<InteractionCard ix={question} />));
    const hiddenSide = document.createElement("aside");
    hiddenSide.setAttribute("data-side-panel", "");
    hiddenSide.style.display = "none";
    const nestedDialog = document.createElement("div");
    nestedDialog.setAttribute("role", "dialog");
    hiddenSide.append(nestedDialog);
    document.body.append(hiddenSide);
    try {
      // Also verify AZERTY-style Shift+digit (key === "1", shiftKey: true) works.
      pressWindow("1", document.body, { shiftKey: true });
      await flush();
      expect(calls).toHaveLength(1);
    } finally {
      hiddenSide.remove();
    }
    calls.length = 0;

    // 5. Visible fixed <aside data-side-panel> covering <main> blocks the covered <main> card, while relative desktop SidePanel and a dialog-hosted card (z-40 above fixed SidePanel z-30) still work.
    act(() => root.render(null));
    act(() => root.render(<InteractionCard ix={question} />));
    const sidePanel = document.createElement("aside");
    sidePanel.setAttribute("data-side-panel", "");
    sidePanel.style.position = "fixed";
    document.body.append(sidePanel);
    try {
      pressWindow("1");
      await flush();
      expect(calls).toEqual([]);
      // A QuestionCard inside a modal dialog above the fixed SidePanel still accepts digits.
      act(() => root.render(<div role="dialog"><InteractionCard ix={question} /></div>));
      pressWindow("1");
      await flush();
      expect(calls).toHaveLength(1);
      calls.length = 0;
      act(() => root.render(<InteractionCard ix={question} />));
      sidePanel.style.position = "relative";
      pressWindow("1");
      await flush();
      expect(calls).toHaveLength(1);
    } finally {
      sidePanel.remove();
    }
    calls.length = 0;

    // 6. Card rendered INSIDE a dialog works when alone, but is blocked when a sibling menu/dialog opens over it.
    act(() => root.render(<div role="dialog"><InteractionCard ix={question} /></div>));
    const overMenu = document.createElement("div");
    overMenu.setAttribute("role", "menu");
    document.body.append(overMenu);
    try {
      pressWindow("1");
      await flush();
      expect(calls).toEqual([]);
    } finally {
      overMenu.remove();
    }
    pressWindow("1");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/q_1/resolve", body: { answers: { "哪个框架？": "Swift Testing" } } }]);

    // 7. Two visible cards: focusing a button inside Card 2 and pressing "2" toggles Card 2 only (Card 1 must not hijack).
    calls.length = 0;
    act(() => root.render(<><InteractionCard ix={multi} /><InteractionCard ix={multi2} /></>));
    const cards = [...host.querySelectorAll<HTMLElement>("[data-card]")];
    const card1Opts = [...cards[0]!.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")];
    const card2Opts = [...cards[1]!.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")];
    act(() => card2Opts[0]!.click());
    act(() => card2Opts[0]!.focus());
    pressWindow("2", card2Opts[0]!);
    expect(card1Opts.map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "false"]);
    expect(card2Opts.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "true"]);
  });

  it("cancels the auto-advance slide timer across rapid re-pick, footer navigation, custom focus/typing/Enter, and ix.id swap", async () => {
    vi.useFakeTimers();
    try {
      const option = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent?.includes(label))!;
      const customInput = () => host.querySelector<HTMLInputElement>(`input[aria-label="${t("chat-interactions.custom_answer")}"]`)!;

      // 1. Rapid re-pick A -> B within 380ms advances once (to Q2), not twice (skipping Q2 to Q3).
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => vi.advanceTimersByTime(150));
      act(() => option("B").click());
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第二题");
      expect(host.textContent).not.toContain("第三题");

      // 2. Clicking footer Next button within 380ms cancels the pending slide timer.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.next_question")}"]`)!.click());
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第二题");
      expect(host.textContent).not.toContain("第三题");

      // 3. Clicking a question dot within 380ms cancels the pending slide timer.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.question_n", { n: 2 })}"]`)!.click());
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第二题");
      expect(host.textContent).not.toContain("第三题");

      // 4. Focusing the custom input (even before typing) cancels the slide timer and stays on Q1.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => vi.advanceTimersByTime(100));
      act(() => customInput().focus());
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第一题");

      // 5. Typing in the custom input within 380ms cancels the slide timer and stays on Q1.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => vi.advanceTimersByTime(100));
      const field = customInput();
      act(() => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "自定义");
        field.dispatchEvent(new Event("input", { bubbles: true }));
      });
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第一题");
      expect(customInput().value).toBe("自定义");

      // 6. Pressing Enter in custom input while slide timer is armed advances once (to Q2), not twice.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => customInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第二题");
      expect(host.textContent).not.toContain("第三题");

      // 7. Swapping ix.id in place cancels any pending slide timer from the previous question.
      const followUp = {
        ...threeQuestions,
        id: "q_3_next",
        questions: [
          { question: "新第一题", multi_select: false, options: [{ label: "X" }] },
          { question: "新第二题", multi_select: false, options: [{ label: "Y" }] },
        ],
      } as Interaction;
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => root.render(<InteractionCard ix={followUp} />));
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("新第一题");
      expect(host.textContent).not.toContain("新第二题");

      // 8. Hand back (onDismiss) cancels any pending slide timer.
      act(() => root.render(null));
      act(() => root.render(<InteractionCard ix={threeQuestions} />));
      act(() => option("A").click());
      act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.hand_back")}"]`)!.click());
      act(() => vi.advanceTimersByTime(400));
      expect(host.textContent).toContain("第一题");
      expect(host.textContent).not.toContain("第二题");
    } finally {
      vi.useRealTimers();
    }
  });
});
