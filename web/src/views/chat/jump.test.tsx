// @vitest-environment jsdom
// "Jump to the step that started it" (#2719): a background task's notice, opened, has the button when its call is known; pressing it
// opens the work group the call is folded in, so the call's row is drawn, and a call no exchange holds opens nothing.
// The exchange is made up (a Bash run in the background, its notice in a later exchange).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { buildTaskIndex } from "../../lib/tasks";
import { segments } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatContext } from "./chat-context";
import { openToCall, seekCall } from "./jump";
import { SegmentView } from "./SegmentView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-05T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
let seq = 0;
const mk = (id: string, role: Item["role"], kind: Item["kind"], s: number, extra: Partial<Item> = {}): Item => ({ id, seq: ++seq, role, kind, ts: at(s), ...extra });

const items: Item[] = [
  mk("u1", "user", "text", 0, { text: "跑一遍测试" }),
  mk("g", "assistant", "tool_use", 1, { tool_name: "Grep", tool_use_id: "tu-g", tool_input: { pattern: "x" } }),
  mk("g-r", "user", "tool_result", 2, { tool_use_id: "tu-g", text: "ok" }),
  mk("bg", "assistant", "tool_use", 3, { tool_name: "Bash", tool_use_id: "tu-bg", tool_input: { command: "npm test", description: "跑测试", run_in_background: true } }),
  mk("bg-r", "user", "tool_result", 4, { tool_use_id: "tu-bg", text: "Command running in background with ID: b1" }),
  mk("a1", "assistant", "text", 5, { text: "测试在后台跑。" }),
  mk("u2", "user", "text", 60, { text: "好了吗" }),
  mk("n1", "system", "notice", 61, { text: "后台命令完成", task: { kind: "shell", status: "completed", task_id: "b1", detail: "npm test" } }),
  mk("a2", "assistant", "text", 62, { text: "测试都过了。" }),
];

let host: HTMLDivElement;
let root: Root;
const scope = "m/s:";
const jumps: string[] = [];

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  jumps.length = 0;
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const button = (pick: (b: HTMLButtonElement) => boolean) => [...host.querySelectorAll("button")].find(pick);

async function draw(shown: Item[] = items) {
  const ctx = { tasks: buildTaskIndex(shown), subagents: [], scope, jumpToCall: (call: string) => { jumps.push(call); openToCall(scope, call); } };
  await act(async () => {
    root.render(
      <ChatContext.Provider value={ctx}>
        {segments(shown).map((seg) => <SegmentView key={seg.id} seg={seg} live={false} mid="m" sid="s" expandFold={async () => {}} />)}
      </ChatContext.Provider>,
    );
  });
  await flush();
}

describe("跳到发起的这一步", () => {
  it("opens the group the call is folded in, and its row is drawn", async () => {
    await draw();
    expect(host.querySelector('[data-tool-use="tu-bg"]')).toBeNull(); // folded in its finished group
    // the notice is in the later exchange's group: open it, as the reader would
    const later = [...host.querySelectorAll<HTMLElement>("[data-group-id]")].at(-1)!.querySelector("button")!;
    await act(async () => later.click());
    await flush();
    const notice = button((b) => b.textContent?.includes("后台命令") === true);
    expect(notice, "the notice's row").toBeTruthy();
    await act(async () => notice!.click());
    const jump = host.querySelector<HTMLButtonElement>("[data-jump-to-call]");
    expect(jump?.textContent).toBe("跳转到发起此操作的步骤");
    await act(async () => jump!.click());
    await flush();
    expect(jumps).toEqual(["tu-bg"]);
    expect(host.querySelector('[data-tool-use="tu-bg"]'), "the call's row").toBeTruthy();
  });

  it("a call no exchange holds opens nothing", async () => {
    await draw();
    expect(openToCall(scope, "tu-nope")).toBe(false);
    expect(openToCall("other/chat:", "tu-bg")).toBe(false); // another chat's exchanges do not answer
  });

  it("fetches the folded runs of work, newest first, until the call shows up", async () => {
    // the first page as the hub folds it (fold_work): the run holding the call is one fold item
    const run = items.slice(1, 5);
    const fold: Item = { id: "fold:g", seq: run[0].seq, ts: run[0].ts, role: "assistant", kind: "fold", fold: { items: run.length, tools: 2, thoughts: 0, until: run[3].ts!, through_seq: run[3].seq, through_id: run[3].id } } as Item;
    const page = [items[0], fold, ...items.slice(5)];
    await draw(page);
    expect(openToCall(scope, "tu-bg")).toBe(false); // not among the steps drawn
    const fetched: string[] = [];
    const found = await seekCall(scope, "tu-bg", page, async (f) => {
      fetched.push(f.id);
      await draw(items); // the fold's steps in its place
    }, flush);
    expect(found).toBe(true);
    expect(fetched).toEqual(["fold:g"]);
    expect(host.querySelector('[data-tool-use="tu-bg"]'), "the call's row, its group opened").toBeTruthy();
  });

  it("says no once every fold is fetched and the call is still not there", async () => {
    await draw();
    expect(await seekCall(scope, "tu-nope", items, async () => {}, flush)).toBe(false);
  });
});
