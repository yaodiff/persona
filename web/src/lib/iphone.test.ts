import { describe, expect, it } from "vitest";
import { iphonePollEvery, iphoneRow, iphoneView, newerIPhone, type IPhoneDevice, type MachineIPhone } from "./iphone";

// "iPhone control" on a computer's page (#8217): each state from the hub's codes, in the iPhone app's words. Made-up values.
const cable: IPhoneDevice = { id: "dev-1", name: "我的 iPhone", ios: "27.2", paired: false, usb: true, connected: false, locked: false, ready: false, code: "usb_unpaired" };
const paired: IPhoneDevice = { ...cable, paired: true, connected: true, ready: true, code: undefined, transport: "network", developer_mode: true };
const on = (more: Partial<MachineIPhone> = {}): MachineIPhone => ({ enabled: true, installed: true, ddi_present: true, devices: [], ...more });

describe("iPhone control", () => {
  it("shows nothing below the switch while off, and offline first", () => {
    expect(iphoneView({ enabled: false }, true, true)).toEqual({ kind: "off" });
    expect(iphoneView(on(), false, true)).toEqual({ kind: "offline" });
  });

  it("says a Linux computer can't yet, before anything to install", () => {
    expect(iphoneView(on({ installed: false, ddi_present: false }), true, false)).toEqual({ kind: "notMac" });
    expect(iphoneView(on({ installed: false, ddi_present: undefined }), true, false).kind, "an older node that doesn't say").toBe("notMac");
    expect(iphoneView(on({ ddi_present: true }), true, false).kind, "a developer disk image put there by hand").toBe("devices");
  });

  it("offers the helper's install, its progress at once, and why one failed", () => {
    expect(iphoneView(on({ installed: false, code: "bridge_missing" }), true, true)).toEqual({ kind: "install", text: "该电脑尚未安装 iPhone 助手程序", retry: false });
    expect(iphoneView(on({ installed: false }), true, true, { action: "install" })).toEqual({ kind: "installing", text: "正在安装 iPhone 助手程序…" });
    expect(iphoneView(on({ installed: false, job: { kind: "install", state: "running", step: "正在校验" } }), true, true)).toEqual({ kind: "installing", text: "正在校验" });
    const failed = (code: string) => iphoneView(on({ installed: false, job: { kind: "install", state: "failed", code, error: "x" } }), true, true);
    expect(failed("download_failed")).toEqual({ kind: "install", text: "无法下载 iPhone 助手程序。请检查该电脑的网络连接后重试。", retry: true });
    expect(failed("checksum_mismatch")).toMatchObject({ text: "已下载的 iPhone 助手程序未通过校验，未执行安装。请重试。" });
    expect(failed("install_failed")).toMatchObject({ text: "iPhone 助手程序安装失败：x" });
  });

  it("says a Mac without Xcode needs it, and still pairs", () => {
    const v = iphoneView(on({ ddi_present: false, devices: [cable] }), true, true);
    expect(v).toMatchObject({ kind: "devices", noXcode: true });
    expect(v.kind === "devices" && v.rows[0]!.action).toBe("pair");
    expect(iphoneView(on({ ddi_present: undefined }), true, true)).toMatchObject({ noXcode: false });
  });

  it("walks an iPhone from the cable to ready", () => {
    expect(iphoneRow(cable, undefined, false)).toMatchObject({ text: "已通过数据线连接，尚未配对", tone: "need", action: "pair", forget: false });
    const pairing = { kind: "pair", state: "running", code: "trust_pending", device: "dev-1" };
    expect(iphoneRow(cable, pairing, false)).toMatchObject({ text: "请在这台 iPhone 上轻点「信任」，然后输入锁屏密码", tone: "work", action: "cancel" });
    expect(iphoneRow(cable, { ...pairing, code: "locked" }, false).text).toBe("请解锁这台 iPhone 以继续配对");
    expect(iphoneRow(cable, { kind: "pair", state: "running" }, false).text, "the moment Pair is clicked").toBe("正在与此 iPhone 配对…");
    expect(iphoneRow({ ...paired, ready: false, developer_mode: false, code: "developer_mode_off" }, undefined, false)).toMatchObject({ tone: "need", forget: true });
    const unprepared = { ...paired, ready: false, code: "ddi_not_mounted" };
    expect(iphoneRow(unprepared, undefined, false)).toMatchObject({ text: "尚未就绪", action: "prepare", prepare: true });
    const noXcode = iphoneRow(unprepared, undefined, true);
    expect([noXcode.text, noXcode.action]).toEqual(["尚未就绪：该 Mac 需先安装 Xcode", undefined]);
    const again = iphoneRow({ ...unprepared, auto_prepare: true }, undefined, false);
    expect([again.tone, again.action]).toEqual(["work", undefined]);
    expect(iphoneRow(unprepared, { kind: "prepare", state: "running", device: "dev-1" }, false)).toMatchObject({ text: "正在配置此 iPhone，预计耗时约 1 分钟", action: "cancel" });
    expect(iphoneRow({ ...paired, locked: true, ready: false }, undefined, false).text).toBe("设备已锁屏，解锁后助手方可操控");
    expect(iphoneRow({ ...paired, allowed: false }, undefined, false)).toMatchObject({ text: "等待此 iPhone 授权", tone: "need" });
    expect(iphoneRow({ ...paired, allowed: true }, undefined, false)).toMatchObject({ text: "助手可以操控 · 已通过 Wi‑Fi 连接", tone: "ok", forget: true });
  });

  it("only one thing runs: no Pair or Prepare beside another iPhone's job", () => {
    const other = { kind: "prepare", state: "running", device: "dev-2" };
    expect(iphoneRow(cable, other, false).action).toBeUndefined();
    expect(iphoneRow({ ...paired, ready: false, code: "ddi_not_mounted" }, other, false).action).toBeUndefined();
  });

  it("points to the iPhone for its consent, and says why a job failed", () => {
    const v = iphoneView(on({ devices: [{ ...paired, allowed: false }], job: { kind: "prepare", state: "failed", code: "ddi_missing", device: "dev-1" } }), true, true);
    expect(v).toMatchObject({ kind: "devices", consentHint: true, failed: "该 Mac 需先安装 Xcode。请前往 App Store 安装完成后重试。" });
    const timeout = iphoneView(on({ devices: [cable], job: { kind: "pair", state: "failed", code: "timeout", device: "dev-1" } }), true, true);
    expect(timeout).toMatchObject({ failed: "未在规定时间内在 iPhone 上轻点「信任」。请点击「配对」重试。" });
    expect(iphoneView(on({ note: "这台电脑的节点太旧" }), true, true)).toMatchObject({ note: "这台电脑的节点太旧" });
    expect(iphoneView(on({ note: "没配对", code: "none_paired" }), true, true)).toMatchObject({ note: undefined, rows: [] });
  });

  it("asks again while a job runs or the next step is at the iPhone", () => {
    expect(iphonePollEvery(on({ job: { kind: "pair", state: "running" } }), true, true)).toBe(2000);
    expect(iphonePollEvery(on({ installed: false, job: { kind: "install", state: "running" } }), true, true)).toBe(2000);
    expect(iphonePollEvery(on(), true, true), "waiting for the cable").toBe(5000);
    expect(iphonePollEvery(on({ devices: [{ ...paired, allowed: false }] }), true, true)).toBe(5000);
    expect(iphonePollEvery(on({ devices: [{ ...paired, allowed: true }] }), true, true)).toBe(0);
    expect(iphonePollEvery(on({ devices: [cable] }), true, true), "Pair is the person's click here").toBe(0);
    expect(iphonePollEvery(on({ installed: false }), true, true)).toBe(0);
    expect(iphonePollEvery(on(), false, true)).toBe(0);
  });

  it("shows the newer of the page's read and the machine list's", () => {
    const listed = on({ checked: "2026-11-15T10:00:05Z" });
    expect(newerIPhone(null, listed)).toBe(listed);
    expect(newerIPhone(on({ checked: "2026-11-15T10:00:00Z" }), listed)).toBe(listed);
    const fresh = on({ checked: "2026-11-15T10:00:09Z" });
    expect(newerIPhone(fresh, listed)).toBe(fresh);
    const unchecked = on();
    expect(newerIPhone(unchecked, listed), "a write's answer without a time").toBe(unchecked);
  });
});
