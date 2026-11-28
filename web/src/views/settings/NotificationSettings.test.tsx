// @vitest-environment jsdom
// Settings › Notifications (#2971): loading, a load that fails and "Retry", a save the hub refuses (the control goes back, a toast says
// why), the system switch when the browser has said no or has no notifications at all, and "Alert sound" on this page (#4057).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";

type Pending = { method: string; json?: unknown; resolve: (v: unknown) => void; reject: (e: unknown) => void };
const pending: Pending[] = [];
// The hub-wide push grading switch at the end of the page reads the legacy route (#8157, LowDistractionSettings.test.tsx):
// kept apart, so pending is this device's prefs as before.
const lnMode: Pending[] = [];
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) =>
    new Promise((resolve, reject) => (path === "/push/ln-mode" ? lnMode : pending).push({ method: init?.method ?? "GET", json: init?.json, resolve, reject })),
  enc: encodeURIComponent,
  errorText: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));
const toast = vi.fn();
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));

const { NotificationSettings } = await import("./NotificationSettings");
const { setToken } = await import("../../lib/api");

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  pending.length = 0;
  lnMode.length = 0;
  toast.mockClear();
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setToken("");
  vi.unstubAllGlobals();
});

const render = () => act(async () => root.render(<NotificationSettings />));
const switches = () => [...host.querySelectorAll<HTMLButtonElement>('[role="switch"]')];
const named = (label: string) => switches().find((b) => b.getAttribute("aria-label") === label);
const inputSwitch = () => named("需要你处理时")!;
const answer = async (i: number, v: unknown) => act(async () => pending[i].resolve(v));
const fail = async (i: number, e: Error) => act(async () => pending[i].reject(e));

describe("设置 › 通知", () => {
  it("先转圈，hub 答了再显示两项设置", async () => {
    vi.stubGlobal("Notification", { permission: "granted", requestPermission: async () => "granted" });
    await render();
    expect(named("需要你处理时")).toBeUndefined();
    await answer(0, { input: false, done: "always" });
    expect(named("需要你处理时")).toBeDefined();
    expect(inputSwitch().getAttribute("aria-checked")).toBe("false");
    expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("每次都通知");
  });

  it("读不到时说为什么，点重试再读", async () => {
    await render();
    await fail(0, new Error("hub 没回答"));
    expect(host.textContent).toContain("无法加载通知设置");
    expect(host.textContent).toContain("hub 没回答");
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === "重试")!;
    await act(async () => retry.click());
    expect(pending).toHaveLength(2);
    await answer(1, { input: true, done: "long" });
    expect(named("需要你处理时")).toBeDefined();
    expect(host.textContent).not.toContain("无法加载通知设置");
  });

  it("hub 不让保存：开关回到原来的样子，并提示保存失败", async () => {
    await render();
    await answer(0, { input: true, done: "long" });
    await act(async () => inputSwitch().click());
    expect(inputSwitch().getAttribute("aria-checked")).toBe("false"); // optimistic
    expect(pending[1].method).toBe("PUT");
    await fail(1, new Error("500"));
    expect(inputSwitch().getAttribute("aria-checked")).toBe("true");
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("保存失败"), "error");
  });

  it("does not show a failed-save toast from a retired login", async () => {
    setToken("test-token-a");
    await render();
    await answer(0, { input: true, done: "long" });
    await act(async () => inputSwitch().click());
    await act(async () => setToken("test-token-b"));
    expect(toast).not.toHaveBeenCalled();
    expect(inputSwitch()).toBeUndefined();
    await answer(2, { input: true, done: "off" });
    await answer(1, { input: false, done: "long" });
    expect(toast).not.toHaveBeenCalled();
    expect(inputSwitch().getAttribute("aria-checked")).toBe("true");
  });

  it("浏览器拒绝了：开关关着、点不了，并说去哪改", async () => {
    vi.stubGlobal("Notification", { permission: "denied", requestPermission: async () => "denied" });
    localStorage.setItem("persona.systemNotify", "1");
    await render();
    const sys = named("弹出系统通知")!;
    expect(sys.disabled).toBe(true);
    expect(sys.getAttribute("aria-checked")).toBe("false");
    expect(host.textContent).toContain("已拒绝");
    expect(host.textContent).toContain("网站设置");
  });

  it("浏览器不支持：开关点不了，并说明", async () => {
    vi.stubGlobal("Notification", undefined);
    await render();
    expect(named("弹出系统通知")!.disabled).toBe(true);
    expect(host.textContent).toContain("当前浏览器不支持系统通知");
  });

  it("提示音在这一页：hub 还没答就能开关，存在这个浏览器，不发请求", async () => {
    await render();
    const sound = () => named("提示音")!;
    expect(sound().getAttribute("aria-checked")).toBe("false");
    await act(async () => sound().click());
    expect(sound().getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem("persona.attentionSound")).toBe("1");
    expect(pending).toHaveLength(1); // only the prefs load: the sound is not the hub's
    await act(async () => sound().click());
    expect(localStorage.getItem("persona.attentionSound")).toBeNull();
  });

  it("读不到 hub 的设置时提示音照样能开关", async () => {
    localStorage.setItem("persona.attentionSound", "1");
    await render();
    await fail(0, new Error("hub 没回答"));
    expect(named("提示音")!.getAttribute("aria-checked")).toBe("true");
    await act(async () => named("提示音")!.click());
    expect(named("提示音")!.getAttribute("aria-checked")).toBe("false");
    expect(localStorage.getItem("persona.attentionSound")).toBeNull();
  });

  it("免打扰时段：打开后显示起止时间，存的时候带上这个浏览器的时区（#5806）", async () => {
    await render();
    await answer(0, { input: true, done: "long", quiet: { enabled: false, start: "", end: "" } });
    const quiet = () => named("免打扰时段")!;
    expect(quiet().getAttribute("aria-checked")).toBe("false");
    expect(host.querySelector('input[type="time"]')).toBeNull();
    await act(async () => quiet().click());
    expect(quiet().getAttribute("aria-checked")).toBe("true");
    const sent = pending[1];
    expect(sent.method).toBe("PUT");
    expect(sent.json).toMatchObject({ quiet: { enabled: true, start: "23:00", end: "07:00", tz: Intl.DateTimeFormat().resolvedOptions().timeZone } });
    await answer(1, { input: true, done: "long", quiet: { enabled: true, start: "23:00", end: "07:00" } });
    const times = [...host.querySelectorAll<HTMLInputElement>('input[type="time"]')].map((i) => i.value);
    expect(times).toEqual(["23:00", "07:00"]);
    expect(host.textContent).toContain(`${new Intl.DateTimeFormat("zh-Hans", { timeStyle: "short" }).format(new Date(2000, 0, 1, 23, 0))} – `);
  });

  describe("Always show system notifications (#2451)", () => {
    const always = () => named("总是弹出系统通知")!;

    it("is off and disabled until system notifications are on", async () => {
      vi.stubGlobal("Notification", { permission: "default", requestPermission: async () => "granted" });
      localStorage.setItem("persona.systemNotifyAlways", "1");
      await render();
      expect(always().disabled).toBe(true);
      expect(always().getAttribute("aria-checked")).toBe("false");
      expect(host.textContent).toContain("请先开启系统通知");
    });

    it("turns on and off once they are, kept in this browser, and the texts say so", async () => {
      vi.stubGlobal("Notification", { permission: "granted", requestPermission: async () => "granted" });
      localStorage.setItem("persona.systemNotify", "1");
      await render();
      expect(always().disabled).toBe(false);
      expect(host.textContent).toContain("网页不在前台时弹出");
      await act(async () => always().click());
      expect(always().getAttribute("aria-checked")).toBe("true");
      expect(localStorage.getItem("persona.systemNotifyAlways")).toBe("1");
      expect(host.textContent).toContain("网页在前台时也弹出");
      expect(host.textContent).toContain("即使 Persona 标签页在前台也不例外；无论打开多少个标签页，都只弹出一次");
      expect(pending).toHaveLength(1); // only the prefs load: this switch is not the hub's
      await act(async () => always().click());
      expect(localStorage.getItem("persona.systemNotifyAlways")).toBeNull();
    });

    it("turning system notifications off turns it off with them, and back on brings the choice back", async () => {
      vi.stubGlobal("Notification", { permission: "granted", requestPermission: async () => "granted" });
      localStorage.setItem("persona.systemNotify", "1");
      localStorage.setItem("persona.systemNotifyAlways", "1");
      await render();
      expect(always().getAttribute("aria-checked")).toBe("true");
      await act(async () => named("弹出系统通知")!.click());
      expect(always().getAttribute("aria-checked")).toBe("false");
      expect(always().disabled).toBe(true);
      await act(async () => named("弹出系统通知")!.click());
      expect(always().getAttribute("aria-checked")).toBe("true");
    });
  });
});

describe("Settings › Notifications › Push grading", () => {
  const pushGrading = () => named(t("settings-notifications.low_distraction"));

  it("comes after the browser-only settings and reflects the hub's answer", async () => {
    await render();
    expect(pushGrading()).toBeUndefined();
    expect(lnMode).toHaveLength(1);
    expect(lnMode[0].method).toBe("GET");
    await act(async () => lnMode[0].resolve({ enabled: true }));
    expect(pushGrading()?.getAttribute("aria-checked")).toBe("true");
    const text = host.textContent ?? "";
    const deviceNote = text.indexOf(t("settings-notifications.device_note"));
    expect(deviceNote).toBeGreaterThan(-1);
    expect(text.indexOf(t("settings-notifications.low_distraction_footer"))).toBeGreaterThan(deviceNote);
  });

  it.each([404, 405])("explains unsupported push grading on an older hub (HTTP %s)", async (status) => {
    await render();
    await act(async () => lnMode[0].reject(Object.assign(new Error("not supported"), { status })));
    expect(pushGrading()).toBeUndefined();
    expect(host.textContent).toContain(t("settings-notifications.low_distraction_unsupported"));
  });
});
