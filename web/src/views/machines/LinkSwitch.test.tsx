// @vitest-environment jsdom
// Switching an ssh computer to Persona Link (#10662): what each state of the machine list's `link_switch` says, and a
// start that posts once, shows "Starting…" at once, takes a 202's state, and on anything it cannot trust reads the list
// again instead of posting again, whichever mount pressed it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetLinkSwitchAttempts } from "../../lib/link-switch";
import type { MachineLinkSwitch } from "../../lib/types";
import { homeUser, LinkSwitch } from "./LinkSwitch";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});

type Post = () => Promise<{ link_switch?: MachineLinkSwitch } | undefined>;
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => host.textContent ?? "";
const buttons = (label: string) => [...document.body.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label);
const START = "切换到 Persona Link…";
const CONFIRM = "切换";
const view = (state: MachineLinkSwitch | undefined, post: Post = vi.fn(), refresh: () => unknown = vi.fn(), id = "box", user?: string) => (
  <LinkSwitch key={id} machineId={id} name="盒子" state={state} post={post} refresh={refresh} user={user} />
);
const render = async (node: React.ReactNode) => {
  await act(async () => root.render(node));
  await flush();
};
const open = async () => {
  const b = buttons(START);
  expect(b).toHaveLength(1);
  await act(async () => b[0]!.click());
};
const deferred = <T,>() => {
  let resolve!: (v: T) => void, reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
  return { promise, resolve, reject };
};
const at = (s: number) => new Date(Date.UTC(2026, 10, 19, 8, 0, s)).toISOString();

beforeEach(() => {
  resetLinkSwitchAttempts();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
});

describe("LinkSwitch states", () => {
  it("shows nothing when the hub says nothing (an older hub)", async () => {
    await render(view(undefined));
    expect(host.innerHTML).toBe("");
  });

  it("offers the switch when it is available", async () => {
    await render(view({ available: true }));
    expect(text()).toContain("当前通过 SSH 连接");
    expect(buttons(START)).toHaveLength(1);
  });

  it.each([
    ["link_unavailable", "hub 尚未启用 Persona Link 连接"],
    ["unsupported_os", "仅支持在此切换 Linux 电脑。Mac 或 Windows 电脑请直接在该电脑上安装 Persona Link。"],
    ["pending", "请先确认这台电脑是你的。"],
    ["custom_node", "安装在自定义路径"],
    ["unsafe_permissions", "chmod go-w ~ ~/.persona ~/.persona/bin"],
    ["hub_old", "hub 需要更新后才能切换电脑。"],
    ["something_new", "这台电脑暂时无法切换到 Persona Link。"],
  ])("says why it can't start (%s), with no button", async (reason, words) => {
    await render(view({ available: false, reason }));
    expect(text()).toContain(words);
    expect(text()).not.toContain(reason);
    expect(buttons(START)).toHaveLength(0);
  });

  it.each(["offline", "busy"])("says nothing of a switch while the computer is %s: the page says that already", async (reason) => {
    await render(view({ available: false, reason }));
    expect(host.innerHTML).toBe("");
  });

  it.each([
    ["checking", 0],
    ["installing", 1],
    ["connecting", 2],
  ] as const)("shows the steps while %s, even with no button", async (phase, current) => {
    await render(view({ available: false, reason: "in_progress", phase, at: at(1) }));
    const steps = [...host.querySelectorAll("[data-testid=link-switch-steps] li")];
    expect(steps.map((s) => s.getAttribute("data-state"))).toEqual(["checking", "installing", "connecting"].map((_, i) => (i < current ? "done" : i === current ? "current" : "todo")));
    expect(steps.map((s) => s.lastElementChild?.textContent)).toEqual(["检查电脑", "安装 Persona Link", "等待电脑连接"]);
    expect(buttons(START)).toHaveLength(0);
  });

  it("says it is going back to ssh while rolling back", async () => {
    await render(view({ available: false, reason: "in_progress", phase: "rolling_back", error: "link_timeout" }));
    expect(text()).toContain("切换失败，正在将电脑恢复为 SSH 连接…");
    expect(text()).toContain("电脑未能及时通过 Persona Link 建立连接。");
  });

  it("says it worked", async () => {
    await render(view({ available: false, phase: "succeeded" }));
    expect(text()).toContain("已切换至 Persona Link");
    expect(buttons(START)).toHaveLength(0);
  });

  it.each([
    ["no_systemd", "没有 systemd 用户服务"],
    ["existing_link", "已安装或配对 Persona Link"],
    ["no_node", "未能在该电脑的默认位置找到其程序"],
    ["no_package", "没有适用于该电脑系统的 Persona Link 安装包"],
    ["transfer_failed", "未能通过 SSH 将 Persona Link 传输到该电脑"],
    ["install_failed", "未能在这台电脑上完成 Persona Link 的安装或配对"],
    ["link_timeout", "电脑未能及时通过 Persona Link 建立连接"],
    ["config_changed", "配置发生了变更"],
    ["interrupted", "Persona 在切换过程中发生了重启"],
    ["unknown", "发生未知错误。"],
    ["brand_new_code", "发生未知错误。"],
  ])("says a failure's cause in its own words and that it is back on ssh (%s)", async (error, words) => {
    await render(view({ available: true, phase: "failed", error }));
    const line = host.querySelector("[data-link-switch-failed]")!;
    expect(line.getAttribute("data-link-switch-failed")).toBe("rolled_back");
    expect(line.textContent).toContain("未能切换到 Persona Link。");
    expect(line.textContent).toContain(words);
    expect(line.textContent).toContain("电脑已恢复为 SSH 连接。");
    expect(line.textContent).not.toContain(error);
    // available again: it can be tried again, behind the same confirmation
    expect(buttons(START)).toHaveLength(1);
  });

  it("never reports a rollback it could not confirm as done, and says what to check", async () => {
    await render(view({ available: false, reason: "offline", phase: "failed", error: "link_timeout", rollback_unconfirmed: true }));
    const line = host.querySelector("[data-link-switch-failed]")!;
    expect(line.getAttribute("data-link-switch-failed")).toBe("unconfirmed");
    expect(line.textContent).toContain("Persona 无法确认是否已成功回退");
    expect(line.textContent).toContain("请检查这台电脑是否可正常访问；如果无法连接，请在该电脑上重新连接。");
    expect(line.textContent).not.toContain("电脑已恢复为 SSH 连接");
    expect(text()).toContain("电脑需要处于在线状态才能切换。");
  });

  it("says the hub did not answer when undoing ran out of time", async () => {
    await render(view({ available: false, reason: "in_progress", phase: "failed", error: "hub_unanswered", rollback_unconfirmed: true }));
    const line = host.querySelector("[data-link-switch-failed]")!;
    expect(line.getAttribute("data-link-switch-failed")).toBe("unconfirmed");
    expect(line.textContent).toContain("hub 在 1 小时内未响应，因此无法回退此次切换。");
    expect(line.textContent).not.toContain("hub_unanswered");
  });

  it("names the linger command with the computer's account, or a placeholder", async () => {
    await render(view({ available: true, phase: "failed", error: "linger_off" }, vi.fn(), vi.fn(), "box", "alice"));
    expect(host.querySelector("code")?.textContent).toBe("sudo loginctl enable-linger alice");
    expect(text()).toContain("需要这台电脑的管理员先运行");
    await render(view({ available: true, phase: "failed", error: "linger_off" }, vi.fn(), vi.fn(), "other"));
    expect(host.querySelector("code")?.textContent).toBe("sudo loginctl enable-linger <用户名>");
    expect(homeUser("/home/alice")).toBe("alice");
    expect(homeUser("/root/")).toBe("root");
    expect(homeUser(undefined)).toBeUndefined();
  });
});

describe("after a start", () => {
  const accepted = { available: false, reason: "in_progress", phase: "checking" as const, at: at(5) };

  it("lets the list speak once it has caught up, even when it later says what it said before the press", async () => {
    const post = vi.fn(async () => ({ link_switch: accepted }));
    const before: MachineLinkSwitch = { available: true };
    await render(view(before, post));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
    await render(view({ available: false, reason: "in_progress", phase: "installing", at: at(9) }, post));
    await render(view({ available: false, phase: "succeeded", at: at(30) }, post));
    expect(text()).toContain("已切换至 Persona Link");
    // an hour later the hub drops the finished record: the list is back to the press's own state
    await render(view({ ...before }, post));
    expect(host.querySelector("[data-testid=link-switch-steps]")).toBeNull();
    expect(buttons(START)).toHaveLength(1);
    expect(post).toHaveBeenCalledTimes(1);
  });

  it("drops a notice the list has moved past, so it does not come back with the old state", async () => {
    const post = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const before: MachineLinkSwitch = { available: true };
    await render(view(before, post, vi.fn(async () => {})));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(host.querySelector("[data-link-switch-notice]")).not.toBeNull();
    await render(view({ available: false, reason: "in_progress", phase: "checking", at: at(3) }, post));
    await render(view({ ...before }, post));
    expect(host.querySelector("[data-link-switch-notice]")).toBeNull();
    expect(buttons(START)).toHaveLength(1);
  });

  it("does not let a start nobody watched retire speak for the list later", async () => {
    const now = vi.spyOn(Date, "now");
    try {
      now.mockReturnValue(Date.UTC(2026, 10, 19, 8, 0, 0));
      const post = vi.fn(async () => ({ link_switch: accepted }));
      const before: MachineLinkSwitch = { available: true };
      await render(view(before, post));
      await open();
      await act(async () => buttons(CONFIRM)[0]!.click());
      await flush();
      // the page is left before the list moves; an hour later it opens again on the pre-press state
      act(() => root.unmount());
      root = createRoot(host);
      now.mockReturnValue(Date.UTC(2026, 10, 19, 9, 0, 0));
      await render(view({ ...before }, post));
      expect(host.querySelector("[data-testid=link-switch-steps]")).toBeNull();
      expect(buttons(START)).toHaveLength(1);
    } finally {
      now.mockRestore();
    }
  });
});

describe("starting a switch", () => {
  it("asks first, posts once on a double click, shows Starting… at once and then the 202's state", async () => {
    const answer = deferred<{ link_switch?: MachineLinkSwitch }>();
    const post = vi.fn(() => answer.promise);
    const refresh = vi.fn();
    await render(view({ available: true }, post, refresh));
    await open();
    expect(document.body.querySelector("[role=dialog]")?.textContent).toContain("这台电脑会短暂断开并重新连接");
    expect(post).not.toHaveBeenCalled();
    const confirm = buttons(CONFIRM)[0]!;
    act(() => {
      confirm.click();
      confirm.click();
    });
    expect(post).toHaveBeenCalledTimes(1);
    expect(text()).toContain("正在启动…");
    expect(buttons(START)).toHaveLength(0);
    await act(async () => answer.resolve({ link_switch: { available: false, reason: "in_progress", phase: "checking", at: at(5) } }));
    await flush();
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
    expect(refresh).not.toHaveBeenCalled();
    expect(post).toHaveBeenCalledTimes(1);
  });

  it("keeps the 202's state until the list is as new, then follows the list", async () => {
    const post = vi.fn(async () => ({ link_switch: { available: false, reason: "in_progress", phase: "checking" as const, at: at(5) } }));
    const old: MachineLinkSwitch = { available: true, phase: "failed", error: "link_timeout", at: at(1) };
    await render(view(old, post));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
    // a push from before the start (the old failure) does not take it back
    await render(view({ ...old }, post));
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
    await render(view({ available: false, reason: "in_progress", phase: "installing", at: at(9) }, post));
    expect(host.querySelector("[data-step=installing]")?.getAttribute("data-state")).toBe("current");
  });

  it("reads the list again after a lost answer, never posting again by itself", async () => {
    const post = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const read = deferred<void>();
    const refresh = vi.fn(() => read.promise);
    await render(view({ available: true }, post, refresh));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(post).toHaveBeenCalledTimes(1);
    expect(refresh).toHaveBeenCalledTimes(1);
    // still in flight until the list is back: no second start
    expect(text()).toContain("正在启动…");
    expect(buttons(START)).toHaveLength(0);
    await act(async () => read.resolve());
    await flush();
    expect(text()).toContain("无法确认切换是否已启动");
    expect(text()).not.toContain("Failed to fetch");
    expect(post).toHaveBeenCalledTimes(1);
    // the list then says it started: the steps, and the notice is gone
    await render(view({ available: false, reason: "in_progress", phase: "checking", at: at(3) }, post, refresh));
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
    expect(text()).not.toContain("无法确认切换是否已启动");
    expect(post).toHaveBeenCalledTimes(1);
  });

  it.each([
    [{ status: 409, code: "in_progress" }, "这台电脑正在切换到 Persona Link。"],
    [{ status: 409, code: "offline" }, "电脑需要处于在线状态才能切换。"],
    [{ status: 409, code: "odd" }, "这台电脑暂时无法切换到 Persona Link。"],
    [{ status: 502 }, "无法确认切换是否已启动"],
  ])("reads the list again after a refusal or an unclear answer (%o) and says it in words", async (error, words) => {
    const post = vi.fn(async () => {
      throw Object.assign(new Error("raw hub text"), error);
    });
    const refresh = vi.fn(async () => {});
    await render(view({ available: true }, post, refresh));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(post).toHaveBeenCalledTimes(1);
    expect(host.querySelector("[data-link-switch-notice]")?.textContent).toContain(words);
    expect(text()).not.toContain("raw hub text");
  });

  it("says a device without full access can't switch, without reading the list", async () => {
    const post = vi.fn(async () => {
      throw Object.assign(new Error("forbidden"), { status: 403, code: "forbidden" });
    });
    const refresh = vi.fn();
    await render(view({ available: true }, post, refresh));
    await open();
    await act(async () => buttons(CONFIRM)[0]!.click());
    await flush();
    expect(text()).toContain("当前设备无法切换电脑连接方式");
    expect(refresh).not.toHaveBeenCalled();
  });

  it("lets only one mount start it: a second one shows Starting… and cannot post", async () => {
    const answer = deferred<{ link_switch?: MachineLinkSwitch }>();
    const first = vi.fn(() => answer.promise);
    const second = vi.fn(() => answer.promise);
    const state: MachineLinkSwitch = { available: true };
    await render(
      <>
        <LinkSwitch machineId="box" name="盒子" state={state} post={first} refresh={vi.fn()} />
        <LinkSwitch machineId="box" name="盒子" state={state} post={second} refresh={vi.fn()} />
      </>,
    );
    const starts = buttons(START);
    expect(starts).toHaveLength(2);
    await act(async () => starts[0]!.click());
    await act(async () => starts[1]!.click());
    const confirms = buttons(CONFIRM);
    expect(confirms).toHaveLength(2);
    act(() => {
      confirms[0]!.click();
      confirms[1]!.click();
    });
    expect(first.mock.calls.length + second.mock.calls.length).toBe(1);
    expect(host.querySelectorAll("[data-link-switch=starting]")).toHaveLength(2);
    // remounted while in flight: still Starting…, still one post
    await render(<LinkSwitch key="again" machineId="box" name="盒子" state={state} post={second} refresh={vi.fn()} />);
    expect(text()).toContain("正在启动…");
    expect(buttons(START)).toHaveLength(0);
    await act(async () => answer.resolve({ link_switch: { available: false, reason: "in_progress", phase: "checking", at: at(2) } }));
    await flush();
    expect(first.mock.calls.length + second.mock.calls.length).toBe(1);
  });
});
