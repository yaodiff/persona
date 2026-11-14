// @vitest-environment jsdom
// The sidebar's switcher (docs/assistant/managers.md "Clients"): the current assistant in the sidebar's head, the roster it
// opens (status, where each runs, what waits there, the default tagged, the current one checked), switching, New
// assistant and Manage assistants…, an installing one's install commands, and the earlier product row without a roster.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const hub = vi.hoisted(() => ({ calls: [] as { path: string; method: string; json?: unknown }[], roster: null as unknown, error: null as unknown }));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    const method = init?.method ?? "GET";
    hub.calls.push({ path, method, json: init?.json });
    if (path === "/managers" && method === "POST") return Promise.resolve({ manager: { id: "m-new", name: (init?.json as { name: string }).name, online: false, installing: soon(10) }, code: "mi_new", expires_at: soon(10), commands: { service: "curl -fsSL <hub address>/i/mi_new | sh" } });
    if (path === "/managers") return hub.error ? Promise.reject(hub.error) : Promise.resolve(hub.roster);
    if (path === "/managers/m-3/code") return Promise.resolve({ code: "mi_again", expires_at: soon(10) });
    return Promise.resolve({});
  },
}));
const toast = vi.fn();
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast: (...args: unknown[]) => toast(...args) }));
const navigate = vi.fn();
vi.mock("../lib/router", async (original) => ({ ...(await original<typeof import("../lib/router")>()), navigate: (...args: unknown[]) => navigate(...args) }));

import { ApiError } from "../lib/api";
import { chooseManager, noteShownIdentity, pickedName, resetManagerChoice, shownManager } from "../lib/manager-choice";
import { loadRoster, resetRoster, setRoster, type ManagerList } from "../lib/managers";
import { connect, flushPushes, getState, reset } from "../lib/store";
import type { Interaction, Session } from "../lib/types";
import { loadPages } from "../lib/lazy-page";
import { AssistantSwitcher } from "./AssistantSwitcher";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// New assistant and the install code are lazy dialogs: their chunk is loaded before the clicks that open them
beforeAll(() => loadPages(), 60_000);

function soon(min: number) {
  return new Date(Date.now() + min * 60_000).toISOString();
}
const ROSTER: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, default: true, builtin: true, host: "server", inbox_needs_you: 1 },
    { id: "m-1", name: "工作", online: true, host: "MacBook", inbox_needs_you: 2 },
    { id: "m-2", name: "家里", online: false, host: "NAS", last_seen: new Date(Date.now() - 3 * 3600_000).toISOString() },
    { id: "m-3", name: "测试", online: false, installing: soon(7.2) },
  ],
};

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() {
    FakeSocket.last = this;
  }
  send() {}
  close() {
    this.readyState = 3;
  }
}

let root: Root;
let host: HTMLDivElement;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (route: Parameters<typeof AssistantSwitcher>[0]["route"] = { name: "persona" }) => {
  act(() => root.render(<AssistantSwitcher phase="online" online={2} total={3} route={route} />));
  await flush();
};
const head = () => host.querySelector<HTMLButtonElement>("[data-assistant-switcher]")!;
const open = async () => {
  await act(async () => head().click());
  await flush();
};
const rows = () => [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio], [role=menuitem]")];
const menuHeader = () => document.body.querySelector<HTMLElement>("[role=menu]")?.firstElementChild?.textContent;
const expectDescribedStatus = (text: string) => {
  const id = head().getAttribute("aria-describedby");
  expect(id).toBeTruthy();
  const line = document.getElementById(id!);
  expect(line).not.toBeNull();
  expect(head().contains(line!)).toBe(true);
  expect(line!.textContent).toBe(text);
};
const row = (name: string) => rows().find((r) => r.textContent?.includes(name))!;
const choose = async (name: string) => {
  await act(async () => row(name).click());
  await flush();
};

beforeEach(() => {
  hub.calls = [];
  hub.roster = ROSTER;
  hub.error = null;
  toast.mockClear();
  navigate.mockClear();
  resetManagerChoice();
  setRoster(ROSTER);
  // one agent of 工作 waits (a card), none of the others
  Object.assign(getState(), {
    interactions: [{ id: "x1", manager: "m-1", machine_id: "mac", session_id: "s1", kind: "question" } as Interaction],
    sessions: { mac: [{ machine_id: "mac", session_id: "s1", manager: "m-1", state: "needs_you" } as Session] },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  reset();
  resetRoster();
  resetManagerChoice();
});

describe("the sidebar's assistant switcher", () => {
  it("shows the current assistant with its status, and a mark when another one has something waiting", async () => {
    await render();
    expect(head().textContent).toContain("助手");
    expect(head().textContent).toContain("在线 · server");
    expect(head().querySelector("[data-avatar-status=green]")).not.toBeNull();
    expect(head().querySelector("[data-avatar-waiting]")).not.toBeNull();
    expect(head().getAttribute("aria-label")).toContain("别的助手有事等你处理");
    expectDescribedStatus("在线 · server");
    // on 工作 itself: what waits is its own (its pages count it), and the default's one inbox item is another's
    act(() => chooseManager("m-1"));
    await flush();
    expect(head().textContent).toContain("工作");
    expect(head().textContent).toContain("在线 · MacBook");
    expect(head().querySelector("[data-avatar-waiting]")).not.toBeNull();
  });

  it("lists every assistant: status and where, the default tagged, the current checked, the others' counts", async () => {
    await render();
    await open();
    const radios = [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(radios.map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
    expect(radios[0].textContent).toContain("助手默认");
    expect(radios[0].textContent).toContain("在线 · server");
    // 工作: one agent waiting and two inbox items; the current one shows no count of its own
    expect(radios[1].querySelector("[data-menu-badge]")?.textContent).toBe("3");
    expect(radios[0].querySelector("[data-menu-badge]")).toBeNull();
    expect(radios[2].textContent).toContain("离线 · 最后同步于");
    expect(row("测试").textContent).toContain("安装中 · 码 7 分钟后过期");
    expect(rows().map((r) => r.textContent).filter((x) => x === "新建助手" || x === "管理助手…")).toEqual(["新建助手", "管理助手…"]);
  });

  it("switches the whole app to the one picked, and leaves another assistant's page for its own", async () => {
    await render({ name: "session", mid: "mac", sid: "s9" });
    await open();
    await choose("工作");
    expect(shownManager()).toBe("m-1");
    expect(navigate).toHaveBeenCalledWith({ name: "persona" });
    expect(toast).toHaveBeenCalledWith("已切换到 工作", "ok");
    expect(head().textContent).toContain("工作");
    // an offline one can be picked too: its pages show what was last read
    await open();
    await choose("家里");
    expect(shownManager()).toBe("m-2");
    expect(head().querySelector("[data-avatar-status=grey]")).not.toBeNull();
  });

  it("makes a new assistant from the list, and shows its install commands", async () => {
    await render();
    await open();
    await choose("新建助手");
    const input = document.body.querySelector<HTMLInputElement>("[role=dialog] input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "书房");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => [...document.body.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent === "创建")!.click());
    await flush();
    expect(hub.calls.find((c) => c.path === "/managers" && c.method === "POST")?.json).toEqual({ name: "书房" });
    expect(document.body.textContent).toContain("安装「书房」");
    expect(document.body.textContent).toContain("mi_new");
    expect(document.body.textContent).toContain("curl -fsSL <hub address>/i/mi_new | sh");
  });

  it("opens Settings › Assistants from Manage assistants…", async () => {
    await render();
    await open();
    await choose("管理助手…");
    expect(navigate).toHaveBeenCalledWith("/settings?tab=managers");
  });

  it("shows an installing assistant's install code instead of switching to it", async () => {
    await render();
    await open();
    await choose("测试");
    expect(shownManager()).toBe("assistant");
    expect(hub.calls.find((c) => c.path === "/managers/m-3/code")?.method).toBe("POST");
    expect(document.body.textContent).toContain("安装「测试」");
    expect(document.body.textContent).toContain("mi_again");
  });

  it("shows one assistant's identity and roster, with New and Manage but no default tag or counts", async () => {
    setRoster({ default: "assistant", managers: [ROSTER.managers[0]] });
    await render();
    expect(head().textContent).toContain("助手");
    expect(head().textContent).toContain("在线 · server");
    expect(head().getAttribute("data-assistant-switcher")).toBe("assistant");
    // The default assistant's head is the look's face, not the first-letter disc (#6401).
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(head().querySelector("svg")).not.toBeNull();
    expect(head().querySelector("[data-avatar-status=green]")).not.toBeNull();
    expect(head().querySelector("[data-avatar-waiting]")).toBeNull();
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("264px");
    expect(menuHeader()).toBe("助手");
    const radios = [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(radios).toHaveLength(1);
    expect(radios[0].getAttribute("aria-checked")).toBe("true");
    expect(radios[0].textContent).toContain("助手");
    expect(radios[0].textContent).not.toContain("默认");
    expect(radios[0].querySelector("[data-menu-badge]")).toBeNull();
    expect(rows().slice(1).map((r) => r.textContent)).toEqual(["新建助手", "管理助手…", "跟随系统", "浅色", "深色", "设置", "断开这个浏览器…"]);
    await choose("设置");
    expect(navigate).toHaveBeenCalledWith({ name: "settings" });
  });

  it("shows a renamed sole assistant's identity even when it is not built in and is offline", async () => {
    setRoster({ default: "m-only", managers: [{ id: "m-only", name: "Home helper", online: false }] });
    await render();
    expect(head().textContent).toContain("Home helper");
    expect(head().textContent).toContain("离线");
    expect(head().textContent).not.toContain("Persona");
    expect(head().getAttribute("data-assistant-switcher")).toBe("m-only");
    // It is the roster's default, so its head is the look's face even with a custom name (#6401).
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(head().querySelector("svg")).not.toBeNull();
    expect(head().querySelector("[data-avatar-status=grey]")).not.toBeNull();
    await open();
    expect(document.body.querySelectorAll("[role=menuitemradio]")).toHaveLength(1);
    expect(row("Home helper").textContent).toContain("离线");
  });

  it("shows one installed and one installing assistant in the roster, still without counts", async () => {
    setRoster({ default: "assistant", managers: [ROSTER.managers[0], ROSTER.managers[3]] });
    await render();
    expect(head().textContent).toContain("助手");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(head().querySelector("svg")).not.toBeNull();
    expect(head().querySelector("[data-avatar-waiting]")).toBeNull();
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("264px");
    expect(menuHeader()).toBe("助手");
    const radios = [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(radios).toHaveLength(1);
    expect(radios[0].textContent).not.toContain("默认");
    expect(row("测试").textContent).toContain("安装中");
    expect(document.body.querySelector("[data-menu-badge]")).toBeNull();
    expect(row("新建助手")).toBeDefined();
    expect(row("管理助手…")).toBeDefined();
  });

  it("moves a mounted head from no roster to one, two and one assistant without a reload", async () => {
    resetRoster();
    resetManagerChoice(); // the first read, or an old hub: there is no remembered head identity
    hub.roster = new Promise(() => {}); // keep the read out while the test supplies each fresh roster
    await render();
    expect(head().textContent).toContain("Persona");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();

    const one = { default: "assistant", managers: [ROSTER.managers[0]] };
    act(() => setRoster(one));
    await flush();
    expect(head().textContent).toContain("助手");
    expect(head().textContent).not.toContain("Persona");
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("264px");
    expect(menuHeader()).toBe("助手");
    expect(document.body.querySelectorAll("[role=menuitemradio]")).toHaveLength(1);
    await choose("助手"); // close the menu without changing the current assistant

    act(() => setRoster({ default: "assistant", managers: ROSTER.managers.slice(0, 2) }));
    await flush();
    await open();
    expect(document.body.querySelectorAll("[role=menuitemradio]")).toHaveLength(2);
    await choose("工作");
    expect(shownManager()).toBe("m-1");
    expect(head().textContent).toContain("工作");

    act(() => setRoster(one));
    await flush();
    expect(shownManager()).toBe("assistant");
    expect(head().textContent).toContain("助手");
    expect(head().textContent).not.toContain("Persona");
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("264px");
    expect(menuHeader()).toBe("助手");
    expect(document.body.querySelectorAll("[role=menuitemradio]")).toHaveLength(1);
    expect(document.body.textContent).not.toContain("工作");
  });

  it("uses the product head, width and appearance menu for a fresh empty roster", async () => {
    const empty = { default: "assistant", managers: [] };
    hub.roster = empty;
    setRoster(empty);
    await render();
    expect(head().textContent).toContain("Persona");
    expect(head().textContent).not.toContain("助手");
    expect(head().getAttribute("data-assistant-switcher")).toBe("");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("220px");
    expect(menuHeader()).toBe("外观");
    expect(rows().map((r) => r.textContent)).toEqual(["跟随系统", "浅色", "深色", "设置", "断开这个浏览器…"]);
  });

  it("uses the same product form when the fresh roster has only an installing assistant", async () => {
    const installing = { default: "m-3", managers: [ROSTER.managers[3]] };
    hub.roster = installing;
    setRoster(installing);
    await render();
    expect(head().textContent).toContain("Persona");
    expect(head().textContent).not.toContain("测试");
    expect(head().getAttribute("data-assistant-switcher")).toBe("");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("220px");
    expect(menuHeader()).toBe("外观");
    expect(rows().some((r) => r.textContent === "新建助手" || r.textContent === "管理助手…")).toBe(false);
  });

  it("keeps identity and a one-row roster when two assistants become one", async () => {
    setRoster({ default: "assistant", managers: [ROSTER.managers[0], ROSTER.managers[1]] });
    await render();
    expect(head().textContent).toContain("助手");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(head().querySelector("svg")).not.toBeNull();
    await open();
    expect(document.body.querySelectorAll("[role=menuitemradio]")).toHaveLength(2);
    await choose("工作");
    expect(shownManager()).toBe("m-1");
    expect(head().textContent).toContain("工作");
    act(() => setRoster({ default: "assistant", managers: [ROSTER.managers[0]] }));
    await flush();
    expect(shownManager()).toBe("assistant");
    expect(head().textContent).toContain("助手");
    expect(head().textContent).not.toContain("Persona");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(head().querySelector("svg")).not.toBeNull();
    expect(head().querySelector("[data-avatar-waiting]")).toBeNull();
    await open();
    const radios = [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(radios).toHaveLength(1);
    expect(radios[0].getAttribute("aria-checked")).toBe("true");
    expect(radios[0].textContent).toContain("助手");
    expect(document.body.textContent).not.toContain("工作");
  });

  it("keeps the row it had on a hub without a roster", async () => {
    resetRoster();
    resetManagerChoice(); // no roster has ever supplied an identity on this old hub
    hub.roster = new Promise(() => {});
    await render();
    expect(head().textContent).toContain("Persona");
    expect(head().textContent).toContain("2 / 3 台电脑在线");
    expect(head().getAttribute("data-assistant-switcher")).toBe("");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("220px");
    expect(menuHeader()).toBe("外观");
    expect(rows().map((r) => r.textContent)).toEqual(["跟随系统", "浅色", "深色", "设置", "断开这个浏览器…"]);
    expect(rows().some((r) => r.textContent === "新建助手")).toBe(false);
  });

  it("fades an offline assistant's whole row, which can still be picked", async () => {
    await render();
    await open();
    expect(row("家里").className).toContain("opacity-55");
    expect(row("家里").querySelector("[data-assistant-avatar]")?.className).not.toContain("opacity");
    expect(row("工作").className).not.toContain("opacity-55");
    expect(row("测试").className).not.toContain("opacity-55"); // installing: its own line says so
    await choose("家里");
    expect(shownManager()).toBe("m-2");
  });

  it("counts again when the hub pushes `managers` (an inbox count moved)", async () => {
    vi.stubGlobal("WebSocket", FakeSocket);
    await render();
    await open();
    const badge = () => row("工作").querySelector("[data-menu-badge]")?.textContent;
    expect(badge()).toBe("3");
    act(() => connect());
    act(() => {
      FakeSocket.last.readyState = 1;
      FakeSocket.last.onopen?.();
    });
    await flush();
    hub.roster = { ...ROSTER, managers: ROSTER.managers.map((m) => (m.id === "m-1" ? { ...m, inbox_needs_you: 5 } : m)) };
    const asked = hub.calls.filter((c) => c.path === "/managers").length;
    act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify({ type: "managers" }) }));
    act(() => flushPushes());
    await flush();
    expect(hub.calls.filter((c) => c.path === "/managers").length).toBeGreaterThan(asked);
    // one agent waiting and now five inbox items, in the list as it was left open
    expect(badge()).toBe("6");
    vi.unstubAllGlobals();
  });

  it("keeps a remembered default through a temporary failure, and clears it for a hub without a roster", async () => {
    const one = { default: "assistant", managers: [ROSTER.managers[0]] };
    setRoster(one);
    resetRoster(); // a reload: the roster is gone, the default assistant's display identity is not
    hub.error = new ApiError(503, "unavailable", { error: "unavailable" });
    await render();
    expect(head().textContent).toContain("助手");
    expect(head().textContent).not.toContain("Persona");
    expect(head().getAttribute("data-assistant-switcher")).toBe("assistant");

    await act(async () => { await loadRoster(); });
    await flush();
    expect(head().textContent).toContain("助手");
    expect(head().getAttribute("data-assistant-switcher")).toBe("assistant");
    await open();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("264px");
    expect(menuHeader()).toBe("助手");

    hub.error = new ApiError(404, "not found", { error: "not found" }); // no code: the roster endpoint is unsupported
    await act(async () => { await loadRoster(); });
    await flush();
    expect(head().textContent).toContain("Persona");
    expect(head().textContent).not.toContain("助手");
    expect(head().getAttribute("data-assistant-switcher")).toBe("");
    expect(head().querySelector("[data-assistant-avatar]")).toBeNull();
    expect(document.body.querySelector<HTMLElement>("[role=menu]")!.style.width).toBe("220px");
    expect(menuHeader()).toBe("外观");
    expect(rows().some((r) => r.textContent === "新建助手" || r.textContent === "管理助手…")).toBe(false);
  });

  it("shows a kept pick by the name the roster last gave it until the roster is read again", async () => {
    act(() => chooseManager("m-1"));
    expect(pickedName()).toBe("工作");
    noteShownIdentity(null); // a pick from before the display identity cache existed still has its own name
    resetRoster(); // a reload: the pick and its name are kept, the roster not read yet
    await render();
    expect(head().textContent).toContain("工作");
    expect(head().textContent).not.toContain("Persona");
    expect(head().getAttribute("data-assistant-switcher")).toBe("m-1");
    act(() => setRoster(ROSTER));
    await flush();
    expect(head().textContent).toContain("在线 · MacBook");
  });

  it("says the hub is out of reach instead of the assistant's own status", async () => {
    act(() => root.render(<AssistantSwitcher phase="unreachable" online={0} total={3} route={{ name: "persona" }} />));
    await flush();
    expect(head().textContent).toContain("助手");
    expect(head().textContent).toContain("连不上 hub");
    expect(head().querySelector("[data-avatar-status]")).toBeNull();
    expectDescribedStatus("连不上 hub");
  });
});
