// @vitest-environment jsdom
// The changed-files card after cindy's TurnChangesCard (#5157): counted from the exchange's own successful edits, by
// file, folded work by its fold's changes; only at the end of a finished Agent exchange (never a Persona conversation's,
// never while it runs, never in a supervision trace, never with a fold an older node made); three files, then Show N
// more; Review opens the session's review.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
});

const fake = vi.hoisted(() => ({ features: [] as string[], cwd: "/Users/me/code/weather" as string | undefined }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), features: fake.features }),
    getSession: (mid: string, sid: string) => (fake.cwd === undefined ? undefined : ({ machine_id: mid, session_id: sid, cwd: fake.cwd } as ReturnType<typeof real.getSession>)),
  };
});

import { closePanel, getPanel } from "../../lib/panel";
import { loadPages } from "../../lib/lazy-page";
import { emptyTaskIndex } from "../../lib/tasks";
import { segments, type Step } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatContext, type ChatContextValue } from "./chat-context";
import { SegmentView } from "./SegmentView";
import { editCounts, turnChanges } from "../../lib/turn-changes";
import { diffStats, editDiffs, lineDiff } from "./ActionRow";
import shared from "../../../../spec/fixtures/fold-changes/cases.json";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CWD = "/Users/me/code/weather";
let seq = 0;
const item = (over: Partial<Item>): Item => ({ id: `i${++seq}#0`, seq, role: "assistant", kind: "text", ts: "2026-11-12T10:00:00Z", ...over });
/** A call and its result, as the transcript has them. */
function call(tool: string, input: Record<string, unknown>, result: Partial<Item> | null = {}): Item[] {
  const id = `c${seq + 1}`;
  const use = item({ kind: "tool_use", tool_use_id: id, tool_name: tool, tool_input: input });
  return result ? [use, item({ role: "user", kind: "tool_result", tool_use_id: id, text: "ok", ...result })] : [use];
}
const edit = (path: string, from: string, to: string, result?: Partial<Item> | null) => call("Edit", { file_path: path, old_string: from, new_string: to }, result);
const write = (path: string, lines: number) => call("Write", { file_path: path, content: Array.from({ length: lines }, (_, n) => `line ${n}`).join("\n") });
const steps = (items: Item[]): Step[] => segments(items)[0].steps;
const ask = () => item({ role: "user", kind: "text", text: "Show 12 hours of rain" });
const done = () => item({ kind: "text", text: "Done." });

describe("turnChanges: what an exchange's own edits changed", () => {
  it("sums each file's edits (Edit, MultiEdit, Write and a Codex diff), in the order first edited, relative to the folder", () => {
    const c = turnChanges(steps([
      ask(),
      ...edit(`${CWD}/Widget/Provider.swift`, "a\nb", "a\nc"),
      ...call("MultiEdit", { file_path: `${CWD}/Widget/Rain.swift`, edits: [{ old_string: "x", new_string: "y\nz" }, { old_string: "q", new_string: "" }] }),
      ...write(`${CWD}/Tests/RainTests.swift`, 3),
      ...edit(`${CWD}/Widget/Provider.swift`, "", "d"),
      ...call("Edit", { file_path: "/elsewhere/notes.md", diff: "@@ -1,2 +1,2 @@\n-old\n+new\n same\n", change: "update" }),
      done(),
    ]), CWD)!;
    expect(c.files.map((f) => [f.shown, f.add, f.del])).toEqual([
      ["Widget/Provider.swift", 2, 1],
      ["Widget/Rain.swift", 2, 2],
      ["Tests/RainTests.swift", 3, 0],
      ["/elsewhere/notes.md", 1, 1],
    ]);
    expect([c.add, c.del, c.exact]).toEqual([8, 4, true]);
    expect(c.files[0].path).toBe(`${CWD}/Widget/Provider.swift`);
  });

  it("counts only edits that succeeded, and never reads or other tools", () => {
    const c = turnChanges(steps([
      ask(),
      ...edit(`${CWD}/a.ts`, "1", "2", { is_error: true, text: "String to replace not found" }),
      ...edit(`${CWD}/b.ts`, "1", "2", null),
      ...call("Read", { file_path: `${CWD}/c.ts` }),
      ...call("Bash", { command: "git apply fix.patch" }),
      ...edit(`${CWD}/d.ts`, "1", "2"),
      done(),
    ]), CWD)!;
    expect(c.files.map((f) => f.shown)).toEqual(["d.ts"]);
  });

  it("has nothing to show without a successful edit", () => {
    expect(turnChanges(steps([ask(), ...call("Read", { file_path: `${CWD}/c.ts` }), done()]), CWD)).toBeNull();
  });

  it("counts folded work by the changes its fold carries, in the order the work came", () => {
    const fold = item({ kind: "fold", fold: { items: 6, tools: 3, thoughts: 0, through_seq: seq + 3, changes: [
      { path: `${CWD}/b.ts`, add: 4, del: 1, exact: true },
      { path: "c.ts", add: 2, del: 0, exact: true },
    ] } });
    const c = turnChanges(steps([ask(), ...edit(`${CWD}/a.ts`, "1", "2"), fold, ...edit(`${CWD}/b.ts`, "x", "y"), done()]), CWD)!;
    expect(c.files.map((f) => [f.shown, f.add, f.del])).toEqual([["a.ts", 1, 1], ["b.ts", 5, 2], ["c.ts", 2, 0]]);
  });

  it("draws no card for a fold without changes (an older node): its edits are not on the page", () => {
    for (const changes of [undefined, null]) {
      const fold = item({ kind: "fold", fold: { items: 6, tools: 3, thoughts: 0, through_seq: seq + 3, changes } });
      expect(turnChanges(steps([ask(), ...edit(`${CWD}/a.ts`, "1", "2"), fold, done()]), CWD)).toBeNull();
    }
    const empty = item({ kind: "fold", fold: { items: 4, tools: 2, thoughts: 0, through_seq: seq + 2, changes: [] } });
    expect(turnChanges(steps([ask(), empty, ...edit(`${CWD}/a.ts`, "1", "2"), done()]), CWD)!.files).toHaveLength(1);
    expect(turnChanges(steps([ask(), empty, done()]), CWD)).toBeNull();
  });

  it("counts a final newline as the end of the last line where both sides allow it, and still shows one added alone", () => {
    expect(lineDiff("", "a\nb\n").map((l) => l.kind)).toEqual(["add", "add"]);
    expect(lineDiff("x\n", "x\ny\n").map((l) => `${l.kind}:${l.text}`)).toEqual(["same:x", "add:y"]);
    expect(lineDiff("\n", "")).toEqual([{ kind: "del", text: "" }]);
    // "foo" becoming "foo\n" changed something: the row and an approval's diff must not come out empty
    expect(lineDiff("foo", "foo\n").map((l) => `${l.kind}:${l.text}`)).toEqual(["same:foo", "add:"]);
    expect(lineDiff("foo\n", "foo").map((l) => `${l.kind}:${l.text}`)).toEqual(["same:foo", "del:"]);
  });

  it("keeps a file whose edit the hub clipped for good, without counts it cannot know", () => {
    const clipped = "line\n".repeat(10) + "\n…（已截断，共 40210 字节）"; // i18n-ignore: the hub's note
    const c = turnChanges(steps([ask(), ...call("Write", { file_path: `${CWD}/big.json`, content: clipped }), ...edit(`${CWD}/a.ts`, "1", "2"), done()]), CWD)!;
    expect(c.files.map((f) => [f.shown, f.exact])).toEqual([["big.json", false], ["a.ts", true]]);
    expect(c.exact).toBe(false);
  });

  it("reads the structural clip marker without changing the retained MultiEdit projection", () => {
    const input = {
      file_path: `${CWD}/a.ts`,
      edits: [{ old_string: "1", new_string: "2" }],
      zz_persona_clipped: { note: "Omitted 8 array items and 0 object fields.\n…（已截断，共 40210 字节）" }, // i18n-ignore: the hub's note
    };
    const c = turnChanges(steps([ask(), ...call("MultiEdit", input), done()]), CWD)!;
    expect(c.files.map((f) => [f.shown, f.add, f.del, f.exact])).toEqual([["a.ts", 1, 1, false]]);
    expect(c.exact).toBe(false);
  });
});

// The cases the hub reads too (server/internal/transcript/fold_changes.go): the same calls counted on the page and the
// fold the node makes of them give one card, so a reload does not change it.
describe("the shared cases (spec/fixtures/fold-changes)", () => {
  type Case = { name: string; input: { items: Item[] }; expected: { changes: { path: string; add: number; del: number; exact: boolean }[] } };
  const out = (c: ReturnType<typeof turnChanges>) => c?.files.map((f) => ({ path: f.path, add: f.add, del: f.del, exact: f.exact })) ?? [];
  it.each((shared.cases as Case[]).map((c) => [c.name, c] as const))("%s: unfolded, and as the hub's fold", (_, c) => {
    const user = item({ role: "user", kind: "text", text: "go" });
    const answer = item({ kind: "text", text: "done" });
    const live = turnChanges(segments([user, ...c.input.items, answer])[0].steps);
    expect(out(live)).toEqual(c.expected.changes);
    const fold = item({ kind: "fold", fold: { items: c.input.items.length, tools: 0, thoughts: 0, through_seq: 0, changes: c.expected.changes } });
    expect(turnChanges(segments([user, fold, answer])[0].steps)).toEqual(live);
    // the card counts without drawing a diff, and comes to what each call's row draws
    for (const use of c.input.items.filter((i) => i.kind === "tool_use" && ["Edit", "Write", "MultiEdit"].includes(i.tool_name ?? ""))) {
      const diffs = editDiffs(use);
      expect(editCounts(use), use.id).toEqual(diffs ? diffStats(diffs) : { add: 0, del: 0 });
    }
  });
});

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  fake.features = [];
  fake.cwd = CWD;
  closePanel();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const ctx = (lineStyle: ChatContextValue["lineStyle"]): ChatContextValue => ({ tasks: emptyTaskIndex(), subagents: [], scope: "t:", lineStyle });
async function draw(items: Item[], o: { lineStyle?: ChatContextValue["lineStyle"]; live?: boolean; trace?: boolean } = {}) {
  const seg = segments(items)[0];
  await act(async () => {
    root.render(
      <ChatContext.Provider value={ctx("lineStyle" in o ? o.lineStyle : "agents")}>
        <SegmentView seg={seg} live={o.live ?? false} trace={o.trace} mid="m" sid="s" expandFold={async () => {}} />
      </ChatContext.Provider>,
    );
  });
  await act(async () => {
    await loadPages();
  });
}
const card = () => host.querySelector<HTMLElement>("[data-turn-changes]");
const rows = () => [...host.querySelectorAll<HTMLElement>("[data-changed-file]")].map((r) => r.dataset.changedFile);
const button = (text: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text));
const five = () => [ask(), ...["a", "b", "c", "d", "e"].flatMap((n) => edit(`${CWD}/src/${n}.ts`, "1", "2")), done()];

describe("the card in the chat", () => {
  it("ends a finished Agent exchange, after its last words", async () => {
    await draw([ask(), ...edit(`${CWD}/a.ts`, "1\n2", "1\n3"), ...write(`${CWD}/b.ts`, 2), done()]);
    expect(card()).not.toBeNull();
    expect(card()!.textContent).toContain("已更改 2 个文件");
    expect(card()!.textContent).toContain("+3 -1");
    expect(rows()).toEqual(["a.ts", "b.ts"]);
    const section = host.querySelector("[data-segment]")!;
    expect(section.lastElementChild).toBe(card());
    expect(card()!.previousElementSibling?.hasAttribute("data-latest-text")).toBe(true);
  });

  it("never shows in a Persona conversation, nor without a known conversation type", async () => {
    await draw([ask(), ...edit(`${CWD}/a.ts`, "1", "2"), done()], { lineStyle: "assistant" });
    expect(card()).toBeNull();
    await draw([ask(), ...edit(`${CWD}/a.ts`, "1", "2"), done()], { lineStyle: undefined });
    expect(card()).toBeNull();
  });

  it("shows after a reload whose page folded the work, from the fold's changes", async () => {
    const fold = item({ kind: "fold", fold: { items: 8, tools: 4, thoughts: 0, through_seq: seq + 4, changes: [
      { path: `${CWD}/src/a.ts`, add: 3, del: 1, exact: true }, { path: `${CWD}/src/b.ts`, add: 2, del: 0, exact: true },
    ] } });
    await draw([ask(), fold, done()]);
    expect(card()!.textContent).toContain("已更改 2 个文件");
    expect(card()!.textContent).toContain("+5 -1");
    expect(rows()).toEqual(["src/a.ts", "src/b.ts"]);
  });

  it("never shows while the exchange runs, nor in a supervision trace", async () => {
    const items = [ask(), ...edit(`${CWD}/a.ts`, "1", "2"), done()];
    await draw(items, { live: true });
    expect(card()).toBeNull();
    await draw(items, { trace: true });
    expect(card()).toBeNull();
    await draw(items);
    expect(card()).not.toBeNull();
  });

  it("lists three files, then Show N more files, and Show less", async () => {
    await draw(five());
    expect(rows()).toEqual(["src/a.ts", "src/b.ts", "src/c.ts"]);
    const more = button("显示其余 2 个文件")!;
    expect(more.getAttribute("aria-expanded")).toBe("false");
    act(() => more.click());
    expect(rows()).toHaveLength(5);
    expect(button("显示其余")).toBeUndefined();
    act(() => button("收起")!.click());
    expect(rows()).toHaveLength(3);
  });

  it("Review opens the branch's review (the turn's commits and what is not committed), a file opens it at that file", async () => {
    fake.features = ["git_review"];
    await draw(five());
    act(() => button("评审")!.click());
    expect(getPanel()).toEqual({ kind: "review", mid: "m", sid: "s", cwd: CWD, scope: "branch" });
    act(() => host.querySelector<HTMLButtonElement>('[data-changed-file="src/b.ts"]')!.click());
    expect(getPanel()).toEqual({ kind: "review", mid: "m", sid: "s", cwd: CWD, scope: "branch", at: { path: `${CWD}/src/b.ts`, file: true } });
  });

  it("a file outside the session's folder opens in the files panel, not the review", async () => {
    fake.features = ["git_review"];
    await draw([ask(), ...edit("/Users/me/.config/tool.json", "1", "2"), ...edit(`${CWD}/./src//a.ts`, "1", "2"), done()]);
    expect(rows()).toEqual(["/Users/me/.config/tool.json", "src/a.ts"]);
    act(() => host.querySelector<HTMLButtonElement>('[data-changed-file="/Users/me/.config/tool.json"]')!.click());
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: "/Users/me/.config/tool.json", cwd: CWD, sid: "s" });
  });

  it("is not drawn when the page starts inside the exchange: edits before it would be missing", async () => {
    await draw([...edit(`${CWD}/a.ts`, "1", "2"), done()]);
    expect(card()).toBeNull();
  });

  it("without a review on this hub, has no Review and a file opens in the files panel", async () => {
    await draw(five());
    expect(button("评审")).toBeUndefined();
    act(() => host.querySelector<HTMLButtonElement>('[data-changed-file="src/a.ts"]')!.click());
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: `${CWD}/src/a.ts`, cwd: CWD, sid: "s" });
  });

  it("is part of the exchange's guessed height off screen, folded at three rows and Show more", async () => {
    const est = async (lineStyle: ChatContextValue["lineStyle"]) => {
      const seg = segments(five())[0];
      await act(async () => {
        root.render(
          <ChatContext.Provider value={ctx(lineStyle)}>
            <SegmentView seg={seg} live={false} mid="m" sid="s" expandFold={async () => {}} offscreen={() => () => {}} />
          </ChatContext.Provider>,
        );
      });
      return parseFloat(host.querySelector<HTMLElement>("[data-segment]")!.style.getPropertyValue("--seg-est"));
    };
    expect((await est("agents")) - (await est("assistant"))).toBe(12 + 8 + 2 + 64 + 1 + 16 + 3 * 36 + 32);
  });

  it("without the session's folder, has no Review either", async () => {
    fake.features = ["git_review"];
    fake.cwd = undefined;
    await draw([ask(), ...edit("/Users/me/code/weather/a.ts", "1", "2"), done()]);
    expect(card()).not.toBeNull();
    expect(button("评审")).toBeUndefined();
  });
});
