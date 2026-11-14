// The live screen's connection-stats meter (#6558): one reading a second from what the page measured, and nothing for
// what it did not. The iOS overlay reads the same fields (ScreenStats).
import { describe, expect, it } from "vitest";
import { mbpsText, median, ROUND_TRIP_FRESH_MS, ROUND_TRIPS_KEPT, ScreenMeter } from "./meter";

describe("the screen meter", () => {
  it("reads bitrate and frame rate over the time since the last reading, VNC through the hub", () => {
    const m = new ScreenMeter(1000, true);
    m.received(250_000);
    m.received(250_000);
    for (let i = 0; i < 30; i++) m.drew();
    expect(m.read(2000)).toEqual({ mode: "vnc", path: "relay", bitrateMbps: 4, fps: 30 });
    // the next window starts where that one ended
    m.received(125_000);
    for (let i = 0; i < 15; i++) m.drew();
    expect(m.read(4000)).toEqual({ mode: "vnc", path: "relay", bitrateMbps: 0.5, fps: 7.5 });
  });

  it("says 0 for a screen that did not change: that was measured", () => {
    const m = new ScreenMeter(0, true);
    expect(m.read(1000)).toEqual({ mode: "vnc", path: "relay", bitrateMbps: 0, fps: 0 });
  });

  it("leaves the frame rate out when frames are not counted (a noVNC it does not know)", () => {
    const m = new ScreenMeter(0, false);
    m.received(1000);
    m.drew();
    const r = m.read(1000);
    expect(r.fps).toBeUndefined();
    expect(r.bitrateMbps).toBeCloseTo(0.008);
  });

  it("reads no rates for a window of no time", () => {
    const m = new ScreenMeter(500, true);
    expect(m.read(500)).toEqual({ mode: "vnc", path: "relay" });
  });

  it("shows the median of the recent round trips to the hub, so one slow answer does not make it jump", () => {
    const m = new ScreenMeter(0, true);
    expect(m.read(0).hubRoundTripMs).toBeUndefined(); // none yet: no number
    m.roundTrip(20, 100);
    expect(m.read(200).hubRoundTripMs).toBe(20);
    m.roundTrip(400, 300); // one slow answer
    m.roundTrip(22, 500);
    expect(m.read(600).hubRoundTripMs).toBe(22);
    for (let i = 0; i < ROUND_TRIPS_KEPT; i++) m.roundTrip(30, 700 + i);
    expect(m.read(800).hubRoundTripMs).toBe(30); // only the last ROUND_TRIPS_KEPT count
  });

  it("drops round trips that are stale (the page was in the background) and ones that are not a time", () => {
    const m = new ScreenMeter(0, true);
    m.roundTrip(Number.NaN, 0);
    m.roundTrip(-3, 0);
    m.roundTrip(Number.POSITIVE_INFINITY, 0);
    expect(m.read(10).hubRoundTripMs).toBeUndefined();
    m.roundTrip(25, 1000);
    expect(m.read(1000 + ROUND_TRIP_FRESH_MS).hubRoundTripMs).toBe(25);
    expect(m.read(1001 + ROUND_TRIP_FRESH_MS).hubRoundTripMs).toBeUndefined();
  });

  it("ignores byte counts that are not positive", () => {
    const m = new ScreenMeter(0, true);
    m.received(-10);
    m.received(0);
    expect(m.read(1000).bitrateMbps).toBe(0);
  });
});

describe("mbpsText", () => {
  it("writes one decimal, two below 1 Mbps so a measured trickle does not read as 0.0", () => {
    expect(mbpsText(12.34)).toBe("12.3");
    expect(mbpsText(1)).toBe("1.0");
    expect(mbpsText(0.034)).toBe("0.03");
    expect(mbpsText(0)).toBe("0.00");
  });
});

describe("median", () => {
  it("is the middle one, or the mean of the two middle ones", () => {
    expect(median([5])).toBe(5);
    expect(median([9, 1, 5])).toBe(5);
    expect(median([1, 9, 3, 5])).toBe(4);
  });
});
