// @vitest-environment jsdom
// ActionRow draws every tool step of a chat, but StepDetail (a plan's Markdown, a question's options and ticks) only
// under an opened ExitPlanMode / AskUserQuestion / ask_user step. The row asks step-detail-kind.ts whether a step has
// one; the detail's own chunk loads when such a row is opened, or as the pointer or focus reaches it, and is no longer
// part of every first screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";
import type { ToolStep } from "../../lib/work-groups";

const loads = vi.hoisted(() => ({ count: 0, done: false }));
vi.mock("./StepDetail", async (original) => {
  loads.count++;
  const real = await original<typeof import("./StepDetail")>();
  loads.done = true;
  return real;
});
import { ActionRow } from "./ActionRow";
import { forgetExpanded } from "./expanded";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
// A dynamic import goes through the test's module transform: it takes real time, not just microtasks.
const until = async (done: () => boolean) => {
  for (let i = 0; i < 200 && !done(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
};
const drawnPlan = () => host.querySelector('[data-step-detail="plan"]')?.textContent ?? "";
const use = (name: string, input: Record<string, unknown>): Item => ({ id: "t1#0", seq: 1, role: "assistant", kind: "tool_use", tool_name: name, tool_use_id: "c1", tool_input: input });
const step = (u: Item): ToolStep => ({ kind: "tool", id: u.id, use: u, images: [] });
const render = (s: ToolStep) => act(() => root.render(<ActionRow step={s} mid="m" sid="s" />));
const row = () => host.querySelector<HTMLButtonElement>("[data-action-row] > button")!;
const plan = step(use("ExitPlanMode", { plan: "# 计划\n\n1. 先做这个" }));

beforeEach(() => {
  forgetExpanded();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

// The first test must be the first to load StepDetail.tsx in this file: the mock counts that one load.
describe("StepDetail stays off the row's first load", () => {
  it("loads the detail only for a row that has one, on intent or when opened, and then draws it", async () => {
    const draw = render;
    await draw(step(use("Bash", { command: "ls" })));
    act(() => row().dispatchEvent(new PointerEvent("pointerover", { bubbles: true })));
    act(() => row().focus());
    await until(() => loads.count > 0); // runs out its time: nothing should load for a plain step
    expect(loads.count).toBe(0);

    await draw(plan);
    await flush();
    expect(loads.count).toBe(0);
    act(() => row().dispatchEvent(new PointerEvent("pointerover", { bubbles: true })));
    await until(() => loads.done);
    await flush();
    expect(loads.count).toBe(1);

    act(() => row().click());
    await flush();
    expect(drawnPlan()).toContain("先做这个"); // already in: drawn in the same update as the opening
  });

  it("draws an opened plan even when nothing loaded it ahead", async () => {
    await render(plan);
    act(() => row().click());
    await until(() => drawnPlan() !== "");
    expect(drawnPlan()).toContain("先做这个");
  });
});
