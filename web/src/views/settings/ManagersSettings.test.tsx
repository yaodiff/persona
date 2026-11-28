// @vitest-environment jsdom
// Settings › Managers (docs/assistant/managers.md): the roster with where each runs and its status, a hub without the roster, the
// `managers` push reloading the list, New manager → the install code (with the commands when the hub gives them), and
// Delete off for the default with why, confirmed for the rest, and the hub's refusals in plain words.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Call = { path: string; method: string; json?: unknown; resolve: (v: unknown) => void; reject: (e: unknown) => void };
const calls: Call[] = [];
// this device's prefs (the Notifications switch): answered at once, apart from the roster's calls
const prefsCalls: { method: string; json?: unknown }[] = [];
let prefsAnswer: (json: unknown) => unknown = () => ({ input: true, done: "long", muted_managers: [] });
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    if (path.startsWith("/devices/")) {
      prefsCalls.push({ method: init?.method ?? "GET", json: init?.json });
      try {
        return Promise.resolve(prefsAnswer(init?.json));
      } catch (e) {
        return Promise.reject(e);
      }
    }
    return new Promise((resolve, reject) => calls.push({ path, method: init?.method ?? "GET", json: init?.json, resolve, reject }));
  },
}));
const listeners = new Map<string, (m: unknown) => void>();
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: unknown) => void) => (listeners.set(type, l), () => listeners.delete(type)),
}));
const toast = vi.fn();
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: (...args: unknown[]) => toast(...args) }));

const { ApiError, setToken } = await import("../../lib/api");
const { initI18n } = await import("../../i18n");
const { ManagersSettings } = await import("./ManagersSettings");

const soon = (min: number) => new Date(Date.now() + min * 60_000).toISOString();
const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, default: true, builtin: true, host: "server", os: "linux", deploy: "child" },
    { id: "m-1", name: "工作", online: true, host: "MacBook", os: "darwin", deploy: "service" },
    { id: "m-2", name: "家里", online: false, host: "NAS", os: "linux", deploy: "docker", last_seen: new Date(Date.now() - 3 * 3600_000).toISOString() },
    { id: "m-3", name: "测试", online: false, installing: soon(7.2) },
  ],
};

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  calls.length = 0;
  prefsCalls.length = 0;
  toast.mockClear();
  prefsAnswer = () => ({ input: true, done: "long", muted_managers: [] });
  listeners.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  setToken("");
  document.body.innerHTML = "";
  await initI18n("zh-Hans");
});

const render = () => act(async () => root.render(<ManagersSettings />));
const answer = (i: number, v: unknown) => act(async () => calls[i].resolve(v));
const refuse = (i: number, e: unknown) => act(async () => calls[i].reject(e));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (el: Element | undefined) => act(async () => (el as HTMLElement).click());
const row = (name: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(name) && b.textContent.includes("·"));
const type = (value: string) =>
  act(async () => {
    const input = document.body.querySelector("input")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("设置 › 助手", () => {
  it("列出每个助手：默认、在哪、在线 / 最后同步 / 安装中", async () => {
    await render();
    await answer(0, ROSTER);
    expect(row("助手")?.textContent).toContain("默认");
    expect(row("助手")?.textContent).toContain("在线 · server · Linux · hub 内置");
    expect(row("工作")?.textContent).toContain("MacBook · macOS · 服务");
    expect(row("家里")?.textContent).toMatch(/离线 · 最后同步于 3\s?小时前 · NAS · Linux · 容器/);
    expect(row("测试")?.textContent).toContain("安装中 · 安装码 7 分钟后过期");
  });

  it("没有名册的旧 hub：说升级 hub", async () => {
    await render();
    await refuse(0, new ApiError(404, "not found", { error: "not found" }));
    expect(text()).toContain("当前 hub 版本暂不支持助手");
  });

  it("名册变了（managers 推送）就重读", async () => {
    await render();
    await answer(0, ROSTER);
    await act(async () => listeners.get("managers")!({ type: "managers" }));
    expect(calls[1].path).toBe("/managers");
    await answer(1, { ...ROSTER, managers: [ROSTER.managers[0], { ...ROSTER.managers[3], online: true, installing: undefined, host: "PC" }] });
    expect(row("测试")?.textContent).toContain("在线 · PC");
    expect(row("家里")).toBeUndefined();
  });

  it("leaves old hubs and a single manager without a bot warning", async () => {
    await render();
    await answer(0, ROSTER);
    expect(host.querySelector('[role="alert"]')).toBeNull();
    await act(async () => listeners.get("managers")!({ type: "managers" }));
    await answer(1, { ...ROSTER, managers: [ROSTER.managers[0]], bot_conflicts: [] });
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it.each([
    ["zh-Hans", "多个助手共用机器人凭据", "助手和工作 共用同一个飞书机器人 App ID。", "工作和家里 共用同一个微信账号。", "包含离线助手", "请为每个助手配置独立的机器人或微信账号"],
    ["en", "Shared bot credentials", "助手 and 工作 use the same Lark bot app ID.", "工作 and 家里 use the same WeChat account.", "including offline assistants", "Configure a separate bot or WeChat account for each assistant"],
    ["ja", "ボットの認証情報が共有されています", "助手、工作が同じLarkボットのApp IDを使用しています。", "工作、家里が同じWeChatアカウントを使用しています。", "オフラインのアシスタントを含む", "アシスタントごとに別のボットまたはWeChatアカウントを設定してください"],
  ])("shows shared bot warnings in %s with manager names and guidance", async (lang, title, feishu, wechat, offline, guidance) => {
    await initI18n(lang);
    await render();
    await answer(0, {
      ...ROSTER,
      bot_conflicts: [
        { platform: "feishu", managers: ["assistant", "m-1"] },
        { platform: "wechat", managers: ["m-1", "m-2"] },
      ],
    });
    const alert = host.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain(title);
    expect(alert?.textContent).toContain(feishu);
    expect(alert?.textContent).toContain(wechat);
    expect(alert?.textContent).toContain(offline);
    expect(alert?.textContent).toContain(guidance);
    expect(alert?.textContent).not.toContain("m-1");
  });

  it("updates names and clears conflicts after a managers event", async () => {
    await render();
    const bot_conflicts = [{ platform: "feishu", managers: ["assistant", "m-1"] }];
    await answer(0, { ...ROSTER, bot_conflicts });
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("助手和工作");
    await act(async () => listeners.get("managers")!({ type: "managers" }));
    await answer(1, { ...ROSTER, bot_conflicts, managers: ROSTER.managers.map((m) => m.id === "m-1" ? { ...m, name: "新的工作" } : m) });
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("助手和新的工作");
    await act(async () => listeners.get("managers")!({ type: "managers" }));
    await answer(2, { ...ROSTER, bot_conflicts: [] });
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("新建：起名字 → 安装码和命令，能复制", async () => {
    await render();
    await answer(0, ROSTER);
    await click(button("新建助手"));
    expect(button("创建")?.disabled).toBe(true);
    await type("  书房 ");
    await click(button("创建"));
    expect(calls[1]).toMatchObject({ path: "/managers", method: "POST", json: { name: "书房" } });
    await answer(1, {
      manager: { id: "m-9", name: "书房", online: false, installing: soon(10) },
      code: "mi_abc",
      expires_at: soon(10),
      commands: { service: "curl -fsSL <hub 地址>/i/mi_abc | sh", docker: "docker run … mi_abc" },
    });
    expect(text()).toContain("安装「书房」");
    expect(text()).toContain("mi_abc");
    expect(text()).toContain("作为服务安装");
    expect(text()).toContain("使用 Docker");
    expect(text()).toMatch(/\d:\d\d 后过期/);
    expect(calls[2].path).toBe("/managers"); // the list again
  });

  it("没有命令的 hub：只给码和一句说明", async () => {
    await render();
    await answer(0, ROSTER);
    await click(button("新建助手"));
    await type("书房");
    await click(button("创建"));
    await answer(1, { manager: { id: "m-9", name: "书房", online: false }, code: "mi_abc", expires_at: soon(10) });
    expect(text()).not.toContain("作为服务安装");
    expect(text()).toContain("使用此安装码安装助手");
  });

  it("默认的不能删，说为什么；别的确认后删，hub 拒绝时说大白话", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("助手"));
    expect(button("删除")?.disabled).toBe(true);
    expect(text()).toContain("无法删除由 hub 自身运行的助手");
    expect(button("更换安装位置")).toBeUndefined();
    await click(button("完成"));

    await click(row("工作"));
    expect(button("删除")?.disabled).toBe(false);
    await click(button("删除"));
    expect(text()).toContain("确定删除「工作」？其 token 将立即失效");
    await click(button("删除"));
    expect(calls[1]).toMatchObject({ path: "/managers/m-1", method: "DELETE" });
    await refuse(1, new ApiError(409, "make another manager the default first", { error: "x", code: "default_manager" }));
    expect(text()).toContain("这是默认助手，请先将其他助手设为默认后再删除");
  });

  it("换个地方装先警告，再给新码", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("工作"));
    await click(button("更换安装位置"));
    expect(text()).toContain("原位置的助手会立即停止运行");
    await click(button("更换安装位置"));
    expect(calls[1]).toMatchObject({ path: "/managers/m-1/code", method: "POST" });
    await answer(1, { code: "mi_new", expires_at: soon(10) });
    expect(text()).toContain("安装「工作」");
    expect(text()).toContain("mi_new");
  });

  it("安装中的：不能改名、设默认，按钮是「新安装码」「取消安装」", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("测试"));
    expect(button("重命名")).toBeUndefined();
    expect(button("设为默认")).toBeUndefined();
    expect(button("新安装码")).toBeDefined();
    await click(button("取消安装"));
    expect(text()).toContain("确定取消安装「测试」？当前的安装码将随即失效。");
  });

  it("makes a never-installed manager again under the same name when its expired code is gone from the hub (404 no_manager)", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("测试"));
    await click(button("新安装码"));
    expect(calls[1]).toMatchObject({ path: "/managers/m-3/code", method: "POST" });
    await refuse(1, new ApiError(404, "no manager", { error: "no manager", code: "no_manager" }));
    expect(calls[2]).toMatchObject({ path: "/managers", method: "POST", json: { name: "测试" } });
    await answer(2, { manager: { id: "m-4", name: "测试", online: false, installing: soon(10) }, code: "mi_new", expires_at: soon(10) });
    expect(text()).toContain("安装「测试」");
    expect(text()).toContain("mi_new");
    // the code dialog's own New install code now goes to the new one
    expect(calls.some((c) => c.path === "/managers" && c.method === "GET" && calls.indexOf(c) > 2)).toBe(true);
  });

  it("reports a 404 as usual when an installed manager is installed elsewhere, without making it again", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("工作"));
    await click(button("更换安装位置"));
    await click([...document.body.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "更换安装位置").at(-1));
    expect(calls[1]).toMatchObject({ path: "/managers/m-1/code", method: "POST" });
    await refuse(1, new ApiError(404, "no manager", { error: "no manager", code: "no_manager" }));
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
    expect(text()).toContain("该助手已不存在");
  });

  it("改名、设为默认", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("家里"));
    await click(button("设为默认"));
    expect(calls[1]).toMatchObject({ path: "/managers/m-2", method: "PATCH", json: { default: true } });
    await answer(1, { manager: {} });
    await click(button("重命名"));
    await type("家");
    await click(button("保存"));
    expect(calls.at(-1)).toMatchObject({ path: "/managers/m-2", method: "PATCH", json: { name: "家" } });
  });

  it("has a switcher switch on the built-in assistant only: off while it is the default, else it sends hide (#9055)", async () => {
    const sw = () => document.body.querySelector<HTMLButtonElement>('[role="switch"][aria-label="在切换器中显示"]');
    await render();
    await answer(0, ROSTER);
    await click(row("助手"));
    expect(sw()?.getAttribute("aria-checked")).toBe("true");
    expect(sw()?.disabled).toBe(true); // the default: the hub would refuse to hide it
    expect(text()).toContain("由于它是默认助手或名下已有电脑，切换器中仍会显示它");
    await click(button("完成"));
    await click(row("工作"));
    expect(sw()).toBeNull();
  });

  it("hides the built-in assistant from the switcher when it is not the default, and shows it again (#9055)", async () => {
    const sw = () => document.body.querySelector<HTMLButtonElement>('[role="switch"][aria-label="在切换器中显示"]');
    const other = { ...ROSTER, default: "m-1", managers: ROSTER.managers.map((m) => ({ ...m, default: m.id === "m-1" })) };
    await render();
    await answer(0, other);
    await click(row("助手"));
    expect(sw()?.disabled).toBe(false);
    expect(text()).toContain("关闭后，当此助手既非默认助手且名下没有电脑时，切换器中将不显示它");
    await click(sw()!);
    expect(calls.at(-1)).toMatchObject({ path: "/managers/assistant", method: "PATCH", json: { hide: true } });
  });

  it("has one push switch per manager: turning it off sends only muted_managers, and a refusal puts it back", async () => {
    await render();
    await answer(0, ROSTER);
    await click(row("工作"));
    const sw = () => document.body.querySelector<HTMLButtonElement>('[role="switch"][aria-label="推送通知"]');
    expect(sw()?.getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("关闭后，当前设备将不再接收此助手的推送通知");
    prefsAnswer = (json) => ({ input: true, done: "long", ...(json as object) });
    await click(sw()!);
    expect(prefsCalls.at(-1)).toEqual({ method: "PUT", json: { muted_managers: ["m-1"] } });
    expect(sw()?.getAttribute("aria-checked")).toBe("false");
    prefsAnswer = () => {
      throw new ApiError(500, "boom", { error: "boom" });
    };
    await click(sw()!);
    expect(sw()?.getAttribute("aria-checked")).toBe("false");
    expect(toast).toHaveBeenCalled();
  });

  it("does not show a failed-push toast from a retired login", async () => {
    setToken("test-token-a");
    await render();
    await answer(0, ROSTER);
    await click(row("工作"));
    let finish!: (value: unknown) => void;
    prefsAnswer = (json) => json ? new Promise((resolve) => { finish = resolve; }) : { input: true, done: "long", muted_managers: [] };
    await click(document.body.querySelector('[role="switch"][aria-label="推送通知"]')!);
    await act(async () => setToken("test-token-b"));
    expect(toast).not.toHaveBeenCalled();
    await act(async () => finish({ input: true, done: "long", muted_managers: ["m-1"] }));
    expect(toast).not.toHaveBeenCalled();
  });

  it("marks a manager that needs an update in the list and its details, which say to update it where it runs", async () => {
    await render();
    await answer(0, { ...ROSTER, managers: ROSTER.managers.map((m) => (m.id === "m-1" ? { ...m, needs_update: true } : m)) });
    expect(row("工作")?.textContent).toContain("需要更新");
    expect(row("助手")?.textContent).not.toContain("需要更新");
    await click(row("工作"));
    expect(text()).toContain("请在运行该助手的电脑上更新 personad");
    await click(button("完成"));
    await click(row("助手"));
    const dialog = [...document.body.querySelectorAll('[role="dialog"]')].at(-1);
    expect(dialog?.textContent).toContain("助手");
    expect(dialog?.textContent).not.toContain("需要更新");
  });

  it("shows no push switch with a hub whose prefs have no muted_managers", async () => {
    prefsAnswer = () => ({ input: true, done: "long" });
    await render();
    await answer(0, ROSTER);
    await click(row("工作"));
    expect(document.body.querySelector('[aria-label="推送通知"]')).toBeNull();
  });

  it("marks a manager with pending_restart in the list and its detail dialog", async () => {
    await render();
    await answer(0, { ...ROSTER, managers: ROSTER.managers.map((m) => (m.id === "m-1" ? { ...m, pending_restart: true } : m)) });
    expect(row("工作")?.textContent).toContain("待空闲重启");
    expect(row("助手")?.textContent).not.toContain("待空闲重启");
    await click(row("工作"));
    expect(document.body.querySelector('[data-testid="pending-restart-note"]')?.textContent).toContain("空闲时自动重启");
    await click(button("完成"));
    await click(row("助手"));
    const dialog = [...document.body.querySelectorAll('[role="dialog"]')].at(-1);
    expect(dialog?.textContent).not.toContain("待空闲重启");
  });
});
