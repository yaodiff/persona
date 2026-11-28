import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  coresNow,
  loadAxis,
  agentMeta,
  agentsBySpace,
  bytesAxis,
  bytesBin,
  connectionText,
  countsText,
  hm,
  isEmpty,
  level,
  memPercent,
  nearest,
  niceStep,
  percent,
  RANGES,
  rate,
  runs,
  scale,
  systemFacts,
  systemLine,
  timeAt,
  timeTicks,
  uptime,
  usedOfTotal,
  yAxis,
  type DetailMachine,
  type DetailPoint,
} from "./machine-detail";
import type { Session } from "./types";

const MB = 1 << 20;
const GB = 1 << 30;

describe("ranges", () => {
  it("match the hub's steps and refetch the hour more often", () => {
    expect(RANGES["1h"]).toMatchObject({ secs: 3600, step: 10, poll: 10_000 });
    expect(RANGES["6h"]).toMatchObject({ step: 60, poll: 60_000 });
    expect(RANGES["24h"]).toMatchObject({ secs: 86400, step: 240 });
  });
});

describe("series", () => {
  const pts: DetailPoint[] = [
    { t: 0, cpu: 10 },
    { t: 10, cpu: 20 },
    { t: 20, gap: true },
    { t: 60, cpu: 30 },
    { t: 70 }, // the first sample after a restart has no CPU
    { t: 80, cpu: 50 },
    { t: 90, cpu: 60 },
  ];

  it("break the line at gaps and missing values", () => {
    expect(runs(pts, (p) => p.cpu)).toEqual([
      [{ t: 0, v: 10 }, { t: 10, v: 20 }],
      [{ t: 60, v: 30 }],
      [{ t: 80, v: 50 }, { t: 90, v: 60 }],
    ]);
    expect(runs([], (p) => p.cpu)).toEqual([]);
  });

  it("find the nearest point within two steps, nothing over a gap", () => {
    expect(nearest(pts, 12, 10)?.t).toBe(10);
    expect(nearest(pts, 86, 10)?.t).toBe(90);
    expect(nearest(pts, 35, 10)).toBeUndefined(); // 25 s from both sides of the gap: more than two steps
    expect(nearest([{ t: 0, gap: true }], 0, 10)).toBeUndefined();
  });

  it("know when there is nothing to draw", () => {
    expect(isEmpty([{ t: 0, gap: true }])).toBe(true);
    expect(isEmpty(pts)).toBe(false);
    expect(memPercent(24 * GB, 48 * GB)).toBe(50);
    expect(memPercent(undefined, 48 * GB)).toBeUndefined();
    expect(memPercent(1, 0)).toBeUndefined();
  });
});

describe("scales", () => {
  it("map linearly and invert the pointer", () => {
    const x = scale(100, 200, 0, 400);
    expect(x(150)).toBe(200);
    const y = scale(0, 100, 140, 0);
    expect(y(25)).toBe(105);
    expect(timeAt(200, 400, 100, 200)).toBe(150);
    expect(timeAt(-5, 400, 100, 200)).toBe(100);
    expect(timeAt(999, 400, 100, 200)).toBe(200);
  });

  it("pick round steps, in 1024s for bytes", () => {
    expect(niceStep(100, 2)).toBe(50);
    expect(niceStep(37, 2)).toBe(20);
    expect(niceStep(3 * MB, 2, true)).toBe(2 * MB);
    expect(niceStep(1100 * 1024, 2, true)).toBe(MB); // 550 KB → 1 MB, not "1000 KB"
    expect(niceStep(0)).toBe(1);
  });

  it("keep percent axes at 0…100 and others a little over the top", () => {
    expect(yAxis(37, { percent: true })).toEqual({ top: 100, ticks: [0, 50, 100] });
    const net = yAxis(3 * MB, { binary: true });
    expect(net.top).toBeGreaterThanOrEqual(3 * MB);
    expect(net.ticks[0]).toBe(0);
    expect(net.ticks.length).toBeLessThanOrEqual(3);
    expect(net.ticks[net.ticks.length - 1]).toBe(net.top);
    expect(yAxis(0).top).toBeGreaterThan(0);
  });

  it("put at most four round times on the local clock", () => {
    const t1 = 1_790_000_000;
    const hour = timeTicks(t1 - 3600, t1, 4);
    expect(hour.length).toBeLessThanOrEqual(4);
    expect(hour.every((t) => t % (15 * 60) === 0)).toBe(true);
    const day = timeTicks(t1 - 86400, t1, 4);
    expect(day.length).toBeLessThanOrEqual(4);
    for (const t of day) expect(hm(t)).toMatch(/^(00|06|12|18):00$/); // 00:00, 06:00… local
  });

  describe("across a daylight-saving change (#9863)", () => {
    // America/New_York: 02:00 EDT became 01:00 EST on 2026-11-01; 02:00 EST becomes 03:00 EDT on 2027-03-14.
    const savedTZ = process.env.TZ;
    beforeAll(() => { process.env.TZ = "America/New_York"; });
    afterAll(() => { if (savedTZ === undefined) delete process.env.TZ; else process.env.TZ = savedTZ; });
    const at = (iso: string) => Date.parse(iso) / 1000;

    it("read each time with the offset it had", () => {
      expect(hm(at("2026-10-31T16:00:00Z"))).toBe("12:00");
      expect(hm(at("2026-11-02T17:00:00Z"))).toBe("12:00");
      expect(hm(at("2027-03-13T17:00:00Z"))).toBe("12:00");
      expect(hm(at("2027-03-15T16:00:00Z"))).toBe("12:00");
    });

    it("keep the ticks on round local times on both sides", () => {
      expect(timeTicks(at("2026-10-31T16:00:00Z"), at("2026-11-01T16:00:00Z"), 4).map((t) => hm(t))).toEqual(["12:00", "18:00", "00:00", "06:00"]);
      expect(timeTicks(at("2027-03-13T17:00:00Z"), at("2027-03-14T15:00:00Z"), 4).map((t) => hm(t))).toEqual(["12:00", "18:00", "00:00", "06:00"]);
      // the hour the clocks repeat gets one tick, the hour they skip none
      expect(timeTicks(at("2026-11-01T04:00:00Z"), at("2026-11-01T08:00:00Z"), 4).map((t) => hm(t))).toEqual(["00:00", "01:00", "02:00", "03:00"]);
      expect(timeTicks(at("2027-03-14T05:00:00Z"), at("2027-03-14T09:00:00Z"), 4).map((t) => hm(t))).toEqual(["00:00", "01:00", "03:00", "04:00", "05:00"]);
    });
  });
});

describe("formats", () => {
  it("say used of total with one shared unit so the 390-wide tile shows the total (#3637)", () => {
    expect(usedOfTotal(56.5 * GB, 64 * GB)).toBe("56.5 / 64.0 GB");
    expect(usedOfTotal(640 * GB, 1000 * GB)).toBe("640 / 1000 GB");
    expect(usedOfTotal(1.2 * 1024 * GB, 2 * 1024 * GB)).toBe("1.20 / 2.00 TB");
    expect(usedOfTotal(812 * MB, 8 * GB)).toBe("812 MB / 8.00 GB");
    expect(usedOfTotal(800, 1000)).toBe("800 / 1000 B");
    expect(usedOfTotal(undefined, 64 * GB)).toBe("— / 64.0 GB");
    expect(usedOfTotal(5 * GB, undefined)).toBe("5.00 GB / —");
  });

  // apple/Shared/MachineMetrics.swift bytes: the unit and the decimals follow the number as it is shown, after rounding (#8497).
  it("step to the next unit when the value rounds to 1024, like the app's bytes (#8497)", () => {
    expect(bytesBin(1023.4 * 1024)).toBe("1023 KB");
    expect(bytesBin(1023.6 * 1024)).toBe("1.00 MB");
    expect(bytesBin(1023.6 * MB)).toBe("1.00 GB");
    expect(bytesBin(1023.6 * GB)).toBe("1.00 TB");
    expect(bytesBin(1023.6 * 1024 * GB)).toBe("1024 TB");
  });

  it("choose the decimals from the rounded value, like the app's bytes (#8497)", () => {
    expect(bytesBin(99.96 * MB)).toBe("100 MB");
    expect(bytesBin(99.94 * MB)).toBe("99.9 MB");
    expect(bytesBin(9.999 * MB)).toBe("10.0 MB");
    expect(bytesBin(9.99 * MB)).toBe("9.99 MB");
  });

  it("say bytes, rates, percents and uptimes like the app", () => {
    expect(bytesBin(48 * GB)).toBe("48.0 GB");
    expect(bytesBin(512 * 1024)).toBe("512 KB");
    expect(bytesBin(1.5 * MB)).toBe("1.50 MB");
    expect(bytesBin(800)).toBe("800 B");
    expect(bytesBin(undefined)).toBe("—");
    expect(bytesAxis(2 * MB)).toBe("2 MB");
    expect(bytesAxis(1.5 * MB)).toBe("1.5 MB");
    expect(bytesAxis(0)).toBe("0");
    expect(rate(2048)).toBe("2.00 KB/s");
    expect(rate(undefined)).toBe("—");
    expect(percent(4.46)).toBe("4.5%");
    expect(percent(42.6)).toBe("43%");
    expect(percent(0)).toBe("0%");
    expect(percent(undefined)).toBe("—");
    expect(uptime(0, 3 * 86400 + 4 * 3600 + 60)).toBe("3 天 4 小时");
    expect(uptime(0, 5 * 3600 + 12 * 60)).toBe("5 小时 12 分钟");
    expect(uptime(0, 8 * 60 + 5)).toBe("8 分钟");
    expect(hm(13 * 3600 + 5 * 60, 0)).toBe("13:05");
    expect(hm(23 * 3600, 2 * 3600)).toBe("01:00");
  });

  it("grade readings against the alert lines", () => {
    expect(level(74, 75, 90)).toBe("normal");
    expect(level(75, 75, 90)).toBe("warning");
    expect(level(92, 75, 90)).toBe("critical");
    expect(level(undefined, 75, 90)).toBe("normal");
  });
});

const sess = (id: string, over: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: id, title: id, cwd: "/x", status: "idle", state: "idle", ...over });

describe("agents by space", () => {
  it("group live agents by space in herdr's order, loose ones last, the assistant left out", () => {
    const groups = agentsBySpace([
      sess("b2", { space_id: "w2", space_name: "blog", space_index: 2, slot_index: 2 }),
      sess("loose", { state: "working" }),
      sess("a1", { space_id: "w1", space_name: "weather", space_index: 1, slot_index: 1, state: "needs_you" }),
      sess("b1", { space_id: "w2", space_name: "blog", space_index: 2, slot_index: 1, state: "working" }),
      sess("gone", { space_id: "w1", space_name: "weather", space_index: 1, state: "offline" }),
      sess("helper", { assistant: true, space_id: "w1", space_index: 1 }),
    ]);
    expect(groups.map((g) => g.name)).toEqual(["weather", "blog", "未加入空间"]);
    expect(groups[1].sessions.map((s) => s.session_id)).toEqual(["b1", "b2"]);
    expect(groups[0].sessions.map((s) => s.session_id)).toEqual(["a1"]);
    expect(groups[0].counts).toEqual({ needs: 1, working: 0, unread: 0 });
    expect(countsText(groups[1].counts)).toBe("1 个工作中");
    expect(countsText({ needs: 0, working: 0, unread: 0 })).toBe("");
  });

  it("label the tab and a non-Claude agent", () => {
    expect(agentMeta(sess("x", { slot_name: "2", agent: "codex" }))).toBe("#2 · Codex");
    expect(agentMeta(sess("x", { slot_name: "2", agent: "traex", agent_label: "traex" }))).toBe("#2 · TRAE CLI");
    expect(agentMeta(sess("x", { slot_name: "widget", agent: "claude" }))).toBe("widget");
    expect(agentMeta(sess("x", { agent: "dsh", agent_label: "DSH" }))).toBe("DSH");
  });
});

describe("system", () => {
  const m: DetailMachine = { id: "macbook", name: "MacBook", online: true, kind: "ssh", ssh_host: "macbook", hostname: "MacBook", claude_version: "2.1.320", herdr_version: "0.9.5", arch: "arm64" };
  const sys = { name: "macOS 26.1", build: "25B78", kernel: "Darwin 25.1.0", arch: "arm64", model: "MacBook Pro", cpu: "Apple M4 Pro", cores: 14, memory: 48 * GB, boot_time: 1_790_000_000 };

  it("say the system in one line", () => {
    expect(systemLine(sys, m)).toBe("macOS 26.1 · MacBook Pro · Apple M4 Pro · 14 核 · 48.0 GB");
    expect(systemLine(undefined, { ...m, os: "darwin" })).toBe("darwin · arm64");
  });

  it("list the facts with uptime only while online", () => {
    const facts = systemFacts(sys, m, 1_790_000_000 + 3 * 86400 + 4 * 3600, 0);
    const get = (k: string) => facts.find((f) => f[0] === k)?.[1];
    expect(get("系统")).toBe("macOS 26.1（25B78）");
    expect(get("型号")).toBe("MacBook Pro");
    expect(get("核心")).toBe("14 核");
    expect(get("开机时间")).toContain("已运行 3 天 4 小时");
    expect(get("Claude Code")).toBe("2.1.320");
    expect(get("连接方式")).toBe("hub 通过 ssh macbook 连接");
    const off = systemFacts(sys, { ...m, online: false }, 1_790_000_000 + 100, 0);
    expect(off.find((f) => f[0] === "开机时间")?.[1]).not.toContain("已运行");
    expect(connectionText({ ...m, kind: "local" })).toBe("hub 所在的机器，直接运行 node");
  });
});

describe("byte axes", () => {
  it("turn a top of 1000 KB into 1 MB", () => {
    expect(yAxis(900 * 1024, { binary: true })).toEqual({ top: MB, ticks: [0, 512 * 1024, MB] });
    expect(bytesAxis(512 * 1024)).toBe("512 KB");
    expect(bytesAxis(MB)).toBe("1 MB");
  });
});

describe("load and cores", () => {
  it("load axis labels drop trailing zeros", () => {
    expect(loadAxis(0)).toBe("0");
    expect(loadAxis(0.5)).toBe("0.5");
    expect(loadAxis(16)).toBe("16");
    expect(loadAxis(2.125)).toBe("2.13");
  });

  it("each core only for an online computer with more than one (Linux sends them)", () => {
    expect(coresNow({ t: 1, cores: [10, 120, -3] }, true)).toEqual([10, 100, 0]);
    expect(coresNow({ t: 1, cores: [10, 20] }, false)).toBeUndefined();
    expect(coresNow({ t: 1, cores: [10] }, true)).toBeUndefined();
    expect(coresNow({ t: 1 }, true)).toBeUndefined();
    expect(coresNow(null, true)).toBeUndefined();
  });
});
