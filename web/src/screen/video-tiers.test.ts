// One stream's quality tiers in the browser (#6558), as the iPhone's ScreenVideoTiers: nothing before a helper with
// tiers says hello; then the policy's start tier, Quality and the data cap as tier changes, and each change as the
// `update` that sets it on the helper.
import { describe, expect, it } from "vitest";
import { TierPolicy } from "./tiers/policy";
import { ScreenVideoTiers, updateOf, type TiersContext } from "./video-tiers";
import type { Hello } from "./video-wire";

const display = { points: [1512, 982], pixels: [3024, 1964] };
const hello = (more: Partial<Hello> = {}): Hello => ({ type: "hello", version: 1, codec: "hevc", display: 1, width: 1920, height: 1248, features: ["tiers"], ...display, ...more });
const context = (more: Partial<TiersContext> = {}): TiersContext => ({ bias: "auto", budget: { maxKbps: 12_000, fps: 60 }, maxSize: 2880, link: null, remembered: null, ...more });

describe("ScreenVideoTiers", () => {
  it("changes nothing until a helper with tiers says hello with its display", () => {
    const t = new ScreenVideoTiers(context());
    expect([t.tick(1_000_000), t.setBias("sharper", 1_000_000), t.active]).toEqual([null, null, false]);
    expect(t.hello(hello({ features: undefined }), 1)).toBeNull();
    expect(t.hello(hello({ points: undefined }), 1)).toBeNull();
    expect(t.active).toBe(false);
  });

  it("starts where the policy starts for the same display, caps and route, and remembers this computer's tier", () => {
    const start = new ScreenVideoTiers(context()).hello(hello(), 10)!;
    const policy = new TierPolicy({ display: { points: [1512, 982], pixels: [3024, 1964], maxSize: 2880 }, bias: "auto", capKbps: 12_000, maxFps: 60, link: null, now: 10 });
    expect(start).toEqual(policy.last);
    expect(start.reason).toBe("start");
    const again = new ScreenVideoTiers(context({ remembered: 3 })).hello(hello(), 10)!;
    expect(again.index).toBe(3);
  });

  it("Data Saver's one pixel per point and cap shape the ladder; Clearer and Quality move the tier", () => {
    const t = new ScreenVideoTiers(context({ budget: { maxKbps: 1_500, fps: 60, maxScale: 1 } }));
    const saver = t.hello(hello(), 0)!;
    expect(saver.tier.scale).toBe(1);
    expect(saver.tier.maxKbps).toBeLessThanOrEqual(1_500);
    const clearer = t.setBudget({ maxKbps: 4_000, fps: 60 }, 1_000_000)!;
    expect([clearer.reason, clearer.tier.maxKbps > 1_500, clearer.tier.maxKbps <= 4_000]).toEqual(["network_changed", true, true]);
    expect(t.setBudget({ maxKbps: 4_000, fps: 60 }, 2_000_000), "the same cap: nothing").toBeNull();
    expect(t.setBias("smoother", 3_000_000)?.reason).toBe("choice");
    expect(t.setBias("smoother", 4_000_000)).toBeNull();
  });

  it("sends a tier as the update that sets it", () => {
    const c = new ScreenVideoTiers(context()).hello(hello(), 0)!;
    expect(updateOf(c)).toEqual({ fps: c.tier.fps, max_kbps: c.tier.maxKbps, scale: c.tier.scale, min_kbps: c.tier.minKbps, target_kbps: c.targetKbps });
  });

  it("steps down when frames queue up and the helper's rate sits at the band's floor", () => {
    const t = new ScreenVideoTiers(context());
    const start = t.hello(hello(), 0)!;
    // 30 fps whose one-way delay grows 10 ms a frame: the queue passes 150 ms; the helper is at the band's floor
    let at = 1_000_000;
    let change = null;
    for (let i = 0; i < 120 && !change; i++) {
      at += 33_333;
      t.frame(at - 20_000 - i * 10_000, at, null, 6_000, false);
      t.helper({ type: "timing", kbps: start.tier.minKbps, fps: 30, target_kbps: start.tier.minKbps });
      if (i % 8 === 7) change = t.tick(at);
    }
    expect(change?.reason).toBe("link_slowed");
    expect(change!.index).toBeGreaterThan(start.index);
  });
});
