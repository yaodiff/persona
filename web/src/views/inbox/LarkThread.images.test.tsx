// @vitest-environment jsdom
// Group-chat pictures must reserve a box from their reported size and never rely on native lazy loading:
// a zero-size lazy img is skipped by some browsers, so the picture stayed permanently blank.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Listener = (msg: Record<string, unknown>) => void;
const { realListeners } = vi.hoisted(() => ({ realListeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>() }));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    onHubEvent: (type: string, l: Listener) => {
      const set = realListeners.get(type) ?? new Set<Listener>();
      set.add(l);
      realListeners.set(type, set);
      return () => void set.delete(l);
    },
  };
});

import { LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let page: unknown;

class FakeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

beforeEach(() => {
  page = { messages: [] };
  vi.stubGlobal("IntersectionObserver", FakeObserver);
  Element.prototype.scrollIntoView = vi.fn();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const body = url.includes("/conversations/f1/messages?") ? page : { error: "not found" };
    const status = url.includes("/conversations/f1/messages?") ? 200 : 404;
    return { ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};

const open = async () => {
  await act(async () => root.render(<LarkThread chat={{ feed: "f1", title: "Example group", group: true }} />));
  await flush();
};

describe("chat pictures load eagerly with a reserved box", () => {
  it("renders image and sticker imgs without lazy loading", async () => {
    page = {
      messages: [
        {
          id: "om_img",
          position: "1",
          sender_name: "Example A",
          sender_id: "u1",
          parts: [
            { kind: "image", url: "/api/v1/lark/media/img_wide", metadata: { width: 1505, height: 398 } },
            { kind: "image", url: "/api/v1/lark/media/img_nometa" },
            { kind: "emotion", text: "[smile]", url: "/api/v1/lark/media/sticker_1", metadata: { semanticType: "sticker" } },
          ],
        },
      ],
    };
    await open();

    const row = host.querySelector('[data-mid="om_img"]')!;
    expect(row).toBeTruthy();
    const imgs = [...row.querySelectorAll("img")];
    expect(imgs).toHaveLength(3);
    for (const img of imgs) {
      expect(img.getAttribute("loading")).not.toBe("lazy");
      expect(img.getAttribute("decoding")).toBe("async");
    }
  });

  it("reserves the scaled box for a picture with reported dimensions", async () => {
    page = {
      messages: [
        {
          id: "om_sized",
          position: "1",
          sender_name: "Example A",
          sender_id: "u1",
          parts: [
            { kind: "image", url: "/api/v1/lark/media/img_wide", metadata: { width: 1505, height: 398 } },
            { kind: "image", url: "/api/v1/lark/media/img_nometa" },
          ],
        },
      ],
    };
    await open();

    const sized = host.querySelector<HTMLImageElement>('img[src*="img_wide"]')!;
    // 1505x398 scaled by width into the 280px box: 280x74
    expect(sized.style.width).toBe("280px");
    expect(sized.style.height).toBe("74px");

    const unsized = host.querySelector<HTMLImageElement>('img[src*="img_nometa"]')!;
    expect(unsized.style.width).toBe("");
    expect(unsized.style.height).toBe("");
  });

  it("shows the quoted picture as a thumbnail in the reply strip", async () => {
    page = {
      messages: [
        {
          id: "om_quote",
          position: "2",
          sender_name: "Example A",
          sender_id: "u1",
          text: "my reply",
          reply_to: {
            id: "om_old",
            sender_name: "Example B",
            text: "[Image]",
            parts: [{ kind: "image", url: "/api/v1/lark/media/quote_pic", metadata: { width: 1620, height: 860 } }],
          },
        },
      ],
    };
    await open();

    const row = host.querySelector('[data-mid="om_quote"]')!;
    const thumb = row.querySelector<HTMLImageElement>('img[src*="quote_pic"]')!;
    expect(thumb).toBeTruthy();
    expect(thumb.getAttribute("loading")).not.toBe("lazy");
    expect(thumb.className).toContain("size-5");
    // the reply strip still names the quoted sender
    expect(row.textContent).toContain("Example B");
  });

  it("blocks mailto:, cross-origin blob:, and traversal URLs in images/quotes and rejects malformed mailto: links", async () => {
    const sameOriginBlob = `blob:${location.origin}/local-preview`;
    page = {
      messages: [
        {
          id: "om_sec",
          position: "3",
          sender_name: "Example A",
          sender_id: "u1",
          reply_to: {
            id: "om_q",
            sender_name: "Example B",
            text: "[Image]",
            parts: [{ kind: "image", url: "mailto:team@example.com" }],
          },
          parts: [
            { kind: "image", url: "mailto:team@example.com" },
            { kind: "image", url: "blob:https://evil.example/x" },
            { kind: "image", url: "/api/v1/lark/media/../../admin" },
            { kind: "image", url: sameOriginBlob },
            { kind: "link", text: "bad-slash", url: "mailto://evil.example" },
            { kind: "link", text: "bad-backslash", url: "mailto:\\evil.example" },
            { kind: "link", text: "good-mail", url: "mailto:team@example.com" },
          ],
        },
      ],
    };
    await open();

    const row = host.querySelector('[data-mid="om_sec"]')!;
    const imgs = [...row.querySelectorAll("img")];
    expect(imgs.map((img) => img.getAttribute("src"))).toEqual([sameOriginBlob]);
    const anchors = [...row.querySelectorAll("a")];
    expect(anchors.map((a) => a.getAttribute("href"))).toEqual(["mailto:team@example.com"]);
    expect(row.textContent).toContain("bad-slash");
    expect(row.textContent).toContain("bad-backslash");
  });
});
