// Where Settings is (#1996): which category the URL opens, and the list of them a phone shows first. Wide, the
// categories are in the left sidebar and /settings is "General"; phone width, /settings is the list (as on iOS), a category is
// /settings?tab=… pushed over it, and Back goes back to the list. An item's page (an extension, a channel, a connector) is
// pushed over its category's page the same way at any width, so Back steps out of it one level (#2800).
import { t } from "../../i18n";
import { settingsTab } from "../../lib/channels";
import type { IconName } from "../../ui/icons";

export type Tab = "general" | "persona" | "prompts" | "notifications" | "models" | "agents" | "channels" | "connectors" | "sources" | "extensions" | "shortcuts" | "devices" | "hub" | "managers" | "knowledge" | "avatar";
export const TABS: Tab[] = ["general", "persona", "prompts", "notifications", "models", "agents", "channels", "connectors", "sources", "extensions", "shortcuts", "devices", "hub", "managers", "knowledge", "avatar"];
export const AGENT_KINDS = ["claude", "codex", "agy", "omp"] as const;
export type AgentTab = (typeof AGENT_KINDS)[number];
export type SettingsSection = "providers" | "model-config" | "translation" | AgentTab;
// The tab whose page opens one of its items, and the query parameter that names it.
const SUB: Partial<Record<Tab, string>> = { models: "purpose", channels: "channel", extensions: "extension", connectors: "connector" };
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
  translate: { tab: "general", section: "translation" },
  assistant: { tab: "persona", section: null },
  sent: { tab: "connectors", section: null },
};

// `hint`: one short line under the category on the phone's list (#3636), so what it holds is clear without opening it.
export const TAB_INFO: Record<Tab, { label: string; icon: IconName; hint: string }> = {
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
  channels: { label: t("settings-nav.tab_channels"), icon: "chat", hint: t("settings-nav.hint_channels") },
  connectors: { label: t("settings-nav.tab_connectors"), icon: "link", hint: t("settings-nav.hint_connectors") },
  // the manager's pages of every source of the Inbox (#5278), shown with inbox_sources
  sources: { label: t("settings-nav.tab_sources"), icon: "inbox", hint: t("settings-nav.hint_sources") },
  extensions: { label: t("settings-nav.tab_extensions"), icon: "extension", hint: t("settings-nav.hint_extensions") },
  shortcuts: { label: t("settings-nav.tab_shortcuts"), icon: "keyboard", hint: t("settings-nav.hint_shortcuts") },
  devices: { label: t("settings-nav.tab_devices"), icon: "key", hint: t("settings-nav.hint_devices") },
  hub: { label: t("settings-nav.tab_hub"), icon: "hub", hint: t("settings-nav.hint_hub") },
  managers: { label: t("settings-nav.tab_managers"), icon: "person", hint: t("settings-nav.hint_managers") },
};

/** The sidebar (desktop) and the phone's list: the categories in three groups, in this order (docs/assistant/managers.md
 *  "The current assistant"): the current assistant's settings, each page reading and writing that assistant only
 *  ("<name>'s settings", with Switch); this device's; and the hub's, the same whichever assistant is current. */
export type NavGroup = "assistant" | "device" | "hub";
export const NAV_GROUPS: { key: NavGroup; tabs: Tab[] }[] = [
  { key: "assistant", tabs: ["models", "persona", "prompts", "channels", "connectors", "sources", "extensions", "knowledge", "agents"] },
  { key: "device", tabs: ["general", "notifications", "shortcuts"] },
  { key: "hub", tabs: ["hub", "managers", "avatar", "devices"] },
];

/** The current assistant's group: its pages follow the pick, and an item opened in one goes back to its list when the
 *  pick changes (an item is one assistant's). */
export const ASSISTANT_TABS: ReadonlySet<Tab> = new Set(NAV_GROUPS[0].tabs);

/** The category and item a URL's query names; `tab` is null when it names none (the phone's list). */
export function settingsAt(search: string, hash = ""): { tab: Tab | null; sub: string | null; section: SettingsSection | null } {
  const q = new URLSearchParams(search);
  const old = settingsTab(q.get("tab"));
  const legacy = old ? LEGACY_TABS[old] : undefined;
  const t = (legacy?.tab ?? old) as Tab;
  if (!TABS.includes(t)) return { tab: null, sub: null, section: null };
  const param = SUB[t];
  // ?tab=plugins&plugin=<id>: a link from before #1845
  const sub = old === "sent" ? SENT_SUB : param ? (q.get(param) ?? (t === "extensions" ? q.get("plugin") : null)) : null;
  if (t === "extensions" && sub === "-channels") return { tab: "channels", sub: null, section: null };
  if (t === "extensions" && sub?.startsWith("-channels/")) return { tab: "channels", sub: sub.slice("-channels/".length), section: null };
  const section = legacy?.section ?? sectionOfHash(hash, t);
  return { tab: t, sub, section };
}

/** The URL of a category (and its open item). "General" is plain /settings on a wide screen, where that opens it; on a
 *  phone /settings is the list, so it is named. */
export function settingsHref(tab: Tab, id: string | null, phone: boolean, manager?: string | null, section?: SettingsSection | null): string {
  const param = SUB[tab];
  const query = `/settings?tab=${tab}${id && param ? `&${param}=${encodeURIComponent(id)}` : ""}${manager ? `&manager=${encodeURIComponent(manager)}` : ""}`;
  return `${tab === "general" && !phone ? "/settings" : query}${section ? `#settings-${section}` : ""}`;
}

/** Replace a legacy category URL in place, retaining provider and manager parameters. */
export function redirectLegacySettings(search: string, hash: string, path = "/settings"): string | null {
  const q = new URLSearchParams(search);
  if (path === "/settings/sent" || path === "/settings/sent/") q.set("tab", "sent");
  const old = settingsTab(q.get("tab"));
  if (!old || !LEGACY_TABS[old]) return null;
  const at = settingsAt(q.toString(), hash);
  if (!at.tab) return null;
  q.set("tab", at.tab);
  if (old === "sent") q.set("connector", SENT_SUB);
  if (at.tab !== "models") q.delete("purpose");
  return `/settings?${q.toString()}${at.section ? `#settings-${at.section}` : ""}`;
}

function sectionOfHash(hash: string, tab: Tab): SettingsSection | null {
  const section = hash.startsWith("#settings-") ? hash.slice("#settings-".length) : "";
  if (tab === "models" && (section === "providers" || section === "model-config")) return section;
  if (tab === "agents" && (AGENT_KINDS as readonly string[]).includes(section)) return section as AgentTab;
  if (tab === "general" && section === "translation") return "translation";
  return null;
}

/** The history state of a category opened from the phone's list: Back from it returns to the list. Its value is how many
 *  entries back the list is (true: one; an item's page over such a category: two). */
export const FROM_LIST = "personaSettingsList";
/** The history state of an item's page (an extension, a channel, a connector) opened from its category's page: Back from
 *  it returns to that page. */
export const FROM_SUB = "personaSettingsSub";

const marker = (state: unknown, key: string): unknown => (state && typeof state === "object" ? (state as Record<string, unknown>)[key] : undefined);

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
  return rest;
}

/** Whether a category's item is a page one level below the category's page. A model purpose opens in a dialog, which
 *  Back closes only where it is a sheet (ui/atoms Dialog), so it stays on the category's entry. */
export function itemIsPage(tab: Tab): boolean {
  return tab === "channels" || tab === "connectors" || tab === "extensions";
}
