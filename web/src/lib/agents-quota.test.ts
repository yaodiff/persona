// A personal Agent's allowance as a charge (quotaGauge): drawn whenever there is a figure, whatever the source's own
// `show` says; the tone follows what is left.
import { describe, expect, it } from "vitest";
import { quotaGauge } from "./agents";

describe("quotaGauge", () => {
  it("reads percent first, then remaining as a fraction", () => {
    expect(quotaGauge({ state: "ok", percent: 70 })).toEqual({ level: 0.7, percent: 70, tone: "ok" });
    expect(quotaGauge({ state: "ok", remaining: 0.42 })).toMatchObject({ percent: 42, tone: "ok" });
    expect(quotaGauge({ state: "ok", remaining: 0.9, percent: 30 })).toMatchObject({ percent: 30 });
  });

  it("is drawn even when the source says show: false (older accounts' default)", () => {
    expect(quotaGauge({ state: "ok", percent: 70, show: false })).toMatchObject({ percent: 70 });
  });

  it("is low at 20% or less, exhausted at nothing left, muted when stale", () => {
    expect(quotaGauge({ state: "ok", percent: 20 })?.tone).toBe("low");
    expect(quotaGauge({ state: "ok", percent: 15 })?.tone).toBe("low");
    expect(quotaGauge({ state: "ok", percent: 0 })?.tone).toBe("exhausted");
    expect(quotaGauge({ state: "exhausted" })).toEqual({ level: 0, percent: 0, tone: "exhausted" });
    expect(quotaGauge({ state: "exhausted", percent: 40 })).toMatchObject({ level: 0, tone: "exhausted" });
    expect(quotaGauge({ state: "ok", percent: 60, stale: true })?.tone).toBe("stale");
  });

  it("draws nothing without a figure", () => {
    expect(quotaGauge(undefined)).toBeNull();
    expect(quotaGauge({ state: "unknown" })).toBeNull();
    expect(quotaGauge({ state: "ok", percent: null, remaining: null })).toBeNull();
  });

  it("keeps the figure in range and reads when it resets", () => {
    expect(quotaGauge({ state: "ok", percent: 140 })).toMatchObject({ level: 1, percent: 100 });
    expect(quotaGauge({ state: "ok", remaining: -1 })).toMatchObject({ level: 0, tone: "exhausted" });
    expect(quotaGauge({ state: "ok", percent: 50, resetsAt: 1_800_000_000_000 })?.resetsAt).toBe(1_800_000_000_000);
    expect(quotaGauge({ state: "ok", percent: 50, resetsAt: "2027-01-01T00:00:00Z" })?.resetsAt).toBe(Date.parse("2027-01-01T00:00:00Z"));
    expect(quotaGauge({ state: "ok", percent: 50, resetsAt: "soon" })?.resetsAt).toBeUndefined();
  });
});
