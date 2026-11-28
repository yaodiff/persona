// @vitest-environment jsdom
// Settings › Managers' words (docs/assistant/managers.md): where one runs, its status line, why Delete is off, and the hub's refusals.
import { describe, expect, it, vi } from "vitest";
import { ApiError, errorText } from "./api";
import { deleteBlocked, deployText, hostedManagerAttention, hostedManagersForMachine, listManagers, managerError, managerVersionLag, minutesLeft, statusLine, unsupported, validName, whereText, type Manager, type ManagerList } from "./managers";
import { extensionInfoAccessMode } from "./scoped-extension-trust";

const NOW = Date.parse("2026-11-05T12:00:00Z");
const m = (over: Partial<Manager>): Manager => ({ id: "m-1", name: "工作", online: false, ...over });

it("recognizes only an explicit true capability for the owning assistant in the roster response", async () => {
  const response = {
    default: "assistant",
    managers: [
      m({ id: "assistant", online: true }),
      m({ id: "supported", online: true, extension_info_access_scoped: true }),
      m({ id: "previous", online: true, extension_info_access_scoped: false }),
      m({ id: "omitted", online: true }),
      { ...m({ id: "malformed", online: true }), extension_info_access_scoped: "true" },
    ],
  };
  vi.stubGlobal("fetch", vi.fn(async () => Response.json(response)));
  try {
    const roster = await listManagers();
    expect(extensionInfoAccessMode(roster, "assistant")).toBe("legacy");
    expect(extensionInfoAccessMode(roster, "supported")).toBe("scoped");
    expect(extensionInfoAccessMode(roster, "previous")).toBeUndefined();
    expect(extensionInfoAccessMode(roster, "omitted")).toBeUndefined();
    expect(extensionInfoAccessMode(roster, "malformed")).toBeUndefined();
    expect(extensionInfoAccessMode(roster, "unknown")).toBeUndefined();
  } finally {
    vi.unstubAllGlobals();
  }
});

describe("助手", () => {
  it("运行在哪：机器 · 系统 · 怎么跑", () => {
    expect(whereText(m({ builtin: true, host: "server", os: "linux", deploy: "child" }))).toBe("server · Linux · hub 内置");
    expect(whereText(m({ host: "MacBook", os: "darwin", deploy: "service" }))).toBe("MacBook · macOS · 服务");
    expect(whereText(m({ host: "NAS", os: "linux", deploy: "docker" }))).toBe("NAS · Linux · 容器");
    expect(whereText(m({ os: "freebsd", deploy: "nomad" }))).toBe("freebsd · nomad");
    expect(whereText(m({}))).toBe("");
    expect(deployText({ deploy: "external" })).toBe("单独运行");
    expect(deployText({ deploy: "launchd" })).toBe("服务");
    expect(deployText({ deploy: "container" })).toBe("容器");
  });

  it("状态：在线、离线多久前同步、安装中码还剩几分钟", () => {
    expect(statusLine(m({ online: true }), NOW)).toEqual({ text: "在线", tone: "green" });
    expect(statusLine(m({ last_seen: "2026-11-05T09:00:00Z" }), NOW)).toEqual({ text: "离线 · 最后同步于 3小时前", tone: "grey" });
    expect(statusLine(m({}), NOW).text).toBe("离线");
    expect(statusLine(m({ installing: "2026-11-05T12:06:30Z" }), NOW)).toEqual({ text: "安装中 · 安装码 7 分钟后过期", tone: "accent" });
    expect(statusLine(m({ installing: "2026-11-05T11:59:00Z" }), NOW).text).toBe("安装中 · 安装码已过期");
    expect(minutesLeft("2026-11-05T12:10:00.040Z", NOW)).toBe(10);
    expect(minutesLeft("2026-11-05T12:00:10Z", NOW)).toBe(1);
    expect(minutesLeft("not a time", NOW)).toBe(0);
  });

  it("hub 自己的、默认的不能删，说为什么", () => {
    expect(deleteBlocked(m({ builtin: true, default: true }))).toBe("无法删除由 hub 自身运行的助手。");
    expect(deleteBlocked(m({ default: true }))).toBe("无法删除默认助手，请先将其他助手设为默认。");
    expect(deleteBlocked(m({}))).toBe("");
  });

  it("名字 1 到 40 个字", () => {
    expect(validName("  ")).toBe(false);
    expect(validName("工作")).toBe(true);
    expect(validName("字".repeat(40))).toBe(true);
    expect(validName("字".repeat(41))).toBe(false);
  });

  it("hub 的拒绝说成大白话；没有名册的旧 hub 单独认出来", () => {
    const err = (status: number, code?: string) => new ApiError(status, "raw", code ? { error: "raw", code } : { error: "raw" });
    expect(managerError(err(409, "default_manager"))).toBe("这是默认助手，请先将其他助手设为默认后再删除。");
    expect(managerError(err(409, "builtin_manager"))).toBe("此助手由 hub 自身运行，无法删除或更换安装位置。");
    expect(managerError(err(404, "no_manager"))).toBe("该助手已不存在。");
    expect(managerError(err(409, "installing"))).toBe("该助手尚未完成安装，安装完成后才能重命名或设为默认。");
    expect(managerError(err(400))).toBe("名称长度需为 1 到 40 个字符。");
    expect(managerError(err(403))).toBe("当前设备没有修改权限。");
    // anything else is the shared explanation (#3849: a next step, the status and the hub's own words kept)
    expect(managerError(err(500))).toBe(errorText(err(500)));
    expect(managerError(err(500))).toContain("raw");
    expect(unsupported(err(404))).toBe(true);
    expect(unsupported(err(404, "no_manager"))).toBe(false);
  });
});

describe("hostedManagersForMachine and version lag", () => {
  const roster = (managers: Manager[], def = "assistant"): ManagerList => ({ default: def, managers });

  it("matches by machine_uid first and does not fall back to host when distinct UIDs disagree", () => {
    const list = roster([
      m({ id: "m-1", name: "Work", online: true, machine_uid: "UID-1", host: "other-host" }),
      m({ id: "m-2", name: "Conflict", online: true, machine_uid: "UID-2", host: "dev-box" }),
      m({ id: "assistant", name: "Default", online: true, machine_uid: "uid-1", host: "dev-box" }),
      m({ id: "m-inst", name: "Installing", online: false, machine_uid: "uid-1", installing: "2099-01-01T00:00:00Z" }),
    ]);
    const matched = hostedManagersForMachine(list, { id: "dev", hostname: "dev-box", machine_uid: "UID-1" });
    expect(matched.map((x) => x.id)).toEqual(["assistant", "m-1"]);
  });

  it("falls back to case-insensitive host match when either side lacks machine_uid", () => {
    const list = roster([
      m({ id: "m-old", name: "Legacy", online: true, host: "Dev-Box" }),
      m({ id: "m-new", name: "New", online: true, machine_uid: "uid-a", host: "DEV-BOX" }),
      m({ id: "m-other", name: "Other", online: true, host: "nas" }),
      m({ id: "m-empty", name: "NoHost", online: true }),
    ], "m-old");
    // Machine has UID, legacy manager does not -> host fallback matches m-old
    expect(hostedManagersForMachine(list, { id: "dev", hostname: "dev-box", machine_uid: "uid-b" }).map((x) => x.id)).toEqual(["m-old"]);
    // Machine lacks UID, manager has UID -> host fallback matches both
    expect(hostedManagersForMachine(list, { id: "dev", hostname: "dev-box" }).map((x) => x.id)).toEqual(["m-old", "m-new"]);
    // Machine lacks hostname -> falls back to machine.id
    expect(hostedManagersForMachine(list, { id: "NAS" }).map((x) => x.id)).toEqual(["m-other"]);
    // Machine has hostname -> does not false-match machine.id against another manager's host
    expect(hostedManagersForMachine(list, { id: "nas", hostname: "workstation" })).toEqual([]);
    // Null roster
    expect(hostedManagersForMachine(null, { id: "dev", hostname: "dev-box" })).toEqual([]);
  });

  it("detects version lag and prioritizes pending_restart", () => {
    expect(managerVersionLag({ online: true, version: "v1-202611201000" }, "v1-202611241200")).toBe(true);
    expect(managerVersionLag({ online: true, version: "v1-202611241200" }, "v1-202611241200")).toBe(false);
    expect(managerVersionLag({ online: true, version: "v1-202611251200" }, "v1-202611241200")).toBe(false);
    expect(managerVersionLag({ online: true, version: "v0.9.0" }, "v0.10.0")).toBe(true);
    expect(managerVersionLag({ online: true, version: "v0.10.0" }, "v0.9.0")).toBe(false);
    expect(managerVersionLag({ online: true, version: "sha-old" }, "sha-new")).toBe(true);
    expect(managerVersionLag({ online: true, version: "dev" }, "v1-202611241200")).toBe(false);
    expect(managerVersionLag({ online: true, version: "v1-202611201000" }, "unknown")).toBe(false);
    expect(managerVersionLag({ online: true, version: undefined }, "v1-202611241200")).toBe(false);
    expect(managerVersionLag({ online: false, version: "v1-202611201000" }, "v1-202611241200")).toBe(false);

    expect(hostedManagerAttention({ online: true, version: "v1-202611201000", pending_restart: true }, "v1-202611241200")).toBe("pending_restart");
    expect(hostedManagerAttention({ online: true, version: "v1-202611241200", pending_restart: true }, "v1-202611241200")).toBe("pending_restart");
    expect(hostedManagerAttention({ online: true, version: "v1-202611201000" }, "v1-202611241200")).toBe("version_lag");
    expect(hostedManagerAttention({ online: true, version: "v1-202611241200" }, "v1-202611241200")).toBeNull();
  });
});
