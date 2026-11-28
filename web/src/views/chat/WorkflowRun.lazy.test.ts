/// <reference types="node" />
// The chat's first screen draws a Workflow call's card, never what opens below it or the Background tasks panel's run
// detail: those are WorkflowRun.tsx, a chunk of its own, loaded by the tap that opens a run in place
// (WorkflowCard.tsx, lazyPage) or with the panel's own chunk (BackgroundPanel.tsx, itself lazy).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { describe, expect, it } from "vitest";

const root = new URL("../../", import.meta.url);
const staticImport = /^import\s+(?!type\b)[^;]*from\s+["'][./]*(?:views\/)?(?:chat\/)?WorkflowRun["']/m;

function sources(dir: URL): string[] {
  return readdirSync(dir).flatMap((name) => {
    const u = new URL(name, dir);
    if (statSync(u).isDirectory()) return sources(new URL(name + "/", dir));
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [u.pathname] : [];
  });
}

describe("the workflow run detail boundary", () => {
  it("leaves WorkflowRun out of every static import but the Background tasks panel's", () => {
    const allowed = new Set(["BackgroundPanel.tsx"]); // its own chunk, loaded when the panel opens
    for (const file of sources(root)) {
      if (allowed.has(file.split("/").pop()!)) continue;
      expect(readFileSync(file, "utf8"), file).not.toMatch(staticImport);
    }
  });

  it("loads the inline detail lazily in the call's card, and starts it on the tap that opens a run", () => {
    const card = readFileSync(new URL("./WorkflowCard.tsx", import.meta.url), "utf8");
    expect(card).toMatch(/lazyPage\(\(\) => import\("\.\/WorkflowRun"\)\.then\(\(m\) => m\.WorkflowRunInline\)\)/);
    expect(card).toMatch(/WorkflowRunInline\.preload\(\)/);
  });

  it("resolves the dynamic import to the existing components", async () => {
    const run = await import("./WorkflowRun");
    expect(typeof run.WorkflowRunInline).toBe("function");
    expect(typeof run.WorkflowCard).toBe("function");
  }, 60_000); // a cold transform of the chunk's imports, slow when the whole suite runs at once
});
