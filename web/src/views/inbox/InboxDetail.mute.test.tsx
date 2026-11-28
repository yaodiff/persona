// @vitest-environment jsdom
// The detail's "Mute" button (iOS InboxDetailView's menu): hidden with nothing to mute by; its menu lists what the item
// can be muted by, and choosing one sends POST /api/v1/inbox/mutes with the item and the tag.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";

vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { InboxDetail } from "./InboxDetail";
import { settleRosterDefault } from "../../lib/manager-choice";

// the roster's default is known, as once the app has read it: an item's page draws at once
settleRosterDefault();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const sender = { tag: "sender", value: "u1", name: "示例甲" };
const chat = { tag: "chat", value: "c1", name: "示例群" };
const item = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "github:o/r#1", source: "github", kind: "mention", account: "github", account_name: "GitHub", title: "示例 PR", preview: "看一下",
  count: 1, tier: "action", ...over,
});

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const muteButton = () => host.querySelector<HTMLButtonElement>('button[aria-label="静音"]');

beforeEach(() => {
  fetcher = vi.fn().mockResolvedValue({ ok: true, status: 200, text: async () => JSON.stringify({ settled: 1 }) });
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("收件箱详情的静音", () => {
  it("has no 静音 button for an item with nothing to mute by", () => {
    act(() => root.render(<InboxDetail item={item()} loaded />));
    expect(muteButton()).toBeNull();
  });

  it("lists what it can be muted by and mutes with the chosen one", async () => {
    act(() => root.render(<InboxDetail item={item({ mutes: [sender, chat] })} loaded />));
    act(() => muteButton()!.click());
    const rows = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')];
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining("静音此发件人示例甲"), expect.stringContaining("静音此会话示例群"),
      expect.stringContaining("为此发件人设置规则…示例甲"), expect.stringContaining("为此会话设置规则…示例群"),
    ]);
    act(() => rows[1].click());
    await flush();
    const post = fetcher.mock.calls.find(([url]) => url === "/api/v1/inbox/mutes");
    expect(post).toBeTruthy();
    expect(post![1].method).toBe("POST");
    expect(JSON.parse(post![1].body)).toEqual({ item_id: "github:o/r#1", tag: "chat" });
  });
});
