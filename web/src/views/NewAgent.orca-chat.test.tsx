// @vitest-environment jsdom
// "New agent" says where a start goes when the computer starts new Agents as Orca chats (#17650): the hub's default_opens,
// while the start keeps the default; nothing once a launcher is picked, in plan mode, or from a hub that says nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import type { Launcher, Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[] }));
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));

import { NewAgent } from "./NewAgent";
import { resetModelListsForTests } from "../lib/session-models";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const LAUNCHERS: Launcher[] = [
  { name: "claude" },
  { name: "orca-claude", display_name: "Orca chat · Claude Code", source: "orca" },
];
const PURPOSE = {
  purpose: { id: "agent.new", effective: { provider: "launcher.claude" } },
  providers: [{ id: "launcher.claude", name: "Claude Code", kind: "launcher" }],
};
const OPENS = { launcher: "orca-claude", display_name: "Orca chat · Claude Code", permission_modes: ["default", "acceptEdits", "auto", "bypassPermissions"] };

let opens: unknown;
let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); });
const q = (sel: string) => document.body.querySelector<HTMLElement>(sel);
const note = () => q("[data-testid=new-agent-orca-chat]");
const pill = () => q("[data-testid=new-agent-model] [data-model-pill]")!;
const row = (key: string) => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")].find((r) => r.dataset.panelRow === key)!;

function open() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<NewAgent open onClose={() => {}} />));
}

beforeEach(() => {
  resetModelListsForTests();
  opens = OPENS;
  state.machines = [{ id: "mac", name: "Mac", online: true }];
  localStorage.clear();
  localStorage.setItem("new.machine", "mac");
  localStorage.setItem("new.cwd.mac", "/code/app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json(PURPOSE);
    if (url.includes("/launchers/") && url.includes("/models")) return json({ models: [] });
    if (url.includes("/launchers")) return json({ launchers: LAUNCHERS, default: { provider: "claude", model: "" }, ...(opens ? { default_opens: opens } : {}) });
    return json({ path: "/code", entries: [] });
  }));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("New agent: Orca chats as the default (#17650)", () => {
  it("says the default start opens as the hub's Orca chat, until a launcher is picked", async () => {
    open();
    await flush();
    expect(note()?.textContent).toBe(t("newagent.opens_orca_chat", { name: "Orca chat · Claude Code" }));
    act(() => pill().click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    for (const header of document.body.querySelectorAll<HTMLButtonElement>('[data-panel-launcher][aria-expanded="false"]')) act(() => header.click());
    await flush();
    act(() => row("model:claude\ndefault").click());
    await flush();
    expect(note()).toBeNull();
  });

  it("says Orca's own model runs when the default's does not go with it", async () => {
    opens = { ...OPENS, dropped: true };
    open();
    await flush();
    expect(note()?.textContent).toBe(t("newagent.opens_orca_chat_own", { name: "Orca chat · Claude Code" }));
  });

  it("says nothing in plan mode, or when the hub says nothing", async () => {
    localStorage.setItem("new.mode.mac", "plan");
    open();
    await flush();
    expect(note()).toBeNull();
    act(() => root.unmount());
    host.remove();
    localStorage.removeItem("new.mode.mac");
    opens = undefined;
    open();
    await flush();
    expect(note()).toBeNull();
  });
});
