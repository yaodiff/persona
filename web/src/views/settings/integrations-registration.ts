// Self-contained Settings host navigation registration metadata for Integrations (#17762 / #13395-ready).
// Exposes stable entry identity, localized title/hint/search keywords, owning-assistant scope,
// visibility predicates, route/target resolution, legacy 4-tab compatibility mapping, and a lazy
// component boundary so Settings host registry adoption can plug in without duplicating heuristics.
// Strictly leaves Devices & Pairing and Assistant Managers navigation to #13395 / #17716.
import { t } from "../../i18n";
import type { LegacyIntegrationTab } from "../../lib/deep-links";
import type { IconName } from "../../ui/icons";
import {
  decodeIntegrationsRoute,
  integrationHref,
  integrationQuery,
  isIntegrationDetailRoute,
  isIntegrationTab,
  isLegacyIntegrationTab,
  legacyIntegrationTarget,
  readIntegrationsAddress,
  type IntegrationsRoute,
  type IntegrationView,
} from "./integrations-route";

export const LEGACY_INTEGRATION_TABS: readonly LegacyIntegrationTab[] = [
  "channels",
  "connectors",
  "sources",
  "extensions",
];

export interface IntegrationsVisibilityContext {
  integrationsV2: boolean;
  activeTab: string | null;
  hasChannels: boolean;
  hasExtensions: boolean;
  hasSources: boolean;
}

export interface IntegrationsSearchEntry {
  tab: "integrations" | LegacyIntegrationTab;
  group: string;
  anchor: string;
  hint?: string;
  sub?: string;
}

export interface IntegrationsSettingsRegistration {
  readonly id: "integrations";
  readonly scope: "assistant";
  readonly icon: IconName;
  readonly legacyTabs: readonly LegacyIntegrationTab[];
  label: () => string;
  hint: () => string;
  /** Legacy category names & hints that also match Integrations when unified mode is active. */
  searchKeywords: () => string[];
  /** Search index entries for either the unified (`true`) or legacy (`false`) Integrations surfaces. */
  searchGroups: (integrationsV2: boolean) => IntegrationsSearchEntry[];
  isVisible: (ctx: Pick<IntegrationsVisibilityContext, "integrationsV2">) => boolean;
  isLegacyTabVisible: (tab: LegacyIntegrationTab, ctx: IntegrationsVisibilityContext) => boolean;
  matchesTab: typeof isIntegrationTab;
  isLegacyTab: typeof isLegacyIntegrationTab;
  resolveAddress: typeof readIntegrationsAddress;
  decodeRoute: typeof decodeIntegrationsRoute;
  resolveLegacyTarget: typeof legacyIntegrationTarget;
  buildHref: typeof integrationHref;
  buildQuery: typeof integrationQuery;
  isDetailView: typeof isIntegrationDetailRoute;
  /** Bidirectional redirect between unified `integrations` and legacy 4 tabs based on Labs mode. */
  redirectAddress: (
    search: string,
    hash: string,
    path: string,
    integrationsV2: boolean | "loading",
    address?: ReturnType<typeof readIntegrationsAddress>,
  ) => string | null;
  /** Lazy component boundary keeping Integrations and its detail shell out of the Settings/first-screen bundle. */
  load: () => Promise<typeof import("./Integrations").Integrations>;
}

const joinPath = (...parts: string[]) => parts.join(" › ");
const joinWords = (...parts: string[]) => parts.join(" / ");

const LEGACY_TAB_COPY: Record<LegacyIntegrationTab, { labelKey: string; hintKey: string }> = {
  channels: { labelKey: "settings-nav.tab_channels", hintKey: "settings-nav.hint_channels" },
  connectors: { labelKey: "settings-nav.tab_connectors", hintKey: "settings-nav.hint_connectors" },
  sources: { labelKey: "settings-nav.tab_sources", hintKey: "settings-nav.hint_sources" },
  extensions: { labelKey: "settings-nav.tab_extensions", hintKey: "settings-nav.hint_extensions" },
};

const INTEGRATION_QUERY_KEYS = [
  "tab",
  "manager",
  "integration",
  "filter",
  "channel",
  "connector",
  "extension",
  "plugin",
  "state",
  "set",
  "purpose",
] as const;

function buildIntegrationSearchGroups(v2: boolean): IntegrationsSearchEntry[] {
  const connectors = v2 ? "integrations" : "connectors";
  const extensions = v2 ? "integrations" : "extensions";
  const sources = v2 ? "integrations" : "sources";
  const flow = t("settings-integrations.view_sources");
  const forward = t("settings-assistantsettings.inbox_todo_forward");
  const bot = v2 ? "builtin:feishu-bot" : "feishu-bot";
  return [
    { tab: connectors, group: t("settings-connectors.mail"), anchor: "mail", sub: v2 ? "builtin:mail" : undefined },
    { tab: connectors, group: t("settings-connectors.lark"), anchor: "lark", sub: v2 ? "builtin:lark" : undefined },
    { tab: connectors, group: t("settings-larkdisplay.title"), hint: joinWords(t("settings-larkdisplay.anti_recall"), t("settings-larkdisplay.edit_history")), anchor: "lark", sub: v2 ? "builtin:lark" : undefined },
    { tab: connectors, group: t("settings-feishubot.title"), hint: t("settings-integrations.feishu_bot_description"), anchor: "", sub: bot },
    { tab: connectors, group: t("settings-larksync.title"), hint: joinWords(t("settings-larksync.add"), t("settings-larksync.schedule_label")), anchor: "", sub: bot },
    { tab: connectors, group: t("settings-sent.entry_label"), hint: t("settings-sent.hint"), anchor: "sent", sub: v2 ? "-sent" : undefined },
    { tab: sources, group: v2 ? flow : t("settings-nav.tab_sources"), hint: joinWords(t("settings-nav.tab_sources"), t("settings-nav.hint_sources")), anchor: "inbox-sources", sub: v2 ? "-sources" : undefined },
    { tab: sources, group: v2 ? joinPath(flow, forward) : forward, hint: t("settings-assistantsettings.inbox_todo_forward_hint"), anchor: "inbox-todo-forward", sub: v2 ? "-sources" : undefined },
    { tab: sources, group: v2 ? joinPath(flow, t("external-sources.flows")) : t("external-sources.flows"), hint: joinWords(t("external-sources.section"), t("external-sources.add_binding")), anchor: "inbox-sources", sub: v2 ? "-sources" : undefined },
    { tab: extensions, group: v2 ? t("settings-integrations.group_extensions") : t("settings-extensions.title"), hint: t("settings-extensions.title"), anchor: "extensions" },
    { tab: extensions, group: t("settings-extensions.secrets_title"), anchor: "extensions" },
    { tab: extensions, group: t("settings-extensions.market_title"), anchor: "market", sub: v2 ? "-market" : undefined },
    { tab: extensions, group: t("settings-extensionmarket.manage_markets"), hint: t("settings-extensionmarket.token_optional"), anchor: "market", sub: v2 ? "-markets" : undefined },
    ...(v2 ? ["thread_mode", "thread_idle_hours"].map((field): IntegrationsSearchEntry => ({
      tab: "integrations",
      group: joinPath(t("settings-integrations.routing_title"), t(`settings-integrations.${field}_title`)),
      hint: field,
      anchor: `configuration-channel-routing-feishu-${field}`,
      sub: bot,
    })) : []),
  ];
}

export const INTEGRATIONS_SETTINGS_REGISTRATION: IntegrationsSettingsRegistration = {
  id: "integrations",
  scope: "assistant",
  icon: "plug",
  legacyTabs: LEGACY_INTEGRATION_TABS,
  label: () => t("settings-nav.tab_integrations"),
  hint: () => t("settings-nav.hint_integrations"),
  searchKeywords: () => LEGACY_INTEGRATION_TABS.flatMap((tab) => {
    const copy = LEGACY_TAB_COPY[tab];
    return [t(copy.labelKey), t(copy.hintKey)];
  }),
  searchGroups: buildIntegrationSearchGroups,
  isVisible: ({ integrationsV2 }) => integrationsV2,
  isLegacyTabVisible: (tab, ctx) => {
    if (ctx.integrationsV2) return false;
    if (tab === "extensions") return ctx.hasExtensions || ctx.activeTab === "extensions";
    if (tab === "channels") return ctx.hasChannels || ctx.activeTab === "channels";
    if (tab === "sources") return ctx.hasSources || ctx.activeTab === "sources";
    return true;
  },
  matchesTab: isIntegrationTab,
  isLegacyTab: isLegacyIntegrationTab,
  resolveAddress: readIntegrationsAddress,
  decodeRoute: decodeIntegrationsRoute,
  resolveLegacyTarget: legacyIntegrationTarget,
  buildHref: integrationHref,
  buildQuery: integrationQuery,
  isDetailView: isIntegrationDetailRoute,
  redirectAddress: (search, hash, path, integrationsV2, address = readIntegrationsAddress(search, path)) => {
    if (!address || address.route.view.kind === "unavailable" || integrationsV2 === "loading") return null;
    if (integrationsV2 === (address.tab === "integrations")) return null;
    const q = new URLSearchParams(search);
    for (const key of INTEGRATION_QUERY_KEYS) q.delete(key);
    integrationQuery(address.route, integrationsV2).forEach((value, key) => q.set(key, value));
    return `/settings?${q.toString()}${hash}`;
  },
  load: () => import("./Integrations").then((m) => m.Integrations),
};

export type { IntegrationsRoute, IntegrationView };
