// A started plan's task that did not start says why on the plan (#4145, #4809).
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PlanCard } from "./Tasks";

describe("a started PlanCard", () => {
  it("says why the task did not start", () => {
    const plan = { id: "p", conversation: "c", state: "started" as const, tasks: [{ title: "示例任务", prompt: "", machine: "m", cwd: "/", todo_id: "t1", error: "rpc: connection closed" }] };
    expect(renderToStaticMarkup(createElement(PlanCard, { plan }))).toContain("「示例任务」启动失败：rpc: connection closed");
  });
});

describe("PlanCard with a blank reason (#4809)", () => {
  it("says the reason is unknown instead of leaving it empty", () => {
    const plan = { id: "p", conversation: "c", state: "started" as const, tasks: [{ title: "示例任务", prompt: "", machine: "m", cwd: "/", todo_id: "t1", error: "  \n " }] };
    expect(renderToStaticMarkup(createElement(PlanCard, { plan }))).toContain("「示例任务」启动失败：未知原因");
  });
});
