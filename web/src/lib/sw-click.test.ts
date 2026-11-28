// The service worker's notificationclick (#2971): an app window it controls comes forward on the conversation, a
// focused one first; with none, or one that can't be navigated, a new window opens.
import { describe, expect, it, vi } from "vitest";
import { answeringWindows, clickNotification, openFromNotification, type ClickWindow } from "./sw-click";

const win = (o: Partial<ClickWindow> & { name: string }, navigates = true) => {
  const w = {
    ...o,
    focus: vi.fn(async () => w),
    navigate: vi.fn(async () => (navigates ? w : null)),
  };
  return w;
};

const clients = (wins: ClickWindow[]) => ({
  matchAll: vi.fn(async () => wins),
  openWindow: vi.fn(async () => null),
});

describe("点系统通知", () => {
  it("只看这个 worker 管着的窗口，挑有焦点的那个，转到对话", async () => {
    const back = win({ name: "back", visibilityState: "hidden" });
    const front = win({ name: "front", focused: true, visibilityState: "visible" });
    const c = clients([back, front]);
    await openFromNotification(c, "/m/s");
    expect(c.matchAll).toHaveBeenCalledWith({ type: "window", includeUncontrolled: false });
    expect(front.focus).toHaveBeenCalled();
    expect(front.navigate).toHaveBeenCalledWith("/m/s");
    expect(back.navigate).not.toHaveBeenCalled();
    expect(c.openWindow).not.toHaveBeenCalled();
  });

  it("没有窗口就开一个新的", async () => {
    const c = clients([]);
    await openFromNotification(c, "/m/s");
    expect(c.openWindow).toHaveBeenCalledWith("/m/s");
  });

  it("窗口转不过去（返回空或报错）就开一个新的", async () => {
    const stuck = win({ name: "stuck" }, false);
    const c = clients([stuck]);
    await openFromNotification(c, "/m/s");
    expect(c.openWindow).toHaveBeenCalledWith("/m/s");

    const broken = win({ name: "broken" });
    broken.navigate.mockRejectedValueOnce(new TypeError("not controlled"));
    const c2 = clients([broken]);
    await openFromNotification(c2, "/m/s");
    expect(c2.openWindow).toHaveBeenCalledWith("/m/s");
  });
});

describe("授权通知上的按钮（#4464）", () => {
  // a page that says on the port whether it took the click (lib/notification-answer.ts), or never answers
  const page = (took: boolean | null, o: Partial<ClickWindow> = {}) => {
    const w = win({ name: "page", visibilityState: "hidden", ...o });
    const sent: unknown[] = [];
    return Object.assign(w, {
      sent,
      postMessage: vi.fn((m: unknown, [port]: MessagePort[]) => {
        sent.push(m);
        if (took !== null) port.postMessage(took);
      }),
    });
  };
  const data = { url: "/s/m/a", ix: "p1", created_at: "2026-01-02T03:04:05.000000001Z" };

  it("点「允许」：交给开着的网页，不打开也不切到网页", async () => {
    const p = page(true);
    const c = clients([p]);
    await clickNotification(c, data, "allow", 50);
    expect(p.sent).toEqual([expect.objectContaining({ type: "persona.notification", action: "allow", ix: "p1", created_at: data.created_at })]);
    expect(p.focus).not.toHaveBeenCalled();
    expect(p.navigate).not.toHaveBeenCalled();
    expect(c.openWindow).not.toHaveBeenCalled();
  });

  it("网页不认（卡已经答掉）或不回话：退回打开对话", async () => {
    for (const took of [false, null]) {
      const p = page(took);
      const c = clients([p]);
      await clickNotification(c, data, "deny", 50);
      expect(p.navigate).toHaveBeenCalledWith("/s/m/a");
    }
  });

  it("没有开着的网页：开一个新窗口到对话", async () => {
    const c = clients([]);
    await clickNotification(c, data, "allow", 50);
    expect(c.openWindow).toHaveBeenCalledWith("/s/m/a");
  });

  it("点通知本身：拉到前面，网页自己转到对话", async () => {
    const p = page(true);
    await clickNotification(clients([p]), data, "", 50);
    expect(p.focus).toHaveBeenCalled();
    expect(p.sent).toEqual([expect.objectContaining({ action: "open", url: "/s/m/a" })]);
    expect(p.navigate).not.toHaveBeenCalled();
  });

  it("最前面的窗口冻住了（不回话）：问下一个，只有一个答", async () => {
    const frozen = page(null, { focused: true, visibilityState: "visible" });
    const other = page(true);
    const third = page(true);
    const c = clients([third, frozen, other]);
    await clickNotification(c, data, "allow", 60);
    expect(frozen.sent).toHaveLength(1);
    expect(other.sent.length + third.sent.length).toBe(1);
    expect(c.openWindow).not.toHaveBeenCalled();
  });

  it("管理页（/admin）不问：它不听这条消息", async () => {
    const admin = page(true, { focused: true, visibilityState: "visible", url: "https://hub.example/admin/" });
    const app = page(true, { url: "https://hub.example/overview" });
    await clickNotification(clients([admin, app]), data, "deny", 50);
    expect(admin.postMessage).not.toHaveBeenCalled();
    expect(app.sent).toEqual([expect.objectContaining({ action: "deny" })]);
  });

  it("挑窗口：有焦点 / 可见的在前，跳过管理页（只认根路径的）和不能收消息的，最多问三个", () => {
    const front = page(true, { focused: true, url: "/s/m/a" });
    const seen = page(true, { visibilityState: "visible", url: "/" });
    const back = page(true, { url: "/overview" });
    const admin = page(true, { url: "/admin" });
    const named = page(true, { url: "https://hub.example/s/m/admin" });
    const old = win({ name: "old" });
    expect(answeringWindows([back, admin, old, seen, front])).toEqual([front, seen, back]);
    // only the root's /admin is the admin page: a conversation called admin is the app's (#5070)
    expect(answeringWindows([admin, named])).toEqual([named]);
    expect(answeringWindows([back, back, back, back])).toHaveLength(3);
  });

  it("不是授权卡的通知照旧", async () => {
    const p = page(true);
    await clickNotification(clients([p]), { url: "/s/m/a" }, "", 50);
    expect(p.postMessage).not.toHaveBeenCalled();
    expect(p.navigate).toHaveBeenCalledWith("/s/m/a");
  });
});
