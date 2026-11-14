// A row's right-click menu (iOS InboxView.rowMenu): "Done" or "Move back" first, then what the item can be muted by.
import { describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";

const calls = vi.hoisted(() => ({ done: [] as unknown[][], mute: [] as unknown[][], toast: [] as unknown[][], fail: false }));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: (...a: unknown[]) => void calls.toast.push(a),
}));
vi.mock("../../lib/inbox", async (original) => ({
  ...(await original<typeof import("../../lib/inbox")>()),
  setDone: (...a: unknown[]) => (calls.done.push(a), Promise.resolve(!calls.fail)),
  muteItem: (...a: unknown[]) => (calls.mute.push(a), Promise.resolve()),
}));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
import { markFromRow, rowMenu } from "./InboxList";

const sender = { tag: "sender", value: "u1", name: "示例甲" };
const chat = { tag: "chat", value: "c1", name: "示例群" };
const item = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:f1:1", source: "lark", kind: "dm", account: "lark", account_name: "飞书", title: "示例甲", preview: "", count: 1, ...over,
});

describe("收件箱的行菜单", () => {
  it("offers 已处理 and 加入待办 for an open item with nothing to mute by", () => {
    const m = rowMenu(item(), false);
    expect(m.map((x) => [x.key, x.label])).toEqual([["done", "已处理"], ["todo", "加入待办"]]);
    m[0].onSelect();
    expect(calls.done.at(-1)).toEqual([expect.objectContaining({ id: "lark:f1:1" }), true]);
  });

  it("已处理 from the menu says so with 撤销 even with a mouse (#4162)", async () => {
    calls.toast.length = 0;
    rowMenu(item(), false)[0].onSelect();
    await vi.waitFor(() => expect(calls.toast).toHaveLength(1));
    const [text, action] = [calls.toast[0][0], calls.toast[0][2] as { label: string; run: () => void }];
    expect([text, action.label]).toEqual(["已标为已处理", "撤销"]);
    action.run();
    expect(calls.done.at(-1)?.[1]).toBe(false);
  });

  it("offers 移回 for a done item", () => {
    const m = rowMenu(item({ done_at: "2026-11-01T09:00:00Z" }), true);
    expect(m.map((x) => x.key)).toEqual(["undone"]);
    m[0].onSelect();
    expect(calls.done.at(-1)?.[1]).toBe(false);
  });

  it("puts the mutes under a divider on the first, each muting with its own option", () => {
    const it0 = item({ mutes: [sender, chat] });
    const m = rowMenu(it0, false);
    expect(m.map((x) => x.key)).toEqual(["done", "todo", "mute:sender:u1", "mute:chat:c1", "policy:sender:u1", "policy:chat:c1"]);
    expect(m.map((x) => !!x.divider)).toEqual([false, false, true, false, true, false]);
    expect(m[2].detail).toBeTruthy();
    m[3].onSelect();
    expect(calls.mute.at(-1)).toEqual([it0, chat]);
  });

  it("adds 移到文件夹… last when the list shows folders", () => {
    expect(rowMenu(item({ mutes: [sender] }), false).some((x) => x.key === "folder")).toBe(false);
    const m = rowMenu(item({ mutes: [sender], folder: "work/alerts" }), false, true);
    expect(m.map((x) => x.key)).toEqual(["done", "todo", "mute:sender:u1", "policy:sender:u1", "folder"]);
    expect([m[4].label, m[4].hint, m[4].divider]).toEqual(["移到文件夹…", "work/alerts", true]);
  });
});

describe("行内的已处理（#4141）", () => {
  it("on a touch screen marks done and offers 撤销, which moves it back", async () => {
    calls.toast.length = 0;
    await markFromRow(item(), true, true);
    expect(calls.done.at(-1)?.[1]).toBe(true);
    expect(calls.toast).toHaveLength(1);
    const [text, tone, action] = calls.toast[0] as [string, string, { label: string; run: () => void }];
    expect([text, tone, action.label]).toEqual(["已标为已处理", "ok", "撤销"]);
    action.run();
    expect(calls.done.at(-1)?.[1]).toBe(false);
  });

  it("with a mouse, or moving back, marks without a toast", async () => {
    calls.toast.length = 0;
    await markFromRow(item(), true, false);
    await markFromRow(item({ done_at: "2026-11-01T09:00:00Z" }), false, true);
    expect(calls.toast).toHaveLength(0);
    expect(calls.done.at(-1)?.[1]).toBe(false);
  });

  it("says nothing when the hub did not take it (setDone shows its own error)", async () => {
    calls.toast.length = 0;
    calls.fail = true;
    await markFromRow(item(), true, true);
    calls.fail = false;
    expect(calls.toast).toHaveLength(0);
  });
});
