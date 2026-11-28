// @vitest-environment jsdom
// ultracode on the plan card and on to-dos (#12): each task's chip on the open card and its switch in "Edit" start from
// the assistant's choice and change it before Start, which sends only what changed; a computer whose Claude Code is too
// old starts it without; a hub without ultracode shows nothing. To-do cards, lines and rows carry the Agent list row's mark.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlanTask, TaskPlan } from "../../lib/feed";
import { loadPages } from "../../lib/lazy-page";
import type { Todo } from "../../lib/todos";
import type { Session } from "../../lib/types";

const startPlan = vi.fn(async (..._args: unknown[]) => {});
vi.mock("../../lib/feed", async (orig) => ({
  ...(await orig<typeof import("../../lib/feed")>()),
  startPlan: (...args: unknown[]) => startPlan(...args),
}));

const { getState } = await import("../../lib/store");
const { PlanCard } = await import("./Tasks");
const { TodosView } = await import("../Todos");
beforeAll(() => loadPages(), 60_000);

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (el: React.ReactNode) => {
  await act(async () => root.render(el));
  await flush();
};
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const byText = (label: string, sel = "button") => [...document.querySelectorAll<HTMLElement>(sel)].filter((b) => b.textContent?.trim() === label);
const chips = () => [...document.querySelectorAll<HTMLButtonElement>('[data-testid="plan-ultracode"]')];
const sent = () => startPlan.mock.calls.at(-1)?.[2] as Partial<PlanTask>[] | undefined;
const editSwitches = () => [...document.querySelectorAll<HTMLButtonElement>('[data-testid="plan-edit-ultracode"] [role="switch"]')];
const startInDialog = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"] button')].filter((b) => b.textContent?.trim() === "开始").at(-1);
const pickMachine = async (id: string, task = 0) => {
  const select = document.querySelector<HTMLSelectElement>(`#plan-p1-${task}-machine`);
  expect(select, "machine").toBeTruthy();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select, id);
    select!.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
};

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

const task = (o: Partial<PlanTask> = {}): PlanTask => ({ title: "补测试", prompt: "补", machine: "m1", cwd: "/w", ...o });
const plan = (tasks: PlanTask[]): TaskPlan => ({ id: "p1", conversation: "c1", state: "open", tasks });
const MACHINES = [{ id: "m1", name: "工作机", claude_version: "2.1.300" }, { id: "m2", name: "旧电脑", claude_version: "2.1.250" }];

beforeEach(() => {
  startPlan.mockClear();
  seed({ features: ["ultracode"], machines: MACHINES });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

describe("ultracode on the plan card", () => {
  it("shows each task's choice and starts the plan as it is when nothing changed", async () => {
    await render(<PlanCard plan={plan([task(), task({ title: "重构", ultracode: true })])} />);
    expect(chips().map((c) => c.getAttribute("aria-pressed"))).toEqual(["false", "true"]);
    await click(byText("启动 2 个任务")[0]);
    expect(startPlan).toHaveBeenCalledTimes(1);
    expect(sent()).toBeUndefined();
  });

  it("sends true / false for the tasks whose chip changed, nothing for the others", async () => {
    await render(<PlanCard plan={plan([task(), task({ title: "重构", ultracode: true }), task({ title: "文档" })])} />);
    await click(chips()[0]);
    await click(chips()[1]);
    expect(chips().map((c) => c.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    await click(byText("启动 3 个任务")[0]);
    expect(sent()).toEqual([{ ultracode: true }, { ultracode: false }, {}]);
  });

  it("starts without it on a computer whose Claude Code is too old, and says so where the assistant asked for it", async () => {
    await render(<PlanCard plan={plan([task({ machine: "m2", ultracode: true })])} />);
    const [chip] = chips();
    expect(chip.disabled).toBe(true);
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    expect(chip.title).toContain("2.1.284");
    expect(document.body.textContent).toContain("不开启 ultracode 启动");
    await click(byText("开始")[0]);
    expect(sent()).toEqual([{ ultracode: false }]);
  });

  it("shows nothing and sends nothing where the hub has no ultracode", async () => {
    seed({ features: [] });
    await render(<PlanCard plan={plan([task({ ultracode: true })])} />);
    expect(chips()).toHaveLength(0);
    await click(byText("开始")[0]);
    expect(sent()).toBeUndefined();
  });

  it("opens 改一下 with the card's choice and sends only what differs from the assistant's", async () => {
    await render(<PlanCard plan={plan([task(), task({ title: "重构", ultracode: true })])} />);
    await click(chips()[0]); // on, from the card
    await click(byText("编辑")[0]);
    expect(editSwitches().map((s) => s.getAttribute("aria-checked"))).toEqual(["true", "true"]);
    await click(editSwitches()[1]); // off, in the dialog
    await click(startInDialog());
    expect(sent()?.map((e) => e.ultracode)).toEqual([true, false]);
  });

  it("keeps the choice in 改一下 for a task moved off a computer that can't", async () => {
    await render(<PlanCard plan={plan([task({ machine: "m2", ultracode: true })])} />);
    await click(byText("编辑")[0]);
    expect(editSwitches()[0].getAttribute("aria-checked")).toBe("false"); // drawn off on the old computer
    await pickMachine("m1");
    expect(editSwitches()[0].getAttribute("aria-checked")).toBe("true"); // the assistant's choice again
    await click(startInDialog());
    expect(sent()?.[0]).not.toHaveProperty("ultracode"); // left out: the hub keeps the assistant's
  });

  it("starts without it from 改一下 where the task stays on a computer that can't", async () => {
    await render(<PlanCard plan={plan([task({ machine: "m2", ultracode: true })])} />);
    await click(byText("编辑")[0]);
    await click(startInDialog());
    expect(sent()?.[0]?.ultracode).toBe(false);
  });

  it("keeps an omitted permission omitted when starting from the lazy plan editor", async () => {
    const proposed = plan([task(), task({ permission: "default" })]);
    await render(<PlanCard plan={proposed} />);
    await click(byText("编辑")[0]);
    await click(startInDialog());
    expect(startPlan.mock.calls[0]?.[0]).toBe(proposed);
    expect(sent()?.[0]).not.toHaveProperty("permission");
    expect(sent()?.[1]).toHaveProperty("permission", "default");
  });

  it("does not count the card's own choice as a change to throw away in 改一下", async () => {
    const ask = vi.spyOn(window, "confirm").mockReturnValue(false);
    await render(<PlanCard plan={plan([task()])} />);
    await click(chips()[0]);
    await click(byText("编辑")[0]);
    await click(byText("取消")[0]);
    expect(ask).not.toHaveBeenCalled();
    expect(chips()[0].getAttribute("aria-pressed")).toBe("true"); // the card keeps it
  });
});

describe("ultracode on to-dos", () => {
  const session = (o: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: "s1", title: "修", cwd: "/w", agent: "claude", status: "working", state: "working", ...o }) as Session;
  const todo = (o: Partial<Todo> = {}): Todo => ({
    id: "t1", title: "修小组件", created_at: new Date().toISOString(), updated_at: new Date().toISOString(), origin: "c1",
    agents: [{ machine_id: "m1", session_id: "s1", linked_at: new Date().toISOString() }], ...o,
  });
  const marks = () => document.querySelectorAll("[data-ultracode-mark]").length;

  it("puts the mark on the to-do's row on the to-dos page", async () => {
    seed({ todos: [todo()], todosLoaded: true, sessions: { m1: [session({ ultracode: true })] } });
    await render(<TodosView />);
    expect(marks()).toBe(1);
  });
});
