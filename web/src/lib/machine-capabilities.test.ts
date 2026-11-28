// The capability index's rows and the hand-off's ranked targets (#8037).
import { describe, expect, it } from "vitest";
import { capabilityRow, osFamily, rankTargets, runtimeGroups, toolsText, type TargetCandidate } from "./machine-capabilities";
import type { Machine, MachineMetrics } from "./types";

const GB = 1024 ** 3;
const mac = (over: Partial<Machine> = {}): Machine => ({ id: "mac", name: "Studio Mac", online: true, os: "darwin", system: { cores: 10, memory: 32 * GB }, ...over });
const linux = (over: Partial<Machine> = {}): Machine => ({ id: "box", name: "Build Box", online: true, os: "linux", system: { cores: 16, memory: 64 * GB }, ...over });
const sample = (mid: string, used: number, total: number, extra: Partial<NonNullable<MachineMetrics["latest"]>> = {}): MachineMetrics => ({ machine_id: mid, online: true, points: [], latest: { t: 1, mem_used: used, mem_total: total, ...extra } });

describe("capabilityRow", () => {
  it("lists what a computer is and can run", () => {
    const m = { ...mac(), kind: "link", arch: "arm64", engines: [{ id: "herdr", name: "herdr", available: true, version: "0.9.5" }, { id: "orca", name: "Orca", available: false }], system: { name: "macOS 26.1", model: "Mac mini", cores: 10, memory: 32 * GB } } as Machine;
    const row = capabilityRow({
      machine: m,
      machines: [m],
      metrics: sample("mac", 8 * GB, 32 * GB, { disk_used: 300 * GB, disk_total: 500 * GB }),
      launchers: [{ name: "claude" }, { name: "team", display_name: "Team relay", agent: "claude" }, { name: "codex", agent: "codex" }],
      tools: { state: "ok", tools: [{ id: "docker", version: "27.3.1" }, { id: "git", version: "" }] },
    });
    expect(row).toMatchObject({ name: "Studio Mac", presence: "online", system: "macOS 26.1 · arm64", model: "Mac mini", cores: 10, memory: 32 * GB, diskFree: 200 * GB, engines: ["herdr 0.9.5"] });
    expect(row.runtimes).toEqual({ state: "ok", value: [{ id: "claude", name: "Claude Code", entries: ["Claude Code", "Team relay"] }, { id: "codex", name: "Codex", entries: ["Codex"] }] });
    expect(row.tools.state === "ok" && toolsText(row.tools.value)).toBe("Docker 27.3.1 · Git");
    expect(row.reach).not.toBe("");
  });

  it("says why tools and runtimes are missing", () => {
    const off = capabilityRow({ machine: mac({ online: false }), machines: [] });
    expect(off.presence).toBe("offline");
    expect(off.tools).toEqual({ state: "unavailable", text: "连接后显示" });
    expect(capabilityRow({ machine: mac(), machines: [] }).tools).toEqual({ state: "loading" });
    expect(capabilityRow({ machine: mac(), machines: [], tools: { state: "failed", failure: { kind: "node_outdated", text: "" } } }).tools).toEqual({ state: "unavailable", text: "更新这台电脑上的 Persona 后即可查看" });
    expect(capabilityRow({ machine: mac(), machines: [], tools: { state: "failed", failure: { kind: "hub_outdated", text: "" } } }).tools).toEqual({ state: "unavailable", text: "更新 hub 后即可查看" });
    expect(capabilityRow({ machine: mac(), machines: [], launchers: null }).runtimes.state).toBe("unavailable");
  });

  it("groups one runtime's named entries", () => {
    expect(runtimeGroups([{ name: "codex", agent: "codex" }, { name: "codex-local", display_name: "Codex (local)", agent_type: "codex" }])).toEqual([{ id: "codex", name: "Codex", entries: ["Codex", "Codex (local)"] }]);
  });

  it("tells OS families", () => {
    expect(osFamily(mac())).toBe("macos");
    expect(osFamily(linux())).toBe("linux");
    expect(osFamily(mac({ os: "windows" }))).toBe("windows");
  });
});

describe("rankTargets", () => {
  const source = mac({ id: "laptop", name: "Travel Laptop" });

  it("leaves out the source and makes offline, pending and critical computers ineligible, each saying why", () => {
    const candidates: TargetCandidate[] = [
      { machine: source },
      { machine: linux({ id: "nas", name: "NAS", online: false, error: "timed out" }) },
      { machine: linux({ id: "new", name: "New", trust: "pending" }) },
      { machine: linux({ id: "hot", name: "Hot" }), metrics: sample("hot", 60 * GB, 64 * GB) },
      { machine: mac({ id: "mini", name: "Mini" }), metrics: sample("mini", 10 * GB, 32 * GB) },
    ];
    const r = rankTargets(source, candidates);
    expect(r.map((x) => [x.machine.id, x.eligible])).toEqual([["mini", true], ["nas", false], ["new", false], ["hot", false]]);
    expect(r.find((x) => x.machine.id === "nas")?.reason).toBe("离线");
    expect(r.find((x) => x.machine.id === "new")?.reason).toBe("尚未确认或尚未连接");
    expect(r.find((x) => x.machine.id === "hot")?.reason).toBe("内存 94%");
    expect(r[0].reason).toBe("同一系统 · 空闲内存 22 GB");
  });

  it("ranks by fewer alerts, then more free memory, then the same system, then the source's tools", () => {
    const big = { machine: linux({ id: "big", name: "Big" }), metrics: sample("big", 8 * GB, 64 * GB) };
    const busy = { machine: linux({ id: "busy", name: "Busy" }), metrics: sample("busy", 8 * GB, 64 * GB, { disk_used: 85, disk_total: 100 }) };
    const small = { machine: mac({ id: "small", name: "Small" }), metrics: sample("small", 8 * GB, 16 * GB) };
    expect(rankTargets(source, [busy, small, big]).map((x) => x.machine.id)).toEqual(["big", "small", "busy"]);
    expect(rankTargets(source, [busy]).at(0)?.reason).toBe("Linux · 空闲内存 56 GB · 磁盘 85%");

    const a = { machine: linux({ id: "a", name: "A" }), metrics: sample("a", 8 * GB, 16 * GB) };
    const b = { machine: mac({ id: "b", name: "B" }), metrics: sample("b", 8 * GB, 16 * GB) };
    expect(rankTargets(source, [a, b]).map((x) => x.machine.id)).toEqual(["b", "a"]);

    const tools = [{ id: "xcode", version: "26" }, { id: "docker", version: "27" }];
    const c = { machine: mac({ id: "c", name: "C" }), metrics: sample("c", 8 * GB, 16 * GB), tools: [{ id: "git", version: "" }] };
    const d = { machine: mac({ id: "d", name: "D" }), metrics: sample("d", 8 * GB, 16 * GB), tools };
    const ranked = rankTargets(source, [c, d], { sourceTools: tools });
    expect(ranked.map((x) => x.machine.id)).toEqual(["d", "c"]);
    expect(ranked[1].reason).toContain("缺少 Xcode");
  });

  it("picks nothing when no computer is eligible", () => {
    const r = rankTargets(source, [{ machine: linux({ online: false }) }]);
    expect(r.every((x) => !x.eligible)).toBe(true);
  });
});
