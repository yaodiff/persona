// @vitest-environment jsdom
// The cards above the composer as one slot: the first waiting card with "1 / 3" on it, the next taking its place as it
// is answered, and what was answered as one line that goes 3 s after the last answer (card-stack.ts). Fake timers only.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../../lib/ops", () => ({ apiOp: vi.fn(async () => ({})) }));
import { PendingCards } from "./Interactions";
import { getState, reset } from "../../lib/store";
import type { AnsweredCard } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = new Date("2026-01-01T00:00:00Z").getTime();
const perm = (id: string) => ({ id, machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: `run ${id}` } }) as Interaction;
const ids = (...names: string[]) => names.map(perm);
const ans = (id: string, allow: boolean, at: number): AnsweredCard => ({ ix: perm(id), resolution: { allow }, at });

let host: HTMLDivElement;
let root: Root;
type Props = Partial<Parameters<typeof PendingCards>[0]>;
const render = (p: Props = {}) =>
  act(() => root.render(<PendingCards interactions={[]} answered={[]} narrow={false} keyboard={false} onDismissKeyboard={() => {}} {...p} />));
const cardsIn = () => [...host.querySelectorAll("[data-card]")].map((e) => e.getAttribute("data-card"));
const chip = () => host.querySelector<HTMLButtonElement>("[data-chat-cards-position]");
const summary = () => host.querySelector<HTMLElement>("[data-answered-summary]");
const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 })));
const buttonWith = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.useRealTimers();
});

describe("the slot", () => {
  it("shows the first waiting card with its place, and the next one as it goes", () => {
    render({ interactions: ids("a", "b", "c") });
    expect(cardsIn()).toEqual(["a"]);
    expect(chip()?.textContent).toBe("1 / 3");

    render({ interactions: ids("b", "c"), answered: [ans("a", true, T0)] });
    expect(cardsIn()).toEqual(["b"]);
    expect(chip()?.textContent).toBe("2 / 3");

    render({ interactions: ids("c"), answered: [ans("a", true, T0), ans("b", true, T0)] });
    expect(cardsIn()).toEqual(["c"]);
    expect(chip()).toBeNull();
  });

  it("hands the keyboard on to the next card once one is answered with it, and not after a pointer answer", () => {
    const cardRoot = (id: string) => host.querySelector<HTMLElement>(`[data-card="${id}"] [data-card-keys]`)!;
    render({ interactions: ids("a", "b", "c") });
    act(() => {
      cardRoot("a").dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
      cardRoot("a").focus();
    });
    render({ interactions: ids("b", "c") });
    expect(document.activeElement).toBe(cardRoot("b"));

    act(() => cardRoot("b").dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    act(() => (document.activeElement as HTMLElement).blur());
    render({ interactions: ids("c") });
    expect(document.activeElement).toBe(document.body);
  });

  it("a card arriving mid-run grows the total", () => {
    render({ interactions: ids("a", "b") });
    render({ interactions: ids("b"), answered: [ans("a", true, T0)] });
    render({ interactions: ids("b", "c"), answered: [ans("a", true, T0)] });
    expect(chip()?.textContent).toBe("2 / 3");
  });

  it("has no chip for a single card", () => {
    render({ interactions: ids("a") });
    expect(cardsIn()).toEqual(["a"]);
    expect(chip()).toBeNull();
  });

  it("shows all cards when the chip is clicked, and folds them back from the same chip", () => {
    render({ interactions: ids("a", "b", "c") });
    expect(buttonWith("收起其余")).toBeUndefined();
    click(chip()!);
    expect(cardsIn()).toEqual(["a", "b", "c"]);
    // the same chip, in the same place, now folds them back: no button of its own under the cards
    expect(chip()?.textContent).toBe("收起其余");
    expect(chip()?.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelectorAll("button")).toHaveLength([...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() !== "收起其余" || b === chip()).length);
    click(chip()!);
    expect(cardsIn()).toEqual(["a"]);
    expect(buttonWith("收起其余")).toBeUndefined();
    expect(chip()?.textContent).toBe("1 / 3");
  });

  it("is one card and one answered line however many wait and were answered", () => {
    render({ interactions: ids("w1", "w2", "w3", "w4", "w5", "w6"), answered: ["d1", "d2", "d3", "d4", "d5"].map((id) => ans(id, true, T0)) });
    expect(host.querySelectorAll("[data-card]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-answered-summary]")).toHaveLength(1);
    // the answers are inside the line (sr-only list), not boxes of their own
    const boxes = [...host.querySelectorAll("[data-answered]")];
    expect(boxes).toHaveLength(5);
    expect(boxes.every((b) => summary()!.contains(b))).toBe(true);
    expect(chip()?.textContent).toBe("1 / 6");
  });
});

describe("the answered line", () => {
  const four = (extra: AnsweredCard[] = []) => [ans("d1", true, T0 - 3), ans("d2", true, T0 - 2), ans("d3", true, T0 - 1), ans("d4", true, T0), ...extra];

  it("says how many and that all were allowed", () => {
    render({ answered: four() });
    expect(summary()?.textContent).toContain("已回答 4 项");
    expect(summary()?.textContent).toContain("全部允许");
  });

  it("says a denied one", () => {
    render({ answered: [...four().slice(0, 3), ans("d4", false, T0)] });
    expect(summary()?.textContent).toContain("项拒绝");
    expect(summary()?.textContent).not.toContain("全部允许");
  });

  it("goes 3 s after the latest answer, and comes back with a newer one", () => {
    render({ answered: four() });
    advance(2999);
    expect(summary()).not.toBeNull();
    advance(1);
    expect(summary()).toBeNull();
    vi.setSystemTime(T0 + 10_000);
    render({ answered: four([ans("d5", true, T0 + 10_000)]) });
    expect(summary()?.textContent).toContain("已回答 5 项");
  });

  it("stays open past 3 s, lists each answer, and restarts 3 s when closed", () => {
    render({ answered: four() });
    click(summary()!.querySelector("button")!);
    expect(summary()!.querySelector("button")!.getAttribute("aria-expanded")).toBe("true");
    advance(10_000);
    expect(summary()).not.toBeNull();
    expect([...summary()!.querySelectorAll("[data-answered]")].map((e) => e.getAttribute("data-answered"))).toEqual(["d1", "d2", "d3", "d4"]);
    expect(summary()!.querySelector("[data-answered-about]")?.textContent).toContain("run d1");

    click(summary()!.querySelector("button")!);
    expect(summary()).not.toBeNull();
    advance(2999);
    expect(summary()).not.toBeNull();
    advance(1);
    expect(summary()).toBeNull();
  });

  it("says one answer as the answer and what it was about", () => {
    render({ answered: [ans("d1", true, T0)] });
    expect(summary()?.textContent).toContain("已回答");
    expect(summary()?.textContent).toContain("允许");
    expect(summary()?.textContent).toContain("run d1");
  });
});

describe("narrow", () => {
  it("shows one card and the chip", () => {
    render({ interactions: ids("a", "b", "c"), narrow: true });
    // narrow keeps the others mounted in a closed (inert) fold; only the first is open to the user
    const open = [...host.querySelectorAll("[data-card]")].filter((e) => !e.closest("[inert]")).map((e) => e.getAttribute("data-card"));
    expect(open).toEqual(["a"]);
    expect(chip()?.textContent).toBe("1 / 3");
    expect(host.textContent).not.toContain("还有");
    click(chip()!);
    expect([...host.querySelectorAll("[data-card]")].filter((e) => !e.closest("[inert]"))).toHaveLength(3);
  });

  it("folds into one line while the keyboard is up", () => {
    render({ interactions: ids("a", "b", "c"), narrow: true, keyboard: true });
    expect(host.textContent).toContain("3 项待处理");
    expect(chip()).toBeNull();
  });
});
