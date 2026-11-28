// @vitest-environment jsdom
// A connector's chat opened from the inbox (#2728): a group says how many are in it, a quote leads back to the message
// it answers (reading back a page when it is not loaded), and a reply to one message carries its id as reply_to.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ThreadMessage, ThreadPage } from "../../lib/connectors";
import type { InboxItem } from "../../lib/inbox";

const calls = vi.hoisted(() => ({ load: [] as (string | undefined)[], reply: [] as unknown[], managers: [] as (string | undefined)[] }));
const pages = vi.hoisted(() => ({ newest: null as unknown, older: null as unknown }));
vi.mock("../../lib/connectors", async (original) => ({
  ...(await original<typeof import("../../lib/connectors")>()),
  loadThread: (_id: string, _thread: string, before?: string, manager?: string) => {
    calls.load.push(before);
    calls.managers.push(manager);
    return Promise.resolve(before ? pages.older : pages.newest);
  },
  replyInThread: (_id: string, body: unknown, manager?: string) => {
    calls.reply.push(body);
    calls.managers.push(manager);
    return Promise.resolve({ ok: true });
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../../ui/atoms";
import { ConnectorBody, openSystem } from "./ConnectorThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const msg = (id: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({
  id, at: "2026-11-01T09:00:00Z", sender: { id: "wxid_example_a", name: "示例用户" }, text: `消息 ${id}`, ...extra,
});
const item: InboxItem = {
  id: "examplechat:1", source: "examplechat", kind: "group", account: "examplechat", account_name: "Example Chat", title: "示例群",
  preview: "", count: 1, connector: { thread: "primary.example-group" },
} as InboxItem;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("button")];

beforeEach(() => {
  calls.load = [];
  calls.reply = [];
  calls.managers = [];
  vi.mocked(toast).mockClear();
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("连接器会话页", () => {
  it("opens a Telegram sender's sized avatar on the item's manager and keeps the legacy media fallback", async () => {
    const path = "/api/v1/avatars/telegram/YS51MS41MDE";
    pages.newest = {
      chat: { id: "g", name: "Workshop", kind: "group", avatar_url: "/api/v1/avatars/telegram/Z3JvdXA" },
      messages: [
        msg("1", { sender: { name: "Artist", avatar: "a.u1.501", avatar_url: path } }),
        msg("2", { sender: { name: "Editor", avatar: "legacy:2" } }),
        msg("3", { sender: { name: "Guest" } }),
      ],
    };
    const face = (id: string) => host.querySelector<HTMLImageElement>(`[data-mid="${id}"] img`);
    act(() => root.render(<ConnectorBody item={{ ...item, source: "telegram", manager: "studio" }} />));
    await flush();
    const first = new URL(face("1")!.src);
    expect(first.pathname).toBe(path);
    expect(first.searchParams.get("size")).toBe(String(32 * devicePixelRatio));
    expect(first.searchParams.get("manager")).toBe("studio");
    const legacy = new URL(face("2")!.src);
    expect(legacy.pathname).toBe("/api/v1/connectors/telegram/media");
    expect(legacy.searchParams.get("ref")).toBe("legacy:2");
    expect(legacy.searchParams.get("manager")).toBe("studio");
    expect(face("3")).toBeNull();
    act(() => face("1")!.dispatchEvent(new Event("error")));
    expect(face("1")).toBeNull();
    expect(host.querySelector('[data-mid="1"] [aria-hidden]')?.textContent).toBe("A");
    act(() => root.render(<ConnectorBody item={{ ...item, source: "telegram", manager: "garden" }} />));
    await flush();
    expect(new URL(face("1")!.src).searchParams.get("manager")).toBe("garden");
  });

  it("uses the GitHub author's and commenter's separate hub pictures", async () => {
    const author = "/api/v1/avatars/github/YXV0aG9y", reviewer = "/api/v1/avatars/github/cmV2aWV3ZXI";
    pages.newest = {
      chat: { id: "demo#1", name: "A change", kind: "pr", body: "Description",
        author: { name: "Author", avatar: "old-author", avatar_url: author } },
      messages: [msg("1", { sender: { name: "Reviewer", avatar: "old-reviewer", avatar_url: reviewer } })],
    };
    act(() => root.render(<ConnectorBody item={{ ...item, source: "github" }} />));
    await flush();
    const pictures = [...host.querySelectorAll<HTMLImageElement>("img")].map((img) => new URL(img.src));
    expect(pictures.map((url) => url.pathname)).toEqual([author, reviewer]);
    expect(pictures.every((url) => url.searchParams.get("size") === String(28 * devicePixelRatio) && !url.searchParams.has("manager"))).toBe(true);
  });

  it("keeps own-message quotes, attachment details and focus readable on blue bubbles", async () => {
    pages.newest = {
      chat: { id: "demo", name: "Demo group", kind: "group" },
      messages: [msg("demo-own", { mine: true, text: "Demo reply", reply_to: { id: "demo-earlier", text: "Demo quote" }, media: [{ kind: "file", name: "demo.txt", size: 120 }] })],
    } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    const row = host.querySelector('[data-mid="demo-own"]')!;
    expect(row.querySelector(".bg-user-bubble")).not.toBeNull();
    expect(row.querySelector("button.border-l-2")?.className).toContain("text-ink-3");
    expect(row.querySelector("button.border-l-2")?.className).toContain("focus-visible:outline-accent");
    expect(row.querySelector("button.border-l-2")?.className).toContain("hover:text-ink");
    expect(row.querySelector("button.border-l-2")?.className).not.toContain("opacity-80");
    expect(row.querySelector("p.text-ink-2")?.textContent).toContain("demo.txt");
    expect(row.innerHTML).not.toContain("text-white");
    expect(row.innerHTML).not.toContain("outline-white");
  });
  it("says the group's size over the messages", async () => {
    pages.newest = { chat: { id: "g", name: "示例群", kind: "group", members: 128 }, messages: [msg("1")] } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    expect(host.textContent).toContain("群聊 · 128 位成员");
  });

  it("leads a quote back to its message, reading back a page for one not loaded", async () => {
    pages.newest = {
      chat: { id: "g", name: "示例群", kind: "group" },
      messages: [msg("5"), msg("6", { reply_to: { id: "5", from: "示例用户", text: "消息 5" } }), msg("7", { reply_to: { id: "2", text: "更早的" } })],
      before: "5",
    } satisfies ThreadPage;
    pages.older = { chat: { id: "g", name: "示例群", kind: "group" }, messages: [msg("2")] } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    const quote = (text: string) => buttons().find((b) => b.getAttribute("aria-label")?.includes(text))!;
    act(() => quote("消息 5").click());
    await flush();
    expect(calls.load).toEqual([undefined]); // on the page already: nothing more read
    expect(host.querySelector('[data-mid="5"]')!.className).toContain("bg-accent-tint");
    act(() => quote("更早的").click());
    await flush();
    expect(calls.load).toEqual([undefined, "5"]);
    expect(host.querySelector('[data-mid="2"]')).not.toBeNull();
    expect(host.querySelector('[data-mid="2"]')!.className).toContain("bg-accent-tint");
    expect(toast).not.toHaveBeenCalled();
  });

  it("says so when the quoted message is not found", async () => {
    pages.newest = { chat: { id: "g", name: "示例群", kind: "group" }, messages: [msg("6", { reply_to: { id: "x", text: "很早的" } })] } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    act(() => buttons().find((b) => b.getAttribute("aria-label")?.includes("很早的"))!.click());
    await flush();
    expect(toast).toHaveBeenCalledWith("原消息较早，请向上翻页查找。");
  });

  it("sends a reply to one message with its id as reply_to", async () => {
    pages.newest = { chat: { id: "g", name: "示例群", kind: "group", reply: "examplechat_send" }, messages: [msg("1"), msg("2")] } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    const reply = buttons().filter((b) => b.textContent?.trim() === "回复");
    expect(reply).toHaveLength(2);
    act(() => reply[0].click());
    await flush();
    const box = host.querySelector("textarea")!;
    expect(document.activeElement).toBe(box);
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => {
      setter.call(box, "好的");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls.reply).toEqual([{ thread: "primary.example-group", action: "examplechat_send", text: "好的", reply_to: "1" }]);
    expect(calls.managers.every((m) => m === undefined)).toBe(true); // the default's: no manager named
  });

  it("another manager's item: its thread is read and answered through that manager", async () => {
    pages.newest = { chat: { id: "g", name: "示例群", kind: "group", reply: "examplechat_send" }, messages: [msg("1")] } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={{ ...item, manager: "m-1" }} />));
    await flush();
    act(() => buttons().find((b) => b.textContent?.trim() === "回复")!.click());
    await flush();
    const box = host.querySelector("textarea")!;
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => {
      setter.call(box, "好的");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls.reply).toHaveLength(1);
    expect(calls.managers.length).toBeGreaterThanOrEqual(2);
    expect(calls.managers.every((m) => m === "m-1")).toBe(true);
  });

  it("opens a picture full screen in the image viewer (G039)", async () => {
    pages.newest = {
      chat: { id: "g", name: "示例群", kind: "group" },
      messages: [msg("1", { text: "", media: [{ kind: "image", ref: "img-1", width: 800, height: 600 }] })],
    } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    const picture = host.querySelector<HTMLButtonElement>("button[data-thread-picture]")!;
    expect(picture.getAttribute("aria-label")).toBe("查看图片");
    act(() => picture.click());
    const viewer = document.querySelector<HTMLElement>('[aria-label="图片"]');
    expect(viewer).not.toBeNull();
    expect(viewer!.querySelector("img")?.getAttribute("src")).toContain("img-1");
  });

  describe("when its box changes size", () => {
    // fired by hand for whatever watches the element; jsdom has no layout, so the list's sizes are the test's
    const watchers: { cb: ResizeObserverCallback; targets: Set<Element> }[] = [];
    beforeEach(() => {
      watchers.length = 0;
      vi.stubGlobal("ResizeObserver", class {
        targets = new Set<Element>();
        constructor(cb: ResizeObserverCallback) { watchers.push({ cb, targets: this.targets }); }
        observe(el: Element) { this.targets.add(el); }
        unobserve(el: Element) { this.targets.delete(el); }
        disconnect() { this.targets.clear(); }
      });
    });
    afterEach(() => vi.unstubAllGlobals());
    const resize = (el: Element) => act(() => {
      for (const w of watchers) if (w.targets.has(el)) w.cb([], {} as ResizeObserver);
    });
    const open = async (page: ThreadPage) => {
      pages.newest = page;
      act(() => root.render(<ConnectorBody item={item} />));
      await flush();
      const el = host.querySelector("[data-mid]")!.closest<HTMLElement>(".overflow-y-auto")!;
      const m = { scrollHeight: 2000, clientHeight: 600, scrollTop: 1400 };
      Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => m.scrollHeight });
      Object.defineProperty(el, "clientHeight", { configurable: true, get: () => m.clientHeight });
      Object.defineProperty(el, "scrollTop", { configurable: true, get: () => m.scrollTop, set: (v: number) => (m.scrollTop = Math.max(0, Math.min(v, m.scrollHeight - m.clientHeight))) });
      const scroll = (top: number) => act(() => {
        m.scrollTop = top;
        el.dispatchEvent(new Event("scroll"));
      });
      scroll(1400);
      return { el, m, scroll };
    };
    const chat = { id: "g", name: "示例群", kind: "group" } as const;

    it("at the newest message keeps it in view; scrolled up stays", async () => {
      const { el, m, scroll } = await open({ chat, messages: [msg("1"), msg("2")] });
      expect(watchers.some((w) => w.targets.has(el) && w.targets.has(el.firstElementChild!))).toBe(true);
      m.clientHeight = 300; // the keyboard up
      resize(el);
      expect(m.scrollTop).toBe(1700);
      scroll(900);
      m.clientHeight = 220;
      resize(el);
      expect(m.scrollTop).toBe(900);
    });

    it("leaves a quote's jump where it goes", async () => {
      const { el, m } = await open({ chat, messages: [msg("5"), msg("6", { reply_to: { id: "5", text: "消息 5" } })] });
      act(() => buttons().find((b) => b.getAttribute("aria-label")?.includes("消息 5"))!.click());
      await flush();
      m.clientHeight = 300; // while the quote is lit, its glide is not pulled back
      resize(el);
      expect(m.scrollTop).toBe(1400);
    });

    it("leaves the list where it is when older messages are asked for", async () => {
      const { el, m } = await open({ chat, messages: [msg("5"), msg("6")], before: "5" });
      pages.older = { chat, messages: [msg("2")] } satisfies ThreadPage;
      act(() => buttons().find((b) => b.textContent?.trim() === "更早的消息")!.click());
      await flush();
      expect(host.querySelector('[data-mid="2"]')).not.toBeNull();
      m.scrollHeight = 2300; // the older page drawn above
      resize(el);
      expect(m.scrollTop).toBe(1400);
    });
  });

  it("keeps a sticker as it is, as iOS does (G039)", async () => {
    pages.newest = {
      chat: { id: "g", name: "示例群", kind: "group" },
      messages: [msg("1", { text: "", media: [{ kind: "sticker", ref: "stk-1", emoji: "👍" }] })],
    } satisfies ThreadPage;
    act(() => root.render(<ConnectorBody item={item} />));
    await flush();
    expect(host.querySelector("img[src*='stk-1']")).not.toBeNull();
    expect(host.querySelector("button[data-thread-picture]")).toBeNull();
  });

  it("rejects javascript/data/file/userinfo URLs in openSystem while allowing credential-free http(s) links", () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
    const prevLoc = window.location;
    const hrefSet = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        get href() { return "http://localhost/"; },
        set href(v: string) { hrefSet(v); },
      },
    });
    try {
      for (const bad of [
        "javascript:alert(1)",
        "  JaVaScRiPt:alert(1)",
        "java\tscript:alert(1)",
        "data:text/html,<script>alert(1)</script>",
        "vbscript:msgbox(1)",
        "file:///etc/passwd",
        "vscode://file/etc/passwd",
        "//evil.example/x",
        "https://github.example@evil.example/pr/1",
        "http://me:pw@example.com/pr/1",
        "https:/admin",
        "https://@evil.example/pr/1",
        "https://good.example\\@evil.example/pr/1",
        "https://example.com/\x00pr",
        "tg://me:pw@evil.example/join",
        "tg://",
        "tg://@evil.example/join",
        "sms:",
        "mailto:",
        "tel:",
        "sms://me:pw@evil.example",
      ]) {
        openSystem(bad);
      }
      expect(openSpy).not.toHaveBeenCalled();
      expect(hrefSet).not.toHaveBeenCalled();

      openSystem("https://github.com/nwverse/persona/pull/1");
      expect(openSpy).toHaveBeenCalledWith("https://github.com/nwverse/persona/pull/1", "_blank", "noopener,noreferrer");
      openSystem("sms:+15551234");
      openSystem("sms:me@example.com&body=hi");
      openSystem("mailto:me@example.com");
      openSystem("tel:+15550100");
      openSystem("tg://resolve?domain=demo");
      expect(hrefSet.mock.calls).toEqual([
        ["sms:+15551234"],
        ["sms:me@example.com&body=hi"],
        ["mailto:me@example.com"],
        ["tel:+15550100"],
        ["tg://resolve?domain=demo"],
      ]);
    } finally {
      Object.defineProperty(window, "location", { configurable: true, value: prevLoc });
      openSpy.mockRestore();
    }
  });
});
