// The route table every client follows (docs/protocol/deep-links.md, #9926): which links are pages of Persona's, and
// which page. On the web a link to this hub is read by its router (lib/links.ts hubPage); this module reads the app links
// (persona://open<path>, the desktop app's persona://open?route=, and the older persona://session|task|kb|settings|
// extension forms) into the path of the page they name, when one is opened (off the first screen). The iPhone
// (apple/Shared/HubPageLink.swift) and Android read the same table; spec/fixtures/deep-links/cases.json is what every
// client checks (lib/deep-links.test.ts for both kinds of link here). Only the keys a page lists are read: a link only
// opens a page, it never signs in or acts.
import { decodeURIComponentSafe } from "./uri";
import { machineLocation, machinePath, type MachineLocation } from "./machine-route";
import { APP_SCHEMES, assistantPath } from "./compatibility";


export type DeepPage =
  | { page: "persona" }
  | { page: "persona-new" }
  | { page: "conversation"; id: string; machine?: string; manager?: string; at?: string }
  | { page: "session"; machine: string; session: string; at?: string; view?: "chat" | "term" }
  | { page: "agents" }
  | { page: "inbox" }
  | { page: "inbox-item"; id: string; manager?: string }
  | { page: "todos"; view?: "calendar"; mode?: string; date?: string }
  | { page: "todo"; id: string }
  | { page: "knowledge"; kind?: string }
  | { page: "knowledge-entry"; kind: string; id: string }
  | { page: "machines" }
  | ({ page: "machine"; id: string } & MachineLocation)
  | { page: "settings"; tab?: string; item?: string; manager?: string; otherManager?: string; filter?: string; prefill?: string; integration?: string }
  | { page: "extension"; extension: string; ui: string; state?: string };

/** The knowledge base's kinds (lib/kb.ts KB_KINDS; a test keeps the two the same). */
export const DEEP_KB_KINDS = ["projects", "topics", "chats", "people", "docs", "meetings", "events", "recordings"];

/** Settings tabs and the query key that names an item of the ones that have one (views/settings/nav.ts; a test keeps
 *  the two the same). */
export const DEEP_SETTINGS_TABS: Record<string, string | null> = {
  general: null, persona: null, prompts: null, notifications: null, models: "purpose", agents: null, channels: "channel",
  connectors: "connector", sources: "state", extensions: "extension", integrations: "integration", shortcuts: null, advanced: null, devices: null, hub: null,
  managers: null, knowledge: null, avatar: null, labs: null,
};

export type LegacyIntegrationTab = "channels" | "connectors" | "extensions" | "sources";

export function isLegacyIntegrationTab(tab: string | null | undefined): tab is LegacyIntegrationTab {
  return tab === "channels" || tab === "connectors" || tab === "extensions" || tab === "sources";
}

export function splitSettingsOwner(raw: string): { id: string; manager?: string } {
  const at = raw.indexOf("@");
  if (at <= 0) return { id: raw };
  const manager = raw.slice(at + 1);
  return manager ? { id: raw.slice(0, at), manager } : { id: raw.slice(0, at) };
}

/**
 * Parses path-based Settings URLs such as `/settings/connectors/github@work`,
 * `/settings/channels/feishu`, `/settings/extensions/larkx@work/ui/board`,
 * `/settings/sources`, `/settings/sources/<state>`, `/settings/sources@work/<state>`,
 * `/settings/plugins/larkx`, `/settings/integrations/builtin:mail`, or `/settings/sent`.
 */
export function parseSettingsPath(path: string): { tab: string; sub: string | null; manager?: string } | null {
  const trimmed = path.replace(/\/+$/, "");
  if (!trimmed.startsWith("/settings/")) return null;
  const rest = trimmed.slice("/settings/".length);
  if (!rest) return null;
  const slash = rest.indexOf("/");
  const head = decodeURIComponentSafe(slash >= 0 ? rest.slice(0, slash) : rest);
  const { id: tab, manager } = splitSettingsOwner(head);
  if (head === "sent") return { tab: "sent", sub: null };
  if (head !== "plugins" && tab !== "integrations" && !isLegacyIntegrationTab(tab)) return null;

  const parts = slash >= 0 ? rest.slice(slash + 1).split("/") : [];
  const first = decodeURIComponentSafe(parts[0] ?? "");
  // The integration sub-codec owns the escaping inside a sources route. Other settings
  // items (including legacy sources state) use decoded path components.
  let sub = tab === "integrations" && splitSettingsOwner(first).id === "-sources"
    ? [first, ...parts.slice(1)].join("/")
    : parts.map(decodeURIComponentSafe).join("/");

  if (manager && sub && tab !== "sources") {
    // Detail views read ownership from the item. Keep list ownership in the manager
    // parameter and let an explicitly owned item override its tab's default owner.
    const segments = sub.split("/");
    const ownerIndex = segments[0] === "-market" ? 2 : segments[0] === "-channels" ? 1 : 0;
    const item = segments[ownerIndex];
    if (item && (!item.startsWith("-") || item === "-sources") && !splitSettingsOwner(item).manager) {
      segments[ownerIndex] = `${item}@${manager}`;
      sub = segments.join("/");
    }
  }
  return { tab: head === "plugins" ? "extensions" : tab, sub: sub || null, ...(manager ? { manager } : {}) };
}

/** Sent records: an item of the connectors tab (views/settings/nav.ts SENT_SUB). */
const SENT = "-sent";

/** The page without its undefined values, so two pages compare by what they say. */
function clean(o: DeepPage): DeepPage {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== "")) as DeepPage;
}

function segments(path: string): string[] | null {
  try {
    return path.split("/").filter(Boolean).map(decodeURIComponent);
  } catch {
    return null; // a malformed escape: no page
  }
}

function settingsPage(q: URLSearchParams, route?: ReturnType<typeof parseSettingsPath>): DeepPage {
  const rawTab = route?.tab ?? q.get("tab") ?? "";
  const tab = rawTab === "plugins" ? "extensions" : rawTab === "sent" ? "connectors" : rawTab;
  const managers = [...new Set([...q.getAll("manager"), route?.manager].filter(Boolean))];
  const [manager, otherManager] = managers;
  if (!(tab in DEEP_SETTINGS_TABS)) return clean({ page: "settings", manager });
  const key = DEEP_SETTINGS_TABS[tab];
  const item = rawTab === "sent" ? SENT : route?.sub ?? (key ? q.get(key) ?? (tab === "extensions" ? q.get("plugin") : null) : null);
  if (tab === "integrations" || isLegacyIntegrationTab(tab)) {
    const set = q.getAll("set");
    const prefill = set.length === 1 ? set[0] : undefined;
    const integration = tab === "integrations" ? undefined : q.get("integration") ?? undefined;
    return clean({ page: "settings", tab, item: item ?? undefined, manager, otherManager, filter: q.get("filter") ?? undefined, prefill, integration });
  }
  return clean({ page: "settings", tab, item: item ?? undefined, manager });
}

/** The page a path of the hub's web app is (without the sub-path), with its query; null for a path the table does not
 *  have (the web's other pages, /admin, /share/…). */
export function pageOfPath(path: string, search = ""): DeepPage | null {
  const p = segments(path);
  if (!p) return null;
  const q = new URLSearchParams(search);
  const v = (k: string) => q.get(k) || undefined;
  const view = v("view");
  const sessionView = view === "chat" || view === "term" ? view : undefined;
  switch (p[0]) {
    case undefined:
      return { page: "persona" };
    case "s":
      return p.length === 3 ? clean({ page: "session", machine: p[1]!, session: p[2]!, at: v("at"), view: sessionView }) : null;
    case "t":
      return p.length === 5 ? clean({ page: "session", machine: p[1]!, session: p[4]!, at: v("at"), view: sessionView }) : null;
    case "agents":
    case "a":
      return p.length === 1 ? { page: "agents" } : null;
    case "inbox":
      if (p.length === 1) return { page: "inbox" };
      return p.length === 2 ? clean({ page: "inbox-item", id: p[1]!, manager: v("manager") }) : null;
    case "todos": {
      if (p.length === 2) return { page: "todo", id: p[1]! };
      if (p.length !== 1) return null;
      const id = v("id");
      if (id) return { page: "todo", id };
      return clean({ page: "todos", view: view === "calendar" ? ("calendar" as const) : undefined, mode: v("mode"), date: v("date") });
    }
    case "kb":
      if (p.length === 1) return { page: "knowledge" };
      if (!DEEP_KB_KINDS.includes(p[1]!)) return null;
      if (p.length === 2) return { page: "knowledge", kind: p[1]! };
      return p.length === 3 ? { page: "knowledge-entry", kind: p[1]!, id: p[2]! } : null;
    case "machines": {
      if (p.length === 1) return { page: "machines" };
      const location = machineLocation(p, search);
      return location ? { page: "machine", id: p[1]!, ...location } : null;
    }
    case "settings": {
      if (p.length === 2 && p[1] === "sent") return { page: "settings", tab: "connectors", item: SENT };
      if (p.length === 1) return settingsPage(q);
      const route = parseSettingsPath(path);
      if (!route || route.tab === "sent") return null;
      return settingsPage(q, route);
    }
    case "extension":
    case "plugin":
      return p.length === 3 ? clean({ page: "extension", extension: p[1]!, ui: p[2]!, state: v("state") }) : null;
    default:
      if (!assistantPath(p[0]!)) return null;
      if (p.length === 1) return { page: "persona" };
      if (p.length === 2 && p[1] === "new") return { page: "persona-new" };
      return p.length === 2 ? clean({ page: "conversation", id: p[1]!, machine: v("machine"), manager: v("manager"), at: v("at") }) : null;
  }
}

/** A route as the desktop app's first form carries it (`route=`): one leading "/", never "//", no backslash or control
 *  character, staying on the hub. */
function routePage(route: string): DeepPage | null {
  // eslint-disable-next-line no-control-regex
  if (!route.startsWith("/") || route.startsWith("//") || route.includes("\\") || /[\u0000-\u001f\u007f]/.test(route)) return null;
  let r: URL;
  try {
    r = new URL(route, "https://route.invalid/");
  } catch {
    return null;
  }
  return r.origin === "https://route.invalid" ? pageOfPath(r.pathname, r.search) : null;
}

/** The page an app link opens, with the hub its `hub=` names (none: the hub this page is on); null for any other link,
 *  and for a malformed app link (a port, a user name, a route that would leave the hub). */
export function appPage(u: URL): { page: DeepPage; hub?: string } | null {
  if (!APP_SCHEMES.has(u.protocol.slice(0, -1).toLowerCase()) || u.username || u.password || u.port) return null;
  const hub = u.searchParams.get("hub") || undefined;
  const parts = segments(u.pathname);
  if (!parts) return null;
  const at = u.searchParams.get("at") || undefined;
  let page: DeepPage | null = null;
  switch (u.host.toLowerCase()) {
    case "open": {
      if (u.pathname.startsWith("//")) return null; // persona://open//<host>: not a path of the hub
      const route = u.searchParams.get("route");
      const machine = u.searchParams.get("machine");
      const session = u.searchParams.get("session");
      if (parts.length) page = pageOfPath(u.pathname, u.search);
      else if (route) page = routePage(route);
      else if (machine && session) page = { page: "session", machine, session }; // the iPhone's Live Activity rows
      break;
    }
    case "session":
      if (parts.length === 2) page = clean({ page: "session", machine: parts[0]!, session: parts[1]!, at });
      break;
    case "task":
      if (parts.length === 1) page = { page: "todo", id: parts[0]! };
      break;
    case "kb":
      if (parts.length >= 2 && DEEP_KB_KINDS.includes(parts[0]!)) page = { page: "knowledge-entry", kind: parts[0]!, id: parts.slice(1).join("/") };
      break;
    case "settings":
      if (parts.length === 1 && parts[0] === "sent") page = { page: "settings", tab: "connectors", item: SENT };
      else if (parts.length === 2 && parts[0] === "extensions") page = { page: "settings", tab: "extensions", item: parts[1]! };
      break;
    case "extension":
      if (parts.length === 2) page = clean({ page: "extension", extension: parts[0]!, ui: parts[1]!, state: u.searchParams.get("state") || undefined });
      break;
  }
  return page ? (hub ? { page, hub } : { page }) : null;
}

/** The page an app link (a URL string) opens, or null: what a message keeps of a persona:// link. */
export function appLinkPage(url: string): DeepPage | null {
  try {
    return appPage(new URL(url.trim()))?.page ?? null;
  } catch {
    return null;
  }
}

const enc = encodeURIComponent;

function withQuery(path: string, pairs: [string, string | undefined][]): string {
  const q = pairs.filter(([, v]) => v).map(([k, v]) => `${k}=${enc(v!)}`).join("&");
  return q ? `${path}?${q}` : path;
}

/** The page's path in this web app (without the sub-path), as the router reads it: what a link to it navigates to. */
export function pagePath(p: DeepPage): string {
  switch (p.page) {
    case "persona":
      return "/persona";
    case "persona-new":
      return "/persona/new";
    case "conversation":
      return withQuery(`/persona/${enc(p.id)}`, [["machine", p.machine], ["manager", p.manager], ["at", p.at]]);
    case "session":
      return withQuery(`/s/${enc(p.machine)}/${enc(p.session)}`, [["view", p.view], ["at", p.at]]);
    case "agents":
      return "/agents";
    case "inbox":
      return "/inbox";
    case "inbox-item":
      return withQuery(`/inbox/${enc(p.id)}`, [["manager", p.manager]]);
    case "todos":
      return withQuery("/todos", [["view", p.view], ["mode", p.mode], ["date", p.date]]);
    case "todo":
      return `/todos/${enc(p.id)}`;
    case "knowledge":
      return p.kind ? `/kb/${enc(p.kind)}` : "/kb";
    case "knowledge-entry":
      return `/kb/${enc(p.kind)}/${enc(p.id)}`;
    case "machines":
      return "/machines";
    case "machine":
      return machinePath(p.id, p);
    case "settings": {
      const key = p.tab ? DEEP_SETTINGS_TABS[p.tab] : null;
      return withQuery("/settings", [["tab", p.tab], [key ?? "", key ? p.item : undefined],
        ["manager", p.manager], ["manager", p.otherManager], ["filter", p.filter], ["set", p.prefill], ["integration", p.integration]]);
    }
    case "extension":
      return withQuery(`/extension/${enc(p.extension)}/${enc(p.ui)}`, [["state", p.state]]);
  }
}

/** The path in this web app an app link opens (lib/links.ts opens it as a link to this hub); null when it names no page. */
export function appPath(url: string): string | null {
  const p = appLinkPage(url);
  return p ? pagePath(p) : null;
}
