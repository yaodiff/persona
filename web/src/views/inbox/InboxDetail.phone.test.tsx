// @vitest-environment jsdom
// On a phone-width screen the detail's title comes first (#4144): "Pinned messages", "Mute" and "Same matter as…" fold into one ⋯ menu, and the
// time in the source line keeps its own span so it is not the part that gets cut.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";

vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("../../lib/viewport", async (original) => ({ ...(await original<typeof import("../../lib/viewport")>()), useMedia: (q: string) => q === "(max-width: 639px)" }));
vi.mock("./LarkThread", () => ({ LarkThread: () => null, chatOfItem: () => ({}) }));
import { InboxDetail } from "./InboxDetail";
import { settleRosterDefault } from "../../lib/manager-choice";

// the roster's default is known, as once the app has read it: an item's page draws at once
settleRosterDefault();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:f1:1", source: "lark", kind: "mention", account: "lark", account_name: "飞书", title: "示例群", preview: "看一下",
  count: 1, tier: "action", updated_at: new Date().toISOString(), lark: { is_group: true } as InboxItem["lark"],
  mutes: [{ tag: "chat", value: "c1", name: "示例群" }], ...over,
});

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("收件箱详情在手机宽度", () => {
  it("folds Pin 消息 and 静音 into one 更多 menu", () => {
    act(() => root.render(<InboxDetail item={item()} loaded />));
    expect(host.querySelector('button[aria-label="Pin 消息"]')).toBeNull();
    expect(host.querySelector('button[aria-label="静音"]')).toBeNull();
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="更多"]')!.click());
    const rows = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((r) => r.textContent);
    expect(rows).toEqual([expect.stringContaining("加入待办"), expect.stringContaining("Pin 消息"), expect.stringContaining("静音这个会话示例群"), expect.stringContaining("为这个会话设规则…示例群"), expect.stringContaining("是同一件事…")]);
  });

  it("folds a lone Pin 消息 with 是同一件事… into 更多", () => {
    act(() => root.render(<InboxDetail item={item({ mutes: [] })} loaded />));
    expect(host.querySelector('button[aria-label="Pin 消息"]')).toBeNull();
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="更多"]')!.click());
    const rows = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((r) => r.textContent);
    expect(rows).toEqual([expect.stringContaining("加入待办"), expect.stringContaining("Pin 消息"), expect.stringContaining("是同一件事…")]);
  });

  it("keeps the time whole in its own span", () => {
    act(() => root.render(<InboxDetail item={item()} loaded />));
    const time = [...host.querySelectorAll("p span")].find((s) => /· \d\d:\d\d$/.test(s.textContent ?? ""));
    expect(time?.className).toContain("shrink-0");
  });

  // audit item 60: the words took all the free width (flex-1), so the time sat at the line's far end as " · 08:29";
  // now they take their own width and the time follows them, with Original / Handling pushed to the end on its own
  it("puts the time right after where it came from, not at the far end", () => {
    act(() => root.render(<InboxDetail item={item()} loaded />));
    const meta = host.querySelector("[data-detail-meta]")!;
    const [words, time] = [...meta.children];
    expect(words.className).toContain("truncate");
    expect(words.className).not.toContain("flex-1");
    expect(time.textContent).toMatch(/^ · \d\d:\d\d$/);
    expect(time.previousElementSibling).toBe(words);
    act(() => root.render(<InboxDetail item={item({ lark: undefined, chain: [{ id: "c1", kind: "received", text: "看一下" }] } as Partial<InboxItem>)} loaded />));
    expect(host.querySelector("[data-detail-meta] button")?.className).toContain("ml-auto");
  });

  it("truncates the where line and keeps Original on one line", () => {
    act(() => root.render(<InboxDetail item={item({ lark: undefined, where: "一个很长的会话名称", now: "needs_you" })} loaded />));
    const meta = host.querySelector("[data-detail-meta]")!;
    expect(meta.querySelector("span")?.className).toContain("truncate");
    expect(meta.querySelector("span")?.className).toContain("min-w-0");
    const original = meta.querySelector("button")!;
    expect(original.textContent).toBe("看原文");
    expect(original.className).toContain("whitespace-nowrap");
    expect(original.className).toContain("shrink-0");
  });
});
