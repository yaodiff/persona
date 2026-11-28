/// <reference types="node" />
// The inbox list's filter (lib/inbox.ts, on every first screen) needs only inFolderFilter. Keeping it in
// inbox-folders.ts put the folder tree and the folder settings' store there too.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import * as filter from "./inbox-folder-filter";
import * as folders from "./inbox-folders";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("inbox-folder-filter", () => {
  it("imports nothing, so inbox-folders.ts stays off the first screen", () => {
    expect(read("./inbox-folder-filter.ts")).not.toMatch(/^import\s+(?!type\b)/m);
  });

  it("is what inbox-folders.ts offers its callers", () => {
    expect(folders.inFolderFilter).toBe(filter.inFolderFilter);
    expect(folders.UNFILED).toBe(filter.UNFILED);
    expect(filter.inFolderFilter({ folder: "work/alerts" }, "work")).toBe(true);
    expect(filter.inFolderFilter({ folder: "workshop" }, "work")).toBe(false);
    expect(filter.inFolderFilter({}, filter.UNFILED)).toBe(true);
  });
});
