// @vitest-environment jsdom
// The plan card's brief has one control (#3822): "Show all" opens the whole request and the goal, the criteria, the
// expected time and what is out of bounds together; "Show less" folds them again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { TaskBrief } from "../../lib/brief";
import { PlanBrief } from "./TaskBrief";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-03T12:00:00Z";
const brief: TaskBrief = {
  id: "brief_1", created_at: at, started_by: "owner", request: { text: "Please fix the sign-in crash before Friday", at },
  goal: "The sign-in page no longer crashes in landscape", done_when: ["The fix is merged"], criteria: ["The fix is merged"],
  expect_minutes: 30, out_of_bounds: ["Do not touch the payment flow"], log: [],
};

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the plan card's brief (#3822)", () => {
  it("opens and folds everything with its one control", () => {
    act(() => root.render(<PlanBrief brief={brief} title="Fix the sign-in crash" />));
    const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("button")];
    expect(buttons()).toHaveLength(1);
    expect(host.querySelector("[data-brief-summary]")?.textContent).toBe("1 条验收标准 · 约 30 分钟 · 1 条禁止事项");
    expect(host.textContent).not.toContain("The fix is merged");
    act(() => buttons()[0].click());
    expect(buttons()).toHaveLength(1);
    expect(buttons()[0].getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-brief-clamp]")?.getAttribute("data-brief-clamp")).toBe("open");
    for (const text of ["The sign-in page no longer crashes in landscape", "The fix is merged", "约 30 分钟", "Do not touch the payment flow"]) expect(host.textContent).toContain(text);
    expect(host.querySelector("[data-brief-summary]")).toBeNull(); // the rows say it now
    act(() => buttons()[0].click());
    expect(host.textContent).not.toContain("The fix is merged");
  });
});
