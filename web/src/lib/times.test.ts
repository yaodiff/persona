import { describe, expect, it } from "vitest";
import { byTime, timeMs, timeNs, timeOrNaN } from "./times";

describe("times compared as times (#3638)", () => {
  it("reads what the hub writes: a fraction of any length, Z or an offset", () => {
    expect(timeMs("2026-11-06T10:00:05.12Z")).toBe(Date.UTC(2026, 10, 6, 10, 0, 5, 120));
    expect(timeMs("2026-11-06T18:00:00+08:00")).toBe(Date.UTC(2026, 10, 6, 10, 0, 0));
    expect(timeMs("")).toBe(0);
    expect(timeMs(undefined)).toBe(0);
    expect(timeMs("not a time")).toBe(0);
    expect(timeOrNaN("2026-11-06T10:00:05.12Z")).toBe(Date.UTC(2026, 10, 6, 10, 0, 5, 120));
    expect(timeOrNaN("")).toBeNaN();
    expect(timeOrNaN(undefined)).toBeNaN();
    expect(timeOrNaN("not a time")).toBeNaN();
  });

  it("orders by the instant, where comparing the strings gets it wrong", () => {
    const latestFirst = (list: string[]) => [...list].sort((a, b) => byTime(b, a));
    expect(latestFirst(["2026-11-06T10:00:05.12Z", "2026-11-06T10:00:05.123Z"])).toEqual(["2026-11-06T10:00:05.123Z", "2026-11-06T10:00:05.12Z"]);
    expect(latestFirst(["2026-11-06T10:00:05Z", "2026-11-06T10:00:05.5Z"])).toEqual(["2026-11-06T10:00:05.5Z", "2026-11-06T10:00:05Z"]);
    expect(latestFirst(["2026-11-06T18:00:00+08:00", "2026-11-06T10:30:00Z"])).toEqual(["2026-11-06T10:30:00Z", "2026-11-06T18:00:00+08:00"]);
    expect(latestFirst(["", "2026-11-06T10:00:00Z"])).toEqual(["2026-11-06T10:00:00Z", ""]);
    expect(byTime("2026-11-06T10:00:00Z", "2026-11-06T18:00:00+08:00")).toBe(0);
  });

  it("keeps occurrence identity at nanosecond precision, including offsets", () => {
    const first = timeNs("2026-11-06T10:00:05.000000001Z");
    expect(first).not.toBeNull();
    expect(timeNs("2026-11-06T10:00:05.000000002Z")).toBe(first! + 1n);
    expect(timeNs("2026-11-06T18:00:05+08:00")).toBe(first! - 1n);
    expect(timeNs("2026-11-06T18:00:05.000000001+08:00")).toBe(first);
    expect(timeNs("1969-12-31T23:59:59.123456789Z")).toBe(-876_543_211n);
    expect(timeNs("1970-01-01T00:00:00.000000001Z")).toBe(1n);
    expect(timeNs("not a time")).toBeNull();
    expect(timeNs(undefined)).toBeNull();
  });
});
