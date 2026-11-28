// @vitest-environment jsdom
// Reaction pills must not flash a raw "[KEY]" while the emoji catalogue is still loading: a neutral placeholder keeps
// the pill's shape, and only an emoji the loaded catalogue genuinely lacks falls back to text.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CardButtons, Reactions } from "./LarkExtras";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let resolveCatalog: ((body: unknown) => void) | null;

beforeEach(() => {
  resolveCatalog = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise((resolve) => {
          resolveCatalog = (body) => resolve(new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } }));
        }),
    ),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

describe("reaction pills while the emoji catalogue loads", () => {
  it("shows a placeholder first, then the picture; an unknown emoji keeps the text fallback", async () => {
    await act(async () =>
      root.render(
        <Reactions
          reactions={[
            { key: "FACEPALM", count: 1 },
            { key: "MYSTERY", count: 2 },
          ]}
        />,
      ),
    );

    // first paint: catalogue still null — no bracket text, two sized placeholders
    expect(host.textContent).not.toContain("[FACEPALM]");
    expect(host.querySelectorAll("span[aria-hidden]")).toHaveLength(2);

    resolveCatalog?.({
      emojis: { facepalm: { label: "Facepalm", url: "/api/v1/lark/media/emoji_facepalm.png" } },
      groups: [],
    });
    await settle();

    const facepalm = host.querySelector<HTMLImageElement>('img[src*="emoji_facepalm"]')!;
    expect(facepalm).toBeTruthy();
    expect(facepalm.width).toBe(16);
    // the loaded catalogue has no MYSTERY emoji: the text fallback stays
    expect(host.textContent).toContain("[MYSTERY]");
    expect(host.textContent).not.toContain("[FACEPALM]");
  });

  it("blocks unsafe schemes and credentialed URLs on CardButtons while opening safe https links", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
    try {
      await act(async () =>
        root.render(
          <CardButtons
            buttons={[
              { label: "BadJS", kind: "default", disabled: false, url: "javascript:alert(1)" },
              { label: "BadAuth", kind: "default", disabled: false, url: "https://feishu.example@evil.example/doc" },
              { label: "GoodLink", kind: "default", disabled: false, url: "https://open.feishu.cn/app" },
            ]}
          />,
        ),
      );
      const btns = [...host.querySelectorAll<HTMLButtonElement>("button")];
      expect(btns.map((b) => [b.textContent, b.disabled])).toEqual([
        ["BadJS", true],
        ["BadAuth", true],
        ["GoodLink", false],
      ]);
      for (const b of btns) await act(async () => { b.click(); });
      expect(openSpy).toHaveBeenCalledTimes(1);
      expect(openSpy).toHaveBeenCalledWith("https://open.feishu.cn/app", "_blank", "noopener,noreferrer");
    } finally {
      openSpy.mockRestore();
    }
  });
});
