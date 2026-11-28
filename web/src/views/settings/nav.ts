// Where Settings is (#1996): which category the URL opens, and the list of them a phone shows first. Wide, the
// categories are in the left sidebar and /settings is "General"; phone width, /settings is the list (as on iOS), a category is
// /settings?tab=… pushed over it, and Back goes back to the list. An item's page (an extension, a channel, a connector) is
// pushed over its category's page the same way at any width, so Back steps out of it one level (#2800).
import { registeredModules } from "../../core/module-host";
import { enabledSettingsTabs } from "../../core/module-settings";
import { t } from "../../i18n";
import { settingsTab } from "../../lib/channels";
import type { IconName } from "../../ui/icons";
import { SUB } from "./href";
import { INTEGRATIONS_SETTINGS_REGISTRATION } from "./integrations-registration";
import {
  integrationManager,
  isLegacyIntegrationTab,
  parseSettingsPath,
  readIntegrationsAddress,
  type IntegrationsRoute,
} from "./integrations-route";

export { settingsHref } from "./href";
export { INTEGRATIONS_SETTINGS_REGISTRATION } from "./integrations-registration";

export type CoreTab = "general" | "persona" | "prompts" | "notifications" | "models" | "agents" | "integrations" | "channels" | "connectors" | "sources" | "extensions" | "shortcuts" | "advanced" | "devices" | "hub" | "managers" | "knowledge" | "avatar" | "labs";
/** A feature module's category (core/module-host.ts `settingsTabs`): its key, never a core one. */
export type ModuleTab = string & { readonly moduleTab?: never };
/** A category: one of the core's, or an enabled module's. */
export type Tab = CoreTab | ModuleTab;
/** The core's categories (core/module-host.ts CORE_SETTINGS_TABS names the same; nav.test.ts keeps them in step). */
export const TABS: CoreTab[] = ["general", "persona", "prompts", "notifications", "models", "agents", "integrations", "channels", "connectors", "sources", "extensions", "shortcuts", "advanced", "devices", "hub", "managers", "knowledge", "avatar", "labs"];
export const AGENT_KINDS = ["claude", "codex", "agy", "omp", "grok"] as const;
export type AgentTab = (typeof AGENT_KINDS)[number];
export type SettingsSection = "providers" | "model-config" | "runtimes" | "execution-channels" | "translation" | "seen-rows" | "repositories" | "knowledge-items" | AgentTab;
/** A host-owned secondary page, not a connector ID. */
export const SENT_SUB = "-sent";

// Links from before the categories were combined keep their extra provider parameters. A fragment is the legacy
// category's destination within its new page, so a shared link still lands on the same section.
const LEGACY_TABS: Record<string, { tab: Tab; section: SettingsSection | null }> = {
  "model-config": { tab: "models", section: "model-config" },
  claude: { tab: "agents", section: "claude" },
  codex: { tab: "agents", section: "codex" },
  agy: { tab: "agents", section: "agy" },
  omp: { tab: "agents", section: "omp" },
  grok: { tab: "agents", section: "grok" },
  translate: { tab: "general", section: "translation" },
  assistant: { tab: "persona", section: null },
  sent: { tab: "connectors", section: null },
};

// `hint`: one short line under the category on the phone's list (#3636), so what it holds is clear without opening it.
export const TAB_INFO: Record<CoreTab, { label: string; icon: IconName; hint: string }> = {
  general: { label: t("settings-nav.tab_general"), icon: "gear", hint: t("settings-nav.hint_general") },
  persona: { label: t("settings-nav.tab_profile"), icon: "spark", hint: t("settings-nav.hint_persona") },
  avatar: { label: t("settings.avatar"), icon: "smile", hint: t("settings-nav.hint_avatar") },
  // the user's own "/" prompts, this assistant's (#7329, SavedPrompts.tsx)
  prompts: { label: t("settings-nav.tab_prompts"), icon: "book", hint: t("settings-nav.hint_prompts") },
  // what Persona reads and keeps: a filter on its sources (Models keeps the layers mark)
  knowledge: { label: t("settings-nav.tab_knowledge"), icon: "filter", hint: t("settings-nav.hint_knowledge") },
  notifications: { label: t("settings-nav.tab_notifications"), icon: "bell", hint: t("settings-nav.hint_notifications") },
  models: { label: t("settings-nav.tab_models"), icon: "layers", hint: t("settings-nav.hint_models") },
  agents: { label: t("settings-nav.tab_agents"), icon: "terminal", hint: t("settings-nav.hint_agents") },
  integrations: { label: t("settings-nav.tab_integrations"), icon: "plug", hint: t("settings-nav.hint_integrations") },
  channels: { label: t("settings-nav.tab_channels"), icon: "chat", hint: t("settings-nav.hint_channels") },
  connectors: { label: t("settings-nav.tab_connectors"), icon: "link", hint: t("settings-nav.hint_connectors") },
  // the manager's pages of every source of the Inbox (#5278), shown with inbox_sources
  sources: { label: t("settings-nav.tab_sources"), icon: "inbox", hint: t("settings-nav.hint_sources") },
  extensions: { label: t("settings-nav.tab_extensions"), icon: "extension", hint: t("settings-nav.hint_extensions") },
  shortcuts: { label: t("settings-nav.tab_shortcuts"), icon: "keyboard", hint: t("settings-nav.hint_shortcuts") },
  // #13398: the opt-in looks (theme color, extension theme, fonts, colored sidebar icons) live here, not in General.
  advanced: { label: t("settings-nav.tab_advanced"), icon: "sliders", hint: t("settings-nav.hint_advanced") },
  // Temporary trials with a stable default, lossless rollback and a tracked exit (docs/web/web.md "Labs").
  // General owns permanent module switches. Put lasting advanced preferences in their owning category and
  // keep debug controls, demos and irreversible changes out of Labs.
  labs: { label: t("settings-nav.tab_labs"), icon: "sparkles", hint: t("settings-nav.hint_labs") },
  devices: { label: t("settings-nav.tab_devices"), icon: "key", hint: t("settings-nav.hint_devices") },
  hub: { label: t("settings-nav.tab_hub"), icon: "hub", hint: t("settings-nav.hint_hub") },
  managers: { label: t("settings-nav.tab_managers"), icon: "person", hint: t("settings-nav.hint_managers") },
};

/** The sidebar (desktop) and the phone's list: the categories in three groups, in this order (docs/assistant/managers.md
 *  "The current assistant"): the current assistant's settings, each page reading and writing that assistant only
 *  ("<name>'s settings", with Switch); this device's; and the hub's, the same whichever assistant is current. Then Labs
 *  on its own, untitled: its modules each say whose setting they hold (Labs.tsx). */
export type NavGroup = "assistant" | "device" | "hub" | "labs";
export const NAV_GROUPS: { key: NavGroup; tabs: CoreTab[] }[] = [
  { key: "assistant", tabs: ["models", "persona", "prompts", "integrations", "channels", "connectors", "sources", "extensions", "knowledge", "agents"] },
  { key: "device", tabs: ["general", "notifications", "shortcuts", "advanced"] },
  { key: "hub", tabs: ["hub", "managers", "avatar", "devices"] },
  { key: "labs", tabs: ["labs"] },
];

export const isCoreTab = (k: string): k is CoreTab => (TABS as string[]).includes(k);

/** Whether a key is an enabled module's category. */
export const isModuleTab = (k: string): boolean => !isCoreTab(k) && enabledSettingsTabs().some((m) => m.key === k);

/** A category's words and icon: the core's own, or what its module's descriptor says (read even while the module is
 *  off, for a page left open as it goes). */
export function tabInfo(k: Tab): { label: string; icon: IconName; hint: string } {
  if (isCoreTab(k)) return TAB_INFO[k];
  const m = registeredModules().flatMap((d) => d.settingsTabs ?? []).find((x) => x.key === k);
  return m ? { label: m.label(), icon: m.icon, hint: m.hint() } : { label: k, icon: "gear", hint: "" };
}

/** NAV_GROUPS with the enabled modules' categories, each after its group's own.
 *  When `integrationsV2` is false (default), hides `integrations` so legacy tabs stay unchanged.
 *  When `integrationsV2` is true, collapses `channels`, `connectors`, `extensions`, and `sources` into `integrations`. */
export function navGroups(integrationsV2 = false): { key: NavGroup; tabs: Tab[] }[] {
  const extra = enabledSettingsTabs();
  const unifiedVisible = INTEGRATIONS_SETTINGS_REGISTRATION.isVisible({ integrationsV2 });
  return NAV_GROUPS.map((g) => {
    const coreTabs = g.tabs.filter((k) =>
      unifiedVisible ? !isLegacyIntegrationTab(k) : k !== INTEGRATIONS_SETTINGS_REGISTRATION.id,
    );
    const modTabs = extra.filter((m) => m.group === g.key).map((m) => m.key);
    return { key: g.key, tabs: modTabs.length ? [...coreTabs, ...modTabs] : coreTabs };
  });
}

/** The current assistant's group: its pages follow the pick, and an item opened in one goes back to its list when the
 *  pick changes (an item is one assistant's). */
export const ASSISTANT_TABS: ReadonlySet<Tab> = new Set<Tab>(NAV_GROUPS[0].tabs);

/** The category and item a URL's query names; `tab` is null when it names none (the phone's list). */
export function settingsAt(search: string, hash = ""): { tab: Tab | null; sub: string | null; section: SettingsSection | null } {
  const q = new URLSearchParams(search);
  const old = settingsTab(q.get("tab"));
  const legacy = old ? LEGACY_TABS[old] : undefined;
  const t = (legacy?.tab ?? old) as Tab;
  if (!isCoreTab(t)) return isModuleTab(t) ? { tab: t, sub: null, section: null } : { tab: null, sub: null, section: sectionOfHash(hash, "general") };
  const param = SUB[t];
  // ?tab=plugins&plugin=<id>: a link from before #1845
  const requested = old === "sent" ? SENT_SUB : param ? (q.get(param) ?? (t === "extensions" ? q.get("plugin") : null)) : null;
  const sub = t === "hub" && requested !== "recovery" ? null : requested;
  if (t === "extensions" && sub === "-channels") return { tab: "channels", sub: null, section: null };
  if (t === "extensions" && sub?.startsWith("-channels/")) return { tab: "channels", sub: sub.slice("-channels/".length), section: null };
  const section = legacy?.section ?? sectionOfHash(hash, t);
  return { tab: t, sub, section };
}

/** Replace a legacy category URL (or path-based settings route) in place, retaining provider and manager parameters.
 *  Also performs bidirectional redirection between `integrations` and legacy `connectors`/`channels`/`extensions`/`sources`
 *  according to `integrationsV2`. Pass `"loading"` while assistant state is still loading to avoid
 *  prematurely redirecting away from `/settings?tab=integrations`. */
export function redirectLegacySettings(
  search: string,
  hash: string,
  path = "/settings",
  integrationsV2: boolean | "loading" = false,
  integration = readIntegrationsAddress(search, path),
): string | null {
  if (integration?.route.view.kind === "unavailable") return null;
  const redirected = INTEGRATIONS_SETTINGS_REGISTRATION.redirectAddress(search, hash, path, integrationsV2, integration);
  if (redirected) return redirected;
  const q = new URLSearchParams(search);
  const pathRoute = parseSettingsPath(path);
  let changed = false;
  if (pathRoute) {
    changed = true;
    q.set("tab", pathRoute.tab);
    if (pathRoute.sub) {
      const param = SUB[pathRoute.tab as Tab];
      if (param) q.set(param, pathRoute.sub);
    }
    if (pathRoute.manager && !q.get("manager")) {
      q.set("manager", pathRoute.manager);
    }
  }

  const old = settingsTab(q.get("tab"));
  let section: SettingsSection | null = null;

  if (old && LEGACY_TABS[old]) {
    changed = true;
    const at = settingsAt(q.toString(), hash);
    if (!at.tab) return null;
    q.set("tab", at.tab);
    if (old === "sent") q.set("connector", SENT_SUB);
    if (at.tab !== "models") q.delete("purpose");
    section = at.section;
  }

  const currentTab = settingsTab(q.get("tab"));
  if (currentTab === "extensions") {
    const extSub = q.get("extension") ?? q.get("plugin");
    if (extSub === "-channels") {
      changed = true;
      q.set("tab", "channels");
      q.delete("extension");
      q.delete("plugin");
    } else if (extSub?.startsWith("-channels/")) {
      changed = true;
      q.set("tab", "channels");
      q.set("channel", extSub.slice("-channels/".length));
      q.delete("extension");
      q.delete("plugin");
    }
  }

  const owner = integration && integrationManager(integration.route.scope);
  if (owner && !q.get("manager")) {
    changed = true;
    q.set("manager", owner);
  }

  if (!changed) return null;
  const frag = section && section !== "repositories" && section !== "knowledge-items" ? `#settings-${section}` : hash;
  return `/settings?${q.toString()}${frag}`;
}

function sectionOfHash(hash: string, tab: Tab): SettingsSection | null {
  if (tab === "persona" && hash === "#repositories") return "repositories";
  if (tab === "knowledge" && hash === "#knowledge-items") return "knowledge-items";
  const section = hash.startsWith("#settings-") ? hash.slice("#settings-".length) : "";
  if (tab === "models" && (section === "providers" || section === "model-config" || section === "runtimes" || section === "execution-channels")) return section;
  if (tab === "agents" && (AGENT_KINDS as readonly string[]).includes(section)) return section as AgentTab;
  if (tab === "general" && (section === "translation" || section === "seen-rows")) return section;
  return null;
}

/** The history state of a category opened from the phone's list: Back from it returns to the list. Its value is how many
 *  entries back the list is (true: one; an item's page over such a category: two). */
export const FROM_LIST = "personaSettingsList";
/** The history state of an item's page (an extension, a channel, a connector) opened from its category's page: Back from
 *  it returns to that page. */
export const FROM_SUB = "personaSettingsSub";

const marker = (state: unknown, key: string): unknown => (state && typeof state === "object" ? (state as Record<string, unknown>)[key] : undefined);
const INTEGRATION_PARENT = "integrationParent";

export function integrationParent(state: unknown): IntegrationsRoute | null {
  const parent = marker(state, INTEGRATION_PARENT) as Partial<IntegrationsRoute> | undefined;
  return parent?.scope && parent.view && parent.filter ? parent as IntegrationsRoute : null;
}

export function integrationChildState(state: unknown, parent: IntegrationsRoute): Record<string, unknown> {
  return { ...itemState(state), [INTEGRATION_PARENT]: parent };
}

export function openedFromList(state: unknown): boolean {
  return Boolean(marker(state, FROM_LIST));
}

export function openedFromSub(state: unknown): boolean {
  return Boolean(marker(state, FROM_SUB));
}

/** How many entries back the phone's list is from this one; 0: it is not under it. */
export function stepsToList(state: unknown): number {
  const n = marker(state, FROM_LIST);
  return typeof n === "number" && n > 0 ? n : n ? 1 : 0;
}

/** The state of an item's page pushed over its category's page (`state`). */
export function itemState(state: unknown): Record<string, unknown> {
  const n = stepsToList(state);
  return n ? { [FROM_SUB]: true, [FROM_LIST]: n + 1 } : { [FROM_SUB]: true };
}

/** The state an entry keeps when another category replaces it: its way back to the list, not to the item's category. */
export function categoryState(state: unknown): Record<string, unknown> | null {
  if (!openedFromList(state)) return null;
  const rest = { ...(state as Record<string, unknown>) };
  delete rest[FROM_SUB];
  delete rest[INTEGRATION_PARENT];
  return rest;
}

/** Whether a category's item is a page one level below the category's page. A model purpose opens in a dialog, which
 *  Back closes only where it is a sheet (ui/atoms Dialog), so it stays on the category's entry. */
export function itemIsPage(tab: Tab): boolean {
  return tab === "channels" || tab === "connectors" || tab === "extensions" || tab === "integrations" || tab === "hub";
}
