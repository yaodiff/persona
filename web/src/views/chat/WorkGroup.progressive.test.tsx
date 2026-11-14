// @vitest-environment jsdom
// Opening a long work summary (audit item 90: 650 commands with notes, about 8,000 elements) draws the first screenful
// with the tap and the rest in idle batches (work-budget.ts), across nested groups too; a search hit or a jump to a call
// inside it draws everything at once so the row asked for is there; a live group draws everything.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Step } from "../../lib/turns";
import type { WorkGroupNode, WorkNode } from "../../lib/work-groups";
import { forgetExpanded } from "./expanded";
import { openToCall, useCallSeeker } from "./jump";
import { clearReveal, revealItem } from "./search-reveal";
import { FIRST_STEPS, STEP_BATCH, stepWeight, withinBudget } from "./work-budget";
import { WorkGroup, type WorkProps } from "./WorkGroup";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const T0 = Date.parse("2026-11-04T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const tool = (n: number): Step => ({
  kind: "tool", id: `t${n}#0`,
  use: { id: `t${n}#0`, seq: n, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: `c-${n}`, tool_input: { command: `make step-${n}` }, ts: at(n) },
  result: { id: `t${n}-r#0`, seq: n, role: "user", kind: "tool_result", tool_use_id: `c-${n}`, text: "ok", ts: at(n) },
  images: [],
});
const steps = (from: number, count: number): WorkNode[] => Array.from({ length: count }, (_, i) => ({ kind: "step", step: tool(from + i) }));
const flat = (count: number, live = false): WorkGroupNode => ({ kind: "group", id: "work-big", children: steps(1, count), live, startedAt: T0, durationMs: 4_200_000 });
const props: WorkProps = { mid: "m", sid: "s", scope: "big:", expandFold: async () => {}, agentStatus: () => "completed" };
/** An exchange's work as SegmentView draws it: its groups answer "Jump to the step that started it" (jump.ts). */
function Exchange({ g }: { g: WorkGroupNode }) {
  useCallSeeker(props.scope, [g]);
  return <WorkGroup g={g} p={props} />;
}

let host: HTMLDivElement;
let root: Root;
const rows = () => host.querySelectorAll("[data-work-detail] [data-action-row]").length;
const header = () => host.querySelector<HTMLButtonElement>("[data-work-group] > button")!;
// one idle batch: jsdom has no requestIdleCallback, so whenIdle waits a short timer
const idle = () => act(() => vi.advanceTimersByTime(60));

beforeEach(() => {
  vi.useFakeTimers();
  forgetExpanded();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  clearReveal();
  vi.useRealTimers();
});

describe("a long work summary opens a screenful at a time (audit item 90)", () => {
  it("draws the first screenful with the tap and the rest in idle batches, keeping what it drew", () => {
    act(() => root.render(<WorkGroup g={flat(200)} p={props} />));
    act(() => header().click());
    expect(header().getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toBe(FIRST_STEPS);
    const first = host.querySelector("[data-work-detail] [data-action-row]");
    idle();
    expect(rows()).toBe(FIRST_STEPS + STEP_BATCH);
    // the rows already drawn stay the same elements
    expect(host.querySelector("[data-work-detail] [data-action-row]")).toBe(first);
    for (let i = 0; i < 5; i++) idle();
    expect(rows()).toBe(200);
    expect(host.textContent).toContain("make step-200");
  });

  it("shares one budget with the groups nested in it", () => {
    const words = (n: number): WorkNode => ({ kind: "step", step: { kind: "text", id: `w${n}#0`, item: { id: `w${n}#0`, seq: n, role: "assistant", kind: "text", text: `progress ${n}`, ts: at(n) } } });
    const inner = (n: number, from: number): WorkGroupNode => ({ kind: "group", id: `work-inner-${n}`, children: steps(from, 50), live: false, startedAt: T0, durationMs: 60_000 });
    const timeline: WorkGroupNode = { kind: "group", id: "work-timeline", children: [inner(1, 1), words(1000), inner(2, 101), words(1001), inner(3, 201)], live: false, startedAt: T0, durationMs: 600_000 };
    act(() => root.render(<WorkGroup g={timeline} p={props} />));
    act(() => header().click());
    // nested groups open with their timeline: the first is whole, the second only what is left of the first screenful
    const drawn = rows();
    expect(drawn).toBeLessThanOrEqual(FIRST_STEPS);
    expect(drawn).toBeGreaterThan(50);
    expect(host.textContent).not.toContain("make step-201");
    for (let i = 0; i < 5; i++) idle();
    expect(rows()).toBe(150);
    expect(host.textContent).toContain("progress 1001");
  });

  it("a search hit deep inside opens it with every step drawn at once", () => {
    act(() => root.render(<WorkGroup g={flat(300)} p={props} />));
    act(() => revealItem("big:", "t290#0"));
    expect(header().getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toBe(300);
    expect(host.querySelector('[data-items~="t290#0"]')).not.toBeNull();
  });

  it("a hit in a group already opening draws the rest at once too", () => {
    act(() => root.render(<WorkGroup g={flat(300)} p={props} />));
    act(() => header().click());
    expect(rows()).toBe(FIRST_STEPS);
    act(() => revealItem("big:", "t290#0"));
    expect(rows()).toBe(300);
  });

  it("a jump to a call deep inside (a background task's notice, a subagent or child link) draws its row at once", () => {
    const g = flat(650);
    act(() => root.render(<Exchange g={g} />));
    let found = false;
    // ChatView.jumpToCall → seekCall → openToCall: the same update opens the group and draws step 600's row
    act(() => void (found = openToCall("big:", "c-600")));
    expect(found).toBe(true);
    expect(header().getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toBe(650);
    expect(host.querySelector('[data-tool-use="c-600"]')).not.toBeNull();
  });

  it("a jump into a group already open with part of its steps draws the rest at once", () => {
    act(() => root.render(<Exchange g={flat(300)} />));
    act(() => header().click());
    expect(rows()).toBe(FIRST_STEPS);
    act(() => void openToCall("big:", "c-290"));
    expect(rows()).toBe(300);
    expect(host.querySelector('[data-tool-use="c-290"]')).not.toBeNull();
    // closed and opened again by hand: the first screenful again
    act(() => header().click());
    act(() => vi.advanceTimersByTime(500));
    act(() => header().click());
    expect(rows()).toBe(FIRST_STEPS);
  });

  it("closing and opening again starts from the first screenful", () => {
    act(() => root.render(<WorkGroup g={flat(200)} p={props} />));
    act(() => header().click());
    for (let i = 0; i < 5; i++) idle();
    expect(rows()).toBe(200);
    act(() => header().click());
    act(() => vi.advanceTimersByTime(500)); // the fold's close runs out and drops its rows
    act(() => header().click());
    expect(rows()).toBe(FIRST_STEPS);
  });

  it("counts what drawing costs and stops where the budget ends", () => {
    const inner: WorkGroupNode = { kind: "group", id: "work-inner", children: steps(1, 10), live: false };
    const nodes: WorkNode[] = [...steps(100, 3), inner, ...steps(200, 5)];
    expect(stepWeight(inner)).toBe(11);
    expect(withinBudget(nodes, undefined).shown).toBe(nodes);
    const part = withinBudget(nodes, 8);
    expect(part.shown).toEqual(nodes.slice(0, 4));
    expect(part.limits?.get(inner)).toBe(4); // its header and four of its steps
    expect(withinBudget(nodes, 14).shown).toHaveLength(4);
    expect(withinBudget(nodes, 15).shown).toHaveLength(5);
  });
});
