import { describe, expect, it } from "vitest";
import { logText, mergeLines, metricsCharts, msText, rateText, rateTone, stepLabel } from "./logic";
import { mockLogs, mockMetrics, mockOverview } from "./mock-metrics";
import type { LogLine } from "./types";

describe("运行情况 (docs/extensions/extensions.md)", () => {
  it("writes rates and latencies as people read them", () => {
    expect(rateText(0, 0)).toBe("—");
    expect(rateText(0, 12)).toBe("0%");
    expect(rateText(0.0004, 3000)).toBe("<0.1%");
    expect(rateText(0.0125, 80)).toBe("1.3%");
    expect(rateText(0.5, 2)).toBe("50%");
    expect(msText(null)).toBe("—");
    expect(msText(0.84)).toBe("0.8 ms");
    expect(msText(140.4)).toBe("140 ms");
    expect(msText(1240)).toBe("1.2 s");
    expect(msText(60000)).toBe("60 s");
  });

  it("moves a latency that would read 1000 ms to seconds (#8513)", () => {
    expect(msText(999.4)).toBe("999 ms");
    expect(msText(999.6)).toBe("1 s");
    expect(msText(1000)).toBe("1 s");
    expect(msText(9999.96)).toBe("10 s");
  });

  it("colors a rate only with calls enough to mean it", () => {
    expect(rateTone(0.5, 2)).toBe("gray");
    expect(rateTone(0.005, 100)).toBe("gray");
    expect(rateTone(0.02, 100)).toBe("orange");
    expect(rateTone(0.2, 100)).toBe("red");
  });

  it("charts the calls stacked and the p95 as a line, labelled by the window's step", () => {
    const m = mockMetrics("example-notes", "1h", Date.parse("2026-11-05T10:02:00Z"));
    expect(m.series).toHaveLength(12);
    const { calls, latency } = metricsCharts(m);
    expect(calls.stacked).toBe(true);
    expect(calls.series?.map((s) => s.name)).toEqual(["成功", "失败"]);
    const total = calls.series!.flatMap((s) => s.points).reduce((a, p) => a + p.value, 0);
    expect(total).toBe(m.total.calls);
    expect(latency?.kind).toBe("line");
    expect(metricsCharts({ window: "1h", series: [{ at: m.from, calls: 0, errors: 0, p95_ms: null }] }).latency).toBeNull();
    expect(stepLabel("2026-11-05T10:00:00", "24h")).toBe("10:00");
    expect(stepLabel("2026-11-05T06:00:00", "7d")).toBe("11-05 06:00");
  });

  it("keeps the latest log lines once, and copies them as text", () => {
    const line = (seq: number, source = "program"): LogLine => ({ seq, at: "2026-11-05T10:00:00Z", source, text: `l${seq}` });
    const have = [line(1), line(2)];
    expect(mergeLines(have, [line(2), line(3)]).map((l) => l.seq)).toEqual([1, 2, 3]);
    expect(mergeLines(have, [line(3), line(4)], 3).map((l) => l.seq)).toEqual([2, 3, 4]);
    expect(logText([line(1), line(2, "hub")])).toBe("2026-11-05T10:00:00Z l1\n2026-11-05T10:00:00Z [hub] l2");
  });

  it("mocks the same counts on every load, and filters the log", () => {
    expect(mockMetrics("voice", "24h", 1e12)).toEqual(mockMetrics("voice", "24h", 1e12));
    expect(mockOverview(["voice", "telegram"], "24h").extensions.map((x) => x.id)).toEqual(["voice", "telegram"]);
    const all = mockLogs("example-notes", "");
    expect(all.lines.length).toBeGreaterThan(0);
    expect(mockLogs("example-notes", "RATE LIMITED").lines.every((l) => l.text.includes("rate limited"))).toBe(true);
    expect(mockLogs("example-notes", "", all.next - 2).lines).toHaveLength(1);
  });
});
