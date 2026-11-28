/// <reference types="node" />
// Dropping a file or a Files entry on a chat's composer (entry-drag.ts) is on the first screen. It needs only whether a
// drag carries files and a path as a shell word: importing them from terminal-clipboard.ts put the terminal's
// clipboard code and all its words on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import * as clipboard from "./terminal-clipboard";
import { dragHasFiles, escapePastePath } from "./paste-path";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const valueImport = /^import\s+(?!type\b)[^;]*from\s+["']\.\/terminal-clipboard["']/m;

describe("paste-path", () => {
  it("keeps terminal-clipboard.ts and its words off the first screen", () => {
    expect(read("./entry-drag.ts")).not.toMatch(valueImport);
    expect(read("./paste-path.ts")).not.toMatch(/^import\s/m);
    // The chat's copy button (views/chat/MessageActions.tsx) says "Copied" with the terminal's word, so the first screen
    // keeps that one key and none of the rest.
    expect(startup.filter((key) => key === "lib-terminal-clipboard" || key.startsWith("lib-terminal-clipboard."))).toEqual([
      "lib-terminal-clipboard.copied",
    ]);
  });

  it("is what terminal-clipboard.ts offers the terminal", () => {
    expect(clipboard.escapePastePath).toBe(escapePastePath);
    expect(clipboard.dragHasFiles).toBe(dragHasFiles);
    expect(clipboard.pastedPaths(["a b", "c"])).toBe("a\\ b c ");
  });
});
