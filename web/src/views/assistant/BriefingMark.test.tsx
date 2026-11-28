// @vitest-environment jsdom
// A decision held for the briefing (#5803): the to-do's ask says `for_briefing`, and its ask line (a To-dos row, a task's card, the task panel's
// banner), its page and its decision card in Needs you say it comes with the next briefing, with what that means in one line. Nothing is
// drawn for an ask without the field (an older hub, a decision that was pushed).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { initI18n } from "../../i18n";
import { reset } from "../../lib/store";
import type { Todo, TodoAsk } from "../../lib/todos";
import { TodoAskLine } from "../TodoAsk";
import { HeldForBriefing } from "../Todos";
import { TaskDecision } from "./TaskDecision";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = "2026-11-10T08:00:00Z";
const HINT = "This decision can wait, so nothing was pushed for it. It comes with your next briefing, and you can answer it here any time.";
const decision: TodoAsk = {
  outcome: "decide", verdict: "Needs your decision: keep the example cache or replace it", recommend: "Keep it", asked_at: at,
  question: "Keep the example cache or replace it?", why_owner: "priority",
  options: [{ label: "Keep it", leads_to: "nothing changes this week" }, { label: "Replace it", leads_to: "two more days of work" }],
};
const todo = (ask?: TodoAsk): Todo => ({ id: "t1", title: "Example cache", created_at: at, updated_at: at, supervision: { supervisor: "c1", state: "needs_user", reviews: [{ at, verdict: "needs_user" }] }, decision_review: { review_at: at }, ask }) as Todo;
const held = { ...decision, for_briefing: true };

let host: HTMLDivElement;
let root: Root;
const render = async (el: React.ReactNode) => {
  await act(async () => root.render(el));
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const marks = () => [...host.querySelectorAll<HTMLElement>("[data-for-briefing]")];

beforeEach(async () => {
  await initI18n("en");
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { headers: { "Content-Type": "application/json" } })));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

describe("on the to-do's ask line (a To-dos row, a task's card, the task panel's banner) and page", () => {
  it("only a to-do whose ask says for_briefing: true carries it", async () => {
    for (const ask of [decision, { ...decision, for_briefing: false }, { ...decision, for_briefing: "yes" as unknown as boolean }, undefined, { ...held, asked_at: "" }]) {
      await render(<><TodoAskLine todo={todo(ask)} /><HeldForBriefing todo={todo(ask)} /></>);
      expect(marks()).toHaveLength(0);
    }
  });

  it("the ask line has a quiet mark whose hover says what it means, under the verdict and the recommendation", async () => {
    await render(<TodoAskLine todo={todo(held)} className="px-4" />);
    expect(marks()).toHaveLength(1);
    expect(marks()[0].dataset.forBriefing).toBe("mark");
    expect(marks()[0].textContent).toBe("With the next briefing");
    expect(marks()[0].title).toBe(HINT);
    // inside the line (so inside the task panel's banner band), after what the line already said
    const lineEl = host.querySelector<HTMLElement>('[data-testid="todo-ask"]')!;
    expect(lineEl.contains(marks()[0])).toBe(true);
    expect(marks()[0].previousElementSibling!.textContent).toContain("Keep it");
  });

  it("the page writes the definition out", async () => {
    await render(<HeldForBriefing todo={todo(held)} />);
    expect(marks()).toHaveLength(1);
    expect(marks()[0].dataset.forBriefing).toBe("caption");
    expect(marks()[0].textContent).toBe(`With the next briefing · ${HINT}`);
  });
});

describe("on the decision's card in Needs you", () => {
  it("says it comes with the next briefing and what that means; a pushed decision's card says nothing", async () => {
    await render(<TaskDecision todo={todo(held)} rowKey="task:t1" />);
    expect(marks()).toHaveLength(1);
    expect(marks()[0].textContent).toBe(`With the next briefing · ${HINT}`);
    expect(marks()[0].closest("[data-decision-row]")).not.toBeNull();
    // its options are answerable all the same
    expect([...host.querySelectorAll("[data-decision-option]")].map((o) => o.textContent)).toEqual(["Keep it", "Replace it"]);
    await render(<TaskDecision todo={todo(decision)} rowKey="task:t1" />);
    expect(marks()).toHaveLength(0);
  });

  it("a held decision without options on its row keeps the card it had, with the caption", async () => {
    const plain = { ...held, options: undefined };
    await render(<TaskDecision todo={todo(plain)} rowKey="task:t1" />);
    expect(host.querySelector('[data-brief-row="accept"]')).not.toBeNull();
    expect(marks()).toHaveLength(1);
    expect(marks()[0].dataset.forBriefing).toBe("caption");
  });

  it("is in Simplified Chinese and Japanese", async () => {
    await initI18n("zh-Hans");
    await render(<TaskDecision todo={todo(held)} rowKey="task:t1" />);
    expect(marks()[0].textContent).toContain("随下次简报送达");
    expect(marks()[0].textContent).toContain("未发送推送提醒");
    act(() => root.unmount());
    root = createRoot(host);
    await initI18n("ja");
    await render(<TaskDecision todo={todo(held)} rowKey="task:t1" />);
    expect(marks()[0].textContent).toContain("次のブリーフィングで届きます");
  });
});
