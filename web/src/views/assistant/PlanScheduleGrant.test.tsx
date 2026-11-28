// @vitest-environment jsdom
// A scheduled task's own start grant on the plan card (#8045): a plan its run made offers "Let “…” start these by
// itself" under "Start these directly", off until ticked; Start sends schedule_grant only when it is ticked, and every
// other plan (no schedule, or only read-only tasks) draws the card as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlanTask, TaskPlan } from "../../lib/feed";

const api = vi.fn(async (..._args: unknown[]) => ({ plan: { id: "p1", conversation: "c1", state: "started", tasks: [] } }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (...args: unknown[]) => api(...args),
}));

const { getState } = await import("../../lib/store");
const { PlanCard, planSchedule } = await import("./Tasks");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
const task = (o: Partial<PlanTask> = {}): PlanTask => ({ title: "整理周报", prompt: "整理", machine: "m1", cwd: "/home/me/notes", ...o });
const plan = (tasks: PlanTask[]): TaskPlan => ({ id: "p1", conversation: "c1", state: "open", tasks });
const scheduled = (o: Partial<PlanTask> = {}) => task({ schedule: "t1", schedule_title: "每周周报", ...o });
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (p: TaskPlan) => {
  await act(async () => root.render(<PlanCard plan={p} />));
  await flush();
};
const checkboxes = () => [...host.querySelectorAll<HTMLInputElement>("input[type=checkbox]")];
const grantBox = () => {
  const label = [...host.querySelectorAll("label")].find((l) => l.textContent?.includes("每周周报"));
  return label ? (document.getElementById(label.htmlFor) as HTMLInputElement | null) : null;
};
const start = async () => {
  const button = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "开始");
  expect(button, "Start").toBeTruthy();
  await act(async () => button!.click());
  await flush();
};
const starts = () => api.mock.calls.filter((c) => c[0] === "/task-plans/p1/start");
const body = () => (starts().at(-1)?.[1] as { json: Record<string, unknown> }).json;

beforeEach(() => {
  api.mockClear();
  seed({ machines: [{ id: "m1", name: "工作机" }] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
});

describe("the plan card's schedule grant", () => {
  it("names the scheduled task, falls back to plain words, and is absent from other plans", () => {
    expect(planSchedule(plan([scheduled()]))).toEqual({ id: "t1", name: "每周周报" });
    expect(planSchedule(plan([scheduled({ schedule_title: " " })]))?.name).toBe("此定时任务");
    expect(planSchedule(plan([task()]))).toBeNull();
  });

  it("is not drawn for a plan no scheduled task made: the card is as before", async () => {
    await render(plan([task()]));
    expect(checkboxes()).toHaveLength(1);
    expect(host.textContent).not.toContain("自动启动此类任务");
  });

  it("is not drawn when every task only reads (those start without asking anyway)", async () => {
    await render(plan([scheduled({ readonly: true })]));
    expect(checkboxes()).toHaveLength(0);
    expect(host.textContent).not.toContain("自动启动此类任务");
  });

  it("is drawn unticked under Start these directly, with what it covers", async () => {
    await render(plan([scheduled()]));
    expect(checkboxes()).toHaveLength(2);
    const box = grantBox();
    expect(box).toBe(checkboxes()[1]);
    expect(box!.checked).toBe(false);
    expect(host.textContent).toContain("允许「每周周报」今后自动启动此类任务");
    const note = document.getElementById(box!.getAttribute("aria-describedby")!);
    expect(note?.textContent).toContain("若当次运行读取过邮件、收件箱或网页，则不会自动启动");
  });

  it("sends schedule_grant only when ticked, independent of remember", async () => {
    await render(plan([scheduled()]));
    await start();
    expect(starts()).toHaveLength(1);
    expect(body()).toEqual({ tasks: undefined, remember: false });
    expect("schedule_grant" in body()).toBe(false);

    await act(async () => grantBox()!.click());
    await start();
    expect(body()).toEqual({ tasks: undefined, remember: false, schedule_grant: true });

    await act(async () => checkboxes()[0].click());
    await start();
    expect(body()).toEqual({ tasks: undefined, remember: true, schedule_grant: true });
  });
});
