import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";
import type { InboxItem } from "../../lib/inbox";
import type { UIPlace } from "../../lib/extension-ui";
import { InboxDetail } from "./InboxDetail";
import { fallbackOf, InboxExtensionBody } from "./InboxExtensionBody";
import { settleRosterDefault } from "../../lib/manager-choice";

// the roster's default is known, as once the app has read it: an item's page draws at once
settleRosterDefault();

// The slot list as lib/extension-slots.ts caches it: one extension draws the items of its connector examplechat.
const place: UIPlace = { extension: "example-chat", extension_name: "Example Chat", id: "event", title: "事件", placement: "inbox_item", size: "full", connector: "examplechat" };
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: (source?: string) => (source === "examplechat" ? place : undefined) }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./LarkThread", () => ({ LarkThread: () => <div data-chat-body>chat body</div>, chatOfItem: () => ({}) }));

const item = (over: Partial<InboxItem>): InboxItem => ({
  id: "examplechat:1", source: "examplechat", kind: "dm", account: "examplechat", account_name: "Example Chat", title: "周会", preview: "周四下午可以吗",
  count: 1, summary: "周四下午开会", tier: "action", version: "v1", ...over,
});

describe("收件箱：扩展画的详情（inbox_item）", () => {
  it("draws the extension's page under the detail's own header for its connector's items", () => {
    const html = renderToStaticMarkup(<InboxDetail item={item({})} loaded />);
    expect(html).toContain('data-extension-inbox="example-chat/event"');
    expect(html).toContain("周会"); // the header stays the web's
    expect(html).toContain("周四下午开会");
    expect(html).toContain("已处理");
    expect(html).not.toContain("周四下午可以吗"); // the own body is not drawn under it
    // a chat's Pin works on the chat the own body draws: not there under the extension's page
    expect(renderToStaticMarkup(<InboxDetail item={item({ lark: { feed_id: "f1", is_group: false } })} loaded />)).not.toContain("Pin 消息");
  });
  it("keeps a chat original collapsed and unmounted even when the extension offers a page", () => {
    const html = renderToStaticMarkup(<InboxDetail item={item({ lark: { feed_id: "f1", is_group: false }, chain: [{ id: "c1", kind: "received", text: "New message" }] })} loaded />);
    expect(html).not.toContain('data-event-body="embedded"');
    expect(html).not.toContain("data-chat-body");
    expect(html).not.toContain("Pin 消息");
    expect(html).toContain('data-testid="inbox-original-toggle"');
    expect(html).toContain('data-testid="inbox-original-row-toggle"');
    expect(html).not.toContain("data-extension-inbox");
  });
  it("keeps the own detail for a source without one", () => {
    const html = renderToStaticMarkup(<InboxDetail item={item({ id: "x:1", source: "telegramx" })} loaded />);
    expect(html).not.toContain("data-extension-inbox");
    expect(html).toContain("周四下午可以吗");
  });
  it("offers Same matter as on open and done items without eagerly loading the picker", () => {
    for (const current of [item({}), item({ done_at: "2026-01-01T00:00:00Z", done_by: "user" })]) {
      const html = renderToStaticMarkup(<InboxDetail item={current} loaded />);
      expect(html).toContain('aria-label="是同一件事…"');
      expect(html).not.toContain("搜索标题和位置");
    }
  });
  it("falls back quietly when refused or there is nothing to draw, with why when the extension fails", () => {
    expect(fallbackOf(new ApiError(403, "这一条不是这个扩展的连接器收进来的", { code: "forbidden" }))).toEqual({ quiet: true });
    expect(fallbackOf(new ApiError(404, "收件箱里没有这一条", { code: "not_found" }))).toEqual({ quiet: true });
    const failed = fallbackOf(new ApiError(502, "扩展画的界面这个 hub 画不了", { code: "plugin_failed" }));
    expect("error" in failed && failed.error).toContain("画不了");
    expect(fallbackOf(new Error(""))).toEqual({ error: "出错了" });
  });
  it("draws while loading in its own scroller, not the own body", () => {
    const html = renderToStaticMarkup(<InboxExtensionBody item={item({})} place={place} own={() => <p>own body</p>} />);
    expect(html).toContain("overflow-y-auto");
    expect(html).not.toContain("own body");
  });
});
