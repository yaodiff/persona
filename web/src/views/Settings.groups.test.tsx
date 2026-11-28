// @vitest-environment jsdom
// Settings' three groups (docs/assistant/managers.md "The current assistant"): "<name>'s settings" with the current
// assistant's avatar and a Switch that opens the sidebar's list, "This device" and "Hub", in that order, with their lines;
// the first group's pages ask the current assistant only, read again for another, and an item of theirs open when the
// pick changes goes back to its list.
import { installedMailbox, preloadMailbox } from "../test/installed-mailbox";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const viewport = vi.hoisted(() => ({ phone: false, lark: false }));
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ get matches() { return viewport.phone; }, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (state: Record<string, unknown>) => unknown) => select({ features: ["channels", "extension_manager", "connectors", ...(viewport.lark ? ["lark"] : [])], machines: [], sessions: {}, interactions: [], mutes: { spaces: [], sessions: [] }, conn: "online", link: { networkUp: true, restarting: false }, hubName: "", mesh: [], loaded: true }),
}));
vi.mock("./settings/ShortcutSettings", () => ({ ShortcutSettings: () => <div data-page="shortcuts" /> }));
vi.mock("./settings/NotificationSettings", () => ({ NotificationSettings: () => <div data-page="notifications" /> }));
vi.mock("./settings/ModelConfiguration", () => ({ ModelConfiguration: () => <div data-page="model-config" /> }));
vi.mock("./settings/KnowledgeSelection", () => ({ KnowledgeSelection: () => <div data-page="knowledge-selection" /> }));
vi.mock("./settings/TranslationSettings", () => ({ TranslationSettings: () => <div data-page="translation" /> }));
vi.mock("./settings/AssistantSettings", () => ({ AssistantSettings: () => <div data-page="persona" /> }));
vi.mock("./settings/SidebarPages", () => ({ SidebarPagesGroup: () => <div data-page="sidebar-pages" /> }));
vi.mock("./settings/StartPage", () => ({ StartPageGroup: () => <div data-page="start-page" /> }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));
vi.mock("./settings/AgentSettings", () => ({ AgentSettings: ({ agent }: { agent: string }) => <div data-page="agent" data-agent={agent} /> }));
vi.mock("./settings/Extensions", () => ({
  Extensions: ({ open, prefill }: { open: string | null; prefill?: unknown }) => <div data-page="extensions" data-open={open ?? ""} data-prefill={prefill ? JSON.stringify(prefill) : ""} />,
}));
vi.mock("./settings/FeishuBot", () => ({ FeishuBot: () => null }));
vi.mock("./settings/HubConnector", () => ({ ConnectorList: () => <div data-page="connector-list" />, ConnectorDetail: () => <div data-page="connector-detail" /> }));
vi.mock("./settings/SentSettings", () => ({ SentSettings: () => <div data-page="sent" /> }));
vi.mock("./settings/HubSettings", () => ({ HubSettings: () => <div data-page="hub" /> }));
vi.mock("./settings/AgentGroup", () => ({ AgentGroup: () => <div data-page="agent-group" /> }));

import { t } from "../i18n";
import { chooseManager, resetManagerChoice } from "../lib/manager-choice";
import { resetRoster, setRoster, type ManagerList } from "../lib/managers";
import { Settings } from "./Settings";
import { loadPages } from "../lib/lazy-page";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, builtin: true },
    { id: "m-1", name: "工作", online: true },
    { id: "m-2", name: "家里", online: false },
  ],
};

let host: HTMLDivElement;
let root: Root;
let calls: { path: string; manager: string | null }[];
// what the hub answers GET /managers (the roster is read again when the page subscribes to it)
let roster: ManagerList | null;
// the current assistant has no route for the chat connector: its manager answers 404
const larkAbsent = { on: false };
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
// Extensions and Connectors are chunks of their own (Settings.tsx, lazyPage): in before the pages render
beforeAll(() => loadPages());

beforeEach(() => {
  viewport.phone = false;
  viewport.lark = false;
  localStorage.clear();
  resetManagerChoice();
  resetRoster();
  calls = [];
  roster = ROSTER;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => {
    const path = new URL(String(input), "http://hub.example.com").pathname;
    calls.push({ path, manager: new Headers(init.headers).get("X-Persona-Manager") });
    if (path === "/api/v1/agents/triage") return Promise.resolve(json({ triage: true }));
    if (path === "/api/v1/lark/status" && larkAbsent.on) return Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404, headers: { "Content-Type": "application/json" } }));
    if (path === "/api/v1/lark/status") return Promise.resolve(json({ configured: true, connected: true }));
    if (path === "/api/v1/lark/message-display") return Promise.resolve(json({ anti_recall: false, show_edit_history: false, account_id: "a1" }));
    if (path === "/api/v1/mail/accounts") return Promise.resolve(json({ accounts: [] }));
    if (path === "/api/v1/managers") return Promise.resolve(roster ? json(roster) : new Response("{}", { status: 404 }));
    return Promise.resolve(json({ accounts: [], connectors: [], channels: [], extensions: [] }));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  resetRoster();
  vi.unstubAllGlobals();
});
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const mount = async (url: string) => {
  history.replaceState(null, "", url);
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
  await flush();
};
const switchTo = async (id: string | null) => {
  await act(async () => chooseManager(id));
  await flush();
};
const navGroups = () => [...host.querySelectorAll<HTMLElement>("[data-settings-nav-group]")];
const asked = (path: string) => calls.filter((c) => c.path === path).map((c) => c.manager);

describe("Settings' groups", () => {
  it("are the current assistant's, this device's and the hub's, in that order, with their titles and lines, then Labs untitled", async () => {
    setRoster(ROSTER);
    await mount("/settings?tab=shortcuts");
    const groups = navGroups();
    expect(groups.map((g) => g.dataset.settingsNavGroup)).toEqual(["assistant", "device", "hub", "labs"]);
    expect(groups[0].textContent).toContain(t("settings-nav.group_assistant", { name: "助手" }));
    expect(groups[0].querySelector("[data-assistant-avatar]")?.getAttribute("data-assistant-avatar")).toBe("assistant");
    expect(groups[0].querySelector("[data-settings-switch]")?.textContent).toBe(t("settings-nav.switch"));
    expect(groups[0].textContent).toContain(t("settings-nav.assistant_footer", { name: "助手" }));
    expect(groups[1].textContent).toContain(t("settings-nav.group_device"));
    expect(groups[2].textContent).toContain(t("settings-nav.group_hub"));
    expect(groups[2].textContent).toContain(t("settings-nav.hub_footer"));
    // the rows: the assistant's profile in the first, the assistant list in the hub's, never called like a group
    expect(groups[0].textContent).toContain(t("settings-nav.tab_profile"));
    expect(groups[2].textContent).toContain(t("settings-nav.tab_managers"));
    // Labs on its own: only its row, no title or line of a group
    expect(groups[3].textContent).toBe(t("settings-nav.tab_labs"));
  });

  it("are the phone's first page too", async () => {
    viewport.phone = true;
    setRoster(ROSTER);
    await mount("/settings");
    const sections = [...host.querySelectorAll<HTMLElement>("[data-settings-group^='nav-']")];
    expect(sections.map((s) => s.dataset.settingsGroup)).toEqual(["nav-assistant", "nav-device", "nav-hub", "nav-labs"]);
    expect(sections[0].textContent).toContain(t("settings-nav.group_assistant", { name: "助手" }));
    expect(sections[0].querySelector("[data-settings-switch]")).not.toBeNull();
  });

  it("open the sidebar's list from Switch, and follow the assistant picked there", async () => {
    setRoster(ROSTER);
    await mount("/settings?tab=shortcuts");
    await act(async () => host.querySelector<HTMLButtonElement>("[data-settings-switch]")!.click());
    await flush();
    const rows = [...document.body.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(rows.map((r) => r.textContent?.includes("工作"))).toContain(true);
    expect(rows).toHaveLength(3);
    const actions = [...document.body.querySelectorAll<HTMLElement>("[role=menuitem]")].map((r) => r.textContent);
    expect(actions).toEqual(expect.arrayContaining([t("sidebar.new_assistant"), t("sidebar.manage_assistants")]));
    await act(async () => rows.find((r) => r.textContent?.includes("工作"))!.click());
    await flush();
    expect(navGroups()[0].textContent).toContain(t("settings-nav.group_assistant", { name: "工作" }));
    expect(navGroups()[0].textContent).toContain(t("settings-nav.assistant_footer", { name: "工作" }));
  });

  it("show the title and Switch with one assistant, and no Switch on a hub without a roster", async () => {
    roster = { default: "assistant", managers: [ROSTER.managers[0]] };
    setRoster(roster);
    await mount("/settings?tab=shortcuts");
    expect(navGroups()[0].textContent).toContain(t("settings-nav.group_assistant", { name: "助手" }));
    expect(navGroups()[0].querySelector("[data-settings-switch]")).not.toBeNull();
    await act(async () => root.render(null));
    roster = null; // a hub from before the roster: 404
    resetRoster();
    await mount("/settings?tab=shortcuts");
    expect(navGroups()[0].textContent).toContain(t("settings-nav.group_assistant", { name: "Persona" }));
    expect(navGroups()[0].querySelector("[data-settings-switch]")).toBeNull();
  });
});

describe("the current assistant's pages", () => {
  it("ask its manager for session triage, and ask again for another assistant made current", async () => {
    setRoster(ROSTER);
    await mount("/settings?tab=persona");
    expect(asked("/api/v1/agents/triage")).toEqual([null]);
    await switchTo("m-1");
    expect(asked("/api/v1/agents/triage")).toEqual([null, "m-1"]);
    await switchTo(null);
    expect(asked("/api/v1/agents/triage")).toEqual([null, "m-1", null]);
  });

  it("ask the current assistant for its mail accounts on Connectors, and leave other pages' requests alone", async () => {
    await preloadMailbox("Settings");
    setRoster(ROSTER);
    await switchTo("m-1");
    await mount("/settings?tab=connectors");
    expect(asked("/api/v1/mail/accounts")).toEqual(installedMailbox ? ["m-1"] : []);
    // a page outside the group: nothing pinned
    await act(async () => root.render(null));
    calls = [];
    await mount("/settings?tab=general");
    expect(calls.every((c) => c.manager === null)).toBe(true);
  });

  it("ask the current assistant for every route of the chat connector, message display included", async () => {
    viewport.lark = true;
    setRoster(ROSTER);
    await switchTo("m-1");
    await mount("/settings?tab=connectors");
    expect(asked("/api/v1/lark/status")).toEqual(["m-1"]);
    expect(asked("/api/v1/lark/accounts")).toEqual(["m-1"]);
    expect(asked("/api/v1/lark/message-display").length).toBeGreaterThan(0);
    expect(asked("/api/v1/lark/message-display").every((m) => m === "m-1")).toBe(true);
  });

  it("show the chat connector's group as not set up, with no display switches, for an assistant without it", async () => {
    viewport.lark = true;
    larkAbsent.on = true;
    try {
      setRoster(ROSTER);
      await switchTo("m-1");
      await mount("/settings?tab=connectors");
      const group = host.querySelector('[data-anchor="lark"], #settings-lark') ?? host;
      expect(group.textContent).toContain(t("settings-connectors.not_configured"));
      expect(host.textContent).not.toContain(t("settings-connectors.lark_status_failed", { error: "" }).slice(0, 4));
      expect(host.textContent).not.toContain(t("settings-larkdisplay.title"));
    } finally {
      larkAbsent.on = false;
    }
  });

  it("go back to the list when an item is open and another assistant becomes current", async () => {
    setRoster(ROSTER);
    await mount("/settings?tab=extensions&extension=example");
    expect(host.querySelector('[data-page="extensions"]')?.getAttribute("data-open")).toBe("example");
    await switchTo("m-1");
    expect(host.querySelector('[data-page="extensions"]')?.getAttribute("data-open")).toBe("");
    expect(location.search).toBe("?tab=extensions");
  });

  it("passes an extension settings link's proposed values to the page", async () => {
    setRoster(ROSTER);
    await mount(`/settings?tab=extensions&extension=example&set=${encodeURIComponent(JSON.stringify({ machine: "studio" }))}`);
    const page = host.querySelector('[data-page="extensions"]');
    expect(page?.getAttribute("data-open")).toBe("example");
    expect(JSON.parse(page?.getAttribute("data-prefill") ?? "null")).toEqual({ machine: "studio" });
  });

  it("keep a page of this device's as it is when the pick changes", async () => {
    setRoster(ROSTER);
    await mount("/settings?tab=shortcuts");
    const page = host.querySelector('[data-page="shortcuts"]');
    await switchTo("m-1");
    expect(host.querySelector('[data-page="shortcuts"]')).toBe(page);
  });
});
