// Pure aggregation, deduplication, capability classification, and deep-link target resolution
// for the lazy-loaded unified Integrations & Extensions view (integrations_v2, #14874 / Refs #14802).
import { formatList, t } from "../../../i18n";
import { mailSummary, type MailAccount } from "../../../lib/agent-settings";
import { channelStateText, channelSubtitle, channelTone, type Channel } from "../../../lib/channels";
import { connectorSummary, connectorTone, storesData, type HubConnector } from "../../../lib/connectors";
import { listAuthNote } from "../../../lib/extension-auth";
import { channelOf, extensionState, extensionTone, type HubExtension } from "../../../lib/extensions";
import { botSummary, botTone, type FeishuBotStatus } from "../../../lib/feishu-bot";
import type { IntegrationOrigin, IntegrationRef } from "../../../lib/integrations-projection-wire";
import { defaultManager, sameManager } from "../../../lib/manager-choice";
import {
  isPushSubscription,
  isSubscriptionAlertState,
  subscriptionSourceState,
  type HubSubscription,
  type SubscriptionAlertState,
} from "../../../lib/subscriptions";
import { integrationManager, type IntegrationFilter, type IntegrationsRoute, type IntegrationView } from "../integrations-route";
import type { IntegrationTarget } from "./model";

export type IntegrationCapability = "channels" | "data" | "tools";
export type IntegrationTone = "green" | "orange" | "red" | "idle";

export interface IntegrationSubscriptionAlert {
  count: number;
  tone: "red" | "orange";
  state: SubscriptionAlertState;
  badgeText: string;
  detailText: string;
}

export interface LarkGatewaySummary {
  configured: boolean;
  connected: boolean;
  name?: string;
  error?: string;
}

export interface IntegrationItem {
  key: string;
  ref?: IntegrationRef;
  origin?: IntegrationOrigin;
  kind: "builtin" | "extension" | "source";
  /** True when an `extension` item ships built into Persona (`extension.source === "builtin"`). */
  builtinExtension?: boolean;
  id: string;
  name: string;
  description?: string;
  manager?: string;
  capabilities: IntegrationCapability[];
  tone: IntegrationTone;
  statusText: string;
  subtitle?: string;
  alertTone?: "red" | "orange";
  subscriptionAlert?: IntegrationSubscriptionAlert;
  channel?: Channel;
  connector?: HubConnector;
  extension?: HubExtension;
  subscription?: HubSubscription;
  /** Encoded declarative UI state passed to SourcesUI for standalone push sources. */
  sourceState?: string;
  view: IntegrationView;
  /** Target passed to `<IntegrationDetail />` (absent for standalone push sources opened in SourcesUI). */
  target?: IntegrationTarget;
}

export interface IntegrationCatalogInput {
  extensions: readonly HubExtension[];
  channels: readonly Channel[];
  connectors: readonly HubConnector[];
  subscriptions?: readonly HubSubscription[];
  hasMail: boolean;
  hasLark: boolean;
  hasFeishuBot: boolean;
  mailAccounts?: readonly MailAccount[] | null;
  larkStatus?: LarkGatewaySummary | null;
  feishuBotStatus?: FeishuBotStatus | null;
  ownerManager?: string;
}

const NARROW_LIST: Intl.ListFormatOptions = { type: "conjunction", style: "narrow" };
const RESERVED_BUILTIN_CONNECTORS = new Set(["mail", "lark", "larkcli", "imessage", "github"]);
const CHANNEL_POINTS = new Set(["channel", "channels", "voice"]);
const DATA_POINTS = new Set(["connectors", "connector", "messaging", "inbox", "knowledge", "sources", "sync"]);

function nonDefaultManager(manager?: string): string | undefined {
  return manager && manager !== defaultManager() ? manager : undefined;
}

function classifyExtensionCapabilities(
  ext: HubExtension,
  matchedChannel?: Channel,
  matchedConnectors: readonly HubConnector[] = [],
): IntegrationCapability[] {
  const caps = new Set<IntegrationCapability>();
  const points = Object.keys(ext.contributes ?? {}).filter((k) => (ext.contributes?.[k]?.length ?? 0) > 0);

  if (matchedChannel || points.some((p) => CHANNEL_POINTS.has(p))) {
    caps.add("channels");
  }
  if (
    matchedConnectors.some((c) => storesData(c)) ||
    points.some((p) => DATA_POINTS.has(p))
  ) {
    caps.add("data");
  }
  if (matchedConnectors.some((c) => !storesData(c))) {
    caps.add("channels");
  }
  if (points.some((p) => !CHANNEL_POINTS.has(p) && !DATA_POINTS.has(p))) {
    caps.add("tools");
  }
  if (caps.size === 0) {
    caps.add("tools");
  }
  return (["channels", "data", "tools"] as const).filter((c) => caps.has(c));
}

function worstTone(...tones: IntegrationTone[]): IntegrationTone {
  if (tones.includes("red")) return "red";
  if (tones.includes("orange")) return "orange";
  if (tones.includes("green")) return "green";
  return "idle";
}

const ALERT_RANK: Record<SubscriptionAlertState, number> = {
  failing: 3,
  needs_recipe: 2,
  rate_limited: 1,
  not_connected: 0,
};

function subscriptionAlertLabel(state: SubscriptionAlertState): string {
  switch (state) {
    case "failing":
      return t("settings-integrations.sub_alert_failing");
    case "needs_recipe":
      return t("settings-integrations.sub_alert_needs_recipe");
    case "rate_limited":
      return t("settings-integrations.sub_alert_rate_limited");
    case "not_connected":
      return t("settings-integrations.sub_alert_not_connected");
  }
}

function buildSubscriptionAlert(subs: readonly HubSubscription[]): IntegrationSubscriptionAlert | undefined {
  const alerts = subs.filter((s): s is HubSubscription & { state: SubscriptionAlertState } => isSubscriptionAlertState(s.state));
  if (!alerts.length) return undefined;
  const sorted = [...alerts].sort((a, b) => ALERT_RANK[b.state] - ALERT_RANK[a.state]);
  const worst = sorted[0]!;
  const state = worst.state;
  const tone: "red" | "orange" = state === "failing" ? "red" : "orange";
  const badgeText = alerts.length > 1
    ? t("settings-integrations.sub_alert_count", { count: alerts.length })
    : subscriptionAlertLabel(state);
  const errMsg = worst.error?.message?.trim() ?? "";
  const detailText = errMsg
    ? (worst.label ? `${worst.label}: ${errMsg}` : errMsg)
    : (worst.label ? `${worst.label} · ${subscriptionAlertLabel(state)}` : subscriptionAlertLabel(state));
  return { count: alerts.length, tone, state, badgeText, detailText };
}

function attachSubscriptions(item: IntegrationItem, ownedSubs: readonly HubSubscription[]): IntegrationItem {
  if (!ownedSubs.length) return item;
  const capabilities = item.capabilities.includes("data")
    ? item.capabilities
    : (["channels", "data", "tools"] as const).filter((c) => c === "data" || item.capabilities.includes(c));
  const alert = buildSubscriptionAlert(ownedSubs);
  if (!alert) {
    return capabilities === item.capabilities ? item : { ...item, capabilities };
  }
  const tone = worstTone(item.tone, alert.tone);
  const alertTone: "red" | "orange" = item.alertTone === "red" || alert.tone === "red" ? "red" : "orange";
  const subtitle = item.alertTone === "red" && item.subtitle ? item.subtitle : alert.detailText;
  return {
    ...item,
    capabilities,
    tone,
    alertTone,
    subtitle,
    subscriptionAlert: alert,
  };
}

/**
 * Aggregates built-in services, hub connectors, channels, installed extensions, and standalone push
 * sources into a single deduplicated list. A multi-capability extension (for example `larkx`
 * contributing connector + channel + tools) appears once with combined capability tags, merged
 * target metadata, and any pull warning/error hints from its owned subscriptions.
 */
export function buildIntegrationCatalog(input: IntegrationCatalogInput): IntegrationItem[] {
  const owner = nonDefaultManager(input.ownerManager);
  const installedChannels = input.channels.filter((c) => c.source !== "market");
  const allSubscriptions = input.subscriptions ?? [];
  const claimedChannels = new Set<string>();
  const claimedConnectors = new Set<string>();
  const claimedSubscriptions = new Set<string>();

  const claimSubscriptions = (predicate: (s: HubSubscription) => boolean): HubSubscription[] => {
    const matched: HubSubscription[] = [];
    for (const s of allSubscriptions) {
      if (claimedSubscriptions.has(s.id) || !predicate(s)) continue;
      claimedSubscriptions.add(s.id);
      matched.push(s);
    }
    return matched;
  };

  // 1. Installed extensions first so their contributed channels, connectors, and subscriptions merge into one object.
  const extensionItems: IntegrationItem[] = input.extensions.map((ext) => {
    const extManager = nonDefaultManager(ext.manager ?? owner);
    const contribConnectors = (ext.contributes?.connectors ?? []).filter((id) => id.trim() !== "");
    const contribChannels = (ext.contributes?.channel ?? []).filter((id) => id.trim() !== "");

    const matchedConnectors: HubConnector[] = [];
    for (const c of input.connectors) {
      if (claimedConnectors.has(c.id)) continue;
      if (contribConnectors.includes(c.id) || (!RESERVED_BUILTIN_CONNECTORS.has(c.id) && c.id === ext.id)) {
        matchedConnectors.push(c);
        claimedConnectors.add(c.id);
      }
    }

    const matchedChannels: Channel[] = [];
    for (const ch of installedChannels) {
      if (claimedChannels.has(ch.id)) continue;
      if (
        contribChannels.includes(ch.id) ||
        (ch.source === "plugin" && (ch.plugin === ext.id || ch.id === ext.id))
      ) {
        matchedChannels.push(ch);
        claimedChannels.add(ch.id);
      }
    }

    const matchedConnectorIds = new Set(matchedConnectors.map((c) => c.id));
    const ownedSubscriptions = claimSubscriptions(
      (s) =>
        !isPushSubscription(s) &&
        (s.extension === ext.id ||
          contribConnectors.includes(s.connector) ||
          matchedConnectorIds.has(s.connector) ||
          (!RESERVED_BUILTIN_CONNECTORS.has(s.connector) && s.connector === ext.id)),
    );

    const primaryChannel = matchedChannels[0];
    const primaryConnector = matchedConnectors[0];
    const channelId = primaryChannel?.id ?? channelOf(ext) ?? undefined;
    const connectorId = primaryConnector?.id ?? contribConnectors[0] ?? undefined;

    const capabilities = classifyExtensionCapabilities(ext, primaryChannel, matchedConnectors);
    const extTone = extensionTone(ext);
    const tone = worstTone(
      extTone,
      ...(primaryChannel ? [channelTone(primaryChannel)] : []),
      ...(primaryConnector ? [connectorTone(primaryConnector)] : []),
    );

    const needs = ext.needs_setup?.length
      ? t("settings-extensions.needs_setup", { items: formatList(ext.needs_setup, NARROW_LIST) })
      : "";
    const statusText = `${extensionState(ext)}${listAuthNote(ext.auth)}${ext.upgrade?.state === "needs_permission" ? t("settings-extensions.upgrade_pending") : ""}`;
    const errorNote = ext.error || (primaryChannel?.state === "error" ? primaryChannel.error : undefined) || (primaryConnector?.status === "error" ? primaryConnector.error : undefined);
    const subtitle = errorNote || needs || primaryChannel?.account?.name || primaryConnector?.account || ext.description;
    const alertTone: "red" | "orange" | undefined = errorNote ? "red" : needs ? "orange" : undefined;

    const builtinExtension = ext.source === "builtin";
    return attachSubscriptions(
      {
        key: `ext:${extManager ?? ""}:${ext.id}`,
        kind: "extension",
        ...(builtinExtension ? { builtinExtension: true } : {}),
        id: ext.id,
        name: ext.name,
        description: ext.description,
        manager: extManager,
        capabilities,
        tone,
        statusText,
        subtitle,
        alertTone,
        channel: primaryChannel,
        connector: primaryConnector,
        extension: ext,
        view: { kind: "extension", id: ext.id },
        target: {
          kind: "extension",
          id: ext.id,
          name: ext.name,
          ...(channelId ? { channelId } : {}),
          ...(connectorId ? { connectorId } : {}),
          ...(extManager ? { manager: extManager } : {}),
        },
      },
      ownedSubscriptions,
    );
  });

  // 2. Built-in services (deduplicated against built-in channels, connectors, and their subscriptions).
  const builtinItems: IntegrationItem[] = [];

  const mailConnector = input.connectors.find((c) => c.id === "mail");
  claimedConnectors.add("mail");
  if (input.hasMail) {
    const mailSubs = claimSubscriptions((s) => !isPushSubscription(s) && s.connector === "mail");
    const accounts = input.mailAccounts;
    const hasError = accounts?.some((a) => a.status === "error") || mailConnector?.status === "error";
    const connected = (accounts && accounts.length > 0 && !hasError) || mailConnector?.status === "ok";
    const tone: IntegrationTone = accounts === null ? "idle" : hasError ? "red" : connected ? "green" : "idle";
    const statusText = accounts === null ? t("settings-connectors.status_unknown") : accounts
      ? (accounts.length > 0 ? mailSummary([...accounts]) : t("settings-integrations.status_not_configured"))
      : (mailConnector ? connectorSummary(mailConnector) : t("settings-integrations.status_not_configured"));
    builtinItems.push(
      attachSubscriptions(
        {
          key: `builtin:${owner ?? ""}:mail`,
          kind: "builtin",
          id: "mail",
          name: t("settings-connectors.mail"),
          description: t("settings-integrations.mail_description"),
          manager: owner,
          capabilities: ["data"],
          tone,
          statusText,
          subtitle: t("settings-integrations.mail_description"),
          alertTone: tone === "red" ? "red" : undefined,
          connector: mailConnector,
          view: { kind: "builtin", id: "mail" },
          target: {
            kind: "builtin",
            id: "mail",
            name: t("settings-connectors.mail"),
            description: t("settings-integrations.mail_description"),
            ...(owner ? { manager: owner } : {}),
          },
        },
        mailSubs,
      ),
    );
  }

  const feishuChannel = installedChannels.find((c) => c.id === "feishu" && !claimedChannels.has("feishu"));
  const larkcliConnector = input.connectors.find((c) => c.id === "larkcli" && !claimedConnectors.has("larkcli"));
  if (input.hasFeishuBot || feishuChannel || larkcliConnector) {
    claimedChannels.add("feishu");
    claimedConnectors.add("larkcli");
    const larkcliSubs = claimSubscriptions((s) => !isPushSubscription(s) && s.connector === "larkcli");
    const bot = input.feishuBotStatus ?? null;
    const tone: IntegrationTone = bot
      ? worstTone(botTone(bot) ?? "idle", ...(feishuChannel ? [channelTone(feishuChannel)] : []), ...(larkcliConnector ? [connectorTone(larkcliConnector)] : []))
      : feishuChannel
        ? worstTone(channelTone(feishuChannel), ...(larkcliConnector ? [connectorTone(larkcliConnector)] : []))
        : larkcliConnector
          ? connectorTone(larkcliConnector)
          : "idle";
    const statusText = bot
      ? botSummary(bot)
      : feishuChannel
        ? channelStateText(feishuChannel)
        : larkcliConnector
          ? connectorSummary(larkcliConnector)
          : t("settings-connectors.status_unknown");
    const name = bot?.bot_name || feishuChannel?.name || t("settings-feishubot.title");
    const description = t("settings-integrations.feishu_bot_description");
    const subtitle = (feishuChannel?.state === "error" && feishuChannel.error)
      || (larkcliConnector?.status === "error" && larkcliConnector.error)
      || feishuChannel?.account?.name
      || description;
    const caps: IntegrationCapability[] = larkcliConnector ? ["channels", "data"] : ["channels"];
    builtinItems.push(
      attachSubscriptions(
        {
          key: `builtin:${owner ?? ""}:feishu-bot`,
          kind: "builtin",
          id: "feishu-bot",
          name,
          description,
          manager: owner,
          capabilities: caps,
          tone,
          statusText,
          subtitle,
          alertTone: tone === "red" ? "red" : undefined,
          channel: feishuChannel,
          connector: larkcliConnector,
          view: { kind: "builtin", id: "feishu-bot" },
          target: {
            kind: "builtin",
            id: "feishu-bot",
            name,
            description,
            channelId: feishuChannel?.id ?? "feishu",
            ...(larkcliConnector ? { connectorId: larkcliConnector.id } : {}),
            ...(owner ? { manager: owner } : {}),
          },
        },
        larkcliSubs,
      ),
    );
  }

  const larkConnector = input.connectors.find((c) => c.id === "lark" && !claimedConnectors.has("lark"));
  if (input.hasLark) {
    claimedConnectors.add("lark");
    const larkSubs = claimSubscriptions((s) => !isPushSubscription(s) && s.connector === "lark");
    const ls = input.larkStatus;
    const tone: IntegrationTone = ls === null ? "idle" : ls
      ? (ls.error ? "red" : ls.connected ? "green" : "idle")
      : (larkConnector ? connectorTone(larkConnector) : "idle");
    const statusText = ls === null ? t("settings-connectors.status_unknown") : ls
      ? (ls.error ? t("settings-integrations.status_error") : ls.connected ? t("settings-integrations.status_connected") : t("settings-integrations.status_not_configured"))
      : (larkConnector ? connectorSummary(larkConnector) : t("settings-integrations.status_not_configured"));
    builtinItems.push(
      attachSubscriptions(
        {
          key: `builtin:${owner ?? ""}:lark`,
          kind: "builtin",
          id: "lark",
          name: t("settings-connectors.lark"),
          description: t("settings-integrations.lark_gateway_description"),
          manager: owner,
          capabilities: ["data"],
          tone,
          statusText,
          subtitle: ls?.error || ls?.name || t("settings-integrations.lark_gateway_description"),
          alertTone: ls?.error ? "red" : undefined,
          connector: larkConnector,
          view: { kind: "builtin", id: "lark" },
          target: {
            kind: "builtin",
            id: "lark",
            name: t("settings-connectors.lark"),
            description: t("settings-integrations.lark_gateway_description"),
            ...(owner ? { manager: owner } : {}),
          },
        },
        larkSubs,
      ),
    );
  }

  // Remaining unclaimed connectors (for example GitHub, Messages / iMessage, or legacy connectors).
  for (const c of input.connectors) {
    if (claimedConnectors.has(c.id)) continue;
    claimedConnectors.add(c.id);
    const matchedChannel = installedChannels.find((ch) => !claimedChannels.has(ch.id) && ch.id === c.id);
    if (matchedChannel) claimedChannels.add(matchedChannel.id);
    const connectorSubs = claimSubscriptions((s) => !isPushSubscription(s) && s.connector === c.id);

    const cManager = nonDefaultManager(c.manager ?? owner);
    const caps: IntegrationCapability[] = [];
    if (matchedChannel || !storesData(c)) caps.push("channels");
    if (storesData(c)) caps.push("data");
    if (!caps.length) caps.push("data");

    const tone = worstTone(connectorTone(c), ...(matchedChannel ? [channelTone(matchedChannel)] : []));
    builtinItems.push(
      attachSubscriptions(
        {
          key: `builtin:${cManager ?? ""}:${c.id}`,
          kind: "builtin",
          id: c.id,
          name: c.name,
          manager: cManager,
          capabilities: caps,
          tone,
          statusText: connectorSummary(c),
          subtitle: c.error || (matchedChannel ? channelSubtitle(matchedChannel) : c.account),
          alertTone: tone === "red" ? "red" : undefined,
          channel: matchedChannel,
          connector: c,
          view: { kind: "builtin", id: c.id },
          target: {
            kind: "builtin",
            id: c.id,
            name: c.name,
            connectorId: c.id,
            ...(matchedChannel ? { channelId: matchedChannel.id } : {}),
            ...(cManager ? { manager: cManager } : {}),
          },
        },
        connectorSubs,
      ),
    );
  }

  // Remaining unclaimed installed channels (Persona apps, voice, /agent mention, or plugin channels without extension_manager).
  for (const ch of installedChannels) {
    if (claimedChannels.has(ch.id)) continue;
    claimedChannels.add(ch.id);
    const tone = channelTone(ch);
    builtinItems.push({
      key: `channel:${owner ?? ""}:${ch.id}`,
      kind: "builtin",
      id: ch.id,
      name: ch.name,
      description: ch.description,
      manager: owner,
      capabilities: ["channels"],
      tone,
      statusText: ch.kind === "app" ? t("settings-integrations.status_connected") : channelStateText(ch),
      subtitle: channelSubtitle(ch),
      alertTone: ch.state === "error" ? "red" : undefined,
      channel: ch,
      view: { kind: "channel", id: ch.id },
      target: {
        kind: "builtin",
        id: ch.id,
        name: ch.name,
        description: ch.description,
        channelId: ch.id,
        connectorId: null,
        ...(owner ? { manager: owner } : {}),
      },
    });
  }

  // 3. Standalone push sources (e.g. iOS Shortcuts push sources) not owned by any built-in connector or extension.
  const pushSourceItems: IntegrationItem[] = [];
  for (const s of allSubscriptions) {
    if (claimedSubscriptions.has(s.id) || !isPushSubscription(s)) continue;
    claimedSubscriptions.add(s.id);
    const sourceState = subscriptionSourceState(s.id);
    const alert = buildSubscriptionAlert([s]);
    const active = s.state === "ok" && (s.to_inbox || s.to_kb);
    const tone: IntegrationTone = alert ? alert.tone : active ? "green" : "idle";
    const statusText = alert
      ? alert.badgeText
      : active
        ? t("settings-integrations.status_receiving")
        : t("settings-integrations.status_off");
    const name = s.label || s.source_name || t("settings-integrations.push_source_default_name");
    const description = t("settings-integrations.push_source_description");
    const subtitle = alert
      ? alert.detailText
      : s.source_name
        ? t("settings-integrations.push_source_device_hint", { device: s.source_name })
        : description;
    pushSourceItems.push({
      key: `source:${owner ?? ""}:${s.id}`,
      kind: "source",
      id: s.id,
      name,
      description,
      manager: owner,
      capabilities: ["data"],
      tone,
      statusText,
      subtitle,
      alertTone: alert?.tone,
      subscriptionAlert: alert,
      subscription: s,
      sourceState,
      view: { kind: "source", state: sourceState },
    });
  }

  const builtinExtensionItems = extensionItems.filter((it) => it.builtinExtension);
  const installedExtensionItems = extensionItems.filter((it) => !it.builtinExtension);

  return [...builtinItems, ...builtinExtensionItems, ...pushSourceItems, ...installedExtensionItems];
}

/** Filters items by the active capability filter chip. */
export function filterIntegrationItems(items: readonly IntegrationItem[], filter: IntegrationFilter): IntegrationItem[] {
  if (filter === "all") return [...items];
  return items.filter((it) => it.capabilities.includes(filter));
}

/** Resolves an active IntegrationsRoute into an IntegrationTarget for `<IntegrationDetail />`. */
export function resolveIntegrationTarget(
  route: IntegrationsRoute,
  items: readonly IntegrationItem[],
  currentManager: string | null | undefined,
): IntegrationTarget | null {
  const { view } = route;
  const manager = integrationManager(route.scope);
  const targetManager = manager ?? currentManager ?? undefined;

  if (view.kind === "extension") {
    const matched = items.find((it) => it.kind === "extension" && it.id === view.id && sameManager(it.manager, targetManager));
    return matched?.target ?? {
      kind: "extension",
      id: view.id,
      ...(manager ? { manager } : {}),
    };
  }

  if (view.kind === "builtin") {
    const matched = items.find((it) => it.kind === "builtin" && it.id === view.id && sameManager(it.manager, targetManager));
    return matched?.target ?? {
      kind: "builtin",
      id: view.id,
      ...(manager ? { manager } : {}),
    };
  }

  if (view.kind === "channel") {
    const matched = items.find((it) => it.channel?.id === view.id && sameManager(it.manager, targetManager));
    if (matched?.target) return matched.target;
    return {
      kind: "builtin",
      id: view.id,
      channelId: view.id,
      connectorId: null,
      ...(manager ? { manager } : {}),
    };
  }

  if (view.kind === "connector") {
    // Keep larkcli openable directly as its own connector detail when navigated via connector:larkcli.
    if (view.id === "larkcli") {
      return {
        kind: "builtin",
        id: "larkcli",
        connectorId: "larkcli",
        ...(manager ? { manager } : {}),
      };
    }
    const matched = items.find(
      (it) =>
        (it.connector?.id === view.id || it.extension?.contributes?.connectors?.includes(view.id)) &&
        sameManager(it.manager, targetManager),
    );
    if (matched?.target) return matched.target;
    return {
      kind: "builtin",
      id: view.id,
      connectorId: view.id,
      ...(manager ? { manager } : {}),
    };
  }

  return null;
}

function matchesSourceState(item: IntegrationItem, rawState: string): boolean {
  if (item.kind !== "source") return false;
  if (item.sourceState === rawState) return true;
  try {
    const parsed = JSON.parse(rawState) as { p?: string; id?: string };
    return parsed.p === "source" && parsed.id === item.id;
  } catch {
    return false;
  }
}

/** Finds the standalone push source for this view, if the directory has it. */
export function resolveSourceItem(
  route: IntegrationsRoute,
  items: readonly IntegrationItem[],
  currentManager: string | null | undefined,
): IntegrationItem | null {
  const { view } = route;
  if (view.kind !== "source") return null;
  const manager = integrationManager(route.scope) ?? currentManager ?? undefined;
  return items.find((it) => matchesSourceState(it, view.state) && sameManager(it.manager, manager)) ?? null;
}

/** Whether this view names the row, including legacy channel and connector links. */
export function isIntegrationRowSelected(
  route: IntegrationsRoute,
  item: IntegrationItem,
  currentManager: string | null | undefined,
): boolean {
  const { view } = route;
  const manager = integrationManager(route.scope) ?? currentManager ?? undefined;
  if (!sameManager(manager, item.manager)) return false;
  switch (view.kind) {
    case "builtin":
    case "extension":
      return item.kind === view.kind && item.id === view.id;
    case "channel":
      return item.channel?.id === view.id;
    case "connector":
      return view.id !== "larkcli" &&
        (item.connector?.id === view.id || !!item.extension?.contributes?.connectors?.includes(view.id));
    case "source":
      return matchesSourceState(item, view.state);
    default:
      return false;
  }
}
