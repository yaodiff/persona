import { describe, expect, it } from "vitest";
import {
  canSaveEngine,
  canSelectEngine,
  currentEngine,
  detectionText,
  engineDetail,
  engineState,
  orcaStopped,
  engineFooter,
  engineName,
  engineOptions,
  engineSaveError,
  KNOWN_ENGINES,
  alsoOpenText,
  currentHosts,
  ENGINE_HOSTS,
  hostOptions,
  hostsFooter,
  hostsName,
  normalizeHosts,
  sameHosts,
  startHosts,
  supportsHostSets,
  toggleHost,
} from "./engines";
import type { Machine } from "./types";

const machine = (extra: Partial<Machine> = {}): Machine => ({ id: "computer", name: "电脑", online: false, ...extra });

describe("engine choice (iOS EngineSelectionModel)", () => {
  it("is herdr until one is chosen, whatever happens to be detected", () => {
    expect(currentEngine(machine())).toBe("herdr");
    expect(currentEngine(machine({ engines: [{ id: "herdr-core", name: "Herdr App", available: true }] }))).toBe("herdr");
    expect(currentEngine(machine({ engine: "herdr-core" }))).toBe("herdr-core");
  });

  it("lists what the node reports, names, notes and future engines included", () => {
    const m = machine({
      online: true,
      engine: "orca",
      engines: [
        { id: "orca", name: "Orca Desktop", available: false, note: "请先更新 Orca" },
        { id: "future", name: "新引擎", available: true },
      ],
    });
    expect(engineOptions(m)).toBe(m.engines);
    expect(engineName("orca", m)).toBe("Orca Desktop");
    expect(engineName("future", m)).toBe("新引擎");
    expect(canSelectEngine("orca", m)).toBe(true);
    expect(canSelectEngine("future", m)).toBe(true);
    m.engines![1].available = false;
    expect(canSelectEngine("future", m)).toBe(false);
  });

  it("lists every known engine when the node reports none (offline, older hubs)", () => {
    const m = machine({ engine: "future" });
    expect(engineOptions(m).map((e) => e.id)).toEqual(["herdr", "herdr-core", "herdr-both", "orca", "tern", "rex", "paseo", "cindy", "t3code"]);
    expect(engineOptions(m).every((e) => !e.available)).toBe(true);
    expect(engineOptions({ engines: [] }).length).toBe(KNOWN_ENGINES.length);
    // known ones stay selectable offline; an unknown one only while reported available
    for (const e of KNOWN_ENGINES) expect(canSelectEngine(e.id, m)).toBe(true);
    expect(canSelectEngine("future", m)).toBe(false);
    expect(engineName("future", m)).toBe("future");
  });

  it("names and describes the known engines like iOS", () => {
    expect(engineName("herdr-both")).toBe("herdr + Herdr App");
    expect(engineDetail("herdr-both")).toBe("同时显示终端与桌面端的会话");
    expect(engineName("paseo")).toBe("Paseo");
    expect(engineDetail("paseo")).toBe("Paseo 中的 Agent");
    expect(engineName("tern")).toBe("Tern");
    expect(engineDetail("tern")).toBe("Tern 中的终端");
    expect(engineName("rex")).toBe("Rex");
    expect(engineDetail("rex")).toBe("Rex 中的终端");
    expect(engineName("cindy")).toBe("Cindy");
    expect(engineDetail("cindy")).toBe("Cindy 中的 Agent");
    expect(engineName("t3code")).toBe("T3 Code");
    expect(engineDetail("t3code")).toBe("T3 Code 中的 Agent");
    expect(engineDetail("future")).toBeUndefined();
  });

  it("says what the node found", () => {
    const engines = [
      { id: "herdr", name: "herdr", available: true, version: "0.9.5" },
      { id: "herdr-core", name: "Herdr App", available: true },
      { id: "orca", name: "Orca", available: false, version: "1.0" },
      { id: "tern", name: "Tern", available: true, version: "0.4.1" },
    ];
    const on = machine({ online: true, engines });
    expect(detectionText("herdr", on)).toBe("检测到可用 · 0.9.5");
    expect(detectionText("herdr-core", on)).toBe("检测到可用");
    expect(detectionText("orca", on)).toBe("未检测到");
    expect(detectionText("tern", on)).toBe("检测到可用 · 0.4.1");
    expect(detectionText("paseo", on)).toBe("暂无检测结果");
    expect(detectionText("herdr", machine({ engines }))).toBe("电脑离线，暂无检测结果");
  });

  it("tells a closed Orca from one that is not there, and shows an incompatible one's version (#17037)", () => {
    const on = (orca: Record<string, unknown>) => machine({ online: true, engines: [{ id: "orca", name: "Orca", available: false, ...orca }] });
    expect(detectionText("orca", on({ state: "stopped" }))).toBe("已安装，未运行");
    expect(detectionText("orca", on({ state: "stopped", version: "1.4.277" }))).toBe("已安装，未运行 · 1.4.277");
    expect(detectionText("orca", on({ state: "incompatible", version: "1.4.100", note: "update" }))).toBe("不兼容 · 1.4.100");
    expect(detectionText("orca", on({ state: "missing" }))).toBe("未检测到");
    expect(detectionText("orca", on({ state: "from-the-future" }))).toBe("未检测到");
    expect(detectionText("orca", on({ available: true, state: "running", version: "1.4.277" }))).toBe("检测到可用 · 1.4.277");
  });

  it("reads a state it does not know as unknown", () => {
    expect(engineState({ state: "stopped" })).toBe("stopped");
    expect(engineState({ state: "sleeping" })).toBe("unknown");
    expect(engineState({})).toBe("unknown");
    expect(engineState(undefined)).toBe("unknown");
  });

  it("says Orca is stopped only for an online computer that enables it and whose node says so (#17037)", () => {
    const stopped = [{ id: "orca", name: "Orca", available: false, state: "stopped" }];
    const m = (extra: Partial<Machine>) => machine({ online: true, engine_hosts: ["herdr", "orca"], engine_default: "herdr", engines: stopped, ...extra });
    expect(orcaStopped(m({}))).toBe(true); // in the set, not the default
    expect(orcaStopped(m({ engine_hosts: ["orca"], engine_default: "orca" }))).toBe(true);
    expect(orcaStopped(m({ engine_hosts: ["herdr"], engine_default: "herdr" }))).toBe(false); // not enabled
    expect(orcaStopped(m({ online: false }))).toBe(false);
    expect(orcaStopped(m({ engines: [{ id: "orca", name: "Orca", available: true, state: "running" }] }))).toBe(false);
    expect(orcaStopped(m({ engines: [{ id: "orca", name: "Orca", available: false, state: "incompatible", note: "x" }] }))).toBe(false);
    expect(orcaStopped(m({ engines: [{ id: "orca", name: "Orca", available: false }] }))).toBe(false); // an older node
    expect(orcaStopped(machine({ online: true, engine: "orca", engines: stopped }))).toBe(true); // an older hub: its single engine
  });

  it("offers 保存 only for a change the hub can take", () => {
    const m = machine({ engine: "herdr-core" });
    expect(canSaveEngine("herdr-core", m)).toBe(false);
    expect(canSaveEngine("herdr", m)).toBe(true);
    expect(canSaveEngine("future", m)).toBe(false);
    expect(canSaveEngine("herdr", machine())).toBe(false);
  });

  it("explains the choice, offline included, and a failed save", () => {
    expect(engineFooter("herdr", true)).toBe("仅显示所选引擎中的会话。");
    expect(engineFooter("orca", false)).toBe("机器当前离线，设置将在重新连接后生效。仅显示所选引擎中的会话。");
    expect(engineFooter("herdr-both", true)).toContain("从网页端新建的会话仍会在 herdr 中启动");
    expect(engineFooter("herdr-both", false)).toBe("机器当前离线，设置将在重新连接后生效。将同时显示两个引擎中的会话。");
    expect(engineSaveError("暂时无法保存")).toBe("保存失败：暂时无法保存");
  });
});

describe("engine host sets (#2576)", () => {
  it("reads the hub's set, else the one the single engine stands for", () => {
    expect(supportsHostSets(machine())).toBe(false);
    expect(currentHosts(machine({ engine: "herdr-both" }))).toEqual({ hosts: ["herdr", "herdr-core"], default: "herdr" });
    expect(currentHosts(machine({ engine: "orca" }))).toEqual({ hosts: ["orca"], default: "orca" });
    expect(currentHosts(machine({ engine: "tern" }))).toEqual({ hosts: ["tern"], default: "tern" });
    expect(currentHosts(machine())).toEqual({ hosts: ["herdr"], default: "herdr" });
    const m = machine({ engine: "orca", engine_hosts: ["herdr", "orca"], engine_default: "orca" });
    expect(supportsHostSets(m)).toBe(true);
    expect(currentHosts(m)).toEqual({ hosts: ["herdr", "orca"], default: "orca" });
    expect(currentHosts(machine({ engine_hosts: [], engine_default: "" }))).toEqual({ hosts: [], default: "" });
  });

  it("toggles hosts in canonical order and keeps the default enabled", () => {
    let c = { hosts: ["herdr"], default: "herdr" };
    c = toggleHost(c, "orca");
    expect(c).toEqual({ hosts: ["herdr", "orca"], default: "herdr" });
    c = toggleHost(c, "herdr"); // the default goes: the first one left
    expect(c).toEqual({ hosts: ["orca"], default: "orca" });
    c = toggleHost(c, "orca"); // none
    expect(c).toEqual({ hosts: [], default: "" });
    expect(normalizeHosts(["paseo", "herdr", "nope"], "paseo")).toEqual({ hosts: ["herdr", "paseo"], default: "paseo" });
    // Tern goes between Orca and Paseo (#2154), and may be the default
    expect(toggleHost({ hosts: ["herdr", "paseo"], default: "herdr" }, "tern")).toEqual({ hosts: ["herdr", "tern", "paseo"], default: "herdr" });
    expect(normalizeHosts(["paseo", "tern", "orca"], "tern")).toEqual({ hosts: ["orca", "tern", "paseo"], default: "tern" });
    expect(sameHosts({ hosts: ["herdr"], default: "herdr" }, { hosts: ["herdr"], default: "herdr" })).toBe(true);
    expect(sameHosts({ hosts: ["herdr", "orca"], default: "herdr" }, { hosts: ["herdr", "orca"], default: "orca" })).toBe(false);
  });

  it("offers the set's members, not the combined engine, in canonical order", () => {
    const m = machine({
      online: true,
      engines: [
        { id: "paseo", name: "Paseo", available: false },
        { id: "tern", name: "Tern", available: true, version: "0.4.1" },
        { id: "orca", name: "Orca", available: true },
        { id: "herdr-both", name: "herdr + Herdr App", available: false },
        { id: "herdr", name: "herdr", available: true },
      ],
    });
    expect(hostOptions(m).map((e) => e.id)).toEqual(["herdr", "orca", "tern", "paseo"]);
    const offline = hostOptions(machine());
    expect(offline.map((e) => e.id)).toEqual(ENGINE_HOSTS);
    expect(offline.map((e) => e.name)).toEqual(["herdr", "Herdr App", "Orca", "Tern", "Rex", "Paseo", "Cindy", "T3 Code"]);
  });

  it("offers every enabled host to start in", () => {
    expect(startHosts(machine({ engine_hosts: ["herdr", "herdr-core", "orca"], engine_default: "herdr" }))).toEqual(["herdr", "herdr-core", "orca"]);
    expect(startHosts(machine({ engine: "herdr-both" }))).toEqual(["herdr", "herdr-core"]);
    expect(startHosts(machine({ engine_hosts: ["herdr-core", "tern"], engine_default: "tern" }))).toEqual(["herdr-core", "tern"]);
  });

  it("names the other hosts a session is open in", () => {
    expect(alsoOpenText({})).toBe("");
    expect(alsoOpenText({ also_open_in: ["orca"] })).toContain("Orca");
    expect(alsoOpenText({ also_open_in: ["tern"] })).toContain("Tern");
    expect(hostsFooter({ hosts: [], default: "" }, true)).toContain("未启用任何终端引擎");
    expect(hostsFooter({ hosts: ["herdr", "orca"], default: "orca" }, false)).toContain("Orca");
    expect(hostsFooter({ hosts: ["herdr", "tern"], default: "tern" }, true)).toContain("Tern");
  });

  it("names a set by its engines, for the header's engine pill", () => {
    expect(hostsName({ hosts: ["herdr", "orca"], default: "herdr" })).toBe("herdr + Orca");
    expect(hostsName({ hosts: ["orca"], default: "orca" })).toBe("Orca");
    expect(hostsName({ hosts: ["orca", "tern"], default: "tern" })).toBe("Orca + Tern");
    expect(hostsName({ hosts: [], default: "" })).not.toBe("");
  });
});
