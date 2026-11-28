/// <reference types="node" />
// The chat on every first screen reads only a card's open targets (AssistantCard) and a card reply's tag (SegmentView).
// Keeping them in assistant-blocks.ts put its block parser, card checks and extension-ui's item list there too.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import * as actions from "./assistant-card-actions";
import * as blocks from "./assistant-blocks";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const imports = (code: string) => [...code.matchAll(/^import\s+(?!type\b)[^;]*?from\s+["']([^"']+)["']/gm)].map((m) => m[1]);

describe("assistant-card-actions", () => {
  it("keeps assistant-blocks.ts and extension-ui.ts off the first screen", () => {
    expect(imports(read("./assistant-card-actions.ts"))).toEqual(["./compatibility"]);
  });

  it("is what assistant-blocks.ts offers its callers", () => {
    expect(blocks.openTarget).toBe(actions.openTarget);
    expect(blocks.cardReply).toBe(actions.cardReply);
    expect(blocks.openProblem("https://example.com/a")).toBe("");
    expect(blocks.openProblem("http://example.com/a")).toBe("open");
    expect(actions.openTarget("https://example.com/a")).toEqual({ kind: "web", url: "https://example.com/a" });
    expect(actions.cardReply('ok\n<persona-action view="v1" action="go">{"values":{"a":"b"}}</persona-action>')).toEqual({
      text: "ok",
      view: "v1",
      action: "go",
      values: { a: "b" },
    });
  });
});
