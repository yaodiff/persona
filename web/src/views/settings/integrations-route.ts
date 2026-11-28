// Settings integration views and their URL compatibility across the Labs switch.
import { isLegacyIntegrationTab, parseSettingsPath, splitSettingsOwner, type LegacyIntegrationTab } from "../../lib/deep-links";
import { decodeURIComponentSafe } from "../../lib/uri";
import { SUB } from "./href";

export { isLegacyIntegrationTab, parseSettingsPath } from "../../lib/deep-links";

export type IntegrationFilter = "all" | "channels" | "data" | "tools";
export type IntegrationScope = { kind: "current" } | { kind: "owned"; manager: string };

export type IntegrationView =
  (
    | { kind: "list" | "catalog" | "markets" | "sent" | "data-flow" | "unavailable" }
    | { kind: "builtin" | "extension" | "channel" | "connector"; id: string }
    | { kind: "page"; id: string; ui: string }
    | { kind: "source"; state: string }
    | { kind: "item"; market: string; id: string }
  ) & { prefill?: string };

export interface IntegrationsRoute {
  scope: IntegrationScope;
  view: IntegrationView;
  filter: IntegrationFilter;
}

type IntegrationTab = "integrations" | LegacyIntegrationTab;
type ViewAddress = { view: IntegrationView; owner?: string };

const BUILTIN_IDS = new Set(["mail", "feishu-bot", "lark", "imessage", "github", "larkcli"]);
const BUILTIN_CHANNEL_IDS = new Set(["apps", "voice", "feishu-mention", "feishu"]);

export function isIntegrationTab(tab: string | null): tab is IntegrationTab {
  return tab === "integrations" || isLegacyIntegrationTab(tab);
}

export function integrationScope(manager?: string | null): IntegrationScope {
  return manager ? { kind: "owned", manager } : { kind: "current" };
}

export function integrationManager(scope: IntegrationScope): string | undefined {
  return scope.kind === "owned" ? scope.manager : undefined;
}

export function normalizeIntegrationFilter(value: string | null | undefined): IntegrationFilter {
  return value === "channels" || value === "data" || value === "tools" ? value : "all";
}

function sourceView(state?: string | null): IntegrationView {
  return state ? { kind: "source", state } : { kind: "data-flow" };
}

function objectView(kind: "builtin" | "extension" | "channel" | "connector", sub: string): ViewAddress {
  const { id, manager } = splitSettingsOwner(sub);
  return { view: id ? { kind, id } : { kind: "list" }, owner: manager };
}

function extensionView(sub: string): ViewAddress {
  if (sub === "-market") return { view: { kind: "catalog" } };
  if (sub === "-markets") return { view: { kind: "markets" } };
  const item = /^-market\/([^/]+)\/([^/@]+)(?:@([^/]+))?$/.exec(sub);
  if (item) return { view: { kind: "item", market: item[1]!, id: item[2]! }, owner: item[3] };
  const page = /^([^/@-][^/@]*)(?:@([^/]+))?\/ui\/([^/]+)$/.exec(sub);
  if (page) return { view: { kind: "page", id: page[1]!, ui: page[3]! }, owner: page[2] };
  if (sub.startsWith("-") || sub.includes("/")) return { view: { kind: "list" } };
  return objectView("extension", sub);
}

function unifiedView(sub: string): ViewAddress {
  if (sub === "-sent") return { view: { kind: "sent" } };
  const source = /^-sources(?:@([^/]+))?(?:\/(.*))?$/s.exec(sub);
  if (source) {
    const raw = source[2];
    const state = raw && !/^\s*[\[{]/.test(raw) ? decodeURIComponentSafe(raw) : raw;
    return { view: sourceView(state), owner: source[1] };
  }

  const object = /^(builtin|extension|ext|channel|connector):(.*)$/.exec(sub);
  if (object) {
    const [, prefix, id] = object;
    switch (prefix) {
      case "ext":
      case "extension": return id?.includes("/") ? extensionView(id) : objectView("extension", id ?? "");
      case "builtin":
      case "channel":
      case "connector": return id && !id.includes("/") ? objectView(prefix, id) : { view: { kind: "list" } };
    }
  }

  return BUILTIN_IDS.has(splitSettingsOwner(sub).id) ? objectView("builtin", sub) : extensionView(sub);
}

function legacyView(tab: LegacyIntegrationTab, sub: string): ViewAddress {
  switch (tab) {
    case "sources": {
      const source = /^@([^/]+)(?:\/(.*))?$/s.exec(sub);
      return source ? { view: sourceView(source[2]), owner: source[1] } : { view: sourceView(sub) };
    }
    case "connectors": {
      if (sub === "-sent") return { view: { kind: "sent" } };
      const { id } = splitSettingsOwner(sub);
      return objectView(id === "mail" || id === "lark" || id === "feishu-bot" ? "builtin" : "connector", sub);
    }
    case "channels": {
      const { id, manager } = splitSettingsOwner(sub);
      return id === "feishu" ? { view: { kind: "builtin", id: "feishu-bot" }, owner: manager } : objectView("channel", sub);
    }
    case "extensions":
      return extensionView(sub);
  }
}

function defaultFilter(tab: IntegrationTab, view: IntegrationView): IntegrationFilter {
  if (view.kind !== "list") return "all";
  if (tab === "channels") return "channels";
  if (tab === "connectors") return "data";
  return "all";
}

export function decodeIntegrationsRoute(
  tab: IntegrationTab,
  sub: string | null,
  query = new URLSearchParams(),
  pathOwner?: string,
): IntegrationsRoute {
  if (tab === "extensions" && (sub === "-channels" || sub?.startsWith("-channels/"))) {
    tab = "channels";
    sub = sub.slice("-channels/".length) || null;
  }
  const parsed = tab === "integrations" ? unifiedView(sub ?? "") : legacyView(tab, sub ?? "");
  // A legacy page can carry its original view when two unified views share a legacy destination.
  // Accept that hint only if both representations address the same legacy page.
  const hint = tab !== "integrations" && query.has("integration") ? unifiedView(query.get("integration")!) : null;
  const hinted = hint
    ? legacyIntegrationTarget({ scope: integrationScope(), view: hint.view, filter: "all" })
    : null;
  const useHint = hinted?.tab === tab && unifiedSub(legacyView(hinted.tab, hinted.sub ?? "").view) === unifiedSub(parsed.view);
  const view = useHint ? hint!.view : parsed.view;
  const filter = query.has("filter") ? normalizeIntegrationFilter(query.get("filter")) : defaultFilter(tab, view);
  const owners = new Set([...query.getAll("manager"), pathOwner, parsed.owner, useHint ? hint!.owner : undefined].filter(Boolean));
  if (owners.size > 1) return { scope: integrationScope(), view: { kind: "unavailable" }, filter };

  const [owner] = owners;
  const values = query.getAll("set");
  return {
    scope: integrationScope(owner),
    view: values.length === 1 ? { ...view, prefill: values[0]! } : view,
    filter,
  };
}

/** Decode every supported bookmark before any decision about the assistant's Labs mode. */
export function readIntegrationsAddress(search: string, path = "/settings"): { tab: IntegrationTab; route: IntegrationsRoute } | null {
  const query = new URLSearchParams(search);
  // Keep a path's owner separate so a conflicting query or item owner cannot overwrite it.
  const pathRoute = parseSettingsPath(path);
  const rawTab = pathRoute?.tab ?? query.get("tab");
  const tab = rawTab === "plugins" ? "extensions" : rawTab === "sent" ? "connectors" : rawTab;
  if (!isIntegrationTab(tab)) return null;
  const sub = rawTab === "sent" ? "-sent"
    : pathRoute?.sub ?? query.get(SUB[tab]!) ?? (tab === "extensions" ? query.get("plugin") : null);
  return { tab, route: decodeIntegrationsRoute(tab, sub, query, pathRoute?.manager) };
}

const withOwner = (id: string, owner?: string) => owner ? `${id}@${owner}` : id;

function unifiedSub(view: IntegrationView, owner?: string): string | null {
  switch (view.kind) {
    case "list":
    case "unavailable": return null;
    case "sent": return "-sent";
    case "catalog": return "-market";
    case "markets": return "-markets";
    case "item": return `-market/${view.market}/${withOwner(view.id, owner)}`;
    case "page": return `ext:${withOwner(view.id, owner)}/ui/${view.ui}`;
    case "data-flow": return withOwner("-sources", owner);
    case "source": return `${withOwner("-sources", owner)}/${encodeURIComponent(view.state)}`;
    case "extension": return `ext:${withOwner(view.id, owner)}`;
    case "builtin":
    case "channel":
    case "connector": return `${view.kind}:${withOwner(view.id, owner)}`;
  }
}

/** The tab and item the previous Settings implementation understands. */
export function legacyIntegrationTarget({ scope, view, filter }: IntegrationsRoute): { tab: LegacyIntegrationTab; sub: string | null } {
  const owner = integrationManager(scope);
  switch (view.kind) {
    case "list":
    case "unavailable": return { tab: filter === "channels" ? "channels" : filter === "data" ? "connectors" : "extensions", sub: null };
    case "sent": return { tab: "connectors", sub: "-sent" };
    case "data-flow": return { tab: "sources", sub: null };
    case "source": return { tab: "sources", sub: view.state };
    case "catalog":
    case "markets":
    case "item": return { tab: "extensions", sub: unifiedSub(view, owner) };
    case "page": return { tab: "extensions", sub: `${withOwner(view.id, owner)}/ui/${view.ui}` };
    case "extension": return { tab: "extensions", sub: withOwner(view.id, owner) };
    case "channel": return { tab: "channels", sub: withOwner(view.id, owner) };
    case "connector": return { tab: "connectors", sub: withOwner(view.id, owner) };
    case "builtin": return { tab: BUILTIN_CHANNEL_IDS.has(view.id) ? "channels" : "connectors", sub: withOwner(view.id, owner) };
  }
}

export function integrationQuery(route: IntegrationsRoute, unified: boolean): URLSearchParams {
  const { scope, view, filter } = route;
  const owner = integrationManager(scope);
  const { tab, sub }: { tab: IntegrationTab; sub: string | null } = unified
    ? { tab: "integrations", sub: unifiedSub(view, owner) } : legacyIntegrationTarget(route);
  const query = new URLSearchParams({ tab });
  if (owner) query.set("manager", owner);
  if (sub) query.set(SUB[tab]!, sub);
  if (!unified && isLegacyIntegrationTab(tab) && legacyView(tab, sub ?? "").view.kind !== view.kind) {
    const hint = unifiedSub(view);
    if (hint) query.set("integration", hint);
  }
  if (filter !== defaultFilter(tab, view)) query.set("filter", filter);
  if (view.prefill !== undefined) query.set("set", view.prefill);
  return query;
}

/** Shareable objects also carry the owner syntax understood by a Web client from before the `manager` parameter. */
export function integrationHref(route: IntegrationsRoute, unified = true): string {
  return `/settings?${integrationQuery(route, unified)}`;
}

export function isIntegrationDetailRoute(view: IntegrationView): boolean {
  return view.kind === "builtin" || view.kind === "extension" || view.kind === "channel" ||
    view.kind === "connector" || view.kind === "source";
}
