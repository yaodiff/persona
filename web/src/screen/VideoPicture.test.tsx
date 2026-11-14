// @vitest-environment jsdom
// What the page says about the video channel (#6558): the fallback line (with "How to turn it on" for a Mac that hasn't
// allowed video, one sentence that depends on whether macOS has asked yet), and the stats panel in video mode (the
// iPhone's lines) or, when the Mac offers video but this page can't play it, why; and the mobile-data card.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

vi.mock("./probes", () => ({ countFrames: () => null, countBytes: () => () => {}, probeHub: () => () => {} }));
import { FallbackLine, MobileDataCard } from "./VideoPicture";
import { StatsOverlay, tierParts, tierReasonParts } from "./StatsOverlay";
import type { Change } from "./tiers/policy";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
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
  document.body.innerHTML = "";
});

describe("the fallback line", () => {
  it("says why in plain words", () => {
    act(() => root.render(<FallbackLine fallback="locked" asked={false} />));
    expect(host.querySelector('[data-testid="screen-fallback"]')?.textContent).toBe(t("screen-video.fallback_locked"));
    act(() => root.render(<FallbackLine fallback="lost" asked={false} />));
    expect(host.querySelector('[data-testid="screen-fallback"]')?.textContent).toBe(t("screen-video.fallback_lost"));
  });

  it("offers How to turn it on for a Mac that hasn't allowed video: a window before macOS asked, System Settings after", () => {
    for (const [asked, words] of [[false, "screen-video.how_window"], [true, "screen-video.how_settings"]] as const) {
      act(() => root.render(<FallbackLine key={String(asked)} fallback="notAllowed" asked={asked} />));
      const how = host.querySelector<HTMLButtonElement>('[data-testid="screen-fallback-help"]')!;
      expect(how.textContent).toBe(`${t("screen-video.fallback_not_allowed")} ${t("screen-video.how_to_turn_on")}`);
      act(() => how.click());
      const dialog = document.querySelector('[role="dialog"]')!;
      expect(dialog.textContent).toContain(t("screen-video.turn_on_title"));
      expect(dialog.textContent).toContain(t(words));
    }
  });
});

describe("the stats panel", () => {
  const props = { rfb: {}, socket: new EventTarget() as WebSocket, onClose: () => {} };

  it("shows the video's lines while it is the picture: total first, rates with codec and size, then the segments", () => {
    const reading = { fps: 30, mbps: 2.4, captureMs: 0, encodeMs: 5, networkMs: 7.5, decodeMs: 2, totalMs: 14.5 };
    act(() => root.render(<StatsOverlay {...props} video={{ reading, codec: "HEVC", size: { width: 1920, height: 1200 } }} />));
    const panel = host.querySelector('[data-testid="screen-stats"]')!;
    expect(panel.firstElementChild!.textContent).toContain(t("screen-video.stats_video"));
    expect(host.querySelector('[data-testid="screen-stats-total"]')?.textContent).toBe(t("screen-video.stats_ms", { ms: 15 }));
    expect(panel.textContent).toContain("HEVC");
    expect(panel.textContent).toContain("1920×1200");
    expect(host.querySelector('[data-testid="screen-stats-segments"]')?.textContent).toBe(
      [t("screen-video.stats_capture", { ms: 0 }), t("screen-video.stats_encode", { ms: 5 }), t("screen-video.stats_network", { ms: 8 }), t("screen-video.stats_decode", { ms: 2 })].join("·"),
    );
  });

  it("leaves out what wasn't measured: no total without every segment", () => {
    act(() => root.render(<StatsOverlay {...props} video={{ reading: { fps: 30, decodeMs: 2 }, codec: "H.264", size: null }} />));
    expect(host.querySelector('[data-testid="screen-stats-total"]')).toBeNull();
    expect(host.querySelector('[data-testid="screen-stats-segments"]')?.textContent).toBe(t("screen-video.stats_decode", { ms: 2 }));
  });

  it("shows the tier as the iPhone does: Quality, scale and frame rate, then why it last changed and how long ago", () => {
    const change: Change = { tier: { scale: 1.5, fps: 60, width: 2268, height: 1472, minKbps: 4_006, maxKbps: 12_000 }, index: 1, reason: "link_steady", targetKbps: 5_000 };
    vi.spyOn(performance, "now").mockReturnValue(73_000);
    act(() => root.render(<StatsOverlay {...props} video={{ reading: {}, codec: "HEVC", size: null, tier: { change, at: 61_000, quality: "auto" } }} />));
    const value = [t("screen-video.quality_auto"), "1.5×", t("screen-video.stats_fps", { fps: 60 })].join("·");
    expect(host.querySelector('[data-testid="screen-stats-tier"]')?.textContent).toBe(t("screen-video.stats_tier") + value);
    expect(host.querySelector('[data-testid="screen-stats-tier-reason"]')?.textContent).toBe(`${t("screen-video.tier_link_steady")}·${t("screen-video.tier_seconds_ago", { n: 12 })}`);
    expect(tierParts("sharper", { ...change, tier: { ...change.tier, scale: 2, fps: 30 } })).toEqual([t("screen-video.quality_sharper"), "2×", t("screen-video.stats_fps", { fps: 30 })]);
    expect(tierReasonParts("choice", 0, 125_000)).toEqual([t("screen-video.tier_choice"), t("screen-video.tier_minutes_ago", { n: 2 })]);
    act(() => root.render(<StatsOverlay {...props} video={{ reading: {}, codec: "HEVC", size: null }} />));
    expect(host.querySelector('[data-testid="screen-stats-tier"]'), "no tier line without tiers").toBeNull();
  });

  it("says why the Mac's video isn't playing here, over VNC only", () => {
    for (const [reason, words] of [["insecure", "screen-video.stats_no_video_insecure"], ["unsupported", "screen-video.stats_no_video_unsupported"], ["failed", "screen-video.stats_no_video_failed"]] as const) {
      act(() => root.render(<StatsOverlay {...props} noVideo={reason} />));
      expect(host.querySelector('[data-testid="screen-stats-no-video"]')?.textContent).toBe(t(words));
    }
    act(() => root.render(<StatsOverlay {...props} noVideo="insecure" video={{ reading: {}, codec: "HEVC", size: null }} />));
    expect(host.querySelector('[data-testid="screen-stats-no-video"]')).toBeNull();
  });
});

describe("the mobile-data card", () => {
  it("asks in the iPhone's words, each choice with its data an hour on one line, and closes", () => {
    const chose: string[] = [];
    let closed = 0;
    act(() => root.render(<MobileDataCard onChoose={(m) => chose.push(m)} onClose={() => closed++} />));
    const card = host.querySelector('[data-testid="screen-mobile-data"]')!;
    expect(card.getAttribute("aria-label")).toBe(t("screen-video.mobile_data_title"));
    expect(card.textContent).toContain(t("screen-video.mobile_data_line"));
    const clearer = host.querySelector<HTMLButtonElement>('[data-testid="screen-mobile-data-clearer"]')!;
    const saver = host.querySelector<HTMLButtonElement>('[data-testid="screen-mobile-data-saver"]')!;
    expect(clearer.textContent).toBe(t("screen-video.mobile_data_clearer") + t("screen-video.mobile_data_clearer_usage"));
    expect(saver.textContent).toBe(t("screen-video.mobile_data_saver") + t("screen-video.mobile_data_saver_usage"));
    expect(clearer.className).toContain("whitespace-nowrap");
    act(() => clearer.click());
    act(() => saver.click());
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="screen-mobile-data-close"]')!.click());
    expect([chose, closed]).toEqual([["clearer", "saver"], 1]);
  });
});
