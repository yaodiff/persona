/// <reference types="node" />
// Every page's top bar is one height with one side padding (#16462; docs/web/web.md "The top bar": 56 px like every
// page header), so moving between pages never moves the title or the line under it. Read from the source: a `<header>`
// is a top bar when it has a fixed height (`h-N`) or sits at the top of its column drawing the line under it
// (`shrink-0` with `border-b`), so a bar sized by its content (`py-3`, `min-h-14`) is one too. Every top bar is `h-14`
// with `px-3` (and `sm:px-4` when it widens at all). Headers inside a page (a card's, a column's) have neither mark.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const SRC = fileURLToPath(new URL("../", import.meta.url));
const SOURCE_FILE = /\.tsx$/;
const TEST_FILE = /\.test\.tsx$/;
/** `<header className="…">` or `<header className={cx("…", …)}>`: the class list it always has. */
const HEADER = /<header\s+className=(?:"([^"]*)"|\{cx\(\s*"([^"]*)")/g;
const FIXED_HEIGHT = /^h-(\d+(?:\.\d+)?|\[[^\]]+\])$/;

/** The only bars off the rule, each with why. Keep it this short: a new page's bar follows the rule. */
const EXEMPT = new Map([
  ["modules/screen-share/ScreenView.tsx", "a window of its own: a thin bar over a remote screen"],
  ["admin/Shell.tsx", "the admin console, an app of its own: its phone header is a brand row over a nav row"],
  ["views/chat/SubagentPanel.tsx", "a side panel's two-row header: its actions over the subagent's title"],
]);

/** The top bars in `source`, as their class lists. */
function topBars(source: string): string[][] {
  const out: string[][] = [];
  for (const m of source.matchAll(HEADER)) {
    const classes = (m[1] ?? m[2] ?? "").split(/\s+/).filter(Boolean);
    if (classes.some((c) => FIXED_HEIGHT.test(c)) || (classes.includes("shrink-0") && classes.includes("border-b"))) out.push(classes);
  }
  return out;
}

/** What is off in one bar's class list: its height (none fixed, or not h-14), its side padding. */
function offRule(classes: string[]): string[] {
  const off: string[] = [];
  const heights = classes.filter((c) => FIXED_HEIGHT.test(c));
  if (heights.join(" ") !== "h-14") off.push(heights.join(" ") || `no fixed height (${classes.filter((c) => /^(?:min-h|max-h|py|pt|pb)-/.test(c)).join(" ")})`);
  const padding = classes.filter((c) => /^(?:[a-z0-9@]+:)?px-/.test(c));
  if (!padding.includes("px-3") || padding.some((c) => c !== "px-3" && c !== "sm:px-4")) off.push(padding.join(" "));
  return off;
}

const files = (readdirSync(SRC, { recursive: true }) as string[])
  .map((file) => file.split("\\").join("/"))
  .filter((file) => SOURCE_FILE.test(file) && !TEST_FILE.test(file));
const bars = files.flatMap((file) => topBars(readFileSync(join(SRC, file), "utf8")).map((classes) => ({ file, classes })));

describe("page top bars (#16462)", () => {
  it("are all 56 px with the same side padding", () => {
    const off = bars.filter((bar) => !EXEMPT.has(bar.file)).flatMap(({ file, classes }) => offRule(classes).map((what) => `${file}: ${what}`));
    expect(off).toEqual([]);
  });

  it("reads the pages' bars, so a pattern that finds none cannot pass for one that keeps the rule", () => {
    const read = new Set(bars.map((bar) => bar.file));
    for (const page of [
      "views/Todos.tsx", "views/inbox/Inbox.tsx", "views/assistant/Routines.tsx", "views/knowledge/KBList.tsx", "views/Home.tsx",
      "views/Machines.tsx", "views/StarredMessages.tsx", "views/WorkspaceBoard.tsx", "views/Settings.tsx",
    ]) {
      expect(read).toContain(page);
    }
    // the board's two bars: the board's own, and the one while there is no board to show
    expect(bars.filter((bar) => bar.file === "views/WorkspaceBoard.tsx")).toHaveLength(2);
    expect(bars.length).toBeGreaterThan(30);
    // every exemption still names a bar there, so the list cannot keep a stale entry
    for (const file of EXEMPT.keys()) expect(read).toContain(file);
  });

  it("tells a bar off the rule from one on it", () => {
    expect(topBars('<header className="flex h-13 shrink-0 px-4 sm:px-6">')).toEqual([["flex", "h-13", "shrink-0", "px-4", "sm:px-6"]]);
    expect(topBars('<header className={cx("flex h-14 px-3 sm:px-4", wide && "x")}>')).toEqual([["flex", "h-14", "px-3", "sm:px-4"]]);
    // a page's bar sized by its content is a top bar too
    expect(topBars('<header className="flex shrink-0 items-center gap-3 border-b border-line px-4 py-3">')).toHaveLength(1);
    expect(topBars('<header className="flex min-h-14 shrink-0 border-b border-line px-3 py-2 sm:px-4">')).toHaveLength(1);
    // a card's or a column's header inside a page is not
    expect(topBars('<header className="flex flex-wrap items-center border-b border-line px-3 py-2">')).toEqual([]);
    expect(topBars('<header className="min-w-0">')).toEqual([]);
    expect(offRule(["flex", "h-13", "px-4", "sm:px-6"])).toEqual(["h-13", "px-4 sm:px-6"]);
    expect(offRule(["flex", "shrink-0", "border-b", "px-4", "py-3"])).toEqual(["no fixed height (py-3)", "px-4"]);
    expect(offRule(["flex", "min-h-14", "shrink-0", "border-b", "px-3", "py-2", "sm:px-4"])).toEqual(["no fixed height (min-h-14 py-2)"]);
    expect(offRule(["flex", "h-14", "px-3", "sm:px-4"])).toEqual([]);
    expect(offRule(["flex", "h-14", "px-3"])).toEqual([]);
  });
});
