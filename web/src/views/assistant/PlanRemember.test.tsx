// @vitest-environment jsdom
// The plan card's "Start these directly" says what it remembers (#8095): each task that changes files, by its computer
// and folder, with the folder's subfolders; read-only tasks (they start without asking anyway) are not named, and a plan
// of only those offers nothing to remember.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { PlanTask, TaskPlan } from "../../lib/feed";

const { getState } = await import("../../lib/store");
const { formatList } = await import("../../i18n");
const { PlanCard, rememberScope } = await import("./Tasks");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
const task = (o: Partial<PlanTask> = {}): PlanTask => ({ title: "补测试", prompt: "补", machine: "m1", cwd: "/home/me/src/tool", ...o });
const plan = (tasks: PlanTask[]): TaskPlan => ({ id: "p1", conversation: "c1", state: "open", tasks });
const machines = [{ id: "m1", name: "工作机" }, { id: "m2", name: "家里" }] as never;

beforeEach(() => {
  seed({ machines });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
});

it("names each writable task's computer and folder once, with its subfolders", () => {
  const line = rememberScope([task(), task({ title: "又一个" }), task({ machine: "m2", cwd: "/opt/app" }), task({ cwd: "/docs", readonly: true })], machines);
  expect(line).toBe(`今后在${formatList(["「工作机」的 /home/me/src/tool", "「家里」的 /opt/app"])} 及其子目录中修改文件的任务将直接启动`);
  expect(line).not.toContain("/docs");
});

it("offers nothing to remember when every task only reads (those start without asking anyway)", async () => {
  expect(rememberScope([task({ readonly: true })], machines)).toBe("");
  await act(async () => root.render(<PlanCard plan={plan([task({ readonly: true })])} />));
  expect(host.querySelector("input[type=checkbox]")).toBeNull();
});

it("draws the scope next to the card's checkbox", async () => {
  await act(async () => root.render(<PlanCard plan={plan([task({ cwd: "/home/me/src/tool/cli" })])} />));
  const label = [...host.querySelectorAll("label")].find((l) => l.querySelector("input[type=checkbox]"));
  expect(label?.textContent).toBe("今后在「工作机」的 /home/me/src/tool/cli 及其子目录中修改文件的任务将直接启动");
});
