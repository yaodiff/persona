// The hub line's buttons (#2897): each kind shows only its own, all on one line.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

import HubNoticeView from "./HubNoticeView";
import type { HubNotice } from "../../lib/hub-update";

const noop = () => {};
const draw = (notice: HubNotice) => renderToStaticMarkup(<HubNoticeView notice={notice} onStatus={noop} onDismiss={noop} />);
const ids = (html: string) => [...html.matchAll(/data-testid="(hub-notice-[a-z-]+)"/g)].map((m) => m[1]);

describe("the hub line", () => {
  it("has 关闭 and 知道了 for auto-update, nothing to close", () => {
    expect(ids(draw({ kind: "auto_on" }))).toEqual(["hub-notice-auto-off", "hub-notice-seen"]);
  });
  it("has 更新 and × for a newer version", () => {
    const html = draw({ kind: "available", version: "v2", build: 2 });
    expect(ids(html)).toEqual(["hub-notice-update", "hub-notice-dismiss"]);
    expect(html).toContain("hub 有新版本 v2");
  });
  it("only closes a rollback or a new image", () => {
    expect(ids(draw({ kind: "rolled_back", why: "x", build: 2 }))).toEqual(["hub-notice-dismiss"]);
    expect(ids(draw({ kind: "new_image", version: "0.3.1" }))).toEqual(["hub-notice-dismiss"]);
  });
  it("has no buttons while it updates", () => {
    expect(ids(draw({ kind: "updating", what: "下载中" }))).toEqual([]);
  });
  it("keeps every button's label on one line", () => {
    const html = draw({ kind: "auto_on" });
    for (const b of html.match(/<button [^>]*data-testid="hub-notice-(auto-off|seen)"[^>]*>/g)!) expect(b).toContain("whitespace-nowrap");
  });
});
