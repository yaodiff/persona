import { describe, expect, it } from "vitest";
import { computerView, computerWatching, GRANT_HINT, missingPermissions, screenPath, showsScreen, type ComputerView, type MachineComputer } from "./computer";

const base: MachineComputer = { enabled: true, installed: true, version: "0.38.0", daemon: true, accessibility: "granted", screen_recording: "granted", desktop: "active", ready: true, screen_sharing: true };

describe("computer section", () => {
  it("shows nothing below the switch while off, and offline first", () => {
    expect(computerView({ enabled: false }, true)).toEqual({ kind: "off" });
    expect(computerView(base, false)).toEqual({ kind: "offline" });
  });

  it("is ready with the version", () => {
    expect(computerView(base, true)).toEqual({ kind: "ready", text: "已就绪，可以操控 · Cua Driver 0.38.0" });
    expect(computerView({ ...base, backend: "lcu", version: "0.13.5" }, true)).toEqual({ kind: "ready", text: "已就绪，可以操控 · LCU 0.13.5" });
    expect(computerView({ enabled: true, backend: "lcu", installed: false }, true)).toEqual({ kind: "install", text: "该 Mac 尚未安装 LCU", retry: false });
  });

  it("offers the install, and says why one failed", () => {
    expect(computerView({ enabled: true, installed: false }, true)).toEqual({ kind: "install", text: "该 Mac 尚未安装 Cua Driver", retry: false });
    const failed = computerView({ enabled: true, installed: false, job: { kind: "install", state: "failed", error: "校验没过" } }, true);
    expect(failed).toEqual({ kind: "install", text: "安装失败：校验没过", retry: true });
  });

  it("shows a running job's step, cancel only for an install", () => {
    expect(computerView({ enabled: true, installed: false, job: { kind: "install", state: "running", step: "下载安装器" } }, true)).toEqual({ kind: "running", text: "下载安装器", cancel: true });
    const grant = computerView({ ...base, ready: false, accessibility: "missing", job: { kind: "grant", state: "running" } }, true);
    expect(grant).toEqual({ kind: "running", text: GRANT_HINT, cancel: false });
  });

  it("asks for the missing permissions, then waits for 允许 on the Mac", () => {
    const c: MachineComputer = { ...base, ready: false, accessibility: "missing", screen_recording: "missing" };
    expect(missingPermissions(c)).toEqual(["辅助功能", "屏幕录制"]);
    expect(computerView(c, true)).toEqual({ kind: "grant", text: "Cua Driver 尚缺少辅助功能、屏幕录制权限" });
    expect(computerView({ ...c, job: { kind: "grant", state: "failed", error: "没有弹窗" } }, true)).toEqual({ kind: "grant", text: "没有弹窗" });
    const done = { ...c, job: { kind: "grant", state: "done" } };
    expect(computerView(done, true)).toEqual({ kind: "granted" });
    expect(computerWatching(done, true)).toBe(true);
  });

  it("falls back to the hub's note", () => {
    expect(computerView({ ...base, ready: false, desktop: "locked", note: "这台 Mac 锁屏了，解锁后才能操控" }, true)).toEqual({ kind: "unknown", text: "这台 Mac 锁屏了，解锁后才能操控" });
  });

  it("watches only while something moves", () => {
    expect(computerWatching(base, true)).toBe(false);
    expect(computerWatching({ ...base, job: { kind: "install", state: "running" } }, true)).toBe(true);
    expect(computerWatching({ ...base, job: { kind: "install", state: "running" } }, false)).toBe(false);
    expect(computerWatching({ ...base, job: { kind: "grant", state: "done" } }, true)).toBe(false);
  });

  it("links the live screen", () => {
    expect(screenPath("mac 1")).toBe("/screen/mac%201");
  });
});

describe("live screen entry", () => {
  // Screen Sharing answers while Cua Driver is anywhere short of ready: the screen goes through the hub's port 5900 relay,
  // not Cua Driver, so each still offers it. The section's state is checked too, so a case cannot drift into another.
  const notReady: [ComputerView["kind"], MachineComputer][] = [
    ["unknown", { ...base, daemon: false, ready: false, accessibility: "unknown", screen_recording: "unknown", note: "Cua Driver 没在运行，助手要用时会自动打开它" }],
    ["install", { enabled: true, installed: false, screen_sharing: true }],
    ["grant", { ...base, ready: false, accessibility: "missing" }],
    ["running", { enabled: true, installed: false, screen_sharing: true, job: { kind: "install", state: "running" } }],
    ["granted", { ...base, ready: false, accessibility: "missing", job: { kind: "grant", state: "done" } }],
  ];
  it.each(notReady)("offers it while the section is %s", (kind, c) => {
    expect(computerView(c, true).kind).toBe(kind);
    expect(showsScreen({ online: true, computer: c })).toBe(true);
  });

  it("offers it when ready, and not once Screen Sharing is off", () => {
    expect(showsScreen({ online: true, computer: base })).toBe(true);
    expect(showsScreen({ online: true, computer: { ...base, screen_sharing: false } })).toBe(false);
    expect(showsScreen({ online: true, computer: { ...notReady[0]![1], screen_sharing: false } })).toBe(false);
  });

  it("needs the Mac online", () => {
    expect(showsScreen({ online: false, computer: base })).toBe(false);
  });

  it("needs cua or ego on for the Mac, the hub's condition for relaying port 5900", () => {
    const off = { ...base, enabled: false };
    expect(showsScreen({ online: true })).toBe(false);
    expect(showsScreen({ online: true, computer: off })).toBe(false);
    expect(showsScreen({ online: true, computer: off, browser: { enabled: false, screen_sharing: true } })).toBe(false);
    // Computer control off, ego on: the screen is still there
    expect(showsScreen({ online: true, computer: off, browser: { enabled: true, screen_sharing: true } })).toBe(true);
    expect(showsScreen({ online: true, browser: { enabled: true, screen_sharing: true } })).toBe(true);
  });

  it("reads cua's answer first when both are on, as the screen page does before it connects", () => {
    expect(showsScreen({ online: true, computer: { ...base, screen_sharing: false }, browser: { enabled: true, screen_sharing: true } })).toBe(false);
    expect(showsScreen({ online: true, computer: base, browser: { enabled: true, screen_sharing: false } })).toBe(true);
  });
});
