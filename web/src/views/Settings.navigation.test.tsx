// @vitest-environment jsdom
// Settings category navigation keeps the existing URLs and phone history while moving beside the content.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const viewport = vi.hoisted(() => ({ phone: false }));
vi.hoisted(() => {
  // a phone matches the narrow queries; a wide screen the wide ones (the list beside an item, from 900 px)
  window.matchMedia = ((query: string) => ({ get matches() { return /min-width/.test(query) ? !viewport.phone : viewport.phone; }, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (state: { features: string[]; machines: unknown[]; conn: string; link: { networkUp: boolean; restarting: boolean }; hubName: string; mesh: unknown[] }) => unknown) => select({ features: ["channels", "extension_manager", "connectors"], machines: [], conn: "online", link: { networkUp: true, restarting: false }, hubName: "", mesh: [] }),
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
vi.mock("./settings/KnowledgeSelection", () => ({ KnowledgeSelection: () => <div data-page="knowledge-selection" /> }));
vi.mock("./settings/TranslationSettings", () => ({ TranslationSettings: () => <div data-page="translation" /> }));
vi.mock("./settings/AssistantSettings", () => ({ AssistantSettings: () => <div data-page="persona" /> }));
vi.mock("./settings/SidebarPages", () => ({ SidebarPagesGroup: () => <div data-page="sidebar-pages" /> }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));
vi.mock("../lib/api", async (original) => ({ ...(await original<typeof import("../lib/api")>()), api: () => Promise.resolve({ accounts: [] }) }));
vi.mock("./settings/AgentSettings", () => ({ AgentSettings: ({ agent }: { agent: string }) => <div data-page="agent" data-agent={agent} /> }));
vi.mock("./settings/Extensions", () => ({
  Extensions: ({ open, onOpen, headerBack }: { open: string | null; onOpen: (id: string | null) => void; headerBack?: boolean }) => (
    <div data-page="extensions" data-open={open} data-header-back={headerBack ? "yes" : "no"}>
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

import { initI18n, t } from "../i18n";
import { Settings } from "./Settings";
import { FROM_LIST, openedFromSub } from "./settings/nav";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  viewport.phone = false;
  localStorage.clear();
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
  // audit item 17: General's hints were cut at one line ("切换后页面会重新载…"), at every width
  it.each([false, true])("lets General's hints wrap rather than cutting them (phone: %s)", async (phone) => {
    viewport.phone = phone;
    await mount("/settings?tab=general");
    const hints: [string, string][] = [
      ["language", t("language.hint")],
      ["text-size", t("ui-look.text_size_hint")],
      ["reply-motion", t("settings.reply_motion_hint")],
      ["agent-details", t("context.details_hint")],
      ["context-as-used", t("context.as_used_hint")],
      ["install-app", t("settings.install_installed")],
    ];
    for (const [anchor, text] of hints) {
      const hint = [...host.querySelectorAll<HTMLElement>(`[data-settings-row="${anchor}"] div`)].find((d) => d.textContent === text);
      expect(hint, anchor).toBeTruthy();
      expect(hint!.classList.contains("truncate"), anchor).toBe(false);
    }
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
    expect([...host.querySelectorAll('[data-page="agent"]')].map((el) => el.getAttribute("data-agent"))).toEqual(["claude", "codex", "agy", "omp"]);
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
    expect(rows.map((b) => b.textContent)).toEqual(["Claude Code", "Codex", "Antigravity", "omp"]);
    expect(rows.map((b) => b.getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "true"]);
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
    const own = [...host.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("settings-hubconnector.back"));
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

  // audit item 64: pages inside an extension's or the market's had the top bar's Back, to the extension list, over their
  // own, to the catalog or the extension; the top bar's now goes where theirs did, and they draw none
  it.each([
    { from: "-market/example-market/example", to: "-market", label: () => t("settings.back_to", { name: t("settings-extensionmarket.market_title") }) },
    { from: "-markets", to: "-market", label: () => t("settings.back_to", { name: t("settings-extensionmarket.market_title") }) },
    { from: "-market/example-market/example@work", to: "example@work", label: () => t("settings-extensionview.back") },
    { from: "example/ui/main", to: "example", label: () => t("settings-extensionview.back") },
    { from: "-market", to: null, label: () => t("settings.back_to", { name: t("settings-nav.tab_extensions") }) },
  ])("on a phone, the top bar's Back goes from extension page $from to its parent", async ({ from, to, label }) => {
    viewport.phone = true;
    history.pushState(null, "", "/");
    await mount(`/settings?tab=extensions&extension=${encodeURIComponent(from)}`);
    const page = () => [...host.querySelectorAll("[data-page=extensions]")].at(-1)!;
    expect(page().getAttribute("data-header-back")).toBe("yes");
    const back = host.querySelector<HTMLButtonElement>("header button:first-child")!;
    expect(back.getAttribute("aria-label")).toBe(label());
    await act(async () => back.click());
    expect(page().getAttribute("data-open")).toBe(to);
    expect(new URLSearchParams(location.search).get("extension")).toBe(to);
  });

  it("keeps a connector page's own Back on a wide screen", async () => {
    await mount("/settings?tab=connectors&connector=github");
    expect(host.querySelector("[data-own-back]")).not.toBeNull();
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
});
