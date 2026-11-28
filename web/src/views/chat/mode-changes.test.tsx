// Permission mode change lines in an Agent's chat (#10297): each change among the rows at its time, worded by kind,
// keys that survive the list growing or losing its oldest entry.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatLine, Segment } from "../../lib/turns";

const api = vi.hoisted(() => vi.fn(async (_path: string, _opts?: unknown) => ({ changes: [] })));
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<object>()), api }));
import type { ChatDayRow } from "./day-dividers";
import { modeChangeLabel, ModeChangeDivider } from "./ModeChangeDivider";
import * as kit from "./mode-changes";
import { modeChangesOf, withModeChanges, type ModeChange } from "./mode-changes";

const at = (min: number) => new Date(Date.UTC(2026, 10, 18, 9, min)).toISOString();
const line = (id: string, min: number): ChatDayRow => {
  const seg = { id, user: { id: id + "u", text: id, images: [], ts: at(min), contexts: [] }, start: at(min), steps: [], tools: 0, thoughts: 0, replies: 0, folds: [] } as Segment;
  return { kind: "line", id, line: { kind: "segment", id, seg, live: false } as ChatLine };
};
const change = (min: number, extra: Partial<ModeChange> = {}): ModeChange => ({ kind: "set", from: "default", to: "acceptEdits", applied: "now", actor_kind: "device", device_name: "Phone", at: at(min), ...extra });
const ids = (rows: { id: string }[]) => rows.map((r) => r.id);

describe("withModeChanges", () => {
  it("puts each change before the first row after it, the newest after the last", () => {
    const rows = withModeChanges([line("a", 0), line("b", 10), line("c", 20)], [change(15), change(5), change(30, { to: "plan" })]);
    expect(ids(rows)).toEqual(["a", `mode-${Date.parse(at(5))}-set-acceptEdits`, "b", `mode-${Date.parse(at(15))}-set-acceptEdits`, "c", `mode-${Date.parse(at(30))}-set-plan`]);
  });

  it("leaves the rows as they are without changes", () => {
    const rows = [line("a", 0)];
    expect(withModeChanges(rows, [])).toBe(rows);
  });

  it("keeps a change from before the loaded history out until its page is there", () => {
    const rows = withModeChanges([line("b", 10), line("c", 20)], [change(5), change(15)], [{ id: "i", seq: 1, role: "user", kind: "text", text: "b", ts: at(10) }]);
    expect(rows.filter((r) => r.kind === "mode")).toHaveLength(1);
    expect(ids(rows)[1]).toBe(`mode-${Date.parse(at(15))}-set-acceptEdits`);
  });

  it("numbers two changes of the same moment and keeps keys when the oldest one drops off", () => {
    const twice = [change(5), change(5), change(15)];
    const before = withModeChanges([line("a", 0)], twice).filter((r) => r.kind === "mode").map((r) => r.id);
    expect(new Set(before).size).toBe(3);
    const after = withModeChanges([line("a", 0)], twice.slice(1)).filter((r) => r.kind === "mode").map((r) => r.id);
    expect(after.at(-1)).toBe(before.at(-1));
  });
});

describe("modeChangesOf", () => {
  it("takes the well-formed changes and nothing from another answer", () => {
    expect(modeChangesOf({ changes: [change(1), { kind: "made up", at: at(2) }, { kind: "set", at: "never" }] })).toHaveLength(1);
    expect(modeChangesOf({ items: [] })).toEqual([]);
    expect(modeChangesOf(null)).toEqual([]);
  });
});

describe("ModeChangeDivider", () => {
  it("says who changed what in quotes, and how a restart went and why (tests run in zh-Hans)", () => {
    expect(modeChangeLabel(change(1, { to: "bypassPermissions", applied: "restart" }))).toBe("Phone 选择了「完全访问」，本轮结束后将重启会话并切换至该模式");
    expect(modeChangeLabel(change(1))).toBe("Phone 将权限模式从「默认权限」更改为「允许编辑」");
    expect(modeChangeLabel(change(1, { from: "" }))).toBe("Phone 将权限模式更改为「允许编辑」");
    expect(modeChangeLabel(change(1, { actor_kind: "hub", device_name: "" }))).toBe("hub 将权限模式从「默认权限」更改为「允许编辑」");
    expect(modeChangeLabel(change(1, { actor_kind: "assistant", device_name: "Persona", from: "default", to: "plan" }))).toBe("Persona 将权限模式从「默认权限」更改为「计划模式」");
    expect(modeChangeLabel(change(1, { actor_kind: "device", device_name: "Phone", from: "plan", to: "acceptEdits", applied: "" }))).toBe("Phone 将权限模式从「计划模式」更改为「允许编辑」");
    const outcome = (kind: ModeChange["kind"], reason?: string) => modeChangeLabel({ kind, to: "bypassPermissions", reason, at: at(1) });
    expect(outcome("restart_done")).toBe("已重启并切换至「完全访问」");
    expect(outcome("restart_done", "already_in")).toBe("当前已是「完全访问」，无需重启");
    expect(outcome("restart_dropped", "process_replaced")).toBe("已取消，不会重启并切换至「完全访问」：会话已在另一进程中运行");
    expect(outcome("restart_dropped", "process_exited")).toBe("已取消，不会重启并切换至「完全访问」：原进程已退出");
    expect(outcome("restart_dropped", "replaced")).toBe("已取消，不会重启并切换至「完全访问」：已被新的切换请求取代");
    // the mode it runs in picked again (#11089): called off, not replaced
    expect(outcome("restart_dropped", "cancelled")).toBe("已取消，不会重启并切换至「完全访问」：会话保持当前模式");
    expect(outcome("restart_failed", "start_failed")).toBe("无法重启并切换至「完全访问」：新进程启动失败，会话将继续以原模式运行");
    // no reason, or English words from before the codes: no colon
    expect(outcome("restart_failed")).toBe("无法重启并切换至「完全访问」");
    expect(outcome("restart_dropped", "claude 4101, the process it was picked for, has exited")).toBe("已取消，不会重启并切换至「完全访问」");
    // a code this app does not know yet (a newer node): the plain line
    expect(outcome("restart_dropped", "some_later_code")).toBe("已取消，不会重启并切换至「完全访问」");
  });

  it("is a separator, not a control", () => {
    const html = renderToStaticMarkup(<ModeChangeDivider change={change(1)} />);
    expect(html).toContain('role="separator"');
    expect(html).toContain('data-mode-change="set"');
    expect(html).not.toContain("<button");
  });
});

describe("watchModeChanges", () => {
  afterEach(() => { vi.useRealTimers(); api.mockClear(); });

  it("asks once when a chat opens or is shown again, twice only after its mode changed", async () => {
    vi.useFakeTimers();
    const look = (key: string) => kit.watchModeChanges("m", "s-count", key, new AbortController().signal, kit, () => {});
    look("default|false"); // opened
    look("default|false"); // shown again, the page back in front, a re-render: the same mode
    await vi.advanceTimersByTimeAsync(5000);
    expect(api).toHaveBeenCalledTimes(2);
    look("bypassPermissions|false"); // the mode changed: now and once more 2 s later
    await vi.advanceTimersByTimeAsync(5000);
    expect(api).toHaveBeenCalledTimes(4);
    expect(api.mock.calls.every((c) => String(c[0]).endsWith("/mode-changes"))).toBe(true);
  });
});
