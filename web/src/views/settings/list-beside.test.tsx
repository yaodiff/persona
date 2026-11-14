// @vitest-environment jsdom
// Settings › Extensions / Connectors / Channels keep their list beside the open item from 900 px (Settings.tsx, audit
// item 68): the list marks the open item's row as current, and in that narrow column each row puts its state under its
// name, so the name has the row's width. A list on its own keeps its state at the row's end and marks nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// the current assistant (none picked: the default)
const chosen = vi.hoisted(() => ({ value: null as string | null }));
vi.mock("../../lib/manager-choice", async (original) => ({ ...(await original<typeof import("../../lib/manager-choice")>()), useChosenManager: () => chosen.value }));
import { setToken } from "../../lib/api";
import { extensionState, type HubExtension } from "../../lib/extensions";
import { ChannelList } from "./Channels";
import { Extensions } from "./Extensions";
import { ConnectorList } from "./HubConnector";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const EXTENSIONS = [
  { id: "example-gateway", name: "Example gateway panel with a long name", kind: "ui", status: "running", source: "installed", enabled: true, description: "Shows the gateway's balance" },
  { id: "example-notes", name: "Example notes", kind: "process", status: "running", source: "installed", enabled: true },
];
const CONNECTORS = [
  { id: "github", name: "GitHub", status: "ok", account: "example-user" },
  { id: "telegram", name: "Telegram", status: "not_signed_in" },
];
const CHANNELS = [
  { id: "wechat", name: "WeChat with a long name", kind: "im", source: "builtin", installed: true, enabled: true, state: "connected", account: { name: "Example user" }, capabilities: {}, bind: "qr" },
  { id: "example-team", name: "Example Team", kind: "im", source: "plugin", installed: true, enabled: true, state: "error", error: "token expired", capabilities: {}, bind: "fields" },
];

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
/** The hub's lists, each item `manager`'s (none: the default's). */
const hubWith = (manager?: string) => {
  const own = <T,>(items: T[]) => (manager ? items.map((x) => ({ ...x, manager })) : items);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://hub.test").pathname.replace(/^\/api\/v1/, "");
    if (path === "/extensions" || path === "/plugins") return Promise.resolve(json({ extensions: own(EXTENSIONS), plugins: own(EXTENSIONS), markets: false }));
    if (path === "/connectors") return Promise.resolve(json({ connectors: own(CONNECTORS) }));
    if (path === "/channels") return Promise.resolve(json({ channels: CHANNELS }));
    return Promise.resolve(json({}));
  }));
};
beforeEach(() => {
  setToken("fixture-token");
  chosen.value = null;
  hubWith();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});
const draw = async (ui: React.ReactNode) => {
  await act(async () => root.render(ui));
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
/** The row (a button) whose text has `name`. */
const row = (name: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(name))!;
/** The column holding the row's name, and whether the state text is in it (under the name) or after it. */
const stateUnderName = (r: HTMLElement, name: string, state: string) => {
  const nameEl = [...r.querySelectorAll<HTMLElement>("span")].find((s) => s.textContent === name)!;
  const column = [...r.children].find((c) => c.contains(nameEl))!;
  expect(r.textContent).toContain(state);
  return column.textContent!.includes(state);
};

describe("a settings list beside the open item", () => {
  it("marks the open extension and puts each extension's state under its name", async () => {
    await draw(<Extensions open={null} openBeside="example-gateway" onOpen={() => {}} />);
    const open = row("Example gateway panel");
    expect(open.getAttribute("aria-current")).toBe("true");
    expect(row("Example notes").hasAttribute("aria-current")).toBe(false);
    expect(host.querySelectorAll('[aria-current="true"]')).toHaveLength(1);
    expect(stateUnderName(open, EXTENSIONS[0].name, extensionState(EXTENSIONS[0] as HubExtension))).toBe(true);
    // the description gives way in the narrow column
    expect(open.textContent).not.toContain("Shows the gateway's balance");
  });

  it("keeps an extension list on its own as it was", async () => {
    await draw(<Extensions open={null} onOpen={() => {}} />);
    expect(host.querySelectorAll("[aria-current]")).toHaveLength(0);
    const r = row("Example gateway panel");
    expect(stateUnderName(r, EXTENSIONS[0].name, extensionState(EXTENSIONS[0] as HubExtension))).toBe(false);
    expect(r.textContent).toContain("Shows the gateway's balance");
  });

  it("marks the open connector and puts each connector's state under its name", async () => {
    await draw(<ConnectorList openBeside="github" onOpen={() => {}} />);
    const open = host.querySelector<HTMLButtonElement>('[data-connector-row="github"]')!;
    expect(open.getAttribute("aria-current")).toBe("true");
    expect(host.querySelector('[data-connector-row="telegram"]')!.hasAttribute("aria-current")).toBe(false);
    expect(stateUnderName(open, "GitHub", "example-user")).toBe(true);
    await draw(<ConnectorList onOpen={() => {}} />);
    expect(host.querySelectorAll("[aria-current]")).toHaveLength(0);
    expect(stateUnderName(host.querySelector<HTMLButtonElement>('[data-connector-row="github"]')!, "GitHub", "example-user")).toBe(false);
  });

  it("marks the open channel and puts each channel's state under its name", async () => {
    await draw(<ChannelList openBeside="wechat" onOpen={() => {}} onMarketItem={() => {}} />);
    const open = row("WeChat with a long name");
    expect(open.getAttribute("aria-current")).toBe("true");
    expect(row("Example Team").hasAttribute("aria-current")).toBe(false);
    expect(stateUnderName(open, CHANNELS[0].name, "已连接")).toBe(true);
    // in the size the other two lists use for a state under a name
    const state = [...open.querySelectorAll<HTMLElement>("span")].find((s) => s.textContent === "已连接" && s.querySelector("span"))!;
    expect(state.className).toContain("text-submeta");
    expect(state.className).not.toContain("text-[12px]");
    await draw(<ChannelList onOpen={() => {}} onMarketItem={() => {}} />);
    expect(host.querySelectorAll("[aria-current]")).toHaveLength(0);
    expect(stateUnderName(row("WeChat with a long name"), CHANNELS[0].name, "已连接")).toBe(false);
  });

  // an address without @manager opens the current assistant's copy (the item's page says so), so that copy's row is the
  // one marked, as with the default assistant
  it("marks the current assistant's copy when the address names no assistant", async () => {
    chosen.value = "work";
    hubWith("work");
    await draw(<Extensions open={null} openBeside="example-gateway" onOpen={() => {}} />);
    expect(row("Example gateway panel").getAttribute("aria-current")).toBe("true");
    await draw(<ConnectorList openBeside="github" onOpen={() => {}} />);
    expect(host.querySelector('[data-connector-row="github"]')!.getAttribute("aria-current")).toBe("true");
    await draw(<ChannelList openBeside="wechat" onOpen={() => {}} onMarketItem={() => {}} />);
    expect(row("WeChat with a long name").getAttribute("aria-current")).toBe("true");
    // the same id of another assistant, named in the address, is not this one's
    await draw(<ConnectorList openBeside="github@home" onOpen={() => {}} />);
    expect(host.querySelectorAll("[aria-current]")).toHaveLength(0);
    await draw(<ChannelList openBeside="wechat@home" onOpen={() => {}} onMarketItem={() => {}} />);
    expect(host.querySelectorAll("[aria-current]")).toHaveLength(0);
  });
});
