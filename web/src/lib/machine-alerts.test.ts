// The Machines page's alert model (#8037): which alerts a computer has, worst first; none for a computer the card
// explains itself (pending, never connected, its assistant offline); the cards' order (the apps' MachineConcern);
// and how far apart two builds are.
import { afterEach, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { alertLabel, alertTitle, buildGap, buildTime, concernOf, gapText, loadDefinition, machineAlerts, offlineReason, orderMachines, perCoreLabel, perCoreText, readings, worst, type MachineAlert } from "./machine-alerts";
import type { Machine, MachineMetrics } from "./types";

const GB = 1024 ** 3;
const machine = (over: Partial<Machine> = {}): Machine => ({ id: "m", name: "Studio", online: true, system: { cores: 8, memory: 16 * GB }, ...over });
const metrics = (latest: NonNullable<MachineMetrics["latest"]>, over: Partial<MachineMetrics> = {}): MachineMetrics => ({ machine_id: "m", online: true, points: [], latest, ...over });
const kinds = (a: MachineAlert[]) => a.map((x) => `${x.kind}:${x.severity}`);

describe("machineAlerts", () => {
  it("has none for a healthy computer", () => {
    expect(machineAlerts({ machine: machine(), metrics: metrics({ t: 1, cpu: 20, mem_used: 4 * GB, mem_total: 16 * GB, disk_used: 10, disk_total: 100, load: 2 }) })).toEqual([]);
  });

  it("reads the thresholds of docs/server/machines.md, at and over each line", () => {
    const at = (latest: NonNullable<MachineMetrics["latest"]>) => kinds(machineAlerts({ machine: machine(), metrics: metrics(latest) }));
    expect(at({ t: 1, cpu: 74.9 })).toEqual([]);
    expect(at({ t: 1, cpu: 75 })).toEqual(["cpu:warning"]);
    expect(at({ t: 1, cpu: 90 })).toEqual(["cpu:critical"]);
    expect(at({ t: 1, mem_used: 12.8 * GB, mem_total: 16 * GB })).toEqual(["memory:warning"]);
    expect(at({ t: 1, mem_used: 14.4 * GB, mem_total: 16 * GB })).toEqual(["memory:critical"]);
    expect(at({ t: 1, disk_used: 80, disk_total: 100 })).toEqual(["disk:warning"]);
    expect(at({ t: 1, disk_used: 95, disk_total: 100 })).toEqual(["disk:critical"]);
    // load per core: 8 cores, 8 is 1× (warning), 16 is 2× (critical)
    expect(at({ t: 1, load: 7.9 })).toEqual([]);
    expect(at({ t: 1, load: 8 })).toEqual(["load:warning"]);
    expect(at({ t: 1, load: 16 })).toEqual(["load:critical"]);
  });

  it("takes the memory total from the sample, then the metrics' system, then the machine's", () => {
    const a = machineAlerts({ machine: machine({ system: { memory: 100 } }), metrics: metrics({ t: 1, mem_used: 95 }) });
    expect(kinds(a)).toEqual(["memory:critical"]);
    expect(a[0].value).toBe(95);
  });

  it("puts the worst first: critical, warning, info, then by kind", () => {
    const a = machineAlerts({
      machine: machine({ node_mismatch: true }),
      metrics: metrics({ t: 1, cpu: 80, mem_used: 15 * GB, mem_total: 16 * GB, disk_used: 85, disk_total: 100 }),
      launchers: 2,
    });
    expect(kinds(a)).toEqual(["memory:critical", "disk:warning", "cpu:warning", "launcher:warning", "mismatch:info"]);
    expect(a.find((x) => x.kind === "launcher")?.count).toBe(2);
    expect(worst(a)).toBe("critical");
  });

  it("gives an offline computer its reason and no metric alerts", () => {
    const m = machine({ online: false, error: "ssh: connect to host nas port 22: Operation timed out" });
    const a = machineAlerts({ machine: m, metrics: metrics({ t: 1, cpu: 99 }, { online: false }) });
    expect(a).toEqual([{ kind: "offline", severity: "critical", reason: "timeout" }]);
    expect(offlineReason(machine({ online: false, error_code: "node_service_down", error: "timed out" }))).toBe("node_service_down");
    expect(offlineReason(machine({ online: false, error: "something else" }))).toBe("unknown");
  });

  it("has none for a computer the card explains itself", () => {
    expect(machineAlerts({ machine: machine({ trust: "pending", online: false }) })).toEqual([]);
    expect(machineAlerts({ machine: machine({ online: false, error_code: "never_connected" }) })).toEqual([]);
    expect(machineAlerts({ machine: machine({ online: false, error: "timed out" }), managerOffline: true })).toEqual([]);
  });

  it("does not call a planned restart offline, and says a failed update", () => {
    expect(machineAlerts({ machine: machine({ online: false, deploy_phase: "restarting", deploying: true, node_mismatch: true }) })).toEqual([{ kind: "mismatch", severity: "info", busy: true }]);
    expect(kinds(machineAlerts({ machine: machine({ node_mismatch: true, deploy_error: "copy failed" }) }))).toEqual(["deploy_failed:warning"]);
  });

  it("marks draining deployments as waiting to restart while online, and still reports unexpected disconnects", () => {
    const draining = machineAlerts({ machine: machine({ online: true, deploy_phase: "draining", deploying: true, node_mismatch: true }) });
    expect(draining).toEqual([{ kind: "mismatch", severity: "info", busy: true, draining: true }]);
    expect(alertTitle(draining[0]!)).toBe("待空闲重启");
    expect(alertLabel(draining[0]!)).toBe("待空闲重启");

    const dropped = machineAlerts({ machine: machine({ online: false, deploy_phase: "draining", deploying: true, node_mismatch: true, error: "link offline" }) });
    expect(kinds(dropped)).toEqual(["offline:critical", "mismatch:info"]);
  });

  it("labels each alert with its number, and an offline card's chip with what it opens", () => {
    expect(alertLabel({ kind: "memory", severity: "critical", value: 95.2 })).toBe("内存占用 · 95%");
    expect(alertLabel({ kind: "launcher", severity: "warning", count: 2 })).toBe("启动器异常 · 2");
    expect(alertLabel({ kind: "load", severity: "warning", value: 1.24 })).toBe("负载偏高 · 每核 1.2");
    expect(alertLabel({ kind: "offline", severity: "critical" })).toBe("离线");
    expect(alertLabel({ kind: "offline", severity: "critical" }, true)).toBe("离线 · 排查建议");
  });
});

describe("one meaning for load (#12519)", () => {
  // switching languages loads their catalogs, which takes a few seconds on a busy machine
  afterEach(async () => {
    await initI18n("zh-Hans");
  }, 30_000);

  it("writes load per core with one decimal everywhere, the figure the alert goes by", () => {
    expect(perCoreText(25.4 / 18)).toBe("1.4");
    expect(perCoreText(0.04)).toBe("0.0");
    expect(perCoreLabel(1.43)).toBe("每核 1.4");
    const studio = machine({ system: { cores: 18, memory: 16 * GB } });
    const now = metrics({ t: 1, cpu: 41, load: 25.4 });
    const r = readings(studio, now);
    expect(perCoreText(r.load!)).toBe("1.4");
    const [load] = machineAlerts({ machine: studio, metrics: now });
    expect(load.kind).toBe("load");
    expect(alertLabel(load)).toBe(`负载偏高 · ${perCoreLabel(r.load!)}`);
  });

  it("defines load in one sentence, and calls the used-memory alert what it counts", async () => {
    expect(loadDefinition()).toMatch(/^负载指过去 1 分钟内正在运行及等待 CPU 的平均线程数/);
    await initI18n("en");
    expect(loadDefinition()).toMatch(/^Load is the number of threads running or waiting for a CPU/);
    expect(perCoreLabel(1.43)).toBe("1.4 per core");
    expect(alertLabel({ kind: "memory", severity: "critical", value: 95.2 })).toBe("Memory used · 95%");
    expect(alertLabel({ kind: "load", severity: "warning", value: 1.24 })).toBe("High load · 1.2 per core");
  }, 30_000);

  it("lets no alert other than load call itself load, in any language", async () => {
    // the word for load in each language; ja's CPU alert used to read "CPU 高負荷", CPU high load
    for (const [lang, word] of [["zh-Hans", "负载"], ["en", "load"], ["ja", "負荷"]] as const) {
      await initI18n(lang);
      expect(alertTitle({ kind: "load" }).toLowerCase()).toContain(word);
      for (const kind of ["memory", "disk", "cpu", "launcher", "offline", "deploy_failed", "mismatch"] as const) {
        expect(alertTitle({ kind }).toLowerCase()).not.toContain(word);
      }
    }
  }, 30_000);
});

describe("the cards' order", () => {
  const list = [
    machine({ id: "ok", name: "ok" }),
    machine({ id: "mismatch", node_mismatch: true }),
    machine({ id: "needs" }),
    machine({ id: "warn" }),
    machine({ id: "crit" }),
    machine({ id: "off", online: false, error: "timed out" }),
    machine({ id: "pending", trust: "pending" }),
    machine({ id: "ok2" }),
  ];
  const alertsOf: Record<string, MachineAlert[]> = {
    warn: [{ kind: "disk", severity: "warning" }],
    crit: [{ kind: "memory", severity: "critical" }],
    mismatch: [{ kind: "mismatch", severity: "info" }],
  };
  const concern = (m: Machine) => concernOf(m, alertsOf[m.id] ?? [], m.id === "needs" ? 1 : 0);

  it("follows MachineConcern: pending, offline, critical, warning, needs you, mismatch, then the rest in the hub's order", () => {
    expect(orderMachines(list, concern).map((m) => m.id)).toEqual(["pending", "off", "crit", "warn", "needs", "mismatch", "ok", "ok2"]);
  });

  it("keeps the same array when nothing moves", () => {
    const calm = [machine({ id: "a" }), machine({ id: "b" })];
    expect(orderMachines(calm, () => null)).toBe(calm);
  });
});

describe("builds", () => {
  it("reads a build's time off its name", () => {
    expect(buildTime("v0.9.0-14-g1a2b3c4-202611141020")).toBe(Date.UTC(2026, 10, 14, 10, 20));
    expect(buildTime("abc1234-dirty-202611010900")).toBe(Date.UTC(2026, 10, 1, 9, 0));
    expect(buildTime("mock")).toBeUndefined();
    expect(buildTime(undefined)).toBeUndefined();
  });

  it("says how far apart two builds are", () => {
    const gap = buildGap("v1-202611141020", "v1-202611110815");
    expect(gap).toBe(Date.UTC(2026, 10, 14, 10, 20) - Date.UTC(2026, 10, 11, 8, 15));
    expect(gapText(gap!)).toBe("3 天");
    expect(gapText(-5 * 3600_000)).toBe("5 小时");
    expect(gapText(10_000)).toBe("1 分钟");
    expect(buildGap("dev", "v1-202611110815")).toBeUndefined();
  });
});
