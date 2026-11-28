/// <reference types="node" />
// ActionRow draws every tool step of a chat, but StepDetail only under an opened ExitPlanMode / AskUserQuestion /
// ask_user step: the row asks step-detail-kind.ts whether a step has one, and loads StepDetail.tsx as a chunk of its own
// (StepDetail.lazy.test.tsx). A runtime import of StepDetail.tsx from ActionRow.tsx would put it on every first screen
// again; the build's startup-namespaces check also fails then, since chat-stepdetail is no longer a startup namespace.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import * as detail from "./StepDetail";
import { hasStepDetail, showsResult } from "./step-detail-kind";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const use = (tool_name: string, tool_input: Record<string, unknown>) => ({ id: "t1#0", seq: 1, role: "assistant" as const, kind: "tool_use" as const, tool_name, tool_use_id: "c1", tool_input });

describe("step detail kind", () => {
  it("keeps StepDetail.tsx out of ActionRow.tsx's runtime imports", () => {
    const source = read("./ActionRow.tsx");
    expect(source).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/StepDetail["']/m);
    expect(source).toMatch(/^import\s+\{\s*hasStepDetail,\s*showsResult\s*\}\s+from\s+["']\.\/step-detail-kind["']/m);
    expect(source).toMatch(/lazyPage\(\(\) => import\("\.\/StepDetail"\)/);
  });

  it("is what StepDetail.tsx re-exports", () => {
    expect(detail.hasStepDetail).toBe(hasStepDetail);
    expect(detail.showsResult).toBe(showsResult);
  });

  it("says which steps draw their own detail", () => {
    expect(hasStepDetail(use("ExitPlanMode", { plan: "# Plan" }))).toBe(true);
    expect(hasStepDetail(use("ExitPlanMode", { plan: "  " }))).toBe(false);
    expect(hasStepDetail(use("AskUserQuestion", { questions: [{ question: "Which?", options: [{ label: "A" }] }] }))).toBe(true);
    expect(hasStepDetail(use("mcp__persona__ask_user", { questions: [{ question: "Which?", options: [{ label: "A" }] }] }))).toBe(true);
    expect(hasStepDetail(use("Bash", { command: "ls" }))).toBe(false);
    expect(showsResult("mcp__persona__ask_user", { id: "r", seq: 2, role: "user", kind: "tool_result" })).toBe(false);
    expect(showsResult("mcp__persona__ask_user", { id: "r", seq: 2, role: "user", kind: "tool_result", is_error: true })).toBe(true);
    expect(showsResult("Bash", undefined)).toBe(true);
  });
});
