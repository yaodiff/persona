// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, getState: () => ({ ...real.getState(), features: ["extensions"] }) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../../ui/atoms";
import { UninstallDialog } from "./ExtensionMarket";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const warnings = Array.from({ length: 5 }, (_, i) => `请手动删除 /tmp/example-extension-${i % 4}/${"long-directory/".repeat(16)}data`);
let host: HTMLDivElement, root: Root;
let replies: { status?: number; body: unknown }[];
let requests: string[];
let listed: { id: string; name: string }[];
const done = vi.fn();
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const click = async (label: string) => {
  const button = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
  expect(button, label).toBeTruthy();
  await act(async () => button!.click());
  await flush();
};
const open = async (pack?: { id: string; name?: string }[]) => {
  function Parent() {
    const [show, setShow] = useState(true);
    return show && <UninstallDialog id="example" name="示例扩展" pack={pack ?? []} onClose={() => setShow(false)} onDone={() => { done(); setShow(false); }} />;
  }
  await act(async () => root.render(<Parent />));
  await flush();
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.mocked(toast).mockClear();
  done.mockClear();
  requests = [];
  listed = [];
  replies = [{ body: { ok: true, warnings } }];
  vi.stubGlobal("fetch", async (url: string, init: RequestInit = {}) => {
    if (init.method === "DELETE") {
      requests.push(url);
      const reply = replies.shift()!;
      return new Response(JSON.stringify(reply.body), { status: reply.status ?? 200 });
    }
    // The installed list (a pack's members by name, #4072).
    if (new URL(url, "http://hub.test").pathname === "/api/v1/extensions") return new Response(JSON.stringify({ extensions: listed }));
    return new Response(JSON.stringify({ id: "example", name: "示例扩展" }));
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("uninstall warnings", () => {
  it("keeps every warning until acknowledged, without another uninstall", async () => {
    await open();
    await click("卸载");
    expect(dialog()?.textContent ?? "").toContain(warnings[0]);
    expect([...dialog()!.querySelectorAll("li")].map((li) => li.textContent)).toEqual(warnings);
    expect(done).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    expect(dialog()?.querySelectorAll("input")).toHaveLength(0);
    expect([...dialog()!.querySelectorAll("button")].map((b) => b.textContent?.trim()).filter(Boolean)).toEqual(["关闭"]);
    await act(async () => { vi.advanceTimersByTime(60_000); });
    for (const warning of warnings) expect(dialog()?.textContent).toContain(warning);
    await click("关闭");
    expect(done).toHaveBeenCalledTimes(1);
    expect(requests).toEqual(["/api/v1/extensions/example/-/?keep_data=1"]);
  });

  it.each([{ value: undefined }, { value: null }, { value: [] }])("keeps the old success flow for warnings=$value", async ({ value }) => {
    replies = [{ body: { ok: true, ...(value === undefined ? {} : { warnings: value }) } }];
    await open();
    await click("卸载");
    expect(done).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith("已卸载「示例扩展」", "ok");
  });

  it("keeps failures retryable and sends the selected keep-data and pack options", async () => {
    replies = [{ status: 500, body: { error: "cleanup could not start" } }, { body: { ok: true } }];
    await open([{ id: "member", name: "成员" }]);
    const inputs = dialog()!.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
    await act(async () => { inputs[0].click(); inputs[1].click(); });
    await click("卸载");
    expect(dialog()?.textContent).toContain("cleanup could not start");
    expect(done).not.toHaveBeenCalled();
    await click("卸载");
    expect(done).toHaveBeenCalledTimes(1);
    expect(requests).toEqual(Array(2).fill("/api/v1/extensions/example/-/?members=1"));
  });

  it("names a pack's members and lists a partial uninstall's three groups, then tries again the same way", async () => {
    listed = [{ id: "member-a", name: "成员甲" }, { id: "member-b", name: "成员乙" }];
    replies = [{ status: 409, body: { ok: false, code: "partial_uninstall", error: "没卸载完：member-a 已卸载",
      removed_members: ["member-a"], failed_members: [{ id: "member-b", error: "仍在运行" }], remaining_members: ["member-c"] } }, { body: { ok: true } }];
    await open([{ id: "member-a" }, { id: "member-b" }, { id: "member-c" }]);
    // By name; one not installed keeps its id.
    expect(dialog()?.textContent).toContain("同时卸载合集成员成员甲、成员乙、member-c。如不勾选，这些成员将保留。");
    const inputs = dialog()!.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
    await act(async () => inputs[1].click());
    await click("卸载");
    const part = dialog()!.querySelector("[data-partial-uninstall]")!;
    expect(part.textContent).toContain("「示例扩展」仍处于已安装状态。此前已完成卸载的成员不会自动还原，你可以重试卸载。");
    expect([...part.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["已卸载：成员甲", "未能卸载：成员乙（仍在运行）", "未处理（仍处于已安装状态）：member-c"]);
    // The lists say it; the hub's sentence isn't repeated as an error.
    expect(dialog()?.textContent).not.toContain("没卸载完");
    expect(done).not.toHaveBeenCalled();
    await click("卸载");
    expect(dialog()?.querySelector("[data-partial-uninstall]") ?? null).toBeNull();
    expect(done).toHaveBeenCalledTimes(1);
    expect(requests).toEqual(Array(2).fill("/api/v1/extensions/example/-/?keep_data=1&members=1"));
  });

  it("still requires consent to disable dependents before retrying", async () => {
    replies = [{ status: 409, body: { error: "in use", code: "has_dependents", dependents: [{ id: "dependent", name: "依赖者" }] } }, { body: { ok: true, warnings } }];
    await open();
    await click("卸载");
    expect(dialog()?.textContent).toContain("同时停用依赖它的扩展：依赖者");
    const remove = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "卸载")!;
    expect(remove.disabled).toBe(true);
    const inputs = dialog()!.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
    await act(async () => inputs[1].click());
    await click("卸载");
    expect(requests).toEqual(["/api/v1/extensions/example/-/?keep_data=1", "/api/v1/extensions/example/-/?keep_data=1&cascade=1"]);
    expect([...dialog()!.querySelectorAll("li")].map((li) => li.textContent)).toEqual(warnings);
    expect(done).not.toHaveBeenCalled();
  });
});
