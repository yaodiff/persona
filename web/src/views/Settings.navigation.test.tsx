// @vitest-environment jsdom
// Settings category navigation keeps the existing URLs and phone history while moving beside the content.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const viewport = vi.hoisted(() => ({
  phone: false, integrationsV2: false, inboxSources: false, selected: undefined as string | undefined,
  otherModes: {} as Record<string, boolean | null>,
}));
const profileReads = vi.hoisted(() => vi.fn((manager: string) => ({
  manager: viewport.otherModes[manager] === null ? "assistant" : manager,
  integrations_v2: viewport.otherModes[manager] ?? viewport.integrationsV2,
})));
vi.hoisted(() => {
  // a phone matches the narrow queries; a wide screen the wide ones (the list beside an item, from 900 px)
  window.matchMedia = ((query: string) => ({ get matches() { return /min-width/.test(query) ? !viewport.phone : viewport.phone; }, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  getState: () => ({ features: viewport.inboxSources ? ["inbox_sources"] : [] }),
  useStore: (select: (state: {
    features: string[];
    machines: unknown[];
    conn: string;
    link: { networkUp: boolean; restarting: boolean };
    hubName: string;
    mesh: unknown[];
    loaded: boolean;
    assistant: { manager?: string; integrations_v2: boolean };
  }) => unknown) =>
    select({
      features: ["channels", "extension_manager", "connectors", ...(viewport.inboxSources ? ["inbox_sources", "inbox_sources_graph"] : [])],
      machines: [],
      conn: "online",
      link: { networkUp: true, restarting: false },
      hubName: "",
      mesh: [],
      loaded: true,
      assistant: { manager: viewport.selected, integrations_v2: viewport.integrationsV2 },
    }),
}));
vi.mock("./settings/ShortcutSettings", () => ({ ShortcutSettings: () => <div data-page="shortcuts" /> }));
vi.mock("./settings/NotificationSettings", () => ({ NotificationSettings: () => <div data-page="notifications" /> }));
vi.mock("./settings/ModelConfiguration", () => ({
  ModelConfiguration: ({ open, onOpen }: { open: string | null; onOpen: (id: string | null) => void }) => (
    <div data-page="model-config" data-open={open}>
      <button type="button" data-purpose="open" onClick={() => onOpen("agent.triage")} />
      <button type="button" data-purpose="close" onClick={() => onOpen(null)} />
    </div>
  ),
}));
vi.mock("./settings/KnowledgeSelection", () => ({
  KnowledgeSelection: () => (
    <div data-page="knowledge-selection">
      <section data-settings-group="knowledge-items"><div /></section>
    </div>
  ),
}));
vi.mock("./settings/TranslationSettings", () => ({ TranslationSettings: () => <div data-page="translation" /> }));
vi.mock("./settings/AssistantSettings", () => ({
  AssistantSettings: () => (
    <div data-page="persona">
      <section data-settings-group="repositories"><div /></section>
    </div>
  ),
}));
vi.mock("./settings/SidebarPages", () => ({ SidebarPagesGroup: () => <div data-page="sidebar-pages" /> }));
vi.mock("./settings/StartPage", () => ({ StartPageGroup: () => <div data-page="start-page" /> }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));
vi.mock("../lib/api", async (original) => ({
  ...(await original<typeof import("../lib/api")>()),
  api: (path: string, options?: RequestInit) => {
    const manager = new Headers(options?.headers).get("X-Persona-Manager");
    return Promise.resolve(path === "/assistant" && manager ? profileReads(manager) : { accounts: [] });
  },
}));
vi.mock("./settings/AgentSettings", () => ({ AgentSettings: ({ agent }: { agent: string }) => <div data-page="agent" data-agent={agent} /> }));
vi.mock("./settings/Extensions", () => ({
  Extensions: ({ open, onOpen, headerBack, manager, prefill }: {
    open: string | null; onOpen: (id: string | null) => void; headerBack?: boolean; manager?: string; prefill?: unknown;
  }) => (
    <div data-page="extensions" data-open={open} data-manager={manager} data-prefill={JSON.stringify(prefill)} data-header-back={headerBack ? "yes" : "no"}>
      <button type="button" data-item="open" onClick={() => onOpen("example")} />
      <button type="button" data-item="close" onClick={() => onOpen(null)} />
    </div>
  ),
}));
vi.mock("./settings/FeishuBot", () => ({ FeishuBot: () => null }));
vi.mock("./settings/HubConnector", () => ({
  ConnectorList: ({ onOpen }: { onOpen: (id: string) => void }) => <div data-page="connector-list"><button type="button" data-connector="github" onClick={() => onOpen("github")} /></div>,
  // its own Back only where it is given one
  ConnectorDetail: ({ onBack }: { onBack?: () => void }) => <div data-page="connector-detail">{onBack && <button type="button" data-own-back onClick={onBack} />}</div>,
}));
vi.mock("./settings/SentSettings", () => ({ SentSettings: () => <div data-page="sent" /> }));
vi.mock("./settings/Integrations", () => ({
  Integrations: ({ route, onOpen, parentView, onBack, onSettings }: IntegrationsProps) => (
    <div data-page="integrations" data-open={new URL(integrationHref(route), location.origin).searchParams.get("integration") ?? ""}
      data-manager={integrationManager(route.scope)} data-parent={parentView?.kind ?? ""} data-view={route.view.kind}>
      <button type="button" data-related-connector onClick={() => onOpen({ ...route, view: { kind: "connector", id: "larkcli" } })} />
      <button type="button" data-related-sources onClick={() => onOpen({ ...route, view: { kind: "source", state: "scoped" } })} />
      <button type="button" data-back-parent onClick={onBack} />
      <button type="button" data-integrations-sources onClick={() => onOpen({ ...route, view: { kind: "data-flow" } })} />
      <button type="button" data-integrations-repos onClick={() => onSettings?.("settings/persona#repositories", integrationManager(route.scope))} />
      <button type="button" data-integrations-kb onClick={() => onSettings?.("settings/knowledge#knowledge-items", integrationManager(route.scope))} />
    </div>
  ),
}));
vi.mock("./subscriptions/SourcesUI", () => ({
  SourcesUI: ({ state }: { state?: string }) => <div data-page="sources" data-state={state ?? ""} />,
}));

import { enabledNavItems, registerModule, resetModulesForTest } from "../core/module-host";
import { forgetModuleSwitches, MODULE_SWITCH_KEY } from "../core/module-switch";
import { initI18n, t } from "../i18n";
import { Settings } from "./Settings";
import { chooseManager, chosenManager, resetManagerChoice } from "../lib/manager-choice";
import { navigate } from "../lib/router";
import type { IntegrationsProps } from "./settings/Integrations";
import { integrationHref, integrationManager } from "./settings/integrations-route";
import { loadPages } from "../lib/lazy-page";
import { FROM_LIST, openedFromSub } from "./settings/nav";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
// Extensions and Connectors are chunks of their own (Settings.tsx, lazyPage): in before the pages render
beforeAll(() => loadPages());

beforeEach(() => {
  viewport.phone = false;
  viewport.integrationsV2 = false;
  viewport.inboxSources = false;
  viewport.selected = undefined;
  viewport.otherModes = {};
  profileReads.mockClear();
  localStorage.clear();
  resetManagerChoice();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const mount = async (url: string) => {
  history.replaceState(null, "", url);
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
};
const nav = () => host.querySelector("nav")!;
/** Runs `go`, then waits for the Back it causes (history.back() is asynchronous). */
const popped = (go: () => void) => act(async () => {
  const pop = new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
  });
  go();
  await pop;
});
const category = (label: string) => [...nav().querySelectorAll("button")].find((button) => button.textContent?.startsWith(label))!;

describe("Settings category navigation", () => {
  it("shows a neutral source link at the foot of General in every language and viewport, without touching old prompt data", async () => {
    const legacy = JSON.stringify({ launches: 20, welcomed: false, done: false });
    localStorage.setItem("persona.starPrompt", legacy);
    try {
      for (const phone of [false, true]) {
        viewport.phone = phone;
        for (const lang of ["en", "zh-Hans", "ja"]) {
          await initI18n(lang);
          await mount("/settings?tab=general");
          const about = host.querySelector('[data-settings-group="about-persona"]')!;
          expect(about.querySelector("h2")?.textContent).toBe(t("settings.about_persona"));
          expect(about.textContent).toContain(t("settings.source_code"));
          const link = about.querySelector("a")!;
          expect(link.textContent?.trim()).toBe("github.com/nwverse/persona");
          expect(link.href).toBe("https://github.com/nwverse/persona");
          expect(link.target).toBe("_blank");
          expect(link.rel).toBe("noopener noreferrer");
          expect(about.nextElementSibling).toBeNull();
          link.focus();
          expect(document.activeElement).toBe(link);
          link.addEventListener("click", (e) => e.preventDefault(), { once: true });
          act(() => link.click());
          expect(localStorage.getItem("persona.starPrompt")).toBe(legacy);
        }
      }
    } finally {
      await initI18n("zh-Hans");
    }
  });
  it("omits Optional features when the build registers no modules", async () => {
    await mount("/settings?tab=general");
    expect(host.querySelector('[data-configuration="features"]')).toBeNull();
  });

  it("switches a module off and back on from General and reflects another tab's choice", async () => {
    forgetModuleSwitches();
    registerModule({
      id: "garden", title: () => "Garden", description: () => "A synthetic module",
      navItems: [{ key: "garden", path: "/garden", icon: "heart", label: () => "Garden", after: "inbox" }],
      load: async () => ({}),
    });
    try {
      await mount("/settings?tab=general");
      const toggle = () => host.querySelector<HTMLButtonElement>('[data-configuration-field="garden"] [role="switch"]')!;
      expect(toggle()).not.toBeNull();
      expect(toggle().getAttribute("aria-checked")).toBe("true");
      expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);

      await act(async () => toggle().click());
      expect(localStorage.getItem(MODULE_SWITCH_KEY)).toBe('["garden"]');
      expect(enabledNavItems()).toEqual([]);

      await act(async () => toggle().click());
      expect(localStorage.getItem(MODULE_SWITCH_KEY)).toBeNull();
      expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);

      await act(async () => {
        localStorage.setItem(MODULE_SWITCH_KEY, '["garden"]');
        window.dispatchEvent(new StorageEvent("storage", { key: MODULE_SWITCH_KEY, newValue: '["garden"]' }));
      });
      expect(toggle().getAttribute("aria-checked")).toBe("false");
      expect(enabledNavItems()).toEqual([]);
    } finally {
      await act(async () => resetModulesForTest());
      localStorage.removeItem(MODULE_SWITCH_KEY);
      forgetModuleSwitches();
    }
  });

  it("controls Needs you auto-open with an off-by-default browser setting (#17061)", async () => {
    await initI18n("en");
    try {
      await mount("/settings?tab=general");
      const row = () => host.querySelector<HTMLElement>('[data-settings-row="needs-you-auto-open"]');
      const toggle = () => row()?.querySelector<HTMLButtonElement>('[role="switch"]');
      expect(row(), 'Auto-open "Needs you" on entering main chat').not.toBeNull();
      expect(row()?.textContent).toContain('Auto-open "Needs you" on entering main chat');
      expect(toggle()?.getAttribute("aria-checked")).toBe("false");
      expect(localStorage.getItem("persona.needsYouAutoOpen")).toBeNull();

      await act(async () => toggle()!.click());
      expect(toggle()?.getAttribute("aria-checked")).toBe("true");
      expect(localStorage.getItem("persona.needsYouAutoOpen")).toBe("1");

      await act(async () => root.render(null));
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      expect(toggle()?.getAttribute("aria-checked")).toBe("true");

      await act(async () => toggle()!.click());
      expect(toggle()?.getAttribute("aria-checked")).toBe("false");
      expect(localStorage.getItem("persona.needsYouAutoOpen")).toBeNull();
    } finally {
      await initI18n("zh-Hans");
    }
  });

  // audit item 17: General's hints were cut at one line ("切换后页面会重新载…"), at every width
  it.each([false, true])("lets General's hints wrap rather than cutting them (phone: %s)", async (phone) => {
    viewport.phone = phone;
    await mount("/settings?tab=general");
    const hints: [string, string][] = [
      ["language", t("language.hint")],
      ["text-size", t("ui-look.text_size_hint")],
      ["reply-motion", t("settings.reply_motion_hint")],
      ["needs-you-auto-open", t("settings.needs_you_auto_open_hint")],
      ["agent-details", t("context.details_hint")],
      ["install-app", t("settings.install_installed")],
    ];
    for (const [anchor, text] of hints) {
      const hint = [...host.querySelectorAll<HTMLElement>(`[data-settings-row="${anchor}"] div`)].find((d) => d.textContent === text);
      expect(hint, anchor).toBeTruthy();
      expect(hint!.classList.contains("truncate"), anchor).toBe(false);
    }
  });

  // #13398: General keeps the default appearance; the opt-in looks moved to Advanced › Appearance with no storage change
  it("moves the non-default looks to Advanced and keeps an existing choice", async () => {
    await mount("/settings?tab=general");
    let region = host.querySelector('[role="region"]')!;
    expect(region.querySelector('[data-settings-row="theme"]')).not.toBeNull();
    expect(region.querySelector('[data-settings-row="text-size"]')).not.toBeNull();
    expect(region.querySelector('[data-settings-row="theme-color"]')).toBeNull();
    expect(region.querySelector('[data-settings-row="font"]')).toBeNull();
    expect(region.querySelector('[data-settings-row="sidebar-icons"]')).toBeNull();

    await act(async () => category(t("settings-nav.tab_advanced")).click());
    expect(location.search).toBe("?tab=advanced");
    region = host.querySelector('[role="region"]')!;
    expect(region.getAttribute("aria-label")).toBe(t("settings-nav.tab_advanced"));
    expect(region.querySelector('[data-settings-group="appearance"]')).not.toBeNull();
    const color = region.querySelector('[data-settings-row="theme-color"]')!;
    expect(region.querySelector('[data-settings-row="font"]')).not.toBeNull();
    expect(region.querySelector('[data-settings-row="sidebar-icons"]')).not.toBeNull();
    // the default still stores nothing
    expect(color.querySelector('[aria-checked="true"]')?.getAttribute("aria-label")).toContain(t("ui-look.default"));

    const radio = (name: string) => [...host.querySelectorAll<HTMLButtonElement>('[data-settings-row="theme-color"] [role="radio"]')]
      .find((r) => r.getAttribute("aria-label")?.startsWith(name))!;
    await act(async () => radio(t("ui-look.coral")).click());
    expect(localStorage.getItem("persona.accent")).toBe("coral");
    expect(document.documentElement.dataset.accent).toBe("coral");

    // a reload lands on the same Advanced page with the same choice
    await act(async () => root.render(null));
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    expect(location.search).toBe("?tab=advanced");
    expect(radio(t("ui-look.coral")).getAttribute("aria-checked")).toBe("true");

    await act(async () => radio(t("ui-look.default")).click());
    expect(localStorage.getItem("persona.accent")).toBeNull();
  });

  it("opens knowledge selection as the current assistant's, with no manager in the address", async () => {
    await mount("/settings?tab=models&manager=remote");
    await act(async () => category(t("settings-nav.tab_knowledge")).click());
    expect(host.querySelector('[data-page="knowledge-selection"]')).not.toBeNull();
    expect(location.search).toBe("?tab=knowledge");
    expect(category(t("settings-nav.tab_knowledge")).getAttribute("aria-current")).toBe("page");
  });
  it("places desktop navigation beside a separately focusable, named content region", async () => {
    await mount("/settings?tab=shortcuts");
    const region = host.querySelector('[role="region"]')!;
    expect(host.querySelector("header nav")).toBeNull();
    expect(nav().parentElement!.nextElementSibling).toBe(region);
    expect(nav().getAttribute("aria-label")).toBe(t("settings.categories"));
    expect(nav().classList.contains("overflow-y-auto")).toBe(true);
    expect(region.classList.contains("overflow-y-auto")).toBe(true);
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(region.getAttribute("aria-label")).toBe(t("settings-nav.tab_shortcuts"));
    // every category controls the region (the current assistant's Switch opens its list of assistants instead)
    for (const button of nav().querySelectorAll<HTMLButtonElement>("button:not([data-settings-switch])")) {
      expect(button.type).toBe("button");
      expect(button.getAttribute("aria-controls")).toBe(region.id);
      expect(button.tabIndex).toBe(0);
    }
    expect(nav().querySelectorAll('[aria-current="page"]')).toHaveLength(1);
    expect(category(t("settings-nav.tab_shortcuts")).getAttribute("aria-current")).toBe("page");
  });

  it("changes the URL, current category and region together, and restores them on reload", async () => {
    await mount("/settings?tab=shortcuts");
    await act(async () => category(t("settings-nav.tab_notifications")).click());
    expect(location.search).toBe("?tab=notifications");
    expect(host.querySelector('[data-page="notifications"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_notifications")).getAttribute("aria-current")).toBe("page");
    expect(category(t("settings-nav.tab_shortcuts")).hasAttribute("aria-current")).toBe(false);
    expect(host.querySelector('[role="region"]')?.getAttribute("aria-label")).toBe(t("settings-nav.tab_notifications"));
    await act(async () => root.render(null));
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    expect(host.querySelector('[data-page="notifications"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_notifications")).getAttribute("aria-current")).toBe("page");
  });

  it("keeps a deep-linked configuration purpose, and the current assistant's page drops a manager from the address", async () => {
    await mount("/settings?tab=model-config&purpose=agent.triage&manager=example");
    expect(location.search).toBe("?tab=models&purpose=agent.triage&manager=example");
    expect(location.hash).toBe("#settings-model-config");
    expect(host.querySelector('[data-page="model-config"]')?.getAttribute("data-open")).toBe("agent.triage");
    await act(async () => category(t("settings-nav.tab_models")).click());
    expect(location.search).toBe("?tab=models");
  });

  it("redirects old agent, translation, and assistant links to their new categories", async () => {
    await mount("/settings?tab=codex");
    expect(location.pathname + location.search + location.hash).toBe("/settings?tab=agents#settings-codex");
    expect([...host.querySelectorAll('[data-page="agent"]')].map((el) => el.getAttribute("data-agent"))).toEqual(["claude", "codex", "agy", "omp", "grok"]);
    expect(host.querySelector("#settings-codex-body")?.hasAttribute("hidden")).toBe(false);
    expect(host.querySelector("#settings-claude-body")?.hasAttribute("hidden")).toBe(false);
    await act(async () => root.render(null));
    await mount("/settings?tab=translate");
    expect(location.pathname + location.search + location.hash).toBe("/settings?tab=general#settings-translation");
    expect(host.querySelector('[data-page="translation"]')).not.toBeNull();
    await act(async () => root.render(null));
    await mount("/settings?tab=assistant");
    expect(location.search).toBe("?tab=persona");
    expect(host.querySelector('[data-page="persona"]')).not.toBeNull();
  });

  it("starts with every agent open, and a heading folds just that one", async () => {
    await mount("/settings?tab=agents");
    const rows = [...host.querySelectorAll<HTMLButtonElement>("button[aria-expanded]:not([data-settings-switch])")];
    expect(rows.map((b) => b.textContent)).toEqual(["Claude Code", "Codex", "Antigravity", "omp", "Grok"]);
    expect(rows.map((b) => b.getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "true", "true"]);
    await act(async () => rows[1].click());
    expect(host.querySelector("#settings-codex-body")?.hasAttribute("hidden")).toBe(true);
    expect(host.querySelector("#settings-claude-body")?.hasAttribute("hidden")).toBe(false);
    await act(async () => rows[1].click());
    expect(host.querySelector("#settings-codex-body")?.hasAttribute("hidden")).toBe(false);
  });

  it("scrolls a shared old link to its section in the combined page", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=agy");
    expect(location.hash).toBe("#settings-agy");
    await act(async () => {});
    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("leaves the Models page where it is scrolled when a model purpose's dialog opens and closes", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    // the page scrolled to a section; the sidebar keeping its current category in view (block "nearest") is not one
    const toSection = () => scroll.mock.calls.filter(([options]) => options?.block === "start").length;
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=models");
    const page = host.querySelector<HTMLElement>('[role="region"]')!;
    const purpose = (name: string) => host.querySelector<HTMLButtonElement>(`[data-purpose="${name}"]`)!;
    page.scrollTop = 400;
    page.dispatchEvent(new Event("scroll"));
    await act(async () => purpose("open").click());
    expect(host.querySelector('[data-page="model-config"]')?.getAttribute("data-open")).toBe("agent.triage");
    expect(location.search + location.hash).toBe("?tab=models&purpose=agent.triage");
    expect(page.scrollTop).toBe(400);
    await act(async () => purpose("close").click());
    expect(host.querySelector('[data-page="model-config"]')?.hasAttribute("data-open")).toBe(false);
    expect(location.search + location.hash).toBe("?tab=models");
    expect(page.scrollTop).toBe(400);
    expect(toSection()).toBe(0);
  });

  it("scrolls a link to a model purpose to its section once, and closing the dialog leaves the page there", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const toSection = () => scroll.mock.calls.filter(([options]) => options?.block === "start").length;
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=models&purpose=agent.triage#settings-model-config");
    await act(async () => {});
    expect(toSection()).toBe(1);
    const page = host.querySelector<HTMLElement>('[role="region"]')!;
    page.scrollTop = 400;
    page.dispatchEvent(new Event("scroll"));
    await act(async () => host.querySelector<HTMLButtonElement>('[data-purpose="close"]')!.click());
    expect(page.scrollTop).toBe(400);
    expect(toSection()).toBe(1);
  });

  it("preserves legacy extension deep links", async () => {
    await mount("/settings?tab=plugins&plugin=example");
    // wide, the list is beside it: the extension is the second column's
    expect([...host.querySelectorAll('[data-page="extensions"]')].at(-1)?.getAttribute("data-open")).toBe("example");
    expect(category(t("settings-nav.tab_extensions")).getAttribute("aria-current")).toBe("page");
  });

  it("opens phone categories above the list and follows browser Back to that list", async () => {
    viewport.phone = true;
    await mount("/settings");
    expect(host.querySelector('[role="region"]')).toBeNull();
    expect(host.querySelector("nav")?.classList.contains("w-56")).toBe(false);
    await act(async () => category(t("settings-nav.tab_shortcuts")).click());
    expect(location.search).toBe("?tab=shortcuts");
    expect(history.state).toEqual({ [FROM_LIST]: true });
    expect(host.querySelector("nav")).toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("settings-nav.tab_shortcuts"));
    const back = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!;
    const historyBack = vi.spyOn(history, "back").mockImplementation(() => {});
    await act(async () => back.click());
    expect(historyBack).toHaveBeenCalledOnce();
    await act(async () => {
      history.replaceState(null, "", "/settings");
      dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(host.querySelector("nav")).not.toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("settings.title"));
  });

  it.each([false, true])("opens Sent from the Connectors bottom row and returns to Connectors (phone: %s)", async (phone) => {
    viewport.phone = phone;
    await mount("/settings?tab=connectors");
    expect(nav()?.textContent ?? "").not.toContain(t("settings-sent.entry_label"));
    const entry = host.querySelector('[data-settings-group="sent"]')!;
    expect(entry.parentElement?.lastElementChild).toBe(entry);
    expect(entry.textContent).toContain(t("settings-sent.hint"));
    await act(async () => entry.querySelector("button")!.click());
    expect(location.search).toBe("?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
    expect(host.querySelector('[data-page="connector-detail"]')).toBeNull();
    expect(host.querySelector('[data-settings-group="sent"]')).toBeNull();
    const own = [...host.querySelectorAll<HTMLButtonElement>('[role="region"] button')].find(b => b.textContent === t("settings-hubconnector.back"));
    // a phone's top bar has the one Back (audit item 64); wider, the page has its own
    // named for where it goes: Connectors
    const header = host.querySelector<HTMLButtonElement>(`header button[aria-label="${t("settings.back_to", { name: t("settings-nav.tab_connectors") })}"]`);
    expect(!!own).toBe(!phone);
    await popped(() => (phone ? header! : own!).click());
    expect(location.search).toBe("?tab=connectors");
    expect(host.querySelector('[data-settings-group="sent"]')).not.toBeNull();
  });

  // audit item 64: a connector's page on a phone had the top bar's Back and one of its own under it
  it("gives a phone's connector page one Back, the top bar's, which returns to Connectors, then to the list", async () => {
    viewport.phone = true;
    history.pushState(null, "", "/");
    await mount("/settings");
    await act(async () => category(t("settings-nav.tab_connectors")).click());
    await act(async () => host.querySelector<HTMLButtonElement>('[data-connector="github"]')!.click());
    expect(location.search).toBe("?tab=connectors&connector=github");
    expect(host.querySelector('[data-page="connector-detail"]')).not.toBeNull();
    expect(host.querySelector("[data-own-back]")).toBeNull();
    // one way back, named for where it goes: Connectors, then (from Connectors) Settings
    const back = () => host.querySelector<HTMLButtonElement>("header button:first-child")!;
    const toConnectors = t("settings.back_to", { name: t("settings-nav.tab_connectors") });
    expect(back().getAttribute("aria-label")).toBe(toConnectors);
    expect(host.querySelectorAll(`button[aria-label="${toConnectors}"], button[aria-label="${t("settings.back")}"]`)).toHaveLength(1);
    await popped(() => back().click());
    expect(location.search).toBe("?tab=connectors");
    expect(host.querySelector('[data-page="connector-list"]')).not.toBeNull();
    expect(back().getAttribute("aria-label")).toBe(t("settings.back"));
    await popped(() => back().click());
    expect(location.search).toBe("");
    expect(host.querySelector("nav")).not.toBeNull();
  });

  // a Lark bot channel's settings link opens Connectors with the bot's dialog over the list: not an item's page, so the
  // top bar's Back is the list's, not "Back to Connectors" (which took two presses to leave)
  it("leaves the top bar going to the Settings list under the Lark bot's dialog on a phone", async () => {
    viewport.phone = true;
    history.pushState(null, "", "/");
    await mount("/settings?tab=connectors&connector=feishu-bot");
    expect(host.querySelector('[data-page="connector-list"]')).not.toBeNull();
    const back = host.querySelector<HTMLButtonElement>("header button:first-child")!;
    expect(back.getAttribute("aria-label")).toBe(t("settings.back"));
    await act(async () => back.click());
    expect(location.search).toBe("");
    expect(host.querySelector("nav")).not.toBeNull();
  });

  // A direct link has no visited parent. The phone's header returns to that assistant's directory.
  it.each(["-market/example-market/example", "-markets", "-market/example-market/example@work", "example/ui/main", "-market"])(
    "returns from a direct legacy extension link %s to its assistant's directory", async (from) => {
      viewport.phone = true;
      history.pushState(null, "", "/");
      await mount(`/settings?tab=extensions&extension=${encodeURIComponent(from)}`);
      const page = () => [...host.querySelectorAll("[data-page=extensions]")].at(-1)!;
      expect(page().getAttribute("data-header-back")).toBe("yes");
      const back = host.querySelector<HTMLButtonElement>("header button:first-child")!;
      expect(back.getAttribute("aria-label")).toBe(t("settings.back_to", { name: t("settings-nav.tab_extensions") }));
      await act(async () => back.click());
      expect(page().getAttribute("data-open")).toBeNull();
      const query = new URLSearchParams(location.search);
      expect(query.get("extension")).toBeNull();
      expect(query.get("manager")).toBe(from.includes("@") ? "work" : null);
    },
  );

  it("keeps a connector page's own Back on a wide screen and leaves legacy scroll container unchanged", async () => {
    await mount("/settings?tab=connectors&connector=github");
    expect(host.querySelector("[data-own-back]")).not.toBeNull();
    const region = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(region.hasAttribute("data-integrations-split-host")).toBe(false);
    expect(region.classList.contains("overflow-y-auto")).toBe(true);
    expect(region.classList.contains("overscroll-contain")).toBe(true);
  });

  it.each(["/settings?tab=sent", "/settings/sent", "/settings/sent/"])("redirects %s to the secondary Sent page", async (url) => {
    await mount(url);
    expect(location.pathname + location.search).toBe("/settings?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_connectors")).getAttribute("aria-current")).toBe("page");
  });

  it("normalizes a legacy Sent URL reached by browser history while Settings stays mounted", async () => {
    await mount("/settings?tab=connectors");
    await act(async () => {
      history.replaceState(null, "", "/settings/sent");
      dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(location.pathname + location.search).toBe("/settings?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
  });

  // An item's page is pushed over its category's page (#2800): Back steps out of it one level, its own Back leaves no
  // entry to come back to, and switching categories adds none.
  const item = (which: "open" | "close") => [...host.querySelectorAll<HTMLButtonElement>(`[data-item="${which}"]`)].at(-1)!;
  const browserBack = () => popped(() => history.back());
  it.each([
    { name: "wide: Back from an extension returns to the extension list", phone: false, url: "/settings?tab=extensions", steps: [
      { go: () => act(async () => item("open").click()), at: "?tab=extensions&extension=example", entries: 1 },
      { go: browserBack, at: "?tab=extensions", entries: 1 },
      { go: () => act(async () => item("open").click()), at: "?tab=extensions&extension=example", entries: 1 },
      { go: () => popped(() => item("close").click()), at: "?tab=extensions", entries: 1 },
    ] },
    { name: "phone: Back goes from an extension to the extension list, then to the Settings list", phone: true, url: "/settings", steps: [
      { go: () => act(async () => category(t("settings-nav.tab_extensions")).click()), at: "?tab=extensions", entries: 1 },
      { go: () => act(async () => item("open").click()), at: "?tab=extensions&extension=example", entries: 2 },
      { go: browserBack, at: "?tab=extensions", entries: 2 },
      { go: browserBack, at: "", entries: 2 },
    ] },
    // the top bar's Back steps out one level, as the page's own Back it stands in for did (audit item 64)
    { name: "phone: the header's Back goes from an extension to the extension list, then to the Settings list", phone: true, url: "/settings", steps: [
      { go: () => act(async () => category(t("settings-nav.tab_extensions")).click()), at: "?tab=extensions", entries: 1 },
      { go: () => act(async () => item("open").click()), at: "?tab=extensions&extension=example", entries: 2 },
      { go: () => popped(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back_to", { name: t("settings-nav.tab_extensions") })}"]`)!.click()), at: "?tab=extensions", entries: 2 },
      { go: () => popped(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!.click()), at: "", entries: 2 },
    ] },
    { name: "switching categories adds no entries", phone: false, url: "/settings?tab=extensions", steps: [
      { go: () => act(async () => category(t("settings-nav.tab_general")).click()), at: "", entries: 0 },
      { go: () => act(async () => category(t("settings-nav.tab_notifications")).click()), at: "?tab=notifications", entries: 0 },
      { go: () => act(async () => category(t("settings-nav.tab_extensions")).click()), at: "?tab=extensions", entries: 0 },
    ] },
  ])("$name", async ({ phone, url, steps }) => {
    viewport.phone = phone;
    history.pushState(null, "", "/"); // drops forward entries other cases left, so the length counts this one's
    await mount(url);
    const start = history.length;
    for (const step of steps) {
      await step.go();
      expect(location.search).toBe(step.at);
      expect(history.length - start).toBe(step.entries);
      expect([...host.querySelectorAll("[data-page=extensions]")].at(-1)?.getAttribute("data-open") ?? null).toBe(step.at.includes("extension=") ? "example" : null);
    }
    expect(openedFromSub(history.state)).toBe(false); // no way back into the extension from here
  });

  // audit item 18: the search box finds a setting by its own name, Enter opens the first result, Esc clears
  it("finds a setting by its row's label, and Enter takes the page there and lights the row", async () => {
    const scrolled: Element[] = [];
    Element.prototype.scrollIntoView = function (this: Element) { scrolled.push(this); };
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=shortcuts");
    const box = host.querySelector<HTMLInputElement>(`input[aria-label="${t("settings.search_settings")}"]`)!;
    const type = (value: string) => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, value);
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const key = (k: string) => act(async () => { box.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });
    await type(t("language.label"));
    expect(nav().textContent).toContain(`${t("settings-nav.tab_general")}›${t("language.label")}`);
    expect(nav().textContent).not.toContain(t("settings.search_no_results"));
    await key("Enter");
    expect(location.search).toBe("");
    expect(host.querySelector('[role="region"]')?.getAttribute("aria-label")).toBe(t("settings-nav.tab_general"));
    const row = host.querySelector<HTMLElement>('[data-settings-row="language"]')!;
    expect(row.textContent).toContain(t("language.label"));
    expect(scrolled).toContain(row);
    expect(row.style.boxShadow).toContain("var(--accent)");
    expect(box.value).toBe("");
    // Esc clears what was typed and keeps the page
    await type(t("ui-look.text_size"));
    expect(box.value).toBe(t("ui-look.text_size"));
    await key("Escape");
    expect(box.value).toBe("");
    expect(host.querySelector('[role="region"]')?.getAttribute("aria-label")).toBe(t("settings-nav.tab_general"));
    // the Enter that confirms an input method's text is not ours: Safari sends it with isComposing false and keyCode 229
    await type(t("settings-nav.tab_notifications"));
    const confirming = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    Object.defineProperty(confirming, "keyCode", { value: 229 });
    await act(async () => { box.dispatchEvent(confirming); });
    await act(async () => { box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true, cancelable: true })); });
    expect(location.search).toBe("");
    expect(box.value).toBe(t("settings-nav.tab_notifications"));
    // Enter on a category's name opens the category
    await key("Enter");
    expect(location.search).toBe("?tab=notifications");
  });

  // a setting inside a named part of a page ("Claude Code › Thinking effort") opens that page at that part
  it("finds an agent's own setting by its name, and its result opens that agent's section", async () => {
    Element.prototype.scrollIntoView = function () {};
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=general");
    const box = host.querySelector<HTMLInputElement>(`input[aria-label="${t("settings.search_settings")}"]`)!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, t("lib-agent-settings.setting_effortLevel"));
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(nav().textContent).toContain(`${t("settings-nav.tab_agents")}›Claude Code › ${t("lib-agent-settings.setting_effortLevel")}`);
    expect(nav().textContent).toContain(`${t("settings-nav.tab_models")}›${t("settings-nav.tab_model_config")} › ${t("settings-modelconfiguration.effort")}`);
    await act(async () => [...nav().querySelectorAll("button")].find((b) => b.textContent?.includes("Claude Code"))!.click());
    expect(location.search).toBe("?tab=agents");
    expect(location.hash).toBe("#settings-claude");
  });

  it("returns to the phone list from a shared detail URL without navigating away", async () => {
    viewport.phone = true;
    await mount("/settings?tab=notifications");
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!.click());
    expect(location.pathname + location.search).toBe("/settings");
    expect(host.querySelector("nav")).not.toBeNull();
  });

  it("folds channels/connectors/extensions/sources 4-in-1 under integrations_v2 on desktop and phone and restores losslessly when off", async () => {
    viewport.inboxSources = true;
    for (const phone of [false, true]) {
      viewport.phone = phone;
      viewport.integrationsV2 = true;
      await mount(phone ? "/settings" : "/settings?tab=general");
      const hasTab = (buttons: string[], label: string) => buttons.some((b) => (phone ? b.startsWith(label) : b === label));
      const labelsOn = [...nav().querySelectorAll("button")].map((b) => b.textContent ?? "");
      expect(hasTab(labelsOn, t("settings-nav.tab_integrations"))).toBe(true);
      expect(hasTab(labelsOn, t("settings-nav.tab_channels"))).toBe(false);
      expect(hasTab(labelsOn, t("settings-nav.tab_connectors"))).toBe(false);
      expect(hasTab(labelsOn, t("settings-nav.tab_extensions"))).toBe(false);
      expect(hasTab(labelsOn, t("settings-nav.tab_sources"))).toBe(false);

      // Flipping integrations_v2 off restores all 4 legacy items identically.
      viewport.integrationsV2 = false;
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      const labelsOff = [...nav().querySelectorAll("button")].map((b) => b.textContent ?? "");
      expect(hasTab(labelsOff, t("settings-nav.tab_integrations"))).toBe(false);
      expect(hasTab(labelsOff, t("settings-nav.tab_channels"))).toBe(true);
      expect(hasTab(labelsOff, t("settings-nav.tab_connectors"))).toBe(true);
      expect(hasTab(labelsOff, t("settings-nav.tab_extensions"))).toBe(true);
      expect(hasTab(labelsOff, t("settings-nav.tab_sources"))).toBe(true);
      await act(async () => root.render(null));
    }
  });

  it("redirects sources URLs and Settings search results into integrations -sources when integrations_v2 is on and transitions live on toggle", async () => {
    viewport.inboxSources = true;
    viewport.integrationsV2 = true;
    chooseManager("work");
    viewport.selected = "work";
    const sampleState = JSON.stringify({ p: "source", id: "sub_push_1" });
    const enc = encodeURIComponent(sampleState);

    // Direct legacy /settings/sources/<state> with manager redirects into integrations -sources@work/<encodedState>.
    await mount(`/settings/sources/${enc}?manager=work`);
    expect(location.pathname + location.search).toBe(
      `/settings?tab=integrations&manager=work&integration=${encodeURIComponent(`-sources@work/${enc}`)}`,
    );
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-open")).toBe(`-sources@work/${enc}`);

    // Live toggle integrations_v2 off -> falls back to legacy sources preserving manager & state.
    viewport.integrationsV2 = false;
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    expect(location.pathname + location.search).toBe(`/settings?tab=sources&manager=work&state=${enc}`);
    expect(host.querySelector('[data-page="sources"]')?.getAttribute("data-state")).toBe(sampleState);

    // Live toggle integrations_v2 back on -> returns smoothly to unified integrations -sources@work/<encodedState>.
    viewport.integrationsV2 = true;
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    expect(location.pathname + location.search).toBe(
      `/settings?tab=integrations&manager=work&integration=${encodeURIComponent(`-sources@work/${enc}`)}`,
    );
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-open")).toBe(`-sources@work/${enc}`);

    // Searching Inbox Sources in Settings search box opens Integrations -sources rather than hidden sources tab.
    await act(async () => category(t("settings-nav.tab_general")).click());
    const box = host.querySelector<HTMLInputElement>(`input[aria-label="${t("settings.search_settings")}"]`)!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, t("settings-nav.tab_sources"));
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const groupBtn = [...nav().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.includes(`${t("settings-nav.tab_integrations")}›${t("settings-integrations.view_sources")}`),
    )!;
    expect(groupBtn).toBeTruthy();
    await act(async () => groupBtn.click());
    expect(location.pathname + location.search).toBe("/settings?tab=integrations&integration=-sources");
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-open")).toBe("-sources");
  });

  it.each([["connector", "connector:larkcli@work"], ["sources", "-sources@work/scoped"]])(
    "returns through the parent integration from phone %s settings using header or browser Back", async (kind, child) => {
    viewport.phone = true;
    viewport.integrationsV2 = true;
    await mount("/settings?tab=integrations&integration=builtin%3Afeishu-bot%40work");
    const parent = location.href;
    for (const browserBack of [false, true]) {
      await act(async () => host.querySelector<HTMLButtonElement>(`[data-related-${kind}]`)!.click());
      expect(new URLSearchParams(location.search).get("integration")).toBe(child);
      await popped(() => browserBack ? history.back() : host.querySelector<HTMLButtonElement>("header button")!.click());
      expect(location.href).toBe(parent);
    }
  });

  it.each([["connector", "connector:larkcli@work"], ["sources", "-sources@work/scoped"]])(
    "preserves integrationParent and returns to parent integration on desktop %s drill-down", async (kind, child) => {
    viewport.phone = false;
    viewport.integrationsV2 = true;
    await mount("/settings?tab=integrations&integration=builtin%3Afeishu-bot%40work");
    const parent = location.href;
    await act(async () => host.querySelector<HTMLButtonElement>(`[data-related-${kind}]`)!.click());
    expect(new URLSearchParams(location.search).get("integration")).toBe(child);
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-parent")).toBe("builtin");
    expect(history.state.integrationParent).toEqual({
      scope: { kind: "owned", manager: "work" }, view: { kind: "builtin", id: "feishu-bot" }, filter: "all",
    });
    await popped(() => host.querySelector<HTMLButtonElement>("[data-back-parent]")!.click());
    expect(location.href).toBe(parent);
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-parent")).toBe("");
  });

  it.each([true, false])("shows conflicting owners without reading either assistant when the selected Labs flag is %s", async (enabled) => {
    viewport.integrationsV2 = enabled;
    viewport.phone = true;
    await mount("/settings?tab=integrations&manager=assistant&integration=ext:example@work");
    expect(host.textContent).toContain(t("settings-integrations.link_conflict"));
    expect(host.querySelector('[data-page="integrations"], [data-page="extensions"]')).toBeNull();
    expect(profileReads).not.toHaveBeenCalled();

    await act(async () => host.querySelector<HTMLButtonElement>("header button")!.click());
    expect(new URLSearchParams(location.search).get("tab")).toBe(enabled ? "integrations" : "extensions");
    expect(new URLSearchParams(location.search).has("manager")).toBe(false);
    expect(chosenManager()).toBeNull();
  });

  it("uses the linked assistant's Labs mode, and keeps scope, filter, and prefill on a live rollback", async () => {
    viewport.integrationsV2 = true;
    viewport.otherModes.work = false;
    const set = '{ "color": "blue" }';
    await mount(`/settings?tab=integrations&manager=work&integration=ext:example&filter=tools&set=${encodeURIComponent(set)}`);
    await act(async () => loadPages());
    expect(profileReads).toHaveBeenCalledWith("work");
    expect(chosenManager()).toBeNull();
    const legacy = host.querySelector('[data-page="extensions"][data-open="example@work"]');
    expect(legacy?.getAttribute("data-manager")).toBe("work");
    expect(legacy?.getAttribute("data-prefill")).toBe('{"color":"blue"}');
    expect(new URLSearchParams(location.search).get("set")).toBe(set);

    viewport.selected = "work";
    await act(async () => {
      chooseManager("work");
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    let query = new URLSearchParams(location.search);
    expect(query.get("tab")).toBe("integrations");
    expect(query.get("integration")).toBe("ext:example@work");
    expect(query.get("manager")).toBe("work");
    expect(query.get("filter")).toBe("tools");
    expect(query.get("set")).toBe(set);

    viewport.integrationsV2 = false;
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    query = new URLSearchParams(location.search);
    expect(query.get("extension")).toBe("example@work");
    expect(query.get("set")).toBe(set);
    expect(query.get("filter")).toBe("tools");
  });

  it("blocks a response from a different assistant and recovers through Back", async () => {
    viewport.integrationsV2 = true;
    viewport.otherModes.work = null;
    await mount("/settings?tab=extensions&manager=work&extension=example");
    expect(profileReads).toHaveBeenCalledWith("work");
    expect(host.querySelector('[data-page="extensions"], [data-page="integrations"]')).toBeNull();
    expect(host.textContent).toContain(t("settings.start_page_unavailable"));
    await act(async () => host.querySelector<HTMLButtonElement>('[role="region"] button')!.click());
    expect(host.querySelector('[data-page="integrations"]')?.getAttribute("data-view")).toBe("list");
  });

  it("keeps a complete parent across a same-address filter replacement and browser Forward", async () => {
    viewport.integrationsV2 = true;
    const initial = "/settings?tab=integrations&manager=work&integration=ext:example&filter=data&set=%7B%7D";
    await mount(initial);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-related-sources]")!.click());
    const parent = {
      scope: { kind: "owned", manager: "work" }, view: { kind: "extension", id: "example", prefill: "{}" }, filter: "data",
    };
    expect(history.state.integrationParent).toEqual(parent);
    const child = new URL(location.href);
    child.searchParams.set("filter", "tools");
    await act(async () => navigate(child.pathname + child.search, true));
    await popped(() => history.back());
    expect(location.pathname + location.search).toBe(initial);
    await popped(() => history.forward());
    expect(history.state.integrationParent).toEqual(parent);
    await popped(() => host.querySelector<HTMLButtonElement>("[data-back-parent]")!.click());
    expect(location.pathname + location.search).toBe(initial);
  });

  it.each([["persona", "repositories"], ["knowledge", "knowledge-items"]])(
    "restores %s group #%s on reload and follows content resizes for 500ms", async (tab, group) => {
      vi.useFakeTimers();
      const resize = vi.fn();
      const stop = vi.fn();
      let align!: () => void;
      vi.stubGlobal("ResizeObserver", class {
        constructor(callback: () => void) { align = callback; }
        observe = resize;
        disconnect = stop;
      });
      const scroll = vi.fn();
      Element.prototype.scrollIntoView = scroll;
      try {
        await mount(`/settings?tab=${tab}#${group}`);
        const target = host.querySelector<HTMLElement>(`[data-settings-group="${group}"]`)!;
        expect(resize).toHaveBeenCalledWith(host.querySelector('[role="region"]')!.firstElementChild);
        expect(target.firstElementChild?.getAttribute("style")).toContain("var(--accent)");
        expect(scroll.mock.contexts.filter(el => el === target)).toHaveLength(1);
        align();
        expect(scroll.mock.contexts.filter(el => el === target)).toHaveLength(2);
        vi.advanceTimersByTime(499);
        expect(stop).not.toHaveBeenCalled();
        vi.advanceTimersByTime(1);
        expect(stop).toHaveBeenCalledTimes(1);
      } finally { vi.useRealTimers(); }
    },
  );

  it("handles Integrations onSettings #anchor jumps to Assistant repositories and Knowledge items smoothly in-app", async () => {
    viewport.integrationsV2 = true;
    const scrolled: Element[] = [];
    Element.prototype.scrollIntoView = function (this: Element) { scrolled.push(this); };

    // Jump from Integrations to Settings › Assistant #repositories
    await mount("/settings?tab=integrations&integration=builtin%3Agithub");
    await act(async () => host.querySelector<HTMLButtonElement>("[data-integrations-repos]")!.click());
    expect(location.pathname + location.search).toBe("/settings?tab=persona");
    expect(location.hash).toBe("#repositories");
    const reposGroup = host.querySelector<HTMLElement>('[data-settings-group="repositories"]')!;
    expect(reposGroup).not.toBeNull();
    expect(scrolled).toContain(reposGroup);
    expect(reposGroup.querySelector<HTMLElement>(":scope > div")!.style.boxShadow).toContain("var(--accent)");

    // Jump from Integrations to Settings › Knowledge #knowledge-items
    scrolled.length = 0;
    await act(async () => category(t("settings-nav.tab_integrations")).click());
    await act(async () => host.querySelector<HTMLButtonElement>("[data-integrations-kb]")!.click());
    expect(location.pathname + location.search).toBe("/settings?tab=knowledge");
    expect(location.hash).toBe("#knowledge-items");
    const kbGroup = host.querySelector<HTMLElement>('[data-settings-group="knowledge-items"]')!;
    expect(kbGroup).not.toBeNull();
    expect(scrolled).toContain(kbGroup);
    expect(kbGroup.querySelector<HTMLElement>(":scope > div")!.style.boxShadow).toContain("var(--accent)");
  });
});
